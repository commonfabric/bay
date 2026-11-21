package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

// TestMonitorAutostart_TriggersOnRegularCommand verifies the auto-start
// hook fires for a normal command (one without the no-autostart
// annotation).
func TestMonitorAutostart_TriggersOnRegularCommand(t *testing.T) {
	called := 0
	withFakeEnsureMonitor(t, func() { called++ })

	root := NewRootCmd("test")
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	// `version` IS opt-out — pick a command that isn't.
	// `pwd` runs RunE that talks to tmux which we don't want here, so
	// use a no-op test command instead.
	tc := &cobra.Command{
		Use: "tc-runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	root.AddCommand(tc)

	root.SetArgs([]string{"tc-runs"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if called != 1 {
		t.Errorf("ensureMonitor calls = %d, want 1", called)
	}
}

// TestMonitorAutostart_SkipsAnnotatedCommand verifies that a command
// carrying the opt-out annotation does NOT trigger ensureMonitor.
func TestMonitorAutostart_SkipsAnnotatedCommand(t *testing.T) {
	called := 0
	withFakeEnsureMonitor(t, func() { called++ })

	root := NewRootCmd("test")
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	tc := &cobra.Command{
		Use:         "tc-skips",
		Annotations: map[string]string{noMonitorAutostartAnnotation: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	root.AddCommand(tc)

	root.SetArgs([]string{"tc-skips"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if called != 0 {
		t.Errorf("ensureMonitor calls = %d, want 0 (annotation should opt out)", called)
	}
}

// TestMonitorAutostart_SkipsForChildOfAnnotatedParent verifies that a
// subcommand inherits its parent's opt-out annotation. This is how
// `bay monitor stop` and friends are kept from auto-starting.
func TestMonitorAutostart_SkipsForChildOfAnnotatedParent(t *testing.T) {
	called := 0
	withFakeEnsureMonitor(t, func() { called++ })

	root := NewRootCmd("test")
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	parent := &cobra.Command{
		Use:         "tc-parent",
		Annotations: map[string]string{noMonitorAutostartAnnotation: "true"},
	}
	child := &cobra.Command{
		Use: "tc-child",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	parent.AddCommand(child)
	root.AddCommand(parent)

	root.SetArgs([]string{"tc-parent", "tc-child"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if called != 0 {
		t.Errorf("ensureMonitor calls = %d, want 0 (parent annotation should propagate)", called)
	}
}

// TestMonitorAutostart_RealOptOutCommandsAreAnnotated locks in the set
// of commands that must opt out, since each one has a clear reason
// (high-frequency, pure stdout, or self-managing). If we add a new
// command that fits one of those categories and forget to annotate it,
// nothing here catches it — but if we ACCIDENTALLY remove an
// annotation from one of these, this test fails.
func TestMonitorAutostart_RealOptOutCommandsAreAnnotated(t *testing.T) {
	root := NewRootCmd("test")

	mustOptOut := []string{
		"status-line",
		"completion",
		"monitor",
		"version",
		"agent-guide",
		"prepare-worker",
		"who",   // read-only; agents capture its output
		"peers", // read-only; agents capture its output
	}
	for _, name := range mustOptOut {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Errorf("command %q not found", name)
			continue
		}
		if cmd.Annotations[noMonitorAutostartAnnotation] != "true" {
			t.Errorf("command %q is missing %s=true annotation", name, noMonitorAutostartAnnotation)
		}
	}
}

// withFakeEnsureMonitor swaps ensureMonitorFn for the duration of a
// test and restores the original on cleanup.
func withFakeEnsureMonitor(t *testing.T, fake func()) {
	t.Helper()
	prev := ensureMonitorFn
	ensureMonitorFn = fake
	t.Cleanup(func() { ensureMonitorFn = prev })
}

// TestMonitorAutostart_NoCommandOverridesPersistentPreRunE pins the
// invariant that no subcommand defines its own PersistentPreRunE.
// Cobra resolves PersistentPreRunE by walking up from the invoked
// command and running the FIRST one it finds, so any subcommand-level
// override would silently shadow the root's auto-start hook for that
// entire subtree. This test fails loudly if that ever happens, so the
// auto-start behavior can't be silently disabled.
func TestMonitorAutostart_NoCommandOverridesPersistentPreRunE(t *testing.T) {
	root := NewRootCmd("test")
	var visit func(*cobra.Command)
	visit = func(c *cobra.Command) {
		if c != root && c.PersistentPreRunE != nil {
			t.Errorf("command %q has its own PersistentPreRunE — this would shadow the root's monitor auto-start hook", c.CommandPath())
		}
		if c != root && c.PersistentPreRun != nil {
			t.Errorf("command %q has its own PersistentPreRun — this would shadow the root's monitor auto-start hook", c.CommandPath())
		}
		for _, child := range c.Commands() {
			visit(child)
		}
	}
	visit(root)
}

// TestShouldAutostartMonitor_SkipsHelpAndUnderscore covers the
// defensive checks for cobra's hidden / built-in commands. These run
// on every shell tab-completion request or help invocation and must
// never fork a daemon, even though they don't carry the explicit
// no-autostart annotation.
func TestShouldAutostartMonitor_SkipsHelpAndUnderscore(t *testing.T) {
	tests := []struct {
		name string
		use  string
	}{
		{"help command", "help"},
		{"hidden complete command", "__complete"},
		{"hidden complete-no-desc command", "__completeNoDesc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: tt.use}
			if shouldAutostartMonitor(cmd) {
				t.Errorf("expected %q to be skipped, got autostart=true", tt.use)
			}
		})
	}

	// Sanity: a normal command name should not be skipped by the
	// hidden-command filter.
	t.Run("normal command is not skipped", func(t *testing.T) {
		cmd := &cobra.Command{Use: "normal-command"}
		if !shouldAutostartMonitor(cmd) {
			t.Error("normal command should not be skipped")
		}
	})
}
