package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/threave-io/threave/internal/agents"
	"github.com/threave-io/threave/internal/console"
	eventservice "github.com/threave-io/threave/internal/events"
	"github.com/threave-io/threave/internal/hosting"
	"github.com/threave-io/threave/internal/notifications"
	"github.com/threave-io/threave/internal/reposkills"
	"github.com/threave-io/threave/internal/scheduler"
	runcontrol "github.com/threave-io/threave/internal/session"
	"github.com/threave-io/threave/internal/store"
)

const (
	defaultEventLimit         = 500
	maxEventLimit             = 1000
	maxEventTurnLimit         = 50
	maxEventTurnPageLimit     = 5000
	eventHistoryBackfillStep  = 250
	maxEventPayloadStringLen  = 64 * 1024
	maxToolEventPayloadBytes  = 24 * 1024
	maxOtherEventPayloadBytes = 64 * 1024
	maxEventHistoryBytes      = 2 * 1024 * 1024
	defaultSessionLimit       = 50
	maxSessionLimit           = 100
	streamHeartbeat           = 15 * time.Second
	streamResyncEventType     = "stream.resync.required"
	immutableAssetCache       = "public, max-age=31536000, immutable"
	revalidatingCache         = "no-cache"
	staticShellCache          = "public, max-age=3600"
)

type Store interface {
	CreateSession(ctx context.Context, params store.CreateSessionParams) (store.Session, error)
	GetSession(ctx context.Context, id string) (store.Session, error)
	ListSessions(ctx context.Context, params store.ListSessionsParams) ([]store.Session, error)
	ArchiveSession(ctx context.Context, params store.ArchiveSessionParams) (store.Session, error)
	RestoreSession(ctx context.Context, params store.RestoreSessionParams) (store.Session, error)
	UpdateSessionTitle(ctx context.Context, params store.UpdateSessionTitleParams) (store.Session, error)
	UpdateSessionParent(ctx context.Context, params store.UpdateSessionParentParams) (store.Session, error)
	UpdateSessionWorkspace(ctx context.Context, params store.UpdateSessionWorkspaceParams) (store.Session, error)
	UpdateSessionAgentOptions(ctx context.Context, params store.UpdateSessionAgentOptionsParams) (store.Session, error)
	UpdateSessionPin(ctx context.Context, params store.UpdateSessionPinParams) (store.Session, error)
	UpdateSessionStatus(ctx context.Context, params store.UpdateSessionStatusParams) (store.Session, error)
	SetSessionProviderSessionID(ctx context.Context, params store.SetSessionProviderSessionIDParams) (store.Session, error)
	ClearSessionProviderSessionID(ctx context.Context, params store.ClearSessionProviderSessionIDParams) (store.Session, error)
	EnqueueMessage(ctx context.Context, params store.EnqueueMessageParams) (store.QueuedMessage, error)
	ListQueuedMessages(ctx context.Context, sessionID string) ([]store.QueuedMessage, error)
	RemoveQueuedMessage(ctx context.Context, params store.QueueMessageIDParams) (store.QueuedMessage, error)
	ClaimNextQueuedMessage(ctx context.Context, sessionID string) (store.QueuedMessage, error)
	MarkQueuedMessageSent(ctx context.Context, params store.QueueMessageIDParams) (store.QueuedMessage, error)
	ReleaseQueuedMessage(ctx context.Context, params store.QueueMessageIDParams) (store.QueuedMessage, error)
	ListEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]store.Event, error)
	ListEventsFiltered(ctx context.Context, sessionID string, afterSeq int64, limit int, filter store.EventListFilter) ([]store.Event, error)
	GetEvent(ctx context.Context, sessionID string, seq int64) (store.Event, error)
	ListRecentEvents(ctx context.Context, sessionID string, limit int) ([]store.Event, error)
	ListRecentEventsFiltered(ctx context.Context, sessionID string, limit int, filter store.EventListFilter) ([]store.Event, error)
	ListEventsBefore(ctx context.Context, sessionID string, beforeSeq int64, limit int) ([]store.Event, error)
	ListEventsBeforeFiltered(ctx context.Context, sessionID string, beforeSeq int64, limit int, filter store.EventListFilter) ([]store.Event, error)
	ListRecentEventTurnsFiltered(ctx context.Context, sessionID string, turns int, filter store.EventListFilter) ([]store.Event, error)
	ListEventTurnsBeforeFiltered(ctx context.Context, sessionID string, beforeSeq int64, turns int, filter store.EventListFilter) ([]store.Event, error)
	ListRecentEventTurnsPageFiltered(ctx context.Context, sessionID string, turns int, limit int, filter store.EventListFilter) ([]store.Event, error)
	ListEventTurnsBeforePageFiltered(ctx context.Context, sessionID string, beforeSeq int64, turns int, limit int, filter store.EventListFilter) ([]store.Event, error)
	ListEventTurnsAfterFiltered(ctx context.Context, sessionID string, afterSeq int64, turns int, limit int, filter store.EventListFilter) ([]store.Event, error)
	ClearNotificationAttention(ctx context.Context, sessionID string) error
	ClearAllNotificationAttention(ctx context.Context) error
}

type DashboardStore interface {
	Dashboard(context.Context, store.DashboardParams) (store.DashboardData, error)
	ListDashboardRuns(context.Context, store.DashboardRunListParams) (store.DashboardRunPage, error)
}

type SearchStore interface {
	Search(context.Context, string, int) ([]store.SearchResult, error)
	SearchSessions(context.Context, string, int) ([]store.SearchResult, error)
	SearchHistory(context.Context, string, int) ([]store.SearchResult, error)
}

type EventBlobStore interface {
	GetEventBlob(context.Context, string, int64, string, int) (store.EventBlob, error)
}

type GlobalEventStore interface {
	GlobalEventCursor(context.Context) (int64, error)
	ListGlobalEventsFiltered(context.Context, int64, int, store.EventListFilter) ([]store.Event, error)
}

type EventService interface {
	Append(ctx context.Context, params eventservice.AppendParams) (store.Event, error)
	Subscribe(sessionID string) (<-chan store.Event, func())
	SubscribeAll() (<-chan store.Event, func())
}

type SessionActivityEventService interface {
	SubscribeSessionActivity(clientID string, watchedSessionID string, includeDebug bool, allTransient bool) (<-chan store.Event, func())
	WatchSessionActivity(clientID string, watchedSessionID string, includeDebug bool) bool
	SessionActivityWatch(clientID string) (watchedSessionID string, includeDebug bool, ok bool)
	SessionActivityReceivesAllTransient(clientID string) bool
	LiveSnapshot() eventservice.SessionLiveSnapshot
	LiveSessionSnapshot(sessionID string) eventservice.SessionLiveSnapshot
}

type SessionActivityStatsService interface {
	SessionActivityStats() eventservice.SessionActivityStats
}

type RecentEventService interface {
	Recent(sessionID string) []store.Event
}

type MaintenanceService interface {
	Status(context.Context) (store.EventMaintenanceStatus, error)
}

type AgentRegistry interface {
	Get(agentType string) (agents.Agent, bool)
}

type RunManager interface {
	Register(parent context.Context, sessionID string) (context.Context, func(), error)
	RegisterRun(parent context.Context, sessionID string, runID string) (context.Context, func(), error)
	Cancel(sessionID string, cancellation runcontrol.Cancellation) error
	CancelRun(sessionID string, runID string, cancellation runcontrol.Cancellation) error
	Cancellation(sessionID string) (runcontrol.Cancellation, bool)
	Active(sessionID string) bool
	ActiveRun(sessionID string, runID string) bool
	OpenUserInput(ctx context.Context, request agents.UserInputRequest) (agents.UserInputWaiter, error)
	PendingUserInput(sessionID string, requestID string) (agents.UserInputRequest, error)
	AnswerUserInputWithPersistence(ctx context.Context, sessionID string, requestID string, response agents.UserInputResponse, persist func() error) error
	OpenPermission(ctx context.Context, request agents.PermissionRequest) (agents.PermissionWaiter, error)
	PendingPermission(sessionID string, requestID string) (agents.PermissionRequest, error)
	ResolvePermission(sessionID string, requestID string, response agents.PermissionResponse) error
}

type NotificationService interface {
	PublicKey(ctx context.Context) (string, error)
	SaveSubscription(ctx context.Context, input notifications.SubscriptionInput) (store.PushSubscription, error)
	DeleteSubscription(ctx context.Context, endpoint string) error
	Acknowledge(ctx context.Context, endpoint string, sessionID string, seq int64) error
	Seen(ctx context.Context, sessionID string, seq int64) error
	SendTest(ctx context.Context) error
	SendBadgeVariantTest(ctx context.Context, variant string) error
	Debug(ctx context.Context) (notifications.DebugState, error)
}

// HostRuntimeStore is deliberately separate from Store so the hosted-preview
// control plane does not expand every HTTP store fake and alternate backend.
type HostRuntimeStore interface {
	GetHostRuntime(ctx context.Context, sessionID string) (store.HostRuntime, error)
}

// HostingManager is the HTTP-facing portion of the hosted-preview supervisor.
// Keeping this as a narrow interface makes API behavior testable without
// starting child processes or binding ports.
type HostingManager interface {
	Start(context.Context, hosting.StartRequest) (hosting.Snapshot, error)
	Stop(context.Context, string) (hosting.Snapshot, error)
	Restart(context.Context, hosting.StartRequest) (hosting.Snapshot, error)
	Status(string) (hosting.Snapshot, error)
	Wait(context.Context, string) (hosting.Snapshot, error)
	Check(context.Context, string) ([]hosting.ServiceCheck, error)
	Logs(string, uint64, int, string) (hosting.LogSnapshot, error)
	SubscribeLogs(string, uint64, string) (hosting.LogSnapshot, <-chan hosting.LogChunk, func(), error)
	LookupHost(string) (string, bool)
	ServeHTTP(http.ResponseWriter, *http.Request)
}

type Dependencies struct {
	Store             Store
	Events            EventService
	Agents            AgentRegistry
	Runs              RunManager
	Console           *console.Manager
	Notifications     NotificationService
	Workdir           string
	WorkspaceRoots    []string
	StaticAssets      fs.FS
	AgentAPIURL       string
	Executable        string
	Hosting           HostingManager
	HostStore         HostRuntimeStore
	Schedules         *scheduler.Service
	Maintenance       MaintenanceService
	UserHome          string
	MaxLineageDepth   int
	MaxActiveChildren int
}

type API struct {
	store             Store
	events            EventService
	agents            AgentRegistry
	runs              RunManager
	console           *console.Manager
	notifications     NotificationService
	workdir           string
	workspaces        workspaceConfig
	staticAssets      fs.FS
	agentAPIURL       string
	executable        string
	hosting           HostingManager
	hostStore         HostRuntimeStore
	dashboard         DashboardStore
	search            SearchStore
	schedules         *scheduler.Service
	maintenance       MaintenanceService
	repositorySkills  *reposkills.Manager
	performance       *performanceDiagnosticsStore
	agentOptionsMu    *sync.Mutex
	userHome          string
	maxLineageDepth   int
	maxActiveChildren int
}

var _ RunManager = (*runcontrol.Manager)(nil)

type healthResponse struct {
	Status string `json:"status"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type eventResponse struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Seq       int64           `json:"seq"`
	GlobalSeq int64           `json:"global_seq,omitempty"`
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Status    string          `json:"status"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
	Transient bool            `json:"transient,omitempty"`
}

type eventHistoryResponse struct {
	Events      []eventResponse  `json:"events"`
	Page        eventHistoryPage `json:"page"`
	InputEvents []eventResponse  `json:"input_events"`
}

type eventHistoryPage struct {
	FirstSeq      int64 `json:"first_seq"`
	LastSeq       int64 `json:"last_seq"`
	ServerLastSeq int64 `json:"server_last_seq"`
	HasOlder      bool  `json:"has_older"`
	HasNewer      bool  `json:"has_newer"`
	StartsMidTurn bool  `json:"starts_mid_turn"`
	EndsMidTurn   bool  `json:"ends_mid_turn"`
}

type eventHistoryResult struct {
	Events       []store.Event
	PreferLatest bool
}

func NewRouter(deps ...Dependencies) http.Handler {
	api := API{
		performance:       &performanceDiagnosticsStore{},
		agentOptionsMu:    &sync.Mutex{},
		maxLineageDepth:   defaultMaxLineageDepth,
		maxActiveChildren: defaultMaxActiveChildren,
	}
	if len(deps) > 0 {
		api.store = deps[0].Store
		api.events = deps[0].Events
		api.agents = deps[0].Agents
		api.runs = deps[0].Runs
		api.console = deps[0].Console
		api.notifications = deps[0].Notifications
		api.workdir = deps[0].Workdir
		api.workspaces = newWorkspaceConfig(deps[0].Workdir, deps[0].WorkspaceRoots)
		api.staticAssets = deps[0].StaticAssets
		api.agentAPIURL = deps[0].AgentAPIURL
		api.executable = deps[0].Executable
		api.hosting = deps[0].Hosting
		api.hostStore = deps[0].HostStore
		api.schedules = deps[0].Schedules
		api.maintenance = deps[0].Maintenance
		api.userHome = strings.TrimSpace(deps[0].UserHome)
		if deps[0].MaxLineageDepth > 0 {
			api.maxLineageDepth = deps[0].MaxLineageDepth
		}
		if deps[0].MaxActiveChildren > 0 {
			api.maxActiveChildren = deps[0].MaxActiveChildren
		}
		if dashboard, ok := deps[0].Store.(DashboardStore); ok {
			api.dashboard = dashboard
		}
		if search, ok := deps[0].Store.(SearchStore); ok {
			api.search = search
		}
	}
	if api.console == nil {
		api.console = console.NewManager()
	}
	api.repositorySkills = reposkills.NewManager()
	if api.userHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			api.userHome = normalizeWorkspaceRoot(home)
		}
	}

	r := chi.NewRouter()
	r.Get("/api/health", healthHandler)
	r.Get("/api/diagnostics/performance", api.performanceDiagnosticsHandler)
	r.Post("/api/diagnostics/performance", api.saveClientPerformanceHandler)
	if api.maintenance != nil {
		r.Get("/api/maintenance/events", api.eventMaintenanceStatusHandler)
	}

	if api.store != nil && api.events != nil && api.agents != nil && api.runs != nil {
		r.Get("/api/capabilities", api.capabilitiesHandler)
		r.Get("/api/agents", api.agentsHandler)
		r.Get("/api/agents/{agentType}/options", api.agentOptionsHandler)
		r.Post("/api/runs", api.createRunHandler)
		r.Get("/api/runs/{runId}", api.getRunHandler)
		r.Get("/api/runs/{runId}/report", api.getRunReportHandler)
		r.Get("/api/runs/{runId}/events", api.getRunEventsHandler)
		r.Get("/api/runs/{runId}/events/stream", api.runEventStreamHandler)
		r.Get("/api/runs/{runId}/requests", api.getRunRequestsHandler)
		r.Post("/api/runs/{runId}/cancel", api.cancelRunHandler)
		r.Post("/api/runs/{runId}/requests/{requestId}/answer", api.answerRunRequestHandler)
		r.Post("/api/runs/{runId}/permissions/{requestId}/resolve", api.resolveRunPermissionHandler)
		r.Get("/api/workspaces/roots", api.workspaceRootsHandler)
		r.Get("/api/workspaces/browse", api.workspaceBrowseHandler)
		r.Post("/api/sessions", api.createSessionHandler)
		r.Patch("/api/sessions/{sessionId}", api.updateSessionHandler)
		r.Put("/api/sessions/{sessionId}/agent-options/runtime", api.updateSessionRuntimeAgentOptionsHandler)
		r.Post("/api/sessions/{sessionId}/archive", api.archiveSessionHandler)
		r.Post("/api/sessions/{sessionId}/restore", api.restoreSessionHandler)
		r.Get("/api/sessions/{sessionId}/files", api.sessionFilesHandler)
		r.Post("/api/sessions/{sessionId}/files/upload", api.uploadSessionFilesHandler)
		r.Get("/api/sessions/{sessionId}/files/content", api.sessionFileContentHandler)
		r.Put("/api/sessions/{sessionId}/files/content", api.updateSessionFileContentHandler)
		r.Get("/api/sessions/{sessionId}/submissions/{submissionId}", api.messageSubmissionStatusHandler)
		r.Get("/api/sessions/{sessionId}/files/raw", api.sessionFileRawHandler)
		r.Get("/api/sessions/{sessionId}/files/search", api.sessionFileSearchHandler)
		r.Get("/api/sessions/{sessionId}/events/{seq}/file-citation", api.eventFileCitationHandler)
		r.Get("/api/sessions/{sessionId}/skills", api.sessionSkillsHandler)
		r.Get("/api/sessions/{sessionId}/repository-skills", api.listRepositorySkillsHandler)
		r.Post("/api/sessions/{sessionId}/repository-skills", api.createRepositorySkillHandler)
		r.Post("/api/sessions/{sessionId}/repository-skills/repair-claude-bridges", api.repairRepositorySkillClaudeBridgesHandler)
		r.Get("/api/sessions/{sessionId}/repository-skills/{name}", api.getRepositorySkillHandler)
		r.Patch("/api/sessions/{sessionId}/repository-skills/{name}", api.updateRepositorySkillHandler)
		r.Delete("/api/sessions/{sessionId}/repository-skills/{name}", api.deleteRepositorySkillHandler)
		r.Post("/api/sessions/{sessionId}/repository-skills/{name}/claude-bridge", api.repairRepositorySkillClaudeBridgeHandler)
		r.Post("/api/sessions/{sessionId}/messages", api.submitMessageHandler)
		r.Get("/api/sessions/{sessionId}/queued-messages", api.listQueuedMessagesHandler)
		r.Delete("/api/sessions/{sessionId}/queued-messages/{queuedMessageId}", api.removeQueuedMessageHandler)
		r.Post("/api/sessions/{sessionId}/clear", api.clearSessionHandler)
		r.Post("/api/sessions/{sessionId}/compact", api.compactSessionHandler)
		r.Post("/api/sessions/{sessionId}/cancel", api.cancelSessionHandler)
		r.Post("/api/sessions/{sessionId}/requests/{requestId}/answer", api.answerUserInputHandler)
		r.Post("/api/sessions/{sessionId}/permissions/{requestId}/resolve", api.resolvePermissionHandler)
		if api.schedules != nil {
			r.Get("/api/sessions/{sessionId}/schedules", api.listSchedulesHandler)
			r.Post("/api/sessions/{sessionId}/schedules", api.createScheduleHandler)
			r.Patch("/api/sessions/{sessionId}/schedules/{scheduleId}", api.updateScheduleHandler)
			r.Delete("/api/sessions/{sessionId}/schedules/{scheduleId}", api.deleteScheduleHandler)
			r.Post("/api/sessions/{sessionId}/schedules/{scheduleId}/run-now", api.runScheduleNowHandler)
			r.Get("/api/sessions/{sessionId}/schedules/{scheduleId}/occurrences", api.listScheduleOccurrencesHandler)
			r.Delete("/api/sessions/{sessionId}/schedules/{scheduleId}/occurrences/{occurrenceId}", api.cancelScheduleOccurrenceHandler)
		}
		r.Get("/api/sessions/{sessionId}/console", api.consoleStatusHandler)
		r.Post("/api/sessions/{sessionId}/console", api.startConsoleHandler)
		r.Delete("/api/sessions/{sessionId}/console", api.killConsoleHandler)
		r.Get("/api/sessions/{sessionId}/console/ws", api.consoleWebSocketHandler)
	}
	if api.userHome != "" {
		r.Get("/api/user-skills", api.listUserSkillsHandler)
		r.Post("/api/user-skills", api.createUserSkillHandler)
		r.Post("/api/user-skills/repair-claude-bridges", api.repairUserSkillClaudeBridgesHandler)
		r.Get("/api/user-skills/{name}", api.getUserSkillHandler)
		r.Patch("/api/user-skills/{name}", api.updateUserSkillHandler)
		r.Delete("/api/user-skills/{name}", api.deleteUserSkillHandler)
		r.Post("/api/user-skills/{name}/claude-bridge", api.repairUserSkillClaudeBridgeHandler)
	}
	if api.store != nil {
		r.Get("/api/sessions", api.listSessionsHandler)
		r.Post("/api/sessions/notification-attention/clear", api.clearAllSessionNotificationAttentionHandler)
		r.Get("/api/sessions/{sessionId}", api.getSessionHandler)
		r.Get("/api/sessions/{sessionId}/children", api.listSessionChildrenHandler)
		r.Post("/api/sessions/{sessionId}/notification-attention/clear", api.clearSessionNotificationAttentionHandler)
		r.Get("/api/sessions/{sessionId}/events", api.eventHistoryHandler)
		r.Get("/api/sessions/{sessionId}/events/{seq}/attachments/{attachmentIndex}", api.eventAttachmentHandler)
		r.Get("/api/sessions/{sessionId}/events/{seq}/tool-content/{contentIndex}", api.eventToolContentHandler)
		r.Get("/api/sessions/{sessionId}/events/{seq}/tool-output", api.eventToolOutputHandler)
	}
	if api.search != nil {
		r.Get("/api/search", api.searchHandler)
		r.Get("/api/search/stream", api.searchStreamHandler)
	}
	if api.dashboard != nil {
		r.Get("/api/dashboard", api.dashboardHandler)
		r.Get("/api/dashboard/runs", api.dashboardRunsHandler)
	}
	if api.store != nil && api.hosting != nil {
		r.Get("/api/sessions/{sessionId}/host", api.hostStatusHandler)
		r.Post("/api/sessions/{sessionId}/host/validate", api.validateHostHandler)
		r.Post("/api/sessions/{sessionId}/host/start", api.startHostHandler)
		r.Post("/api/sessions/{sessionId}/host/stop", api.stopHostHandler)
		r.Post("/api/sessions/{sessionId}/host/restart", api.restartHostHandler)
		r.Post("/api/sessions/{sessionId}/host/check", api.checkHostHandler)
		r.Get("/api/sessions/{sessionId}/host/logs", api.hostLogsHandler)
		r.Get("/api/sessions/{sessionId}/host/logs/stream", api.hostLogStreamHandler)
	}
	if api.store != nil && api.events != nil {
		r.Get("/api/sessions/{sessionId}/events/stream", api.eventStreamHandler)
		r.Get("/api/sessions/activity/stream", api.sessionActivityStreamHandler)
		r.Put("/api/sessions/activity/watch", api.watchSessionActivityHandler)
	}
	if api.notifications != nil {
		r.Get("/api/notifications/public-key", api.notificationPublicKeyHandler)
		r.Post("/api/notifications/subscriptions", api.saveNotificationSubscriptionHandler)
		r.Delete("/api/notifications/subscriptions", api.deleteNotificationSubscriptionHandler)
		r.Post("/api/notifications/acknowledge", api.acknowledgeNotificationHandler)
		r.Post("/api/notifications/seen", api.seenNotificationHandler)
		r.Post("/api/notifications/test", api.testNotificationHandler)
	}
	r.NotFound(api.notFoundHandler)
	if api.schedules != nil {
		api.schedules.SetDispatch(func(sessionID string) {
			go api.startQueuedMessageRun(context.Background(), sessionID)
		})
	}

	if api.hosting != nil {
		return hostedPreviewDispatch{app: r, hosting: api.hosting}
	}
	return r
}

type hostedPreviewDispatch struct {
	app     http.Handler
	hosting HostingManager
}

func (h hostedPreviewDispatch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == hosting.IngressHealthPath {
		h.hosting.ServeHTTP(w, r)
		return
	}
	if _, ok := h.hosting.LookupHost(r.Host); ok {
		h.hosting.ServeHTTP(w, r)
		return
	}
	h.app.ServeHTTP(w, r)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (api API) notFoundHandler(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if api.staticAssets == nil {
		http.NotFound(w, r)
		return
	}
	serveStaticAsset(api.staticAssets, w, r)
}

func serveStaticAsset(assets fs.FS, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	if staticAssetExists(assets, name) {
		serveStaticFile(assets, name, w, r)
		return
	}
	if isFrontendAssetPath(name) {
		http.NotFound(w, r)
		return
	}
	serveStaticFile(assets, "index.html", w, r)
}

func isFrontendAssetPath(name string) bool {
	return strings.HasPrefix(name, "assets/") ||
		name == "favicon.svg" ||
		name == "favicon-notify.svg" ||
		name == "icon.svg" ||
		name == "manifest.webmanifest" ||
		name == "service-worker.js"
}

func staticAssetExists(assets fs.FS, name string) bool {
	info, err := fs.Stat(assets, name)
	return err == nil && !info.IsDir()
}

func serveStaticFile(assets fs.FS, name string, w http.ResponseWriter, r *http.Request) {
	info, err := fs.Stat(assets, name)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	servedName := name
	precompressedName := name + ".gz"
	precompressed := staticAssetExists(assets, precompressedName)
	if precompressed {
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Header.Get("Range") == "" && acceptsEncoding(r.Header.Get("Accept-Encoding"), "gzip") {
			servedName = precompressedName
			w.Header().Set("Content-Encoding", "gzip")
			if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
		}
	}
	content, err := fs.ReadFile(assets, servedName)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	setStaticCacheHeader(w.Header(), name)
	http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(content))
}

func setStaticCacheHeader(headers http.Header, name string) {
	switch {
	case strings.HasPrefix(name, "assets/"):
		headers.Set("Cache-Control", immutableAssetCache)
	case name == "favicon.svg" || name == "favicon-notify.svg" || name == "icon.svg" || name == "manifest.webmanifest":
		headers.Set("Cache-Control", staticShellCache)
	default:
		headers.Set("Cache-Control", revalidatingCache)
	}
}

func (api API) eventHistoryHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	if !api.sessionExists(w, r, sessionID) {
		return
	}

	turns, ok := parseEventTurnLimit(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	maxBytes, ok := parseEventHistoryByteLimit(w, r)
	if !ok {
		return
	}
	filter, ok := parseEventListFilter(w, r)
	if !ok {
		return
	}

	result, err := api.listHistoryEvents(r, sessionID, limit, turns, maxBytes, filter)
	if errors.Is(err, errInvalidEventHistoryCursor) {
		writeError(w, http.StatusBadRequest, eventHistoryCursorMessage(err))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list events")
		return
	}

	responses := boundedEventResponses(result.Events, result.PreferLatest, maxBytes)
	page, err := api.eventHistoryPage(r.Context(), sessionID, responses, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to inspect event page")
		return
	}
	inputs := make([]eventResponse, 0)
	if source, ok := api.store.(interface {
		ListPendingInputEvents(context.Context, string, int64) ([]store.Event, error)
	}); ok && api.runs != nil && api.runs.Active(sessionID) {
		pending, err := source.ListPendingInputEvents(r.Context(), sessionID, page.ServerLastSeq)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load pending questions")
			return
		}
		for _, event := range pending {
			inputs = append(inputs, newEventResponse(event))
		}
	}
	writeCompressedJSON(w, r, http.StatusOK, eventHistoryResponse{Events: responses, Page: page, InputEvents: inputs})
}

func (api API) eventAttachmentHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	if !api.sessionExists(w, r, sessionID) {
		return
	}

	seq, err := strconv.ParseInt(chi.URLParam(r, "seq"), 10, 64)
	if err != nil || seq <= 0 {
		writeError(w, http.StatusBadRequest, "invalid event sequence")
		return
	}
	attachmentIndex, err := strconv.Atoi(chi.URLParam(r, "attachmentIndex"))
	if err != nil || attachmentIndex < 0 {
		writeError(w, http.StatusBadRequest, "invalid attachment index")
		return
	}
	if blob, found, failed := api.loadEventBlob(w, r, sessionID, seq, "attachment", attachmentIndex); failed {
		return
	} else if found {
		serveEventBlob(w, blob, true)
		return
	}

	event, err := api.store.GetEvent(r.Context(), sessionID, seq)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load event")
		return
	}

	attachment, ok := eventImageAttachment(event, attachmentIndex)
	if !ok {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	data, err := decodeImageDataURL(attachment.DataURL, attachment.MediaType)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attachment data")
		return
	}

	w.Header().Set("Content-Type", attachment.MediaType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", sanitizeAttachmentFilename(attachment.Name)))
	_, _ = w.Write(data)
}

func (api API) eventToolContentHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	if !api.sessionExists(w, r, sessionID) {
		return
	}

	seq, err := strconv.ParseInt(chi.URLParam(r, "seq"), 10, 64)
	if err != nil || seq <= 0 {
		writeError(w, http.StatusBadRequest, "invalid event sequence")
		return
	}
	contentIndex, err := strconv.Atoi(chi.URLParam(r, "contentIndex"))
	if err != nil || contentIndex < 0 {
		writeError(w, http.StatusBadRequest, "invalid tool content index")
		return
	}
	if blob, found, failed := api.loadEventBlob(w, r, sessionID, seq, "tool-content", contentIndex); failed {
		return
	} else if found {
		serveEventBlob(w, blob, true)
		return
	}

	event, err := api.store.GetEvent(r.Context(), sessionID, seq)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load event")
		return
	}

	content, ok := eventToolContent(event, contentIndex)
	if !ok {
		writeError(w, http.StatusNotFound, "tool content not found")
		return
	}
	data, err := decodeToolContentData(content.Data, content.MediaType)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tool content data")
		return
	}

	w.Header().Set("Content-Type", content.MediaType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", sanitizeAttachmentFilename(content.Name)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (api API) eventToolOutputHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	if !api.sessionExists(w, r, sessionID) {
		return
	}
	seq, err := strconv.ParseInt(chi.URLParam(r, "seq"), 10, 64)
	if err != nil || seq <= 0 {
		writeError(w, http.StatusBadRequest, "invalid event sequence")
		return
	}
	if blob, found, failed := api.loadEventBlob(w, r, sessionID, seq, "tool-output", 0); failed {
		return
	} else if found {
		serveEventBlob(w, blob, false)
		return
	}

	event, err := api.store.GetEvent(r.Context(), sessionID, seq)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load event")
		return
	}
	output := legacyEventToolOutput(event)
	if output == "" {
		writeError(w, http.StatusNotFound, "tool output not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, output)
}

func (api API) loadEventBlob(
	w http.ResponseWriter,
	r *http.Request,
	sessionID string,
	seq int64,
	kind string,
	itemIndex int,
) (store.EventBlob, bool, bool) {
	blobs, ok := api.store.(EventBlobStore)
	if !ok {
		return store.EventBlob{}, false, false
	}
	blob, err := blobs.GetEventBlob(r.Context(), sessionID, seq, kind, itemIndex)
	if errors.Is(err, store.ErrNotFound) {
		return store.EventBlob{}, false, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load event content")
		return store.EventBlob{}, false, true
	}
	return blob, true, false
}

func serveEventBlob(w http.ResponseWriter, blob store.EventBlob, contentDisposition bool) {
	mediaType := strings.TrimSpace(blob.MediaType)
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if contentDisposition {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", sanitizeAttachmentFilename(blob.Name)))
	}
	_, _ = w.Write(blob.Data)
}

var errInvalidEventHistoryCursor = errors.New("invalid event history cursor")

func eventHistoryCursorMessage(err error) string {
	message := err.Error()
	return strings.TrimPrefix(message, errInvalidEventHistoryCursor.Error()+": ")
}

func (api API) listHistoryEvents(
	r *http.Request,
	sessionID string,
	limit int,
	turns int,
	maxBytes int,
	filter store.EventListFilter,
) (eventHistoryResult, error) {
	query := r.URL.Query()
	rawAfterSeq := query.Get("after_seq")
	rawBeforeSeq := query.Get("before_seq")
	rawAroundSeq := query.Get("around_seq")
	rawTail := query.Get("tail")

	tail := false
	if rawTail != "" {
		parsedTail, err := strconv.ParseBool(rawTail)
		if err != nil {
			return eventHistoryResult{}, fmt.Errorf("%w: tail must be a boolean", errInvalidEventHistoryCursor)
		}
		tail = parsedTail
	}

	cursorCount := 0
	if rawAfterSeq != "" {
		cursorCount++
	}
	if rawBeforeSeq != "" {
		cursorCount++
	}
	if rawAroundSeq != "" {
		cursorCount++
	}
	if tail {
		cursorCount++
	}
	if cursorCount > 1 {
		return eventHistoryResult{}, fmt.Errorf("%w: use only one event history cursor", errInvalidEventHistoryCursor)
	}
	if turns > 0 && !tail && rawBeforeSeq == "" && rawAfterSeq == "" && rawAroundSeq == "" {
		return eventHistoryResult{}, fmt.Errorf("%w: turns requires tail=true, before_seq, after_seq, or around_seq", errInvalidEventHistoryCursor)
	}

	if rawAroundSeq != "" {
		aroundSeq, err := parseNonNegativeInt64(rawAroundSeq, "around_seq")
		if err != nil || aroundSeq <= 0 {
			if err == nil {
				err = errors.New("around_seq must be greater than zero")
			}
			return eventHistoryResult{}, fmt.Errorf("%w: %s", errInvalidEventHistoryCursor, err.Error())
		}
		windowTurns := turns
		if windowTurns <= 0 {
			windowTurns = 2
		}
		older, err := api.store.ListEventTurnsBeforePageFiltered(r.Context(), sessionID, aroundSeq+1, windowTurns, limit, filter)
		if err != nil {
			return eventHistoryResult{}, err
		}
		newer, err := api.store.ListEventTurnsAfterFiltered(r.Context(), sessionID, aroundSeq, windowTurns, limit, filter)
		if err != nil {
			return eventHistoryResult{}, err
		}
		bySeq := make(map[int64]store.Event, len(older)+len(newer))
		for _, event := range append(older, newer...) {
			bySeq[event.Seq] = event
		}
		events := make([]store.Event, 0, len(bySeq))
		for _, event := range bySeq {
			events = append(events, event)
		}
		sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
		return eventHistoryResult{Events: events}, nil
	}

	if tail {
		var events []store.Event
		var err error
		if turns > 0 {
			if r.URL.Query().Has("limit") {
				events, err = api.store.ListRecentEventTurnsPageFiltered(r.Context(), sessionID, turns, limit, filter)
			} else {
				events, err = adaptiveLatestTurnEvents(turns, maxBytes, func(pageLimit int) ([]store.Event, error) {
					return api.store.ListRecentEventTurnsPageFiltered(r.Context(), sessionID, turns, pageLimit, filter)
				})
			}
		} else {
			events, err = api.listBoundarySafeRecentEvents(r.Context(), sessionID, limit, filter)
		}
		return eventHistoryResult{Events: events, PreferLatest: true}, err
	}
	if rawBeforeSeq != "" {
		beforeSeq, err := parseNonNegativeInt64(rawBeforeSeq, "before_seq")
		if err != nil {
			return eventHistoryResult{}, fmt.Errorf("%w: %s", errInvalidEventHistoryCursor, err.Error())
		}
		var events []store.Event
		if turns > 0 {
			if r.URL.Query().Has("limit") {
				events, err = api.store.ListEventTurnsBeforePageFiltered(r.Context(), sessionID, beforeSeq, turns, limit, filter)
			} else {
				events, err = adaptiveLatestTurnEvents(turns, maxBytes, func(pageLimit int) ([]store.Event, error) {
					return api.store.ListEventTurnsBeforePageFiltered(
						r.Context(), sessionID, beforeSeq, turns, pageLimit, filter,
					)
				})
			}
		} else {
			events, err = api.listBoundarySafeEventsBefore(r.Context(), sessionID, beforeSeq, limit, filter)
		}
		return eventHistoryResult{Events: events, PreferLatest: true}, err
	}

	afterSeq := int64(0)
	if rawAfterSeq != "" {
		parsedAfterSeq, err := parseNonNegativeInt64(rawAfterSeq, "after_seq")
		if err != nil {
			return eventHistoryResult{}, fmt.Errorf("%w: %s", errInvalidEventHistoryCursor, err.Error())
		}
		afterSeq = parsedAfterSeq
	}
	var events []store.Event
	var err error
	if turns > 0 {
		events, err = api.store.ListEventTurnsAfterFiltered(r.Context(), sessionID, afterSeq, turns, limit, filter)
	} else {
		events, err = api.store.ListEventsFiltered(r.Context(), sessionID, afterSeq, limit, filter)
	}
	return eventHistoryResult{Events: events}, err
}

func adaptiveLatestTurnEvents(
	turns int,
	maxBytes int,
	list func(limit int) ([]store.Event, error),
) ([]store.Event, error) {
	limit := defaultEventLimit
	for {
		events, err := list(limit)
		if err != nil {
			return nil, err
		}
		if len(events) < limit || visibleTurnCount(events) >= turns || limit >= maxEventTurnPageLimit {
			return events, nil
		}
		bounded := boundedLatestTurnResponses(eventResponses(events), maxBytes)
		if encodedEventResponsesBytes(bounded)+2 >= maxBytes*9/10 {
			return events, nil
		}
		limit = min(limit*2, maxEventTurnPageLimit)
	}
}

func visibleTurnCount(events []store.Event) int {
	count := 0
	for _, event := range events {
		if event.Type == "user.message.completed" {
			count++
		}
	}
	return count
}

func (api API) listBoundarySafeRecentEvents(
	ctx context.Context,
	sessionID string,
	limit int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	events, err := api.store.ListRecentEventsFiltered(ctx, sessionID, limit, filter)
	if err != nil {
		return nil, err
	}
	return api.expandHistoryWindowToSafeBoundary(ctx, sessionID, events, filter)
}

func (api API) listBoundarySafeEventsBefore(
	ctx context.Context,
	sessionID string,
	beforeSeq int64,
	limit int,
	filter store.EventListFilter,
) ([]store.Event, error) {
	events, err := api.store.ListEventsBeforeFiltered(ctx, sessionID, beforeSeq, limit, filter)
	if err != nil {
		return nil, err
	}
	return api.expandHistoryWindowToSafeBoundary(ctx, sessionID, events, filter)
}

func (api API) expandHistoryWindowToSafeBoundary(
	ctx context.Context,
	sessionID string,
	events []store.Event,
	filter store.EventListFilter,
) ([]store.Event, error) {
	for len(events) > 0 && !safeHistoryWindowStart(events[0]) && len(events) < maxEventLimit {
		beforeSeq := events[0].Seq
		if beforeSeq <= 1 {
			return events, nil
		}

		backfillLimit := eventHistoryBackfillStep
		if remaining := maxEventLimit - len(events); backfillLimit > remaining {
			backfillLimit = remaining
		}
		if backfillLimit <= 0 {
			return events, nil
		}

		olderEvents, err := api.store.ListEventsBeforeFiltered(ctx, sessionID, beforeSeq, backfillLimit, filter)
		if err != nil {
			return nil, err
		}
		if len(olderEvents) == 0 {
			return events, nil
		}

		expanded := make([]store.Event, 0, len(olderEvents)+len(events))
		expanded = append(expanded, olderEvents...)
		expanded = append(expanded, events...)
		events = expanded
	}

	return events, nil
}

func safeHistoryWindowStart(event store.Event) bool {
	switch event.Type {
	case "agent.message.delta",
		"agent.plan.delta",
		"agent.thinking.delta",
		"agent.log.delta",
		"tool.call.delta",
		"file.change.delta",
		"tool.call.completed",
		"file.change.completed",
		"agent.thinking.completed":
		return false
	default:
		return true
	}
}

func (api API) eventStreamHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	if !api.sessionExists(w, r, sessionID) {
		return
	}

	afterSeq, ok := parseAfterSeq(w, r)
	if !ok {
		return
	}
	filter, ok := parseEventListFilter(w, r)
	if !ok {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	liveEvents, unsubscribe := api.events.Subscribe(sessionID)
	defer unsubscribe()

	replayedEvents, resyncRequired, err := api.replayEvents(r.Context(), sessionID, afterSeq, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to replay events")
		return
	}

	headers := w.Header()
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := writeSSEComment(w, "connected"); err != nil {
		return
	}
	flusher.Flush()
	if resyncRequired {
		if err := writeSSEControl(w, streamResyncEventType, map[string]any{
			"reason":    "replay_window_exceeded",
			"after_seq": afterSeq,
		}); err == nil {
			flusher.Flush()
		}
		return
	}

	highestSeqSent := afterSeq
	for _, event := range replayedEvents {
		if err := writeSSE(w, event); err != nil {
			return
		}
		flusher.Flush()
		if event.Seq > highestSeqSent {
			highestSeqSent = event.Seq
		}
	}

	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := writeSSEComment(w, "heartbeat"); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-liveEvents:
			if !ok {
				return
			}
			if event.Seq <= highestSeqSent {
				continue
			}
			if !eventVisible(event, filter) {
				continue
			}
			if err := writeSSE(w, event); err != nil {
				return
			}
			flusher.Flush()
			highestSeqSent = event.Seq
		}
	}
}

func (api API) sessionActivityStreamHandler(w http.ResponseWriter, r *http.Request) {
	resumeRequested := r.URL.Query().Has("after_cursor") || strings.TrimSpace(r.Header.Get("Last-Event-ID")) != ""
	afterCursor, ok := parseAfterCursor(w, r)
	if !ok {
		return
	}
	filter, ok := parseEventListFilter(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	excludedSessionID := strings.TrimSpace(r.URL.Query().Get("exclude_session_id"))
	clientID := strings.TrimSpace(r.URL.Query().Get("client_id"))
	watchedSessionID := strings.TrimSpace(r.URL.Query().Get("watch_session_id"))
	allTransient := strings.TrimSpace(r.URL.Query().Get("live_scope")) == "all"
	if clientID != "" && !validActivityClientID(clientID) {
		writeError(w, http.StatusBadRequest, "client_id is invalid")
		return
	}
	activityEvents, multiplexed := api.events.(SessionActivityEventService)
	var liveEvents <-chan store.Event
	var unsubscribe func()
	if multiplexed && clientID != "" {
		liveEvents, unsubscribe = activityEvents.SubscribeSessionActivity(clientID, watchedSessionID, filter.IncludeDebug, allTransient)
	} else {
		liveEvents, unsubscribe = api.events.SubscribeAll()
	}
	defer unsubscribe()
	api.performance.streamConnected(
		clientID,
		clientID == "" && (afterCursor > 0 || strings.TrimSpace(r.Header.Get("Last-Event-ID")) != ""),
	)
	defer api.performance.streamDisconnected()

	var replayedEvents []store.Event
	resyncRequired := false
	globalStore, hasGlobalStore := api.store.(GlobalEventStore)
	if hasGlobalStore && resumeRequested {
		var err error
		replayedEvents, resyncRequired, err = api.replayGlobalEvents(r.Context(), globalStore, afterCursor)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to replay global events")
			return
		}
	}

	headers := w.Header()
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := writeSSEComment(w, "connected"); err != nil {
		return
	}
	flusher.Flush()
	if resyncRequired {
		api.performance.streamResync()
		cursor, err := globalStore.GlobalEventCursor(r.Context())
		if err != nil {
			return
		}
		if err := writeSSEControl(w, streamResyncEventType, map[string]any{
			"reason":       "replay_window_exceeded",
			"after_cursor": afterCursor,
			"cursor":       cursor,
		}); err == nil {
			flusher.Flush()
		}
		return
	}

	highestCursorSent := afterCursor
	for _, event := range replayedEvents {
		if excludedSessionID != "" && event.SessionID == excludedSessionID {
			continue
		}
		if !sessionActivityEventVisible(event, filter, activityEvents, clientID, multiplexed) {
			continue
		}
		eventBytes, err := writeGlobalSSE(w, event)
		if err != nil {
			return
		}
		api.performance.streamReplay(eventBytes)
		flusher.Flush()
		if event.GlobalSeq > highestCursorSent {
			highestCursorSent = event.GlobalSeq
		}
	}
	if multiplexed && clientID != "" && allTransient {
		snapshot := activityEvents.LiveSnapshot()
		responses := make([]eventResponse, 0, len(snapshot.Events))
		for _, event := range snapshot.Events {
			if eventVisible(event, store.EventListFilter{}) {
				responses = append(responses, newLiveEventResponse(event))
			}
		}
		if err := writeSSEControl(w, "session.live.snapshot", map[string]any{
			"events":     responses,
			"watermarks": snapshot.Watermarks,
		}); err != nil {
			return
		}
		flusher.Flush()
	}

	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := writeSSEComment(w, "heartbeat"); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-liveEvents:
			if !ok {
				return
			}
			if excludedSessionID != "" && event.SessionID == excludedSessionID {
				continue
			}
			if event.GlobalSeq > 0 && event.GlobalSeq <= highestCursorSent {
				continue
			}
			if !sessionActivityEventVisible(event, filter, activityEvents, clientID, multiplexed) {
				continue
			}
			eventBytes, err := writeGlobalSSE(w, event)
			if err != nil {
				return
			}
			api.performance.streamLive(eventBytes, event.Transient, time.Since(event.CreatedAt))
			flusher.Flush()
			if event.GlobalSeq > highestCursorSent {
				highestCursorSent = event.GlobalSeq
			}
		}
	}
}

type watchSessionActivityRequest struct {
	ClientID     string `json:"client_id"`
	SessionID    string `json:"session_id"`
	IncludeDebug bool   `json:"include_debug"`
}

func (api API) watchSessionActivityHandler(w http.ResponseWriter, r *http.Request) {
	activityEvents, ok := api.events.(SessionActivityEventService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "session activity multiplexing is unavailable")
		return
	}
	var request watchSessionActivityRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024))
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	request.ClientID = strings.TrimSpace(request.ClientID)
	request.SessionID = strings.TrimSpace(request.SessionID)
	if !validActivityClientID(request.ClientID) {
		writeError(w, http.StatusBadRequest, "client_id is invalid")
		return
	}
	connected := activityEvents.WatchSessionActivity(request.ClientID, request.SessionID, request.IncludeDebug)
	response := map[string]any{
		"connected":  connected,
		"session_id": request.SessionID,
	}
	if connected && request.SessionID != "" && activityEvents.SessionActivityReceivesAllTransient(request.ClientID) {
		snapshot := activityEvents.LiveSessionSnapshot(request.SessionID)
		events := make([]eventResponse, 0, len(snapshot.Events))
		for _, event := range snapshot.Events {
			events = append(events, newLiveEventResponse(event))
		}
		response["events"] = events
		response["watermark"] = snapshot.Watermarks[request.SessionID]
	}
	writeJSON(w, http.StatusOK, response)
}

func sessionActivityEventVisible(
	event store.Event,
	defaultFilter store.EventListFilter,
	activityEvents SessionActivityEventService,
	clientID string,
	multiplexed bool,
) bool {
	if !multiplexed || clientID == "" {
		return eventVisible(event, defaultFilter)
	}
	watchedSessionID, includeDebug, ok := activityEvents.SessionActivityWatch(clientID)
	if !ok {
		return false
	}
	if event.Transient && !activityEvents.SessionActivityReceivesAllTransient(clientID) && event.SessionID != watchedSessionID {
		return false
	}
	return eventVisible(event, store.EventListFilter{
		IncludeDebug: includeDebug && event.SessionID == watchedSessionID,
	})
}

func validActivityClientID(clientID string) bool {
	if len(clientID) < 1 || len(clientID) > 128 {
		return false
	}
	for _, character := range clientID {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func (api API) replayGlobalEvents(
	ctx context.Context,
	globalStore GlobalEventStore,
	afterCursor int64,
) ([]store.Event, bool, error) {
	events, err := globalStore.ListGlobalEventsFiltered(
		ctx,
		afterCursor,
		maxEventLimit+1,
		store.EventListFilter{},
	)
	if err != nil {
		return nil, false, err
	}
	if len(events) > maxEventLimit {
		return nil, true, nil
	}
	usedBytes := 0
	for _, event := range events {
		encoded, err := json.Marshal(newEventResponse(event))
		if err != nil {
			return nil, false, fmt.Errorf("marshal global replay event: %w", err)
		}
		usedBytes += len(encoded)
		if usedBytes > maxEventHistoryBytes {
			return nil, true, nil
		}
	}
	return events, false, nil
}

func (api API) replayEvents(
	ctx context.Context,
	sessionID string,
	afterSeq int64,
	filter store.EventListFilter,
) ([]store.Event, bool, error) {
	events, err := api.store.ListEventsFiltered(ctx, sessionID, afterSeq, maxEventLimit+1, filter)
	if err != nil {
		return nil, false, err
	}
	if len(events) > maxEventLimit {
		return nil, true, nil
	}
	if recent, ok := api.events.(RecentEventService); ok {
		bySeq := make(map[int64]store.Event, len(events))
		for _, event := range events {
			bySeq[event.Seq] = event
		}
		for _, event := range recent.Recent(sessionID) {
			if event.Seq <= afterSeq || !eventVisible(event, filter) {
				continue
			}
			bySeq[event.Seq] = event
		}
		events = events[:0]
		for _, event := range bySeq {
			events = append(events, event)
		}
		sort.Slice(events, func(left int, right int) bool {
			return events[left].Seq < events[right].Seq
		})
		if len(events) > maxEventLimit {
			return nil, true, nil
		}
	}
	usedBytes := 0
	for _, event := range events {
		encoded, err := json.Marshal(newEventResponse(event))
		if err != nil {
			return nil, false, fmt.Errorf("marshal replay event: %w", err)
		}
		usedBytes += len(encoded)
		if usedBytes > maxEventHistoryBytes {
			return nil, true, nil
		}
	}
	return events, false, nil
}

func (api API) sessionExists(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	if _, err := api.store.GetSession(r.Context(), sessionID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return false
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return false
	}

	return true
}

func parseAfterSeq(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.URL.Query().Get("after_seq")
	if raw == "" {
		return 0, true
	}

	afterSeq, err := parseNonNegativeInt64(raw, "after_seq")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return 0, false
	}

	return afterSeq, true
}

func parseAfterCursor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.URL.Query().Get("after_cursor")
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
		if raw == "" {
			return 0, true
		}
	}

	afterCursor, err := parseNonNegativeInt64(raw, "after_cursor")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return 0, false
	}
	return afterCursor, true
}

func parseNonNegativeInt64(raw string, name string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return value, nil
}

func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultEventLimit, true
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		writeError(w, http.StatusBadRequest, "limit must be a non-negative integer")
		return 0, false
	}
	if limit == 0 {
		return defaultEventLimit, true
	}
	if limit > maxEventLimit {
		return maxEventLimit, true
	}

	return limit, true
}

func parseEventTurnLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("turns")
	if raw == "" {
		return 0, true
	}

	turns, err := strconv.Atoi(raw)
	if err != nil || turns <= 0 {
		writeError(w, http.StatusBadRequest, "turns must be a positive integer")
		return 0, false
	}
	if turns > maxEventTurnLimit {
		return maxEventTurnLimit, true
	}
	return turns, true
}

func parseEventHistoryByteLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("max_bytes"))
	if raw == "" {
		return maxEventHistoryBytes, true
	}

	maxBytes, err := strconv.Atoi(raw)
	if err != nil || maxBytes <= 0 {
		writeError(w, http.StatusBadRequest, "max_bytes must be a positive integer")
		return 0, false
	}
	if maxBytes > maxEventHistoryBytes {
		return maxEventHistoryBytes, true
	}
	return maxBytes, true
}

func parseEventListFilter(w http.ResponseWriter, r *http.Request) (store.EventListFilter, bool) {
	raw := r.URL.Query().Get("include_debug")
	if raw == "" {
		return store.EventListFilter{}, true
	}

	includeDebug, err := strconv.ParseBool(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "include_debug must be a boolean")
		return store.EventListFilter{}, false
	}
	return store.EventListFilter{IncludeDebug: includeDebug}, true
}

func eventVisible(event store.Event, filter store.EventListFilter) bool {
	return filter.IncludeDebug || !debugOnlyEvent(event)
}

func debugOnlyEvent(event store.Event) bool {
	switch event.Type {
	case "agent.log.delta",
		"provider.codex.request",
		"provider.codex.parse_error",
		"provider.claude.parse_error",
		"provider.opencode.request",
		"provider.opencode.parse_error",
		"provider.pi.parse_error",
		"provider.pi.event":
		return true
	case "provider.codex.event":
		providerEventType := payloadStringAt(event.Payload, "provider_event_type")
		if providerEventType == "thread/tokenUsage/updated" || providerEventType == "item/plan/delta" {
			return false
		}
		return providerEventType != "item/completed" || payloadStringAt(event.Payload, "raw", "item", "type") != "plan"
	case "provider.claude.event":
		return !payloadHas(event.Payload, "usage")
	case "provider.opencode.event":
		return payloadStringAt(event.Payload, "provider_event_type") != "usage_update"
	default:
		return false
	}
}

func payloadStringAt(payload json.RawMessage, path ...string) string {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return ""
	}
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = object[key]
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func payloadHas(payload json.RawMessage, key string) bool {
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		return false
	}
	_, ok := object[key]
	return ok
}

func eventResponses(events []store.Event) []eventResponse {
	if len(events) == 0 {
		return []eventResponse{}
	}

	responses := make([]eventResponse, 0, len(events))
	for _, event := range events {
		responses = append(responses, newEventResponse(event))
	}

	return responses
}

func boundedEventResponses(events []store.Event, preferLatest bool, maxBytes int) []eventResponse {
	responses := eventResponses(events)
	if maxBytes <= 0 || len(responses) == 0 {
		return responses
	}

	used := 2
	if preferLatest {
		return boundedLatestTurnResponses(responses, maxBytes)
	}

	end := 0
	for index := range responses {
		encoded, err := json.Marshal(responses[index])
		if err != nil {
			continue
		}
		next := used + len(encoded)
		if end > 0 {
			next++
		}
		if next > maxBytes && end > 0 {
			break
		}
		if next > maxBytes {
			responses[index].Payload = json.RawMessage(`{"_gorchestra_window_truncated":true}`)
			return responses[index : index+1]
		}
		end = index + 1
		used = next
	}
	return responses[:end]
}

func boundedLatestTurnResponses(responses []eventResponse, maxBytes int) []eventResponse {
	turnStarts := make([]int, 0)
	for index := range responses {
		if responses[index].Type == "user.message.completed" {
			turnStarts = append(turnStarts, index)
		}
	}
	if len(turnStarts) == 0 {
		return boundedLatestEventResponses(responses, maxBytes)
	}

	start := len(responses)
	used := 2
	selectedTurns := 0
	for turnIndex := len(turnStarts) - 1; turnIndex >= 0; turnIndex-- {
		turnStart := turnStarts[turnIndex]
		turnEnd := len(responses)
		if turnIndex+1 < len(turnStarts) {
			turnEnd = turnStarts[turnIndex+1]
		}
		turnBytes := encodedEventResponsesBytes(responses[turnStart:turnEnd])
		next := used + turnBytes
		if selectedTurns > 0 {
			next++
		}
		if next > maxBytes && selectedTurns > 0 {
			break
		}
		start = turnStart
		used = next
		selectedTurns++
	}
	if start == turnStarts[0] && start > 0 && responses[0].Seq == 1 {
		preambleBytes := encodedEventResponsesBytes(responses[:start])
		next := used + preambleBytes
		if used > 2 && preambleBytes > 0 {
			next++
		}
		if next <= maxBytes {
			start = 0
		}
	}

	selected := append([]eventResponse(nil), responses[start:]...)
	if encodedEventResponsesBytes(selected)+2 <= maxBytes {
		return selected
	}
	return compactEventResponsesToBytes(selected, maxBytes)
}

func boundedLatestEventResponses(responses []eventResponse, maxBytes int) []eventResponse {
	used := 2
	start := len(responses)
	for index := len(responses) - 1; index >= 0; index-- {
		eventBytes := encodedEventResponseBytes(responses[index])
		next := used + eventBytes
		if start < len(responses) {
			next++
		}
		if next > maxBytes && start < len(responses) {
			break
		}
		start = index
		used = next
	}
	selected := append([]eventResponse(nil), responses[start:]...)
	if encodedEventResponsesBytes(selected)+2 <= maxBytes {
		return selected
	}
	return compactEventResponsesToBytes(selected, maxBytes)
}

func compactEventResponsesToBytes(responses []eventResponse, maxBytes int) []eventResponse {
	compacted := append([]eventResponse(nil), responses...)
	marker := json.RawMessage(`{"_gorchestra_window_truncated":true}`)
	for encodedEventResponsesBytes(compacted)+2 > maxBytes {
		largestIndex := -1
		largestPayload := len(marker)
		for index := range compacted {
			if len(compacted[index].Payload) > largestPayload {
				largestIndex = index
				largestPayload = len(compacted[index].Payload)
			}
		}
		if largestIndex < 0 {
			break
		}
		compacted[largestIndex].Payload = marker
	}
	return compacted
}

func encodedEventResponsesBytes(responses []eventResponse) int {
	used := 0
	for index := range responses {
		used += encodedEventResponseBytes(responses[index])
		if index > 0 {
			used++
		}
	}
	return used
}

func encodedEventResponseBytes(response eventResponse) int {
	encoded, err := json.Marshal(response)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func (api API) eventHistoryPage(
	ctx context.Context,
	sessionID string,
	events []eventResponse,
	_ store.EventListFilter,
) (eventHistoryPage, error) {
	session, err := api.store.GetSession(ctx, sessionID)
	if err != nil {
		return eventHistoryPage{}, err
	}
	if len(events) == 0 {
		return eventHistoryPage{ServerLastSeq: session.LastEventSeq}, nil
	}
	first := events[0]
	last := events[len(events)-1]
	hasOlder := first.Seq > 1
	hasNewer := session.LastEventSeq > last.Seq
	return eventHistoryPage{
		FirstSeq:      first.Seq,
		LastSeq:       last.Seq,
		ServerLastSeq: session.LastEventSeq,
		HasOlder:      hasOlder,
		HasNewer:      hasNewer,
		StartsMidTurn: hasOlder && first.Type != "user.message.completed",
		EndsMidTurn:   hasNewer,
	}, nil
}

func newEventResponse(event store.Event) eventResponse {
	return eventResponse{
		ID:        event.ID,
		SessionID: event.SessionID,
		Seq:       event.Seq,
		GlobalSeq: event.GlobalSeq,
		Type:      event.Type,
		Role:      event.Role,
		Status:    string(event.Status),
		Payload:   responseEventPayload(event.Type, event.Payload),
		CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339Nano),
		Transient: event.Transient,
	}
}

func newLiveEventResponse(event store.Event) eventResponse {
	response := newEventResponse(event)
	response.Payload = append(json.RawMessage(nil), event.Payload...)
	return response
}

func responseEventPayload(eventType string, payload json.RawMessage) json.RawMessage {
	budget := maxOtherEventPayloadBytes
	if strings.HasPrefix(eventType, "tool.call") || strings.HasPrefix(eventType, "file.change") {
		budget = maxToolEventPayloadBytes
	}
	if len(payload) <= budget && !bytes.Contains(payload, []byte(`"data_url"`)) && !bytes.Contains(payload, []byte(`"blob"`)) {
		return payload
	}

	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return payload
	}

	stringLimit := min(maxEventPayloadStringLen, budget/2)
	for stringLimit >= 128 {
		truncated, changed := truncatePayloadValue(decoded, stringLimit)
		encoded, err := json.Marshal(truncated)
		if err != nil {
			return payload
		}
		if len(encoded) <= budget {
			if changed {
				return encoded
			}
			return payload
		}
		decoded = truncated
		stringLimit /= 2
	}
	fallback, _ := json.Marshal(map[string]any{
		"_gorchestra_truncated":      true,
		"_gorchestra_original_bytes": len(payload),
	})
	return fallback
}

func truncatePayloadValue(value any, stringLimit int) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		next := make(map[string]any, len(typed)+1)
		changed := false
		contentType, _ := typed["type"].(string)
		for key, child := range typed {
			if key == "data_url" || key == "blob" || (key == "data" && (contentType == "image" || contentType == "audio")) {
				if text, ok := child.(string); ok && text != "" {
					next[key] = ""
					changed = true
					continue
				}
			}
			truncatedChild, childChanged := truncatePayloadValue(child, stringLimit)
			next[key] = truncatedChild
			changed = changed || childChanged
		}
		if changed {
			next["_gorchestra_truncated"] = true
		}
		return next, changed
	case []any:
		next := make([]any, len(typed))
		changed := false
		for index, child := range typed {
			truncatedChild, childChanged := truncatePayloadValue(child, stringLimit)
			next[index] = truncatedChild
			changed = changed || childChanged
		}
		return next, changed
	case string:
		truncated, changed := truncatePayloadString(typed, stringLimit)
		return truncated, changed
	default:
		return value, false
	}
}

func truncatePayloadString(value string, stringLimit int) (string, bool) {
	if len(value) <= stringLimit {
		return value, false
	}

	suffix := fmt.Sprintf("\n\n[gorchestra truncated from %d bytes for browser display]", len(value))
	limit := stringLimit - len(suffix)
	if limit < 0 {
		limit = stringLimit
		suffix = ""
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit] + suffix, true
}

func eventImageAttachment(event store.Event, attachmentIndex int) (submitAttachment, bool) {
	if event.Type != "user.message.completed" || attachmentIndex < 0 {
		return submitAttachment{}, false
	}

	var payload struct {
		Attachments []submitAttachment `json:"attachments"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return submitAttachment{}, false
	}
	if attachmentIndex >= len(payload.Attachments) {
		return submitAttachment{}, false
	}

	attachment := payload.Attachments[attachmentIndex]
	if !strings.HasPrefix(attachment.MediaType, "image/") || strings.TrimSpace(attachment.DataURL) == "" {
		return submitAttachment{}, false
	}
	return attachment, true
}

type storedToolContent struct {
	Name      string
	MediaType string
	Data      string
}

func eventToolContent(event store.Event, contentIndex int) (storedToolContent, bool) {
	if event.Type != "tool.call.completed" || contentIndex < 0 {
		return storedToolContent{}, false
	}

	var payload struct {
		Result struct {
			Content []struct {
				Type     string `json:"type"`
				Data     string `json:"data"`
				MimeType string `json:"mimeType"`
				Resource *struct {
					URI      string `json:"uri"`
					Blob     string `json:"blob"`
					MimeType string `json:"mimeType"`
				} `json:"resource"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil || contentIndex >= len(payload.Result.Content) {
		return storedToolContent{}, false
	}

	block := payload.Result.Content[contentIndex]
	switch block.Type {
	case "image":
		if !strings.HasPrefix(block.MimeType, "image/") || strings.TrimSpace(block.Data) == "" {
			return storedToolContent{}, false
		}
		return storedToolContent{Name: "tool-image", MediaType: block.MimeType, Data: block.Data}, true
	case "audio":
		if !strings.HasPrefix(block.MimeType, "audio/") || strings.TrimSpace(block.Data) == "" {
			return storedToolContent{}, false
		}
		return storedToolContent{Name: "tool-audio", MediaType: block.MimeType, Data: block.Data}, true
	case "resource":
		if block.Resource == nil || strings.TrimSpace(block.Resource.Blob) == "" {
			return storedToolContent{}, false
		}
		mediaType := strings.TrimSpace(block.Resource.MimeType)
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		name := path.Base(strings.TrimSpace(strings.Split(block.Resource.URI, "?")[0]))
		if name == "." || name == "/" || name == "" {
			name = "tool-resource"
		}
		return storedToolContent{Name: name, MediaType: mediaType, Data: block.Resource.Blob}, true
	default:
		return storedToolContent{}, false
	}
}

func legacyEventToolOutput(event store.Event) string {
	if event.Type != "tool.call.completed" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"output", "aggregated_output", "text", "error"} {
		if output, ok := payload[key].(string); ok && output != "" {
			return output
		}
	}
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		return ""
	}
	if structured, ok := result["structuredContent"].(map[string]any); ok {
		if output, ok := structured["output"].(string); ok && output != "" {
			return output
		}
	}
	content, _ := result["content"].([]any)
	parts := make([]string, 0, len(content))
	for _, raw := range content {
		block, _ := raw.(map[string]any)
		if text, ok := block["text"].(string); ok && text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func decodeToolContentData(encoded string, mediaType string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if strings.HasPrefix(encoded, "data:") {
		header, payload, ok := strings.Cut(encoded, ",")
		if !ok || !strings.Contains(header, ";base64") {
			return nil, fmt.Errorf("invalid data url")
		}
		encodedMediaType := strings.TrimPrefix(strings.TrimSuffix(header, ";base64"), "data:")
		if encodedMediaType != mediaType {
			return nil, fmt.Errorf("media type mismatch")
		}
		encoded = payload
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func decodeImageDataURL(dataURL string, mediaType string) ([]byte, error) {
	header, payload, ok := strings.Cut(strings.TrimSpace(dataURL), ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.Contains(header, ";base64") {
		return nil, fmt.Errorf("invalid data url")
	}
	encodedMediaType := strings.TrimPrefix(strings.TrimSuffix(header, ";base64"), "data:")
	if encodedMediaType != mediaType || !strings.HasPrefix(encodedMediaType, "image/") {
		return nil, fmt.Errorf("media type mismatch")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func sanitizeAttachmentFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "image"
	}
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.ReplaceAll(name, "\r", " ")
	name = strings.ReplaceAll(name, `"`, "'")
	return name
}

func writeSSE(w http.ResponseWriter, event store.Event) error {
	body, err := json.Marshal(newEventResponse(event))
	if err != nil {
		return fmt.Errorf("marshal sse event: %w", err)
	}

	if _, err := fmt.Fprintf(w, "id: %d\n", event.Seq); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event.Type); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
		return err
	}

	return nil
}

func writeGlobalSSE(w http.ResponseWriter, event store.Event) (int, error) {
	body, err := json.Marshal(newEventResponse(event))
	if err != nil {
		return 0, fmt.Errorf("marshal global sse event: %w", err)
	}
	if !event.Transient {
		cursor := event.GlobalSeq
		if cursor <= 0 {
			// Compatibility for alternate EventService implementations that have not
			// adopted the durable global cursor yet.
			cursor = event.Seq
		}
		if _, err := fmt.Fprintf(w, "id: %d\n", cursor); err != nil {
			return 0, err
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event.Type); err != nil {
		return 0, err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", body)
	return len(body), err
}

func writeSSEControl(w http.ResponseWriter, eventType string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal sse control event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", body)
	return err
}

func writeSSEComment(w http.ResponseWriter, comment string) error {
	_, err := fmt.Fprintf(w, ": %s\n\n", comment)
	return err
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeCompressedJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Vary", "Accept-Encoding")
	if len(body) < 1024 || !acceptsEncoding(r.Header.Get("Accept-Encoding"), "gzip") {
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}

	w.Header().Set("Content-Encoding", "gzip")
	w.WriteHeader(status)
	writer := gzip.NewWriter(w)
	_, _ = writer.Write(body)
	_ = writer.Close()
}

func acceptsEncoding(header string, encoding string) bool {
	for _, value := range strings.Split(header, ",") {
		parts := strings.Split(value, ";")
		name := strings.TrimSpace(parts[0])
		if !strings.EqualFold(name, encoding) && name != "*" {
			continue
		}
		enabled := true
		for _, parameter := range parts[1:] {
			key, rawQuality, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "q") && strings.TrimSpace(rawQuality) == "0" {
				enabled = false
			}
		}
		if enabled {
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
