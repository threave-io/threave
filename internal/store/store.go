package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const defaultEventLimit = 500
const defaultSessionLimit = 50
const durableEventSQL = `type NOT LIKE '%.delta'`

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithClock(ctx, path, func() time.Time { return time.Now().UTC() })
}

// OpenWithClock opens a store with a caller-supplied clock. It is useful for
// constructing reproducible event histories and time-sensitive tests.
func OpenWithClock(ctx context.Context, path string, now func() time.Time) (*Store, error) {
	if now == nil {
		return nil, fmt.Errorf("open sqlite database: clock is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{
		db:  db,
		now: now,
	}

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable sqlite wal journal mode: %w", err)
	}

	if _, err := db.ExecContext(ctx, `PRAGMA synchronous = NORMAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set sqlite synchronous mode: %w", err)
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable sqlite foreign keys: %w", err)
	}

	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 30000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set sqlite busy timeout: %w", err)
	}

	if err := store.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.syncDashboardProjection(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.syncSearchProjection(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) CreateSession(ctx context.Context, params CreateSessionParams) (Session, error) {
	params.AgentType = strings.TrimSpace(params.AgentType)
	params.WorkspacePath = strings.TrimSpace(params.WorkspacePath)
	params.ParentSessionID = strings.TrimSpace(params.ParentSessionID)
	if params.AgentType == "" {
		return Session{}, fmt.Errorf("%w: agent_type is required", ErrInvalidArgument)
	}

	id, err := newPrefixedUUID("sess_")
	if err != nil {
		return Session{}, err
	}

	now := s.now()
	session := Session{
		ID:            id,
		Title:         params.Title,
		AgentType:     params.AgentType,
		Status:        SessionStatusIdle,
		WorkspacePath: params.WorkspacePath,
		AgentOptions:  json.RawMessage(`{}`),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if len(params.AgentOptions) > 0 {
		if !json.Valid(params.AgentOptions) {
			return Session{}, fmt.Errorf("%w: agent_options must be valid JSON", ErrInvalidArgument)
		}
		session.AgentOptions = append(json.RawMessage(nil), params.AgentOptions...)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("begin create session: %w", err)
	}
	defer rollback(tx)

	if params.ParentSessionID != "" {
		var parentDepth int
		var archivedAt sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT lineage_depth, archived_at FROM sessions WHERE id = ?`, params.ParentSessionID).Scan(&parentDepth, &archivedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Session{}, fmt.Errorf("%w: parent session", ErrNotFound)
			}
			return Session{}, fmt.Errorf("load parent session: %w", err)
		}
		if archivedAt.Valid {
			return Session{}, fmt.Errorf("%w: parent session is archived", ErrInvalidArgument)
		}
		session.ParentSessionID = params.ParentSessionID
		session.LineageDepth = parentDepth + 1
		if params.MaxLineageDepth > 0 && session.LineageDepth > params.MaxLineageDepth {
			return Session{}, fmt.Errorf("%w: maximum child depth %d exceeded", ErrInvalidArgument, params.MaxLineageDepth)
		}
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO sessions (id, title, agent_type, status, workspace_path, agent_options_json, created_at, updated_at,
		                       parent_session_id, lineage_depth)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID,
		session.Title,
		session.AgentType,
		string(session.Status),
		session.WorkspacePath,
		string(session.AgentOptions),
		formatTime(session.CreatedAt),
		formatTime(session.UpdatedAt),
		nullIfEmpty(session.ParentSessionID),
		session.LineageDepth,
	); err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit create session: %w", err)
	}

	return session, nil
}

func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, title, agent_type, status, provider_session_id, workspace_path, agent_options_json,
		        durable_event_count, last_durable_event_seq, materialized_tool_count, materialized_token_count,
		        pending_input_count, pending_permission_count,
		        COALESCE((SELECT seq FROM notification_attention WHERE notification_attention.session_id = sessions.id), 0) AS notification_attention_seq,
		        created_at, updated_at, last_activity_at, completed_at, archived_at, pinned_at,
		        parent_session_id, spawned_by_run_id, lineage_depth,
		        (SELECT COUNT(*) FROM sessions children WHERE children.parent_session_id = sessions.id AND children.archived_at IS NULL) AS child_count
		 FROM sessions
		 WHERE id = ?`,
		id,
	)

	session, err := scanSession(row)
	if err != nil {
		return Session{}, err
	}

	return session, nil
}

func (s *Store) ListSessions(ctx context.Context, params ListSessionsParams) ([]Session, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = defaultSessionLimit
	}

	query := `SELECT id, title, agent_type, status, provider_session_id, workspace_path, agent_options_json,
		        durable_event_count, last_durable_event_seq, materialized_tool_count, materialized_token_count,
		        pending_input_count, pending_permission_count,
		        COALESCE((SELECT seq FROM notification_attention WHERE notification_attention.session_id = sessions.id), 0) AS notification_attention_seq,
		        created_at, updated_at, last_activity_at, completed_at, archived_at, pinned_at,
		        parent_session_id, spawned_by_run_id, lineage_depth,
		        (SELECT COUNT(*) FROM sessions children WHERE children.parent_session_id = sessions.id AND children.archived_at IS NULL) AS child_count
		 FROM sessions`
	args := []any{}
	filters := make([]string, 0, 2)
	if !params.IncludeArchived {
		filters = append(filters, `archived_at IS NULL`)
	}
	if params.Status != "" {
		filters = append(filters, `status = ?`)
		args = append(args, string(params.Status))
	}
	if len(filters) > 0 {
		query += ` WHERE ` + strings.Join(filters, ` AND `)
	}
	query += ` ORDER BY CASE WHEN pinned_at IS NULL THEN 1 ELSE 0 END,
		 pinned_at DESC, COALESCE(last_activity_at, created_at) DESC, created_at DESC, id DESC
		 LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]Session, 0)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions rows: %w", err)
	}

	return sessions, nil
}

func (s *Store) UpdateSessionTitle(ctx context.Context, params UpdateSessionTitleParams) (Session, error) {
	if strings.TrimSpace(params.ID) == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalidArgument)
	}

	title := strings.TrimSpace(params.Title)
	now := s.now()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE sessions
		 SET title = ?, updated_at = ?
		 WHERE id = ?`,
		title,
		formatTime(now),
		params.ID,
	)
	if err != nil {
		return Session{}, fmt.Errorf("update session title: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return Session{}, fmt.Errorf("check updated session title rows: %w", err)
	}
	if rowsAffected == 0 {
		return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, params.ID)
	}

	return s.GetSession(ctx, params.ID)
}

func (s *Store) UpdateSessionParent(ctx context.Context, params UpdateSessionParentParams) (Session, error) {
	sessionID := strings.TrimSpace(params.ID)
	parentSessionID := strings.TrimSpace(params.ParentSessionID)
	if sessionID == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalidArgument)
	}
	if sessionID == parentSessionID {
		return Session{}, fmt.Errorf("%w: a session cannot be its own parent", ErrInvalidArgument)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("begin update session parent: %w", err)
	}
	defer rollback(tx)

	var currentParentID sql.NullString
	var currentDepth int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT parent_session_id, lineage_depth FROM sessions WHERE id = ?`,
		sessionID,
	).Scan(&currentParentID, &currentDepth); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, sessionID)
		}
		return Session{}, fmt.Errorf("load session lineage: %w", err)
	}
	if currentParentID.String == parentSessionID {
		if err := tx.Commit(); err != nil {
			return Session{}, fmt.Errorf("commit unchanged session parent: %w", err)
		}
		return s.GetSession(ctx, sessionID)
	}

	newDepth := 0
	if parentSessionID != "" {
		var parentDepth int
		if err := tx.QueryRowContext(ctx, `SELECT lineage_depth FROM sessions WHERE id = ?`, parentSessionID).Scan(&parentDepth); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Session{}, fmt.Errorf("%w: parent session", ErrNotFound)
			}
			return Session{}, fmt.Errorf("load parent session: %w", err)
		}

		var parentIsDescendant bool
		if err := tx.QueryRowContext(ctx, `
			WITH RECURSIVE subtree(id) AS (
				SELECT id FROM sessions WHERE id = ?
				UNION ALL
				SELECT child.id FROM sessions child JOIN subtree parent ON child.parent_session_id = parent.id
			)
			SELECT EXISTS(SELECT 1 FROM subtree WHERE id = ?)`, sessionID, parentSessionID).Scan(&parentIsDescendant); err != nil {
			return Session{}, fmt.Errorf("check session parent cycle: %w", err)
		}
		if parentIsDescendant {
			return Session{}, fmt.Errorf("%w: parent session cannot be a descendant", ErrInvalidArgument)
		}
		newDepth = parentDepth + 1
	}

	var subtreeHeight int
	if err := tx.QueryRowContext(ctx, `
		WITH RECURSIVE subtree(id, relative_depth) AS (
			SELECT id, 0 FROM sessions WHERE id = ?
			UNION ALL
			SELECT child.id, parent.relative_depth + 1
			FROM sessions child JOIN subtree parent ON child.parent_session_id = parent.id
		)
		SELECT COALESCE(MAX(relative_depth), 0) FROM subtree`, sessionID).Scan(&subtreeHeight); err != nil {
		return Session{}, fmt.Errorf("measure session subtree: %w", err)
	}
	if params.MaxLineageDepth > 0 && newDepth+subtreeHeight > params.MaxLineageDepth {
		return Session{}, fmt.Errorf("%w: maximum child depth %d exceeded", ErrInvalidArgument, params.MaxLineageDepth)
	}

	depthDelta := newDepth - currentDepth
	if _, err := tx.ExecContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
			SELECT id FROM sessions WHERE id = ?
			UNION ALL
			SELECT child.id FROM sessions child JOIN subtree parent ON child.parent_session_id = parent.id
		)
		UPDATE sessions
		SET parent_session_id = CASE WHEN id = ? THEN ? ELSE parent_session_id END,
		    lineage_depth = lineage_depth + ?,
		    updated_at = CASE WHEN id = ? THEN ? ELSE updated_at END
		WHERE id IN (SELECT id FROM subtree)`,
		sessionID,
		sessionID,
		nullIfEmpty(parentSessionID),
		depthDelta,
		sessionID,
		formatTime(s.now()),
	); err != nil {
		return Session{}, fmt.Errorf("update session parent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit update session parent: %w", err)
	}

	return s.GetSession(ctx, sessionID)
}

func (s *Store) UpdateSessionWorkspace(ctx context.Context, params UpdateSessionWorkspaceParams) (Session, error) {
	sessionID := strings.TrimSpace(params.ID)
	if sessionID == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalidArgument)
	}
	workspacePath := strings.TrimSpace(params.WorkspacePath)
	if workspacePath == "" {
		return Session{}, fmt.Errorf("%w: workspace_path is required", ErrInvalidArgument)
	}

	now := s.now()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE sessions
		 SET workspace_path = ?, updated_at = ?
		 WHERE id = ?`,
		workspacePath,
		formatTime(now),
		sessionID,
	)
	if err != nil {
		return Session{}, fmt.Errorf("update session workspace: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return Session{}, fmt.Errorf("check updated session workspace rows: %w", err)
	}
	if rowsAffected == 0 {
		return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, sessionID)
	}

	return s.GetSession(ctx, sessionID)
}

func (s *Store) UpdateSessionAgentOptions(ctx context.Context, params UpdateSessionAgentOptionsParams) (Session, error) {
	sessionID := strings.TrimSpace(params.ID)
	if sessionID == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalidArgument)
	}
	agentOptions := params.AgentOptions
	if len(agentOptions) == 0 {
		agentOptions = json.RawMessage(`{}`)
	}
	if !json.Valid(agentOptions) {
		return Session{}, fmt.Errorf("%w: agent_options must be valid JSON", ErrInvalidArgument)
	}

	now := s.now()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE sessions
		 SET agent_options_json = ?, updated_at = ?
		 WHERE id = ?`,
		string(agentOptions),
		formatTime(now),
		sessionID,
	)
	if err != nil {
		return Session{}, fmt.Errorf("update session agent_options: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return Session{}, fmt.Errorf("check updated session agent_options rows: %w", err)
	}
	if rowsAffected == 0 {
		return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, sessionID)
	}

	return s.GetSession(ctx, sessionID)
}

func (s *Store) ArchiveSession(ctx context.Context, params ArchiveSessionParams) (Session, error) {
	if strings.TrimSpace(params.ID) == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalidArgument)
	}

	now := s.now()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE sessions
		 SET archived_at = COALESCE(archived_at, ?), pinned_at = NULL, updated_at = ?
		 WHERE id = ?`,
		formatTime(now),
		formatTime(now),
		params.ID,
	)
	if err != nil {
		return Session{}, fmt.Errorf("archive session: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return Session{}, fmt.Errorf("check archived session rows: %w", err)
	}
	if rowsAffected == 0 {
		return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, params.ID)
	}

	return s.GetSession(ctx, params.ID)
}

func (s *Store) UpdateSessionPin(ctx context.Context, params UpdateSessionPinParams) (Session, error) {
	if strings.TrimSpace(params.ID) == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalidArgument)
	}

	var (
		result sql.Result
		err    error
	)
	if params.Pinned {
		result, err = s.db.ExecContext(
			ctx,
			`UPDATE sessions SET pinned_at = ? WHERE id = ? AND archived_at IS NULL`,
			formatTime(s.now()),
			params.ID,
		)
	} else {
		result, err = s.db.ExecContext(ctx, `UPDATE sessions SET pinned_at = NULL WHERE id = ?`, params.ID)
	}
	if err != nil {
		return Session{}, fmt.Errorf("update session pin: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return Session{}, fmt.Errorf("check updated session pin rows: %w", err)
	}
	if rowsAffected == 0 {
		session, getErr := s.GetSession(ctx, params.ID)
		if errors.Is(getErr, ErrNotFound) {
			return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, params.ID)
		}
		if getErr != nil {
			return Session{}, getErr
		}
		if params.Pinned && session.ArchivedAt != nil {
			return Session{}, fmt.Errorf("%w: archived session cannot be pinned", ErrInvalidArgument)
		}
	}

	return s.GetSession(ctx, params.ID)
}

func (s *Store) RestoreSession(ctx context.Context, params RestoreSessionParams) (Session, error) {
	if strings.TrimSpace(params.ID) == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalidArgument)
	}

	now := s.now()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE sessions
		 SET archived_at = NULL, updated_at = CASE WHEN archived_at IS NOT NULL THEN ? ELSE updated_at END
		 WHERE id = ?`,
		formatTime(now),
		params.ID,
	)
	if err != nil {
		return Session{}, fmt.Errorf("restore session: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return Session{}, fmt.Errorf("check restored session rows: %w", err)
	}
	if rowsAffected == 0 {
		return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, params.ID)
	}

	return s.GetSession(ctx, params.ID)
}

func (s *Store) SetSessionProviderSessionID(ctx context.Context, params SetSessionProviderSessionIDParams) (Session, error) {
	sessionID := strings.TrimSpace(params.ID)
	providerSessionID := strings.TrimSpace(params.ProviderSessionID)
	if sessionID == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalidArgument)
	}
	if providerSessionID == "" {
		return Session{}, fmt.Errorf("%w: provider_session_id is required", ErrInvalidArgument)
	}

	session, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	if session.ProviderSessionID != "" {
		if session.ProviderSessionID != providerSessionID {
			if params.Replace {
				return s.updateSessionProviderSessionID(ctx, sessionID, providerSessionID)
			}
			return Session{}, fmt.Errorf("%w: provider_session_id already set for session %s", ErrInvalidArgument, sessionID)
		}
		return session, nil
	}

	return s.updateSessionProviderSessionID(ctx, sessionID, providerSessionID)
}

func (s *Store) updateSessionProviderSessionID(ctx context.Context, sessionID string, providerSessionID string) (Session, error) {
	now := s.now()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE sessions
		 SET provider_session_id = ?, updated_at = ?
		 WHERE id = ?`,
		providerSessionID,
		formatTime(now),
		sessionID,
	)
	if err != nil {
		return Session{}, fmt.Errorf("set session provider_session_id: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return Session{}, fmt.Errorf("check session provider_session_id rows: %w", err)
	}
	if rowsAffected == 0 {
		return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, sessionID)
	}

	return s.GetSession(ctx, sessionID)
}

func (s *Store) ClearSessionProviderSessionID(ctx context.Context, params ClearSessionProviderSessionIDParams) (Session, error) {
	sessionID := strings.TrimSpace(params.ID)
	if sessionID == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalidArgument)
	}

	now := s.now()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE sessions
		 SET provider_session_id = NULL, updated_at = ?
		 WHERE id = ?`,
		formatTime(now),
		sessionID,
	)
	if err != nil {
		return Session{}, fmt.Errorf("clear session provider_session_id: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return Session{}, fmt.Errorf("check clear session provider_session_id rows: %w", err)
	}
	if rowsAffected == 0 {
		return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, sessionID)
	}

	return s.GetSession(ctx, sessionID)
}

func (s *Store) UpdateSessionStatus(ctx context.Context, params UpdateSessionStatusParams) (Session, error) {
	if strings.TrimSpace(params.ID) == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalidArgument)
	}
	if strings.TrimSpace(string(params.Status)) == "" {
		return Session{}, fmt.Errorf("%w: status is required", ErrInvalidArgument)
	}
	if !isValidSessionStatus(params.Status) {
		return Session{}, fmt.Errorf("%w: unsupported status %s", ErrInvalidArgument, params.Status)
	}

	now := s.now()
	var completedAt any
	if isTerminalSessionStatus(params.Status) {
		completedAt = formatTime(now)
	}

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE sessions
		 SET status = ?, updated_at = ?, completed_at = ?
		 WHERE id = ?`,
		string(params.Status),
		formatTime(now),
		completedAt,
		params.ID,
	)
	if err != nil {
		return Session{}, fmt.Errorf("update session status: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return Session{}, fmt.Errorf("check updated session status rows: %w", err)
	}
	if rowsAffected == 0 {
		return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, params.ID)
	}

	return s.GetSession(ctx, params.ID)
}

func (s *Store) ReserveEventSequences(ctx context.Context, sessionID string, count int64) (int64, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, fmt.Errorf("%w: session_id is required", ErrInvalidArgument)
	}
	if count <= 0 {
		return 0, fmt.Errorf("%w: sequence reservation count must be positive", ErrInvalidArgument)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin reserve event sequences: %w", err)
	}
	defer rollback(tx)

	var firstSeq int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT next_event_seq FROM sessions WHERE id = ?`,
		sessionID,
	).Scan(&firstSeq); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("%w: session %s", ErrNotFound, sessionID)
		}
		return 0, fmt.Errorf("read next event sequence: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE sessions SET next_event_seq = next_event_seq + ? WHERE id = ?`,
		count,
		sessionID,
	); err != nil {
		return 0, fmt.Errorf("reserve event sequences: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit reserve event sequences: %w", err)
	}

	return firstSeq, nil
}

func (s *Store) AppendEvent(ctx context.Context, params AppendEventParams) (Event, error) {
	if strings.TrimSpace(params.SessionID) == "" {
		return Event{}, fmt.Errorf("%w: session_id is required", ErrInvalidArgument)
	}
	if strings.TrimSpace(params.Type) == "" {
		return Event{}, fmt.Errorf("%w: type is required", ErrInvalidArgument)
	}
	if strings.TrimSpace(string(params.Status)) == "" {
		return Event{}, fmt.Errorf("%w: status is required", ErrInvalidArgument)
	}
	if !json.Valid(params.Payload) {
		return Event{}, fmt.Errorf("%w: payload must be valid JSON", ErrInvalidArgument)
	}

	if params.Seq <= 0 {
		seq, err := s.ReserveEventSequences(ctx, params.SessionID, 1)
		if err != nil {
			return Event{}, err
		}
		params.Seq = seq
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("begin append event: %w", err)
	}
	defer rollback(tx)

	var exists int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sessions WHERE id = ?`,
		params.SessionID,
	).Scan(&exists); err != nil {
		return Event{}, fmt.Errorf("check session: %w", err)
	}
	if exists == 0 {
		return Event{}, fmt.Errorf("%w: session %s", ErrNotFound, params.SessionID)
	}

	payload := append(json.RawMessage(nil), params.Payload...)
	var sessionTotalTokens *int64
	if usage, ok := normalizedSessionTokenUsage(payload); ok {
		sessionTotal, err := upsertSessionTokenUsage(ctx, tx, params.SessionID, params.Seq, usage)
		if err != nil {
			return Event{}, err
		}
		sessionTotalTokens = &sessionTotal
		payload, err = payloadWithSessionTotalTokens(payload, sessionTotal)
		if err != nil {
			return Event{}, err
		}
	}

	id, err := NewEventID()
	if err != nil {
		return Event{}, err
	}

	projectionPayload := append(json.RawMessage(nil), payload...)
	compactPayload, blobs, err := prepareEventPayload(params.Type, payload)
	if err != nil {
		return Event{}, err
	}
	event := Event{
		ID:        id,
		SessionID: params.SessionID,
		Seq:       params.Seq,
		Type:      params.Type,
		Role:      params.Role,
		Status:    params.Status,
		Payload:   compactPayload,
		CreatedAt: s.now(),
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO events (id, session_id, seq, type, role, status, payload_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.SessionID,
		event.Seq,
		event.Type,
		event.Role,
		string(event.Status),
		string(event.Payload),
		formatTime(event.CreatedAt),
	); err != nil {
		return Event{}, fmt.Errorf("insert event: %w", err)
	}
	if !strings.HasSuffix(event.Type, ".delta") {
		result, err := tx.ExecContext(
			ctx,
			`INSERT INTO global_event_stream (event_id) VALUES (?)`,
			event.ID,
		)
		if err != nil {
			return Event{}, fmt.Errorf("index global event: %w", err)
		}
		event.GlobalSeq, err = result.LastInsertId()
		if err != nil {
			return Event{}, fmt.Errorf("read global event sequence: %w", err)
		}
	}
	for index := range blobs {
		blobs[index].EventID = event.ID
		blobs[index].CreatedAt = event.CreatedAt
		if err := insertEventBlob(ctx, tx, blobs[index]); err != nil {
			return Event{}, err
		}
	}
	if err := updateSessionEventSummary(ctx, tx, event, sessionTotalTokens); err != nil {
		return Event{}, err
	}

	projectionEvent := event
	projectionEvent.Payload = projectionPayload
	if err := projectDashboardEvent(ctx, tx, projectionEvent); err != nil {
		return Event{}, err
	}
	if err := projectSearchEvent(ctx, tx, projectionEvent); err != nil {
		return Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit append event: %w", err)
	}

	return event, nil
}

func insertEventBlob(ctx context.Context, tx *sql.Tx, blob EventBlob) error {
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO event_blobs
		 (event_id, kind, item_index, name, media_type, encoding, original_bytes, data, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		blob.EventID,
		blob.Kind,
		blob.ItemIndex,
		blob.Name,
		blob.MediaType,
		blob.Encoding,
		blob.OriginalBytes,
		blob.Data,
		formatTime(blob.CreatedAt),
	); err != nil {
		return fmt.Errorf("insert event blob: %w", err)
	}
	return nil
}

// IsSessionActivityEventType excludes session metadata and provider debug traffic.
func IsSessionActivityEventType(eventType string) bool {
	return strings.HasPrefix(eventType, "user.") || strings.HasPrefix(eventType, "agent.") ||
		strings.HasPrefix(eventType, "tool.") || strings.HasPrefix(eventType, "file.change.")
}

func updateSessionEventSummary(ctx context.Context, tx *sql.Tx, event Event, sessionTotalTokens *int64) error {
	durableIncrement := 1
	if strings.HasSuffix(event.Type, ".delta") {
		durableIncrement = 0
	}
	toolIncrement := 0
	if event.Type == "tool.call.started" || event.Type == "file.change.started" {
		toolIncrement = 1
	}

	pendingInputDelta := 0
	pendingPermissionDelta := 0
	resetPending := false
	switch event.Type {
	case "agent.input.requested":
		pendingInputDelta = 1
	case "agent.input.answered", "agent.input.failed", "agent.input.cancelled":
		delta, err := controlResolutionDelta(ctx, tx, event)
		if err != nil {
			return err
		}
		pendingInputDelta = delta
	case "agent.permission.requested":
		pendingPermissionDelta = 1
	case "agent.permission.resolved", "agent.permission.cancelled":
		delta, err := controlResolutionDelta(ctx, tx, event)
		if err != nil {
			return err
		}
		pendingPermissionDelta = delta
	case "agent.run.completed", "agent.run.failed", "agent.run.cancelled":
		resetPending = true
	}

	tokenCount := int64(-1)
	if sessionTotalTokens != nil {
		tokenCount = *sessionTotalTokens
	}
	var activityAt any
	if IsSessionActivityEventType(event.Type) {
		activityAt = formatTime(event.CreatedAt)
	}
	resetPendingInt := 0
	if resetPending {
		resetPendingInt = 1
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE sessions
		 SET last_activity_at = COALESCE(?, last_activity_at),
		     durable_event_count = durable_event_count + ?,
		     last_durable_event_seq = CASE WHEN ? = 1 THEN MAX(last_durable_event_seq, ?) ELSE last_durable_event_seq END,
		     materialized_tool_count = materialized_tool_count + ?,
		     materialized_token_count = CASE WHEN ? >= 0 THEN ? ELSE materialized_token_count END,
		     pending_input_count = CASE WHEN ? = 1 THEN 0 ELSE MAX(0, pending_input_count + ?) END,
		     pending_permission_count = CASE WHEN ? = 1 THEN 0 ELSE MAX(0, pending_permission_count + ?) END
		 WHERE id = ?`,
		activityAt,
		durableIncrement,
		durableIncrement,
		event.Seq,
		toolIncrement,
		tokenCount,
		tokenCount,
		resetPendingInt,
		pendingInputDelta,
		resetPendingInt,
		pendingPermissionDelta,
		event.SessionID,
	); err != nil {
		return fmt.Errorf("update session event summary: %w", err)
	}
	return nil
}

type sessionTokenUsage struct {
	Provider    string
	ContextID   string
	TotalTokens int64
}

func normalizedSessionTokenUsage(payload json.RawMessage) (sessionTokenUsage, bool) {
	var value struct {
		Provider string `json:"provider"`
		Usage    *struct {
			ContextID   string `json:"context_id"`
			TotalTokens int64  `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &value); err != nil || value.Usage == nil {
		return sessionTokenUsage{}, false
	}
	usage := sessionTokenUsage{
		Provider:    strings.TrimSpace(value.Provider),
		ContextID:   strings.TrimSpace(value.Usage.ContextID),
		TotalTokens: value.Usage.TotalTokens,
	}
	if usage.Provider == "" || usage.ContextID == "" || usage.TotalTokens <= 0 {
		return sessionTokenUsage{}, false
	}
	return usage, true
}

func upsertSessionTokenUsage(
	ctx context.Context,
	tx *sql.Tx,
	sessionID string,
	eventSeq int64,
	usage sessionTokenUsage,
) (int64, error) {
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO session_token_usage (session_id, provider, context_id, token_count, last_event_seq)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, provider, context_id) DO UPDATE SET
		   token_count = MAX(token_count, excluded.token_count),
		   last_event_seq = MAX(last_event_seq, excluded.last_event_seq)`,
		sessionID,
		usage.Provider,
		usage.ContextID,
		usage.TotalTokens,
		eventSeq,
	); err != nil {
		return 0, fmt.Errorf("upsert session token usage: %w", err)
	}

	var sessionTotal int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(SUM(token_count), 0) FROM session_token_usage WHERE session_id = ?`,
		sessionID,
	).Scan(&sessionTotal); err != nil {
		return 0, fmt.Errorf("sum session token usage: %w", err)
	}
	return sessionTotal, nil
}

func payloadWithSessionTotalTokens(payload json.RawMessage, sessionTotal int64) (json.RawMessage, error) {
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, fmt.Errorf("decode token usage payload: %w", err)
	}
	value["session_total_tokens"] = sessionTotal
	updated, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode token usage payload: %w", err)
	}
	return updated, nil
}

func (s *Store) EnqueueMessage(ctx context.Context, params EnqueueMessageParams) (QueuedMessage, error) {
	sessionID := strings.TrimSpace(params.SessionID)
	content := strings.TrimSpace(params.Content)
	if sessionID == "" {
		return QueuedMessage{}, fmt.Errorf("%w: session_id is required", ErrInvalidArgument)
	}
	agentOptions := params.AgentOptions
	if len(agentOptions) == 0 {
		agentOptions = json.RawMessage(`{}`)
	}
	if !json.Valid(agentOptions) {
		return QueuedMessage{}, fmt.Errorf("%w: agent_options must be valid JSON", ErrInvalidArgument)
	}
	skills := params.Skills
	if len(skills) == 0 {
		skills = json.RawMessage(`[]`)
	}
	if !json.Valid(skills) {
		return QueuedMessage{}, fmt.Errorf("%w: skills must be valid JSON", ErrInvalidArgument)
	}
	var skillValues []any
	if err := json.Unmarshal(skills, &skillValues); err != nil {
		return QueuedMessage{}, fmt.Errorf("%w: skills must be a JSON array", ErrInvalidArgument)
	}
	if content == "" && len(skillValues) == 0 {
		return QueuedMessage{}, fmt.Errorf("%w: content or skills are required", ErrInvalidArgument)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QueuedMessage{}, fmt.Errorf("begin enqueue message: %w", err)
	}
	defer rollback(tx)

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		return QueuedMessage{}, fmt.Errorf("check session: %w", err)
	}
	if exists == 0 {
		return QueuedMessage{}, fmt.Errorf("%w: session %s", ErrNotFound, sessionID)
	}

	if params.MaxPending > 0 {
		var pendingCount int
		if err := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM queued_messages WHERE session_id = ? AND status = ? AND source_kind = 'manual'`,
			sessionID,
			string(QueuedMessageStatusPending),
		).Scan(&pendingCount); err != nil {
			return QueuedMessage{}, fmt.Errorf("count pending queued messages: %w", err)
		}
		if pendingCount >= params.MaxPending {
			return QueuedMessage{}, fmt.Errorf("%w: queue limit reached", ErrInvalidArgument)
		}
	}

	var seq int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM queued_messages WHERE session_id = ?`,
		sessionID,
	).Scan(&seq); err != nil {
		return QueuedMessage{}, fmt.Errorf("assign queued message sequence: %w", err)
	}

	id, err := newPrefixedUUID("qmsg_")
	if err != nil {
		return QueuedMessage{}, err
	}
	now := s.now()
	queued := QueuedMessage{
		ID:           id,
		SessionID:    sessionID,
		Seq:          seq,
		Status:       QueuedMessageStatusPending,
		Content:      content,
		AgentOptions: append(json.RawMessage(nil), agentOptions...),
		Skills:       append(json.RawMessage(nil), skills...),
		SourceKind:   "manual",
		SourceID:     params.ClientSubmissionID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO queued_messages (id, session_id, seq, status, content, agent_options_json, skills_json, source_kind, source_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		queued.ID,
		queued.SessionID,
		queued.Seq,
		string(queued.Status),
		queued.Content,
		string(queued.AgentOptions),
		string(queued.Skills),
		queued.SourceKind,
		queued.SourceID,
		formatTime(queued.CreatedAt),
		formatTime(queued.UpdatedAt),
	); err != nil {
		return QueuedMessage{}, fmt.Errorf("insert queued message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return QueuedMessage{}, fmt.Errorf("commit enqueue message: %w", err)
	}

	return queued, nil
}

func (s *Store) ListQueuedMessages(ctx context.Context, sessionID string) ([]QueuedMessage, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session_id is required", ErrInvalidArgument)
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, session_id, seq, status, content, agent_options_json, skills_json, source_kind, source_id, created_at, updated_at
		 FROM queued_messages
		 WHERE session_id = ? AND status = ? AND source_kind = 'manual'
		 ORDER BY seq ASC`,
		sessionID,
		string(QueuedMessageStatusPending),
	)
	if err != nil {
		return nil, fmt.Errorf("list queued messages: %w", err)
	}
	defer rows.Close()

	messages := make([]QueuedMessage, 0)
	for rows.Next() {
		message, err := scanQueuedMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list queued messages rows: %w", err)
	}

	return messages, nil
}

func (s *Store) RemoveQueuedMessage(ctx context.Context, params QueueMessageIDParams) (QueuedMessage, error) {
	return s.updateQueuedMessageStatus(ctx, params, QueuedMessageStatusRemoved, QueuedMessageStatusPending)
}

func (s *Store) ClaimNextQueuedMessage(ctx context.Context, sessionID string) (QueuedMessage, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return QueuedMessage{}, fmt.Errorf("%w: session_id is required", ErrInvalidArgument)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QueuedMessage{}, fmt.Errorf("begin claim queued message: %w", err)
	}
	defer rollback(tx)

	row := tx.QueryRowContext(
		ctx,
		`SELECT id, session_id, seq, status, content, agent_options_json, skills_json, source_kind, source_id, created_at, updated_at
		 FROM queued_messages
		 WHERE session_id = ? AND status = ?
		 ORDER BY CASE source_kind WHEN 'manual' THEN 0 ELSE 1 END, seq ASC
		 LIMIT 1`,
		sessionID,
		string(QueuedMessageStatusPending),
	)
	message, err := scanQueuedMessage(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
			return QueuedMessage{}, ErrNotFound
		}
		return QueuedMessage{}, err
	}

	now := s.now()
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE queued_messages SET status = ?, updated_at = ? WHERE id = ?`,
		string(QueuedMessageStatusSending),
		formatTime(now),
		message.ID,
	); err != nil {
		return QueuedMessage{}, fmt.Errorf("claim queued message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return QueuedMessage{}, fmt.Errorf("commit claim queued message: %w", err)
	}
	message.Status = QueuedMessageStatusSending
	message.UpdatedAt = now
	return message, nil
}

func (s *Store) MarkQueuedMessageSent(ctx context.Context, params QueueMessageIDParams) (QueuedMessage, error) {
	return s.updateQueuedMessageStatus(ctx, params, QueuedMessageStatusSent, QueuedMessageStatusSending)
}

func (s *Store) ReleaseQueuedMessage(ctx context.Context, params QueueMessageIDParams) (QueuedMessage, error) {
	return s.updateQueuedMessageStatus(ctx, params, QueuedMessageStatusPending, QueuedMessageStatusSending)
}

func (s *Store) updateQueuedMessageStatus(ctx context.Context, params QueueMessageIDParams, next QueuedMessageStatus, expected QueuedMessageStatus) (QueuedMessage, error) {
	sessionID := strings.TrimSpace(params.SessionID)
	messageID := strings.TrimSpace(params.ID)
	if sessionID == "" {
		return QueuedMessage{}, fmt.Errorf("%w: session_id is required", ErrInvalidArgument)
	}
	if messageID == "" {
		return QueuedMessage{}, fmt.Errorf("%w: queued message id is required", ErrInvalidArgument)
	}

	current, err := s.getQueuedMessage(ctx, sessionID, messageID)
	if err != nil {
		return QueuedMessage{}, err
	}
	if current.Status != expected {
		return QueuedMessage{}, fmt.Errorf("%w: queued message is %s", ErrInvalidArgument, current.Status)
	}

	now := s.now()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE queued_messages
		 SET status = ?, updated_at = ?
		 WHERE session_id = ? AND id = ? AND status = ?`,
		string(next),
		formatTime(now),
		sessionID,
		messageID,
		string(expected),
	)
	if err != nil {
		return QueuedMessage{}, fmt.Errorf("update queued message status: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return QueuedMessage{}, fmt.Errorf("check queued message update rows: %w", err)
	}
	if rowsAffected == 0 {
		return QueuedMessage{}, fmt.Errorf("%w: queued message %s", ErrNotFound, messageID)
	}

	current.Status = next
	current.UpdatedAt = now
	return current, nil
}

func (s *Store) getQueuedMessage(ctx context.Context, sessionID string, messageID string) (QueuedMessage, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, session_id, seq, status, content, agent_options_json, skills_json, source_kind, source_id, created_at, updated_at
		 FROM queued_messages
		 WHERE session_id = ? AND id = ?`,
		sessionID,
		messageID,
	)
	message, err := scanQueuedMessage(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
			return QueuedMessage{}, fmt.Errorf("%w: queued message %s", ErrNotFound, messageID)
		}
		return QueuedMessage{}, err
	}
	return message, nil
}

func (s *Store) ListEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = defaultEventLimit
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, session_id, seq, type, role, status, payload_json, created_at
		 FROM events
		 WHERE session_id = ? AND seq > ? AND `+durableEventSQL+`
		 ORDER BY seq ASC
		 LIMIT ?`,
		sessionID,
		afterSeq,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events rows: %w", err)
	}

	return events, nil
}

func (s *Store) ListEventsFiltered(
	ctx context.Context,
	sessionID string,
	afterSeq int64,
	limit int,
	filter EventListFilter,
) ([]Event, error) {
	if filter.IncludeDebug {
		return s.ListEvents(ctx, sessionID, afterSeq, limit)
	}
	if limit <= 0 {
		limit = defaultEventLimit
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, session_id, seq, type, role, status, payload_json, created_at
		 FROM events
		 WHERE session_id = ? AND seq > ? AND `+durableEventSQL+` `+nonDebugEventSQL()+`
		 ORDER BY seq ASC
		 LIMIT ?`,
		sessionID,
		afterSeq,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list filtered events: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list filtered events rows: %w", err)
	}

	return events, nil
}

// GlobalEventCursor returns the latest server-wide durable event sequence. The
// AUTOINCREMENT counter is intentionally used instead of MAX(global_seq): event
// maintenance may delete the newest indexed event, but a previously issued
// cursor must never move backwards.
func (s *Store) GlobalEventCursor(ctx context.Context) (int64, error) {
	var cursor int64
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'global_event_stream'), 0)`,
	).Scan(&cursor); err != nil {
		return 0, fmt.Errorf("read global event cursor: %w", err)
	}
	return cursor, nil
}

func (s *Store) ListGlobalEventsFiltered(
	ctx context.Context,
	afterCursor int64,
	limit int,
	filter EventListFilter,
) ([]Event, error) {
	if limit <= 0 {
		limit = defaultEventLimit
	}

	query := `SELECT e.id, e.session_id, e.seq, e.type, e.role, e.status, e.payload_json, e.created_at,
			g.global_seq
		 FROM global_event_stream AS g
		 JOIN events AS e ON e.id = g.event_id
		 WHERE g.global_seq > ? AND ` + durableEventSQL
	if !filter.IncludeDebug {
		query += ` ` + nonDebugEventSQL()
	}
	query += ` ORDER BY g.global_seq ASC LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, afterCursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list global events: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0)
	for rows.Next() {
		event, err := scanGlobalEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list global event rows: %w", err)
	}
	return events, nil
}

func (s *Store) GetEvent(ctx context.Context, sessionID string, seq int64) (Event, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, session_id, seq, type, role, status, payload_json, created_at
		 FROM events
		 WHERE session_id = ? AND seq = ? AND `+durableEventSQL,
		sessionID,
		seq,
	)
	event, err := scanEvent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
			return Event{}, fmt.Errorf("%w: event %s/%d", ErrNotFound, sessionID, seq)
		}
		return Event{}, err
	}
	return event, nil
}

func (s *Store) ListRecentEvents(ctx context.Context, sessionID string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = defaultEventLimit
	}

	events, err := s.listEventsDescending(ctx, sessionID, ``, 0, limit)
	if err != nil {
		return nil, err
	}
	reverseEvents(events)
	return events, nil
}

func (s *Store) ListRecentEventsFiltered(
	ctx context.Context,
	sessionID string,
	limit int,
	filter EventListFilter,
) ([]Event, error) {
	if filter.IncludeDebug {
		return s.ListRecentEvents(ctx, sessionID, limit)
	}
	if limit <= 0 {
		limit = defaultEventLimit
	}

	events, err := s.listEventsDescending(ctx, sessionID, nonDebugEventSQL(), 0, limit)
	if err != nil {
		return nil, err
	}
	reverseEvents(events)
	return events, nil
}

func (s *Store) ListEventsBefore(ctx context.Context, sessionID string, beforeSeq int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = defaultEventLimit
	}

	events, err := s.listEventsDescending(ctx, sessionID, `AND seq < ?`, beforeSeq, limit)
	if err != nil {
		return nil, err
	}
	reverseEvents(events)
	return events, nil
}

func (s *Store) ListEventsBeforeFiltered(
	ctx context.Context,
	sessionID string,
	beforeSeq int64,
	limit int,
	filter EventListFilter,
) ([]Event, error) {
	if filter.IncludeDebug {
		return s.ListEventsBefore(ctx, sessionID, beforeSeq, limit)
	}
	if limit <= 0 {
		limit = defaultEventLimit
	}

	events, err := s.listEventsDescending(ctx, sessionID, `AND seq < ? `+nonDebugEventSQL(), beforeSeq, limit)
	if err != nil {
		return nil, err
	}
	reverseEvents(events)
	return events, nil
}

func (s *Store) ListRecentEventTurnsFiltered(
	ctx context.Context,
	sessionID string,
	turns int,
	filter EventListFilter,
) ([]Event, error) {
	return s.ListRecentEventTurnsPageFiltered(ctx, sessionID, turns, 0, filter)
}

func (s *Store) ListRecentEventTurnsPageFiltered(
	ctx context.Context,
	sessionID string,
	turns int,
	limit int,
	filter EventListFilter,
) ([]Event, error) {
	startSeq, found, err := s.eventTurnStartSeq(ctx, sessionID, nil, turns)
	if err != nil {
		return nil, err
	}
	if !found {
		startSeq = 0
	}
	return s.listEventTurnRange(ctx, sessionID, startSeq, nil, limit, true, filter)
}

func (s *Store) ListEventTurnsBeforeFiltered(
	ctx context.Context,
	sessionID string,
	beforeSeq int64,
	turns int,
	filter EventListFilter,
) ([]Event, error) {
	return s.ListEventTurnsBeforePageFiltered(ctx, sessionID, beforeSeq, turns, 0, filter)
}

func (s *Store) ListEventTurnsBeforePageFiltered(
	ctx context.Context,
	sessionID string,
	beforeSeq int64,
	turns int,
	limit int,
	filter EventListFilter,
) ([]Event, error) {
	startSeq, found, err := s.eventTurnStartSeq(ctx, sessionID, &beforeSeq, turns)
	if err != nil {
		return nil, err
	}
	if !found {
		startSeq = 0
	}
	return s.listEventTurnRange(ctx, sessionID, startSeq, &beforeSeq, limit, true, filter)
}

func (s *Store) ListEventTurnsAfterFiltered(
	ctx context.Context,
	sessionID string,
	afterSeq int64,
	turns int,
	limit int,
	filter EventListFilter,
) ([]Event, error) {
	endSeq, found, err := s.eventTurnEndSeq(ctx, sessionID, afterSeq, turns)
	if err != nil {
		return nil, err
	}
	var beforeSeq *int64
	if found {
		beforeSeq = &endSeq
	}
	return s.listEventTurnRange(ctx, sessionID, afterSeq+1, beforeSeq, limit, false, filter)
}

func (s *Store) eventTurnStartSeq(ctx context.Context, sessionID string, beforeSeq *int64, turns int) (int64, bool, error) {
	query := `SELECT seq FROM events WHERE session_id = ? AND type = 'user.message.completed'`
	args := []any{sessionID}
	if beforeSeq != nil {
		query += ` AND seq < ?`
		args = append(args, *beforeSeq)
	}
	query += ` ORDER BY seq DESC LIMIT 1 OFFSET ?`
	args = append(args, turns-1)

	var seq int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&seq); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("find event turn boundary: %w", err)
	}
	return seq, true, nil
}

func (s *Store) eventTurnEndSeq(ctx context.Context, sessionID string, afterSeq int64, turns int) (int64, bool, error) {
	query := `SELECT seq FROM events
		WHERE session_id = ? AND type = 'user.message.completed' AND seq > ?
		ORDER BY seq ASC LIMIT 1 OFFSET ?`
	var seq int64
	if err := s.db.QueryRowContext(ctx, query, sessionID, afterSeq, turns).Scan(&seq); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("find event turn end boundary: %w", err)
	}
	return seq, true, nil
}

func (s *Store) listEventTurnRange(
	ctx context.Context,
	sessionID string,
	startSeq int64,
	beforeSeq *int64,
	limit int,
	preferLatest bool,
	filter EventListFilter,
) ([]Event, error) {
	query := `SELECT id, session_id, seq, type, role, status, payload_json, created_at
		 FROM events
		 WHERE session_id = ? AND ` + durableEventSQL
	args := []any{sessionID}
	if startSeq > 0 {
		query += ` AND seq >= ?`
		args = append(args, startSeq)
	}
	if beforeSeq != nil {
		query += ` AND seq < ?`
		args = append(args, *beforeSeq)
	}
	if !filter.IncludeDebug {
		query += ` ` + nonDebugEventSQL()
	}
	if preferLatest && limit > 0 {
		query += ` ORDER BY seq DESC LIMIT ?`
		args = append(args, limit)
	} else {
		query += ` ORDER BY seq ASC`
		if limit > 0 {
			query += ` LIMIT ?`
			args = append(args, limit)
		}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list event turns: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list event turns rows: %w", err)
	}
	if preferLatest && limit > 0 {
		reverseEvents(events)
	}
	return events, nil
}

func (s *Store) listEventsDescending(ctx context.Context, sessionID string, extraWhere string, seqBound int64, limit int) ([]Event, error) {
	query := `SELECT id, session_id, seq, type, role, status, payload_json, created_at
		 FROM events
		 WHERE session_id = ? AND ` + durableEventSQL + ` ` + extraWhere + `
		 ORDER BY seq DESC
		 LIMIT ?`
	args := []any{sessionID}
	if strings.Contains(extraWhere, "?") {
		args = append(args, seqBound)
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list events descending: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events descending rows: %w", err)
	}

	return events, nil
}

func nonDebugEventSQL() string {
	return `AND (
		type NOT IN (
			'agent.log.delta',
			'provider.codex.request',
			'provider.codex.parse_error',
			'provider.claude.parse_error',
			'provider.opencode.request',
			'provider.opencode.parse_error',
			'provider.pi.parse_error'
		)
		AND (
			type NOT IN (
				'provider.codex.event',
				'provider.claude.event',
				'provider.opencode.event',
				'provider.pi.event'
			)
			OR (
				type = 'provider.codex.event'
				AND (
					json_extract(payload_json, '$.provider_event_type') = 'thread/tokenUsage/updated'
					OR json_extract(payload_json, '$.provider_event_type') = 'item/plan/delta'
					OR (
						json_extract(payload_json, '$.provider_event_type') = 'item/completed'
						AND json_extract(payload_json, '$.raw.item.type') = 'plan'
					)
				)
			)
			OR (
				type = 'provider.claude.event'
				AND json_type(payload_json, '$.usage') IS NOT NULL
			)
			OR (
				type = 'provider.opencode.event'
				AND json_extract(payload_json, '$.provider_event_type') = 'usage_update'
			)
		)
	)`
}

func reverseEvents(events []Event) {
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(row rowScanner) (Session, error) {
	var session Session
	var status string
	var providerSessionID sql.NullString
	var workspacePath sql.NullString
	var agentOptions string
	var eventCount int64
	var lastEventSeq int64
	var toolCount int64
	var tokenCount int64
	var pendingInputCount int64
	var pendingPermissionCount int64
	var notificationAttentionSeq int64
	var createdAt string
	var updatedAt string
	var lastActivityAt sql.NullString
	var completedAt sql.NullString
	var archivedAt sql.NullString
	var pinnedAt sql.NullString
	var parentSessionID sql.NullString
	var spawnedByRunID sql.NullString
	var lineageDepth int
	var childCount int

	if err := row.Scan(
		&session.ID,
		&session.Title,
		&session.AgentType,
		&status,
		&providerSessionID,
		&workspacePath,
		&agentOptions,
		&eventCount,
		&lastEventSeq,
		&toolCount,
		&tokenCount,
		&pendingInputCount,
		&pendingPermissionCount,
		&notificationAttentionSeq,
		&createdAt,
		&updatedAt,
		&lastActivityAt,
		&completedAt,
		&archivedAt,
		&pinnedAt,
		&parentSessionID,
		&spawnedByRunID,
		&lineageDepth,
		&childCount,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, fmt.Errorf("scan session: %w", err)
	}

	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return Session{}, fmt.Errorf("parse session created_at: %w", err)
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return Session{}, fmt.Errorf("parse session updated_at: %w", err)
	}

	session.Status = SessionStatus(status)
	if providerSessionID.Valid {
		session.ProviderSessionID = providerSessionID.String
	}
	if workspacePath.Valid {
		session.WorkspacePath = workspacePath.String
	}
	if agentOptions == "" {
		agentOptions = "{}"
	}
	if !json.Valid([]byte(agentOptions)) {
		return Session{}, fmt.Errorf("scan session: invalid agent_options_json")
	}
	session.AgentOptions = json.RawMessage(agentOptions)
	session.EventCount = eventCount
	session.LastEventSeq = lastEventSeq
	session.ToolCount = toolCount
	session.TokenCount = tokenCount
	session.PendingInputCount = pendingInputCount
	session.PendingPermissionCount = pendingPermissionCount
	session.NotificationAttentionSeq = notificationAttentionSeq
	session.CreatedAt = parsedCreatedAt
	session.UpdatedAt = parsedUpdatedAt
	if lastActivityAt.Valid {
		parsed, err := parseTime(lastActivityAt.String)
		if err != nil {
			return Session{}, fmt.Errorf("parse session last_activity_at: %w", err)
		}
		session.LastActivityAt = &parsed
	}

	if completedAt.Valid {
		parsedCompletedAt, err := parseTime(completedAt.String)
		if err != nil {
			return Session{}, fmt.Errorf("parse session completed_at: %w", err)
		}
		session.CompletedAt = &parsedCompletedAt
	}
	if archivedAt.Valid {
		parsedArchivedAt, err := parseTime(archivedAt.String)
		if err != nil {
			return Session{}, fmt.Errorf("parse session archived_at: %w", err)
		}
		session.ArchivedAt = &parsedArchivedAt
	}
	if pinnedAt.Valid {
		parsedPinnedAt, err := parseTime(pinnedAt.String)
		if err != nil {
			return Session{}, fmt.Errorf("parse session pinned_at: %w", err)
		}
		session.PinnedAt = &parsedPinnedAt
	}
	session.ParentSessionID = parentSessionID.String
	session.SpawnedByRunID = spawnedByRunID.String
	session.LineageDepth = lineageDepth
	session.ChildCount = childCount

	return session, nil
}

func scanEvent(row rowScanner) (Event, error) {
	var event Event
	var role sql.NullString
	var status string
	var payload string
	var createdAt string

	if err := row.Scan(
		&event.ID,
		&event.SessionID,
		&event.Seq,
		&event.Type,
		&role,
		&status,
		&payload,
		&createdAt,
	); err != nil {
		return Event{}, fmt.Errorf("scan event: %w", err)
	}

	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return Event{}, fmt.Errorf("parse event created_at: %w", err)
	}

	event.Status = EventStatus(status)
	if role.Valid {
		event.Role = role.String
	}
	event.Payload = json.RawMessage(payload)
	event.CreatedAt = parsedCreatedAt

	return event, nil
}

func scanGlobalEvent(row rowScanner) (Event, error) {
	var event Event
	var role sql.NullString
	var status string
	var payload string
	var createdAt string

	if err := row.Scan(
		&event.ID,
		&event.SessionID,
		&event.Seq,
		&event.Type,
		&role,
		&status,
		&payload,
		&createdAt,
		&event.GlobalSeq,
	); err != nil {
		return Event{}, fmt.Errorf("scan global event: %w", err)
	}

	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return Event{}, fmt.Errorf("parse global event created_at: %w", err)
	}
	event.Status = EventStatus(status)
	if role.Valid {
		event.Role = role.String
	}
	event.Payload = json.RawMessage(payload)
	event.CreatedAt = parsedCreatedAt
	return event, nil
}

func scanQueuedMessage(row rowScanner) (QueuedMessage, error) {
	var message QueuedMessage
	var status string
	var agentOptions string
	var skills string
	var createdAt string
	var updatedAt string

	if err := row.Scan(
		&message.ID,
		&message.SessionID,
		&message.Seq,
		&status,
		&message.Content,
		&agentOptions,
		&skills,
		&message.SourceKind,
		&message.SourceID,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QueuedMessage{}, ErrNotFound
		}
		return QueuedMessage{}, fmt.Errorf("scan queued message: %w", err)
	}

	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return QueuedMessage{}, fmt.Errorf("parse queued message created_at: %w", err)
	}
	parsedUpdatedAt, err := parseTime(updatedAt)
	if err != nil {
		return QueuedMessage{}, fmt.Errorf("parse queued message updated_at: %w", err)
	}
	if agentOptions == "" {
		agentOptions = "{}"
	}
	if !json.Valid([]byte(agentOptions)) {
		return QueuedMessage{}, fmt.Errorf("scan queued message: invalid agent_options_json")
	}
	if skills == "" {
		skills = "[]"
	}
	var skillValues []any
	if err := json.Unmarshal([]byte(skills), &skillValues); err != nil {
		return QueuedMessage{}, fmt.Errorf("scan queued message: invalid skills_json")
	}

	message.Status = QueuedMessageStatus(status)
	if !isValidQueuedMessageStatus(message.Status) {
		return QueuedMessage{}, fmt.Errorf("scan queued message: unsupported status %s", status)
	}
	message.AgentOptions = json.RawMessage(agentOptions)
	message.Skills = json.RawMessage(skills)
	message.CreatedAt = parsedCreatedAt
	message.UpdatedAt = parsedUpdatedAt

	return message, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}

	return t.UTC(), nil
}

func isTerminalSessionStatus(status SessionStatus) bool {
	return status == SessionStatusFailed
}

func isValidSessionStatus(status SessionStatus) bool {
	switch status {
	case SessionStatusIdle, SessionStatusRunning, SessionStatusFailed:
		return true
	default:
		return false
	}
}

func isValidQueuedMessageStatus(status QueuedMessageStatus) bool {
	switch status {
	case QueuedMessageStatusPending, QueuedMessageStatusSending, QueuedMessageStatusSent, QueuedMessageStatusRemoved:
		return true
	default:
		return false
	}
}
