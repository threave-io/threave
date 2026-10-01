import { act, createEvent, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import type { AgentEvent } from '@/lib/api'
import { ChatTranscript as ChatTranscriptComponent } from '@/components/chat-transcript'
import type { ChatTranscriptMessage } from '@/lib/events'
import { ClientDebugContext } from '@/lib/client-debug'

function ChatTranscript(props: ComponentProps<typeof ChatTranscriptComponent>) {
  return <ChatTranscriptComponent autoScroll {...props} />
}

test('debug toggle retains the transcript and populates idle scroll measurements immediately', () => {
  const events = [event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Idle message' })]
  const { container, rerender } = render(<ClientDebugContext.Provider value={false}><ChatTranscript events={events} /></ClientDebugContext.Provider>)
  const scroller = screen.getByRole('log')
  expect(container.querySelector('[data-debug-scroll-readout]')).not.toBeInTheDocument()
  rerender(<ClientDebugContext.Provider value={true}><ChatTranscript events={events} /></ClientDebugContext.Provider>)
  expect(screen.getByRole('log')).toBe(scroller)
  const readout = container.querySelector('[data-debug-scroll-readout]')
  expect(readout).toHaveTextContent('inset 0px · tail 6px · dist 0px')
  expect(readout).toHaveTextContent('pinned · idle')
  expect(readout).not.toHaveTextContent('waiting for scroll')
  expect(readout).not.toBeVisible()
  rerender(<ClientDebugContext.Provider value={false}><ChatTranscript events={events} /></ClientDebugContext.Provider>)
  expect(container.querySelector('[data-debug-scroll-readout]')).not.toBeInTheDocument()
  expect(screen.getByRole('log')).toBe(scroller)
})

test('renders user and assistant messages without duplicating completion text', () => {
  const { container } = render(
    <ChatTranscript
      events={[
        event(1, 'user.message.completed', 'user', 'completed', { text: 'Hello' }),
        event(2, 'agent.message.delta', 'assistant', 'delta', { text: 'Hi' }),
        event(3, 'agent.message.delta', 'assistant', 'delta', { text: ' there' }),
        event(4, 'agent.message.completed', 'assistant', 'completed', { text: 'Hi there' }),
      ]}
    />,
  )

  expect(screen.getByText('Hello')).toBeInTheDocument()
  expect(screen.getByText('Hi there')).toBeInTheDocument()
  expect(screen.queryByText('Hi thereHi there')).not.toBeInTheDocument()
  expect(container.querySelectorAll('time[datetime="2026-06-12T16:00:00Z"]')).toHaveLength(2)
  const firstTimestamp = container.querySelector('time[datetime="2026-06-12T16:00:00Z"]')
  expect(firstTimestamp).toBeVisible()
  expect(firstTimestamp?.parentElement?.previousElementSibling).toContainElement(screen.getByText('Hello'))
  const timestampPosition = firstTimestamp?.compareDocumentPosition(screen.getByText('Hello')) ?? 0
  expect(timestampPosition & Node.DOCUMENT_POSITION_PRECEDING).toBe(
    Node.DOCUMENT_POSITION_PRECEDING,
  )
  expect(screen.getAllByRole('button', { name: 'Copy message' })).toHaveLength(2)
})

test('renders connection errors after the message list as a centered status', () => {
  render(
    <ChatTranscript
      error="HTTP 502"
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Last response' })]}
    />,
  )

  const message = screen.getByText('Last response')
  const alert = screen.getByRole('alert')
  const position = message.compareDocumentPosition(alert)

  expect(position & Node.DOCUMENT_POSITION_FOLLOWING).toBe(Node.DOCUMENT_POSITION_FOLLOWING)
  expect(alert).toHaveTextContent('Chat issue')
  expect(alert).toHaveTextContent('HTTP 502')
  expect(alert).toHaveClass('mx-auto', 'justify-center', 'text-center')
})

test('dates every earlier-day message, including the first loaded message, but not today', () => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-06-13T12:00:00'))
  try {
    const firstTimestamp = '2026-06-12T23:50:00'
    const sameDayTimestamp = '2026-06-12T23:55:00'
    const nextDayTimestamp = '2026-06-13T00:05:00'
    const { container } = render(
      <ChatTranscript
        events={[
          { ...event(1, 'user.message.completed', 'user', 'completed', { text: 'Late prompt' }), created_at: firstTimestamp },
          { ...event(2, 'agent.message.completed', 'assistant', 'completed', { text: 'Late answer' }), created_at: sameDayTimestamp },
          { ...event(3, 'user.message.completed', 'user', 'completed', { text: 'Next prompt' }), created_at: nextDayTimestamp },
        ]}
      />,
    )

    const firstTime = container.querySelector(`time[datetime="${firstTimestamp}"]`)
    const sameDayTime = container.querySelector(`time[datetime="${sameDayTimestamp}"]`)
    const nextDayTime = container.querySelector(`time[datetime="${nextDayTimestamp}"]`)

    const dated = new Intl.DateTimeFormat(undefined, { dateStyle: 'short', timeStyle: 'short' })
    const timeOnly = new Intl.DateTimeFormat(undefined, { timeStyle: 'short' })
    expect(firstTime?.textContent).toBe(dated.format(new Date(firstTimestamp)))
    expect(sameDayTime?.textContent).toBe(dated.format(new Date(sameDayTimestamp)))
    expect(nextDayTime?.textContent).toBe(timeOnly.format(new Date(nextDayTimestamp)))
  } finally {
    vi.useRealTimers()
  }
})

test('dates messages across a local year boundary using the completed response time', () => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(2027, 0, 1, 12))
  try {
    const yesterday = new Date(2026, 11, 31, 23, 55).toISOString()
    const today = new Date(2027, 0, 1, 0, 5).toISOString()
    const { container } = render(
      <ChatTranscript events={[
        { ...event(1, 'user.message.completed', 'user', 'completed', { text: 'Late request' }), created_at: yesterday },
        { ...event(2, 'agent.message.delta', 'assistant', 'delta', { text: 'Answer', item_id: 'reply' }), created_at: yesterday },
        { ...event(3, 'agent.message.completed', 'assistant', 'completed', { text: 'Answer', item_id: 'reply' }), created_at: today },
      ]} />,
    )
    expect(container.querySelector(`time[datetime="${yesterday}"]`)?.textContent).toBe(
      new Intl.DateTimeFormat(undefined, { dateStyle: 'short', timeStyle: 'short' }).format(new Date(yesterday)),
    )
    expect(container.querySelector(`time[datetime="${today}"]`)?.textContent).toBe(
      new Intl.DateTimeFormat(undefined, { timeStyle: 'short' }).format(new Date(today)),
    )
  } finally {
    vi.useRealTimers()
  }
})

test('shows the completed response time and total turn duration', () => {
  const completedAt = '2026-06-12T16:04:00Z'
  const { container } = render(
    <ChatTranscript
      events={[
        { ...event(1, 'user.message.completed', 'user', 'completed', { text: 'Do the work' }), created_at: '2026-06-12T16:03:25Z' },
        { ...event(2, 'agent.run.started', 'assistant', 'started', {}), created_at: '2026-06-12T16:03:26Z' },
        { ...event(3, 'agent.message.completed', 'assistant', 'completed', { item_id: 'msg_1', text: 'Done' }), created_at: completedAt },
      ]}
    />,
  )

  expect(container.querySelector(`time[datetime="${completedAt}"]`)).toHaveTextContent(
    new Intl.DateTimeFormat(undefined, { timeStyle: 'short' }).format(new Date(completedAt)),
  )
  expect(screen.getByLabelText('Total turn time 34 sec')).toHaveTextContent('34 sec')
})

test('renders session actions as conversation breaks', () => {
  render(
    <ChatTranscript
      events={[
        event(1, 'user.message.completed', 'user', 'completed', { text: 'Hello' }),
        event(2, 'session.action.completed', 'system', 'completed', {
          action: 'clear',
          text: 'Clear context',
        }),
        event(3, 'agent.message.completed', 'assistant', 'completed', { text: 'Done' }),
      ]}
    />,
  )

  expect(screen.getByRole('separator', { name: 'CONVERSATION CLEARED' })).toBeInTheDocument()
  expect(screen.getByText('CONVERSATION CLEARED')).toBeInTheDocument()
  expect(screen.queryByText('Clear context')).not.toBeInTheDocument()
})

test('renders workspace changes with their old and new paths', () => {
  render(
    <ChatTranscript
      events={[
        event(1, 'session.action.completed', 'system', 'completed', {
          action: 'workspace_changed',
          label: 'WORKSPACE CHANGED',
          previous_workspace_path: '/repo/old',
          workspace_path: '/repo/new',
        }),
      ]}
    />,
  )

  expect(screen.getByRole('separator', { name: 'WORKSPACE CHANGED' })).toBeInTheDocument()
  expect(screen.getByText('/repo/old -> /repo/new')).toBeInTheDocument()
})

test('renders run failures as system error rows instead of assistant text', () => {
  const errorText = 'read codex app-server stdout: bufio.Scanner: token too long'

  render(
    <ChatTranscript
      events={[
        event(1, 'user.message.completed', 'user', 'completed', { text: 'Keep working' }),
        event(2, 'agent.message.completed', 'assistant', 'completed', { text: 'I started the change.' }),
        event(3, 'agent.run.failed', 'assistant', 'failed', { error: errorText }),
      ]}
    />,
  )

  const alert = screen.getByRole('alert', { name: `Run failed: ${errorText}` })
  expect(alert).toHaveTextContent('Run failed')
  expect(alert).toHaveTextContent(errorText)
  expect(alert).toHaveTextContent('#3')
  expect(alert.querySelector('time')).toHaveClass('hidden')
  expect(alert.querySelector('time')).toHaveClass('sm:inline')

  const assistantMessage = screen.getByText('I started the change.').closest('article')
  expect(assistantMessage).toHaveTextContent('I started the change.')
  expect(assistantMessage).not.toHaveTextContent(errorText)
})

test('renders markdown in chat messages', () => {
  render(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', {
          item_id: 'msg_1',
          text: '**Section 1**\n\n- First item\n- Second item',
        }),
      ]}
    />,
  )

  expect(screen.getByText('Section 1').tagName).toBe('STRONG')
  expect(screen.getAllByRole('listitem')).toHaveLength(2)
  expect(screen.getByText('First item')).toBeInTheDocument()
})

test('preserves selected response text while unrelated stream content updates', () => {
  const completed = event(1, 'agent.message.completed', 'assistant', 'completed', {
    item_id: 'msg_1',
    text: 'Copy this completed response',
  })
  const { rerender } = render(<ChatTranscript events={[completed]} />)
  const response = screen.getByText('Copy this completed response')
  const text = response.firstChild
  expect(text).toBeInstanceOf(Text)
  if (!(text instanceof Text)) return

  const range = document.createRange()
  range.setStart(text, 5)
  range.setEnd(text, 9)
  const selection = window.getSelection()
  selection?.removeAllRanges()
  selection?.addRange(range)
  expect(selection?.toString()).toBe('this')

  rerender(
    <ChatTranscript
      events={[
        completed,
        event(2, 'user.message.completed', 'user', 'completed', { text: 'Next request' }),
        event(3, 'agent.message.delta', 'assistant', 'delta', { item_id: 'msg_2', text: 'Streaming' }),
      ]}
    />,
  )

  expect(window.getSelection()?.toString()).toBe('this')
})

test('preserves selected response text while that response continues streaming', () => {
  const firstDelta = event(1, 'agent.message.delta', 'assistant', 'delta', {
    item_id: 'msg_1',
    text: 'Copy this response',
  })
  const { rerender } = render(<ChatTranscript events={[firstDelta]} />)
  const response = screen.getByText('Copy this response')
  const text = response.firstChild
  expect(text).toBeInstanceOf(Text)
  if (!(text instanceof Text)) return

  const range = document.createRange()
  range.setStart(text, 5)
  range.setEnd(text, 9)
  const selection = window.getSelection()
  selection?.removeAllRanges()
  selection?.addRange(range)

  rerender(
    <ChatTranscript
      events={[
        firstDelta,
        event(2, 'agent.message.delta', 'assistant', 'delta', { item_id: 'msg_1', text: ' keeps growing' }),
      ]}
    />,
  )

  expect(window.getSelection()?.toString()).toBe('this')
  expect(screen.queryByText('Copy this response keeps growing')).not.toBeInTheDocument()

  selection?.removeAllRanges()
  fireEvent(document, new Event('selectionchange'))

  expect(screen.getByText('Copy this response keeps growing')).toBeInTheDocument()
})

test('opens markdown file links in the file editor action', async () => {
  const user = userEvent.setup()
  const onOpenFilePath = vi.fn()

  render(
    <ChatTranscript
      onOpenFilePath={onOpenFilePath}
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', {
          text: 'Changed [chat-transcript.tsx](/Users/joey/Source/gorchestra/web/src/components/chat-transcript.tsx:54).',
        }),
      ]}
    />,
  )

  await user.click(screen.getByRole('link', { name: 'chat-transcript.tsx' }))

  expect(onOpenFilePath).toHaveBeenCalledWith('/Users/joey/Source/gorchestra/web/src/components/chat-transcript.tsx')
})

test('renders legacy raw Codex plan messages with a plan label', () => {
  const planText = 'Review `README.md` before running:\n\n```sh\nbun test\n```\n'
  const { container } = render(
    <ChatTranscript
      events={[
        event(1, 'provider.codex.event', 'system', 'completed', {
          provider_event_type: 'item/plan/delta',
          raw: { threadId: 'thread_1', turnId: 'turn_1', itemId: 'plan_1', delta: planText },
        }),
        event(2, 'provider.codex.event', 'system', 'completed', {
          provider_event_type: 'item/completed',
          raw: {
            threadId: 'thread_1',
            turnId: 'turn_1',
            item: { type: 'plan', id: 'plan_1', text: planText },
          },
        }),
      ]}
    />,
  )

  expect(screen.getByText('Plan')).toBeInTheDocument()
  expect(screen.getByText('README.md')).toHaveClass('bg-amber-100/85')
  expect(screen.getByText(/bun test/)).toHaveClass('bg-amber-100/80')
  expect(screen.queryByText('item/plan/delta')).not.toBeInTheDocument()
  expect(screen.getByText('Plan').closest('article')).toHaveAttribute('data-message-variant', 'plan')
  expect(container.querySelector('.border-l-amber-400')).toBeInTheDocument()
})

test('does not render pagination controls in either direction', () => {
  render(
    <ChatTranscript
      hasOlderEvents
      hasNewerEvents
      onLoadOlderEvents={() => undefined}
      onLoadNewerEvents={() => undefined}
      events={[event(251, 'agent.message.completed', 'assistant', 'completed', { text: 'Tail' })]}
    />,
  )

  expect(screen.queryByRole('button', { name: 'Load older events' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Load newer events' })).not.toBeInTheDocument()
})

test('uses fixed transcript bottom breathing room', () => {
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Tail' })]}
    />,
  )

  expect(screen.getByRole('log', { name: 'Chat messages' })).toHaveAttribute('data-tail-clearance-height', '6')
})

test('adds breathing room after the measured bottom inset', () => {
  render(
    <ChatTranscript
      bottomInsetHeight={260}
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Tail' })]}
    />,
  )

  expect(screen.getByRole('log', { name: 'Chat messages' })).toHaveAttribute('data-tail-clearance-height', '266')
})

test('keeps bottom breathing room in a stable tail item after live activity', () => {
  render(
    <ChatTranscript
      activityStatus={{ kind: 'working', since: '2026-06-12T16:00:00Z' }}
      bottomInsetHeight={260}
      events={[event(1, 'user.message.completed', 'user', 'completed', { text: 'Do the work' })]}
    />,
  )

  const activity = screen.getByRole('status', { name: /Working for/ })
  const activityRow = activity.closest('[data-index]')
  const tailSpacer = screen.getByTestId('chat-tail-breathing-room')

  expect(activity.parentElement?.parentElement).toHaveClass('py-1.5')
  expect(tailSpacer).toHaveStyle({ height: '6px' })
  expect(activityRow?.nextElementSibling).toBe(tailSpacer.closest('[data-index]'))
})

test('positions and clears the hatch glow from mouse movement', () => {
  const { container } = render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Tail' })]}
    />,
  )
  const canvas = container.querySelector<HTMLDivElement>('.chat-canvas')
  expect(canvas).not.toBeNull()
  if (!canvas) return

  vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue({
    x: 100,
    y: 50,
    left: 100,
    top: 50,
    right: 500,
    bottom: 450,
    width: 400,
    height: 400,
    toJSON: () => ({}),
  })

  fireEvent.pointerMove(canvas, { clientX: 136, clientY: 92, pointerType: 'mouse' })

  expect(canvas).toHaveStyle({ '--chat-glow-x': '36px', '--chat-glow-y': '42px' })
  expect(canvas).toHaveAttribute('data-glow-active', 'true')

  fireEvent.pointerLeave(canvas, { pointerType: 'mouse' })

  expect(canvas).not.toHaveAttribute('data-glow-active')
})

test('does not activate the hatch glow for touch input', () => {
  const { container } = render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Tail' })]}
    />,
  )
  const canvas = container.querySelector<HTMLDivElement>('.chat-canvas')
  expect(canvas).not.toBeNull()
  if (!canvas) return

  fireEvent.pointerMove(canvas, { clientX: 20, clientY: 20, pointerType: 'touch' })

  expect(canvas).not.toHaveAttribute('data-glow-active')
})

test('renders thinking activity status where the transcript tail indicator appears', () => {
  render(<ChatTranscript activityStatus={{ kind: 'thinking' }} events={[]} />)

  expect(screen.getByRole('status', { name: 'Thinking' })).toBeInTheDocument()
  expect(screen.getByRole('log', { name: 'Chat messages' })).toContainElement(
    screen.getByRole('status', { name: 'Thinking' }),
  )
})

test('renders working activity status with a quiet-time counter', () => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-06-12T16:00:05Z'))

  try {
    render(<ChatTranscript activityStatus={{ kind: 'working', since: '2026-06-12T16:00:00Z' }} events={[]} />)

    expect(screen.getByRole('status', { name: 'Working for 5 seconds' })).toBeInTheDocument()

    act(() => {
      vi.setSystemTime(new Date('2026-06-12T16:00:07Z'))
      vi.advanceTimersByTime(1000)
    })

    expect(screen.getByRole('status', { name: 'Working for 8 seconds' })).toBeInTheDocument()
  } finally {
    vi.useRealTimers()
  }
})

async function settleVirtualScroll() {
  await act(async () => {
    await new Promise<void>((resolve) => window.setTimeout(resolve, 5))
  })
}

test('scrolling backward pauses following and shows jump to latest', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 900, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log)

  expect(onFollowingTailChange).toHaveBeenLastCalledWith(false)
  expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()
})

test('a programmatic size correction restores the physical tail without pausing following', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  log.dataset.mockVirtualDistanceFromEnd = '0'
  setScrollMetrics(log, { scrollTop: 900, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)

  expect(log.scrollTop).toBe(2000)
  expect(onFollowingTailChange).not.toHaveBeenCalledWith(false)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
})

test('scrolling backward near the leading edge automatically loads older history once', async () => {
  const onLoadOlderEvents = vi.fn()
  render(
    <ChatTranscript
      hasOlderEvents
      onLoadOlderEvents={onLoadOlderEvents}
      events={[event(251, 'agent.message.completed', 'assistant', 'completed', { text: 'Tail' })]}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 600, scrollHeight: 1000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 0, scrollHeight: 1000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log)
  fireEvent.scroll(log)

  expect(onLoadOlderEvents).toHaveBeenCalledOnce()
  expect(screen.queryByRole('button', { name: 'Load older events' })).not.toBeInTheDocument()
})

test('does not request older history while an older request is already active', async () => {
  const onLoadOlderEvents = vi.fn()
  render(
    <ChatTranscript
      hasOlderEvents
      loadingOlderEvents
      onLoadOlderEvents={onLoadOlderEvents}
      events={[event(251, 'agent.message.completed', 'assistant', 'completed', { text: 'Tail' })]}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 600, scrollHeight: 1000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 0, scrollHeight: 1000, clientHeight: 400 })
  fireEvent.scroll(log)

  expect(onLoadOlderEvents).not.toHaveBeenCalled()
})

test('automatically loads older history when the transcript cannot fill its viewport', async () => {
  const onLoadOlderEvents = vi.fn()
  const tail = event(251, 'agent.message.completed', 'assistant', 'completed', { text: 'Tail' })
  const { rerender } = render(
    <ChatTranscript
      onLoadOlderEvents={onLoadOlderEvents}
      events={[tail]}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 0, scrollHeight: 320, clientHeight: 500 })

  rerender(
    <ChatTranscript
      hasOlderEvents
      onLoadOlderEvents={onLoadOlderEvents}
      events={[tail]}
    />,
  )
  await act(async () => new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve())))

  expect(onLoadOlderEvents).toHaveBeenCalledOnce()
})

test('does not automatically load older history when the transcript is scrollable', async () => {
  const onLoadOlderEvents = vi.fn()
  const tail = event(251, 'agent.message.completed', 'assistant', 'completed', { text: 'Tail' })
  const { rerender } = render(
    <ChatTranscript
      onLoadOlderEvents={onLoadOlderEvents}
      events={[tail]}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 0, scrollHeight: 1000, clientHeight: 500 })

  rerender(
    <ChatTranscript
      hasOlderEvents
      onLoadOlderEvents={onLoadOlderEvents}
      events={[tail]}
    />,
  )
  await act(async () => new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve())))

  expect(onLoadOlderEvents).not.toHaveBeenCalled()
})

test('stable timeline keys preserve the visible row when older history is prepended', async () => {
  const { rerender } = render(
    <ChatTranscript
      hasOlderEvents
      events={[event(251, 'agent.message.completed', 'assistant', 'completed', { text: 'Tail' })]}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })
  const tailRow = screen.getByText('Tail').closest('[data-index]')
  setScrollMetrics(log, { scrollTop: 600, scrollHeight: 1000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 300, scrollHeight: 1000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log)
  await settleVirtualScroll()

  rerender(
    <ChatTranscript
      events={[
        event(1, 'user.message.completed', 'user', 'completed', { text: 'Older' }),
        event(251, 'agent.message.completed', 'assistant', 'completed', { text: 'Tail' }),
      ]}
    />,
  )

  expect(screen.getByText('Tail').closest('[data-index]')).toBe(tailRow)
  expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()
})

test('scrolling toward newer history loads another page outside the snap zone', () => {
  const onLoadNewerEvents = vi.fn()
  render(
    <ChatTranscript
      hasNewerEvents
      onLoadNewerEvents={onLoadNewerEvents}
      events={[event(251, 'agent.message.completed', 'assistant', 'completed', { text: 'Older' })]}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: 100 })
  fireEvent.scroll(log)

  expect(onLoadNewerEvents).toHaveBeenCalledOnce()
  expect(screen.queryByRole('button', { name: 'Load newer events' })).not.toBeInTheDocument()
})

test('settled forward scrolling within 16px adopts the live tail', async () => {
  const onJumpToLatest = vi.fn()
  const { rerender } = render(
    <ChatTranscript
      hasNewerEvents
      bottomInsetHeight={280}
      onJumpToLatest={onJumpToLatest}
      events={[event(251, 'agent.message.completed', 'assistant', 'completed', { text: 'Older' })]}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 990, scrollHeight: 1400, clientHeight: 400 })

  fireEvent.wheel(log, { deltaY: 100 })
  fireEvent.scroll(log)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
  await settleVirtualScroll()
  expect(onJumpToLatest).toHaveBeenCalledOnce()

  rerender(
    <ChatTranscript
      bottomInsetHeight={280}
      onJumpToLatest={onJumpToLatest}
      events={[event(252, 'agent.message.completed', 'assistant', 'completed', { text: 'Latest' })]}
    />,
  )
  expect(log.scrollTop).toBe(1400)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
})

test('reversing away from the 16px reattach zone before scroll settle cancels the pending snap', async () => {
  vi.useFakeTimers()
  try {
    const onJumpToLatest = vi.fn()
    render(
      <ChatTranscript
        hasNewerEvents
        bottomInsetHeight={280}
        onJumpToLatest={onJumpToLatest}
        events={[event(251, 'agent.message.completed', 'assistant', 'completed', { text: 'Older' })]}
      />,
    )
    const log = screen.getByRole('log', { name: 'Chat messages' })

    setScrollMetrics(log, { scrollTop: 990, scrollHeight: 1400, clientHeight: 400 })
    fireEvent.wheel(log, { deltaY: 100 })
    fireEvent.scroll(log)
    expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()

    setScrollMetrics(log, { scrollTop: 900, scrollHeight: 1400, clientHeight: 400 })
    fireEvent.wheel(log, { deltaY: -100 })
    fireEvent.scroll(log)
    expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()

    await act(async () => vi.runAllTimersAsync())
    expect(onJumpToLatest).not.toHaveBeenCalled()
  } finally {
    vi.useRealTimers()
  }
})

test('settled forward scrolling near the tail pulls to the physical bottom', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 900, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 1590, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: 100 })
  fireEvent.scroll(log)
  await settleVirtualScroll()

  expect(log.scrollTop).toBe(2000)
  expect(onFollowingTailChange).toHaveBeenLastCalledWith(true)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
})

test('an upward gesture within 48px snaps back without pausing following', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 1568, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log)

  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
  expect(onFollowingTailChange).not.toHaveBeenCalledWith(false)
  await settleVirtualScroll()
  expect(log.scrollTop).toBe(2000)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
})

test('a deliberate touch drag detaches from the tail inside the wheel snap zone', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  const pointerDown = createEvent.pointerDown(log, { clientY: 200 })
  Object.defineProperty(pointerDown, 'pointerType', { value: 'touch' })
  fireEvent(log, pointerDown)
  const pointerMove = createEvent.pointerMove(log, { clientY: 204 })
  Object.defineProperty(pointerMove, 'pointerType', { value: 'touch' })
  fireEvent(log, pointerMove)
  setScrollMetrics(log, { scrollTop: 1596, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)

  expect(onFollowingTailChange).toHaveBeenLastCalledWith(false)
  expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()
})

test('an upward gesture beyond 48px pauses following', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 1551, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log)

  expect(onFollowingTailChange).toHaveBeenLastCalledWith(false)
  expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()
})

test('one-pixel wheel jitter stays pinned without flashing jump to latest', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 1599, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: -1 })
  fireEvent.scroll(log)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: 1 })
  fireEvent.scroll(log)
  await settleVirtualScroll()

  expect(onFollowingTailChange).not.toHaveBeenCalledWith(false)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
})

test('segmented touch momentum can reattach after an intermediate virtualizer settle', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 1315, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.pointerDown(log, { pointerType: 'touch', clientY: 250 })
  fireEvent.pointerMove(log, { pointerType: 'touch', clientY: 550 })
  fireEvent.pointerCancel(log, { pointerType: 'touch' })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()

  fireEvent.pointerDown(log, { pointerType: 'touch', clientY: 650 })
  fireEvent.pointerMove(log, { pointerType: 'touch', clientY: 610 })
  fireEvent.pointerCancel(log, { pointerType: 'touch' })
  setScrollMetrics(log, { scrollTop: 1340, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 1500, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 1580, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  // The native scroll path remains authoritative even if WebKit does not
  // produce a corresponding synchronous virtualizer change.
  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()

  expect(onFollowingTailChange).toHaveBeenLastCalledWith(true)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
})

test.each(['scroll', 'scrollend'] as const)(
  'a canceled touch flick reattaches at the bottom through native %s without pointer moves',
  async (finalEvent) => {
    const onFollowingTailChange = vi.fn()
    render(
      <ChatTranscript
        events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
        onFollowingTailChange={onFollowingTailChange}
      />,
    )
    const log = screen.getByRole('log', { name: 'Chat messages' })

    setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
    fireEvent.scroll(log)
    await settleVirtualScroll()
    const pointerDown = createEvent.pointerDown(log, { clientY: 200 })
    Object.defineProperty(pointerDown, 'pointerType', { value: 'touch' })
    fireEvent(log, pointerDown)
    const pointerMove = createEvent.pointerMove(log, { clientY: 500 })
    Object.defineProperty(pointerMove, 'pointerType', { value: 'touch' })
    fireEvent(log, pointerMove)
    fireEvent.pointerCancel(log)
    setScrollMetrics(log, { scrollTop: 1300, scrollHeight: 2000, clientHeight: 400 })
    fireEvent.scroll(log)
    await settleVirtualScroll()
    fireEvent(log, new Event('scrollend'))
    expect(onFollowingTailChange).toHaveBeenLastCalledWith(false)
    expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()

    // WebKit can take over the next pan before delivering a pointermove. Its
    // momentum scrolls must clear the previous upward gesture's detached state.
    const flick = createEvent.pointerDown(log, { clientY: 600 })
    Object.defineProperty(flick, 'pointerType', { value: 'touch' })
    fireEvent(log, flick)
    fireEvent.pointerCancel(log)
    if (finalEvent === 'scroll') {
      for (const scrollTop of [1340, 1500, 1580]) {
        setScrollMetrics(log, { scrollTop, scrollHeight: 2000, clientHeight: 400 })
        fireEvent.scroll(log)
        await settleVirtualScroll()
      }
    }
    setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
    fireEvent(log, new Event(finalEvent))
    await settleVirtualScroll()

    expect(onFollowingTailChange).toHaveBeenLastCalledWith(true)
    expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
  },
)

test('an upward touch stays detached through cancellation and an elastic rebound near the tail', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  const pointerDown = createEvent.pointerDown(log, { clientY: 200 })
  Object.defineProperty(pointerDown, 'pointerType', { value: 'touch' })
  fireEvent(log, pointerDown)
  const pointerMove = createEvent.pointerMove(log, { clientY: 208 })
  Object.defineProperty(pointerMove, 'pointerType', { value: 'touch' })
  fireEvent(log, pointerMove)
  fireEvent.pointerCancel(log)
  for (const scrollTop of [1592, 1596]) {
    setScrollMetrics(log, { scrollTop, scrollHeight: 2000, clientHeight: 400 })
    fireEvent.scroll(log)
    await settleVirtualScroll()
  }
  fireEvent(log, new Event('scrollend'))

  expect(onFollowingTailChange).toHaveBeenLastCalledWith(false)
  expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()
})

test.each(['native scrollend', 'virtualizer idle'] as const)(
  'settled live bottom clears stale touch detachment through %s without a new forward gesture',
  async (settlePath) => {
    const onFollowingTailChange = vi.fn()
    const events = [event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]
    const view = render(<ChatTranscript events={events} onFollowingTailChange={onFollowingTailChange} />)
    const log = screen.getByRole('log', { name: 'Chat messages' })
    setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
    fireEvent.scroll(log)
    await settleVirtualScroll()

    const down = createEvent.pointerDown(log, { clientY: 200 })
    Object.defineProperty(down, 'pointerType', { value: 'touch' })
    fireEvent(log, down)
    const move = createEvent.pointerMove(log, { clientY: 500 })
    Object.defineProperty(move, 'pointerType', { value: 'touch' })
    fireEvent(log, move)
    fireEvent.pointerCancel(log)
    setScrollMetrics(log, { scrollTop: 1300, scrollHeight: 2000, clientHeight: 400 })
    fireEvent.scroll(log)
    await settleVirtualScroll()
    fireEvent(log, new Event('scrollend'))
    expect(onFollowingTailChange).toHaveBeenLastCalledWith(false)

    // Reproduce the observed state: the browser reaches the physical bottom,
    // but the prior touch detachment outlives the gesture's direction/baseline.
    setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
    if (settlePath === 'native scrollend') {
      fireEvent(log, new Event('scrollend'))
    } else {
      fireEvent.scroll(log)
    }
    await settleVirtualScroll()

    expect(onFollowingTailChange).toHaveBeenLastCalledWith(true)
    expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
    // Recover following, not just the chip's appearance.
    setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2200, clientHeight: 400 })
    view.rerender(<ChatTranscript events={[...events, event(2, 'agent.message.delta', 'assistant', 'delta', { text: 'More' })]} onFollowingTailChange={onFollowingTailChange} />)
    expect(log.scrollTop).toBe(2200)
  },
)

test('settling at the bottom does not reattach while the finger is still touching after pointer cancellation', async () => {
  const onFollowingTailChange = vi.fn()
  render(<ChatTranscript events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]} onFollowingTailChange={onFollowingTailChange} />)
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()

  fireEvent.touchStart(log, { touches: [{ identifier: 1, clientY: 200 }] })
  const down = createEvent.pointerDown(log, { clientY: 200 })
  Object.defineProperty(down, 'pointerType', { value: 'touch' })
  fireEvent(log, down)
  const move = createEvent.pointerMove(log, { clientY: 208 })
  Object.defineProperty(move, 'pointerType', { value: 'touch' })
  fireEvent(log, move)
  fireEvent.pointerCancel(log)
  fireEvent.scroll(log)
  fireEvent(log, new Event('scrollend'))
  await settleVirtualScroll()
  expect(onFollowingTailChange).toHaveBeenLastCalledWith(false)

  // With no further scrolling, the end of contact must still reconcile the tail.
  fireEvent.touchEnd(log, { touches: [] })
  expect(onFollowingTailChange).toHaveBeenLastCalledWith(true)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
})

test('a canceled upward touch flick also detaches without pointer moves', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  const pointerDown = createEvent.pointerDown(log, { clientY: 200 })
  Object.defineProperty(pointerDown, 'pointerType', { value: 'touch' })
  fireEvent(log, pointerDown)
  fireEvent.pointerCancel(log)
  setScrollMetrics(log, { scrollTop: 1592, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()

  expect(onFollowingTailChange).toHaveBeenLastCalledWith(false)
  expect(log.scrollTop).toBe(1592)
  expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()
})

test('a touch tap does not mistake a later layout scroll for an upward gesture', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  const pointerDown = createEvent.pointerDown(log, { clientY: 200 })
  Object.defineProperty(pointerDown, 'pointerType', { value: 'touch' })
  fireEvent(log, pointerDown)
  fireEvent.pointerUp(log)
  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2050, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()

  expect(onFollowingTailChange).not.toHaveBeenCalledWith(false)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
})

test('detaching cancels an already scheduled tail-scroll animation frame', async () => {
  vi.useFakeTimers()
  try {
    render(
      <ChatTranscript
        events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      />,
    )
    const log = screen.getByRole('log', { name: 'Chat messages' })
    setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
    const pointerDown = createEvent.pointerDown(log, { clientY: 200 })
    Object.defineProperty(pointerDown, 'pointerType', { value: 'touch' })
    fireEvent(log, pointerDown)
    const pointerMove = createEvent.pointerMove(log, { clientY: 208 })
    Object.defineProperty(pointerMove, 'pointerType', { value: 'touch' })
    fireEvent(log, pointerMove)
    setScrollMetrics(log, { scrollTop: 1592, scrollHeight: 2000, clientHeight: 400 })
    fireEvent.scroll(log)
    await act(async () => vi.runAllTimersAsync())

    expect(log.scrollTop).toBe(1592)
    expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()
  } finally {
    vi.useRealTimers()
  }
})

test('the physical bottom hydrates newer events even after input intent has settled', async () => {
  vi.useFakeTimers()
  try {
    const onJumpToLatest = vi.fn()
    const { rerender } = render(
      <ChatTranscript
        hasNewerEvents
        onJumpToLatest={onJumpToLatest}
        events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      />,
    )
    const log = screen.getByRole('log', { name: 'Chat messages' })

    setScrollMetrics(log, { scrollTop: 1500, scrollHeight: 2000, clientHeight: 400 })
    fireEvent.wheel(log, { deltaY: 100 })
    fireEvent.scroll(log)
    await act(async () => vi.runAllTimersAsync())
    setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
    fireEvent.scroll(log)
    await act(async () => vi.runAllTimersAsync())

    expect(onJumpToLatest).toHaveBeenCalledOnce()
    rerender(
      <ChatTranscript
        onJumpToLatest={onJumpToLatest}
        events={[event(2, 'agent.message.completed', 'assistant', 'completed', { text: 'Latest' })]}
      />,
    )
    expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
  } finally {
    vi.useRealTimers()
  }
})

test('ArrowUp uses the upward escape threshold instead of immediately pausing', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  fireEvent.keyDown(log, { key: 'ArrowUp' })
  setScrollMetrics(log, { scrollTop: 1580, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()

  expect(onFollowingTailChange).not.toHaveBeenCalledWith(false)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
})

test.each(['PageUp', 'Home'])('%s immediately pauses following', (key) => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  fireEvent.keyDown(log, { key })

  expect(onFollowingTailChange).toHaveBeenLastCalledWith(false)
  expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()
})

test('a slight forward scroll does not snap from far above the tail', async () => {
  const onFollowingTailChange = vi.fn()
  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  await act(async () => {
    await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()))
  })
  setScrollMetrics(log, { scrollTop: 1150, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: -450 })
  fireEvent.scroll(log)
  await settleVirtualScroll()

  setScrollMetrics(log, { scrollTop: 1151, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: 1 })
  fireEvent.scroll(log)
  await settleVirtualScroll()

  expect(onFollowingTailChange).toHaveBeenLastCalledWith(false)
  expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()
})

test('appended rows follow only while the viewport is pinned', async () => {
  const { rerender } = render(
    <ChatTranscript events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]} />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 600, scrollHeight: 1000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 600, scrollHeight: 1160, clientHeight: 400 })
  rerender(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' }),
        event(2, 'user.message.completed', 'user', 'completed', { text: 'Next' }),
        event(3, 'agent.message.completed', 'assistant', 'completed', { text: 'Two' }),
      ]}
    />,
  )

  expect(log.scrollTop).toBe(1160)

  setScrollMetrics(log, { scrollTop: 700, scrollHeight: 1160, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 300, scrollHeight: 1160, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 300, scrollHeight: 1320, clientHeight: 400 })
  rerender(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' }),
        event(2, 'user.message.completed', 'user', 'completed', { text: 'Next' }),
        event(3, 'agent.message.completed', 'assistant', 'completed', { text: 'Two' }),
        event(4, 'user.message.completed', 'user', 'completed', { text: 'Again' }),
        event(5, 'agent.message.completed', 'assistant', 'completed', { text: 'Three' }),
      ]}
    />,
  )

  expect(log.scrollTop).toBe(300)
})

test('a locally submitted user message resumes following from paused history', async () => {
  const onFollowingTailChange = vi.fn()
  const events = [event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]
  const { rerender } = render(
    <ChatTranscript events={events} onFollowingTailChange={onFollowingTailChange} />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  setScrollMetrics(log, { scrollTop: 1600, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.scroll(log)
  await settleVirtualScroll()
  setScrollMetrics(log, { scrollTop: 900, scrollHeight: 2000, clientHeight: 400 })
  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log)
  await settleVirtualScroll()

  expect(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).toBeInTheDocument()

  rerender(
    <ChatTranscript
      events={events}
      optimisticUserMessages={[optimisticUserMessage('client-1', 'New local prompt')]}
      onFollowingTailChange={onFollowingTailChange}
    />,
  )

  expect(screen.getByText('New local prompt')).toBeInTheDocument()
  expect(onFollowingTailChange).toHaveBeenLastCalledWith(true)
  expect(log.scrollTop).toBe(2000)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
})

test('a pinned transcript follows when the live tail is replaced without increasing the row count', async () => {
  const prompt = event(1, 'user.message.completed', 'user', 'completed', { text: 'Do the work' })
  const { rerender } = render(
    <ChatTranscript
      activityStatus={{ kind: 'thinking' }}
      events={[prompt]}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })

  await act(async () => {
    await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()))
  })
  setScrollMetrics(log, { scrollTop: 600, scrollHeight: 1000, clientHeight: 400 })
  setScrollMetrics(log, { scrollTop: 600, scrollHeight: 1300, clientHeight: 400 })

  rerender(
    <ChatTranscript
      events={[
        prompt,
        event(2, 'agent.message.delta', 'assistant', 'delta', { item_id: 'answer_1', text: 'Starting' }),
      ]}
    />,
  )

  expect(log.scrollTop).toBe(1300)
  expect(screen.getByText('Starting')).toBeInTheDocument()
})

test('a pinned transcript keeps a hydrated tool-only assistant row with the same event count and tail sequence', async () => {
  const prompt = event(1, 'user.message.completed', 'user', 'completed', { text: 'Inspect the files' })
  const cachedEvents = [
    prompt,
    event(2, 'provider.opencode.event', 'system', 'completed', { provider: 'opencode' }),
    event(3, 'tool.call.started', 'assistant', 'started', {
      provider: 'opencode',
      item_id: 'tool_1',
      command: 'cached command',
    }),
  ]
  const hydratedEvents = [
    prompt,
    event(2, 'tool.call.started', 'assistant', 'started', {
      provider: 'opencode',
      item_id: 'tool_1',
      command: 'durable command',
    }),
    event(3, 'provider.opencode.event', 'system', 'completed', { provider: 'opencode' }),
  ]
  const { rerender } = render(<ChatTranscript events={cachedEvents} />)
  const log = screen.getByRole('log', { name: 'Chat messages' })

  await act(async () => {
    await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()))
  })
  setScrollMetrics(log, { scrollTop: 600, scrollHeight: 1000, clientHeight: 400 })
  setScrollMetrics(log, { scrollTop: 600, scrollHeight: 1300, clientHeight: 400 })

  rerender(<ChatTranscript events={hydratedEvents} />)

  expect(log.scrollTop).toBe(1300)
  expect(screen.getByText('Working...')).toBeInTheDocument()
  expect(screen.getByText('durable command')).toBeInTheDocument()
})

test('growing composer clearance keeps a pinned transcript at the tail', () => {
  const { rerender } = render(
    <ChatTranscript
      bottomInsetHeight={176}
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 1000, scrollHeight: 1160, clientHeight: 400 })

  rerender(
    <ChatTranscript
      bottomInsetHeight={336}
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]}
    />,
  )

  expect(log.scrollTop).toBe(1160)
  expect(log).toHaveAttribute('data-tail-clearance-height', '342')
})

test('pinning a newly selected session hydrates and scrolls to its live tail', async () => {
  const onJumpToLatest = vi.fn()
  const { rerender } = render(
    <ChatTranscript
      pinToLatestOnMount
      hasNewerEvents
      onJumpToLatest={onJumpToLatest}
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Cached' })]}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 100, scrollHeight: 1200, clientHeight: 400 })

  await waitFor(() => expect(onJumpToLatest).toHaveBeenCalledOnce())
  rerender(
    <ChatTranscript
      pinToLatestOnMount
      onJumpToLatest={onJumpToLatest}
      events={[event(2, 'agent.message.completed', 'assistant', 'completed', { text: 'Latest' })]}
    />,
  )
  await waitFor(() => expect(log.scrollTop).toBe(1200))
})

test('jump to latest hydrates newer events before resuming following', async () => {
  const user = userEvent.setup()
  const onJumpToLatest = vi.fn()
  const onFollowingTailChange = vi.fn()
  const { rerender } = render(
    <ChatTranscript
      hasNewerEvents
      onJumpToLatest={onJumpToLatest}
      onFollowingTailChange={onFollowingTailChange}
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Older' })]}
    />,
  )
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 100, scrollHeight: 1200, clientHeight: 400 })

  await user.click(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' }))
  expect(onJumpToLatest).toHaveBeenCalledOnce()
  rerender(
    <ChatTranscript
      onJumpToLatest={onJumpToLatest}
      onFollowingTailChange={onFollowingTailChange}
      events={[event(2, 'agent.message.completed', 'assistant', 'completed', { text: 'Latest' })]}
    />,
  )

  await waitFor(() => expect(log.scrollTop).toBe(1200))
  expect(onFollowingTailChange).toHaveBeenLastCalledWith(true)
})

test('uses contained overscroll and bottom-aligns short transcripts', () => {
  render(
    <ChatTranscript events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'One' })]} />,
  )

  expect(screen.getByRole('log', { name: 'Chat messages' })).toHaveClass('overscroll-y-none')
  const virtualContent = screen.getByTestId('chat-transcript-virtual-content')
  const messageRow = screen.getByText('One').closest<HTMLElement>('[data-index]')
  expect(virtualContent).toHaveClass('mt-auto')
  expect(virtualContent.style.height).toBe('')
  expect(messageRow?.style.transform).toBe('')
})

test('copies fenced code blocks from user and assistant messages', async () => {
  const user = userEvent.setup()
  const writeText = vi.fn(async () => undefined)
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  })

  render(
    <ChatTranscript
      events={[
        event(1, 'user.message.completed', 'user', 'completed', {
          text: 'Run this:\n\n```\nbun test\n```',
        }),
        event(2, 'agent.message.completed', 'assistant', 'completed', {
          text: 'Use this:\n\n```ts\nconst answer = 42\n```',
        }),
      ]}
    />,
  )

  const copyButtons = screen.getAllByRole('button', { name: 'Copy code' })
  expect(copyButtons).toHaveLength(2)

  await user.click(copyButtons[0])
  await user.click(copyButtons[1])

  expect(writeText).toHaveBeenNthCalledWith(1, expect.stringContaining('bun test'))
  expect(writeText).toHaveBeenNthCalledWith(2, expect.stringContaining('const answer = 42'))
})

test('shows an explicit error when code cannot be copied', async () => {
  const user = userEvent.setup()
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: vi.fn(async () => { throw new DOMException('Denied', 'NotAllowedError') }) },
  })
  Object.defineProperty(document, 'execCommand', {
    configurable: true,
    value: vi.fn(() => false),
  })

  render(
    <ChatTranscript
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: '```\nno copy\n```' })]}
    />,
  )

  await user.click(screen.getByRole('button', { name: 'Copy code' }))

  expect(await screen.findByRole('alert')).toHaveTextContent('Copy failed')
})

test('always shows the subtle message copy action, including while streaming', async () => {
  const user = userEvent.setup()
  const writeText = vi.fn(async () => undefined)
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  })

  const { rerender } = render(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', {
          text: 'Full answer body',
        }),
      ]}
    />,
  )

  expect(screen.getByRole('button', { name: 'Copy message' })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Copy message' }))

  expect(writeText).toHaveBeenCalledWith('Full answer body')

  rerender(
    <ChatTranscript
      events={[event(1, 'agent.message.delta', 'assistant', 'delta', { text: 'Streaming body' })]}
    />,
  )

  expect(screen.getByRole('button', { name: 'Copy message' })).toBeInTheDocument()
})

test('groups tool calls under assistant messages with expandable output', async () => {
  const user = userEvent.setup()
  const writeText = vi.fn(async () => undefined)
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  })

  render(
    <ChatTranscript
      events={[
        event(1, 'user.message.completed', 'user', 'completed', { text: 'Run tests' }),
        event(2, 'tool.call.started', 'assistant', 'started', { item_id: 'tool_1', command: 'go test ./...' }),
        event(3, 'tool.call.completed', 'assistant', 'completed', { item_id: 'tool_1', output: 'ok' }),
        event(4, 'agent.message.completed', 'assistant', 'completed', { text: 'Tests passed.' }),
      ]}
    />,
  )

  expect(screen.getByText('Tests passed.')).toBeInTheDocument()
  expect(screen.queryByText('Tool Calls (1)')).not.toBeInTheDocument()
  expect(screen.getByText('go test ./...')).toBeInTheDocument()
  expect(screen.queryByText('completed')).not.toBeInTheDocument()
  expect(screen.queryByText(/go test \.\/\.\.\.\s+ok/)).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: /expand go test \.\/\.\.\./i }))

  expect(screen.getByText(/go test \.\/\.\.\.\s+ok/)).toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Copy tool output' }))

  expect(writeText).toHaveBeenCalledWith('go test ./...\nok')
})

test('expands a running tool call from the chevron, status dot, label, and row', async () => {
  const user = userEvent.setup()

  render(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Checking channels.' }),
        event(2, 'tool.call.started', 'assistant', 'started', {
          item_id: 'tool_1',
          command: 'slackdump list channels',
        }),
      ]}
    />,
  )

  const row = screen.getByRole('button', { name: /expand slackdump list channels/i })
  const [chevron, statusDot, label] = Array.from(row.children) as HTMLElement[]

  expect(row).toHaveClass('h-5', 'touch-manipulation')
  expect(row.parentElement?.parentElement).not.toHaveClass('space-y-1')
  expect(chevron).toHaveClass('pointer-events-none')
  expect(statusDot).toHaveClass('pointer-events-none')
  expect(label).toHaveClass('pointer-events-none')

  await user.click(chevron)
  expect(row).toHaveAttribute('aria-expanded', 'true')
  await user.click(statusDot)
  expect(row).toHaveAttribute('aria-expanded', 'false')
  await user.click(label)
  expect(row).toHaveAttribute('aria-expanded', 'true')
  await user.click(row)
  expect(row).toHaveAttribute('aria-expanded', 'false')
})

test('opens file-change diffs in the file editor', async () => {
  const user = userEvent.setup()
  const onOpenFilePath = vi.fn()

  render(
    <ChatTranscript
      onOpenFilePath={onOpenFilePath}
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Updating file.' }),
        event(2, 'file.change.completed', 'assistant', 'completed', {
          item_id: 'edit_1',
          paths: ['/repo/src/main.go'],
          changes: [
            {
              path: '/repo/src/main.go',
              patch: '@@ -1,2 +1,2 @@\n-old\n+new',
            },
          ],
        }),
      ]}
    />,
  )

  await user.click(screen.getByRole('button', { name: /expand main\.go/i }))
  expect(screen.getByText('-old')).toHaveClass('min-w-full', 'w-max')
  expect(screen.getByText('+new')).toHaveClass('min-w-full', 'w-max')
  await user.click(screen.getByRole('button', { name: 'Show in File Editor' }))

  expect(onOpenFilePath).toHaveBeenCalledWith('/repo/src/main.go')
})

test('shows codex command aggregated output in expandable tool output', async () => {
  const user = userEvent.setup()

  render(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Listing files.' }),
        event(2, 'tool.call.started', 'assistant', 'started', {
          item_id: 'tool_1',
          command: "/bin/zsh -lc 'ls -la'",
        }),
        event(3, 'tool.call.completed', 'assistant', 'completed', {
          item_id: 'tool_1',
          command: "/bin/zsh -lc 'ls -la'",
          aggregated_output: 'total 56\nREADME.md\nweb\n',
          exit_code: 0,
        }),
      ]}
    />,
  )

  expect(screen.getByText('ls -la')).toBeInTheDocument()
  expect(screen.queryByText(/README\.md/)).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: /expand ls -la/i }))

  expect(screen.getByText(/ls -la\s+total 56\s+README\.md\s+web/)).toBeInTheDocument()
})

test('loads externalized tool output only when requested', async () => {
  const user = userEvent.setup()
  const request = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('complete output\nsecond line'))

  render(
    <ChatTranscript
      events={[
        event(1, 'tool.call.started', 'assistant', 'started', {
          item_id: 'tool_1',
          command: 'run-report',
        }),
        event(2, 'tool.call.completed', 'assistant', 'completed', {
          item_id: 'tool_1',
          output: 'preview… output truncated; load full output',
          _gorchestra_tool_output: { truncated: true, original_bytes: 100000 },
        }),
      ]}
    />,
  )

  await user.click(screen.getByRole('button', { name: /expand run-report/i }))
  expect(request).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Load full output' }))
  expect(await screen.findByText(/complete output\s+second line/)).toBeInTheDocument()
  expect(request).toHaveBeenCalledWith('/api/sessions/sess_1/events/2/tool-output')
  request.mockRestore()
})

test('expands historical nested MCP tool output', async () => {
  const user = userEvent.setup()

  render(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Running tests.' }),
        event(2, 'tool.call.started', 'assistant', 'started', {
          item_id: 'tool_1',
          item_type: 'mcpToolCall',
          server: 'life',
          tool: 'exec_command',
          arguments: { command: 'go test ./...', cwd: '/repo' },
        }),
        event(3, 'tool.call.completed', 'assistant', 'completed', {
          item_id: 'tool_1',
          item_type: 'mcpToolCall',
          server: 'life',
          tool: 'exec_command',
          arguments: { command: 'go test ./...', cwd: '/repo' },
          result: {
            content: [{ type: 'text', text: '{"output":"ok\\n"}' }],
            structuredContent: { output: 'ok\n' },
          },
        }),
      ]}
    />,
  )

  const row = screen.getByRole('button', { name: /expand go test/i })
  await user.click(row)

  expect(row).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByText(/go test \.\/\.\.\.\s+ok/)).toBeInTheDocument()
})

test('renders MCP media and resource result blocks', async () => {
  const user = userEvent.setup()

  render(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Fetching artifacts.' }),
        event(2, 'tool.call.completed', 'assistant', 'completed', {
          item_id: 'tool_1',
          item_type: 'mcpToolCall',
          tool: 'fetch_artifacts',
          result: {
            content: [
              { type: 'image', data: 'aW1hZ2U=', mimeType: 'image/png' },
              { type: 'audio', data: 'YXVkaW8=', mimeType: 'audio/wav' },
              {
                type: 'resource',
                resource: { uri: 'mcp://files/report.pdf', blob: 'cGRm', mimeType: 'application/pdf' },
              },
              {
                type: 'resource_link',
                name: 'Reference',
                uri: 'https://example.com/reference',
                description: 'Supporting source',
                mimeType: 'text/html',
              },
            ],
          },
        }),
      ]}
    />,
  )

  await user.click(screen.getByRole('button', { name: /expand fetch_artifacts/i }))

  expect(screen.getByRole('button', { name: 'Preview image result' }).querySelector('img')).toHaveAttribute(
    'src',
    '/api/sessions/sess_1/events/2/tool-content/0',
  )
  expect(document.querySelector('audio')).toHaveAttribute('src', '/api/sessions/sess_1/events/2/tool-content/1')
  expect(screen.getByRole('link', { name: /report\.pdf/i })).toHaveAttribute(
    'href',
    '/api/sessions/sess_1/events/2/tool-content/2',
  )
  expect(screen.getByRole('link', { name: /Reference/i })).toHaveAttribute('href', 'https://example.com/reference')
  expect(screen.getByRole('link', { name: /Reference/i })).toHaveAttribute('target', '_blank')
})

test('does not show an expand affordance for a tool with no details', () => {
  render(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Waiting.' }),
        event(2, 'tool.call.completed', 'assistant', 'completed', {
          item_id: 'tool_1',
          item_type: 'collabAgentToolCall',
          tool: 'wait',
        }),
      ]}
    />,
  )

  const row = screen.getByRole('button', { name: 'wait' })
  expect(row).toBeDisabled()
  expect(row).not.toHaveAttribute('aria-expanded')
  expect(row.querySelector('svg')).not.toBeInTheDocument()
})

test('expands nested MCP error messages as failed tool output', async () => {
  const user = userEvent.setup()

  render(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Trying a tool.' }),
        event(2, 'tool.call.started', 'assistant', 'started', {
          item_id: 'tool_1',
          item_type: 'mcpToolCall',
          tool: 'unavailable_tool',
        }),
        event(3, 'tool.call.completed', 'assistant', 'failed', {
          item_id: 'tool_1',
          item_type: 'mcpToolCall',
          tool: 'unavailable_tool',
          error: { message: 'Tool unavailable', code: -32000 },
        }),
      ]}
    />,
  )

  await user.click(screen.getByRole('button', { name: /expand unavailable_tool/i }))

  expect(screen.getByText('Tool unavailable')).toHaveClass('text-destructive')
})

test('shows web search query details in expandable tool output', async () => {
  const user = userEvent.setup()

  render(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', { text: 'Checking weather.' }),
        event(2, 'tool.call.started', 'assistant', 'started', {
          item_id: 'web_1',
          item_type: 'webSearch',
          action: { type: 'other' },
          query: '',
        }),
        event(3, 'tool.call.completed', 'assistant', 'completed', {
          item_id: 'web_1',
          item_type: 'webSearch',
          action: {
            type: 'search',
            query: 'weather: 33445, United States',
            queries: ['weather: 33445, United States'],
          },
          query: 'weather: 33445, United States',
        }),
      ]}
    />,
  )

  expect(screen.getByText('Web search: weather: 33445, United States')).toBeInTheDocument()
  expect(screen.queryByText('Query: weather: 33445, United States')).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: /expand web search: weather: 33445/i }))

  expect(screen.getByText(/Query: weather: 33445, United States/)).toBeInTheDocument()
  expect(screen.getByText(/- weather: 33445, United States/)).toBeInTheDocument()
})

test('renders active tool indicators with the animated activity dot', () => {
  render(
    <ChatTranscript
      events={[
        event(1, 'agent.message.completed', 'assistant', 'completed', { item_id: 'msg_1', text: 'Running a tool.' }),
        event(2, 'tool.call.started', 'assistant', 'started', {
          item_id: 'tool_1',
          command: 'sleep 20',
        }),
      ]}
    />,
  )

  const toolButton = screen.getByRole('button', { name: /expand sleep 20/i })
  expect(toolButton.querySelector('.tool-activity-dot')).toBeInTheDocument()
  expect(toolButton.querySelector('.animate-pulse')).not.toBeInTheDocument()
})

test('immediately collapses the latest message to its three most recent tool calls', () => {
  const events = [
    event(1, 'agent.message.completed', 'assistant', 'completed', { item_id: 'msg_1', text: 'Working through tools.' }),
  ]
  for (let index = 1; index <= 5; index += 1) {
    events.push(
      event(index * 2, 'tool.call.started', 'assistant', 'started', {
        item_id: `tool_${index}`,
        command: `tool-${index}`,
      }),
      event(index * 2 + 1, 'tool.call.completed', 'assistant', 'completed', {
        item_id: `tool_${index}`,
        output: `output-${index}`,
      }),
    )
  }

  render(<ChatTranscript events={events} />)

  expect(screen.queryByText('tool-1')).not.toBeInTheDocument()
  expect(screen.queryByText('tool-2')).not.toBeInTheDocument()
  expect(screen.getByText('tool-3')).toBeInTheDocument()
  expect(screen.getByText('tool-4')).toBeInTheDocument()
  expect(screen.getByText('tool-5')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Show 2 Earlier' })).toBeInTheDocument()
})

test('rolls the visible tool window forward as new calls arrive', () => {
  function eventsThrough(toolCount: number) {
    const events = [
      event(1, 'agent.message.delta', 'assistant', 'delta', { item_id: 'msg_1', text: 'Working through tools.' }),
    ]
    for (let index = 1; index <= toolCount; index += 1) {
      events.push(
        event(index * 2, 'tool.call.started', 'assistant', 'started', {
          item_id: `tool_${index}`,
          command: `tool-${index}`,
        }),
        event(index * 2 + 1, 'tool.call.completed', 'assistant', 'completed', {
          item_id: `tool_${index}`,
          output: `output-${index}`,
        }),
      )
    }
    return events
  }

  const { rerender } = render(<ChatTranscript events={eventsThrough(4)} />)

  expect(screen.queryByText('tool-1')).not.toBeInTheDocument()
  expect(screen.getAllByText(/^tool-[2-4]$/).map((element) => element.textContent)).toEqual(['tool-2', 'tool-3', 'tool-4'])

  rerender(<ChatTranscript events={eventsThrough(5)} />)

  expect(screen.queryByText('tool-2')).not.toBeInTheDocument()
  expect(screen.getAllByText(/^tool-[3-5]$/).map((element) => element.textContent)).toEqual(['tool-3', 'tool-4', 'tool-5'])
  expect(screen.getByRole('button', { name: 'Show 2 Earlier' })).toBeInTheDocument()
})

test('keeps completed tool lists collapsed after the next message bubble appears', async () => {
  const user = userEvent.setup()
  const events = [
    event(1, 'agent.message.completed', 'assistant', 'completed', { item_id: 'msg_1', text: 'Working through tools.' }),
  ]
  for (let index = 1; index <= 5; index += 1) {
    events.push(
      event(index * 2, 'tool.call.started', 'assistant', 'started', {
        item_id: `tool_${index}`,
        command: `tool-${index}`,
      }),
      event(index * 2 + 1, 'tool.call.completed', 'assistant', 'completed', {
        item_id: `tool_${index}`,
        output: `output-${index}`,
      }),
    )
  }
  events.push(
    event(20, 'agent.message.completed', 'assistant', 'completed', {
      item_id: 'msg_2',
      text: 'Done with the tools.',
    }),
  )

  render(<ChatTranscript events={events} />)

  expect(screen.queryByText('Tool Calls (5)')).not.toBeInTheDocument()
  expect(screen.queryByText('tool-1')).not.toBeInTheDocument()
  expect(screen.queryByText('tool-2')).not.toBeInTheDocument()
  expect(screen.getByText('tool-3')).toBeInTheDocument()
  expect(screen.getByText('tool-4')).toBeInTheDocument()
  expect(screen.getByText('tool-5')).toBeInTheDocument()
  expect(screen.getByText('Done with the tools.')).toBeInTheDocument()
  const showMoreButton = screen.getByRole('button', { name: 'Show 2 Earlier' })
  expect(showMoreButton).toHaveClass('flex', 'min-h-6', 'w-fit', 'py-1', 'leading-4')
  expect(showMoreButton).not.toHaveClass('inline-flex', 'py-0', 'leading-none')

  await user.click(showMoreButton)

  expect(screen.getByText('tool-1')).toBeInTheDocument()
  expect(screen.getByText('tool-2')).toBeInTheDocument()
  expect(screen.getByText('tool-4')).toBeInTheDocument()
  expect(screen.getByText('tool-5')).toBeInTheDocument()
})

test('renders separate assistant items with sequential tools', () => {
  render(
    <ChatTranscript
      events={[
        event(1, 'user.message.completed', 'user', 'completed', { text: 'Split this into sections' }),
        event(2, 'agent.message.delta', 'assistant', 'delta', { item_id: 'msg_1', text: 'Section 1' }),
        event(3, 'agent.message.completed', 'assistant', 'completed', { item_id: 'msg_1', text: 'Section 1' }),
        event(4, 'tool.call.started', 'assistant', 'started', { item_id: 'tool_1', command: '/bin/zsh -lc pwd' }),
        event(5, 'tool.call.completed', 'assistant', 'completed', { item_id: 'tool_1', output: '/repo' }),
        event(6, 'agent.message.delta', 'assistant', 'delta', { item_id: 'msg_2', text: 'Section 2' }),
        event(7, 'agent.message.completed', 'assistant', 'completed', { item_id: 'msg_2', text: 'Section 2' }),
        event(8, 'tool.call.started', 'assistant', 'started', {
          item_id: 'tool_2',
          command: "/bin/zsh -lc 'git status --short'",
        }),
        event(9, 'tool.call.completed', 'assistant', 'completed', { item_id: 'tool_2', output: ' M file.ts' }),
      ]}
    />,
  )

  expect(screen.getByText('Section 1')).toBeInTheDocument()
  expect(screen.getByText('Section 2')).toBeInTheDocument()
  expect(screen.getByText('pwd')).toBeInTheDocument()
  expect(screen.getByText('git status --short')).toBeInTheDocument()
  expect(screen.queryByText(/\/bin\/zsh/)).not.toBeInTheDocument()
  expect(screen.queryByText('Assistant')).not.toBeInTheDocument()
})

test('renders streaming assistant messages without a badge', () => {
  render(
    <ChatTranscript
      events={[
        event(1, 'user.message.completed', 'user', 'completed', { text: 'Hello' }),
        event(2, 'agent.message.delta', 'assistant', 'delta', { text: 'Thinking' }),
      ]}
    />,
  )

  expect(screen.getByText('Thinking')).toBeInTheDocument()
  expect(screen.queryByText('Streaming')).not.toBeInTheDocument()
})

test('renders active thinking inline in the chat log', () => {
  render(
    <ChatTranscript
      activityStatus={{ kind: 'thinking' }}
      events={[event(1, 'user.message.completed', 'user', 'completed', { text: 'Hello' })]}
    />,
  )

  const thinkingStatus = screen.getByRole('status', { name: 'Thinking' })
  expect(thinkingStatus).toBeInTheDocument()
  expect(screen.getByRole('log', { name: 'Chat messages' })).toContainElement(thinkingStatus)
  expect(screen.getByText('Hello').compareDocumentPosition(thinkingStatus)).toBe(Node.DOCUMENT_POSITION_FOLLOWING)
})

test('renders active thinking instead of the empty transcript state', () => {
  render(<ChatTranscript activityStatus={{ kind: 'thinking' }} events={[]} />)

  expect(screen.getByRole('status', { name: 'Thinking' })).toBeInTheDocument()
  expect(screen.queryByText('No messages yet. Submit a prompt to start the chat.')).not.toBeInTheDocument()
})

test('previews image attachments in a dialog with download action', async () => {
  const user = userEvent.setup()
  render(
    <ChatTranscript
      events={[
        event(4, 'user.message.completed', 'user', 'completed', {
          text: 'see image',
          attachments: [
            {
              name: 'image.png',
              media_type: 'image/png',
              data_url: 'data:image/png;base64,[gorchestra truncated 100 bytes from this field for browser display]',
              size_bytes: 1234,
            },
          ],
        }),
      ]}
    />,
  )

  const thumbnail = screen.getByRole('button', { name: 'Preview image.png' })
  expect(screen.getByRole('img', { name: 'image.png' })).toHaveAttribute('src', '/api/sessions/sess_1/events/4/attachments/0')

  await user.click(thumbnail)

  expect(screen.getByRole('dialog')).toBeInTheDocument()
  expect(screen.getByRole('heading', { name: 'image.png' })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'Download' })).toHaveAttribute(
    'href',
    '/api/sessions/sess_1/events/4/attachments/0',
  )
  expect(screen.getByRole('link', { name: 'Download' })).toHaveAttribute('download', 'image.png')
})

test('hides debug-only events unless enabled', () => {
  render(
    <ChatTranscript
      events={[
        event(1, 'user.message.completed', 'user', 'completed', { text: 'Hello' }),
        event(2, 'session.status.updated', 'system', 'started', { status: 'running' }),
        event(3, 'agent.log.delta', 'system', 'delta', { text: 'debug line' }),
        event(4, 'agent.message.completed', 'assistant', 'completed', { text: 'Done' }),
      ]}
    />,
  )

  expect(screen.getByText('Hello')).toBeInTheDocument()
  expect(screen.getByText('Done')).toBeInTheDocument()
  expect(screen.queryByText('Session status')).not.toBeInTheDocument()
  expect(screen.queryByText('debug line')).not.toBeInTheDocument()
})

test('renders compact debug rows with expandable payloads', async () => {
  const user = userEvent.setup()
  const writeText = vi.fn(async () => undefined)
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  })

  render(
    <ChatTranscript
      showDebugEvents
      events={[
        event(1, 'user.message.completed', 'user', 'completed', { text: 'Hello' }),
        event(2, 'session.status.updated', 'system', 'started', { status: 'running' }),
        event(3, 'agent.log.delta', 'system', 'delta', { text: 'debug line' }),
        event(4, 'agent.message.completed', 'assistant', 'completed', { text: 'Done' }),
      ]}
    />,
  )

  expect(screen.getByText('Session status')).toBeInTheDocument()
  expect(screen.getByText('Log')).toBeInTheDocument()
  expect(screen.getByText('debug line')).toBeInTheDocument()
  expect(screen.getByText('Session status').closest('article')?.parentElement).toHaveClass('mt-2')
  expect(screen.getByText('Log').closest('article')?.parentElement).toHaveClass('mt-1')

  await user.click(screen.getByRole('button', { name: /expand session status/i }))

  expect(screen.getByText(/"status": "running"/)).toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Copy debug payload' }))

  expect(writeText).toHaveBeenCalledWith(expect.stringContaining('"status": "running"'))
})

test('labels provider debug rows with provider event type', () => {
  render(
    <ChatTranscript
      showDebugEvents
      events={[event(1, 'provider.codex.event', 'system', 'completed', { provider_event_type: 'turn/completed' })]}
    />,
  )

  expect(screen.getByText('turn/completed')).toBeInTheDocument()
  expect(screen.queryByText('provider.codex.event')).not.toBeInTheDocument()
})

test('reports the visible conversation sequence range for the rail map', async () => {
  const onVisibleSequenceRangeChange = vi.fn()
  render(
    <ChatTranscript
      events={[
        event(1, 'user.message.completed', 'user', 'completed', { text: 'First prompt' }),
        event(2, 'agent.message.completed', 'assistant', 'completed', { text: 'First answer' }),
        event(3, 'user.message.completed', 'user', 'completed', { text: 'Second prompt' }),
        event(4, 'agent.message.completed', 'assistant', 'completed', { text: 'Second answer' }),
      ]}
      onVisibleSequenceRangeChange={onVisibleSequenceRangeChange}
    />,
  )

  await waitFor(() => {
    expect(onVisibleSequenceRangeChange).toHaveBeenCalledWith({ firstSeq: 1, lastSeq: 4 })
  })
})

test('a repeated focus request scrolls the targeted virtual conversation row into view', () => {
  const events = [
    event(1, 'user.message.completed', 'user', 'completed', { text: 'First prompt' }),
    event(2, 'agent.message.completed', 'assistant', 'completed', { text: 'First answer' }),
    event(3, 'user.message.completed', 'user', 'completed', { text: 'Second prompt' }),
    event(4, 'agent.message.completed', 'assistant', 'completed', { text: 'Second answer' }),
  ]
  const { rerender } = render(<ChatTranscript events={events} focusSeq={3} focusRequest={0} />)
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 0, scrollHeight: 1000, clientHeight: 200 })

  rerender(<ChatTranscript events={events} focusSeq={3} focusRequest={1} />)

  expect(log.scrollTop).toBeGreaterThan(0)
})

test.each([true, false])('explicit historical focus wins over initial tail pinning and live appends (newer events: %s)', async (hasNewerEvents) => {
  const onJumpToLatest = vi.fn()
  const onFollowingTailChange = vi.fn()
  const events = [event(3, 'user.message.completed', 'user', 'completed', { text: 'Historical target' })]
  const props = { events, focusSeq: 3, hasNewerEvents, pinToLatestOnMount: true, onJumpToLatest, onFollowingTailChange }
  const view = render(<ChatTranscript {...props} />)
  const log = screen.getByRole('log', { name: 'Chat messages' })
  setScrollMetrics(log, { scrollTop: 0, scrollHeight: 400, clientHeight: 400 })
  fireEvent.scroll(log)
  fireEvent(log, new Event('scrollend'))
  await settleVirtualScroll()
  view.rerender(<ChatTranscript {...props} events={[...events, event(4, 'agent.message.completed', 'assistant', 'completed', { text: 'Later' })]} />)
  expect(screen.getByText('Historical target')).toBeInTheDocument()
  expect(onJumpToLatest).not.toHaveBeenCalled()
  expect(onFollowingTailChange).not.toHaveBeenCalledWith(true)
})

function setScrollMetrics(
  element: HTMLElement,
  metrics: { scrollTop: number; scrollHeight: number; clientHeight: number },
) {
  Object.defineProperty(element, 'scrollTop', { configurable: true, writable: true, value: metrics.scrollTop })
  Object.defineProperty(element, 'scrollHeight', { configurable: true, writable: true, value: metrics.scrollHeight })
  Object.defineProperty(element, 'clientHeight', { configurable: true, writable: true, value: metrics.clientHeight })
}

function optimisticUserMessage(id: string, text: string): ChatTranscriptMessage {
  return {
    id,
    role: 'user',
    label: 'You',
    variant: 'default',
    text,
    attachments: [],
    skills: [],
    status: 'pending',
    createdAt: '2026-06-12T16:00:00Z',
    completedAt: '',
    durationMs: null,
    tools: [],
    streaming: false,
    startSeq: 0,
    endSeq: 0,
  }
}

function event(seq: number, type: string, role: string, status: string, payload: Record<string, unknown>): AgentEvent {
  return {
    id: `evt_${seq}`,
    session_id: 'sess_1',
    seq,
    type,
    role,
    status,
    payload,
    created_at: '2026-06-12T16:00:00Z',
  }
}

test('renders every supported follow-up directive form as buttons with exact decoded prompts', () => {
  const onFollowUp = vi.fn()
  const text = [
    '- :codex-followup[Check status]{prompt="Check the job status."}',
    '- :codex-followup[Review **changes**]{prompt="Review &quot;draft&quot; &amp; tests."}',
    '- :codex-followup{prompt="Find the missing permits and paid-in-full receipts."}',
    '- :codex-followup[Prepare the PDF for the selected print shop.]',
  ].join('\n')
  render(<ChatTranscript onFollowUp={onFollowUp} events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text })]} />)
  fireEvent.click(screen.getByRole('button', { name: 'Check status' }))
  expect(onFollowUp).toHaveBeenLastCalledWith('Check the job status.')
  fireEvent.click(screen.getByRole('button', { name: 'Review changes' }))
  expect(onFollowUp).toHaveBeenLastCalledWith('Review "draft" & tests.')
  fireEvent.click(screen.getByRole('button', { name: 'Find the missing permits and paid-in-full receipts.' }))
  expect(onFollowUp).toHaveBeenLastCalledWith('Find the missing permits and paid-in-full receipts.')
  fireEvent.click(screen.getByRole('button', { name: 'Prepare the PDF for the selected print shop.' }))
  expect(onFollowUp).toHaveBeenLastCalledWith('Prepare the PDF for the selected print shop.')
  expect(screen.getByRole('log')).not.toHaveTextContent(':codex-followup')
})

test('renders file citations as verified event links and keeps unsupported directives inert', () => {
  const onOpenFilePath = vi.fn()
  const text = [
    '`'+':codex-followup[Code]{prompt="Do not run"}'+'`',
    '```text\n:codex-followup[Fenced]{prompt="Do not run"}\n```',
    ':codex-file-citation{path="/tmp/example.txt" purpose="source"}',
    ':codex-file-citation{path="/tmp/unsafe.txt" purpose="source" onclick="alert(1)"}',
    ':codex-followup[Partial]{prompt="unfinished',
    ':codex-followup[Empty]{prompt=" "}',
    ':codex-followup[Unsafe]{prompt="Do not run" onclick="alert(1)"}',
    '[A :codex-followup[Nested]{prompt="Do not run"} link](https://example.com)',
  ].join('\n\n')
  render(
    <ChatTranscript
      onFollowUp={vi.fn()}
      onOpenFilePath={onOpenFilePath}
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text })]}
    />,
  )
  for (const name of ['Code', 'Fenced', 'Partial', 'Empty', 'Unsafe', 'Nested']) {
    expect(screen.queryByRole('button', { name })).not.toBeInTheDocument()
  }
  const citation = screen.getByRole('link', { name: 'example.txt' })
  expect(citation).toHaveAttribute('title', '/tmp/example.txt')
  expect(citation).toHaveAttribute('href', '/api/sessions/sess_1/events/1/file-citation?path=%2Ftmp%2Fexample.txt')
  expect(citation).toHaveAttribute('target', '_blank')
  expect(onOpenFilePath).not.toHaveBeenCalled()
  expect(screen.getByRole('log')).not.toHaveTextContent(':codex-file-citation{path="/tmp/example.txt" purpose="source"}')
  expect(screen.getByRole('log')).toHaveTextContent(':codex-file-citation{path="/tmp/unsafe.txt"')
  expect(screen.getByRole('log')).toHaveTextContent(':codex-followup[Partial]{prompt="unfinished')
})

test('renders an output file citation with spaces and underscores in its absolute path', () => {
  const onOpenFilePath = vi.fn()
  const path = '/Users/joey/Documents/Legal/Divorce/Ryann Responses/certificate-of-completion-OPP_17893173627841.pdf'
  const text = [
    'Done. I created `Ryann Responses` inside the divorce folder and saved the verified attachment there:',
    '',
    `:codex-file-citation{path="${path}" purpose="output"}`,
    '',
    'It confirms her 12-hour co-parenting course completion on September 13, 2026.',
  ].join('\n')
  render(
    <ChatTranscript
      onOpenFilePath={onOpenFilePath}
      events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text })]}
    />,
  )

  const citation = screen.getByRole('link', { name: 'certificate-of-completion-OPP_17893173627841.pdf' })
  expect(citation).toHaveAttribute('title', path)
  expect(citation).toHaveAttribute(
    'href',
    '/api/sessions/sess_1/events/1/file-citation?path=%2FUsers%2Fjoey%2FDocuments%2FLegal%2FDivorce%2FRyann+Responses%2Fcertificate-of-completion-OPP_17893173627841.pdf',
  )
  expect(citation).toHaveAttribute('target', '_blank')
  expect(onOpenFilePath).not.toHaveBeenCalled()
  expect(screen.getByRole('log')).not.toHaveTextContent(':codex-file-citation')
})

test('activates a follow-up only when its streaming directive is complete and keeps user examples literal', () => {
  const onFollowUp = vi.fn()
  const partial = ':codex-followup[Continue]{prompt="Continue the checks.'
  const { rerender } = render(<ChatTranscript onFollowUp={onFollowUp} events={[event(1, 'agent.message.delta', 'assistant', 'delta', { text: partial })]} />)
  expect(screen.queryByRole('button', { name: 'Continue' })).not.toBeInTheDocument()
  const full = partial + '"}'
  rerender(<ChatTranscript onFollowUp={onFollowUp} events={[event(1, 'agent.message.completed', 'assistant', 'completed', { text: full })]} />)
  fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
  expect(onFollowUp).toHaveBeenCalledWith('Continue the checks.')
  rerender(<ChatTranscript onFollowUp={onFollowUp} events={[event(1, 'user.message.completed', 'user', 'completed', { text: full })]} />)
  expect(screen.queryByRole('button', { name: 'Continue' })).not.toBeInTheDocument()
  expect(screen.getByRole('log')).toHaveTextContent(full)
})

test('keeps file citation examples in user messages literal', () => {
  const citation = ':codex-file-citation{path="/tmp/example.txt" purpose="source"}'
  render(
    <ChatTranscript
      onOpenFilePath={vi.fn()}
      events={[event(1, 'user.message.completed', 'user', 'completed', { text: citation })]}
    />,
  )
  expect(screen.queryByRole('link', { name: 'example.txt' })).not.toBeInTheDocument()
  expect(screen.getByRole('log')).toHaveTextContent(citation)
})
