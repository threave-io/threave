import type { AgentEvent, Session } from '@/lib/api'
import { applySessionEvent } from '@/lib/session-events'
import { preserveSessionActivity, sortSessions } from '@/lib/session-order'

function session(id: string, minute: number, parent?: string): Session {
  return {
    id, title: id, agent_type: 'fake', status: 'idle', workspace_path: '/repo',
    event_count: 0, tool_count: 0, created_at: '2026-06-12T16:00:00Z',
    updated_at: '2026-06-12T18:00:00Z', completed_at: null, archived_at: null,
    last_activity_at: `2026-06-12T16:${String(minute).padStart(2, '0')}:00Z`,
    parent_session_id: parent,
  }
}

function event(type: string, minute = 10): AgentEvent {
  return {
    id: 'evt_1', session_id: 'a', seq: 1, type, role: 'assistant',
    status: 'completed', payload: {},
    created_at: `2026-06-12T16:${String(minute).padStart(2, '0')}:00Z`,
  }
}

test('sorts by activity regardless of metadata update time, preserving pins', () => {
  const old = { ...session('old', 1), updated_at: '2026-06-12T19:00:00Z' }
  const recent = session('recent', 5)
  const pinned = { ...session('pinned', 0), pinned_at: '2026-06-12T15:00:00Z' }
  expect(sortSessions([old, recent, pinned]).map((s) => s.id)).toEqual(['pinned', 'recent', 'old'])
})

test('groups use the latest activity from any descendant, including nested groups', () => {
  const parent = session('parent', 1)
  const child = session('child', 2, parent.id)
  const grandchild = session('grandchild', 8, child.id)
  const sibling = session('sibling', 4, parent.id)
  const standalone = session('standalone', 6)
  const sorted = sortSessions([standalone, sibling, grandchild, child, parent])
  expect(sorted.indexOf(parent)).toBeLessThan(sorted.indexOf(standalone))
  expect(sorted.indexOf(child)).toBeLessThan(sorted.indexOf(sibling))
})

test.each(['user.message.completed', 'agent.message.completed', 'agent.input.answered', 'tool.call.started', 'file.change.completed'])(
  '%s advances activity without changing the metadata timestamp', (type) => {
    const original = session('a', 1)
    const updated = applySessionEvent(original, event(type), null)
    expect(updated.last_activity_at).toBe(event(type).created_at)
    expect(updated.updated_at).toBe(original.updated_at)
  },
)

test.each(['session.status.updated', 'session.pin.updated', 'session.agent_options.updated', 'provider.codex.event'])(
  '%s does not count as user or agent activity', (type) => {
    const original = session('a', 1)
    expect(applySessionEvent(original, event(type), null).last_activity_at).toBe(original.last_activity_at)
  },
)

test('streaming advances activity without consuming the durable event sequence or counters', () => {
  const original = session('a', 1)
  const delta = event('agent.message.delta')
  const streaming = applySessionEvent(original, delta, null)
  expect(streaming.last_activity_at).toBe(delta.created_at)
  expect(streaming.last_event_seq).toBeUndefined()
  expect(streaming.event_count).toBe(0)
  expect(applySessionEvent(streaming, event('agent.message.completed', 11), null).last_event_seq).toBe(1)
})

test('old replay and fresh metadata snapshots cannot move activity backwards', () => {
  const recent = session('a', 10)
  expect(applySessionEvent(recent, event('agent.message.completed', 2), null).last_activity_at).toBe(recent.last_activity_at)
  const incoming = { ...session('a', 1), title: 'Refreshed title' }
  expect(preserveSessionActivity(incoming, recent)).toMatchObject({ title: incoming.title, last_activity_at: recent.last_activity_at })
})

test('sessions without activity use creation time, never their settings update time', () => {
  const empty = { ...session('empty', 0), last_activity_at: null, created_at: '2026-06-12T16:02:00Z' }
  expect(sortSessions([empty, session('active', 1)]).map((s) => s.id)).toEqual(['empty', 'active'])
})
