package store

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ListSessionTree paginates roots while returning every visible descendant and
// any archived ancestors needed to preserve the path to an unarchived child.
func (s *Store) ListSessionTree(ctx context.Context, rootLimit int, includeArchived bool) ([]Session, error) {
	if rootLimit <= 0 {
		rootLimit = defaultSessionLimit
	}
	all, err := s.ListSessions(ctx, ListSessionsParams{Limit: 1_000_000, IncludeArchived: true})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Session, len(all))
	for _, session := range all {
		byID[session.ID] = session
	}
	rootFor := func(session Session) string {
		seen := map[string]bool{}
		for session.ParentSessionID != "" && !seen[session.ID] {
			seen[session.ID] = true
			parent, ok := byID[session.ParentSessionID]
			if !ok {
				break
			}
			session = parent
		}
		return session.ID
	}
	eligibleRoots := map[string]bool{}
	rootActivity := map[string]time.Time{}
	for _, session := range all {
		if includeArchived || session.ArchivedAt == nil {
			rootID := rootFor(session)
			eligibleRoots[rootID] = true
			activity := session.CreatedAt
			if session.LastActivityAt != nil {
				activity = *session.LastActivityAt
			}
			if activity.After(rootActivity[rootID]) {
				rootActivity[rootID] = activity
			}
		}
	}
	roots := []Session{}
	for _, session := range all {
		if session.ParentSessionID == "" && eligibleRoots[session.ID] {
			roots = append(roots, session)
		}
	}
	// Rank whole groups before applying the root limit so an active child cannot
	// disappear from the snapshot because its parent has an older timestamp.
	sort.SliceStable(roots, func(i, j int) bool {
		left, right := roots[i], roots[j]
		if (left.PinnedAt != nil) != (right.PinnedAt != nil) {
			return left.PinnedAt != nil
		}
		if left.PinnedAt != nil && !left.PinnedAt.Equal(*right.PinnedAt) {
			return left.PinnedAt.After(*right.PinnedAt)
		}
		return rootActivity[left.ID].After(rootActivity[right.ID])
	})
	selectedRoots := map[string]bool{}
	for _, root := range roots[:min(rootLimit, len(roots))] {
		selectedRoots[root.ID] = true
	}
	visible := map[string]bool{}
	for _, session := range all {
		if !selectedRoots[rootFor(session)] || (!includeArchived && session.ArchivedAt != nil) {
			continue
		}
		visible[session.ID] = true
		parentID := session.ParentSessionID
		for parentID != "" {
			visible[parentID] = true
			parent, ok := byID[parentID]
			if !ok {
				break
			}
			parentID = parent.ParentSessionID
		}
	}
	result := make([]Session, 0, len(visible))
	for _, session := range all {
		if visible[session.ID] {
			result = append(result, session)
		}
	}
	return result, nil
}

// ListSessionChildren returns direct children or the complete descendant tree.
// Stored lineage depth gives clients stable indentation after restarts.
func (s *Store) ListSessionChildren(ctx context.Context, parentSessionID string, recursive bool, includeArchived bool) ([]Session, error) {
	whereArchived := "AND child.archived_at IS NULL"
	if includeArchived {
		whereArchived = ""
	}
	query := `WITH RECURSIVE descendants(id) AS (
		SELECT child.id FROM sessions child WHERE child.parent_session_id = ? ` + whereArchived + `
		UNION
		SELECT child.id FROM sessions child JOIN descendants parent ON child.parent_session_id = parent.id ` + whereArchived + `
	)
	SELECT sessions.id, sessions.title, sessions.agent_type, sessions.status, sessions.provider_session_id,
	       sessions.workspace_path, sessions.agent_options_json, sessions.durable_event_count,
	       sessions.last_durable_event_seq, sessions.materialized_tool_count, sessions.materialized_token_count,
	       sessions.pending_input_count, sessions.pending_permission_count,
	       COALESCE((SELECT seq FROM notification_attention WHERE notification_attention.session_id = sessions.id), 0),
	       sessions.created_at, sessions.updated_at, sessions.last_activity_at, sessions.completed_at, sessions.archived_at, sessions.pinned_at,
	       sessions.parent_session_id, sessions.spawned_by_run_id, sessions.lineage_depth,
	       (SELECT COUNT(*) FROM sessions children WHERE children.parent_session_id = sessions.id AND children.archived_at IS NULL)
	FROM sessions JOIN descendants ON descendants.id = sessions.id`
	if !recursive {
		query += ` WHERE sessions.parent_session_id = ?`
	}
	query += ` ORDER BY sessions.lineage_depth ASC, COALESCE(sessions.last_activity_at, sessions.created_at) DESC, sessions.id DESC`
	args := []any{parentSessionID}
	if !recursive {
		args = append(args, parentSessionID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list session children: %w", err)
	}
	defer rows.Close()
	children := make([]Session, 0)
	for rows.Next() {
		child, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		children = append(children, child)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list session children rows: %w", err)
	}
	return children, nil
}
