package store

import (
	"encoding/json"
	"time"
)

type SessionStatus string

const (
	SessionStatusIdle    SessionStatus = "idle"
	SessionStatusRunning SessionStatus = "running"
	SessionStatusFailed  SessionStatus = "failed"
)

type EventStatus string

const (
	EventStatusStarted   EventStatus = "started"
	EventStatusDelta     EventStatus = "delta"
	EventStatusCompleted EventStatus = "completed"
	EventStatusFailed    EventStatus = "failed"
	EventStatusCancelled EventStatus = "cancelled"
)

type QueuedMessageStatus string

const (
	QueuedMessageStatusPending QueuedMessageStatus = "pending"
	QueuedMessageStatusSending QueuedMessageStatus = "sending"
	QueuedMessageStatusSent    QueuedMessageStatus = "sent"
	QueuedMessageStatusRemoved QueuedMessageStatus = "removed"
)

type HostRuntimeStatus string

const (
	HostRuntimeStatusStopped  HostRuntimeStatus = "stopped"
	HostRuntimeStatusStarting HostRuntimeStatus = "starting"
	HostRuntimeStatusRunning  HostRuntimeStatus = "running"
	HostRuntimeStatusStopping HostRuntimeStatus = "stopping"
	HostRuntimeStatusFailed   HostRuntimeStatus = "failed"
)

type HostServiceStatus string

const (
	HostServiceStatusStopped  HostServiceStatus = "stopped"
	HostServiceStatusStarting HostServiceStatus = "starting"
	HostServiceStatusRunning  HostServiceStatus = "running"
	HostServiceStatusStopping HostServiceStatus = "stopping"
	HostServiceStatusFailed   HostServiceStatus = "failed"
)

type Session struct {
	ID                       string
	ParentSessionID          string
	SpawnedByRunID           string
	LineageDepth             int
	ChildCount               int
	Title                    string
	AgentType                string
	Status                   SessionStatus
	ProviderSessionID        string
	WorkspacePath            string
	AgentOptions             json.RawMessage
	EventCount               int64
	LastEventSeq             int64
	ToolCount                int64
	TokenCount               int64
	PendingInputCount        int64
	PendingPermissionCount   int64
	NotificationAttentionSeq int64
	CreatedAt                time.Time
	UpdatedAt                time.Time
	LastActivityAt           *time.Time
	CompletedAt              *time.Time
	ArchivedAt               *time.Time
	PinnedAt                 *time.Time
}

// Run is the durable, event-derived record of one agent invocation. Events
// remain canonical; this projection provides exact run lookup and reports.
type Run struct {
	ID                     string
	SessionID              string
	ParentSessionID        string
	SpawnedByRunID         string
	SessionTitle           string
	Kind                   string
	AgentType              string
	WorkspacePath          string
	Status                 string
	StartSeq               int64
	TerminalSeq            int64
	StartedAt              time.Time
	CompletedAt            *time.Time
	RequestedOptions       json.RawMessage
	ResolvedOptions        json.RawMessage
	FinalResponse          string
	FinalResponseSeq       int64
	Error                  string
	ToolCount              int64
	FileCount              int64
	InputRequestCount      int64
	PermissionRequestCount int64
	TokenCount             int64
	HasTokenUsage          bool
	CostAmount             float64
	CostCurrency           string
	HasCostUsage           bool
}

type RunSubmission struct {
	RequestID   string
	RequestHash string
	SessionID   string
	RunID       string
	Prompt      string
	State       string
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Event struct {
	ID        string
	SessionID string
	Seq       int64
	GlobalSeq int64
	Type      string
	Role      string
	Status    EventStatus
	Payload   json.RawMessage
	CreatedAt time.Time
	Transient bool
}

type EventBlob struct {
	EventID       string
	Kind          string
	ItemIndex     int
	Name          string
	MediaType     string
	Encoding      string
	OriginalBytes int64
	Data          []byte
	CreatedAt     time.Time
}

type EventMaintenanceStatus struct {
	Running             bool
	LastStartedAt       *time.Time
	LastCompletedAt     *time.Time
	LastError           string
	DeletedDeltaEvents  int64
	DeletedDebugEvents  int64
	ExtractedBlobEvents int64
	ReclaimedBytes      int64
	RetainedDebugAfter  *time.Time
}

type EventMaintenanceBatch struct {
	DeletedDeltaEvents  int64
	DeletedDebugEvents  int64
	ExtractedBlobEvents int64
	ReclaimedBytes      int64
	More                bool
}

type EventListFilter struct {
	IncludeDebug bool
}

type SearchResult struct {
	Kind          string
	SessionID     string
	SessionTitle  string
	WorkspacePath string
	EventSeq      int64
	Title         string
	Snippet       string
	CreatedAt     time.Time
	ArchivedAt    *time.Time
	Rank          float64
}

type QueuedMessage struct {
	ID           string
	SessionID    string
	Seq          int64
	Status       QueuedMessageStatus
	Content      string
	AgentOptions json.RawMessage
	Skills       json.RawMessage
	SourceKind   string
	SourceID     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type SessionSchedule struct {
	ID               string
	SessionID        string
	Name             string
	Prompt           string
	Cadence          json.RawMessage
	Timezone         string
	Enabled          bool
	NextRunAt        *time.Time
	DeletedAt        *time.Time
	PendingCount     int
	LastStatus       string
	LastScheduledFor *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type ScheduleOccurrence struct {
	ID             string
	ScheduleID     string
	SessionID      string
	QueueMessageID string
	Trigger        string
	ScheduledFor   time.Time
	Status         string
	RunID          string
	Error          string
	CreatedAt      time.Time
	StartedAt      *time.Time
	CompletedAt    *time.Time
}

// HostRuntime is the durable control-plane snapshot for one session preview.
// Process handles and logs remain in memory; PID is diagnostic only and is
// cleared during startup recovery.
type HostRuntime struct {
	SessionID      string
	RouteSlug      string
	WorkspacePath  string
	ConfigPath     string
	RecipeName     string
	RecipeHash     string
	RecipeSnapshot []byte
	Status         HostRuntimeStatus
	Services       []HostServiceSnapshot
	StartedAt      *time.Time
	StoppedAt      *time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type HostServiceSnapshot struct {
	Name      string            `json:"name"`
	Port      int               `json:"port,omitempty"`
	PID       int               `json:"pid,omitempty"`
	Status    HostServiceStatus `json:"status"`
	ExitCode  *int              `json:"exit_code,omitempty"`
	Error     string            `json:"error,omitempty"`
	StartedAt *time.Time        `json:"started_at,omitempty"`
	StoppedAt *time.Time        `json:"stopped_at,omitempty"`
}

type NotificationKeys struct {
	PublicKey  string
	PrivateKey string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type PushSubscription struct {
	Endpoint   string
	P256DH     string
	Auth       string
	UserAgent  string
	Origin     string
	LastError  string
	DisabledAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type PushDeliveryAttempt struct {
	ID             int64
	EndpointHash   string
	Origin         string
	PayloadKind    string
	SessionID      string
	EventType      string
	HTTPStatus     int
	ResponseStatus string
	Error          string
	CreatedAt      time.Time
}

type CreateSessionParams struct {
	Title           string
	AgentType       string
	WorkspacePath   string
	AgentOptions    json.RawMessage
	ParentSessionID string
	MaxLineageDepth int
}

type CreateRunSubmissionParams struct {
	RequestID         string
	RequestHash       string
	RunID             string
	Prompt            string
	Title             string
	AgentType         string
	WorkspacePath     string
	AgentOptions      json.RawMessage
	ParentSessionID   string
	SpawnedByRunID    string
	MaxLineageDepth   int
	MaxActiveChildren int
}

type UpdateSessionStatusParams struct {
	ID     string
	Status SessionStatus
}

type UpdateSessionTitleParams struct {
	ID    string
	Title string
}

type UpdateSessionParentParams struct {
	ID              string
	ParentSessionID string
	MaxLineageDepth int
}

type UpdateSessionWorkspaceParams struct {
	ID            string
	WorkspacePath string
}

type UpdateSessionAgentOptionsParams struct {
	ID           string
	AgentOptions json.RawMessage
}

type UpdateSessionPinParams struct {
	ID     string
	Pinned bool
}

type ArchiveSessionParams struct {
	ID string
}

type RestoreSessionParams struct {
	ID string
}

type SetSessionProviderSessionIDParams struct {
	ID                string
	ProviderSessionID string
	Replace           bool
}

type ClearSessionProviderSessionIDParams struct {
	ID string
}

type ListSessionsParams struct {
	Limit           int
	Status          SessionStatus
	IncludeArchived bool
}

type AppendEventParams struct {
	SessionID string
	Seq       int64
	Type      string
	Role      string
	Status    EventStatus
	Payload   json.RawMessage
}

type EnqueueMessageParams struct {
	ClientSubmissionID string
	SessionID          string
	Content            string
	AgentOptions       json.RawMessage
	Skills             json.RawMessage
	MaxPending         int
}

type SaveHostRuntimeParams struct {
	SessionID      string
	RouteSlug      string
	WorkspacePath  string
	ConfigPath     string
	RecipeName     string
	RecipeHash     string
	RecipeSnapshot []byte
	Status         HostRuntimeStatus
	Services       []HostServiceSnapshot
	StartedAt      *time.Time
	StoppedAt      *time.Time
	LastError      string
}

type QueueMessageIDParams struct {
	SessionID string
	ID        string
}

type CreateScheduleParams struct {
	SessionID string
	Name      string
	Prompt    string
	Cadence   json.RawMessage
	Timezone  string
	Enabled   bool
	NextRunAt *time.Time
}

type UpdateScheduleParams struct {
	SessionID string
	ID        string
	Name      string
	Prompt    string
	Cadence   json.RawMessage
	Timezone  string
	Enabled   bool
	NextRunAt *time.Time
}

type MaterializeScheduleOccurrenceParams struct {
	ScheduleID   string
	SessionID    string
	Prompt       string
	Trigger      string
	ScheduledFor time.Time
	NextRunAt    *time.Time
	Advance      bool
}

type SetNotificationKeysParams struct {
	PublicKey  string
	PrivateKey string
}

type SavePushSubscriptionParams struct {
	Endpoint  string
	P256DH    string
	Auth      string
	UserAgent string
	Origin    string
}

type DisablePushSubscriptionParams struct {
	Endpoint  string
	LastError string
}

type RecordPushDeliveryAttemptParams struct {
	EndpointHash   string
	Origin         string
	PayloadKind    string
	SessionID      string
	EventType      string
	HTTPStatus     int
	ResponseStatus string
	Error          string
}

type MarkNotificationAttentionParams struct {
	SessionID string
	Seq       int64
	EventType string
}
