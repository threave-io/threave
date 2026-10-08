import {
  ArrowUpRight,
  Brain,
  Check,
  ChevronDown,
  ChevronRight,
  ClipboardList,
  CircleAlert,
  Copy,
  Download,
  FileText,
  Loader2,
} from 'lucide-react'
import {
  isValidElement,
  memo,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  type TouchEvent as ReactTouchEvent,
  type WheelEvent as ReactWheelEvent,
} from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import { useVirtualizer, type Virtualizer } from '@tanstack/react-virtual'
import remarkGfm from 'remark-gfm'
import remarkDirective from 'remark-directive'
import { remarkCodexDirectives } from '@/lib/remark-codex-followups'
import type { AgentEvent } from '@/lib/api'
import type {
  ChatActionBreak,
  ChatDebugEvent,
  ChatRunError,
  ChatTimelineItem,
  ChatTranscriptAttachment,
  ChatTranscriptMessage,
  ChatTranscriptTool,
  ChatTranscriptToolContent,
  TranscriptSequenceRange,
} from '@/lib/events'
import { useViewedCompletion, viewedCompletion } from '@/hooks/use-viewed-completion'
import { buildChatTimeline } from '@/lib/events'
import { clipboardCopyErrorMessage, copyText } from '@/lib/clipboard'
import { Dialog, DialogContent, DialogDescription, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { cn } from '@/lib/utils'
import { ClientDebugContext, clientDebugEnabled } from '@/lib/client-debug'

const TRANSCRIPT_BOTTOM_BREATHING_ROOM_PX = 6
// Hysteresis keeps incidental wheel/touch jitter magnetized to the live tail,
// while requiring a return to the physical tail before following resumes.
const AUTO_SCROLL_DETACH_THRESHOLD_PX = 48
const AUTO_SCROLL_REATTACH_THRESHOLD_PX = 16
const PHYSICAL_TAIL_THRESHOLD_PX = 2
const HISTORY_LOAD_EDGE_ROWS = 3

type ChatActivityStatus = { kind: 'thinking' } | { kind: 'working'; since: string }

type Props = {
  events: AgentEvent[]
  optimisticUserMessages?: ChatTranscriptMessage[]
  loading?: boolean
  error?: string
  emptyMessage?: string
  topInset?: 'none' | 'sessionHeader'
  bottomInsetHeight?: number
  pinToLatestOnMount?: boolean
  autoScroll?: boolean
  activityStatus?: ChatActivityStatus | null
  showDebugEvents?: boolean
  hasOlderEvents?: boolean
  olderHistoryUnavailable?: boolean
  hasNewerEvents?: boolean
  loadingOlderEvents?: boolean
  loadingNewerEvents?: boolean
  onLoadOlderEvents?: () => Promise<void> | void
  onLoadNewerEvents?: () => Promise<void> | void
  onJumpToLatest?: () => Promise<void> | void
  onFollowingTailChange?: (following: boolean) => void
  onOpenFilePath?: (path: string) => Promise<void> | void
  onFollowUp?: (prompt: string) => void
  focusSeq?: number
  focusRequest?: number
  onVisibleSequenceRangeChange?: (range: TranscriptSequenceRange | null) => void
}

type VirtualTimelineItem =
  | {
      kind: 'timeline'
      id: string
      item: ChatTimelineItem
      timelineIndex: number
    }
  | { kind: 'activity'; id: string; status: ChatActivityStatus }
  | { kind: 'error'; id: string; message: string }
  | { kind: 'tail'; id: 'tail-breathing-room' }

type TranscriptVirtualizer = Virtualizer<HTMLDivElement, HTMLDivElement>

export function ChatTranscript({
  events,
  optimisticUserMessages = [],
  loading = false,
  error = '',
  emptyMessage = 'No messages yet. Submit a prompt to start the chat.',
  topInset = 'none',
  bottomInsetHeight = 0,
  pinToLatestOnMount = false,
  autoScroll = false,
  activityStatus = null,
  showDebugEvents = false,
  hasOlderEvents = false,
  olderHistoryUnavailable = false,
  hasNewerEvents = false,
  loadingOlderEvents = false,
  loadingNewerEvents = false,
  onLoadOlderEvents,
  onLoadNewerEvents,
  onJumpToLatest,
  onFollowingTailChange,
  onOpenFilePath,
  onFollowUp,
  focusSeq = 0,
  focusRequest = 0,
  onVisibleSequenceRangeChange,
}: Props) {
  const sessionID = events.find((event) => event.session_id)?.session_id ?? ''
  const timeline = useMemo(() => [
    ...buildChatTimeline(events, showDebugEvents),
    ...optimisticUserMessages.map((message) => ({
      kind: 'message' as const,
      id: message.id,
      startSeq: 0,
      endSeq: 0,
      message,
    })),
  ], [events, optimisticUserMessages, showDebugEvents])
  const latestOptimisticMessageID = optimisticUserMessages.at(-1)?.id ?? ''
  const previousOptimisticMessageIDRef = useRef(latestOptimisticMessageID)
  const scrollDebug = useContext(ClientDebugContext) ?? clientDebugEnabled()
  const scrollerElementRef = useRef<HTMLDivElement | null>(null)
  const completion = useMemo(() => viewedCompletion(events, timeline), [events, timeline])
  useViewedCompletion(scrollerElementRef, completion, !hasNewerEvents && !loading && optimisticUserMessages.length === 0,
    Math.max(0, bottomInsetHeight), 0)
  const scrollDebugReadoutRef = useRef<HTMLDivElement | null>(null)
  const initiallyFollowingTail = focusSeq <= 0 && (pinToLatestOnMount || !hasNewerEvents)
  const followingTailRef = useRef(initiallyFollowingTail)
  const initialTailPinPendingRef = useRef(initiallyFollowingTail)
  const snapToTailPendingRef = useRef(false)
  const resumeInFlightRef = useRef(false)
  const lastScrollDirectionRef = useRef<'forward' | 'backward' | null>(null)
  const userScrollIntentRef = useRef<'forward' | 'backward' | null>(null)
  const scrollPointerActiveRef = useRef(false)
  const scrollTouchActiveRef = useRef(false)
  const scrollPointerYRef = useRef<number | null>(null)
  const scrollPointerTypeRef = useRef('')
  const touchDetachedFromTailRef = useRef(false)
  const touchScrollRef = useRef<{ startDistance: number; previousDistance: number } | null>(null)
  const tailScrollFrameRef = useRef<number | null>(null)
  const visibleSequenceRangeRef = useRef('')
  const [followingTail, setFollowingTailState] = useState(initiallyFollowingTail)
  const [snapToTailPending, setSnapToTailPending] = useState(false)
  const autoLoadOlderRef = useRef(false)
  const autoLoadNewerRef = useRef(false)
  const composerClearanceHeight = Math.max(0, bottomInsetHeight)
  const tailClearanceHeight = composerClearanceHeight + TRANSCRIPT_BOTTOM_BREATHING_ROOM_PX
  const setFollowingTail = useCallback((next: boolean, forceNotification = false) => {
    const changed = followingTailRef.current !== next
    followingTailRef.current = next
    if (changed) setFollowingTailState(next)
    if (changed || forceNotification) onFollowingTailChange?.(next)
  }, [onFollowingTailChange])

  const setSnapToTailPendingValue = useCallback((next: boolean) => {
    if (snapToTailPendingRef.current === next) return
    snapToTailPendingRef.current = next
    setSnapToTailPending(next)
  }, [])

  const pauseFollowing = useCallback(() => {
    initialTailPinPendingRef.current = false
    if (tailScrollFrameRef.current !== null) {
      window.cancelAnimationFrame(tailScrollFrameRef.current)
      tailScrollFrameRef.current = null
    }
    setSnapToTailPendingValue(false)
    setFollowingTail(false)
  }, [setFollowingTail, setSnapToTailPendingValue])

  useEffect(() => () => {
    if (tailScrollFrameRef.current !== null) window.cancelAnimationFrame(tailScrollFrameRef.current)
  }, [])

  useEffect(() => {
    if (!loadingOlderEvents) {
      autoLoadOlderRef.current = false
    }
  }, [loadingOlderEvents])

  useEffect(() => {
    if (!loadingNewerEvents) autoLoadNewerRef.current = false
  }, [loadingNewerEvents])

  const requestOlderEvents = useCallback(() => {
    if (
      !hasOlderEvents ||
      loadingOlderEvents ||
      autoLoadOlderRef.current ||
      !onLoadOlderEvents
    ) {
      return Promise.resolve()
    }
    autoLoadOlderRef.current = true
    return Promise.resolve(onLoadOlderEvents()).finally(() => {
      autoLoadOlderRef.current = false
    })
  }, [hasOlderEvents, loadingOlderEvents, onLoadOlderEvents])

  const requestOlderEventsIfUnderfilled = useCallback(() => {
    const scroller = scrollerElementRef.current
    if (!scroller || scroller.clientHeight <= 0) return
    if (scroller.scrollHeight > scroller.clientHeight + PHYSICAL_TAIL_THRESHOLD_PX) return
    void requestOlderEvents()
  }, [requestOlderEvents])

  function requestNewerEvents() {
    if (
      !hasNewerEvents ||
      loadingNewerEvents ||
      autoLoadNewerRef.current ||
      !onLoadNewerEvents
    ) {
      return Promise.resolve()
    }
    autoLoadNewerRef.current = true
    return Promise.resolve(onLoadNewerEvents()).finally(() => {
      autoLoadNewerRef.current = false
    })
  }

  const virtualItems = useMemo<VirtualTimelineItem[]>(() => {
    const items: VirtualTimelineItem[] = []
    for (const [timelineIndex, item] of timeline.entries()) {
      items.push({ kind: 'timeline', id: item.id, item, timelineIndex })
    }
    if (activityStatus && !hasNewerEvents) items.push({ kind: 'activity', id: 'activity', status: activityStatus })
    if (error) items.push({ kind: 'error', id: 'chat-error', message: error })
    if (items.length > 0) items.push({ kind: 'tail', id: 'tail-breathing-room' })
    return items
  }, [
    activityStatus,
    error,
    hasNewerEvents,
    timeline,
  ])
  const focusedVirtualIndex = useMemo(
    () => virtualItems.findIndex((item) => item.kind === 'timeline' && timelineItemContainsSeq(item.item, focusSeq)),
    [focusSeq, virtualItems],
  )
  const getItemKey = useCallback((index: number) => virtualItems[index]?.id ?? index, [virtualItems])
  const estimateSize = useCallback((index: number) => estimateVirtualTimelineItem(virtualItems[index]), [virtualItems])
  const latestEventSeq = events.at(-1)?.seq ?? 0
  const activityRevision = activityStatus?.kind === 'working'
    ? `working:${activityStatus.since}`
    : (activityStatus?.kind ?? '')
  const liveTailRevision = `${events.length}:${latestEventSeq}:${activityRevision}:${error}:${showDebugEvents}`
  const timelineStructureRevision = timeline.map((item) => `${item.kind}:${item.id}`).join('|')
  const previousTimelineStructureRevisionRef = useRef(timelineStructureRevision)
  const scheduleTailScroll = useCallback((instance: TranscriptVirtualizer) => {
    instance.scrollToEnd({ behavior: 'auto' })
    if (tailScrollFrameRef.current !== null) window.cancelAnimationFrame(tailScrollFrameRef.current)
    tailScrollFrameRef.current = window.requestAnimationFrame(() => {
      tailScrollFrameRef.current = null
      instance.scrollToEnd({ behavior: 'auto' })
    })
  }, [])
  const resumeFollowing = useCallback(async (instance: TranscriptVirtualizer) => {
    if (resumeInFlightRef.current) return
    resumeInFlightRef.current = true
    touchDetachedFromTailRef.current = false
    touchScrollRef.current = null
    initialTailPinPendingRef.current = false
    setSnapToTailPendingValue(true)
    try {
      if (hasNewerEvents) await onJumpToLatest?.()
      setFollowingTail(true, true)
      scheduleTailScroll(instance)
    } finally {
      resumeInFlightRef.current = false
      setSnapToTailPendingValue(false)
    }
  }, [hasNewerEvents, onJumpToLatest, scheduleTailScroll, setFollowingTail, setSnapToTailPendingValue])
  // TanStack Virtual intentionally exposes a mutable virtualizer instance; React Compiler skips this component.
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer<HTMLDivElement, HTMLDivElement>({
    count: virtualItems.length,
    getScrollElement: () => scrollerElementRef.current,
    estimateSize,
    getItemKey,
    anchorTo: 'end',
    // The event timeline can replace or regroup the final row without changing
    // `count` (for example, Thinking -> first assistant message). TanStack's
    // followOnAppend intentionally ignores that case, so the event-stream
    // layout effect below owns live-tail reconciliation instead.
    followOnAppend: false,
    scrollEndThreshold: PHYSICAL_TAIL_THRESHOLD_PX,
    // The overlay itself remains virtual padding. A permanent, exactly-sized
    // tail item owns the visual breathing room so it never moves between rows
    // during a tool/activity transition.
    paddingEnd: composerClearanceHeight,
    overscan: 6,
    directDomUpdates: true,
    // ResizeObserver can report from React's layout phase. Avoid asking the
    // virtualizer to nest flushSync in that lifecycle; the explicit tail frame
    // below still performs the second scroll after the new height is committed.
    useFlushSync: false,
    onChange: handleVirtualizerChange,
  })

  useLayoutEffect(() => {
    if (previousOptimisticMessageIDRef.current === latestOptimisticMessageID) return
    previousOptimisticMessageIDRef.current = latestOptimisticMessageID
    if (!latestOptimisticMessageID) return
    void resumeFollowing(virtualizer)
  }, [latestOptimisticMessageID, resumeFollowing, virtualizer])

  function handleVirtualizerChange(instance: TranscriptVirtualizer, sync: boolean) {
    updateVisibleSequenceRange(instance)
    updateScrollDebugReadout(instance)
    const direction = instance.scrollDirection
    const userDirection = userScrollIntentRef.current
    if (sync && direction && direction === userDirection) lastScrollDirectionRef.current = direction

    if (
      !userDirection &&
      followingTailRef.current &&
      !hasNewerEvents &&
      physicalDistanceFromEnd(scrollerElementRef.current) > PHYSICAL_TAIL_THRESHOLD_PX
    ) {
      instance.scrollToEnd({ behavior: 'auto' })
    }

    if (sync && direction === 'backward' && userDirection === 'backward') {
      const distanceFromEnd = physicalDistanceFromEnd(scrollerElementRef.current)
      if (followingTailRef.current) {
        if (distanceFromEnd > AUTO_SCROLL_DETACH_THRESHOLD_PX) {
          pauseFollowing()
        } else {
          setSnapToTailPendingValue(true)
        }
      } else {
        setSnapToTailPendingValue(false)
      }
      const firstVisibleIndex = instance.getVirtualItems()[0]?.index ?? Number.POSITIVE_INFINITY
      if (firstVisibleIndex <= HISTORY_LOAD_EDGE_ROWS) void requestOlderEvents()
      return
    }

    if (sync && direction === 'forward' && userDirection === 'forward') {
      const distanceFromEnd = physicalDistanceFromEnd(scrollerElementRef.current)
      if (followingTailRef.current) {
        if (distanceFromEnd <= PHYSICAL_TAIL_THRESHOLD_PX) {
          setSnapToTailPendingValue(false)
        } else if (distanceFromEnd <= AUTO_SCROLL_DETACH_THRESHOLD_PX) {
          setSnapToTailPendingValue(true)
        } else {
          pauseFollowing()
        }
        return
      }
      if (distanceFromEnd <= PHYSICAL_TAIL_THRESHOLD_PX && !hasNewerEvents) {
        setSnapToTailPendingValue(false)
        setFollowingTail(true)
        return
      }
      if (distanceFromEnd <= AUTO_SCROLL_REATTACH_THRESHOLD_PX) {
        setSnapToTailPendingValue(true)
        return
      }
      setSnapToTailPendingValue(false)
      const lastVisibleIndex = instance.getVirtualItems().at(-1)?.index ?? -1
      if (hasNewerEvents && lastVisibleIndex >= virtualItems.length - HISTORY_LOAD_EDGE_ROWS - 1) {
        void requestNewerEvents()
      }
      return
    }

    if (!sync && !instance.isScrolling) {
      if (!loading && !error) requestOlderEventsIfUnderfilled()
      const shouldSnap = snapToTailPendingRef.current
      lastScrollDirectionRef.current = null
      userScrollIntentRef.current = null
      // Safari may not deliver native scrollend. The virtualizer's existing
      // idle notification must also recover a stale touch-detached state.
      if (reconcileSettledLiveTail(instance)) return
      if (!shouldSnap) return
      if (followingTailRef.current && !hasNewerEvents) {
        setSnapToTailPendingValue(false)
        scheduleTailScroll(instance)
      } else if (physicalDistanceFromEnd(scrollerElementRef.current) <= AUTO_SCROLL_REATTACH_THRESHOLD_PX) {
        void resumeFollowing(instance)
      } else {
        setSnapToTailPendingValue(false)
      }
    }
  }

  function updateVisibleSequenceRange(instance: TranscriptVirtualizer) {
    const renderedItems = instance.getVirtualItems()
    const viewportStart = instance.scrollOffset ?? 0
    const viewportHeight = instance.scrollRect?.height ?? 0
    const visibleRows = viewportHeight > 0
      ? renderedItems.filter((item) => item.end >= viewportStart && item.start <= viewportStart + viewportHeight)
      : renderedItems
    const visibleItems = visibleRows
      .flatMap((virtualItem) => {
        const item = virtualItems[virtualItem.index]
        return item?.kind === 'timeline' ? [item.item] : []
      })
    const first = visibleItems[0]
    const last = visibleItems.at(-1)
    const range = first && last ? { firstSeq: first.startSeq, lastSeq: last.endSeq } : null
    const key = range ? `${range.firstSeq}:${range.lastSeq}` : ''
    if (visibleSequenceRangeRef.current === key) return
    visibleSequenceRangeRef.current = key
    onVisibleSequenceRangeChange?.(range)
  }

  function updateScrollDebugReadout(instance: TranscriptVirtualizer) {
    const readout = scrollDebugReadoutRef.current
    const scroller = scrollerElementRef.current
    if (!scrollDebug || !readout || !scroller) return
    const maxScrollTop = Math.max(0, scroller.scrollHeight - scroller.clientHeight)
    const physicalDistance = physicalDistanceFromEnd(scroller)
    readout.textContent = [
      `inset ${composerClearanceHeight}px`,
      `tail ${TRANSCRIPT_BOTTOM_BREATHING_ROOM_PX}px`,
      `dist ${Math.round(physicalDistance)}px`,
      `top ${Math.round(scroller.scrollTop)}/${Math.round(maxScrollTop)}`,
      followingTailRef.current ? 'pinned' : 'paused',
      instance.scrollDirection ?? 'idle',
      scrollTouchActiveRef.current ? 'touch held' : 'no touch',
      scrollPointerActiveRef.current ? 'pointer held' : 'no pointer',
      `intent ${userScrollIntentRef.current ?? 'none'}`,
    ].join(' · ')
  }

  function recordUserScrollIntent(direction: 'forward' | 'backward') {
    userScrollIntentRef.current = direction
    lastScrollDirectionRef.current = direction
  }

  function reconcileSettledLiveTail(instance: TranscriptVirtualizer) {
    if (
      focusSeq > 0 ||
      hasNewerEvents ||
      scrollPointerActiveRef.current ||
      scrollTouchActiveRef.current ||
      physicalDistanceFromEnd(scrollerElementRef.current) > PHYSICAL_TAIL_THRESHOLD_PX
    ) return false

    // At the settled physical live tail, position wins over stale gesture
    // intent. Do not use the wider snap zone here: a small upward drag must
    // remain paused, and the end of a historical window is not the live tail.
    touchDetachedFromTailRef.current = false
    touchScrollRef.current = null
    lastScrollDirectionRef.current = null
    userScrollIntentRef.current = null
    setSnapToTailPendingValue(false)
    setFollowingTail(true)
    updateScrollDebugReadout(instance)
    return true
  }

  function reconcileTouchScrollIntent(distanceFromEnd: number) {
    const gesture = touchScrollRef.current
    if (!gesture) return
    const previousDistance = gesture.previousDistance
    gesture.previousDistance = distanceFromEnd
    // Native panning cancels pointer events, but momentum keeps scrolling. Use
    // physical progress for the rest of the gesture, even across virtualizer
    // settles. Requiring progress past its start ignores elastic rebounds.
    if (distanceFromEnd > previousDistance && distanceFromEnd > gesture.startDistance + PHYSICAL_TAIL_THRESHOLD_PX) {
      recordUserScrollIntent('backward')
      touchDetachedFromTailRef.current = true
      if (followingTailRef.current) pauseFollowing()
    } else if (distanceFromEnd < previousDistance && distanceFromEnd < gesture.startDistance - PHYSICAL_TAIL_THRESHOLD_PX) {
      recordUserScrollIntent('forward')
      touchDetachedFromTailRef.current = false
    }
  }

  function handleNativeScroll() {
    // Programmatic focus/measurement also emits native scroll events. A short
    // historical window can be at its own bottom without being the live tail.
    if (focusSeq > 0) return
    const distanceFromEnd = physicalDistanceFromEnd(scrollerElementRef.current)
    reconcileTouchScrollIntent(distanceFromEnd)
    if (distanceFromEnd <= AUTO_SCROLL_REATTACH_THRESHOLD_PX) {
      if (touchDetachedFromTailRef.current) return
      if (hasNewerEvents) {
        setSnapToTailPendingValue(true)
      } else {
        setSnapToTailPendingValue(false)
        setFollowingTail(true)
      }
    } else if (!followingTailRef.current) {
      setSnapToTailPendingValue(false)
    }
  }

  function handleNativeScrollEnd() {
    if (focusSeq > 0) return
    const distanceFromEnd = physicalDistanceFromEnd(scrollerElementRef.current)
    reconcileTouchScrollIntent(distanceFromEnd)
    touchScrollRef.current = null
    if (reconcileSettledLiveTail(virtualizer)) return
    if (touchDetachedFromTailRef.current) return
    if (distanceFromEnd > AUTO_SCROLL_REATTACH_THRESHOLD_PX) return
    if (hasNewerEvents) {
      void resumeFollowing(virtualizer)
    } else {
      setSnapToTailPendingValue(false)
      setFollowingTail(true)
    }
  }

  function handleScrollWheel(event: ReactWheelEvent<HTMLDivElement>) {
    if (event.deltaY === 0) return
    touchScrollRef.current = null
    const direction = event.deltaY > 0 ? 'forward' : 'backward'
    recordUserScrollIntent(direction)
  }

  function handleScrollPointerDown(event: ReactPointerEvent<HTMLDivElement>) {
    scrollPointerActiveRef.current = true
    scrollPointerYRef.current = event.clientY
    scrollPointerTypeRef.current = event.pointerType
    const distanceFromEnd = physicalDistanceFromEnd(scrollerElementRef.current)
    touchScrollRef.current = event.pointerType === 'touch' || event.pointerType === 'pen'
      ? { startDistance: distanceFromEnd, previousDistance: distanceFromEnd }
      : null
  }

  function handleScrollPointerMove(event: ReactPointerEvent<HTMLDivElement>) {
    const previousY = scrollPointerYRef.current
    if (!scrollPointerActiveRef.current || previousY === null) return
    const deltaY = event.clientY - previousY
    scrollPointerYRef.current = event.clientY
    if (Math.abs(deltaY) < 2) return
    const touchLike = scrollPointerTypeRef.current === 'touch' || scrollPointerTypeRef.current === 'pen'
    const direction = touchLike
      ? (deltaY > 0 ? 'backward' : 'forward')
      : (deltaY > 0 ? 'forward' : 'backward')
    recordUserScrollIntent(direction)
    if (touchLike) {
      touchDetachedFromTailRef.current = direction === 'backward'
      if (direction === 'backward' && followingTailRef.current) pauseFollowing()
    }
  }

  function handleScrollPointerEnd(event: ReactPointerEvent<HTMLDivElement>) {
    scrollPointerActiveRef.current = false
    scrollPointerYRef.current = null
    scrollPointerTypeRef.current = ''
    // A normal pointerup ends a tap; pointercancel hands panning to the browser,
    // so retain its scroll baseline until native scrollend or the next input.
    if (event.type === 'pointerup') {
      touchScrollRef.current = null
      if (!virtualizer.isScrolling) reconcileSettledLiveTail(virtualizer)
    }
  }

  function handleScrollTouchContact(event: ReactTouchEvent<HTMLDivElement>) {
    // Pointer cancellation starts native panning; it does not mean the finger
    // lifted. Avoid settling back into auto-follow while contact is still held.
    scrollTouchActiveRef.current = event.touches.length > 0
    if (!scrollTouchActiveRef.current && !virtualizer.isScrolling) {
      reconcileSettledLiveTail(virtualizer)
    }
  }

  function handleScrollKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    const backward = event.key === 'ArrowUp' || event.key === 'PageUp' || event.key === 'Home'
    const forward = event.key === 'ArrowDown' || event.key === 'PageDown' || event.key === 'End' || event.key === ' '
    if (!backward && !forward) return
    touchScrollRef.current = null
    const direction = backward ? 'backward' : 'forward'
    recordUserScrollIntent(direction)
    if (event.key === 'PageUp' || event.key === 'Home') pauseFollowing()
  }

  useLayoutEffect(() => {
    if (focusSeq > 0) pauseFollowing()
  }, [focusSeq, focusRequest, pauseFollowing])

  useLayoutEffect(() => {
    if (focusSeq > 0 || !initialTailPinPendingRef.current || loading || virtualItems.length === 0) return
    void resumeFollowing(virtualizer)
  }, [focusSeq, loading, resumeFollowing, virtualItems.length, virtualizer])

  useLayoutEffect(() => {
    updateVisibleSequenceRange(virtualizer)
    // A keyboard toggle must populate the panel even on an idle transcript.
    updateScrollDebugReadout(virtualizer)
  })

  useLayoutEffect(() => {
    if (loading || error || !hasOlderEvents || loadingOlderEvents || virtualItems.length === 0) return
    const frame = window.requestAnimationFrame(requestOlderEventsIfUnderfilled)
    return () => window.cancelAnimationFrame(frame)
  }, [
    error,
    hasOlderEvents,
    loading,
    loadingOlderEvents,
    requestOlderEventsIfUnderfilled,
    virtualItems.length,
  ])

  useLayoutEffect(() => {
    if (loading || focusSeq <= 0 || focusedVirtualIndex < 0) return
    virtualizer.scrollToIndex(focusedVirtualIndex, { align: 'center', behavior: 'auto' })
  }, [focusRequest, focusSeq, focusedVirtualIndex, loading, virtualizer])

  useEffect(() => () => onVisibleSequenceRangeChange?.(null), [onVisibleSequenceRangeChange])

  useLayoutEffect(() => {
    if (previousTimelineStructureRevisionRef.current === timelineStructureRevision) return
    previousTimelineStructureRevisionRef.current = timelineStructureRevision
    // Refresh hydration can replace a cached tool-only assistant row with the
    // durable version while preserving the row count and latest sequence. Pin
    // the replacement back to the live tail without clearing measurements for
    // the historical rows above it.
    if (
      initialTailPinPendingRef.current ||
      !followingTailRef.current ||
      hasNewerEvents ||
      virtualItems.length === 0
    ) return
    scheduleTailScroll(virtualizer)
  }, [hasNewerEvents, scheduleTailScroll, timelineStructureRevision, virtualItems.length, virtualizer])

  useLayoutEffect(() => {
    if (
      initialTailPinPendingRef.current ||
      !followingTailRef.current ||
      hasNewerEvents ||
      virtualItems.length === 0
    ) {
      return
    }
    if (physicalDistanceFromEnd(scrollerElementRef.current) <= PHYSICAL_TAIL_THRESHOLD_PX) return
    virtualizer.scrollToEnd({ behavior: 'auto' })
  }, [hasNewerEvents, liveTailRevision, virtualItems.length, virtualizer])

  const previousTailClearanceHeightRef = useRef(tailClearanceHeight)
  useLayoutEffect(() => {
    if (previousTailClearanceHeightRef.current === tailClearanceHeight) return
    previousTailClearanceHeightRef.current = tailClearanceHeight
    if (followingTailRef.current && !hasNewerEvents) scheduleTailScroll(virtualizer)
  }, [hasNewerEvents, scheduleTailScroll, tailClearanceHeight, virtualizer])

  if (loading && timeline.length === 0) {
    return (
      <div className="flex h-full items-center justify-center p-8 text-center text-sm text-muted-foreground">
        Loading chat history...
      </div>
    )
  }

  if (timeline.length === 0 && !activityStatus && !error) {
    return (
      <div className="flex h-full items-center justify-center p-8 text-center text-sm text-muted-foreground">
        {emptyMessage}
      </div>
    )
  }

  function updateChatGlow(event: ReactPointerEvent<HTMLDivElement>) {
    if (event.pointerType !== 'mouse' && event.pointerType !== 'pen') return

    const bounds = event.currentTarget.getBoundingClientRect()
    event.currentTarget.style.setProperty('--chat-glow-x', `${event.clientX - bounds.left}px`)
    event.currentTarget.style.setProperty('--chat-glow-y', `${event.clientY - bounds.top}px`)
    event.currentTarget.dataset.glowActive = 'true'
  }

  function hideChatGlow(event: ReactPointerEvent<HTMLDivElement>) {
    delete event.currentTarget.dataset.glowActive
  }

  return (
    <div
      className="chat-canvas relative h-full min-h-0 overflow-hidden"
      onPointerEnter={updateChatGlow}
      onPointerMove={updateChatGlow}
      onPointerLeave={hideChatGlow}
      onPointerCancel={hideChatGlow}
    >
      <div
        ref={scrollerElementRef}
        className="chat-scroll-area subtle-scrollbar flex h-full min-h-0 flex-col overflow-y-auto overscroll-y-none"
        role="log"
        aria-label="Chat messages"
        aria-live="polite"
        aria-relevant="additions text"
        aria-busy={loading || autoScroll}
        data-tail-clearance-height={tailClearanceHeight}
        onScrollCapture={handleNativeScroll}
        onScrollEndCapture={handleNativeScrollEnd}
        onWheelCapture={handleScrollWheel}
        onPointerDownCapture={handleScrollPointerDown}
        onPointerMoveCapture={handleScrollPointerMove}
        onPointerUpCapture={handleScrollPointerEnd}
        onPointerCancelCapture={handleScrollPointerEnd}
        onTouchStartCapture={handleScrollTouchContact}
        onTouchEndCapture={handleScrollTouchContact}
        onTouchCancelCapture={handleScrollTouchContact}
        onKeyDownCapture={handleScrollKeyDown}
      >
        {olderHistoryUnavailable ? (
          <p role="status" className="shrink-0 px-4 pb-3 pt-24 text-center text-xs text-muted-foreground">
            Older messages aren’t saved on this device. Reconnect to load more history.
          </p>
        ) : null}
        <div
          key="tanstack-direct-container-v1"
          ref={virtualizer.containerRef}
          data-testid="chat-transcript-virtual-content"
          className="relative mt-auto w-full shrink-0"
        >
          {virtualizer.getVirtualItems().map((virtualRow) => {
            const virtualItem = virtualItems[virtualRow.index]
            if (!virtualItem) return null
            let content: ReactNode
          if (virtualItem.kind === 'tail') {
              content = (
                <div
                  data-testid="chat-tail-breathing-room"
                  aria-hidden="true"
                  className={cn(scrollDebug && 'bg-fuchsia-500/80')}
                  style={{ height: `${TRANSCRIPT_BOTTOM_BREATHING_ROOM_PX}px` }}
                />
              )
            } else if (virtualItem.kind === 'activity') {
              content = (
              <div className={cn(
                'px-4 py-1.5',
                scrollDebug && 'bg-amber-400/20 outline outline-2 -outline-offset-2 outline-amber-400',
              )}>
                <ActivityIndicatorRow status={virtualItem.status} />
              </div>
            )
            } else if (virtualItem.kind === 'error') {
              content = (
              <div className="px-4 pt-3">
                <div
                  role="alert"
                  data-testid="chat-transcript-error"
                  className="mx-auto flex w-fit max-w-[min(36rem,100%)] items-center justify-center gap-2 rounded-lg border border-destructive/25 bg-destructive/[0.06] px-3 py-2 text-center text-xs shadow-sm"
                >
                  <CircleAlert className="size-3.5 shrink-0 text-destructive/80" aria-hidden="true" />
                  <span className="shrink-0 font-medium text-destructive/90">Chat issue</span>
                  <span className="min-w-0 break-words text-muted-foreground">{virtualItem.message}</span>
                </div>
              </div>
            )
            } else {
              const { item, timelineIndex } = virtualItem
              content = (
            <div
              data-transcript-row={item.id}
              className={cn(
                'px-4',
                timelineItemContainsSeq(item, focusSeq)
                  && 'mx-2 rounded-xl bg-primary/8 px-2 py-2 ring-2 ring-inset ring-primary/35',
                timelineIndex === 0 && !hasOlderEvents && topInset === 'sessionHeader' && 'lg:pt-24',
                timelineRowSpacing(
                  item,
                  timeline[timelineIndex - 1],
                  timelineIndex > 0 || hasOlderEvents || loadingOlderEvents,
                ),
              )}
            >
              <ChatTimelineRow
                item={item}
                focusSeq={focusSeq}
                collapseExtraTools={item.kind === 'message'}
                sessionID={sessionID}
                onOpenFilePath={onOpenFilePath}
                onFollowUp={onFollowUp}
              />
              {completion?.rowID === item.id ? <div data-viewed-completion aria-hidden="true" className="h-px w-full" /> : null}
            </div>
          )
            }
            return (
              <div
                key={virtualRow.key}
                ref={virtualizer.measureElement}
                data-index={virtualRow.index}
                className="absolute left-0 top-0 w-full"
              >
                {content}
              </div>
            )
          })}
        </div>
      </div>
      {scrollDebug ? (
        <>
          <div
            aria-hidden="true"
            className="pointer-events-none absolute inset-x-0 z-40 h-0.5 bg-cyan-400"
            style={{ bottom: `${composerClearanceHeight}px` }}
          />
          <div
            ref={scrollDebugReadoutRef}
            data-debug-scroll-readout="true"
            hidden
          >
            inset {composerClearanceHeight}px · tail {TRANSCRIPT_BOTTOM_BREATHING_ROOM_PX}px · waiting for scroll
          </div>
        </>
      ) : null}
      {(!followingTail || hasNewerEvents) && !snapToTailPending ? (
        <div
          className="pointer-events-none absolute inset-x-0 z-20 flex justify-center px-4"
          style={{ bottom: `${Math.max(16, bottomInsetHeight + 12)}px` }}
        >
          <button
            type="button"
            className="pointer-events-auto inline-flex h-9 items-center gap-2 rounded-full border border-border/70 bg-background/90 px-3.5 text-xs font-medium text-foreground shadow-lg shadow-black/10 backdrop-blur transition-colors hover:bg-background"
            aria-label="Scroll to latest and resume auto-scroll"
            onClick={() => void resumeFollowing(virtualizer)}
          >
            <ChevronDown className="size-3.5" aria-hidden="true" />
            Jump to latest
          </button>
        </div>
      ) : null}
    </div>
  )
}

function physicalDistanceFromEnd(scroller: HTMLDivElement | null) {
  if (!scroller) return Number.POSITIVE_INFINITY
  return Math.max(0, scroller.scrollHeight - scroller.clientHeight - scroller.scrollTop)
}

function estimateVirtualTimelineItem(item: VirtualTimelineItem | undefined) {
  if (!item) return 160
  if (item.kind === 'tail') return TRANSCRIPT_BOTTOM_BREATHING_ROOM_PX
  if (item.kind === 'activity') return 40
  if (item.kind === 'error') return 56
  if (item.item.kind === 'action' || item.item.kind === 'debug') return 56
  return 180
}

function ActivityIndicatorRow({ status }: { status: ChatActivityStatus }) {
  if (status.kind === 'thinking') {
    return <ThinkingIndicatorRow />
  }
  return <WorkingIndicatorRow since={status.since} />
}

function ThinkingIndicatorRow() {
  const gradientId = `thinking-gradient-${useId().replace(/:/g, '')}`

  return (
    <article className="flex justify-start">
      <div
        role="status"
        aria-label="Thinking"
        aria-live="polite"
        className="thinking-indicator inline-flex max-w-full sm:max-w-[min(48rem,90%)] items-center gap-2 px-1 py-0.5 text-sm font-medium"
      >
        <Brain className="thinking-indicator__icon size-4" aria-hidden="true" stroke={`url(#${gradientId})`}>
          <defs>
            <linearGradient id={gradientId} x1="0" y1="0" x2="24" y2="0" gradientUnits="userSpaceOnUse">
              <stop offset="0%" stopColor="hsl(var(--muted-foreground))" />
              <stop offset="42%" stopColor="hsl(var(--primary))" />
              <stop offset="58%" stopColor="hsl(var(--glow))" />
              <stop offset="100%" stopColor="hsl(var(--muted-foreground))" />
              <animateTransform
                attributeName="gradientTransform"
                type="translate"
                values="-24 0; 24 0; -24 0"
                dur="2.4s"
                repeatCount="indefinite"
              />
            </linearGradient>
          </defs>
        </Brain>
        <span className="thinking-indicator__text">Thinking</span>
      </div>
    </article>
  )
}

function WorkingIndicatorRow({ since }: { since: string }) {
  const label = useWorkingLabel(since)

  return (
    <article className="flex justify-start">
      <div
        role="status"
        aria-label={label}
        aria-live="polite"
        className="thinking-indicator inline-flex max-w-full sm:max-w-[min(48rem,90%)] items-center gap-2 px-1 py-0.5 text-sm font-medium"
      >
        <Loader2 className="size-4 animate-spin text-primary" aria-hidden="true" />
        <span className="thinking-indicator__text">{label}</span>
      </div>
    </article>
  )
}

function useWorkingLabel(since: string) {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    setNow(Date.now())
    const interval = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(interval)
  }, [since])

  const startedAt = Date.parse(since)
  if (!Number.isFinite(startedAt)) {
    return 'Working'
  }

  const elapsedSeconds = Math.max(0, Math.floor((now - startedAt) / 1000))
  if (elapsedSeconds < 1) {
    return 'Working'
  }
  return `Working for ${elapsedSeconds} ${elapsedSeconds === 1 ? 'second' : 'seconds'}`
}

function timelineRowSpacing(item: ChatTimelineItem, previous: ChatTimelineItem | undefined, hasPriorRow: boolean) {
  if (!hasPriorRow) {
    return ''
  }
  if (item.kind === 'debug' && previous?.kind === 'debug') {
    return 'mt-1'
  }
  if (item.kind === 'error' && previous?.kind === 'error') {
    return 'mt-2'
  }
  if (item.kind === 'debug' || previous?.kind === 'debug') {
    return 'mt-2'
  }
  if (item.kind === 'error' || previous?.kind === 'error') {
    return 'mt-3'
  }
  return 'mt-4'
}

function timelineItemContainsSeq(item: ChatTimelineItem, seq: number) {
  return seq > 0 && seq >= item.startSeq && seq <= item.endSeq
}

function ChatTimelineRow({
  item,
  collapseExtraTools,
  sessionID,
  onOpenFilePath,
  onFollowUp,
  focusSeq,
}: {
  item: ChatTimelineItem
  collapseExtraTools: boolean
  sessionID: string
  onOpenFilePath?: (path: string) => Promise<void> | void
  onFollowUp?: (prompt: string) => void
  focusSeq: number
}) {
  if (item.kind === 'action') {
    return <ActionBreakRow action={item.action} />
  }
  if (item.kind === 'debug') {
    return <DebugEventRow event={item.event} />
  }
  if (item.kind === 'error') {
    return <RunErrorRow error={item.error} />
  }
  return (
    <ChatMessageRow
      message={item.message}
      collapseExtraTools={collapseExtraTools}
      sessionID={sessionID}
      onOpenFilePath={onOpenFilePath}
      onFollowUp={onFollowUp}
      focusSeq={focusSeq}
    />
  )
}

function ActionBreakRow({ action }: { action: ChatActionBreak }) {
  return (
    <div className="py-1" role="separator" aria-label={action.label}>
      <div className="flex items-center gap-3">
        <div className="h-px flex-1 bg-border/70" aria-hidden="true" />
        <span className="rounded-full border border-border/70 bg-background/85 px-2.5 py-1 text-[11px] font-medium uppercase tracking-[0.12em] text-muted-foreground shadow-sm">
          {action.label}
        </span>
        <div className="h-px flex-1 bg-border/70" aria-hidden="true" />
      </div>
      {action.detail ? (
        <p
          className="mx-auto mt-1 max-w-[min(42rem,calc(100%-2rem))] truncate text-center font-mono text-[10px] text-muted-foreground"
          title={action.detail}
        >
          {action.detail}
        </p>
      ) : null}
    </div>
  )
}

function ChatMessageRow({
  message,
  collapseExtraTools,
  sessionID,
  onOpenFilePath,
  onFollowUp,
  focusSeq,
}: {
  message: ChatTranscriptMessage
  collapseExtraTools: boolean
  sessionID: string
  onOpenFilePath?: (path: string) => Promise<void> | void
  onFollowUp?: (prompt: string) => void
  focusSeq: number
}) {
  const user = message.role === 'user'
  const plan = message.variant === 'plan'
  const [showAllTools, setShowAllTools] = useState(false)
  const [messageCopied, setMessageCopied] = useState(false)
  const [messageCopyFailed, setMessageCopyFailed] = useState(false)
  const shouldCollapseTools = collapseExtraTools && message.tools.length > 3
  const visibleTools = !shouldCollapseTools || showAllTools ? message.tools : message.tools.slice(-3)
  const hasHiddenTools = message.tools.length > visibleTools.length
  const timestampValue = messageTimestamp(message)
  const timestamp = formatMessageTimestamp(timestampValue)
  const turnDuration = formatTurnDuration(message.durationMs)
  const showMessageCopy = Boolean(message.text)
  const focusedTool = message.tools.find((tool) => focusSeq >= tool.startSeq && focusSeq <= tool.endSeq)
  const images = message.tools.flatMap((tool) => tool.content.filter((content) => content.kind === 'image'))

  useEffect(() => {
    if (focusedTool) setShowAllTools(true)
  }, [focusedTool])

  async function handleMessageCopy() {
    if (!message.text) {
      return
    }
    setMessageCopyFailed(false)
    try {
      await copyText(message.text)
      setMessageCopied(true)
      window.setTimeout(() => setMessageCopied(false), 1200)
    } catch {
      setMessageCopied(false)
      setMessageCopyFailed(true)
    }
  }

  return (
    <article className={cn('flex', user ? 'justify-end' : 'justify-start')} data-message-variant={message.variant}>
      <div className="relative inline-block max-w-full sm:max-w-[min(48rem,90%)]">
        <div
          className={cn(
            'rounded-lg px-3.5 py-3 text-sm shadow-sm',
            user
              ? 'border border-primary/30 bg-primary text-primary-foreground shadow-[0_12px_30px_hsl(var(--primary)/0.16)]'
              : plan
                ? 'border border-l-4 border-amber-300/75 border-l-amber-400 bg-amber-50/85 text-amber-950 shadow-[0_12px_30px_hsl(43_96%_56%/0.12)] dark:border-amber-400/35 dark:border-l-amber-300 dark:bg-amber-400/10 dark:text-amber-100'
                : 'command-card border text-card-foreground',
          )}
        >
          {plan ? (
            <div className="mb-1.5 flex items-center gap-2 text-xs leading-none font-medium text-amber-800/90 dark:text-amber-200/90">
              {plan ? <ClipboardList className="size-3.5 shrink-0" aria-hidden="true" /> : null}
              <span>{message.label}</span>
            </div>
          ) : null}

          {message.skills.length > 0 ? <MessageSkills skills={message.skills} /> : null}

          {message.attachments.length > 0 ? <MessageAttachments attachments={message.attachments} /> : null}

          {message.text ? (
            <MarkdownContent
              content={message.text}
              variant={user ? 'inverted' : plan ? 'plan' : 'default'}
              renderCodexDirectives={!user}
              fileCitationBaseURL={sessionID && message.endSeq > 0
                ? `/api/sessions/${encodeURIComponent(sessionID)}/events/${message.endSeq}/file-citation`
                : undefined}
              onOpenFilePath={onOpenFilePath}
              onFollowUp={user ? undefined : onFollowUp}
            />
          ) : user && (message.attachments.length > 0 || message.skills.length > 0) ? null : (
            <p className="text-muted-foreground">Working...</p>
          )}
        </div>

        {timestamp ? (
          <div className="mt-0.5 flex min-h-4 flex-wrap items-center justify-end gap-0.5 px-1 text-[10px] leading-none text-muted-foreground/55">
            {turnDuration ? (
              <>
                <span className="tabular-nums" aria-label={`Total turn time ${turnDuration}`}>
                  {turnDuration}
                </span>
                <span aria-hidden="true">·</span>
              </>
            ) : null}
            <time className="font-normal tabular-nums" dateTime={timestampValue}>
              {timestamp}
            </time>
            {messageCopyFailed ? (
              <span role="alert" title={clipboardCopyErrorMessage} className="ml-1 whitespace-nowrap text-destructive">
                Copy failed
              </span>
            ) : null}
            {showMessageCopy ? (
              <button
                type="button"
                aria-label="Copy message"
                title={messageCopied ? 'Copied' : 'Copy message'}
                onClick={() => void handleMessageCopy()}
                className="inline-flex size-4 items-center justify-center rounded text-muted-foreground/45 transition-colors hover:bg-muted/55 hover:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {messageCopied ? (
                  <Check className="size-3" aria-hidden="true" />
                ) : (
                  <Copy className="size-3" aria-hidden="true" />
                )}
              </button>
            ) : null}
          </div>
        ) : null}

        {message.tools.length > 0 ? (
          <div
            className={cn(
              'mt-2 border-l pl-3',
              plan ? 'border-amber-300/70 dark:border-amber-400/35' : 'border-border/80',
            )}
          >
            {shouldCollapseTools ? (
              <button
                type="button"
                className="relative z-10 -ml-1 flex min-h-6 w-fit items-center gap-0.5 rounded px-1 py-1 text-[10px] font-normal leading-4 text-muted-foreground/65 hover:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-expanded={showAllTools}
                onClick={() => setShowAllTools((current) => !current)}
              >
                {hasHiddenTools ? (
                  <ChevronRight className="size-2.5" aria-hidden="true" />
                ) : (
                  <ChevronDown className="size-2.5" aria-hidden="true" />
                )}
                {hasHiddenTools ? `Show ${message.tools.length - visibleTools.length} Earlier` : 'Show Less'}
              </button>
            ) : null}
            {visibleTools.map((tool) => (
              <ToolCallRow
                key={tool.id}
                tool={tool}
                onOpenFilePath={onOpenFilePath}
                focused={tool.id === focusedTool?.id}
              />
            ))}
          </div>
        ) : null}
        {images.length > 0 ? <ToolResultContent content={images} className="mt-2" largeImages /> : null}
      </div>
    </article>
  )
}

function MessageSkills({ skills }: { skills: ChatTranscriptMessage['skills'] }) {
  return (
    <div className="mb-2 flex flex-wrap gap-1.5" aria-label="Invoked skills">
      {skills.map((skill) => (
        <span
          key={`${skill.name}\u0000${skill.path}`}
          className="rounded-full border border-current/25 bg-background/15 px-2 py-0.5 font-mono text-[11px] font-medium"
          title={skill.path}
        >
          ${skill.name}
        </span>
      ))}
    </div>
  )
}

function MessageAttachments({ attachments }: { attachments: ChatTranscriptAttachment[] }) {
  return (
    <div className="mb-2 flex flex-wrap gap-2">
      {attachments.map((attachment, index) => (
        <ImageAttachmentPreview key={`${attachment.name}-${index}`} attachment={attachment} />
      ))}
    </div>
  )
}

function ImageAttachmentPreview({ attachment, large = false }: { attachment: ChatTranscriptAttachment; large?: boolean }) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <button
          type="button"
          className="block overflow-hidden rounded-md border border-border/70 bg-background/80 transition-colors hover:border-border focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label={`Preview ${attachment.name}`}
        >
          <img
            src={attachment.sourceURL}
            alt={attachment.name}
            className={large ? 'max-h-96 max-w-full object-contain' : 'h-24 w-24 object-cover'}
          />
        </button>
      </DialogTrigger>
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[calc(100vw-2rem)] gap-4 overflow-hidden p-4 sm:max-w-5xl">
        <div className="flex min-w-0 items-start justify-between gap-3 pr-8">
          <div className="min-w-0">
            <DialogTitle className="truncate text-base">{attachment.name}</DialogTitle>
            <DialogDescription>{formatAttachmentSize(attachment.sizeBytes) || attachment.mediaType}</DialogDescription>
          </div>
          <a
            href={attachment.sourceURL}
            download={attachment.name}
            className="inline-flex h-9 shrink-0 items-center gap-2 rounded-md border border-border/80 bg-background px-3 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Download className="size-4" aria-hidden="true" />
            Download
          </a>
        </div>
        <div className="flex min-h-0 max-h-[calc(100dvh-9rem)] items-center justify-center overflow-auto rounded-md border border-border/70 bg-black/90">
          <img
            src={attachment.sourceURL}
            alt={attachment.name}
            className="max-h-[calc(100dvh-10rem)] max-w-full object-contain"
          />
        </div>
      </DialogContent>
    </Dialog>
  )
}

function formatAttachmentSize(sizeBytes: number) {
  if (!Number.isFinite(sizeBytes) || sizeBytes <= 0) {
    return ''
  }
  if (sizeBytes < 1024) {
    return `${sizeBytes} B`
  }
  if (sizeBytes < 1024 * 1024) {
    return `${(sizeBytes / 1024).toFixed(1)} KB`
  }
  return `${(sizeBytes / (1024 * 1024)).toFixed(1)} MB`
}

function sameLocalCalendarDay(left: Date, right: Date) {
  return (
    left.getFullYear() === right.getFullYear() &&
    left.getMonth() === right.getMonth() &&
    left.getDate() === right.getDate()
  )
}

function messageTimestamp(message: ChatTranscriptMessage) {
  return message.role === 'assistant' && !message.streaming && message.completedAt
    ? message.completedAt
    : message.createdAt
}

function formatTurnDuration(durationMs: number | null) {
  if (durationMs === null || !Number.isFinite(durationMs) || durationMs < 0) return ''
  const totalSeconds = Math.round(durationMs / 1000)
  if (totalSeconds < 60) return `${totalSeconds} sec`
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  if (minutes < 60) return seconds ? `${minutes} min ${seconds} sec` : `${minutes} min`
  const hours = Math.floor(minutes / 60)
  const remainingMinutes = minutes % 60
  return remainingMinutes ? `${hours} hr ${remainingMinutes} min` : `${hours} hr`
}

function formatMessageTimestamp(value: string, includeDate = false) {
  if (!value) {
    return ''
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return ''
  }
  const options: Intl.DateTimeFormatOptions = includeDate || !sameLocalCalendarDay(date, new Date())
    ? { dateStyle: 'short', timeStyle: 'short' }
    : { timeStyle: 'short' }
  return new Intl.DateTimeFormat(undefined, options).format(date)
}

type MarkdownVariant = 'default' | 'inverted' | 'plan'

const MarkdownContent = memo(function MarkdownContent({
  content,
  variant,
  renderCodexDirectives,
  fileCitationBaseURL,
  onOpenFilePath,
  onFollowUp,
}: {
  content: string
  variant: MarkdownVariant
  renderCodexDirectives: boolean
  fileCitationBaseURL?: string
  onOpenFilePath?: (path: string) => Promise<void> | void
  onFollowUp?: (prompt: string) => void
}) {
  const inverted = variant === 'inverted'
  const plan = variant === 'plan'
  const contentRef = useRef<HTMLDivElement>(null)
  const latestContentRef = useRef(content)
  const fileCitationBaseURLRef = useRef(fileCitationBaseURL)
  const [displayedContent, setDisplayedContent] = useState(content)

  useLayoutEffect(() => {
    fileCitationBaseURLRef.current = fileCitationBaseURL
    latestContentRef.current = content
    if (content === displayedContent || hasActiveSelectionWithin(contentRef.current)) return
    setDisplayedContent(content)
  }, [content, displayedContent, fileCitationBaseURL])

  useEffect(() => {
    function flushHeldContent() {
      if (hasActiveSelectionWithin(contentRef.current)) return
      setDisplayedContent((current) => current === latestContentRef.current ? current : latestContentRef.current)
    }

    document.addEventListener('selectionchange', flushHeldContent)
    return () => document.removeEventListener('selectionchange', flushHeldContent)
  }, [])

  const components = useMemo<Components>(
    () => ({
        button: ({ children, node }) => {
          const prompt = node?.properties['data-codex-followup']
          if (typeof prompt !== 'string' || !onFollowUp) return <span>{children}</span>
          return (
            <button
              type="button"
              title={prompt}
              className="my-1 inline-flex min-h-9 max-w-full items-center gap-2 rounded-lg border border-primary/25 bg-primary/5 px-3 py-1.5 text-left text-sm font-medium text-primary transition-colors hover:bg-primary/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              onClick={() => onFollowUp(prompt)}
            >
              <span className="min-w-0 whitespace-pre-wrap break-words">{children}</span>
              <ArrowUpRight className="size-4 shrink-0" aria-hidden="true" />
            </button>
          )
        },
        p: ({ children }) => (
          <p className="my-2 first:mt-0 last:mb-0 whitespace-pre-wrap break-words leading-relaxed">{children}</p>
        ),
        a: ({ children, href, title, node }) => {
          const citedPath = node?.properties['data-codex-file-citation']
          const citationBaseURL = fileCitationBaseURLRef.current
          const citationHref = typeof citedPath === 'string' && citationBaseURL
            ? `${citationBaseURL}?${new URLSearchParams({ path: citedPath })}`
            : ''
          const filePath = markdownFilePathFromHref(href)
          const opensFileEditor = Boolean(!citationHref && filePath && onOpenFilePath)

          return (
            <a
              href={citationHref || href}
              title={title}
              target={opensFileEditor ? undefined : '_blank'}
              rel={opensFileEditor ? undefined : 'noreferrer'}
              className={cn(
                'underline underline-offset-2',
                inverted ? 'text-primary-foreground' : plan ? 'text-amber-700 dark:text-amber-200' : 'text-primary',
              )}
              onClick={
                opensFileEditor
                  ? (event) => {
                      event.preventDefault()
                      void onOpenFilePath?.(filePath)
                    }
                  : undefined
              }
            >
              {children}
            </a>
          )
        },
        ul: ({ children }) => <ul className="my-2 list-disc space-y-1 pl-5">{children}</ul>,
        ol: ({ children }) => <ol className="my-2 list-decimal space-y-1 pl-5">{children}</ol>,
        li: ({ children }) => <li className="leading-relaxed">{children}</li>,
        blockquote: ({ children }) => (
          <blockquote
            className={cn(
              'my-2 border-l-2 pl-3 italic',
              inverted
                ? 'border-primary-foreground/50'
                : plan
                  ? 'border-amber-400/70 text-amber-900/80 dark:border-amber-300/50 dark:text-amber-100/80'
                  : 'border-border text-muted-foreground',
            )}
          >
            {children}
          </blockquote>
        ),
        code: ({ children, className }) => {
          const code = childrenToString(children)
          const block = className?.startsWith('language-') || code.endsWith('\n')
          if (block) {
            return (
              <CodeBlock code={code} className={className} variant={variant}>
                {children}
              </CodeBlock>
            )
          }
          return (
            <code
              className={cn(
                'rounded border px-1 py-0.5 font-mono text-[0.85em]',
                inverted
                  ? 'border-primary-foreground/20 bg-primary-foreground/15'
                  : plan
                    ? 'border-amber-300/70 bg-amber-100/85 text-amber-950 dark:border-amber-300/35 dark:bg-amber-300/14 dark:text-amber-50'
                    : 'border-transparent bg-muted',
              )}
            >
              {children}
            </code>
          )
        },
        pre: ({ children }) => <>{children}</>,
        h1: ({ children }) => <h3 className="mb-2 mt-3 text-base font-semibold first:mt-0">{children}</h3>,
        h2: ({ children }) => <h3 className="mb-2 mt-3 text-base font-semibold first:mt-0">{children}</h3>,
        h3: ({ children }) => <h3 className="mb-2 mt-3 text-sm font-semibold first:mt-0">{children}</h3>,
        hr: () => (
          <hr
            className={cn(
              'my-3 border-t',
              inverted ? 'border-primary-foreground/30' : plan ? 'border-amber-300/70' : 'border-border',
            )}
          />
        ),
        table: ({ children }) => (
          <div className="my-2 overflow-auto">
            <table className="w-full border-collapse text-xs">{children}</table>
          </div>
        ),
        th: ({ children }) => <th className="border px-2 py-1 text-left font-medium">{children}</th>,
        td: ({ children }) => <td className="border px-2 py-1 align-top">{children}</td>,
    }),
    [inverted, onOpenFilePath, onFollowUp, plan, variant],
  )

  return (
    <div ref={contentRef} className="contents">
      <ReactMarkdown
        remarkPlugins={renderCodexDirectives && (onFollowUp || onOpenFilePath)
          ? [remarkGfm, remarkDirective, remarkCodexDirectives]
          : [remarkGfm]}
        components={components}
      >
        {displayedContent}
      </ReactMarkdown>
    </div>
  )
})

function hasActiveSelectionWithin(element: HTMLElement | null) {
  if (!element) return false
  const selection = window.getSelection()
  if (!selection || selection.isCollapsed || selection.rangeCount === 0) return false
  return element.contains(selection.anchorNode) || element.contains(selection.focusNode)
}

function markdownFilePathFromHref(href: string | undefined) {
  let value = href?.trim() ?? ''
  if (!value || value.startsWith('#')) {
    return ''
  }

  if (value.startsWith('file://')) {
    value = value.slice('file://'.length)
  } else if (/^[a-z][a-z0-9+.-]*:/i.test(value) && !/^[a-z]:[\\/]/i.test(value)) {
    try {
      const url = new URL(value)
      if (url.origin !== window.location.origin) {
        return ''
      }
      value = `${url.pathname}${url.search}${url.hash}`
    } catch {
      return ''
    }
  }

  try {
    value = decodeURI(value)
  } catch {
    // Keep the original href if it is not valid URI-encoded text.
  }

  value =
    value
      .split('#')[0]
      ?.split('?')[0]
      ?.replaceAll('\\', '/')
      .replace(/:\d+(?::\d+)?$/, '') ?? ''
  if (!value || value.endsWith('/')) {
    return ''
  }

  return looksLikeWorkspaceFileLink(value) ? value : ''
}

function looksLikeWorkspaceFileLink(path: string) {
  if (path.startsWith('./') || path.startsWith('../')) {
    return true
  }
  if (path.startsWith('/')) {
    return (
      ['/Users/', '/home/', '/repo/', '/workspace/', '/workspaces/', '/private/', '/tmp/', '/var/'].some((prefix) =>
        path.startsWith(prefix),
      ) && looksLikeFileName(path)
    )
  }
  return looksLikeFileName(path)
}

function looksLikeFileName(path: string) {
  const name = path.split('/').at(-1) ?? ''
  return (
    /^(AGENTS\.md|README(?:\.[\w-]+)?|Makefile|Dockerfile|go\.mod|go\.sum|package\.json|pnpm-lock\.yaml|yarn\.lock|\.env(?:\.[\w-]+)?)$/i.test(
      name,
    ) || /\.[A-Za-z0-9][A-Za-z0-9_-]{0,15}$/.test(name)
  )
}

function CodeBlock({
  code,
  className,
  variant,
  children,
}: {
  code: string
  className?: string
  variant: MarkdownVariant
  children: ReactNode
}) {
  return (
    <div className="group/code relative my-2">
      <FloatingCopyButton label="Copy code" value={code} variant={variant} />
      <pre className="overflow-auto rounded-md">
        <code
          className={cn(
            'block min-w-full whitespace-pre border p-3 pr-12 font-mono text-xs',
            variant === 'inverted'
              ? 'border-primary-foreground/20 bg-primary-foreground/15'
              : variant === 'plan'
                ? 'border-amber-300/70 bg-amber-100/80 text-amber-950 dark:border-amber-300/35 dark:bg-amber-300/12 dark:text-amber-50'
                : 'border-transparent bg-muted',
            className,
          )}
        >
          {children}
        </code>
      </pre>
    </div>
  )
}

function FloatingCopyButton({
  label,
  value,
  variant = 'default',
}: {
  label: string
  value: string
  variant?: MarkdownVariant
}) {
  const [copied, setCopied] = useState(false)
  const [copyFailed, setCopyFailed] = useState(false)

  async function handleCopy() {
    setCopyFailed(false)
    try {
      await copyText(value)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1200)
    } catch {
      setCopied(false)
      setCopyFailed(true)
    }
  }

  return (
    <>
      {copyFailed ? (
        <span
          role="alert"
          title={clipboardCopyErrorMessage}
          className="absolute right-12 top-2 z-10 whitespace-nowrap rounded-md border border-destructive/30 bg-background/95 px-2 py-1 text-[11px] font-medium text-destructive shadow-sm"
        >
          Copy failed
        </span>
      ) : null}
      <button
        type="button"
        aria-label={label}
        title={copyFailed ? clipboardCopyErrorMessage : label}
        onClick={() => void handleCopy()}
        className={cn(
          'absolute right-4 top-2 z-10 inline-flex size-7 items-center justify-center rounded-md border text-xs shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
          variant === 'inverted'
            ? 'border-primary-foreground/20 bg-primary-foreground/12 text-primary-foreground hover:bg-primary-foreground/20'
            : variant === 'plan'
              ? 'border-amber-300/70 bg-amber-50/95 text-amber-800 hover:bg-amber-100 dark:border-amber-300/35 dark:bg-amber-950/80 dark:text-amber-100 dark:hover:bg-amber-900'
              : 'border-border/70 bg-background/90 text-muted-foreground hover:bg-background hover:text-foreground',
        )}
      >
        {copied ? <Check className="size-3.5" aria-hidden="true" /> : <Copy className="size-3.5" aria-hidden="true" />}
      </button>
    </>
  )
}

function childrenToString(children: ReactNode): string {
  if (typeof children === 'string' || typeof children === 'number') {
    return String(children)
  }
  if (Array.isArray(children)) {
    return children.map(childrenToString).join('')
  }
  if (isValidElement<{ children?: ReactNode }>(children)) {
    return childrenToString(children.props.children)
  }
  return ''
}

function ToolCallRow({
  tool,
  onOpenFilePath,
  focused = false,
}: {
  tool: ChatTranscriptTool
  onOpenFilePath?: (path: string) => Promise<void> | void
  focused?: boolean
}) {
  const [loadedOutput, setLoadedOutput] = useState('')
  const [loadingFullOutput, setLoadingFullOutput] = useState(false)
  const [fullOutputError, setFullOutputError] = useState('')
  const output = loadedOutput || tool.error || tool.text
  const detailContent = tool.content.filter((content) => content.kind !== 'image')
  const hasDetails = Boolean(output || detailContent.length > 0 || tool.fullOutputURL)
  const [outputOpen, setOutputOpen] = useState(false)
  const name = tool.label.replace(/^Tool:\s*/, '')
  const statusDotClassName = toolStatusDotClassName(tool)
  const filePath = tool.kind === 'file-change' ? (tool.paths[0] ?? '') : ''
  const showFileEditorAction = Boolean(onOpenFilePath && filePath && output && !tool.error && looksLikeDiff(output))

  useEffect(() => {
    if (focused && hasDetails) setOutputOpen(true)
  }, [focused, hasDetails])

  return (
    <div
      className={cn('rounded text-xs', focused && 'bg-primary/10 ring-2 ring-primary/45')}
      data-search-focused={focused || undefined}
    >
      <button
        type="button"
        className="relative z-10 flex h-5 w-full min-w-0 touch-manipulation items-center gap-1 rounded py-0.5 text-left font-mono focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default"
        aria-expanded={hasDetails ? outputOpen : undefined}
        aria-label={hasDetails ? `${outputOpen ? 'Collapse' : 'Expand'} ${name}` : name}
        disabled={!hasDetails}
        onClick={() => {
          if (hasDetails) {
            setOutputOpen((current) => !current)
          }
        }}
      >
        {hasDetails ? (
          outputOpen ? (
            <ChevronDown className="pointer-events-none size-3 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="pointer-events-none size-3 shrink-0 text-muted-foreground" />
          )
        ) : (
          <span className="pointer-events-none size-3 shrink-0" aria-hidden="true" />
        )}
        {statusDotClassName ? (
          <span
            className={cn('pointer-events-none mr-0.5 size-2 shrink-0 rounded-full', statusDotClassName)}
            aria-hidden="true"
          />
        ) : null}
        <span className="pointer-events-none min-w-0 flex-1 truncate font-medium">{name}</span>
      </button>
      {hasDetails ? (
        outputOpen ? (
          <div className="relative ml-5 mt-1">
            {output ? <FloatingCopyButton label="Copy tool output" value={output} /> : null}
            {showFileEditorAction ? <FloatingOpenFileButton path={filePath} onOpenFilePath={onOpenFilePath} /> : null}
            {output ? (
              <ToolOutput
                output={output}
                error={Boolean(tool.error)}
                diff={tool.kind === 'file-change'}
                actionPadding={showFileEditorAction}
              />
            ) : null}
            {tool.fullOutputURL && !loadedOutput ? (
              <button
                type="button"
                className="mt-1 inline-flex items-center gap-1 rounded px-1.5 py-1 text-[11px] text-primary hover:bg-primary/10 disabled:opacity-60"
                disabled={loadingFullOutput}
                onClick={async () => {
                  setLoadingFullOutput(true)
                  setFullOutputError('')
                  try {
                    const response = await fetch(tool.fullOutputURL)
                    if (!response.ok) throw new Error(`HTTP ${response.status}`)
                    setLoadedOutput(await response.text())
                  } catch {
                    setFullOutputError('Could not load full output')
                  } finally {
                    setLoadingFullOutput(false)
                  }
                }}
              >
                {loadingFullOutput ? (
                  <Loader2 className="size-3 animate-spin" aria-hidden="true" />
                ) : (
                  <Download className="size-3" aria-hidden="true" />
                )}
                {loadingFullOutput ? 'Loading…' : 'Load full output'}
              </button>
            ) : null}
            {fullOutputError ? <p className="mt-1 text-[11px] text-destructive">{fullOutputError}</p> : null}
            {detailContent.length > 0 ? (
              <ToolResultContent content={detailContent} className={output ? 'mt-2' : ''} />
            ) : null}
          </div>
        ) : null
      ) : null}
    </div>
  )
}

function ToolResultContent({ content, className, largeImages = false }: {
  content: ChatTranscriptToolContent[]
  className?: string
  largeImages?: boolean
}) {
  return (
    <div className={cn('flex flex-wrap gap-2', className)}>
      {content.map((item, index) => {
        if (item.kind === 'image') {
          return (
            <ImageAttachmentPreview
              key={`${item.kind}-${item.sourceURL}-${index}`}
              large={largeImages}
              attachment={{
                name: item.name,
                mediaType: item.mediaType,
                dataURL: '',
                sourceURL: item.sourceURL,
                sizeBytes: 0,
              }}
            />
          )
        }
        if (item.kind === 'audio') {
          return (
            <div
              key={`${item.kind}-${item.sourceURL}-${index}`}
              className="min-w-0 max-w-full rounded border border-border/60 bg-surface-muted/70 p-2"
            >
              <p className="mb-1 truncate font-medium text-muted-foreground">{item.name}</p>
              <audio controls preload="none" src={item.sourceURL} className="h-8 max-w-full" />
            </div>
          )
        }
        return <ToolResourceLink key={`${item.kind}-${item.uri}-${item.sourceURL}-${index}`} content={item} />
      })}
    </div>
  )
}

function ToolResourceLink({ content }: { content: ChatTranscriptToolContent }) {
  const externalURL = safeExternalURL(content.uri)
  const href = content.sourceURL || externalURL
  const download = content.sourceURL ? content.name : undefined
  const body = (
    <>
      <span className="block truncate font-medium text-foreground">{content.name}</span>
      {content.description ? <span className="block text-muted-foreground">{content.description}</span> : null}
      {content.mediaType || content.uri ? (
        <span className="block truncate font-mono text-[11px] text-muted-foreground/75">
          {[content.mediaType, content.uri].filter(Boolean).join(' · ')}
        </span>
      ) : null}
    </>
  )
  const className =
    'min-w-0 max-w-full rounded border border-border/60 bg-surface-muted/70 px-3 py-2 text-left transition-colors'

  if (!href) {
    return <div className={className}>{body}</div>
  }
  return (
    <a
      href={href}
      download={download}
      target={externalURL && !content.sourceURL ? '_blank' : undefined}
      rel={externalURL && !content.sourceURL ? 'noreferrer' : undefined}
      className={cn(className, 'hover:border-border hover:bg-surface-muted')}
    >
      {body}
    </a>
  )
}

function safeExternalURL(value: string) {
  if (!value) {
    return ''
  }
  try {
    const url = new URL(value)
    return url.protocol === 'http:' || url.protocol === 'https:' ? value : ''
  } catch {
    return ''
  }
}

function FloatingOpenFileButton({
  path,
  onOpenFilePath,
}: {
  path: string
  onOpenFilePath?: (path: string) => Promise<void> | void
}) {
  return (
    <button
      type="button"
      aria-label="Show in File Editor"
      onClick={() => void onOpenFilePath?.(path)}
      className="absolute right-10 top-2 z-10 inline-flex size-7 items-center justify-center rounded-md border border-border/70 bg-background/90 text-xs text-muted-foreground shadow-sm transition-colors hover:bg-background hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <FileText className="size-3.5" aria-hidden="true" />
    </button>
  )
}

function ToolOutput({
  output,
  error,
  diff,
  actionPadding = false,
}: {
  output: string
  error: boolean
  diff: boolean
  actionPadding?: boolean
}) {
  if (!diff || error || !looksLikeDiff(output)) {
    return (
      <pre
        className={cn(
          'max-h-44 overflow-auto whitespace-pre-wrap break-words rounded border border-border/60 bg-surface-muted/70 p-2 font-mono leading-relaxed text-muted-foreground',
          actionPadding ? 'pr-20' : 'pr-12',
          error && 'text-destructive',
        )}
      >
        {output}
      </pre>
    )
  }

  return (
    <pre
      className={cn(
        'max-h-64 overflow-auto rounded border border-border/60 bg-surface-muted/70 p-2 font-mono leading-relaxed text-muted-foreground',
        actionPadding ? 'pr-20' : 'pr-12',
      )}
    >
      {output.split('\n').map((line, index) => (
        <span
          key={`${index}-${line}`}
          className={cn('block min-h-[1.25em] min-w-full w-max whitespace-pre', diffLineClassName(line))}
        >
          {line || ' '}
        </span>
      ))}
    </pre>
  )
}

function looksLikeDiff(output: string) {
  return output
    .split('\n')
    .some(
      (line) =>
        line.startsWith('@@') ||
        line.startsWith('--- ') ||
        line.startsWith('+++ ') ||
        line.startsWith('-') ||
        line.startsWith('+'),
    )
}

function diffLineClassName(line: string) {
  if (line.startsWith('+') && !line.startsWith('+++')) {
    return 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-300'
  }
  if (line.startsWith('-') && !line.startsWith('---')) {
    return 'bg-destructive/10 text-destructive'
  }
  if (line.startsWith('@@')) {
    return 'bg-primary/10 text-primary'
  }
  return ''
}

function DebugEventRow({ event }: { event: ChatDebugEvent }) {
  const [open, setOpen] = useState(false)
  const sequence = event.startSeq === event.endSeq ? `#${event.startSeq}` : `#${event.startSeq}-${event.endSeq}`
  const payload = JSON.stringify(event.payload, null, 2)

  return (
    <article className="flex justify-start">
      <div className="max-w-full sm:max-w-[min(48rem,90%)] border-l border-border/70 pl-3 text-xs">
        <button
          type="button"
          className="flex w-full min-w-0 items-center gap-1 rounded py-0.5 text-left font-mono text-muted-foreground/75 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-expanded={open}
          aria-label={`${open ? 'Collapse' : 'Expand'} ${event.label}`}
          onClick={() => setOpen((current) => !current)}
        >
          {open ? (
            <ChevronDown className="size-3 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="size-3 shrink-0 text-muted-foreground" />
          )}
          <span className="min-w-0 flex-1 truncate">{event.label}</span>
          <span className="shrink-0 text-[11px] capitalize text-muted-foreground/70">{event.status}</span>
          <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground/55">{sequence}</span>
          {event.eventCount > 1 ? (
            <span className="shrink-0 text-[11px] text-muted-foreground/55">{event.eventCount} events</span>
          ) : null}
        </button>
        {event.text || event.error ? (
          <p
            className={cn(
              'ml-5 mt-0.5 whitespace-pre-wrap break-words font-mono text-[11px] text-muted-foreground/70',
              event.error && 'text-destructive/85',
            )}
          >
            {event.error || event.text}
          </p>
        ) : null}
        {open ? (
          <div className="relative ml-5 mt-1">
            <FloatingCopyButton label="Copy debug payload" value={payload} />
            <pre className="max-h-44 overflow-auto whitespace-pre-wrap break-words rounded border border-border/60 bg-surface-muted/70 p-2 pr-12 font-mono leading-relaxed text-muted-foreground">
              {payload}
            </pre>
          </div>
        ) : null}
      </div>
    </article>
  )
}

function RunErrorRow({ error }: { error: ChatRunError }) {
  const sequence = error.startSeq === error.endSeq ? `#${error.startSeq}` : `#${error.startSeq}-${error.endSeq}`
  const timestamp = formatMessageTimestamp(error.createdAt, true)

  return (
    <article className="flex justify-start" role="alert" aria-label={`${error.label}: ${error.error}`}>
      <div className="relative max-w-full sm:max-w-[min(48rem,90%)] rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 pr-12 text-sm text-destructive shadow-sm">
        <FloatingCopyButton label="Copy error" value={error.error} />
        <div className="flex min-w-0 items-center gap-2 text-xs font-medium">
          <span className="min-w-0 flex-1 truncate">{error.label}</span>
          <span className="shrink-0 text-[11px] capitalize text-destructive/75">{error.status}</span>
          <span className="shrink-0 text-[11px] tabular-nums text-destructive/65">{sequence}</span>
          {timestamp ? (
            <time
              className="hidden shrink-0 text-[11px] font-normal tabular-nums text-destructive/65 sm:inline"
              dateTime={error.createdAt}
            >
              {timestamp}
            </time>
          ) : null}
        </div>
        <p className="mt-1 whitespace-pre-wrap break-words font-mono text-xs leading-relaxed text-destructive">
          {error.error}
        </p>
      </div>
    </article>
  )
}

function toolStatusDotClassName(tool: ChatTranscriptTool) {
  if (tool.error || tool.status === 'failed') return 'bg-destructive'
  if (tool.status !== 'completed') return 'tool-activity-dot'
  return ''
}
