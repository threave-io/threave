import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { AgentEvent, Session } from '@/lib/api'
import { RunHealthRail } from '@/components/run-health-rail'
import { publishComposerActivity } from '@/lib/composer-activity'

const session: Session = {
  id: 'sess_1',
  title: 'Inspect repo',
  agent_type: 'fake',
  status: 'running',
  workspace_path: '/repo',
  event_count: 1209,
  tool_count: 1209,
  created_at: '2026-06-12T16:00:00Z',
  updated_at: '2026-06-12T16:01:00Z',
  completed_at: null,
  archived_at: null,
}

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(JSON.stringify({ root_path: '/repo', path: '', entries: [] }), {
          headers: { 'Content-Type': 'application/json' },
        }),
    ),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

test('run health rail shows metrics and active chat status without session identity', () => {
  const onToggleArchive = vi.fn(async () => undefined)

  render(
    <RunHealthRail
      session={session}
      events={[
        event(1, 'user.message.completed', 'user', 'completed', { text: 'Run checks' }),
        event(2, 'tool.call.started', 'assistant', 'started', { item_id: 'tool_1', command: 'bun test' }),
        event(3, 'tool.call.completed', 'assistant', 'completed', { item_id: 'tool_1', output: 'ok' }),
        event(4, 'agent.message.completed', 'assistant', 'completed', { text: 'Done' }),
      ]}
      streamState="connected"
      streamError=""
      onToggleArchive={onToggleArchive}
    />,
  )

  expect(screen.queryByText('Session')).not.toBeInTheDocument()
  expect(screen.queryByText('Inspect repo')).not.toBeInTheDocument()
  expect(screen.queryByText('sess_1')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Copy session id' })).not.toBeInTheDocument()
  expect(screen.getByRole('img', { name: 'Active chat: Running' })).toHaveClass(
    'animate-pulse',
    'bg-[hsl(var(--success))]',
  )
  expect(screen.queryByText('Running')).not.toBeInTheDocument()
  expect(screen.queryByText('fake')).not.toBeInTheDocument()
  expect(screen.queryByRole('tab', { name: /chat/i })).not.toBeInTheDocument()
  expect(screen.queryByRole('tab', { name: /debug/i })).not.toBeInTheDocument()
  expect(screen.getByText('Events')).toBeInTheDocument()
  expect(screen.getByText('Tools')).toBeInTheDocument()
  expect(screen.getByText('Tokens')).toBeInTheDocument()
  expect(screen.getAllByText('1.2k')).toHaveLength(2)
  expect(screen.getByText('Agent message')).toBeInTheDocument()
  expect(screen.queryByText('Connection')).not.toBeInTheDocument()
  expect(screen.queryByText('Live')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Refresh files' })).toBeInTheDocument()
  expect(screen.getByTestId('workspace-file-scroll-area')).toHaveClass('subtle-scrollbar')
  expect(screen.queryByRole('button', { name: /cancel run/i })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /theme/i })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Archive selected session' })).toBeDisabled()
})

test('run health rail shows loading while a routed session resolves', () => {
  render(
    <RunHealthRail
      session={null}
      resolvingSessionID="sess_1"
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  expect(screen.getAllByText('Loading session')).toHaveLength(2)
  expect(screen.queryByText('No session selected')).not.toBeInTheDocument()
  expect(screen.queryByText('Select a session to browse files.')).not.toBeInTheDocument()
})

test('run health rail can hide file browsing for compact mobile details', () => {
  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      showUtilityContent={false}
      onToggleArchive={async () => undefined}
    />,
  )

  expect(screen.getByText('Activity')).toBeInTheDocument()
  expect(screen.queryByText('Files')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Refresh files' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /Rail content:/ })).not.toBeInTheDocument()
})

test('the hidden desktop utility does no file work at mobile widths', () => {
  const previousWidth = window.innerWidth
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: 767 })
  const fetchMock = vi.mocked(fetch)
  fetchMock.mockClear()

  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      contentMode="files"
      onToggleArchive={async () => undefined}
    />,
  )

  expect(screen.queryByRole('button', { name: /Rail content:/ })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Refresh files' })).not.toBeInTheDocument()
  expect(fetchMock).not.toHaveBeenCalled()
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: previousWidth })
})

test('run health rail changes its configurable middle content from the activity header', async () => {
  const user = userEvent.setup()
  const onContentModeChange = vi.fn()
  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      contentMode="files"
      onContentModeChange={onContentModeChange}
      onToggleArchive={async () => undefined}
    />,
  )

  await user.click(screen.getByRole('button', { name: 'Rail content: Files' }))
  await user.click(screen.getByRole('menuitemradio', { name: 'Blocks' }))

  expect(onContentModeChange).toHaveBeenCalledWith('blocks')
})

test('run health rail picker offers the signal field', async () => {
  const user = userEvent.setup()
  const onContentModeChange = vi.fn()
  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      contentMode="files"
      onContentModeChange={onContentModeChange}
      onToggleArchive={async () => undefined}
    />,
  )

  await user.click(screen.getByRole('button', { name: 'Rail content: Files' }))
  await user.click(screen.getByRole('menuitemradio', { name: 'Signal field' }))

  expect(onContentModeChange).toHaveBeenCalledWith('signal-field')
})

test('signal field renders against the live activity window', () => {
  const getContext = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
  render(
    <RunHealthRail
      session={session}
      events={[]}
      activityEvents={[event(42, 'tool.call.started', 'assistant', 'started', { command: 'bun test' })]}
      streamState="connected"
      streamError=""
      contentMode="signal-field"
      onToggleArchive={async () => undefined}
    />,
  )

  expect(screen.getByRole('region', { name: 'Signal field visualizer' })).toBeInTheDocument()
  expect(screen.getByRole('img', { name: 'Ambient visualization of current session activity. Live.' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Refresh files' })).not.toBeInTheDocument()
  getContext.mockRestore()
})

test('signal field reports and renders sustained thinking activity', () => {
  const getContext = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
  render(
    <RunHealthRail
      session={session}
      events={[]}
      activityEvents={[event(42, 'agent.thinking.started', 'assistant', 'started', { item_id: 'thought_1' })]}
      streamState="connected"
      streamError=""
      contentMode="signal-field"
      onToggleArchive={async () => undefined}
    />,
  )

  expect(
    screen.getByRole('img', { name: 'Ambient visualization of current session activity. Thinking.' }),
  ).toBeInTheDocument()
  expect(screen.getByText('Thinking')).toHaveAttribute('data-state', 'thinking')
  getContext.mockRestore()
})

test('signal field reports local typing immediately', () => {
  const getContext = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      contentMode="signal-field"
      onToggleArchive={async () => undefined}
    />,
  )

  act(() => publishComposerActivity('sess_1', 1))

  expect(
    screen.getByRole('img', { name: 'Ambient visualization of current session activity. Typing.' }),
  ).toBeInTheDocument()
  expect(screen.getByText('Typing')).toHaveAttribute('data-state', 'typing')
  getContext.mockRestore()
})

test('blank rail content keeps operational panels and removes the flexible utility', () => {
  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      contentMode="blank"
      onToggleArchive={async () => undefined}
    />,
  )

  expect(screen.getByText('Activity')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Archive selected session' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Refresh files' })).not.toBeInTheDocument()
  expect(screen.queryByRole('navigation', { name: 'Conversation map' })).not.toBeInTheDocument()
  expect(screen.queryByRole('region', { name: 'Blocks game' })).not.toBeInTheDocument()
})

test('conversation map receives the rail history controls', async () => {
  const user = userEvent.setup()
  const onLoadOlderEvents = vi.fn()
  const onSelectConversationSeq = vi.fn()
  render(
    <RunHealthRail
      session={session}
      events={[event(1, 'user.message.completed', 'user', 'completed', { text: 'Earlier prompt' })]}
      streamState="connected"
      streamError=""
      contentMode="conversation-map"
      hasOlderEvents
      onLoadOlderEvents={onLoadOlderEvents}
      onSelectConversationSeq={onSelectConversationSeq}
      onToggleArchive={async () => undefined}
    />,
  )

  await user.click(screen.getByRole('button', { name: 'Load earlier conversation turns' }))
  await user.click(screen.getByRole('button', { name: /Open User message · #1/ }))

  expect(onLoadOlderEvents).toHaveBeenCalledOnce()
  expect(onSelectConversationSeq).toHaveBeenCalledWith(1)
})

test('run health rail archives an idle session from the slice action', async () => {
  const user = userEvent.setup()
  const onToggleArchive = vi.fn(async () => undefined)

  render(
    <RunHealthRail
      session={{ ...session, status: 'idle' }}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={onToggleArchive}
    />,
  )

  await user.click(screen.getByRole('button', { name: 'Archive selected session' }))

  expect(onToggleArchive).toHaveBeenCalledOnce()
})

test('run health rail offers restore for archived sessions', async () => {
  const user = userEvent.setup()
  const onToggleArchive = vi.fn(async () => undefined)

  render(
    <RunHealthRail
      session={{ ...session, status: 'idle', archived_at: '2026-06-12T16:05:00Z' }}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={onToggleArchive}
    />,
  )

  await user.click(screen.getByRole('button', { name: 'Restore selected session' }))

  expect(onToggleArchive).toHaveBeenCalledOnce()
  expect(screen.getByRole('button', { name: 'Restore selected session' })).toHaveTextContent('Restore')
})

test.each(['codex', 'claude'] as const)('run health rail exposes %s clear and compact actions', async (agentType) => {
  const providerLabel = agentType === 'claude' ? 'Claude' : 'Codex'
  const user = userEvent.setup()
  const onClear = vi.fn(async () => undefined)
  const onCompact = vi.fn(async () => undefined)

  render(
    <RunHealthRail
      session={{ ...session, agent_type: agentType, status: 'idle', provider_session_id: 'thread_1' }}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
      onClear={onClear}
      onCompact={onCompact}
    />,
  )

  const tokenPanel = screen.getByText('No token usage yet').closest('section')
  expect(tokenPanel).toBeInTheDocument()
  expect(within(tokenPanel as HTMLElement).getByText('No token usage yet')).toBeInTheDocument()

  await user.click(within(tokenPanel as HTMLElement).getByRole('button', { name: `Clear ${providerLabel} context` }))
  await user.click(within(tokenPanel as HTMLElement).getByRole('button', { name: `Compact ${providerLabel} context` }))

  expect(onClear).toHaveBeenCalledOnce()
  expect(onCompact).toHaveBeenCalledOnce()
})

test.each(['codex', 'claude'] as const)('run health rail disables compact until a %s session exists', (agentType) => {
  const providerLabel = agentType === 'claude' ? 'Claude' : 'Codex'
  render(
    <RunHealthRail
      session={{ ...session, agent_type: agentType, status: 'idle', provider_session_id: undefined }}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  expect(screen.getByRole('button', { name: `Clear ${providerLabel} context` })).toBeEnabled()
  expect(screen.getByRole('button', { name: `Compact ${providerLabel} context` })).toBeDisabled()
})

test('run health rail exposes clear without compact for OpenCode sessions', async () => {
  const user = userEvent.setup()
  const onClear = vi.fn(async () => undefined)

  render(
    <RunHealthRail
      session={{ ...session, agent_type: 'opencode', status: 'idle', provider_session_id: 'ses_1' }}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
      onClear={onClear}
    />,
  )

  const tokenPanel = screen.getByText('No token usage yet').closest('section')
  expect(tokenPanel).toBeInTheDocument()
  await user.click(within(tokenPanel as HTMLElement).getByRole('button', { name: 'Clear OpenCode context' }))

  expect(onClear).toHaveBeenCalledOnce()
  expect(screen.queryByRole('button', { name: /Compact .* context/ })).not.toBeInTheDocument()
})

test('run health rail latest event shows provider event type', () => {
  render(
    <RunHealthRail
      session={session}
      events={[event(1, 'provider.codex.event', 'system', 'completed', { provider_event_type: 'turn/completed' })]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  expect(screen.getByText('turn/completed')).toBeInTheDocument()
  expect(screen.queryByText('provider.codex.event')).not.toBeInTheDocument()
})

test('run health rail shows latest token usage summary', () => {
  render(
    <RunHealthRail
      session={session}
      events={[
        event(1, 'provider.codex.event', 'system', 'completed', {
          provider: 'codex',
          provider_event_type: 'thread/tokenUsage/updated',
          raw: tokenUsageRaw(),
        }),
      ]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  expect(screen.getAllByText('Tokens')).toHaveLength(2)
  expect(screen.getByText('5%')).toBeInTheDocument()
  expect(screen.getByText('14k / 258k current')).toBeInTheDocument()
  expect(screen.getByText('14k cumulative')).toBeInTheDocument()
  expect(screen.getByText('Input')).toBeInTheDocument()
  expect(screen.getByText('Output')).toBeInTheDocument()
  expect(screen.getByText('4.5k cached (32%)')).toBeInTheDocument()
  expect(screen.getByText('0 reasoning')).toBeInTheDocument()
})

test('run health rail shows lifetime session tokens across cleared contexts', () => {
  render(
    <RunHealthRail
      session={{ ...session, agent_type: 'codex', token_count: 614_805_054 }}
      events={[
        event(1, 'provider.codex.event', 'system', 'completed', {
          provider: 'codex',
          provider_event_type: 'thread/tokenUsage/updated',
          session_total_tokens: 614_805_054,
          raw: tokenUsageRaw(),
        }),
      ]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  const activityPanel = screen.getByText('Activity').closest('section')
  expect(within(activityPanel as HTMLElement).getByText('Tokens')).toBeInTheDocument()
  expect(within(activityPanel as HTMLElement).getByText('614.8M')).toBeInTheDocument()
  expect(screen.getByText('614.8M cumulative')).toBeInTheDocument()
})

test('run health rail shows OpenCode context usage without token breakdown rows', () => {
  render(
    <RunHealthRail
      session={{ ...session, agent_type: 'opencode' }}
      events={[
        event(1, 'provider.opencode.event', 'system', 'completed', {
          provider: 'opencode',
          provider_event_type: 'usage_update',
          raw_update: {
            sessionUpdate: 'usage_update',
            used: 53_000,
            size: 200_000,
            cost: { amount: 0.1234, currency: 'USD' },
          },
        }),
      ]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  expect(screen.getAllByText('Tokens')).toHaveLength(2)
  expect(screen.getByText('27%')).toBeInTheDocument()
  expect(screen.getByText('53k / 200k current')).toBeInTheDocument()
  expect(screen.getByText('$0.1234 cost')).toBeInTheDocument()
  expect(screen.queryByText('Input')).not.toBeInTheDocument()
  expect(screen.queryByText('Output')).not.toBeInTheDocument()
  expect(screen.queryByText(/cached/)).not.toBeInTheDocument()
  expect(screen.queryByText(/reasoning/)).not.toBeInTheDocument()
})

test('run health rail active chat dot shows disconnected state', () => {
  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="disconnected"
      streamError="lost connection"
      onToggleArchive={async () => undefined}
    />,
  )

  expect(screen.getByRole('img', { name: 'Active chat: Disconnected' })).toHaveClass('bg-destructive')
  expect(screen.queryByText('lost connection')).not.toBeInTheDocument()
})

test('run health rail file explorer refresh reloads the current folder', async () => {
  const user = userEvent.setup()
  const fetchMock = vi.fn(async (url: RequestInfo | URL) => {
    switch (String(url)) {
      case '/api/sessions/sess_1/files':
        return jsonResponse({
          root_path: '/repo',
          path: '',
          entries: [
            {
              name: 'src',
              path: 'src',
              type: 'directory',
              size_bytes: 0,
              modified_at: '2026-06-12T16:00:00Z',
            },
          ],
        })
      case '/api/sessions/sess_1/files?path=src':
        return jsonResponse({
          root_path: '/repo',
          path: 'src',
          entries: [
            {
              name: 'nested',
              path: 'src/nested',
              type: 'directory',
              size_bytes: 0,
              modified_at: '2026-06-12T16:00:00Z',
            },
          ],
        })
      default:
        throw new Error(`unexpected URL ${String(url)}`)
    }
  })
  vi.stubGlobal('fetch', fetchMock)
  const requestedURLs = () => fetchMock.mock.calls.map(([url]) => String(url))

  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  await screen.findByRole('button', { name: 'src' })
  await user.click(screen.getByRole('button', { name: 'Refresh files' }))
  await waitFor(() => expect(requestedURLs().filter((url) => url === '/api/sessions/sess_1/files')).toHaveLength(2))

  await user.click(screen.getByRole('button', { name: 'src' }))
  await screen.findByRole('button', { name: 'nested' })
  await user.click(screen.getByRole('button', { name: 'Refresh files' }))
  await waitFor(() =>
    expect(requestedURLs().filter((url) => url === '/api/sessions/sess_1/files?path=src')).toHaveLength(2),
  )
})

test('run health rail file explorer external refresh preserves the current folder', async () => {
  const user = userEvent.setup()
  const fetchMock = vi.fn(async (url: RequestInfo | URL) => {
    switch (String(url)) {
      case '/api/sessions/sess_1/files':
        return jsonResponse({
          root_path: '/repo',
          path: '',
          entries: [
            {
              name: 'src',
              path: 'src',
              type: 'directory',
              size_bytes: 0,
              modified_at: '2026-06-12T16:00:00Z',
            },
          ],
        })
      case '/api/sessions/sess_1/files?path=src':
        return jsonResponse({
          root_path: '/repo',
          path: 'src',
          entries: [
            {
              name: 'main.go',
              path: 'src/main.go',
              type: 'file',
              size_bytes: 10,
              modified_at: '2026-06-12T16:00:00Z',
            },
          ],
        })
      default:
        throw new Error(`unexpected URL ${String(url)}`)
    }
  })
  vi.stubGlobal('fetch', fetchMock)
  const requestedURLs = () => fetchMock.mock.calls.map(([url]) => String(url))
  const renderRail = (fileRefreshKey: number) => (
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      fileRefreshKey={fileRefreshKey}
      onToggleArchive={async () => undefined}
    />
  )

  const { rerender } = render(renderRail(0))

  await user.click(await screen.findByRole('button', { name: 'src' }))
  await screen.findByRole('button', { name: 'main.go' })
  await waitFor(() =>
    expect(requestedURLs().filter((url) => url === '/api/sessions/sess_1/files?path=src')).toHaveLength(1),
  )

  rerender(renderRail(1))

  await waitFor(() =>
    expect(requestedURLs().filter((url) => url === '/api/sessions/sess_1/files?path=src')).toHaveLength(2),
  )
})

test('run health rail file explorer uploads files to the current folder', async () => {
  const user = userEvent.setup()
  let uploaded = false
  const fetchMock = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    switch (String(url)) {
      case '/api/sessions/sess_1/files':
        return jsonResponse({
          root_path: '/repo',
          path: '',
          entries: [
            {
              name: 'src',
              path: 'src',
              type: 'directory',
              size_bytes: 0,
              modified_at: '2026-06-12T16:00:00Z',
            },
          ],
        })
      case '/api/sessions/sess_1/files?path=src':
        return jsonResponse({
          root_path: '/repo',
          path: 'src',
          entries: uploaded
            ? [
                {
                  name: 'notes.txt',
                  path: 'src/notes.txt',
                  type: 'file',
                  size_bytes: 5,
                  modified_at: '2026-06-12T16:00:00Z',
                },
              ]
            : [],
        })
      case '/api/sessions/sess_1/files/upload?path=src': {
        expect(init?.method).toBe('POST')
        expect(init?.body).toBeInstanceOf(FormData)
        expect((init?.body as FormData).get('files')).toEqual(expect.objectContaining({ name: 'notes.txt' }))
        expect(init?.headers).not.toHaveProperty('Content-Type')
        uploaded = true
        return jsonResponse({ files: [] }, 201)
      }
      default:
        throw new Error(`unexpected URL ${String(url)}`)
    }
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  await user.click(await screen.findByRole('button', { name: 'src' }))
  await user.upload(screen.getByLabelText('Select files to upload'), new File(['notes'], 'notes.txt'))

  expect(await screen.findByRole('button', { name: 'notes.txt' })).toBeInTheDocument()
})

test('run health rail file explorer shows git file summary counts', async () => {
  const fetchMock = vi.fn(async (url: RequestInfo | URL) => {
    if (String(url) === '/api/sessions/sess_1/files') {
      return jsonResponse({
        root_path: '/repo',
        path: '',
        git_summary: { branch: 'feature/files-pill', added: 2, modified: 3, deleted: 1 },
        entries: [],
      })
    }
    throw new Error(`unexpected URL ${String(url)}`)
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  const summary = await screen.findByLabelText('Git file summary')
  expect(within(summary).getByText('feature/files-pill')).toBeInTheDocument()
  expect(within(summary).getByText('+2')).toBeInTheDocument()
  expect(within(summary).getByText('~3')).toBeInTheDocument()
  expect(within(summary).getByText('-1')).toBeInTheDocument()
})

test('run health rail file explorer dot folders hide at root and navigate from subfolders', async () => {
  const user = userEvent.setup()
  const fetchMock = vi.fn(async (url: RequestInfo | URL) => {
    switch (String(url)) {
      case '/api/sessions/sess_1/files':
        return jsonResponse({
          root_path: '/repo',
          path: '',
          entries: [
            {
              name: 'src',
              path: 'src',
              type: 'directory',
              size_bytes: 0,
              modified_at: '2026-06-12T16:00:00Z',
            },
          ],
        })
      case '/api/sessions/sess_1/files?path=src':
        return jsonResponse({
          root_path: '/repo',
          path: 'src',
          entries: [
            {
              name: 'nested',
              path: 'src/nested',
              type: 'directory',
              size_bytes: 0,
              modified_at: '2026-06-12T16:00:00Z',
            },
          ],
        })
      case '/api/sessions/sess_1/files?path=src%2Fnested':
        return jsonResponse({
          root_path: '/repo',
          path: 'src/nested',
          entries: [],
        })
      default:
        throw new Error(`unexpected URL ${String(url)}`)
    }
  })
  vi.stubGlobal('fetch', fetchMock)
  const requestedURLs = () => fetchMock.mock.calls.map(([url]) => String(url))

  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  expect(await screen.findByRole('button', { name: 'src' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Go to parent folder' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Go to workspace root' })).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'src' }))
  expect(screen.getByRole('button', { name: 'Go to parent folder' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Go to workspace root' })).toBeInTheDocument()

  await user.click(await screen.findByRole('button', { name: 'nested' }))
  await waitFor(() => expect(requestedURLs()).toContain('/api/sessions/sess_1/files?path=src%2Fnested'))

  await user.click(screen.getByRole('button', { name: 'Go to parent folder' }))
  await waitFor(() =>
    expect(requestedURLs().filter((url) => url === '/api/sessions/sess_1/files?path=src')).toHaveLength(1),
  )

  await user.click(screen.getByRole('button', { name: 'Go to workspace root' }))
  await waitFor(() => expect(requestedURLs().filter((url) => url === '/api/sessions/sess_1/files')).toHaveLength(1))
  expect(screen.queryByRole('button', { name: 'Go to parent folder' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Go to workspace root' })).not.toBeInTheDocument()
})

test('run health rail file explorer searches file contents with snippets', async () => {
  const user = userEvent.setup()
  const fetchMock = vi.fn(async (url: RequestInfo | URL) => {
    switch (String(url)) {
      case '/api/sessions/sess_1/files':
        return jsonResponse({ root_path: '/repo', path: '', entries: [] })
      case '/api/sessions/sess_1/files/search?q=needle':
        return jsonResponse({
          query: 'needle',
          path: '',
          results: [
            {
              name: 'main.go',
              path: 'src/main.go',
              type: 'file',
              size_bytes: 48,
              modified_at: '2026-06-12T16:00:00Z',
              match_type: 'content',
              line_number: 4,
              line_text: 'println("needle")',
            },
          ],
        })
      default:
        throw new Error(`unexpected URL ${String(url)}`)
    }
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  await user.type(screen.getByRole('textbox', { name: 'Search files and contents' }), 'needle')

  expect(await screen.findByRole('button', { name: /main\.go/i })).toBeInTheDocument()
  expect(screen.getByText('src/main.go:4 println("needle")')).toBeInTheDocument()
})

test('run health rail file explorer clears search and restores the file list', async () => {
  const user = userEvent.setup()
  const fetchMock = vi.fn(async (url: RequestInfo | URL) => {
    switch (String(url)) {
      case '/api/sessions/sess_1/files':
        return jsonResponse({
          root_path: '/repo',
          path: '',
          entries: [
            {
              name: 'README.md',
              path: 'README.md',
              type: 'file',
              size_bytes: 128,
              modified_at: '2026-06-12T16:00:00Z',
            },
          ],
        })
      case '/api/sessions/sess_1/files/search?q=needle':
        return jsonResponse({
          query: 'needle',
          path: '',
          results: [
            {
              name: 'main.go',
              path: 'src/main.go',
              type: 'file',
              size_bytes: 48,
              modified_at: '2026-06-12T16:00:00Z',
              match_type: 'content',
              line_number: 4,
              line_text: 'println("needle")',
            },
          ],
        })
      default:
        throw new Error(`unexpected URL ${String(url)}`)
    }
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
    />,
  )

  expect(await screen.findByRole('button', { name: /README\.md/i })).toBeInTheDocument()

  const searchInput = screen.getByRole('textbox', { name: 'Search files and contents' })
  await user.type(searchInput, 'needle')

  expect(await screen.findByRole('button', { name: /main\.go/i })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /README\.md/i })).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Clear file search' }))

  expect(searchInput).toHaveValue('')
  expect(screen.queryByRole('button', { name: /main\.go/i })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: /README\.md/i })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Clear file search' })).not.toBeInTheDocument()
})

test('run health rail file explorer sends file content to the viewer', async () => {
  const user = userEvent.setup()
  const onOpenFile = vi.fn()
  const fetchMock = vi.fn(async (url: RequestInfo | URL) => {
    if (String(url) === '/api/sessions/sess_1/files') {
      return jsonResponse({
        root_path: '/repo',
        path: '',
        entries: [
          {
            name: 'main.go',
            path: 'main.go',
            type: 'file',
            size_bytes: 13,
            modified_at: '2026-06-12T16:00:00Z',
            git_status: 'modified',
          },
        ],
      })
    }
    if (String(url) === '/api/sessions/sess_1/files/content?path=main.go') {
      return jsonResponse({
        name: 'main.go',
        path: 'main.go',
        size_bytes: 13,
        modified_at: '2026-06-12T16:00:00Z',
        content: 'package main\n',
        encoding: 'utf-8',
        truncated: false,
        git_status: 'modified',
      })
    }
    throw new Error(`unexpected URL ${String(url)}`)
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <RunHealthRail
      session={session}
      events={[]}
      streamState="connected"
      streamError=""
      onToggleArchive={async () => undefined}
      onOpenFile={onOpenFile}
    />,
  )

  await user.click(await screen.findByRole('button', { name: /main\.go/i }))

  await waitFor(() =>
    expect(onOpenFile).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'main.go',
        content: 'package main\n',
      }),
    ),
  )
  expect(screen.queryByText(/package main/)).not.toBeInTheDocument()
  expect(screen.getAllByText('M').length).toBeGreaterThan(0)
})

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

function tokenUsageRaw() {
  return {
    threadId: '019ec777-787f-7742-9909-38e1d24b9502',
    turnId: '019ec777-7938-73c0-b031-140f55aa66a1',
    tokenUsage: {
      total: {
        totalTokens: 13903,
        inputTokens: 13884,
        cachedInputTokens: 4480,
        outputTokens: 19,
        reasoningOutputTokens: 0,
      },
      last: {
        totalTokens: 13903,
        inputTokens: 13884,
        cachedInputTokens: 4480,
        outputTokens: 19,
        reasoningOutputTokens: 0,
      },
      modelContextWindow: 258400,
    },
  }
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}
