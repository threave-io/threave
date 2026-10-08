import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ComponentProps, ReactNode } from 'react'
import type { AgentEvent, Session } from '@/lib/api'
import { clearAPIRequestCachesForTest } from '@/lib/api'
import { SessionDetail } from '@/components/session-detail'
import { readPendingSubmissions, savePendingSubmission } from '@/lib/pending-submissions'

beforeEach(() => {
  window.localStorage.clear()
  clearAPIRequestCachesForTest()
  vi.stubGlobal('indexedDB', undefined)
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({ messages: [], state: 'unknown', models: [], collaboration_modes: [] })))
})
afterEach(() => vi.unstubAllGlobals())

const baseSession: Session = {
  id: 'sess_1',
  title: 'Inspect repo',
  agent_type: 'fake',
  status: 'idle',
  workspace_path: '/repo',
  event_count: 0,
  tool_count: 0,
  created_at: '2026-06-12T16:00:00Z',
  updated_at: '2026-06-12T16:00:00Z',
  completed_at: null,
  archived_at: null,
}

test('cancel button is visible only while running', () => {
  const onCancel = vi.fn(async () => undefined)
  const { rerender } = renderDetail({ onCancel })

  expect(screen.queryByRole('button', { name: /cancel/i })).not.toBeInTheDocument()

  rerenderDetail(rerender, { session: { ...baseSession, status: 'running' }, onCancel })

  expect(screen.getByRole('button', { name: /cancel running session/i })).toBeInTheDocument()
})

test('prompt composer remains enabled after a completed run returns to idle', () => {
  renderDetail({ session: { ...baseSession, status: 'idle' } })

  expect(screen.getByLabelText('Prompt')).toBeEnabled()
})

test('async question card leaves thinking and the composer draft intact and clears after acknowledgement', async () => {
  const events = [
    event(1, 'agent.run.started', {}),
    event(2, 'agent.input.requested', { delivery: 'async', request_id: 'q', text: 'Pick one', questions: [{ id: 'q1', question: 'Pick one', is_other: true, options: [{ label: 'Alpha' }, { label: 'Beta' }] }] }),
    event(3, 'agent.thinking.started', { item_id: 'thinking' }),
  ]
  const onAnswerUserInput = vi.fn(async () => undefined)
  const overrides = { session: { ...baseSession, status: 'running' as const }, events, onAnswerUserInput }
  const view = renderDetail(overrides)
  const prompt = screen.getByLabelText('Prompt')
  fireEvent.change(prompt, { target: { value: 'My unrelated draft' } })
  const card = screen.getByRole('group', { name: 'Agent question' })
  expect(screen.getByText('Thinking')).toBeInTheDocument()
  fireEvent.click(within(card).getByRole('button', { name: 'Beta' }))
  await waitFor(() => expect(onAnswerUserInput).toHaveBeenCalledExactlyOnceWith('q', { q1: { answers: ['Beta'] } }))
  expect(prompt).toHaveValue('My unrelated draft')
  rerenderDetail(view.rerender, { ...overrides, events: [...events, event(4, 'agent.input.answered', { request_id: 'q', delivery: 'async', text: 'Beta' })] })
  expect(screen.queryByRole('group', { name: 'Agent question' })).not.toBeInTheDocument()
  expect(prompt).toHaveValue('My unrelated draft')
})

test('uncertain submission remains recoverable after unmount without becoming a new draft', async () => {
  let rejectSubmit: ((error: Error) => void) | undefined
  const onErrorMessageChange = vi.fn()
  const onSubmitPrompt = vi.fn(() => new Promise<void>((_resolve, reject) => {
      rejectSubmit = reject
    }))
  const view = renderDetail({
    onSubmitPrompt,
    onErrorMessageChange,
  })

  const prompt = screen.getByLabelText('Prompt')
  fireEvent.change(prompt, { target: { value: 'Prompt that fails' } })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Submit prompt' })).toBeEnabled())
  fireEvent.keyDown(prompt, { key: 'Enter' })

  await waitFor(() => expect(within(screen.getByRole('log', { name: 'Chat messages' })).getByText('Prompt that fails')).toBeInTheDocument())

  await act(async () => {
    rejectSubmit?.(new Error('HTTP 502'))
    await Promise.resolve()
  })

  await waitFor(() => expect(screen.queryByRole('log', { name: 'Chat messages' })).not.toBeInTheDocument())
  expect(prompt).toHaveValue('')
  expect(await readPendingSubmissions('sess_1')).toHaveLength(1)
  view.unmount()
  renderDetail({ onSubmitPrompt })
  const recovery = await screen.findByRole('region', { name: 'Pending message recovery' })
  expect(within(recovery).getByText('Prompt that fails')).toBeInTheDocument()
  expect(onSubmitPrompt).toHaveBeenCalledOnce()
})

test('session detail shows loading while a routed session resolves', () => {
  renderDetail({ session: null, resolvingSessionID: 'sess_1' })

  expect(screen.getByText('Loading session...')).toBeInTheDocument()
  expect(screen.queryByText('No session selected')).not.toBeInTheDocument()
})

test('reload restores an unacknowledged request; safe retry preserves its exact identity', async () => {
  const pending = { id: 'original-id', sessionID: baseSession.id, content: 'Durable request', attachments: [], skills: [], queue: false, createdAt: new Date().toISOString() }
  await savePendingSubmission(pending)
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({ messages: [], state: 'not_received' })))
  const onSubmitPrompt = vi.fn(async () => ({ session_id: baseSession.id, status: 'running' as const, accepted_as: 'run' as const }))
  renderDetail({ onSubmitPrompt })
  await screen.findByText('Durable request')
  expect(onSubmitPrompt).not.toHaveBeenCalled()
  expect(screen.getByLabelText('Prompt')).toHaveValue('')
  fireEvent.click(screen.getByRole('button', { name: 'Retry safely' }))
  await waitFor(() => expect(onSubmitPrompt).toHaveBeenCalledExactlyOnceWith('Durable request', undefined, [], false, [], 'original-id'))
  await waitFor(async () => expect(await readPendingSubmissions(baseSession.id)).toEqual([]))
})

test('a confirmed unsent message can be dismissed without losing the current draft', async () => {
  await savePendingSubmission({ id: 'unsent-id', sessionID: baseSession.id, content: 'Old unsent message', attachments: [], skills: [], queue: false, createdAt: new Date().toISOString() })
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({ messages: [], state: 'not_received' })))
  const onSubmitPrompt = vi.fn(async () => undefined)
  renderDetail({ onSubmitPrompt })
  const prompt = screen.getByLabelText('Prompt')
  fireEvent.change(prompt, { target: { value: 'My new draft' } })
  expect(screen.getByRole('button', { name: 'Submit prompt' })).toBeDisabled()
  fireEvent.click(await screen.findByRole('button', { name: 'Dismiss unsent message' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'Submit prompt' })).toBeEnabled())
  expect(prompt).toHaveValue('My new draft')
  expect(await readPendingSubmissions(baseSession.id)).toEqual([])
  expect(onSubmitPrompt).not.toHaveBeenCalled()
})

test('uncertain messages cannot be dismissed or retried as new work', async () => {
  await savePendingSubmission({ id: 'unknown-id', sessionID: baseSession.id, content: 'Uncertain message', attachments: [], skills: [], queue: false, createdAt: new Date().toISOString() })
  const onSubmitPrompt = vi.fn(async () => undefined)
  renderDetail({ onSubmitPrompt })
  await screen.findByRole('region', { name: 'Pending message recovery' })
  fireEvent.click(screen.getByRole('button', { name: 'Retry safely' }))
  await screen.findByText(/Delivery is still uncertain/)
  expect(screen.queryByRole('button', { name: /Dismiss/ })).not.toBeInTheDocument()
  expect(onSubmitPrompt).not.toHaveBeenCalled()
  expect(await readPendingSubmissions(baseSession.id)).toHaveLength(1)
})

test('Send now persists its target and recovers without changing a failed steer into a new turn', async () => {
  const events = [event(1, 'agent.run.started', { run_id: 'run1' }), event(2, 'agent.thinking.started', { run_id: 'run1' })]
  const onSubmitPrompt = vi.fn(async () => { throw new Error('Lost connection') })
  const view = renderDetail({ session: { ...baseSession, agent_type: 'codex', status: 'running' }, events, onSubmitPrompt })
  fireEvent.change(screen.getByLabelText('Prompt'), { target: { value: 'Change direction' } })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Send now' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: 'Send now' }))
  await screen.findByRole('region', { name: 'Pending message recovery' })
  expect(screen.getByLabelText('Prompt')).toHaveValue('')
  expect(screen.getByText('Thinking')).toBeInTheDocument()
  const pending = await readPendingSubmissions('sess_1')
  expect(pending).toEqual([expect.objectContaining({ content: 'Change direction', steerRunID: 'run1', queue: false })])
  expect(onSubmitPrompt).toHaveBeenCalledExactlyOnceWith('Change direction', undefined, [], false, [], pending[0].id, 'run1')
  view.unmount()
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({ messages: [], state: 'not_received' })))
  const retry = vi.fn(async () => undefined)
  renderDetail({ session: { ...baseSession, status: 'running' }, events: [event(3, 'agent.run.started', { run_id: 'run2' })], onSubmitPrompt: retry })
  await screen.findByRole('region', { name: 'Pending message recovery' })
  fireEvent.click(screen.getByRole('button', { name: 'Retry safely' }))
  await waitFor(() => expect(retry).toHaveBeenCalledExactlyOnceWith('Change direction', undefined, [], false, [], pending[0].id, 'run1'))
})

test('accepted receipt removes recovery record without resending', async () => {
  await savePendingSubmission({ id: 'accepted-id', sessionID: baseSession.id, content: 'Already accepted', attachments: [], skills: [], queue: true, createdAt: new Date().toISOString() })
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({ messages: [], state: 'accepted' })))
  const onSubmitPrompt = vi.fn(async () => undefined)
  renderDetail({ onSubmitPrompt })
  await waitFor(async () => expect(await readPendingSubmissions(baseSession.id)).toEqual([]))
  expect(onSubmitPrompt).not.toHaveBeenCalled()
  expect(screen.queryByRole('region', { name: 'Pending message recovery' })).not.toBeInTheDocument()
})

test('storage failure preserves composer and never submits an unprotected request', async () => {
  const onSubmitPrompt = vi.fn(async () => undefined)
  renderDetail({ onSubmitPrompt })
  const prompt = screen.getByLabelText('Prompt')
  fireEvent.change(prompt, { target: { value: 'Do not lose this' } })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Submit prompt' })).toBeEnabled())
  const original = window.localStorage.setItem.bind(window.localStorage)
  const spy = vi.spyOn(window.localStorage, 'setItem').mockImplementation((key, value) => {
    if (key.startsWith('gorchestra.pending-submission.')) throw new Error('quota')
    original(key, value)
  })
  fireEvent.keyDown(prompt, { key: 'Enter' })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Submit prompt' })).toBeEnabled())
  expect(prompt).toHaveValue('Do not lose this')
  expect(onSubmitPrompt).not.toHaveBeenCalled()
  spy.mockRestore()
})

test('session detail keeps session loading visible while initial chat history loads', () => {
  renderDetail({
    resolvingSessionID: 'sess_1',
    streamState: 'loading',
    events: [],
  })

  expect(screen.getByText('Loading session...')).toBeInTheDocument()
  expect(screen.queryByText('Loading chat history...')).not.toBeInTheDocument()
})

test('thinking indicator follows active reasoning events while running', () => {
  const { rerender } = renderDetail()

  expect(screen.queryByRole('status', { name: /thinking/i })).not.toBeInTheDocument()

  rerenderDetail(rerender, {
    session: { ...baseSession, status: 'running' },
    events: [event(1, 'agent.status.started', { provider_event_type: 'turn/started' })],
  })

  const thinkingStatus = screen.getByRole('status', { name: /thinking/i })
  expect(thinkingStatus).toBeInTheDocument()
  expect(screen.getByRole('log', { name: 'Chat messages' })).toContainElement(thinkingStatus)

  rerenderDetail(rerender, {
    session: { ...baseSession, status: 'running' },
    events: [
      event(1, 'agent.status.started', { provider_event_type: 'turn/started' }),
      event(2, 'agent.thinking.completed', {
        provider_event_type: 'item/completed',
        item_type: 'reasoning',
        item_id: 'rs_1',
        text: '',
      }),
    ],
  })

  expect(screen.queryByRole('status', { name: /thinking/i })).not.toBeInTheDocument()

  rerenderDetail(rerender, {
    session: { ...baseSession, status: 'running' },
    events: [
      event(1, 'agent.status.started', { provider_event_type: 'turn/started' }),
      event(2, 'agent.thinking.completed', {
        provider_event_type: 'item/completed',
        item_type: 'reasoning',
        item_id: 'rs_1',
        text: '',
      }),
      event(3, 'agent.thinking.started', {
        provider_event_type: 'item/started',
        item_type: 'reasoning',
        item_id: 'rs_2',
      }),
    ],
  })

  expect(screen.getByRole('status', { name: /thinking/i })).toBeInTheDocument()
})

test('running session shows working status after visible activity goes quiet', () => {
  renderDetail({
    session: { ...baseSession, status: 'running' },
    events: [event(1, 'agent.run.started', {})],
  })

  expect(screen.getByRole('status', { name: /working/i })).toBeInTheDocument()
})

test('running session suppresses working status while assistant text streams', () => {
  renderDetail({
    session: { ...baseSession, status: 'running' },
    events: [
      event(1, 'agent.run.started', {}),
      event(2, 'agent.message.delta', { item_id: 'msg_1', text: 'Streaming answer' }),
    ],
  })

  expect(screen.queryByRole('status', { name: /working/i })).not.toBeInTheDocument()
  expect(screen.getByText('Streaming answer')).toBeInTheDocument()
})

test('running session shows working status after streamed assistant text completes', () => {
  renderDetail({
    session: { ...baseSession, status: 'running' },
    events: [
      event(1, 'agent.run.started', {}),
      event(2, 'agent.message.delta', { item_id: 'msg_1', text: 'Streaming answer' }),
      event(3, 'agent.message.completed', { item_id: 'msg_1', text: 'Streaming answer' }),
    ],
  })

  expect(screen.getByRole('status', { name: /working/i })).toBeInTheDocument()
  expect(screen.getByText('Streaming answer')).toBeInTheDocument()
})

test('running session suppresses working status while a tool is active', () => {
  renderDetail({
    session: { ...baseSession, status: 'running' },
    events: [
      event(1, 'agent.run.started', {}),
      event(2, 'agent.message.completed', { item_id: 'msg_1', text: 'Running checks.' }),
      event(3, 'tool.call.started', { item_id: 'tool_1', command: 'sleep 20' }),
    ],
  })

  expect(screen.queryByRole('status', { name: /working/i })).not.toBeInTheDocument()
  expect(screen.getByText('sleep 20')).toBeInTheDocument()
})

test('running session prefers thinking status over quiet working status', () => {
  renderDetail({
    session: { ...baseSession, status: 'running' },
    events: [
      event(1, 'agent.run.started', {}),
      event(2, 'agent.thinking.started', {
        provider_event_type: 'item/started',
        item_type: 'reasoning',
        item_id: 'rs_1',
      }),
    ],
  })

  expect(screen.getByRole('status', { name: /thinking/i })).toBeInTheDocument()
  expect(screen.queryByRole('status', { name: /working/i })).not.toBeInTheDocument()
})

test('pending user input suppresses quiet working status', () => {
  renderDetail({
    session: { ...baseSession, status: 'running' },
    events: [
      event(1, 'agent.run.started', {}),
      event(2, 'agent.input.requested', {
        request_id: 'call_test',
        provider: 'codex',
        provider_event_type: 'item/tool/requestUserInput',
        item_id: 'call_test',
        questions: [
          {
            id: 'approval',
            header: 'Trust',
            question: 'Approve this action?',
            options: [{ label: 'Approve', description: 'Allow the action.' }],
          },
        ],
      }),
    ],
  })

  expect(screen.queryByRole('status', { name: /working/i })).not.toBeInTheDocument()
  expect(screen.getByText('Approve this action?')).toBeInTheDocument()
})

test('session detail uses matching floating headers on mobile and desktop', () => {
  renderDetail({ mobileLeadingAction: <button type="button">Open sessions</button> })

  const mobileHeader = screen.getByTestId('mobile-floating-session-header')
  expect(mobileHeader).toHaveClass('mobile-floating-header-shell')
  expect(mobileHeader).toHaveClass('lg:hidden')
  expect(within(mobileHeader).getByRole('button', { name: 'Open sessions' })).toBeInTheDocument()
  expect(screen.getByTestId('floating-session-header')).toHaveClass('hidden')
  expect(screen.getByTestId('floating-session-header')).toHaveClass('lg:block')
  expect(screen.queryByText(/Created:/)).not.toBeInTheDocument()
  expect(screen.queryByText(/Updated:/)).not.toBeInTheDocument()
  expect(screen.queryByText(/Last event:/)).not.toBeInTheDocument()
})

test('session headers omit the parent label and icon', () => {
  renderDetail({
    session: { ...baseSession, parent_session_id: 'sess_parent', lineage_depth: 1 },
    onSelectParent: () => undefined,
  })

  for (const headerTestID of ['mobile-floating-session-header', 'floating-session-header']) {
    const header = screen.getByTestId(headerTestID)
    expect(within(header).queryByText('Parent session')).not.toBeInTheDocument()
    expect(header.querySelector('.lucide-git-branch')).not.toBeInTheDocument()
  }
})

test('session header gives the title priority without inline settings controls', () => {
  renderDetail()

  const header = desktopFloatingHeader()
  const title = within(header).getByRole('heading', { name: 'Inspect repo' })

  expect(title.parentElement).toHaveClass('min-w-0', 'flex-1')
  expect(within(header).queryByRole('button', { name: 'Session settings' })).not.toBeInTheDocument()
  expect(within(header).queryByRole('button', { name: 'Edit session title' })).not.toBeInTheDocument()
})

test('composer stack floats over the transcript while reserving tail inset', () => {
  renderDetail()

  const bottomStack = screen.getByTestId('session-bottom-stack')
  expect(bottomStack).toHaveClass('pointer-events-auto')
  expect(bottomStack.parentElement).toHaveClass('absolute')
  expect(bottomStack.parentElement).toHaveClass('bottom-0')
  expect(bottomStack.parentElement).toHaveClass('session-bottom-safe-area')
  expect(bottomStack).toContainElement(screen.getByLabelText('Prompt'))
  expect(screen.getByText('No messages yet. Submit a prompt to start the chat.')).toBeInTheDocument()
})

test('chat presents session errors as a centered transcript status', () => {
  renderDetail({
    errorMessage: 'HTTP 502',
  })

  const transcript = screen.getByRole('log', { name: 'Chat messages' })
  const alert = within(transcript).getByRole('alert')
  const header = within(desktopFloatingHeader()).getByText('Inspect repo').closest('.command-chat-header')
  expect(within(desktopFloatingHeader()).queryByRole('alert')).not.toBeInTheDocument()
  expect(alert).toHaveTextContent('Chat issue')
  expect(alert).toHaveTextContent('HTTP 502')
  expect(alert).toHaveClass('mx-auto', 'justify-center', 'text-center')
  expect(header).toHaveClass('rounded-xl')
  expect(screen.queryByText(/Failed to load chat history/)).not.toBeInTheDocument()
})

type SessionDetailProps = ComponentProps<typeof SessionDetail>

function renderDetail(overrides: Partial<SessionDetailProps> = {}) {
  return render(<SessionDetail {...props(overrides)} />)
}

function rerenderDetail(rerender: (ui: ReactNode) => void, overrides: Partial<SessionDetailProps> = {}) {
  rerender(<SessionDetail {...props(overrides)} />)
}

function desktopFloatingHeader() {
  return screen.getByTestId('floating-session-header')
}

function props(overrides: Partial<SessionDetailProps>): SessionDetailProps {
  return {
    session: baseSession,
    events: [],
    streamState: 'connected',
    showDebugEvents: false,
    onSubmitPrompt: async () => undefined,
    onAnswerUserInput: async () => undefined,
    onCancel: async () => undefined,
    ...overrides,
  }
}

function event(seq: number, type: string, payload: Record<string, unknown>): AgentEvent {
  return {
    id: `evt_${seq}`,
    session_id: 'sess_1',
    seq,
    type,
    role: 'assistant',
    status: type.endsWith('.completed') ? 'completed' : 'started',
    payload,
    created_at: '2026-06-12T16:00:00Z',
  }
}

test('follow-up buttons append to the draft, focus it, and never send or duplicate after switching sessions', async () => {
  const onSubmitPrompt = vi.fn(async () => undefined)
  const events = [event(1, 'agent.message.completed', { text: ':codex-followup[Check status]{prompt="Check the job status."}' })]
  const view = renderDetail({ events, onSubmitPrompt })
  const prompt = screen.getByLabelText('Prompt')
  fireEvent.change(prompt, { target: { value: 'Existing draft' } })
  fireEvent.click(screen.getByRole('button', { name: 'Check status' }))
  await waitFor(() => expect(prompt).toHaveValue('Existing draft\n\nCheck the job status.'))
  expect(prompt).toHaveFocus()
  expect((prompt as HTMLTextAreaElement).selectionStart).toBe('Existing draft\n\nCheck the job status.'.length)
  expect(onSubmitPrompt).not.toHaveBeenCalled()
  await waitFor(() => expect(window.localStorage.getItem('gorchestra.session-composer.sess_1')).toContain('Check the job status.'))
  rerenderDetail(view.rerender, { session: { ...baseSession, id: 'sess_2' }, events: [], onSubmitPrompt })
  expect(screen.getByLabelText('Prompt')).toHaveValue('')
  rerenderDetail(view.rerender, { events, onSubmitPrompt })
  expect(screen.getByLabelText('Prompt')).toHaveValue('Existing draft\n\nCheck the job status.')
})

test('follow-ups remain editable while a run is active without submitting or steering', async () => {
  const onSubmitPrompt = vi.fn(async () => undefined)
  renderDetail({ session: { ...baseSession, status: 'running' }, onSubmitPrompt,
    events: [event(1, 'agent.message.completed', { text: ':codex-followup[Next check]{prompt="Run the next check."}' })] })
  fireEvent.click(screen.getByRole('button', { name: 'Next check' }))
  await waitFor(() => expect(screen.getByLabelText('Prompt')).toHaveValue('Run the next check.'))
  expect(onSubmitPrompt).not.toHaveBeenCalled()
})
