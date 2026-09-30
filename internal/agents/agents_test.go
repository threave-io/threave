package agents

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestAgentInputProviderMessageKeepsOriginalMessageSeparate(t *testing.T) {
	input := AgentInput{
		Message: "build the app",
		Context: "  Hosting is available through threave host.  ",
	}

	got := input.ProviderMessage()
	want := "<threave_context>\nHosting is available through threave host.\n</threave_context>\n\nbuild the app"
	if got != want {
		t.Fatalf("expected provider message %q, got %q", want, got)
	}
	if input.Message != "build the app" {
		t.Fatalf("expected original message to remain unchanged, got %q", input.Message)
	}
}

func TestAgentInputProviderMessageWithoutContextIsUnchanged(t *testing.T) {
	input := AgentInput{Message: "  preserve my whitespace  ", Context: " \n\t"}
	if got := input.ProviderMessage(); got != input.Message {
		t.Fatalf("expected unchanged message %q, got %q", input.Message, got)
	}
}

func TestApplyEnvironmentOverridesCommandOnly(t *testing.T) {
	t.Setenv("GORCHESTRA_AGENT_ENV_TEST", "parent")
	cmd := exec.Command(os.Args[0])
	if err := ApplyEnvironment(cmd, map[string]string{
		"GORCHESTRA_AGENT_ENV_TEST":     "run-only",
		"GORCHESTRA_AGENT_ENV_TEST_NEW": "new-value",
	}); err != nil {
		t.Fatalf("apply environment: %v", err)
	}

	if got := environmentValue(cmd.Env, "GORCHESTRA_AGENT_ENV_TEST"); got != "run-only" {
		t.Fatalf("expected run override, got %q", got)
	}
	if got := environmentValue(cmd.Env, "GORCHESTRA_AGENT_ENV_TEST_NEW"); got != "new-value" {
		t.Fatalf("expected new run value, got %q", got)
	}
	if got := os.Getenv("GORCHESTRA_AGENT_ENV_TEST"); got != "parent" {
		t.Fatalf("expected parent environment unchanged, got %q", got)
	}
}

func TestApplyEnvironmentRejectsInvalidNames(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	if err := ApplyEnvironment(cmd, map[string]string{"BAD=NAME": "value"}); err == nil {
		t.Fatal("expected invalid environment name error")
	}
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
