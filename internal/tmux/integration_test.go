//go:build integration

// Integration tests for tmux primitives against a real tmux server. These
// pin the Mock's modeled behavior to ground truth — for any sequence the
// mock claims produces a particular pane order, the same sequence on real
// tmux is asserted to produce the same order.
//
// These tests start a tmux server on an isolated socket (`-L bay-test-...`)
// so they don't disturb the user's running tmux. They're skipped by
// default; run them explicitly:
//
//   go test -tags=integration ./internal/tmux/
//
// Requires tmux to be installed and on PATH.

package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// integrationServer wraps a tmux server running on a private socket
// path, so the test's commands can't touch the user's regular tmux
// state.
type integrationServer struct {
	socketPath string
	t          *testing.T
}

// startTmux starts a new tmux server on a private socket and registers
// a cleanup hook that kills the server and removes the socket dir when
// the test ends.
//
// The socket dir lives directly under /tmp rather than t.TempDir()
// because tmux enforces a ~104-character limit on socket paths
// (sockaddr_un.sun_path) and t.TempDir on macOS returns long
// /var/folders/.../T/TestName.../001 paths that overflow it. /tmp is
// available on every Unix host bay supports.
//
// Using `-S <path>` (vs `-L <name>`) lets us control the socket
// location, which sidesteps the dead-socket leak that happens with
// `-L`: tmux's kill-server drops the server but does not unlink the
// socket file, so socket files would accumulate in $TMUX_TMPDIR.
func startTmux(t *testing.T) *integrationServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bayit-")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	s := &integrationServer{
		socketPath: filepath.Join(dir, "s"),
		t:          t,
	}
	t.Cleanup(func() {
		s.killServer()
		_ = os.RemoveAll(dir)
	})
	return s
}

// setupSession is the shared prologue for the integration tests below:
// starts a private tmux server, creates a session, and returns the
// initial window ID and root pane ID.
func setupSession(t *testing.T) (s *integrationServer, winID, rootPaneID string) {
	t.Helper()
	s = startTmux(t)
	s.newDetachedSession("test")
	winID = s.firstWindowID("test")
	rootPaneID = s.listPaneIDs(winID)[0]
	return
}

func (s *integrationServer) tmuxCmd(args ...string) (string, error) {
	full := append([]string{"-S", s.socketPath}, args...)
	out, err := exec.Command("tmux", full...).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("tmux %s: %w (%s)",
			strings.Join(full, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *integrationServer) mustCmd(args ...string) string {
	s.t.Helper()
	out, err := s.tmuxCmd(args...)
	if err != nil {
		s.t.Fatalf("%v", err)
	}
	return out
}

func (s *integrationServer) realImplementation(t *testing.T) *Real {
	t.Helper()
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("find tmux: %v", err)
	}
	wrapperDir := t.TempDir()
	wrapperPath := filepath.Join(wrapperDir, "tmux")
	script := "#!/bin/sh\nexec \"$TMUX_REAL_BIN\" -S \"$TMUX_SOCKET_PATH\" \"$@\"\n"
	if err := os.WriteFile(wrapperPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write tmux wrapper: %v", err)
	}
	t.Setenv("TMUX_REAL_BIN", realTmux)
	t.Setenv("TMUX_SOCKET_PATH", s.socketPath)
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return NewReal()
}

func (s *integrationServer) killServer() {
	_ = exec.Command("tmux", "-S", s.socketPath, "kill-server").Run()
}

func (s *integrationServer) newDetachedSession(name string) {
	// -d: detached. -x/-y: pin a known terminal size so split-window
	// success doesn't depend on the test runner's TERM.
	s.mustCmd("new-session", "-d", "-s", name, "-x", "200", "-y", "60")
}

// firstWindowID returns the ID of the active window in the session.
func (s *integrationServer) firstWindowID(session string) string {
	return s.mustCmd("display-message", "-t", session, "-p", "#{window_id}")
}

// listPaneIDs returns pane IDs in tmux's natural depth-first order.
func (s *integrationServer) listPaneIDs(target string) []string {
	out := s.mustCmd("list-panes", "-t", target, "-F", "#{pane_id}")
	return strings.Split(out, "\n")
}

// split runs split-window and returns the new pane ID.
func (s *integrationServer) split(target, dir string, before bool) string {
	args := []string{"split-window"}
	if before {
		args = append(args, "-fb")
	}
	flag := "-v"
	if dir == "h" {
		flag = "-h"
	}
	args = append(args, "-t", target, flag, "-c", "/tmp", "-P", "-F", "#{pane_id}")
	return s.mustCmd(args...)
}

// killPane kills a pane by ID. Mirror of the SplitWindow helper for
// callsite symmetry — keeps tmux command details out of test bodies.
func (s *integrationServer) killPane(paneID string) {
	s.mustCmd("kill-pane", "-t", paneID)
}

// paneGeometry returns (left, top, width, height) for a pane.
func (s *integrationServer) paneGeometry(paneID string) (left, top, width, height int) {
	out := s.mustCmd("display-message", "-t", paneID, "-p",
		"#{pane_left} #{pane_top} #{pane_width} #{pane_height}")
	if _, err := fmt.Sscanf(out, "%d %d %d %d", &left, &top, &width, &height); err != nil {
		s.t.Fatalf("parsing geometry %q: %v", out, err)
	}
	return
}

// --- Tests ---

// TestIntegration_SplitNestsBinaryTree mirrors TestMockLayout_SplitNestsBinaryTree.
// Two successive splits against the previous pane should yield three panes
// in their creation order in real tmux too.
func TestIntegration_SplitNestsBinaryTree(t *testing.T) {
	s, win, root := setupSession(t)
	a := s.split(root, "v", false)
	b := s.split(a, "v", false)

	got := s.listPaneIDs(win)
	want := []string{root, a, b}
	if !slices.Equal(got, want) {
		t.Errorf("pane order = %v, want %v", got, want)
	}
}

// TestIntegration_SplitBeforeWrapsRoot mirrors TestMockLayout_SplitBeforeWrapsRoot.
// `split-window -fb` should insert the new pane at the top/left of the
// window, ahead of any existing panes.
func TestIntegration_SplitBeforeWrapsRoot(t *testing.T) {
	s, win, root := setupSession(t)
	b := s.split(root, "v", false)
	prepended := s.split(root, "v", true)

	got := s.listPaneIDs(win)
	want := []string{prepended, root, b}
	if !slices.Equal(got, want) {
		t.Errorf("pane order = %v, want %v", got, want)
	}

	// The prepended pane must occupy the topmost row in the window.
	_, prependedTop, _, _ := s.paneGeometry(prepended)
	_, rootTop, _, _ := s.paneGeometry(root)
	if prependedTop >= rootTop {
		t.Errorf("prepended pane top=%d not above root top=%d", prependedTop, rootTop)
	}
}

// TestIntegration_KillPaneCollapsesBinarySplit mirrors
// TestMockLayout_KillPaneCollapsesBinarySplit. After killing one of two
// sibling panes, the survivor should be the only pane left in the window
// (tmux's binary-split tree collapses).
func TestIntegration_KillPaneCollapsesBinarySplit(t *testing.T) {
	s, win, root := setupSession(t)
	other := s.split(root, "v", false)
	s.killPane(other)

	got := s.listPaneIDs(win)
	want := []string{root}
	if !slices.Equal(got, want) {
		t.Errorf("after kill, pane order = %v, want %v", got, want)
	}
}

// TestIntegration_MixedAxisLayout exercises the same shape as
// TestSurfaceRestore_LayoutMixedAxisStaysIntact: a horizontal split on the
// outer axis with a vertical split inside the right half.
func TestIntegration_MixedAxisLayout(t *testing.T) {
	s, win, root := setupSession(t)
	right := s.split(root, "h", false)
	rightBottom := s.split(right, "v", false)

	got := s.listPaneIDs(win)
	want := []string{root, right, rightBottom}
	if !slices.Equal(got, want) {
		t.Errorf("pane order = %v, want %v", got, want)
	}

	// Geometry sanity: root and right share the same row (horizontal
	// split); right and rightBottom share the same column (vertical
	// split inside right's region).
	rootLeft, rootTop, _, _ := s.paneGeometry(root)
	rightLeft, rightTop, _, _ := s.paneGeometry(right)
	rightBottomLeft, rightBottomTop, _, _ := s.paneGeometry(rightBottom)

	if rootTop != rightTop {
		t.Errorf("root and right not on same row: root_top=%d right_top=%d", rootTop, rightTop)
	}
	if rootLeft >= rightLeft {
		t.Errorf("root_left=%d not left of right_left=%d", rootLeft, rightLeft)
	}
	if rightLeft != rightBottomLeft {
		t.Errorf("right and rightBottom not in same column: right_left=%d rightBottom_left=%d",
			rightLeft, rightBottomLeft)
	}
	if rightTop >= rightBottomTop {
		t.Errorf("right_top=%d not above rightBottom_top=%d", rightTop, rightBottomTop)
	}
}

// TestIntegration_StackedCloseRestoreSequence simulates the user-reported
// "delete last of three, restore appends" scenario at the tmux primitive
// level. Build [root, middle, bottom], close bottom, re-add by splitting
// against middle (matching SurfaceAdd's `lastSurfaceInLayoutGroup` behavior),
// and verify the new pane lands at the visual end.
func TestIntegration_StackedCloseRestoreSequence(t *testing.T) {
	s, win, root := setupSession(t)
	middle := s.split(root, "v", false)
	bottom := s.split(middle, "v", false)

	got := s.listPaneIDs(win)
	want := []string{root, middle, bottom}
	if !slices.Equal(got, want) {
		t.Fatalf("initial pane order = %v, want %v", got, want)
	}

	// Close the last pane, then re-add by splitting against the now-last
	// surviving sibling — this is what bay's restore does.
	s.killPane(bottom)
	got = s.listPaneIDs(win)
	if !slices.Equal(got, []string{root, middle}) {
		t.Fatalf("after kill, pane order = %v, want %v", got, []string{root, middle})
	}

	restored := s.split(middle, "v", false)
	got = s.listPaneIDs(win)
	want = []string{root, middle, restored}
	if !slices.Equal(got, want) {
		t.Errorf("after restore, pane order = %v, want %v", got, want)
	}
}

// TestIntegration_SessionOptionsTargetExact pins the workaround for a
// tmux quirk that motivated resolveSessionTarget in real.go: on tmux
// 3.6a, set-option / show-options reject the "=name" exact-match
// prefix that has-session and rename-session honor. Targeting by bare
// name then prefix-matches sibling sessions, so writing to "loom"
// when only "loom-old" exists silently writes to "loom-old". The
// $session_id form ($1, $2, ...) is unambiguous; bay's real impl
// resolves the name to that ID via list-sessions before issuing
// option commands.
//
// If a future tmux version restores `=name` semantics for option
// commands, this test still passes (the $id approach also works);
// the test fails only if tmux's targeting semantics regress in a way
// that would let the prefix-match bug back in.
func TestIntegration_SessionOptionsTargetExact(t *testing.T) {
	s := startTmux(t)
	s.newDetachedSession("loom-old")
	r := s.realImplementation(t)

	// The prefix-match bug is easiest to catch when only the longer
	// sibling exists: a bare `-t loom` would silently target
	// "loom-old". Real.SetSessionOption must resolve exact names first
	// and reject the missing target instead.
	if err := r.SetSessionOption("loom", "@bay-test-marker", "WRONG"); err == nil {
		t.Fatal("SetSessionOption(\"loom\") succeeded while only loom-old exists")
	}
	if got, err := r.GetSessionOption("loom-old", "@bay-test-marker"); err != nil || got != "" {
		t.Fatalf("loom-old marker after failed loom write = %q, err=%v; want empty", got, err)
	}

	if err := r.SetSessionOption("loom-old", "@bay-test-marker", "VALUE-OLD"); err != nil {
		t.Fatalf("SetSessionOption(\"loom-old\"): %v", err)
	}
	if got, err := r.GetSessionOption("loom", "@bay-test-marker"); err != nil || got != "" {
		t.Fatalf("GetSessionOption(\"loom\") = %q, err=%v; want empty while loom is absent", got, err)
	}

	if err := r.NewSession("loom"); err != nil {
		t.Fatalf("NewSession(\"loom\"): %v", err)
	}
	if err := r.SetSessionOption("loom", "@bay-test-marker", "VALUE-LOOM"); err != nil {
		t.Fatalf("SetSessionOption(\"loom\"): %v", err)
	}

	if got, err := r.GetSessionOption("loom", "@bay-test-marker"); err != nil || got != "VALUE-LOOM" {
		t.Errorf("loom marker = %q, err=%v; want VALUE-LOOM", got, err)
	}
	if got, err := r.GetSessionOption("loom-old", "@bay-test-marker"); err != nil || got != "VALUE-OLD" {
		t.Errorf("loom-old marker = %q, err=%v; want VALUE-OLD", got, err)
	}
}

// TestIntegration_CurrentSessionAnchoring pins the tmux behavior that
// makes currentTarget() necessary.
//
// With no client context, tmux resolves an untargeted query against the
// most recently active session on the server — not the session that
// invoked the command. bay's keybindings run via run-shell, which
// exports TMUX but no TMUX_PANE, so before anchoring, a key pressed in
// one dock resolved to whichever session happened to be newest
// (including a detached one spawned by unrelated background tooling).
func TestIntegration_CurrentSessionAnchoring(t *testing.T) {
	s := startTmux(t)
	s.newDetachedSession("alpha")
	// beta is created second, so it is the most recently active session
	// and wins any untargeted resolution.
	s.newDetachedSession("beta")

	alphaID := s.mustCmd("display-message", "-t", "alpha", "-p", "#{session_id}")
	if !strings.HasPrefix(alphaID, "$") {
		t.Fatalf("unexpected session id %q", alphaID)
	}

	r := s.realImplementation(t)

	// Reproduce the run-shell environment: TMUX names alpha's session,
	// TMUX_PANE is absent.
	t.Setenv("TMUX", fmt.Sprintf("%s,1234,%s", s.socketPath, strings.TrimPrefix(alphaID, "$")))
	t.Setenv("TMUX_PANE", "")

	// Ground truth: untargeted resolution picks beta, not the invoking
	// session. If tmux ever stops doing this, the fix is no longer
	// load-bearing and this test should be the thing that says so.
	if got := s.mustCmd("display-message", "-p", "#{session_name}"); got != "beta" {
		t.Errorf("untargeted display-message = %q, want %q "+
			"(tmux no longer prefers the newest session)", got, "beta")
	}

	// The fix: anchored to TMUX's session, bay sees alpha.
	got, err := r.CurrentSession()
	if err != nil {
		t.Fatalf("CurrentSession: %v", err)
	}
	if got != "alpha" {
		t.Errorf("CurrentSession() = %q, want %q", got, "alpha")
	}
}

// TestIntegration_CurrentPaneIDAnchoring covers the destructive edge of
// the same bug: `bay close self` / `bay sf close self` resolve the
// target from the current pane, so an unanchored answer closes a
// surface in the wrong session.
func TestIntegration_CurrentPaneIDAnchoring(t *testing.T) {
	s := startTmux(t)
	s.newDetachedSession("alpha")
	alphaPane := s.listPaneIDs(s.firstWindowID("alpha"))[0]
	s.newDetachedSession("beta")
	betaPane := s.listPaneIDs(s.firstWindowID("beta"))[0]

	r := s.realImplementation(t)

	// The run-shell environment again: no TMUX_PANE to fall back on.
	// (tmux honors TMUX_PANE natively when it is set, so anchoring only
	// has to earn its keep when it is absent — exactly the keybinding
	// case, and exactly when `close self` is destructive.)
	alphaID := s.mustCmd("display-message", "-t", "alpha", "-p", "#{session_id}")
	t.Setenv("TMUX", fmt.Sprintf("%s,1234,%s", s.socketPath, strings.TrimPrefix(alphaID, "$")))
	t.Setenv("TMUX_PANE", "")

	got, err := r.CurrentPaneID()
	if err != nil {
		t.Fatalf("CurrentPaneID: %v", err)
	}
	if got != alphaPane {
		t.Errorf("CurrentPaneID() = %q, want %q (beta's pane is %q)", got, alphaPane, betaPane)
	}
}

// TestIntegration_ListPanesReportsDeadPane pins ListWindows parsing and Pane.Dead to tmux's
// pane_dead: a pane whose process exits under remain-on-exit stays in
// the window and must read as dead, since its PID is stale.
func TestIntegration_ListPanesReportsDeadPane(t *testing.T) {
	s, winID, rootPaneID := setupSession(t)
	s.mustCmd("set-option", "-w", "-t", winID, "remain-on-exit", "on")
	deadPane := s.mustCmd("split-window", "-t", rootPaneID, "-P", "-F", "#{pane_id}", "true")
	r := s.realImplementation(t)

	// The lookup reaches the pane through the session's window list.
	windows, err := r.ListWindows("test")
	if err != nil || len(windows) != 1 || windows[0].ID != winID {
		t.Fatalf("ListWindows = %+v, %v; want window %s", windows, err, winID)
	}

	byID := func() map[string]Pane {
		panes, err := r.ListPanes(winID)
		if err != nil {
			t.Fatalf("ListPanes: %v", err)
		}
		m := map[string]Pane{}
		for _, p := range panes {
			m[p.ID] = p
		}
		return m
	}
	deadline := time.Now().Add(5 * time.Second)
	panes := byID()
	for !panes[deadPane].Dead && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		panes = byID()
	}
	if !panes[deadPane].Dead {
		t.Fatalf("pane %s never reported dead: %+v", deadPane, panes)
	}
	if panes[rootPaneID].Dead {
		t.Fatalf("live root pane reported dead: %+v", panes[rootPaneID])
	}
}

// TestIntegration_WindowTargetsNameTheSession checks that SelectWindow and
// MoveWindowAfter act in the named session when the window is also linked
// into another session that was used more recently.
func TestIntegration_WindowTargetsNameTheSession(t *testing.T) {
	s := startTmux(t)
	s.newDetachedSession("dock")
	first := s.firstWindowID("dock")
	s.mustCmd("new-window", "-d", "-t", "=dock:")
	s.newDetachedSession("aux")
	s.mustCmd("link-window", "-s", "=dock:"+first, "-t", "=aux:")
	r := s.realImplementation(t)

	added := s.mustCmd("new-window", "-d", "-t", "=dock:", "-P", "-F", "#{window_id}")
	if err := r.MoveWindowAfter("dock", added, first); err != nil {
		t.Fatalf("MoveWindowAfter: %v", err)
	}
	dockWindows := strings.Split(s.mustCmd("list-windows", "-t", "=dock", "-F", "#{window_id}"), "\n")
	if len(dockWindows) != 3 || dockWindows[1] != added {
		t.Errorf("dock windows = %v, want %s second", dockWindows, added)
	}
	if aux := s.mustCmd("list-windows", "-t", "=aux", "-F", "#{window_id}"); strings.Contains(aux, added) {
		t.Errorf("aux windows = %q, want no %s", aux, added)
	}

	if err := r.SelectWindow("dock", first); err != nil {
		t.Fatalf("SelectWindow: %v", err)
	}
	if got := s.mustCmd("display-message", "-t", "=dock:", "-p", "#{window_id}"); got != first {
		t.Errorf("dock's current window = %s, want %s", got, first)
	}
}

// TestIntegration_KillSessionKillsWindowsLinkedElsewhere checks that a
// window of a killed session does not keep running in another session it
// is linked into.
func TestIntegration_KillSessionKillsWindowsLinkedElsewhere(t *testing.T) {
	s := startTmux(t)
	s.newDetachedSession("dock")
	shared := s.firstWindowID("dock")
	s.mustCmd("new-window", "-d", "-t", "=dock:")
	s.newDetachedSession("aux")
	s.mustCmd("link-window", "-d", "-s", "=dock:"+shared, "-t", "=aux:")
	r := s.realImplementation(t)

	if err := r.KillSession("dock"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	if aux := s.mustCmd("list-windows", "-t", "=aux", "-F", "#{window_id}"); strings.Contains(aux, shared) {
		t.Errorf("aux windows = %q, want no %s", aux, shared)
	}
	if out, err := s.tmuxCmd("has-session", "-t", "=dock"); err == nil {
		t.Errorf("dock session still exists: %s", out)
	}
}

// TestMain lets the test binary play a process inside a tmux window:
// with BAY_TEST_KILL_SESSION set, it kills that session from inside it.
func TestMain(m *testing.M) {
	if name := os.Getenv("BAY_TEST_KILL_SESSION"); name != "" {
		_ = NewReal().KillSession(name)
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestIntegration_KillSessionFromInsideKillsEveryWindow runs KillSession
// in the session's first window. Killing that window ends the process, so
// it must be killed last, or the windows after it keep running.
func TestIntegration_KillSessionFromInsideKillsEveryWindow(t *testing.T) {
	s := startTmux(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s.newDetachedSession("keep")
	s.mustCmd("new-session", "-d", "-s", "dock", "-e", "BAY_TEST_KILL_SESSION=dock", "sleep 3; exec "+exe)
	s.mustCmd("new-window", "-d", "-t", "=dock:", "sleep 60")
	s.mustCmd("new-window", "-d", "-t", "=dock:", "sleep 60")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.tmuxCmd("has-session", "-t", "=dock"); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the session survived: %s", s.mustCmd("list-windows", "-a", "-F", "#{session_name}:#{window_index} #{pane_current_command}"))
}
