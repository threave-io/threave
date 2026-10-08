import type { AgentEvent, PermissionRequest, SessionStatus, SkillReference, UserInputQuestion } from '@/lib/api'

export const knownEventTypes = [
  'user.message.completed',
  'user.message.steer.submitted',
  'user.message.steer.failed',
  'user.message.queued',
  'user.message.queue.removed',
  'user.action.completed',
  'session.action.completed',
  'session.agent_options.updated',
  'session.pin.updated',
  'session.archived',
  'session.restored',
  'session.status.updated',
  'agent.run.started',
  'agent.status.started',
  'agent.message.delta',
  'agent.message.completed',
  'agent.plan.delta',
  'agent.plan.completed',
  'agent.thinking.started',
  'agent.thinking.delta',
  'agent.thinking.completed',
  'agent.log.delta',
  'agent.input.requested',
  'agent.input.answered',
  'agent.input.cancelled',
  'agent.input.submitted',
  'agent.input.failed',
  'agent.permission.requested',
  'agent.permission.resolved',
  'agent.permission.cancelled',
  'tool.call.started',
  'tool.call.delta',
  'tool.call.completed',
  'file.change.started',
  'file.change.delta',
  'file.change.completed',
  'provider.codex.event',
  'provider.codex.request',
  'provider.codex.parse_error',
  'provider.claude.event',
  'provider.claude.parse_error',
  'provider.opencode.event',
  'provider.opencode.request',
  'provider.opencode.parse_error',
  'provider.pi.event',
  'provider.pi.parse_error',
  'agent.run.completed',
  'agent.run.failed',
  'agent.run.cancelled',
] as const

export function isDebugOnlyEvent(event: AgentEvent) {
  switch (event.type) {
    case 'agent.log.delta':
    case 'provider.codex.request':
    case 'provider.codex.parse_error':
    case 'provider.claude.parse_error':
    case 'provider.opencode.request':
    case 'provider.opencode.parse_error':
    case 'provider.pi.parse_error':
    case 'provider.pi.event':
      return true
    case 'provider.codex.event': {
      const providerEventType = debugPayloadStringAt(event.payload, 'provider_event_type')
      if (providerEventType === 'thread/tokenUsage/updated' || providerEventType === 'item/plan/delta') {
        return false
      }
      return providerEventType !== 'item/completed' || debugPayloadStringAt(event.payload, 'raw', 'item', 'type') !== 'plan'
    }
    case 'provider.claude.event':
      return !isRecord(event.payload) || !('usage' in event.payload)
    case 'provider.opencode.event':
      return debugPayloadStringAt(event.payload, 'provider_event_type') !== 'usage_update'
    default:
      return false
  }
}

function debugPayloadStringAt(payload: unknown, ...path: string[]) {
  let value = payload
  for (const key of path) {
    if (!isRecord(value)) return ''
    value = value[key]
  }
  return typeof value === 'string' ? value : ''
}

export type DisplayEvent = AgentEvent & {
  display_type?: string
}

export type EventGroupKind =
  | 'user-message'
  | 'action-break'
  | 'agent-message'
  | 'plan'
  | 'thinking'
  | 'tool-call'
  | 'file-change'
  | 'log'
  | 'error'
  | 'terminal'
  | 'unknown'

export type EventGroup = {
  id: string
  kind: EventGroupKind
  label: string
  status: string
  startSeq: number
  endSeq: number
  events: AgentEvent[]
  text: string
  error: string
  paths: string[]
  defaultOpen: boolean
  terminal: boolean
}

export type ChatTranscriptTool = {
  id: string
  kind: Extract<EventGroupKind, 'tool-call' | 'file-change'>
  label: string
  status: string
  text: string
  error: string
  content: ChatTranscriptToolContent[]
  fullOutputURL: string
  paths: string[]
  startSeq: number
  endSeq: number
}

export type ChatTranscriptToolContent = {
  kind: 'image' | 'audio' | 'resource' | 'resource-link'
  name: string
  mediaType: string
  sourceURL: string
  uri: string
  description: string
}

export type ChatTranscriptAttachment = {
  name: string
  mediaType: string
  dataURL: string
  sourceURL: string
  sizeBytes: number
}

export type ChatTranscriptMessage = {
  id: string
  role: 'user' | 'assistant'
  label: string
  variant: 'default' | 'plan'
  text: string
  attachments: ChatTranscriptAttachment[]
  skills: SkillReference[]
  status: string
  createdAt: string
  completedAt: string
  durationMs: number | null
  tools: ChatTranscriptTool[]
  streaming: boolean
  startSeq: number
  endSeq: number
}

export type ChatDebugEvent = {
  id: string
  label: string
  status: string
  startSeq: number
  endSeq: number
  eventCount: number
  text: string
  error: string
  payload: unknown
  createdAt: string
}

export type ActiveRunActivity = {
  runStartedAt: string
  lastVisibleActivityAt: string
  lastVisibleActivitySeq: number
}

export type ChatActionBreak = {
  id: string
  action: string
  label: string
  detail: string
  createdAt: string
  startSeq: number
  endSeq: number
}

export type ChatRunError = {
  id: string
  label: string
  error: string
  status: string
  createdAt: string
  startSeq: number
  endSeq: number
}

export type ChatTimelineItem =
  | {
      kind: 'message'
      id: string
      startSeq: number
      endSeq: number
      message: ChatTranscriptMessage
    }
  | {
      kind: 'debug'
      id: string
      startSeq: number
      endSeq: number
      event: ChatDebugEvent
    }
  | {
      kind: 'error'
      id: string
      startSeq: number
      endSeq: number
      error: ChatRunError
    }
  | {
      kind: 'action'
      id: string
      startSeq: number
      endSeq: number
      action: ChatActionBreak
    }

export type TranscriptSequenceRange = {
  firstSeq: number
  lastSeq: number
}

export type PendingUserInputRequest = {
  requestID: string
  provider: string
  providerEventType: string
  threadID: string
  turnID: string
  itemID: string
  questions: UserInputQuestion[]
  createdAt: string
  seq: number
  delivery?: 'async'
  submitting?: boolean
  deliveryError?: string
}

export type PendingPermissionRequest = PermissionRequest & { createdAt: string; seq: number }

export type TokenUsageSnapshot = {
  totalTokens: number
  inputTokens: number
  cachedInputTokens: number
  outputTokens: number
  reasoningOutputTokens: number
}

export type TokenUsageSummary = {
  kind?: 'tokens' | 'context'
  total: TokenUsageSnapshot
  last: TokenUsageSnapshot | null
  modelContextWindow: number | null
  sessionTotalTokens?: number
  cost?: {
    amount: number
    currency: string
  }
  updatedAt: string
  seq: number
}

export function appendEvent(events: AgentEvent[], event: AgentEvent) {
  if (events.some((existing) => existing.seq === event.seq)) {
    return events
  }
  return [...eventsWithoutCompletedTransient(events, event), event].sort((left, right) => left.seq - right.seq)
}

export function appendEvents(events: AgentEvent[], nextEvents: AgentEvent[]) {
  return nextEvents.reduce(appendEvent, events)
}

export function lastSeq(events: AgentEvent[]) {
  return events.reduce((max, event) => Math.max(max, event.seq), 0)
}

export function isTransientEvent(event: AgentEvent) {
  return event.transient === true || event.type.endsWith('.delta')
}

function eventsWithoutCompletedTransient(events: AgentEvent[], event: AgentEvent) {
  if (isTransientEvent(event)) {
    return events
  }

  const deltaType = deltaTypeCompletedBy(event.type)
  if (!deltaType) {
    return events
  }

  const identity = transientCompletionIdentity(event)
  let changed = false
  const next = events.filter((existing) => {
    if (!isTransientEvent(existing) || existing.type !== deltaType || existing.seq >= event.seq) {
      return true
    }
    if (identity && transientCompletionIdentity(existing) !== identity) {
      return true
    }
    changed = true
    return false
  })
  return changed ? next : events
}

function deltaTypeCompletedBy(eventType: string) {
  if (eventType === 'agent.message.completed') return 'agent.message.delta'
  if (eventType === 'agent.plan.completed') return 'agent.plan.delta'
  if (eventType === 'agent.thinking.completed') return 'agent.thinking.delta'
  if (eventType === 'tool.call.completed') return 'tool.call.delta'
  if (eventType === 'file.change.completed') return 'file.change.delta'
  return ''
}

function transientCompletionIdentity(event: AgentEvent) {
  if (event.type.startsWith('tool.call.')) {
    return transientIdentityString(event.payload, ['tool_call_id', 'item_id', 'id'])
  }
  if (event.type.startsWith('file.change.')) {
    return transientIdentityString(event.payload, ['item_id', 'file_change_id', 'path', 'file_path', 'id'])
  }
  if (event.type.startsWith('agent.message.')) {
    return transientIdentityString(event.payload, ['item_id', 'message_id', 'id'])
  }
  if (event.type.startsWith('agent.thinking.')) {
    return transientIdentityString(event.payload, ['item_id', 'message_id', 'id'])
  }
  if (event.type.startsWith('agent.plan.')) {
    return transientIdentityString(event.payload, ['item_id', 'plan_id', 'id'])
  }
  return ''
}

function transientIdentityString(payload: unknown, keys: string[]) {
  for (const key of keys) {
    const value = payloadString(payload, [key])
    if (value) {
      return value.startsWith('message:') ? value.slice('message:'.length) : value
    }
  }
  return ''
}

export function coalesceDisplayEvents(events: AgentEvent[]) {
  const displayEvents: DisplayEvent[] = []

  for (const event of events) {
    const previous = displayEvents[displayEvents.length - 1]
    if (event.type === 'agent.message.delta' && previous?.type === 'agent.message.delta') {
      previous.payload = {
        text: `${payloadText(previous.payload)}${payloadText(event.payload)}`,
      }
      previous.seq = event.seq
      previous.id = event.id
      previous.created_at = event.created_at
      continue
    }
    displayEvents.push({ ...event })
  }

  return displayEvents
}

export function groupEvents(events: AgentEvent[]) {
  const groups: EventGroup[] = []
  const toolGroupsByID = new Map<string, EventGroup>()
  const fileChangeGroupsByID = new Map<string, EventGroup>()

  for (const event of eventsWithLegacyClaudeToolCalls(sortedUniqueEvents(events))) {
    const previous = groups[groups.length - 1]
    const toolID = toolGroupID(event)
    const fileChangeID = fileChangeGroupID(event)

    if (
      event.type === 'agent.message.delta' &&
      previous?.kind === 'agent-message' &&
      previous.events[previous.events.length - 1]?.type === 'agent.message.delta' &&
      previous.events[previous.events.length - 1]?.session_id === event.session_id &&
      sameTransientMessage(previous.events[previous.events.length - 1], event)
    ) {
      appendToGroup(previous, event)
      continue
    }

    if (
      isPlanDeltaEvent(event) &&
      previous?.kind === 'plan' &&
      previous.events[previous.events.length - 1] &&
      isPlanDeltaEvent(previous.events[previous.events.length - 1])
    ) {
      appendToGroup(previous, event)
      continue
    }

    if (
      event.type === 'agent.thinking.delta' &&
      previous?.kind === 'thinking' &&
      previous.events[previous.events.length - 1]?.type === 'agent.thinking.delta'
    ) {
      appendToGroup(previous, event)
      continue
    }

    if (event.type === 'agent.log.delta' && previous?.kind === 'log') {
      appendToGroup(previous, event)
      continue
    }

    if (event.type.startsWith('tool.call')) {
      const toolGroup = toolID ? toolGroupsByID.get(toolID) : nearbyToolGroup(previous, event)
      if (toolGroup) {
        appendToGroup(toolGroup, event)
        continue
      }

      const group = newGroup(event)
      groups.push(group)
      if (toolID) {
        toolGroupsByID.set(toolID, group)
      }
      continue
    }

    if (event.type.startsWith('file.change')) {
      const fileChangeGroup = fileChangeID
        ? fileChangeGroupsByID.get(fileChangeID)
        : nearbyFileChangeGroup(previous, event)
      if (fileChangeGroup) {
        appendToGroup(fileChangeGroup, event)
        continue
      }

      const group = newGroup(event)
      groups.push(group)
      if (fileChangeID) {
        fileChangeGroupsByID.set(fileChangeID, group)
      }
      continue
    }

    groups.push(newGroup(event))
  }

  return groups
}

function sameTransientMessage(previous: AgentEvent | undefined, next: AgentEvent) {
  if (!previous) return false
  const previousID = transientCompletionIdentity(previous)
  const nextID = transientCompletionIdentity(next)
  return !previousID || !nextID || previousID === nextID
}

function eventsWithLegacyClaudeToolCalls(events: AgentEvent[]) {
  const canonicalToolIDs = new Set(
    events.flatMap((event) => (event.type.startsWith('tool.call') ? [toolGroupID(event)].filter(Boolean) : [])),
  )
  if (canonicalToolIDs.size === 0 && !events.some(isLegacyClaudeToolEvent)) {
    return events
  }

  const toolInputs = new Map<string, Record<string, unknown>>()
  const prepared: AgentEvent[] = []
  for (const event of events) {
    prepared.push(event)
    for (const toolEvent of legacyClaudeToolEvents(event, canonicalToolIDs, toolInputs)) {
      prepared.push(toolEvent)
    }
  }
  return prepared
}

export function buildChatTranscript(events: AgentEvent[]) {
  return buildChatTimeline(events, false).flatMap((item) => (item.kind === 'message' ? [item.message] : []))
}

export function buildChatTimeline(events: AgentEvent[], includeDebugEvents: boolean) {
  const items: ChatTimelineItem[] = []
  const messages: ChatTranscriptMessage[] = []
  let currentAssistant: ChatTranscriptMessage | null = null
  const assistantMessagesByItemID = new Map<string, ChatTranscriptMessage>()
  const openCodeMessageIDs = new Set<string>()

  for (const group of groupEvents(events)) {
    if (group.kind === 'action-break') {
      currentAssistant = null
      items.push({
        kind: 'action',
        id: `action-${group.id}`,
        startSeq: group.startSeq,
        endSeq: group.endSeq,
        action: chatActionFromGroup(group),
      })
      continue
    }

    if (group.kind === 'user-message') {
      const message = chatMessageFromGroup('user', group)
      messages.push(message)
      syncMessageTimelineItem(items, message)
      currentAssistant = null
      continue
    }

    if (group.kind === 'agent-message' || group.kind === 'plan') {
      currentAssistant = assistantMessageForGroup(messages, currentAssistant, assistantMessagesByItemID, group)
      mergeAssistantMessage(currentAssistant, group)
      if (group.events.some(isOpenCodeEvent)) {
        openCodeMessageIDs.add(currentAssistant.id)
      }
      syncMessageTimelineItem(items, currentAssistant)
      continue
    }

    if (group.kind === 'tool-call' || group.kind === 'file-change') {
      currentAssistant = ensureAssistantMessage(messages, currentAssistant, group)
      currentAssistant.tools.push(chatToolFromGroup(group))
      currentAssistant.streaming = group.status !== 'completed'
      updateMessageRange(currentAssistant, group)
      syncMessageTimelineItem(items, currentAssistant)
      continue
    }

    if (group.kind === 'error') {
      items.push({
        kind: 'error',
        id: `error-${group.id}`,
        startSeq: group.startSeq,
        endSeq: group.endSeq,
        error: chatRunErrorFromGroup(group),
      })
      currentAssistant = null
      continue
    }

    if (includeDebugEvents && isHiddenDebugGroup(group)) {
      items.push({
        kind: 'debug',
        id: `debug-${group.id}`,
        startSeq: group.startSeq,
        endSeq: group.endSeq,
        event: chatDebugEventFromGroup(group),
      })
    }
  }

  applyAssistantBlockDurations(messages, events)

  return items.filter((item) => {
    if (item.kind === 'debug' || item.kind === 'action' || item.kind === 'error') {
      return true
    }
    if (openCodeMessageIDs.has(item.message.id) && isOpenCodeCompactionSummary(item.message.text)) {
      return false
    }
    return (
      item.message.text.trim() ||
      item.message.tools.length > 0 ||
      item.message.attachments.length > 0 ||
      item.message.skills.length > 0
    )
  })
}

const openCodeCompactionHeadings = [
  '## Goal',
  '## Constraints & Preferences',
  '## Progress',
  '### Done',
  '### In Progress',
  '### Blocked',
  '## Key Decisions',
  '## Next Steps',
  '## Critical Context',
  '## Relevant Files',
]

function isOpenCodeEvent(event: AgentEvent) {
  return payloadString(event.payload, ['provider']).trim().toLowerCase() === 'opencode'
}

// OpenCode's ACP bridge currently emits generated compaction summaries as
// ordinary assistant text and omits the message's `summary`/`compaction` mode.
// Match the complete, fixed summary structure so it stays out of the chat
// transcript without hiding normal OpenCode responses that use one heading.
function isOpenCodeCompactionSummary(text: string) {
  const normalized = text.trimStart()
  if (!normalized.startsWith(openCodeCompactionHeadings[0])) {
    return false
  }

  let after = 0
  for (const heading of openCodeCompactionHeadings) {
    const index = normalized.indexOf(heading, after)
    if (index < after) {
      return false
    }
    after = index + heading.length
  }
  return true
}

export function pendingUserInputRequest(events: AgentEvent[]) {
  const requests = new Map<string, PendingUserInputRequest>()
  const answered = new Set<string>()
  let latestTerminalSeq = 0

  for (const event of sortedUniqueEvents(events)) {
    if (isTerminalEvent(event.type)) {
      latestTerminalSeq = event.seq
    }
    if (event.type === 'agent.input.requested') {
      const request = userInputRequestFromEvent(event)
      if (request) {
        requests.set(request.requestID, request)
      }
    }
    if (event.type === 'agent.input.answered' || event.type === 'agent.input.cancelled') {
      const requestID = payloadString(event.payload, ['request_id'])
      if (requestID) {
        answered.add(requestID)
      }
    }
    if (event.type === 'agent.input.submitted' || event.type === 'agent.input.failed') {
      const request = requests.get(payloadString(event.payload, ['request_id']))
      if (request) {
        request.submitting = event.type === 'agent.input.submitted'
        request.deliveryError = event.type === 'agent.input.failed'
          ? payloadString(event.payload, ['error']) || 'Answer delivery was not confirmed. It will not be resent automatically.'
          : undefined
      }
    }
  }

  return (
    [...requests.values()]
      .filter((request) => request.seq > latestTerminalSeq && !answered.has(request.requestID))
      // Blocking requests need attention first. Keep the oldest async question
      // stable while more questions arrive, without resetting an in-progress reply.
      .sort((left, right) => Number(Boolean(left.deliveryError)) - Number(Boolean(right.deliveryError)) || Number(left.delivery === 'async') - Number(right.delivery === 'async') || left.seq - right.seq)[0] ?? null
  )
}

// Run IDs are server-owned and appear on every run-linked agent event, so a
// bounded tail still knows its target. Ignore late user-delivery acknowledgments.
export function activeRunID(events: AgentEvent[]): string {
  for (const event of sortedUniqueEvents(events).reverse()) {
    if (event.type.startsWith('user.')) continue
    if (isTerminalEvent(event.type)) return ''
    const id = payloadString(event.payload, ['run_id'])
    if (id) return id
  }
  return ''
}

export function pendingPermissionRequests(events: AgentEvent[]): PendingPermissionRequest[] {
  const requests = new Map<string, PendingPermissionRequest>()
  const resolved = new Set<string>()
  let latestTerminalSeq = 0
  for (const event of sortedUniqueEvents(events)) {
    if (isTerminalEvent(event.type)) latestTerminalSeq = event.seq
    if (event.type === 'agent.permission.requested' && isRecord(event.payload)) {
      const requestID = payloadString(event.payload, ['request_id'])
      const options = Array.isArray(event.payload.options)
        ? event.payload.options.filter(isRecord).flatMap((option) => {
            const id = payloadString(option, ['id'])
            const decision = payloadString(option, ['decision']) as 'allow' | 'deny' | 'cancel'
            if (!id || !['allow', 'deny', 'cancel'].includes(decision)) return []
            return [{ id, label: payloadString(option, ['label']) || id, description: payloadString(option, ['description']), decision, scope: payloadString(option, ['scope']) as 'once' | 'session' }]
          })
        : []
      if (requestID && options.length > 0) {
        requests.set(requestID, {
          request_id: requestID, provider: payloadString(event.payload, ['provider']), provider_event_type: payloadString(event.payload, ['provider_event_type']),
          kind: payloadString(event.payload, ['kind']), title: payloadString(event.payload, ['title']) || 'Permission required',
          description: payloadString(event.payload, ['description']), reason: payloadString(event.payload, ['reason']), command: payloadString(event.payload, ['command']),
          cwd: payloadString(event.payload, ['cwd']), tool_name: payloadString(event.payload, ['tool_name']), tool_input: event.payload.tool_input,
          paths: Array.isArray(event.payload.paths) ? event.payload.paths.filter((path): path is string => typeof path === 'string') : [],
          diff: payloadString(event.payload, ['diff']), requested_grants: event.payload.requested_grants, options, createdAt: event.created_at, seq: event.seq,
        })
      }
    }
    if (event.type === 'agent.permission.resolved' || event.type === 'agent.permission.cancelled') {
      const requestID = payloadString(event.payload, ['request_id']); if (requestID) resolved.add(requestID)
    }
  }
  return [...requests.values()].filter((request) => request.seq > latestTerminalSeq && !resolved.has(request.request_id)).sort((a, b) => a.seq - b.seq)
}

export function latestTerminalEvent(events: AgentEvent[]) {
  let latest: AgentEvent | null = null
  for (const event of sortedUniqueEvents(events)) {
    if (isTerminalEvent(event.type)) {
      latest = event
    }
  }
  return latest
}

export function latestTokenUsage(events: AgentEvent[]) {
  let latest: TokenUsageSummary | null = null
  const claudeUsage = claudeTokenUsageReducer()
  for (const event of sortedUniqueEvents(events)) {
    const summary = claudeUsage(event) ?? tokenUsageFromEvent(event)
    if (summary) {
      latest = summary
    }
  }
  return latest
}

export function activeThinking(events: AgentEvent[]) {
  let activeGenericThinking = false
  const activeThinkingItems = new Set<string>()

  for (const event of sortedUniqueEvents(events)) {
    if (event.type === 'agent.status.started') {
      activeGenericThinking = true
      continue
    }

    if (event.type === 'agent.thinking.started' || event.type === 'agent.thinking.delta') {
      const itemID = payloadItemID(event.payload)
      if (itemID) {
        activeThinkingItems.add(itemID)
      } else {
        activeGenericThinking = true
      }
      continue
    }

    if (event.type === 'agent.thinking.completed') {
      const itemID = payloadItemID(event.payload)
      if (itemID) {
        activeThinkingItems.delete(itemID)
      }
      activeGenericThinking = false
      continue
    }

    if (clearsActiveThinking(event)) {
      activeGenericThinking = false
      activeThinkingItems.clear()
    }
  }

  return activeGenericThinking || activeThinkingItems.size > 0
}

export function activeRunActivity(events: AgentEvent[]): ActiveRunActivity | null {
  let activity: ActiveRunActivity | null = null

  for (const event of sortedUniqueEvents(events)) {
    if (event.type === 'agent.run.started') {
      activity = {
        runStartedAt: event.created_at,
        lastVisibleActivityAt: event.created_at,
        lastVisibleActivitySeq: event.seq,
      }
      continue
    }

    if (!activity) {
      continue
    }

    if (isTerminalEvent(event.type)) {
      activity = null
      continue
    }

    if (isVisibleRunActivity(event)) {
      activity = {
        ...activity,
        lastVisibleActivityAt: event.created_at,
        lastVisibleActivitySeq: event.seq,
      }
    }
  }

  return activity
}

export function activeStreamingResponse(events: AgentEvent[]) {
  let runActive = false
  let activeGenericStream = false
  const activeStreamItems = new Set<string>()

  for (const event of sortedUniqueEvents(events)) {
    if (event.type === 'agent.run.started') {
      runActive = true
      activeGenericStream = false
      activeStreamItems.clear()
      continue
    }

    if (!runActive) {
      continue
    }

    if (isTerminalEvent(event.type)) {
      runActive = false
      activeGenericStream = false
      activeStreamItems.clear()
      continue
    }

    if (event.type === 'agent.message.delta' || isPlanDeltaEvent(event)) {
      const itemID = streamingResponseItemID(event)
      if (itemID) {
        activeStreamItems.add(itemID)
      } else {
        activeGenericStream = true
      }
      continue
    }

    if (event.type === 'agent.message.completed' || event.type === 'agent.plan.completed' || legacyProviderPlanKind(event) === 'completed') {
      const itemID = streamingResponseItemID(event)
      if (itemID) {
        activeStreamItems.delete(itemID)
      } else {
        activeGenericStream = false
      }
    }
  }

  return activeGenericStream || activeStreamItems.size > 0
}

export function activeToolActivity(events: AgentEvent[]) {
  let runActive = false
  let activeGenericTool = false
  const activeTools = new Set<string>()

  for (const event of sortedUniqueEvents(events)) {
    if (event.type === 'agent.run.started') {
      runActive = true
      activeGenericTool = false
      activeTools.clear()
      continue
    }

    if (!runActive) {
      continue
    }

    if (isTerminalEvent(event.type)) {
      runActive = false
      activeGenericTool = false
      activeTools.clear()
      continue
    }

    if (!event.type.startsWith('tool.call') && !event.type.startsWith('file.change')) {
      continue
    }

    const activityID = activeToolActivityID(event)
    if (event.type.endsWith('.completed')) {
      if (activityID) {
        activeTools.delete(activityID)
      } else {
        activeGenericTool = false
      }
      continue
    }

    if (activityID) {
      activeTools.add(activityID)
    } else {
      activeGenericTool = true
    }
  }

  return activeGenericTool || activeTools.size > 0
}

export function eventLabel(eventOrType: AgentEvent | string) {
  const type = typeof eventOrType === 'string' ? eventOrType : eventOrType.type
  const providerEventType =
    typeof eventOrType === 'string' ? '' : payloadString(eventOrType.payload, ['provider_event_type'])
  if (typeof eventOrType !== 'string' && isPlanEvent(eventOrType)) return 'Plan'
  if (typeof eventOrType !== 'string' && isActionBreakEvent(eventOrType)) return actionBreakLabel(eventOrType)
  if (providerEventType && type.startsWith('provider.')) return providerEventType
  if (type === 'session.status.updated') return 'Session status'
  if (type.startsWith('session.action') || type.startsWith('user.action')) return 'Session action'
  if (type === 'user.message.steer.submitted') return 'Sending now'
  if (type === 'user.message.steer.failed') return 'Send now not confirmed'
  if (type.startsWith('user.message')) return 'User message'
  if (type.startsWith('agent.message')) return 'Agent message'
  if (type.startsWith('agent.plan')) return 'Plan'
  if (type.startsWith('agent.thinking')) return 'Thinking'
  if (type === 'agent.input.requested') return 'Question'
  if (type === 'agent.input.submitted') return 'Sending answer'
  if (type === 'agent.input.answered') return 'Answer delivered'
  if (type === 'agent.input.cancelled') return 'Question withdrawn'
  if (type === 'agent.input.failed') return 'Answer not confirmed'
  if (type.startsWith('tool.call')) return 'Tool call'
  if (type.startsWith('file.change')) return 'File change'
  if (type === 'agent.log.delta') return 'Log'
  if (type.includes('failed') || type.includes('parse_error')) return 'Error'
  if (type === 'agent.run.completed') return 'Completed'
  if (type === 'agent.run.cancelled') return 'Cancelled'
  return type
}

export function payloadText(payload: unknown) {
  if (isRecord(payload)) {
    const value =
      payload.text ??
      payload.delta ??
      payload.output ??
      payload.aggregated_output ??
      payload.command ??
      payload.error ??
      payload.summary ??
      payload.message
    if (typeof value === 'string') {
      return value
    }

    const resultText = nestedToolResultText(payload.result)
    if (resultText) {
      return resultText
    }

    if (isRecord(payload.arguments)) {
      const command = payloadString(payload.arguments, ['command'])
      if (command) {
        return command
      }
    }
  }
  return ''
}

export function payloadError(payload: unknown) {
  if (!isRecord(payload)) {
    return ''
  }
  const value = payload.error ?? payload.message
  if (typeof value === 'string') {
    return value
  }
  if (!isRecord(value)) {
    return ''
  }
  return payloadString(value, ['message', 'error', 'details'])
}

export function shouldRefreshWorkspaceFilesForEvent(event: AgentEvent) {
  if (event.type === 'file.change.completed') {
    return true
  }
  if (event.type !== 'tool.call.completed') {
    return false
  }
  const command = payloadString(event.payload, ['command'])
  return Boolean(command && isWorkspaceMutatingGitCommand(command))
}

function isWorkspaceMutatingGitCommand(command: string) {
  const cleaned = cleanShellCommand(command)
  return workspaceMutatingGitCommandPattern.test(cleaned)
}

const workspaceMutatingGitCommandPattern =
  /(?:^|[;&|(\n])\s*(?:sudo\s+|command\s+|env\s+(?:\S+=\S+\s+)*)?(?:[./\w-]+\/)?git(?:\s+-(?:C|c|[A-Za-z-]+)(?:[=\s]+(?:"[^"]*"|'[^']*'|[^\s;&|()]+))?)*\s+(?:add|am|apply|checkout|cherry-pick|clean|clone|commit|fetch|init|merge|mv|pull|push|rebase|reset|restore|revert|rm|stash|submodule|switch|tag|worktree)\b/i

function groupText(event: AgentEvent, kind: EventGroupKind) {
  if (kind === 'plan') {
    return planText(event)
  }
  return payloadText(event.payload)
}

function planText(event: AgentEvent) {
  const direct = payloadText(event.payload)
  if (direct) {
    return direct
  }
  const raw = legacyProviderRaw(event)
  if (!raw) {
    return ''
  }
  const delta = payloadLiteralString(raw, ['delta'])
  if (delta) {
    return delta
  }
  return isRecord(raw.item) ? payloadLiteralString(raw.item, ['text']) : ''
}

function isPlanEvent(event: AgentEvent) {
  return event.type.startsWith('agent.plan') || isLegacyProviderPlanEvent(event)
}

function isPlanDeltaEvent(event: AgentEvent) {
  return event.type === 'agent.plan.delta' || legacyProviderPlanKind(event) === 'delta'
}

function isLegacyProviderPlanEvent(event: AgentEvent) {
  return legacyProviderPlanKind(event) !== ''
}

function legacyProviderPlanKind(event: AgentEvent) {
  if (event.type !== 'provider.codex.event') {
    return ''
  }
  const providerEventType = payloadString(event.payload, ['provider_event_type'])
  if (providerEventType === 'item/plan/delta') {
    return 'delta'
  }
  if (providerEventType !== 'item/completed') {
    return ''
  }
  const raw = legacyProviderRaw(event)
  if (!raw || !isRecord(raw.item)) {
    return ''
  }
  return payloadString(raw.item, ['type']) === 'plan' ? 'completed' : ''
}

function legacyProviderRaw(event: AgentEvent) {
  if (!isRecord(event.payload) || !isRecord(event.payload.raw)) {
    return null
  }
  return event.payload.raw
}

function planItemID(event: AgentEvent | undefined) {
  if (!event) {
    return ''
  }
  const direct = payloadItemID(event.payload)
  if (direct) {
    return direct
  }
  const raw = legacyProviderRaw(event)
  if (!raw) {
    return ''
  }
  const itemID = payloadString(raw, ['itemId'])
  if (itemID) {
    return itemID
  }
  return isRecord(raw.item) ? payloadString(raw.item, ['id']) : ''
}

export function statusFromEvent(eventOrType: AgentEvent | string): SessionStatus | null {
  const type = typeof eventOrType === 'string' ? eventOrType : eventOrType.type
  if (type === 'session.status.updated' && typeof eventOrType !== 'string') {
    return payloadSessionStatus(eventOrType.payload)
  }

  switch (type) {
    case 'agent.run.started':
      return 'running'
    case 'agent.run.completed':
      return 'idle'
    case 'agent.run.failed':
      return 'failed'
    case 'agent.run.cancelled':
      return 'idle'
    default:
      return null
  }
}

function payloadSessionStatus(payload: unknown) {
  if (!isRecord(payload)) {
    return null
  }
  const status = payload.status
  return isSessionStatus(status) ? status : null
}

function isSessionStatus(value: unknown): value is SessionStatus {
  return value === 'idle' || value === 'running' || value === 'failed'
}

export function isTerminalEvent(type: string) {
  return type === 'agent.run.completed' || type === 'agent.run.failed' || type === 'agent.run.cancelled'
}

export function isErrorEvent(type: string, status: string) {
  if (type === 'session.status.updated') {
    return false
  }
  return status === 'failed' || type.endsWith('.parse_error') || type === 'agent.run.failed'
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function sortedUniqueEvents(events: AgentEvent[]) {
  const bySeq = new Map<number, AgentEvent>()
  for (const event of events) {
    if (!bySeq.has(event.seq)) {
      bySeq.set(event.seq, event)
    }
  }
  return [...bySeq.values()].sort((left, right) => left.seq - right.seq)
}

function isLegacyClaudeToolEvent(event: AgentEvent) {
  if (event.type !== 'provider.claude.event' && event.type !== 'agent.message.completed') {
    return false
  }
  return legacyClaudeToolStarts(event).length > 0 || legacyClaudeToolInputs(event).length > 0 || legacyClaudeToolResults(event).length > 0
}

function legacyClaudeToolEvents(
  event: AgentEvent,
  canonicalToolIDs: Set<string>,
  toolInputs: Map<string, Record<string, unknown>>,
) {
  const synthetic: AgentEvent[] = []
  for (const tool of legacyClaudeToolStarts(event)) {
    if (canonicalToolIDs.has(tool.id)) {
      continue
    }
    if (tool.rawInput) {
      toolInputs.set(tool.id, tool.rawInput)
    }
    synthetic.push(legacyClaudeToolEvent(event, 'tool.call.started', 'started', tool))
  }
  for (const tool of legacyClaudeToolInputs(event)) {
    if (canonicalToolIDs.has(tool.id)) {
      continue
    }
    if (tool.rawInput) {
      toolInputs.set(tool.id, tool.rawInput)
    }
    synthetic.push(legacyClaudeToolEvent(event, 'tool.call.delta', 'delta', tool))
  }
  for (const result of legacyClaudeToolResults(event)) {
    if (canonicalToolIDs.has(result.id)) {
      continue
    }
    const rawInput = toolInputs.get(result.id)
    synthetic.push(
      legacyClaudeToolEvent(event, 'tool.call.completed', 'completed', {
        id: result.id,
        name: result.name,
        rawInput,
        output: result.output,
        rawOutput: result.rawOutput,
        isError: result.isError,
      }),
    )
  }
  return synthetic
}

type LegacyClaudeTool = {
  id: string
  name?: string
  rawInput?: Record<string, unknown>
  output?: string
  rawOutput?: unknown
  isError?: boolean
}

function legacyClaudeToolEvent(
  source: AgentEvent,
  type: 'tool.call.started' | 'tool.call.delta' | 'tool.call.completed',
  status: 'started' | 'delta' | 'completed',
  tool: LegacyClaudeTool,
): AgentEvent {
  const payload: Record<string, unknown> = {
    provider: 'claude',
    provider_event_type: type === 'tool.call.completed' ? 'tool_result' : 'tool_use',
    tool_call_id: tool.id,
    item_id: tool.id,
  }
  const providerSessionID = payloadString(source.payload, ['provider_session_id'])
  if (providerSessionID) {
    payload.provider_session_id = providerSessionID
  }
  if (tool.name) {
    payload.name = tool.name
    payload.tool = tool.name
    payload.kind = tool.name
    payload.title = tool.name
  }
  if (tool.rawInput) {
    payload.raw_input = tool.rawInput
    const command = payloadString(tool.rawInput, ['command'])
    if (command) {
      payload.command = command
    }
    const description = payloadString(tool.rawInput, ['description'])
    if (description) {
      payload.description = description
    }
    const path = payloadString(tool.rawInput, ['file_path', 'filePath', 'path'])
    if (path) {
      payload.path = path
    }
  }
  if (tool.output) {
    payload.output = tool.output
    payload.aggregated_output = tool.output
  }
  if (tool.rawOutput !== undefined) {
    payload.raw_output = tool.rawOutput
  }
  if (tool.isError) {
    payload.is_error = true
    if (tool.output) {
      payload.error = tool.output
    }
  }

  return {
    ...source,
    id: `${source.id}-${type}-${tool.id}`,
    type,
    role: 'assistant',
    status,
    payload,
  }
}

function legacyClaudeToolStarts(event: AgentEvent): LegacyClaudeTool[] {
  if (event.type !== 'provider.claude.event' || !isRecord(event.payload)) {
    return []
  }
  const rawEvent = isRecord(event.payload.raw_event) ? event.payload.raw_event : null
  const block = rawEvent && isRecord(rawEvent.content_block) ? rawEvent.content_block : null
  if (!block || payloadString(block, ['type']) !== 'tool_use') {
    return []
  }
  const id = payloadString(block, ['id'])
  if (!id) {
    return []
  }
  return [
    {
      id,
      name: payloadString(block, ['name']),
      rawInput: isRecord(block.input) ? block.input : undefined,
    },
  ]
}

function legacyClaudeToolInputs(event: AgentEvent): LegacyClaudeTool[] {
  if (event.type !== 'agent.message.completed' || !isRecord(event.payload) || event.payload.provider !== 'claude') {
    return []
  }
  const rawMessage = isRecord(event.payload.raw_message) ? event.payload.raw_message : null
  const content = Array.isArray(rawMessage?.content) ? rawMessage.content : []
  return content.flatMap((item) => {
    if (!isRecord(item) || payloadString(item, ['type']) !== 'tool_use') {
      return []
    }
    const id = payloadString(item, ['id'])
    if (!id) {
      return []
    }
    return [
      {
        id,
        name: payloadString(item, ['name']),
        rawInput: isRecord(item.input) ? item.input : undefined,
      },
    ]
  })
}

function legacyClaudeToolResults(event: AgentEvent): LegacyClaudeTool[] {
  if (event.type !== 'provider.claude.event' || !isRecord(event.payload) || event.payload.provider !== 'claude') {
    return []
  }
  const raw = isRecord(event.payload.raw) ? event.payload.raw : null
  const message = raw && isRecord(raw.message) ? raw.message : null
  const content = Array.isArray(message?.content) ? message.content : []
  const rawOutput = raw?.tool_use_result
  return content.flatMap((item) => {
    if (!isRecord(item) || payloadString(item, ['type']) !== 'tool_result') {
      return []
    }
    const id = payloadString(item, ['tool_use_id'])
    if (!id) {
      return []
    }
    return [
      {
        id,
        output: payloadString(item, ['content']) || legacyClaudeToolOutput(rawOutput),
        rawOutput,
        isError: item.is_error === true,
      },
    ]
  })
}

function legacyClaudeToolOutput(value: unknown) {
  if (typeof value === 'string') {
    return value
  }
  if (!isRecord(value)) {
    return ''
  }
  const stdout = payloadString(value, ['stdout'])
  const stderr = payloadString(value, ['stderr'])
  if (stdout && stderr) {
    return `${stdout}\n${stderr}`
  }
  return stdout || stderr
}

function newGroup(event: AgentEvent): EventGroup {
  const kind = groupKind(event)
  const group: EventGroup = {
    id: groupID(event),
    kind,
    label: groupLabel(event, kind),
    status: event.status,
    startSeq: event.seq,
    endSeq: event.seq,
    events: [event],
    text: groupText(event, kind),
    error: payloadError(event.payload),
    paths: payloadPaths(event.payload),
    defaultOpen: defaultOpen(kind, event),
    terminal: isTerminalEvent(event.type),
  }
  return group
}

function appendToGroup(group: EventGroup, event: AgentEvent) {
  group.events.push(event)
  group.endSeq = event.seq
  group.status = event.status
  group.text = joinGroupText(group.text, groupText(event, group.kind))
  group.error = group.error || payloadError(event.payload)
  group.paths = uniqueStrings([...group.paths, ...payloadPaths(event.payload)])
  if (group.kind === 'tool-call') {
    group.label = toolLabelFromEvents(group.events)
  }
  if (group.kind === 'file-change') {
    group.label = fileChangeLabel(group)
  }
  group.defaultOpen =
    group.events.some((item) => isErrorEvent(item.type, item.status)) || defaultOpen(group.kind, event)
  group.terminal = group.terminal || isTerminalEvent(event.type)
}

function groupKind(event: AgentEvent): EventGroupKind {
  if (isActionBreakEvent(event)) return 'action-break'
  if (event.type === 'user.message.completed') return 'user-message'
  if (event.type === 'agent.input.requested' && payloadString(event.payload, ['delivery']) === 'async') return 'agent-message'
  if (event.type === 'agent.input.answered' && payloadString(event.payload, ['delivery']) === 'async') return 'user-message'
  if (isClaudeToolOnlyAssistantEvent(event)) return 'unknown'
  if (event.type.startsWith('agent.message')) return 'agent-message'
  if (isPlanEvent(event)) return 'plan'
  if (event.type.startsWith('agent.thinking')) return 'thinking'
  if (event.type.startsWith('tool.call')) return 'tool-call'
  if (event.type.startsWith('file.change')) return 'file-change'
  if (isErrorEvent(event.type, event.status)) return 'error'
  if (event.type === 'agent.log.delta') return 'log'
  if (isTerminalEvent(event.type)) return 'terminal'
  return 'unknown'
}

function isClaudeToolOnlyAssistantEvent(event: AgentEvent) {
  if (event.type !== 'agent.message.completed' || !isRecord(event.payload) || event.payload.provider !== 'claude') {
    return false
  }
  if (payloadText(event.payload)) {
    return false
  }
  const rawMessage = isRecord(event.payload.raw_message) ? event.payload.raw_message : null
  const content = Array.isArray(rawMessage?.content) ? rawMessage.content : []
  return content.length > 0 && content.every((item) => isRecord(item) && payloadString(item, ['type']) === 'tool_use')
}

function groupLabel(event: AgentEvent, kind: EventGroupKind) {
  if (kind === 'tool-call') {
    return toolLabelFromEvents([event])
  }
  if (kind === 'file-change') {
    return fileChangeLabel({
      id: `${kind}-${event.seq}`,
      kind,
      label: eventLabel(event),
      status: event.status,
      startSeq: event.seq,
      endSeq: event.seq,
      events: [event],
      text: payloadText(event.payload),
      error: payloadError(event.payload),
      paths: payloadPaths(event.payload),
      defaultOpen: defaultOpen(kind, event),
      terminal: isTerminalEvent(event.type),
    })
  }
  return eventLabel(event)
}

function groupID(event: AgentEvent) {
  if (event.type.startsWith('tool.call')) {
    const toolID = toolGroupID(event)
    if (toolID) return `tool-${toolID}`
  }
  if (event.type.startsWith('file.change')) {
    const fileChangeID = fileChangeGroupID(event)
    if (fileChangeID) return `file-change-${fileChangeID}`
  }
  if (isPlanEvent(event)) {
    const planID = planItemID(event)
    if (planID) return `plan-${planID}`
  }
  return `${groupKind(event)}-${event.seq}`
}

function toolGroupID(event: AgentEvent) {
  if (!event.type.startsWith('tool.call')) {
    return ''
  }
  return payloadString(event.payload, ['tool_call_id', 'call_id', 'item_id', 'process_id', 'tool_id', 'id'])
}

function fileChangeGroupID(event: AgentEvent) {
  if (!event.type.startsWith('file.change')) {
    return ''
  }
  return payloadString(event.payload, ['file_change_id', 'change_id', 'item_id', 'tool_call_id', 'call_id', 'id'])
}

function payloadItemID(payload: unknown) {
  return payloadString(payload, ['item_id', 'itemId', 'id'])
}

function streamingResponseItemID(event: AgentEvent) {
  return payloadString(event.payload, ['item_id', 'itemId', 'message_id', 'messageId', 'id'])
}

function activeToolActivityID(event: AgentEvent) {
  if (event.type.startsWith('tool.call')) {
    return toolGroupID(event)
  }
  if (event.type.startsWith('file.change')) {
    return fileChangeGroupID(event)
  }
  return ''
}

function clearsActiveThinking(event: AgentEvent) {
  return (
    event.type.startsWith('agent.message') ||
    event.type.startsWith('agent.plan') ||
    isLegacyProviderPlanEvent(event) ||
    event.type.startsWith('tool.call') ||
    event.type.startsWith('file.change') ||
    (event.type === 'agent.input.requested' && payloadString(event.payload, ['delivery']) !== 'async') ||
    isTerminalEvent(event.type)
  )
}

function isVisibleRunActivity(event: AgentEvent) {
  return (
    event.type === 'agent.status.started' ||
    event.type.startsWith('agent.message') ||
    event.type.startsWith('agent.plan') ||
    isLegacyProviderPlanEvent(event) ||
    event.type.startsWith('agent.thinking') ||
    event.type === 'agent.log.delta' ||
    event.type === 'agent.input.requested' ||
    event.type.startsWith('tool.call') ||
    event.type.startsWith('file.change')
  )
}

function nearbyToolGroup(previous: EventGroup | undefined, event: AgentEvent) {
  if (!previous || previous.kind !== 'tool-call') {
    return null
  }
  if (event.seq - previous.endSeq > 2) {
    return null
  }
  return previous
}

function nearbyFileChangeGroup(previous: EventGroup | undefined, event: AgentEvent) {
  if (!previous || previous.kind !== 'file-change') {
    return null
  }
  if (event.seq - previous.endSeq > 2) {
    return null
  }
  return previous
}

function chatMessageFromGroup(role: ChatTranscriptMessage['role'], group: EventGroup): ChatTranscriptMessage {
  return {
    id: `chat-${role}-${group.startSeq}`,
    role,
    label: messageLabel(role, group.kind),
    variant: messageVariant(role, group.kind),
    text: group.text,
    attachments: chatAttachmentsFromGroup(group),
    skills: chatSkillsFromGroup(group),
    status: group.status,
    createdAt: group.events[0]?.created_at ?? '',
    completedAt: latestGroupTimestamp(group),
    durationMs: null,
    tools: [],
    streaming: group.status === 'delta' || group.status === 'started',
    startSeq: group.startSeq,
    endSeq: group.endSeq,
  }
}

function chatActionFromGroup(group: EventGroup): ChatActionBreak {
  const event = group.events[0]
  const action = event ? payloadString(event.payload, ['action']) : ''
  return {
    id: `action-${group.startSeq}`,
    action,
    label: event ? actionBreakLabel(event) : group.label,
    detail: event ? actionBreakDetail(event) : '',
    createdAt: event?.created_at ?? '',
    startSeq: group.startSeq,
    endSeq: group.endSeq,
  }
}

function actionBreakDetail(event: AgentEvent) {
  if (payloadString(event.payload, ['action']).trim().toLowerCase() !== 'workspace_changed') {
    return ''
  }
  const previousPath = payloadString(event.payload, ['previous_workspace_path'])
  const workspacePath = payloadString(event.payload, ['workspace_path'])
  return previousPath && workspacePath ? `${previousPath} -> ${workspacePath}` : workspacePath
}

function isActionBreakEvent(event: AgentEvent) {
  return event.type === 'session.action.completed' || event.type === 'user.action.completed'
}

function actionBreakLabel(event: AgentEvent) {
  const action = payloadString(event.payload, ['action']).trim().toLowerCase()
  switch (action) {
    case 'clear':
      return 'CONVERSATION CLEARED'
    case 'compact':
      return 'CONVERSATION COMPACTED'
    default:
      return payloadString(event.payload, ['label']) || 'SESSION ACTION'
  }
}

function messageLabel(role: ChatTranscriptMessage['role'], kind: EventGroupKind) {
  if (role === 'user') {
    return 'You'
  }
  if (kind === 'plan') {
    return 'Plan'
  }
  return 'Assistant'
}

function messageVariant(role: ChatTranscriptMessage['role'], kind: EventGroupKind): ChatTranscriptMessage['variant'] {
  return role === 'assistant' && kind === 'plan' ? 'plan' : 'default'
}

function chatAttachmentsFromGroup(group: EventGroup): ChatTranscriptAttachment[] {
  if (group.kind !== 'user-message') {
    return []
  }
  const event = group.events[0]
  const payload = event?.payload
  if (!isRecord(payload) || !Array.isArray(payload.attachments)) {
    return []
  }
  return payload.attachments.flatMap((attachment, index): ChatTranscriptAttachment[] => {
    if (!isRecord(attachment)) {
      return []
    }
    const name = typeof attachment.name === 'string' ? attachment.name : 'image'
    const mediaType = typeof attachment.media_type === 'string' ? attachment.media_type : ''
    const dataURL = typeof attachment.data_url === 'string' ? attachment.data_url : ''
    const sizeBytes = typeof attachment.size_bytes === 'number' ? attachment.size_bytes : 0
    const sourceURL = event
      ? `/api/sessions/${encodeURIComponent(event.session_id)}/events/${event.seq}/attachments/${index}`
      : dataURL
    if (!mediaType.startsWith('image/') || (!dataURL && !sourceURL)) {
      return []
    }
    return [{ name, mediaType, dataURL, sourceURL, sizeBytes }]
  })
}

function chatSkillsFromGroup(group: EventGroup): SkillReference[] {
  if (group.kind !== 'user-message') return []
  const payload = group.events[0]?.payload
  if (!isRecord(payload) || !Array.isArray(payload.skills)) return []
  return payload.skills.flatMap((skill): SkillReference[] => {
    if (!isRecord(skill) || typeof skill.name !== 'string' || typeof skill.path !== 'string') return []
    const name = skill.name.trim()
    const path = skill.path.trim()
    return name && path ? [{ name, path }] : []
  })
}

function ensureAssistantMessage(
  messages: ChatTranscriptMessage[],
  currentAssistant: ChatTranscriptMessage | null,
  group: EventGroup,
) {
  if (currentAssistant) {
    return currentAssistant
  }

  const message = chatMessageFromGroup('assistant', group)
  message.text = ''
  messages.push(message)
  return message
}

function assistantMessageForGroup(
  messages: ChatTranscriptMessage[],
  currentAssistant: ChatTranscriptMessage | null,
  assistantMessagesByItemID: Map<string, ChatTranscriptMessage>,
  group: EventGroup,
) {
  const itemID = chatGroupItemID(group)
  if (!itemID) {
    return ensureAssistantMessage(messages, currentAssistant, group)
  }

  const existing = assistantMessagesByItemID.get(itemID)
  if (existing) {
    return existing
  }

  if (currentAssistant && isSameAssistantTextStream(currentAssistant.text, group.text)) {
    assistantMessagesByItemID.set(itemID, currentAssistant)
    return currentAssistant
  }

  if (currentAssistant && !currentAssistant.text.trim() && currentAssistant.tools.length > 0) {
    assistantMessagesByItemID.set(itemID, currentAssistant)
    return currentAssistant
  }

  const message = chatMessageFromGroup('assistant', group)
  message.text = ''
  messages.push(message)
  assistantMessagesByItemID.set(itemID, message)
  return message
}

function mergeAssistantMessage(message: ChatTranscriptMessage, group: EventGroup) {
  if (group.kind === 'plan') {
    message.label = 'Plan'
    message.variant = 'plan'
  }
  message.text = mergeChatText(message.text, group.text)
  message.status = group.status
  message.streaming =
    group.events.some((event) => event.type === 'agent.message.delta' || isPlanDeltaEvent(event)) &&
    group.status !== 'completed'
  updateMessageRange(message, group)
}

function chatToolFromGroup(group: EventGroup): ChatTranscriptTool {
  const text = chatToolText(group)
  const fullOutputEvent = group.events.find((event) => {
    if (!isRecord(event.payload) || !isRecord(event.payload._gorchestra_tool_output)) return false
    return event.payload._gorchestra_tool_output.truncated === true
  })
  return {
    id: group.id,
    kind: group.kind as ChatTranscriptTool['kind'],
    label: cleanToolLabel(group.label),
    status: group.status,
    text,
    error: group.error || (group.status === 'failed' ? text : ''),
    content: chatToolContentFromGroup(group),
    fullOutputURL: fullOutputEvent
      ? `/api/sessions/${encodeURIComponent(fullOutputEvent.session_id)}/events/${fullOutputEvent.seq}/tool-output`
      : '',
    paths: group.paths,
    startSeq: group.startSeq,
    endSeq: group.endSeq,
  }
}

function chatToolText(group: EventGroup) {
  if (group.kind === 'file-change') {
    const diffText = fileChangeDiffText(group)
    if (diffText) {
      return diffText
    }
  }

  const lines: string[] = []
  for (const event of group.events) {
    for (const line of toolTextLines(event.payload)) {
      if (!line || lines[lines.length - 1] === line) {
        continue
      }
      lines.push(line)
    }
  }
  if (lines.length > 0) {
    return lines.join('\n')
  }
  return group.paths.join('\n')
}

function chatToolContentFromGroup(group: EventGroup): ChatTranscriptToolContent[] {
  const content: ChatTranscriptToolContent[] = []
  const seen = new Set<string>()

  for (const event of group.events) {
    if (!isRecord(event.payload) || !isRecord(event.payload.result) || !Array.isArray(event.payload.result.content)) {
      continue
    }
    event.payload.result.content.forEach((rawBlock, contentIndex) => {
      const block = toolContentBlock(rawBlock, event, contentIndex)
      if (!block) {
        return
      }
      const key = [block.kind, block.sourceURL, block.uri, block.name].join('\u0000')
      if (seen.has(key)) {
        return
      }
      seen.add(key)
      content.push(block)
    })
  }

  return content
}

function toolContentBlock(
  value: unknown,
  event: AgentEvent,
  contentIndex: number,
): ChatTranscriptToolContent | null {
  if (!isRecord(value)) {
    return null
  }

  const type = payloadString(value, ['type'])
  if (type === 'image' || type === 'audio') {
    const mediaType = payloadString(value, ['mimeType', 'mime_type'])
    const hasData = typeof value.data === 'string'
    if (!mediaType || !hasData) {
      return null
    }
    return {
      kind: type,
      name: payloadString(value, ['name']) || `${type} result`,
      mediaType,
      sourceURL: toolContentSourceURL(event, contentIndex),
      uri: '',
      description: '',
    }
  }

  if (type === 'resource') {
    const resource = isRecord(value.resource) ? value.resource : null
    if (!resource) {
      return null
    }
    const uri = payloadString(resource, ['uri'])
    const mediaType = payloadString(resource, ['mimeType', 'mime_type'])
    const hasBlob = typeof resource.blob === 'string'
    if (!uri && !mediaType && !hasBlob) {
      return null
    }
    return {
      kind: 'resource',
      name: resourceName(resource, uri || 'Embedded resource'),
      mediaType,
      sourceURL: hasBlob ? toolContentSourceURL(event, contentIndex) : '',
      uri,
      description: '',
    }
  }

  if (type === 'resource_link' || type === 'resourceLink') {
    const uri = payloadString(value, ['uri'])
    if (!uri) {
      return null
    }
    return {
      kind: 'resource-link',
      name: resourceName(value, uri),
      mediaType: payloadString(value, ['mimeType', 'mime_type']),
      sourceURL: '',
      uri,
      description: payloadString(value, ['description']),
    }
  }

  return null
}

function toolContentSourceURL(event: AgentEvent, contentIndex: number) {
  return `/api/sessions/${encodeURIComponent(event.session_id)}/events/${event.seq}/tool-content/${contentIndex}`
}

function resourceName(payload: Record<string, unknown>, fallback: string) {
  return payloadString(payload, ['title', 'name']) || basename(fallback) || fallback
}

function fileChangeLabel(group: EventGroup) {
  const fileName = group.paths[0] ? basename(group.paths[0]) : ''
  if (!fileName) {
    return 'File change'
  }
  if (group.paths.length > 1) {
    return `${fileName} +${group.paths.length - 1}`
  }
  return fileName
}

function fileChangeDiffText(group: EventGroup) {
  const chunks: string[] = []
  for (const event of group.events) {
    for (const chunk of fileChangeDiffChunks(event.payload)) {
      if (!chunk || chunks[chunks.length - 1] === chunk) {
        continue
      }
      chunks.push(chunk)
    }
  }
  return chunks.join('\n')
}

function fileChangeDiffChunks(payload: unknown) {
  if (!isRecord(payload)) {
    return []
  }

  const direct = firstPayloadString(payload, [
    'diff',
    'patch',
    'unified_diff',
    'unifiedDiff',
    'text',
    'output',
    'summary',
  ])
  if (direct) {
    return [direct]
  }

  const changes = Array.isArray(payload.changes) ? payload.changes : []
  return changes.flatMap((change) => fileChangeDiffChunk(change))
}

function fileChangeDiffChunk(change: unknown) {
  if (!isRecord(change)) {
    return []
  }

  const direct = firstPayloadString(change, ['diff', 'patch', 'unified_diff', 'unifiedDiff'])
  if (direct) {
    return [direct]
  }

  const path = payloadString(change, ['path'])
  const oldText = firstPayloadString(change, ['old_text', 'oldText', 'before', 'previous', 'original'])
  const newText = firstPayloadString(change, ['new_text', 'newText', 'after', 'current', 'replacement'])
  if (oldText || newText) {
    return [simpleDiff(path, oldText, newText)]
  }

  const fallback = firstPayloadString(change, ['text', 'output', 'summary'])
  return fallback ? [path ? `${path}\n${fallback}` : fallback] : []
}

function simpleDiff(path: string, oldText: string, newText: string) {
  const lines: string[] = []
  if (path) {
    lines.push(`--- ${path}`)
    lines.push(`+++ ${path}`)
  }
  if (oldText) {
    lines.push(
      ...oldText
        .split('\n')
        .filter(Boolean)
        .map((line) => `- ${line}`),
    )
  }
  if (newText) {
    lines.push(
      ...newText
        .split('\n')
        .filter(Boolean)
        .map((line) => `+ ${line}`),
    )
  }
  return lines.join('\n')
}

function basename(path: string) {
  const trimmed = path.trim().replace(/\/+$/, '')
  return trimmed.split('/').filter(Boolean).pop() ?? trimmed
}

function firstPayloadString(payload: Record<string, unknown>, keys: string[]) {
  for (const key of keys) {
    const value = payload[key]
    if (typeof value === 'string' && value.trim()) {
      return value
    }
  }
  return ''
}

function toolLabelFromEvents(events: AgentEvent[]) {
  for (const event of [...events].reverse()) {
    const label = toolLabelFromPayload(event.payload)
    if (label) {
      return `Tool: ${label}`
    }
  }
  return 'Tool call'
}

function toolLabelFromPayload(payload: unknown) {
  if (!isRecord(payload)) {
    return ''
  }

  const itemType = payloadString(payload, ['item_type'])
  if (itemType === 'webSearch') {
    const query = toolQueryFromPayload(payload)
    return query ? `Web search: ${query}` : 'Web search'
  }

  const command = payloadString(payload, ['command'])
  if (command) {
    return cleanShellCommand(command)
  }

  const input = toolInputFromPayload(payload)
  const inputCommand = input ? payloadString(input, ['command']) : ''
  if (inputCommand) {
    return cleanShellCommand(inputCommand)
  }

  const query = toolQueryFromPayload(payload)
  if (query) {
    return query
  }

  const structured = structuredToolLabelFromPayload(payload)
  if (structured) {
    return structured
  }

  const name = payloadString(payload, ['name', 'tool', 'server', 'namespace', 'item_type'])
  return name
}

function structuredToolLabelFromPayload(payload: Record<string, unknown>) {
  const rawInput = toolInputFromPayload(payload)
  const kind = payloadString(payload, ['kind', 'tool', 'name'])
  const title = payloadString(payload, ['title'])
  const kindKey = toolKindKey(kind || title)
  const path = firstToolPath(payload)
  const pattern = rawInput ? payloadString(rawInput, ['pattern', 'glob']) : ''
  const rawCommand = rawInput ? payloadString(rawInput, ['command']) : ''
  const command = rawCommand || (!isGenericToolTitle(title, kindKey) ? title : '')

  if (kindKey === 'read') {
    return path ? `Read ${basename(path)}` : title && !isGenericToolTitle(title, kindKey) ? `Read ${basename(title)}` : 'Read'
  }

  if (kindKey === 'write' || kindKey === 'edit' || kindKey === 'patch') {
    const label = kindKey === 'write' ? 'Write' : 'Edit'
    return path ? `${label} ${basename(path)}` : title && !isGenericToolTitle(title, kindKey) ? `${label} ${title}` : label
  }

  if (kindKey === 'glob') {
    const suffix = path ? ` in ${basename(path)}` : ''
    return pattern ? `Glob ${pattern}${suffix}` : 'Glob'
  }

  if (kindKey === 'grep' || kindKey === 'search') {
    const suffix = path ? ` in ${basename(path)}` : ''
    return pattern ? `Search ${pattern}${suffix}` : 'Search'
  }

  if (kindKey === 'bash' || kindKey === 'shell') {
    return command ? cleanShellCommand(command) : 'Shell'
  }

  if (title && !isGenericToolTitle(title, kindKey)) {
    return title
  }
  if (kind && !isGenericToolTitle(kind, kindKey)) {
    return kind
  }
  return ''
}

function toolKindKey(value: string) {
  return value.trim().toLowerCase().replace(/[\s_-]+/g, '')
}

function isGenericToolTitle(value: string, kindKey: string) {
  const key = toolKindKey(value)
  if (!key) {
    return true
  }
  return (
    key === kindKey ||
    key === 'tool' ||
    key === 'toolcall' ||
    key === 'read' ||
    key === 'write' ||
    key === 'edit' ||
    key === 'patch' ||
    key === 'glob' ||
    key === 'grep' ||
    key === 'search' ||
    key === 'bash' ||
    key === 'shell'
  )
}

function firstToolPath(payload: Record<string, unknown>) {
  const rawInput = toolInputFromPayload(payload)
  const rawOutput = isRecord(payload.raw_output) ? payload.raw_output : null
  const rawOutputMetadata = rawOutput && isRecord(rawOutput.metadata) ? rawOutput.metadata : null
  const rawOutputDisplay = rawOutputMetadata && isRecord(rawOutputMetadata.display) ? rawOutputMetadata.display : null

  return (
    payloadString(payload, ['path', 'file', 'file_path', 'filePath']) ||
    firstLocationPath(payload.locations) ||
    (rawInput ? payloadString(rawInput, ['path', 'file', 'file_path', 'filePath']) : '') ||
    (rawOutputDisplay ? payloadString(rawOutputDisplay, ['path', 'file', 'file_path', 'filePath']) : '') ||
    (rawOutputMetadata ? payloadString(rawOutputMetadata, ['path', 'file', 'file_path', 'filePath']) : '')
  )
}

function toolInputFromPayload(payload: Record<string, unknown>) {
  if (isRecord(payload.raw_input)) {
    return payload.raw_input
  }
  return isRecord(payload.arguments) ? payload.arguments : null
}

function firstLocationPath(value: unknown) {
  if (!Array.isArray(value)) {
    return ''
  }
  for (const item of value) {
    if (!isRecord(item)) {
      continue
    }
    const path = payloadString(item, ['path', 'file', 'file_path', 'filePath'])
    if (path) {
      return path
    }
  }
  return ''
}

function toolTextLines(payload: unknown) {
  const text = cleanShellCommand(payloadText(payload))
  if (text) {
    return [text]
  }
  if (!isRecord(payload)) {
    return []
  }

  const itemType = payloadString(payload, ['item_type'])
  const queries = toolQueriesFromPayload(payload)
  const query = toolQueryFromPayload(payload)
  if (itemType === 'webSearch' || query || queries.length > 0) {
    const lines: string[] = []
    if (query) {
      lines.push(`Query: ${query}`)
    }
    if (queries.length > 0) {
      lines.push('Queries:')
      lines.push(...queries.map((value) => `- ${value}`))
    }
    return lines
  }

  return []
}

function nestedToolResultText(value: unknown) {
  if (typeof value === 'string') {
    return value
  }
  if (!isRecord(value)) {
    return ''
  }

  if (isRecord(value.structuredContent)) {
    const output = payloadLiteralString(value.structuredContent, ['output'])
    if (output) {
      return output
    }
  }

  if (Array.isArray(value.content)) {
    const parts = value.content.flatMap((rawBlock): string[] => {
      if (!isRecord(rawBlock)) {
        return []
      }
      const type = payloadString(rawBlock, ['type'])
      if (type === 'text') {
        const text = payloadLiteralString(rawBlock, ['text'])
        return text ? [text] : []
      }
      if (type === 'resource' && isRecord(rawBlock.resource)) {
        const text = payloadLiteralString(rawBlock.resource, ['text'])
        return text ? [text] : []
      }
      return []
    })
    if (parts.length > 0) {
      return parts.join('\n')
    }
  }

  if (value.structuredContent !== undefined && value.structuredContent !== null) {
    const structured = JSON.stringify(value.structuredContent, null, 2)
    return structured === '{}' || structured === 'null' ? '' : structured
  }
  return ''
}

function toolQueryFromPayload(payload: unknown) {
  if (!isRecord(payload)) {
    return ''
  }
  const direct = payloadString(payload, ['query'])
  if (direct) {
    return direct
  }
  if (isRecord(payload.action)) {
    const query = payloadString(payload.action, ['query'])
    if (query) {
      return query
    }
  }
  if (isRecord(payload.arguments)) {
    return payloadString(payload.arguments, ['query'])
  }
  return ''
}

function toolQueriesFromPayload(payload: unknown) {
  if (!isRecord(payload)) {
    return []
  }
  const values = isRecord(payload.action)
    ? payload.action.queries
    : isRecord(payload.arguments)
      ? payload.arguments.queries
      : payload.queries
  if (!Array.isArray(values)) {
    return []
  }
  return uniqueStrings(values.flatMap((value) => (typeof value === 'string' && value.trim() ? [value.trim()] : [])))
}

function cleanToolLabel(label: string) {
  const prefix = 'Tool: '
  if (!label.startsWith(prefix)) {
    return label
  }
  return `${prefix}${cleanShellCommand(label.slice(prefix.length))}`
}

function syncMessageTimelineItem(items: ChatTimelineItem[], message: ChatTranscriptMessage) {
  const existing = items.find((item) => item.kind === 'message' && item.message === message)
  if (existing) {
    existing.startSeq = message.startSeq
    existing.endSeq = message.endSeq
    return
  }
  items.push({
    kind: 'message',
    id: message.id,
    startSeq: message.startSeq,
    endSeq: message.endSeq,
    message,
  })
}

function isHiddenDebugGroup(group: EventGroup) {
  switch (group.kind) {
    case 'user-message':
    case 'action-break':
    case 'agent-message':
    case 'plan':
    case 'tool-call':
    case 'file-change':
    case 'error':
      return false
    default:
      return true
  }
}

function chatDebugEventFromGroup(group: EventGroup): ChatDebugEvent {
  return {
    id: `debug-${group.id}`,
    label: group.label,
    status: group.status,
    startSeq: group.startSeq,
    endSeq: group.endSeq,
    eventCount: group.events.length,
    text: group.text,
    error: group.error,
    payload: group.events.length === 1 ? group.events[0]?.payload : group.events.map(rawEventSummary),
    createdAt: group.events[0]?.created_at ?? '',
  }
}

function chatRunErrorFromGroup(group: EventGroup): ChatRunError {
  return {
    id: `error-${group.id}`,
    label: runErrorLabel(group),
    status: group.status,
    startSeq: group.startSeq,
    endSeq: group.endSeq,
    error: group.error || group.text || group.label,
    createdAt: group.events[0]?.created_at ?? '',
  }
}

function runErrorLabel(group: EventGroup) {
  if (group.events.some((event) => event.type === 'agent.run.failed')) {
    return 'Run failed'
  }
  return group.label
}

function rawEventSummary(event: AgentEvent) {
  return {
    seq: event.seq,
    type: event.type,
    status: event.status,
    payload: event.payload,
    created_at: event.created_at,
  }
}

function cleanShellCommand(value: string) {
  const match = value.trim().match(/^(?:\/[^\s]+\/)?(?:zsh|bash|sh)\s+-lc\s+(.+)$/)
  if (!match) {
    return value
  }
  return unquoteShellArg(match[1])
}

function unquoteShellArg(value: string) {
  const trimmed = value.trim()
  if (trimmed.length >= 2) {
    const first = trimmed[0]
    const last = trimmed[trimmed.length - 1]
    if ((first === "'" && last === "'") || (first === '"' && last === '"')) {
      return trimmed.slice(1, -1)
    }
  }
  return trimmed
}

function chatGroupItemID(group: EventGroup) {
  if (group.kind === 'plan') {
    return planItemID(group.events[0])
  }
  return payloadString(group.events[0]?.payload, ['item_id', 'message_id', 'id'])
}

function updateMessageRange(message: ChatTranscriptMessage, group: EventGroup) {
  if (group.endSeq >= message.endSeq) {
    message.completedAt = latestGroupTimestamp(group) || message.completedAt
  }
  message.startSeq = Math.min(message.startSeq, group.startSeq)
  message.endSeq = Math.max(message.endSeq, group.endSeq)
}

function latestGroupTimestamp(group: EventGroup) {
  let latestEvent: AgentEvent | undefined
  for (const event of group.events) {
    if (!latestEvent || event.seq > latestEvent.seq) latestEvent = event
  }
  return latestEvent?.created_at ?? ''
}

function applyAssistantBlockDurations(messages: ChatTranscriptMessage[], events: AgentEvent[]) {
  const sortedEvents = sortedUniqueEvents(events)
  let previousAssistantCompletedAt = ''
  let currentUserMessageAt = ''

  for (const message of messages) {
    message.durationMs = null
    if (message.role === 'user') {
      currentUserMessageAt = message.createdAt
      previousAssistantCompletedAt = ''
      continue
    }
    if (message.streaming) continue

    const completedAt = timestampMilliseconds(message.completedAt)
    if (completedAt === null) continue

    const startedAt = timestampMilliseconds(
      previousAssistantCompletedAt || runStartedAtBefore(sortedEvents, message.startSeq) || currentUserMessageAt,
    )
    if (message.text.trim() && startedAt !== null && completedAt >= startedAt) {
      message.durationMs = completedAt - startedAt
    }
    previousAssistantCompletedAt = message.completedAt
  }
}

function runStartedAtBefore(events: AgentEvent[], sequence: number) {
  for (let index = events.length - 1; index >= 0; index -= 1) {
    const event = events[index]
    if (event.seq > sequence) continue
    if (event.type === 'agent.run.started') return event.created_at
    if (isTerminalEvent(event.type)) break
  }
  return ''
}

function timestampMilliseconds(value: string) {
  if (!value) return null
  const timestamp = new Date(value).getTime()
  return Number.isNaN(timestamp) ? null : timestamp
}

function mergeChatText(current: string, next: string) {
  if (!next) return current
  if (!current) return next
  if (next === current || current.endsWith(next)) return current
  if (next.startsWith(current)) return next
  return `${current}\n\n${next}`
}

function isSameAssistantTextStream(current: string, next: string) {
  const currentText = current.trim()
  const nextText = next.trim()
  if (!currentText || !nextText) {
    return false
  }
  return nextText === currentText || nextText.startsWith(currentText) || currentText.endsWith(nextText)
}

function payloadPaths(payload: unknown) {
  if (!isRecord(payload)) {
    return []
  }

  const paths = new Set<string>()
  addPathValue(paths, payload.path)
  addPathValue(paths, payload.file)
  addPathValue(paths, payload.files)
  addPathValue(paths, payload.paths)
  addPathValue(paths, payload.changes)
  return [...paths]
}

function addPathValue(paths: Set<string>, value: unknown) {
  if (typeof value === 'string' && value.trim()) {
    paths.add(value)
    return
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      if (typeof item === 'string') {
        addPathValue(paths, item)
      } else if (isRecord(item)) {
        addPathValue(paths, item.path ?? item.file)
      }
    }
  }
}

function userInputRequestFromEvent(event: AgentEvent): PendingUserInputRequest | null {
  if (!isRecord(event.payload)) {
    return null
  }

  const requestID = payloadString(event.payload, ['request_id'])
  if (!requestID) {
    return null
  }
  const questions = payloadQuestions(event.payload.questions)
  if (questions.length === 0) {
    return null
  }

  return {
    requestID,
    provider: payloadString(event.payload, ['provider']),
    providerEventType: payloadString(event.payload, ['provider_event_type']),
    threadID: payloadString(event.payload, ['thread_id']),
    turnID: payloadString(event.payload, ['turn_id']),
    itemID: payloadString(event.payload, ['item_id']),
    delivery: payloadString(event.payload, ['delivery']) === 'async' ? 'async' : undefined,
    questions,
    createdAt: event.created_at,
    seq: event.seq,
  }
}

function tokenUsageFromEvent(event: AgentEvent): TokenUsageSummary | null {
  const openCodeUsage = openCodeTokenUsageFromEvent(event)
  if (openCodeUsage) {
    return openCodeUsage
  }

  if (
    event.type !== 'provider.codex.event' ||
    payloadString(event.payload, ['provider_event_type']) !== 'thread/tokenUsage/updated'
  ) {
    return null
  }
  if (!isRecord(event.payload) || !isRecord(event.payload.raw)) {
    return null
  }

  const tokenUsage = event.payload.raw.tokenUsage
  if (!isRecord(tokenUsage)) {
    return null
  }

  const total = tokenUsageSnapshot(tokenUsage.total)
  const last = tokenUsageSnapshot(tokenUsage.last)
  const modelContextWindow = payloadNumber(tokenUsage, ['modelContextWindow'])
  if (!total || !last || modelContextWindow <= 0) {
    return null
  }

  return {
    kind: 'tokens',
    total,
    last,
    modelContextWindow,
    sessionTotalTokens: positivePayloadNumber(event.payload, ['session_total_tokens']),
    updatedAt: event.created_at,
    seq: event.seq,
  }
}

function positivePayloadNumber(payload: unknown, keys: string[]) {
  const value = payloadNumber(payload, keys)
  return value > 0 ? value : undefined
}

function openCodeTokenUsageFromEvent(event: AgentEvent): TokenUsageSummary | null {
  if (
    event.type !== 'provider.opencode.event' ||
    payloadString(event.payload, ['provider_event_type']) !== 'usage_update' ||
    !isRecord(event.payload)
  ) {
    return null
  }

  const usageUpdate = isRecord(event.payload.raw_update) ? event.payload.raw_update : event.payload
  const used = payloadNumber(usageUpdate, ['used'])
  const size = payloadNumber(usageUpdate, ['size'])
  if (used <= 0 || size <= 0) {
    return null
  }

  const snapshot = {
    totalTokens: used,
    inputTokens: 0,
    cachedInputTokens: 0,
    outputTokens: 0,
    reasoningOutputTokens: 0,
  }

  return {
    kind: 'context',
    total: snapshot,
    last: snapshot,
    modelContextWindow: size,
    cost: openCodeCost(usageUpdate),
    updatedAt: event.created_at,
    seq: event.seq,
  }
}

// Claude's result usage is a run total; message usage describes the current context.
// Reduce durable events so refresh/replay and live updates use the same accounting.
function claudeTokenUsageReducer() {
  let model = ''
  let window: number | null = null
  let last: TokenUsageSnapshot | null = null
  let messageID = ''
  let runID = ''
  const messages = new Map<string, Record<string, unknown>>()
  let total: TokenUsageSnapshot = emptyTokenUsage()

  return (event: AgentEvent): TokenUsageSummary | null => {
    const payload = event.payload
    if (!isRecord(payload) || payload.provider !== 'claude' || payloadString(payload, ['parent_tool_use_id'])) return null
    const type = payloadString(payload, ['provider_event_type'])
    const nextRun = payloadString(payload, ['run_id'])
    if ((nextRun && nextRun !== runID) || type === 'system/init') {
      runID = nextRun
      messages.clear()
      total = emptyTokenUsage()
      messageID = ''
    }
    const rawEvent = isRecord(payload.raw_event) ? payload.raw_event : {}
    const rawMessage = isRecord(payload.raw_message) ? payload.raw_message
      : isRecord(rawEvent.message) ? rawEvent.message : {}
    const nextModel = payloadString(payload, ['model']) || payloadString(rawMessage, ['model'])
    if (nextModel && nextModel !== model) {
      model = nextModel
      window = null
      last = null
    }

    const isResult = type === 'result'
    const isMessage = type === 'message_start' || type === 'message_delta' || type === 'assistant'
    if (isMessage) {
      const id = payloadString(payload, ['message_id']) || payloadString(rawMessage, ['id'])
      if (id) messageID = id
      else if (!messageID) messageID = `message:${event.seq}`
      const usage = isRecord(payload.usage) ? payload.usage
        : isRecord(rawMessage.usage) ? rawMessage.usage : isRecord(rawEvent.usage) ? rawEvent.usage : null
      if (usage) {
        const previous = messages.get(messageID) ?? {}
        const merged = { ...previous, ...usage }
        // The completed assistant envelope can repeat the initial output count.
        merged.output_tokens = Math.max(payloadNumber(previous, ['output_tokens']), payloadNumber(usage, ['output_tokens']))
        messages.set(messageID, merged)
        last = ['input_tokens', 'cache_creation_input_tokens', 'cache_read_input_tokens'].some((key) => typeof merged[key] === 'number')
          ? claudeTokenUsageSnapshot(merged) : null
        total = emptyTokenUsage()
        for (const value of messages.values()) {
          const snapshot = claudeTokenUsageSnapshot(value)
          if (snapshot) for (const key of Object.keys(total) as (keyof TokenUsageSnapshot)[]) total[key] += snapshot[key]
        }
      }
    }
    if (isResult) {
      window = claudeModelContextWindow(payload, model) ?? window
      if (isRecord(payload.usage)) total = claudeTokenUsageSnapshot(payload.usage) ?? total
    }
    if (!isMessage && !isResult && type !== 'system/init') return null
    return { kind: 'tokens', total, last, modelContextWindow: window, updatedAt: event.created_at, seq: event.seq }
  }
}

function emptyTokenUsage(): TokenUsageSnapshot {
  return { totalTokens: 0, inputTokens: 0, cachedInputTokens: 0, outputTokens: 0, reasoningOutputTokens: 0 }
}

function openCodeCost(usageUpdate: Record<string, unknown>) {
  if (!isRecord(usageUpdate.cost)) {
    return undefined
  }
  const amount = payloadNumber(usageUpdate.cost, ['amount'])
  const currency = payloadString(usageUpdate.cost, ['currency'])
  if (!Number.isFinite(amount) || amount < 0 || !currency) {
    return undefined
  }
  return { amount, currency }
}

function claudeTokenUsageSnapshot(usage: Record<string, unknown>): TokenUsageSnapshot | null {
  const inputTokens = payloadNumber(usage, ['input_tokens'])
  const cacheCreationInputTokens = payloadNumber(usage, ['cache_creation_input_tokens'])
  const cacheReadInputTokens = payloadNumber(usage, ['cache_read_input_tokens'])
  const outputTokens = payloadNumber(usage, ['output_tokens'])
  const reasoningOutputTokens = isRecord(usage.output_tokens_details)
    ? payloadNumber(usage.output_tokens_details, ['thinking_tokens'])
    : 0
  const totalInputTokens = inputTokens + cacheCreationInputTokens + cacheReadInputTokens
  const totalTokens = totalInputTokens + outputTokens
  if (totalTokens <= 0) {
    return null
  }
  return {
    totalTokens,
    inputTokens: totalInputTokens,
    cachedInputTokens: cacheReadInputTokens,
    outputTokens,
    reasoningOutputTokens,
  }
}

function claudeModelContextWindow(payload: Record<string, unknown>, model: string): number | null {
  if (!isRecord(payload.model_usage)) return null
  const entries = Object.entries(payload.model_usage).filter((entry): entry is [string, Record<string, unknown>] => isRecord(entry[1]))
  const exact = entries.find(([name]) => name === model)
  const canonical = entries.filter(([, value]) => model && value.canonicalModel === model)
  const match = exact ?? (canonical.length === 1 ? canonical[0] : undefined)
    ?? (!model && entries.length === 1 ? entries[0] : undefined)
  const size = match ? payloadNumber(match[1], ['contextWindow']) : 0
  return size > 0 ? size : null
}

function tokenUsageSnapshot(value: unknown): TokenUsageSnapshot | null {
  if (!isRecord(value)) {
    return null
  }
  return {
    totalTokens: payloadNumber(value, ['totalTokens']),
    inputTokens: payloadNumber(value, ['inputTokens']),
    cachedInputTokens: payloadNumber(value, ['cachedInputTokens']),
    outputTokens: payloadNumber(value, ['outputTokens']),
    reasoningOutputTokens: payloadNumber(value, ['reasoningOutputTokens']),
  }
}

function payloadQuestions(value: unknown): UserInputQuestion[] {
  if (!Array.isArray(value)) {
    return []
  }
  return value.flatMap((item) => {
    if (!isRecord(item)) {
      return []
    }
    const id = payloadString(item, ['id'])
    const question = payloadString(item, ['question'])
    if (!id || !question) {
      return []
    }
    return [
      {
        id,
        header: payloadString(item, ['header']),
        question,
        is_other: item.is_other === true,
        is_secret: item.is_secret === true,
        multi_select: item.multi_select === true,
        options: payloadOptions(item.options),
      },
    ]
  })
}

function payloadOptions(value: unknown) {
  if (!Array.isArray(value)) {
    return []
  }
  return value.flatMap((item) => {
    if (!isRecord(item)) {
      return []
    }
    const label = payloadString(item, ['label'])
    if (!label) {
      return []
    }
    return [
      {
        label,
        description: payloadString(item, ['description']),
      },
    ]
  })
}

function payloadNumber(payload: unknown, keys: string[]) {
  if (!isRecord(payload)) {
    return 0
  }
  for (const key of keys) {
    const value = payload[key]
    if (typeof value === 'number' && Number.isFinite(value)) {
      return value
    }
  }
  return 0
}

function payloadString(payload: unknown, keys: string[]) {
  if (!isRecord(payload)) {
    return ''
  }
  for (const key of keys) {
    const value = payload[key]
    if (typeof value === 'string' && value.trim()) {
      return value.trim()
    }
    if (typeof value === 'number') {
      return String(value)
    }
  }
  return ''
}

function payloadLiteralString(payload: unknown, keys: string[]) {
  if (!isRecord(payload)) {
    return ''
  }
  for (const key of keys) {
    const value = payload[key]
    if (typeof value === 'string' && value) {
      return value
    }
  }
  return ''
}

function joinGroupText(current: string, next: string) {
  if (!current) return next
  if (!next) return current
  return `${current}${next}`
}

function uniqueStrings(values: string[]) {
  return [...new Set(values)]
}

function defaultOpen(kind: EventGroupKind, event: AgentEvent) {
  if (isErrorEvent(event.type, event.status)) return true
  if (kind === 'unknown') return false
  if ((kind === 'tool-call' || kind === 'file-change' || kind === 'log') && event.status === 'completed') {
    return false
  }
  return true
}
