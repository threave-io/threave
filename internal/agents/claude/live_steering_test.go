package claude

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/threave-io/threave/internal/agents"
	runcontrol "github.com/threave-io/threave/internal/session"
)

// Explicit opt-in: this test requires a logged-in CLI and makes live model calls.
func TestLiveClaudeSteering(t *testing.T) {
	if os.Getenv("THREAVE_LIVE_CLAUDE_STEERING") != "1" {
		t.Skip("set THREAVE_LIVE_CLAUDE_STEERING=1 to test the installed Claude CLI")
	}
	binary, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("THREAVE_LIVE_CLAUDE_BIN", binary)
	// Isolate user/project settings and MCP tools while retaining CLI credentials.
	wrapper := filepath.Join(t.TempDir(), "claude-local-test")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then exec \"$THREAVE_LIVE_CLAUDE_BIN\" --version; fi\nexec \"$THREAVE_LIVE_CLAUDE_BIN\" \"$@\" --setting-sources '' --strict-mcp-config --mcp-config '{\"mcpServers\":{}}' --tools Bash --allowedTools Bash\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	manager := runcontrol.NewManager()
	ctx, cleanup, err := manager.Register(timeoutCtx, "live-steering")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	broker := readySteeringBroker{manager, make(chan struct{})}
	recorder := newEventRecorder()
	done := make(chan error, 1)
	workdir := t.TempDir()
	go func() {
		done <- New(WithBinary(wrapper), WithModel("haiku")).Run(ctx, agents.AgentInput{
			SessionID: "live-steering", RunID: "live-run", Workdir: workdir, Steering: broker,
			Message: "Run the Bash command sleep 8 and then reply with the single word ORIGINAL. Do nothing else.",
		}, recorder.emit)
	}()
	// Wait for the tool's full input, not just the start of its JSON stream.
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
waitForTool:
	for {
		select {
		case err := <-done:
			t.Fatalf("run ended before tool execution: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
			for _, event := range recorder.snapshot() {
				payload, _ := event.Event.Payload.(map[string]any)
				if strings.HasPrefix(event.Event.Type, "tool.call.") && strings.Contains(mapString(payload, "command"), "sleep 8") {
					break waitForTool
				}
			}
		}
	}
	select {
	case <-time.After(time.Second):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, event := range recorder.snapshot() {
		if event.Event.Type == "tool.call.completed" {
			t.Fatal("tool finished before input could be injected")
		}
	}
	start := time.Now()
	deliveryCtx, cancelDelivery := context.WithTimeout(ctx, 20*time.Second)
	defer cancelDelivery()
	if err := manager.SteerWithPersistence(deliveryCtx, "live-steering", "live-run", agents.SteeringInput{Message: "Change the final word to STEERED instead of ORIGINAL."}, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("provider acknowledged the injected input after %s", time.Since(start).Round(time.Millisecond))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	completed := 0
	for _, event := range recorder.snapshot() {
		if event.Event.Type == "agent.run.completed" {
			completed++
			if text := mapString(event.Event.Payload.(map[string]any), "text"); strings.TrimSpace(text) != "STEERED" {
				t.Fatalf("injected instruction not consumed: %q", text)
			}
		}
	}
	if completed != 1 {
		t.Fatalf("expected one terminal completion, got %d", completed)
	}
	t.Log("Claude consumed the correction during an active tool run and completed the same Threave run with STEERED")
}
