import {
  BookOpen,
  ChevronDown,
  ClipboardList,
  CornerDownLeft,
  Paperclip,
  RefreshCw,
  Search,
  Send,
  SlidersHorizontal,
  Square,
  X,
  Zap,
} from 'lucide-react'
import {
  type ClipboardEvent,
  type DragEvent,
  type ChangeEvent,
  type CSSProperties,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import {
  type AgentEvent,
  fetchAgentOptions,
  fetchSessionSkills,
  fetchQueuedMessages,
  type AgentType,
  type AgentSkill,
  type AgentSkillError,
  type CodexAgentOptions,
  type CodexModelOption,
  type CodexServiceTierOption,
  type MessageAttachment,
  type OpenCodeAgentOptions,
  type PiAgentOptions,
  type QueuedMessage,
  type SessionAgentOptions,
  type SessionRuntimeAgentOptions,
  type SkillReference,
  type SkillScope,
  type SubmitMessageResponse,
  type UpdateSessionRuntimeAgentOptionsResponse,
  updateQueuedMessagesCache,
  removeQueuedMessage as deleteQueuedMessage,
  type SubmitAgentOptions,
} from '@/lib/api'
import { cn } from '@/lib/utils'
import { publishComposerActivity } from '@/lib/composer-activity'
import { useComposerDraftStorage } from '@/hooks/use-composer-draft-storage'
import { composerStorageKey, loadComposerDraft, parseComposerDraft, saveComposerDraft } from '@/lib/composer-drafts'

const maxPromptRows = 5
const fallbackLineHeight = 20
const maxImageAttachmentBytes = 5 * 1024 * 1024
const maxImageAttachmentCount = 8
const maxQueuedMessages = 5
const queueShortcutLabel = 'Cmd/Ctrl+Shift+Enter'
const composerAutoFocusMediaQuery = '(hover: hover) and (pointer: fine)'
const noQueueEvents: AgentEvent[] = []
const claudeModelOptions = [
  { value: '', label: 'Default' },
  { value: 'opus', label: 'Opus' },
  { value: 'sonnet', label: 'Sonnet' },
]
const claudeEffortOptions = ['low', 'medium', 'high', 'xhigh', 'max'].map((value) => ({ value, label: value }))

export type PromptInsertion = { sessionID: string; prompt: string }

type Props = {
  sessionID?: string
  agentType?: AgentType
  sessionAgentOptions?: SessionAgentOptions
  sessionAgentOptionsSeq?: number
  sessionStatus?: 'idle' | 'running' | 'failed'
  steeringRunID?: string
  hasPendingUserInput?: boolean
  latestTerminalEvent?: AgentEvent | null
  queueEvents?: AgentEvent[]
  latestQueueEvent?: AgentEvent | null
  disabled: boolean
  disabledReason: string
  onSubmit: (
    content: string,
    agentOptions?: SubmitAgentOptions,
    attachments?: MessageAttachment[],
    queue?: boolean,
    skills?: SkillReference[],
    onPrepared?: () => void,
    steerRunID?: string,
  ) => Promise<SubmitMessageResponse | void>
  onUpdateRuntimeAgentOptions?: (
    sessionID: string,
    options: SessionRuntimeAgentOptions,
    initializeIfAbsent?: boolean,
  ) => Promise<UpdateSessionRuntimeAgentOptionsResponse>
  onCancel?: () => Promise<void>
  onError?: (message: string) => void
  onFocus?: () => void
  focusRequest?: number
  promptInsertion?: PromptInsertion | null
  onPromptInserted?: (insertion: PromptInsertion) => void
  offline?: boolean
  submissionBlocked?: boolean
  prepareBeforeClear?: boolean
}

type CodexSelection = {
  model: string
  reasoning_effort: string
  fast_mode: boolean
  planning_mode: boolean
}

type ClaudeSelection = {
  model: string
  effort: string
  planning_mode: boolean
}

type OpenCodeSelection = {
  model: string
  planning_mode: boolean
}

type PiSelection = {
  model: string
  thinking_level: string
}

type RuntimeAgentOptionsSyncState = {
  inFlight: boolean
  queued: {
    options: SessionRuntimeAgentOptions
    initializeIfAbsent: boolean
  } | null
  initialized: boolean
  confirmedKey: string
  confirmedOptions: SessionRuntimeAgentOptions | null
  latestServerSeq: number
  adoptingServerKey: string
}

type ComposerStorageValue = {
  draft?: string
  selectedSkills?: SkillReference[]
  codexSelection?: Partial<CodexSelection>
  claudeSelection?: Partial<ClaudeSelection>
  opencodeSelection?: Partial<OpenCodeSelection>
  piSelection?: Partial<PiSelection>
}

type ComposerAttachment = MessageAttachment & {
  id: string
}

type SkillTypeahead = {
  start: number
  end: number
  query: string
}

export function PromptComposer({
  sessionID,
  agentType = 'fake',
  sessionAgentOptions,
  sessionAgentOptionsSeq = 0,
  sessionStatus = 'idle',
  steeringRunID = '',
  hasPendingUserInput = false,
  queueEvents = noQueueEvents,
  latestQueueEvent = null,
  disabled,
  disabledReason,
  onSubmit,
  onUpdateRuntimeAgentOptions,
  onCancel,
  onError,
  onFocus,
  focusRequest = 0,
  promptInsertion,
  onPromptInserted,
  offline = false,
  submissionBlocked = false,
  prepareBeforeClear = false,
}: Props) {
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const queueEventsRef = useRef<Map<string, AgentEvent>>(new Map())
  const removedQueueIDsRef = useRef(new Set<string>())
  const queueActionRef = useRef(false)
  const submitActionRef = useRef(false)
  const attachmentReadsRef = useRef({ pending: 0 })
  const lifetimeRef = useRef({ active: true, sessionID })
  const skillsLoadedSessionRef = useRef('')
  const [content, setContent] = useState(() =>
    ensureInlineSkillTokens(loadDraft(sessionID), loadSelectedSkills(sessionID)),
  )
  const [selectedSkills, setSelectedSkills] = useState<SkillReference[]>(() => loadSelectedSkills(sessionID))
  const { storageError: draftStorageError, markRestored: markDraftRestored } = useComposerDraftStorage(sessionID, { draft: content, selectedSkills })
  const promptSelectionRef = useRef({ start: content.length, end: content.length })
  const [skills, setSkills] = useState<AgentSkill[]>([])
  const [skillErrors, setSkillErrors] = useState<AgentSkillError[]>([])
  const [skillsLoading, setSkillsLoading] = useState(false)
  const [skillsError, setSkillsError] = useState('')
  const [skillsOpen, setSkillsOpen] = useState(false)
  const [skillSearch, setSkillSearch] = useState('')
  const [skillScopeFilter, setSkillScopeFilter] = useState<SkillScope | null>(null)
  const [skillTypeahead, setSkillTypeahead] = useState<SkillTypeahead | null>(null)
  const [skillHighlight, setSkillHighlight] = useState(0)
  const [promptScrollTop, setPromptScrollTop] = useState(0)
  const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([])
  const [attachments, setAttachments] = useState<ComposerAttachment[]>([])
  const [dragActive, setDragActive] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [cancelling, setCancelling] = useState(false)
  const [queueActionPending, setQueueActionPending] = useState(false)
  const currentDraftRef = useRef({ content, selectedSkills, attachments, offline, submitting })
  const adoptedDraftRef = useRef<string | null>(null)
  useLayoutEffect(() => {
    currentDraftRef.current = { content, selectedSkills, attachments, offline, submitting }
  }, [content, selectedSkills, attachments, offline, submitting])
  useLayoutEffect(() => {
    const lifetime = { active: true, sessionID }
    lifetimeRef.current = lifetime
    return () => { lifetime.active = false }
  }, [sessionID])
  const [codexOptions, setCodexOptions] = useState<CodexAgentOptions | null>(null)
  const [codexOptionsLoading, setCodexOptionsLoading] = useState(false)
  const [codexOptionsError, setCodexOptionsError] = useState('')
  const [opencodeOptions, setOpenCodeOptions] = useState<OpenCodeAgentOptions | null>(null)
  const [opencodeOptionsLoading, setOpenCodeOptionsLoading] = useState(false)
  const [opencodeOptionsError, setOpenCodeOptionsError] = useState('')
  const [piOptions, setPiOptions] = useState<PiAgentOptions | null>(null)
  const [piOptionsLoading, setPiOptionsLoading] = useState(false)
  const [piOptionsError, setPiOptionsError] = useState('')
  const initialServerRuntimeOptions = runtimeAgentOptionsFromSession(agentType, sessionAgentOptions)
  const [codexSelection, setCodexSelection] = useState<CodexSelection>(() =>
    loadCodexSelection(sessionID, initialServerRuntimeOptions),
  )
  const [claudeSelection, setClaudeSelection] = useState<ClaudeSelection>(() =>
    loadClaudeSelection(sessionID, initialServerRuntimeOptions),
  )
  const [opencodeSelection, setOpenCodeSelection] = useState<OpenCodeSelection>(() =>
    loadOpenCodeSelection(sessionID, initialServerRuntimeOptions),
  )
  const [piSelection, setPiSelection] = useState<PiSelection>(() =>
    loadPiSelection(sessionID, initialServerRuntimeOptions),
  )
  const runtimeSyncRef = useRef<RuntimeAgentOptionsSyncState>({
    inFlight: false,
    queued: null,
    initialized: initialServerRuntimeOptions !== null,
    confirmedKey: runtimeAgentOptionsKey(initialServerRuntimeOptions),
    confirmedOptions: initialServerRuntimeOptions,
    latestServerSeq: sessionAgentOptionsSeq,
    adoptingServerKey: '',
  })
  const [closeSettingsSignal, setCloseSettingsSignal] = useState(0)
  const hasAttachments = attachments.length > 0
  const hasSelectedSkills = selectedSkills.length > 0
  const shouldLoadSkills = hasSelectedSkills || skillsOpen || skillTypeahead !== null
  const canSubmit = !submissionBlocked && !offline && !disabled && !submitting && !cancelling && !queueActionPending && (content.trim().length > 0 || hasAttachments || hasSelectedSkills)
  const showSendNow = (agentType === 'codex' || agentType === 'claude') && sessionStatus === 'running'
  const canSendNow = showSendNow && Boolean(steeringRunID) && !hasPendingUserInput && !submissionBlocked && !offline && !submitting && !cancelling && !queueActionPending && (content.trim().length > 0 || hasAttachments || hasSelectedSkills)
  const queueBlockedByAttachments = hasAttachments
  const canQueue =
    !submissionBlocked &&
    !offline &&
    !submitting &&
    !cancelling &&
    !queueActionPending &&
    (content.trim().length > 0 || hasSelectedSkills) &&
    !queueBlockedByAttachments &&
    queuedMessages.length < maxQueuedMessages
  const canCancel = !offline && disabled && Boolean(onCancel)
  const inputDisabled = submitting
  const promptPlaceholder =
    offline
      ? 'Offline — draft locally; sending resumes when connected.'
      : disabled && canCancel
      ? 'Prepare your next message...'
      : disabled
        ? disabledReason
        : 'Ask the agent to work on this repository...'
  const codexToolbarVisible = agentType === 'codex'
  const claudeToolbarVisible = agentType === 'claude'
  const skillsSupported = agentType === 'codex' || agentType === 'claude'
  const opencodeToolbarVisible = agentType === 'opencode'
  const piToolbarVisible = agentType === 'pi'
  const selectedCodexModel = useMemo(
    () => selectedModel(codexOptions, codexSelection.model),
    [codexOptions, codexSelection.model],
  )
  const selectedFastTier = useMemo(() => fastTierForModel(selectedCodexModel), [selectedCodexModel])
  const codexControlsDisabled = offline || submitting || codexOptionsLoading || !codexOptions
  const opencodeControlsDisabled = offline || submitting || opencodeOptionsLoading || !opencodeOptions
  const piControlsDisabled = offline || submitting || piOptionsLoading || !piOptions
  const codexPlanAvailable = Boolean(codexOptions?.collaboration_modes.some((mode) => mode.mode === 'plan'))
  const openCodePlanAvailable = opencodeOptions?.collaboration_modes.some((mode) => mode.mode === 'plan') ?? false
  const currentRuntimeAgentOptions = useMemo(
    () => runtimeAgentOptionsFromSelections(
      agentType,
      codexSelection,
      claudeSelection,
      opencodeSelection,
      piSelection,
    ),
    [agentType, claudeSelection, codexSelection, opencodeSelection, piSelection],
  )
  const runtimeAgentOptionsReady =
    agentType === 'claude' ||
    agentType === 'codex' && codexOptions !== null ||
    agentType === 'opencode' && opencodeOptions !== null ||
    agentType === 'pi' && piOptions !== null
  const sortedSkills = useMemo(() => sortSkills(skills), [skills])
  const skillScopes = useMemo(() => availableSkillScopes(sortedSkills), [sortedSkills])
  const browsedSkills = useMemo(
    () => filterSkills(
      skillScopeFilter ? sortedSkills.filter((skill) => skill.scope === skillScopeFilter) : sortedSkills,
      skillSearch,
    ),
    [skillScopeFilter, skillSearch, sortedSkills],
  )
  const suggestedSkills = useMemo(
    () => (skillTypeahead ? filterSkills(sortedSkills, skillTypeahead.query).slice(0, 8) : []),
    [skillTypeahead, sortedSkills],
  )

  const applyRuntimeAgentOptions = useCallback((options: SessionRuntimeAgentOptions) => {
    if (options.codex) {
      setCodexSelection(() => reconcileCodexSelection({
        model: options.codex?.model ?? '',
        reasoning_effort: options.codex?.reasoning_effort ?? '',
        fast_mode: options.codex?.fast_mode ?? false,
        planning_mode: options.codex?.planning_mode ?? false,
      }, codexOptions))
      return
    }
    if (options.claude) {
      setClaudeSelection({
        model: options.claude.model ?? '',
        effort: options.claude.effort ?? 'medium',
        planning_mode: options.claude.planning_mode ?? false,
      })
      return
    }
    if (options.opencode) {
      setOpenCodeSelection(() => reconcileOpenCodeSelection({
        model: options.opencode?.model ?? '',
        planning_mode: options.opencode?.planning_mode ?? false,
      }, opencodeOptions))
      return
    }
    if (options.pi) {
      setPiSelection(() => reconcilePiSelection({
        model: options.pi?.model ?? '',
        thinking_level: options.pi?.thinking_level ?? '',
      }, piOptions))
    }
  }, [codexOptions, opencodeOptions, piOptions])

  const enqueueRuntimeAgentOptions = useCallback((
    options: SessionRuntimeAgentOptions,
    initializeIfAbsent: boolean,
  ) => {
    if (offline || !sessionID || !onUpdateRuntimeAgentOptions) return
    const sync = runtimeSyncRef.current
    const optionsKey = runtimeAgentOptionsKey(options)
    if (!sync.inFlight && sync.initialized && optionsKey === sync.confirmedKey) return
    sync.queued = { options, initializeIfAbsent }
    if (sync.inFlight) return

    sync.inFlight = true
    void (async () => {
      while (sync.queued) {
        const pending = sync.queued
        sync.queued = null
        try {
          const response = await onUpdateRuntimeAgentOptions(
            sessionID,
            pending.options,
            pending.initializeIfAbsent && !sync.initialized,
          )
          const confirmed = runtimeAgentOptionsFromSession(agentType, response.session.agent_options)
          const responseSeq = response.session.last_event_seq ?? 0
          if (confirmed && responseSeq >= sync.latestServerSeq) {
            sync.latestServerSeq = responseSeq
            sync.confirmedOptions = confirmed
            sync.confirmedKey = runtimeAgentOptionsKey(confirmed)
          }
          sync.initialized = confirmed !== null
          clearStoredAgentSelections(sessionID)
          if (!sync.queued && sync.confirmedOptions) {
            sync.adoptingServerKey = sync.confirmedKey
            applyRuntimeAgentOptions(sync.confirmedOptions)
          }
        } catch (updateError) {
          sync.queued = null
          if (sync.confirmedOptions) {
            sync.adoptingServerKey = sync.confirmedKey
            applyRuntimeAgentOptions(sync.confirmedOptions)
          }
          onError?.(updateError instanceof Error ? updateError.message : 'Failed to save agent settings')
        }
      }
    })().finally(() => {
      sync.inFlight = false
    })
  }, [agentType, applyRuntimeAgentOptions, offline, onError, onUpdateRuntimeAgentOptions, sessionID])

  useEffect(() => {
    const serverOptions = runtimeAgentOptionsFromSession(agentType, sessionAgentOptions)
    if (!serverOptions) return
    const sync = runtimeSyncRef.current
    if (sessionAgentOptionsSeq < sync.latestServerSeq) return
    sync.latestServerSeq = sessionAgentOptionsSeq
    sync.initialized = true
    sync.confirmedOptions = serverOptions
    sync.confirmedKey = runtimeAgentOptionsKey(serverOptions)
    clearStoredAgentSelections(sessionID)
    if (!sync.inFlight && !sync.queued) {
      sync.adoptingServerKey = sync.confirmedKey
      applyRuntimeAgentOptions(serverOptions)
    }
  }, [agentType, applyRuntimeAgentOptions, sessionAgentOptions, sessionAgentOptionsSeq, sessionID])

  useEffect(() => {
    if (offline || !onUpdateRuntimeAgentOptions || !sessionID || !runtimeAgentOptionsReady || !currentRuntimeAgentOptions) {
      return
    }
    const sync = runtimeSyncRef.current
    const currentKey = runtimeAgentOptionsKey(currentRuntimeAgentOptions)
    if (sync.adoptingServerKey) {
      if (currentKey === sync.adoptingServerKey) sync.adoptingServerKey = ''
      return
    }
    enqueueRuntimeAgentOptions(currentRuntimeAgentOptions, !sync.initialized)
  }, [
    currentRuntimeAgentOptions,
    enqueueRuntimeAgentOptions,
    offline,
    onUpdateRuntimeAgentOptions,
    runtimeAgentOptionsReady,
    sessionID,
  ])

  const commitQueuedMessages = useCallback((
    update: QueuedMessage[] | ((current: QueuedMessage[]) => QueuedMessage[]),
  ) => {
    setQueuedMessages((current) => {
      const next = (typeof update === 'function' ? update(current) : update)
        .filter((message) => !removedQueueIDsRef.current.has(message.id))
      if (sessionID) updateQueuedMessagesCache(sessionID, next)
      return next
    })
  }, [sessionID])

  useLayoutEffect(() => {
    resizePromptTextarea(textareaRef.current)
  }, [content])

  useEffect(() => {
    if (offline || agentType !== 'codex') {
      return
    }

    let cancelled = false
    setCodexOptionsLoading(true)
    setCodexOptionsError('')
    void fetchAgentOptions('codex')
      .then((options) => {
        if (cancelled) return
        setCodexOptions(options)
        setCodexSelection((current) => reconcileCodexSelection(current, options))
      })
      .catch((loadError) => {
        if (cancelled) return
        setCodexOptionsError(loadError instanceof Error ? loadError.message : 'Failed to load Codex options')
      })
      .finally(() => {
        if (!cancelled) setCodexOptionsLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [agentType, offline])

  useEffect(() => {
    if (!skillsSupported || !sessionID) {
      skillsLoadedSessionRef.current = ''
      setSkills([])
      setSkillErrors([])
      setSkillsError('')
      setSkillsOpen(false)
      setSkillScopeFilter(null)
      setSkillTypeahead(null)
      return
    }
    if (!shouldLoadSkills) {
      return
    }
    if (offline) return
    if (skillsLoadedSessionRef.current === sessionID) {
      return
    }

    let cancelled = false
    setSkillsLoading(true)
    setSkillsError('')
    void fetchSessionSkills(sessionID)
      .then((catalog) => {
        if (cancelled) return
        const available = Array.isArray(catalog.skills) ? catalog.skills : []
        skillsLoadedSessionRef.current = sessionID
        setSkills(available)
        setSkillErrors(Array.isArray(catalog.errors) ? catalog.errors : [])
        setSkillScopeFilter((current) =>
          current && available.some((skill) => skill.scope === current) ? current : null,
        )
        setSelectedSkills((current) => {
          const next = current.filter((reference) =>
            available.some((skill) => skillKey(skill) === skillKey(reference)),
          )
          if (next.length !== current.length) {
            window.setTimeout(() => onError?.('One or more draft skills are no longer available.'), 0)
          }
          return next
        })
      })
      .catch((loadError) => {
        if (cancelled) return
        setSkills([])
        setSkillErrors([])
        setSkillScopeFilter(null)
        setSkillsError(loadError instanceof Error ? loadError.message : 'Failed to load skills')
      })
      .finally(() => {
        if (!cancelled) setSkillsLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [skillsSupported, offline, sessionID, onError, shouldLoadSkills])

  useEffect(() => {
    if (offline || agentType !== 'opencode') {
      return
    }

    let cancelled = false
    setOpenCodeOptionsLoading(true)
    setOpenCodeOptionsError('')
    void fetchAgentOptions('opencode')
      .then((options) => {
        if (cancelled) return
        setOpenCodeOptions(options)
        setOpenCodeSelection((current) => reconcileOpenCodeSelection(current, options))
      })
      .catch((loadError) => {
        if (cancelled) return
        setOpenCodeOptionsError(loadError instanceof Error ? loadError.message : 'Failed to load OpenCode options')
      })
      .finally(() => {
        if (!cancelled) setOpenCodeOptionsLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [agentType, offline])

  useEffect(() => {
    if (offline || agentType !== 'pi') {
      return
    }

    let cancelled = false
    setPiOptionsLoading(true)
    setPiOptionsError('')
    void fetchAgentOptions('pi')
      .then((options) => {
        if (cancelled) return
        setPiOptions(options)
        setPiSelection((current) => reconcilePiSelection(current, options))
      })
      .catch((loadError) => {
        if (cancelled) return
        setPiOptionsError(loadError instanceof Error ? loadError.message : 'Failed to load Pi options')
      })
      .finally(() => {
        if (!cancelled) setPiOptionsLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [agentType, offline])

  useEffect(() => {
    function receiveDraft(event: StorageEvent) {
      if (event.key !== composerStorageKey(sessionID)) return
      const incoming = parseComposerDraft(event.newValue)
      const previous = parseComposerDraft(event.oldValue)
      // Provider setting writes can carry an unchanged draft; those aren't draft edits.
      if (!incoming || JSON.stringify(incoming) === JSON.stringify(previous)) return
      const local = currentDraftRef.current
      const followingSavedDraft = adoptedDraftRef.current === JSON.stringify({ draft: local.content, selectedSkills: local.selectedSkills })
      if ((!followingSavedDraft && (local.content.length || local.selectedSkills.length)) || local.attachments.length ||
        local.submitting || queueActionRef.current || attachmentReadsRef.current.pending) return
      if (!followingSavedDraft && !incoming.draft.length && !incoming.selectedSkills.length) return
      const restoredContent = ensureInlineSkillTokens(incoming.draft, incoming.selectedSkills)
      adoptedDraftRef.current = JSON.stringify({ draft: restoredContent, selectedSkills: incoming.selectedSkills })
      markDraftRestored({ draft: restoredContent, selectedSkills: incoming.selectedSkills })
      setContent(restoredContent)
      setSelectedSkills(incoming.selectedSkills)
      promptSelectionRef.current = { start: restoredContent.length, end: restoredContent.length }
    }
    window.addEventListener('storage', receiveDraft)
    return () => window.removeEventListener('storage', receiveDraft)
  }, [markDraftRestored, sessionID])

  useEffect(() => {
    function handleWindowFocus() {
      if (document.activeElement === textareaRef.current) onFocus?.()
    }

    window.addEventListener('focus', handleWindowFocus)
    return () => window.removeEventListener('focus', handleWindowFocus)
  }, [onFocus])

  useEffect(() => {
    if (focusRequest > 0 && supportsComposerAutoFocus()) {
      textareaRef.current?.focus({ preventScroll: true })
    }
  }, [focusRequest])

  const appliedInsertionRef = useRef<PromptInsertion | null>(null)
  const insertionNeedsFocusRef = useRef(false)
  useEffect(() => {
    if (!promptInsertion || promptInsertion.sessionID !== sessionID ||
      appliedInsertionRef.current === promptInsertion || submitting) return
    appliedInsertionRef.current = promptInsertion
    setContent((current) => current ? `${current}\n\n${promptInsertion.prompt}` : promptInsertion.prompt)
    setSkillTypeahead(null)
    insertionNeedsFocusRef.current = true
    onPromptInserted?.(promptInsertion)
  }, [onPromptInserted, promptInsertion, sessionID, submitting])

  useLayoutEffect(() => {
    if (!insertionNeedsFocusRef.current) return
    insertionNeedsFocusRef.current = false
    textareaRef.current?.focus({ preventScroll: true })
    textareaRef.current?.setSelectionRange(content.length, content.length)
    promptSelectionRef.current = { start: content.length, end: content.length }
  }, [content])

  useEffect(() => {
    removedQueueIDsRef.current.clear()
  }, [sessionID])

  useEffect(() => {
    if (!sessionID) {
      queueEventsRef.current.clear()
      setQueuedMessages([])
      return
    }
    if (offline) return

    queueEventsRef.current.clear()
    let cancelled = false
    void fetchQueuedMessages(sessionID)
      .then((response) => {
        if (!cancelled) {
          const messages = Array.isArray(response.messages) ? response.messages : []
          commitQueuedMessages(reconcileQueuedMessages(messages, queueEventsRef.current.values()))
        }
      })
      .catch((queueError) => {
        if (!cancelled) onError?.(queueError instanceof Error ? queueError.message : 'Failed to load queued messages')
      })

    return () => {
      cancelled = true
    }
  }, [commitQueuedMessages, offline, sessionID, onError])

  useEffect(() => {
    if (!sessionID) return
    const incoming = latestQueueEvent ? [...queueEvents, latestQueueEvent] : queueEvents
    for (const event of incoming) {
      if (event.session_id !== sessionID) continue
      const queueItemID = queuedMessageID(event)
      if (queueItemID) queueEventsRef.current.set(queueItemID, event)
    }
    commitQueuedMessages((current) => reconcileQueuedMessages(current, queueEventsRef.current.values()))
  }, [commitQueuedMessages, latestQueueEvent, queueEvents, sessionID])

  useEffect(() => {
    if (onUpdateRuntimeAgentOptions) return
    saveCodexSelection(sessionID, codexSelection)
  }, [codexSelection, onUpdateRuntimeAgentOptions, sessionID])

  useEffect(() => {
    if (onUpdateRuntimeAgentOptions) return
    saveClaudeSelection(sessionID, claudeSelection)
  }, [claudeSelection, onUpdateRuntimeAgentOptions, sessionID])

  useEffect(() => {
    if (onUpdateRuntimeAgentOptions) return
    saveOpenCodeSelection(sessionID, opencodeSelection)
  }, [onUpdateRuntimeAgentOptions, opencodeSelection, sessionID])

  useEffect(() => {
    if (onUpdateRuntimeAgentOptions) return
    savePiSelection(sessionID, piSelection)
  }, [onUpdateRuntimeAgentOptions, piSelection, sessionID])

  function currentSubmitOptions() {
    if (codexToolbarVisible) {
      return submitOptionsForCodex(codexSelection, selectedFastTier)
    }
    if (claudeToolbarVisible) {
      return submitOptionsForClaude(claudeSelection)
    }
    if (opencodeToolbarVisible) {
      return submitOptionsForOpenCode(opencodeSelection)
    }
    if (piToolbarVisible) {
      return submitOptionsForPi(piSelection)
    }
    return undefined
  }

  async function submitText(contentToSend: string, submitAttachments: MessageAttachment[] = [], queue = false, onPrepared?: () => void) {
    const submitOptions = currentSubmitOptions()
    const submitSkills = selectedSkills.map(({ name, path }) => ({ name, path }))
    if (onPrepared) return onSubmit(contentToSend, submitOptions, submitAttachments, queue, submitSkills, onPrepared)

    if (submitSkills.length > 0) {
      return onSubmit(
        contentToSend,
        submitOptions,
        submitAttachments.length > 0 ? submitAttachments : undefined,
        queue,
        submitSkills,
      )
    }

    if (queue) {
      return onSubmit(contentToSend, submitOptions, submitAttachments.length > 0 ? submitAttachments : undefined, true)
    }
    if (submitOptions && submitAttachments.length > 0) {
      return onSubmit(contentToSend, submitOptions, submitAttachments)
    }
    if (submitOptions) {
      return onSubmit(contentToSend, submitOptions)
    }
    if (submitAttachments.length > 0) {
      return onSubmit(contentToSend, undefined, submitAttachments)
    }
    return onSubmit(contentToSend)
  }

  function restoreTextareaFocus() {
    window.setTimeout(() => {
      window.requestAnimationFrame(() => {
        textareaRef.current?.focus({ preventScroll: true })
      })
    }, 0)
  }

  async function submitPrompt(forceRestoreFocus = false, steer = false) {
    if (submitActionRef.current || (steer ? !canSendNow : !canSubmit)) {
      return
    }
    submitActionRef.current = true

    const restorePromptFocus = forceRestoreFocus || document.activeElement === textareaRef.current
    const submittedContent = content
    const submittedAttachments = attachments
    const submittedSkills = selectedSkills
    setSubmitting(true)
    onError?.('')
    const clearPreparedDraft = () => {
      setContent('')
      setAttachments([])
      setSelectedSkills([])
      setSkillTypeahead(null)
    }
    if (!prepareBeforeClear) clearPreparedDraft()
    try {
      const submitAttachments = submittedAttachments.map((attachment) => ({
        name: attachment.name,
        media_type: attachment.media_type,
        data_url: attachment.data_url,
        size_bytes: attachment.size_bytes,
      }))
      const response = steer
        ? await onSubmit(submittedContent.trim(), undefined, submitAttachments, false, submittedSkills.map(({ name, path }) => ({ name, path })), prepareBeforeClear ? clearPreparedDraft : undefined, steeringRunID)
        : await submitText(submittedContent.trim(), submitAttachments, false, prepareBeforeClear ? clearPreparedDraft : undefined)
      if (response?.queued_message) {
        const queuedMessage = response.queued_message
        commitQueuedMessages((current) => upsertQueuedMessage(current, queuedMessage))
      }
    } catch (submitError) {
      setContent(submittedContent)
      setAttachments(submittedAttachments)
      setSelectedSkills(submittedSkills)
      onError?.(submitError instanceof Error ? submitError.message : 'Failed to submit prompt')
    } finally {
      submitActionRef.current = false
      setSubmitting(false)
      if (restorePromptFocus) {
        restoreTextareaFocus()
      }
    }
  }

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    await submitPrompt()
  }

  function handleKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.nativeEvent.isComposing || event.keyCode === 229) return
    if (skillTypeahead && suggestedSkills.length > 0) {
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        setSkillHighlight((current) => (current + 1) % suggestedSkills.length)
        return
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault()
        setSkillHighlight((current) => (current - 1 + suggestedSkills.length) % suggestedSkills.length)
        return
      }
      if ((event.key === 'Enter' && !event.shiftKey && !event.metaKey && !event.ctrlKey) || event.key === 'Tab') {
        event.preventDefault()
        selectSkill(suggestedSkills[skillHighlight] ?? suggestedSkills[0], true)
        return
      }
    }
    if (skillTypeahead && event.key === 'Escape') {
      event.preventDefault()
      setSkillTypeahead(null)
      return
    }
    if (event.key !== 'Enter') {
      return
    }
    if ((event.metaKey || event.ctrlKey) && event.shiftKey) {
      event.preventDefault()
      void enqueueDraft(true)
      return
    }
    if ((event.metaKey || event.ctrlKey) && showSendNow) {
      event.preventDefault()
      void submitPrompt(true, true)
      return
    }
    if (event.shiftKey) {
      event.preventDefault()
      insertTextareaNewline(event.currentTarget, setContent)
      return
    }
    if (sessionStatus === 'running') {
      event.preventDefault()
      void enqueueDraft(true)
      return
    }
    event.preventDefault()
    void submitPrompt(true)
  }

  function handleTextareaFocus() {
    setCloseSettingsSignal((value) => value + 1)
    onFocus?.()
  }

  function handleContentChange(event: ChangeEvent<HTMLTextAreaElement>) {
    adoptedDraftRef.current = null
    const nextContent = event.target.value
    const caret = event.target.selectionStart ?? nextContent.length
    publishComposerActivity(sessionID, Math.abs(nextContent.length - content.length))
    setContent(nextContent)
    promptSelectionRef.current = {
      start: caret,
      end: event.target.selectionEnd ?? caret,
    }
    setSelectedSkills((current) => inlineSkillReferences(nextContent, sortedSkills, current))
    setSkillTypeahead(skillsSupported ? skillTypeaheadAt(nextContent, caret) : null)
    setSkillHighlight(0)
  }

  function selectSkill(skill: AgentSkill, fromTypeahead = false) {
    adoptedDraftRef.current = null
    setSelectedSkills((current) => {
      const sameName = current.filter((reference) => reference.name === skill.name)
      if (sameName.length === 1 && skillKey(sameName[0]) === skillKey(skill)) {
        return current
      }
      return [
        ...current.filter((reference) => reference.name !== skill.name),
        { name: skill.name, path: skill.path },
      ]
    })

    if (fromTypeahead && skillTypeahead) {
      const insertion = insertInlineSkillToken(content, skill.name, skillTypeahead.start, skillTypeahead.end)
      setContent(insertion.content)
      promptSelectionRef.current = { start: insertion.caret, end: insertion.caret }
      restorePromptSelection(insertion.caret)
    } else if (!contentHasSkillToken(content, skill.name)) {
      const insertion = insertInlineSkillToken(
        content,
        skill.name,
        promptSelectionRef.current.start,
        promptSelectionRef.current.end,
      )
      setContent(insertion.content)
      promptSelectionRef.current = { start: insertion.caret, end: insertion.caret }
      restorePromptSelection(insertion.caret)
    }
    setSkillTypeahead(null)
    setSkillsOpen(false)
    setSkillSearch('')
  }

  function restorePromptSelection(caret: number) {
    window.setTimeout(() => {
      textareaRef.current?.focus({ preventScroll: true })
      textareaRef.current?.setSelectionRange(caret, caret)
    }, 0)
  }

  async function refreshSkills() {
    if (offline || !sessionID || !skillsSupported || skillsLoading) {
      return
    }
    setSkillsLoading(true)
    setSkillsError('')
    try {
      const catalog = await fetchSessionSkills(sessionID, true)
      const available = Array.isArray(catalog.skills) ? catalog.skills : []
      skillsLoadedSessionRef.current = sessionID
      setSkills(available)
      setSkillErrors(Array.isArray(catalog.errors) ? catalog.errors : [])
      setSkillScopeFilter((current) =>
        current && available.some((skill) => skill.scope === current) ? current : null,
      )
      const nextSelected = selectedSkills.filter((reference) =>
        available.some((skill) => skillKey(skill) === skillKey(reference)),
      )
      if (nextSelected.length !== selectedSkills.length) {
        setSelectedSkills(nextSelected)
        onError?.('One or more draft skills are no longer available.')
      }
    } catch (loadError) {
      setSkillsError(loadError instanceof Error ? loadError.message : 'Failed to load skills')
    } finally {
      setSkillsLoading(false)
    }
  }

  async function handleCancel() {
    if (offline || !onCancel || queueActionRef.current) {
      return
    }

    const lifetime = lifetimeRef.current
    const restoreAfterStop = composerIsEmpty()
    queueActionRef.current = true
    setCancelling(true)
    onError?.('')
    let stopped = false
    try {
      await onCancel()
      stopped = true
      if (sessionID && lifetime.active && restoreAfterStop && composerIsEmpty()) {
        // A fresh read avoids restoring a message already consumed/removed on another device.
        const eventsBeforeRead = new Map(queueEventsRef.current)
        const response = await fetchQueuedMessages(sessionID, true)
        if (!lifetime.active || !composerIsEmpty()) return
        const duringRead = [...queueEventsRef.current.values()].filter((event) =>
          event.seq > (eventsBeforeRead.get(queuedMessageID(event))?.seq ?? -1),
        )
        const messages = reconcileQueuedMessages(response.messages, duringRead)
          .filter((message) => !removedQueueIDsRef.current.has(message.id))
        commitQueuedMessages(messages)
        const first = [...messages].sort((left, right) => left.seq - right.seq)[0]
        if (first) await restoreQueuedDraft(first, supportsComposerAutoFocus())
      }
    } catch (cancelError) {
      if (lifetime.active) {
        const detail = cancelError instanceof Error ? cancelError.message : 'Request failed'
        onError?.(stopped ? `Run stopped, but the queued draft could not be restored. ${detail}` : detail)
      }
    } finally {
      queueActionRef.current = false
      if (lifetime.active) setCancelling(false)
    }
  }

  function composerIsEmpty() {
    const draft = currentDraftRef.current
    return !draft.offline && !draft.submitting && draft.content.length === 0 &&
      draft.selectedSkills.length === 0 && draft.attachments.length === 0 &&
      attachmentReadsRef.current.pending === 0 &&
      loadDraft(sessionID).length === 0 && loadSelectedSkills(sessionID).length === 0
  }

  async function restoreQueuedDraft(message: QueuedMessage, focus: boolean) {
    if (!sessionID || message.session_id !== sessionID) return
    const lifetime = lifetimeRef.current
    const restoredSkills = message.skills ?? []
    const restoredContent = ensureInlineSkillTokens(message.content, restoredSkills)
    // Save before deleting: a lost response or closing this tab cannot lose the message.
    // Unlike ordinary editing, storage failure must leave the server queue untouched.
    try {
      saveComposerDraft(sessionID, { draft: restoredContent, selectedSkills: restoredSkills })
    } catch {
      throw new Error('Unable to save the draft locally. The message was left queued.')
    }
    setContent(restoredContent)
    setSelectedSkills(restoredSkills)
    adoptedDraftRef.current = null
    attachmentReadsRef.current = { pending: 0 }
    setAttachments([])
    setSkillTypeahead(null)
    promptSelectionRef.current = { start: restoredContent.length, end: restoredContent.length }
    if (focus) textareaRef.current?.focus({ preventScroll: true })
    try {
      const removed = await deleteQueuedMessage(sessionID, message.id)
      if (!lifetime.active) return
      removedQueueIDsRef.current.add(removed.id)
      commitQueuedMessages((current) => current.filter((item) => item.id !== removed.id))
    } catch (removeError) {
      if (lifetime.active) {
        const detail = removeError instanceof Error ? removeError.message : 'Request failed'
        onError?.(`A copy is saved in the composer, but queue removal could not be confirmed. Check the queue before sending. ${detail}`)
      }
    }
  }

  async function moveQueuedDraft(message: QueuedMessage) {
    if (offline || !sessionID || submitting || queueActionRef.current) return
    const lifetime = lifetimeRef.current
    queueActionRef.current = true
    setQueueActionPending(true)
    onError?.('')
    try {
      // Explicit action: replace the draft, even when it contains text, skills, or images.
      await restoreQueuedDraft(message, true)
    } catch (moveError) {
      if (lifetime.active) onError?.(moveError instanceof Error ? moveError.message : 'Failed to restore queued prompt')
    } finally {
      queueActionRef.current = false
      if (lifetime.active) setQueueActionPending(false)
    }
  }

  async function enqueueDraft(forceRestoreFocus = false) {
    if (offline || submissionBlocked || submitting || submitActionRef.current || queueActionRef.current) return
    const trimmed = content.trim()
    if (!trimmed && selectedSkills.length === 0) {
      return
    }
    if (queueBlockedByAttachments) {
      onError?.('Queued messages cannot include image attachments.')
      return
    }
    if (queuedMessages.length >= maxQueuedMessages) {
      onError?.(`Queue up to ${maxQueuedMessages} messages.`)
      return
    }
    const restorePromptFocus =
      forceRestoreFocus ||
      document.activeElement === textareaRef.current ||
      document.activeElement instanceof HTMLButtonElement
    setSubmitting(true)
    onError?.('')
    try {
      const response = await submitText(trimmed, [], true)
      setContent('')
      setSelectedSkills([])
      setSkillTypeahead(null)
      if (response?.queued_message) {
        const queuedMessage = response.queued_message
        commitQueuedMessages((current) => upsertQueuedMessage(current, queuedMessage))
      } else if (sessionID) {
        const response = await fetchQueuedMessages(sessionID, true)
        commitQueuedMessages(Array.isArray(response.messages) ? response.messages : [])
      }
    } catch (queueError) {
      onError?.(queueError instanceof Error ? queueError.message : 'Failed to queue prompt')
    } finally {
      setSubmitting(false)
      if (restorePromptFocus) {
        restoreTextareaFocus()
      }
    }
  }

  async function removeQueuedDraft(queuedMessageID: string) {
    if (offline || !sessionID || submitting || queueActionRef.current) {
      return
    }
    const lifetime = lifetimeRef.current
    queueActionRef.current = true
    setQueueActionPending(true)
    onError?.('')
    try {
      const removed = await deleteQueuedMessage(sessionID, queuedMessageID)
      if (!lifetime.active) return
      removedQueueIDsRef.current.add(removed.id)
      commitQueuedMessages((current) => current.filter((message) => message.id !== removed.id))
    } catch (removeError) {
      if (lifetime.active) onError?.(removeError instanceof Error ? removeError.message : 'Failed to remove queued prompt')
    } finally {
      queueActionRef.current = false
      if (lifetime.active) setQueueActionPending(false)
    }
  }

  async function handleFiles(files: FileList | File[]) {
    const selectedFiles = Array.from(files)
    if (selectedFiles.length === 0) {
      return
    }
    adoptedDraftRef.current = null
    if (attachments.length + selectedFiles.length > maxImageAttachmentCount) {
      onError?.(`Attach up to ${maxImageAttachmentCount} images.`)
      return
    }

    const imageFiles: File[] = []
    for (const file of selectedFiles) {
      if (!file.type.startsWith('image/')) {
        onError?.('Only image attachments are supported.')
        return
      }
      if (file.size > maxImageAttachmentBytes) {
        onError?.(`${file.name} is larger than 5 MB.`)
        return
      }
      imageFiles.push(file)
    }

    const lifetime = lifetimeRef.current
    const reads = attachmentReadsRef.current
    reads.pending += 1
    try {
      const nextAttachments = await Promise.all(imageFiles.map(fileToAttachment))
      if (!lifetime.active || reads !== attachmentReadsRef.current) return
      setAttachments((current) => [...current, ...nextAttachments])
      onError?.('')
    } catch (attachmentError) {
      if (lifetime.active && reads === attachmentReadsRef.current) {
        onError?.(attachmentError instanceof Error ? attachmentError.message : 'Failed to attach image')
      }
    } finally {
      reads.pending -= 1
    }
  }

  function handleFileInputChange(event: ChangeEvent<HTMLInputElement>) {
    void handleFiles(event.currentTarget.files ?? [])
    event.currentTarget.value = ''
  }

  function handleDragOver(event: DragEvent<HTMLDivElement>) {
    if (inputDisabled || !hasDraggedFiles(event)) {
      return
    }
    event.preventDefault()
    setDragActive(true)
  }

  function handleDragLeave(event: DragEvent<HTMLDivElement>) {
    if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
      setDragActive(false)
    }
  }

  function handleDrop(event: DragEvent<HTMLDivElement>) {
    if (inputDisabled || !hasDraggedFiles(event)) {
      return
    }
    event.preventDefault()
    setDragActive(false)
    void handleFiles(event.dataTransfer.files)
  }

  function handlePaste(event: ClipboardEvent<HTMLDivElement>) {
    if (inputDisabled) {
      return
    }
    const imageFiles = clipboardImageFiles(event.clipboardData)
    if (imageFiles.length === 0) {
      return
    }
    event.preventDefault()
    void handleFiles(imageFiles)
  }

  return (
    <form onSubmit={(event) => void handleSubmit(event)} className="prompt-composer-shell relative shrink-0 px-3 pb-3">
      {draftStorageError ? <p role="alert" className="mb-2 text-xs text-destructive">{draftStorageError}</p> : null}
      {queuedMessages.length > 0 ? (
        <div className="pointer-events-auto relative z-0 mx-3 -mb-3 rounded-t-[20px] border border-border/85 border-b-0 bg-surface-muted/75 px-4 pb-4 pt-2 shadow-[0_10px_24px_hsl(var(--foreground)/0.08)] backdrop-blur">
          <div className="flex items-center justify-between gap-3">
            <p className="text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
              Queued {queuedMessages.length}/{maxQueuedMessages}
            </p>
            <p className="hidden text-xs text-muted-foreground sm:block">{queueShortcutLabel}</p>
          </div>
          <div className="mt-1.5">
            {queuedMessages.map((message, index) => (
              <QueuedMessageRow
                key={message.id}
                index={index}
                message={message.content}
                skills={message.skills ?? []}
                disabled={offline || submitting || cancelling || queueActionPending}
                onMoveToComposer={() => void moveQueuedDraft(message)}
                onRemove={() => void removeQueuedDraft(message.id)}
              />
            ))}
          </div>
        </div>
      ) : null}
      <div
        data-testid="prompt-composer-dropzone"
        onPaste={handlePaste}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
        className={cn(
          'command-composer relative z-10 rounded-xl border border-border/90 p-2 shadow-[0_10px_30px_hsl(var(--foreground)/0.10)] transition-colors',
          ((codexToolbarVisible && codexSelection.planning_mode) ||
            (claudeToolbarVisible && claudeSelection.planning_mode) ||
            (opencodeToolbarVisible && opencodeSelection.planning_mode)) &&
            'codex-plan-composer',
          dragActive && 'border-primary/70 bg-primary/5 ring-2 ring-primary/20',
        )}
      >
        {skillTypeahead ? (
          <SkillSuggestionList
            skills={suggestedSkills}
            loading={skillsLoading}
            error={skillsError}
            highlighted={skillHighlight}
            onHighlight={setSkillHighlight}
            onSelect={(skill) => selectSkill(skill, true)}
          />
        ) : null}
        {attachments.length > 0 ? (
          <div className="mb-2 flex flex-wrap gap-2">
            {attachments.map((attachment) => (
              <ImageAttachmentPreview
                key={attachment.id}
                attachment={attachment}
                onRemove={() => setAttachments((current) => current.filter((item) => item.id !== attachment.id))}
              />
            ))}
          </div>
        ) : null}
        <div className="relative">
          {selectedSkills.length > 0 ? (
            <InlineSkillHighlights
              content={content}
              selected={selectedSkills}
              skills={skills}
              scrollTop={promptScrollTop}
            />
          ) : null}
          <Textarea
            ref={textareaRef}
            aria-label="Prompt"
            placeholder={promptPlaceholder}
            value={content}
            onChange={handleContentChange}
            onSelect={(event) => {
              promptSelectionRef.current = {
                start: event.currentTarget.selectionStart ?? content.length,
                end: event.currentTarget.selectionEnd ?? content.length,
              }
            }}
            onScroll={(event) => setPromptScrollTop(event.currentTarget.scrollTop)}
            onFocus={handleTextareaFocus}
            onKeyDown={handleKeyDown}
            aria-autocomplete={skillTypeahead ? 'list' : undefined}
            aria-controls={skillTypeahead ? 'skill-typeahead-list' : undefined}
            aria-expanded={skillTypeahead ? true : undefined}
            disabled={inputDisabled}
            rows={1}
            className="relative z-10 h-9 min-h-9 resize-none border-transparent bg-transparent px-1 py-1.5 text-base shadow-none focus-visible:ring-0 sm:py-2 sm:text-sm"
          />
        </div>
        <div className="mt-2 flex min-h-8 flex-wrap items-center gap-1.5">
          {skillsSupported && sessionID ? (
            <SkillBrowser
              open={skillsOpen}
              onOpenChange={setSkillsOpen}
              search={skillSearch}
              onSearchChange={setSkillSearch}
              skills={browsedSkills}
              scopes={skillScopes}
              scopeFilter={skillScopeFilter}
              onScopeFilterChange={setSkillScopeFilter}
              selected={selectedSkills}
              loading={skillsLoading}
              error={skillsError}
              errors={skillErrors}
              onSelect={selectSkill}
              onRefresh={() => void refreshSkills()}
            />
          ) : null}
          {codexToolbarVisible ? (
            <>
              <CodexToolbar
                options={codexOptions}
                selection={codexSelection}
                loading={codexOptionsLoading}
                error={codexOptionsError}
                disabled={codexControlsDisabled}
                onChange={setCodexSelection}
                closeSettingsSignal={closeSettingsSignal}
                className="composer-desktop-options"
              />
              <MobileCodexOptions
                options={codexOptions}
                selection={codexSelection}
                loading={codexOptionsLoading}
                error={codexOptionsError}
                disabled={codexControlsDisabled}
                onChange={setCodexSelection}
                closeSettingsSignal={closeSettingsSignal}
              />
              <span className="composer-mobile-plan">
                <SwitchControl
                  label="Plan"
                  active={codexSelection.planning_mode && codexPlanAvailable}
                  disabled={offline || submitting || !codexPlanAvailable}
                  onClick={() =>
                    setCodexSelection({
                      ...codexSelection,
                      planning_mode: codexPlanAvailable ? !codexSelection.planning_mode : false,
                    })
                  }
                />
              </span>
            </>
          ) : null}
          {claudeToolbarVisible ? (
            <>
              <ClaudeToolbar
                selection={claudeSelection}
                disabled={offline || submitting}
                onChange={setClaudeSelection}
                closeSettingsSignal={closeSettingsSignal}
                className="composer-desktop-options"
              />
              <MobileClaudeOptions
                selection={claudeSelection}
                disabled={offline || submitting}
                onChange={setClaudeSelection}
                closeSettingsSignal={closeSettingsSignal}
              />
              <span className="composer-mobile-plan">
                <SwitchControl
                  label="Plan"
                  active={claudeSelection.planning_mode}
                  disabled={offline || submitting}
                  onClick={() => setClaudeSelection({ ...claudeSelection, planning_mode: !claudeSelection.planning_mode })}
                />
              </span>
            </>
          ) : null}
          {opencodeToolbarVisible ? (
            <>
              <OpenCodeToolbar
                options={opencodeOptions}
                selection={opencodeSelection}
                loading={opencodeOptionsLoading}
                error={opencodeOptionsError}
                disabled={opencodeControlsDisabled}
                onChange={setOpenCodeSelection}
                closeSettingsSignal={closeSettingsSignal}
                className="composer-desktop-options"
              />
              <MobileOpenCodeOptions
                options={opencodeOptions}
                selection={opencodeSelection}
                loading={opencodeOptionsLoading}
                error={opencodeOptionsError}
                disabled={opencodeControlsDisabled}
                onChange={setOpenCodeSelection}
                closeSettingsSignal={closeSettingsSignal}
              />
              <span className="composer-mobile-plan">
                <SwitchControl
                  label="Plan"
                  active={opencodeSelection.planning_mode && openCodePlanAvailable}
                  disabled={offline || submitting || !openCodePlanAvailable}
                  onClick={() =>
                    setOpenCodeSelection({
                      ...opencodeSelection,
                      planning_mode: openCodePlanAvailable ? !opencodeSelection.planning_mode : false,
                    })
                  }
                />
              </span>
            </>
          ) : null}
          {piToolbarVisible ? (
            <>
              <PiToolbar
                options={piOptions}
                selection={piSelection}
                loading={piOptionsLoading}
                error={piOptionsError}
                disabled={piControlsDisabled}
                onChange={setPiSelection}
                closeSettingsSignal={closeSettingsSignal}
                className="composer-desktop-options"
              />
              <MobilePiOptions
                options={piOptions}
                selection={piSelection}
                loading={piOptionsLoading}
                error={piOptionsError}
                disabled={piControlsDisabled}
                onChange={setPiSelection}
                closeSettingsSignal={closeSettingsSignal}
              />
            </>
          ) : null}
          <div className="ml-auto flex items-center gap-1.5">
            <input
              ref={fileInputRef}
              type="file"
              accept="image/*"
              multiple
              className="sr-only"
              aria-label="Image attachments"
              onChange={handleFileInputChange}
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              disabled={inputDisabled}
              onClick={() => fileInputRef.current?.click()}
              aria-label="Attach images"
              className="h-8 w-8 text-muted-foreground hover:text-foreground"
            >
              <Paperclip />
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={!canQueue}
              onClick={() => void enqueueDraft()}
              title={
                queueBlockedByAttachments
                  ? 'Queued messages cannot include image attachments.'
                  : `Queue message (${queueShortcutLabel})`
              }
              aria-label={`Queue message (${queueShortcutLabel})`}
              className="h-8 px-2.5 text-sm"
            >
              <ClipboardList />
              <span className="composer-wide-label">Queue</span>
            </Button>
            {showSendNow ? (
              <Button
                type="button"
                disabled={!canSendNow}
                onClick={() => void submitPrompt(false, true)}
                aria-label="Send now"
                title="Send to the current run now (Cmd/Ctrl+Enter). Keeps the current model and settings."
                className="h-8 px-2.5 text-sm"
              >
                <Send />
                <span className="composer-send-now-label">{submitting ? 'Sending' : 'Send now'}</span>
              </Button>
            ) : null}
            {canCancel ? (
              <Button
                type="button"
                variant="outline"
                size="icon"
                disabled={cancelling || queueActionPending}
                onClick={() => void handleCancel()}
                className={cn(
                  'running-stop-button h-8 w-8 border-destructive/40 text-destructive hover:bg-destructive/10',
                )}
                aria-label="Cancel running session"
              >
                <Square />
              </Button>
            ) : (
              <Button
                type="submit"
                disabled={!canSubmit}
                aria-label="Submit prompt"
                className="h-8 px-2.5 text-sm"
              >
                <Send />
                <span className="composer-wide-label">{submitting ? 'Sending' : 'Send'}</span>
              </Button>
            )}
          </div>
        </div>
      </div>
    </form>
  )
}

function upsertQueuedMessage(messages: QueuedMessage[], queuedMessage: QueuedMessage) {
  return [...messages.filter((message) => message.id !== queuedMessage.id), queuedMessage]
    .sort((left, right) => left.seq - right.seq)
}

function queuedMessageID(event: AgentEvent) {
  if (typeof event.payload !== 'object' || event.payload === null || !('queue_item_id' in event.payload)) {
    return ''
  }
  return typeof event.payload.queue_item_id === 'string' ? event.payload.queue_item_id : ''
}

function queuedMessageFromEvent(event: AgentEvent, id: string): QueuedMessage {
  const payload = typeof event.payload === 'object' && event.payload !== null ? event.payload : {}
  const skills = 'skills' in payload && Array.isArray(payload.skills) ? (payload.skills as SkillReference[]) : undefined
  const agentOptions =
    'agent_options' in payload && typeof payload.agent_options === 'object' && payload.agent_options !== null
      ? (payload.agent_options as SubmitAgentOptions)
      : undefined
  return {
    id,
    session_id: event.session_id,
    seq: event.seq,
    content: 'text' in payload && typeof payload.text === 'string' ? payload.text : '',
    agent_options: agentOptions,
    skills,
    created_at: event.created_at,
  }
}

function reconcileQueuedMessages(messages: QueuedMessage[], events: Iterable<AgentEvent>) {
  let next = messages
  for (const event of events) {
    const id = queuedMessageID(event)
    if (!id) continue
    if (event.type === 'user.message.queued') {
      if (!next.some((message) => message.id === id)) {
        next = upsertQueuedMessage(next, queuedMessageFromEvent(event, id))
      }
    } else {
      next = next.filter((message) => message.id !== id)
    }
  }
  return next
}

function ImageAttachmentPreview({ attachment, onRemove }: { attachment: ComposerAttachment; onRemove: () => void }) {
  return (
    <figure className="group relative grid w-24 gap-1 rounded-lg border border-border/80 bg-surface-muted/70 p-1.5 shadow-sm">
      <div className="aspect-square overflow-hidden rounded-md bg-background">
        <img src={attachment.data_url} alt={attachment.name} className="h-full w-full object-cover" />
      </div>
      <figcaption className="min-w-0 truncate px-0.5 text-[10px] text-muted-foreground">{attachment.name}</figcaption>
      <button
        type="button"
        aria-label={`Remove ${attachment.name}`}
        onClick={onRemove}
        className="absolute right-0.5 top-0.5 inline-flex size-6 items-center justify-center rounded-full border border-border/80 bg-background/95 text-muted-foreground shadow-sm transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <X className="size-3.5" aria-hidden="true" />
      </button>
    </figure>
  )
}

function SkillBrowser({
  open,
  onOpenChange,
  search,
  onSearchChange,
  skills,
  scopes,
  scopeFilter,
  onScopeFilterChange,
  selected,
  loading,
  error,
  errors,
  onSelect,
  onRefresh,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  search: string
  onSearchChange: (value: string) => void
  skills: AgentSkill[]
  scopes: Array<{ scope: SkillScope; count: number }>
  scopeFilter: SkillScope | null
  onScopeFilterChange: (scope: SkillScope | null) => void
  selected: SkillReference[]
  loading: boolean
  error: string
  errors: AgentSkillError[]
  onSelect: (skill: AgentSkill) => void
  onRefresh: () => void
}) {
  const browserRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    function handlePointerDown(event: PointerEvent) {
      if (!browserRef.current?.contains(event.target as Node)) onOpenChange(false)
    }
    function handleEscape(event: globalThis.KeyboardEvent) {
      if (event.key === 'Escape') onOpenChange(false)
    }
    document.addEventListener('pointerdown', handlePointerDown)
    document.addEventListener('keydown', handleEscape)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [onOpenChange, open])

  return (
    <div ref={browserRef} className="relative">
      <Button
        type="button"
        variant="ghost"
        aria-label="Skills"
        aria-expanded={open}
        onClick={() => onOpenChange(!open)}
        className={cn(
          'h-8 gap-1.5 px-2 text-muted-foreground hover:text-foreground',
          selected.length > 0 && 'bg-primary/10 text-primary hover:bg-primary/15 hover:text-primary',
        )}
      >
        <BookOpen className="size-4" aria-hidden="true" />
        <span className="composer-wide-label">Skills</span>
        {selected.length > 0 ? (
          <span className="rounded-full bg-primary/15 px-1.5 text-[10px] tabular-nums">{selected.length}</span>
        ) : null}
      </Button>
      {open ? (
        <div
          role="dialog"
          aria-label="Available skills"
          className="absolute bottom-full left-0 z-50 mb-2 w-[min(26rem,calc(100vw-2rem))] overflow-hidden rounded-lg border border-border/90 bg-popover text-popover-foreground shadow-xl"
        >
          <div className="flex items-center gap-2 border-b border-border/70 p-2">
            <label className="flex min-w-0 flex-1 items-center gap-2 rounded-md border border-border/80 bg-background px-2">
              <Search className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
              <input
                autoFocus
                aria-label="Search skills"
                value={search}
                onChange={(event) => onSearchChange(event.target.value)}
                className="h-8 min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                placeholder="Search skills"
              />
            </label>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              aria-label="Refresh skills"
              disabled={loading}
              onClick={onRefresh}
              className="h-8 w-8 shrink-0 text-muted-foreground"
            >
              <RefreshCw className={cn('size-3.5', loading && 'animate-spin')} aria-hidden="true" />
            </Button>
          </div>
          <div
            aria-label="Filter skills by scope"
            className="flex items-center gap-1.5 overflow-x-auto border-b border-border/70 px-2 py-1.5"
          >
            <button
              type="button"
              aria-pressed={scopeFilter === null}
              onClick={() => onScopeFilterChange(null)}
              className={skillScopeFilterClassName(scopeFilter === null)}
            >
              All
              <span aria-hidden="true" className="text-[10px] tabular-nums opacity-70">
                {scopes.reduce((total, scope) => total + scope.count, 0)}
              </span>
            </button>
            {scopes.map(({ scope, count }) => (
              <button
                key={scope}
                type="button"
                aria-label={`${skillScopeLabel(scope)} skills (${count})`}
                aria-pressed={scopeFilter === scope}
                onClick={() => onScopeFilterChange(scope)}
                className={skillScopeFilterClassName(scopeFilter === scope)}
              >
                {skillScopeLabel(scope)}
                <span aria-hidden="true" className="text-[10px] tabular-nums opacity-70">{count}</span>
              </button>
            ))}
          </div>
          <div className="max-h-[min(24rem,55dvh)] overflow-y-auto p-1.5">
            {loading && skills.length === 0 ? (
              <p className="px-2 py-6 text-center text-sm text-muted-foreground">Loading skills...</p>
            ) : error ? (
              <p role="alert" className="px-2 py-6 text-center text-sm text-destructive">{error}</p>
            ) : skills.length === 0 ? (
              <p className="px-2 py-6 text-center text-sm text-muted-foreground">No matching skills.</p>
            ) : (
              skills.map((skill) => {
                const isSelected = selected.some((reference) => skillKey(reference) === skillKey(skill))
                return (
                  <button
                    key={skillKey(skill)}
                    type="button"
                    disabled={isSelected}
                    onClick={() => onSelect(skill)}
                    className="flex w-full items-start gap-2 rounded-md px-2 py-2 text-left transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-55"
                  >
                    <SkillAccent skill={skill} />
                    <span className="min-w-0 flex-1">
                      <span className="flex items-center gap-2">
                        <span className="truncate text-sm font-medium">{skillDisplayName(skill)}</span>
                        <span className="shrink-0 rounded-full bg-muted px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wide text-muted-foreground">
                          {skill.scope}
                        </span>
                      </span>
                      <span className="mt-0.5 block line-clamp-2 text-xs leading-4 text-muted-foreground">
                        {skill.short_description || skill.description}
                      </span>
                      {skill.display_name && skill.display_name !== skill.name ? (
                        <span className="mt-0.5 block truncate font-mono text-[10px] text-muted-foreground/80">${skill.name}</span>
                      ) : null}
                    </span>
                    {isSelected ? <span className="pt-0.5 text-[10px] font-medium text-primary">Selected</span> : null}
                  </button>
                )
              })
            )}
          </div>
          {errors.length > 0 ? (
            <p className="border-t border-border/70 px-3 py-2 text-[11px] text-amber-700 dark:text-amber-300" title={errors.map((item) => `${item.path}: ${item.message}`).join('\n')}>
              {errors.length} skill {errors.length === 1 ? 'entry could' : 'entries could'} not be loaded.
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

function SkillSuggestionList({
  skills,
  loading,
  error,
  highlighted,
  onHighlight,
  onSelect,
}: {
  skills: AgentSkill[]
  loading: boolean
  error: string
  highlighted: number
  onHighlight: (index: number) => void
  onSelect: (skill: AgentSkill) => void
}) {
  return (
    <div
      id="skill-typeahead-list"
      role="listbox"
      aria-label="Skill suggestions"
      className="absolute inset-x-2 bottom-full z-50 mb-2 max-h-72 overflow-y-auto rounded-lg border border-border/90 bg-popover p-1.5 text-popover-foreground shadow-xl"
    >
      {loading && skills.length === 0 ? (
        <p className="px-3 py-3 text-sm text-muted-foreground">Loading skills...</p>
      ) : error ? (
        <p role="alert" className="px-3 py-3 text-sm text-destructive">{error}</p>
      ) : skills.length === 0 ? (
        <p className="px-3 py-3 text-sm text-muted-foreground">No matching skills.</p>
      ) : (
        skills.map((skill, index) => (
          <button
            key={skillKey(skill)}
            type="button"
            role="option"
            aria-selected={index === highlighted}
            onMouseEnter={() => onHighlight(index)}
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => onSelect(skill)}
            className={cn(
              'flex w-full items-start gap-2 rounded-md px-2 py-2 text-left',
              index === highlighted && 'bg-accent',
            )}
          >
            <SkillAccent skill={skill} />
            <span className="min-w-0 flex-1">
              <span className="flex items-center gap-2">
                <span className="truncate text-sm font-medium">{skillDisplayName(skill)}</span>
                <span className="shrink-0 text-[10px] uppercase tracking-wide text-muted-foreground">{skill.scope}</span>
              </span>
              <span className="block truncate text-xs text-muted-foreground">{skill.short_description || skill.description}</span>
            </span>
            <span className="font-mono text-[10px] text-muted-foreground">${skill.name}</span>
          </button>
        ))
      )}
    </div>
  )
}

function SkillAccent({ skill }: { skill: AgentSkill }) {
  const color = validSkillColor(skill.brand_color)
  return (
    <span
      className="mt-1 size-2 shrink-0 rounded-full bg-primary/55"
      style={color ? ({ backgroundColor: color } satisfies CSSProperties) : undefined}
      aria-hidden="true"
    />
  )
}

function InlineSkillHighlights({
  content,
  selected,
  skills,
  scrollTop,
}: {
  content: string
  selected: SkillReference[]
  skills: AgentSkill[]
  scrollTop: number
}) {
  const segments = inlineSkillHighlightSegments(content, selected, skills)
  return (
    <div
      aria-hidden="true"
      data-testid="inline-skill-highlights"
      className="pointer-events-none absolute inset-0 z-0 overflow-hidden rounded-md"
    >
      <div
        className="min-h-full w-full whitespace-pre-wrap break-words border border-transparent px-1 py-1.5 text-base text-transparent sm:py-2 sm:text-sm"
        style={{ transform: `translateY(-${scrollTop}px)` }}
      >
        {segments.map((segment, index) => {
          if (!segment.reference) return <span key={index}>{segment.text}</span>
          const color = validSkillColor(segment.skill?.brand_color)
          return (
            <span
              key={index}
              data-testid="inline-skill-chip"
              data-skill-name={segment.reference.name}
              className="-mx-0.5 -my-0.5 rounded-md bg-primary/15 px-0.5 py-0.5 text-transparent ring-1 ring-inset ring-primary/30"
              style={
                color
                  ? {
                      backgroundColor: `${color}20`,
                      boxShadow: `inset 0 0 0 1px ${color}66`,
                    }
                  : undefined
              }
            >
              {segment.text}
            </span>
          )
        })}
        {content.endsWith('\n') ? '\u00a0' : null}
      </div>
    </div>
  )
}

function QueuedMessageRow({
  index,
  message,
  skills,
  disabled,
  onMoveToComposer,
  onRemove,
}: {
  index: number
  message: string
  skills: SkillReference[]
  disabled: boolean
  onMoveToComposer: () => void
  onRemove: () => void
}) {
  return (
    <div
      className={cn(
        'flex items-start gap-2 py-1.5 text-sm text-muted-foreground',
        index > 0 && 'border-t border-border/55',
      )}
    >
      <span className="pt-0.5 text-xs font-semibold tabular-nums text-muted-foreground/75">{index + 1}</span>
      <div className="min-w-0 flex-1">
        {skills.length > 0 ? (
          <div className="mb-1 flex flex-wrap gap-1">
            {skills.map((skill) => (
              <span
                key={skillKey(skill)}
                className="rounded-full border border-border/70 bg-background/70 px-2 py-0.5 text-[10px] font-medium"
              >
                ${skill.name}
              </span>
            ))}
          </div>
        ) : null}
        {message ? <p className="truncate">{message}</p> : null}
      </div>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        disabled={disabled}
        aria-label={`Move queued message ${index + 1} to composer`}
        title="Move to composer (replaces current draft)"
        onClick={onMoveToComposer}
        className="h-6 w-6 shrink-0 text-muted-foreground hover:text-foreground"
      >
        <CornerDownLeft className="size-3.5" aria-hidden="true" />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label={`Remove queued message ${index + 1}`}
        disabled={disabled}
        onClick={onRemove}
        className="h-6 w-6 shrink-0 text-muted-foreground hover:text-foreground"
      >
        <X className="size-3.5" aria-hidden="true" />
      </Button>
    </div>
  )
}

function hasDraggedFiles(event: DragEvent<HTMLDivElement>) {
  return Array.from(event.dataTransfer.types).includes('Files')
}

function clipboardImageFiles(data: DataTransfer) {
  const directFiles = Array.from(data.files ?? []).filter((file) => file.type.startsWith('image/'))
  if (directFiles.length > 0) {
    return directFiles
  }

  return Array.from(data.items ?? []).flatMap((item) => {
    if (item.kind !== 'file' || !item.type.startsWith('image/')) {
      return []
    }
    const file = item.getAsFile()
    return file ? [file] : []
  })
}

async function fileToAttachment(file: File): Promise<ComposerAttachment> {
  const dataURL = await readFileAsDataURL(file)
  return {
    id: createAttachmentID(),
    name: file.name || 'image',
    media_type: file.type,
    data_url: dataURL,
    size_bytes: file.size,
  }
}

function readFileAsDataURL(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      if (typeof reader.result === 'string') {
        resolve(reader.result)
      } else {
        reject(new Error('Failed to read image attachment'))
      }
    }
    reader.onerror = () => reject(reader.error ?? new Error('Failed to read image attachment'))
    reader.readAsDataURL(file)
  })
}

function createAttachmentID() {
  return typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : `attachment-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

function CodexToolbar({
  options,
  selection,
  loading,
  error,
  disabled,
  onChange,
  closeSettingsSignal,
  className,
}: {
  options: CodexAgentOptions | null
  selection: CodexSelection
  loading: boolean
  error: string
  disabled: boolean
  onChange: (selection: CodexSelection) => void
  closeSettingsSignal: number
  className?: string
}) {
  const [openMenu, setOpenMenu] = useState<'model' | 'reasoning' | null>(null)
  const model = selectedModel(options, selection.model)
  const reasoningOptions = model?.supported_reasoning_efforts ?? []
  const fastTier = fastTierForModel(model)
  const planAvailable = Boolean(options?.collaboration_modes.some((mode) => mode.mode === 'plan'))

  useEffect(() => {
    setOpenMenu(null)
  }, [closeSettingsSignal])

  if (loading && !options) {
    return <span className={cn('text-xs font-medium text-muted-foreground', className)}>Loading Codex options...</span>
  }

  if (error && !options) {
    return <span className={cn('text-xs font-medium text-destructive', className)}>Codex options unavailable</span>
  }

  return (
    <div
      className={cn(
        'flex min-w-0 flex-nowrap items-center gap-1.5 pl-1.5 text-sm font-medium text-muted-foreground',
        className,
      )}
    >
      <SlidersHorizontal className="size-4 shrink-0" aria-hidden="true" />
      <OptionMenu
        label="Model"
        value={model?.display_name || selection.model || 'Model'}
        open={openMenu === 'model'}
        onOpenChange={(open) => setOpenMenu(open ? 'model' : null)}
        disabled={disabled || !options?.models.length}
        options={(options?.models ?? []).map((item) => ({ value: item.model, label: item.display_name }))}
        onSelect={(modelValue) => {
          onChange(reconcileCodexSelection({ ...selection, model: modelValue }, options))
        }}
      />
      <span aria-hidden="true" className="text-muted-foreground/70">
        ·
      </span>
      <OptionMenu
        label="Reasoning"
        value={selection.reasoning_effort || model?.default_reasoning_effort || 'Reasoning'}
        open={openMenu === 'reasoning'}
        onOpenChange={(open) => setOpenMenu(open ? 'reasoning' : null)}
        disabled={disabled || reasoningOptions.length === 0}
        options={reasoningOptions.map((item) => ({
          value: item.reasoning_effort,
          label: item.reasoning_effort,
          description: item.description,
        }))}
        onSelect={(reasoningEffort) => onChange({ ...selection, reasoning_effort: reasoningEffort })}
      />
      <span aria-hidden="true" className="text-muted-foreground/70">
        ·
      </span>
      <ToggleControl
        label="Fast"
        icon={<Zap className="size-4" aria-hidden="true" />}
        active={selection.fast_mode && Boolean(fastTier)}
        disabled={disabled || !fastTier}
        onClick={() => onChange({ ...selection, fast_mode: fastTier ? !selection.fast_mode : false })}
      />
      <SwitchControl
        label="Plan"
        active={selection.planning_mode && planAvailable}
        disabled={disabled || !planAvailable}
        onClick={() => onChange({ ...selection, planning_mode: planAvailable ? !selection.planning_mode : false })}
      />
    </div>
  )
}

function MobileCodexOptions({
  options,
  selection,
  loading,
  error,
  disabled,
  onChange,
  closeSettingsSignal,
}: {
  options: CodexAgentOptions | null
  selection: CodexSelection
  loading: boolean
  error: string
  disabled: boolean
  onChange: (selection: CodexSelection) => void
  closeSettingsSignal: number
}) {
  const menuRef = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [openMenu, setOpenMenu] = useState<'model' | 'reasoning' | null>(null)
  const model = selectedModel(options, selection.model)
  const reasoningOptions = model?.supported_reasoning_efforts ?? []
  const fastTier = fastTierForModel(model)
  const hasActiveMode = selection.fast_mode && Boolean(fastTier)
  const summary = mobileCodexSummary({
    loading,
    error,
    options,
    modelName: model?.display_name || selection.model,
    reasoningEffort: selection.reasoning_effort || model?.default_reasoning_effort,
  })

  useEffect(() => {
    setOpen(false)
    setOpenMenu(null)
  }, [closeSettingsSignal])

  useEffect(() => {
    if (!open) {
      setOpenMenu(null)
      return
    }

    function handlePointerDown(event: PointerEvent) {
      if (!menuRef.current?.contains(event.target as Node)) {
        setOpen(false)
      }
    }

    function handleKeyDown(event: globalThis.KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }

    document.addEventListener('pointerdown', handlePointerDown)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [open])

  return (
    <div ref={menuRef} className="composer-mobile-options relative">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Composer options"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className={cn(
          'h-8 w-8 text-muted-foreground hover:text-foreground',
          hasActiveMode && 'bg-primary/12 text-primary hover:bg-primary/16 hover:text-primary',
        )}
      >
        <SlidersHorizontal aria-hidden="true" />
      </Button>
      {open ? (
        <div
          role="dialog"
          aria-label="Composer options"
          className="absolute bottom-full left-0 z-50 mb-2 w-[min(20rem,calc(100vw-2rem))] rounded-lg border border-border/80 bg-popover p-3 text-popover-foreground shadow-lg"
        >
          <div className="mb-3 flex min-w-0 items-center justify-between gap-3">
            <p className="text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">Options</p>
            <p className="min-w-0 truncate text-right text-xs text-muted-foreground">{summary}</p>
          </div>
          {loading && !options ? (
            <p className="text-sm text-muted-foreground">Loading Codex options...</p>
          ) : error && !options ? (
            <p className="text-sm text-destructive">Codex options unavailable</p>
          ) : (
            <div className="space-y-2">
              <OptionMenu
                label="Model"
                value={model?.display_name || selection.model || 'Model'}
                open={openMenu === 'model'}
                onOpenChange={(nextOpen) => setOpenMenu(nextOpen ? 'model' : null)}
                disabled={disabled || !options?.models.length}
                options={(options?.models ?? []).map((item) => ({ value: item.model, label: item.display_name }))}
                onSelect={(modelValue) => {
                  onChange(reconcileCodexSelection({ ...selection, model: modelValue }, options))
                }}
                buttonClassName="w-full justify-between rounded-md border border-border/80 bg-surface-muted/40 px-2"
                valueClassName="max-w-[13rem]"
                menuLayout="inline"
              />
              <OptionMenu
                label="Reasoning"
                value={selection.reasoning_effort || model?.default_reasoning_effort || 'Reasoning'}
                open={openMenu === 'reasoning'}
                onOpenChange={(nextOpen) => setOpenMenu(nextOpen ? 'reasoning' : null)}
                disabled={disabled || reasoningOptions.length === 0}
                options={reasoningOptions.map((item) => ({
                  value: item.reasoning_effort,
                  label: item.reasoning_effort,
                  description: item.description,
                }))}
                onSelect={(reasoningEffort) => onChange({ ...selection, reasoning_effort: reasoningEffort })}
                buttonClassName="w-full justify-between rounded-md border border-border/80 bg-surface-muted/40 px-2"
                valueClassName="max-w-[13rem]"
                menuLayout="inline"
              />
              <div className="flex flex-wrap items-center gap-2 pt-1">
                <ToggleControl
                  label="Fast"
                  icon={<Zap className="size-4" aria-hidden="true" />}
                  active={selection.fast_mode && Boolean(fastTier)}
                  disabled={disabled || !fastTier}
                  onClick={() => onChange({ ...selection, fast_mode: fastTier ? !selection.fast_mode : false })}
                />
              </div>
            </div>
          )}
        </div>
      ) : null}
    </div>
  )
}

function mobileCodexSummary({
  loading,
  error,
  options,
  modelName,
  reasoningEffort,
}: {
  loading: boolean
  error: string
  options: CodexAgentOptions | null
  modelName: string
  reasoningEffort: string | undefined
}) {
  if (loading && !options) {
    return 'Loading'
  }
  if (error && !options) {
    return 'Unavailable'
  }
  return [modelName || 'Model', reasoningEffort].filter(Boolean).join(' / ')
}

function OpenCodeToolbar({
  options,
  selection,
  loading,
  error,
  disabled,
  onChange,
  closeSettingsSignal,
  className,
}: {
  options: OpenCodeAgentOptions | null
  selection: OpenCodeSelection
  loading: boolean
  error: string
  disabled: boolean
  onChange: (selection: OpenCodeSelection) => void
  closeSettingsSignal: number
  className?: string
}) {
  const [openMenu, setOpenMenu] = useState<'model' | null>(null)
  const model = selectedOpenCodeModel(options, selection.model)
  const planAvailable = options?.collaboration_modes.some((mode) => mode.mode === 'plan') ?? false

  useEffect(() => {
    setOpenMenu(null)
  }, [closeSettingsSignal])

  return (
    <div
      className={cn(
        'flex min-w-0 flex-nowrap items-center gap-1.5 pl-1.5 text-sm font-medium text-muted-foreground',
        className,
      )}
    >
      <SlidersHorizontal className="size-4 shrink-0" aria-hidden="true" />
      <OptionMenu
        label="Model"
        value={loading && !options ? 'Loading' : error && !options ? 'Unavailable' : model?.display_name || 'Model'}
        open={openMenu === 'model'}
        onOpenChange={(open) => setOpenMenu(open ? 'model' : null)}
        disabled={disabled || !options?.models.length}
        options={(options?.models ?? []).map((item) => ({ value: item.model, label: item.display_name }))}
        onSelect={(modelValue) => onChange(reconcileOpenCodeSelection({ ...selection, model: modelValue }, options))}
      />
      <span aria-hidden="true" className="text-muted-foreground/70">
        ·
      </span>
      <SwitchControl
        label="Plan"
        active={selection.planning_mode && planAvailable}
        disabled={disabled || !planAvailable}
        onClick={() => onChange({ ...selection, planning_mode: planAvailable ? !selection.planning_mode : false })}
      />
    </div>
  )
}

function MobileOpenCodeOptions({
  options,
  selection,
  loading,
  error,
  disabled,
  onChange,
  closeSettingsSignal,
}: {
  options: OpenCodeAgentOptions | null
  selection: OpenCodeSelection
  loading: boolean
  error: string
  disabled: boolean
  onChange: (selection: OpenCodeSelection) => void
  closeSettingsSignal: number
}) {
  const menuRef = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [openMenu, setOpenMenu] = useState<'model' | null>(null)
  const model = selectedOpenCodeModel(options, selection.model)
  const hasActiveMode = false
  const summary =
    loading && !options
      ? 'Loading'
      : error && !options
        ? 'Unavailable'
        : [model?.display_name || 'Model', selection.planning_mode ? 'Plan' : 'Build'].filter(Boolean).join(' / ')

  useEffect(() => {
    setOpen(false)
    setOpenMenu(null)
  }, [closeSettingsSignal])

  useEffect(() => {
    if (!open) {
      setOpenMenu(null)
      return
    }

    function handlePointerDown(event: PointerEvent) {
      if (!menuRef.current?.contains(event.target as Node)) {
        setOpen(false)
      }
    }

    function handleKeyDown(event: globalThis.KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }

    document.addEventListener('pointerdown', handlePointerDown)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [open])

  return (
    <div ref={menuRef} className="composer-mobile-options relative">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Composer options"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className={cn(
          'h-8 w-8 text-muted-foreground hover:text-foreground',
          hasActiveMode && 'bg-primary/12 text-primary hover:bg-primary/16 hover:text-primary',
        )}
      >
        <SlidersHorizontal aria-hidden="true" />
      </Button>
      {open ? (
        <div
          role="dialog"
          aria-label="Composer options"
          className="absolute bottom-full left-0 z-50 mb-2 w-[min(20rem,calc(100vw-2rem))] rounded-lg border border-border/80 bg-popover p-3 text-popover-foreground shadow-lg"
        >
          <div className="mb-3 flex min-w-0 items-center justify-between gap-3">
            <p className="text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">Options</p>
            <p className="min-w-0 truncate text-right text-xs text-muted-foreground">{summary}</p>
          </div>
          <div className="space-y-2">
            <OptionMenu
              label="Model"
              value={model?.display_name || selection.model || 'Model'}
              open={openMenu === 'model'}
              onOpenChange={(nextOpen) => setOpenMenu(nextOpen ? 'model' : null)}
              disabled={disabled || !options?.models.length}
              options={(options?.models ?? []).map((item) => ({ value: item.model, label: item.display_name }))}
              onSelect={(modelValue) => onChange(reconcileOpenCodeSelection({ ...selection, model: modelValue }, options))}
              buttonClassName="w-full justify-between rounded-md border border-border/80 bg-surface-muted/40 px-2"
              valueClassName="max-w-[13rem]"
              menuLayout="inline"
            />
          </div>
        </div>
      ) : null}
    </div>
  )
}

function PiToolbar({
  options,
  selection,
  loading,
  error,
  disabled,
  onChange,
  closeSettingsSignal,
  className,
}: {
  options: PiAgentOptions | null
  selection: PiSelection
  loading: boolean
  error: string
  disabled: boolean
  onChange: (selection: PiSelection) => void
  closeSettingsSignal: number
  className?: string
}) {
  const [openMenu, setOpenMenu] = useState<'model' | 'thinking' | null>(null)
  const model = selectedPiModel(options, selection.model)
  const thinkingOptions = thinkingOptionsForModel(model)

  useEffect(() => {
    setOpenMenu(null)
  }, [closeSettingsSignal])

  return (
    <div
      className={cn(
        'flex min-w-0 flex-nowrap items-center gap-1.5 pl-1.5 text-sm font-medium text-muted-foreground',
        className,
      )}
    >
      <SlidersHorizontal className="size-4 shrink-0" aria-hidden="true" />
      <OptionMenu
        label="Model"
        value={loading && !options ? 'Loading' : error && !options ? 'Unavailable' : model?.display_name || 'Model'}
        open={openMenu === 'model'}
        onOpenChange={(open) => setOpenMenu(open ? 'model' : null)}
        disabled={disabled || !options?.models.length}
        options={(options?.models ?? []).map((item) => ({ value: item.model, label: item.display_name }))}
        onSelect={(modelValue) => onChange(reconcilePiSelection({ ...selection, model: modelValue }, options))}
      />
      <span aria-hidden="true" className="text-muted-foreground/70">
        ·
      </span>
      <OptionMenu
        label="Thinking"
        value={selection.thinking_level || 'Default'}
        open={openMenu === 'thinking'}
        onOpenChange={(open) => setOpenMenu(open ? 'thinking' : null)}
        disabled={disabled || thinkingOptions.length === 0}
        options={thinkingOptions}
        onSelect={(thinkingLevel) => onChange({ ...selection, thinking_level: thinkingLevel })}
      />
    </div>
  )
}

function MobilePiOptions({
  options,
  selection,
  loading,
  error,
  disabled,
  onChange,
  closeSettingsSignal,
}: {
  options: PiAgentOptions | null
  selection: PiSelection
  loading: boolean
  error: string
  disabled: boolean
  onChange: (selection: PiSelection) => void
  closeSettingsSignal: number
}) {
  const menuRef = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [openMenu, setOpenMenu] = useState<'model' | 'thinking' | null>(null)
  const model = selectedPiModel(options, selection.model)
  const thinkingOptions = thinkingOptionsForModel(model)
  const hasActiveMode = Boolean(selection.thinking_level)
  const summary =
    loading && !options
      ? 'Loading'
      : error && !options
        ? 'Unavailable'
        : [model?.display_name || 'Model', selection.thinking_level || 'Default'].filter(Boolean).join(' / ')

  useEffect(() => {
    setOpen(false)
    setOpenMenu(null)
  }, [closeSettingsSignal])

  useEffect(() => {
    if (!open) {
      setOpenMenu(null)
      return
    }

    function handlePointerDown(event: PointerEvent) {
      if (!menuRef.current?.contains(event.target as Node)) {
        setOpen(false)
      }
    }

    function handleKeyDown(event: globalThis.KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }

    document.addEventListener('pointerdown', handlePointerDown)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [open])

  return (
    <div ref={menuRef} className="composer-mobile-options relative">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Composer options"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className={cn(
          'h-8 w-8 text-muted-foreground hover:text-foreground',
          hasActiveMode && 'bg-primary/12 text-primary hover:bg-primary/16 hover:text-primary',
        )}
      >
        <SlidersHorizontal aria-hidden="true" />
      </Button>
      {open ? (
        <div
          role="dialog"
          aria-label="Composer options"
          className="absolute bottom-full left-0 z-50 mb-2 w-[min(20rem,calc(100vw-2rem))] rounded-lg border border-border/80 bg-popover p-3 text-popover-foreground shadow-lg"
        >
          <div className="mb-3 flex min-w-0 items-center justify-between gap-3">
            <p className="text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">Options</p>
            <p className="min-w-0 truncate text-right text-xs text-muted-foreground">{summary}</p>
          </div>
          <div className="space-y-2">
            <OptionMenu
              label="Model"
              value={model?.display_name || selection.model || 'Model'}
              open={openMenu === 'model'}
              onOpenChange={(nextOpen) => setOpenMenu(nextOpen ? 'model' : null)}
              disabled={disabled || !options?.models.length}
              options={(options?.models ?? []).map((item) => ({ value: item.model, label: item.display_name }))}
              onSelect={(modelValue) => onChange(reconcilePiSelection({ ...selection, model: modelValue }, options))}
              buttonClassName="w-full justify-between rounded-md border border-border/80 bg-surface-muted/40 px-2"
              valueClassName="max-w-[13rem]"
              menuLayout="inline"
            />
            <OptionMenu
              label="Thinking"
              value={selection.thinking_level || 'Default'}
              open={openMenu === 'thinking'}
              onOpenChange={(nextOpen) => setOpenMenu(nextOpen ? 'thinking' : null)}
              disabled={disabled || thinkingOptions.length === 0}
              options={thinkingOptions}
              onSelect={(thinkingLevel) => onChange({ ...selection, thinking_level: thinkingLevel })}
              buttonClassName="w-full justify-between rounded-md border border-border/80 bg-surface-muted/40 px-2"
              valueClassName="max-w-[13rem]"
              menuLayout="inline"
            />
          </div>
        </div>
      ) : null}
    </div>
  )
}

function ClaudeToolbar({
  selection,
  disabled,
  onChange,
  closeSettingsSignal,
  className,
}: {
  selection: ClaudeSelection
  disabled: boolean
  onChange: (selection: ClaudeSelection) => void
  closeSettingsSignal: number
  className?: string
}) {
  const [openMenu, setOpenMenu] = useState<'model' | 'effort' | null>(null)

  useEffect(() => {
    setOpenMenu(null)
  }, [closeSettingsSignal])

  return (
    <div
      className={cn(
        'flex min-w-0 flex-nowrap items-center gap-1.5 pl-1.5 text-sm font-medium text-muted-foreground',
        className,
      )}
    >
      <SlidersHorizontal className="size-4 shrink-0" aria-hidden="true" />
      <OptionMenu
        label="Model"
        value={claudeModelLabel(selection.model)}
        open={openMenu === 'model'}
        onOpenChange={(open) => setOpenMenu(open ? 'model' : null)}
        disabled={disabled}
        options={claudeModelOptions}
        onSelect={(model) => onChange({ ...selection, model })}
      />
      <span aria-hidden="true" className="text-muted-foreground/70">
        ·
      </span>
      <OptionMenu
        label="Effort"
        value={selection.effort || 'Effort'}
        open={openMenu === 'effort'}
        onOpenChange={(open) => setOpenMenu(open ? 'effort' : null)}
        disabled={disabled}
        options={claudeEffortOptions}
        onSelect={(effort) => onChange({ ...selection, effort })}
      />
      <SwitchControl
        label="Plan"
        active={selection.planning_mode}
        disabled={disabled}
        onClick={() => onChange({ ...selection, planning_mode: !selection.planning_mode })}
      />
    </div>
  )
}

function MobileClaudeOptions({
  selection,
  disabled,
  onChange,
  closeSettingsSignal,
}: {
  selection: ClaudeSelection
  disabled: boolean
  onChange: (selection: ClaudeSelection) => void
  closeSettingsSignal: number
}) {
  const menuRef = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [openMenu, setOpenMenu] = useState<'model' | 'effort' | null>(null)
  const hasActiveMode = false
  const summary = [claudeModelLabel(selection.model), selection.effort].filter(Boolean).join(' / ')

  useEffect(() => {
    setOpen(false)
    setOpenMenu(null)
  }, [closeSettingsSignal])

  useEffect(() => {
    if (!open) {
      setOpenMenu(null)
      return
    }

    function handlePointerDown(event: PointerEvent) {
      if (!menuRef.current?.contains(event.target as Node)) {
        setOpen(false)
      }
    }

    function handleKeyDown(event: globalThis.KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }

    document.addEventListener('pointerdown', handlePointerDown)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [open])

  return (
    <div ref={menuRef} className="composer-mobile-options relative">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Composer options"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className={cn(
          'h-8 w-8 text-muted-foreground hover:text-foreground',
          hasActiveMode && 'bg-primary/12 text-primary hover:bg-primary/16 hover:text-primary',
        )}
      >
        <SlidersHorizontal aria-hidden="true" />
      </Button>
      {open ? (
        <div
          role="dialog"
          aria-label="Composer options"
          className="absolute bottom-full left-0 z-50 mb-2 w-[min(20rem,calc(100vw-2rem))] rounded-lg border border-border/80 bg-popover p-3 text-popover-foreground shadow-lg"
        >
          <div className="mb-3 flex min-w-0 items-center justify-between gap-3">
            <p className="text-xs font-semibold uppercase tracking-[0.12em] text-muted-foreground">Options</p>
            <p className="min-w-0 truncate text-right text-xs text-muted-foreground">{summary}</p>
          </div>
          <div className="space-y-2">
            <OptionMenu
              label="Model"
              value={claudeModelLabel(selection.model)}
              open={openMenu === 'model'}
              onOpenChange={(nextOpen) => setOpenMenu(nextOpen ? 'model' : null)}
              disabled={disabled}
              options={claudeModelOptions}
              onSelect={(model) => onChange({ ...selection, model })}
              buttonClassName="w-full justify-between rounded-md border border-border/80 bg-surface-muted/40 px-2"
              valueClassName="max-w-[13rem]"
              menuLayout="inline"
            />
            <OptionMenu
              label="Effort"
              value={selection.effort || 'Effort'}
              open={openMenu === 'effort'}
              onOpenChange={(nextOpen) => setOpenMenu(nextOpen ? 'effort' : null)}
              disabled={disabled}
              options={claudeEffortOptions}
              onSelect={(effort) => onChange({ ...selection, effort })}
              buttonClassName="w-full justify-between rounded-md border border-border/80 bg-surface-muted/40 px-2"
              valueClassName="max-w-[13rem]"
              menuLayout="inline"
            />
          </div>
        </div>
      ) : null}
    </div>
  )
}

function claudeModelLabel(model: string) {
  return claudeModelOptions.find((option) => option.value === model)?.label ?? (model || 'Default')
}

function OptionMenu({
  label,
  value,
  open,
  onOpenChange,
  options,
  disabled,
  onSelect,
  buttonClassName,
  valueClassName,
  menuLayout = 'floating',
}: {
  label: string
  value: string
  open: boolean
  onOpenChange: (open: boolean) => void
  options: { value: string; label: string; description?: string }[]
  disabled: boolean
  onSelect: (value: string) => void
  buttonClassName?: string
  valueClassName?: string
  menuLayout?: 'floating' | 'inline'
}) {
  return (
    <div className="relative">
      <button
        type="button"
        aria-label={label}
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => onOpenChange(!open)}
        className={cn(
          'inline-flex h-8 items-center gap-1 rounded-md px-1.5 text-sm font-semibold text-foreground/78 transition-colors hover:text-foreground disabled:pointer-events-none disabled:opacity-50',
          buttonClassName,
        )}
      >
        <span className={cn('max-w-40 truncate', valueClassName)}>{value}</span>
        <ChevronDown className="size-4 text-muted-foreground" aria-hidden="true" />
      </button>
      {open ? (
        <div
          role="listbox"
          aria-label={label}
          className={cn(
            'z-50 overflow-auto rounded-lg bg-popover p-1 text-popover-foreground shadow-lg ring-1 ring-border/70',
            menuLayout === 'inline'
              ? 'mt-1 max-h-52 w-full'
              : 'absolute bottom-full left-0 mb-2 max-h-72 min-w-48',
          )}
        >
          {options.map((option) => (
            <button
              key={option.value}
              type="button"
              role="option"
              aria-selected={option.label === value}
              onClick={() => {
                onSelect(option.value)
                onOpenChange(false)
              }}
              className="flex w-full flex-col rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent/70"
            >
              <span className="font-medium">{option.label}</span>
              {option.description ? (
                <span className="max-w-72 truncate text-xs text-muted-foreground">{option.description}</span>
              ) : null}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  )
}

function ToggleControl({
  label,
  icon,
  active,
  disabled,
  iconOnly = false,
  activeClassName,
  onClick,
}: {
  label: string
  icon: ReactNode
  active: boolean
  disabled: boolean
  iconOnly?: boolean
  activeClassName?: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      aria-label={label}
      aria-pressed={active}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        'inline-flex h-8 items-center gap-1.5 text-sm font-semibold transition-colors disabled:pointer-events-none disabled:opacity-50',
        iconOnly ? 'w-8 justify-center rounded-md px-0' : 'rounded-full px-2',
        active ? (activeClassName ?? 'bg-primary/12 text-primary') : 'text-foreground/72 hover:text-foreground',
      )}
    >
      {icon}
      {iconOnly ? null : <span>{label}</span>}
    </button>
  )
}

function SwitchControl({
  label,
  active,
  disabled,
  onClick,
}: {
  label: string
  active: boolean
  disabled: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-label={label}
      aria-checked={active}
      aria-pressed={active}
      disabled={disabled}
      onClick={onClick}
      className="inline-flex h-8 items-center gap-2 rounded-full px-2 text-sm font-semibold text-foreground/72 transition-colors hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
    >
      <ClipboardList className="size-4" aria-hidden="true" />
      <span>{label}</span>
      <span
        className={cn(
          'relative inline-flex h-4 w-7 shrink-0 rounded-full border transition-colors',
          active ? 'border-amber-500/50 bg-amber-400 dark:bg-amber-400/70' : 'border-border/80 bg-surface-muted',
        )}
        aria-hidden="true"
      >
        <span
          className={cn(
            'absolute top-1/2 size-3 -translate-y-1/2 rounded-full bg-background shadow-sm transition-transform',
            active ? 'translate-x-3.5' : 'translate-x-0.5',
          )}
        />
      </span>
    </button>
  )
}

function submitOptionsForCodex(selection: CodexSelection, fastTier: CodexServiceTierOption | null): SubmitAgentOptions {
  return {
    codex: {
      model: selection.model || undefined,
      reasoning_effort: selection.reasoning_effort || undefined,
      fast_mode: selection.fast_mode,
      planning_mode: selection.planning_mode,
      service_tier: selection.fast_mode ? fastTier?.id : undefined,
    },
  }
}

function submitOptionsForClaude(selection: ClaudeSelection): SubmitAgentOptions {
  return {
    claude: {
      model: selection.model || undefined,
      effort: selection.effort || undefined,
      planning_mode: selection.planning_mode,
    },
  }
}

function submitOptionsForOpenCode(selection: OpenCodeSelection): SubmitAgentOptions {
  return {
    opencode: {
      model: selection.model || undefined,
      planning_mode: selection.planning_mode,
    },
  }
}

function submitOptionsForPi(selection: PiSelection): SubmitAgentOptions {
  return {
    pi: {
      model: selection.model || undefined,
      thinking_level: selection.thinking_level || undefined,
    },
  }
}

function reconcileCodexSelection(selection: CodexSelection, options: CodexAgentOptions | null): CodexSelection {
  if (!options || options.models.length === 0) {
    return selection
  }

  const model =
    options.models.find((item) => item.model === selection.model) ??
    options.models.find((item) => item.model === options.default_model) ??
    options.models.find((item) => item.is_default) ??
    options.models[0]
  const reasoningEfforts = model.supported_reasoning_efforts.map((item) => item.reasoning_effort)
  const reasoningEffort = reasoningEfforts.includes(selection.reasoning_effort)
    ? selection.reasoning_effort
    : model.default_reasoning_effort || reasoningEfforts[0] || ''
  const planAvailable = options.collaboration_modes.some((mode) => mode.mode === 'plan')

  return {
    model: model.model,
    reasoning_effort: reasoningEffort,
    fast_mode: selection.fast_mode && Boolean(fastTierForModel(model)),
    planning_mode: selection.planning_mode && planAvailable,
  }
}

function reconcileOpenCodeSelection(
  selection: OpenCodeSelection,
  options: OpenCodeAgentOptions | null,
): OpenCodeSelection {
  if (!options || options.models.length === 0) {
    return selection
  }

  const model =
    options.models.find((item) => item.model === selection.model) ??
    options.models.find((item) => item.model === options.default_model) ??
    options.models.find((item) => item.is_default) ??
    options.models[0]
  const planAvailable = options.collaboration_modes.some((mode) => mode.mode === 'plan')

  return {
    model: model.model,
    planning_mode: selection.planning_mode && planAvailable,
  }
}

function reconcilePiSelection(selection: PiSelection, options: PiAgentOptions | null): PiSelection {
  if (!options || options.models.length === 0) {
    return selection
  }

  const model =
    options.models.find((item) => item.model === selection.model) ??
    options.models.find((item) => item.model === options.default_model) ??
    options.models.find((item) => item.is_default) ??
    options.models[0]
  const thinkingLevels = model.supported_reasoning_efforts.map((item) => item.reasoning_effort)
  const thinkingLevel = thinkingLevels.includes(selection.thinking_level)
    ? selection.thinking_level
    : model.default_reasoning_effort || thinkingLevels[0] || ''

  return {
    model: model.model,
    thinking_level: thinkingLevel,
  }
}

function selectedModel(options: CodexAgentOptions | null, model: string) {
  if (!options) {
    return null
  }
  return (
    options.models.find((item) => item.model === model) ??
    options.models.find((item) => item.model === options.default_model) ??
    options.models.find((item) => item.is_default) ??
    options.models[0] ??
    null
  )
}

function selectedOpenCodeModel(options: OpenCodeAgentOptions | null, model: string) {
  if (!options) {
    return null
  }
  return (
    options.models.find((item) => item.model === model) ??
    options.models.find((item) => item.model === options.default_model) ??
    options.models.find((item) => item.is_default) ??
    options.models[0] ??
    null
  )
}

function selectedPiModel(options: PiAgentOptions | null, model: string) {
  if (!options) {
    return null
  }
  return (
    options.models.find((item) => item.model === model) ??
    options.models.find((item) => item.model === options.default_model) ??
    options.models.find((item) => item.is_default) ??
    options.models[0] ??
    null
  )
}

function thinkingOptionsForModel(model: CodexModelOption | null) {
  return model?.supported_reasoning_efforts.map((item) => ({ value: item.reasoning_effort, label: item.reasoning_effort })) ?? []
}

function fastTierForModel(model: CodexModelOption | null): CodexServiceTierOption | null {
  return model?.service_tiers.find((tier) => tier.name.toLowerCase() === 'fast') ?? null
}

function loadComposerStorage(sessionID: string | undefined): ComposerStorageValue {
  try {
    const stored = window.localStorage.getItem(composerStorageKey(sessionID))
    if (!stored) {
      return {}
    }
    const parsed = JSON.parse(stored) as ComposerStorageValue
    return typeof parsed === 'object' && parsed !== null ? parsed : {}
  } catch {
    return {}
  }
}

function saveComposerStorage(sessionID: string | undefined, value: ComposerStorageValue) {
  try {
    window.localStorage.setItem(composerStorageKey(sessionID), JSON.stringify(value))
  } catch {
    // Keep the composer functional if storage is unavailable.
  }
}

function loadDraft(sessionID: string | undefined) {
  return loadComposerDraft(sessionID).draft
}

function loadSelectedSkills(sessionID: string | undefined): SkillReference[] {
  return loadComposerDraft(sessionID).selectedSkills
}

function skillKey(reference: SkillReference) {
  return `${reference.name}\u0000${reference.path}`
}

function skillDisplayName(skill: AgentSkill) {
  return skill.display_name || skill.name
}

const skillScopeOrder: SkillScope[] = ['repo', 'user', 'admin', 'system']

function availableSkillScopes(skills: AgentSkill[]) {
  return skillScopeOrder.flatMap((scope) => {
    const count = skills.filter((skill) => skill.scope === scope).length
    return count > 0 ? [{ scope, count }] : []
  })
}

function skillScopeLabel(scope: SkillScope) {
  return `${scope.charAt(0).toUpperCase()}${scope.slice(1)}`
}

function skillScopeFilterClassName(selected: boolean) {
  return cn(
    'inline-flex h-7 shrink-0 items-center gap-1 rounded-full border px-2.5 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
    selected
      ? 'border-primary/30 bg-primary/10 text-primary'
      : 'border-border/80 bg-background text-muted-foreground hover:bg-accent hover:text-foreground',
  )
}

function scopeRank(scope: SkillScope) {
  switch (scope) {
    case 'repo':
      return 0
    case 'user':
      return 1
    case 'admin':
      return 2
    case 'system':
      return 3
  }
}

function sortSkills(skills: AgentSkill[]) {
  return [...skills].sort((left, right) => {
    const scopeDifference = scopeRank(left.scope) - scopeRank(right.scope)
    if (scopeDifference !== 0) return scopeDifference
    return skillDisplayName(left).localeCompare(skillDisplayName(right)) || left.path.localeCompare(right.path)
  })
}

function filterSkills(skills: AgentSkill[], query: string) {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return skills
  return skills
    .map((skill) => ({ skill, score: skillMatchScore(skill, normalized) }))
    .filter((candidate) => candidate.score < 100)
    .sort((left, right) => {
      const scopeDifference = scopeRank(left.skill.scope) - scopeRank(right.skill.scope)
      return scopeDifference || left.score - right.score || skillDisplayName(left.skill).localeCompare(skillDisplayName(right.skill))
    })
    .map(({ skill }) => skill)
}

function skillMatchScore(skill: AgentSkill, query: string) {
  const name = skill.name.toLowerCase()
  const displayName = skillDisplayName(skill).toLowerCase()
  if (name === query || displayName === query) return 0
  if (name.startsWith(query)) return 1
  if (displayName.startsWith(query)) return 2
  if (name.includes(query) || displayName.includes(query)) return 3
  const description = `${skill.short_description || ''} ${skill.description}`.toLowerCase()
  return description.includes(query) ? 4 : 100
}

function skillTypeaheadAt(content: string, caret: number): SkillTypeahead | null {
  const prefix = content.slice(0, caret)
  const match = prefix.match(/(?:^|\s)\$([A-Za-z0-9:_-]*)$/)
  if (!match) return null
  const query = match[1] ?? ''
  return { start: caret - query.length - 1, end: caret, query }
}

function insertInlineSkillToken(content: string, name: string, start: number, end: number) {
  const safeStart = Math.max(0, Math.min(start, content.length))
  const safeEnd = Math.max(safeStart, Math.min(end, content.length))
  const before = content.slice(0, safeStart)
  const after = content.slice(safeEnd)
  const leadingSpace = before && !/\s$/.test(before) ? ' ' : ''
  const trailingSpace = !after || !/^\s/.test(after) ? ' ' : ''
  const token = `$${name}`
  const inserted = `${leadingSpace}${token}${trailingSpace}`
  return {
    content: before + inserted + after,
    caret: safeStart + inserted.length,
  }
}

function ensureInlineSkillTokens(content: string, selected: SkillReference[]) {
  return selected.reduce((current, skill) => {
    if (contentHasSkillToken(current, skill.name)) return current
    const prefix = current && !/\s$/.test(current) ? ' ' : ''
    return `${current}${prefix}$${skill.name} `
  }, content)
}

function inlineSkillReferences(content: string, available: AgentSkill[], current: SkillReference[]) {
  const byName = new Map<string, SkillReference>()
  current.forEach((reference) => {
    if (contentHasSkillToken(content, reference.name) && !byName.has(reference.name)) {
      byName.set(reference.name, reference)
    }
  })
  sortSkills(available).forEach((skill) => {
    if (contentHasSkillToken(content, skill.name) && !byName.has(skill.name)) {
      byName.set(skill.name, { name: skill.name, path: skill.path })
    }
  })
  const next = [...byName.values()]
  return sameSkillReferences(current, next) ? current : next
}

function contentHasSkillToken(content: string, name: string) {
  const escapedName = escapeRegExp(name)
  return new RegExp(`\\$${escapedName}(?![A-Za-z0-9:_-])`).test(content)
}

function inlineSkillHighlightSegments(content: string, selected: SkillReference[], skills: AgentSkill[]) {
  const references = new Map(selected.map((reference) => [reference.name, reference]))
  const names = [...references.keys()].sort((left, right) => right.length - left.length)
  if (names.length === 0) return [{ text: content }]

  const pattern = new RegExp(`\\$(${names.map(escapeRegExp).join('|')})(?![A-Za-z0-9:_-])`, 'g')
  const segments: Array<{ text: string; reference?: SkillReference; skill?: AgentSkill }> = []
  let cursor = 0
  for (const match of content.matchAll(pattern)) {
    const start = match.index ?? 0
    if (start > cursor) segments.push({ text: content.slice(cursor, start) })
    const reference = references.get(match[1])
    segments.push({
      text: match[0],
      reference,
      skill: reference ? skills.find((skill) => skillKey(skill) === skillKey(reference)) : undefined,
    })
    cursor = start + match[0].length
  }
  if (cursor < content.length) segments.push({ text: content.slice(cursor) })
  return segments
}

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function sameSkillReferences(left: SkillReference[], right: SkillReference[]) {
  return left.length === right.length && left.every((reference, index) => skillKey(reference) === skillKey(right[index]))
}

function validSkillColor(value: string | undefined) {
  return value && /^#[0-9a-f]{6}$/i.test(value) ? value : ''
}

function loadCodexSelection(
  sessionID: string | undefined,
  runtimeOptions: SessionRuntimeAgentOptions | null = null,
): CodexSelection {
  if (runtimeOptions?.codex) {
    return {
      model: runtimeOptions.codex.model ?? '',
      reasoning_effort: runtimeOptions.codex.reasoning_effort ?? '',
      fast_mode: runtimeOptions.codex.fast_mode,
      planning_mode: runtimeOptions.codex.planning_mode,
    }
  }
  const stored = loadComposerStorage(sessionID).codexSelection ?? {}
  return {
    model: typeof stored.model === 'string' ? stored.model : '',
    reasoning_effort: typeof stored.reasoning_effort === 'string' ? stored.reasoning_effort : '',
    fast_mode: Boolean(stored.fast_mode),
    planning_mode: Boolean(stored.planning_mode),
  }
}

function saveCodexSelection(sessionID: string | undefined, selection: CodexSelection) {
  saveComposerStorage(sessionID, {
    ...loadComposerStorage(sessionID),
    codexSelection: selection,
  })
}

function loadClaudeSelection(
  sessionID: string | undefined,
  runtimeOptions: SessionRuntimeAgentOptions | null = null,
): ClaudeSelection {
  if (runtimeOptions?.claude) {
    return {
      model: runtimeOptions.claude.model ?? '',
      effort: runtimeOptions.claude.effort ?? 'medium',
      planning_mode: runtimeOptions.claude.planning_mode,
    }
  }
  const stored = loadComposerStorage(sessionID).claudeSelection ?? {}
  return {
    model: typeof stored.model === 'string' ? stored.model : '',
    effort: typeof stored.effort === 'string' ? stored.effort : 'medium',
    planning_mode: Boolean(stored.planning_mode),
  }
}

function saveClaudeSelection(sessionID: string | undefined, selection: ClaudeSelection) {
  saveComposerStorage(sessionID, {
    ...loadComposerStorage(sessionID),
    claudeSelection: selection,
  })
}

function loadOpenCodeSelection(
  sessionID: string | undefined,
  runtimeOptions: SessionRuntimeAgentOptions | null = null,
): OpenCodeSelection {
  if (runtimeOptions?.opencode) {
    return {
      model: runtimeOptions.opencode.model ?? '',
      planning_mode: runtimeOptions.opencode.planning_mode,
    }
  }
  const stored = loadComposerStorage(sessionID).opencodeSelection ?? {}
  return {
    model: typeof stored.model === 'string' ? stored.model : '',
    planning_mode: Boolean(stored.planning_mode),
  }
}

function saveOpenCodeSelection(sessionID: string | undefined, selection: OpenCodeSelection) {
  saveComposerStorage(sessionID, {
    ...loadComposerStorage(sessionID),
    opencodeSelection: selection,
  })
}

function loadPiSelection(
  sessionID: string | undefined,
  runtimeOptions: SessionRuntimeAgentOptions | null = null,
): PiSelection {
  if (runtimeOptions?.pi) {
    return {
      model: runtimeOptions.pi.model ?? '',
      thinking_level: runtimeOptions.pi.thinking_level ?? '',
    }
  }
  const stored = loadComposerStorage(sessionID).piSelection ?? {}
  return {
    model: typeof stored.model === 'string' ? stored.model : '',
    thinking_level: typeof stored.thinking_level === 'string' ? stored.thinking_level : '',
  }
}

function savePiSelection(sessionID: string | undefined, selection: PiSelection) {
  saveComposerStorage(sessionID, {
    ...loadComposerStorage(sessionID),
    piSelection: selection,
  })
}

function clearStoredAgentSelections(sessionID: string | undefined) {
  if (!sessionID) return
  const remaining = loadComposerStorage(sessionID)
  delete remaining.codexSelection
  delete remaining.claudeSelection
  delete remaining.opencodeSelection
  delete remaining.piSelection
  saveComposerStorage(sessionID, remaining)
}

function runtimeAgentOptionsFromSession(
  agentType: AgentType,
  options: SessionAgentOptions | undefined,
): SessionRuntimeAgentOptions | null {
  if (agentType === 'codex' && options?.codex && hasRuntimeOption(options.codex, [
    'model',
    'reasoning_effort',
    'fast_mode',
    'planning_mode',
  ])) {
    return {
      codex: {
        model: options.codex.model,
        reasoning_effort: options.codex.reasoning_effort,
        fast_mode: options.codex.fast_mode ?? false,
        planning_mode: options.codex.planning_mode ?? false,
      },
    }
  }
  if (agentType === 'claude' && options?.claude && hasRuntimeOption(options.claude, [
    'model',
    'effort',
    'planning_mode',
  ])) {
    return {
      claude: {
        // Match the picker defaults: the server omits an empty model. Different
        // keys leave adoptingServerKey set and suppress subsequent user edits.
        model: options.claude.model ?? '',
        effort: options.claude.effort ?? 'medium',
        planning_mode: options.claude.planning_mode ?? false,
      },
    }
  }
  if (agentType === 'opencode' && options?.opencode && hasRuntimeOption(options.opencode, [
    'model',
    'planning_mode',
  ])) {
    return {
      opencode: {
        model: options.opencode.model,
        planning_mode: options.opencode.planning_mode ?? false,
      },
    }
  }
  if (agentType === 'pi' && options?.pi && hasRuntimeOption(options.pi, ['model', 'thinking_level'])) {
    return {
      pi: {
        model: options.pi.model,
        thinking_level: options.pi.thinking_level,
      },
    }
  }
  return null
}

function runtimeAgentOptionsFromSelections(
  agentType: AgentType,
  codex: CodexSelection,
  claude: ClaudeSelection,
  opencode: OpenCodeSelection,
  pi: PiSelection,
): SessionRuntimeAgentOptions | null {
  switch (agentType) {
    case 'codex':
      return { codex: { ...codex } }
    case 'claude':
      return { claude: { ...claude } }
    case 'opencode':
      return { opencode: { ...opencode } }
    case 'pi':
      return { pi: { ...pi } }
    default:
      return null
  }
}

function runtimeAgentOptionsKey(options: SessionRuntimeAgentOptions | null) {
  return options ? JSON.stringify(options) : ''
}

function hasRuntimeOption(options: object, keys: string[]) {
  return keys.some((key) => Object.prototype.hasOwnProperty.call(options, key))
}

function resizePromptTextarea(textarea: HTMLTextAreaElement | null) {
  if (!textarea) {
    return
  }

  textarea.style.height = 'auto'

  const styles = window.getComputedStyle(textarea)
  const lineHeight = parseFloat(styles.lineHeight) || fallbackLineHeight
  const paddingY = cssNumber(styles.paddingTop) + cssNumber(styles.paddingBottom)
  const borderY = cssNumber(styles.borderTopWidth) + cssNumber(styles.borderBottomWidth)
  const minHeight = lineHeight + paddingY + borderY
  const maxHeight = lineHeight * maxPromptRows + paddingY + borderY
  const scrollHeight = textarea.scrollHeight + borderY
  const nextHeight = Math.min(Math.max(scrollHeight, minHeight), maxHeight)

  textarea.style.height = `${nextHeight}px`
  textarea.style.overflowY = scrollHeight > maxHeight ? 'auto' : 'hidden'
}

function supportsComposerAutoFocus() {
  if (typeof window === 'undefined') {
    return false
  }
  if (typeof window.matchMedia !== 'function') {
    return navigator.maxTouchPoints === 0
  }
  return window.matchMedia(composerAutoFocusMediaQuery).matches
}

function cssNumber(value: string) {
  return parseFloat(value) || 0
}

function insertTextareaNewline(textarea: HTMLTextAreaElement, setContent: (content: string) => void) {
  const start = textarea.selectionStart
  const end = textarea.selectionEnd
  const nextContent = `${textarea.value.slice(0, start)}\n${textarea.value.slice(end)}`
  const nextPosition = start + 1

  textarea.value = nextContent
  textarea.selectionStart = nextPosition
  textarea.selectionEnd = nextPosition
  setContent(nextContent)
  resizePromptTextarea(textarea)
}
