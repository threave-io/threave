package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/threave-io/threave/internal/agents"
)

func TestMain(m *testing.M) {
	if os.Getenv("GORCHESTRA_FAKE_PI_RPC") != "" {
		runFakePi()
		return
	}
	os.Exit(m.Run())
}

func TestAvailabilityDetection(t *testing.T) {
	agent := New(
		WithBinary("pi-test"),
		WithVersionChecker(func(_ context.Context, binary string) (string, error) {
			if binary != "pi-test" {
				t.Fatalf("expected binary pi-test, got %q", binary)
			}
			return "0.1.0", nil
		}),
	)

	version, err := agent.CheckAvailability(context.Background())
	if err != nil {
		t.Fatalf("check availability: %v", err)
	}
	if version != "0.1.0" {
		t.Fatalf("expected version 0.1.0, got %q", version)
	}
	if err := agent.Available(); err != nil {
		t.Fatalf("expected available agent, got %v", err)
	}
}

func TestAvailabilityDetectionWrapsUnavailable(t *testing.T) {
	agent := New(
		WithBinary("missing-pi"),
		WithVersionChecker(func(context.Context, string) (string, error) {
			return "", errors.New("not found")
		}),
	)

	_, err := agent.CheckAvailability(context.Background())
	if !errors.Is(err, agents.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestCommandConstructionUsesRPCMode(t *testing.T) {
	agent := New(WithBinary("/opt/bin/pi"))
	cmd := agent.command("/tmp/workspace", "session_1")

	if cmd.Path != "/opt/bin/pi" {
		t.Fatalf("expected path /opt/bin/pi, got %q", cmd.Path)
	}
	wantArgs := []string{"/opt/bin/pi", "--mode", "rpc", "--no-approve", "--session", "session_1"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("expected args %#v, got %#v", wantArgs, cmd.Args)
	}
	if cmd.Dir != "/tmp/workspace" {
		t.Fatalf("expected dir /tmp/workspace, got %q", cmd.Dir)
	}
}

func TestOptionsCommandUsesNoSession(t *testing.T) {
	agent := New(WithBinary("/opt/bin/pi"))
	cmd := agent.optionsCommand("/tmp/workspace")

	wantArgs := []string{"/opt/bin/pi", "--mode", "rpc", "--no-approve", "--no-session"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("expected args %#v, got %#v", wantArgs, cmd.Args)
	}
	if cmd.Dir != "/tmp/workspace" {
		t.Fatalf("expected dir /tmp/workspace, got %q", cmd.Dir)
	}
}

func TestOptionsFromModels(t *testing.T) {
	options := optionsFromModels(json.RawMessage(`{
		"models": [
			{"provider":"anthropic","id":"claude-sonnet-4-5","name":"Claude Sonnet 4.5","reasoning":true,"default":true},
			{"provider":"openai","id":"gpt-5.4","name":"GPT 5.4","reasoning":false}
		]
	}`))

	if options.DefaultModel != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("unexpected default model %q", options.DefaultModel)
	}
	if len(options.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(options.Models))
	}
	if len(options.Models[0].SupportedReasoningEfforts) == 0 {
		t.Fatalf("expected thinking levels for reasoning model, got %#v", options.Models[0])
	}
}

func TestSampleRPCEventsNormalizeExpectedEvents(t *testing.T) {
	events := normalizeMessages(t, []string{
		`{"type":"message_update","message":{"id":"msg_1"},"assistantMessageEvent":{"type":"thinking_start","contentIndex":0}}`,
		`{"type":"message_update","message":{"id":"msg_1"},"assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"Checking"}}`,
		`{"type":"message_update","message":{"id":"msg_1"},"assistantMessageEvent":{"type":"thinking_end","contentIndex":0}}`,
		`{"type":"message_update","message":{"id":"msg_1"},"assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"Hello"}}`,
		`{"type":"tool_execution_start","toolCallId":"tool_1","toolName":"Bash","args":{"command":"pwd"}}`,
		`{"type":"tool_execution_end","toolCallId":"tool_1","toolName":"Bash","args":{"command":"pwd"},"result":{"content":[{"type":"text","text":"/repo"}]}}`,
		`{"type":"message_end","message":{"id":"msg_1","model":"claude-sonnet-4-5","provider":"anthropic","stopReason":"end_turn","content":[{"type":"text","text":"Hello"}]}}`,
		`{"type":"agent_end"}`,
	})

	assertAgentEventTypes(t, events, []string{
		"agent.thinking.started",
		"agent.thinking.delta",
		"agent.thinking.completed",
		"agent.message.delta",
		"tool.call.started",
		"tool.call.completed",
		"agent.message.completed",
		"agent.run.completed",
	})
	thinkingPayload := events[1].Event.Payload.(map[string]any)
	if thinkingPayload["text"] != "Checking" {
		t.Fatalf("unexpected thinking payload %#v", thinkingPayload)
	}
	completedThinkingPayload := events[2].Event.Payload.(map[string]any)
	if completedThinkingPayload["text"] != "Checking" {
		t.Fatalf("expected completed thinking snapshot, got %#v", completedThinkingPayload)
	}
	toolPayload := events[5].Event.Payload.(map[string]any)
	if toolPayload["command"] != "pwd" || toolPayload["output"] != "/repo" {
		t.Fatalf("unexpected tool payload %#v", toolPayload)
	}
}

func TestAgentRunsFakePiRPC(t *testing.T) {
	t.Setenv("GORCHESTRA_FAKE_PI_RPC", "1")
	t.Setenv("GORCHESTRA_FAKE_PI_EXPECT_ENV", "run-only")
	t.Setenv("GORCHESTRA_FAKE_PI_EXPECT_CONTEXT", "Use the Gorchestra host controls.")
	t.Setenv("GORCHESTRA_AGENT_RUN_TEST_VALUE", "parent")
	agent := fakePiAgent(t)
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID:   "sess_test",
		Message:     "hello",
		Workdir:     t.TempDir(),
		Environment: map[string]string{"GORCHESTRA_AGENT_RUN_TEST_VALUE": "run-only"},
		Context:     "Use the Gorchestra host controls.",
		Metadata: map[string]any{
			"pi_options": map[string]any{
				"model":          "anthropic/claude-sonnet-4-5",
				"thinking_level": "high",
			},
		},
	}, recorder.emit)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}

	events := recorder.snapshot()
	assertAgentEventTypes(t, events, []string{
		"agent.run.started",
		"agent.message.delta",
		"agent.run.completed",
	})
	startPayload := events[0].Event.Payload.(map[string]any)
	if startPayload["provider_session_id"] != "pi_fake" {
		t.Fatalf("expected provider session id pi_fake, got %#v", startPayload)
	}
	if got := os.Getenv("GORCHESTRA_AGENT_RUN_TEST_VALUE"); got != "parent" {
		t.Fatalf("expected parent environment to remain unchanged, got %q", got)
	}
}

func TestAgentResumesExistingPiSession(t *testing.T) {
	t.Setenv("GORCHESTRA_FAKE_PI_RPC", "1")
	agent := fakePiAgent(t)
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID:         "sess_test",
		ProviderSessionID: "pi_existing",
		Message:           "hello",
		Workdir:           t.TempDir(),
	}, recorder.emit)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}

	events := recorder.snapshot()
	startPayload := events[0].Event.Payload.(map[string]any)
	if startPayload["provider_session_id"] != "pi_existing" {
		t.Fatalf("expected resumed provider session, got %#v", startPayload)
	}
}

func TestInvalidJSONProducesParseError(t *testing.T) {
	incoming := readRPC(strings.NewReader("{"), strings.NewReader(""))
	message, ok := <-incoming
	if !ok {
		t.Fatal("expected parse error message")
	}
	if message.ParseErr == nil {
		t.Fatalf("expected parse error, got %#v", message)
	}
	if message.ParseErr.Line != 1 {
		t.Fatalf("expected line 1, got %d", message.ParseErr.Line)
	}
}

func normalizeMessages(t *testing.T, lines []string) []normalizedEvent {
	t.Helper()
	normalizer := newNormalizer()
	events := make([]normalizedEvent, 0)
	for _, line := range lines {
		message, err := parseRPCMessage([]byte(line))
		if err != nil {
			t.Fatalf("parse fixture line: %v", err)
		}
		events = append(events, normalizer.normalize(message)...)
	}
	return events
}

func assertAgentEventTypes(t *testing.T, events []normalizedEvent, want []string) {
	t.Helper()
	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.Event.Type)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected event types %#v, got %#v", want, got)
	}
}

func fakePiAgent(t *testing.T) *Agent {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	return New(
		WithBinary(binary),
		WithVersionChecker(func(context.Context, string) (string, error) {
			return "0.1.0", nil
		}),
	)
}

type eventRecorder struct {
	mu     sync.Mutex
	events []normalizedEvent
}

func newEventRecorder() *eventRecorder {
	return &eventRecorder{}
}

func (r *eventRecorder) emit(_ context.Context, event agents.AgentEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, normalizedEvent{Event: event})
	return nil
}

func (r *eventRecorder) snapshot() []normalizedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]normalizedEvent(nil), r.events...)
}

func runFakePi() {
	if expected := os.Getenv("GORCHESTRA_FAKE_PI_EXPECT_ENV"); expected != "" && os.Getenv("GORCHESTRA_AGENT_RUN_TEST_VALUE") != expected {
		os.Exit(10)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	expectedModelSet := false
	expectedThinkingSet := false
	for scanner.Scan() {
		var request map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		commandID := request["commandId"]
		switch request["command"] {
		case "get_state":
			_ = encoder.Encode(map[string]any{
				"commandId": commandID,
				"data":      map[string]any{"sessionId": "pi_fake"},
			})
		case "set_auto_retry":
			_ = encoder.Encode(map[string]any{"commandId": commandID, "data": map[string]any{}})
		case "set_model":
			data, _ := request["data"].(map[string]any)
			if data["provider"] == "anthropic" && data["modelId"] == "claude-sonnet-4-5" {
				expectedModelSet = true
			}
			_ = encoder.Encode(map[string]any{"commandId": commandID, "data": map[string]any{}})
		case "set_thinking_level":
			data, _ := request["data"].(map[string]any)
			if data["thinkingLevel"] == "high" {
				expectedThinkingSet = true
			}
			_ = encoder.Encode(map[string]any{"commandId": commandID, "data": map[string]any{}})
		case "prompt":
			if expected := os.Getenv("GORCHESTRA_FAKE_PI_EXPECT_CONTEXT"); expected != "" {
				data, _ := request["data"].(map[string]any)
				prompt, _ := data["prompt"].(string)
				if !strings.Contains(prompt, "<threave_context>\n"+expected+"\n</threave_context>") || !strings.HasSuffix(prompt, "\n\nhello") {
					_ = encoder.Encode(map[string]any{"commandId": commandID, "error": "unexpected prompt context"})
					continue
				}
			}
			if !expectedModelSet && os.Getenv("GORCHESTRA_FAKE_PI_EXPECT_MODEL") != "" {
				_ = encoder.Encode(map[string]any{"commandId": commandID, "error": "model was not set"})
				continue
			}
			if !expectedThinkingSet && os.Getenv("GORCHESTRA_FAKE_PI_EXPECT_THINKING") != "" {
				_ = encoder.Encode(map[string]any{"commandId": commandID, "error": "thinking was not set"})
				continue
			}
			_ = encoder.Encode(map[string]any{
				"type": "message_update",
				"message": map[string]any{
					"id": "msg_fake",
				},
				"assistantMessageEvent": map[string]any{
					"type":         "text_delta",
					"contentIndex": 0,
					"delta":        "Hello",
				},
			})
			_ = encoder.Encode(map[string]any{"type": "agent_end"})
			_ = encoder.Encode(map[string]any{"commandId": commandID, "data": map[string]any{}})
		case "abort":
			_ = encoder.Encode(map[string]any{"commandId": commandID, "data": map[string]any{}})
		}
	}
}
