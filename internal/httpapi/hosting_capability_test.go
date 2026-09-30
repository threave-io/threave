package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/threave-io/threave/internal/store"
)

func TestAgentRuntimeEnvironmentIncludesHostCLIContext(t *testing.T) {
	api := API{agentAPIURL: "http://127.0.0.1:18080/", executable: "/tmp/gorchestra"}
	environment := api.agentRuntimeEnvironment("sess_1")
	if environment["GORCHESTRA_SESSION_ID"] != "sess_1" {
		t.Fatalf("unexpected session environment %#v", environment)
	}
	if environment["GORCHESTRA_API_URL"] != "http://127.0.0.1:18080" {
		t.Fatalf("unexpected API URL environment %#v", environment)
	}
	if environment["GORCHESTRA_BIN"] != "/tmp/gorchestra" {
		t.Fatalf("unexpected binary environment %#v", environment)
	}
	if environment["THREAVE_SESSION_ID"] != "sess_1" || environment["THREAVE_API_URL"] != "http://127.0.0.1:18080" || environment["THREAVE_BIN"] != "/tmp/gorchestra" {
		t.Fatalf("missing Threave environment aliases %#v", environment)
	}
}

func TestAgentRuntimeContextIntroducesOrchestrationAndDelegation(t *testing.T) {
	context := (API{}).agentRuntimeContext(store.Session{
		ID:            "sess_root",
		WorkspacePath: t.TempDir(),
	}, "run_root")
	for _, expected := range []string{
		"This session is running inside Threave",
		"agent orchestration service",
		"multiple concurrent agents",
		"This is a root session.",
		"Current session ID: sess_root",
		"Current run ID: run_root",
		"Parent session ID: none",
		"$THREAVE_SESSION_ID",
		"$THREAVE_RUN_ID",
		"delegate independent work to child agents",
		"New runs automatically become children of this session",
		`run --title "TASK NAME" --prompt-file task.md --detach --json`,
		"runs wait RUN_ID --timeout 10m --json",
		"runs report RUN_ID --json",
		`search "QUERY" --session current --format ndjson`,
		"You can search past chats across Threave sessions",
		`search "QUERY" --session none --format ndjson`,
		"Chat history search always spans sessions",
		"it does not limit which chats are searched",
		"Results include session IDs and history event sequence numbers",
		`"$THREAVE_BIN" commands --json`,
	} {
		if !strings.Contains(context, expected) {
			t.Fatalf("runtime context missing %q: %s", expected, context)
		}
	}
}

func TestAgentRuntimeContextIdentifiesDelegatedChild(t *testing.T) {
	context := (API{}).agentRuntimeContext(store.Session{
		ID:              "sess_child",
		ParentSessionID: "sess_parent",
		WorkspacePath:   t.TempDir(),
	}, "run_child")
	for _, expected := range []string{
		"This is a delegated child session.",
		"Current session ID: sess_child",
		"Current run ID: run_child",
		"Parent session ID: sess_parent",
		"You can search past chats across Threave sessions",
		`search "QUERY" --session none --format ndjson`,
		`search "QUERY" --session current --format ndjson`,
	} {
		if !strings.Contains(context, expected) {
			t.Fatalf("runtime context missing %q: %s", expected, context)
		}
	}
}

func TestAgentHostingContextRequiresRecipe(t *testing.T) {
	workspace := t.TempDir()
	api := API{}
	if context := api.agentHostingContext(workspace); context != "" {
		t.Fatalf("expected no context without recipe, got %q", context)
	}
	configDirectory := filepath.Join(workspace, ".gorchestra")
	if err := os.MkdirAll(configDirectory, 0o755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "host.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}
	context := api.agentHostingContext(workspace)
	if !strings.Contains(context, `"$THREAVE_BIN" host`) || !strings.Contains(context, ".gorchestra/host.yaml") {
		t.Fatalf("unexpected hosting context %q", context)
	}
}
