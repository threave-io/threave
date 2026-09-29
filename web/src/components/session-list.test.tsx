import type { ComponentProps } from 'react'
import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Session } from '@/lib/api'
import { SessionList } from '@/components/session-list'
import { sessionGroupStorageKey } from '@/hooks/use-session-group-preference'

beforeEach(() => window.localStorage.clear())

const sessions: Session[] = [
  {
    id: 'sess_running',
    title: 'Running work',
    agent_type: 'fake',
    status: 'running',
    workspace_path: '/repo',
    event_count: 0,
    tool_count: 0,
    created_at: '2026-06-12T16:00:00Z',
    updated_at: '2026-06-12T16:10:00Z',
    completed_at: null,
    archived_at: null,
  },
  {
    id: 'sess_failed',
    title: 'Documentation pass',
    agent_type: 'codex',
    status: 'failed',
    workspace_path: '/repo',
    event_count: 0,
    tool_count: 0,
    created_at: '2026-06-12T16:00:00Z',
    updated_at: '2026-06-12T16:05:00Z',
    completed_at: '2026-06-12T16:05:00Z',
    archived_at: null,
  },
  {
    id: 'sess_archived',
    title: 'Archived notes',
    agent_type: 'claude',
    status: 'idle',
    workspace_path: '/repo',
    event_count: 2,
    tool_count: 0,
    created_at: '2026-06-12T16:00:00Z',
    updated_at: '2026-06-12T16:03:00Z',
    completed_at: '2026-06-12T16:03:00Z',
    archived_at: '2026-06-12T16:06:00Z',
  },
]

test('session list exposes spotlight search without the old filter controls', async () => {
  const user = userEvent.setup()
  const onSearch = vi.fn()

  render(<SessionListHarness onSearch={onSearch} />)

  expect(screen.queryByRole('textbox', { name: 'Search sessions' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Session filters' })).not.toBeInTheDocument()
  expect(screen.getByText('Running work')).toBeInTheDocument()
  expect(screen.getByText('Documentation pass')).toBeInTheDocument()
  expect(screen.queryByText('Archived notes')).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Search' }))
  expect(onSearch).toHaveBeenCalledOnce()
})

test('session list shows hover-only shortcuts for the first five sessions', () => {
  const extraSessions = [
    sessionFixture('sess_3', 'Third'),
    sessionFixture('sess_4', 'Fourth'),
    sessionFixture('sess_5', 'Fifth'),
    sessionFixture('sess_6', 'Sixth'),
  ]
  const { container } = render(<SessionListHarness sessions={[...sessions.slice(0, 2), ...extraSessions]} />)
  const modifier = navigator.platform.toLowerCase().includes('mac') ? '⌘' : 'Ctrl '
  const shortcuts = Array.from(container.querySelectorAll('kbd'), (element) => element.textContent)

  expect(shortcuts).toEqual([
    `${modifier}O`,
    `${modifier}S`,
    `${modifier}K`,
    `${modifier}1`,
    `${modifier}2`,
    `${modifier}3`,
    `${modifier}4`,
    `${modifier}5`,
  ])
  const sessionRows = container.querySelectorAll('.session-row')
  expect(sessionRows[0].querySelector('kbd')?.parentElement).toHaveClass(
    'hidden',
    'overflow-hidden',
    'opacity-0',
    'md:inline-flex',
    'md:w-0',
    'md:group-hover:w-9',
    'md:group-hover:opacity-100',
  )
  expect(sessionRows[4].querySelector('kbd')).toHaveTextContent(`${modifier}5`)
  expect(sessionRows[5].querySelector('kbd')).toBeNull()
})

test('session rows are keyboard selectable', async () => {
  const user = userEvent.setup()
  const onSelect = vi.fn()

  render(<SessionListHarness onSelect={onSelect} />)

  screen.getByRole('button', { name: 'Running work' }).focus()
  await user.keyboard('{Enter}')

  expect(onSelect).toHaveBeenCalledWith('sess_running')
})

test('session rows show status as a dot indicator', () => {
  render(<SessionListHarness />)

  expect(screen.getByRole('img', { name: 'Session status: running' })).toHaveClass(
    'animate-pulse',
    'bg-[hsl(var(--success))]',
  )
  expect(screen.queryByText('running')).not.toBeInTheDocument()
})

test('session rows show the agent only as a lightweight hover badge and omit update time metadata', () => {
  const { container } = render(<SessionListHarness sessions={[sessions[0]]} />)

  expect(screen.getByText('Fake')).toHaveClass(
    'hidden',
    'min-h-4',
    'text-[9px]',
    'font-normal',
    'text-muted-foreground',
    'group-hover:inline-flex',
  )
  expect(container.querySelector('.session-row-meta')).not.toBeInTheDocument()
})

test.each([
  ['codex', 'Codex'],
  ['claude', 'Claude'],
  ['opencode', 'OpenCode'],
  ['pi', 'Pi'],
] as const)('session rows label the %s agent badge as %s', (agentType, label) => {
  render(<SessionListHarness sessions={[{ ...sessions[0], agent_type: agentType }]} />)

  expect(screen.getByText(label)).toHaveClass('hidden', 'group-hover:inline-flex')
})

test('selected session row still shows the session status indicator', () => {
  render(<SessionListHarness selectedSessionID="sess_running" />)

  expect(screen.getByRole('img', { name: 'Session status: running' })).toHaveClass(
    'animate-pulse',
    'bg-[hsl(var(--success))]',
  )
})

test('session row keeps a red dot for a transient chat error when it is not selected', () => {
  render(
    <SessionListHarness
      selectedSessionID="sess_failed"
      errorSessionIDs={new Set(['sess_running'])}
    />,
  )

  const status = screen.getByRole('img', { name: 'Session has an error' })
  expect(status).toHaveClass('bg-destructive')
  expect(status).not.toHaveClass('animate-pulse', 'bg-[hsl(var(--success))]')
})

test('session rows show pending input with a pulsing yellow indicator', () => {
  render(<SessionListHarness sessions={[{ ...sessions[0], pending_input: true }]} />)

  expect(screen.getByRole('img', { name: 'Session pending user input' })).toHaveClass(
    'animate-pulse',
    'bg-[hsl(var(--warning))]',
  )
})

test('idle session rows show unseen results with a solid yellow indicator', () => {
  render(
    <SessionListHarness
      sessions={[{ ...sessions[0], status: 'idle', event_count: 8, last_event_seq: 8 }]}
      lastSeenSeqBySession={{ sess_running: 4 }}
    />,
  )

  expect(screen.getByRole('img', { name: 'Session has unseen results' })).toHaveClass('bg-[hsl(var(--warning))]')
  expect(screen.getByRole('img', { name: 'Session has unseen results' })).not.toHaveClass('animate-pulse')
})

test('session list keeps notifications in the header instead of adding a navigation row', async () => {
  const user = userEvent.setup()
  const onOpen = vi.fn()

  render(<SessionListHarness notificationAction={<button onClick={onOpen}>Notifications</button>} />)

  expect(screen.queryByRole('button', { name: 'Dismiss all notifications' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Notifications' }))
  expect(onOpen).toHaveBeenCalledOnce()
})

test('session list exposes the app menu action', async () => {
  const user = userEvent.setup()
  const onOpen = vi.fn()

  render(<SessionListHarness appMenuAction={<button onClick={onOpen}>App menu</button>} />)

  await user.click(screen.getByRole('button', { name: 'App menu' }))

  expect(onOpen).toHaveBeenCalledOnce()
})

test('full session list uses the app icon instead of the text header', () => {
  render(<SessionListHarness />)

  expect(screen.getByRole('img', { name: 'Threave' })).toHaveAttribute('src', '/icon.svg')
  expect(screen.queryByText('Threave')).not.toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: 'Sessions' })).not.toBeInTheDocument()
})

test('embedded session list hides desktop header controls', () => {
  render(<SessionListHarness variant="embedded" />)

  expect(screen.queryByRole('heading', { name: 'Sessions' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Theme: System' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Create session' })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Search' })).toBeInTheDocument()
})

test('pinned sessions use only a persistent highlighted pin treatment', () => {
  const pinned = { ...sessions[1], pinned_at: '2026-06-12T16:20:00Z' }
  const { container } = render(
    <SessionListHarness sessions={[pinned, sessions[0]]} onPinChange={() => undefined} />,
  )

  expect(container.querySelectorAll('.session-row')[0]).toHaveAttribute('data-session-id', pinned.id)
  expect(container.querySelector(`[data-session-id="${pinned.id}"]`)).toHaveAttribute('data-pinned', 'true')
  expect(screen.getByRole('button', { name: 'Unpin session' })).toHaveClass('text-primary', 'opacity-100')
  expect(screen.getByRole('button', { name: 'Unpin session' })).toHaveClass('justify-center')
  expect(screen.queryByText('Pinned')).not.toBeInTheDocument()
  expect(screen.queryByText('Recent')).not.toBeInTheDocument()
})

test('right-side pin action pins and unpins without selecting the session', async () => {
  const user = userEvent.setup()
  const onPinChange = vi.fn()
  const onSelect = vi.fn()
  const pinned = { ...sessions[1], pinned_at: '2026-06-12T16:20:00Z' }

  const { rerender } = render(
    <SessionListHarness sessions={[sessions[0]]} onPinChange={onPinChange} onSelect={onSelect} />,
  )
  await user.click(screen.getByRole('button', { name: 'Pin session' }))
  expect(onPinChange).toHaveBeenCalledWith('sess_running', true)
  expect(onSelect).not.toHaveBeenCalled()

  rerender(<SessionListHarness sessions={[pinned]} onPinChange={onPinChange} onSelect={onSelect} />)
  await user.click(screen.getByRole('button', { name: 'Unpin session' }))
  expect(onPinChange).toHaveBeenCalledWith('sess_failed', false)
})

test('session context menu requests child creation for the clicked row', async () => {
  const user = userEvent.setup()
  const onCreateChild = vi.fn()
  const onSelect = vi.fn()

  const { container } = render(
    <SessionListHarness onCreateChild={onCreateChild} onSelect={onSelect} />,
  )
  const row = container.querySelector('[data-session-id="sess_failed"]')
  expect(row).not.toBeNull()
  fireEvent.contextMenu(row!, { clientX: 24, clientY: 32 })

  await user.click(await screen.findByRole('menuitem', { name: 'New child session' }))

  expect(onCreateChild).toHaveBeenCalledWith('sess_failed')
  expect(onSelect).not.toHaveBeenCalled()
})

test('session context menu requests moving the clicked row', async () => {
  const user = userEvent.setup()
  const onMove = vi.fn()
  const onSelect = vi.fn()
  const { container } = render(<SessionListHarness onMove={onMove} onSelect={onSelect} />)
  const row = container.querySelector('[data-session-id="sess_failed"]')
  expect(row).not.toBeNull()
  fireEvent.contextMenu(row!, { clientX: 24, clientY: 32 })

  await user.click(await screen.findByRole('menuitem', { name: 'Move under parent…' }))

  expect(onMove).toHaveBeenCalledWith('sess_failed')
  expect(onSelect).not.toHaveBeenCalled()
})

test('session context menu targets archive and disables invalid child actions', async () => {
  const onArchive = vi.fn()
  const { container, unmount } = render(
    <SessionListHarness sessions={[sessions[0]]} onArchive={onArchive} onCreateChild={() => undefined} />,
  )
  const row = container.querySelector('[data-session-id="sess_running"]')
  fireEvent.contextMenu(row!, { clientX: 24, clientY: 32 })

  expect(await screen.findByRole('menuitem', { name: 'Archive session…' })).toHaveAttribute('aria-disabled', 'true')
  unmount()

  const { container: archivedContainer } = render(
    <SessionListHarness sessions={[sessions[2]]} onArchive={onArchive} onCreateChild={() => undefined} />,
  )
  expect(screen.queryByText(/^Archived$/)).not.toBeInTheDocument()
  fireEvent.contextMenu(archivedContainer.querySelector('[data-session-id="sess_archived"]')!, { clientX: 48, clientY: 48 })

  expect(await screen.findByRole('menuitem', { name: 'New child session' })).toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByRole('menuitem', { name: 'Restore session' })).toBeInTheDocument()
})

test('unpinned session action stays visible on mobile and becomes hover-only on desktop', () => {
  render(<SessionListHarness sessions={[sessions[0]]} onPinChange={() => undefined} />)

  expect(screen.getByRole('button', { name: 'Pin session' })).toHaveClass(
    'size-8',
    'justify-center',
    'opacity-100',
    'md:pointer-events-none',
    'md:opacity-0',
    'md:group-hover:pointer-events-auto',
    'md:group-hover:opacity-100',
  )
})

test.each([false, true])('pin sits close to the row edge without shrinking its target (pinned: %s)', (pinned) => {
  render(<SessionListHarness sessions={[{
    ...sessions[0], pinned_at: pinned ? '2026-09-09T12:00:00Z' : undefined,
  }]} onPinChange={() => undefined} />)

  const pin = screen.getByRole('button', { name: pinned ? 'Unpin session' : 'Pin session' })
  expect(pin).toHaveClass('size-8', 'justify-center')
  expect(pin.parentElement).not.toHaveClass('mr-1')
  expect(pin.parentElement).not.toHaveClass('pr-2')
})

test('session shortcuts consume row width only while hovered or focused', () => {
  const { container } = render(
    <SessionListHarness sessions={[sessions[0]]} onPinChange={() => undefined} />,
  )

  expect(screen.getByText(/O$/, { selector: 'kbd' })).toHaveClass('inline-flex', 'w-9', 'justify-center')
  expect(container.querySelector('.session-row kbd')).toHaveClass('inline-flex', 'w-9', 'justify-center')
  expect(container.querySelector('.session-row kbd')?.parentElement).toHaveClass(
    'md:w-0',
    'md:group-hover:w-9',
    'md:group-focus-within:w-9',
  )
  expect(screen.getByRole('button', { name: 'Pin session' })).toHaveClass('size-8', 'justify-center')
})

test('navigation shortcuts slide in only while their row is hovered or focused', () => {
  render(<SessionListHarness />)

  const shortcutSlot = screen.getByText(/O$/, { selector: 'kbd' }).parentElement
  expect(shortcutSlot).toHaveClass(
    'hidden',
    'overflow-hidden',
    'opacity-0',
    'md:inline-flex',
    'md:w-0',
    'md:group-hover:w-9',
    'md:group-focus-within:w-9',
  )
})

test('session list has no drag handle or drop target', () => {
  render(<SessionListHarness onPinChange={() => undefined} />)

  expect(screen.queryByRole('button', { name: 'Drag to pin' })).not.toBeInTheDocument()
  expect(screen.queryByTestId('session-pin-drop-target')).not.toBeInTheDocument()
})

test('session list renders expandable lineage without descendant count badges', async () => {
  const user = userEvent.setup()
  const parent = { ...sessions[1], id: 'sess_parent', title: 'Parent', child_count: 2 }
  const child = { ...sessions[0], id: 'sess_child', title: 'Child', parent_session_id: parent.id, lineage_depth: 1 }
  const grandchild = { ...sessions[0], id: 'sess_grandchild', title: 'Grandchild', parent_session_id: child.id, lineage_depth: 2, status: 'idle' as const, pending_input: true }
  const { container } = render(<SessionListHarness sessions={[grandchild, child, parent]} />)

  expect(Array.from(container.querySelectorAll('.session-row'), (row) => row.getAttribute('data-session-id'))).toEqual([
    'sess_parent', 'sess_child', 'sess_grandchild',
  ])
  const parentRow = container.querySelector('[data-session-id="sess_parent"]')
  const childRow = container.querySelector('[data-session-id="sess_child"]')
  expect(childRow).toHaveAttribute('data-lineage-depth', '1')
  expect(childRow?.querySelector('.lucide-git-branch')).not.toBeInTheDocument()
  expect(screen.queryByText(/active$/)).not.toBeInTheDocument()
  expect(screen.queryByText(/waiting$/)).not.toBeInTheDocument()
  expect(parentRow?.querySelector('[aria-label="Collapse Parent"]')).toHaveClass('size-5')
  expect(screen.getByRole('button', { name: 'Parent' })).toHaveClass('pl-1')

  await user.click(screen.getByRole('button', { name: 'Collapse Parent' }))
  expect(screen.queryByRole('button', { name: 'Child' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Expand Parent' }))
  expect(screen.getByRole('button', { name: 'Child' })).toBeInTheDocument()
})

test('session list hides stale child controls when no visible children remain', () => {
  const parent = { ...sessions[1], id: 'sess_parent', title: 'Parent', child_count: 1 }
  render(<SessionListHarness sessions={[parent]} />)

  expect(screen.queryByRole('button', { name: 'Collapse Parent' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Expand Parent' })).not.toBeInTheDocument()
})

test('session groups remember collapse and expansion across remounts, including nested groups', async () => {
  const user = userEvent.setup()
  const parent = sessionFixture('sess_parent', 'Parent')
  const child = { ...sessionFixture('sess_child', 'Child'), parent_session_id: parent.id }
  const grandchild = { ...sessionFixture('sess_grandchild', 'Grandchild'), parent_session_id: child.id }
  const groupSessions = [parent, child, grandchild]
  const first = render(<SessionListHarness sessions={groupSessions} />)

  await user.click(screen.getByRole('button', { name: 'Collapse Child' }))
  await user.click(screen.getByRole('button', { name: 'Collapse Parent' }))
  first.unmount()

  const second = render(<SessionListHarness sessions={[]} />)
  second.rerender(<SessionListHarness sessions={groupSessions} />)
  expect(screen.queryByRole('button', { name: 'Child' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Expand Parent' }))
  expect(screen.getByRole('button', { name: 'Child' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Grandchild' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Expand Child' }))
  second.unmount()

  render(<SessionListHarness sessions={groupSessions} />)
  expect(screen.getByRole('button', { name: 'Grandchild' })).toBeInTheDocument()
})

test('desktop and mobile lists share group preferences', async () => {
  const user = userEvent.setup()
  const parent = sessionFixture('sess_parent', 'Parent')
  const child = { ...sessionFixture('sess_child', 'Child'), parent_session_id: parent.id }
  const desktop = render(<SessionListHarness sessions={[parent, child]} />)
  const mobile = render(<SessionListHarness sessions={[parent, child]} variant="embedded" />)

  await user.click(within(desktop.container).getByRole('button', { name: 'Collapse Parent' }))
  expect(within(mobile.container).queryByRole('button', { name: 'Child' })).not.toBeInTheDocument()
  await user.click(within(mobile.container).getByRole('button', { name: 'Expand Parent' }))
  expect(within(desktop.container).getByRole('button', { name: 'Child' })).toBeInTheDocument()
})

test.each(['invalid JSON', '{"unexpected":true}', '[null,42]'])('invalid group preferences keep the list usable: %s', (stored) => {
  window.localStorage.setItem(sessionGroupStorageKey, stored)
  const parent = sessionFixture('sess_parent', 'Parent')
  const child = { ...sessionFixture('sess_child', 'Child'), parent_session_id: parent.id }
  render(<SessionListHarness sessions={[parent, child]} />)

  expect(screen.getByRole('button', { name: 'Child' })).toBeInTheDocument()
})

test('session groups still toggle when browser storage rejects writes', async () => {
  const user = userEvent.setup()
  const parent = sessionFixture('sess_parent', 'Parent')
  const child = { ...sessionFixture('sess_child', 'Child'), parent_session_id: parent.id }
  render(<SessionListHarness sessions={[parent, child]} />)
  const write = vi.spyOn(window.localStorage, 'setItem').mockImplementation(() => {
    throw new DOMException('Storage is full', 'QuotaExceededError')
  })

  try {
    await user.click(screen.getByRole('button', { name: 'Collapse Parent' }))
    expect(screen.queryByRole('button', { name: 'Child' })).not.toBeInTheDocument()
  } finally {
    write.mockRestore()
    await user.click(screen.getByRole('button', { name: 'Expand Parent' }))
  }
  expect(screen.getByRole('button', { name: 'Child' })).toBeInTheDocument()
})

function baseProps() {
  return {
    sessions: sessions.filter((session) => !session.archived_at),
    selectedSessionID: null,
    lastSeenSeqBySession: {},
    onSelect: () => undefined,
    onSearch: () => undefined,
    onCreate: () => undefined,
  }
}

function SessionListHarness(props: Partial<ComponentProps<typeof SessionList>>) {
  return <SessionList {...baseProps()} {...props} />
}

function sessionFixture(id: string, title: string): Session {
  return {
    ...sessions[0],
    id,
    title,
  }
}
