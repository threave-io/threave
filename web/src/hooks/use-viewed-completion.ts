import { useEffect, useRef, type RefObject } from 'react'
import { markNotificationSeen, type AgentEvent } from '@/lib/api'
import { isTerminalEvent, type ChatTimelineItem } from '@/lib/events'

// A terminal event is often hidden in the transcript. Associate it with the
// final output of its run rather than treating the visible sequence range as
// proof that the user saw the completion.
export function viewedCompletion(events: AgentEvent[], timeline: ChatTimelineItem[]) {
  const ordered = [...events].sort((a, b) => a.seq - b.seq)
  const terminal = ordered.findLast((event) => isTerminalEvent(event.type))
  if (!terminal) return null
  if (ordered.some((event) => event.seq > terminal.seq &&
    (event.type === 'agent.run.started' || event.type === 'user.message.completed'))) return null
  const start = ordered.findLast((event) => event.seq < terminal.seq &&
    (event.type === 'agent.run.started' || event.type === 'user.message.completed'))?.seq
  if (start === undefined) return null
  const row = timeline.findLast((item) => item.startSeq > start && item.endSeq <= terminal.seq &&
    (item.kind === 'error' || (item.kind === 'message' && item.message.role === 'assistant')))
  return row ? { sessionID: terminal.session_id, seq: terminal.seq, rowID: row.id } : null
}

export function useViewedCompletion(
  scrollerRef: RefObject<HTMLDivElement | null>,
  candidate: ReturnType<typeof viewedCompletion>,
  enabled: boolean,
  bottomInset: number,
  topInset: number,
) {
  const sessionID = candidate?.sessionID
  const seq = candidate?.seq
  const rowID = candidate?.rowID
  const attemptedRef = useRef(new Set<string>())
  useEffect(() => {
    const scroller = scrollerRef.current
    if (!scroller || !sessionID || !seq || !rowID || !enabled) return
    const key = sessionID + ":" + seq
    let attempted = attemptedRef.current.has(key)
    let disposed = false
    let frame = 0
    const check = () => {
      frame = 0
      if (disposed || attempted || document.visibilityState !== 'visible' || !document.hasFocus()) return
      if (document.querySelector('[role="dialog"][data-state="open"], [aria-modal="true"]')) return
      const target = scroller.querySelector<HTMLElement>('[data-viewed-completion]')
      if (!target) return
      const bounds = scroller.getBoundingClientRect()
      const rect = target.getBoundingClientRect()
      const x = rect.left + rect.width / 2
      const y = rect.top + rect.height / 2
      if (y < Math.max(0, bounds.top + topInset) || y > Math.min(window.innerHeight, bounds.bottom - bottomInset) ||
        x < bounds.left || x > bounds.right) return
      // Detect fixed headers/composers and other overlays covering this point.
      const hit = document.elementFromPoint(x, y)
      if (!hit || !target.parentElement?.contains(hit)) return
      attempted = true
      if (attemptedRef.current.size >= 128) attemptedRef.current.clear()
      attemptedRef.current.add(key)
      void markNotificationSeen(sessionID, seq).catch(() => {
        // Best effort only: failure leaves normal notifications enabled.
      })
    }
    const schedule = () => {
      if (!frame && !disposed && !attempted) frame = window.requestAnimationFrame(check)
    }
    const observer = new MutationObserver(schedule)
    observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['data-state', 'aria-modal', 'style'] })
    const resize = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(schedule) : null
    resize?.observe(scroller)
    scroller.addEventListener('scroll', schedule, { passive: true })
    window.addEventListener('focus', schedule)
    window.addEventListener('pageshow', schedule)
    window.addEventListener('resize', schedule)
    document.addEventListener('visibilitychange', schedule)
    schedule()
    return () => {
      disposed = true
      window.cancelAnimationFrame(frame)
      observer.disconnect()
      resize?.disconnect()
      scroller.removeEventListener('scroll', schedule)
      window.removeEventListener('focus', schedule)
      window.removeEventListener('pageshow', schedule)
      window.removeEventListener('resize', schedule)
      document.removeEventListener('visibilitychange', schedule)
    }
  }, [scrollerRef, sessionID, seq, rowID, enabled, bottomInset, topInset])
}
