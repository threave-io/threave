package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/threave-io/threave/internal/agents"
)

func TestMain(m *testing.M) {
	if payload := os.Getenv("GORCHESTRA_FAKE_CLAUDE_PROCESS_OUTPUT"); payload != "" {
		_, _ = os.Stdout.WriteString(payload)
		return
	}
	if os.Getenv("GORCHESTRA_FAKE_CLAUDE_STREAM") != "" {
		runFakeClaude()
		return
	}
	os.Exit(m.Run())
}

func TestWaitProcessAllowsOutputReadersToDrain(t *testing.T) {
	t.Setenv("GORCHESTRA_FAKE_CLAUDE_PROCESS_OUTPUT", "{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"session_fake\"}\n")
	cmd := exec.Command(os.Args[0])
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	output := startStream(context.Background(), &delayedReader{Reader: stdout, delay: 50 * time.Millisecond}, stderr)
	process := waitProcess(cmd, output.done)
	if _, ok := process.waitTimeout(5 * time.Second); !ok {
		process.kill()
		t.Fatal("process did not finish after output readers drained")
	}

	var event *streamEvent
	for incoming := range output.incoming {
		if incoming.ReadErr != nil {
			t.Fatal(incoming.ReadErr)
		}
		if incoming.Event != nil {
			event = incoming.Event
		}
	}
	if event == nil || event.Type != "result" {
		t.Fatalf("expected final result after process exit, got %#v", event)
	}
}

type delayedReader struct {
	io.Reader
	delay time.Duration
	once  sync.Once
}

func (r *delayedReader) Read(buffer []byte) (int, error) {
	r.once.Do(func() { time.Sleep(r.delay) })
	return r.Reader.Read(buffer)
}

func TestAvailabilityDetection(t *testing.T) {
	agent := New(
		WithBinary("claude-test"),
		WithVersionChecker(func(_ context.Context, binary string) (string, error) {
			if binary != "claude-test" {
				t.Fatalf("expected binary claude-test, got %q", binary)
			}
			return "2.1.128", nil
		}),
	)

	version, err := agent.CheckAvailability(context.Background())
	if err != nil {
		t.Fatalf("check availability: %v", err)
	}
	if version != "2.1.128" {
		t.Fatalf("expected version 2.1.128, got %q", version)
	}
	if err := agent.Available(); err != nil {
		t.Fatalf("expected available agent, got %v", err)
	}
}

func TestAvailabilityDetectionWrapsUnavailable(t *testing.T) {
	agent := New(
		WithBinary("missing-claude"),
		WithVersionChecker(func(context.Context, string) (string, error) {
			return "", errors.New("not found")
		}),
	)

	_, err := agent.CheckAvailability(context.Background())
	if !errors.Is(err, agents.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestCommandConstructionUsesStreamJSONPromptAndResume(t *testing.T) {
	agent := New(WithBinary("/opt/bin/claude"), WithModel("claude-opus-4-7"))
	cmd := agent.command("session_1", "/tmp/workspace")

	if cmd.Path != "/opt/bin/claude" {
		t.Fatalf("expected path /opt/bin/claude, got %q", cmd.Path)
	}
	wantArgs := []string{
		"/opt/bin/claude",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--input-format", "stream-json", "--permission-prompt-tool", "stdio", "-p",
		"--model", "claude-opus-4-7",
		"--permission-mode", "dontAsk",
		"--resume", "session_1",
	}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("expected args %#v, got %#v", wantArgs, cmd.Args)
	}
	if cmd.Dir != "/tmp/workspace" {
		t.Fatalf("expected dir /tmp/workspace, got %q", cmd.Dir)
	}
}

func TestCommandConstructionCanSkipPermissionsDangerously(t *testing.T) {
	agent := New(WithBinary("/opt/bin/claude"))
	cmd := agent.commandWithOptions("", "/tmp/workspace", claudeRunOptions{
		RunDangerously: true,
		Model:          "opus",
		Effort:         "high",
		PermissionMode: "plan",
	})

	wantArgs := []string{
		"/opt/bin/claude",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--input-format", "stream-json", "--permission-prompt-tool", "stdio", "-p",
		"--model", "opus",
		"--effort", "high",
		"--permission-mode", "plan",
		"--allow-dangerously-skip-permissions",
	}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("expected args %#v, got %#v", wantArgs, cmd.Args)
	}
}

func TestRunOptionsFromMetadataReadsDangerousMode(t *testing.T) {
	options := runOptionsFromMetadata(map[string]any{
		"claude_options": map[string]any{
			"run_dangerously": true,
			"model":           "opus",
			"effort":          "high",
			"permission_mode": "plan",
		},
	})

	if !options.RunDangerously {
		t.Fatal("expected run dangerously option")
	}
	if options.Model != "opus" || options.Effort != "high" || options.PermissionMode != "plan" {
		t.Fatalf("unexpected options %#v", options)
	}
}

func TestCommandConstructionEnablesStdioPermissionPrompts(t *testing.T) {
	agent := New(WithBinary("/opt/bin/claude"))
	cmd := agent.commandWithOptions("", "/tmp/workspace", claudeRunOptions{PermissionPolicy: "ask"})
	want := []string{"/opt/bin/claude", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--input-format", "stream-json", "--permission-prompt-tool", "stdio", "-p"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("expected args %#v, got %#v", want, cmd.Args)
	}
}

func TestSessionPermissionSuggestionsNeverPersistProviderSettings(t *testing.T) {
	result := sessionPermissionSuggestions([]any{map[string]any{"type": "addRules", "destination": "userSettings", "rules": []any{}}})
	permission := result[0].(map[string]any)
	if permission["destination"] != "session" {
		t.Fatalf("expected session destination, got %#v", permission)
	}
}

func TestSampleStreamNormalizesExpectedEvents(t *testing.T) {
	events := normalizeLines(t, []string{
		`{"type":"system","subtype":"init","cwd":"/Users/joey/Source/life","session_id":"2fe74369-4b15-49f9-8025-517ed6e52fed","tools":["Task","Bash"],"model":"claude-opus-4-7[1m]"}`,
		`{"type":"system","subtype":"status","status":"requesting","uuid":"4b039800-2856-4239-9254-a943ea47521a","session_id":"2fe74369-4b15-49f9-8025-517ed6e52fed"}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"model":"claude-opus-4-7","id":"msg_01","type":"message","role":"assistant","content":[]}},"session_id":"2fe74369-4b15-49f9-8025-517ed6e52fed","uuid":"e779a1c9-0a9a-4cad-acc3-c970df94042f"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hey"}},"session_id":"2fe74369-4b15-49f9-8025-517ed6e52fed","uuid":"4f64f00f-42ea-4b54-8ff8-3f65b5ad7af7"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Joey, what's up?"}},"session_id":"2fe74369-4b15-49f9-8025-517ed6e52fed","uuid":"29f8a13a-e721-40d9-945c-9c338974af17"}`,
		`{"type":"assistant","message":{"model":"claude-opus-4-7","id":"msg_01","type":"message","role":"assistant","content":[{"type":"text","text":"Hey Joey, what's up?"}]},"session_id":"2fe74369-4b15-49f9-8025-517ed6e52fed","uuid":"8bfd0948-daac-461b-a1af-1e81f6516029"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour"},"uuid":"6f16750f-be14-4330-8211-05f68ed80b56","session_id":"2fe74369-4b15-49f9-8025-517ed6e52fed"}`,
		`{"type":"result","subtype":"success","is_error":false,"duration_ms":4286,"result":"Hey Joey, what's up?","stop_reason":"end_turn","session_id":"2fe74369-4b15-49f9-8025-517ed6e52fed","total_cost_usd":0.0175095}`,
	})

	assertAgentEventTypes(t, events, []string{
		"agent.run.started",
		"provider.claude.event",
		"agent.status.started",
		"agent.message.delta",
		"agent.message.delta",
		"agent.message.completed",
		"provider.claude.event",
		"agent.run.completed",
	})
	assertTerminalCount(t, events, 1)

	startPayload := events[0].Event.Payload.(map[string]any)
	if startPayload["provider_session_id"] != "2fe74369-4b15-49f9-8025-517ed6e52fed" {
		t.Fatalf("expected provider session id, got %#v", startPayload)
	}
	deltaPayload := events[3].Event.Payload.(map[string]any)
	if deltaPayload["text"] != "Hey" {
		t.Fatalf("expected delta text Hey, got %#v", deltaPayload["text"])
	}
	if deltaPayload["message_id"] != "msg_01" {
		t.Fatalf("expected delta message id, got %#v", deltaPayload["message_id"])
	}
	completedPayload := events[5].Event.Payload.(map[string]any)
	if completedPayload["text"] != "Hey Joey, what's up?" {
		t.Fatalf("expected completed text, got %#v", completedPayload["text"])
	}
}

func TestClaudeToolUseNormalizesToolCallEvents(t *testing.T) {
	events := normalizeLines(t, []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_01","type":"message","role":"assistant","content":[]}},"session_id":"session_1","uuid":"uuid_start"}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"toolu_01","name":"Bash","input":{},"caller":{"type":"direct"}},"index":0},"session_id":"session_1","uuid":"uuid_tool_start"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"pwd\""}},"session_id":"session_1","uuid":"uuid_tool_delta"}`,
		`{"type":"assistant","message":{"model":"claude-sonnet-5","id":"msg_01","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"pwd","description":"Print working directory"}}]},"session_id":"session_1","uuid":"uuid_assistant"}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0},"session_id":"session_1","uuid":"uuid_tool_stop"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","is_error":false,"content":"/Users/joey/Source"}]},"tool_use_result":{"stdout":"/Users/joey/Source","stderr":"","interrupted":false},"session_id":"session_1","uuid":"uuid_result"}`,
	})

	assertAgentEventTypes(t, events, []string{
		"agent.status.started",
		"provider.claude.event",
		"tool.call.started",
		"provider.claude.event",
		"agent.message.completed",
		"tool.call.delta",
		"provider.claude.event",
		"provider.claude.event",
		"tool.call.completed",
	})

	startPayload := events[2].Event.Payload.(map[string]any)
	if startPayload["tool_call_id"] != "toolu_01" || startPayload["name"] != "Bash" {
		t.Fatalf("expected bash tool start payload, got %#v", startPayload)
	}

	deltaPayload := events[5].Event.Payload.(map[string]any)
	if deltaPayload["command"] != "pwd" {
		t.Fatalf("expected command in tool delta, got %#v", deltaPayload)
	}
	rawInput, ok := deltaPayload["raw_input"].(map[string]any)
	if !ok || rawInput["description"] != "Print working directory" {
		t.Fatalf("expected raw input in tool delta, got %#v", deltaPayload["raw_input"])
	}

	resultPayload := events[8].Event.Payload.(map[string]any)
	if resultPayload["tool_call_id"] != "toolu_01" || resultPayload["output"] != "/Users/joey/Source" {
		t.Fatalf("expected tool result payload, got %#v", resultPayload)
	}
	if resultPayload["command"] != "pwd" {
		t.Fatalf("expected command copied to tool result, got %#v", resultPayload)
	}
}

func TestClaudeToolUseInputCanBeRecoveredFromStreamDeltas(t *testing.T) {
	events := normalizeLines(t, []string{
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"toolu_read","name":"Read","input":{}},"index":0},"session_id":"session_1"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"/Users/joey/Source/gennari/index.html\"}"}},"session_id":"session_1"}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0},"session_id":"session_1"}`,
	})

	assertAgentEventTypes(t, events, []string{
		"provider.claude.event",
		"tool.call.started",
		"provider.claude.event",
		"provider.claude.event",
		"tool.call.delta",
	})

	payload := events[4].Event.Payload.(map[string]any)
	if payload["path"] != "/Users/joey/Source/gennari/index.html" {
		t.Fatalf("expected read path from stream deltas, got %#v", payload)
	}
}

func TestClaudeThinkingBlocksNormalizeThinkingEvents(t *testing.T) {
	events := normalizeLines(t, []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_01","type":"message","role":"assistant","content":[]}},"session_id":"session_1","uuid":"uuid_start"}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"thinking","thinking":"","signature":""},"index":0},"session_id":"session_1","uuid":"uuid_thinking_start"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Checking the workspace"}},"session_id":"session_1","uuid":"uuid_thinking_delta"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"abc"}},"session_id":"session_1","uuid":"uuid_signature_delta"}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0},"session_id":"session_1","uuid":"uuid_thinking_stop"}`,
	})

	assertAgentEventTypes(t, events, []string{
		"agent.status.started",
		"provider.claude.event",
		"agent.thinking.started",
		"provider.claude.event",
		"agent.thinking.delta",
		"provider.claude.event",
		"provider.claude.event",
		"agent.thinking.completed",
	})

	startPayload := events[2].Event.Payload.(map[string]any)
	if startPayload["item_id"] != "msg_01:thinking:0" || startPayload["message_id"] != "msg_01" {
		t.Fatalf("unexpected thinking start payload %#v", startPayload)
	}
	deltaPayload := events[4].Event.Payload.(map[string]any)
	if deltaPayload["text"] != "Checking the workspace" {
		t.Fatalf("expected thinking delta text, got %#v", deltaPayload)
	}
	completedPayload := events[7].Event.Payload.(map[string]any)
	if completedPayload["item_id"] != "msg_01:thinking:0" || completedPayload["text"] != "Checking the workspace" {
		t.Fatalf("unexpected thinking completed payload %#v", completedPayload)
	}
}

func TestResultErrorNormalizesFailedTerminal(t *testing.T) {
	events := normalizeLines(t, []string{
		`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"max turns exceeded","session_id":"session_1"}`,
	})

	assertAgentEventTypes(t, events, []string{"agent.run.failed"})
	if events[0].Terminal != terminalFailed {
		t.Fatalf("expected failed terminal, got %#v", events[0].Terminal)
	}
	payload := events[0].Event.Payload.(map[string]any)
	if payload["error"] != "max turns exceeded" {
		t.Fatalf("expected result error, got %#v", payload)
	}
}

func TestBackgroundTaskResultDoesNotCompleteHumanRun(t *testing.T) {
	events := normalizeLines(t, []string{
		`{"type":"system","subtype":"task_notification","status":"stopped","session_id":"session_1"}`,
		`{"type":"result","subtype":"success","is_error":false,"duration_ms":35,"origin":{"kind":"task-notification"},"num_turns":1,"terminal_reason":"completed","session_id":"session_1"}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"model":"claude-opus-5","id":"msg_human"}},"session_id":"session_1"}`,
		`{"type":"assistant","message":{"model":"claude-opus-5","id":"msg_human","content":[{"type":"text","text":"Working on it"}]},"session_id":"session_1"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"Working on it","origin":{"kind":"human"},"num_turns":1,"terminal_reason":"completed","session_id":"session_1"}`,
	})

	assertAgentEventTypes(t, events, []string{
		"provider.claude.event",
		"provider.claude.event",
		"agent.status.started",
		"agent.message.completed",
		"agent.run.completed",
	})
	assertTerminalCount(t, events, 1)
	background := events[1].Event.Payload.(map[string]any)
	origin := background["origin"].(map[string]any)
	if origin["kind"] != "task-notification" || background["num_turns"] != 1 || background["terminal_reason"] != "completed" {
		t.Fatalf("expected background result diagnostics, got %#v", background)
	}
}

func TestBackgroundTaskFailureDoesNotFailHumanRun(t *testing.T) {
	events := normalizeLines(t, []string{
		`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"background task failed","origin":{"kind":"task-notification"},"session_id":"session_1"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"Human response","origin":{"kind":"human"},"session_id":"session_1"}`,
	})

	assertAgentEventTypes(t, events, []string{"provider.claude.event", "agent.run.completed"})
	assertTerminalCount(t, events, 1)
	if events[0].Event.Status != "failed" {
		t.Fatalf("expected failed background status, got %#v", events[0].Event)
	}
}

func TestEmptyResultWithoutActivityFailsVisibly(t *testing.T) {
	events := normalizeLines(t, []string{
		`{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"session_1"}`,
		`{"type":"result","subtype":"success","is_error":false,"origin":{"kind":"human"},"session_id":"session_1"}`,
	})

	assertAgentEventTypes(t, events, []string{"agent.run.started", "agent.run.failed"})
	assertTerminalCount(t, events, 1)
	payload := events[1].Event.Payload.(map[string]any)
	if payload["error"] != "claude ended without responding" {
		t.Fatalf("expected visible empty-result error, got %#v", payload)
	}
}

func TestInvalidJSONProducesParseError(t *testing.T) {
	incoming := readStream(context.Background(), strings.NewReader("{"), strings.NewReader(""))
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

func TestAgentRunsFakeClaudeStream(t *testing.T) {
	t.Setenv("GORCHESTRA_FAKE_CLAUDE_STREAM", "1")
	t.Setenv("GORCHESTRA_FAKE_CLAUDE_EXPECT_ENV", "run-only")
	t.Setenv("GORCHESTRA_FAKE_CLAUDE_EXPECT_CONTEXT", "Use the Gorchestra host controls.")
	t.Setenv("GORCHESTRA_AGENT_RUN_TEST_VALUE", "parent")
	agent := fakeClaudeAgent(t)
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

	assertAgentEventTypes(t, recorder.snapshot(), []string{
		"agent.run.started",
		"agent.status.started",
		"agent.message.delta",
		"agent.message.completed",
		"agent.run.completed",
	})
	if got := os.Getenv("GORCHESTRA_AGENT_RUN_TEST_VALUE"); got != "parent" {
		t.Fatalf("expected parent environment to remain unchanged, got %q", got)
	}
}

func TestAgentWaitsPastBackgroundTaskResult(t *testing.T) {
	t.Setenv("GORCHESTRA_FAKE_CLAUDE_STREAM", "1")
	t.Setenv("GORCHESTRA_FAKE_CLAUDE_BACKGROUND_RESULT", "1")
	agent := fakeClaudeAgent(t)
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID: "sess_test",
		Message:   "hello",
		Workdir:   t.TempDir(),
	}, recorder.emit)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}

	assertAgentEventTypes(t, recorder.snapshot(), []string{
		"agent.run.started",
		"provider.claude.event",
		"provider.claude.event",
		"agent.status.started",
		"agent.message.delta",
		"agent.message.completed",
		"agent.run.completed",
	})
}

func normalizeLines(t *testing.T, lines []string) []normalizedEvent {
	t.Helper()
	normalizer := newNormalizer()
	events := make([]normalizedEvent, 0)
	for _, line := range lines {
		event, err := parseStreamEvent([]byte(line))
		if err != nil {
			t.Fatalf("parse fixture line: %v", err)
		}
		events = append(events, normalizer.normalize(event)...)
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

func assertTerminalCount(t *testing.T, events []normalizedEvent, want int) {
	t.Helper()
	got := 0
	for _, event := range events {
		if event.Terminal != terminalNone {
			got++
		}
	}
	if got != want {
		t.Fatalf("expected %d terminal events, got %d", want, got)
	}
}

func fakeClaudeAgent(t *testing.T) *Agent {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	return New(
		WithBinary(binary),
		WithVersionChecker(func(context.Context, string) (string, error) {
			return "2.1.128", nil
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

func runFakeClaude() {
	if expected := os.Getenv("GORCHESTRA_FAKE_CLAUDE_EXPECT_ENV"); expected != "" && os.Getenv("GORCHESTRA_AGENT_RUN_TEST_VALUE") != expected {
		os.Exit(10)
	}
	if os.Getenv("GORCHESTRA_FAKE_CLAUDE_CONTROL") == "ignore_stdin" {
		time.Sleep(time.Minute)
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	var initialize, user map[string]any
	if decoder.Decode(&initialize) != nil || decoder.Decode(&user) != nil {
		os.Exit(20)
	}
	if mapString(initialize, "type") != "control_request" || mapString(user, "type") != "user" {
		os.Exit(21)
	}
	origin, _ := user["origin"].(map[string]any)
	if mapString(origin, "kind") != "human" {
		os.Exit(22)
	}
	if capture := os.Getenv("GORCHESTRA_FAKE_CLAUDE_CAPTURE"); capture != "" {
		data, _ := json.Marshal(user)
		_ = os.WriteFile(capture, data, 0600)
	}
	if expected := os.Getenv("GORCHESTRA_FAKE_CLAUDE_EXPECT_CONTEXT"); expected != "" {
		message, _ := user["message"].(map[string]any)
		prompt := mapString(message, "content")
		if !strings.Contains(prompt, "<threave_context>\n"+expected+"\n</threave_context>") || !strings.HasSuffix(prompt, "\n\nhello") {
			os.Exit(11)
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	_ = encoder.Encode(map[string]any{
		"type":       "system",
		"subtype":    "init",
		"session_id": "session_fake",
	})
	if os.Getenv("GORCHESTRA_FAKE_CLAUDE_BACKGROUND_RESULT") != "" {
		_ = encoder.Encode(map[string]any{
			"type":       "system",
			"subtype":    "task_notification",
			"status":     "stopped",
			"session_id": "session_fake",
		})
		_ = encoder.Encode(map[string]any{
			"type":        "result",
			"subtype":     "success",
			"is_error":    false,
			"duration_ms": 35,
			"origin":      map[string]any{"kind": "task-notification"},
			"session_id":  "session_fake",
		})
	}
	if scenario := os.Getenv("GORCHESTRA_FAKE_CLAUDE_CONTROL"); scenario != "" {
		runFakeClaudeControl(scenario, decoder, encoder)
	}
	_ = encoder.Encode(map[string]any{
		"type":       "stream_event",
		"session_id": "session_fake",
		"event": map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":    "msg_fake",
				"model": "claude-test",
			},
		},
	})
	_ = encoder.Encode(map[string]any{
		"type":       "stream_event",
		"session_id": "session_fake",
		"event": map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": "Hello",
			},
		},
	})
	_ = encoder.Encode(map[string]any{
		"type":       "assistant",
		"session_id": "session_fake",
		"message": map[string]any{
			"id":    "msg_fake",
			"model": "claude-test",
			"content": []map[string]any{
				{"type": "text", "text": "Hello"},
			},
		},
	})
	_ = encoder.Encode(map[string]any{
		"type":       "result",
		"subtype":    "success",
		"is_error":   false,
		"result":     "Hello",
		"session_id": "session_fake",
	})
}
