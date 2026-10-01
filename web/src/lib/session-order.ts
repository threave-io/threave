import type { AgentEvent, Session } from '@/lib/api'
import { isTerminalEvent } from '@/lib/events'
import { latestSessionSeq } from '@/lib/session-attention'

export function sessionActivityTime(session: Session): number {
  return Date.parse(session.last_activity_at ?? session.created_at) || 0
}

export function applySessionActivity(session: Session, event: AgentEvent): Session {
  if (!isTerminalEvent(event.type)) return session
  if (Date.parse(event.created_at) <= sessionActivityTime(session)) return session
  return { ...session, last_activity_at: event.created_at }
}

export function preserveSessionActivity(incoming: Session, existing?: Session): Session {
  // An equally recent server snapshot is authoritative, including corrected activity timestamps.
  return existing?.last_activity_at && latestSessionSeq(existing) > latestSessionSeq(incoming)
    && sessionActivityTime(existing) > sessionActivityTime(incoming)
    ? { ...incoming, last_activity_at: existing.last_activity_at }
    : incoming
}

export function sortSessions(sessions: Session[]): Session[] {
  const byID = new Map(sessions.map((session) => [session.id, session]))
  const activity = new Map(sessions.map((session) => [session.id, sessionActivityTime(session)]))
  // Carry each descendant's activity to all its ancestors, including collapsed groups.
  for (const session of sessions) {
    const latest = sessionActivityTime(session)
    const visited = new Set([session.id])
    let parentID = session.parent_session_id
    while (parentID && byID.has(parentID) && !visited.has(parentID)) {
      visited.add(parentID)
      activity.set(parentID, Math.max(activity.get(parentID) ?? 0, latest))
      parentID = byID.get(parentID)?.parent_session_id
    }
  }
  return [...sessions].sort((left, right) => {
    const leftPinned = Boolean(left.pinned_at)
    const rightPinned = Boolean(right.pinned_at)
    if (leftPinned !== rightPinned) return rightPinned ? 1 : -1
    if (leftPinned && rightPinned) {
      const byPinned = Date.parse(right.pinned_at!) - Date.parse(left.pinned_at!)
      if (byPinned !== 0) return byPinned
    }
    const byActivity = activity.get(right.id)! - activity.get(left.id)!
    return byActivity || right.id.localeCompare(left.id)
  })
}
