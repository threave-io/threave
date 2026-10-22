package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/threave-io/threave/internal/agents"
)

func TestCompactResumesSessionWithBareCommand(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			t.Setenv("GORCHESTRA_FAKE_CLAUDE_STREAM", "1")
			if fail {
				t.Setenv("GORCHESTRA_FAKE_CLAUDE_COMPACT_ERROR", "1")
			}
			capture := filepath.Join(t.TempDir(), "input.json")
			t.Setenv("GORCHESTRA_FAKE_CLAUDE_CAPTURE", capture)
			recorder := newEventRecorder()
			err := fakeClaudeAgent(t).Run(context.Background(), agents.AgentInput{
				SessionID: "sess_test", ProviderSessionID: "session_existing", Workdir: t.TempDir(), Action: agents.AgentActionCompact,
				Message: "must not become a prompt", Context: "runtime instructions",
				Skills:      []agents.SkillReference{{Name: "review", Path: "/skills/review/SKILL.md"}},
				Attachments: []agents.Attachment{{MediaType: "invalid"}},
			}, recorder.emit)
			if fail {
				if err == nil || !strings.Contains(err.Error(), "Compaction failed") {
					t.Fatalf("expected compaction failure, got %v", err)
				}
				assertAgentEventTypes(t, recorder.snapshot(), []string{"agent.run.started", "agent.run.failed"})
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertAgentEventTypes(t, recorder.snapshot(), []string{"agent.run.started", "provider.claude.event", "agent.run.completed"})
			}
			data, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			var user struct{ Message struct{ Content string } }
			if err := json.Unmarshal(data, &user); err != nil || user.Message.Content != "/compact" {
				t.Fatalf("expected bare /compact: %s (%v)", data, err)
			}
		})
	}
}

func TestCompactRequiresProviderSession(t *testing.T) {
	err := fakeClaudeAgent(t).Run(context.Background(), agents.AgentInput{Action: agents.AgentActionCompact}, newEventRecorder().emit)
	if err == nil || !strings.Contains(err.Error(), "requires an existing provider session") {
		t.Fatalf("expected missing session error, got %v", err)
	}
}
