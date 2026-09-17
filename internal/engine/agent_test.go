package engine

import (
	"os"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/tmux"
)

func TestBuildAgentCommand_QuotesArgs(t *testing.T) {
	eng, _ := testEngine(t)
	eng.Config.Agents["claude"] = config.AgentConfig{
		Command:    "claude",
		Args:       []string{"--dangerously-skip-permissions"},
		LaunchArgs: []string{"--append-system-prompt", "be brief; don't ask"},
	}

	got, err := eng.buildAgentCommand("claude", eng.Config.Agents["claude"].Args,
		eng.Config.Agents["claude"].LaunchArgs, false)
	if err != nil {
		t.Fatalf("buildAgentCommand: %v", err)
	}
	want := `claude --dangerously-skip-permissions --append-system-prompt 'be brief; don'\''t ask'`
	if got != want {
		t.Errorf("command = %q, want %q", got, want)
	}

	// Plain flags stay unquoted; tilde paths keep their leading word form.
	got, err = eng.buildAgentCommand("claude", []string{"--config", "~/x.toml"}, nil, false)
	if err != nil {
		t.Fatalf("buildAgentCommand: %v", err)
	}
	if got != "claude --config ~/x.toml" {
		t.Errorf("command = %q, want unquoted plain args", got)
	}
}

// TestRecover_LaunchArgsNotReplayed asserts the reboot-recovery path
// directly (recoverSurfaceLaunch → SendKeys), complementing the
// undo-close restore test: launch_args must not be replayed when the
// agent resumes, so the session keeps its own model.
func TestRecover_LaunchArgsNotReplayed(t *testing.T) {
	eng, _ := testEngine(t)
	eng.Config.Agents["fable"] = config.AgentConfig{
		Extends:    "claude",
		LaunchArgs: []string{"--model", "fable"},
	}

	bay, err := eng.BayNew(BayNewOptions{Dock: "labs", Agent: "fable"})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := os.MkdirAll(bay.Path, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Simulate reboot.
	mockTmux := eng.Tmux.(*tmux.Mock)
	mockTmux.Reset()

	if _, err := eng.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	resumed := false
	for _, c := range mockTmux.Calls {
		if c.Method != "SendKeys" || len(c.Args) < 2 {
			continue
		}
		if strings.Contains(c.Args[1], "--model") {
			t.Errorf("recover replayed launch args: %q", c.Args[1])
		}
		if strings.Contains(c.Args[1], "claude --continue") {
			resumed = true
		}
	}
	if !resumed {
		t.Errorf("expected recover to send claude --continue; calls=%+v", mockTmux.Calls)
	}
}
