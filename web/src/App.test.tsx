import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from '@/App'
import { applySessionEvent } from '@/lib/session-events'
import { clearAPIRequestCachesForTest, type AgentEvent, type Session } from '@/lib/api'
import { clearSessionEventCacheForTest } from '@/hooks/use-session-events'
import { readCachedSessionEvents, writeCachedSession, writeCachedSessionEvents } from '@/lib/session-cache'
import { createFakeIndexedDB } from '@/test/fake-indexeddb'
import {
  clearNotificationAttentionCacheForTest,
  writeNotificationAttention,
} from '@/lib/notification-attention'

vi.mock('@monaco-editor/react', () => ({
  default: ({ value, onChange }: { value?: string; onChange?: (value: string | undefined) => void }) => (
    <textarea
      aria-label="File editor"
      value={value ?? ''}
      onChange={(event) => onChange?.(event.currentTarget.value)}
    />
  ),
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {}
  },
}))

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80
    rows = 24

    loadAddon() {}
    open() {}
    write() {}
    dispose() {}
    onData() {
      return { dispose() {} }
    }
  },
}))

const firstSession = session('sess_1', 'Inspect repo', '2026-06-12T16:02:00Z')
const secondSession = session('sess_2', 'Write docs', '2026-06-12T16:01:00Z')

beforeEach(() => {
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: 1024 })
  window.history.replaceState({}, '', '/sessions/sess_1')
  window.localStorage.clear()
  clearSessionEventCacheForTest()
  clearAPIRequestCachesForTest()
  clearNotificationAttentionCacheForTest()
  document.head.innerHTML = '<link rel="icon" type="image/svg+xml" href="/favicon.svg" />'
  FakeEventSource.instances = []
  vi.stubGlobal('fetch', fetchMock())
  vi.stubGlobal('EventSource', FakeEventSource)
  vi.stubGlobal('WebSocket', FakeWebSocket)
  vi.stubGlobal('ResizeObserver', FakeResizeObserver)
  vi.stubGlobal('matchMedia', matchMediaMock)
})

test('uses a selected session from the initial list without refetching its detail', async () => {
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))
  expect(fetch.mock.calls.filter(([url]) => String(url) === '/api/sessions/sess_1')).toHaveLength(0)
})

test('a rejected Stop request cannot trigger queue-to-composer restoration', async () => {
  const normalFetch = fetchMock({ sessions: [{ ...firstSession, status: 'running' }, secondSession] })
  const queued = { id: 'queue_1', session_id: 'sess_1', seq: 1, content: 'Leave this queued', created_at: firstSession.created_at }
  const fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === '/api/sessions/sess_1/cancel') return new Response('{"error":"Run already stopped"}', { status: 409 })
    if (String(url) === '/api/sessions/sess_1/queued-messages') return jsonResponse({ messages: [queued] })
    return normalFetch(url, init)
  })
  vi.stubGlobal('fetch', fetch)
  render(<App />)
  await screen.findByRole('button', { name: 'Move queued message 1 to composer' })
  fireEvent.click(screen.getByRole('button', { name: 'Cancel running session' }))
  await screen.findByText('Run already stopped')
  await waitFor(() => expect(screen.getByRole('button', { name: 'Cancel running session' })).toBeEnabled())
  expect(screen.getByLabelText('Prompt')).toHaveValue('')
  expect(screen.getByRole('button', { name: 'Move queued message 1 to composer' })).toBeInTheDocument()
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'DELETE')).toHaveLength(0)
})

test('only the visible selected session acknowledges SSE notifications for this device', async () => {
  const originalServiceWorker = Object.getOwnPropertyDescriptor(navigator, 'serviceWorker')
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  const showNotification = vi.fn()
  const subscription = {
    endpoint: 'https://push.example.test/this-device',
    toJSON: () => ({ endpoint: 'https://push.example.test/this-device', keys: { p256dh: 'key', auth: 'auth' } }),
  }
  vi.stubGlobal('isSecureContext', true)
  vi.stubGlobal('Notification', { permission: 'granted' })
  vi.stubGlobal('PushManager', class {})
  const registration = { pushManager: { getSubscription: vi.fn().mockResolvedValue(subscription) }, showNotification }
  Object.defineProperty(navigator, 'serviceWorker', {
    configurable: true,
    value: { getRegistration: vi.fn().mockResolvedValue(registration), ready: Promise.resolve(registration) },
  })
  const normalFetch = fetchMock()
  const fetch = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url).startsWith('/api/notifications/')) return Promise.resolve(jsonResponse({ enabled: true, acknowledged: true }))
    return normalFetch(url, init)
  })
  vi.stubGlobal('fetch', fetch)
  const app = render(<App />)
  try {
    const source = await findEventSource('/api/sessions/activity/stream')
    const acknowledgements = () => fetch.mock.calls.filter(([url]) => String(url) === '/api/notifications/acknowledge')
    const completed = { ...event(8, 'agent.run.completed', {}), global_seq: 10 }
    act(() => source.emit(completed))
    await waitFor(() => expect(acknowledgements()).toHaveLength(1))
    expect(JSON.parse(String(acknowledgements()[0][1]?.body))).toEqual({ endpoint: subscription.endpoint, session_id: 'sess_1', seq: 8 })
    expect(acknowledgements()[0][1]?.method).toBe('POST')

    act(() => {
      source.emit(completed) // Replay must not create a duplicate ACK or alert.
      source.emit({ ...event(9, 'agent.run.completed', {}, 'sess_2'), global_seq: 11 })
      source.emit({ ...event(10, 'agent.message.completed', { text: 'Not a notification' }), global_seq: 12 })
    })
    visibility.mockReturnValue('hidden')
    act(() => source.emit({ ...event(11, 'agent.run.failed', {}), global_seq: 13 }))
    visibility.mockReturnValue('visible')
    act(() => source.emit({ ...event(12, 'agent.permission.requested', { request_id: 'approval_1' }), global_seq: 14 }))
    await waitFor(() => expect(acknowledgements()).toHaveLength(2))
    expect(JSON.parse(String(acknowledgements()[1][1]?.body)).seq).toBe(12)

    fireEvent.click(screen.getAllByRole('button', { name: /Write docs/ })[0])
    act(() => {
      source.emit({ ...event(13, 'agent.run.completed', {}), global_seq: 15 })
      source.emit({ ...event(14, 'agent.run.completed', {}, 'sess_2'), global_seq: 16 })
    })
    await waitFor(() => expect(acknowledgements()).toHaveLength(3))
    expect(JSON.parse(String(acknowledgements()[2][1]?.body)).session_id).toBe('sess_2')
    expect(showNotification).not.toHaveBeenCalled()
  } finally {
    app.unmount()
    visibility.mockRestore()
    if (originalServiceWorker) Object.defineProperty(navigator, 'serviceWorker', originalServiceWorker)
    else Reflect.deleteProperty(navigator, 'serviceWorker')
  }
})

test('Cmd/Ctrl+D toggles client debug without restarting SSE, refetching history, or changing the draft', async () => {
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)
  render(<App />)
  const source = await findEventSource('/api/sessions/activity/stream')
  const prompt = screen.getByRole('textbox', { name: 'Prompt' })
  fireEvent.change(prompt, { target: { value: 'Unsent draft stays here' } })
  prompt.focus()
  expect(screen.queryByRole('complementary', { name: 'Client debug' })).not.toBeInTheDocument()
  const historyRequests = () => fetch.mock.calls.filter(([url]) => /\/events\?|\/api\/sessions\?/.test(String(url))).length
  const requestsBefore = historyRequests()

  expect(fireEvent.keyDown(prompt, { key: 'd', metaKey: true })).toBe(false)
  expect(screen.getByRole('complementary', { name: 'Client debug' })).toBeVisible()
  expect(window.location.search).toBe('?debug=1')
  fireEvent.keyDown(prompt, { key: 'd', metaKey: true, repeat: true })
  expect(screen.getByRole('complementary', { name: 'Client debug' })).toBeVisible()
  expect(prompt).toHaveValue('Unsent draft stays here')
  expect(prompt).toHaveFocus()
  expect(FakeEventSource.instances).toHaveLength(1)
  expect(source.closed).toBe(false)
  expect(historyRequests()).toBe(requestsBefore)

  fireEvent.keyDown(prompt, { key: 'd', ctrlKey: true })
  expect(screen.queryByRole('complementary', { name: 'Client debug' })).not.toBeInTheDocument()
  expect(window.location.search).toBe('')
  fireEvent.keyDown(prompt, { key: 'd', metaKey: true, shiftKey: true })
  fireEvent.keyDown(prompt, { key: 'd', metaKey: true, isComposing: true })
  expect(screen.queryByRole('complementary', { name: 'Client debug' })).not.toBeInTheDocument()
})

test.each(['debug=1', 'debug-scroll=1', 'viewportDebug=true'])('debug URLs survive session navigation: %s', async (query) => {
  window.history.replaceState({}, '', `/sessions/sess_1?${query}`)
  render(<App />)
  await findEventSource('/api/sessions/activity/stream')
  expect(screen.getByRole('complementary', { name: 'Client debug' })).toBeVisible()
  fireEvent.click(screen.getAllByRole('button', { name: /Write docs/ })[0])
  expect(window.location.pathname).toBe('/sessions/write-docs')
  expect(window.location.search).toBe('?debug=1')
  fireEvent.keyDown(window, { key: 's', metaKey: true })
  expect(window.location.pathname).toBe('/skills')
  expect(window.location.search).toBe('?debug=1')
  fireEvent.click(screen.getByRole('button', { name: 'Close debug view' }))
  expect(window.location.search).toBe('')
})

test('debug distinguishes the last global SSE event from the last selected-session message', async () => {
  render(<App />)
  const source = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    source.emit({ ...event(5, 'agent.message.completed', { text: 'Selected answer' }), global_seq: 10 })
    source.emit({ ...event(6, 'tool.call.completed', { command: 'private command' }, 'sess_2'), global_seq: 11 })
  })
  fireEvent.keyDown(window, { key: 'd', metaKey: true })
  const panel = within(screen.getByRole('complementary', { name: 'Client debug' }))
  await waitFor(() => expect(panel.getByText('sess_2 #6 · global 11')).toBeVisible(), { timeout: 2500 })
  expect(panel.getByText('tool.call.completed · durable')).toBeVisible()
  expect(panel.getByText('Selected answer')).toBeVisible()
  expect(panel.queryByText('private command')).not.toBeInTheDocument()
  expect(panel.getByText('11 · opened after 0')).toBeVisible()
  expect(document.querySelector('[data-debug-scroll-readout]')).toBeInTheDocument()
})

test('debug reports stream errors and foreground resyncs without changing recovery', async () => {
  render(<App />)
  const source = await findEventSource('/api/sessions/activity/stream')
  act(() => source.fail())
  fireEvent.keyDown(window, { key: 'd', metaKey: true })
  const panel = within(screen.getByRole('complementary', { name: 'Client debug' }))
  expect(panel.getAllByText('reconnecting · source closed').length).toBeGreaterThan(0)
  expect(panel.getByText('1 · errors 1')).toBeVisible()
  act(() => document.dispatchEvent(new Event('visibilitychange')))
  await waitFor(() => expect(panel.getByText(/foreground return/)).toBeVisible(), { timeout: 2500 })
  expect(FakeEventSource.instances.filter((item) => !item.closed)).toHaveLength(1)
})

test('initial gateway failure recovers automatically without an online event or reload', async () => {
  const normalFetch = fetchMock()
  let unavailable = true
  vi.stubGlobal('fetch', vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === '/api/sessions?limit=50' && unavailable) return Promise.resolve(new Response('<html>gateway</html>', { status: 502 }))
    return normalFetch(url, init)
  }))
  render(<App />)
  await screen.findByText('Session unavailable offline')
  unavailable = false
  await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1), { timeout: 8000 })
  expect(screen.queryByTestId('offline-session-status')).not.toBeInTheDocument()
  expect(screen.queryByText('HTTP 502')).not.toBeInTheDocument()
}, 10000)

test('offline transition keeps the dirty file editor mounted', async () => {
  vi.stubGlobal('fetch', fetchMock({ fileEntry: true }))
  window.history.replaceState({}, '', '/sessions/inspect-repo/files/main.go')
  render(<App />)
  const editor = await screen.findByLabelText('File editor')
  fireEvent.change(editor, { target: { value: 'package unsaved\n' } })
  act(() => {
    Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
    window.dispatchEvent(new Event('offline'))
  })
  expect(screen.getByLabelText('File editor')).toBe(editor)
  expect(editor).toHaveValue('package unsaved\n')
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
})

test('invalid session link cannot enable an unrelated composer', async () => {
  window.history.replaceState({}, '', '/sessions/does-not-exist')
  render(<App />)
  await screen.findByRole('heading', { name: 'Session unavailable' })
  expect(window.location.pathname).toBe('/sessions/does-not-exist')
  expect(screen.queryByRole('textbox', { name: 'Prompt' })).not.toBeInTheDocument()
})

test('offline root launch restores cached sessions, saved history, and a local-only draft', async () => {
  const user = userEvent.setup()
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: false })
  window.history.replaceState({}, '', '/')
  vi.stubGlobal('indexedDB', createFakeIndexedDB())
  await writeCachedSession(firstSession)
  await writeCachedSession(secondSession)
  await writeCachedSessionEvents('sess_2', [
    event(1, 'user.message.completed', { text: 'Saved offline prompt' }, 'sess_2'),
    event(2, 'agent.message.completed', { text: 'Saved offline answer' }, 'sess_2'),
  ], false)
  window.localStorage.setItem('gorchestra.last-selected-session.v1', 'sess_2')
  const onlineFetch = fetchMock()
  let networkAvailable = false
  const fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
    networkAvailable ? onlineFetch(input, init) : Promise.reject(new TypeError('Failed to fetch')),
  )
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  expect(await screen.findByText('Saved offline answer')).toBeInTheDocument()
  expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0)
  expect(screen.getAllByText('Write docs').length).toBeGreaterThan(0)
  expect(screen.getByTestId('offline-session-status')).toHaveTextContent('Showing saved history')
  expect(screen.queryByText('Failed to fetch')).not.toBeInTheDocument()

  const prompt = screen.getByRole('textbox', { name: 'Prompt' })
  await user.type(prompt, 'Draft while disconnected')
  expect(prompt).toHaveValue('Draft while disconnected')
  expect(screen.getByRole('button', { name: 'Submit prompt' })).toBeDisabled()
  expect(screen.getByRole('button', { name: /Queue message/ })).toBeDisabled()
  expect(window.localStorage.getItem('gorchestra.session-composer.sess_2')).toContain('Draft while disconnected')
  expect(fetch).not.toHaveBeenCalled()
  expect(FakeEventSource.instances).toHaveLength(0)

  await user.click(screen.getAllByRole('button', { name: /Inspect repo/ })[0])
  expect(await screen.findByText("This session's history hasn't been saved on this device yet.")).toBeInTheDocument()

  networkAvailable = true
  Object.defineProperty(navigator, 'onLine', { configurable: true, value: true })
  act(() => window.dispatchEvent(new Event('online')))
  await waitFor(() =>
    expect(fetch.mock.calls.some(([url]) => String(url) === '/api/sessions?limit=50')).toBe(true),
  )
  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])
  expect(screen.getByRole('textbox', { name: 'Prompt' })).toHaveValue('Draft while disconnected')
  await waitFor(() => expect(screen.getByRole('button', { name: 'Submit prompt' })).toBeEnabled())
  expect(screen.queryByTestId('offline-session-status')).not.toBeInTheDocument()
})

test('global activity stays connected when the selected session changes', async () => {
  const user = userEvent.setup()
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)
  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  expect(activitySource.url).toContain('after_cursor=0')
  expect(activitySource.url).toContain('watch_session_id=sess_1')

  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])
  expect(FakeEventSource.instances.filter((source) => source.url.startsWith('/api/sessions/activity/stream')))
    .toHaveLength(1)
  await waitFor(() => expect(
    fetch.mock.calls.some(
      ([url, init]) => String(url) === '/api/sessions/activity/watch' && init?.method === 'PUT',
    ),
  ).toBe(true))
})

test('global activity multiplexes selected transient output into the transcript', async () => {
  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    activitySource.emit({
      ...event(5, 'agent.message.delta', { item_id: 'msg_1', text: 'Multiplexed answer' }),
      transient: true,
    })
  })

  expect(await screen.findByText('Multiplexed answer')).toBeInTheDocument()
  expect(FakeEventSource.instances).toHaveLength(1)
})

test('switching to a background session restores its complete in-progress message', async () => {
  const user = userEvent.setup()
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)
  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  expect(activitySource.url).toContain('live_scope=all')
  act(() => {
    activitySource.emit({
      ...event(5, 'agent.message.delta', { message_id: 'msg_background', text: 'First half ' }, 'sess_2'),
      transient: true,
    })
    activitySource.emit({
      ...event(6, 'agent.message.delta', { message_id: 'msg_background', text: 'second half' }, 'sess_2'),
      transient: true,
    })
  })

  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])
  expect(await screen.findByText('First half second half')).toBeInTheDocument()
  expect(FakeEventSource.instances).toHaveLength(1)
})

test('live snapshot repairs an incomplete background message before selection', async () => {
  const user = userEvent.setup()
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)
  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    activitySource.emit({
      ...event(6, 'agent.message.delta', { message_id: 'msg_background', text: 'second half' }, 'sess_2'),
      transient: true,
    })
    activitySource.emitControl('session.live.snapshot', {
      events: [{
        ...event(6, 'agent.message.delta', { message_id: 'msg_background', text: 'First half second half' }, 'sess_2'),
        id: 'snapshot_background',
        transient: true,
      }],
    })
  })

  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])
  expect(await screen.findByText('First half second half')).toBeInTheDocument()
  expect(screen.queryByText('second half')).not.toBeInTheDocument()
})

test('selecting a background session keeps its global events while hydrating the server tail', async () => {
  const user = userEvent.setup()
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)
  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    activitySource.emit({
      ...event(5, 'agent.message.completed', { text: 'Background answer' }, 'sess_2'),
      global_seq: 45,
    })
  })
  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])

  expect(await screen.findByText('Background answer')).toBeInTheDocument()
  expect(
    fetch.mock.calls.filter(
      ([url]) => String(url) === '/api/sessions/sess_2/events?tail=true&turns=50&max_bytes=2097152',
    ),
  ).toHaveLength(1)
  expect(FakeEventSource.instances).toHaveLength(1)
})

test('background queue lifecycle is current when its session is selected', async () => {
  const user = userEvent.setup()
  const baseFetch = fetchMock()
  const fetch = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === '/api/sessions/sess_2/queued-messages') {
      return Promise.resolve(jsonResponse({ messages: [] }))
    }
    return baseFetch(url, init)
  })
  vi.stubGlobal('fetch', fetch)
  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    activitySource.emit({
      ...event(5, 'user.message.queued', { queue_item_id: 'queue_1', text: 'Background follow-up' }, 'sess_2'),
      global_seq: 45,
    })
  })
  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])

  expect(FakeEventSource.instances).toHaveLength(1)
  expect(await screen.findByText('Background follow-up')).toBeInTheDocument()
  act(() => {
    activitySource.emit({
      ...event(6, 'user.message.queue.removed', { queue_item_id: 'queue_1' }, 'sess_2'),
      global_seq: 46,
    })
  })
  await waitFor(() => expect(screen.queryByText('Background follow-up')).not.toBeInTheDocument())
  expect(fetch.mock.calls.filter(([url]) => String(url) === '/api/sessions/sess_2/queued-messages')).toHaveLength(1)
})

test('known terminal activity does not refetch session details', async () => {
  const runningSecondSession: Session = { ...secondSession, status: 'running', last_event_seq: 4, event_count: 4 }
  const fetch = fetchMock({ sessions: [firstSession, runningSecondSession] })
  vi.stubGlobal('fetch', fetch)
  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    activitySource.emit({ ...event(5, 'agent.run.completed', {}, 'sess_2'), global_seq: 46 })
  })

  await waitFor(() => expect(screen.getByRole('img', { name: 'Session has unseen results' })).toBeInTheDocument())
  expect(fetch.mock.calls.filter(([url]) => String(url) === '/api/sessions/sess_2')).toHaveLength(0)
})

test('overview coalesces an activity burst into one dashboard refresh', async () => {
  window.history.replaceState({}, '', '/')
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  await waitFor(() => {
    expect(fetch.mock.calls.filter(([url]) => String(url).startsWith('/api/dashboard?'))).toHaveLength(1)
    expect(fetch.mock.calls.filter(([url]) => String(url).startsWith('/api/dashboard/runs?'))).toHaveLength(1)
  })

  act(() => {
    for (let seq = 10; seq < 20; seq += 1) {
      activitySource.emit(event(seq, 'agent.message.delta', { text: `chunk ${seq}` }, 'sess_2'))
    }
  })

  await waitFor(
    () => {
      expect(fetch.mock.calls.filter(([url]) => String(url).startsWith('/api/dashboard?'))).toHaveLength(2)
      expect(fetch.mock.calls.filter(([url]) => String(url).startsWith('/api/dashboard/runs?'))).toHaveLength(2)
    },
    { timeout: 2000 },
  )
})

test('global activity reconnects from its latest cursor without refetching the session list', async () => {
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  await waitFor(() =>
    expect(fetch.mock.calls.filter(([url]) => String(url) === '/api/sessions?limit=50')).toHaveLength(1),
  )

  act(() => {
    activitySource.emit({ ...event(10, 'agent.message.completed', { text: 'update' }, 'sess_2'), global_seq: 42 })
    activitySource.fail()
  })
  expect(fetch.mock.calls.filter(([url]) => String(url) === '/api/sessions?limit=50')).toHaveLength(1)

  await waitFor(
    () => {
      expect(FakeEventSource.instances.filter((source) => source.url.startsWith('/api/sessions/activity/stream')))
        .toHaveLength(2)
      expect(fetch.mock.calls.filter(([url]) => String(url) === '/api/sessions?limit=50')).toHaveLength(1)
      expect(FakeEventSource.instances.at(-1)?.url).toContain('after_cursor=42')
    },
    { timeout: 10_000 },
  )
})

test('replay overflow reconciles selected and background transcripts without opening per-session streams', async () => {
  const user = userEvent.setup()
  const baseFetch = fetchMock()
  let snapshots = 0
  const tailRequests: string[] = []
  const fetch = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === '/api/sessions?limit=50') {
      snapshots += 1
      return Promise.resolve(jsonResponse({
        sessions: [firstSession, secondSession],
        event_cursor: snapshots === 1 ? 12 : 80,
      }))
    }
    const tail = String(url).match(/^\/api\/sessions\/(sess_[12])\/events\?tail=/)
    if (tail) {
      tailRequests.push(tail[1])
      return Promise.resolve(jsonResponse({ events: [
        event(1, 'user.message.completed', { text: `Original ${tail[1]}` }, tail[1]),
        ...(snapshots > 1 ? [event(2, 'agent.message.completed', { text: `Recovered ${tail[1]}` }, tail[1])] : []),
      ] }))
    }
    return baseFetch(url, init)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream?after_cursor=12')
  await screen.findByText('Original sess_1')
  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])
  await screen.findByText('Original sess_2')
  await user.click(screen.getAllByRole('button', { name: /Inspect repo/ })[0])
  await screen.findByText('Original sess_1')
  act(() => activitySource.emitControl('stream.resync.required', { cursor: 79 }))

  await waitFor(() => {
    expect(fetch.mock.calls.filter(([url]) => String(url) === '/api/sessions?limit=50')).toHaveLength(2)
    expect(FakeEventSource.instances.at(-1)?.url).toContain('after_cursor=80')
  })
  await screen.findByText('Recovered sess_1')
  expect(tailRequests).toEqual(['sess_1', 'sess_2', 'sess_1'])
  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])
  await screen.findByText('Recovered sess_2')
  expect(tailRequests).toEqual(['sess_1', 'sess_2', 'sess_1', 'sess_2'])
  expect(FakeEventSource.instances.filter((source) => !source.closed)).toHaveLength(1)
  // A queued callback from the replaced stream must not jump the new cursor.
  act(() => activitySource.emit({ ...event(90, 'user.message.completed', { text: 'Obsolete stream' }, 'sess_2'), global_seq: 900 }))
  expect(screen.queryByText('Obsolete stream')).not.toBeInTheDocument()
})

test.each(['visibilitychange', 'pageshow'] as const)('foreground %s repairs a silent stream without an offline event', async (trigger) => {
  const baseFetch = fetchMock()
  let snapshots = 0
  let resolveSnapshot!: (response: Response) => void
  const fetch = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === '/api/sessions?limit=50') {
      snapshots += 1
      if (snapshots === 1) return Promise.resolve(jsonResponse({ sessions: [firstSession], event_cursor: 12 }))
      return new Promise<Response>((resolve) => { resolveSnapshot = resolve })
    }
    if (String(url).includes('/sess_1/events?tail=')) {
      return Promise.resolve(jsonResponse({ events: [
        event(1, 'user.message.completed', { text: 'Before backgrounding' }),
        ...(snapshots > 1 ? [event(2, 'agent.message.completed', { text: 'While this device was asleep' })] : []),
      ] }))
    }
    return baseFetch(url, init)
  })
  vi.stubGlobal('fetch', fetch)
  render(<App />)
  await screen.findByText('Before backgrounding')
  const oldSource = await findEventSource('/api/sessions/activity/stream')
  const prompt = screen.getByRole('textbox', { name: 'Prompt' })
  fireEvent.change(prompt, { target: { value: 'Keep my draft' } })
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  act(() => {
    if (trigger === 'visibilitychange') document.dispatchEvent(new Event('visibilitychange'))
    else window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    // Browsers may dispatch both on the same return; coalesce the recovery.
    window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
  })
  expect(oldSource.closed).toBe(true)
  expect(snapshots).toBe(2)
  await act(async () => resolveSnapshot(jsonResponse({ sessions: [firstSession], event_cursor: 80 })))
  await screen.findByText('While this device was asleep')
  expect(prompt).toHaveValue('Keep my draft')
  expect(FakeEventSource.instances.at(-1)?.url).toContain('after_cursor=80')
  expect(FakeEventSource.instances.filter((source) => !source.closed)).toHaveLength(1)
  expect(fetch.mock.calls.filter(([url]) => String(url).includes('/events?tail='))).toHaveLength(2)
  visibility.mockRestore()
})

test('notification launch keeps the selected finished session unseen until deliberately selected', async () => {
  const user = userEvent.setup()
  const setAppBadge = vi.fn(() => Promise.resolve())
  Object.defineProperty(navigator, 'setAppBadge', { configurable: true, value: setAppBadge })
  vi.stubGlobal('indexedDB', createFakeIndexedDB())
  window.history.replaceState({}, '', '/sessions/sess_1?notification_seq=5')
  vi.stubGlobal('fetch', fetchMock({
    sessions: [{ ...firstSession, last_event_seq: 5, event_count: 5 }],
    events: [event(5, 'agent.run.completed', {})],
  }))

  render(<App />)

  await waitFor(() =>
    expect(screen.getByRole('img', { name: 'Session has unseen results' })).toHaveClass('bg-[hsl(var(--warning))]'),
  )
  await waitFor(() => expect(setAppBadge).toHaveBeenCalledWith(1))
  expect(window.location.search).not.toContain('notification_seq')

  await user.click(screen.getAllByRole('button', { name: /Inspect repo/ })[0])

  await waitFor(() => expect(screen.queryByRole('img', { name: 'Session has unseen results' })).not.toBeInTheDocument())
  await waitFor(() => expect(setAppBadge).toHaveBeenCalledWith(0))
})

test('background notification attention survives app launch for the selected session', async () => {
  const setAppBadge = vi.fn(() => Promise.resolve())
  Object.defineProperty(navigator, 'setAppBadge', { configurable: true, value: setAppBadge })
  vi.stubGlobal('indexedDB', createFakeIndexedDB())
  await writeNotificationAttention('sess_1', 5)
  window.history.replaceState({}, '', '/sessions/sess_1')
  vi.stubGlobal('fetch', fetchMock({
    sessions: [{ ...firstSession, last_event_seq: 5, event_count: 5 }],
    events: [event(5, 'agent.run.completed', {})],
  }))

  render(<App />)

  await waitFor(() =>
    expect(screen.getByRole('img', { name: 'Session has unseen results' })).toHaveClass('bg-[hsl(var(--warning))]'),
  )
  await waitFor(() => expect(setAppBadge).toHaveBeenCalledWith(1))
})

test('server notification attention survives app launch for the selected session', async () => {
  const user = userEvent.setup()
  const setAppBadge = vi.fn(() => Promise.resolve())
  Object.defineProperty(navigator, 'setAppBadge', { configurable: true, value: setAppBadge })
  window.history.replaceState({}, '', '/sessions/sess_1')
  const fetch = fetchMock({
    sessions: [{ ...firstSession, event_count: 5, last_event_seq: 5, notification_attention_seq: 5 }],
    events: [event(5, 'agent.run.completed', {})],
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await waitFor(() =>
    expect(screen.getByRole('img', { name: 'Session has unseen results' })).toHaveClass('bg-[hsl(var(--warning))]'),
  )
  await waitFor(() => expect(setAppBadge).toHaveBeenCalledWith(1))

  await user.click(screen.getAllByRole('button', { name: /Inspect repo/ })[0])

  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/sess_1/notification-attention/clear',
      expect.objectContaining({ method: 'POST', headers: expect.objectContaining({ Accept: 'application/json' }) }),
    ),
  )
  await waitFor(() => expect(screen.queryByRole('img', { name: 'Session has unseen results' })).not.toBeInTheDocument())
  await waitFor(() => expect(setAppBadge).toHaveBeenCalledWith(0))
})

test('focusing the composer clears finished-turn notification attention', async () => {
  const user = userEvent.setup()
  const setAppBadge = vi.fn(() => Promise.resolve())
  Object.defineProperty(navigator, 'setAppBadge', { configurable: true, value: setAppBadge })
  window.history.replaceState({}, '', '/sessions/sess_1')
  const fetch = fetchMock({
    sessions: [{ ...firstSession, event_count: 5, last_event_seq: 5, notification_attention_seq: 5 }],
    events: [event(5, 'agent.run.completed', {})],
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await waitFor(() =>
    expect(screen.getByRole('img', { name: 'Session has unseen results' })).toHaveClass('bg-[hsl(var(--warning))]'),
  )

  await user.click(screen.getByRole('textbox', { name: 'Prompt' }))

  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/sess_1/notification-attention/clear',
      expect.objectContaining({ method: 'POST', headers: expect.objectContaining({ Accept: 'application/json' }) }),
    ),
  )
  await waitFor(() => expect(screen.queryByRole('img', { name: 'Session has unseen results' })).not.toBeInTheDocument())
  await waitFor(() => expect(setAppBadge).toHaveBeenCalledWith(0))
})

test('dismiss all notifications clears every unseen session without opening them', async () => {
  const user = userEvent.setup()
  const setAppBadge = vi.fn(() => Promise.resolve())
  Object.defineProperty(navigator, 'setAppBadge', { configurable: true, value: setAppBadge })
  const fetch = fetchMock({
    sessions: [
      { ...firstSession, event_count: 5, last_event_seq: 5, notification_attention_seq: 5 },
      { ...secondSession, event_count: 7, last_event_seq: 7, notification_attention_seq: 7 },
    ],
    events: [event(5, 'agent.run.completed', {})],
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await waitFor(() => expect(screen.getAllByRole('img', { name: 'Session has unseen results' })).toHaveLength(2))
  await user.click(screen.getByRole('button', { name: 'Notifications, 2 unread' }))
  await user.click(screen.getByRole('menuitem', { name: 'Dismiss all notifications' }))

  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/notification-attention/clear',
      expect.objectContaining({ method: 'POST', headers: expect.objectContaining({ Accept: 'application/json' }) }),
    ),
  )
  expect(screen.queryByRole('img', { name: 'Session has unseen results' })).not.toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: 'Dismiss all notifications' })).not.toBeInTheDocument()
  await waitFor(() => expect(setAppBadge).toHaveBeenCalledWith(0))
})

afterEach(() => {
  vi.unstubAllGlobals()
})

test('selecting a session updates the browser route', async () => {
  const user = userEvent.setup()
  window.history.replaceState({}, '', '/')

  render(<App />)

  expect(await screen.findByRole('heading', { name: 'Your work at a glance' })).toBeInTheDocument()
  expect(window.location.pathname).toBe('/')

  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])

  await waitFor(() => expect(window.location.pathname).toBe('/sessions/write-docs'))
  await waitFor(() => expect(screen.getByRole('textbox', { name: 'Prompt' })).toHaveFocus())
})

test('switching app views updates the session route and browser history', async () => {
  const user = userEvent.setup()

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo'))

  await user.click(screen.getAllByRole('button', { name: 'Show console' })[0])
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/console'))
  expect(
    screen
      .getAllByRole('button', { name: 'Show console' })
      .some((button) => button.getAttribute('aria-pressed') === 'true'),
  ).toBe(true)

  await user.click(screen.getAllByRole('button', { name: 'Show files' })[0])
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/files'))
  expect(
    screen
      .getAllByRole('button', { name: 'Show files' })
      .some((button) => button.getAttribute('aria-pressed') === 'true'),
  ).toBe(true)

  await act(async () => {
    window.history.back()
    await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/console'))
  })
  expect(
    screen
      .getAllByRole('button', { name: 'Show console' })
      .some((button) => button.getAttribute('aria-pressed') === 'true'),
  ).toBe(true)

  await act(async () => {
    window.history.back()
    await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo'))
  })
  expect(
    screen
      .getAllByRole('button', { name: 'Show chat' })
      .some((button) => button.getAttribute('aria-pressed') === 'true'),
  ).toBe(true)
})

test.each(['console', 'files', 'settings'])('changing sessions from %s returns to messages', async (view) => {
  const user = userEvent.setup()
  window.history.replaceState({}, '', `/sessions/sess_1/${view}`)

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Write docs').length).toBeGreaterThan(0))
  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])

  await waitFor(() => expect(window.location.pathname).toBe('/sessions/write-docs'))
  expect(
    screen
      .getAllByRole('button', { name: 'Show chat' })
      .some((button) => button.getAttribute('aria-pressed') === 'true'),
  ).toBe(true)
  expect(screen.getByRole('textbox', { name: 'Prompt' })).toHaveFocus()
})

test('the desktop rail picker persists its selected utility', async () => {
  const user = userEvent.setup()
  render(<App />)

  await user.click(await screen.findByRole('button', { name: 'Rail content: Files' }))
  await user.click(screen.getByRole('menuitemradio', { name: 'Blocks' }))

  await waitFor(() => expect(window.localStorage.getItem('gorchestra.rail-content.v1')).toBe('blocks'))
  expect(screen.getByRole('region', { name: 'Blocks game' })).toBeInTheDocument()
  expect(screen.getByText('Activity')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Archive selected session' })).toBeInTheDocument()
})

test('a conversation map segment opens chat at its event sequence', async () => {
  const user = userEvent.setup()
  const events = [
    { ...event(1, 'user.message.completed', { text: 'Inspect the build' }), role: 'user' },
    event(2, 'agent.message.completed', { text: 'The build passes.' }),
  ]
  window.localStorage.setItem('gorchestra.rail-content.v1', 'conversation-map')
  const baseFetch = fetchMock({ events })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
      if (String(url) === '/api/sessions/sess_1/events?around_seq=2&turns=2&max_bytes=1048576') {
        return jsonResponse({
          events,
          page: {
            first_seq: 1,
            last_seq: 2,
            server_last_seq: 2,
            has_older: false,
            has_newer: true,
            starts_mid_turn: false,
            ends_mid_turn: false,
          },
        })
      }
      return baseFetch(url, init)
    }),
  )

  render(<App />)
  await user.click(await screen.findByRole('button', { name: /Open Agent response · #2/ }))

  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo'))
  expect(window.location.search).toBe('?event_seq=2')
  expect(screen.getByText('The build passes.').closest('[data-transcript-row]')).toHaveClass(
    'mx-2',
    'ring-2',
    'ring-inset',
  )

  await user.click(screen.getByRole('button', { name: 'Jump conversation map to latest' }))
  await waitFor(() => expect(window.location.search).toBe(''))
})

test('loading with console and files routes restores the routed app view', async () => {
  window.history.replaceState({}, '', '/sessions/sess_1/files')
  const firstRender = render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))
  expect(
    screen
      .getAllByRole('button', { name: 'Show files' })
      .some((button) => button.getAttribute('aria-pressed') === 'true'),
  ).toBe(true)

  firstRender.unmount()
  window.history.replaceState({}, '', '/sessions/sess_1/console')
  render(<App />)

  await waitFor(() =>
    expect(
      screen
        .getAllByRole('button', { name: 'Show console' })
        .some((button) => button.getAttribute('aria-pressed') === 'true'),
    ).toBe(true),
  )
})

test('mobile navigation uses the floating session header', async () => {
  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))

  const openSessionsButton = screen.getByRole('button', { name: 'Open sessions' })
  expect(openSessionsButton.closest('.mobile-floating-header-shell')).toBeTruthy()
  expect(document.querySelector('.mobile-app-header')).toBeNull()
})

test('mobile sessions button opens a floating session dialog', async () => {
  const user = userEvent.setup()

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))

  await user.click(screen.getByRole('button', { name: 'Open sessions' }))

  const dialog = await screen.findByRole('dialog', { name: 'Sessions' })
  expect(within(dialog).getByRole('button', { name: 'Create session' })).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: 'App menu' })).toBeInTheDocument()
  expect(within(dialog).getByRole('button', { name: 'Close' })).toBeInTheDocument()

  await user.click(within(dialog).getByRole('button', { name: /Write docs/ }))

  await waitFor(() => expect(window.location.pathname).toBe('/sessions/write-docs'))
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Sessions' })).not.toBeInTheDocument())
})

test('mobile header shows the four-view switcher and moves session details into Settings activity', async () => {
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: 390 })
  const user = userEvent.setup()
  render(<App />)
  await findEventSource('/api/sessions/activity/stream')
  const header = within(screen.getByTestId('mobile-floating-session-header'))
  expect(header.getAllByRole('button', { name: /^Show / }).map((button) => button.getAttribute('aria-label'))).toEqual([
    'Show chat', 'Show files', 'Show console', 'Show session settings',
  ])
  expect(header.queryByRole('button', { name: 'More session actions' })).not.toBeInTheDocument()

  await user.click(header.getByRole('button', { name: 'Show session settings' }))
  expect(window.location.pathname).toBe('/sessions/inspect-repo/settings/activity')
  expect(screen.getByRole('tab', { name: 'Activity' })).toHaveAttribute('aria-selected', 'true')
  const activityPanel = within(screen.getByRole('tabpanel', { name: 'Activity' }))
  expect(activityPanel.getByText('Events')).toBeInTheDocument()
  expect(activityPanel.getByText('Tools')).toBeInTheDocument()
  expect(activityPanel.getByText('Tokens')).toBeInTheDocument()
  expect(activityPanel.getByRole('button', { name: 'Archive selected session' })).toBeInTheDocument()
})

test('mobile console keeps its restart and stop actions beside the view switcher', async () => {
  const user = userEvent.setup()
  render(<App />)
  await findEventSource('/api/sessions/activity/stream')
  const sessionHeader = within(screen.getByTestId('mobile-floating-session-header'))
  await user.click(sessionHeader.getByRole('button', { name: 'Show console' }))

  const consoleHeaderElement = screen.getByRole('button', { name: 'Open sessions' }).closest('.mobile-floating-header-shell')
  expect(consoleHeaderElement).not.toBeNull()
  const consoleHeader = within(consoleHeaderElement as HTMLElement)
  expect(consoleHeader.getByRole('button', { name: 'Show chat' })).toBeInTheDocument()
  await user.click(consoleHeader.getByRole('button', { name: 'Console actions' }))
  const menu = within(consoleHeader.getByRole('menu', { name: 'Console actions' }))
  expect(menu.getByRole('menuitem', { name: 'Restart console' })).toBeInTheDocument()
  expect(menu.getByRole('menuitem', { name: 'Stop console' })).toBeInTheDocument()
})

test('mobile debug toggle reflects URL, keyboard, and panel close state', async () => {
  window.history.replaceState({}, '', '/sessions/sess_1?debug=1')
  const user = userEvent.setup()
  render(<App />)
  await findEventSource('/api/sessions/activity/stream')
  const header = within(screen.getByTestId('mobile-floating-session-header'))
  await user.click(header.getByRole('button', { name: 'Show session settings' }))
  await user.click(await screen.findByRole('tab', { name: 'Activity' }))
  const debug = await screen.findByRole('switch', { name: 'Client debug' })
  expect(debug).toBeChecked()
  fireEvent.click(screen.getByRole('button', { name: 'Close debug view' }))
  expect(debug).not.toBeChecked()
  fireEvent.keyDown(window, { key: 'd', metaKey: true })
  expect(debug).toBeChecked()
  expect(screen.getByRole('complementary', { name: 'Client debug' })).toBeVisible()
})

test('mobile Activity settings show live context usage without fetching more data', async () => {
  const user = userEvent.setup()
  const fetch = fetchMock({
    sessions: [{ ...firstSession, agent_type: 'codex', provider_session_id: 'thread_1' }, secondSession],
  })
  vi.stubGlobal('fetch', fetch)
  render(<App />)
  const source = await findEventSource('/api/sessions/activity/stream')
  const header = within(screen.getByTestId('mobile-floating-session-header'))
  const usageEvent = (seq: number, tokens: number) => event(seq, 'provider.codex.event', {
    provider: 'codex',
    provider_event_type: 'thread/tokenUsage/updated',
    raw: { tokenUsage: {
      total: { totalTokens: 8_000_000, inputTokens: 8_000_000, outputTokens: 0, cachedInputTokens: 0, reasoningOutputTokens: 0 },
      last: { totalTokens: tokens, inputTokens: tokens, outputTokens: 0, cachedInputTokens: 0, reasoningOutputTokens: 0 },
      modelContextWindow: 256_000,
    } },
  })
  act(() => source.emit(usageEvent(5, 128_000)))
  await waitFor(() => expect(screen.getByText('128k / 256k current')).toBeInTheDocument())
  const requestCount = fetch.mock.calls.length
  await user.click(header.getByRole('button', { name: 'Show session settings' }))
  await user.click(screen.getByRole('tab', { name: 'Activity' }))
  const panel = within(screen.getByRole('tabpanel', { name: 'Activity' }))
  const meter = panel.getByRole('meter', { name: 'Context token usage' })
  expect(panel.getByText(/128k \/ 256k/)).toBeInTheDocument()
  expect(panel.getByText('50%')).toBeInTheDocument()
  expect(meter.compareDocumentPosition(panel.getByRole('button', { name: 'Clear Codex context' })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(fetch.mock.calls.length).toBe(requestCount)

  act(() => source.emit(usageEvent(6, 192_000)))
  await waitFor(() => {
    const updatedPanel = within(screen.getByRole('tabpanel', { name: 'Activity' }))
    expect(updatedPanel.getByText(/192k \/ 256k/)).toBeInTheDocument()
    expect(updatedPanel.getByText('75%')).toBeInTheDocument()
  })
})

test('mobile context meter does not invent counts before usage has arrived', async () => {
  const user = userEvent.setup()
  render(<App />)
  await findEventSource('/api/sessions/activity/stream')
  const header = within(screen.getByTestId('mobile-floating-session-header'))
  await user.click(header.getByRole('button', { name: 'Show session settings' }))
  await user.click(await screen.findByRole('tab', { name: 'Activity' }))
  const panel = within(await screen.findByRole('tabpanel', { name: 'Activity' }))
  expect(panel.queryByText('No token usage yet')).not.toBeInTheDocument()
  expect(panel.queryByRole('meter')).not.toBeInTheDocument()
  expect(panel.getByRole('button', { name: 'Archive selected session' })).toBeInTheDocument()
  expect(panel.getByRole('switch', { name: 'Client debug' })).toBeInTheDocument()
})

test('header files view opens workspace files inline', async () => {
  const user = userEvent.setup()
  vi.stubGlobal('fetch', fetchMock({ fileEntry: true }))

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))

  await user.click(screen.getAllByRole('button', { name: 'Show files' })[0])
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/files'))
  const filesHeader = screen.getByTestId('floating-files-header')
  expect(within(filesHeader).getByRole('button', { name: 'Show session settings' })).toBeInTheDocument()
  expect(screen.getByText('No file selected').closest('.host-console-frame')).toBeTruthy()

  await user.click((await screen.findAllByRole('button', { name: /main\.go/i }))[0])

  const fileViewer = await screen.findByRole('region', { name: 'File viewer: main.go' })
  expect(within(fileViewer).getByLabelText('File editor')).toHaveValue('package main\n')
})

test('Hosting inside Settings updates the route and shows host status', async () => {
  const user = userEvent.setup()

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))
  await user.click(screen.getAllByRole('button', { name: 'Show session settings' })[0])
  await user.click(screen.getByRole('tab', { name: 'Hosting' }))

  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/settings/hosting'))
  expect(await screen.findByText('No host recipe found')).toBeInTheDocument()
  expect(within(screen.getByTestId('floating-settings-header')).getByRole('button', { name: 'Show session settings' })).toBeInTheDocument()
  expect(
    screen
      .getAllByRole('button', { name: 'Show session settings' })
      .some((button) => button.getAttribute('aria-pressed') === 'true'),
  ).toBe(true)
})

test('schedules view uses the shared floating session header and settings card', async () => {
  const user = userEvent.setup()

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))
  await user.click(screen.getAllByRole('button', { name: 'Show session settings' })[0])
  await user.click(screen.getByRole('tab', { name: 'Scheduled tasks' }))

  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/settings/schedules'))
  const schedulesHeader = screen.getByTestId('floating-settings-header')
  expect(within(schedulesHeader).getByRole('button', { name: 'Show session settings' })).toBeInTheDocument()
  expect(schedulesHeader.querySelector('.command-chat-header')).toBeInTheDocument()
  const scheduleInfo = (await screen.findByRole('heading', { name: 'Scheduled tasks' })).closest('section')
  const settingsCard = screen.getByRole('region', { name: 'Session settings' })
  expect(settingsCard).toHaveClass('rounded-lg', 'border', 'bg-background/72', 'shadow-sm')
  expect(settingsCard).toContainElement(scheduleInfo)
})

test('session settings uses its tabs as the single card header', async () => {
  const user = userEvent.setup()

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))
  const mobileHeader = screen.getByTestId('mobile-floating-session-header')
  await user.click(within(mobileHeader).getByRole('button', { name: 'Show session settings' }))

  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/settings'))
  const card = screen.getByRole('region', { name: 'Session settings' })
  expect(card).toHaveClass('rounded-lg', 'border', 'bg-background/72', 'shadow-sm')
  expect(within(card).getByRole('tablist', { name: 'Session settings sections' })).toBeInTheDocument()
  expect(within(card).getByRole('region', { name: 'Session configuration' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: 'Session settings' })).not.toBeInTheDocument()
  expect(screen.queryByText(/Review activity and configure this session/)).not.toBeInTheDocument()
  expect(screen.getByTestId('floating-settings-header')).toBeInTheDocument()
})

test('session settings header omits the parent-session link', async () => {
  const parent: Session = { ...firstSession, id: 'sess_parent', title: 'Parent' }
  const child: Session = {
    ...secondSession,
    id: 'sess_child',
    title: 'Child',
    parent_session_id: parent.id,
    lineage_depth: 1,
  }
  window.history.replaceState({}, '', '/sessions/sess_child/settings')
  vi.stubGlobal('fetch', fetchMock({ sessions: [parent, child] }))

  render(<App />)

  await screen.findByRole('region', { name: 'Session settings' })
  expect(within(screen.getByTestId('floating-settings-header')).queryByText('Parent session')).not.toBeInTheDocument()
})

test('repository skills view uses the shared floating session header and settings card', async () => {
  const user = userEvent.setup()
  render(<App />)
  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))
  await user.click(screen.getAllByRole('button', { name: 'Show session settings' })[0])
  await user.click(screen.getByRole('tab', { name: 'Skills' }))
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/settings/skills'))
  const skillsHeader = screen.getByTestId('floating-settings-header')
  expect(within(skillsHeader).getByRole('button', { name: 'Show session settings' })).toBeInTheDocument()
  expect(skillsHeader.querySelector('.command-chat-header')).toBeInTheDocument()
  const info = (await screen.findByRole('heading', { name: 'Repository skills' })).closest('section')
  expect(screen.getByRole('region', { name: 'Session settings' })).toContainElement(info)
})

test('primary navigation has four views and only the active Settings section loads data', async () => {
  const user = userEvent.setup()
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)
  render(<App />)
  const activity = await findEventSource('/api/sessions/activity/stream')
  const sectionRequests = () => fetch.mock.calls.map(([url]) => String(url)).filter((url) => /\/sessions\/sess_1\/(schedules|repository-skills|host)(\/logs)?$/.test(url))
  const header = within(screen.getByTestId('floating-session-header'))
  expect(header.getAllByRole('button', { name: /^Show / }).map((button) => button.getAttribute('aria-label'))).toEqual([
    'Show chat', 'Show files', 'Show console', 'Show session settings',
  ])
  const mobileHeader = within(screen.getByTestId('mobile-floating-session-header'))
  expect(mobileHeader.getAllByRole('button', { name: /^Show / }).map((button) => button.getAttribute('aria-label'))).toEqual([
    'Show chat', 'Show files', 'Show console', 'Show session settings',
  ])
  expect(sectionRequests()).toEqual([])

  await user.click(mobileHeader.getByRole('button', { name: 'Show session settings' }))
  expect(await screen.findByRole('region', { name: 'Session configuration' })).toBeInTheDocument()
  expect(screen.getByRole('tab', { name: 'General' })).toHaveAttribute('aria-selected', 'true')
  expect(sectionRequests()).toEqual([])
  await user.click(screen.getByRole('tab', { name: 'Scheduled tasks' }))
  await screen.findByRole('heading', { name: 'Scheduled tasks' })
  await waitFor(() => expect(sectionRequests()).toEqual(['/api/sessions/sess_1/schedules']))
  expect(screen.queryByRole('region', { name: 'Session configuration' })).not.toBeInTheDocument()

  await user.click(screen.getByRole('tab', { name: 'Skills' }))
  await screen.findByRole('heading', { name: 'Repository skills' })
  await waitFor(() => expect(sectionRequests()).toEqual(['/api/sessions/sess_1/schedules', '/api/sessions/sess_1/repository-skills']))
  expect(screen.queryByRole('heading', { name: 'Scheduled tasks' })).not.toBeInTheDocument()

  await user.click(screen.getByRole('tab', { name: 'Hosting' }))
  await screen.findByText('No host recipe found')
  const logs = await findEventSource('/api/sessions/sess_1/host/logs/stream')
  expect(logs.closed).toBe(false)
  expect(sectionRequests()).toEqual([
    '/api/sessions/sess_1/schedules', '/api/sessions/sess_1/repository-skills',
    '/api/sessions/sess_1/host', '/api/sessions/sess_1/host/logs',
  ])
  await user.click(screen.getByRole('tab', { name: 'General' }))
  await screen.findByRole('region', { name: 'Session configuration' })
  expect(logs.closed).toBe(true)
  expect(activity.closed).toBe(false)
  expect(FakeEventSource.instances.filter((source) => !source.closed)).toHaveLength(1)
})

test('Settings sections support keyboard selection, browser history, and debug URLs', async () => {
  window.history.replaceState({}, '', '/sessions/inspect-repo/settings?debug=1')
  const user = userEvent.setup()
  render(<App />)
  const general = await screen.findByRole('tab', { name: 'General' })
  general.focus()
  await user.keyboard('{ArrowRight}')
  expect(screen.getByRole('tab', { name: 'Scheduled tasks' })).toHaveFocus()
  expect(general).toHaveAttribute('aria-selected', 'true')
  await user.keyboard('{Enter}')
  await screen.findByRole('heading', { name: 'Scheduled tasks' })
  expect(window.location.pathname).toBe('/sessions/inspect-repo/settings/schedules')
  expect(window.location.search).toBe('?debug=1')
  await user.click(screen.getByRole('tab', { name: 'Skills' }))
  await screen.findByRole('heading', { name: 'Repository skills' })
  await act(async () => {
    window.history.back()
    await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/settings/schedules'))
  })
  expect(screen.getByRole('tab', { name: 'Scheduled tasks' })).toHaveAttribute('aria-selected', 'true')
  await act(async () => {
    window.history.forward()
    await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/settings/skills'))
  })
  expect(screen.getByRole('tab', { name: 'Skills' })).toHaveAttribute('aria-selected', 'true')
  expect(window.location.search).toBe('?debug=1')
})

test.each([
  ['schedules', 'Scheduled tasks', 'Scheduled tasks'],
  ['skills', 'Skills', 'Repository skills'],
  ['host', 'Hosting', 'Hosted preview'],
  ['settings/schedules', 'Scheduled tasks', 'Scheduled tasks'],
  ['settings/skills', 'Skills', 'Repository skills'],
  ['settings/hosting', 'Hosting', 'Hosted preview'],
])('deep link %s opens its section under Settings', async (path, tab, heading) => {
  window.history.replaceState({}, '', `/sessions/sess_1/${path}`)
  render(<App />)
  expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument()
  expect(screen.getByRole('region', { name: 'Session settings' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: 'Session settings' })).not.toBeInTheDocument()
  expect(screen.getByRole('tab', { name: tab })).toHaveAttribute('aria-selected', 'true')
  expect(within(screen.getByTestId('floating-settings-header')).getByRole('button', { name: 'Show session settings' })).toHaveAttribute('aria-pressed', 'true')
})

test('user skills appears beneath Overview and opens the global management route', async () => {
  const user = userEvent.setup()
  render(<App />)
  const navigation = await screen.findByRole('complementary', { name: 'Sessions' })
  const overview = within(navigation).getByRole('button', { name: 'Overview' })
  const userSkills = within(navigation).getByRole('button', { name: 'User skills' })
  expect(overview.compareDocumentPosition(userSkills) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  await user.click(userSkills)
  await waitFor(() => expect(window.location.pathname).toBe('/skills'))
  expect(await screen.findByRole('heading', { name: 'User skills' })).toBeInTheDocument()
  expect(screen.getByText('/Users/tester/.agents/skills')).toBeInTheDocument()
  expect(userSkills).toHaveAttribute('aria-current', 'page')
})

test('loading with a session route selects that session', async () => {
  window.history.replaceState({}, '', '/sessions/sess_2')

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Write docs').length).toBeGreaterThan(0))
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/write-docs'))
  expect(
    screen
      .getAllByRole('button', { name: /Write docs/ })
      .some((button) => button.getAttribute('aria-current') === 'true'),
  ).toBe(true)
})

test('duplicate child title keeps an unambiguous route across foreground resync', async () => {
  const user = userEvent.setup()
  const parent: Session = { ...firstSession, id: 'sess_parent', title: 'Shared title', child_count: 1 }
  const child: Session = {
    ...secondSession,
    id: 'sess_child',
    title: 'Shared title',
    parent_session_id: parent.id,
    lineage_depth: 1,
  }
  window.history.replaceState({}, '', '/sessions/sess_parent')
  const fetch = fetchMock({ sessions: [parent, child] })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  const childRow = await waitFor(() => {
    const row = document.querySelector('[data-session-id="sess_child"]')
    expect(row).not.toBeNull()
    return row as HTMLElement
  })
  await user.click(within(childRow).getByRole('button', { name: 'Shared title' }))
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/sess_child'))

  // Older clients could leave an already-selected duplicate on its title slug.
  window.history.replaceState({}, '', '/sessions/shared-title')
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  act(() => document.dispatchEvent(new Event('visibilitychange')))

  await waitFor(() => {
    expect(fetch.mock.calls.filter(([url]) => String(url) === '/api/sessions?limit=50')).toHaveLength(2)
    expect(window.location.pathname).toBe('/sessions/sess_child')
  })
  expect(within(childRow).getByRole('button', { name: 'Shared title' })).toHaveAttribute('aria-current', 'true')
  expect(screen.queryByRole('heading', { name: 'Session unavailable' })).not.toBeInTheDocument()
  visibility.mockRestore()
})

test('reload repairs an ambiguous duplicate-title route from the last selected child', async () => {
  const parent: Session = { ...firstSession, id: 'sess_parent', title: 'Minecraft', child_count: 1 }
  const child: Session = {
    ...secondSession,
    id: 'sess_child',
    title: 'Minecraft',
    parent_session_id: parent.id,
    lineage_depth: 1,
  }
  window.history.replaceState({}, '', '/sessions/minecraft')
  window.localStorage.setItem('gorchestra.last-selected-session.v1', child.id)
  vi.stubGlobal('fetch', fetchMock({ sessions: [parent, child] }))

  render(<App />)

  await waitFor(() => expect(window.location.pathname).toBe('/sessions/sess_child'))
  const childRow = document.querySelector('[data-session-id="sess_child"]')
  expect(childRow).not.toBeNull()
  expect(within(childRow as HTMLElement).getByRole('button', { name: 'Minecraft' })).toHaveAttribute(
    'aria-current',
    'true',
  )
  expect(screen.queryByRole('heading', { name: 'Session unavailable' })).not.toBeInTheDocument()
})

test('loading with a session slug route selects that session without replacing the slug', async () => {
  window.history.replaceState({}, '', '/sessions/write-docs')
  let resolveSessions: (() => void) | undefined
  const fetch = vi.fn(async (url: RequestInfo | URL) => {
    const path = String(url)
    if (path === '/api/health') {
      return jsonResponse({ status: 'ok' })
    }
    if (path === '/api/sessions?limit=50') {
      await new Promise<void>((resolve) => {
        resolveSessions = resolve
      })
      return jsonResponse({ sessions: [firstSession, secondSession] })
    }
    if (path === '/api/sessions/sess_2') {
      return jsonResponse(secondSession)
    }
    if (path === '/api/sessions/sess_2/events?tail=true&turns=50&max_bytes=2097152') {
      return jsonResponse({ events: [] })
    }
    throw new Error(`unexpected URL ${path}`)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  expect(screen.getByText('Loading session...')).toBeInTheDocument()

  await act(async () => {
    resolveSessions?.()
    await Promise.resolve()
  })

  await waitFor(() => expect(screen.getAllByText('Write docs').length).toBeGreaterThan(0))
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/write-docs'))
  expect(
    screen
      .getAllByRole('button', { name: /Write docs/ })
      .some((button) => button.getAttribute('aria-current') === 'true'),
  ).toBe(true)
})

test('loading with a session slug view route restores the view and preserves slug navigation', async () => {
  const user = userEvent.setup()
  window.history.replaceState({}, '', '/sessions/write-docs/files')

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Write docs').length).toBeGreaterThan(0))
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/write-docs/files'))
  expect(
    screen
      .getAllByRole('button', { name: 'Show files' })
      .some((button) => button.getAttribute('aria-pressed') === 'true'),
  ).toBe(true)

  await user.click(screen.getAllByRole('button', { name: 'Show console' })[0])

  await waitFor(() => expect(window.location.pathname).toBe('/sessions/write-docs/console'))
})

test('loading with a session file route opens the routed file', async () => {
  vi.stubGlobal('fetch', fetchMock({ fileEntry: true }))
  window.history.replaceState({}, '', '/sessions/inspect-repo/files/main.go')

  render(<App />)

  const fileViewer = await screen.findByRole('region', { name: 'File viewer: main.go' })
  expect(within(fileViewer).getByLabelText('File editor')).toHaveValue('package main\n')
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/files/main.go'))
})

test('session route shows loading instead of no selection while sessions load', async () => {
  window.history.replaceState({}, '', '/sessions/sess_1')
  let resolveSessions: (() => void) | undefined
  const fetch = vi.fn(async (url: RequestInfo | URL) => {
    const path = String(url)
    if (path === '/api/health') {
      return jsonResponse({ status: 'ok' })
    }
    if (path === '/api/sessions?limit=50') {
      await new Promise<void>((resolve) => {
        resolveSessions = resolve
      })
      return jsonResponse({ sessions: [firstSession, secondSession] })
    }
    if (path === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152') {
      return jsonResponse({ events: [] })
    }
    throw new Error(`unexpected URL ${path}`)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  expect(await screen.findByText('Loading session...')).toBeInTheDocument()
  expect(screen.queryByText('No session selected')).not.toBeInTheDocument()

  await act(async () => {
    resolveSessions?.()
    await Promise.resolve()
  })

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))
})

test('cold start keeps the root route on Overview while sessions load', async () => {
  window.history.replaceState({}, '', '/')

  render(<App />)

  expect(await screen.findByRole('heading', { name: 'Your work at a glance' })).toBeInTheDocument()
  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))
  expect(window.location.pathname).toBe('/')
})

test('cached session route renders the session shell while history loads', async () => {
  window.history.replaceState({}, '', '/sessions/sess_1')
  await writeCachedSession(firstSession)
  let resolveSessions: (() => void) | undefined
  let resolveEvents: (() => void) | undefined
  const fetch = vi.fn(async (url: RequestInfo | URL) => {
    const path = String(url)
    if (path === '/api/health') {
      return jsonResponse({ status: 'ok' })
    }
    if (path.startsWith('/api/dashboard/runs?')) {
      return jsonResponse({ runs: [], total: 0 })
    }
    if (path.startsWith('/api/dashboard?')) {
      return jsonResponse(emptyDashboardResponse())
    }
    if (path === '/api/sessions?limit=50') {
      await new Promise<void>((resolve) => {
        resolveSessions = resolve
      })
      return jsonResponse({ sessions: [firstSession, secondSession] })
    }
    if (path === '/api/sessions/sess_1') {
      return jsonResponse(firstSession)
    }
    if (path === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152') {
      await new Promise<void>((resolve) => {
        resolveEvents = resolve
      })
      return jsonResponse({ events: [] })
    }
    throw new Error(`unexpected URL ${path}`)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  expect(screen.queryByText('Loading session...')).not.toBeInTheDocument()
  expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0)
  expect(await screen.findByText('Loading chat history...')).toBeInTheDocument()

  await act(async () => {
    resolveEvents?.()
    resolveSessions?.()
    await Promise.resolve()
  })
})

test('cached slug route renders the session shell while sessions load', async () => {
  window.history.replaceState({}, '', '/sessions/write-docs')
  await writeCachedSession(secondSession)
  let resolveSessions: (() => void) | undefined
  const fetch = vi.fn(async (url: RequestInfo | URL) => {
    const path = String(url)
    if (path === '/api/health') {
      return jsonResponse({ status: 'ok' })
    }
    if (path === '/api/sessions?limit=50') {
      await new Promise<void>((resolve) => {
        resolveSessions = resolve
      })
      return jsonResponse({ sessions: [firstSession, secondSession] })
    }
    if (path === '/api/sessions/sess_2') {
      return jsonResponse(secondSession)
    }
    if (path === '/api/sessions/sess_2/events?tail=true&turns=50&max_bytes=2097152') {
      return jsonResponse({ events: [] })
    }
    throw new Error(`unexpected URL ${path}`)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  expect(screen.queryByText('Loading session...')).not.toBeInTheDocument()
  expect(screen.getAllByText('Write docs').length).toBeGreaterThan(0)
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/write-docs'))

  await act(async () => {
    resolveSessions?.()
    await Promise.resolve()
  })
})

test('stale cached slug aliases never redirect to an unrelated session', async () => {
  window.history.replaceState({}, '', '/sessions/write-docs')
  await writeCachedSession(secondSession)
  const renamedSecondSession: Session = { ...secondSession, title: 'Renamed docs' }
  let resolveSessions: (() => void) | undefined
  const fetch = vi.fn(async (url: RequestInfo | URL) => {
    const path = String(url)
    if (path === '/api/health') {
      return jsonResponse({ status: 'ok' })
    }
    if (path === '/api/sessions?limit=50') {
      await new Promise<void>((resolve) => {
        resolveSessions = resolve
      })
      return jsonResponse({ sessions: [firstSession, renamedSecondSession] })
    }
    if (path === '/api/sessions/sess_1') {
      return jsonResponse(firstSession)
    }
    if (path === '/api/sessions/sess_2') {
      return jsonResponse(renamedSecondSession)
    }
    if (path === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152') {
      return jsonResponse({ events: [] })
    }
    if (path === '/api/sessions/sess_2/events?tail=true&turns=50&max_bytes=2097152') {
      return jsonResponse({ events: [] })
    }
    throw new Error(`unexpected URL ${path}`)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  expect(screen.queryByText('Loading session...')).not.toBeInTheDocument()
  expect(screen.getAllByText('Write docs').length).toBeGreaterThan(0)

  await act(async () => {
    resolveSessions?.()
    await Promise.resolve()
  })

  expect(await screen.findByRole('heading', { name: 'Session unavailable' })).toBeInTheDocument()
  expect(window.location.pathname).toBe('/sessions/write-docs')
  expect(screen.queryByRole('textbox', { name: 'Prompt' })).not.toBeInTheDocument()
})

test('session route shows inline chat history loading once session details are available', async () => {
  window.history.replaceState({}, '', '/sessions/sess_1')
  let resolveEvents: (() => void) | undefined
  const fetch = vi.fn(async (url: RequestInfo | URL) => {
    const path = String(url)
    if (path === '/api/health') {
      return jsonResponse({ status: 'ok' })
    }
    if (path === '/api/sessions?limit=50') {
      return jsonResponse({ sessions: [firstSession, secondSession] })
    }
    if (path === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152') {
      await new Promise<void>((resolve) => {
        resolveEvents = resolve
      })
      return jsonResponse({ events: [] })
    }
    throw new Error(`unexpected URL ${path}`)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  expect(await screen.findByText('Loading chat history...')).toBeInTheDocument()
  expect(screen.queryByText('Loading session...')).not.toBeInTheDocument()

  await act(async () => {
    resolveEvents?.()
    await Promise.resolve()
  })

  await waitFor(() => expect(screen.queryByText('Loading session...')).not.toBeInTheDocument())
})

test('session route paints cached transcript before the snapshot, then fetches messages missed while away', async () => {
  window.history.replaceState({}, '', '/sessions/sess_1')
  vi.stubGlobal('indexedDB', createFakeIndexedDB())
  clearSessionEventCacheForTest()
  await writeCachedSession(firstSession)
  await writeCachedSessionEvents(
    'sess_1',
    [
      event(10, 'user.message.completed', { text: 'Cached prompt' }),
      event(11, 'agent.message.completed', { text: 'Cached answer' }),
    ],
    false,
  )
  expect(await readCachedSessionEvents('sess_1')).toMatchObject({ lastSeq: 11 })

  let resolveSessions: (() => void) | undefined
  const fetch = vi.fn(async (url: RequestInfo | URL) => {
    const path = String(url)
    if (path === '/api/health') {
      return jsonResponse({ status: 'ok' })
    }
    if (path === '/api/sessions?limit=50') {
      await new Promise<void>((resolve) => {
        resolveSessions = resolve
      })
      return jsonResponse({ sessions: [{ ...firstSession, last_event_seq: 12 }, secondSession], event_cursor: 80 })
    }
    if (path === '/api/sessions/sess_1') {
      return jsonResponse(firstSession)
    }
    if (path === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152') {
      return jsonResponse({
        events: [
          event(10, 'user.message.completed', { text: 'Cached prompt' }),
          event(11, 'agent.message.completed', { text: 'Cached answer' }),
          event(12, 'agent.message.completed', { item_id: 'missed', text: 'Completed on another device' }),
        ],
      })
    }
    throw new Error(`unexpected URL ${path}`)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await waitFor(() => expect(screen.getByText('Cached answer')).toBeInTheDocument(), { timeout: 3000 })
  expect(screen.queryByText('Loading session...')).not.toBeInTheDocument()
  expect(
    fetch.mock.calls.some(
      ([url]) => String(url) === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152',
    ),
  ).toBe(false)
  expect(FakeEventSource.instances.filter((source) => source.url.startsWith('/api/sessions/activity/stream')))
    .toHaveLength(0)

  await act(async () => {
    resolveSessions?.()
    await Promise.resolve()
  })
  await findEventSource('/api/sessions/activity/stream?after_cursor=80')
  await screen.findByText('Completed on another device')
  expect(fetch.mock.calls.filter(([url]) => String(url).includes('/events?tail='))).toHaveLength(1)
})

test('switching sessions from settings returns to messages and discards an unsaved rename', async () => {
  const user = userEvent.setup()

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))
  await user.click(screen.getAllByRole('button', { name: 'Show session settings' })[0])
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/settings'))

  const input = screen.getByRole('textbox', { name: 'Session name' })
  await user.clear(input)
  await user.type(input, 'Renamed session')
  expect(input).toHaveValue('Renamed session')

  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])

  await waitFor(() => expect(window.location.pathname).toBe('/sessions/write-docs'))
  expect(screen.getByRole('textbox', { name: 'Prompt' })).toHaveFocus()
  expect(
    screen
      .getAllByRole('button', { name: /Write docs/ })
      .some((button) => button.getAttribute('aria-current') === 'true'),
  ).toBe(true)

  await user.click(screen.getAllByRole('button', { name: /Inspect repo/ })[0])
  await user.click(screen.getAllByRole('button', { name: 'Show session settings' })[0])
  expect(screen.getByRole('textbox', { name: 'Session name' })).toHaveValue('Inspect repo')
})

test('search button opens global spotlight search', async () => {
  const user = userEvent.setup()
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await user.click(await screen.findByRole('button', { name: 'Search' }))
  await user.type(screen.getByRole('textbox', { name: 'Search Threave' }), 'Inspect')
  expect(await screen.findByRole('option', { name: /Inspect repo/ })).toBeInTheDocument()
  expect(fetch).toHaveBeenCalledWith(
    '/api/search/stream?q=Inspect&session_id=sess_1',
    expect.objectContaining({ signal: expect.any(AbortSignal) }),
  )
})

test('global navigation shortcuts open overview, user skills, and recent sessions', async () => {
  const recentSessions = [
    session('sess_3', 'Third newest', '2026-06-12T16:03:00Z'),
    session('sess_1', 'Newest', '2026-06-12T16:05:00Z'),
    session('sess_5', 'Fifth newest', '2026-06-12T16:01:00Z'),
    session('sess_2', 'Second newest', '2026-06-12T16:04:00Z'),
    session('sess_4', 'Fourth newest', '2026-06-12T16:02:00Z'),
  ]
  vi.stubGlobal('fetch', fetchMock({ sessions: recentSessions }))

  render(<App />)
  await screen.findByText('Fifth newest')

  fireEvent.keyDown(window, { key: 'o', metaKey: true })
  await waitFor(() => expect(window.location.pathname).toBe('/'))

  fireEvent.keyDown(window, { key: 's', metaKey: true })
  await waitFor(() => expect(window.location.pathname).toBe('/skills'))

  fireEvent.keyDown(window, { key: '1', metaKey: true })
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/newest'))
  await waitFor(() => expect(screen.getByRole('textbox', { name: 'Prompt' })).toHaveFocus())

  fireEvent.keyDown(window, { key: '5', ctrlKey: true })
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/fifth-newest'))
  await waitFor(() => expect(screen.getByRole('textbox', { name: 'Prompt' })).toHaveFocus())
})

test('initial session load fetches the recent event window and streams after the tail', async () => {
  const fetch = fetchMock({
    events: [event(39, 'agent.message.delta', { text: 'Tail' }), event(40, 'agent.message.completed', { text: 'Tail' })],
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152',
      expect.objectContaining({ headers: expect.objectContaining({ Accept: 'application/json' }) }),
    ),
  )
  await findEventSource('/api/sessions/activity/stream')
  expect(FakeEventSource.instances).toHaveLength(1)
}, 10_000)

test('a global event arriving during tail load cannot be replaced by the older response', async () => {
  const baseFetch = fetchMock()
  let resolveTail: ((response: Response) => void) | undefined
  const fetch = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152') {
      return new Promise<Response>((resolve) => {
        resolveTail = resolve
      })
    }
    return baseFetch(url, init)
  })
  vi.stubGlobal('fetch', fetch)
  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    activitySource.emit({
      ...event(41, 'agent.message.completed', { text: 'Racing global answer' }),
      global_seq: 71,
    })
  })
  expect(await screen.findByText('Racing global answer')).toBeInTheDocument()

  await act(async () => {
    resolveTail?.(jsonResponse({
      events: [event(40, 'agent.message.completed', { text: 'Older tail answer' })],
      page: { server_last_seq: 40 },
    }))
    await Promise.resolve()
  })

  expect(await screen.findByText('Older tail answer')).toBeInTheDocument()
  expect(screen.getByText('Racing global answer')).toBeInTheDocument()
  expect(FakeEventSource.instances).toHaveLength(1)
})

test('successful prompt submit renders immediately and reconciles with the live stream', async () => {
  const user = userEvent.setup()
  const baseFetch = fetchMock({
    events: [event(40, 'agent.message.completed', { text: 'Previous answer' })],
  })
  let resolveSubmit: ((response: Response) => void) | undefined
  const fetch = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === '/api/sessions/sess_1/messages' && init?.method === 'POST') {
      return new Promise<Response>((resolve) => {
        resolveSubmit = resolve
      })
    }
    return baseFetch(url, init)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152',
      expect.objectContaining({ headers: expect.objectContaining({ Accept: 'application/json' }) }),
    ),
  )
  expect(screen.queryByText('Fresh prompt')).not.toBeInTheDocument()
  const source = await findEventSource('/api/sessions/activity/stream')

  await user.type(screen.getByPlaceholderText('Ask the agent to work on this repository...'), 'Fresh prompt{Enter}')

  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/sess_1/messages',
      expect.objectContaining({ method: 'POST', headers: expect.objectContaining({ Accept: 'application/json' }) }),
    ),
  )
  expect(screen.getAllByText('Fresh prompt')).toHaveLength(1)
  const submitCall = fetch.mock.calls.find(([url]) => String(url) === '/api/sessions/sess_1/messages')
  const submitBody = JSON.parse(String(submitCall?.[1]?.body)) as { client_submission_id: string }
  expect(submitBody.client_submission_id).toBeTruthy()
  expect(fetch.mock.calls.filter(([url]) => String(url) === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152')).toHaveLength(1)
  await act(async () => {
    resolveSubmit?.(jsonResponse({ session_id: 'sess_1', status: 'running', accepted_as: 'run' }))
    await Promise.resolve()
  })
  act(() => {
    source.emit(event(41, 'user.message.completed', {
      text: 'Fresh prompt',
      client_submission_id: submitBody.client_submission_id,
    }))
  })
  await waitFor(() => expect(screen.getAllByText('Fresh prompt')).toHaveLength(1))
  expect(FakeEventSource.instances).toHaveLength(1)
})

test('successful prompt submit keeps the current transcript visible while awaiting its stream event', async () => {
  const user = userEvent.setup()
  let tailRequests = 0
  const fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = String(url)
    if (path === '/api/health') {
      return jsonResponse({ status: 'ok' })
    }
    if (path === '/api/sessions?limit=50') {
      return jsonResponse({ sessions: [firstSession, secondSession] })
    }
    if (path === '/api/sessions/sess_1') {
      return jsonResponse(firstSession)
    }
    if (path === '/api/sessions/sess_1/messages' && init?.method === 'POST') {
      return jsonResponse({ session_id: 'sess_1', status: 'running' })
    }
    if (path === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152') {
      tailRequests += 1
      return jsonResponse({ events: [event(40, 'user.message.completed', { text: 'Previous prompt' })] })
    }
    throw new Error(`unexpected URL ${path}`)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await waitFor(() => expect(screen.getByText('Previous prompt')).toBeInTheDocument())
  const source = await findEventSource('/api/sessions/activity/stream')

  await user.type(screen.getByPlaceholderText('Ask the agent to work on this repository...'), 'Fresh prompt{Enter}')

  await waitFor(() => expect(fetch).toHaveBeenCalledWith(
    '/api/sessions/sess_1/messages',
    expect.objectContaining({ method: 'POST' }),
  ))
  expect(tailRequests).toBe(1)
  expect(screen.getByText('Previous prompt')).toBeInTheDocument()
  expect(screen.queryByText('Loading chat history...')).not.toBeInTheDocument()
  expect(screen.getAllByText('Fresh prompt')).toHaveLength(1)
  const submitCall = fetch.mock.calls.find(([url]) => String(url) === '/api/sessions/sess_1/messages')
  const submitBody = JSON.parse(String(submitCall?.[1]?.body)) as { client_submission_id: string }

  act(() => {
    source.emit(event(41, 'user.message.completed', {
      text: 'Fresh prompt',
      client_submission_id: submitBody.client_submission_id,
    }))
  })
  await waitFor(() => expect(screen.getAllByText('Fresh prompt')).toHaveLength(1))
})

test('switching back to a cached session restores transcript before replaying stream updates', async () => {
  const user = userEvent.setup()
  const baseFetch = fetchMock()
  const fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    return baseFetch(url, init)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  const initialSource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    initialSource.emit(event(40, 'user.message.completed', { text: 'Cached prompt' }))
  })
  expect(await screen.findByText('Cached prompt')).toBeInTheDocument()

  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/write-docs'))

  await user.click(screen.getAllByRole('button', { name: /Inspect repo/ })[0])

  expect(await screen.findByText('Cached prompt')).toBeInTheDocument()
  expect(screen.queryByText('Loading chat history...')).not.toBeInTheDocument()
  expect(
    fetch.mock.calls.filter(([url]) => String(url) === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152'),
  ).toHaveLength(1)

  expect(FakeEventSource.instances).toHaveLength(1)
  const replaySource = initialSource
  act(() => {
    replaySource?.emit(event(41, 'user.message.completed', { text: 'Replayed update' }))
  })

  expect(await screen.findByText('Replayed update')).toBeInTheDocument()
})

test('reviewing history buffers live events until jumping without reconnecting the stream', async () => {
  const user = userEvent.setup()
  const baseFetch = fetchMock({
    events: [
      event(39, 'user.message.completed', { text: 'Visible prompt' }),
      event(40, 'agent.message.completed', { item_id: 'msg_1', text: 'Visible answer' }),
    ],
  })
  let tailRequests = 0
  const fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152') {
      tailRequests += 1
    }
    return baseFetch(url, init)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  const source = await findEventSource('/api/sessions/activity/stream')
  const log = await screen.findByRole('log', { name: 'Chat messages' })
  Object.defineProperties(log, {
    scrollTop: { configurable: true, writable: true, value: 120 },
    scrollHeight: { configurable: true, value: 1000 },
    clientHeight: { configurable: true, value: 400 },
  })
  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log)

  act(() => {
    source.emit(event(41, 'agent.message.completed', { item_id: 'msg_2', text: 'Live answer' }))
  })

  expect(screen.queryByText('Live answer')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Scroll to latest and resume auto-scroll' }))
  expect(await screen.findByText('Live answer')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
  expect(tailRequests).toBe(1)
  expect(FakeEventSource.instances).toHaveLength(1)
})

test('global activity stream marks another session pending input', async () => {
  const runningSecondSession: Session = { ...secondSession, status: 'running', last_event_seq: 4, event_count: 4 }
  vi.stubGlobal('fetch', fetchMock({ sessions: [firstSession, runningSecondSession] }))

  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    activitySource.emit(
      event(5, 'agent.input.requested', { request_id: 'call_test', questions: [] }, 'sess_2'),
    )
  })

  expect(await screen.findByRole('img', { name: 'Session pending user input' })).toHaveClass(
    'animate-pulse',
    'bg-[hsl(var(--warning))]',
  )
  await waitFor(() => expect(faviconPath()).toBe('/favicon-notify.svg'))
})

test('real streamed activity reorders sessions while clicking and metadata refreshes do not', async () => {
  const user = userEvent.setup()
  const fetch = fetchMock({ sessions: [firstSession, { ...secondSession, updated_at: '2026-06-12T19:00:00Z' }] })
  vi.stubGlobal('fetch', fetch)
  render(<App />)

  const source = await findEventSource('/api/sessions/activity/stream')
  const rowIDs = () => Array.from(
    document.querySelector('.session-list-rows')!.querySelectorAll('[data-session-id]'),
    (row) => row.getAttribute('data-session-id'),
  )
  await waitFor(() => expect(rowIDs()).toEqual(['sess_1', 'sess_2']))
  act(() => {
    source.emit({ ...event(1, 'agent.message.delta', { text: 'Working' }, 'sess_2'), created_at: '2026-06-12T16:03:00Z', transient: true })
  })
  await waitFor(() => expect(rowIDs()).toEqual(['sess_2', 'sess_1']))

  // Opening the active session fetches an older durable snapshot; keep its streamed activity.
  await user.click(screen.getByRole('button', { name: secondSession.title }))
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/sessions/sess_2/notification-attention/clear', expect.anything()))
  expect(rowIDs()).toEqual(['sess_2', 'sess_1'])
  await user.click(screen.getByRole('button', { name: firstSession.title }))
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/sessions/sess_1/notification-attention/clear', expect.anything()))
  expect(rowIDs()).toEqual(['sess_2', 'sess_1'])
})

test('session settings events update the cached session snapshot', () => {
  const updated = applySessionEvent(
    firstSession,
    event(1, 'session.agent_options.updated', {
      updated_at: '2026-06-12T16:04:00Z',
      agent_options: {
        codex: {
          model: 'gpt-5.6',
          reasoning_effort: 'xhigh',
          fast_mode: true,
          planning_mode: false,
        },
      },
    }),
    null,
  )

  expect(updated.agent_options).toEqual({
    codex: {
      model: 'gpt-5.6',
      reasoning_effort: 'xhigh',
      fast_mode: true,
      planning_mode: false,
    },
  })
  expect(updated.updated_at).toBe('2026-06-12T16:04:00Z')
  expect(updated.event_count).toBe(1)
  expect(updated.last_event_seq).toBe(1)
})

test('session pin events update pin state without changing activity recency', () => {
  const updated = applySessionEvent(
    firstSession,
    event(1, 'session.pin.updated', { pinned_at: '2026-06-12T16:20:00Z' }),
    null,
  )

  expect(updated.pinned_at).toBe('2026-06-12T16:20:00Z')
  expect(updated.updated_at).toBe(firstSession.updated_at)
  expect(updated.event_count).toBe(1)
  expect(updated.last_event_seq).toBe(1)
})

test('session archive lifecycle events update the cached session snapshot', () => {
  const archived = applySessionEvent(
    firstSession,
    event(1, 'session.archived', {
      archived_at: '2026-06-12T16:20:00Z',
      updated_at: '2026-06-12T16:20:00Z',
    }),
    null,
  )
  expect(archived.archived_at).toBe('2026-06-12T16:20:00Z')
  expect(archived.updated_at).toBe('2026-06-12T16:20:00Z')

  const restored = applySessionEvent(
    archived,
    event(2, 'session.restored', {
      archived_at: null,
      updated_at: '2026-06-12T16:21:00Z',
    }),
    null,
  )
  expect(restored.archived_at).toBeNull()
  expect(restored.updated_at).toBe('2026-06-12T16:21:00Z')
})

test('global archive activity removes an unselected child and its parent expander', async () => {
  const parent: Session = { ...firstSession, id: 'sess_parent', title: 'Parent', child_count: 1 }
  const child: Session = {
    ...secondSession,
    id: 'sess_child',
    title: 'Child',
    parent_session_id: parent.id,
    lineage_depth: 1,
  }
  window.history.replaceState({}, '', '/sessions/sess_parent')
  vi.stubGlobal('fetch', fetchMock({ sessions: [parent, child] }))

  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  expect(await screen.findByRole('button', { name: 'Child' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Collapse Parent' })).toBeInTheDocument()

  act(() => {
    activitySource.emit({
      ...event(1, 'session.archived', {
        archived_at: '2026-06-12T16:20:00Z',
        updated_at: '2026-06-12T16:20:00Z',
      }, 'sess_child'),
      global_seq: 45,
    })
  })

  await waitFor(() => expect(screen.queryByRole('button', { name: 'Child' })).not.toBeInTheDocument())
  expect(screen.queryByRole('button', { name: 'Collapse Parent' })).not.toBeInTheDocument()
})

test('global archive activity removes the selected session and returns to the dashboard', async () => {
  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    activitySource.emit({
      ...event(1, 'session.archived', {
        archived_at: '2026-06-12T16:20:00Z',
        updated_at: '2026-06-12T16:20:00Z',
      }),
      global_seq: 45,
    })
  })

  await waitFor(() => expect(screen.queryByRole('button', { name: 'Inspect repo archived' })).not.toBeInTheDocument())
  expect(await screen.findByRole('heading', { name: 'Your work at a glance' })).toBeInTheDocument()
  expect(window.location.pathname).toBe('/')
})

test('app menu toggles archived sessions and remembers the preference', async () => {
  const user = userEvent.setup()
  const archivedSession: Session = {
    ...session('sess_archived', 'Archived chat', '2026-06-12T16:00:30Z'),
    archived_at: '2026-06-12T16:05:00Z',
  }
  vi.stubGlobal('fetch', fetchMock({ sessions: [firstSession, secondSession, archivedSession] }))
  render(<App />)

  expect(screen.queryByRole('button', { name: 'Archived chat archived' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'App menu' }))
  await user.click(screen.getByRole('menuitemcheckbox', { name: 'Show archived' }))

  expect(await screen.findByRole('button', { name: 'Archived chat archived' })).toBeInTheDocument()
  expect(screen.queryByText(/^Archived$/)).not.toBeInTheDocument()
  expect(window.localStorage.getItem('gorchestra.show-archived-sessions.v1')).toBe('true')

  await user.click(screen.getByRole('button', { name: 'App menu' }))
  await user.click(screen.getByRole('menuitemcheckbox', { name: 'Show archived' }))

  await waitFor(() => expect(screen.queryByRole('button', { name: 'Archived chat archived' })).not.toBeInTheDocument())
  expect(window.localStorage.getItem('gorchestra.show-archived-sessions.v1')).toBe('false')
})

test('global pin activity moves a background session above newer recent activity', async () => {
  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    activitySource.emit({
      ...event(1, 'session.pin.updated', { pinned_at: '2026-06-12T16:20:00Z' }, 'sess_2'),
      global_seq: 45,
    })
  })

  await waitFor(() => {
    const rows = Array.from(document.querySelectorAll<HTMLElement>('.session-row'))
    expect(rows[0]).toHaveAttribute('data-session-id', 'sess_2')
    expect(rows[0]).toHaveAttribute('data-pinned', 'true')
  })
})

test('global activity stream marks finished unselected sessions as unseen until selected', async () => {
  const runningSecondSession: Session = { ...secondSession, status: 'running', last_event_seq: 4, event_count: 4 }
  vi.stubGlobal('fetch', fetchMock({ sessions: [firstSession, runningSecondSession] }))
  const user = userEvent.setup()

  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    activitySource.emit(
      event(5, 'session.status.updated', { status: 'idle', updated_at: '2026-06-12T16:12:00Z' }, 'sess_2'),
    )
  })

  expect(await screen.findByRole('img', { name: 'Session has unseen results' })).toHaveClass('bg-[hsl(var(--warning))]')
  await waitFor(() => expect(faviconPath()).toBe('/favicon-notify.svg'))

  await user.click(screen.getAllByRole('button', { name: /Write docs/ })[0])

  await waitFor(() => expect(screen.queryByRole('img', { name: 'Session has unseen results' })).not.toBeInTheDocument())
  await waitFor(() => expect(faviconPath()).toBe('/favicon.svg'))
})

test('global terminal events mark unselected sessions unseen even when seen state is stale', async () => {
  window.localStorage.setItem('gorchestra.session-seen-seq.v1', JSON.stringify({ sess_2: 5 }))
  const runningSecondSession: Session = { ...secondSession, status: 'running', last_event_seq: 4, event_count: 4 }
  vi.stubGlobal('fetch', fetchMock({ sessions: [firstSession, runningSecondSession] }))

  render(<App />)

  const activitySource = await findEventSource('/api/sessions/activity/stream')
  act(() => {
    activitySource.emit(
      event(5, 'agent.run.completed', { provider: 'codex', provider_event_type: 'turn/completed' }, 'sess_2'),
    )
  })

  expect(await screen.findByRole('img', { name: 'Session has unseen results' })).toHaveClass('bg-[hsl(var(--warning))]')
  await waitFor(() => expect(faviconPath()).toBe('/favicon-notify.svg'))
})

test('reaching the leading edge fetches the previous turn page', async () => {
  const fetch = fetchMock({
    events: [event(251, 'agent.message.delta', { text: 'Tail' }), event(252, 'agent.message.completed', { text: 'Tail' })],
    olderEvents: [event(249, 'user.message.completed', { text: 'Older prompt' }), event(250, 'agent.message.completed', { text: 'Older answer' })],
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await screen.findByText('Tail')
  const log = screen.getByRole('log', { name: 'Chat messages' })
  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log, { target: { scrollTop: 0 } })

  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/sess_1/events?before_seq=252&turns=25&max_bytes=1048576',
      expect.objectContaining({ headers: expect.objectContaining({ Accept: 'application/json' }) }),
    ),
  )
  expect(await screen.findByText('Older prompt')).toBeInTheDocument()
})

test('repeated leading-edge reaches grow the transcript without refetching loaded turns', async () => {
  const baseFetch = fetchMock({
    events: [
      event(9, 'user.message.completed', { text: 'Prompt five' }),
      event(10, 'agent.message.completed', { text: 'Answer five' }),
      event(11, 'user.message.completed', { text: 'Prompt six' }),
      event(12, 'agent.message.completed', { text: 'Answer six' }),
    ],
  })
  const fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = String(url)
    if (path === '/api/sessions/sess_1/events?before_seq=9&turns=25&max_bytes=1048576') {
      return jsonResponse({
        events: [
          event(5, 'user.message.completed', { text: 'Prompt three' }),
          event(6, 'agent.message.completed', { text: 'Answer three' }),
          event(7, 'user.message.completed', { text: 'Prompt four' }),
          event(8, 'agent.message.completed', { text: 'Answer four' }),
        ],
      })
    }
    if (path === '/api/sessions/sess_1/events?before_seq=5&turns=25&max_bytes=1048576') {
      return jsonResponse({
        events: [
          event(1, 'user.message.completed', { text: 'Prompt one' }),
          event(2, 'agent.message.completed', { text: 'Answer one' }),
          event(3, 'user.message.completed', { text: 'Prompt two' }),
          event(4, 'agent.message.completed', { text: 'Answer two' }),
        ],
      })
    }
    return baseFetch(url, init)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await waitFor(() => expect(screen.getByText('Prompt five')).toBeInTheDocument())
  expect(screen.getByText('Prompt six')).toBeInTheDocument()
  const log = screen.getByRole('log', { name: 'Chat messages' })

  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log, { target: { scrollTop: 0 } })
  await waitFor(() => expect(screen.getByText('Prompt three')).toBeInTheDocument())
  expect(screen.getByText('Prompt four')).toBeInTheDocument()

  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log, { target: { scrollTop: 0 } })
  await waitFor(() => expect(screen.getByText('Prompt one')).toBeInTheDocument())
  expect(screen.getByText('Prompt two')).toBeInTheDocument()
  expect(screen.getByText('Prompt six')).toBeInTheDocument()
  expect(fetch.mock.calls.filter(([url]) => String(url).includes('events?tail=true&turns=50&max_bytes=2097152'))).toHaveLength(1)
  expect(fetch.mock.calls.filter(([url]) => String(url).includes('events?before_seq=')).map(([url]) => String(url))).toEqual([
    '/api/sessions/sess_1/events?before_seq=9&turns=25&max_bytes=1048576',
    '/api/sessions/sess_1/events?before_seq=5&turns=25&max_bytes=1048576',
  ])
})

test('submitting a new prompt resumes the tail without discarding loaded older turns', async () => {
  const user = userEvent.setup()
  const baseFetch = fetchMock({
    events: [
      event(5, 'user.message.completed', { text: 'Prompt three' }),
      event(6, 'agent.message.completed', { text: 'Answer three' }),
      event(7, 'user.message.completed', { text: 'Prompt four' }),
      event(8, 'agent.message.completed', { text: 'Answer four' }),
    ],
  })
  const fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    if (String(url) === '/api/sessions/sess_1/events?before_seq=5&turns=25&max_bytes=1048576') {
      return jsonResponse({
        events: [
          event(1, 'user.message.completed', { text: 'Prompt one' }),
          event(2, 'agent.message.completed', { text: 'Answer one' }),
          event(3, 'user.message.completed', { text: 'Prompt two' }),
          event(4, 'agent.message.completed', { text: 'Answer two' }),
        ],
      })
    }
    return baseFetch(url, init)
  })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await waitFor(() => expect(screen.getByText('Prompt three')).toBeInTheDocument())
  const log = screen.getByRole('log', { name: 'Chat messages' })
  fireEvent.wheel(log, { deltaY: -100 })
  fireEvent.scroll(log, { target: { scrollTop: 0 } })
  await waitFor(() => expect(screen.getByText('Prompt one')).toBeInTheDocument())
  const source = await findEventSource('/api/sessions/activity/stream')

  await user.type(screen.getByPlaceholderText('Ask the agent to work on this repository...'), 'Fresh prompt{Enter}')

  expect(fetch.mock.calls.filter(([url]) => String(url) === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152')).toHaveLength(1)
  expect(screen.getAllByText('Fresh prompt')).toHaveLength(1)
  const submitCall = fetch.mock.calls.find(([url]) => String(url) === '/api/sessions/sess_1/messages')
  const submitBody = JSON.parse(String(submitCall?.[1]?.body)) as { client_submission_id: string }
  act(() => {
    source.emit(event(9, 'user.message.completed', {
      text: 'Fresh prompt',
      client_submission_id: submitBody.client_submission_id,
    }))
  })
  expect(screen.getAllByText('Fresh prompt')).toHaveLength(1)
  expect(screen.queryByRole('button', { name: 'Scroll to latest and resume auto-scroll' })).not.toBeInTheDocument()
  await waitFor(() => expect(screen.getAllByText('Fresh prompt')).toHaveLength(1))
  expect(screen.getByText('Prompt one')).toBeInTheDocument()
  expect(screen.getByText('Prompt two')).toBeInTheDocument()
  expect(screen.getByText('Prompt three')).toBeInTheDocument()
})

test('desktop pane resize handles update persisted widths', async () => {
  Object.defineProperty(window, 'innerWidth', {
    configurable: true,
    value: 1600,
  })

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))

  fireEvent.keyDown(screen.getByRole('separator', { name: 'Resize sessions pane', hidden: true }), {
    key: 'ArrowRight',
  })
  fireEvent.keyDown(screen.getByRole('separator', { name: 'Resize details pane', hidden: true }), {
    key: 'ArrowLeft',
  })

  await waitFor(() => {
    const stored = JSON.parse(window.localStorage.getItem('gorchestra.pane-widths.v1') ?? '{}') as {
      left?: number
      right?: number
    }
    expect(stored.left).toBe(364)
    expect(stored.right).toBe(360)
  })
})

test('file browser opens the inline files view', async () => {
  const user = userEvent.setup()
  vi.stubGlobal('fetch', fetchMock({ fileEntry: true }))

  render(<App />)

  await user.click(await screen.findByRole('button', { name: /main\.go/i }))

  const fileViewer = await screen.findByRole('region', { name: 'File viewer: main.go' })
  const filesFrame = fileViewer.closest('.host-console-frame')
  const fileSearch = filesFrame?.querySelector<HTMLInputElement>('input[aria-label="Search files and contents"]')
  const fileBrowser = fileSearch?.closest('section')
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/files/main.go'))
  expect(fileViewer).toBeInTheDocument()
  expect(fileViewer.closest('.mobile-file-viewer-panel')).toBeTruthy()
  expect(within(fileViewer).getAllByText('main.go')).toHaveLength(1)
  expect(within(fileViewer).getByLabelText('File editor')).toHaveValue('package main\n')
  expect(fileBrowser).toHaveClass('hidden')
  expect(fileSearch).toBeInTheDocument()
  expect(screen.getAllByRole('button', { name: 'Show files' }).some((button) => button.getAttribute('aria-pressed') === 'true')).toBe(
    true,
  )

  const closeButton = within(fileViewer).getByRole('button', { name: 'Close file viewer' })
  expect(closeButton).not.toHaveClass('lg:hidden')
  await user.click(closeButton)

  await waitFor(() => expect(screen.queryByRole('region', { name: 'File viewer: main.go' })).not.toBeInTheDocument())
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/files'))
  expect(filesFrame?.querySelector('input[aria-label="Search files and contents"]')).toBe(fileSearch)
  expect(fileBrowser).not.toHaveClass('hidden')
  expect(screen.getByText('No file selected')).toBeInTheDocument()
})

test('file browser renders markdown files as markdown', async () => {
  const user = userEvent.setup()
  vi.stubGlobal(
    'fetch',
    fetchMock({ fileEntry: true, fileName: 'README.md', fileContent: '# Project Notes\n\n- Ship it' }),
  )

  render(<App />)

  await user.click(await screen.findByRole('button', { name: /README\.md/i }))

  const fileViewer = await screen.findByRole('region', { name: 'File viewer: README.md' })
  expect(fileViewer).toBeInTheDocument()
  expect(within(fileViewer).getAllByText('README.md')).toHaveLength(1)
  expect(within(fileViewer).getByRole('heading', { name: 'Project Notes' })).toBeInTheDocument()
  expect(within(fileViewer).getByRole('listitem')).toHaveTextContent('Ship it')
})

test('file browser edit mode saves workspace files', async () => {
  const user = userEvent.setup()
  vi.stubGlobal('fetch', fetchMock({ fileEntry: true, fileName: 'README.md', fileContent: '# Project Notes\n' }))

  render(<App />)

  await user.click(await screen.findByRole('button', { name: /README\.md/i }))
  const fileViewer = await screen.findByRole('region', { name: 'File viewer: README.md' })
  await user.click(within(fileViewer).getByRole('button', { name: /edit/i }))

  const editor = within(fileViewer).getByLabelText('File editor')
  await user.clear(editor)
  await user.type(editor, '# Edited Notes\n\nSaved')
  await user.click(within(fileViewer).getByRole('button', { name: /^save$/i }))

  await waitFor(() => expect(within(fileViewer).getByText('Saved')).toBeInTheDocument())
  await user.click(within(fileViewer).getByRole('button', { name: /preview/i }))

  expect(within(fileViewer).getByRole('heading', { name: 'Edited Notes' })).toBeInTheDocument()
  expect(within(fileViewer).getAllByText('Saved').length).toBeGreaterThan(0)
})

test('file change diff actions open absolute paths in the file editor', async () => {
  const user = userEvent.setup()
  vi.stubGlobal(
    'fetch',
    fetchMock({
      fileName: 'src/main.go',
      fileContent: 'package main\n',
      events: [
        event(1, 'file.change.completed', {
          item_id: 'edit_1',
          paths: ['/repo/src/main.go:12'],
          changes: [
            {
              path: '/repo/src/main.go:12',
              patch: '@@ -1,2 +1,2 @@\n-old\n+new',
            },
          ],
        }),
      ],
    }),
  )

  render(<App />)

  await user.click(await screen.findByRole('button', { name: /expand main\.go/i }))
  await user.click(screen.getByRole('button', { name: 'Show in File Editor' }))

  const fileViewer = await screen.findByRole('region', { name: 'File viewer: src/main.go' })
  await waitFor(() => expect(window.location.pathname).toBe('/sessions/inspect-repo/files/src%2Fmain.go'))
  expect(within(fileViewer).getByLabelText('File editor')).toHaveValue('package main\n')
  expect(screen.getAllByRole('button', { name: 'Show files' }).some((button) => button.getAttribute('aria-pressed') === 'true')).toBe(
    true,
  )
})

test('streamed mutating git commands refresh the file browser', async () => {
  const fetch = fetchMock({ fileEntry: true })
  vi.stubGlobal('fetch', fetch)
  const requestedURLs = () => fetch.mock.calls.map(([url]) => String(url))

  render(<App />)

  await screen.findByRole('button', { name: /main\.go/i })
  await waitFor(() => expect(requestedURLs().filter((url) => url === '/api/sessions/sess_1/files')).toHaveLength(1))
  const sessionSource = await findEventSource('/api/sessions/activity/stream')

  act(() => {
    sessionSource.emit(
      event(41, 'tool.call.completed', {
        item_id: 'tool_1',
        item_type: 'commandExecution',
        command: "/bin/zsh -lc 'git pull --rebase'",
      }),
    )
  })

  await waitFor(() => expect(requestedURLs().filter((url) => url === '/api/sessions/sess_1/files')).toHaveLength(2))
})

test('codex session actions require dialog confirmation', async () => {
  const user = userEvent.setup()
  const codexSession: Session = { ...firstSession, agent_type: 'codex', provider_session_id: 'thread_1' }
  const fetch = fetchMock({ sessions: [codexSession, secondSession] })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await user.click(await screen.findByRole('button', { name: 'Compact Codex context' }))

  const dialog = await screen.findByRole('dialog', { name: 'Compact context?' })
  expect(dialog).toBeInTheDocument()
  expect(fetch).not.toHaveBeenCalledWith(
    '/api/sessions/sess_1/compact',
    expect.objectContaining({ method: 'POST' }),
  )

  await user.click(within(dialog).getByRole('button', { name: 'Compact' }))

  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/sess_1/compact',
      expect.objectContaining({ method: 'POST', headers: expect.objectContaining({ Accept: 'application/json' }) }),
    ),
  )
})

test('OpenCode clear requires dialog confirmation', async () => {
  const user = userEvent.setup()
  const openCodeSession: Session = { ...firstSession, agent_type: 'opencode', provider_session_id: 'ses_1' }
  const fetch = fetchMock({ sessions: [openCodeSession, secondSession] })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await user.click(await screen.findByRole('button', { name: 'Clear OpenCode context' }))

  const dialog = await screen.findByRole('dialog', { name: 'Clear context?' })
  expect(within(dialog).getByText(/Start a fresh OpenCode session/)).toBeInTheDocument()
  expect(fetch).not.toHaveBeenCalledWith(
    '/api/sessions/sess_1/clear',
    expect.objectContaining({ method: 'POST' }),
  )

  await user.click(within(dialog).getByRole('button', { name: 'Clear' }))

  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/sess_1/clear',
      expect.objectContaining({ method: 'POST', headers: expect.objectContaining({ Accept: 'application/json' }) }),
    ),
  )
})

test('archive requires dialog confirmation', async () => {
  const user = userEvent.setup()
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await user.click(await screen.findByRole('button', { name: 'Archive selected session' }))

  const dialog = await screen.findByRole('dialog', { name: 'Archive session?' })
  expect(dialog).toBeInTheDocument()
  expect(within(dialog).getByText('Inspect repo')).toBeInTheDocument()
  expect(fetch).not.toHaveBeenCalledWith(
    '/api/sessions/sess_1/archive',
    expect.objectContaining({ method: 'POST' }),
  )

  await user.click(within(dialog).getByRole('button', { name: 'Archive' }))

  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/sess_1/archive',
      expect.objectContaining({ method: 'POST', headers: expect.objectContaining({ Accept: 'application/json' }) }),
    ),
  )
  expect(await screen.findByRole('heading', { name: 'Your work at a glance' })).toBeInTheDocument()
  expect(window.location.pathname).toBe('/')
})

test('archiving a newly created session releases the dialog and keeps session navigation interactive', async () => {
  const user = userEvent.setup()
  const fetch = fetchMock()
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await user.click(await screen.findByRole('button', { name: 'Create session' }))
  const createDialog = await screen.findByRole('dialog', { name: 'Create session' })
  await user.type(within(createDialog).getByLabelText('Title'), 'Temporary archive test')
  await user.click(within(createDialog).getByRole('button', { name: 'Create' }))

  expect(await screen.findByRole('button', { name: 'Temporary archive test' })).toBeInTheDocument()
  await user.click(await screen.findByRole('button', { name: 'Archive selected session' }))
  const archiveDialog = await screen.findByRole('dialog', { name: 'Archive session?' })
  await user.click(within(archiveDialog).getByRole('button', { name: 'Archive' }))

  expect(await screen.findByRole('heading', { name: 'Your work at a glance' })).toBeInTheDocument()
  await waitFor(() => expect(document.body).not.toHaveStyle({ pointerEvents: 'none' }))
  await user.click(screen.getByRole('button', { name: 'Write docs' }))

  expect(window.location.pathname).toBe('/sessions/write-docs')
})

test('new child session action opens a prefilled dialog before creating', async () => {
  const user = userEvent.setup()
  const parentSession: Session = {
    ...firstSession,
    agent_type: 'codex',
    workspace_path: '/repo/parent',
    agent_options: { codex: { permission_policy: 'bypass', model: 'gpt-6' } },
  }
  const fetch = fetchMock({ sessions: [parentSession, secondSession] })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await waitFor(() => expect(screen.getAllByText('Inspect repo').length).toBeGreaterThan(0))
  const parentRow = document.querySelector('[data-session-id="sess_1"]')
  expect(parentRow).not.toBeNull()
  fireEvent.contextMenu(parentRow!, { clientX: 24, clientY: 32 })
  await user.click(await screen.findByRole('menuitem', { name: 'New child session' }))

  const dialog = await screen.findByRole('dialog', { name: 'Create child session' })
  expect(within(dialog).getByLabelText('Workspace')).toHaveValue('/repo/parent')
  expect(within(dialog).getByRole('radio', { name: 'Bypass' })).toHaveAttribute('aria-checked', 'true')
  expect(fetch.mock.calls.filter(([url, init]) => String(url) === '/api/sessions' && init?.method === 'POST')).toHaveLength(0)

  await user.click(within(dialog).getByRole('button', { name: 'Create' }))

  await waitFor(() => {
    const createCall = fetch.mock.calls.find(([url, init]) => String(url) === '/api/sessions' && init?.method === 'POST')
    expect(createCall).toBeDefined()
    expect(JSON.parse(String(createCall?.[1]?.body))).toEqual({
      agent_type: 'codex',
      agent_options: { codex: { permission_policy: 'bypass' } },
      parent_session_id: 'sess_1',
    })
  })
})

test('move under parent reorganizes an existing session without changing its configuration', async () => {
  const user = userEvent.setup()
  const parent: Session = { ...firstSession, id: 'sess_parent', title: 'Parent project' }
  const existing: Session = {
    ...secondSession,
    id: 'sess_existing',
    title: 'Existing project',
    workspace_path: '/repo/existing',
    agent_type: 'claude',
    agent_options: { claude: { permission_policy: 'deny' } },
  }
  const fetch = fetchMock({ sessions: [parent, existing] })
  vi.stubGlobal('fetch', fetch)
  window.history.replaceState({}, '', '/sessions/sess_parent')

  render(<App />)

  const existingRow = await waitFor(() => {
    const row = document.querySelector('[data-session-id="sess_existing"]')
    expect(row).not.toBeNull()
    return row as HTMLElement
  })
  fireEvent.contextMenu(existingRow, { clientX: 24, clientY: 32 })
  await user.click(await screen.findByRole('menuitem', { name: 'Move under parent…' }))
  const dialog = await screen.findByRole('dialog', { name: 'Move under parent' })
  await user.click(within(dialog).getByRole('radio', { name: /Parent project/ }))
  await user.click(within(dialog).getByRole('button', { name: 'Move' }))

  await waitFor(() => expect(existingRow).toHaveAttribute('data-parent-session-id', 'sess_parent'))
  const updateCall = fetch.mock.calls.find(([url, init]) =>
    String(url) === '/api/sessions/sess_existing' && init?.method === 'PATCH',
  )
  expect(JSON.parse(String(updateCall?.[1]?.body))).toEqual({ parent_session_id: 'sess_parent' })
  expect(existing.workspace_path).toBe('/repo/existing')
  expect(existing.agent_options).toEqual({ claude: { permission_policy: 'deny' } })
})

test('archived session uses restore confirmation', async () => {
  const user = userEvent.setup()
  const archivedSession: Session = {
    ...session('sess_3', 'Archived chat', '2026-06-12T16:00:30Z'),
    archived_at: '2026-06-12T16:05:00Z',
  }
  const fetch = fetchMock({ sessions: [firstSession, secondSession, archivedSession] })
  vi.stubGlobal('fetch', fetch)

  render(<App />)

  await user.click(await screen.findByRole('button', { name: 'Search' }))
  await user.type(screen.getByRole('textbox', { name: 'Search Threave' }), 'Archived')
  await user.click(await screen.findByRole('option', { name: /Archived chat/ }))
  await user.click(await screen.findByRole('button', { name: 'Restore selected session' }))

  const dialog = await screen.findByRole('dialog', { name: 'Restore session?' })
  expect(within(dialog).getByText('Archived chat')).toBeInTheDocument()
  expect(fetch).not.toHaveBeenCalledWith(
    '/api/sessions/sess_3/restore',
    expect.objectContaining({ method: 'POST' }),
  )

  await user.click(within(dialog).getByRole('button', { name: 'Restore' }))

  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      '/api/sessions/sess_3/restore',
      expect.objectContaining({ method: 'POST', headers: expect.objectContaining({ Accept: 'application/json' }) }),
    ),
  )
})

function fetchMock({
  fileEntry = false,
  fileName = 'main.go',
  fileContent = 'package main\n',
  events = [],
  submittedEvents = events,
  olderEvents = [],
  sessions = [firstSession, secondSession],
}: {
  fileEntry?: boolean
  fileName?: string
  fileContent?: string
  events?: AgentEvent[]
  submittedEvents?: AgentEvent[]
  olderEvents?: AgentEvent[]
  sessions?: Session[]
} = {}) {
  let currentContent = fileContent
  let recentEvents = events
  return vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = String(url)
    if (path === '/api/health') {
      return jsonResponse({ status: 'ok' })
    }
    if (path === '/api/sessions' && init?.method === 'POST') {
      const body = JSON.parse(String(init.body)) as { title?: string }
      const createdSession = session('sess_created', body.title || 'Untitled session', '2026-06-12T16:03:00Z')
      sessions.unshift(createdSession)
      return jsonResponse({ session_id: createdSession.id })
    }
    if (path === '/api/sessions?limit=50') {
      return jsonResponse({ sessions: sessions.filter((session) => !session.archived_at) })
    }
    if (path === '/api/sessions?limit=50&include_archived=true') {
      return jsonResponse({ sessions })
    }
    if (path === '/api/sessions/activity/watch' && init?.method === 'PUT') {
      const body = JSON.parse(String(init.body)) as { session_id?: string }
      return jsonResponse({ connected: true, session_id: body.session_id ?? '' })
    }
    if (path.startsWith('/api/search/stream?')) {
      const requestURL = new URL(path, 'http://localhost')
      const query = requestURL.searchParams.get('q')?.toLowerCase() ?? ''
      return jsonResponse({
        query,
        results: sessions
          .filter((session) => session.title.toLowerCase().includes(query))
          .map((session) => ({
            id: `session:${session.id}:0`,
            kind: 'session',
            scope: 'global',
            title: session.title,
            session_id: session.id,
            session_title: session.title,
            workspace_path: session.workspace_path,
            archived: Boolean(session.archived_at),
          })),
      })
    }
    const sessionMatch = path.match(/^\/api\/sessions\/([^/?]+)$/)
    if (sessionMatch) {
      const matchedSessionIndex = sessions.findIndex((session) => session.id === decodeURIComponent(sessionMatch[1]))
      const matchedSession = sessions[matchedSessionIndex]
      if (matchedSession) {
				if (init?.method === 'PATCH') {
					const body = JSON.parse(String(init.body)) as { pinned?: boolean; parent_session_id?: string }
					if (typeof body.pinned === 'boolean') {
						return jsonResponse({
							...matchedSession,
							pinned_at: body.pinned ? '2026-06-12T16:20:00Z' : null,
						})
					}
					if (typeof body.parent_session_id === 'string') {
						const parent = sessions.find((session) => session.id === body.parent_session_id)
						const updated = {
							...matchedSession,
							parent_session_id: body.parent_session_id || undefined,
							lineage_depth: parent ? (parent.lineage_depth ?? 0) + 1 : 0,
						}
						sessions[matchedSessionIndex] = updated
						return jsonResponse(updated)
					}
				}
        return jsonResponse(matchedSession)
      }
    }
    const attentionClearMatch = path.match(/^\/api\/sessions\/([^/?]+)\/notification-attention\/clear$/)
    if (attentionClearMatch && init?.method === 'POST') {
      const matchedSession = sessions.find((session) => session.id === decodeURIComponent(attentionClearMatch[1]))
      if (matchedSession) {
        return jsonResponse({ ...matchedSession, notification_attention_seq: undefined })
      }
    }
    if (path === '/api/sessions/notification-attention/clear' && init?.method === 'POST') {
      return jsonResponse({ cleared: true })
    }
    if (path === '/api/sessions/sess_1/clear' && init?.method === 'POST') {
      return jsonResponse({ session_id: 'sess_1', status: 'running' })
    }
    if (path === '/api/sessions/sess_1/compact' && init?.method === 'POST') {
      return jsonResponse({ session_id: 'sess_1', status: 'running' })
    }
    if (path === '/api/sessions/sess_1/messages' && init?.method === 'POST') {
      recentEvents = submittedEvents
      return jsonResponse({ session_id: 'sess_1', status: 'running' })
    }
    const archiveMatch = path.match(/^\/api\/sessions\/([^/?]+)\/archive$/)
    if (archiveMatch && init?.method === 'POST') {
      const matchedSession = sessions.find((session) => session.id === decodeURIComponent(archiveMatch[1]))
      if (matchedSession) {
        return jsonResponse({
          ...matchedSession,
          archived_at: '2026-06-12T16:05:00Z',
          updated_at: '2026-06-12T16:05:00Z',
        })
      }
    }
    const restoreMatch = path.match(/^\/api\/sessions\/([^/?]+)\/restore$/)
    if (restoreMatch && init?.method === 'POST') {
      const matchedSession = sessions.find((session) => session.id === decodeURIComponent(restoreMatch[1]))
      if (matchedSession) {
        return jsonResponse({
          ...matchedSession,
          archived_at: null,
          updated_at: '2026-06-12T16:06:00Z',
        })
      }
    }
    if (path === '/api/sessions/sess_1/events?tail=true&turns=50&max_bytes=2097152') {
      return jsonResponse({ events: recentEvents })
    }
    if (path === '/api/sessions/sess_2/events?tail=true&turns=50&max_bytes=2097152') {
      return jsonResponse({ events: [] })
    }
    if (/^\/api\/sessions\/[^/]+\/events\?tail=true&turns=50&max_bytes=2097152$/.test(path)) {
      return jsonResponse({ events: [] })
    }
    const consoleMatch = path.match(/^\/api\/sessions\/([^/?]+)\/console$/)
    if (consoleMatch) {
      const matchedSession = sessions.find((session) => session.id === decodeURIComponent(consoleMatch[1]))
      if (matchedSession) {
        return jsonResponse({ session_id: matchedSession.id, workspace_path: matchedSession.workspace_path, running: false })
      }
    }
    const hostMatch = path.match(/^\/api\/sessions\/([^/?]+)\/host$/)
    if (hostMatch) {
      return jsonResponse({
        session_id: decodeURIComponent(hostMatch[1]),
        config: {
          path: '.gorchestra/host.yaml',
          present: false,
          valid: false,
          stale: false,
          errors: [],
        },
        runtime: { status: 'stopped' },
        services: [],
        log_cursor: 0,
      })
    }
    const hostLogsMatch = path.match(/^\/api\/sessions\/([^/?]+)\/host\/logs$/)
    if (hostLogsMatch) {
      return jsonResponse({ chunks: [], first_seq: 0, last_seq: 0, truncated: false })
    }
    const schedulesMatch = path.match(/^\/api\/sessions\/([^/?]+)\/schedules$/)
    if (schedulesMatch) {
      return jsonResponse({ schedules: [] })
    }
    const repositorySkillsMatch = path.match(/^\/api\/sessions\/([^/?]+)\/repository-skills$/)
    if (repositorySkillsMatch) {
      return jsonResponse({ skills: [] })
    }
    if (path === '/api/user-skills') {
      return jsonResponse({ home_path: '/Users/tester', skills: [] })
    }
    if (path === '/api/sessions/sess_1/events?before_seq=252&turns=25&max_bytes=1048576') {
      return jsonResponse({ events: olderEvents })
    }
    if (path === '/api/sessions/sess_1/files') {
      return jsonResponse({
        root_path: '/repo',
        path: '',
        entries: fileEntry
          ? [
              {
                name: fileName,
                path: fileName,
                type: 'file',
                size_bytes: fileContent.length,
                modified_at: '2026-06-12T16:00:00Z',
              },
            ]
          : [],
      })
    }
    if (path === '/api/sessions/sess_2/files') {
      return jsonResponse({ root_path: '/repo', path: '', entries: [] })
    }
    if (path === `/api/sessions/sess_1/files/content?path=${encodeURIComponent(fileName)}`) {
      if (init?.method === 'PUT') {
        const body = JSON.parse(String(init.body)) as { content?: string }
        currentContent = body.content ?? ''
        return jsonResponse({
          name: fileName,
          path: fileName,
          size_bytes: currentContent.length,
          modified_at: '2026-06-12T16:00:00Z',
          content: currentContent,
          encoding: 'utf-8',
          truncated: false,
        })
      }
      return jsonResponse({
        name: fileName,
        path: fileName,
        size_bytes: currentContent.length,
        modified_at: '2026-06-12T16:00:00Z',
        content: currentContent,
        encoding: 'utf-8',
        truncated: false,
      })
    }
    throw new Error(`unexpected URL ${path}`)
  })
}

function emptyDashboardResponse() {
  return {
    generated_at: '2026-06-12T16:03:00Z',
    range: '30d',
    range_start: '2026-05-14T00:00:00Z',
    range_end: '2026-06-13T00:00:00Z',
    time_zone: 'UTC',
    bucket: 'day',
    summary: {
      runs: 0,
      completed_runs: 0,
      failed_runs: 0,
      cancelled_runs: 0,
      running_runs: 0,
      unknown_runs: 0,
      active_now: 0,
      success_rate: null,
      agent_runtime_ms: 0,
      tool_calls: 0,
      files_changed: 0,
      input_requests: 0,
      permission_requests: 0,
      workspaces: 0,
      agents: 0,
    },
    activity: [],
    workspaces: [],
    agents: [],
    usage: { tokens: 0, token_runs: 0, cost_runs: 0, eligible_runs: 0, costs: [] },
    outcomes: [
      { kind: 'commit', count: 0, passed: 0, failed: 0, reported: false },
      { kind: 'pull_request', count: 0, passed: 0, failed: 0, reported: false },
      { kind: 'test', count: 0, passed: 0, failed: 0, reported: false },
      { kind: 'delegation', count: 0, passed: 0, failed: 0, reported: false },
    ],
  }
}

class FakeEventSource {
  static instances: FakeEventSource[] = []

  url: string
  closed = false
  onopen: ((event: Event) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  private listeners = new Map<string, Array<(event: MessageEvent<string>) => void>>()

  constructor(url: string) {
    this.url = url
    FakeEventSource.instances.push(this)
    window.setTimeout(() => this.onopen?.(new Event('open')), 0)
  }

  addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    const nextListeners = this.listeners.get(type) ?? []
    nextListeners.push((event) => {
      if (typeof listener === 'function') {
        listener(event)
      } else {
        listener.handleEvent(event)
      }
    })
    this.listeners.set(type, nextListeners)
  }

  emit(event: AgentEvent) {
    const message = new MessageEvent(event.type, { data: JSON.stringify(event) })
    for (const listener of this.listeners.get(event.type) ?? []) {
      listener(message)
    }
  }

  emitControl(type: string, payload: unknown) {
    const message = new MessageEvent(type, { data: JSON.stringify(payload) })
    for (const listener of this.listeners.get(type) ?? []) {
      listener(message)
    }
  }

  fail() {
    this.onerror?.(new Event('error'))
  }

  close() { this.closed = true }
}

class FakeWebSocket {
  static OPEN = 1
  readyState = FakeWebSocket.OPEN
  private listeners = new Map<string, Set<(event: Event) => void>>()

  constructor() {
    window.setTimeout(() => this.dispatch('open', new Event('open')), 0)
  }

  addEventListener(type: string, listener: (event: Event) => void) {
    const listeners = this.listeners.get(type) ?? new Set()
    listeners.add(listener)
    this.listeners.set(type, listeners)
  }

  removeEventListener(type: string, listener: (event: Event) => void) {
    this.listeners.get(type)?.delete(listener)
  }

  send() {}
  close() {}

  private dispatch(type: string, event: Event) {
    for (const listener of this.listeners.get(type) ?? []) {
      listener(event)
    }
  }
}

class FakeResizeObserver {
  observe() {}
  disconnect() {}
}

async function findEventSource(urlPrefix: string) {
  await waitFor(() => expect(findExistingEventSource(urlPrefix)).toBeTruthy())
  return findExistingEventSource(urlPrefix)!
}

function findExistingEventSource(urlPrefix: string) {
  return FakeEventSource.instances.find((source) => source.url.startsWith(urlPrefix))
}

function matchMediaMock(query: string): MediaQueryList {
  return {
    media: query,
    matches: query === '(hover: hover) and (pointer: fine)',
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }
}

function faviconPath() {
  const href = document.querySelector<HTMLLinkElement>('link[rel~="icon"]')?.href
  return href ? new URL(href).pathname : ''
}

function session(id: string, title: string, updatedAt: string): Session {
  return {
    id,
    title,
    agent_type: 'fake',
    status: 'idle',
    workspace_path: '/repo',
    event_count: 0,
    tool_count: 0,
    created_at: '2026-06-12T16:00:00Z',
    updated_at: updatedAt,
    last_activity_at: updatedAt,
    completed_at: null,
    archived_at: null,
  }
}

function event(seq: number, type: string, payload: Record<string, unknown>, sessionID = 'sess_1'): AgentEvent {
  return {
    id: `evt_${seq}`,
    session_id: sessionID,
    seq,
    type,
    role: 'assistant',
    status: 'completed',
    payload,
    created_at: '2026-06-12T16:00:00Z',
  }
}

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}
