package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestSessionActivityTracksFinishedTurnsOnly(t *testing.T) {
	ctx := context.Background()
	database := newTestStore(t, ctx)
	now := time.Date(2026, 6, 12, 16, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return now }
	session := createTestSession(t, ctx, database)
	if session.LastActivityAt != nil {
		t.Fatal("new session should have no activity")
	}
	var lastActivity time.Time
	for _, eventType := range []string{
		"user.message.completed", "agent.run.started", "agent.message.delta", "agent.message.completed",
		"tool.call.started", "tool.call.completed", "file.change.completed",
		"agent.run.completed", "agent.run.started", "agent.input.answered", "agent.thinking.completed",
		"agent.run.failed", "agent.run.cancelled",
		"session.agent_options.updated", "session.pin.updated", "session.status.updated", "provider.codex.event",
	} {
		now = now.Add(time.Minute)
		event, err := database.AppendEvent(ctx, AppendEventParams{
			SessionID: session.ID, Type: eventType, Role: "assistant", Status: EventStatusCompleted,
			Payload: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		switch eventType {
		case "agent.run.completed", "agent.run.failed", "agent.run.cancelled":
			lastActivity = event.CreatedAt
		}
		loaded, err := database.GetSession(ctx, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if lastActivity.IsZero() {
			if loaded.LastActivityAt != nil {
				t.Fatalf("%s: unfinished turn changed activity to %v", eventType, loaded.LastActivityAt)
			}
		} else if loaded.LastActivityAt == nil || !loaded.LastActivityAt.Equal(lastActivity) {
			t.Fatalf("%s: expected activity %v, got %v", eventType, lastActivity, loaded.LastActivityAt)
		}
	}
	now = now.Add(time.Hour)
	if _, err := database.UpdateSessionTitle(ctx, UpdateSessionTitleParams{ID: session.ID, Title: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	if err := database.ClearNotificationAttention(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.GetSession(ctx, session.ID)
	if err != nil || loaded.LastActivityAt == nil || !loaded.LastActivityAt.Equal(lastActivity) {
		t.Fatalf("metadata and reads changed activity: %v, %v", loaded.LastActivityAt, err)
	}
}

func TestSessionActivityMigrationBackfillsExistingHistory(t *testing.T) {
	ctx := context.Background()
	database := newTestStore(t, ctx)
	active := createTestSession(t, ctx, database)
	empty := createTestSession(t, ctx, database)
	work, err := database.AppendEvent(ctx, AppendEventParams{
		SessionID: active.ID, Type: "agent.run.completed", Role: "assistant", Status: EventStatusCompleted,
		Payload: json.RawMessage(`{"text":"Done"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{active.ID, empty.ID} {
		if _, err := database.AppendEvent(ctx, AppendEventParams{
			SessionID: id, Type: "tool.call.completed", Role: "assistant", Status: EventStatusCompleted,
			Payload: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Recreate the pre-migration schema while retaining existing event history.
	for _, query := range []string{
		"DROP INDEX idx_sessions_activity",
		"ALTER TABLE sessions DROP COLUMN last_activity_at",
		"DELETE FROM schema_migrations WHERE version >= 27",
	} {
		if _, err := database.db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// The previous migration ranked every work event. Re-run the turn migration
	// over those timestamps to verify an upgrade corrects them too.
	if _, err := database.db.ExecContext(ctx, `UPDATE sessions SET last_activity_at = (
		SELECT created_at FROM events WHERE events.session_id = sessions.id ORDER BY seq DESC LIMIT 1
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = 28"); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.GetSession(ctx, active.ID)
	if err != nil || loaded.LastActivityAt == nil || !loaded.LastActivityAt.Equal(work.CreatedAt) {
		t.Fatalf("expected backfilled work timestamp %v, got %v (%v)", work.CreatedAt, loaded.LastActivityAt, err)
	}
	loaded, err = database.GetSession(ctx, empty.ID)
	if err != nil || loaded.LastActivityAt != nil {
		t.Fatalf("unfinished history should not count as activity: %v (%v)", loaded.LastActivityAt, err)
	}
}

func TestSessionTreeRanksGroupActivityBeforePagination(t *testing.T) {
	ctx := context.Background()
	database := newTestStore(t, ctx)
	now := time.Date(2026, 6, 12, 16, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return now }
	parent := createTestSession(t, ctx, database)
	now = now.Add(time.Minute)
	child, err := database.CreateSession(ctx, CreateSessionParams{Title: "Child", AgentType: "codex", ParentSessionID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	other := createTestSession(t, ctx, database)
	now = now.Add(time.Minute)
	if _, err := database.AppendEvent(ctx, AppendEventParams{
		SessionID: child.ID, Type: "tool.call.completed", Role: "assistant", Status: EventStatusCompleted,
		Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	tree, err := database.ListSessionTree(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionIDs(t, tree, []string{other.ID})
	now = now.Add(time.Minute)
	if _, err := database.AppendEvent(ctx, AppendEventParams{
		SessionID: child.ID, Type: "agent.run.completed", Role: "assistant", Status: EventStatusCompleted,
		Payload: json.RawMessage(`{"text":"Latest work"}`),
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := database.UpdateSessionTitle(ctx, UpdateSessionTitleParams{ID: other.ID, Title: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	tree, err = database.ListSessionTree(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionIDs(t, tree, []string{child.ID, parent.ID})
	if _, err := database.ArchiveSession(ctx, ArchiveSessionParams{ID: child.ID}); err != nil {
		t.Fatal(err)
	}
	tree, err = database.ListSessionTree(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionIDs(t, tree, []string{other.ID})
}
