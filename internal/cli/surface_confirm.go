package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/commonfabric/bay/internal/manifest"
)

// closeConfirmWindow is the second-tap deadline. Short enough that two
// presses read as deliberate (Claude Code's ctrl-D-twice timing), long
// enough to forgive a moment of hesitation.
const closeConfirmWindow = 1500 * time.Millisecond

// shouldConfirmLastSurfaceClose scopes the double-tap guard to non-interactive
// invocations, which covers tmux run-shell keybindings such as Option+w.
// Interactive CLI invocations are already deliberate enough to close the last
// surface on the first command.
//
// Package-level var so tests can exercise both paths without depending on the
// test runner's stdin.
var shouldConfirmLastSurfaceClose = func() bool {
	return !stdinIsTTY()
}

// flashFunc displays a transient status-line message. Mirrors
// tmux.Interface.DisplayMessage but narrowed so confirmLastSurfaceClose
// doesn't need the rest of the tmux surface.
type flashFunc func(msg string, durationMs int) error

// tapConfirmed implements the shared double-tap protocol: returns true and
// consumes the record when a recent attempt for the same (dock, bay) is on
// file (the user is double-tapping). Otherwise records a fresh attempt,
// flashes msg asking for a second tap, and returns false. msg is a func so
// callers whose message needs git probes only pay for them on the first tap.
func tapConfirmed(path string, flash flashFunc, dockName, bayName string, msg func() string) bool {
	if recordedRecently(path, dockName, bayName, time.Now()) {
		_ = os.Remove(path)
		return true
	}
	if err := writeCloseConfirm(path, dockName, bayName, time.Now()); err != nil {
		// Fail open: without state we can't enforce a second tap. Better
		// to let the close go through than wedge the user with no recovery.
		return true
	}
	// Message duration matches the confirm window so the message vanishing
	// is itself the deadline — no need to say "2s" and risk a stale number.
	_ = flash(msg(), int(closeConfirmWindow/time.Millisecond))
	return false
}

// confirmLastSurfaceClose decides whether to proceed with closing the last
// surface in a bay.
//
// Package-level var so tests can substitute a deterministic implementation.
var confirmLastSurfaceClose = func(flash flashFunc, dockName, bayName string) bool {
	return tapConfirmed(bayPaths().CloseConfirm, flash, dockName, bayName, func() string {
		return fmt.Sprintf("press again to close last surface in %q", bayName)
	})
}

// confirmLastHomeClose uses the same double-tap record as normal last-surface
// close, but with wording that makes clear the action dismisses the dock tmux
// UI/session and does not unregister the dock.
var confirmLastHomeClose = func(flash flashFunc, dockName string) bool {
	return tapConfirmed(bayPaths().CloseConfirm, flash, dockName, manifest.HomeBayID, func() string {
		return homeCloseConfirmMessage(dockName)
	})
}

// confirmBayCloseTap gates `bay close --tap` (the Option+Shift+W force-close
// keybinding) behind the same double-tap protocol. It uses its own record
// file so a pending Option+w last-surface tap can never be consumed as
// authorization to force-close a dirty bay; the two keys always require
// their own second press. The caller supplies msg naming what a confirmed
// close will discard; it runs only when the first tap flashes.
//
// Package-level var so tests can substitute a deterministic implementation.
var confirmBayCloseTap = func(flash flashFunc, dockName, bayID string, msg func() string) bool {
	return tapConfirmed(bayPaths().ForceCloseConfirm, flash, dockName, bayID, msg)
}

func homeCloseConfirmMessage(dockName string) string {
	return fmt.Sprintf("press again to dismiss dock %q UI/session; dock stays registered", dockName)
}

// recordedRecently reports whether a close-confirm record exists for
// (dock, bay) within closeConfirmWindow of now. Missing or unreadable file,
// mismatched target, or an expired record all return false.
func recordedRecently(path, dockName, bayName string, now time.Time) bool {
	d, w, t, ok := readCloseConfirm(path)
	if !ok {
		return false
	}
	if d != dockName || w != bayName {
		return false
	}
	return now.Sub(t) <= closeConfirmWindow
}

// File format: a single line "<dock>\t<bay>\t<unix-nanos>\n". Tabs are
// disallowed in dock/bay names by ValidateName, so the split is unambiguous.
func readCloseConfirm(path string) (dock, bay string, when time.Time, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", time.Time{}, false
	}
	line := strings.TrimRight(string(data), "\n")
	parts := strings.Split(line, "\t")
	if len(parts) != 3 {
		return "", "", time.Time{}, false
	}
	ns, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", "", time.Time{}, false
	}
	return parts[0], parts[1], time.Unix(0, ns), true
}

func writeCloseConfirm(path, dock, bay string, when time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line := fmt.Sprintf("%s\t%s\t%d\n", dock, bay, when.UnixNano())
	return os.WriteFile(path, []byte(line), 0o644)
}
