package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/threave-io/threave/internal/agents"
	runcontrol "github.com/threave-io/threave/internal/session"
	"github.com/threave-io/threave/internal/store"
)

type steeringTestAgent struct {
	userInputAgent
	agentType  string
	ready      chan string
	onDelivery func(agents.SteeringInput) error
}

func (a *steeringTestAgent) Type() string {
	if a.agentType != "" {
		return a.agentType
	}
	return "fake"
}

func (a *steeringTestAgent) Run(ctx context.Context, input agents.AgentInput, emit agents.EmitFunc) error {
	unregister, err := input.Steering.RegisterSteering(ctx, input.SessionID, input.RunID, func(_ context.Context, value agents.SteeringInput) error { return a.onDelivery(value) })
	if err != nil {
		return err
	}
	defer unregister()
	if err := emit(ctx, agents.AgentEvent{Type: "agent.run.started", Role: "assistant", Status: "started", Payload: map[string]any{}}); err != nil {
		return err
	}
	a.ready <- input.RunID
	<-ctx.Done()
	return ctx.Err()
}

func TestSendNowPersistsDeduplicatesAndNeverQueues(t *testing.T) {
	for _, agentType := range []string{"fake", "claude"} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprint(agentType, " reject=", reject), func(t *testing.T) {
				ctx := context.Background()
				agent := &steeringTestAgent{agentType: agentType, ready: make(chan string, 1)}
				s, _, runs, handler := newIntegrationAPI(t, ctx, agent)
				session, err := s.CreateSession(ctx, store.CreateSessionParams{Title: "Steering", AgentType: agentType})
				if err != nil {
					t.Fatal(err)
				}
				var count atomic.Int32
				agent.onDelivery = func(input agents.SteeringInput) error {
					count.Add(1)
					if input.Message != "Change direction" {
						t.Error("changed input")
					}
					if !hasEventType(t, ctx, s, session.ID, "user.message.steer.submitted") {
						t.Error("not persisted before delivery")
					}
					record, err := s.GetMessageSubmission(ctx, session.ID, "steer1")
					if err != nil || record.State != "unknown" {
						t.Error("reported accepted before provider confirmation")
					}
					if reject {
						return errors.New("provider disconnected")
					}
					return nil
				}
				path := "/api/sessions/" + session.ID + "/messages"
				if rec := postJSON(handler, path, `{"content":"Start"}`); rec.Code != http.StatusAccepted {
					t.Fatal(rec.Body.String())
				}
				var runID string
				select {
				case runID = <-agent.ready:
				case <-time.After(3 * time.Second):
					t.Fatal("not ready")
				}
				defer runs.Cancel(session.ID, runcontrol.Cancellation{})
				body := fmt.Sprintf(`{"content":"Change direction","steer":true,"expected_run_id":%q,"client_submission_id":"steer1"}`, runID)
				rec := postJSON(handler, path, body)
				want := http.StatusAccepted
				if reject {
					want = http.StatusServiceUnavailable
				}
				if rec.Code != want || count.Load() != 1 {
					t.Fatalf("response %d %s count %d", rec.Code, rec.Body.String(), count.Load())
				}
				repeat := postJSON(handler, path, body)
				if count.Load() != 1 || (!reject && repeat.Code != want) {
					t.Fatalf("duplicate delivery: %d %d", count.Load(), repeat.Code)
				}
				var response map[string]any
				status := get(handler, "/api/sessions/"+session.ID+"/submissions/steer1")
				if err := json.Unmarshal(status.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				state := "accepted"
				if reject {
					state = "unknown"
				}
				if response["state"] != state {
					t.Fatalf("receipt: %s", status.Body.String())
				}
				queued, err := s.ListQueuedMessages(ctx, session.ID)
				if err != nil || len(queued) != 0 {
					t.Fatalf("steer queued: %#v %v", queued, err)
				}
				current, _ := s.GetSession(ctx, session.ID)
				if current.Status != store.SessionStatusRunning {
					t.Fatal("steer ended run")
				}
				for i, invalid := range []string{
					`"steer":true`,
					`"steer":true,"expected_run_id":"old"`,
					fmt.Sprintf(`"steer":true,"expected_run_id":%q,"queue":true`, runID),
					fmt.Sprintf(`"steer":true,"expected_run_id":%q,"agent_options":{}`, runID),
				} {
					rec := postJSON(handler, path, fmt.Sprintf(`{"content":"No","client_submission_id":"invalid%d",%s}`, i, invalid))
					if rec.Code != 400 && rec.Code != 409 {
						t.Fatalf("invalid steer: %d %s", rec.Code, rec.Body.String())
					}
				}
				if count.Load() != 1 {
					t.Fatal("invalid requests delivered")
				}
			})
		}
	}
}
