package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/threave-io/threave/internal/agents"
)

func TestMain(m *testing.M) {
	if payload := os.Getenv("GORCHESTRA_FAKE_CODEX_PROCESS_OUTPUT"); payload != "" {
		_, _ = os.Stdout.WriteString(payload)
		return
	}
	if mode := os.Getenv("GORCHESTRA_FAKE_CODEX_APP_SERVER"); mode != "" {
		recordFakeAppServerStart()
		runFakeAppServer(mode)
		return
	}
	os.Exit(m.Run())
}

func TestWaitProcessAllowsOutputReadersToDrain(t *testing.T) {
	t.Setenv("GORCHESTRA_FAKE_CODEX_PROCESS_OUTPUT", "{\"id\":1,\"result\":{}}\n")
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

	output := startAppServerOutput(context.Background(), &delayedReader{Reader: stdout, delay: 50 * time.Millisecond}, stderr)
	process := waitProcess(cmd, output.done)
	if _, ok := process.waitTimeout(5 * time.Second); !ok {
		process.kill()
		t.Fatal("process did not finish after output readers drained")
	}

	var response *rpcMessage
	for incoming := range output.incoming {
		if incoming.ReadErr != nil {
			t.Fatal(incoming.ReadErr)
		}
		if incoming.Message != nil {
			response = incoming.Message
		}
	}
	if response == nil || response.idKey() != "1" {
		t.Fatalf("expected final response after process exit, got %#v", response)
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
		WithBinary("codex-test"),
		WithVersionChecker(func(_ context.Context, binary string) (string, error) {
			if binary != "codex-test" {
				t.Fatalf("expected binary codex-test, got %q", binary)
			}
			return "codex-cli 0.test", nil
		}),
	)

	version, err := agent.CheckAvailability(context.Background())
	if err != nil {
		t.Fatalf("check availability: %v", err)
	}
	if version != "codex-cli 0.test" {
		t.Fatalf("expected version codex-cli 0.test, got %q", version)
	}
	if err := agent.Available(); err != nil {
		t.Fatalf("expected available agent, got %v", err)
	}
}

func TestAvailabilityDetectionWrapsUnavailable(t *testing.T) {
	agent := New(
		WithBinary("missing-codex"),
		WithVersionChecker(func(context.Context, string) (string, error) {
			return "", errors.New("not found")
		}),
	)

	_, err := agent.CheckAvailability(context.Background())
	if !errors.Is(err, agents.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if !errors.Is(agent.Available(), agents.ErrUnavailable) {
		t.Fatalf("expected Available to return ErrUnavailable, got %v", agent.Available())
	}
}

func TestCommandConstructionUsesExplicitArgsAndWorkdir(t *testing.T) {
	agent := New(WithBinary("/opt/bin/codex"))
	cmd := agent.command("/tmp/workspace")

	if cmd.Path != "/opt/bin/codex" {
		t.Fatalf("expected path /opt/bin/codex, got %q", cmd.Path)
	}
	wantArgs := []string{"/opt/bin/codex", "app-server", "--stdio", "-c", `web_search="live"`}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("expected args %#v, got %#v", wantArgs, cmd.Args)
	}
	if cmd.Dir != "/tmp/workspace" {
		t.Fatalf("expected dir /tmp/workspace, got %q", cmd.Dir)
	}
}

func TestCommandConstructionCanOverrideWebSearchMode(t *testing.T) {
	agent := New(WithBinary("/opt/bin/codex"), WithWebSearchMode("cached"))
	cmd := agent.command("/tmp/workspace")

	wantArgs := []string{"/opt/bin/codex", "app-server", "--stdio", "-c", `web_search="cached"`}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("expected args %#v, got %#v", wantArgs, cmd.Args)
	}
}

func TestSuccessFixtureNormalizesExpectedEvents(t *testing.T) {
	events := normalizeFixture(t, "success.jsonl")
	assertAgentEventTypes(t, events, []string{
		"agent.run.started",
		"agent.status.started",
		"agent.message.delta",
		"agent.message.completed",
		"agent.run.completed",
	})
	assertTerminalCount(t, events, 1)

	payload := events[2].Payload.(map[string]any)
	if payload["text"] != "Hello" {
		t.Fatalf("expected delta text Hello, got %#v", payload["text"])
	}
	if payload["provider_event_type"] != "item/agentMessage/delta" {
		t.Fatalf("expected provider event type item/agentMessage/delta, got %#v", payload["provider_event_type"])
	}
}

func TestReasoningItemStartedNormalizesThinkingStarted(t *testing.T) {
	events := newNormalizer().normalize("item/started", json.RawMessage(`{
		"threadId":"thread_1",
		"turnId":"turn_1",
		"item":{"type":"reasoning","id":"rs_1","status":"inProgress"}
	}`))

	if len(events) != 1 {
		t.Fatalf("expected one event, got %#v", events)
	}
	if events[0].Event.Type != "agent.thinking.started" {
		t.Fatalf("expected thinking started event, got %q", events[0].Event.Type)
	}
	payload, ok := events[0].Event.Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected payload map, got %#v", events[0].Event.Payload)
	}
	if payload["item_id"] != "rs_1" || payload["provider_event_type"] != "item/started" {
		t.Fatalf("expected reasoning item payload, got %#v", payload)
	}
}

func TestErrorNotificationWaitsForTurnCompleted(t *testing.T) {
	normalizer := newNormalizer()
	events := normalizer.normalize("error", json.RawMessage(`{
		"threadId":"thread_1",
		"turnId":"turn_1",
		"willRetry":false,
		"error":{
			"message":"response stream disconnected",
			"additionalDetails":"websocket closed before response.completed",
			"codexErrorInfo":{"type":"ResponseStreamDisconnected"}
		}
	}`))

	if len(events) != 1 {
		t.Fatalf("expected one error event, got %#v", events)
	}
	if events[0].Event.Type != "agent.log.delta" || events[0].Event.Status != "failed" {
		t.Fatalf("expected nonterminal failed log event, got %#v", events[0])
	}
	if events[0].Terminal != terminalNone || normalizer.terminal {
		t.Fatalf("expected error notification to remain nonterminal, got %#v", events[0])
	}
	payload, ok := events[0].Event.Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected payload map, got %#v", events[0].Event.Payload)
	}
	if payload["error"] != "response stream disconnected" || payload["will_retry"] != false {
		t.Fatalf("expected error and retry metadata, got %#v", payload)
	}
	if payload["details"] != "websocket closed before response.completed" || payload["codex_error_info"] == nil {
		t.Fatalf("expected Codex error details, got %#v", payload)
	}

	terminalEvents := normalizer.normalize("turn/completed", json.RawMessage(`{
		"threadId":"thread_1",
		"turn":{"id":"turn_1","status":"failed","error":{"message":"response stream disconnected"}}
	}`))
	if len(terminalEvents) != 1 || terminalEvents[0].Event.Type != "agent.run.failed" {
		t.Fatalf("expected turn/completed to fail the run, got %#v", terminalEvents)
	}
	if terminalEvents[0].Terminal != terminalFailed || !normalizer.terminal {
		t.Fatalf("expected terminal failure from turn/completed, got %#v", terminalEvents[0])
	}
}

func TestCommandFixtureNormalizesToolEvents(t *testing.T) {
	events := normalizeFixture(t, "command.jsonl")
	assertAgentEventTypes(t, events, []string{
		"tool.call.started",
		"tool.call.delta",
		"tool.call.completed",
	})

	payload := events[2].Payload.(map[string]any)
	if payload["command"] != "go test ./..." {
		t.Fatalf("expected command payload, got %#v", payload["command"])
	}
	if payload["exit_code"] != float64(0) {
		t.Fatalf("expected exit_code 0, got %#v", payload["exit_code"])
	}
}

func TestMCPToolCallNormalizesArgumentsAndStructuredOutput(t *testing.T) {
	normalizer := newNormalizer()
	started := normalizer.normalize("item/started", json.RawMessage(`{
		"threadId":"thread_1",
		"turnId":"turn_1",
		"item":{
			"type":"mcpToolCall",
			"id":"tool_1",
			"server":"life",
			"tool":"exec_command",
			"status":"inProgress",
			"arguments":{"command":"go test ./...","cwd":"/repo"}
		}
	}`))
	if len(started) != 1 {
		t.Fatalf("expected one started event, got %#v", started)
	}
	startedPayload := started[0].Event.Payload.(map[string]any)
	if startedPayload["command"] != "go test ./..." || startedPayload["cwd"] != "/repo" {
		t.Fatalf("expected canonical command arguments, got %#v", startedPayload)
	}
	if startedPayload["raw_input"] == nil {
		t.Fatalf("expected preserved raw input, got %#v", startedPayload)
	}

	completed := normalizer.normalize("item/completed", json.RawMessage(`{
		"threadId":"thread_1",
		"turnId":"turn_1",
		"item":{
			"type":"mcpToolCall",
			"id":"tool_1",
			"server":"life",
			"tool":"exec_command",
			"status":"completed",
			"arguments":{"command":"go test ./...","cwd":"/repo"},
			"result":{
				"content":[{"type":"text","text":"{\"output\":\"ok\\n\"}"}],
				"structuredContent":{"status":"completed","output":"ok\n"}
			}
		}
	}`))
	if len(completed) != 1 {
		t.Fatalf("expected one completed event, got %#v", completed)
	}
	completedPayload := completed[0].Event.Payload.(map[string]any)
	if completedPayload["output"] != "ok\n" || completedPayload["aggregated_output"] != "ok\n" {
		t.Fatalf("expected canonical structured output, got %#v", completedPayload)
	}
	if completedPayload["result"] == nil {
		t.Fatalf("expected original result to remain available, got %#v", completedPayload)
	}
}

func TestMCPToolCallNormalizesTextStructuredAndErrorResults(t *testing.T) {
	tests := []struct {
		name   string
		result any
		want   string
	}{
		{
			name: "content blocks",
			result: map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "first"},
					map[string]any{"type": "resource", "resource": map[string]any{"text": "second"}},
				},
			},
			want: "first\nsecond",
		},
		{
			name:   "arbitrary structured content",
			result: map[string]any{"structuredContent": map[string]any{"items": []any{"one", "two"}}},
			want:   "{\n  \"items\": [\n    \"one\",\n    \"two\"\n  ]\n}",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := toolResultText(test.result); got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}

	payload := map[string]any{}
	canonicalizeToolItem(payload, map[string]any{
		"error": map[string]any{"message": "tool unavailable", "code": float64(-32000)},
	})
	if payload["error"] != "tool unavailable" || payload["raw_error"] == nil {
		t.Fatalf("expected canonical and raw error payloads, got %#v", payload)
	}
}

func TestPlanFixtureNormalizesPlanEvents(t *testing.T) {
	events := normalizeFixture(t, "plan.jsonl")
	assertAgentEventTypes(t, events, []string{
		"agent.plan.delta",
		"agent.plan.delta",
		"agent.plan.completed",
	})

	payload := events[2].Payload.(map[string]any)
	if payload["item_type"] != "plan" {
		t.Fatalf("expected plan item type, got %#v", payload["item_type"])
	}
	if payload["text"] != "# Plan\n- Check the current transcript\n" {
		t.Fatalf("expected completed plan text, got %#v", payload["text"])
	}
}

func TestUnknownFixtureNormalizesProviderEvent(t *testing.T) {
	events := normalizeFixture(t, "unknown.jsonl")
	assertAgentEventTypes(t, events, []string{"provider.codex.event"})

	payload := events[0].Payload.(map[string]any)
	if payload["provider_event_type"] != "thread/compacted" {
		t.Fatalf("expected provider event type thread/compacted, got %#v", payload["provider_event_type"])
	}
	if payload["raw"] == nil {
		t.Fatal("expected raw payload")
	}
}

func TestTokenUsageNotificationIncludesNormalizedSessionUsage(t *testing.T) {
	normalizer := newNormalizer()
	events := normalizer.normalize("thread/tokenUsage/updated", json.RawMessage(`{
		"threadId":"thread_1",
		"tokenUsage":{"total":{"totalTokens":12345}}
	}`))

	if len(events) != 1 || events[0].Event.Type != "provider.codex.event" {
		t.Fatalf("expected one provider token event, got %#v", events)
	}
	payload := events[0].Event.Payload.(map[string]any)
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		t.Fatalf("expected normalized usage payload, got %#v", payload)
	}
	if usage["context_id"] != "thread_1" || usage["total_tokens"] != float64(12345) {
		t.Fatalf("unexpected normalized usage payload: %#v", usage)
	}
	if payload["raw"] == nil {
		t.Fatal("expected raw token payload to remain available")
	}
}

func TestInvalidJSONRPCProducesParseError(t *testing.T) {
	data, err := os.ReadFile("testdata/invalid.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	incoming := readAppServer(strings.NewReader(string(data)), strings.NewReader(""))
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

func TestReadAppServerAcceptsLargeResponseWithoutRetainingRaw(t *testing.T) {
	var line bytes.Buffer
	line.WriteString(`{"jsonrpc":"2.0","id":"2","result":{"thread":{"id":"thread_large"},"images":["data:image/png;base64,`)
	line.WriteString(strings.Repeat("a", 11*1024*1024))
	line.WriteString(`"]}}` + "\n")

	incoming := readAppServer(bytes.NewReader(line.Bytes()), strings.NewReader(""))
	message, ok := <-incoming
	if !ok {
		t.Fatal("expected large response message")
	}
	if message.ReadErr != nil {
		t.Fatalf("expected large response to be readable, got read error %v", message.ReadErr)
	}
	if message.ParseErr != nil {
		t.Fatalf("expected large response to parse, got parse error %v", message.ParseErr)
	}
	if message.Message == nil {
		t.Fatalf("expected parsed message, got %#v", message)
	}
	if got := message.Message.idKey(); got != "2" {
		t.Fatalf("expected response id 2, got %q", got)
	}
	if got := stringAt(message.Message.Result, "thread", "id"); got != "thread_large" {
		t.Fatalf("expected thread id thread_large, got %q", got)
	}
	if len(message.Message.Raw) != 0 {
		t.Fatalf("expected responses not to retain raw bytes, got %d bytes", len(message.Message.Raw))
	}
}

func TestAgentRunsFakeAppServerSuccess(t *testing.T) {
	t.Setenv("GORCHESTRA_FAKE_CODEX_EXPECT_ENV", "run-only")
	t.Setenv("GORCHESTRA_FAKE_CODEX_EXPECT_CONTEXT", "Use the Gorchestra host controls.")
	t.Setenv("GORCHESTRA_AGENT_RUN_TEST_VALUE", "parent")
	agent := fakeAppServerAgent(t, "success")
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID:   "sess_test",
		Message:     "Say hello",
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

func TestAgentContinuesAfterRetryableError(t *testing.T) {
	agent := fakeAppServerAgent(t, "retry-success")
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID: "sess_test",
		Message:   "Say hello",
		Workdir:   t.TempDir(),
	}, recorder.emit)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}

	events := recorder.snapshot()
	assertAgentEventTypes(t, events, []string{
		"agent.run.started",
		"agent.status.started",
		"agent.log.delta",
		"agent.message.delta",
		"agent.message.completed",
		"agent.run.completed",
	})
	assertTerminalCount(t, events, 1)
	payload, ok := events[2].Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected retry payload map, got %#v", events[2].Payload)
	}
	if payload["will_retry"] != true || payload["error"] != "Reconnecting... 2/5" {
		t.Fatalf("expected retryable error metadata, got %#v", payload)
	}
}

func TestAgentResumesExistingProviderSession(t *testing.T) {
	agent := fakeAppServerAgent(t, "success")
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID:         "sess_test",
		ProviderSessionID: "thread_fake",
		Message:           "Continue",
		Workdir:           t.TempDir(),
	}, recorder.emit)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}

	events := recorder.snapshot()
	assertAgentEventTypes(t, events, []string{
		"agent.run.started",
		"agent.status.started",
		"agent.message.delta",
		"agent.message.completed",
		"agent.run.completed",
	})
	payload, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected run started payload, got %#v", events[0].Payload)
	}
	if payload["provider_event_type"] != "thread/resume" {
		t.Fatalf("expected thread/resume provider event, got %#v", payload["provider_event_type"])
	}
}

func TestAgentRejectsDirectClearAction(t *testing.T) {
	agent := fakeAppServerAgent(t, "success")
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID:         "sess_test",
		ProviderSessionID: "thread_old",
		Action:            agents.AgentActionClear,
		Workdir:           t.TempDir(),
	}, recorder.emit)
	if err == nil || !strings.Contains(err.Error(), "clear is handled by session orchestration") {
		t.Fatalf("expected direct clear rejection, got %v", err)
	}
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("expected no events, got %#v", events)
	}
}

func TestAgentCompactsExistingProviderSession(t *testing.T) {
	agent := fakeAppServerAgent(t, "compact")
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID:         "sess_test",
		ProviderSessionID: "thread_fake",
		Action:            agents.AgentActionCompact,
		Workdir:           t.TempDir(),
	}, recorder.emit)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}

	events := recorder.snapshot()
	assertAgentEventTypes(t, events, []string{
		"agent.run.started",
		"agent.status.started",
		"provider.codex.event",
		"agent.run.completed",
	})
	payload, ok := events[2].Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected compact provider payload, got %#v", events[2].Payload)
	}
	if payload["provider_event_type"] != "thread/compacted" {
		t.Fatalf("expected thread/compacted provider event, got %#v", payload["provider_event_type"])
	}
	assertTerminalCount(t, events, 1)
}

func TestAgentOptionsProbeNormalizesCodexOptions(t *testing.T) {
	agent := fakeAppServerAgent(t, "options")

	options, err := agent.Options(context.Background())
	if err != nil {
		t.Fatalf("load options: %v", err)
	}

	if options.DefaultModel != "gpt-5.5" {
		t.Fatalf("expected default model gpt-5.5, got %q", options.DefaultModel)
	}
	if len(options.Models) != 1 {
		t.Fatalf("expected one model, got %#v", options.Models)
	}
	model := options.Models[0]
	if model.DisplayName != "GPT-5.5" || model.DefaultReasoningEffort != "medium" {
		t.Fatalf("unexpected model option %#v", model)
	}
	if len(model.ServiceTiers) != 1 || model.ServiceTiers[0].ID != "priority" {
		t.Fatalf("expected priority service tier, got %#v", model.ServiceTiers)
	}
	if len(options.CollaborationModes) != 2 || options.CollaborationModes[0].Mode != "plan" {
		t.Fatalf("expected collaboration modes, got %#v", options.CollaborationModes)
	}
}

func TestAgentOptionsAreCached(t *testing.T) {
	countPath := filepath.Join(t.TempDir(), "starts")
	t.Setenv("GORCHESTRA_FAKE_CODEX_APP_SERVER_START_COUNT", countPath)
	agent := fakeAppServerAgent(t, "options")

	options, err := agent.Options(context.Background())
	if err != nil {
		t.Fatalf("load options: %v", err)
	}
	options.Models[0].Model = "mutated"

	cached, err := agent.Options(context.Background())
	if err != nil {
		t.Fatalf("load cached options: %v", err)
	}

	if starts := fakeAppServerStartCount(t, countPath); starts != 1 {
		t.Fatalf("expected one fake app-server start, got %d", starts)
	}
	if cached.Models[0].Model != "gpt-5.5" {
		t.Fatalf("expected cached options to be cloned, got %#v", cached.Models[0])
	}
}

func TestAgentOptionsCacheExpires(t *testing.T) {
	countPath := filepath.Join(t.TempDir(), "starts")
	t.Setenv("GORCHESTRA_FAKE_CODEX_APP_SERVER_START_COUNT", countPath)
	agent := fakeAppServerAgent(t, "options", WithOptionsCacheTTL(time.Nanosecond))

	if _, err := agent.Options(context.Background()); err != nil {
		t.Fatalf("load options: %v", err)
	}
	time.Sleep(time.Millisecond)
	if _, err := agent.Options(context.Background()); err != nil {
		t.Fatalf("reload options: %v", err)
	}

	if starts := fakeAppServerStartCount(t, countPath); starts != 2 {
		t.Fatalf("expected cache expiry to reload options, got %d starts", starts)
	}
}

func TestAgentSkillsListsWorkspaceCatalogAndMetadata(t *testing.T) {
	workdir := t.TempDir()
	agent := fakeAppServerAgent(t, "skills")

	catalog, err := agent.Skills(context.Background(), agents.SkillQuery{
		Workdir:     workdir,
		ForceReload: true,
	})
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}

	wantSkills := []agents.Skill{
		{
			Name:             "openai-docs",
			Description:      "Use official OpenAI documentation.",
			DisplayName:      "OpenAI Docs",
			ShortDescription: "Official product guidance",
			BrandColor:       "#10A37F",
			Path:             "/skills/user/openai-docs/SKILL.md",
			Scope:            "user",
			Enabled:          true,
		},
		{
			Name:             "openai-docs",
			Description:      "Repository-specific OpenAI workflow.",
			ShortDescription: "Repository workflow",
			Path:             "/workspace/.agents/skills/openai-docs/SKILL.md",
			Scope:            "repo",
			Enabled:          false,
		},
	}
	if !reflect.DeepEqual(catalog.Skills, wantSkills) {
		t.Fatalf("unexpected skill catalog: %#v", catalog.Skills)
	}
	wantErrors := []agents.SkillError{{Path: "/broken/SKILL.md", Message: "invalid frontmatter"}}
	if !reflect.DeepEqual(catalog.Errors, wantErrors) {
		t.Fatalf("unexpected skill errors: %#v", catalog.Errors)
	}
	if catalog.Revision == "" {
		t.Fatal("expected skill catalog revision")
	}
}

func TestAgentSkillsCachesByWorkspaceUntilForced(t *testing.T) {
	countPath := filepath.Join(t.TempDir(), "starts")
	t.Setenv("GORCHESTRA_FAKE_CODEX_APP_SERVER_START_COUNT", countPath)
	agent := fakeAppServerAgent(t, "skills")
	workdir := t.TempDir()

	first, err := agent.Skills(context.Background(), agents.SkillQuery{Workdir: workdir, ForceReload: true})
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	first.Skills[0].Name = "mutated"
	second, err := agent.Skills(context.Background(), agents.SkillQuery{Workdir: workdir})
	if err != nil {
		t.Fatalf("list cached skills: %v", err)
	}
	if second.Skills[0].Name == "mutated" {
		t.Fatal("expected cached skill catalog to be cloned")
	}
	if starts := fakeAppServerStartCount(t, countPath); starts != 1 {
		t.Fatalf("expected one app-server probe for cached catalog, got %d", starts)
	}
	if _, err := agent.Skills(context.Background(), agents.SkillQuery{Workdir: workdir, ForceReload: true}); err != nil {
		t.Fatalf("force reload skills: %v", err)
	}
	if starts := fakeAppServerStartCount(t, countPath); starts != 2 {
		t.Fatalf("expected force reload to start a second probe, got %d", starts)
	}
}

func TestStartTurnAppliesRunOptions(t *testing.T) {
	var written bytes.Buffer
	incoming := make(chan incomingMessage, 1)
	incoming <- incomingMessage{Message: &rpcMessage{
		ID:     json.RawMessage(`1`),
		Result: json.RawMessage(`{"turn":{"id":"turn_fake","status":"inProgress"}}`),
	}}

	run := &appServerRun{
		agent:    New(WithModel("gpt-default")),
		rpc:      newRPCClient(bufferWriteCloser{Buffer: &written}),
		incoming: incoming,
		process:  &processState{done: make(chan struct{})},
		emit: func(context.Context, agents.AgentEvent) error {
			return nil
		},
		normalizer: newNormalizer(),
		skills: []agents.SkillReference{
			{Name: "openai-docs", Path: "/skills/openai-docs/SKILL.md"},
		},
		options: codexRunOptions{
			Model:           "gpt-5.5",
			ReasoningEffort: "xhigh",
			ServiceTier:     "priority",
			PlanningMode:    true,
		},
	}
	run.setThreadID("thread_fake")

	if err := run.startTurn(context.Background(), "Hello", "/tmp/workspace"); err != nil {
		t.Fatalf("start turn: %v", err)
	}

	var request struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(written.Bytes()), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if request.Method != "turn/start" {
		t.Fatalf("expected turn/start request, got %q", request.Method)
	}
	if request.Params["model"] != "gpt-5.5" {
		t.Fatalf("expected model override, got %#v", request.Params["model"])
	}
	if request.Params["effort"] != "xhigh" {
		t.Fatalf("expected effort override, got %#v", request.Params["effort"])
	}
	if request.Params["serviceTier"] != "priority" {
		t.Fatalf("expected service tier override, got %#v", request.Params["serviceTier"])
	}
	input, ok := request.Params["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("expected skill and text input items, got %#v", request.Params["input"])
	}
	skill, ok := input[0].(map[string]any)
	if !ok || skill["type"] != "skill" || skill["name"] != "openai-docs" || skill["path"] != "/skills/openai-docs/SKILL.md" {
		t.Fatalf("expected structured skill input first, got %#v", input[0])
	}
	text, ok := input[1].(map[string]any)
	if !ok || text["type"] != "text" || text["text"] != "Hello" {
		t.Fatalf("expected text input second, got %#v", input[1])
	}
	sandboxPolicy, ok := request.Params["sandboxPolicy"].(map[string]any)
	if !ok {
		t.Fatalf("expected sandbox policy, got %#v", request.Params["sandboxPolicy"])
	}
	if sandboxPolicy["type"] != "workspaceWrite" || sandboxPolicy["networkAccess"] != true {
		t.Fatalf("expected workspaceWrite sandbox with network access, got %#v", sandboxPolicy)
	}
	collaborationMode, ok := request.Params["collaborationMode"].(map[string]any)
	if !ok {
		t.Fatalf("expected collaboration mode, got %#v", request.Params["collaborationMode"])
	}
	if collaborationMode["mode"] != "plan" {
		t.Fatalf("expected plan collaboration mode, got %#v", collaborationMode["mode"])
	}
	settings, ok := collaborationMode["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected collaboration settings, got %#v", collaborationMode["settings"])
	}
	if settings["model"] != "gpt-5.5" || settings["reasoning_effort"] != "xhigh" {
		t.Fatalf("unexpected collaboration settings %#v", settings)
	}
	if settings["developer_instructions"] != nil {
		t.Fatalf("expected built-in collaboration instructions, got %#v", settings["developer_instructions"])
	}
}

func TestStartTurnSendsDefaultCollaborationMode(t *testing.T) {
	var written bytes.Buffer
	incoming := make(chan incomingMessage, 1)
	incoming <- incomingMessage{Message: &rpcMessage{
		ID:     json.RawMessage(`1`),
		Result: json.RawMessage(`{"turn":{"id":"turn_fake","status":"inProgress"}}`),
	}}

	run := &appServerRun{
		agent:      New(WithModel("gpt-default")),
		rpc:        newRPCClient(bufferWriteCloser{Buffer: &written}),
		incoming:   incoming,
		process:    &processState{done: make(chan struct{})},
		emit:       func(context.Context, agents.AgentEvent) error { return nil },
		normalizer: newNormalizer(),
		options: codexRunOptions{
			Model:           "gpt-5.5",
			ReasoningEffort: "xhigh",
			PlanningMode:    false,
		},
	}
	run.setThreadID("thread_fake")

	if err := run.startTurn(context.Background(), "Hello", "/tmp/workspace"); err != nil {
		t.Fatalf("start turn: %v", err)
	}

	var request struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(written.Bytes()), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if request.Method != "turn/start" {
		t.Fatalf("expected turn/start request, got %q", request.Method)
	}
	collaborationMode, ok := request.Params["collaborationMode"].(map[string]any)
	if !ok {
		t.Fatalf("expected collaboration mode, got %#v", request.Params["collaborationMode"])
	}
	if collaborationMode["mode"] != "default" {
		t.Fatalf("expected default collaboration mode, got %#v", collaborationMode["mode"])
	}
	settings, ok := collaborationMode["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected collaboration settings, got %#v", collaborationMode["settings"])
	}
	if settings["model"] != "gpt-5.5" || settings["reasoning_effort"] != "xhigh" {
		t.Fatalf("unexpected collaboration settings %#v", settings)
	}
	if settings["developer_instructions"] != nil {
		t.Fatalf("expected built-in collaboration instructions, got %#v", settings["developer_instructions"])
	}
}

func TestStartTurnCanDisableSandboxNetworkAccess(t *testing.T) {
	var written bytes.Buffer
	incoming := make(chan incomingMessage, 1)
	incoming <- incomingMessage{Message: &rpcMessage{
		ID:     json.RawMessage(`1`),
		Result: json.RawMessage(`{"turn":{"id":"turn_fake","status":"inProgress"}}`),
	}}

	run := &appServerRun{
		agent:      New(WithSandbox("read-only"), WithNetworkAccess(false)),
		rpc:        newRPCClient(bufferWriteCloser{Buffer: &written}),
		incoming:   incoming,
		process:    &processState{done: make(chan struct{})},
		emit:       func(context.Context, agents.AgentEvent) error { return nil },
		normalizer: newNormalizer(),
	}
	run.setThreadID("thread_fake")

	if err := run.startTurn(context.Background(), "Hello", "/tmp/workspace"); err != nil {
		t.Fatalf("start turn: %v", err)
	}

	var request struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(written.Bytes()), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	sandboxPolicy, ok := request.Params["sandboxPolicy"].(map[string]any)
	if !ok {
		t.Fatalf("expected sandbox policy, got %#v", request.Params["sandboxPolicy"])
	}
	if sandboxPolicy["type"] != "readOnly" || sandboxPolicy["networkAccess"] != false {
		t.Fatalf("expected readOnly sandbox without network access, got %#v", sandboxPolicy)
	}
}

func TestStartTurnCanRunDangerously(t *testing.T) {
	var written bytes.Buffer
	incoming := make(chan incomingMessage, 1)
	incoming <- incomingMessage{Message: &rpcMessage{
		ID:     json.RawMessage(`1`),
		Result: json.RawMessage(`{"turn":{"id":"turn_fake","status":"inProgress"}}`),
	}}

	run := &appServerRun{
		agent:      New(WithSandbox("read-only"), WithNetworkAccess(false)),
		rpc:        newRPCClient(bufferWriteCloser{Buffer: &written}),
		incoming:   incoming,
		process:    &processState{done: make(chan struct{})},
		emit:       func(context.Context, agents.AgentEvent) error { return nil },
		normalizer: newNormalizer(),
		options:    codexRunOptions{RunDangerously: true},
	}
	run.setThreadID("thread_fake")

	if err := run.startTurn(context.Background(), "Hello", "/tmp/workspace"); err != nil {
		t.Fatalf("start turn: %v", err)
	}

	var request struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(written.Bytes()), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if request.Params["approvalPolicy"] != "never" {
		t.Fatalf("expected never approval policy, got %#v", request.Params["approvalPolicy"])
	}
	sandboxPolicy, ok := request.Params["sandboxPolicy"].(map[string]any)
	if !ok {
		t.Fatalf("expected sandbox policy, got %#v", request.Params["sandboxPolicy"])
	}
	if sandboxPolicy["type"] != "dangerFullAccess" {
		t.Fatalf("expected dangerFullAccess sandbox, got %#v", sandboxPolicy)
	}
}

func TestStartTurnAddsImageAttachments(t *testing.T) {
	var written bytes.Buffer
	incoming := make(chan incomingMessage, 1)
	incoming <- incomingMessage{Message: &rpcMessage{
		ID:     json.RawMessage(`1`),
		Result: json.RawMessage(`{"turn":{"id":"turn_fake","status":"inProgress"}}`),
	}}

	run := &appServerRun{
		agent:      New(),
		rpc:        newRPCClient(bufferWriteCloser{Buffer: &written}),
		incoming:   incoming,
		process:    &processState{done: make(chan struct{})},
		emit:       func(context.Context, agents.AgentEvent) error { return nil },
		normalizer: newNormalizer(),
		attachments: []agents.Attachment{
			{
				Name:      "diagram.png",
				MediaType: "image/png",
				DataURL:   "data:image/png;base64,aGVsbG8=",
				SizeBytes: 5,
			},
		},
	}
	run.setThreadID("thread_fake")

	if err := run.startTurn(context.Background(), "Describe this", "/tmp/workspace"); err != nil {
		t.Fatalf("start turn: %v", err)
	}

	var request struct {
		Params struct {
			Input []map[string]any `json:"input"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(written.Bytes()), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(request.Params.Input) != 2 {
		t.Fatalf("expected text and image inputs, got %#v", request.Params.Input)
	}
	if request.Params.Input[0]["type"] != "text" || request.Params.Input[0]["text"] != "Describe this" {
		t.Fatalf("expected text input, got %#v", request.Params.Input[0])
	}
	if request.Params.Input[1]["type"] != "image" || request.Params.Input[1]["url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("expected image input, got %#v", request.Params.Input[1])
	}
	if request.Params.Input[1]["detail"] != "auto" {
		t.Fatalf("expected auto image detail, got %#v", request.Params.Input[1]["detail"])
	}
}

func TestResumeThreadUsesExistingProviderSessionID(t *testing.T) {
	var written bytes.Buffer
	incoming := make(chan incomingMessage, 1)
	incoming <- incomingMessage{Message: &rpcMessage{
		ID:     json.RawMessage(`1`),
		Result: json.RawMessage(`{"thread":{"id":"thread_existing"}}`),
	}}

	run := &appServerRun{
		agent:      New(WithModel("gpt-default")),
		rpc:        newRPCClient(bufferWriteCloser{Buffer: &written}),
		incoming:   incoming,
		process:    &processState{done: make(chan struct{})},
		emit:       func(context.Context, agents.AgentEvent) error { return nil },
		normalizer: newNormalizer(),
		options: codexRunOptions{
			Model:       "gpt-5.5",
			ServiceTier: "priority",
		},
	}

	if err := run.resumeThread(context.Background(), "thread_existing", "/tmp/workspace"); err != nil {
		t.Fatalf("resume thread: %v", err)
	}

	var request struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(written.Bytes()), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if request.Method != "thread/resume" {
		t.Fatalf("expected thread/resume request, got %q", request.Method)
	}
	if request.Params["threadId"] != "thread_existing" {
		t.Fatalf("expected thread id, got %#v", request.Params["threadId"])
	}
	if request.Params["model"] != "gpt-5.5" {
		t.Fatalf("expected model override, got %#v", request.Params["model"])
	}
	if request.Params["serviceTier"] != "priority" {
		t.Fatalf("expected service tier override, got %#v", request.Params["serviceTier"])
	}
	if request.Params["excludeTurns"] != true {
		t.Fatalf("expected resumed thread history to be excluded, got %#v", request.Params["excludeTurns"])
	}
	if got := run.getThreadID(); got != "thread_existing" {
		t.Fatalf("expected stored thread id thread_existing, got %q", got)
	}
}

func TestResumeThreadReadErrorIncludesRPCContext(t *testing.T) {
	var written bytes.Buffer
	incoming := make(chan incomingMessage, 1)
	incoming <- incomingMessage{ReadErr: errors.New("response exceeded read limit")}

	run := &appServerRun{
		agent:      New(),
		rpc:        newRPCClient(bufferWriteCloser{Buffer: &written}),
		incoming:   incoming,
		process:    &processState{done: make(chan struct{})},
		emit:       func(context.Context, agents.AgentEvent) error { return nil },
		normalizer: newNormalizer(),
	}

	err := run.resumeThread(context.Background(), "thread_large", "/tmp/workspace")
	if err == nil {
		t.Fatal("expected resume read error")
	}
	if !strings.Contains(err.Error(), `codex thread/resume for thread "thread_large" failed while awaiting response`) {
		t.Fatalf("expected RPC context in resume error, got %q", err)
	}
	if !strings.Contains(err.Error(), "response exceeded read limit") {
		t.Fatalf("expected underlying read error to be preserved, got %q", err)
	}
}

func TestAgentEmitsFailedEventWhenAppServerExitsNonZero(t *testing.T) {
	agent := fakeAppServerAgent(t, "nonzero")
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID: "sess_test",
		Message:   "Fail",
		Workdir:   t.TempDir(),
	}, recorder.emit)
	if err == nil {
		t.Fatal("expected run error")
	}

	events := recorder.snapshot()
	if !hasAgentEvent(events, "agent.run.failed") {
		t.Fatalf("expected agent.run.failed in %#v", events)
	}
	assertTerminalCount(t, events, 1)
}

func TestAgentEmitsCancelledEventAfterInterrupt(t *testing.T) {
	agent := fakeAppServerAgent(t, "cancel")
	recorder := newEventRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- agent.Run(ctx, agents.AgentInput{
			SessionID: "sess_test",
			Message:   "Wait",
			Workdir:   t.TempDir(),
		}, recorder.emit)
	}()

	recorder.waitFor(t, "agent.status.started")
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for cancelled run")
	}

	events := recorder.snapshot()
	if !hasAgentEvent(events, "agent.run.cancelled") {
		t.Fatalf("expected agent.run.cancelled in %#v", events)
	}
	assertTerminalCount(t, events, 1)
}

func TestAgentEmitsStderrAsLogEvent(t *testing.T) {
	agent := fakeAppServerAgent(t, "stderr")
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID: "sess_test",
		Message:   "Log",
		Workdir:   t.TempDir(),
	}, recorder.emit)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}
	if !hasAgentEvent(recorder.snapshot(), "agent.log.delta") {
		t.Fatalf("expected agent.log.delta in %#v", recorder.snapshot())
	}
}

func TestAgentAnswersUserInputServerRequest(t *testing.T) {
	agent := fakeAppServerAgent(t, "user-input")
	recorder := newEventRecorder()

	err := agent.Run(context.Background(), agents.AgentInput{
		SessionID: "sess_test",
		Message:   "Ask",
		Workdir:   t.TempDir(),
		UserInput: autoAnswerBroker{
			response: agents.UserInputResponse{
				Answers: map[string]agents.UserInputQuestionAnswer{
					"fake_question": {Answers: []string{"Beta"}},
				},
			},
		},
	}, recorder.emit)
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}

	events := recorder.snapshot()
	assertAgentEventTypes(t, events, []string{
		"agent.run.started",
		"agent.status.started",
		"agent.input.requested",
		"agent.message.delta",
		"agent.message.completed",
		"agent.run.completed",
	})
	payload, ok := events[2].Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected request payload, got %#v", events[2].Payload)
	}
	if payload["request_id"] != "call_fake_question" {
		t.Fatalf("expected request id call_fake_question, got %#v", payload["request_id"])
	}
	assertTerminalCount(t, events, 1)
}

func fakeAppServerAgent(t *testing.T, mode string, options ...Option) *Agent {
	t.Helper()
	t.Setenv("GORCHESTRA_FAKE_CODEX_APP_SERVER", mode)
	baseOptions := []Option{
		WithBinary(os.Args[0]),
		WithInterruptGrace(500 * time.Millisecond),
		WithVersionChecker(func(context.Context, string) (string, error) {
			return "codex-cli fake", nil
		}),
	}
	return New(append(baseOptions, options...)...)
}

func normalizeFixture(t *testing.T, name string) []agents.AgentEvent {
	t.Helper()

	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	normalizer := newNormalizer()
	events := make([]agents.AgentEvent, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		message, err := parseRPCMessage([]byte(line))
		if err != nil {
			t.Fatalf("parse fixture line: %v", err)
		}
		for _, normalized := range normalizer.normalize(message.Method, message.Params) {
			events = append(events, normalized.Event)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return events
}

func assertAgentEventTypes(t *testing.T, events []agents.AgentEvent, want []string) {
	t.Helper()

	if len(events) != len(want) {
		t.Fatalf("expected %d events, got %d: %#v", len(want), len(events), events)
	}
	for i, event := range events {
		if event.Type != want[i] {
			t.Fatalf("expected event %d type %q, got %q", i, want[i], event.Type)
		}
	}
}

func assertTerminalCount(t *testing.T, events []agents.AgentEvent, want int) {
	t.Helper()

	count := 0
	for _, event := range events {
		switch event.Type {
		case "agent.run.completed", "agent.run.failed", "agent.run.cancelled":
			count++
		}
	}
	if count != want {
		t.Fatalf("expected %d terminal events, got %d in %#v", want, count, events)
	}
}

func hasAgentEvent(events []agents.AgentEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

type eventRecorder struct {
	mu     sync.Mutex
	events []agents.AgentEvent
}

type autoAnswerBroker struct {
	response agents.UserInputResponse
}

func (b autoAnswerBroker) OpenUserInput(context.Context, agents.UserInputRequest) (agents.UserInputWaiter, error) {
	return autoAnswerWaiter{response: b.response}, nil
}

type autoAnswerWaiter struct {
	response agents.UserInputResponse
}

func (w autoAnswerWaiter) Wait(context.Context) (agents.UserInputResponse, error) {
	return w.response, nil
}

func (w autoAnswerWaiter) Close() {}

func newEventRecorder() *eventRecorder {
	return &eventRecorder{}
}

func (r *eventRecorder) emit(_ context.Context, event agents.AgentEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *eventRecorder) snapshot() []agents.AgentEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agents.AgentEvent(nil), r.events...)
}

func (r *eventRecorder) waitFor(t *testing.T, eventType string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s in %#v", eventType, r.snapshot())
		case <-ticker.C:
			if hasAgentEvent(r.snapshot(), eventType) {
				return
			}
		}
	}
}

type bufferWriteCloser struct {
	*bytes.Buffer
}

func (w bufferWriteCloser) Close() error {
	return nil
}

func runFakeAppServer(mode string) {
	if expected := os.Getenv("GORCHESTRA_FAKE_CODEX_EXPECT_ENV"); expected != "" && os.Getenv("GORCHESTRA_AGENT_RUN_TEST_VALUE") != expected {
		os.Exit(10)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var request rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		if mode == "user-input" && request.Method == "" && request.idKey() == "99" {
			var response agents.UserInputResponse
			if err := json.Unmarshal(request.Result, &response); err != nil {
				os.Exit(5)
			}
			answer := ""
			if questionAnswer := response.Answers["fake_question"]; len(questionAnswer.Answers) > 0 {
				answer = questionAnswer.Answers[0]
			}
			fakeNotify("item/agentMessage/delta", map[string]any{
				"threadId": "thread_fake",
				"turnId":   "turn_fake",
				"itemId":   "message_fake",
				"delta":    "Selected " + answer,
			})
			fakeNotify("item/completed", map[string]any{
				"threadId": "thread_fake",
				"turnId":   "turn_fake",
				"item": map[string]any{
					"type": "agentMessage",
					"id":   "message_fake",
					"text": "Selected " + answer,
				},
			})
			fakeNotify("turn/completed", map[string]any{
				"threadId": "thread_fake",
				"turn": map[string]any{
					"id":     "turn_fake",
					"status": "completed",
				},
			})
			return
		}

		switch request.Method {
		case "turn/steer":
			want := "Pick one\nBeta"
			if mode == "steer" || mode == "steer-reject" {
				want = "Actually focus on the tests"
			}
			if stringAt(request.Params, "threadId") != "thread_fake" || stringAt(request.Params, "expectedTurnId") != "turn_fake" || !strings.Contains(fakeCodexPromptText(request.Params), want) {
				fakeRespondError(request.ID, -32602, "incorrect steer parameters")
				continue
			}
			if mode == "async-reject" || mode == "steer-reject" {
				fakeRespondError(request.ID, -32602, "turn is no longer active")
				continue
			}
			fakeNotify("item/agentMessage/delta", map[string]any{"threadId": "thread_fake", "turnId": "turn_fake", "itemId": "answer_message", "delta": "Reading your answer"})
			fakeRespond(request.ID, map[string]any{"turnId": "turn_fake"})
			fakeNotify("turn/completed", map[string]any{"threadId": "thread_fake", "turn": map[string]any{"id": "turn_fake", "status": "completed"}})
		case "initialize":
			if mode == "stderr" {
				_, _ = os.Stderr.WriteString("codex log line\n")
			}
			fakeRespond(request.ID, map[string]any{"serverInfo": map[string]any{"name": "fake-codex"}})
		case "initialized":
		case "model/list":
			fakeRespond(request.ID, map[string]any{
				"data": []map[string]any{
					{
						"id":                     "gpt-5.5",
						"model":                  "gpt-5.5",
						"displayName":            "GPT-5.5",
						"description":            "Default Codex model",
						"hidden":                 false,
						"defaultReasoningEffort": "medium",
						"isDefault":              true,
						"supportedReasoningEfforts": []map[string]any{
							{"reasoningEffort": "low", "description": "Low"},
							{"reasoningEffort": "medium", "description": "Medium"},
							{"reasoningEffort": "xhigh", "description": "Extra high"},
						},
						"serviceTiers": []map[string]any{
							{"id": "priority", "name": "Fast", "description": "1.5x speed"},
						},
					},
				},
				"nextCursor": nil,
			})
		case "collaborationMode/list":
			fakeRespond(request.ID, map[string]any{
				"data": []map[string]any{
					{"name": "Plan", "mode": "plan", "model": nil, "reasoning_effort": "medium"},
					{"name": "Default", "mode": "default", "model": nil, "reasoning_effort": nil},
				},
			})
		case "skills/list":
			var params struct {
				CWDs        []string `json:"cwds"`
				ForceReload bool     `json:"forceReload"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil || len(params.CWDs) != 1 {
				os.Exit(12)
			}
			if mode == "skills" && !params.ForceReload {
				os.Exit(13)
			}
			fakeRespond(request.ID, map[string]any{
				"data": []map[string]any{
					{
						"cwd": params.CWDs[0],
						"skills": []map[string]any{
							{
								"name":             "openai-docs",
								"description":      "Use official OpenAI documentation.",
								"shortDescription": "Fallback description",
								"interface": map[string]any{
									"displayName":      "OpenAI Docs",
									"shortDescription": "Official product guidance",
									"brandColor":       "#10A37F",
								},
								"path":    "/skills/user/openai-docs/SKILL.md",
								"scope":   "user",
								"enabled": true,
							},
							{
								"name":             "openai-docs",
								"description":      "Repository-specific OpenAI workflow.",
								"shortDescription": "Repository workflow",
								"path":             "/workspace/.agents/skills/openai-docs/SKILL.md",
								"scope":            "repo",
								"enabled":          false,
							},
						},
						"errors": []map[string]any{
							{"path": "/broken/SKILL.md", "message": "invalid frontmatter"},
						},
					},
				},
			})
		case "thread/start":
			fakeRespond(request.ID, map[string]any{
				"thread": map[string]any{
					"id":        "thread_fake",
					"sessionId": "session_fake",
					"preview":   "",
					"ephemeral": false,
				},
			})
		case "thread/resume":
			var params map[string]any
			if err := json.Unmarshal(request.Params, &params); err != nil || params["excludeTurns"] != true {
				os.Exit(14)
			}
			fakeRespond(request.ID, map[string]any{
				"thread": map[string]any{
					"id":        "thread_fake",
					"sessionId": "session_fake",
					"preview":   "",
					"ephemeral": false,
				},
			})
		case "thread/compact/start":
			if mode != "compact" {
				fakeRespondError(request.ID, -32601, "unknown fake method")
				continue
			}
			if stringAt(request.Params, "threadId") != "thread_fake" {
				os.Exit(7)
			}
			fakeRespond(request.ID, map[string]any{})
			fakeNotify("turn/started", map[string]any{
				"threadId": "thread_fake",
				"turn": map[string]any{
					"id":     "turn_compact",
					"status": "inProgress",
				},
			})
			fakeNotify("thread/compacted", map[string]any{
				"threadId": "thread_fake",
				"summary":  "Short summary.",
			})
			fakeNotify("turn/completed", map[string]any{
				"threadId": "thread_fake",
				"turn": map[string]any{
					"id":     "turn_compact",
					"status": "completed",
				},
			})
			return
		case "turn/start":
			if expected := os.Getenv("GORCHESTRA_FAKE_CODEX_EXPECT_CONTEXT"); expected != "" {
				prompt := fakeCodexPromptText(request.Params)
				if !strings.Contains(prompt, "<threave_context>\n"+expected+"\n</threave_context>") || !strings.HasSuffix(prompt, "\n\nSay hello") {
					os.Exit(11)
				}
			}
			fakeRespond(request.ID, map[string]any{
				"turn": map[string]any{
					"id":     "turn_fake",
					"status": "inProgress",
				},
			})
			fakeNotify("turn/started", map[string]any{
				"threadId": "thread_fake",
				"turn": map[string]any{
					"id":     "turn_fake",
					"status": "inProgress",
				},
			})
			switch mode {
			case "steer", "steer-reject":
				fakeNotify("item/started", map[string]any{"threadId": "thread_fake", "turnId": "turn_fake", "item": map[string]any{"id": "thinking_before_steer", "type": "reasoning"}})
			case "async-input", "async-reject", "async-ended":
				fakeNotify("item/completed", map[string]any{
					"threadId": "thread_fake", "turnId": "turn_fake",
					"item": map[string]any{"id": "call_async", "type": "agentMessage", "text": "Pick one\n- Alpha\n- Beta", "delivery": "async", "questions": []map[string]any{{"title": "Pick one", "options": []string{"Alpha", "Beta"}}}},
				})
				fakeNotify("item/started", map[string]any{"threadId": "thread_fake", "turnId": "turn_fake", "item": map[string]any{"id": "thinking_after_question", "type": "reasoning"}})
				if mode == "async-ended" {
					fakeNotify("turn/completed", map[string]any{"threadId": "thread_fake", "turn": map[string]any{"id": "turn_fake", "status": "completed"}})
				}
			case "success", "stderr", "retry-success":
				if mode == "retry-success" {
					fakeNotify("error", map[string]any{
						"threadId":  "thread_fake",
						"turnId":    "turn_fake",
						"willRetry": true,
						"error": map[string]any{
							"message":           "Reconnecting... 2/5",
							"additionalDetails": "websocket closed before response.completed",
							"codexErrorInfo":    map[string]any{"type": "ResponseStreamDisconnected"},
						},
					})
				}
				fakeNotify("item/agentMessage/delta", map[string]any{
					"threadId": "thread_fake",
					"turnId":   "turn_fake",
					"itemId":   "message_fake",
					"delta":    "Hello",
				})
				fakeNotify("item/completed", map[string]any{
					"threadId": "thread_fake",
					"turnId":   "turn_fake",
					"item": map[string]any{
						"type": "agentMessage",
						"id":   "message_fake",
						"text": "Hello from fake Codex.",
					},
				})
				fakeNotify("turn/completed", map[string]any{
					"threadId": "thread_fake",
					"turn": map[string]any{
						"id":     "turn_fake",
						"status": "completed",
					},
				})
				// Keep the fake process alive until the agent closes stdin. Exiting here
				// races the notification reader and can make process exit appear before
				// the terminal turn event on faster CI hosts.
				continue
			case "user-input":
				fakeWrite(map[string]any{
					"jsonrpc": "2.0",
					"id":      99,
					"method":  "item/tool/requestUserInput",
					"params": map[string]any{
						"threadId": "thread_fake",
						"turnId":   "turn_fake",
						"itemId":   "call_fake_question",
						"questions": []map[string]any{
							{
								"id":       "fake_question",
								"header":   "Choose",
								"question": "Pick one",
								"isOther":  true,
								"isSecret": false,
								"options": []map[string]any{
									{"label": "Alpha", "description": "First"},
									{"label": "Beta", "description": "Second"},
									{"label": "Gamma", "description": "Third"},
								},
							},
						},
					},
				})
			case "nonzero":
				os.Exit(42)
			case "cancel":
			default:
				os.Exit(3)
			}
		case "turn/interrupt":
			fakeRespond(request.ID, map[string]any{})
			fakeNotify("turn/completed", map[string]any{
				"threadId": "thread_fake",
				"turn": map[string]any{
					"id":     "turn_fake",
					"status": "interrupted",
				},
			})
			return
		default:
			fakeRespondError(request.ID, -32601, "unknown fake method")
		}
	}
}

func fakeCodexPromptText(raw json.RawMessage) string {
	var params struct {
		Input []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"input"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ""
	}
	for _, item := range params.Input {
		if item.Type == "text" {
			return item.Text
		}
	}
	return ""
}

func recordFakeAppServerStart() {
	path := os.Getenv("GORCHESTRA_FAKE_CODEX_APP_SERVER_START_COUNT")
	if path == "" {
		return
	}
	count := 0
	if raw, err := os.ReadFile(path); err == nil {
		count, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(count+1)), 0o644)
}

func fakeAppServerStartCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read fake app-server start count: %v", err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse fake app-server start count %q: %v", string(raw), err)
	}
	return count
}

func fakeRespond(id json.RawMessage, result any) {
	fakeWrite(map[string]any{
		"jsonrpc": "2.0",
		"id":      jsonID(id),
		"result":  result,
	})
}

func fakeRespondError(id json.RawMessage, code int, message string) {
	fakeWrite(map[string]any{
		"jsonrpc": "2.0",
		"id":      jsonID(id),
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func fakeNotify(method string, params any) {
	fakeWrite(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func fakeWrite(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		os.Exit(4)
	}
}

func jsonID(raw json.RawMessage) any {
	var id any
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil
	}
	return id
}

func TestCodexCommandPermissionUsesAvailableSessionDecisions(t *testing.T) {
	message := &rpcMessage{ID: json.RawMessage(`42`), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"threadId":"thread_1","turnId":"turn_1","itemId":"item_1","command":"git push","cwd":"/repo","reason":"network","startedAtMs":1,"availableDecisions":["accept", "acceptForSession", {"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git"]}}, "decline", "cancel"]}`)}
	request, _, err := codexPermissionRequest("sess_1", message)
	if err != nil {
		t.Fatalf("parse permission: %v", err)
	}
	if request.RequestID != "item_1" || request.Command != "git push" {
		t.Fatalf("unexpected request %#v", request)
	}
	if got := []string{request.Options[0].ID, request.Options[1].ID, request.Options[2].ID, request.Options[3].ID}; !reflect.DeepEqual(got, []string{"accept", "acceptForSession", "decline", "cancel"}) {
		t.Fatalf("unexpected decisions %#v", got)
	}
	response := codexPermissionResponse(message.Method, map[string]any{}, "acceptForSession")
	if !reflect.DeepEqual(response, map[string]any{"decision": "acceptForSession"}) {
		t.Fatalf("unexpected response %#v", response)
	}
}

func TestCodexPermissionGrantReturnsRequestedProfileAtSelectedScope(t *testing.T) {
	permissions := map[string]any{"network": map[string]any{"enabled": true}}
	response := codexPermissionResponse("item/permissions/requestApproval", map[string]any{"permissions": permissions}, "grant-session")
	if !reflect.DeepEqual(response, map[string]any{"permissions": permissions, "scope": "session"}) {
		t.Fatalf("unexpected response %#v", response)
	}
}
