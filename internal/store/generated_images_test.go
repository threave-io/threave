package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestGeneratedImageMigrationPreservesHistoryAndExternalizesBytes(t *testing.T) {
	ctx := context.Background()
	database := newTestStore(t, ctx)
	session := createTestSession(t, ctx, database)
	image := bytes.Repeat([]byte("image bytes"), 10000)
	payload, _ := json.Marshal(map[string]any{
		"provider": "codex", "provider_event_type": "item/completed", "run_id": "run_1",
		"raw": map[string]any{"threadId": "thread_1", "turnId": "turn_1", "item": map[string]any{
			"type": "imageGeneration", "id": "image_1", "status": "completed", "result": base64.StdEncoding.EncodeToString(image),
		}},
	})
	original, err := database.AppendEvent(ctx, AppendEventParams{
		SessionID: session.ID, Type: "provider.codex.event", Role: "system", Status: EventStatusCompleted, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := database.AppendEvent(ctx, AppendEventParams{
		SessionID: session.ID, Type: "provider.codex.event", Role: "system", Status: EventStatusCompleted,
		Payload: json.RawMessage(`{"provider_event_type":"item/completed","raw":{"item":{"type":"imageGeneration","id":"image_2","status":"failed","failure":{"message":"Generation failed"}}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = 29"); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := database.ListRecentEventsFiltered(ctx, session.ID, 10, EventListFilter{})
	if err != nil || len(events) != 2 {
		t.Fatalf("images must be visible without debug filter: %#v (%v)", events, err)
	}
	if events[0].ID != original.ID || events[0].Seq != original.Seq || !events[0].CreatedAt.Equal(original.CreatedAt) || events[0].Type != "tool.call.completed" || events[0].Role != "assistant" {
		t.Fatalf("migration changed identity or failed to promote image: %#v", events[0])
	}
	if events[1].ID != failed.ID || events[1].Status != EventStatusFailed {
		t.Fatalf("migration must preserve failed generation: %#v", events[1])
	}
	result, err := database.RunEventMaintenanceBatch(ctx, nil, 100)
	if err != nil || result.ExtractedBlobEvents != 1 {
		t.Fatalf("expected one externalized image: %#v (%v)", result, err)
	}
	compact, err := database.GetEvent(ctx, session.ID, original.Seq)
	if err != nil || len(compact.Payload) > 1000 || bytes.Contains(compact.Payload, []byte(base64.StdEncoding.EncodeToString(image))) {
		t.Fatalf("history should only retain compact metadata (%v)", err)
	}
	blob, err := database.GetEventBlob(ctx, session.ID, original.Seq, "tool-content", 0)
	if err != nil || blob.MediaType != "image/png" || !bytes.Equal(blob.Data, image) {
		t.Fatalf("image bytes must round-trip intact (%v)", err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}
