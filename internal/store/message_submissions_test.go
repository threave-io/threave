package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
)

func TestSubmissionReservationSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "submissions.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.CreateSession(ctx, CreateSessionParams{AgentType: "fake", Title: "Fixture"})
	if err != nil {
		t.Fatal(err)
	}
	_, claimed, err := db.ClaimMessageSubmission(ctx, session.ID, "pending-1", "fingerprint")
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	record, claimed, err := db.ClaimMessageSubmission(ctx, session.ID, "pending-1", "fingerprint")
	if err != nil || claimed || record.State != "unknown" {
		t.Fatalf("unsafe reclaim: %#v %v %v", record, claimed, err)
	}
	if err := db.CompleteMessageSubmission(ctx, session.ID, "pending-1", 400, []byte(`{"error":"rejected"}`)); err != nil {
		t.Fatal(err)
	}
	record, err = db.GetMessageSubmission(ctx, session.ID, "pending-1")
	if err != nil || record.State != "rejected" {
		t.Fatalf("rejected receipt: %#v %v", record, err)
	}
}

func TestDefiniteMessagePersistenceFailureCanBeReclaimedExactlyOnce(t *testing.T) {
	ctx := context.Background()
	db := newTestStore(t, ctx)
	session := createTestSession(t, ctx, db)
	_, claimed, err := db.ClaimMessageSubmission(ctx, session.ID, "retry-1", "original")
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if err := db.CompleteMessageSubmission(ctx, session.ID, "retry-1", 500, []byte(`{"error":"failed to persist user message"}`)); err != nil {
		t.Fatal(err)
	}
	record, err := db.GetMessageSubmission(ctx, session.ID, "retry-1")
	if err != nil || record.State != "not_received" {
		t.Fatalf("definite pre-delivery failure must be retryable: %#v %v", record, err)
	}
	_, claimed, err = db.ClaimMessageSubmission(ctx, session.ID, "retry-1", "changed content")
	if err != nil || claimed {
		t.Fatalf("different content must not reclaim the identity: %v %v", claimed, err)
	}
	var group sync.WaitGroup
	results := make(chan bool, 12)
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, claimed, err := db.ClaimMessageSubmission(ctx, session.ID, "retry-1", "original")
			if err != nil {
				t.Error(err)
			}
			results <- claimed
		}()
	}
	group.Wait()
	close(results)
	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("expected one retry claim, got %d", claims)
	}
	record, err = db.GetMessageSubmission(ctx, session.ID, "retry-1")
	if err != nil || record.State != "unknown" || record.HTTPStatus != 0 {
		t.Fatalf("an active or crashed retry must remain unknown: %#v %v", record, err)
	}
}

func TestSubmissionRecoveryDoesNotReclaimAmbiguousOrDeliveredMessages(t *testing.T) {
	for _, test := range []struct {
		name, eventType, want string
		status                int
		response              string
	}{
		{"unfinished handler", "", "unknown", 0, `{}`},
		{"ambiguous failure", "", "unknown", 503, `{"error":"delivery uncertain"}`},
		{"unrelated server failure", "", "unknown", 500, `{"error":"unexpected error"}`},
		{"canonical acceptance", "user.message.completed", "accepted", 500, `{"error":"failed to persist user message"}`},
		{"steering intent", "user.message.steer.submitted", "unknown", 500, `{"error":"failed to persist user message"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db := newTestStore(t, ctx)
			session := createTestSession(t, ctx, db)
			if _, _, err := db.ClaimMessageSubmission(ctx, session.ID, "id", "hash"); err != nil {
				t.Fatal(err)
			}
			if err := db.CompleteMessageSubmission(ctx, session.ID, "id", test.status, []byte(test.response)); err != nil {
				t.Fatal(err)
			}
			if test.eventType != "" {
				if _, err := db.AppendEvent(ctx, AppendEventParams{
					SessionID: session.ID, Type: test.eventType, Role: "user", Status: EventStatusCompleted,
					Payload: json.RawMessage(`{"text":"message","client_submission_id":"id"}`),
				}); err != nil {
					t.Fatal(err)
				}
			}
			record, claimed, err := db.ClaimMessageSubmission(ctx, session.ID, "id", "hash")
			if err != nil || claimed || record.State != test.want {
				t.Fatalf("unexpected recovery: %#v claimed=%v err=%v", record, claimed, err)
			}
		})
	}
}
