package opencode

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
	if os.Getenv("GORCHESTRA_FAKE_OPENCODE_ACP") != "" {
		runFakeOpenCode()
		return
	}
	os.Exit(m.Run())
}

func TestAvailabilityDetection(t *testing.T) {
	agent := New(
		WithBinary("opencode-test"),
		WithVersionChecker(func(_ context.Context, binary string) (string, error) {
			if binary != "opencode-test" {
				t.Fatalf("expected binary opencode-test, got %q", binary)
			}
			return "1.17.13", nil
		}),
	)

	version, err := agent.CheckAvailability(context.Background())
	if err != nil {
		t.Fatalf("check availability: %v", err)
	}
	if version != "1.17.13" {
		t.Fatalf("expected version 1.17.13, got %q", version)
	}
	if err := agent.Available(); err != nil {
		t.Fatalf("expected available agent, got %v", err)
	}
}

func TestAvailabilityDetectionWrapsUnavailable(t *testing.T) {
	agent := New(
		WithBinary("missing-opencode"),
		WithVersionChecker(func(context.Context, string) (string, error) {
			return "", errors.New("not found")
		}),
	)

	_, err := agent.CheckAvailability(context.Background())
	if !errors.Is(err, agents.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestCommandConstructionUsesACPWithCWD(t *testing.T) {
	agent := New(WithBinary("/opt/bin/opencode"))
	cmd := agent.command("/tmp/workspace")

	if cmd.Path != "/opt/bin/opencode" {
		t.Fatalf("expected path /opt/bin/opencode, got %q", cmd.Path)
	}
	wantArgs := []string{"/opt/bin/opencode", "acp", "--cwd", "/tmp/workspace"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("expected args %#v, got %#v", wantArgs, cmd.Args)
	}
	if cmd.Dir != "/tmp/workspace" {
		t.Fatalf("expected dir /tmp/workspace, got %q", cmd.Dir)
	}
}

func TestNormalizeModelListDefaultsToNonOpenCodeModel(t *testing.T) {
	options := normalizeModelList(strings.Join([]string{
		"opencode/big-pickle",
		"openai/gpt-5.4-mini",
		"opencode/mimo-v2.5-free",
		"",
	}, "\n"))

	if options.DefaultModel != "openai/gpt-5.4-mini" {
		t.Fatalf("expected non-opencode default model, got %q", options.DefaultModel)
	}
	if len(options.Models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(options.Models))
	}
	if options.Models[1].DisplayName != "OpenAI/GPT 5.4 Mini" {
		t.Fatalf("unexpected display name %#v", options.Models[1])
	}
	if !options.Models[1].IsDefault {
		t.Fatalf("expected OpenAI model to be default, got %#v", options.Models)
	}
}

func TestSampleACPUpdatesNormalizeExpectedEvents(t *testing.T) {
	events := normalizeMessages(t, []string{
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_test","update":{"sessionUpdate":"agent_thought_chunk","messageId":"msg_1","content":{"type":"text","text":"Thinking"}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_test","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_1","content":{"type":"text","text":"Hello"}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_test","update":{"sessionUpdate":"usage_update","used":10,"size":200000,"cost":{"amount":0,"currency":"USD"}}}}`,
	})

	assertAgentEventTypes(t, events, []string{
		"agent.thinking.delta",
		"agent.message.delta",
		"provider.opencode.event",
	})
	thoughtPayload := events[0].Event.Payload.(map[string]any)
	if thoughtPayload["text"] != "Thinking" || thoughtPayload["provider_session_id"] != "ses_test" {
		t.Fatalf("unexpected thought payload %#v", thoughtPayload)
	}
	messagePayload := events[1].Event.Payload.(map[string]any)
	if messagePayload["text"] != "Hello" || messagePayload["message_id"] != "msg_1" {
		t.Fatalf("unexpected message payload %#v", messagePayload)
	}
}

func TestOpenCodeMessageChunksPreserveLeadingWhitespace(t *testing.T) {
	events := normalizeMessages(t, []string{
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_test","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_1","content":{"type":"text","text":" assist"}}}}`,
	})

	payload := events[0].Event.Payload.(map[string]any)
	if payload["text"] != " assist" {
		t.Fatalf("expected leading whitespace to be preserved, got %#v", payload["text"])
	}
}

func TestAgentRunsFakeOpenCodeACP(t *testing.T) {
	t.Setenv("GORCHESTRA_FAKE_OPENCODE_ACP", "1")
	t.Setenv("GORCHESTRA_FAKE_OPENCODE_EXPECT_ENV", "run-only")
	t.Setenv("GORCHESTRA_FAKE_OPENCODE_EXPECT_CONTEXT", "Use the Gorchestra host controls.")
	t.Setenv("GORCHESTRA_AGENT_RUN_TEST_VALUE", "parent")
	agent := fakeOpenCodeAgent(t)
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID:   "sess_test",
		Message:     "hello",
		Workdir:     t.TempDir(),
		Environment: map[string]string{"GORCHESTRA_AGENT_RUN_TEST_VALUE": "run-only"},
		Context:     "Use the Gorchestra host controls.",
	}, recorder.emit)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}

	events := recorder.snapshot()
	assertAgentEventTypes(t, events, []string{
		"agent.run.started",
		"agent.message.delta",
		"agent.message.completed",
		"agent.run.completed",
	})
	startPayload := events[0].Event.Payload.(map[string]any)
	if startPayload["provider_session_id"] != "ses_fake" {
		t.Fatalf("expected provider session id ses_fake, got %#v", startPayload)
	}
	completedPayload := events[2].Event.Payload.(map[string]any)
	if completedPayload["text"] != "Hello" {
		t.Fatalf("expected completed message snapshot, got %#v", completedPayload)
	}
	if got := os.Getenv("GORCHESTRA_AGENT_RUN_TEST_VALUE"); got != "parent" {
		t.Fatalf("expected parent environment to remain unchanged, got %q", got)
	}
}

func TestAgentResumesExistingOpenCodeSession(t *testing.T) {
	t.Setenv("GORCHESTRA_FAKE_OPENCODE_ACP", "1")
	agent := fakeOpenCodeAgent(t)
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID:         "sess_test",
		ProviderSessionID: "ses_existing",
		Message:           "hello",
		Workdir:           t.TempDir(),
	}, recorder.emit)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}

	events := recorder.snapshot()
	startPayload := events[0].Event.Payload.(map[string]any)
	if startPayload["provider_event_type"] != "session/resume" || startPayload["provider_session_id"] != "ses_existing" {
		t.Fatalf("expected resumed provider session, got %#v", startPayload)
	}
}

func TestAgentSendsSelectedModelBeforePrompt(t *testing.T) {
	t.Setenv("GORCHESTRA_FAKE_OPENCODE_ACP", "1")
	t.Setenv("GORCHESTRA_FAKE_OPENCODE_EXPECT_MODEL", "openai/gpt-5.4-mini")
	agent := fakeOpenCodeAgent(t)
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID: "sess_test",
		Message:   "hello",
		Workdir:   t.TempDir(),
		Metadata: map[string]any{
			"opencode_options": map[string]any{
				"model": "openai/gpt-5.4-mini",
			},
		},
	}, recorder.emit)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}
}

func TestInvalidJSONProducesParseError(t *testing.T) {
	incoming := readACP(strings.NewReader("{"), strings.NewReader(""))
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
		events = append(events, normalizer.normalize(message.Method, message.Params)...)
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

func fakeOpenCodeAgent(t *testing.T) *Agent {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	return New(
		WithBinary(binary),
		WithVersionChecker(func(context.Context, string) (string, error) {
			return "1.17.13", nil
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

func runFakeOpenCode() {
	if expected := os.Getenv("GORCHESTRA_FAKE_OPENCODE_EXPECT_ENV"); expected != "" && os.Getenv("GORCHESTRA_AGENT_RUN_TEST_VALUE") != expected {
		os.Exit(10)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	expectedModel := os.Getenv("GORCHESTRA_FAKE_OPENCODE_EXPECT_MODEL")
	modelSet := expectedModel == ""
	for scanner.Scan() {
		var request map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		id := request["id"]
		switch request["method"] {
		case "initialize":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": 1,
					"agentCapabilities": map[string]any{
						"loadSession": true,
						"sessionCapabilities": map[string]any{
							"resume": map[string]any{},
						},
					},
					"agentInfo": map[string]any{"name": "OpenCode", "version": "1.17.13"},
				},
			})
		case "session/new":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"sessionId": "ses_fake"},
			})
		case "session/resume":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{},
			})
		case "session/set_model":
			params, _ := request["params"].(map[string]any)
			if expectedModel != "" && params["modelId"] != expectedModel {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"error": map[string]any{
						"code":    -32602,
						"message": "unexpected model",
					},
				})
				continue
			}
			modelSet = true
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{},
			})
		case "session/set_mode":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{},
			})
		case "session/prompt":
			if expected := os.Getenv("GORCHESTRA_FAKE_OPENCODE_EXPECT_CONTEXT"); expected != "" {
				prompt := fakeOpenCodePromptText(request)
				if !strings.Contains(prompt, "<threave_context>\n"+expected+"\n</threave_context>") || !strings.HasSuffix(prompt, "\n\nhello") {
					os.Exit(11)
				}
			}
			if !modelSet {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"error": map[string]any{
						"code":    -32603,
						"message": "model was not set",
					},
				})
				continue
			}
			sessionID := "ses_fake"
			if params, ok := request["params"].(map[string]any); ok {
				if value, ok := params["sessionId"].(string); ok && value != "" {
					sessionID = value
				}
			}
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": sessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"messageId":     "msg_fake",
						"content":       map[string]any{"type": "text", "text": "Hello"},
					},
				},
			})
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"stopReason": "end_turn"},
			})
		}
	}
}

func fakeOpenCodePromptText(request map[string]any) string {
	params, _ := request["params"].(map[string]any)
	prompt, _ := params["prompt"].([]any)
	for _, item := range prompt {
		content, _ := item.(map[string]any)
		if content["type"] == "text" {
			text, _ := content["text"].(string)
			return text
		}
	}
	return ""
}

func TestPermissionOptionsPreserveACPIDsAndScopes(t *testing.T) {
	options := permissionOptions(json.RawMessage(`{"options":[{"optionId":"allow-once","name":"Allow once","kind":"allow_once"},{"optionId":"allow-always","name":"Always allow","kind":"allow_always"},{"optionId":"reject-once","name":"Reject","kind":"reject_once"}]}`))
	if got := []string{options[0].ID, options[1].ID, options[2].ID}; !reflect.DeepEqual(got, []string{"allow-once", "allow-always", "reject-once"}) {
		t.Fatalf("unexpected ids %#v", got)
	}
	if options[1].Scope != "session" || options[2].Decision != "deny" {
		t.Fatalf("unexpected options %#v", options)
	}
}
