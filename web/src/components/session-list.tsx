import * as ContextMenu from '@radix-ui/react-context-menu'
import { Archive, BookOpen, ChevronDown, ChevronRight, FolderInput, GitBranch, LayoutDashboard, MessageSquare, Pin, Plus, RotateCcw, Search } from 'lucide-react'
import type { ReactNode } from 'react'
import { useSessionGroupPreference } from '@/hooks/use-session-group-preference'
import type { Session } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { StatusBadge } from '@/components/status-badge'
import { sessionAttention } from '@/lib/session-attention'
import { cn } from '@/lib/utils'

type Props = {
  sessions: Session[]
  selectedSessionID: string | null
  errorSessionIDs?: ReadonlySet<string>
  lastSeenSeqBySession?: Record<string, number>
  loading?: boolean
  onSelect: (sessionID: string) => void
  pinningSessionIDs?: ReadonlySet<string>
  onPinChange?: (sessionID: string, pinned: boolean) => void
  creatingChildSessionIDs?: ReadonlySet<string>
  onCreateChild?: (sessionID: string) => void
  onMove?: (sessionID: string) => void
  archivingSessionID?: string | null
  onArchive?: (sessionID: string) => void
  overviewSelected?: boolean
  onOverview?: () => void
  userSkillsSelected?: boolean
  onUserSkills?: () => void
  onSearch?: () => void
  onCreate: () => void
  createDisabled?: boolean
  notificationAction?: ReactNode
  appMenuAction?: ReactNode
  variant?: 'full' | 'embedded'
}

export function SessionList({
  sessions,
  selectedSessionID,
  errorSessionIDs = new Set(),
  lastSeenSeqBySession = {},
  loading = false,
  onSelect,
  pinningSessionIDs = new Set(),
  onPinChange,
  creatingChildSessionIDs = new Set(),
  onCreateChild,
  onMove,
  archivingSessionID = null,
  onArchive,
  overviewSelected = false,
  onOverview,
  userSkillsSelected = false,
  onUserSkills,
  onSearch,
  onCreate,
  createDisabled = false,
  notificationAction,
  appMenuAction,
  variant = 'full',
}: Props) {
  const showHeader = variant === 'full'
  const { collapsedSessionIDs, toggleSessionGroup } = useSessionGroupPreference()
  const treeRows = buildSessionTreeRows(sessions, collapsedSessionIDs)

  return (
    <aside
      aria-label="Sessions"
      className={cn(
        'flex h-full w-full min-h-0 flex-col',
        variant === 'full' ? 'command-sidebar border-r border-border/70' : 'bg-transparent',
      )}
    >
      {showHeader ? (
        <div className="flex items-center justify-between gap-3 border-b border-border/70 p-4">
          <button
            type="button"
            aria-label="Open overview"
            onClick={onOverview}
            className="rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <img src="/icon.svg" alt="Threave" className="sidebar-logo-mark h-9 w-9 shrink-0" />
          </button>
          <div className="flex shrink-0 items-center gap-2">
            {notificationAction}
            {appMenuAction}
            <Button
              aria-label="Create session"
              size="icon"
              disabled={createDisabled}
              onClick={onCreate}
              className="shadow-sm"
            >
              <Plus />
            </Button>
          </div>
        </div>
      ) : null}

      <div className="border-b border-border/70 p-2.5">
        <button
          type="button"
          onClick={onOverview}
          aria-current={overviewSelected ? 'page' : undefined}
          className={cn(
            'group flex w-full items-center gap-2.5 rounded-md border border-transparent px-2.5 py-2 text-left text-sm font-medium transition-colors hover:border-border/70 hover:bg-background/54 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring',
            overviewSelected && 'border-primary/30 bg-background/80 shadow-sm',
          )}
        >
          <LayoutDashboard className="size-4 text-muted-foreground" />
          <span className="flex-1">Overview</span>
          <ShortcutReveal shortcut="O" />
        </button>
        <button
          type="button"
          onClick={onUserSkills}
          aria-current={userSkillsSelected ? 'page' : undefined}
          className={cn(
            'group mt-1 flex w-full items-center gap-2.5 rounded-md border border-transparent px-2.5 py-2 text-left text-sm font-medium transition-colors hover:border-border/70 hover:bg-background/54 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring',
            userSkillsSelected && 'border-primary/30 bg-background/80 shadow-sm',
          )}
        >
          <BookOpen className="size-4 text-muted-foreground" />
          <span className="flex-1">User skills</span>
          <ShortcutReveal shortcut="S" />
        </button>
        <button
          type="button"
          aria-label="Search"
          disabled={!onSearch}
          onClick={onSearch}
          className="group mt-1 flex w-full items-center gap-2.5 rounded-md border border-transparent px-2.5 py-2 text-left text-sm font-medium transition-colors hover:border-border/70 hover:bg-background/54 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50"
        >
          <Search className="size-4 text-muted-foreground" />
          <span className="flex-1">Search</span>
          <ShortcutReveal shortcut="K" />
        </button>
      </div>

      <ScrollArea className="flex-1">
        {loading && sessions.length === 0 ? (
          <div className="flex h-full min-h-40 items-center justify-center p-4 text-sm text-muted-foreground">
            Loading sessions...
          </div>
        ) : sessions.length === 0 ? (
          <div className="p-4 text-sm text-muted-foreground">No sessions yet.</div>
        ) : (
          <div className="session-list-rows space-y-1.5 p-2.5">
            {treeRows.map(({ session, depth, hasChildren }, index) => (
              <SessionRow
                key={session.id}
                session={session}
                shortcut={index < 5 ? String(index + 1) : undefined}
                selected={selectedSessionID === session.id}
                hasError={errorSessionIDs.has(session.id)}
                attention={sessionAttention(session, lastSeenSeqBySession)}
                pinPending={pinningSessionIDs.has(session.id)}
                childCreatePending={creatingChildSessionIDs.has(session.id)}
                archivePending={archivingSessionID === session.id}
                onSelect={() => onSelect(session.id)}
                onPinChange={onPinChange ? (pinned) => onPinChange(session.id, pinned) : undefined}
                onCreateChild={onCreateChild ? () => onCreateChild(session.id) : undefined}
                onMove={onMove ? () => onMove(session.id) : undefined}
                onArchive={onArchive ? () => onArchive(session.id) : undefined}
                depth={depth}
                hasChildren={hasChildren}
                expanded={!collapsedSessionIDs.has(session.id)}
                onToggle={() => toggleSessionGroup(session.id)}
              />
            ))}
          </div>
        )}
      </ScrollArea>
    </aside>
  )
}

function SessionRow({
  session,
  shortcut,
  selected,
  hasError,
  attention,
  pinPending,
  childCreatePending,
  archivePending,
  onSelect,
  onPinChange,
  onCreateChild,
  onMove,
  onArchive,
  depth,
  hasChildren,
  expanded,
  onToggle,
}: {
  session: Session
  shortcut?: string
  selected: boolean
  hasError: boolean
  attention: ReturnType<typeof sessionAttention>
  pinPending: boolean
  childCreatePending: boolean
  archivePending: boolean
  onSelect: () => void
  onPinChange?: (pinned: boolean) => void
  onCreateChild?: () => void
  onMove?: () => void
  onArchive?: () => void
  depth: number
  hasChildren: boolean
  expanded: boolean
  onToggle: () => void
}) {
  const title = session.title || 'Untitled session'
  const pinned = Boolean(session.pinned_at)
  const archived = Boolean(session.archived_at)

  const row = (
    <div
      data-session-id={session.id}
      data-parent-session-id={session.parent_session_id || undefined}
      data-lineage-depth={depth}
      data-pinned={pinned ? 'true' : undefined}
      className={cn(
        'session-row group flex w-full items-center rounded-md border border-transparent transition-colors hover:border-border/70 hover:bg-background/54 focus-within:ring-2 focus-within:ring-inset focus-within:ring-ring',
        selected && 'border-primary/30 bg-background/80 shadow-sm',
        archived &&
          'border-dashed border-border/80 bg-surface-muted/65 text-muted-foreground hover:border-border hover:bg-surface-muted/80',
      )}
      style={{
        marginInlineStart: `${Math.min(depth, 4) * 12}px`,
        width: `calc(100% - ${Math.min(depth, 4) * 12}px)`,
      }}
    >
      {hasChildren ? (
        <button
          type="button"
          aria-label={`${expanded ? 'Collapse' : 'Expand'} ${title}`}
          aria-expanded={expanded}
          onClick={onToggle}
          className="flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-background/70 hover:text-foreground"
        >
          {expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        </button>
      ) : null}
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected ? 'true' : undefined}
        aria-label={archived ? `${title} archived` : title}
        className={cn(
          'grid min-w-0 flex-1 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 py-2 pr-2.5 text-left focus-visible:outline-none',
          hasChildren ? 'pl-1' : 'pl-2.5',
        )}
      >
        <StatusBadge status={session.status} attention={attention} hasError={hasError} />
        <span
          className="flex min-w-0 items-center gap-1.5 text-sm font-medium"
        >
          <span
            className={cn(
              'truncate',
              archived && 'text-muted-foreground line-through decoration-muted-foreground/60',
            )}
          >
            {title}
          </span>
          <Badge
            aria-hidden="true"
            variant="outline"
            className="hidden min-h-4 shrink-0 whitespace-nowrap rounded px-1.5 py-0 text-[9px] font-normal leading-4 text-muted-foreground group-hover:inline-flex"
          >
            {agentLabel(session.agent_type)}
          </Badge>
        </span>
      </button>
      <div className="flex h-8 shrink-0 items-center">
        {shortcut ? (
          <ShortcutReveal shortcut={shortcut} trailingGap />
        ) : null}
        {!archived && (pinned || onPinChange) ? (
          <button
            type="button"
            aria-label={onPinChange ? (pinned ? 'Unpin session' : 'Pin session') : 'Pinned session'}
            title={onPinChange ? (pinned ? `Unpin ${title}` : `Pin ${title} to top`) : `${title} is pinned`}
            disabled={pinPending || !onPinChange}
            onClick={() => onPinChange?.(!pinned)}
            className={cn(
              'flex size-8 items-center justify-center rounded transition-all hover:bg-background/70',
              pinPending && 'opacity-40',
              pinned
                ? 'text-primary opacity-100'
                : 'text-muted-foreground opacity-100 md:pointer-events-none md:opacity-0 md:group-hover:pointer-events-auto md:group-hover:opacity-100 md:group-focus-within:pointer-events-auto md:group-focus-within:opacity-100',
            )}
          >
            <Pin className={cn('size-4', pinned && 'fill-primary/20')} />
          </button>
        ) : null}
      </div>
    </div>
  )

  if (!onCreateChild && !onMove && !onArchive && !onPinChange) return row

  return (
    <ContextMenu.Root>
      <ContextMenu.Trigger asChild>{row}</ContextMenu.Trigger>
      <ContextMenu.Portal>
        <ContextMenu.Content
          collisionPadding={12}
          className="z-50 min-w-52 overflow-hidden rounded-lg border border-border bg-popover p-1.5 text-popover-foreground shadow-xl"
        >
          <ContextMenu.Item onSelect={onSelect} className={contextMenuItemClass}>
            <MessageSquare className="size-4 text-muted-foreground" aria-hidden="true" />
            Open
          </ContextMenu.Item>
          <ContextMenu.Item
            disabled={archived || childCreatePending || !onCreateChild}
            onSelect={onCreateChild}
            className={contextMenuItemClass}
          >
            <GitBranch className="size-4 text-muted-foreground" aria-hidden="true" />
            {childCreatePending ? 'Creating child…' : 'New child session'}
          </ContextMenu.Item>
          <ContextMenu.Item onSelect={onMove} disabled={!onMove} className={contextMenuItemClass}>
            <FolderInput className="size-4 text-muted-foreground" aria-hidden="true" />
            Move under parent…
          </ContextMenu.Item>
          {onPinChange && !archived ? (
            <ContextMenu.Item
              disabled={pinPending}
              onSelect={() => onPinChange(!pinned)}
              className={contextMenuItemClass}
            >
              <Pin className={cn('size-4 text-muted-foreground', pinned && 'fill-current')} aria-hidden="true" />
              {pinPending ? 'Updating pin…' : pinned ? 'Unpin session' : 'Pin session'}
            </ContextMenu.Item>
          ) : null}
          {onArchive ? (
            <>
              <ContextMenu.Separator className="my-1 h-px bg-border/70" />
              <ContextMenu.Item
                disabled={archivePending || (!archived && session.status === 'running')}
                onSelect={onArchive}
                className={cn(contextMenuItemClass, !archived && 'text-destructive focus:text-destructive')}
              >
                {archived ? (
                  <RotateCcw className="size-4 text-muted-foreground" aria-hidden="true" />
                ) : (
                  <Archive className="size-4" aria-hidden="true" />
                )}
                {archivePending ? (archived ? 'Restoring…' : 'Archiving…') : archived ? 'Restore session' : 'Archive session…'}
              </ContextMenu.Item>
            </>
          ) : null}
        </ContextMenu.Content>
      </ContextMenu.Portal>
    </ContextMenu.Root>
  )
}

const contextMenuItemClass =
  'flex min-h-10 cursor-default select-none items-center gap-3 rounded-md px-2.5 py-2 text-sm outline-none data-[disabled]:pointer-events-none data-[disabled]:opacity-50 data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground'

function agentLabel(agentType: Session['agent_type']) {
  switch (agentType) {
    case 'codex':
      return 'Codex'
    case 'claude':
      return 'Claude'
    case 'opencode':
      return 'OpenCode'
    case 'pi':
      return 'Pi'
    case 'fake':
      return 'Fake'
  }
}

type SessionTreeRow = {
  session: Session
  depth: number
  hasChildren: boolean
}

function buildSessionTreeRows(
  sessions: Session[],
  collapsed: ReadonlySet<string>,
) {
  const byID = new Map(sessions.map((session) => [session.id, session]))
  const children = new Map<string, Session[]>()
  const roots: Session[] = []
  for (const session of sessions) {
    if (session.parent_session_id && byID.has(session.parent_session_id)) {
      const siblings = children.get(session.parent_session_id) ?? []
      siblings.push(session)
      children.set(session.parent_session_id, siblings)
    } else {
      roots.push(session)
    }
  }
  const rows: SessionTreeRow[] = []
  const visited = new Set<string>()
  const visit = (session: Session, depth: number) => {
    if (visited.has(session.id)) return
    visited.add(session.id)
    const descendants = children.get(session.id) ?? []
    rows.push({ session, depth, hasChildren: descendants.length > 0 })
    if (!collapsed.has(session.id)) descendants.forEach((child) => visit(child, depth + 1))
  }
  roots.forEach((session) => visit(session, 0))
  return rows
}

function ShortcutHint({ shortcut }: { shortcut: string }) {
  const modifier = navigator.platform.toLowerCase().includes('mac') ? '⌘' : 'Ctrl '
  return (
    <kbd
      aria-hidden="true"
      className="inline-flex w-9 shrink-0 items-center justify-center rounded border border-border/70 bg-background/70 px-1 py-0.5 text-[10px] font-medium text-muted-foreground"
    >
      {modifier}{shortcut}
    </kbd>
  )
}

function ShortcutReveal({ shortcut, trailingGap = false }: { shortcut: string; trailingGap?: boolean }) {
  return (
    <span
      className={cn(
        'pointer-events-none hidden overflow-hidden opacity-0 transition-[width,margin,opacity] duration-150 md:inline-flex md:w-0 md:group-hover:w-9 md:group-hover:opacity-100 md:group-focus-within:w-9 md:group-focus-within:opacity-100',
        trailingGap && 'md:group-hover:mr-1 md:group-focus-within:mr-1',
      )}
    >
      <ShortcutHint shortcut={shortcut} />
    </span>
  )
}
