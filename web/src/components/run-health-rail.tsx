import * as DropdownMenu from '@radix-ui/react-dropdown-menu'
import {
  Activity,
  Archive,
  Bug,
  Check,
  Clock3,
  Eraser,
  Folder,
  Gamepad2,
  Gauge,
  Loader2,
  Map,
  Minimize2,
  Orbit,
  Square,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import type { AgentEvent, Session, WorkspaceFileContent } from '@/lib/api'
import type { StreamState } from '@/hooks/use-session-events'
import type { RailContentMode } from '@/hooks/use-rail-content'
import type { TokenUsageSummary, TranscriptSequenceRange } from '@/lib/events'
import { activeThinking, eventLabel, groupEvents, latestTokenUsage } from '@/lib/events'
import { BlocksGame } from '@/components/blocks-game'
import { Button } from '@/components/ui/button'
import { ConversationMap } from '@/components/conversation-map'
import { SignalField } from '@/components/signal-field'
import { WorkspaceFileBrowser } from '@/components/workspace-files'
import { cn } from '@/lib/utils'
import { ContextTokenMeter } from '@/components/context-token-meter'
import { formatTokenCount } from '@/lib/token-count'

type Props = {
  session: Session | null
  resolvingSessionID?: string | null
  events: AgentEvent[]
  activityEvents?: AgentEvent[]
  streamState: StreamState
  streamError: string
  onClear?: () => Promise<void>
  onCompact?: () => Promise<void>
  onToggleArchive: () => Promise<void>
  onOpenFile?: (file: WorkspaceFileContent) => void
  fileRefreshKey?: number
  showUtilityContent?: boolean
  contentMode?: RailContentMode
  onContentModeChange?: (mode: RailContentMode) => void
  contentActive?: boolean
  hasOlderEvents?: boolean
  hasNewerEvents?: boolean
  loadingOlderEvents?: boolean
  loadingNewerEvents?: boolean
  onLoadOlderEvents?: () => Promise<void> | void
  onLoadNewerEvents?: () => Promise<void> | void
  onJumpToLatest?: () => Promise<void> | void
  visibleSequenceRange?: TranscriptSequenceRange | null
  focusedEventSeq?: number
  onSelectConversationSeq?: (seq: number) => void
  clearPending?: boolean
  compactPending?: boolean
  archivePending?: boolean
  offline?: boolean
  debugEnabled?: boolean
  onToggleDebug?: () => void
}

export function RunHealthRail({
  session,
  resolvingSessionID = null,
  events,
  activityEvents = events,
  streamState,
  streamError,
  onClear = async () => undefined,
  onCompact = async () => undefined,
  onToggleArchive,
  onOpenFile,
  fileRefreshKey = 0,
  showUtilityContent = true,
  contentMode = 'files',
  onContentModeChange = () => undefined,
  contentActive = true,
  hasOlderEvents = false,
  hasNewerEvents = false,
  loadingOlderEvents = false,
  loadingNewerEvents = false,
  onLoadOlderEvents,
  onLoadNewerEvents,
  onJumpToLatest,
  visibleSequenceRange = null,
  focusedEventSeq = 0,
  onSelectConversationSeq,
  clearPending = false,
  compactPending = false,
  archivePending = false,
  offline = false,
  debugEnabled = false,
  onToggleDebug,
}: Props) {
  const desktopLayout = useDesktopLayout()
  const [blocksMounted, setBlocksMounted] = useState(contentMode === 'blocks')
  const showContentSlot = showUtilityContent && desktopLayout
  const slotActive = showContentSlot && contentActive
  useEffect(() => {
    if (contentMode === 'blocks') setBlocksMounted(true)
  }, [contentMode])
  const latestEvent = events.at(-1)
  const tokenUsage = latestTokenUsage(events)
  const providerCumulativeTokenCount = tokenUsage?.kind === 'context' ? 0 : (tokenUsage?.total.totalTokens ?? 0)
  const cumulativeTokenCount = Math.max(
    session?.token_count ?? 0,
    tokenUsage?.sessionTotalTokens ?? providerCumulativeTokenCount,
  )
  const totalEventCount = Math.max(session?.event_count ?? 0, events.length)
  const loadedToolCount = groupEvents(events).filter(
    (group) => group.kind === 'tool-call' || group.kind === 'file-change',
  ).length
  const totalToolCount = Math.max(session?.tool_count ?? 0, loadedToolCount)
  const actionPending = clearPending || compactPending
  const supportsClear = session?.agent_type === 'codex' || session?.agent_type === 'opencode' || session?.agent_type === 'claude'
  const supportsCompact = session?.agent_type === 'codex' || session?.agent_type === 'claude'
  const contextActionDisabled =
    offline || !session || session.status === 'running' || Boolean(session.archived_at) || actionPending
  const clearDisabled = contextActionDisabled || !supportsClear
  const compactDisabled = contextActionDisabled || !supportsCompact || !session?.provider_session_id
  const contextProviderLabel = session?.agent_type === 'opencode' ? 'OpenCode' : session?.agent_type === 'claude' ? 'Claude' : 'Codex'
  const showTokenPanel = Boolean(tokenUsage) || cumulativeTokenCount > 0 || supportsClear
  const thinkingActive = session?.status === 'running' && activeThinking(activityEvents)

  return (
    <aside className="command-rail flex h-full w-full shrink-0 flex-col px-3 py-4">
      <div className="space-y-3">
        <RailPanel>
          <div className="flex items-center justify-between gap-2">
            <RailSectionTitle icon={Activity} label="Activity" />
            <div className="flex items-center gap-1">
              {showContentSlot ? (
                <RailContentPicker mode={contentMode} onChange={onContentModeChange} />
              ) : null}
              <ActiveChatDot
                active={Boolean(session)}
                running={session?.status === 'running'}
                state={streamState}
                error={streamError}
              />
            </div>
          </div>
          <div className="mt-3 grid grid-cols-3 gap-2">
            <Metric label="Events" value={totalEventCount} />
            <Metric label="Tools" value={totalToolCount} />
            <Metric label="Tokens" value={cumulativeTokenCount} />
          </div>
          <div className="mt-3 border-t border-border/60 pt-3">
            <div className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
              <Clock3 className="size-3" aria-hidden="true" />
              <span>Latest</span>
            </div>
            <div className="mt-1 flex w-full min-w-0 items-center gap-2">
              <p className="min-w-0 flex-1 truncate text-xs font-medium">
                {latestEvent ? eventLabel(latestEvent) : 'No events'}
              </p>
              <p className="ml-auto shrink-0 text-right text-[11px] text-muted-foreground">
                {latestEvent ? formatShortDateTime(latestEvent.created_at) : 'Waiting for activity'}
              </p>
            </div>
          </div>
        </RailPanel>
      </div>

      {showUtilityContent ? (
        <div className={cn('mt-3 min-h-0 flex-1', !desktopLayout && 'hidden')}>
          {desktopLayout && contentMode === 'files' ? (
            <WorkspaceFileBrowser
              session={session}
              resolvingSessionID={resolvingSessionID}
              refreshKey={fileRefreshKey}
              onOpenFile={onOpenFile}
              offline={offline}
            />
          ) : null}
          {desktopLayout && contentMode === 'conversation-map' ? (
            <ConversationMap
              events={events}
              hasOlderEvents={hasOlderEvents}
              hasNewerEvents={hasNewerEvents}
              loadingOlderEvents={loadingOlderEvents}
              loadingNewerEvents={loadingNewerEvents}
              onLoadOlderEvents={onLoadOlderEvents}
              onLoadNewerEvents={onLoadNewerEvents}
              onJumpToLatest={onJumpToLatest}
              visibleRange={visibleSequenceRange}
              focusedSeq={focusedEventSeq}
              onSelectSeq={onSelectConversationSeq}
            />
          ) : null}
          {desktopLayout && contentMode === 'signal-field' ? (
            <SignalField
              sessionID={session?.id ?? null}
              events={activityEvents}
              active={slotActive}
              running={session?.status === 'running'}
              thinking={thinkingActive}
              streamState={streamState}
            />
          ) : null}
          {blocksMounted ? (
            <BlocksGame
              active={slotActive && contentMode === 'blocks'}
              className={contentMode === 'blocks' ? undefined : 'hidden'}
            />
          ) : null}
        </div>
      ) : null}

      <div className={cn('space-y-3 pt-3', showUtilityContent && 'mt-auto')}>
        {showTokenPanel ? (
          <RailPanel>
            <RailSectionTitle icon={Gauge} label="Tokens" />
            {tokenUsage ? (
              <TokenUsageView usage={tokenUsage} cumulativeTokenCount={cumulativeTokenCount} />
            ) : (
              <TokenUsageEmptyState />
            )}
            {supportsClear ? (
              <ContextActions
                providerLabel={contextProviderLabel}
                showCompact={supportsCompact}
                clearPending={clearPending}
                compactPending={compactPending}
                clearDisabled={clearDisabled}
                compactDisabled={compactDisabled}
                onClear={onClear}
                onCompact={onCompact}
              />
            ) : null}
          </RailPanel>
        ) : null}

        <Button
          type="button"
          variant="outline"
          className={cn(
            'w-full justify-start border-border/70 bg-background/40 text-muted-foreground hover:bg-background/70',
            session?.archived_at &&
              'border-[hsl(var(--warning)/0.32)] bg-[hsl(var(--warning)/0.10)] text-[hsl(var(--warning))] hover:bg-[hsl(var(--warning)/0.14)]',
          )}
          disabled={offline || !session || session.status === 'running' || archivePending}
          onClick={() => void onToggleArchive()}
          aria-label={session?.archived_at ? 'Restore selected session' : 'Archive selected session'}
        >
          <Archive aria-hidden="true" />
          {archivePending ? (session?.archived_at ? 'Restoring' : 'Archiving') : session?.archived_at ? 'Restore' : 'Archive'}
        </Button>

        {onToggleDebug ? (
          <Button
            type="button"
            role="switch"
            aria-label="Client debug"
            aria-checked={debugEnabled}
            aria-pressed={debugEnabled}
            variant="outline"
            className="w-full justify-start border-border/70 bg-background/40 text-muted-foreground hover:bg-background/70"
            onClick={onToggleDebug}
          >
            <Bug aria-hidden="true" />
            <span>Client debug</span>
            <span aria-hidden="true" className="ml-auto text-xs">{debugEnabled ? 'On' : 'Off'}</span>
          </Button>
        ) : null}
      </div>
    </aside>
  )
}

const railContentOptions: Array<{
  mode: RailContentMode
  label: string
  icon: LucideIcon
}> = [
  { mode: 'files', label: 'Files', icon: Folder },
  { mode: 'conversation-map', label: 'Conversation map', icon: Map },
  { mode: 'signal-field', label: 'Signal field', icon: Orbit },
  { mode: 'blocks', label: 'Blocks', icon: Gamepad2 },
  { mode: 'blank', label: 'Blank', icon: Square },
]

function RailContentPicker({ mode, onChange }: { mode: RailContentMode; onChange: (mode: RailContentMode) => void }) {
  const selected = railContentOptions.find((option) => option.mode === mode) ?? railContentOptions[0]
  const SelectedIcon = selected.icon
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-7 text-muted-foreground"
          aria-label={`Rail content: ${selected.label}`}
          title={`Rail content: ${selected.label}`}
        >
          <SelectedIcon aria-hidden="true" />
        </Button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          sideOffset={6}
          collisionPadding={12}
          className="z-50 w-52 rounded-lg border border-border bg-popover p-1.5 text-popover-foreground shadow-xl"
        >
          <DropdownMenu.Label className="px-2 pb-1 pt-1 text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
            Rail content
          </DropdownMenu.Label>
          <DropdownMenu.RadioGroup value={mode} onValueChange={(value) => onChange(value as RailContentMode)}>
            {railContentOptions.map(({ mode: optionMode, label, icon: Icon }) => (
              <DropdownMenu.RadioItem
                key={optionMode}
                value={optionMode}
                className="flex min-h-9 cursor-default select-none items-center gap-2 rounded-md px-2 py-1.5 text-sm outline-none data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground"
              >
                <Icon className="size-4 text-muted-foreground" aria-hidden="true" />
                <span className="flex-1">{label}</span>
                <DropdownMenu.ItemIndicator>
                  <Check className="size-4" aria-hidden="true" />
                </DropdownMenu.ItemIndicator>
              </DropdownMenu.RadioItem>
            ))}
          </DropdownMenu.RadioGroup>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  )
}

function useDesktopLayout() {
  const [desktop, setDesktop] = useState(() => typeof window === 'undefined' || window.innerWidth >= 1024)
  useEffect(() => {
    function update() {
      setDesktop(window.innerWidth >= 1024)
    }
    update()
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])
  return desktop
}

function ActiveChatDot({
  active,
  running,
  state,
  error,
}: {
  active: boolean
  running: boolean
  state: StreamState
  error: string
}) {
  const label = active ? activeChatLabel(running, state, error) : 'Inactive'
  return (
    <span
      aria-label={`Active chat: ${label}`}
      role="img"
      title={`Active chat: ${label}`}
      className={cn(
        'inline-block size-2.5 shrink-0 rounded-full',
        active ? activeChatClassName(running, state, error) : 'bg-muted-foreground',
        active && (running || state === 'loading' || state === 'reconnecting') && 'animate-pulse',
      )}
    />
  )
}

function RailPanel({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <section className={cn('rounded-md border border-border/70 bg-background/46 p-3 shadow-sm', className)}>
      {children}
    </section>
  )
}

function RailSectionTitle({ icon: Icon, label }: { icon: LucideIcon; label: string }) {
  return (
    <div className="flex items-center gap-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
      <Icon className="size-3.5" aria-hidden="true" />
      <span>{label}</span>
    </div>
  )
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="min-w-0 rounded bg-surface-muted/72 px-1.5 py-2 text-center">
      <p className="truncate text-lg font-semibold tabular-nums leading-none" title={value.toLocaleString()}>
        {formatCompactCount(value)}
      </p>
      <p className="mt-1 truncate text-[10px] uppercase tracking-[0.1em] text-muted-foreground">{label}</p>
    </div>
  )
}

function TokenUsageView({ usage, cumulativeTokenCount }: { usage: TokenUsageSummary; cumulativeTokenCount: number }) {
  const contextOnly = usage.kind === 'context'
  const cachedPercent = usage.total.inputTokens > 0 ? usage.total.cachedInputTokens / usage.total.inputTokens : 0

  return (
    <div className="mt-3">
      <ContextTokenMeter usage={usage} />
      {contextOnly ? (
        usage.cost ? (
          <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{formatCost(usage.cost)} cost</p>
        ) : null
      ) : (
        <p className="mt-0.5 truncate text-[11px] text-muted-foreground">
          {formatTokenCount(cumulativeTokenCount)} cumulative
        </p>
      )}

      {contextOnly ? null : (
        <>
          <div className="mt-3 grid grid-cols-2 gap-2">
            <TokenMetric label="Input" value={usage.total.inputTokens} />
            <TokenMetric label="Output" value={usage.total.outputTokens} />
          </div>
          <p className="mt-2 truncate text-[11px] text-muted-foreground">
            {formatTokenCount(usage.total.cachedInputTokens)} cached ({formatPercent(cachedPercent)})
          </p>
          <p className="mt-0.5 truncate text-[11px] text-muted-foreground">
            {formatTokenCount(usage.total.reasoningOutputTokens)} reasoning
          </p>
        </>
      )}
    </div>
  )
}

function TokenUsageEmptyState() {
  return <p className="mt-3 text-[11px] text-muted-foreground">No token usage yet</p>
}

function ContextActions({
  providerLabel,
  showCompact,
  clearPending,
  compactPending,
  clearDisabled,
  compactDisabled,
  onClear,
  onCompact,
}: {
  providerLabel: 'Codex' | 'OpenCode' | 'Claude'
  showCompact: boolean
  clearPending: boolean
  compactPending: boolean
  clearDisabled: boolean
  compactDisabled: boolean
  onClear: () => Promise<void>
  onCompact: () => Promise<void>
}) {
  return (
    <div className={cn('mt-3 grid gap-2', showCompact ? 'grid-cols-2' : 'grid-cols-1')}>
      <Button
        type="button"
        variant="outline"
        className="justify-center border-border/70 bg-background/40 px-2 text-muted-foreground hover:bg-background/70"
        disabled={clearDisabled}
        onClick={() => void onClear()}
        aria-label={`Clear ${providerLabel} context`}
      >
        {clearPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Eraser aria-hidden="true" />}
        <span>{clearPending ? 'Clearing' : 'Clear'}</span>
      </Button>
      {showCompact ? (
        <Button
          type="button"
          variant="outline"
          className="justify-center border-border/70 bg-background/40 px-2 text-muted-foreground hover:bg-background/70"
          disabled={compactDisabled}
          onClick={() => void onCompact()}
          aria-label={`Compact ${providerLabel} context`}
        >
          {compactPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Minimize2 aria-hidden="true" />}
          <span>{compactPending ? 'Compacting' : 'Compact'}</span>
        </Button>
      ) : null}
    </div>
  )
}

function TokenMetric({ label, value }: { label: string; value: number }) {
  return (
    <div>
      <p className="text-sm font-semibold tabular-nums leading-none">{formatTokenCount(value)}</p>
      <p className="mt-1 text-[10px] uppercase tracking-[0.12em] text-muted-foreground">{label}</p>
    </div>
  )
}

function streamStateLabel(state: StreamState, error: string) {
  if (error || state === 'disconnected') return 'Disconnected'
  if (state === 'connected') return 'Live'
  if (state === 'reconnecting') return 'Reconnecting'
  return 'Loading'
}

function activeChatLabel(running: boolean, state: StreamState, error: string) {
  if (error || state === 'disconnected') return 'Disconnected'
  if (running) return 'Running'
  return streamStateLabel(state, error)
}

function streamStateClassName(state: StreamState, error: string) {
  if (error || state === 'disconnected') return 'bg-destructive'
  if (state === 'connected') return 'bg-[hsl(var(--success))]'
  if (state === 'reconnecting') return 'bg-[hsl(var(--warning))]'
  return 'bg-muted-foreground'
}

function activeChatClassName(running: boolean, state: StreamState, error: string) {
  if (error || state === 'disconnected') return 'bg-destructive'
  if (running) return 'bg-[hsl(var(--success))]'
  return streamStateClassName(state, error)
}

function formatCompactCount(value: number) {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}k`
  return String(value)
}

function formatPercent(value: number) {
  return `${Math.round(value * 100)}%`
}

function formatCost(cost: { amount: number; currency: string }) {
  const currency = cost.currency.toUpperCase()
  if (currency === 'USD') {
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency,
      maximumFractionDigits: cost.amount < 1 ? 4 : 2,
    }).format(cost.amount)
  }
  return `${formatCompactCount(cost.amount)} ${currency}`
}

function formatShortDateTime(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  }).format(new Date(value))
}
