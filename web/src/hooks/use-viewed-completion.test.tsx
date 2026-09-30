import { act, renderHook, waitFor } from '@testing-library/react'
import { markNotificationSeen, type AgentEvent } from '@/lib/api'
import { buildChatTimeline } from '@/lib/events'
import { useViewedCompletion, viewedCompletion } from './use-viewed-completion'

vi.mock('@/lib/api', () => ({ markNotificationSeen: vi.fn().mockResolvedValue({ acknowledged: true }) }))
const event = (seq: number, type: string, payload = {}): AgentEvent => ({
  id: String(seq), session_id: 's', seq, type, role: 'assistant',
  status: 'completed', payload, created_at: '2026-09-30T12:00:00Z',
})
const events = [event(1, 'agent.run.started'), event(2, 'agent.message.completed', { text: 'Done' }), event(3, 'agent.run.completed')]

test('maps completion to final output and rejects stale or empty runs', () => {
  expect(viewedCompletion(events, buildChatTimeline(events, false))?.seq).toBe(3)
  const tools = [events[0], event(2, 'tool.call.completed', { name: 'shell', command: 'go test ./...' }), events[2]]
  expect(viewedCompletion(tools, buildChatTimeline(tools, false))).not.toBeNull()
  expect(viewedCompletion([events[0], events[2]], buildChatTimeline([events[0], events[2]], false))).toBeNull()
  expect(viewedCompletion([...events, event(4, 'agent.run.started')], buildChatTimeline(events, false))).toBeNull()
  for (const type of ['agent.run.failed', 'agent.run.cancelled']) {
    const failed = [events[0], events[1], event(3, type, { message: 'Stopped' })]
    expect(viewedCompletion(failed, buildChatTimeline(failed, false))).not.toBeNull()
  }
})

test('requires focus, visible output end, and no dialog; deduplicates checks', async () => {
  vi.mocked(markNotificationSeen).mockClear()
  window.requestAnimationFrame ??= (callback) => window.setTimeout(() => callback(0), 0)
  window.cancelAnimationFrame ??= (id) => window.clearTimeout(id)
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => window.setTimeout(() => callback(0), 0))
  vi.spyOn(window, 'cancelAnimationFrame').mockImplementation((id) => window.clearTimeout(id))
  let focused = false
  vi.spyOn(document, 'hasFocus').mockImplementation(() => focused)
  const scroller = document.createElement('div')
  const target = document.createElement('div')
  target.dataset.viewedCompletion = ''
  scroller.append(target)
  document.body.append(scroller)
  let y = 700
  scroller.getBoundingClientRect = () => ({ top: 0, bottom: 600, left: 0, right: 600 } as DOMRect)
  target.getBoundingClientRect = () => ({ top: y, left: 20, width: 400, height: 1 } as DOMRect)
  const original = document.elementFromPoint
  document.elementFromPoint = vi.fn(() => target)
  const candidate = viewedCompletion(events, buildChatTimeline(events, false))
  const ref = { current: scroller }
  const { unmount } = renderHook(() => useViewedCompletion(ref, candidate, true, 100, 0))
  const tick = async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 30)) }) }
  await tick()
  expect(markNotificationSeen).not.toHaveBeenCalled()
  focused = true
  window.dispatchEvent(new Event('focus'))
  await tick()
  expect(markNotificationSeen).not.toHaveBeenCalled()
  y = 450
  const dialog = document.createElement('div')
  dialog.setAttribute('aria-modal', 'true')
  document.body.append(dialog)
  window.dispatchEvent(new Event('focus'))
  await tick()
  expect(markNotificationSeen).not.toHaveBeenCalled()
  dialog.remove()
  await waitFor(() => expect(markNotificationSeen).toHaveBeenCalledWith('s', 3))
  window.dispatchEvent(new Event('focus'))
  await tick()
  expect(markNotificationSeen).toHaveBeenCalledTimes(1)
  unmount()
  scroller.remove()
  document.elementFromPoint = original
  vi.restoreAllMocks()
})
