package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/threave-io/threave/internal/agents"
	eventservice "github.com/threave-io/threave/internal/events"
	"github.com/threave-io/threave/internal/hosting"
	runcontrol "github.com/threave-io/threave/internal/session"
	"github.com/threave-io/threave/internal/store"
)

type createSessionRequest struct {
	AgentType       string              `json:"agent_type"`
	Title           string              `json:"title"`
	WorkspacePath   string              `json:"workspace_path"`
	AgentOptions    *createAgentOptions `json:"agent_options,omitempty"`
	ParentSessionID string              `json:"parent_session_id,omitempty"`
}

type updateSessionRequest struct {
	Title           *string             `json:"title,omitempty"`
	ParentSessionID *string             `json:"parent_session_id,omitempty"`
	WorkspacePath   *string             `json:"workspace_path,omitempty"`
	AgentOptions    *createAgentOptions `json:"agent_options,omitempty"`
	Pinned          *bool               `json:"pinned,omitempty"`
}

type createAgentOptions struct {
	Codex    *createCodexOptions    `json:"codex,omitempty"`
	Claude   *createClaudeOptions   `json:"claude,omitempty"`
	OpenCode *createOpenCodeOptions `json:"opencode,omitempty"`
	Pi       *runtimePiOptions      `json:"pi,omitempty"`
}

type createCodexOptions struct {
	RunDangerously   bool   `json:"run_dangerously,omitempty"`
	PermissionPolicy string `json:"permission_policy,omitempty"`
	Model            string `json:"model,omitempty"`
	ReasoningEffort  string `json:"reasoning_effort,omitempty"`
	FastMode         *bool  `json:"fast_mode,omitempty"`
	PlanningMode     *bool  `json:"planning_mode,omitempty"`
}

type createClaudeOptions struct {
	RunDangerously   bool   `json:"run_dangerously,omitempty"`
	PermissionPolicy string `json:"permission_policy,omitempty"`
	Model            string `json:"model,omitempty"`
	Effort           string `json:"effort,omitempty"`
	PlanningMode     *bool  `json:"planning_mode,omitempty"`
}

type createOpenCodeOptions struct {
	PermissionPolicy string `json:"permission_policy,omitempty"`
	Model            string `json:"model,omitempty"`
	PlanningMode     *bool  `json:"planning_mode,omitempty"`
}

type updateSessionRuntimeAgentOptionsRequest struct {
	Options            *runtimeAgentOptions `json:"options"`
	InitializeIfAbsent bool                 `json:"initialize_if_absent,omitempty"`
}

type runtimeAgentOptions struct {
	Codex    *runtimeCodexOptions    `json:"codex,omitempty"`
	Claude   *runtimeClaudeOptions   `json:"claude,omitempty"`
	OpenCode *runtimeOpenCodeOptions `json:"opencode,omitempty"`
	Pi       *runtimePiOptions       `json:"pi,omitempty"`
}

type runtimeCodexOptions struct {
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	FastMode        bool   `json:"fast_mode"`
	PlanningMode    bool   `json:"planning_mode"`
}

type runtimeClaudeOptions struct {
	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
	PlanningMode bool   `json:"planning_mode"`
}

type runtimeOpenCodeOptions struct {
	Model        string `json:"model,omitempty"`
	PlanningMode bool   `json:"planning_mode"`
}

type runtimePiOptions struct {
	Model         string `json:"model,omitempty"`
	ThinkingLevel string `json:"thinking_level,omitempty"`
}

type updateSessionRuntimeAgentOptionsResponse struct {
	Session sessionResponse `json:"session"`
	Applied bool            `json:"applied"`
}

type createSessionResponse struct {
	SessionID string `json:"session_id"`
}

type sessionResponse struct {
	ID                       string  `json:"id"`
	ParentSessionID          string  `json:"parent_session_id,omitempty"`
	SpawnedByRunID           string  `json:"spawned_by_run_id,omitempty"`
	LineageDepth             int     `json:"lineage_depth"`
	ChildCount               int     `json:"child_count"`
	Title                    string  `json:"title"`
	AgentType                string  `json:"agent_type"`
	Status                   string  `json:"status"`
	ProviderSessionID        string  `json:"provider_session_id,omitempty"`
	WorkspacePath            string  `json:"workspace_path"`
	AgentOptions             any     `json:"agent_options"`
	EventCount               int64   `json:"event_count"`
	LastEventSeq             int64   `json:"last_event_seq"`
	ToolCount                int64   `json:"tool_count"`
	TokenCount               int64   `json:"token_count"`
	NotificationAttentionSeq int64   `json:"notification_attention_seq,omitempty"`
	PendingInput             bool    `json:"pending_input"`
	PendingPermissionCount   int     `json:"pending_permission_count"`
	CreatedAt                string  `json:"created_at"`
	UpdatedAt                string  `json:"updated_at"`
	LastActivityAt           *string `json:"last_activity_at"`
	CompletedAt              *string `json:"completed_at"`
	ArchivedAt               *string `json:"archived_at"`
	PinnedAt                 *string `json:"pinned_at"`
}

type listSessionsResponse struct {
	Sessions    []sessionResponse `json:"sessions"`
	EventCursor int64             `json:"event_cursor"`
}

type submitMessageRequest struct {
	Content            string                 `json:"content"`
	AgentOptions       *submitAgentOptions    `json:"agent_options,omitempty"`
	Attachments        []submitAttachment     `json:"attachments,omitempty"`
	Skills             []submitSkillReference `json:"skills,omitempty"`
	Queue              bool                   `json:"queue,omitempty"`
	Steer              bool                   `json:"steer,omitempty"`
	ExpectedRunID      string                 `json:"expected_run_id,omitempty"`
	ClientSubmissionID string                 `json:"client_submission_id,omitempty"`
	RejectIfBusy       bool                   `json:"reject_if_busy,omitempty"`
}

type submitSkillReference struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type submitAgentOptions struct {
	Codex    *submitCodexOptions    `json:"codex,omitempty"`
	Claude   *submitClaudeOptions   `json:"claude,omitempty"`
	OpenCode *submitOpenCodeOptions `json:"opencode,omitempty"`
	Pi       *submitPiOptions       `json:"pi,omitempty"`
}

type submitCodexOptions struct {
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	FastMode        bool   `json:"fast_mode,omitempty"`
	PlanningMode    bool   `json:"planning_mode,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
}

type submitClaudeOptions struct {
	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
	PlanningMode bool   `json:"planning_mode,omitempty"`
}

type submitOpenCodeOptions struct {
	Model        string `json:"model,omitempty"`
	PlanningMode bool   `json:"planning_mode,omitempty"`
}

type submitPiOptions struct {
	Model         string `json:"model,omitempty"`
	ThinkingLevel string `json:"thinking_level,omitempty"`
}

type submitAttachment struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	DataURL   string `json:"data_url"`
	SizeBytes int64  `json:"size_bytes"`
}

type submitMessageResponse struct {
	SessionID     string                 `json:"session_id"`
	RunID         string                 `json:"run_id,omitempty"`
	Status        string                 `json:"status"`
	AcceptedAs    string                 `json:"accepted_as,omitempty"`
	QueuedMessage *queuedMessageResponse `json:"queued_message,omitempty"`
}

type queuedMessageResponse struct {
	ID           string `json:"id"`
	SessionID    string `json:"session_id"`
	Seq          int64  `json:"seq"`
	Content      string `json:"content"`
	AgentOptions any    `json:"agent_options"`
	Skills       any    `json:"skills"`
	CreatedAt    string `json:"created_at"`
}

type queuedMessagesResponse struct {
	Messages []queuedMessageResponse `json:"messages"`
}

const (
	maxSubmitAttachments     = 8
	maxSubmitAttachmentBytes = 5 * 1024 * 1024
	maxQueuedMessages        = 5
)

type cancelSessionResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type answerUserInputRequest struct {
	Answers map[string]agents.UserInputQuestionAnswer `json:"answers"`
}

type answerUserInputResponse struct {
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
}

func (api API) listSessionsHandler(w http.ResponseWriter, r *http.Request) {
	limit, ok := parseSessionLimit(w, r)
	if !ok {
		return
	}
	status, ok := parseSessionStatus(w, r)
	if !ok {
		return
	}
	includeArchived, ok := parseIncludeArchived(w, r)
	if !ok {
		return
	}

	var eventCursor int64
	if globalStore, ok := api.store.(GlobalEventStore); ok {
		var err error
		eventCursor, err = globalStore.GlobalEventCursor(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to capture event cursor")
			return
		}
	}

	var sessions []store.Session
	var err error
	if status == "" {
		if hierarchy, ok := api.store.(interface {
			ListSessionTree(context.Context, int, bool) ([]store.Session, error)
		}); ok {
			sessions, err = hierarchy.ListSessionTree(r.Context(), limit, includeArchived)
		} else {
			sessions, err = api.store.ListSessions(r.Context(), store.ListSessionsParams{Limit: limit, IncludeArchived: includeArchived})
		}
	} else {
		sessions, err = api.store.ListSessions(r.Context(), store.ListSessionsParams{Limit: limit, Status: status, IncludeArchived: includeArchived})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}

	responses, err := api.sessionResponses(r.Context(), sessions)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session activity")
		return
	}

	writeJSON(w, http.StatusOK, listSessionsResponse{Sessions: responses, EventCursor: eventCursor})
}

func (api API) getSessionHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	session, err := api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}

	response, err := api.sessionResponse(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session activity")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

type sessionChildrenStore interface {
	ListSessionChildren(context.Context, string, bool, bool) ([]store.Session, error)
}

func (api API) listSessionChildrenHandler(w http.ResponseWriter, r *http.Request) {
	lineage, ok := api.store.(sessionChildrenStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "session lineage is unavailable")
		return
	}
	parentID := chi.URLParam(r, "sessionId")
	if _, err := api.store.GetSession(r.Context(), parentID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	recursive := false
	if raw := strings.TrimSpace(r.URL.Query().Get("recursive")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "recursive must be a boolean")
			return
		}
		recursive = value
	}
	includeArchived, valid := parseIncludeArchived(w, r)
	if !valid {
		return
	}
	children, err := lineage.ListSessionChildren(r.Context(), parentID, recursive, includeArchived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list child sessions")
		return
	}
	responses, err := api.sessionResponses(r.Context(), children)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load child session activity")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": 1, "parent_session_id": parentID, "recursive": recursive, "sessions": responses})
}

func (api API) clearSessionNotificationAttentionHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	if err := api.store.ClearNotificationAttention(r.Context(), sessionID); err != nil {
		if errors.Is(err, store.ErrInvalidArgument) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to clear notification attention")
		return
	}

	session, err := api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}

	response, err := api.sessionResponse(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session activity")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (api API) clearAllSessionNotificationAttentionHandler(w http.ResponseWriter, r *http.Request) {
	if err := api.store.ClearAllNotificationAttention(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear notification attention")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

func (api API) createSessionHandler(w http.ResponseWriter, r *http.Request) {
	var request createSessionRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}

	request.ParentSessionID = strings.TrimSpace(request.ParentSessionID)
	var parent store.Session
	if request.ParentSessionID != "" {
		var loadErr error
		parent, loadErr = api.store.GetSession(r.Context(), request.ParentSessionID)
		if errors.Is(loadErr, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "parent session not found")
			return
		}
		if loadErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to load parent session")
			return
		}
		if parent.ArchivedAt != nil {
			writeError(w, http.StatusConflict, "parent session is archived")
			return
		}
	}

	agentType := strings.TrimSpace(request.AgentType)
	if agentType == "" && request.ParentSessionID != "" {
		agentType = parent.AgentType
	}
	if agentType == "" {
		writeError(w, http.StatusBadRequest, "agent_type is required")
		return
	}
	agent, ok := api.agents.Get(agentType)
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported agent_type")
		return
	}
	if !api.agentAvailable(w, agent) {
		return
	}

	workspaceRequest := request.WorkspacePath
	parentWorkspace := ""
	if request.ParentSessionID != "" {
		var resolveErr error
		parentWorkspace, resolveErr = api.workspaces.resolveWorkspacePath(sessionWorkspacePath(parent, api.workdir))
		if resolveErr != nil {
			writeWorkspacePathError(w, resolveErr)
			return
		}
		if strings.TrimSpace(workspaceRequest) == "" {
			workspaceRequest = parentWorkspace
		}
	}
	workspacePath, err := api.workspaces.resolveWorkspacePath(workspaceRequest)
	if err != nil {
		writeWorkspacePathError(w, err)
		return
	}
	if request.ParentSessionID != "" && workspacePath != parentWorkspace {
		writeError(w, http.StatusBadRequest, "child sessions must use the parent workspace")
		return
	}
	var agentOptions json.RawMessage
	if request.ParentSessionID != "" && agentType == parent.AgentType {
		agentOptions, err = mergeInheritedCreateOptions(parent.AgentOptions, agentType, request.AgentOptions)
	} else {
		agentOptions, err = createSessionAgentOptions(agentType, request.AgentOptions)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	session, err := api.store.CreateSession(r.Context(), store.CreateSessionParams{
		Title:           request.Title,
		AgentType:       agentType,
		WorkspacePath:   workspacePath,
		AgentOptions:    agentOptions,
		ParentSessionID: request.ParentSessionID,
		MaxLineageDepth: defaultMaxLineageDepth,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "parent session not found")
			return
		}
		if errors.Is(err, store.ErrInvalidArgument) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	writeJSON(w, http.StatusCreated, createSessionResponse{SessionID: session.ID})
}

func (api API) updateSessionHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")

	var request updateSessionRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}

	if request.Title == nil && request.ParentSessionID == nil && request.WorkspacePath == nil && request.AgentOptions == nil && request.Pinned == nil {
		writeError(w, http.StatusBadRequest, "session update requires title, parent_session_id, workspace_path, agent_options, or pinned")
		return
	}
	if request.AgentOptions != nil && api.agentOptionsMu != nil {
		api.agentOptionsMu.Lock()
		defer api.agentOptionsMu.Unlock()
	}

	session, err := api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	if request.Pinned != nil && *request.Pinned && session.ArchivedAt != nil {
		writeError(w, http.StatusConflict, "archived session cannot be pinned")
		return
	}

	var workspacePath string
	workspaceChanged := false
	if request.WorkspacePath != nil {
		if strings.TrimSpace(*request.WorkspacePath) == "" {
			writeError(w, http.StatusBadRequest, "workspace_path is required")
			return
		}
		if session.Status == store.SessionStatusRunning || api.runs.Active(sessionID) {
			writeError(w, http.StatusConflict, "stop the active agent run before changing workspace")
			return
		}
		if status, ok := api.console.Status(sessionID); ok && status.Running {
			writeError(w, http.StatusConflict, "stop the active console before changing workspace")
			return
		}
		workspacePath, err = api.workspaces.resolveWorkspacePath(*request.WorkspacePath)
		if err != nil {
			writeWorkspacePathError(w, err)
			return
		}
		workspaceChanged = workspacePath != session.WorkspacePath
	}

	var agentOptions json.RawMessage
	agentOptionsChanged := false
	if request.AgentOptions != nil {
		var permissionOptions json.RawMessage
		permissionOptions, err = createSessionAgentOptions(session.AgentType, request.AgentOptions)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		agentOptions, agentOptionsChanged, err = mergeSessionPermissionAgentOptions(
			session.AgentOptions,
			permissionOptions,
			session.AgentType,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to merge session agent options")
			return
		}
	}

	if request.Title != nil {
		session, err = api.store.UpdateSessionTitle(r.Context(), store.UpdateSessionTitleParams{
			ID:    sessionID,
			Title: *request.Title,
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			if errors.Is(err, store.ErrInvalidArgument) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to update session")
			return
		}
	}

	if request.ParentSessionID != nil {
		session, err = api.store.UpdateSessionParent(r.Context(), store.UpdateSessionParentParams{
			ID:              sessionID,
			ParentSessionID: *request.ParentSessionID,
			MaxLineageDepth: defaultMaxLineageDepth,
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			if errors.Is(err, store.ErrInvalidArgument) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to update session parent")
			return
		}
	}

	if workspaceChanged {
		if err := api.stopHostedPreview(r.Context(), sessionID); err != nil {
			writeError(w, http.StatusConflict, "failed to stop the hosted preview before changing workspace: "+err.Error())
			return
		}
		previousWorkspacePath := session.WorkspacePath
		session, err = api.store.UpdateSessionWorkspace(r.Context(), store.UpdateSessionWorkspaceParams{
			ID:            sessionID,
			WorkspacePath: workspacePath,
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			if errors.Is(err, store.ErrInvalidArgument) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to update session workspace")
			return
		}
		if err := api.appendWorkspaceChanged(r.Context(), sessionID, previousWorkspacePath, workspacePath); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist workspace change")
			return
		}
	}
	if request.Pinned != nil {
		session, err = api.store.UpdateSessionPin(r.Context(), store.UpdateSessionPinParams{
			ID:     sessionID,
			Pinned: *request.Pinned,
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			if errors.Is(err, store.ErrInvalidArgument) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to update session pin")
			return
		}
		if err := api.appendSessionPinUpdated(r.Context(), session); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist session pin update")
			return
		}
		session, err = api.store.GetSession(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to reload session")
			return
		}
	}

	if request.AgentOptions != nil && agentOptionsChanged {
		session, err = api.store.UpdateSessionAgentOptions(r.Context(), store.UpdateSessionAgentOptionsParams{
			ID:           sessionID,
			AgentOptions: agentOptions,
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			if errors.Is(err, store.ErrInvalidArgument) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to update session")
			return
		}
		if err := api.appendSessionAgentOptionsUpdated(r.Context(), session); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist session agent options update")
			return
		}
		session, err = api.store.GetSession(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to reload session")
			return
		}
	}
	if workspaceChanged {
		session, err = api.store.GetSession(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to reload session")
			return
		}
	}

	response, err := api.sessionResponse(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session activity")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (api API) updateSessionRuntimeAgentOptionsHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	var request updateSessionRuntimeAgentOptionsRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	if request.Options == nil {
		writeError(w, http.StatusBadRequest, "runtime agent options are required")
		return
	}

	if api.agentOptionsMu != nil {
		api.agentOptionsMu.Lock()
		defer api.agentOptionsMu.Unlock()
	}

	session, err := api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}

	agentOptions, initialized, changed, err := mergeSessionRuntimeAgentOptions(
		session.AgentOptions,
		session.AgentType,
		request.Options,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.InitializeIfAbsent && initialized {
		response, responseErr := api.sessionResponse(r.Context(), session)
		if responseErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to load session activity")
			return
		}
		writeJSON(w, http.StatusOK, updateSessionRuntimeAgentOptionsResponse{Session: response, Applied: false})
		return
	}

	applied := false
	if changed {
		session, err = api.store.UpdateSessionAgentOptions(r.Context(), store.UpdateSessionAgentOptionsParams{
			ID:           sessionID,
			AgentOptions: agentOptions,
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			if errors.Is(err, store.ErrInvalidArgument) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to update session agent options")
			return
		}
		if err := api.appendSessionAgentOptionsUpdated(r.Context(), session); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist session agent options update")
			return
		}
		session, err = api.store.GetSession(r.Context(), sessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to reload session")
			return
		}
		applied = true
	}

	response, err := api.sessionResponse(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session activity")
		return
	}
	writeJSON(w, http.StatusOK, updateSessionRuntimeAgentOptionsResponse{Session: response, Applied: applied})
}

func (api API) archiveSessionHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")

	session, err := api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	if session.Status == store.SessionStatusRunning {
		writeError(w, http.StatusConflict, "running session cannot be archived")
		return
	}
	if err := api.stopHostedPreview(r.Context(), sessionID); err != nil {
		writeError(w, http.StatusConflict, "failed to stop the hosted preview before archiving: "+err.Error())
		return
	}
	if api.schedules != nil {
		if err := api.schedules.PauseForArchive(r.Context(), sessionID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to pause schedules before archiving")
			return
		}
	}

	archived, err := api.store.ArchiveSession(r.Context(), store.ArchiveSessionParams{ID: session.ID})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		if errors.Is(err, store.ErrInvalidArgument) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to archive session")
		return
	}
	if err := api.appendSessionArchiveUpdated(r.Context(), archived, "session.archived"); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist session archive update")
		return
	}
	archived, err = api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload session")
		return
	}

	response, err := api.sessionResponse(r.Context(), archived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session activity")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (api API) restoreSessionHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")

	restored, err := api.store.RestoreSession(r.Context(), store.RestoreSessionParams{ID: sessionID})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		if errors.Is(err, store.ErrInvalidArgument) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to restore session")
		return
	}
	if err := api.appendSessionArchiveUpdated(r.Context(), restored, "session.restored"); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist session restore update")
		return
	}
	restored, err = api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload session")
		return
	}

	response, err := api.sessionResponse(r.Context(), restored)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session activity")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (api API) sessionResponses(ctx context.Context, sessions []store.Session) ([]sessionResponse, error) {
	responses := make([]sessionResponse, 0, len(sessions))
	for _, session := range sessions {
		response, err := api.sessionResponse(ctx, session)
		if err != nil {
			return nil, err
		}
		responses = append(responses, response)
	}
	return responses, nil
}

func (api API) sessionResponse(ctx context.Context, session store.Session) (sessionResponse, error) {
	if live, ok := api.events.(SessionActivityEventService); ok {
		for _, event := range live.LiveSessionSnapshot(session.ID).Events {
			if store.IsSessionActivityEventType(event.Type) && (session.LastActivityAt == nil || event.CreatedAt.After(*session.LastActivityAt)) {
				activityAt := event.CreatedAt
				session.LastActivityAt = &activityAt
			}
		}
	}
	return sessionResponseFromStore(
		session,
		session.PendingInputCount > 0,
		int(session.PendingPermissionCount),
	), nil
}

func sessionResponseFromStore(session store.Session, pendingInput bool, pendingPermissionCount int) sessionResponse {
	var lastActivityAt *string
	if session.LastActivityAt != nil {
		formatted := session.LastActivityAt.UTC().Format(time.RFC3339Nano)
		lastActivityAt = &formatted
	}
	var completedAt *string
	if session.CompletedAt != nil {
		formatted := session.CompletedAt.UTC().Format(time.RFC3339Nano)
		completedAt = &formatted
	}
	var archivedAt *string
	if session.ArchivedAt != nil {
		formatted := session.ArchivedAt.UTC().Format(time.RFC3339Nano)
		archivedAt = &formatted
	}
	var pinnedAt *string
	if session.PinnedAt != nil {
		formatted := session.PinnedAt.UTC().Format(time.RFC3339Nano)
		pinnedAt = &formatted
	}

	agentOptions := map[string]any{}
	if len(session.AgentOptions) > 0 {
		_ = json.Unmarshal(session.AgentOptions, &agentOptions)
	}

	return sessionResponse{
		ID:                       session.ID,
		ParentSessionID:          session.ParentSessionID,
		SpawnedByRunID:           session.SpawnedByRunID,
		LineageDepth:             session.LineageDepth,
		ChildCount:               session.ChildCount,
		Title:                    session.Title,
		AgentType:                session.AgentType,
		Status:                   string(session.Status),
		ProviderSessionID:        session.ProviderSessionID,
		WorkspacePath:            session.WorkspacePath,
		AgentOptions:             agentOptions,
		EventCount:               session.EventCount,
		LastEventSeq:             session.LastEventSeq,
		ToolCount:                session.ToolCount,
		TokenCount:               session.TokenCount,
		NotificationAttentionSeq: session.NotificationAttentionSeq,
		PendingInput:             pendingInput,
		PendingPermissionCount:   pendingPermissionCount,
		CreatedAt:                session.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:                session.UpdatedAt.UTC().Format(time.RFC3339Nano),
		LastActivityAt:           lastActivityAt,
		CompletedAt:              completedAt,
		ArchivedAt:               archivedAt,
		PinnedAt:                 pinnedAt,
	}
}

func createSessionAgentOptions(agentType string, options *createAgentOptions) (json.RawMessage, error) {
	if options == nil || options.Codex == nil && options.Claude == nil && options.OpenCode == nil && options.Pi == nil {
		if agentType == "codex" || agentType == "claude" || agentType == "opencode" {
			return json.RawMessage(fmt.Sprintf(`{"%s":{"permission_policy":"ask"}}`, agentType)), nil
		}
		return json.RawMessage(`{}`), nil
	}
	if options.Codex != nil && agentType != "codex" {
		return nil, fmt.Errorf("codex options require a codex session")
	}
	if options.Claude != nil && agentType != "claude" {
		return nil, fmt.Errorf("claude options require a claude session")
	}
	if options.OpenCode != nil && agentType != "opencode" {
		return nil, fmt.Errorf("opencode options require an opencode session")
	}
	if options.Pi != nil && agentType != "pi" {
		return nil, fmt.Errorf("pi options require a pi session")
	}

	agentOptions := map[string]any{}
	if options.Codex != nil {
		codexOptions := map[string]any{}
		if options.Codex.RunDangerously {
			codexOptions["run_dangerously"] = true
		}
		policy, err := permissionPolicy(options.Codex.PermissionPolicy)
		if err != nil {
			return nil, err
		}
		if policy == "" && !options.Codex.RunDangerously {
			policy = "ask"
		}
		if policy != "" {
			codexOptions["permission_policy"] = policy
		}
		if model := strings.TrimSpace(options.Codex.Model); model != "" {
			codexOptions["model"] = model
		}
		if effort := strings.TrimSpace(options.Codex.ReasoningEffort); effort != "" {
			codexOptions["reasoning_effort"] = effort
		}
		if strings.TrimSpace(options.Codex.Model) != "" || strings.TrimSpace(options.Codex.ReasoningEffort) != "" || options.Codex.FastMode != nil || options.Codex.PlanningMode != nil {
			codexOptions["fast_mode"] = boolOption(options.Codex.FastMode)
			codexOptions["planning_mode"] = boolOption(options.Codex.PlanningMode)
		}
		if len(codexOptions) > 0 {
			agentOptions["codex"] = codexOptions
		}
	}
	if options.Claude != nil {
		claudeOptions := map[string]any{}
		if options.Claude.RunDangerously {
			claudeOptions["run_dangerously"] = true
		}
		policy, err := permissionPolicy(options.Claude.PermissionPolicy)
		if err != nil {
			return nil, err
		}
		if policy == "" && !options.Claude.RunDangerously {
			policy = "ask"
		}
		if policy != "" {
			claudeOptions["permission_policy"] = policy
		}
		if model := strings.TrimSpace(options.Claude.Model); model != "" {
			claudeOptions["model"] = model
		}
		if effort := strings.TrimSpace(options.Claude.Effort); effort != "" {
			claudeOptions["effort"] = effort
		}
		if strings.TrimSpace(options.Claude.Model) != "" || strings.TrimSpace(options.Claude.Effort) != "" || options.Claude.PlanningMode != nil {
			claudeOptions["planning_mode"] = boolOption(options.Claude.PlanningMode)
		}
		if len(claudeOptions) > 0 {
			agentOptions["claude"] = claudeOptions
		}
	}
	if options.OpenCode != nil {
		opencodeOptions := map[string]any{}
		policy, err := permissionPolicy(options.OpenCode.PermissionPolicy)
		if err != nil {
			return nil, err
		}
		if policy == "" {
			policy = "ask"
		}
		if policy != "" {
			opencodeOptions["permission_policy"] = policy
		}
		if model := strings.TrimSpace(options.OpenCode.Model); model != "" {
			opencodeOptions["model"] = model
		}
		if strings.TrimSpace(options.OpenCode.Model) != "" || options.OpenCode.PlanningMode != nil {
			opencodeOptions["planning_mode"] = boolOption(options.OpenCode.PlanningMode)
		}
		if len(opencodeOptions) > 0 {
			agentOptions["opencode"] = opencodeOptions
		}
	}
	if options.Pi != nil {
		piOptions := map[string]any{}
		if model := strings.TrimSpace(options.Pi.Model); model != "" {
			piOptions["model"] = model
		}
		if level := strings.TrimSpace(options.Pi.ThinkingLevel); level != "" {
			piOptions["thinking_level"] = level
		}
		if len(piOptions) > 0 {
			agentOptions["pi"] = piOptions
		}
	}

	encoded, err := json.Marshal(agentOptions)
	if err != nil {
		return nil, fmt.Errorf("marshal agent options: %w", err)
	}
	return encoded, nil
}

func boolOption(value *bool) bool {
	return value != nil && *value
}

func mergeSessionPermissionAgentOptions(
	existing json.RawMessage,
	replacement json.RawMessage,
	agentType string,
) (json.RawMessage, bool, error) {
	current, err := agentOptionsObject(existing)
	if err != nil {
		return nil, false, err
	}
	normalizedBefore, err := json.Marshal(current)
	if err != nil {
		return nil, false, fmt.Errorf("marshal existing agent options: %w", err)
	}
	replacementOptions, err := agentOptionsObject(replacement)
	if err != nil {
		return nil, false, err
	}
	provider := providerAgentOptions(current, agentType)
	delete(provider, "run_dangerously")
	delete(provider, "permission_policy")
	if replacementProvider, ok := replacementOptions[agentType].(map[string]any); ok {
		for _, key := range []string{"run_dangerously", "permission_policy"} {
			if value, exists := replacementProvider[key]; exists {
				provider[key] = value
			}
		}
	}
	if len(provider) == 0 {
		delete(current, agentType)
	} else {
		current[agentType] = provider
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return nil, false, fmt.Errorf("marshal merged agent options: %w", err)
	}
	return encoded, string(encoded) != string(normalizedBefore), nil
}

func mergeSessionRuntimeAgentOptions(
	existing json.RawMessage,
	agentType string,
	options *runtimeAgentOptions,
) (json.RawMessage, bool, bool, error) {
	replacement, err := runtimeAgentOptionsForProvider(agentType, options)
	if err != nil {
		return nil, false, false, err
	}
	current, err := agentOptionsObject(existing)
	if err != nil {
		return nil, false, false, err
	}
	normalizedBefore, err := json.Marshal(current)
	if err != nil {
		return nil, false, false, fmt.Errorf("marshal existing agent options: %w", err)
	}
	provider := providerAgentOptions(current, agentType)
	initialized := false
	for _, key := range runtimeAgentOptionKeys(agentType) {
		if _, ok := provider[key]; ok {
			initialized = true
		}
		delete(provider, key)
	}
	for key, value := range replacement {
		provider[key] = value
	}
	current[agentType] = provider
	encoded, err := json.Marshal(current)
	if err != nil {
		return nil, false, false, fmt.Errorf("marshal merged agent options: %w", err)
	}
	return encoded, initialized, string(encoded) != string(normalizedBefore), nil
}

func runtimeAgentOptionsForProvider(agentType string, options *runtimeAgentOptions) (map[string]any, error) {
	if options == nil {
		return nil, fmt.Errorf("runtime agent options are required")
	}
	providerCount := 0
	if options.Codex != nil {
		providerCount++
	}
	if options.Claude != nil {
		providerCount++
	}
	if options.OpenCode != nil {
		providerCount++
	}
	if options.Pi != nil {
		providerCount++
	}
	if providerCount != 1 {
		return nil, fmt.Errorf("runtime options must contain exactly one agent provider")
	}

	result := map[string]any{}
	switch agentType {
	case "codex":
		if options.Codex == nil {
			return nil, fmt.Errorf("codex options require a codex session")
		}
		if value := strings.TrimSpace(options.Codex.Model); value != "" {
			result["model"] = value
		}
		if value := strings.TrimSpace(options.Codex.ReasoningEffort); value != "" {
			result["reasoning_effort"] = value
		}
		result["fast_mode"] = options.Codex.FastMode
		result["planning_mode"] = options.Codex.PlanningMode
	case "claude":
		if options.Claude == nil {
			return nil, fmt.Errorf("claude options require a claude session")
		}
		if value := strings.TrimSpace(options.Claude.Model); value != "" {
			result["model"] = value
		}
		if value := strings.TrimSpace(options.Claude.Effort); value != "" {
			result["effort"] = value
		}
		result["planning_mode"] = options.Claude.PlanningMode
	case "opencode":
		if options.OpenCode == nil {
			return nil, fmt.Errorf("opencode options require an opencode session")
		}
		if value := strings.TrimSpace(options.OpenCode.Model); value != "" {
			result["model"] = value
		}
		result["planning_mode"] = options.OpenCode.PlanningMode
	case "pi":
		if options.Pi == nil {
			return nil, fmt.Errorf("pi options require a pi session")
		}
		if value := strings.TrimSpace(options.Pi.Model); value != "" {
			result["model"] = value
		}
		if value := strings.TrimSpace(options.Pi.ThinkingLevel); value != "" {
			result["thinking_level"] = value
		}
	default:
		return nil, fmt.Errorf("agent type %q does not support runtime options", agentType)
	}
	return result, nil
}

func runtimeAgentOptionKeys(agentType string) []string {
	switch agentType {
	case "codex":
		return []string{"model", "reasoning_effort", "fast_mode", "planning_mode"}
	case "claude":
		return []string{"model", "effort", "planning_mode"}
	case "opencode":
		return []string{"model", "planning_mode"}
	case "pi":
		return []string{"model", "thinking_level"}
	default:
		return nil
	}
}

func agentOptionsObject(raw json.RawMessage) (map[string]any, error) {
	result := map[string]any{}
	if len(raw) == 0 {
		return result, nil
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode agent options: %w", err)
	}
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}

func providerAgentOptions(options map[string]any, agentType string) map[string]any {
	if provider, ok := options[agentType].(map[string]any); ok {
		return provider
	}
	return map[string]any{}
}

func permissionPolicy(value string) (string, error) {
	value = strings.TrimSpace(value)
	switch value {
	case "", "ask", "deny", "bypass":
		return value, nil
	default:
		return "", fmt.Errorf("unsupported permission_policy %q", value)
	}
}

func submitOptionsMetadata(agentType string, sessionAgentOptions json.RawMessage, options *submitAgentOptions) (map[string]any, map[string]any, error) {
	metadata := map[string]any{
		"agent_type": agentType,
	}
	codexOptions := map[string]any{}
	claudeOptions := map[string]any{}
	opencodeOptions := map[string]any{}
	piOptions := map[string]any{}
	if len(sessionAgentOptions) > 0 {
		var persisted map[string]map[string]any
		if err := json.Unmarshal(sessionAgentOptions, &persisted); err != nil {
			return nil, nil, fmt.Errorf("session agent options are invalid")
		}
		for key, value := range persisted["codex"] {
			codexOptions[key] = value
		}
		for key, value := range persisted["claude"] {
			claudeOptions[key] = value
		}
		for key, value := range persisted["opencode"] {
			opencodeOptions[key] = value
		}
		for key, value := range persisted["pi"] {
			piOptions[key] = value
		}
	}
	if options == nil || options.Codex == nil && options.Claude == nil && options.OpenCode == nil && options.Pi == nil {
		responseOptions := map[string]any{}
		if len(codexOptions) > 0 {
			metadata["codex_options"] = codexOptions
			responseOptions["codex"] = codexOptions
		}
		if len(claudeOptions) > 0 {
			metadata["claude_options"] = claudeOptions
			responseOptions["claude"] = claudeOptions
		}
		if len(opencodeOptions) > 0 {
			metadata["opencode_options"] = opencodeOptions
			responseOptions["opencode"] = opencodeOptions
		}
		if len(piOptions) > 0 {
			metadata["pi_options"] = piOptions
			responseOptions["pi"] = piOptions
		}
		if len(responseOptions) == 0 {
			return metadata, nil, nil
		}
		return metadata, responseOptions, nil
	}
	if options.Pi != nil {
		if options.Codex != nil || options.Claude != nil || options.OpenCode != nil {
			return nil, nil, fmt.Errorf("only one agent options block is supported")
		}
		if agentType != "pi" {
			return nil, nil, fmt.Errorf("pi options require a pi session")
		}
		piOptions["model"] = strings.TrimSpace(options.Pi.Model)
		piOptions["thinking_level"] = strings.TrimSpace(options.Pi.ThinkingLevel)
		metadata["pi_options"] = piOptions
		return metadata, map[string]any{"pi": piOptions}, nil
	}
	if options.OpenCode != nil {
		if options.Codex != nil || options.Claude != nil {
			return nil, nil, fmt.Errorf("only one agent options block is supported")
		}
		if agentType != "opencode" {
			return nil, nil, fmt.Errorf("opencode options require an opencode session")
		}
		opencodeOptions["model"] = strings.TrimSpace(options.OpenCode.Model)
		opencodeOptions["planning_mode"] = options.OpenCode.PlanningMode
		metadata["opencode_options"] = opencodeOptions
		return metadata, map[string]any{"opencode": opencodeOptions}, nil
	}
	if options.Codex == nil {
		if options.Claude == nil {
			return metadata, nil, nil
		}
		if agentType != "claude" {
			return nil, nil, fmt.Errorf("claude options require a claude session")
		}
		claudeOptions["model"] = strings.TrimSpace(options.Claude.Model)
		claudeOptions["effort"] = strings.TrimSpace(options.Claude.Effort)
		claudeOptions["planning_mode"] = options.Claude.PlanningMode
		if options.Claude.PlanningMode {
			claudeOptions["permission_mode"] = "plan"
		} else {
			claudeOptions["permission_mode"] = ""
		}
		metadata["claude_options"] = claudeOptions
		return metadata, map[string]any{"claude": claudeOptions}, nil
	}
	if agentType != "codex" {
		return nil, nil, fmt.Errorf("codex options require a codex session")
	}

	codexOptions["model"] = strings.TrimSpace(options.Codex.Model)
	codexOptions["reasoning_effort"] = strings.TrimSpace(options.Codex.ReasoningEffort)
	codexOptions["fast_mode"] = options.Codex.FastMode
	codexOptions["planning_mode"] = options.Codex.PlanningMode
	codexOptions["service_tier"] = strings.TrimSpace(options.Codex.ServiceTier)
	metadata["codex_options"] = codexOptions
	return metadata, map[string]any{"codex": codexOptions}, nil
}

func validateSubmitAttachments(attachments []submitAttachment) ([]agents.Attachment, error) {
	if len(attachments) > maxSubmitAttachments {
		return nil, fmt.Errorf("too many attachments: maximum is %d", maxSubmitAttachments)
	}

	normalized := make([]agents.Attachment, 0, len(attachments))
	for index, attachment := range attachments {
		name := strings.TrimSpace(attachment.Name)
		if name == "" {
			name = fmt.Sprintf("image-%d", index+1)
		}
		mediaType := strings.TrimSpace(attachment.MediaType)
		dataURL := strings.TrimSpace(attachment.DataURL)
		if mediaType == "" {
			return nil, fmt.Errorf("attachment %d media_type is required", index+1)
		}
		if !strings.HasPrefix(mediaType, "image/") {
			return nil, fmt.Errorf("attachment %d must be an image", index+1)
		}
		if dataURL == "" {
			return nil, fmt.Errorf("attachment %d data_url is required", index+1)
		}
		if err := validateImageDataURL(dataURL, mediaType); err != nil {
			return nil, fmt.Errorf("attachment %d %v", index+1, err)
		}

		sizeBytes := attachment.SizeBytes
		if sizeBytes <= 0 {
			sizeBytes = dataURLDecodedBytes(dataURL)
		}
		if sizeBytes <= 0 {
			return nil, fmt.Errorf("attachment %d size is required", index+1)
		}
		if sizeBytes > maxSubmitAttachmentBytes {
			return nil, fmt.Errorf("attachment %d exceeds %d MB", index+1, maxSubmitAttachmentBytes/(1024*1024))
		}

		normalized = append(normalized, agents.Attachment{
			Name:      name,
			MediaType: mediaType,
			DataURL:   dataURL,
			SizeBytes: sizeBytes,
		})
	}
	return normalized, nil
}

func validateImageDataURL(dataURL string, mediaType string) error {
	header, payload, ok := strings.Cut(dataURL, ",")
	if !ok || !strings.HasPrefix(header, "data:") {
		return errors.New("must be a data URL")
	}
	headerMediaType := strings.TrimPrefix(strings.SplitN(header, ";", 2)[0], "data:")
	if headerMediaType != mediaType {
		return fmt.Errorf("data URL media type %q does not match %q", headerMediaType, mediaType)
	}
	if !strings.Contains(header, ";base64") {
		return errors.New("must be base64 encoded")
	}
	if payload == "" {
		return errors.New("has empty data")
	}
	if _, err := base64.StdEncoding.DecodeString(payload); err != nil {
		return errors.New("has invalid base64 data")
	}
	return nil
}

func dataURLDecodedBytes(dataURL string) int64 {
	_, payload, ok := strings.Cut(dataURL, ",")
	if !ok {
		return 0
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return 0
	}
	return int64(len(decoded))
}

func (api API) submitDecodedMessage(w http.ResponseWriter, r *http.Request, request submitMessageRequest) {
	sessionID := chi.URLParam(r, "sessionId")

	content := strings.TrimSpace(request.Content)
	clientSubmissionID := strings.TrimSpace(request.ClientSubmissionID)
	if len(clientSubmissionID) > 128 {
		writeError(w, http.StatusBadRequest, "client_submission_id must be 128 characters or fewer")
		return
	}
	attachments, err := validateSubmitAttachments(request.Attachments)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if content == "" && len(attachments) == 0 && len(request.Skills) == 0 {
		writeError(w, http.StatusBadRequest, "content, attachments, or skills are required")
		return
	}

	session, err := api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}

	if session.ArchivedAt != nil {
		writeError(w, http.StatusConflict, "session is archived")
		return
	}
	skills, err := api.validateSkillReferences(r.Context(), session, request.Skills)
	if err != nil {
		if errors.Is(err, agents.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "agent unavailable")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.Steer {
		api.steerSessionMessage(w, r, session, request, content, attachments, skills)
		return
	}
	if request.ExpectedRunID != "" {
		writeError(w, http.StatusBadRequest, "expected_run_id requires steer")
		return
	}
	if session.Status == store.SessionStatusRunning && request.RejectIfBusy && !request.Queue {
		writeError(w, http.StatusConflict, "session is busy; use queue or steer explicitly")
		return
	}
	if request.Queue || session.Status == store.SessionStatusRunning {
		if len(attachments) > 0 {
			writeError(w, http.StatusBadRequest, "queued messages cannot include image attachments")
			return
		}
		queued, ok := api.enqueueSessionMessage(w, r, session, content, request.AgentOptions, skills, clientSubmissionID)
		if !ok {
			return
		}
		writeJSON(w, http.StatusAccepted, submitMessageResponse{
			SessionID:     session.ID,
			Status:        string(session.Status),
			AcceptedAs:    "queued",
			QueuedMessage: queuedMessageToResponse(queued),
		})
		return
	}

	metadata, eventOptions, err := submitOptionsMetadata(session.AgentType, session.AgentOptions, request.AgentOptions)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	agent, ok := api.agents.Get(session.AgentType)
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported agent_type")
		return
	}
	if !api.agentAvailable(w, agent) {
		return
	}
	var sourceMetadata map[string]any
	if clientSubmissionID != "" {
		sourceMetadata = map[string]any{"client_submission_id": clientSubmissionID}
	}

	updatedSession, runID, ok := api.startSessionRun(
		w,
		r,
		session,
		content,
		attachments,
		skills,
		agent,
		metadata,
		eventOptions,
		agents.AgentActionMessage,
		func(ctx context.Context) error {
			return api.appendUserMessage(ctx, session.ID, content, attachments, skills, eventOptions, sourceMetadata, "")
		},
		"failed to persist user message",
	)
	if !ok {
		return
	}

	writeJSON(w, http.StatusAccepted, submitMessageResponse{
		SessionID:  updatedSession.ID,
		RunID:      runID,
		Status:     string(updatedSession.Status),
		AcceptedAs: "run",
	})
}

func (api API) listQueuedMessagesHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	if _, err := api.store.GetSession(r.Context(), sessionID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}

	messages, err := api.store.ListQueuedMessages(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load queued messages")
		return
	}
	response := queuedMessagesResponse{Messages: make([]queuedMessageResponse, 0, len(messages))}
	for _, message := range messages {
		response.Messages = append(response.Messages, *queuedMessageToResponse(message))
	}
	writeJSON(w, http.StatusOK, response)
}

func (api API) removeQueuedMessageHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	messageID := chi.URLParam(r, "queuedMessageId")

	removed, err := api.store.RemoveQueuedMessage(r.Context(), store.QueueMessageIDParams{
		SessionID: sessionID,
		ID:        messageID,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "queued message not found")
			return
		}
		if errors.Is(err, store.ErrInvalidArgument) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to remove queued message")
		return
	}
	if err := api.appendQueuedMessageRemoved(r.Context(), removed); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist queued message removal")
		return
	}
	writeJSON(w, http.StatusOK, queuedMessageToResponse(removed))
}

func (api API) enqueueSessionMessage(
	w http.ResponseWriter,
	r *http.Request,
	session store.Session,
	content string,
	options *submitAgentOptions,
	skills []agents.SkillReference,
	clientSubmissionID string,
) (store.QueuedMessage, bool) {
	if _, _, err := submitOptionsMetadata(session.AgentType, session.AgentOptions, options); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return store.QueuedMessage{}, false
	}
	rawOptions, err := json.Marshal(optionsOrEmpty(options))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode queued message options")
		return store.QueuedMessage{}, false
	}
	rawSkills, err := json.Marshal(skills)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode queued message skills")
		return store.QueuedMessage{}, false
	}

	queued, err := api.store.EnqueueMessage(r.Context(), store.EnqueueMessageParams{
		SessionID:          session.ID,
		Content:            content,
		AgentOptions:       rawOptions,
		Skills:             rawSkills,
		MaxPending:         maxQueuedMessages,
		ClientSubmissionID: clientSubmissionID,
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidArgument) {
			writeError(w, http.StatusBadRequest, err.Error())
			return store.QueuedMessage{}, false
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return store.QueuedMessage{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to queue message")
		return store.QueuedMessage{}, false
	}
	if err := api.appendQueuedMessageQueued(r.Context(), queued); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist queued message event")
		return store.QueuedMessage{}, false
	}
	return queued, true
}

func optionsOrEmpty(options *submitAgentOptions) any {
	if options == nil {
		return map[string]any{}
	}
	return options
}

func decodeAgentOptions(raw json.RawMessage) any {
	agentOptions := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &agentOptions)
	}
	return agentOptions
}

func decodeSkillReferences(raw json.RawMessage) []agents.SkillReference {
	skills := make([]agents.SkillReference, 0)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &skills)
	}
	return skills
}

func queuedMessageToResponse(message store.QueuedMessage) *queuedMessageResponse {
	return &queuedMessageResponse{
		ID:           message.ID,
		SessionID:    message.SessionID,
		Seq:          message.Seq,
		Content:      message.Content,
		AgentOptions: decodeAgentOptions(message.AgentOptions),
		Skills:       decodeSkillReferences(message.Skills),
		CreatedAt:    message.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (api API) clearSessionHandler(w http.ResponseWriter, r *http.Request) {
	if !validateEmptyBody(w, r, string(agents.AgentActionClear)) {
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	session, err := api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	if session.Status == store.SessionStatusRunning {
		writeError(w, http.StatusConflict, "session is already running")
		return
	}
	if session.ArchivedAt != nil {
		writeError(w, http.StatusConflict, "session is archived")
		return
	}
	if session.AgentType != "codex" && session.AgentType != "opencode" {
		writeError(w, http.StatusBadRequest, "session clear requires a codex or opencode session")
		return
	}

	clearedSession, err := api.store.ClearSessionProviderSessionID(r.Context(), store.ClearSessionProviderSessionIDParams{ID: session.ID})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		if errors.Is(err, store.ErrInvalidArgument) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to clear session context")
		return
	}
	if err := api.appendSessionAction(r.Context(), session.ID, agents.AgentActionClear); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist session action")
		return
	}
	if clearedSession.Status != store.SessionStatusIdle {
		clearedSession, err = api.updateSessionStatus(r.Context(), store.UpdateSessionStatusParams{
			ID:     clearedSession.ID,
			Status: store.SessionStatusIdle,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to mark session idle")
			return
		}
	}

	writeJSON(w, http.StatusAccepted, submitMessageResponse{
		SessionID: clearedSession.ID,
		Status:    string(clearedSession.Status),
	})
}

func (api API) compactSessionHandler(w http.ResponseWriter, r *http.Request) {
	api.compactSessionActionHandler(w, r)
}

func (api API) compactSessionActionHandler(w http.ResponseWriter, r *http.Request) {
	if !validateEmptyBody(w, r, string(agents.AgentActionCompact)) {
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	session, err := api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	if session.Status == store.SessionStatusRunning {
		writeError(w, http.StatusConflict, "session is already running")
		return
	}
	if session.ArchivedAt != nil {
		writeError(w, http.StatusConflict, "session is archived")
		return
	}
	if session.AgentType != "codex" {
		writeError(w, http.StatusBadRequest, "session action requires a codex session")
		return
	}
	if strings.TrimSpace(session.ProviderSessionID) == "" {
		writeError(w, http.StatusConflict, "session has no codex thread to compact")
		return
	}

	metadata, _, err := submitOptionsMetadata(session.AgentType, session.AgentOptions, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	metadata["agent_action"] = string(agents.AgentActionCompact)

	agent, ok := api.agents.Get(session.AgentType)
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported agent_type")
		return
	}
	if !api.agentAvailable(w, agent) {
		return
	}

	updatedSession, runID, ok := api.startSessionRun(
		w,
		r,
		session,
		"",
		nil,
		nil,
		agent,
		metadata,
		nil,
		agents.AgentActionCompact,
		func(ctx context.Context) error {
			return api.appendSessionAction(ctx, session.ID, agents.AgentActionCompact)
		},
		"failed to persist session action",
	)
	if !ok {
		return
	}

	writeJSON(w, http.StatusAccepted, submitMessageResponse{
		SessionID: updatedSession.ID,
		RunID:     runID,
		Status:    string(updatedSession.Status),
	})
}

func (api API) startSessionRun(
	w http.ResponseWriter,
	r *http.Request,
	session store.Session,
	message string,
	attachments []agents.Attachment,
	skills []agents.SkillReference,
	agent agents.Agent,
	metadata map[string]any,
	requestedOptions map[string]any,
	action agents.AgentAction,
	appendBeforeRun func(context.Context) error,
	appendErrorMessage string,
) (store.Session, string, bool) {
	runID, err := store.NewRunID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create run")
		return store.Session{}, "", false
	}
	return api.startSessionRunWithID(w, r, session, message, attachments, skills, agent, metadata, requestedOptions, action, appendBeforeRun, appendErrorMessage, runID)
}

func (api API) startSessionRunWithID(
	w http.ResponseWriter,
	r *http.Request,
	session store.Session,
	message string,
	attachments []agents.Attachment,
	skills []agents.SkillReference,
	agent agents.Agent,
	metadata map[string]any,
	requestedOptions map[string]any,
	action agents.AgentAction,
	appendBeforeRun func(context.Context) error,
	appendErrorMessage string,
	runID string,
) (store.Session, string, bool) {
	initialDelegation := session.ParentSessionID != "" && session.EventCount == 0
	runCtx, cleanup, err := api.runs.RegisterRun(context.Background(), session.ID, runID)
	if err != nil {
		if errors.Is(err, runcontrol.ErrRunAlreadyActive) {
			writeError(w, http.StatusConflict, "session is already running")
			return store.Session{}, "", false
		}
		writeError(w, http.StatusInternalServerError, "failed to register run")
		return store.Session{}, "", false
	}

	if appendBeforeRun != nil {
		if err := appendBeforeRun(r.Context()); err != nil {
			cleanup()
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "session not found")
				return store.Session{}, "", false
			}
			if errors.Is(err, store.ErrInvalidArgument) {
				writeError(w, http.StatusBadRequest, err.Error())
				return store.Session{}, "", false
			}
			writeError(w, http.StatusInternalServerError, appendErrorMessage)
			return store.Session{}, "", false
		}
	}

	updatedSession, err := api.store.UpdateSessionStatus(r.Context(), store.UpdateSessionStatusParams{
		ID:     session.ID,
		Status: store.SessionStatusRunning,
	})
	if err != nil {
		cleanup()
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return store.Session{}, "", false
		}
		writeError(w, http.StatusInternalServerError, "failed to mark session running")
		return store.Session{}, "", false
	}
	if err := api.appendSessionStatusUpdated(r.Context(), updatedSession, map[string]any{
		"run_id":                  runID,
		"run_kind":                string(action),
		"agent_type":              session.AgentType,
		"workspace_path":          sessionWorkspacePath(session, api.workdir),
		"agent_options":           metadata,
		"requested_agent_options": requestedOptions,
	}); err != nil {
		cleanup()
		writeError(w, http.StatusInternalServerError, "failed to emit session status")
		return store.Session{}, "", false
	}

	go func() {
		completed := api.runAgent(runCtx, updatedSession, message, attachments, skills, agent, metadata, action, runID)
		cleanup()
		if initialDelegation {
			api.appendDelegationCompletion(session, runID)
		}
		if completed {
			api.startQueuedMessageRun(context.Background(), session.ID)
		}
	}()

	return updatedSession, runID, true
}

func (api API) appendDelegationCompletion(child store.Session, childRunID string) {
	status := "unknown"
	finalResponse := ""
	if persistence, ok := api.store.(runControlStore); ok {
		if run, err := persistence.GetRun(context.Background(), childRunID); err == nil {
			status, finalResponse = run.Status, run.FinalResponse
		}
	}
	eventStatus := store.EventStatusCompleted
	if status == "failed" {
		eventStatus = store.EventStatusFailed
	}
	if status == "cancelled" {
		eventStatus = store.EventStatusCancelled
	}
	payload := map[string]any{"parent_session_id": child.ParentSessionID, "child_session_id": child.ID,
		"child_run_id": childRunID, "status": status, "agent_type": child.AgentType}
	if strings.TrimSpace(finalResponse) != "" {
		payload["summary"] = finalResponse
	}
	if err := api.appendAgentEvent(context.Background(), child.ParentSessionID, agents.AgentEvent{
		Type: "agent.delegation.completed", Role: "assistant", Status: string(eventStatus), Payload: payload,
	}, child.SpawnedByRunID); err != nil {
		log.Printf("failed to persist delegation completion: parent_session_id=%s child_session_id=%s child_run_id=%s error=%v", child.ParentSessionID, child.ID, childRunID, err)
	}
}

func (api API) startQueuedMessageRun(ctx context.Context, sessionID string) bool {
	queued, err := api.store.ClaimNextQueuedMessage(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("failed to claim queued message: session_id=%s error=%v", sessionID, err)
		}
		return false
	}
	release := func() {
		if _, releaseErr := api.store.ReleaseQueuedMessage(context.Background(), store.QueueMessageIDParams{
			SessionID: queued.SessionID,
			ID:        queued.ID,
		}); releaseErr != nil {
			log.Printf("failed to release queued message: session_id=%s queue_item_id=%s error=%v", queued.SessionID, queued.ID, releaseErr)
		}
	}

	session, err := api.store.GetSession(ctx, queued.SessionID)
	if err != nil {
		log.Printf("failed to load queued message session: session_id=%s queue_item_id=%s error=%v", queued.SessionID, queued.ID, err)
		release()
		return false
	}
	if session.Status == store.SessionStatusRunning || session.ArchivedAt != nil {
		release()
		return false
	}
	var scheduledOccurrence *store.ScheduleOccurrence
	if queued.SourceKind == "schedule" && api.schedules != nil {
		occurrence, occurrenceErr := api.schedules.Occurrence(ctx, queued.SourceID)
		if occurrenceErr != nil {
			log.Printf("failed to load scheduled occurrence: session_id=%s occurrence_id=%s error=%v", session.ID, queued.SourceID, occurrenceErr)
			release()
			return false
		}
		scheduledOccurrence = &occurrence
	}

	var queuedOptions submitAgentOptions
	if len(queued.AgentOptions) > 0 {
		if err := json.Unmarshal(queued.AgentOptions, &queuedOptions); err != nil {
			log.Printf("failed to decode queued message options: session_id=%s queue_item_id=%s error=%v", queued.SessionID, queued.ID, err)
			release()
			return false
		}
	}
	metadata, eventOptions, err := submitOptionsMetadata(session.AgentType, session.AgentOptions, &queuedOptions)
	if err != nil {
		log.Printf("failed to prepare queued message options: session_id=%s queue_item_id=%s error=%v", queued.SessionID, queued.ID, err)
		release()
		return false
	}
	sourceMetadata := map[string]any{}
	if scheduledOccurrence != nil {
		metadata["run_trigger"] = "schedule"
		metadata["schedule_id"] = scheduledOccurrence.ScheduleID
		metadata["occurrence_id"] = scheduledOccurrence.ID
		metadata["scheduled_for"] = scheduledOccurrence.ScheduledFor.UTC().Format(time.RFC3339Nano)
		for key, value := range metadata {
			if key == "run_trigger" || key == "schedule_id" || key == "occurrence_id" || key == "scheduled_for" {
				sourceMetadata[key] = value
			}
		}
	}
	skills := decodeSkillReferences(queued.Skills)

	agent, ok := api.agents.Get(session.AgentType)
	if !ok {
		log.Printf("queued message agent unavailable: session_id=%s queue_item_id=%s agent_type=%s", queued.SessionID, queued.ID, session.AgentType)
		release()
		return false
	}
	if availability, ok := agent.(agents.Availability); ok {
		if err := availability.Available(); err != nil {
			log.Printf("queued message agent unavailable: session_id=%s queue_item_id=%s agent_type=%s error=%v", queued.SessionID, queued.ID, session.AgentType, err)
			release()
			return false
		}
	}

	runID, err := store.NewRunID()
	if err != nil {
		log.Printf("failed to create queued message run: session_id=%s queue_item_id=%s error=%v", queued.SessionID, queued.ID, err)
		release()
		return false
	}
	runCtx, cleanup, err := api.runs.RegisterRun(context.Background(), session.ID, runID)
	if err != nil {
		if !errors.Is(err, runcontrol.ErrRunAlreadyActive) {
			log.Printf("failed to register queued message run: session_id=%s queue_item_id=%s error=%v", queued.SessionID, queued.ID, err)
		}
		release()
		return false
	}
	if err := api.appendUserMessage(ctx, session.ID, queued.Content, nil, skills, eventOptions, sourceMetadata, queued.ID); err != nil {
		cleanup()
		log.Printf("failed to append queued user message: session_id=%s queue_item_id=%s error=%v", queued.SessionID, queued.ID, err)
		release()
		return false
	}
	updatedSession, err := api.store.UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		ID:     session.ID,
		Status: store.SessionStatusRunning,
	})
	if err != nil {
		cleanup()
		log.Printf("failed to mark queued session running: session_id=%s queue_item_id=%s error=%v", queued.SessionID, queued.ID, err)
		release()
		return false
	}
	if err := api.appendSessionStatusUpdated(ctx, updatedSession, map[string]any{
		"run_id":                  runID,
		"run_kind":                string(agents.AgentActionMessage),
		"agent_type":              session.AgentType,
		"workspace_path":          sessionWorkspacePath(session, api.workdir),
		"agent_options":           metadata,
		"requested_agent_options": eventOptions,
	}); err != nil {
		cleanup()
		log.Printf("failed to emit queued session status: session_id=%s queue_item_id=%s error=%v", queued.SessionID, queued.ID, err)
		release()
		return false
	}
	if _, err := api.store.MarkQueuedMessageSent(ctx, store.QueueMessageIDParams{
		SessionID: queued.SessionID,
		ID:        queued.ID,
	}); err != nil {
		cleanup()
		log.Printf("failed to mark queued message sent: session_id=%s queue_item_id=%s error=%v", queued.SessionID, queued.ID, err)
		release()
		return false
	}
	if scheduledOccurrence != nil {
		api.schedules.MarkRunning(ctx, session.ID, scheduledOccurrence.ScheduleID, scheduledOccurrence.ID, runID)
	}

	go func() {
		completed := api.runAgent(runCtx, updatedSession, queued.Content, nil, skills, agent, metadata, agents.AgentActionMessage, runID)
		cleanup()
		if scheduledOccurrence != nil {
			status := "completed"
			if !completed {
				status = "failed"
				if current, loadErr := api.store.GetSession(context.Background(), session.ID); loadErr == nil && current.Status == store.SessionStatusIdle {
					status = "cancelled"
				}
			}
			api.schedules.MarkFinished(context.Background(), session.ID, scheduledOccurrence.ScheduleID, scheduledOccurrence.ID, status)
		}
		if completed || scheduledOccurrence != nil {
			api.startQueuedMessageRun(context.Background(), session.ID)
		}
	}()
	return true
}

func (api API) cancelSessionHandler(w http.ResponseWriter, r *http.Request) {
	cancellation, ok := parseCancellationRequest(w, r)
	if !ok {
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	session, err := api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}

	if session.Status != store.SessionStatusRunning {
		writeError(w, http.StatusConflict, "session is not running")
		return
	}

	if err := api.runs.Cancel(session.ID, cancellation); err != nil {
		if errors.Is(err, runcontrol.ErrRunAlreadyCanceled) {
			writeError(w, http.StatusConflict, "session cancellation already requested")
			return
		}
		if errors.Is(err, runcontrol.ErrRunNotActive) {
			updatedSession, ok := api.failRunningSessionWithoutActiveRun(r.Context(), session)
			if !ok {
				writeError(w, http.StatusInternalServerError, "failed to repair stale run")
				return
			}
			writeJSON(w, http.StatusAccepted, submitMessageResponse{
				SessionID: updatedSession.ID,
				Status:    string(updatedSession.Status),
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to cancel session")
		return
	}
	log.Printf("session cancellation requested: session_id=%s source=%s reason=%s", session.ID, cancellation.Source, cancellation.Reason)

	writeJSON(w, http.StatusAccepted, cancelSessionResponse{
		SessionID: session.ID,
		Status:    "cancelling",
	})
}

func (api API) answerUserInputHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	requestID := chi.URLParam(r, "requestId")

	var request answerUserInputRequest
	if !decodeJSONBody(w, r, &request) {
		return
	}
	if len(request.Answers) == 0 {
		writeError(w, http.StatusBadRequest, "answers are required")
		return
	}

	session, err := api.store.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load session")
		return
	}
	if session.Status != store.SessionStatusRunning {
		writeError(w, http.StatusConflict, "session is not running")
		return
	}

	pending, err := api.runs.PendingUserInput(session.ID, requestID)
	if err != nil {
		if errors.Is(err, runcontrol.ErrRunNotActive) || errors.Is(err, runcontrol.ErrUserInputNotActive) {
			writeError(w, http.StatusConflict, "user input request is not active")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load user input request")
		return
	}
	if err := validateUserInputAnswers(pending, request.Answers); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Once claimed, persist the user's intent before delivery. For asynchronous
	// questions, don't call it answered until the provider acknowledges the steer.
	// Detach from the HTTP connection: closing a mobile tab must not abort a reply
	// that the provider may already have received. Never automatically resend it.
	answerCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Second)
	defer cancel()
	submitted := false
	err = api.runs.AnswerUserInputWithPersistence(answerCtx, session.ID, pending.RequestID, agents.UserInputResponse{Answers: request.Answers}, func() error {
		eventType := "agent.input.answered"
		if pending.Delivery == "async" {
			eventType = "agent.input.submitted"
		}
		if err := api.appendUserInputAnswerEvent(answerCtx, session.ID, pending, request.Answers, eventType, ""); err != nil {
			return err
		}
		submitted = true
		return nil
	})
	if err != nil {
		if submitted && pending.Delivery == "async" {
			if persistErr := api.appendUserInputAnswerEvent(context.WithoutCancel(answerCtx), session.ID, pending, request.Answers, "agent.input.failed", "Answer delivery was not confirmed. It will not be resent automatically."); persistErr != nil {
				log.Printf("failed to persist answer delivery failure: session_id=%s request_id=%s error=%v", session.ID, pending.RequestID, persistErr)
			}
		}
		if errors.Is(err, runcontrol.ErrRunNotActive) || errors.Is(err, runcontrol.ErrUserInputNotActive) {
			writeError(w, http.StatusConflict, "user input request is not active")
			return
		}
		writeError(w, http.StatusInternalServerError, "Unable to confirm answer delivery. It has not been resent automatically.")
		return
	}
	if pending.Delivery == "async" {
		if err := api.appendUserInputAnswerEvent(answerCtx, session.ID, pending, request.Answers, "agent.input.answered", ""); err != nil {
			writeError(w, http.StatusInternalServerError, "Answer delivered, but its confirmation could not be saved. Do not resend it.")
			return
		}
	}

	writeJSON(w, http.StatusAccepted, answerUserInputResponse{
		SessionID: session.ID,
		RequestID: pending.RequestID,
		Status:    "answered",
	})
}

func (api API) appendUserMessage(
	ctx context.Context,
	sessionID string,
	content string,
	attachments []agents.Attachment,
	skills []agents.SkillReference,
	agentOptions map[string]any,
	sourceMetadata map[string]any,
	queueItemID string,
) error {
	payloadValue := map[string]any{"text": content}
	if len(attachments) > 0 {
		payloadValue["attachments"] = attachments
	}
	if len(skills) > 0 {
		payloadValue["skills"] = skills
	}
	if len(agentOptions) > 0 {
		payloadValue["agent_options"] = agentOptions
	}
	for key, value := range sourceMetadata {
		payloadValue[key] = value
	}
	if strings.TrimSpace(queueItemID) != "" {
		payloadValue["queue_item_id"] = strings.TrimSpace(queueItemID)
	}

	payload, err := json.Marshal(payloadValue)
	if err != nil {
		return fmt.Errorf("marshal user message payload: %w", err)
	}

	_, err = api.events.Append(ctx, eventservice.AppendParams{
		SessionID: sessionID,
		Type:      "user.message.completed",
		Role:      "user",
		Status:    store.EventStatusCompleted,
		Payload:   payload,
	})
	return err
}

func (api API) appendQueuedMessageQueued(ctx context.Context, message store.QueuedMessage) error {
	payload, err := json.Marshal(map[string]any{
		"queue_item_id":        message.ID,
		"client_submission_id": message.SourceID,
		"text":                 message.Content,
		"agent_options":        decodeAgentOptions(message.AgentOptions),
		"skills":               decodeSkillReferences(message.Skills),
	})
	if err != nil {
		return fmt.Errorf("marshal queued message payload: %w", err)
	}
	_, err = api.events.Append(ctx, eventservice.AppendParams{
		SessionID: message.SessionID,
		Type:      "user.message.queued",
		Role:      "user",
		Status:    store.EventStatusCompleted,
		Payload:   payload,
	})
	return err
}

func (api API) appendQueuedMessageRemoved(ctx context.Context, message store.QueuedMessage) error {
	payload, err := json.Marshal(map[string]any{
		"queue_item_id": message.ID,
		"text":          message.Content,
	})
	if err != nil {
		return fmt.Errorf("marshal queued message removed payload: %w", err)
	}
	_, err = api.events.Append(ctx, eventservice.AppendParams{
		SessionID: message.SessionID,
		Type:      "user.message.queue.removed",
		Role:      "user",
		Status:    store.EventStatusCompleted,
		Payload:   payload,
	})
	return err
}

func (api API) appendSessionAction(ctx context.Context, sessionID string, action agents.AgentAction) error {
	payloadValue := map[string]any{
		"action": string(action),
		"text":   userActionText(action),
	}
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		return fmt.Errorf("marshal user action payload: %w", err)
	}

	_, err = api.events.Append(ctx, eventservice.AppendParams{
		SessionID: sessionID,
		Type:      "session.action.completed",
		Role:      "system",
		Status:    store.EventStatusCompleted,
		Payload:   payload,
	})
	return err
}

func (api API) appendWorkspaceChanged(ctx context.Context, sessionID string, previousPath string, workspacePath string) error {
	payload, err := json.Marshal(map[string]any{
		"action":                  "workspace_changed",
		"label":                   "WORKSPACE CHANGED",
		"previous_workspace_path": previousPath,
		"workspace_path":          workspacePath,
	})
	if err != nil {
		return fmt.Errorf("marshal workspace change payload: %w", err)
	}

	_, err = api.events.Append(ctx, eventservice.AppendParams{
		SessionID: sessionID,
		Type:      "session.action.completed",
		Role:      "system",
		Status:    store.EventStatusCompleted,
		Payload:   payload,
	})
	return err
}

func (api API) appendSessionAgentOptionsUpdated(ctx context.Context, session store.Session) error {
	payload, err := json.Marshal(map[string]any{
		"agent_options": decodeAgentOptions(session.AgentOptions),
		"updated_at":    session.UpdatedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return fmt.Errorf("marshal session agent options update payload: %w", err)
	}

	_, err = api.events.Append(ctx, eventservice.AppendParams{
		SessionID: session.ID,
		Type:      "session.agent_options.updated",
		Role:      "system",
		Status:    store.EventStatusCompleted,
		Payload:   payload,
	})
	return err
}

func (api API) appendSessionPinUpdated(ctx context.Context, session store.Session) error {
	var pinnedAt any
	if session.PinnedAt != nil {
		pinnedAt = session.PinnedAt.UTC().Format(time.RFC3339Nano)
	}
	payload, err := json.Marshal(map[string]any{"pinned_at": pinnedAt})
	if err != nil {
		return fmt.Errorf("marshal session pin update payload: %w", err)
	}

	_, err = api.events.Append(ctx, eventservice.AppendParams{
		SessionID: session.ID,
		Type:      "session.pin.updated",
		Role:      "system",
		Status:    store.EventStatusCompleted,
		Payload:   payload,
	})
	return err
}

func (api API) appendSessionArchiveUpdated(ctx context.Context, session store.Session, eventType string) error {
	var archivedAt any
	if session.ArchivedAt != nil {
		archivedAt = session.ArchivedAt.UTC().Format(time.RFC3339Nano)
	}
	payload, err := json.Marshal(map[string]any{
		"archived_at": archivedAt,
		"updated_at":  session.UpdatedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return fmt.Errorf("marshal session archive update payload: %w", err)
	}

	_, err = api.events.Append(ctx, eventservice.AppendParams{
		SessionID: session.ID,
		Type:      eventType,
		Role:      "system",
		Status:    store.EventStatusCompleted,
		Payload:   payload,
	})
	return err
}

func userActionText(action agents.AgentAction) string {
	switch action {
	case agents.AgentActionClear:
		return "Clear context"
	case agents.AgentActionCompact:
		return "Compact context"
	default:
		return "Session action"
	}
}

func (api API) appendUserInputAnswerEvent(ctx context.Context, sessionID string, request agents.UserInputRequest, answers map[string]agents.UserInputQuestionAnswer, eventType string, deliveryError string) error {
	text := ""
	if request.Delivery == "async" && eventType == "agent.input.answered" {
		values := make([]string, 0, len(request.Questions))
		for _, question := range request.Questions {
			value := strings.Join(answers[question.ID].Answers, ", ")
			if question.IsSecret {
				value = "[redacted]"
			}
			values = append(values, value)
		}
		text = strings.Join(values, "\n\n")
	}
	status := string(store.EventStatusCompleted)
	if eventType == "agent.input.submitted" {
		status = string(store.EventStatusStarted)
	}
	if eventType == "agent.input.failed" {
		status = string(store.EventStatusFailed)
	}
	return api.appendAgentEvent(ctx, sessionID, agents.AgentEvent{
		Type:   eventType,
		Role:   "user",
		Status: status,
		Payload: map[string]any{
			"text":                text,
			"delivery":            request.Delivery,
			"error":               deliveryError,
			"provider":            request.Provider,
			"provider_event_type": request.ProviderEventType,
			"provider_request_id": request.ProviderRequestID,
			"request_id":          request.RequestID,
			"thread_id":           request.ThreadID,
			"turn_id":             request.TurnID,
			"item_id":             request.ItemID,
			"answers":             userInputAnsweredPayload(request, answers),
		},
	})
}

func (api API) runAgent(
	ctx context.Context,
	session store.Session,
	message string,
	attachments []agents.Attachment,
	skills []agents.SkillReference,
	agent agents.Agent,
	metadata map[string]any,
	action agents.AgentAction,
	runID string,
) bool {
	terminalEventEmitted := false
	cancellationLogged := false
	emit := func(ctx context.Context, event agents.AgentEvent) error {
		terminalEvent := isTerminalRunEvent(event.Type)
		if terminalEvent && terminalEventEmitted {
			return fmt.Errorf("terminal run event already emitted for session %s", session.ID)
		}
		if event.Type == "agent.run.cancelled" {
			cancellation := api.runCancellation(session.ID)
			event = withCancellationDetails(event, cancellation)
			log.Printf("agent run cancelled: session_id=%s agent_type=%s source=%s reason=%s", session.ID, agent.Type(), cancellation.Source, cancellation.Reason)
			cancellationLogged = true
		}

		updatedSession, err := api.persistProviderSessionIDFromEvent(ctx, session, event, action)
		if err != nil {
			return err
		}
		session = updatedSession

		if err := api.appendAgentEvent(ctx, session.ID, event, runID); err != nil {
			return err
		}
		if terminalEvent {
			terminalEventEmitted = true
		}
		return nil
	}

	steering, _ := api.runs.(agents.SteeringBroker)
	err := agent.Run(ctx, agents.AgentInput{
		SessionID:         session.ID,
		RunID:             runID,
		ProviderSessionID: session.ProviderSessionID,
		Action:            action,
		Message:           message,
		Workdir:           sessionWorkspacePath(session, api.workdir),
		Environment:       api.agentRuntimeEnvironment(session.ID, runID),
		Context:           api.agentRuntimeContext(session, runID),
		Metadata:          metadata,
		Attachments:       attachments,
		Skills:            skills,
		UserInput:         api.runs,
		Permissions:       api.runs,
		Steering:          steering,
	}, emit)
	if errors.Is(err, context.Canceled) {
		cancellation := api.runCancellation(session.ID)
		if !cancellationLogged {
			log.Printf("agent run cancelled: session_id=%s agent_type=%s source=%s reason=%s", session.ID, agent.Type(), cancellation.Source, cancellation.Reason)
		}
		if !terminalEventEmitted {
			if appendErr := api.appendAgentRunCancelled(context.Background(), session.ID, agent.Type(), cancellation, runID); appendErr != nil {
				log.Printf("failed to append agent.run.cancelled: session_id=%s agent_type=%s error=%v", session.ID, agent.Type(), appendErr)
			} else {
				terminalEventEmitted = true
			}
		}
		if _, updateErr := api.updateSessionStatus(context.Background(), store.UpdateSessionStatusParams{
			ID:     session.ID,
			Status: store.SessionStatusIdle,
		}); updateErr != nil {
			log.Printf("failed to mark session idle after cancellation: session_id=%s agent_type=%s error=%v", session.ID, agent.Type(), updateErr)
		}
		return false
	}
	if err != nil {
		log.Printf("agent run failed: session_id=%s agent_type=%s error=%v", session.ID, agent.Type(), err)

		if !terminalEventEmitted {
			if appendErr := api.appendAgentRunFailed(context.Background(), session.ID, agent.Type(), err, runID); appendErr != nil {
				log.Printf("failed to append agent.run.failed: session_id=%s agent_type=%s error=%v", session.ID, agent.Type(), appendErr)
			} else {
				terminalEventEmitted = true
			}
		}
		if _, updateErr := api.updateSessionStatus(context.Background(), store.UpdateSessionStatusParams{
			ID:     session.ID,
			Status: store.SessionStatusFailed,
		}); updateErr != nil {
			log.Printf("failed to mark session failed: session_id=%s agent_type=%s error=%v", session.ID, agent.Type(), updateErr)
		}
		return false
	}

	if !terminalEventEmitted {
		if appendErr := api.appendAgentRunCompleted(context.Background(), session.ID, agent.Type(), runID); appendErr != nil {
			log.Printf("failed to append agent.run.completed: session_id=%s agent_type=%s error=%v", session.ID, agent.Type(), appendErr)
			if _, updateErr := api.updateSessionStatus(context.Background(), store.UpdateSessionStatusParams{
				ID:     session.ID,
				Status: store.SessionStatusFailed,
			}); updateErr != nil {
				log.Printf("failed to mark session failed: session_id=%s agent_type=%s error=%v", session.ID, agent.Type(), updateErr)
			}
			return false
		}
	}

	if _, err := api.updateSessionStatus(context.Background(), store.UpdateSessionStatusParams{
		ID:     session.ID,
		Status: store.SessionStatusIdle,
	}); err != nil {
		log.Printf("failed to mark session idle after completion: session_id=%s agent_type=%s error=%v", session.ID, agent.Type(), err)
		return false
	}
	return true
}

func (api API) agentRuntimeEnvironment(sessionID string, runIDs ...string) map[string]string {
	environment := map[string]string{
		"GORCHESTRA_SESSION_ID": sessionID,
		"THREAVE_SESSION_ID":    sessionID,
	}
	if len(runIDs) > 0 && strings.TrimSpace(runIDs[0]) != "" {
		environment["GORCHESTRA_RUN_ID"] = strings.TrimSpace(runIDs[0])
		environment["THREAVE_RUN_ID"] = strings.TrimSpace(runIDs[0])
	}
	if strings.TrimSpace(api.agentAPIURL) != "" {
		environment["GORCHESTRA_API_URL"] = strings.TrimRight(api.agentAPIURL, "/")
		environment["THREAVE_API_URL"] = strings.TrimRight(api.agentAPIURL, "/")
	}
	if strings.TrimSpace(api.executable) != "" {
		environment["GORCHESTRA_BIN"] = api.executable
		environment["THREAVE_BIN"] = api.executable
	}
	return environment
}

func (api API) agentHostingContext(workspacePath string) string {
	recipePath := hosting.RecipePath(workspacePath)
	if info, err := os.Stat(recipePath); err != nil || info.IsDir() {
		return ""
	}
	return fmt.Sprintf(`This workspace has a Threave hosted-preview recipe at %s.
Use "$THREAVE_BIN" host validate|status|start|stop|restart|check|logs|url to manage this session's preview. The CLI targets this session automatically through THREAVE_SESSION_ID and THREAVE_API_URL.`, filepath.ToSlash(strings.TrimPrefix(recipePath, workspacePath+string(filepath.Separator))))
}

func (api API) agentRuntimeContext(session store.Session, runID string) string {
	parentSessionID := strings.TrimSpace(session.ParentSessionID)
	role := "This is a root session."
	if parentSessionID == "" {
		parentSessionID = "none"
	} else {
		role = "This is a delegated child session."
	}

	parts := []string{fmt.Sprintf(`This session is running inside Threave, an agent orchestration service that coordinates multiple concurrent agents across supported providers.
%s

Current session ID: %s
Current run ID: %s
Parent session ID: %s

The current session and run IDs are also available as $THREAVE_SESSION_ID and $THREAVE_RUN_ID.

You can use Threave's CLI to delegate independent work to child agents and monitor their exact runs. New runs automatically become children of this session and inherit its provider, resolved options, and workspace unless you override supported settings.

To delegate a named task:
  "$THREAVE_BIN" run --title "TASK NAME" --prompt-file task.md --detach --json

The result contains the child session ID and exact run ID. Then use:
  "$THREAVE_BIN" runs wait RUN_ID --timeout 10m --json
  "$THREAVE_BIN" runs report RUN_ID --json

You can search past chats across Threave sessions to find earlier discussions, decisions, and agent work. Search session titles and durable chat history without workspace files:
  "$THREAVE_BIN" search "QUERY" --session none --format ndjson

To also search this session's workspace files:
  "$THREAVE_BIN" search "QUERY" --session current --format ndjson

Chat history search always spans sessions. The --session option only selects the workspace files to include; it does not limit which chats are searched. Results include session IDs and history event sequence numbers so you can identify the source.

Use "$THREAVE_BIN" commands --json to discover the complete CLI contract. Use runs watch to stream activity. Child sessions share this workspace, so assign disjoint edits when delegating parallel work.

The previous GORCHESTRA_BIN, GORCHESTRA_SESSION_ID, GORCHESTRA_RUN_ID, and GORCHESTRA_API_URL variables remain available for older tooling.`, role, session.ID, runID, parentSessionID)}
	if hosting := api.agentHostingContext(sessionWorkspacePath(session, api.workdir)); hosting != "" {
		parts = append(parts, hosting)
	}
	return strings.Join(parts, "\n\n")
}

func (api API) persistProviderSessionIDFromEvent(
	ctx context.Context,
	session store.Session,
	event agents.AgentEvent,
	action agents.AgentAction,
) (store.Session, error) {
	providerSessionID := providerSessionIDFromAgentEvent(session.AgentType, event)
	if providerSessionID == "" {
		return session, nil
	}
	if session.ProviderSessionID != "" {
		if session.ProviderSessionID != providerSessionID {
			if action == agents.AgentActionClear {
				return api.store.SetSessionProviderSessionID(ctx, store.SetSessionProviderSessionIDParams{
					ID:                session.ID,
					ProviderSessionID: providerSessionID,
					Replace:           true,
				})
			}
			return store.Session{}, fmt.Errorf("provider session mismatch for session %s: existing %q, event %q", session.ID, session.ProviderSessionID, providerSessionID)
		}
		return session, nil
	}
	return api.store.SetSessionProviderSessionID(ctx, store.SetSessionProviderSessionIDParams{
		ID:                session.ID,
		ProviderSessionID: providerSessionID,
		Replace:           action == agents.AgentActionClear,
	})
}

func providerSessionIDFromAgentEvent(agentType string, event agents.AgentEvent) string {
	if event.Type != "agent.run.started" {
		return ""
	}
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return ""
	}
	switch agentType {
	case "codex":
		if payloadString(payload, "provider") != "codex" {
			return ""
		}
		switch payloadString(payload, "provider_event_type") {
		case "thread/start", "thread/resume", "thread/started", "thread/resumed":
			return payloadString(payload, "thread_id")
		default:
			return ""
		}
	case "claude":
		if payloadString(payload, "provider") != "claude" || payloadString(payload, "provider_event_type") != "system/init" {
			return ""
		}
		return payloadString(payload, "provider_session_id")
	case "opencode":
		if payloadString(payload, "provider") != "opencode" {
			return ""
		}
		switch payloadString(payload, "provider_event_type") {
		case "session/new", "session/resume", "session/load":
			return payloadString(payload, "provider_session_id")
		default:
			return ""
		}
	case "pi":
		if payloadString(payload, "provider") != "pi" || payloadString(payload, "provider_event_type") != "get_state" {
			return ""
		}
		return payloadString(payload, "provider_session_id")
	default:
		return ""
	}
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func (api API) updateSessionStatus(ctx context.Context, params store.UpdateSessionStatusParams) (store.Session, error) {
	session, err := api.store.UpdateSessionStatus(ctx, params)
	if err != nil {
		return store.Session{}, err
	}
	if err := api.appendSessionStatusUpdated(ctx, session); err != nil {
		return session, err
	}
	return session, nil
}

func (api API) appendAgentEvent(ctx context.Context, sessionID string, event agents.AgentEvent, runIDs ...string) error {
	eventType := strings.TrimSpace(event.Type)
	if eventType == "" {
		return fmt.Errorf("agent event type is required")
	}

	eventStatus := store.EventStatus(strings.TrimSpace(event.Status))
	if eventStatus == "" {
		return fmt.Errorf("agent event status is required")
	}

	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("marshal agent event payload: %w", err)
	}
	if len(runIDs) > 0 && strings.TrimSpace(runIDs[0]) != "" {
		var payloadValue map[string]any
		if err := json.Unmarshal(payload, &payloadValue); err == nil && payloadValue != nil {
			payloadValue["run_id"] = strings.TrimSpace(runIDs[0])
			payload, err = json.Marshal(payloadValue)
			if err != nil {
				return fmt.Errorf("marshal run-linked agent event payload: %w", err)
			}
		}
	}

	_, err = api.events.Append(ctx, eventservice.AppendParams{
		SessionID: sessionID,
		Type:      eventType,
		Role:      strings.TrimSpace(event.Role),
		Status:    eventStatus,
		Payload:   payload,
	})
	return err
}

func (api API) appendSessionStatusUpdated(ctx context.Context, session store.Session, metadata ...map[string]any) error {
	payload := map[string]any{
		"status":     string(session.Status),
		"updated_at": session.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if session.CompletedAt != nil {
		payload["completed_at"] = session.CompletedAt.UTC().Format(time.RFC3339Nano)
	} else {
		payload["completed_at"] = nil
	}
	if len(metadata) > 0 {
		for key, value := range metadata[0] {
			payload[key] = value
		}
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal session status payload: %w", err)
	}

	_, err = api.events.Append(ctx, eventservice.AppendParams{
		SessionID: session.ID,
		Type:      "session.status.updated",
		Role:      "system",
		Status:    eventStatusForSessionStatus(session.Status),
		Payload:   encoded,
	})
	return err
}

func eventStatusForSessionStatus(status store.SessionStatus) store.EventStatus {
	switch status {
	case store.SessionStatusRunning:
		return store.EventStatusStarted
	case store.SessionStatusFailed:
		return store.EventStatusFailed
	default:
		return store.EventStatusCompleted
	}
}

func (api API) appendAgentRunFailed(ctx context.Context, sessionID string, agentType string, runErr error, runIDs ...string) error {
	return api.appendAgentEvent(ctx, sessionID, agents.AgentEvent{
		Type:   "agent.run.failed",
		Role:   "assistant",
		Status: string(store.EventStatusFailed),
		Payload: map[string]any{
			"agent_type": agentType,
			"error":      runErr.Error(),
		},
	}, runIDs...)
}

func (api API) appendAgentRunCompleted(ctx context.Context, sessionID string, agentType string, runIDs ...string) error {
	return api.appendAgentEvent(ctx, sessionID, agents.AgentEvent{
		Type:   "agent.run.completed",
		Role:   "assistant",
		Status: string(store.EventStatusCompleted),
		Payload: map[string]any{
			"agent_type": agentType,
		},
	}, runIDs...)
}

func (api API) appendAgentRunCancelled(ctx context.Context, sessionID string, agentType string, cancellation runcontrol.Cancellation, runIDs ...string) error {
	return api.appendAgentEvent(ctx, sessionID, agents.AgentEvent{
		Type:   "agent.run.cancelled",
		Role:   "assistant",
		Status: string(store.EventStatusCancelled),
		Payload: map[string]any{
			"agent_type":    agentType,
			"cancel_source": cancellation.Source,
			"cancel_reason": cancellation.Reason,
		},
	}, runIDs...)
}

func (api API) runCancellation(sessionID string) runcontrol.Cancellation {
	if cancellation, ok := api.runs.Cancellation(sessionID); ok {
		return cancellation
	}
	return runcontrol.Cancellation{Source: "agent", Reason: "context_cancelled"}
}

func withCancellationDetails(event agents.AgentEvent, cancellation runcontrol.Cancellation) agents.AgentEvent {
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		payload = map[string]any{}
	} else {
		cloned := make(map[string]any, len(payload)+2)
		for key, value := range payload {
			cloned[key] = value
		}
		payload = cloned
	}
	payload["cancel_source"] = cancellation.Source
	payload["cancel_reason"] = cancellation.Reason
	event.Payload = payload
	return event
}

func (api API) failRunningSessionWithoutActiveRun(ctx context.Context, session store.Session) (store.Session, bool) {
	err := fmt.Errorf("running session has no active run")
	if appendErr := api.appendAgentRunFailed(ctx, session.ID, session.AgentType, err); appendErr != nil {
		log.Printf("failed to append agent.run.failed for missing active run: session_id=%s agent_type=%s error=%v", session.ID, session.AgentType, appendErr)
	}
	updatedSession, updateErr := api.updateSessionStatus(ctx, store.UpdateSessionStatusParams{
		ID:     session.ID,
		Status: store.SessionStatusFailed,
	})
	if updateErr != nil {
		log.Printf("failed to mark session failed for missing active run: session_id=%s agent_type=%s error=%v", session.ID, session.AgentType, updateErr)
		return store.Session{}, false
	}
	return updatedSession, true
}

func (api API) agentAvailable(w http.ResponseWriter, agent agents.Agent) bool {
	availability, ok := agent.(agents.Availability)
	if !ok {
		return true
	}
	if err := availability.Available(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "agent unavailable")
		return false
	}
	return true
}

func parseSessionLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultSessionLimit, true
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		writeError(w, http.StatusBadRequest, "limit must be a non-negative integer")
		return 0, false
	}
	if limit > maxSessionLimit {
		return maxSessionLimit, true
	}

	return limit, true
}

func parseSessionStatus(w http.ResponseWriter, r *http.Request) (store.SessionStatus, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("status"))
	if raw == "" {
		return "", true
	}

	status := store.SessionStatus(raw)
	switch status {
	case store.SessionStatusIdle,
		store.SessionStatusRunning,
		store.SessionStatusFailed:
		return status, true
	default:
		writeError(w, http.StatusBadRequest, "status is unsupported")
		return "", false
	}
}

func parseIncludeArchived(w http.ResponseWriter, r *http.Request) (bool, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("include_archived"))
	if raw == "" {
		return false, true
	}

	includeArchived, err := strconv.ParseBool(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "include_archived must be a boolean")
		return false, false
	}

	return includeArchived, true
}

func isTerminalRunEvent(eventType string) bool {
	switch eventType {
	case "agent.run.completed", "agent.run.failed", "agent.run.cancelled":
		return true
	default:
		return false
	}
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(value); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}

	var extra struct{}
	if err := decoder.Decode(&extra); err != nil {
		if !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return false
		}
		return true
	}

	writeError(w, http.StatusBadRequest, "invalid JSON body")
	return false
}

func parseCancellationRequest(w http.ResponseWriter, r *http.Request) (runcontrol.Cancellation, bool) {
	cancellation := runcontrol.Cancellation{Source: "api", Reason: "requested"}
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return cancellation, true
	}

	var request map[string]any
	if !decodeJSONBody(w, r, &request) {
		return runcontrol.Cancellation{}, false
	}
	if len(request) == 0 {
		return cancellation, true
	}
	source, sourceOK := request["source"].(string)
	reason, reasonOK := request["reason"].(string)
	source = strings.TrimSpace(source)
	reason = strings.TrimSpace(reason)
	if len(request) != 2 || !sourceOK || !reasonOK || source != "web_ui" || reason != "stop_button" {
		writeError(w, http.StatusBadRequest, "cancel source and reason are unsupported")
		return runcontrol.Cancellation{}, false
	}
	return runcontrol.Cancellation{Source: source, Reason: reason}, true
}

func validateEmptyBody(w http.ResponseWriter, r *http.Request, requestName string) bool {
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return true
	}

	var body map[string]any
	if !decodeJSONBody(w, r, &body) {
		return false
	}
	if len(body) > 0 {
		writeError(w, http.StatusBadRequest, requestName+" request body must be empty")
		return false
	}

	return true
}

func validateUserInputAnswers(request agents.UserInputRequest, answers map[string]agents.UserInputQuestionAnswer) error {
	questionsByID := make(map[string]agents.UserInputQuestion, len(request.Questions))
	for _, question := range request.Questions {
		questionsByID[question.ID] = question
	}

	for id := range answers {
		if _, ok := questionsByID[id]; !ok {
			return fmt.Errorf("answer %q does not match a pending question", id)
		}
	}
	for _, question := range request.Questions {
		answer, ok := answers[question.ID]
		if !ok {
			return fmt.Errorf("answer %q is required", question.ID)
		}
		if !question.MultiSelect && len(answer.Answers) != 1 {
			return fmt.Errorf("answer %q must include exactly one selection", question.ID)
		}
		if len(answer.Answers) == 0 {
			return fmt.Errorf("answer %q must include at least one selection", question.ID)
		}
		seen := make(map[string]bool, len(answer.Answers))
		for _, raw := range answer.Answers {
			value := strings.TrimSpace(raw)
			if value == "" {
				return fmt.Errorf("answer %q cannot be empty", question.ID)
			}
			if seen[value] {
				return fmt.Errorf("answer %q contains a duplicate selection", question.ID)
			}
			seen[value] = true
			if !questionAllowsAnswer(question, value) {
				return fmt.Errorf("answer %q is not a valid option", question.ID)
			}
		}
	}
	return nil
}

func questionAllowsAnswer(question agents.UserInputQuestion, value string) bool {
	for _, option := range question.Options {
		if value == option.Label {
			return true
		}
	}
	return question.IsOther
}

func userInputAnsweredPayload(
	request agents.UserInputRequest,
	answers map[string]agents.UserInputQuestionAnswer,
) map[string]any {
	secretQuestions := make(map[string]bool, len(request.Questions))
	for _, question := range request.Questions {
		secretQuestions[question.ID] = question.IsSecret
	}

	payload := make(map[string]any, len(answers))
	for id, answer := range answers {
		if secretQuestions[id] {
			payload[id] = map[string]any{
				"answers":  []string{"[redacted]"},
				"redacted": true,
			}
			continue
		}
		payload[id] = answer
	}
	return payload
}
