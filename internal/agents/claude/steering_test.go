package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/threave-io/threave/internal/agents"
	runcontrol "github.com/threave-io/threave/internal/session"
)

type readySteeringBroker struct {
	*runcontrol.Manager
	ready chan struct{}
}

func (b readySteeringBroker) RegisterSteering(ctx context.Context, session, run string, deliver func(context.Context, agents.SteeringInput) error) (func(), error) {
	unregister, err := b.Manager.RegisterSteering(ctx, session, run, deliver)
	close(b.ready)
	return unregister, err
}

func runFakeClaudeSteering(scenario string, decoder *json.Decoder, encoder *json.Encoder) {
	if !strings.Contains(strings.Join(os.Args, " "), "--replay-user-messages") {
		os.Exit(30)
	}
	if scenario == "blocked" {
		time.Sleep(time.Minute)
		return
	}
	var user map[string]any
	if decoder.Decode(&user) != nil {
		os.Exit(31)
	}
	if mapString(user, "type") != "user" || mapString(user, "uuid") == "" {
		os.Exit(32)
	}
	if capture := os.Getenv("GORCHESTRA_FAKE_CLAUDE_CAPTURE"); capture != "" {
		data, _ := json.Marshal(user)
		_ = os.WriteFile(capture+".steer", data, 0600)
	}
	if scenario == "exit" {
		return
	}
	if scenario == "failure" {
		_ = encoder.Encode(map[string]any{"type": "result", "is_error": true, "result": "Provider failed"})
		return
	}
	if scenario == "queued" || scenario == "timeout" {
		_ = encoder.Encode(map[string]any{"type": "result", "subtype": "success", "result": "Original reply"})
	}
	if scenario == "timeout" {
		// No echo: the original result must wait for delivery to expire.
		_ = decoder.Decode(&map[string]any{})
		return
	}
	// Neither an unrelated replay nor a tool-result message acknowledges input.
	_ = encoder.Encode(map[string]any{"type": "user", "uuid": "unrelated", "isReplay": true, "message": map[string]any{"role": "user", "content": "not the injected message"}})
	_ = encoder.Encode(map[string]any{"type": "user", "uuid": user["uuid"], "isReplay": false, "message": user["message"]})
	_ = encoder.Encode(map[string]any{"type": "assistant", "message": map[string]any{"id": "progress", "content": []map[string]any{{"type": "text", "text": "Still processing"}}}})
	user["isReplay"] = true
	_ = encoder.Encode(user)
	if scenario == "twice" {
		var second map[string]any
		if decoder.Decode(&second) != nil {
			os.Exit(33)
		}
		if second["uuid"] == user["uuid"] {
			os.Exit(34)
		}
		second["isReplay"] = true
		_ = encoder.Encode(second)
	}
	_ = encoder.Encode(map[string]any{"type": "assistant", "message": map[string]any{"id": "final", "content": []map[string]any{{"type": "text", "text": "Changed direction"}}}})
	_ = encoder.Encode(map[string]any{"type": "result", "subtype": "success", "result": "Changed direction"})
}

func TestClaudeSteeringUsesSharedBrokerAndConfirmedReplay(t *testing.T) {
	for _, scenario := range []string{"merged", "queued", "twice", "exit", "failure", "timeout", "blocked"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("GORCHESTRA_FAKE_CLAUDE_STREAM", "1")
			t.Setenv("GORCHESTRA_FAKE_CLAUDE_STEERING", scenario)
			capture := filepath.Join(t.TempDir(), "input.json")
			t.Setenv("GORCHESTRA_FAKE_CLAUDE_CAPTURE", capture)
			manager := runcontrol.NewManager()
			ctx, cleanup, err := manager.Register(context.Background(), "s")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			broker := readySteeringBroker{manager, make(chan struct{})}
			recorder := newEventRecorder()
			done := make(chan error, 1)
			agent := fakeClaudeAgent(t)
			go func() {
				done <- agent.Run(ctx, agents.AgentInput{SessionID: "s", RunID: "run1", Workdir: t.TempDir(), Message: "Start", Steering: broker}, recorder.emit)
			}()
			t.Cleanup(func() { _ = manager.Cancel("s", runcontrol.Cancellation{}) })
			select {
			case <-broker.ready:
			case <-time.After(3 * time.Second):
				t.Fatal("steering never registered")
			}
			persisted := false
			if err := manager.SteerWithPersistence(ctx, "s", "stale", agents.SteeringInput{Message: "stale"}, func() error { t.Fatal("stale input persisted"); return nil }); !errors.Is(err, runcontrol.ErrSteeringUnavailable) {
				t.Fatalf("stale input: %v", err)
			}
			input := agents.SteeringInput{Message: "Change direction", Skills: []agents.SkillReference{{Name: "review", Path: "/review/SKILL.md"}}, Attachments: []agents.Attachment{{MediaType: "image/png", DataURL: "data:image/png;base64,aW1hZ2U="}}}
			// Use valid large base64 for the blocked-writer case.
			if scenario == "blocked" {
				input.Attachments[0].DataURL = "data:image/png;base64," + strings.Repeat("YWFh", 400000)
			}
			timeout := 3 * time.Second
			if scenario == "timeout" || scenario == "blocked" {
				timeout = 250 * time.Millisecond
			}
			deliveryCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			err = manager.SteerWithPersistence(deliveryCtx, "s", "run1", input, func() error { persisted = true; return nil })
			if !persisted {
				t.Fatal("input was not persisted")
			}
			if scenario == "exit" || scenario == "failure" || scenario == "timeout" || scenario == "blocked" {
				if err == nil {
					t.Fatal("unacknowledged input reported success")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if scenario == "twice" {
				if err := manager.SteerWithPersistence(ctx, "s", "run1", agents.SteeringInput{Message: "Another correction"}, nil); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "blocked" {
				_ = manager.Cancel("s", runcontrol.Cancellation{})
			}
			select {
			case runErr := <-done:
				if scenario != "exit" && scenario != "failure" && scenario != "blocked" && runErr != nil {
					t.Fatal(runErr)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("run did not finish or leaked a writer")
			}
			if scenario != "blocked" {
				data, err := os.ReadFile(capture + ".steer")
				if err != nil {
					t.Fatal(err)
				}
				var user struct {
					Message struct {
						Content []struct {
							Type   string
							Text   string
							Source map[string]any
						}
					}
				}
				if err := json.Unmarshal(data, &user); err != nil {
					t.Fatal(err)
				}
				blocks := user.Message.Content
				if len(blocks) != 2 || !strings.Contains(blocks[0].Text, "/review/SKILL.md") || blocks[1].Source["data"] != "aW1hZ2U=" {
					t.Fatalf("input was not preserved: %s", data)
				}
			}
			if scenario == "merged" || scenario == "queued" || scenario == "twice" || scenario == "timeout" {
				count := 0
				for _, e := range recorder.snapshot() {
					if e.Event.Type == "agent.run.completed" {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("expected one terminal completion, got %d", count)
				}
			}
			if scenario == "queued" || scenario == "twice" {
				found := false
				for _, e := range recorder.snapshot() {
					if e.Event.Type == "agent.message.completed" && e.Event.Payload.(map[string]any)["text"] == "Changed direction" {
						found = true
					}
				}
				if !found {
					t.Fatal("injected input response was dropped")
				}
			}
			if err := manager.SteerWithPersistence(context.Background(), "s", "run1", input, func() error { t.Fatal("late input persisted"); return nil }); err == nil {
				t.Fatal("handler not removed")
			}
		})
	}
}

func TestLargeReplayIsAcknowledgementOnly(t *testing.T) {
	// Larger than the previous 10 MiB line limit, but below supported image input.
	raw := `{"type":"user","uuid":"input-id","isReplay":true,"message":{"role":"user","content":"` + strings.Repeat("a", 11*1024*1024) + `"}}` + "\n"
	incoming := readStream(context.Background(), strings.NewReader(raw), strings.NewReader(""))
	var replay *streamEvent
	for value := range incoming {
		if value.ReadErr != nil || value.ParseErr != nil {
			t.Fatalf("replay read failed: %#v", value)
		}
		if value.Event != nil {
			replay = value.Event
		}
	}
	if replay == nil || !replay.IsReplay || replay.UUID != "input-id" || len(replay.Message) != 0 || len(replay.Raw) != 0 {
		t.Fatalf("expected acknowledgement without copied message data: %#v", replay)
	}
}
