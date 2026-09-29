import type { AgentEvent, Session, SessionAgentOptions, SessionStatus } from '@/lib/api'
import { isTerminalEvent, isTransientEvent } from '@/lib/events'
import { latestSessionSeq } from '@/lib/session-attention'
import { applySessionActivity } from '@/lib/session-order'

export function applySessionEvent(session: Session, event: AgentEvent, status: SessionStatus | null) {
  session = applySessionActivity(session, event)
  if (isTransientEvent(event)) return session
  const currentLastSeq = latestSessionSeq(session)
  if (event.seq <= currentLastSeq) {
    return session
  }
  const nextLastSeq = Math.max(currentLastSeq, event.seq)
  const eventCount = (session.event_count ?? 0) + (isTransientEvent(event) ? 0 : 1)
  const toolCount = (session.tool_count ?? 0) + (isToolActivityEvent(event) ? 1 : 0)
  const tokenCount = Math.max(session.token_count ?? 0, payloadNumber(event.payload, 'session_total_tokens') ?? 0)
  const pendingInput = pendingInputFromEvent(session.pending_input ?? false, event)
  const pendingPermissionCount = pendingPermissionCountFromEvent(session.pending_permission_count ?? 0, event)
  const updatedAgentOptions = sessionAgentOptionsFromEvent(event)
  const updatedPinnedAt = sessionPinnedAtFromEvent(event)
  const updatedArchivedAt = sessionArchivedAtFromEvent(event)
  if (!status) {
    return {
      ...session,
      event_count: eventCount,
      last_event_seq: nextLastSeq,
      tool_count: toolCount,
      token_count: tokenCount,
      pending_input: pendingInput,
      pending_permission_count: pendingPermissionCount,
      ...(updatedAgentOptions
        ? {
            agent_options: updatedAgentOptions,
            updated_at: payloadString(event.payload, 'updated_at') ?? event.created_at,
          }
        : {}),
      ...(updatedPinnedAt !== undefined ? { pinned_at: updatedPinnedAt } : {}),
      ...(updatedArchivedAt !== undefined
        ? {
            archived_at: updatedArchivedAt,
            updated_at: payloadString(event.payload, 'updated_at') ?? event.created_at,
          }
        : {}),
    }
  }

  const updatedAt = payloadString(event.payload, 'updated_at') ?? event.created_at
  const completedAt =
    status === 'running' || status === 'idle'
      ? null
      : (payloadString(event.payload, 'completed_at') ?? event.created_at)

  return {
    ...session,
    status,
    event_count: eventCount,
    last_event_seq: nextLastSeq,
    tool_count: toolCount,
    token_count: tokenCount,
    pending_input: pendingInput,
    pending_permission_count: pendingPermissionCount,
    updated_at: updatedAt,
    completed_at: completedAt,
  }
}

function sessionArchivedAtFromEvent(event: AgentEvent): string | null | undefined {
  if (
    (event.type !== 'session.archived' && event.type !== 'session.restored') ||
    typeof event.payload !== 'object' ||
    event.payload === null ||
    Array.isArray(event.payload) ||
    !('archived_at' in event.payload)
  ) {
    return undefined
  }
  const archivedAt = (event.payload as Record<string, unknown>).archived_at
  return typeof archivedAt === 'string' ? archivedAt : archivedAt === null ? null : undefined
}

function sessionPinnedAtFromEvent(event: AgentEvent): string | null | undefined {
  if (
    event.type !== 'session.pin.updated' ||
    typeof event.payload !== 'object' ||
    event.payload === null ||
    Array.isArray(event.payload) ||
    !('pinned_at' in event.payload)
  ) {
    return undefined
  }
  const pinnedAt = (event.payload as Record<string, unknown>).pinned_at
  return typeof pinnedAt === 'string' ? pinnedAt : pinnedAt === null ? null : undefined
}

function sessionAgentOptionsFromEvent(event: AgentEvent): SessionAgentOptions | null {
  if (
    event.type !== 'session.agent_options.updated' ||
    typeof event.payload !== 'object' ||
    event.payload === null ||
    Array.isArray(event.payload)
  ) {
    return null
  }
  const agentOptions = (event.payload as Record<string, unknown>).agent_options
  if (typeof agentOptions !== 'object' || agentOptions === null || Array.isArray(agentOptions)) {
    return null
  }
  return agentOptions as SessionAgentOptions
}

function pendingPermissionCountFromEvent(current: number, event: AgentEvent) {
  if (event.type === 'agent.permission.requested') return current + 1
  if (event.type === 'agent.permission.resolved' || event.type === 'agent.permission.cancelled') {
    return Math.max(0, current - 1)
  }
  if (isTerminalEvent(event.type)) return 0
  return current
}

function pendingInputFromEvent(current: boolean, event: AgentEvent) {
  if (event.type === 'agent.input.requested') return true
  if (event.type === 'agent.input.answered' || event.type === 'agent.input.failed' || isTerminalEvent(event.type)) return false
  return current
}

function isToolActivityEvent(event: AgentEvent) {
  return event.type === 'tool.call.started' || event.type === 'file.change.started'
}

function payloadString(payload: unknown, key: string) {
  if (typeof payload !== 'object' || payload === null || Array.isArray(payload)) return null
  const value = (payload as Record<string, unknown>)[key]
  return typeof value === 'string' ? value : null
}

function payloadNumber(payload: unknown, key: string) {
  if (typeof payload !== 'object' || payload === null || Array.isArray(payload)) return null
  const value = (payload as Record<string, unknown>)[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}
