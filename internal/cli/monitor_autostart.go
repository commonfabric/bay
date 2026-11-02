package cli

import (
	"os"
	"strings"
	"syscall"

	"github.com/commonfabric/bay/internal/monitor"
	"github.com/spf13/cobra"
)

// noMonitorAutostartAnnotation, when set on a cobra command, tells the
// root command's PersistentPreRunE not to auto-start the monitor for
// that command. Used for high-frequency or read-only commands like
// status-line, completion, monitor *, version, agent-guide.
const noMonitorAutostartAnnotation = "bay-no-monitor-autostart"

// ensureMonitorFn is the function the auto-start hook calls. It's a
// package var so tests can swap it without forking a real process.
// The default implementation reads the PID file, checks the process,
// and forks the monitor if it's not running. Failures are silent —
// the auto-start path runs in front of every CLI command, and a noisy
// warning per invocation would be worse than the broken-monitor case
// (which `bay doctor` reports anyway).
var ensureMonitorFn = ensureMonitor

func ensureMonitor() {
	p := bayPaths()
	pid, err := monitor.ReadPIDFile(p.PIDFile)
	if err == nil {
		proc, err := os.FindProcess(pid)
		if err == nil && proc.Signal(syscall.Signal(0)) == nil {
			return // already running
		}
	}
	// Not running — start it. Need a full monitor for Start() which
	// forks `bay monitor run`.
	mon, err := newMonitorWithConfig()
	if err != nil {
		return
	}
	_ = mon.Start()
}

// shouldAutostartMonitor walks the command tree from root to the
// invoked command and returns false if any node along the way has the
// no-autostart annotation. The walk lets a parent command (like
// "monitor") opt the entire subtree out without per-subcommand
// annotations.
//
// Note: there's no way for a child to opt back IN once an ancestor has
// opted out — if a parent is annotated, every child is opted out
// regardless. We have no use case for per-child override today; if
// that changes, this function needs a different shape (e.g. a
// "force-autostart" annotation that beats the inherited opt-out).
//
// Cobra's hidden internal commands (__complete, __completeNoDesc, help)
// are also excluded — they run on every shell tab-completion request
// or help invocation and should never fork a daemon.
func shouldAutostartMonitor(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Annotations[noMonitorAutostartAnnotation] == "true" {
			return false
		}
		name := c.Name()
		if name == "help" || strings.HasPrefix(name, "__") {
			return false
		}
	}
	return true
}

// installMonitorAutostart wires a PersistentPreRunE on root that calls
// ensureMonitorFn before any non-opt-out command's RunE. If a command
// already has its own PersistentPreRunE, this preserves it.
func installMonitorAutostart(root *cobra.Command) {
	prev := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if shouldAutostartMonitor(cmd) {
			ensureMonitorFn()
		}
		if prev != nil {
			return prev(cmd, args)
		}
		return nil
	}
}
