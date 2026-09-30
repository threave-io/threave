import { Folder, Menu, MessageSquare, MoreHorizontal, Plus, RefreshCw, Settings, Square, Terminal, WifiOff, X } from 'lucide-react'
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react'
import './App.css'
import type {
  AgentEvent,
  AgentType,
  MessageAttachment,
  Session,
  SessionAgentOptions,
  SessionRuntimeAgentOptions,
  SkillReference,
  SpotlightSearchResult,
  SubmitAgentOptions,
  UpdateSessionRuntimeAgentOptionsResponse,
  UserInputAnswers,
  WorkspaceFileContent,
} from '@/lib/api'
import {
  APIError,
  answerUserInput,
  resolvePermission,
  archiveSession,
  cancelSession,
  clearSession,
  clearAllSessionNotificationAttention,
  clearSessionNotificationAttention,
  compactSession,
  createSession,
  getSession,
  getSessionSnapshot,
  getSessionFileContent,
  restoreSession,
  sessionActivityStreamURL,
  submitMessage,
  updateSessionAgentOptions,
  updateSessionParent,
  updateSessionPin,
  updateSessionRuntimeAgentOptions,
  updateSessionTitle,
  updateSessionWorkspace,
  watchSessionActivity,
} from '@/lib/api'
import {
  appendEvent,
  isDebugOnlyEvent,
  isTransientEvent,
  isTerminalEvent,
  knownEventTypes,
  lastSeq,
  shouldRefreshWorkspaceFilesForEvent,
  statusFromEvent,
} from '@/lib/events'
import { invalidateSessionEventTails, useSessionEvents, type StreamState } from '@/hooks/use-session-events'
import { useAppBadge } from '@/hooks/use-app-badge'
import { useFavicon } from '@/hooks/use-favicon'
import { usePushNotifications } from '@/hooks/use-push-notifications'
import { useReleaseUpdate } from '@/hooks/use-release-update'
import { useClientPerformanceTelemetry } from '@/hooks/use-client-performance-telemetry'
import { useRailContentPreference } from '@/hooks/use-rail-content'
import { useTheme } from '@/hooks/use-theme'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { AppMenu } from '@/components/app-menu'
import { CreateSessionDialog } from '@/components/create-session-dialog'
import { MoveSessionDialog } from '@/components/move-session-dialog'
import { DashboardOverview } from '@/components/dashboard-overview'
import { HostConsole, type ConsoleActions } from '@/components/host-console'
import { NotificationsPopover } from '@/components/notifications-popover'
import { RunHealthRail } from '@/components/run-health-rail'
import { ChatSessionHeader, SessionDetail } from '@/components/session-detail'
import { SessionList } from '@/components/session-list'
import { SessionSettingsPage } from '@/components/session-settings-page'
import { SpotlightSearch } from '@/components/spotlight-search'
import { WorkspaceFilesView } from '@/components/workspace-files'
import { hasSessionAttention, latestSessionSeq, sessionAttention } from '@/lib/session-attention'
import {
  clearNotificationAttention,
  readNotificationAttentionSeqs,
  writeNotificationAttention,
} from '@/lib/notification-attention'
import type { TranscriptSequenceRange } from '@/lib/events'
import {
  isSessionSettingsView,
  sessionPath,
  sessionRouteFromPathname,
  sessionSlugPath,
  sessionTitleSlug,
  type SessionRoute,
  type SessionRouteView,
} from '@/lib/routes'
import {
  readCachedSessionSnapshot,
  readCachedSessionSnapshotBySlug,
  readCachedSessionSnapshots,
  readCachedSession as readPersistentCachedSession,
  writeCachedSession as writePersistentCachedSession,
  writeCachedSessions as writePersistentCachedSessions,
} from '@/lib/session-cache'
import {
  browserIsOnline,
  isNetworkRequestError,
  serverConnectivityEventName,
  type ServerConnectivityDetail,
} from '@/lib/server-connectivity'
import { cn } from '@/lib/utils'
import { applySessionEvent } from '@/lib/session-events'
import { preserveSessionActivity, sortSessions } from '@/lib/session-order'
import { readFileDraft } from '@/lib/file-drafts'
import { useAnchoredPopover } from '@/hooks/use-anchored-popover'
import {
  ingestClientEvent,
  publishClientSessionEvent,
  replaceClientLiveEvents,
  replaceClientSessionLiveEvents,
} from '@/lib/client-event-store'
import { ClientDebugPanel } from '@/components/client-debug-panel'
import {
  ClientDebugContext,
  clientDebugEnabled,
  clientDebugURL,
  createClientDebugTransport,
  debugEventMetadata,
  debugEventRange,
  debugTime,
  latestDebugMessage,
  type ClientDebugSnapshot,
} from '@/lib/client-debug'

const RepositorySkills = lazy(() => import('@/components/repository-skills').then((module) => ({ default: module.RepositorySkills })))

type SessionRouteHistoryMode = 'push' | 'replace' | 'none'
type PaneSide = 'left' | 'right'
type PaneWidths = {
  left: number
  right: number
}
type SessionContextAction = 'clear' | 'compact'
type AppView = SessionRouteView
type PendingSessionAction = {
  action: SessionContextAction
  sessionID: string
}
type InitialSessionState = {
  sessions: Session[]
  selectedSessionID: string | null
  seededCachedSession: boolean
  restoredOfflineSessionAtRoot: boolean
}

const debugStorageKeyPrefix = 'gorchestra.session-debug.'
const paneWidthsStorageKey = 'gorchestra.pane-widths.v1'
const sessionSeenSeqStorageKey = 'gorchestra.session-seen-seq.v1'
const lastSelectedSessionStorageKey = 'gorchestra.last-selected-session.v1'
const showArchivedSessionsStorageKey = 'gorchestra.show-archived-sessions.v1'
const dashboardActivityRefreshDelayMs = 750
const maximumActivityReconnectDelayMs = 15_000
const offlineReconnectDelayMs = 5_000
const defaultPaneWidths: PaneWidths = { left: 348, right: 344 }
const paneLimits = {
  leftMin: 224,
  leftMax: 560,
  rightMin: 300,
  rightMax: 640,
  centerMin: 520,
}

function App() {
  const [clientDebug, setClientDebug] = useState(clientDebugEnabled)
  const debugTransportRef = useRef(createClientDebugTransport())
  const toggleClientDebug = useCallback(() => {
    const next = !clientDebug
    window.history.replaceState({}, '', clientDebugURL(window.location.href, next))
    setClientDebug(next)
  }, [clientDebug])
  useEffect(() => {
    const update = () => setClientDebug(clientDebugEnabled())
    window.addEventListener('popstate', update)
    return () => window.removeEventListener('popstate', update)
  }, [])
  const [initialPreferences] = useState(() => ({ showArchivedSessions: loadShowArchivedSessionsPreference() }))
  const initialSessionState = useMemo(
    () => loadInitialSessionStateFromLocation(initialPreferences.showArchivedSessions),
    [initialPreferences],
  )
  const [sessions, setSessions] = useState<Session[]>(initialSessionState.sessions)
  const [selectedSessionID, setSelectedSessionID] = useState<string | null>(initialSessionState.selectedSessionID)
  const [showArchivedSessions, setShowArchivedSessions] = useState(initialPreferences.showArchivedSessions)
  const [createOpen, setCreateOpen] = useState(false)
  const [createParentSession, setCreateParentSession] = useState<Session | null>(null)
  const [moveSessionID, setMoveSessionID] = useState<string | null>(null)
  const [mobileListOpen, setMobileListOpen] = useState(false)
  const [loadingSessions, setLoadingSessions] = useState(!initialSessionState.seededCachedSession)
  const [refreshingSessions, setRefreshingSessions] = useState(false)
  const [error, setError] = useState('')
  const [erroredSessionIDs, setErroredSessionIDs] = useState<ReadonlySet<string>>(() => new Set())
  const [showDebugEvents, setShowDebugEvents] = useState(false)
  const [archivingSessionID, setArchivingSessionID] = useState<string | null>(null)
  const [creatingChildSessionIDs, setCreatingChildSessionIDs] = useState<ReadonlySet<string>>(() => new Set())
  const [pinningSessionIDs, setPinningSessionIDs] = useState<ReadonlySet<string>>(() => new Set())
  const [confirmArchiveSessionID, setConfirmArchiveSessionID] = useState<string | null>(null)
  const [confirmSessionAction, setConfirmSessionAction] = useState<PendingSessionAction | null>(null)
  const [pendingSessionAction, setPendingSessionAction] = useState<PendingSessionAction | null>(null)
  const [paneWidths, setPaneWidths] = useState<PaneWidths>(() => loadPaneWidths())
  const [openWorkspaceFile, setOpenWorkspaceFile] = useState<WorkspaceFileContent | null>(null)
  const [workspaceFileDirty, setWorkspaceFileDirty] = useState(false)
  const [fileRefreshKey, setFileRefreshKey] = useState(0)
  const [eventRefreshKey, setEventRefreshKey] = useState(0)
  const [historySyncKey, setHistorySyncKey] = useState(0)
  const [dashboardRefreshKey, setDashboardRefreshKey] = useState(0)
  const [activityCursorReady, setActivityCursorReady] = useState(false)
  const [activityStreamState, setActivityStreamState] = useState<StreamState>('loading')
  const [serverReachable, setServerReachable] = useState(() => browserIsOnline())
  const [connectivityRetryKey, setConnectivityRetryKey] = useState(0)
  const sessionSyncErrorRef = useRef('')
  const [lastSeenSeqBySession, setLastSeenSeqBySession] = useState<Record<string, number>>(() => loadSessionSeenSeqs())
  const [notificationAttentionSeqBySession, setNotificationAttentionSeqBySession] = useState<Record<string, number>>({})
  const [notificationAttentionRestored, setNotificationAttentionRestored] = useState(false)
  const [dismissingNotifications, setDismissingNotifications] = useState(false)
  const [spotlightOpen, setSpotlightOpen] = useState(false)
  const [composerFocusRequest, setComposerFocusRequest] = useState(0)
  const [focusedEventSeq, setFocusedEventSeq] = useState(() => eventSequenceFromLocation())
  const [focusedEventRequest, setFocusedEventRequest] = useState(0)
  const [transcriptVisibleRange, setTranscriptVisibleRange] = useState<TranscriptSequenceRange | null>(null)
  const [focusedFileLine, setFocusedFileLine] = useState(() => fileLineFromLocation())
  const [appView, setAppView] = useState<AppView>(() => selectedSessionRouteFromLocation().view)
  const [userSkillsSelected, setUserSkillsSelected] = useState(() => isUserSkillsLocation())
  const [overviewSelected, setOverviewSelected] = useState(
    () => !initialSessionState.restoredOfflineSessionAtRoot && !isSessionLocation() && !isUserSkillsLocation(),
  )
  const selectedSessionIDRef = useRef<string | null>(selectedSessionID)
  const overviewSelectedRef = useRef(overviewSelected)
  const appViewRef = useRef<AppView>(appView)
  const openWorkspaceFileRef = useRef<WorkspaceFileContent | null>(openWorkspaceFile)
  const sessionsRef = useRef<Session[]>(initialSessionState.sessions)
  const showArchivedSessionsRef = useRef(showArchivedSessions)
  const sessionListLoadedRef = useRef(initialSessionState.seededCachedSession)
  const selectedEventsRef = useRef<AgentEvent[]>([])
  const paneWidthsRef = useRef(paneWidths)
  const dashboardRefreshTimerRef = useRef<number | null>(null)
  const activityCursorRef = useRef(0)
  const activitySnapshotReadyRef = useRef(false)
  const activityClientIDRef = useRef(createActivityClientID())
  const activityWatchRef = useRef(`${selectedSessionID ?? ''}:false`)
  const showDebugEventsRef = useRef(showDebugEvents)
  const offlineRootSelectionRestoredRef = useRef(initialSessionState.restoredOfflineSessionAtRoot)

  const selectedSession = useMemo(
    () => sessions.find((session) => session.id === selectedSessionID) ?? null,
    [selectedSessionID, sessions],
  )
  const serverNotificationAttentionSeqBySession = useMemo(
    () => notificationAttentionSeqsFromSessions(sessions),
    [sessions],
  )
  const effectiveNotificationAttentionSeqBySession = useMemo(
    () => mergeNotificationAttentionSeqs(serverNotificationAttentionSeqBySession, notificationAttentionSeqBySession),
    [notificationAttentionSeqBySession, serverNotificationAttentionSeqBySession],
  )
  const effectiveLastSeenSeqBySession = useMemo(
    () => applyNotificationAttentionSeqs(lastSeenSeqBySession, effectiveNotificationAttentionSeqBySession),
    [effectiveNotificationAttentionSeqBySession, lastSeenSeqBySession],
  )
  const hasFaviconAttention = useMemo(
    () => hasSessionAttention(sessions, effectiveLastSeenSeqBySession),
    [effectiveLastSeenSeqBySession, sessions],
  )
  const appBadgeCount = useMemo(
    () =>
      sessions.reduce(
        (count, session) => count + (sessionAttention(session, effectiveLastSeenSeqBySession) === null ? 0 : 1),
        0,
      ),
    [effectiveLastSeenSeqBySession, sessions],
  )
  const dismissibleNotifications = useMemo(
    () =>
      sessions.filter(
        (session) => sessionAttention(session, effectiveLastSeenSeqBySession) === 'unseen-idle',
      ),
    [effectiveLastSeenSeqBySession, sessions],
  )
  const theme = useTheme()
  const railContent = useRailContentPreference()
  const release = useReleaseUpdate()
  const pushNotifications = usePushNotifications()
  const playSessionStopSound = pushNotifications.playSessionStopSound
  const acknowledgeSessionNotification = pushNotifications.acknowledgeSessionNotification
  useFavicon(hasFaviconAttention)
  useAppBadge(appBadgeCount)
  useClientPerformanceTelemetry(selectedSessionID)

  useEffect(() => {
    function handleConnectivity(event: Event) {
      const detail = (event as CustomEvent<ServerConnectivityDetail>).detail
      if (typeof detail?.reachable === 'boolean') setServerReachable(detail.reachable)
    }

    function handleOffline() {
      setServerReachable(false)
    }

    function handleOnline() {
      setConnectivityRetryKey((current) => current + 1)
    }

    function handleVisible() {
      if (document.visibilityState === 'visible') handleOnline()
    }

    function handlePageShow(event: PageTransitionEvent) {
      if (event.persisted) handleOnline()
    }

    window.addEventListener(serverConnectivityEventName, handleConnectivity)
    window.addEventListener('offline', handleOffline)
    window.addEventListener('online', handleOnline)
    document.addEventListener('visibilitychange', handleVisible)
    window.addEventListener('pageshow', handlePageShow)
    return () => {
      window.removeEventListener(serverConnectivityEventName, handleConnectivity)
      window.removeEventListener('offline', handleOffline)
      window.removeEventListener('online', handleOnline)
      document.removeEventListener('visibilitychange', handleVisible)
      window.removeEventListener('pageshow', handlePageShow)
    }
  }, [])

  useEffect(() => {
    if (!serverReachable) {
      setCreateOpen(false)
      setSpotlightOpen(false)
    }
  }, [serverReachable])

  useEffect(() => {
    let cancelled = false
    async function restoreNotificationAttention() {
      const next = await readNotificationAttentionSeqs()
      const routeAttention = notificationAttentionFromLocation()
      if (routeAttention) {
        next[routeAttention.sessionID] = Math.max(next[routeAttention.sessionID] ?? 0, routeAttention.seq)
        void writeNotificationAttention(routeAttention.sessionID, routeAttention.seq)
        clearNotificationAttentionSearchParam()
      }
      if (!cancelled) {
        setNotificationAttentionSeqBySession(next)
        setNotificationAttentionRestored(true)
      }
    }

    void restoreNotificationAttention()
    return () => {
      cancelled = true
    }
  }, [])

  const clearNotificationAttentionForSession = useCallback((sessionID: string | null) => {
    if (!sessionID) {
      return
    }
    setNotificationAttentionSeqBySession((current) => {
      if (!(sessionID in current)) {
        return current
      }
      const next = { ...current }
      delete next[sessionID]
      return next
    })
    void clearNotificationAttention(sessionID)
    setSessions((current) => {
      let changed = false
      const next = current.map((session) => {
        if (session.id !== sessionID || !session.notification_attention_seq) {
          return session
        }
        changed = true
        return { ...session, notification_attention_seq: undefined }
      })
      if (!changed) {
        return current
      }
      sessionsRef.current = next
      const clearedSession = next.find((session) => session.id === sessionID)
      if (clearedSession) {
        void writePersistentCachedSession(clearedSession)
      }
      return next
    })
    void clearSessionNotificationAttention(sessionID)
      .then((updatedSession) => {
        setSessions((current) => {
          if (!current.some((session) => session.id === updatedSession.id)) {
            return current
          }
          const next = sortSessions(
            current.map((session) => (session.id === updatedSession.id ? preserveSessionActivity(updatedSession, session) : session)),
          )
          sessionsRef.current = next
          void writePersistentCachedSession(updatedSession)
          return next
        })
      })
      .catch(() => undefined)
  }, [])

  const applySession = useCallback((session: Session) => {
    void writePersistentCachedSession(session)
    setSessions((current) => {
      let next: Session[]
      if (
        session.archived_at &&
        !showArchivedSessionsRef.current &&
        session.id !== selectedSessionIDRef.current
      ) {
        next = current.filter((item) => item.id !== session.id)
      } else {
        next = sortSessions([preserveSessionActivity(session, current.find((item) => item.id === session.id)), ...current.filter((item) => item.id !== session.id)])
      }
      sessionsRef.current = next
      return next
    })
  }, [])

  const selectSession = useCallback((sessionID: string | null, historyMode: SessionRouteHistoryMode = 'push') => {
    const nextOverviewSelected = sessionID === null
    overviewSelectedRef.current = nextOverviewSelected
    setOverviewSelected(nextOverviewSelected)
    setUserSkillsSelected(false)
    selectedSessionIDRef.current = sessionID
    setSelectedSessionID(sessionID)
    if (sessionID) saveLastSelectedSessionID(sessionID)
    if (historyMode !== 'none') {
      setFocusedEventSeq(0)
      setFocusedFileLine(0)
      writeSelectedSessionRoute(sessionID, historyMode, appViewRef.current, sessionsRef.current)
    }
  }, [])

  const selectOverview = useCallback(
    (historyMode: SessionRouteHistoryMode = 'push') => {
      appViewRef.current = 'session'
      setAppView('session')
      selectSession(null, historyMode)
      setMobileListOpen(false)
    },
    [selectSession],
  )

  const selectUserSkills = useCallback((historyMode: SessionRouteHistoryMode = 'push') => {
    appViewRef.current = 'session'
    setAppView('session')
    overviewSelectedRef.current = false
    setOverviewSelected(false)
    setUserSkillsSelected(true)
    selectedSessionIDRef.current = null
    setSelectedSessionID(null)
    if (historyMode !== 'none' && window.location.pathname !== '/skills') {
      window.history[historyMode === 'replace' ? 'replaceState' : 'pushState']({}, '', clientDebugURL('/skills'))
    }
    setMobileListOpen(false)
  }, [])

  const selectAppView = useCallback(
    (view: AppView, historyMode: Exclude<SessionRouteHistoryMode, 'none'> = 'push', filePath: string | null = null) => {
      if (view === 'session') {
        setComposerFocusRequest((current) => current + 1)
      }
      appViewRef.current = view
      setAppView(view)
      setFocusedEventSeq(0)
      setFocusedFileLine(0)
      writeSelectedSessionRoute(
        selectedSessionIDRef.current,
        historyMode,
        view,
        sessionsRef.current,
        view === 'files' ? (filePath ?? openWorkspaceFileRef.current?.path ?? null) : null,
      )
    },
    [],
  )

  const completeSessionSelection = useCallback(
    (sessionID: string | null, historyMode: SessionRouteHistoryMode = 'push') => {
      if (sessionID && historyMode !== 'none') {
        appViewRef.current = 'session'
        setAppView('session')
        setComposerFocusRequest((current) => current + 1)
      }
      clearNotificationAttentionForSession(sessionID)
      selectSession(sessionID, historyMode)
      setMobileListOpen(false)
    },
    [clearNotificationAttentionForSession, selectSession],
  )

  const requestSessionSelection = useCallback(
    (sessionID: string | null, historyMode: SessionRouteHistoryMode = 'push') => {
      if (sessionID === selectedSessionIDRef.current) {
        clearNotificationAttentionForSession(sessionID)
        return
      }
      completeSessionSelection(sessionID, historyMode)
    },
    [clearNotificationAttentionForSession, completeSessionSelection],
  )

  const refreshSession = useCallback(
    async (sessionID: string) => {
      const session = await getSession(sessionID)
      applySession(session)
      return session
    },
    [applySession],
  )

  const markSessionSeen = useCallback((sessionID: string, seq: number) => {
    if (!sessionID || seq <= 0) {
      return
    }
    setLastSeenSeqBySession((current) => {
      if ((current[sessionID] ?? 0) >= seq) {
        return current
      }
      const next = { ...current, [sessionID]: seq }
      saveSessionSeenSeqs(next)
      return next
    })
  }, [])

  const handleComposerFocus = useCallback(() => {
    const sessionID = selectedSessionIDRef.current
    if (!sessionID) return

    const session = sessionsRef.current.find((item) => item.id === sessionID) ?? null
    const latestSeq = Math.max(lastSeq(selectedEventsRef.current), latestSessionSeq(session))
    markSessionSeen(sessionID, latestSeq)

    const heldAttentionSeq = Math.max(
      effectiveNotificationAttentionSeqBySession[sessionID] ?? 0,
      session?.notification_attention_seq ?? 0,
    )
    if (heldAttentionSeq > 0) clearNotificationAttentionForSession(sessionID)
  }, [clearNotificationAttentionForSession, effectiveNotificationAttentionSeqBySession, markSessionSeen])

  const markSessionUnseenAfter = useCallback((sessionID: string, seq: number) => {
    if (!sessionID || seq <= 0) {
      return
    }
    setLastSeenSeqBySession((current) => {
      const unseenSeq = Math.max(0, seq - 1)
      if ((current[sessionID] ?? 0) <= unseenSeq) {
        return current
      }
      const next = { ...current }
      if (unseenSeq > 0) {
        next[sessionID] = unseenSeq
      } else {
        delete next[sessionID]
      }
      saveSessionSeenSeqs(next)
      return next
    })
  }, [])

  useEffect(() => {
    selectedSessionIDRef.current = selectedSessionID
    setTranscriptVisibleRange(null)
  }, [selectedSessionID])

  useEffect(() => {
    showDebugEventsRef.current = showDebugEvents
  }, [showDebugEvents])

  useEffect(() => {
    const watchKey = `${selectedSessionID ?? ''}:${showDebugEvents}`
    if (!activityCursorReady || activityWatchRef.current === watchKey) return
    activityWatchRef.current = watchKey
    void watchSessionActivity(
      activityClientIDRef.current,
      selectedSessionID,
      showDebugEvents,
    ).then((snapshot) => {
      if (snapshot.connected && snapshot.session_id && snapshot.events) {
        replaceClientSessionLiveEvents(snapshot.session_id, snapshot.events, snapshot.watermark ?? 0)
      }
    }).catch(() => undefined)
  }, [activityCursorReady, selectedSessionID, showDebugEvents])

  useEffect(() => {
    overviewSelectedRef.current = overviewSelected
  }, [overviewSelected])

  useEffect(() => {
    appViewRef.current = appView
  }, [appView])

  useEffect(() => {
    openWorkspaceFileRef.current = openWorkspaceFile
  }, [openWorkspaceFile])

  useEffect(() => {
    sessionsRef.current = sessions
  }, [sessions])

  useEffect(() => {
    showArchivedSessionsRef.current = showArchivedSessions
    saveShowArchivedSessionsPreference(showArchivedSessions)
  }, [showArchivedSessions])

  useEffect(() => {
    paneWidthsRef.current = paneWidths
    savePaneWidths(paneWidths)
  }, [paneWidths])

  useEffect(() => {
    function handlePopState() {
      const route = selectedSessionRouteFromLocation()
      setFocusedEventSeq(eventSequenceFromLocation())
      setFocusedEventRequest((current) => current + 1)
      setFocusedFileLine(fileLineFromLocation())
      if (isUserSkillsLocation()) {
        selectUserSkills('none')
        return
      }
      if (!isSessionLocation()) {
        selectOverview('none')
        return
      }
      appViewRef.current = route.view
      setAppView(route.view)
      requestSessionSelection(resolveSessionRouteSessionID(route, sessionsRef.current), 'none')
    }

    window.addEventListener('popstate', handlePopState)
    return () => window.removeEventListener('popstate', handlePopState)
  }, [requestSessionSelection, selectOverview, selectUserSkills])

  useEffect(() => {
    function handleNavigationShortcut(event: globalThis.KeyboardEvent) {
      if (
        event.defaultPrevented ||
        (!event.metaKey && !event.ctrlKey) ||
        event.altKey ||
        event.shiftKey
      ) {
        return
      }

      const key = event.key.toLowerCase()
      if (key === 'd' && !event.isComposing) {
        event.preventDefault()
        if (!event.repeat) toggleClientDebug()
        return
      }
      if (key === 'k') {
        event.preventDefault()
        setSpotlightOpen(true)
        return
      }
      if (key === 'o') {
        event.preventDefault()
        selectOverview('push')
        return
      }
      if (key === 's') {
        event.preventDefault()
        selectUserSkills('push')
        return
      }

      if (!/^[1-5]$/.test(key)) {
        return
      }
      const session = sessionsRef.current[Number(key) - 1]
      if (!session) {
        return
      }
      event.preventDefault()
      requestSessionSelection(session.id, 'push')
    }
    window.addEventListener('keydown', handleNavigationShortcut)
    return () => window.removeEventListener('keydown', handleNavigationShortcut)
  }, [requestSessionSelection, selectOverview, selectUserSkills, toggleClientDebug])

  useEffect(() => {
    setShowDebugEvents(loadSessionDebugPreference(selectedSessionID))
    setOpenWorkspaceFile(null)
  }, [selectedSessionID])

  useEffect(() => {
    if (appView !== 'files') {
      return
    }

    const route = selectedSessionRouteFromLocation()
    if (route.view !== 'files') {
      return
    }

    if (!route.filePath) {
      if (openWorkspaceFileRef.current) {
        setOpenWorkspaceFile(null)
      }
      return
    }

    if (!selectedSessionID || !selectedSession || openWorkspaceFileRef.current?.path === route.filePath) {
      return
    }

    let cancelled = false
    setError('')
    void getSessionFileContent(selectedSessionID, route.filePath)
      .then((content) => {
        if (!cancelled) {
          setOpenWorkspaceFile(content)
        }
      })
      .catch((openError) => {
        if (!cancelled) {
          const saved = readFileDraft(selectedSessionID, route.filePath!)
          if (saved) setOpenWorkspaceFile(saved.base)
          else setError(messageFromError(openError))
        }
      })

    return () => {
      cancelled = true
    }
  }, [appView, selectedSession, selectedSessionID])

  useEffect(() => {
    if (!selectedSessionID || selectedSession) {
      return
    }

    let cancelled = false
    const sessionID = selectedSessionID
    void readPersistentCachedSession(sessionID).then((cachedSession) => {
      if (
        cancelled ||
        !cachedSession ||
        selectedSessionIDRef.current !== sessionID ||
        sessionsRef.current.some((session) => session.id === sessionID) ||
        (cachedSession.archived_at && cachedSession.id !== selectedSessionIDRef.current)
      ) {
        return
      }
      applySession(cachedSession)
    })

    return () => {
      cancelled = true
    }
  }, [applySession, selectedSession, selectedSessionID])

  const applySessionActivityEvent = useCallback((event: AgentEvent) => {
    const status = statusFromEvent(event)
    if (status && status !== 'failed') {
      setErroredSessionIDs((current) => removeSetValue(current, event.session_id))
    }
    setSessions((current) => {
      let changed = false
      const updated = sortSessions(
        current.map((session) => {
          if (session.id !== event.session_id) {
            return session
          }
          const updatedSession = applySessionEvent(session, event, status)
          if (updatedSession === session) return session
          changed = true
          if (!isTransientEvent(event)) void writePersistentCachedSession(updatedSession)
          return updatedSession
        }),
      )
      const next = updated.filter(
        (session) => showArchivedSessionsRef.current || !session.archived_at,
      )
      if (next.length !== updated.length) changed = true
      if (changed) sessionsRef.current = next
      return changed ? next : current
    })
  }, [])

  const scheduleDashboardRefresh = useCallback((immediate = false) => {
    if (!overviewSelectedRef.current) return

    const refresh = () => {
      dashboardRefreshTimerRef.current = null
      setDashboardRefreshKey((value) => value + 1)
    }
    if (immediate) {
      if (dashboardRefreshTimerRef.current !== null) {
        window.clearTimeout(dashboardRefreshTimerRef.current)
      }
      refresh()
      return
    }
    if (dashboardRefreshTimerRef.current === null) {
      dashboardRefreshTimerRef.current = window.setTimeout(refresh, dashboardActivityRefreshDelayMs)
    }
  }, [])

  useEffect(() => () => {
    if (dashboardRefreshTimerRef.current !== null) {
      window.clearTimeout(dashboardRefreshTimerRef.current)
    }
  }, [])

  const applyIngestedSessionEvent = useCallback(
    (event: AgentEvent) => {
      const knownSession = sessionsRef.current.find((session) => session.id === event.session_id)
      const selected = event.session_id === selectedSessionIDRef.current
      const archivedSelectedSession = selected && event.type === 'session.archived'
      applySessionActivityEvent(event)
      if (selected) selectedEventsRef.current = appendEvent(selectedEventsRef.current, event)
      if (selected && document.visibilityState === 'visible') {
        if (event.type === 'agent.permission.requested') void acknowledgeSessionNotification(event)
      } else {
        playSessionStopSound(event)
      }
      if (shouldRefreshWorkspaceFilesForEvent(event) && selected) {
        setFileRefreshKey((value) => value + 1)
      }
      const terminalUnselected = isTerminalEvent(event.type) && !selected
      if (terminalUnselected && event.seq >= latestSessionSeq(knownSession ?? null)) {
        markSessionUnseenAfter(event.session_id, event.seq)
      }
      if (!knownSession) {
        window.setTimeout(() => {
          void refreshSession(event.session_id)
        }, 250)
      }
      if (archivedSelectedSession) {
        selectOverview('replace')
      }
      scheduleDashboardRefresh(isTerminalEvent(event.type))
    },
    [
      applySessionActivityEvent,
      markSessionUnseenAfter,
      playSessionStopSound,
      refreshSession,
      scheduleDashboardRefresh,
      selectOverview,
      acknowledgeSessionNotification,
    ],
  )

  const handleActivityEvent = useCallback(
    (event: AgentEvent) => {
      if (isDebugOnlyEvent(event)) {
        publishClientSessionEvent(event)
        applyIngestedSessionEvent(event)
        scheduleDashboardRefresh()
        return
      }
      if (isTransientEvent(event)) {
        if (ingestClientEvent(event)) applySessionActivityEvent(event)
        scheduleDashboardRefresh()
        return
      }
      if (ingestClientEvent(event)) applyIngestedSessionEvent(event)
    },
    [applyIngestedSessionEvent, applySessionActivityEvent, scheduleDashboardRefresh],
  )

  const {
    events,
    liveEvents,
    streamState,
    error: streamError,
    hasOlderEvents,
    hasNewerEvents,
    loadingOlderEvents,
    loadingNewerEvents,
    olderHistoryUnavailable,
    loadOlderEvents,
    loadNewerEvents,
    jumpToLatest,
    setFollowingTail,
    readHistoryDebug,
  } = useSessionEvents(selectedSessionID, {
    refreshKey: eventRefreshKey,
    historySyncKey,
    historyReady: activityCursorReady,
    includeDebugEvents: showDebugEvents,
    targetSeq: focusedEventSeq,
    liveStreamState: activityStreamState,
    networkAvailable: serverReachable,
  })

  const readDebugSnapshot = useCallback((): ClientDebugSnapshot => {
    const debugTransport = debugTransportRef.current
    const history = readHistoryDebug()
    const lastEvent = debugTransport.lastEvent
    return {
      stream: {
        connection: `${activityStreamState} · source ${debugTransport.sourceState}`,
        network: `${browserIsOnline() ? 'online' : 'offline'} · server ${serverReachable ? 'reachable' : 'unreachable'} · ${document.visibilityState}`,
        cursor: `${activityCursorRef.current} · opened after ${debugTransport.requestedCursor}`,
        attempts: `${debugTransport.attempts} · errors ${debugTransport.errors}`,
        opened: debugTime(debugTransport.openedAt),
        retry: debugTransport.retryAt ? `in ${Math.max(0, Math.ceil((debugTransport.retryAt - Date.now()) / 1000))}s` : 'none',
        lastError: debugTime(debugTransport.errorAt),
        resync: debugTransport.resyncs ? `${debugTransport.resyncs} · ${debugTransport.resyncReason} · ${debugTime(debugTransport.resyncAt)}` : 'none',
        received: `${debugTransport.received} · rejected ${debugTransport.rejected}`,
        lastEvent: lastEvent ? `${lastEvent.type} · ${lastEvent.transient ? 'transient' : 'durable'}` : 'none received this page load',
        eventSession: lastEvent ? `${lastEvent.session} #${lastEvent.seq} · global ${lastEvent.cursor}` : '—',
        eventReceived: lastEvent ? debugTime(lastEvent.receivedAt) : '—',
        eventCreated: lastEvent ? debugTime(lastEvent.createdAt) : '—',
      },
      history: {
        session: selectedSession ? `${selectedSession.title} (${selectedSession.id})` : 'none',
        sync: `${streamState} · boundary ${historySyncKey} · snapshot ${activityCursorReady ? 'ready' : 'pending'}`,
        sequences: `server ${selectedSession ? latestSessionSeq(selectedSession) : 0} · local durable ${history.lastDurableSeq}`,
        tail: `${history.tailHydrated ? 'validated' : 'not validated'} · ${history.followingTail ? 'following' : 'paused'}`,
        loaded: debugEventRange(events),
        live: debugEventRange(liveEvents),
        pages: `older ${hasOlderEvents ? 'yes' : 'no'}${loadingOlderEvents ? ' (loading)' : ''} · newer ${hasNewerEvents ? 'yes' : 'no'}${loadingNewerEvents ? ' (loading)' : ''}`,
        focus: focusedEventSeq > 0 ? `#${focusedEventSeq}` : 'live tail',
        error: streamError || 'none',
      },
      ...latestDebugMessage(events, liveEvents),
    }
  }, [activityCursorReady, activityStreamState, events, focusedEventSeq, hasNewerEvents, hasOlderEvents, historySyncKey, liveEvents, loadingNewerEvents, loadingOlderEvents, readHistoryDebug, selectedSession, serverReachable, streamError, streamState])

  const handleJumpToLatest = useCallback(() => {
    if (focusedEventSeq <= 0) return jumpToLatest()
    setFocusedEventSeq(0)
    const sessionID = selectedSessionIDRef.current
    if (appViewRef.current === 'session') {
      writeSelectedSessionRoute(sessionID, 'replace', 'session', sessionsRef.current)
    }
    return Promise.resolve()
  }, [focusedEventSeq, jumpToLatest])

  useEffect(() => {
    if (serverReachable && error) {
      setErroredSessionIDs((current) => addSetValue(current, selectedSessionIDRef.current))
    }
  }, [error, serverReachable])

  useEffect(() => {
    if (serverReachable && streamError) {
      setErroredSessionIDs((current) => addSetValue(current, selectedSessionIDRef.current))
    }
  }, [serverReachable, streamError])

  useEffect(() => {
    selectedEventsRef.current = liveEvents
  }, [liveEvents])

  useEffect(() => {
    if (!selectedSessionID || !notificationAttentionRestored) {
      return
    }
    const latestSeq = Math.max(lastSeq(liveEvents), latestSessionSeq(selectedSession))
    const heldAttentionSeq = effectiveNotificationAttentionSeqBySession[selectedSessionID] ?? 0
    if (heldAttentionSeq > 0 && latestSeq <= heldAttentionSeq) {
      return
    }
    markSessionSeen(selectedSessionID, latestSeq)
  }, [
    effectiveNotificationAttentionSeqBySession,
    liveEvents,
    markSessionSeen,
    notificationAttentionRestored,
    selectedSession,
    selectedSessionID,
  ])

  const loadSessions = useCallback(
    async (options: { showLoading?: boolean; resyncStream?: boolean } = {}) => {
      const showLoading = options.showLoading ?? sessionsRef.current.length === 0
      if (showLoading) {
        setLoadingSessions(true)
        setError('')
      } else {
        setRefreshingSessions(true)
      }
      try {
        const snapshot = await getSessionSnapshot({ include_archived: showArchivedSessions })
        setServerReachable(true)
        const recoveredError = sessionSyncErrorRef.current
        sessionSyncErrorRef.current = ''
        setError((current) => (current === recoveredError || isLikelyNetworkErrorMessage(current) ? '' : current))
        const nextSessions = snapshot.sessions
        // A session-list snapshot is not transcript history. Only advance the
        // replay boundary when all cached tails are also marked for recovery.
        if (!activitySnapshotReadyRef.current || options.resyncStream) {
          activityCursorRef.current = Math.max(activityCursorRef.current, snapshot.eventCursor)
          activitySnapshotReadyRef.current = true
          invalidateSessionEventTails()
          setHistorySyncKey((current) => current + 1)
        }
        const selectedID = selectedSessionIDRef.current
        const mergedSessions = (await includeSelectedSession(nextSessions, selectedID)).filter(
          (session) => showArchivedSessions || !session.archived_at,
        )
        void writePersistentCachedSessions(mergedSessions)
        const route = selectedSessionRouteFromLocation()
        const routeSelectedID = resolveSessionRouteSessionID(route, mergedSessions)
        const preferredRouteSelectedID = preferredSessionIDForRouteSlug(route, mergedSessions, selectedID)
        const resolvedRouteSelectedID = routeSelectedID ?? preferredRouteSelectedID
        const preserveSlugRoute = Boolean(route.sessionSlug && routeSelectedID)
        const nextSelectedID =
          overviewSelectedRef.current && !isSessionLocation()
            ? null
            : resolvedRouteSelectedID && mergedSessions.some((session) => session.id === resolvedRouteSelectedID)
              ? resolvedRouteSelectedID
              : !route.sessionSlug && selectedID && mergedSessions.some((session) => session.id === selectedID)
                ? selectedID
                : route.sessionSlug || route.sessionID
                  ? null
                  : (nextSessions[0]?.id ?? mergedSessions[0]?.id ?? null)

        const sortedSessions = sortSessions(preferFresherSessionSnapshots(mergedSessions, sessionsRef.current))
        sessionsRef.current = sortedSessions
        setSessions(sortedSessions)
        setActivityCursorReady(true)
        if (isUserSkillsLocation()) {
          selectedSessionIDRef.current = null
          setSelectedSessionID(null)
          overviewSelectedRef.current = false
          setOverviewSelected(false)
          setUserSkillsSelected(true)
        } else {
          const preserveDestination = preserveSlugRoute || eventSequenceFromLocation() > 0 ||
            (!nextSelectedID && Boolean(route.sessionSlug || route.sessionID))
          selectSession(nextSelectedID, preserveDestination ? 'none' : 'replace')
        }
        return true
      } catch (loadError) {
        // An unsuccessful bootstrap has no cursor/stream to trigger recovery.
        // Retry synchronization even for HTTP errors or malformed snapshots.
        setServerReachable(false)
        sessionSyncErrorRef.current = messageFromError(loadError)
        if (isNetworkRequestError(loadError)) {
          if (
            !offlineRootSelectionRestoredRef.current &&
            !isSessionLocation() &&
            !isUserSkillsLocation() &&
            sessionsRef.current.length > 0
          ) {
            offlineRootSelectionRestoredRef.current = true
            const offlineSessionID = preferredCachedSessionID(sessionsRef.current)
            if (offlineSessionID) selectSession(offlineSessionID, 'none')
          }
        } else if (showLoading) {
          setError(messageFromError(loadError))
        }
        return false
      } finally {
        sessionListLoadedRef.current = true
        if (showLoading) {
          setLoadingSessions(false)
        } else {
          setRefreshingSessions(false)
        }
      }
    },
    [selectSession, showArchivedSessions],
  )

  const handleDismissAllNotifications = useCallback(async () => {
    if (dismissingNotifications) return

    setDismissingNotifications(true)
    const currentSessions = sessionsRef.current
    const notificationSessionIDs = Object.keys(effectiveNotificationAttentionSeqBySession)
    const nextSeenSeqs = { ...lastSeenSeqBySession }
    for (const session of currentSessions) {
      const latestSeq = latestSessionSeq(session)
      if (latestSeq > 0) nextSeenSeqs[session.id] = latestSeq
    }
    saveSessionSeenSeqs(nextSeenSeqs)
    setLastSeenSeqBySession(nextSeenSeqs)
    setNotificationAttentionSeqBySession({})

    const clearedSessions = currentSessions.map((session) =>
      session.notification_attention_seq
        ? { ...session, notification_attention_seq: undefined }
        : session,
    )
    sessionsRef.current = clearedSessions
    setSessions(clearedSessions)
    void writePersistentCachedSessions(clearedSessions)

    try {
      await Promise.all([
        clearAllSessionNotificationAttention(),
        ...notificationSessionIDs.map((sessionID) => clearNotificationAttention(sessionID)),
      ])
    } catch (dismissError) {
      setError(messageFromError(dismissError))
      void loadSessions({ showLoading: false })
    } finally {
      setDismissingNotifications(false)
    }
  }, [
    dismissingNotifications,
    effectiveNotificationAttentionSeqBySession,
    lastSeenSeqBySession,
    loadSessions,
  ])

  useEffect(() => {
    if (!activityCursorReady || !serverReachable) {
      setActivityStreamState('disconnected')
      return
    }
    const debugTransport = debugTransportRef.current
    let closed = false
    let source: EventSource | null = null
    let reconnectTimer: number | undefined
    let reconnectAttempt = 0
    let resyncing = false

    function closeSource() {
      source?.close()
      source = null
      debugTransport.sourceState = 'closed'
      debugTransport.retryAt = 0
    }

    function handleActivityMessage(message: MessageEvent<string>) {
      if (closed || resyncing) return
      try {
        const event = JSON.parse(message.data) as AgentEvent
        debugTransport.received += 1
        debugTransport.lastEvent = debugEventMetadata(event)
        handleActivityEvent(event)
        if (event.global_seq && event.global_seq > activityCursorRef.current) {
          activityCursorRef.current = event.global_seq
        }
      } catch {
        debugTransport.rejected += 1
        // A malformed sidebar event should not interrupt the selected transcript stream.
      }
    }

    function handleResyncRequired(reason: string) {
      if (closed || resyncing) return
      debugTransport.resyncs += 1
      debugTransport.resyncAt = Date.now()
      debugTransport.resyncReason = reason
      resyncing = true
      setActivityStreamState('loading')
      closeSource()
      if (reconnectTimer !== undefined) {
        window.clearTimeout(reconnectTimer)
        reconnectTimer = undefined
      }
      void loadSessions({ showLoading: false, resyncStream: true }).then((synchronized) => {
        resyncing = false
        if (closed) return
        if (synchronized) {
          connect()
        } else {
          scheduleReconnect()
        }
      })
    }

    function scheduleReconnect() {
      if (closed || reconnectTimer !== undefined) return
      setActivityStreamState('reconnecting')
      const delay = Math.min(1000 * 2 ** reconnectAttempt, maximumActivityReconnectDelayMs)
      reconnectAttempt += 1
      debugTransport.retryAt = Date.now() + delay
      reconnectTimer = window.setTimeout(() => {
        reconnectTimer = undefined
        connect()
      }, delay)
    }

    function connect() {
      if (closed || typeof EventSource === 'undefined') {
        if (typeof EventSource === 'undefined') setActivityStreamState('disconnected')
        return
      }
      closeSource()
      debugTransport.attempts += 1
      debugTransport.requestedCursor = activityCursorRef.current
      debugTransport.sourceState = 'connecting'
      const connectedSource = new EventSource(sessionActivityStreamURL(activityCursorRef.current, {
        clientID: activityClientIDRef.current,
        watchSessionID: selectedSessionIDRef.current,
        includeDebug: showDebugEventsRef.current,
        liveScope: 'all',
      }))
      source = connectedSource
      source.onopen = () => {
        if (closed || source !== connectedSource) return
        reconnectAttempt = 0
        debugTransport.sourceState = 'open'
        debugTransport.openedAt = Date.now()
        setServerReachable(true)
        setActivityStreamState('connected')
      }
      source.onerror = () => {
        if (closed || source !== connectedSource) return
        debugTransport.errors += 1
        debugTransport.errorAt = Date.now()
        closeSource()
        if (!browserIsOnline()) setServerReachable(false)
        scheduleReconnect()
      }
      for (const eventType of knownEventTypes) {
        source.addEventListener(eventType, (message) => {
          if (source === connectedSource) handleActivityMessage(message as MessageEvent<string>)
        })
      }
      source.addEventListener('stream.resync.required', () => {
        if (source === connectedSource) handleResyncRequired('stream.resync.required')
      })
      source.addEventListener('session.live.snapshot', (message) => {
        if (closed || source !== connectedSource) return
        try {
          const snapshot = JSON.parse((message as MessageEvent<string>).data) as {
            events?: AgentEvent[]
            watermarks?: Record<string, number>
          }
          replaceClientLiveEvents(snapshot.events ?? [], snapshot.watermarks ?? {})
        } catch {
          debugTransport.rejected += 1
        }
      })
    }

    setActivityStreamState('loading')
    connect()

    // Safari can suspend a page without an offline/error event. On return,
    // replace the potentially half-open stream and reconcile cached history.
    function handleVisible() {
      if (document.visibilityState === 'visible') handleResyncRequired('foreground return')
    }
    function handlePageShow(event: PageTransitionEvent) {
      if (event.persisted) handleResyncRequired('page restored (bfcache)')
    }
    document.addEventListener('visibilitychange', handleVisible)
    window.addEventListener('pageshow', handlePageShow)

    return () => {
      closed = true
      document.removeEventListener('visibilitychange', handleVisible)
      window.removeEventListener('pageshow', handlePageShow)
      if (reconnectTimer !== undefined) {
        window.clearTimeout(reconnectTimer)
      }
      closeSource()
      setActivityStreamState('disconnected')
    }
  }, [activityCursorReady, handleActivityEvent, loadSessions, serverReachable])

  useEffect(() => {
    if (!browserIsOnline()) {
      sessionListLoadedRef.current = true
      setLoadingSessions(false)
      setActivityStreamState('disconnected')
      return
    }
    void loadSessions()
  }, [loadSessions])

  useEffect(() => {
    if (serverReachable || !browserIsOnline()) return

    let cancelled = false
    let retryTimer: number | undefined
    async function retry() {
      const synchronized = await loadSessions({ showLoading: false })
      if (!cancelled && !synchronized) {
        retryTimer = window.setTimeout(() => void retry(), offlineReconnectDelayMs)
      }
    }

    void retry()
    return () => {
      cancelled = true
      if (retryTimer !== undefined) window.clearTimeout(retryTimer)
    }
  }, [connectivityRetryKey, loadSessions, serverReachable])

  useEffect(() => {
    if (!serverReachable || !selectedSessionID || selectedSession || loadingSessions || !sessionListLoadedRef.current) {
      return
    }
    void refreshSession(selectedSessionID).catch((refreshError) => {
      setError(messageFromError(refreshError))
    })
  }, [loadingSessions, refreshSession, selectedSession, selectedSessionID, serverReachable])

  async function handleCreate(params: {
    agent_type: AgentType
    title?: string
    workspace_path?: string
    agent_options?: SessionAgentOptions
    parent_session_id?: string
  }) {
    const parentSessionID = params.parent_session_id
    if (parentSessionID) {
      setCreatingChildSessionIDs((current) => addSetValue(current, parentSessionID))
    }
    setError('')
    try {
      const session = await createSession(params)
      applySession(session)
      if (parentSessionID) {
        setMobileListOpen(false)
        appViewRef.current = 'session'
        setAppView('session')
        setComposerFocusRequest((current) => current + 1)
        selectSession(session.id, 'push')
      } else {
        completeSessionSelection(session.id, 'push')
      }
      return session
    } finally {
      if (parentSessionID) {
        setCreatingChildSessionIDs((current) => removeSetValue(current, parentSessionID))
      }
    }
  }

  function openCreateChildSession(parentSessionID: string) {
    const parentSession = sessions.find((session) => session.id === parentSessionID)
    if (!parentSession) return
    setCreateParentSession(parentSession)
    setMobileListOpen(false)
    setCreateOpen(true)
  }

  function handleCreateOpenChange(open: boolean) {
    setCreateOpen(open)
    if (!open) setCreateParentSession(null)
  }

  async function handleMoveSession(parentSessionID: string | null) {
    if (!moveSessionID) return
    const updated = await updateSessionParent(moveSessionID, parentSessionID)
    applySession(updated)
    await loadSessions({ showLoading: false })
  }

  async function handleSubmitPrompt(
    content: string,
    agentOptions?: SubmitAgentOptions,
    attachments: MessageAttachment[] = [],
    queue = false,
    skills: SkillReference[] = [],
    clientSubmissionID = '',
    steerRunID?: string,
  ) {
    if (!selectedSessionID) {
      throw new Error('Select a session first.')
    }
    const response = await submitMessage(
      selectedSessionID,
      content,
      agentOptions,
      attachments,
      queue,
      skills,
      clientSubmissionID,
      steerRunID,
    )
    // A steer can be acknowledged just as the run ends. SSE owns its status;
    // never turn an already-completed session back to "running" here.
    if (steerRunID) return response
    setSessions((current) =>
      current.map((session) => {
        if (session.id !== selectedSessionID) {
          return session
        }
        const updatedSession = {
          ...session,
          status: response.status,
          completed_at: response.status === 'running' ? null : session.completed_at,
        }
        void writePersistentCachedSession(updatedSession)
        return updatedSession
      }),
    )
    return response
  }

  async function handleCancel() {
    if (!selectedSessionID) {
      return
    }
    try {
      await cancelSession(selectedSessionID)
    } catch (cancelError) {
      setError(messageFromError(cancelError))
      if (cancelError instanceof APIError && cancelError.status === 409) {
        await refreshSession(selectedSessionID)
      }
      // The composer must not dequeue anything when cancellation was rejected.
      throw cancelError
    }
  }

  async function handleAnswerUserInput(requestID: string, answers: UserInputAnswers) {
    if (!selectedSessionID) {
      throw new Error('Select a session first.')
    }
    const sessionID = selectedSessionID
    try {
      await answerUserInput(sessionID, requestID, answers)
    } finally {
      setEventRefreshKey((value) => value + 1)
      void refreshSession(sessionID)
    }
  }

  async function handleResolvePermission(requestID: string, optionID: string) {
    if (!selectedSessionID) throw new Error('Select a session first.')
    const sessionID = selectedSessionID
    try {
      await resolvePermission(sessionID, requestID, optionID)
    } finally {
      setEventRefreshKey((value) => value + 1)
      void refreshSession(sessionID)
    }
  }

  function handleShowDebugEventsChange(nextShowDebugEvents: boolean) {
    setShowDebugEvents(nextShowDebugEvents)
    saveSessionDebugPreference(selectedSessionID, nextShowDebugEvents)
  }

  async function handleUpdateTitle(title: string) {
    if (!selectedSessionID) {
      return
    }
    const updated = await updateSessionTitle(selectedSessionID, title)
    applySession(updated)
  }

  async function handleUpdateWorkspace(workspacePath: string) {
    if (!selectedSessionID) {
      return
    }
    const updated = await updateSessionWorkspace(selectedSessionID, workspacePath)
    applySession(updated)
    setOpenWorkspaceFile(null)
    setWorkspaceFileDirty(false)
    setFileRefreshKey((value) => value + 1)
    if (appViewRef.current === 'files') {
      writeSelectedSessionRoute(selectedSessionID, 'replace', 'files', sessionsRef.current)
    }
  }

  async function handlePinSession(sessionID: string, pinned: boolean) {
    if (pinningSessionIDs.has(sessionID)) return
    const previous = sessionsRef.current.find((session) => session.id === sessionID)
    if (!previous || (pinned && previous.archived_at)) return

    const optimisticPinnedAt = pinned ? new Date().toISOString() : null
    setPinningSessionIDs((current) => addSetValue(current, sessionID))
    setSessions((current) => {
      const next = sortSessions(
        current.map((session) =>
          session.id === sessionID ? { ...session, pinned_at: optimisticPinnedAt } : session,
        ),
      )
      sessionsRef.current = next
      const optimistic = next.find((session) => session.id === sessionID)
      if (optimistic) void writePersistentCachedSession(optimistic)
      return next
    })

    try {
      const updated = await updateSessionPin(sessionID, pinned)
      applySession(updated)
    } catch (pinError) {
      setError(messageFromError(pinError))
      setSessions((current) => {
        const next = sortSessions(
          current.map((session) =>
            session.id === sessionID && session.pinned_at === optimisticPinnedAt
              ? { ...session, pinned_at: previous.pinned_at ?? null }
              : session,
          ),
        )
        sessionsRef.current = next
        const restored = next.find((session) => session.id === sessionID)
        if (restored) void writePersistentCachedSession(restored)
        return next
      })
    } finally {
      setPinningSessionIDs((current) => removeSetValue(current, sessionID))
    }
  }

  async function handleUpdateAgentOptions(agentOptions: SessionAgentOptions) {
    if (!selectedSessionID) {
      return
    }
    try {
      const updated = await updateSessionAgentOptions(selectedSessionID, agentOptions)
      applySession(updated)
      setError('')
    } catch (optionsError) {
      setError(messageFromError(optionsError))
    }
  }

  const handleUpdateRuntimeAgentOptions = useCallback(async (
    sessionID: string,
    options: SessionRuntimeAgentOptions,
    initializeIfAbsent = false,
  ): Promise<UpdateSessionRuntimeAgentOptionsResponse> => {
    try {
      const response = await updateSessionRuntimeAgentOptions(sessionID, options, initializeIfAbsent)
      applySession(response.session)
      setError('')
      return response
    } catch (optionsError) {
      setError(messageFromError(optionsError))
      throw optionsError
    }
  }, [applySession])

  function requestArchiveSession(sessionID = selectedSessionID) {
    if (!sessionID) {
      return
    }
    setConfirmArchiveSessionID(sessionID)
  }

  async function handleConfirmArchiveSession() {
    if (!confirmArchiveSessionID) {
      return
    }

    const sessionID = confirmArchiveSessionID
    const targetSession = sessions.find((session) => session.id === sessionID) ?? null
    const restoring = Boolean(targetSession?.archived_at)
    setArchivingSessionID(sessionID)
    setError('')
    try {
      const updatedSession = restoring ? await restoreSession(sessionID) : await archiveSession(sessionID)
      if (restoring) {
        applySession(updatedSession)
        selectSession(sessionID, 'replace')
      } else {
        if (selectedSessionIDRef.current === sessionID) {
          selectOverview('replace')
        }
        applySession(updatedSession)
      }
      setConfirmArchiveSessionID(null)
    } catch (archiveError) {
      setError(messageFromError(archiveError))
      if (archiveError instanceof APIError && archiveError.status === 409) {
        await refreshSession(sessionID)
      }
    } finally {
      setArchivingSessionID((current) => (current === sessionID ? null : current))
    }
  }

  function requestSessionAction(action: SessionContextAction) {
    if (!selectedSessionID) {
      return
    }
    setConfirmSessionAction({ action, sessionID: selectedSessionID })
  }

  async function handleConfirmSessionAction() {
    if (!confirmSessionAction) {
      return
    }

    const { action, sessionID } = confirmSessionAction
    setPendingSessionAction({ action, sessionID })
    setError('')
    try {
      const response = action === 'clear' ? await clearSession(sessionID) : await compactSession(sessionID)
      setSessions((current) =>
        current.map((session) => {
          if (session.id !== sessionID) {
            return session
          }
          const updatedSession = {
            ...session,
            status: response.status,
            provider_session_id: action === 'clear' ? undefined : session.provider_session_id,
            completed_at: response.status === 'running' ? null : session.completed_at,
          }
          void writePersistentCachedSession(updatedSession)
          return updatedSession
        }),
      )
      if (action === 'clear') {
        await refreshSession(sessionID)
      }
      setConfirmSessionAction(null)
    } catch (actionError) {
      setError(messageFromError(actionError))
      if (actionError instanceof APIError && actionError.status === 409) {
        await refreshSession(sessionID)
      }
    } finally {
      setPendingSessionAction((current) =>
        current?.action === action && current.sessionID === sessionID ? null : current,
      )
    }
  }

  const handleOpenWorkspacePath = useCallback(
    async (path: string) => {
      if (!selectedSessionID || !selectedSession) {
        return
      }

      setError('')
      try {
        const content = await getSessionFileContent(
          selectedSessionID,
          workspaceRelativeFilePath(path, selectedSession.workspace_path),
        )
        setOpenWorkspaceFile(content)
        selectAppView('files', 'push', content.path)
      } catch (openError) {
        setError(messageFromError(openError))
      }
    },
    [selectAppView, selectedSession, selectedSessionID],
  )

  const handleOpenWorkspaceFile = useCallback(
    (file: WorkspaceFileContent) => {
      setOpenWorkspaceFile(file)
      setWorkspaceFileDirty(false)
      selectAppView('files', 'push', file.path)
    },
    [selectAppView],
  )

  const handleCloseWorkspaceFile = useCallback(() => {
    setOpenWorkspaceFile(null)
    setWorkspaceFileDirty(false)
    writeSelectedSessionRoute(selectedSessionIDRef.current, 'push', 'files', sessionsRef.current)
  }, [])

  async function handleSpotlightResult(result: SpotlightSearchResult) {
    setError('')
    try {
      const session =
        sessionsRef.current.find((item) => item.id === result.session_id) ?? (await getSession(result.session_id))
      selectedSessionIDRef.current = session.id
      applySession(session)
      selectSession(session.id, 'none')
      setMobileListOpen(false)
      setOverviewSelected(false)
      setUserSkillsSelected(false)
      setWorkspaceFileDirty(false)

      if ((result.kind === 'file' || result.kind === 'agent_instruction') && result.path) {
        const file = await getSessionFileContent(session.id, result.path)
        appViewRef.current = 'files'
        setAppView('files')
        setOpenWorkspaceFile(file)
        setFocusedEventSeq(0)
        setFocusedFileLine(result.line_number ?? 0)
        writeSpotlightResultRoute(session, 'files', result.path, {
          line: result.line_number,
        })
        return
      }

      appViewRef.current = 'session'
      setAppView('session')
      setComposerFocusRequest((current) => current + 1)
      setOpenWorkspaceFile(null)
      setFocusedFileLine(0)
      setFocusedEventSeq(result.event_seq ?? 0)
      setFocusedEventRequest((current) => current + 1)
      writeSpotlightResultRoute(session, 'session', null, {
        eventSeq: result.event_seq,
      })
    } catch (searchResultError) {
      setError(messageFromError(searchResultError))
    }
  }

  const handleSelectConversationSeq = useCallback((seq: number) => {
    const session = sessionsRef.current.find((item) => item.id === selectedSessionIDRef.current)
    if (!session || !Number.isSafeInteger(seq) || seq <= 0) return
    appViewRef.current = 'session'
    setAppView('session')
    setFocusedFileLine(0)
    setFocusedEventSeq(seq)
    setFocusedEventRequest((current) => current + 1)
    writeSpotlightResultRoute(session, 'session', null, { eventSeq: seq })
  }, [])

  function beginPaneResize(side: PaneSide, event: ReactPointerEvent<HTMLButtonElement>) {
    if (event.button !== 0) {
      return
    }
    event.preventDefault()

    const startX = event.clientX
    const startWidths = paneWidthsRef.current
    const previousCursor = document.documentElement.style.cursor
    const previousUserSelect = document.body.style.userSelect
    document.documentElement.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'

    function handlePointerMove(moveEvent: PointerEvent) {
      const delta = moveEvent.clientX - startX
      const nextWidths =
        side === 'left'
          ? { ...startWidths, left: startWidths.left + delta }
          : { ...startWidths, right: startWidths.right - delta }
      setPaneWidths(clampPaneWidths(nextWidths, side))
    }

    function handlePointerUp() {
      document.documentElement.style.cursor = previousCursor
      document.body.style.userSelect = previousUserSelect
      window.removeEventListener('pointermove', handlePointerMove)
      window.removeEventListener('pointerup', handlePointerUp)
      window.removeEventListener('pointercancel', handlePointerUp)
    }

    window.addEventListener('pointermove', handlePointerMove)
    window.addEventListener('pointerup', handlePointerUp)
    window.addEventListener('pointercancel', handlePointerUp)
  }

  function handlePaneResizeKey(side: PaneSide, event: KeyboardEvent<HTMLButtonElement>) {
    const step = event.shiftKey ? 48 : 16
    let direction = 0
    if (event.key === 'ArrowLeft') direction = side === 'left' ? -1 : 1
    if (event.key === 'ArrowRight') direction = side === 'left' ? 1 : -1
    if (direction === 0) {
      return
    }

    event.preventDefault()
    setPaneWidths((current) =>
      clampPaneWidths(
        {
          ...current,
          [side]: current[side] + direction * step,
        },
        side,
      ),
    )
  }

  const handleShowArchivedSessionsChange = useCallback(
    (showArchived: boolean) => {
      showArchivedSessionsRef.current = showArchived
      setShowArchivedSessions(showArchived)
      if (showArchived) return

      const currentSessions = sessionsRef.current
      const nextSessions = currentSessions.filter((session) => !session.archived_at)
      sessionsRef.current = nextSessions
      setSessions(nextSessions)

      const selected = currentSessions.find((session) => session.id === selectedSessionIDRef.current)
      if (selected?.archived_at) {
        selectOverview('replace')
      }
    },
    [selectOverview],
  )

  const renderAppMenu = () => (
    <AppMenu
      themePreference={theme.preference}
      onThemeChange={theme.setPreference}
      showArchived={showArchivedSessions}
      onShowArchivedChange={handleShowArchivedSessionsChange}
      release={release}
    />
  )

  const renderNotificationsPopover = () => (
    <NotificationsPopover
      notifications={dismissibleNotifications}
      supported={pushNotifications.supported}
      status={pushNotifications.status}
      error={pushNotifications.error}
      soundEnabled={pushNotifications.soundEnabled}
      dismissing={dismissingNotifications}
      onSelectSession={(sessionID) => requestSessionSelection(sessionID, 'push')}
      onDismissAll={handleDismissAllNotifications}
      onEnable={() => void pushNotifications.enable()}
      onDisable={() => void pushNotifications.disable()}
      onSoundEnabledChange={pushNotifications.setSoundEnabled}
    />
  )

  const chatErrorMessage = serverReachable ? error || streamError : ''
  const visibleErrorSessionIDs = new Set(erroredSessionIDs)
  if (selectedSession && chatErrorMessage) {
    visibleErrorSessionIDs.add(selectedSession.id)
  }
  const sessionListProps = {
    sessions,
    selectedSessionID,
    errorSessionIDs: visibleErrorSessionIDs,
    lastSeenSeqBySession: effectiveLastSeenSeqBySession,
    loading: loadingSessions || refreshingSessions,
    onSelect: (sessionID: string) => requestSessionSelection(sessionID, 'push'),
    pinningSessionIDs,
    onPinChange: serverReachable
      ? (sessionID: string, pinned: boolean) => void handlePinSession(sessionID, pinned)
      : undefined,
    creatingChildSessionIDs,
    onCreateChild: serverReachable
      ? openCreateChildSession
      : undefined,
    onMove: serverReachable
      ? (sessionID: string) => {
          setMobileListOpen(false)
          setMoveSessionID(sessionID)
        }
      : undefined,
    archivingSessionID,
    onArchive: serverReachable
      ? (sessionID: string) => requestArchiveSession(sessionID)
      : undefined,
    overviewSelected,
    onOverview: () => selectOverview('push'),
    userSkillsSelected,
    onUserSkills: () => selectUserSkills('push'),
    onSearch: serverReachable
      ? () => {
          setMobileListOpen(false)
          setSpotlightOpen(true)
        }
      : undefined,
    onCreate: () => {
      setCreateParentSession(null)
      setCreateOpen(true)
    },
    createDisabled: !serverReachable,
    notificationAction: renderNotificationsPopover(),
    appMenuAction: renderAppMenu(),
  }
  const list = <SessionList {...sessionListProps} />
  const mobileList = <SessionList {...sessionListProps} variant="embedded" />
  const displayedAppView: AppView = serverReachable || appView === 'files' ? appView : 'session'
  const confirmActionPending =
    pendingSessionAction !== null &&
    confirmSessionAction !== null &&
    pendingSessionAction.sessionID === confirmSessionAction.sessionID &&
    pendingSessionAction.action === confirmSessionAction.action
  const confirmArchiveSession = confirmArchiveSessionID
    ? (sessions.find((session) => session.id === confirmArchiveSessionID) ?? null)
    : null
  const confirmArchivePending = confirmArchiveSessionID !== null && archivingSessionID === confirmArchiveSessionID
  const renderViewToggle = (consoleActions?: ConsoleActions) => (
    <SessionViewNavigation
      view={displayedAppView}
      consoleActions={consoleActions}
      onSelect={(view) => {
        if (serverReachable) selectAppView(view)
      }}
    />
  )
  const viewToggle = renderViewToggle()
  const openSessionsButton = (
    <Button
      type="button"
      size="icon"
      variant="ghost"
      aria-label="Open sessions"
      onClick={() => setMobileListOpen(true)}
      className="h-9 w-9 shrink-0 text-muted-foreground hover:bg-background/50 hover:text-foreground lg:hidden"
    >
      <Menu />
    </Button>
  )
  const currentSessionRoute = selectedSessionRouteFromLocation()
  const resolvingInitialSessionSelection = loadingSessions && !selectedSessionID
  const unresolvedRouteSessionKey = resolvingInitialSessionSelection
    ? (currentSessionRoute.sessionSlug ?? currentSessionRoute.sessionID ?? 'initial-session-selection')
    : null
  const resolvingSelectedSessionID = selectedSession ? null : (selectedSessionID ?? unresolvedRouteSessionKey)
  const resolvingChatSessionID = selectedSession ? null : (selectedSessionID ?? unresolvedRouteSessionKey)
  const isOverview = overviewSelected && selectedSessionID === null
  const isUserSkills = userSkillsSelected && selectedSessionID === null
  const isGlobalView = isOverview || isUserSkills
  const unavailableSessionRoute = !loadingSessions && isSessionLocation() && !selectedSession &&
    serverReachable

  return (
    <ClientDebugContext.Provider value={clientDebug}>
    <main className="app-shell">
      <div className="hidden min-h-0 shrink-0 lg:flex" style={paneWidthStyle(paneWidths.left)}>
        {list}
      </div>

      {!isGlobalView ? (
        <PaneResizeHandle
          label="Resize sessions pane"
          value={paneWidths.left}
          min={paneLimits.leftMin}
          max={paneLimits.leftMax}
          onPointerDown={(event) => beginPaneResize('left', event)}
          onKeyDown={(event) => handlePaneResizeKey('left', event)}
        />
      ) : null}

      <section className="command-workspace flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
        <div className="relative min-h-0 flex-1 overflow-hidden">
          {unavailableSessionRoute ? (
            <section className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
              <h2 className="text-lg font-semibold">Session unavailable</h2>
              <p className="max-w-md text-sm text-muted-foreground">
                {serverReachable ? 'This session link could not be resolved. Choose a session to continue.' : 'This session is not saved on this device. Reconnect or choose a saved session.'}
              </p>
              <Button variant="outline" onClick={() => setMobileListOpen(true)}>Choose session</Button>
            </section>
          ) : !serverReachable && isGlobalView ? (
            <OfflineGlobalView onOpenSessions={() => setMobileListOpen(true)} />
          ) : isOverview ? (
            <DashboardOverview
              refreshKey={dashboardRefreshKey}
              onOpenSession={(sessionID) => requestSessionSelection(sessionID, 'push')}
              onOpenSessions={() => setMobileListOpen(true)}
              onCreate={() => setCreateOpen(true)}
            />
          ) : isUserSkills ? (
            <Suspense fallback={<div role="status" className="p-6 text-sm text-muted-foreground">Loading user skills…</div>}>
              <RepositorySkills userScope onOpenSessions={() => setMobileListOpen(true)} />
            </Suspense>
          ) : isSessionSettingsView(displayedAppView) ? (
            <>
              <div
                data-testid="mobile-floating-settings-header"
                className="mobile-floating-header-shell pointer-events-none absolute inset-x-0 z-20 p-3 lg:hidden"
              >
                <FilesWorkspaceHeader
                  session={selectedSession}
                  resolvingSessionID={resolvingSelectedSessionID}
                  fallbackTitle="Settings"
                  errorMessage={chatErrorMessage}
                  leadingAction={openSessionsButton}
                  headerActions={viewToggle}
                  showParentSession={false}
                  onUpdateTitle={handleUpdateTitle}
                  onUpdateWorkspace={handleUpdateWorkspace}
                  hasUnsavedWorkspaceFile={workspaceFileDirty}
                  onUpdateAgentOptions={handleUpdateAgentOptions}
                  showDebugEvents={showDebugEvents}
                  onShowDebugEventsChange={handleShowDebugEventsChange}
                />
              </div>
              <div
                data-testid="floating-settings-header"
                className="pointer-events-none absolute inset-x-0 top-0 z-20 hidden p-3 lg:block"
              >
                <FilesWorkspaceHeader
                  session={selectedSession}
                  resolvingSessionID={resolvingSelectedSessionID}
                  fallbackTitle="Settings"
                  errorMessage={chatErrorMessage}
                  headerActions={viewToggle}
                  showParentSession={false}
                  onUpdateTitle={handleUpdateTitle}
                  onUpdateWorkspace={handleUpdateWorkspace}
                  hasUnsavedWorkspaceFile={workspaceFileDirty}
                  onUpdateAgentOptions={handleUpdateAgentOptions}
                  showDebugEvents={showDebugEvents}
                  onShowDebugEventsChange={handleShowDebugEventsChange}
                />
              </div>
              <SessionSettingsPage
                section={displayedAppView}
                onSelectSection={selectAppView}
                scheduleRefreshKey={events.filter((event) => event.type.startsWith('schedule.')).length}
                onOpenFile={(path) => void handleOpenWorkspacePath(path)}
                mobileSessionOverview={(
                  <RunHealthRail
                    session={selectedSession}
                    resolvingSessionID={resolvingSelectedSessionID}
                    events={events}
                    activityEvents={liveEvents}
                    streamState={streamState}
                    streamError={chatErrorMessage}
                    showUtilityContent={false}
                    onClear={() => {
                      requestSessionAction('clear')
                      return Promise.resolve()
                    }}
                    onCompact={() => {
                      requestSessionAction('compact')
                      return Promise.resolve()
                    }}
                    onToggleArchive={() => {
                      requestArchiveSession()
                      return Promise.resolve()
                    }}
                    clearPending={
                      selectedSession
                        ? pendingSessionAction?.sessionID === selectedSession.id && pendingSessionAction.action === 'clear'
                        : false
                    }
                    compactPending={
                      selectedSession
                        ? pendingSessionAction?.sessionID === selectedSession.id && pendingSessionAction.action === 'compact'
                        : false
                    }
                    archivePending={selectedSession ? archivingSessionID === selectedSession.id : false}
                    offline={!serverReachable}
                    debugEnabled={clientDebug}
                    onToggleDebug={toggleClientDebug}
                  />
                )}
                session={selectedSession}
                resolvingSessionID={resolvingSelectedSessionID}
                showDebugEvents={showDebugEvents}
                onUpdateTitle={handleUpdateTitle}
                onUpdateWorkspace={handleUpdateWorkspace}
                hasUnsavedWorkspaceFile={workspaceFileDirty}
                onUpdateAgentOptions={handleUpdateAgentOptions}
                onShowDebugEventsChange={handleShowDebugEventsChange}
              />
            </>
          ) : displayedAppView === 'console' ? (
            <HostConsole
              session={selectedSession}
              resolvingSessionID={resolvingSelectedSessionID}
              resolvedTheme={theme.resolvedTheme}
              headerActions={renderViewToggle}
              mobileLeadingAction={openSessionsButton}
            />
          ) : displayedAppView === 'files' ? (
            <>
              <div
                data-testid="mobile-floating-files-header"
                className="mobile-floating-header-shell pointer-events-none absolute inset-x-0 z-20 p-3 lg:hidden"
              >
                <FilesWorkspaceHeader
                  session={selectedSession}
                  resolvingSessionID={resolvingSelectedSessionID}
                  errorMessage={chatErrorMessage}
                  leadingAction={openSessionsButton}
                  headerActions={viewToggle}
                  onUpdateTitle={handleUpdateTitle}
                  onUpdateWorkspace={handleUpdateWorkspace}
                  hasUnsavedWorkspaceFile={workspaceFileDirty}
                  onUpdateAgentOptions={handleUpdateAgentOptions}
                  showDebugEvents={showDebugEvents}
                  onShowDebugEventsChange={handleShowDebugEventsChange}
                  onClear={() => {
                    requestSessionAction('clear')
                    return Promise.resolve()
                  }}
                  onCompact={() => {
                    requestSessionAction('compact')
                    return Promise.resolve()
                  }}
                  onToggleArchive={() => {
                    requestArchiveSession()
                    return Promise.resolve()
                  }}
                  clearPending={
                    selectedSession
                      ? pendingSessionAction?.sessionID === selectedSession.id &&
                        pendingSessionAction.action === 'clear'
                      : false
                  }
                  compactPending={
                    selectedSession
                      ? pendingSessionAction?.sessionID === selectedSession.id &&
                        pendingSessionAction.action === 'compact'
                      : false
                  }
                  archivePending={selectedSession ? archivingSessionID === selectedSession.id : false}
                />
              </div>
              <div
                data-testid="floating-files-header"
                className="pointer-events-none absolute inset-x-0 top-0 z-20 hidden p-3 lg:block"
              >
                <FilesWorkspaceHeader
                  session={selectedSession}
                  resolvingSessionID={resolvingSelectedSessionID}
                  errorMessage={chatErrorMessage}
                  headerActions={viewToggle}
                  onUpdateTitle={handleUpdateTitle}
                  onUpdateWorkspace={handleUpdateWorkspace}
                  hasUnsavedWorkspaceFile={workspaceFileDirty}
                  onUpdateAgentOptions={handleUpdateAgentOptions}
                  showDebugEvents={showDebugEvents}
                  onShowDebugEventsChange={handleShowDebugEventsChange}
                  onClear={() => {
                    requestSessionAction('clear')
                    return Promise.resolve()
                  }}
                  onCompact={() => {
                    requestSessionAction('compact')
                    return Promise.resolve()
                  }}
                  onToggleArchive={() => {
                    requestArchiveSession()
                    return Promise.resolve()
                  }}
                  clearPending={
                    selectedSession
                      ? pendingSessionAction?.sessionID === selectedSession.id &&
                        pendingSessionAction.action === 'clear'
                      : false
                  }
                  compactPending={
                    selectedSession
                      ? pendingSessionAction?.sessionID === selectedSession.id &&
                        pendingSessionAction.action === 'compact'
                      : false
                  }
                  archivePending={selectedSession ? archivingSessionID === selectedSession.id : false}
                />
              </div>
              <WorkspaceFilesView
                session={selectedSession}
                resolvingSessionID={resolvingSelectedSessionID}
                refreshKey={fileRefreshKey}
                selectedFile={openWorkspaceFile}
                resolvedTheme={theme.resolvedTheme}
                onOpenFile={handleOpenWorkspaceFile}
                onFileSaved={setOpenWorkspaceFile}
                onCloseFile={handleCloseWorkspaceFile}
                onDirtyChange={setWorkspaceFileDirty}
                focusedLine={focusedFileLine}
                offline={!serverReachable}
              />
            </>
          ) : (
            <SessionDetail
              key={selectedSessionID ?? resolvingChatSessionID}
              session={selectedSession}
              resolvingSessionID={resolvingChatSessionID}
              events={events}
              liveEvents={liveEvents}
              streamState={streamState}
              hasOlderEvents={hasOlderEvents}
              hasNewerEvents={hasNewerEvents}
              loadingOlderEvents={loadingOlderEvents}
              loadingNewerEvents={loadingNewerEvents}
              olderHistoryUnavailable={olderHistoryUnavailable}
              onLoadOlderEvents={loadOlderEvents}
              onLoadNewerEvents={loadNewerEvents}
              onJumpToLatest={handleJumpToLatest}
              onFollowingTailChange={setFollowingTail}
              errorMessage={chatErrorMessage}
              showDebugEvents={showDebugEvents}
              onSubmitPrompt={handleSubmitPrompt}
              onUpdateRuntimeAgentOptions={handleUpdateRuntimeAgentOptions}
              onAnswerUserInput={handleAnswerUserInput}
              onResolvePermission={handleResolvePermission}
              onCancel={handleCancel}
              onOpenFilePath={handleOpenWorkspacePath}
              onComposerFocus={handleComposerFocus}
              composerFocusRequest={composerFocusRequest}
              onErrorMessageChange={setError}
              focusedEventSeq={focusedEventSeq}
              focusedEventRequest={focusedEventRequest}
              onVisibleSequenceRangeChange={setTranscriptVisibleRange}
              headerActions={viewToggle}
              mobileLeadingAction={openSessionsButton}
              offline={!serverReachable}
              onSelectParent={(sessionID) => requestSessionSelection(sessionID, 'push')}
            />
          )}
        </div>
      </section>

      {!isGlobalView ? (
        <PaneResizeHandle
          label="Resize details pane"
          value={paneWidths.right}
          min={paneLimits.rightMin}
          max={paneLimits.rightMax}
          onPointerDown={(event) => beginPaneResize('right', event)}
          onKeyDown={(event) => handlePaneResizeKey('right', event)}
        />
      ) : null}

      <div
        className={cn('min-h-0 shrink-0', isGlobalView ? 'hidden' : 'hidden lg:flex')}
        style={paneWidthStyle(paneWidths.right)}
      >
        <RunHealthRail
          session={selectedSession}
          resolvingSessionID={resolvingSelectedSessionID}
          events={events}
          activityEvents={liveEvents}
          streamState={streamState}
          streamError={chatErrorMessage}
          fileRefreshKey={fileRefreshKey}
          contentMode={railContent.mode}
          onContentModeChange={railContent.setMode}
          contentActive={!isGlobalView}
          hasOlderEvents={hasOlderEvents}
          hasNewerEvents={hasNewerEvents}
          loadingOlderEvents={loadingOlderEvents}
          loadingNewerEvents={loadingNewerEvents}
          onLoadOlderEvents={loadOlderEvents}
          onLoadNewerEvents={loadNewerEvents}
          onJumpToLatest={handleJumpToLatest}
          visibleSequenceRange={transcriptVisibleRange}
          focusedEventSeq={focusedEventSeq}
          onSelectConversationSeq={handleSelectConversationSeq}
          onClear={() => {
            requestSessionAction('clear')
            return Promise.resolve()
          }}
          onCompact={() => {
            requestSessionAction('compact')
            return Promise.resolve()
          }}
          onToggleArchive={() => {
            requestArchiveSession()
            return Promise.resolve()
          }}
          onOpenFile={handleOpenWorkspaceFile}
          clearPending={
            selectedSession
              ? pendingSessionAction?.sessionID === selectedSession.id && pendingSessionAction.action === 'clear'
              : false
          }
          compactPending={
            selectedSession
              ? pendingSessionAction?.sessionID === selectedSession.id && pendingSessionAction.action === 'compact'
              : false
          }
          archivePending={selectedSession ? archivingSessionID === selectedSession.id : false}
          offline={!serverReachable}
        />
      </div>

      <SessionActionConfirmDialog
        request={confirmSessionAction}
        session={
          confirmSessionAction
            ? (sessions.find((session) => session.id === confirmSessionAction.sessionID) ?? null)
            : null
        }
        pending={confirmActionPending}
        onOpenChange={(open) => {
          if (!open && !pendingSessionAction) {
            setConfirmSessionAction(null)
          }
        }}
        onConfirm={() => void handleConfirmSessionAction()}
      />
      <SpotlightSearch
        open={spotlightOpen}
        sessionID={selectedSessionID}
        onOpenChange={setSpotlightOpen}
        onSelect={(result) => void handleSpotlightResult(result)}
      />
      <ArchiveSessionConfirmDialog
        session={confirmArchiveSession}
        pending={confirmArchivePending}
        onOpenChange={(open) => {
          if (!open && !archivingSessionID) {
            setConfirmArchiveSessionID(null)
          }
        }}
        onConfirm={() => void handleConfirmArchiveSession()}
      />
      <Dialog open={mobileListOpen} onOpenChange={setMobileListOpen}>
        <DialogContent
          aria-describedby={undefined}
          showClose={false}
          className="command-chat-header grid max-h-[min(42rem,calc(100dvh-4rem))] w-[calc(100vw-1.5rem)] max-w-md grid-rows-[auto_minmax(0,1fr)] gap-0 overflow-hidden border-border/90 p-0 shadow-[0_18px_60px_hsl(var(--foreground)/0.18)]"
        >
          <DialogHeader className="border-b border-border/70 p-4">
            <div className="flex items-center justify-between gap-3">
              <DialogTitle>Sessions</DialogTitle>
              <div className="flex shrink-0 items-center gap-2">
                {renderNotificationsPopover()}
                {renderAppMenu()}
                <Button
                  type="button"
                  aria-label="Create session"
                  size="icon"
                  disabled={!serverReachable}
                  onClick={() => {
                    setMobileListOpen(false)
                    setCreateOpen(true)
                  }}
                  className="shadow-sm"
                >
                  <Plus />
                </Button>
                <DialogClose asChild>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label="Close"
                    className="text-muted-foreground hover:bg-background/50 hover:text-foreground"
                  >
                    <X />
                  </Button>
                </DialogClose>
              </div>
            </div>
          </DialogHeader>
          <div className="min-h-0 overflow-hidden">{mobileList}</div>
        </DialogContent>
      </Dialog>
      <CreateSessionDialog
        open={createOpen}
        onOpenChange={handleCreateOpenChange}
        parentSession={createParentSession}
        onCreate={handleCreate}
      />
      <MoveSessionDialog
        open={Boolean(moveSessionID)}
        session={moveSessionID ? sessions.find((session) => session.id === moveSessionID) ?? null : null}
        sessions={sessions}
        onOpenChange={(open) => {
          if (!open) setMoveSessionID(null)
        }}
        onMove={handleMoveSession}
      />
      {clientDebug ? <ClientDebugPanel readSnapshot={readDebugSnapshot} onClose={toggleClientDebug} /> : null}
    </main>
    </ClientDebugContext.Provider>
  )
}

function OfflineGlobalView({ onOpenSessions }: { onOpenSessions: () => void }) {
  return (
    <section className="command-workspace flex h-full w-full min-h-0 flex-col items-center justify-center overflow-hidden p-8 text-center">
      <WifiOff className="mb-3 size-6 text-muted-foreground" aria-hidden="true" />
      <h2 className="text-lg font-semibold">Threave is offline</h2>
      <p className="mt-2 max-w-sm text-sm text-muted-foreground">
        Saved sessions and chat history are still available on this device. New server activity will resume when the connection returns.
      </p>
      <Button type="button" variant="outline" className="mt-4 lg:hidden" onClick={onOpenSessions}>
        Open saved sessions
      </Button>
    </section>
  )
}

function SessionViewNavigation({
  view,
  onSelect,
  consoleActions,
}: {
  view: AppView
  onSelect: (view: AppView) => void
  consoleActions?: ConsoleActions
}) {
  const menuRef = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const { triggerRef, popoverStyle } = useAnchoredPopover(open, 208)
  const views: Array<{ view: AppView; label: string; icon: ReactNode }> = [
    { view: 'session', label: 'Show chat', icon: <MessageSquare className="size-4" /> },
    { view: 'files', label: 'Show files', icon: <Folder className="size-4" /> },
    { view: 'console', label: 'Show console', icon: <Terminal className="size-4" /> },
    { view: 'settings', label: 'Show session settings', icon: <Settings className="size-4" /> },
  ]
  const primaryView = isSessionSettingsView(view) ? 'settings' : view
  const activeIndex = Math.max(0, views.findIndex((item) => item.view === primaryView))

  useEffect(() => {
    if (!open) return
    function handlePointerDown(event: PointerEvent) {
      if (!menuRef.current?.contains(event.target as Node)) setOpen(false)
    }
    function handleKeyDown(event: globalThis.KeyboardEvent) {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('pointerdown', handlePointerDown)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [open])

  function select(nextView: AppView) {
    onSelect(nextView === 'settings' && window.innerWidth < 1024 ? 'activity' : nextView)
  }

  return (
    <div className="flex shrink-0 items-center gap-1">
      <div className="relative grid shrink-0 grid-cols-4 rounded-md bg-muted p-1 shadow-inner">
        <span
          aria-hidden="true"
          className="absolute bottom-1 left-1 top-1 w-8 rounded-sm bg-background shadow-sm transition-transform duration-150 ease-out"
          style={{ transform: `translateX(${activeIndex * 2}rem)` }}
        />
        {views.map((item) => (
          <button
            key={item.view}
            type="button"
            aria-label={item.label}
            aria-pressed={primaryView === item.view}
            className={cn(
              'relative z-10 flex h-8 w-8 items-center justify-center rounded-sm border-0 bg-transparent p-0 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              primaryView === item.view ? 'text-foreground' : 'text-muted-foreground hover:text-foreground',
            )}
            onClick={() => select(item.view)}
          >
            {item.icon}
          </button>
        ))}
      </div>
      {consoleActions ? <div ref={menuRef} className="relative shrink-0 lg:hidden">
        <Button
          ref={triggerRef}
          type="button"
          variant="ghost"
          size="icon"
          className="h-9 w-9 text-muted-foreground hover:bg-background/50 hover:text-foreground"
          aria-label="Console actions"
          aria-haspopup="menu"
          aria-expanded={open}
          onClick={() => setOpen((value) => !value)}
        >
          <MoreHorizontal aria-hidden="true" />
        </Button>
        {open ? (
          <div role="menu" aria-label="Console actions" style={popoverStyle} className="z-50 rounded-lg border border-border/80 bg-popover p-1.5 text-sm text-popover-foreground shadow-lg">
            <button type="button" role="menuitem" className="flex w-full items-center gap-2 rounded-md px-2 py-2 text-left hover:bg-accent hover:text-accent-foreground disabled:opacity-50" disabled={consoleActions.pending} onClick={() => { setOpen(false); consoleActions.onRestart() }}>
              <RefreshCw className="size-4" aria-hidden="true" />Restart console
            </button>
            <button type="button" role="menuitem" className="flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-destructive hover:bg-destructive/10 disabled:opacity-50" disabled={consoleActions.pending} onClick={() => { setOpen(false); consoleActions.onStop() }}>
              <Square className="size-4" aria-hidden="true" />Stop console
            </button>
          </div>
        ) : null}
      </div> : null}
    </div>
  )
}

function FilesWorkspaceHeader({
  session,
  resolvingSessionID,
  fallbackTitle = 'Files',
  errorMessage,
  leadingAction,
  headerActions,
  showParentSession = true,
}: {
  session: Session | null
  resolvingSessionID: string | null
  fallbackTitle?: string
  errorMessage: string
  leadingAction?: ReactNode
  headerActions?: ReactNode
  showParentSession?: boolean
  onUpdateTitle: (title: string) => Promise<void>
  onUpdateWorkspace: (workspacePath: string) => Promise<void>
  hasUnsavedWorkspaceFile: boolean
  onUpdateAgentOptions: (agentOptions: SessionAgentOptions) => Promise<void>
  showDebugEvents: boolean
  onShowDebugEventsChange: (showDebugEvents: boolean) => void
  onClear?: () => Promise<void>
  onCompact?: () => Promise<void>
  onToggleArchive?: () => Promise<void>
  clearPending?: boolean
  compactPending?: boolean
  archivePending?: boolean
}) {
  if (session) {
    return (
      <ChatSessionHeader
        session={session}
        errorMessage={errorMessage}
        headerActions={headerActions}
        leadingAction={leadingAction}
        showParentSession={showParentSession}
      />
    )
  }

  return (
    <div className="pointer-events-auto">
      <div className="command-chat-header flex min-h-14 items-center justify-between gap-3 rounded-xl border border-border/90 px-3 py-2 shadow-[0_10px_30px_hsl(var(--foreground)/0.10)]">
        {leadingAction ? <div className="shrink-0">{leadingAction}</div> : null}
        <div className="min-w-0 flex-1">
          <p className="truncate text-lg font-semibold">{resolvingSessionID ? 'Loading session...' : fallbackTitle}</p>
        </div>
        {headerActions}
      </div>
    </div>
  )
}

function SessionActionConfirmDialog({
  request,
  session,
  pending,
  onOpenChange,
  onConfirm,
}: {
  request: PendingSessionAction | null
  session: Session | null
  pending: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => void
}) {
  const action = request?.action ?? 'compact'
  const copy = sessionActionDialogCopy(action, session?.agent_type)

  return (
    <Dialog open={Boolean(request)} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{copy.title}</DialogTitle>
          <DialogDescription>{copy.description}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <p className="truncate text-sm text-muted-foreground" title={session?.title || undefined}>
            {session?.title || 'Selected session'}
          </p>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" disabled={pending} onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="button" disabled={pending} onClick={onConfirm}>
              {pending ? copy.pendingLabel : copy.confirmLabel}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function ArchiveSessionConfirmDialog({
  session,
  pending,
  onOpenChange,
  onConfirm,
}: {
  session: Session | null
  pending: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => void
}) {
  const isArchived = Boolean(session?.archived_at)

  return (
    <Dialog open={Boolean(session)} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{isArchived ? 'Restore session?' : 'Archive session?'}</DialogTitle>
          <DialogDescription>
            {isArchived
              ? 'Return this session to the active list.'
              : 'Hide this session from the active list. Its event history and files remain stored.'}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <p className="truncate text-sm text-muted-foreground" title={session?.title || undefined}>
            {session?.title || 'Selected session'}
          </p>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" disabled={pending} onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button
              type="button"
              variant={isArchived ? 'default' : 'destructive'}
              disabled={pending}
              onClick={onConfirm}
            >
              {pending ? (isArchived ? 'Restoring' : 'Archiving') : isArchived ? 'Restore' : 'Archive'}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function sessionActionDialogCopy(action: SessionContextAction, agentType?: string) {
  if (action === 'clear') {
    const providerName = agentType === 'opencode' ? 'OpenCode session' : 'Codex thread'
    return {
      title: 'Clear context?',
      description:
        `Start a fresh ${providerName} for this Threave session. Existing Threave activity stays visible in the transcript.`,
      confirmLabel: 'Clear',
      pendingLabel: 'Clearing',
    }
  }

  return {
    title: 'Compact context?',
    description:
      'Ask Codex to summarize the current thread context so the session can continue with less token pressure.',
    confirmLabel: 'Compact',
    pendingLabel: 'Compacting',
  }
}

function PaneResizeHandle({
  label,
  value,
  min,
  max,
  onPointerDown,
  onKeyDown,
}: {
  label: string
  value: number
  min: number
  max: number
  onPointerDown: (event: ReactPointerEvent<HTMLButtonElement>) => void
  onKeyDown: (event: KeyboardEvent<HTMLButtonElement>) => void
}) {
  return (
    <button
      type="button"
      role="separator"
      aria-label={label}
      aria-orientation="vertical"
      aria-valuemin={min}
      aria-valuemax={max}
      aria-valuenow={Math.round(value)}
      className="pane-resize-handle hidden shrink-0 lg:block"
      onPointerDown={onPointerDown}
      onKeyDown={onKeyDown}
    />
  )
}

async function includeSelectedSession(sessions: Session[], selectedSessionID: string | null) {
  if (!selectedSessionID || sessions.some((session) => session.id === selectedSessionID)) {
    return sessions
  }

  try {
    const selectedSession = await getSession(selectedSessionID)
    return [selectedSession, ...sessions]
  } catch {
    return sessions
  }
}

function preferFresherSessionSnapshots(incoming: Session[], current: Session[]) {
  const currentByID = new Map(current.map((session) => [session.id, session]))
  return incoming.map((session) => {
    const existing = currentByID.get(session.id)
    return existing && latestSessionSeq(existing) > latestSessionSeq(session) ? existing : preserveSessionActivity(session, existing)
  })
}

function addSetValue(current: ReadonlySet<string>, value: string | null) {
  if (!value || current.has(value)) {
    return current
  }
  return new Set([...current, value])
}

function removeSetValue(current: ReadonlySet<string>, value: string) {
  if (!current.has(value)) {
    return current
  }
  const next = new Set(current)
  next.delete(value)
  return next
}

function paneWidthStyle(width: number): CSSProperties {
  return { width: `${Math.round(width)}px` }
}

function loadPaneWidths() {
  if (typeof window === 'undefined') {
    return defaultPaneWidths
  }
  try {
    const raw = window.localStorage.getItem(paneWidthsStorageKey)
    if (!raw) {
      return defaultPaneWidths
    }
    const parsed = JSON.parse(raw) as Partial<PaneWidths>
    return clampStoredPaneWidths({
      left: Number(parsed.left) || defaultPaneWidths.left,
      right: Number(parsed.right) || defaultPaneWidths.right,
    })
  } catch {
    return defaultPaneWidths
  }
}

function savePaneWidths(widths: PaneWidths) {
  if (typeof window === 'undefined') {
    return
  }
  try {
    window.localStorage.setItem(paneWidthsStorageKey, JSON.stringify(widths))
  } catch {
    // Resizing remains functional when storage is unavailable.
  }
}

function clampStoredPaneWidths(widths: PaneWidths): PaneWidths {
  return {
    left: clamp(widths.left, paneLimits.leftMin, paneLimits.leftMax),
    right: clamp(widths.right, paneLimits.rightMin, paneLimits.rightMax),
  }
}

function clampPaneWidths(widths: PaneWidths, changedSide?: PaneSide): PaneWidths {
  let next = clampStoredPaneWidths(widths)
  if (typeof window === 'undefined') {
    return next
  }

  const maxCombinedWidth = Math.max(
    paneLimits.leftMin + paneLimits.rightMin,
    window.innerWidth - paneLimits.centerMin - 18,
  )
  let overflow = next.left + next.right - maxCombinedWidth
  if (overflow <= 0) {
    return next
  }

  if (changedSide === 'left') {
    const leftReduction = Math.min(overflow, next.left - paneLimits.leftMin)
    next = { ...next, left: next.left - leftReduction }
    overflow -= leftReduction
  } else {
    const rightReduction = Math.min(overflow, next.right - paneLimits.rightMin)
    next = { ...next, right: next.right - rightReduction }
    overflow -= rightReduction
  }

  if (overflow > 0) {
    if (changedSide === 'left') {
      next = {
        ...next,
        right: Math.max(paneLimits.rightMin, next.right - overflow),
      }
    } else {
      next = {
        ...next,
        left: Math.max(paneLimits.leftMin, next.left - overflow),
      }
    }
  }

  return next
}

function clamp(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max)
}

function workspaceRelativeFilePath(path: string, workspacePath: string) {
  const filePath = path
    .trim()
    .replaceAll('\\', '/')
    .replace(/:\d+(?::\d+)?$/, '')
  if (!filePath) {
    throw new Error('File path is unavailable.')
  }
  if (!filePath.startsWith('/')) {
    return filePath.replace(/^\.\//, '')
  }

  const workspaceRoot = workspacePath.trim().replaceAll('\\', '/').replace(/\/+$/, '')
  if (workspaceRoot && filePath.startsWith(`${workspaceRoot}/`)) {
    return filePath.slice(workspaceRoot.length + 1)
  }
  throw new Error('File change is outside the session workspace.')
}

function messageFromError(error: unknown) {
  return error instanceof Error ? error.message : 'Request failed'
}

function loadSessionDebugPreference(sessionID: string | null) {
  if (!sessionID) {
    return false
  }
  try {
    return window.localStorage.getItem(debugStorageKey(sessionID)) === 'true'
  } catch {
    return false
  }
}

function saveSessionDebugPreference(sessionID: string | null, showDebugEvents: boolean) {
  if (!sessionID) {
    return
  }
  try {
    window.localStorage.setItem(debugStorageKey(sessionID), String(showDebugEvents))
  } catch {
    // Keep the UI functional when storage is unavailable.
  }
}

function loadSessionSeenSeqs(): Record<string, number> {
  if (typeof window === 'undefined') {
    return {}
  }
  try {
    const raw = window.localStorage.getItem(sessionSeenSeqStorageKey)
    if (!raw) {
      return {}
    }
    const parsed = JSON.parse(raw) as Record<string, unknown>
    const seen: Record<string, number> = {}
    for (const [sessionID, value] of Object.entries(parsed)) {
      const seq = Number(value)
      if (sessionID && Number.isFinite(seq) && seq > 0) {
        seen[sessionID] = seq
      }
    }
    return seen
  } catch {
    return {}
  }
}

function saveSessionSeenSeqs(seenSeqs: Record<string, number>) {
  if (typeof window === 'undefined') {
    return
  }
  try {
    window.localStorage.setItem(sessionSeenSeqStorageKey, JSON.stringify(seenSeqs))
  } catch {
    // Seen state is best-effort and browser-local.
  }
}

function applyNotificationAttentionSeqs(
  seenSeqs: Record<string, number>,
  notificationSeqs: Record<string, number>,
): Record<string, number> {
  let next = seenSeqs
  for (const [sessionID, seq] of Object.entries(notificationSeqs)) {
    if (!sessionID || !Number.isFinite(seq) || seq <= 0) {
      continue
    }
    const heldSeenSeq = Math.max(0, seq - 1)
    if ((next[sessionID] ?? 0) <= heldSeenSeq) {
      continue
    }
    if (next === seenSeqs) {
      next = { ...seenSeqs }
    }
    if (heldSeenSeq > 0) {
      next[sessionID] = heldSeenSeq
    } else {
      delete next[sessionID]
    }
  }
  return next
}

function notificationAttentionSeqsFromSessions(sessions: Session[]): Record<string, number> {
  const seqs: Record<string, number> = {}
  for (const session of sessions) {
    const seq = Number(session.notification_attention_seq)
    if (session.id && Number.isFinite(seq) && seq > 0) {
      seqs[session.id] = Math.max(seqs[session.id] ?? 0, seq)
    }
  }
  return seqs
}

function mergeNotificationAttentionSeqs(
  first: Record<string, number>,
  second: Record<string, number>,
): Record<string, number> {
  const merged: Record<string, number> = {}
  for (const source of [first, second]) {
    for (const [sessionID, value] of Object.entries(source)) {
      const seq = Number(value)
      if (sessionID && Number.isFinite(seq) && seq > 0) {
        merged[sessionID] = Math.max(merged[sessionID] ?? 0, seq)
      }
    }
  }
  return merged
}

function notificationAttentionFromLocation(): {
  sessionID: string
  seq: number
} | null {
  if (typeof window === 'undefined') {
    return null
  }
  const route = selectedSessionRouteFromLocation()
  if (!route.sessionID) {
    return null
  }
  const seq = Number(new URLSearchParams(window.location.search).get('notification_seq'))
  if (!Number.isFinite(seq) || seq <= 0) {
    return null
  }
  return { sessionID: route.sessionID, seq }
}

function clearNotificationAttentionSearchParam() {
  if (typeof window === 'undefined') {
    return
  }
  const params = new URLSearchParams(window.location.search)
  if (!params.has('notification_seq')) {
    return
  }
  params.delete('notification_seq')
  const nextSearch = params.toString()
  const nextURL = `${window.location.pathname}${nextSearch ? `?${nextSearch}` : ''}${window.location.hash}`
  window.history.replaceState({}, '', nextURL)
}

function debugStorageKey(sessionID: string) {
  return `${debugStorageKeyPrefix}${sessionID}`
}

function loadInitialSessionStateFromLocation(includeArchived = false): InitialSessionState {
  const route = selectedSessionRouteFromLocation()
  const cachedSession = cachedSessionForRoute(route)
  const cachedSessions = sortSessions(
    [...readCachedSessionSnapshots(includeArchived), ...(cachedSession ? [cachedSession] : [])].filter(
      (session, index, items) =>
        (includeArchived || !session.archived_at) &&
        items.findIndex((item) => item.id === session.id) === index,
    ),
  )
  const rootOffline = !route.sessionID && !route.sessionSlug && !isUserSkillsLocation() && !browserIsOnline()
  const rootOfflineSessionID = rootOffline ? preferredCachedSessionID(cachedSessions) : null

  return {
    sessions: cachedSessions,
    selectedSessionID: cachedSession?.id ?? route.sessionID ?? rootOfflineSessionID,
    seededCachedSession: cachedSessions.length > 0,
    restoredOfflineSessionAtRoot: Boolean(rootOfflineSessionID),
  }
}

function preferredCachedSessionID(sessions: Session[]) {
  const stored = loadLastSelectedSessionID()
  if (stored && sessions.some((session) => session.id === stored)) return stored
  return sessions[0]?.id ?? null
}

function loadLastSelectedSessionID() {
  if (typeof window === 'undefined') return null
  try {
    return window.localStorage.getItem(lastSelectedSessionStorageKey)
  } catch {
    return null
  }
}

function saveLastSelectedSessionID(sessionID: string) {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(lastSelectedSessionStorageKey, sessionID)
  } catch {
    // Session navigation should still work when storage is unavailable.
  }
}

function loadShowArchivedSessionsPreference() {
  if (typeof window === 'undefined') return false
  try {
    return window.localStorage.getItem(showArchivedSessionsStorageKey) === 'true'
  } catch {
    return false
  }
}

function saveShowArchivedSessionsPreference(showArchived: boolean) {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(showArchivedSessionsStorageKey, String(showArchived))
  } catch {
    // Keep the in-memory preference when storage is unavailable.
  }
}

function isLikelyNetworkErrorMessage(message: string) {
  const normalized = message.trim().toLowerCase()
  return normalized === 'failed to fetch' || normalized === 'load failed' || normalized.includes('networkerror')
}

function cachedSessionForRoute(route: SessionRoute) {
  if (route.sessionID) {
    return readCachedSessionSnapshot(route.sessionID)
  }
  if (route.sessionSlug) {
    return readCachedSessionSnapshotBySlug(route.sessionSlug)
  }
  return null
}

function selectedSessionRouteFromLocation() {
  if (typeof window === 'undefined') {
    return {
      sessionID: null,
      sessionSlug: null,
      view: 'session' as const,
      filePath: null,
    }
  }
  return sessionRouteFromPathname(window.location.pathname)
}

function eventSequenceFromLocation() {
  return positiveLocationSearchNumber('event_seq')
}

function fileLineFromLocation() {
  return positiveLocationSearchNumber('line')
}

function positiveLocationSearchNumber(name: string) {
  if (typeof window === 'undefined') return 0
  const value = Number.parseInt(new URLSearchParams(window.location.search).get(name) ?? '', 10)
  return Number.isSafeInteger(value) && value > 0 ? value : 0
}

function isSessionLocation() {
  return typeof window !== 'undefined' && window.location.pathname.startsWith('/sessions/')
}

function isUserSkillsLocation() {
  return typeof window !== 'undefined' && window.location.pathname === '/skills'
}

function resolveSessionRouteSessionID(route: SessionRoute, sessions: Session[]) {
  if (route.sessionID) {
    return route.sessionID
  }
  if (!route.sessionSlug) {
    return null
  }
  const matches = sessions.filter((session) => sessionTitleSlug(session.title) === route.sessionSlug)
  return matches.length === 1 ? matches[0].id : null
}

function preferredSessionIDForRouteSlug(
  route: SessionRoute,
  sessions: Session[],
  selectedSessionID: string | null,
) {
  if (!route.sessionSlug) return null
  const preferredIDs = [selectedSessionID, loadLastSelectedSessionID()]
  return preferredIDs.find((sessionID) =>
    Boolean(
      sessionID &&
      sessions.some(
        (session) => session.id === sessionID && sessionTitleSlug(session.title) === route.sessionSlug,
      ),
    ),
  ) ?? null
}

function writeSelectedSessionRoute(
  sessionID: string | null,
  historyMode: Exclude<SessionRouteHistoryMode, 'none'>,
  view: AppView = 'session',
  sessions: Session[] = [],
  filePath: string | null = null,
) {
  if (typeof window === 'undefined') {
    return
  }

  const currentRoute = selectedSessionRouteFromLocation()
  const routeSession = sessionID ? sessions.find((session) => session.id === sessionID) : null
  const currentRouteSessionID = resolveSessionRouteSessionID(currentRoute, sessions)
  const routeSessionSlug = routeSession ? sessionTitleSlug(routeSession.title) : null
  const routeSessionHasUniqueSlug = Boolean(
    routeSessionSlug && sessions.filter((session) => sessionTitleSlug(session.title) === routeSessionSlug).length === 1,
  )
  const path = routeSession && routeSessionHasUniqueSlug
    ? sessionSlugPath(sessionTitleSlug(routeSession.title), view, filePath)
    : currentRoute.sessionSlug && currentRouteSessionID === sessionID
      ? sessionSlugPath(currentRoute.sessionSlug, view, filePath)
      : sessionPath(sessionID, view, filePath)
  const url = clientDebugURL(path)
  if (`${window.location.pathname}${window.location.search}` === url) {
    return
  }

  if (historyMode === 'replace') {
    window.history.replaceState({}, '', url)
    return
  }
  window.history.pushState({}, '', url)
}

function writeSpotlightResultRoute(
  session: Session,
  view: AppView,
  filePath: string | null,
  target: { eventSeq?: number; line?: number },
) {
  if (typeof window === 'undefined') return
  const path = sessionSlugPath(sessionTitleSlug(session.title), view, filePath)
  const params = new URLSearchParams()
  if (target.eventSeq && target.eventSeq > 0) params.set('event_seq', String(target.eventSeq))
  if (target.line && target.line > 0) params.set('line', String(target.line))
  const url = clientDebugURL(params.size > 0 ? `${path}?${params}` : path)
  if (`${window.location.pathname}${window.location.search}` === url) return
  window.history.pushState({}, '', url)
}

function createActivityClientID() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `client-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
}

export default App
