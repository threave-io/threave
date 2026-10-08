import { GitBranch, Loader2, WifiOff } from 'lucide-react'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type {
  AgentEvent,
  MessageAttachment,
  Session,
  SessionRuntimeAgentOptions,
  SkillReference,
  SubmitAgentOptions,
  SubmitMessageResponse,
  UpdateSessionRuntimeAgentOptionsResponse,
  UserInputAnswers,
} from '@/lib/api'
import type { StreamState } from '@/hooks/use-session-events'
import { ChatTranscript } from '@/components/chat-transcript'
import { PromptComposer, type PromptInsertion } from '@/components/prompt-composer'
import { PermissionQueue } from '@/components/permission-queue'
import { SessionTitle } from '@/components/session-title-editor'
import { UserInputCard } from '@/components/user-input-card'
import {
  activeRunActivity,
  activeRunID,
  activeStreamingResponse,
  activeThinking,
  activeToolActivity,
  latestTerminalEvent,
  pendingUserInputRequest,
  pendingPermissionRequests,
  type ChatTranscriptMessage,
  type TranscriptSequenceRange,
} from '@/lib/events'
import { cn } from '@/lib/utils'
import { getMessageSubmissionStatus } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { pendingSubmissionsChanged, readPendingSubmissions, removePendingSubmission, savePendingSubmission, type PendingSubmission, type SubmissionStatus } from '@/lib/pending-submissions'

type Props = {
  session: Session | null
  resolvingSessionID?: string | null
  events: AgentEvent[]
  liveEvents?: AgentEvent[]
  streamState: StreamState
  hasOlderEvents?: boolean
  hasNewerEvents?: boolean
  loadingOlderEvents?: boolean
  loadingNewerEvents?: boolean
  olderHistoryUnavailable?: boolean
  errorMessage?: string
  showDebugEvents: boolean
  onLoadOlderEvents?: () => Promise<void> | void
  onLoadNewerEvents?: () => Promise<void> | void
  onJumpToLatest?: () => Promise<void> | void
  onFollowingTailChange?: (following: boolean) => void
  onSubmitPrompt: (
    content: string,
    agentOptions?: SubmitAgentOptions,
    attachments?: MessageAttachment[],
    queue?: boolean,
    skills?: SkillReference[],
    clientSubmissionID?: string,
    steerRunID?: string,
  ) => Promise<SubmitMessageResponse | void>
  onUpdateRuntimeAgentOptions?: (
    sessionID: string,
    options: SessionRuntimeAgentOptions,
    initializeIfAbsent?: boolean,
  ) => Promise<UpdateSessionRuntimeAgentOptionsResponse>
  onAnswerUserInput: (requestID: string, answers: UserInputAnswers) => Promise<void>
  onResolvePermission?: (requestID: string, optionID: string) => Promise<void>
  onCancel: () => Promise<void>
  onOpenFilePath?: (path: string) => Promise<void> | void
  onComposerFocus?: () => void
  composerFocusRequest?: number
  onErrorMessageChange?: (message: string) => void
  headerActions?: ReactNode
  mobileLeadingAction?: ReactNode
  focusedEventSeq?: number
  focusedEventRequest?: number
  onVisibleSequenceRangeChange?: (range: TranscriptSequenceRange | null) => void
  offline?: boolean
  onSelectParent?: (sessionID: string) => void
}

export function SessionDetail({
  session,
  resolvingSessionID = null,
  events,
  liveEvents,
  streamState,
  hasOlderEvents = false,
  hasNewerEvents = false,
  loadingOlderEvents = false,
  loadingNewerEvents = false,
  olderHistoryUnavailable = false,
  errorMessage = '',
  showDebugEvents,
  onLoadOlderEvents,
  onLoadNewerEvents,
  onJumpToLatest,
  onFollowingTailChange,
  onSubmitPrompt,
  onUpdateRuntimeAgentOptions,
  onAnswerUserInput,
  onResolvePermission = async () => undefined,
  onCancel,
  onOpenFilePath,
  onComposerFocus,
  composerFocusRequest = 0,
  onErrorMessageChange,
  headerActions,
  mobileLeadingAction,
  focusedEventSeq = 0,
  focusedEventRequest = 0,
  onVisibleSequenceRangeChange,
  offline = false,
  onSelectParent,
}: Props) {
  const bottomInsetRef = useRef<HTMLDivElement>(null)
  const [bottomInsetHeight, setBottomInsetHeight] = useState(0)
  const [promptInsertion, setPromptInsertion] = useState<PromptInsertion | null>(null)
  const followUpSessionID = session?.id
  const handleFollowUp = useCallback((prompt: string) => {
    if (followUpSessionID) setPromptInsertion({ sessionID: followUpSessionID, prompt })
  }, [followUpSessionID])
  const handlePromptInserted = useCallback((insertion: PromptInsertion) => {
    setPromptInsertion((current) => current === insertion ? null : current)
  }, [])
  const [optimisticUserMessages, setOptimisticUserMessages] = useState<ChatTranscriptMessage[]>([])
  const [pendingSubmissions, setPendingSubmissions] = useState<PendingSubmission[]>([])
  const [pendingLoaded, setPendingLoaded] = useState(false)
  const [pendingError, setPendingError] = useState('')
  const [checkingSubmission, setCheckingSubmission] = useState(false)
  const [sendingSubmissionID, setSendingSubmissionID] = useState<string | null>(null)
  const [submissionStates, setSubmissionStates] = useState<Record<string, SubmissionStatus['state']>>({})
  const submitInFlightRef = useRef(false)
  const statusEvents = useMemo(() => offline ? [] : (liveEvents ?? events), [events, liveEvents, offline])
  const persistedClientSubmissionIDs = useMemo(() => clientSubmissionIDs(events), [events])
  const acceptedSubmissionIDs = useMemo(() => new Set([...persistedClientSubmissionIDs, ...clientSubmissionIDs(liveEvents ?? [])]), [persistedClientSubmissionIDs, liveEvents])
  const acceptedSubmissionIDsRef = useRef(new Set<string>())
  useLayoutEffect(() => { acceptedSubmissionIDsRef.current = acceptedSubmissionIDs }, [acceptedSubmissionIDs])
  const visibleOptimisticUserMessages = useMemo(
    () => optimisticUserMessages.filter((message) => !persistedClientSubmissionIDs.has(message.id)),
    [optimisticUserMessages, persistedClientSubmissionIDs],
  )
  const userInputRequest = useMemo(
    () => (session?.status === 'running' ? pendingUserInputRequest(statusEvents) : null),
    [session?.status, statusEvents],
  )
  const permissionRequests = useMemo(
    () => (session?.status === 'running' ? pendingPermissionRequests(statusEvents) : []),
    [session?.status, statusEvents],
  )
  const blockingUserInput = Boolean(userInputRequest && userInputRequest.delivery !== 'async')
  const thinking = useMemo(
    () => session?.status === 'running' && !blockingUserInput && activeThinking(statusEvents),
    [session?.status, statusEvents, blockingUserInput],
  )
  const runActivity = useMemo(
    () => (session?.status === 'running' && !blockingUserInput ? activeRunActivity(statusEvents) : null),
    [session?.status, statusEvents, blockingUserInput],
  )
  const streamingResponse = useMemo(
    () => session?.status === 'running' && !blockingUserInput && activeStreamingResponse(statusEvents),
    [session?.status, statusEvents, blockingUserInput],
  )
  const activeTool = useMemo(
    () => session?.status === 'running' && !blockingUserInput && activeToolActivity(statusEvents),
    [session?.status, statusEvents, blockingUserInput],
  )
  const activityStatus = thinking
    ? ({ kind: 'thinking' } as const)
    : runActivity && !streamingResponse && !activeTool
      ? ({ kind: 'working', since: runActivity.lastVisibleActivityAt } as const)
      : null
  const latestTerminal = useMemo(() => latestTerminalEvent(statusEvents), [statusEvents])
  const queueEvents = useMemo(() => queuedMessageEvents(statusEvents), [statusEvents])
  const steeringRunID = useMemo(() => activeRunID(statusEvents), [statusEvents])

  useEffect(() => {
    let closed = false
    let version = 0
    setPendingLoaded(false)
    setPendingSubmissions([])
    setPendingError('')
    setSubmissionStates({})
    const sessionID = session?.id
    if (!sessionID) return
    async function refresh() {
      const current = ++version
      try {
        const pending = await readPendingSubmissions(sessionID!)
        if (!closed && current === version) {
          setPendingSubmissions(pending)
          setPendingLoaded(true)
          if (pending.length === 0) setPendingError('')
        }
      } catch {
        if (!closed) setPendingError('Unable to read pending-message storage. Reload after freeing storage; sending is paused to avoid duplicates.')
      }
    }
    void refresh()
    window.addEventListener(pendingSubmissionsChanged, refresh)
    return () => { closed = true; window.removeEventListener(pendingSubmissionsChanged, refresh) }
  }, [session?.id])

  useEffect(() => {
    for (const pending of pendingSubmissions) {
      if (acceptedSubmissionIDsRef.current.has(pending.id)) {
        void removePendingSubmission(pending.id).catch(() => setPendingError('Message accepted, but local recovery storage could not be cleared. Check delivery before retrying.'))
      }
    }
  }, [events, liveEvents, pendingSubmissions])

  useEffect(() => {
    if (offline || submitInFlightRef.current || checkingSubmission) return
    let closed = false
    for (const pending of pendingSubmissions) {
      void getMessageSubmissionStatus(pending.sessionID, pending.id).then(async (status) => {
        if (closed) return
        if (status.state === 'accepted') await removePendingSubmission(pending.id)
        else setSubmissionStates((current) => ({ ...current, [pending.id]: status.state }))
      }).catch(() => { /* Keep the durable record and explicit Check delivery action. */ })
    }
    return () => { closed = true }
  }, [offline, pendingSubmissions, checkingSubmission])

  useEffect(() => {
    setOptimisticUserMessages([])
  }, [session?.id])

  useEffect(() => {
    if (persistedClientSubmissionIDs.size === 0) return
    setOptimisticUserMessages((current) => {
      const next = current.filter((message) => !persistedClientSubmissionIDs.has(message.id))
      return next.length === current.length ? current : next
    })
  }, [persistedClientSubmissionIDs])

  const handleSubmitPrompt = useCallback(async (
    content: string,
    agentOptions?: SubmitAgentOptions,
    attachments: MessageAttachment[] = [],
    queue = false,
    skills: SkillReference[] = [],
    onPrepared?: () => void,
    steerRunID?: string,
  ) => {
    if (!session || submitInFlightRef.current) throw new Error('A submission is already in progress.')
    submitInFlightRef.current = true
    try {
      if ((await readPendingSubmissions(session.id)).length > 0) {
        throw new Error('Check the pending message below before sending another message.')
      }
      const clientSubmissionID = newClientSubmissionID()
      setSendingSubmissionID(clientSubmissionID)
      const submittedAt = new Date().toISOString()
      await savePendingSubmission({ id: clientSubmissionID, sessionID: session.id, content, options: agentOptions, attachments, queue, skills, createdAt: submittedAt, ...(steerRunID ? { steerRunID } : {}) })
      onPrepared?.()
      const optimisticMessage: ChatTranscriptMessage = {
        id: clientSubmissionID,
        role: 'user',
        label: 'You',
        variant: 'default',
        text: content,
        attachments: attachments.map((attachment) => ({
          name: attachment.name,
          mediaType: attachment.media_type,
          dataURL: attachment.data_url,
          sourceURL: attachment.data_url,
          sizeBytes: attachment.size_bytes,
        })),
        skills,
        status: 'pending',
        createdAt: submittedAt,
        completedAt: '',
        durationMs: null,
        tools: [],
        streaming: false,
        startSeq: 0,
        endSeq: 0,
      }
      if (!queue) setOptimisticUserMessages((current) => [...current, optimisticMessage])

      try {
        const response = await onSubmitPrompt(
          content,
          agentOptions,
          attachments,
          queue,
          skills,
          clientSubmissionID,
          ...(steerRunID ? [steerRunID] : []),
        )
        if (response?.accepted_as === 'queued') {
          setOptimisticUserMessages((current) => current.filter((message) => message.id !== clientSubmissionID))
        }
        await removePendingSubmission(clientSubmissionID)
        setPendingSubmissions((current) => current.filter((pending) => pending.id !== clientSubmissionID))
        setPendingError('')
        return response
      } catch (error) {
        setOptimisticUserMessages((current) => current.filter((message) => message.id !== clientSubmissionID))
        if (acceptedSubmissionIDsRef.current.has(clientSubmissionID)) {
          await removePendingSubmission(clientSubmissionID)
          return
        }
        // Keep the exact request, including attachments/options and identity.
        // Do not put an uncertain send back into the ordinary composer as a new send.
        setPendingError(error instanceof Error ? error.message : 'Message delivery is uncertain.')
        return
      }
    } finally { submitInFlightRef.current = false; setSendingSubmissionID(null) }
  }, [onSubmitPrompt, session])

  async function checkSubmission(pending: PendingSubmission, retry = false) {
    if (checkingSubmission || offline) return
    setCheckingSubmission(true)
    setPendingError('')
    try {
      const status = await getMessageSubmissionStatus(pending.sessionID, pending.id)
      setSubmissionStates((current) => ({ ...current, [pending.id]: status.state }))
      if (status.state === 'accepted') {
        await removePendingSubmission(pending.id)
      } else if (status.state === 'not_received' && retry) {
        setSubmissionStates((current) => ({ ...current, [pending.id]: 'unknown' }))
        await onSubmitPrompt(pending.content, pending.options, pending.attachments, pending.queue, pending.skills, pending.id, ...(pending.steerRunID ? [pending.steerRunID] : []))
        await removePendingSubmission(pending.id)
      } else {
        setPendingError(status.state === 'not_received'
          ? 'The server has not received this message. Retry safely resends it, or dismiss it to send a different message.'
          : status.state === 'rejected'
            ? 'The server rejected this message. Copy the text below, then dismiss this record to edit and send a new message.'
            : 'Delivery is still uncertain. Your message is preserved; check history before taking further action.')
      }
    } catch (error) {
      setPendingError(error instanceof Error ? error.message : 'Unable to check delivery.')
    } finally { setCheckingSubmission(false) }
  }

  useLayoutEffect(() => {
    const element = bottomInsetRef.current
    if (!element) {
      return
    }
    const target = element

    function updateHeight() {
      const nextHeight = measureBottomStackHeight(target)
      if (nextHeight < 1) {
        return
      }
      setBottomInsetHeight((current) => (current === nextHeight ? current : nextHeight))
    }

    updateHeight()

    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', updateHeight)
      return () => window.removeEventListener('resize', updateHeight)
    }

    const observer = new ResizeObserver(() => updateHeight())
    observer.observe(target)
    return () => observer.disconnect()
  }, [offline, session?.id, userInputRequest, permissionRequests.length])

  if (!session) {
    if (resolvingSessionID) {
      if (offline) {
        return (
          <section className="command-workspace flex h-full w-full min-h-0 flex-col items-center justify-center overflow-hidden p-8 text-center">
            <WifiOff className="mb-3 size-6 text-muted-foreground" aria-hidden="true" />
            <h2 className="text-lg font-semibold">Session unavailable offline</h2>
            <p className="mt-2 max-w-sm text-sm text-muted-foreground">
              This session has not been saved on this device yet. It will load when the server reconnects.
            </p>
          </section>
        )
      }
      return (
        <section className="command-workspace flex h-full w-full min-h-0 flex-col items-center justify-center overflow-hidden p-8 text-center">
          <Loader2 className="mb-3 size-5 animate-spin text-muted-foreground" aria-hidden="true" />
          <h2 className="text-lg font-semibold">Loading session...</h2>
          <p className="mt-2 max-w-sm text-sm text-muted-foreground">Loading session details and chat history.</p>
        </section>
      )
    }

    return (
      <section className="command-workspace flex h-full w-full min-h-0 flex-col items-center justify-center overflow-hidden p-8 text-center">
        <h2 className="text-lg font-semibold">No session selected</h2>
        {errorMessage ? (
          <p role="alert" className="mt-2 max-w-sm text-sm text-destructive">
            {errorMessage}
          </p>
        ) : (
          <p className="mt-2 max-w-sm text-sm text-muted-foreground">
            Create or select a session to monitor agent work.
          </p>
        )}
      </section>
    )
  }

  if (resolvingSessionID && streamState === 'loading' && events.length === 0) {
    return (
      <section className="command-workspace flex h-full w-full min-h-0 flex-col items-center justify-center overflow-hidden p-8 text-center">
        <Loader2 className="mb-3 size-5 animate-spin text-muted-foreground" aria-hidden="true" />
        <h2 className="text-lg font-semibold">Loading session...</h2>
        <p className="mt-2 max-w-sm text-sm text-muted-foreground">Loading session details and chat history.</p>
      </section>
    )
  }

  const composerDisabled = session.status === 'running'
  const disabledReason = session.status === 'running' ? 'This session is running.' : ''
  const recoverableSubmissions = pendingSubmissions.filter((pending) => pending.id !== sendingSubmissionID && !acceptedSubmissionIDs.has(pending.id))

  return (
    <section className="relative h-full w-full min-h-0 overflow-hidden bg-transparent">
      <div className="absolute inset-0 overflow-hidden">
        <ChatTranscript
          key={session.id}
          events={events}
          optimisticUserMessages={visibleOptimisticUserMessages}
          loading={!offline && streamState === 'loading'}
          error={errorMessage}
          emptyMessage={offline ? "This session's history hasn't been saved on this device yet." : undefined}
          topInset="sessionHeader"
          bottomInsetHeight={bottomInsetHeight}
          pinToLatestOnMount
          autoScroll={!offline && session.status === 'running' && !blockingUserInput}
          activityStatus={activityStatus}
          showDebugEvents={showDebugEvents}
          hasOlderEvents={hasOlderEvents && !olderHistoryUnavailable}
          olderHistoryUnavailable={olderHistoryUnavailable}
          hasNewerEvents={hasNewerEvents}
          loadingOlderEvents={loadingOlderEvents}
          loadingNewerEvents={loadingNewerEvents}
          onLoadOlderEvents={onLoadOlderEvents}
          onLoadNewerEvents={onLoadNewerEvents}
          onJumpToLatest={onJumpToLatest}
          onFollowingTailChange={onFollowingTailChange}
          onOpenFilePath={onOpenFilePath}
          onFollowUp={handleFollowUp}
          focusSeq={focusedEventSeq}
          focusRequest={focusedEventRequest}
          onVisibleSequenceRangeChange={onVisibleSequenceRangeChange}
        />
        <div
          data-testid="mobile-floating-session-header"
          className="mobile-floating-header-shell pointer-events-none absolute inset-x-0 z-20 p-3 lg:hidden"
        >
          <ChatSessionHeader
            session={session}
            headerActions={headerActions}
            leadingAction={mobileLeadingAction}
            onSelectParent={onSelectParent}
            showParentSession={false}
          />
        </div>
        <div
          data-testid="floating-session-header"
          className="pointer-events-none absolute inset-x-0 top-0 z-20 hidden p-3 lg:block"
        >
          <ChatSessionHeader
            session={session}
            headerActions={headerActions}
            onSelectParent={onSelectParent}
            showParentSession={false}
          />
        </div>
      </div>
      <div
        ref={bottomInsetRef}
        className="session-bottom-safe-area pointer-events-none absolute inset-x-0 bottom-0 z-20"
      >
        <div data-testid="session-bottom-stack" className="pointer-events-auto relative flex flex-col gap-3 pt-2">
          {offline ? (
            <div
              role="status"
              data-testid="offline-session-status"
              className="mx-2 flex items-center justify-center gap-2 rounded-lg border border-border/80 bg-background/92 px-3 py-2 text-xs text-muted-foreground shadow-sm sm:mx-3"
            >
              <WifiOff className="size-3.5 shrink-0" aria-hidden="true" />
              <span>Offline · Showing saved history. Drafts stay on this device.</span>
            </div>
          ) : null}
          <PermissionQueue requests={permissionRequests} onResolve={onResolvePermission} />
          <UserInputCard key={userInputRequest?.requestID} request={userInputRequest} disabled={offline} onAnswer={onAnswerUserInput} />
          {recoverableSubmissions.length > 0 || pendingError ? (
            <section aria-label="Pending message recovery" className="mx-3 max-h-64 overflow-auto rounded-lg border border-border bg-background p-3 text-xs">
              <p role="status">Message awaiting confirmation. It is saved on this device; nothing is automatically resent.</p>
              {pendingError ? <p role="alert" className="my-2 text-destructive">{pendingError}</p> : null}
              {recoverableSubmissions.map((pending) => (
                <div key={pending.id} className="mt-2">
                  <pre className="max-h-24 overflow-auto whitespace-pre-wrap break-words">{pending.content}</pre>
                  {submissionStates[pending.id] === 'not_received' ? <p className="mt-1 text-muted-foreground">The server has not received this message. You can retry it or dismiss it to send another message.</p> : null}
                  {pending.attachments.length > 0 ? <p>{pending.attachments.length} image attachment(s) preserved</p> : null}
                  <div className="mt-2 flex flex-wrap gap-2">
                    <Button size="sm" variant="outline" disabled={offline || checkingSubmission} onClick={() => void checkSubmission(pending)}>Check delivery</Button>
                    <Button size="sm" variant="outline" disabled={offline || checkingSubmission} onClick={() => void checkSubmission(pending, true)}>Retry safely</Button>
                    {['rejected', 'not_received'].includes(submissionStates[pending.id]) ? <Button size="sm" variant="outline" disabled={checkingSubmission} onClick={() => {
                      void removePendingSubmission(pending.id).then(() => setPendingError('')).catch(() => setPendingError('Unable to clear the pending record.'))
                    }}>{submissionStates[pending.id] === 'rejected' ? 'Dismiss rejected message' : 'Dismiss unsent message'}</Button> : null}
                  </div>
                </div>
              ))}
            </section>
          ) : null}
          <PromptComposer
            key={session.id}
            sessionID={session.id}
            agentType={session.agent_type}
            sessionAgentOptions={session.agent_options}
            sessionAgentOptionsSeq={session.last_event_seq}
            sessionStatus={session.status}
            steeringRunID={steeringRunID}
            hasPendingUserInput={blockingUserInput}
            latestTerminalEvent={latestTerminal}
            queueEvents={queueEvents}
            disabled={composerDisabled}
            disabledReason={disabledReason}
            onSubmit={handleSubmitPrompt}
            onUpdateRuntimeAgentOptions={onUpdateRuntimeAgentOptions}
            onCancel={session.status === 'running' ? onCancel : undefined}
            onError={onErrorMessageChange}
            onFocus={onComposerFocus}
            focusRequest={composerFocusRequest}
            promptInsertion={promptInsertion}
            onPromptInserted={handlePromptInserted}
            offline={offline}
            submissionBlocked={!pendingLoaded || pendingSubmissions.length > 0}
            prepareBeforeClear
          />
        </div>
      </div>
    </section>
  )
}

function measureBottomStackHeight(element: HTMLElement) {
  return Math.ceil(element.getBoundingClientRect().height)
}

let clientSubmissionSequence = 0

function newClientSubmissionID() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  clientSubmissionSequence += 1
  return `client-${Date.now()}-${clientSubmissionSequence}`
}

function clientSubmissionIDs(events: AgentEvent[]) {
  const ids = new Set<string>()
  for (const event of events) {
    if (!['user.message.completed', 'user.message.queued'].includes(event.type) || typeof event.payload !== 'object' || event.payload === null) {
      continue
    }
    const value = (event.payload as Record<string, unknown>).client_submission_id
    if (typeof value === 'string' && value) ids.add(value)
  }
  return ids
}

export function ChatSessionHeader({
  session,
  errorMessage = '',
  headerActions,
  leadingAction,
  onSelectParent,
  showParentSession = true,
}: {
  session: Session
  errorMessage?: string
  headerActions?: ReactNode
  leadingAction?: ReactNode
  onSelectParent?: (sessionID: string) => void
  showParentSession?: boolean
}) {
  return (
    <div className="pointer-events-auto">
      <div
        className={cn(
          'command-chat-header flex min-h-14 items-center justify-between gap-3 border border-border/90 px-3 py-2 shadow-[0_10px_30px_hsl(var(--foreground)/0.10)]',
          errorMessage ? 'rounded-t-xl' : 'rounded-xl',
        )}
      >
        {leadingAction ? <div className="shrink-0">{leadingAction}</div> : null}
        <div className="min-w-0 flex-1">
          <SessionTitle title={session.title} />
          {showParentSession && session.parent_session_id ? (
            <button
              type="button"
              onClick={() => onSelectParent?.(session.parent_session_id!)}
              disabled={!onSelectParent}
              className="mt-0.5 inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground disabled:pointer-events-none"
            >
              <GitBranch className="size-3" />
              Parent session
            </button>
          ) : null}
        </div>
        {headerActions}
      </div>
      {errorMessage ? (
        <div
          role="alert"
          className="command-error-banner -mt-px rounded-b-xl border-x border-b border-destructive/40 px-3 py-2 text-sm font-medium text-destructive shadow-[0_10px_30px_hsl(var(--foreground)/0.10)]"
        >
          {errorMessage}
        </div>
      ) : null}
    </div>
  )
}

function queuedMessageEvents(events: AgentEvent[]) {
  return events.filter(
    (event) =>
      event.type === 'user.message.queued' ||
      event.type === 'user.message.queue.removed' ||
      queuedUserMessageCompleted(event),
  )
}

function queuedUserMessageCompleted(event: AgentEvent) {
  if (event.type !== 'user.message.completed' || typeof event.payload !== 'object' || event.payload === null) {
    return false
  }
  return 'queue_item_id' in event.payload
}
