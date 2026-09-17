package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
)

// noFlash is a flashFunc that drops every message — used by tests that
// don't care about message text.
func noFlash(string, int) error { return nil }

// --- file-format helpers ---

func TestCloseConfirm_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close-confirm")
	now := time.Now()
	if err := writeCloseConfirm(path, "labs", "b1", now); err != nil {
		t.Fatalf("write: %v", err)
	}
	d, w, when, ok := readCloseConfirm(path)
	if !ok {
		t.Fatalf("read: !ok")
	}
	if d != "labs" || w != "b1" {
		t.Errorf("got dock=%q bay=%q, want labs/b1", d, w)
	}
	if !when.Equal(now) {
		t.Errorf("timestamp mismatch: got %v, want %v", when, now)
	}
}

func TestRecordedRecently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close-confirm")
	now := time.Now()
	_ = writeCloseConfirm(path, "labs", "b1", now)

	cases := []struct {
		name string
		dock string
		bay  string
		when time.Time
		want bool
	}{
		{"same target, within window", "labs", "b1", now.Add(closeConfirmWindow / 2), true},
		{"same target, at edge", "labs", "b1", now.Add(closeConfirmWindow), true},
		{"same target, expired", "labs", "b1", now.Add(closeConfirmWindow + time.Millisecond), false},
		{"different bay", "labs", "b2", now, false},
		{"different dock", "other", "b1", now, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := recordedRecently(path, c.dock, c.bay, c.when); got != c.want {
				t.Errorf("recordedRecently = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRecordedRecently_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist")
	if recordedRecently(path, "labs", "b1", time.Now()) {
		t.Error("recordedRecently should be false for missing file")
	}
}

// --- runSurfaceClose wiring ---

// withConfirmStubs replaces both confirmation hooks with deterministic stubs
// for the duration of a test. lastSurfaceCalls and agentCalls receive every
// invocation so tests can assert call counts and arguments.
func withConfirmStubs(t *testing.T, lastSurface, agent func(name string) bool) (lastCalls, agentCalls *[]string) {
	t.Helper()
	lc := []string{}
	ac := []string{}

	origLast := confirmLastSurfaceClose
	origAgent := confirmAgentClose
	origShould := shouldConfirmLastSurfaceClose
	t.Cleanup(func() {
		confirmLastSurfaceClose = origLast
		confirmAgentClose = origAgent
		shouldConfirmLastSurfaceClose = origShould
	})

	shouldConfirmLastSurfaceClose = func() bool { return true }
	confirmLastSurfaceClose = func(_ flashFunc, dock, bay string) bool {
		lc = append(lc, dock+":"+bay)
		if lastSurface == nil {
			return true
		}
		return lastSurface(dock + ":" + bay)
	}
	confirmAgentClose = func(name string) bool {
		ac = append(ac, name)
		if agent == nil {
			return true
		}
		return agent(name)
	}
	return &lc, &ac
}

func TestRunSurfaceClose_LastSurface_NonInteractiveFiresDoubleTap(t *testing.T) {
	last, agent := withConfirmStubs(t, func(string) bool { return false }, nil)

	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	// Sole surface in the bay — closing it should trigger the
	// last-surface confirmation, which our stub declines.
	if err := runSurfaceClose(eng, []string{"b1:shell"}, "", "", false); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}

	if len(*last) != 1 || (*last)[0] != "labs:b1" {
		t.Errorf("expected one last-surface confirm for labs:b1, got %v", *last)
	}
	if len(*agent) != 0 {
		t.Errorf("agent prompt should not fire for shell surface, got %v", *agent)
	}

	bay, _ := eng.BayShow("labs", "b1")
	if len(bay.Surfaces) != 1 {
		t.Errorf("declined confirmation should leave surface intact, got %d surfaces", len(bay.Surfaces))
	}
}

func TestRunSurfaceClose_LastSurface_DirtyShowsRefusalInsteadOfDoubleTap(t *testing.T) {
	last, agent := withConfirmStubs(t,
		func(string) bool {
			t.Fatalf("last-surface confirm should not fire when bay is dirty")
			return false
		},
		nil,
	)

	eng, mockTmux, mockGit, _ := testNavEngine(t)
	bay, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := os.MkdirAll(bay.Path, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	mockGit.SetDirty(bay.Path, true)

	if err := runSurfaceClose(eng, []string{"b1:shell"}, "", "", false); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}

	if len(*last) != 0 {
		t.Errorf("last-surface confirm should not fire, got %v", *last)
	}
	if len(*agent) != 0 {
		t.Errorf("agent prompt should not fire for shell surface, got %v", *agent)
	}
	if msgs := mockTmux.DisplayMessages(); len(msgs) != 1 || !strings.Contains(msgs[0], "local changes may be work in progress") {
		t.Fatalf("expected dirty refusal toast, got %v", msgs)
	}

	got, _ := eng.BayShow("labs", "b1")
	if len(got.Surfaces) != 1 {
		t.Errorf("dirty refusal should leave surface intact, got %d surfaces", len(got.Surfaces))
	}
}

func TestRunSurfaceClose_LastSurface_UnpushedShowsRefusalInsteadOfDoubleTap(t *testing.T) {
	last, agent := withConfirmStubs(t,
		func(string) bool {
			t.Fatalf("last-surface confirm should not fire when bay has unlanded commits")
			return false
		},
		nil,
	)

	eng, mockTmux, mockGit, _ := testNavEngine(t)
	bay, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := os.MkdirAll(bay.Path, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	mockGit.SetUnpushed(bay.Path, true)

	if err := runSurfaceClose(eng, []string{"b1:shell"}, "", "", false); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}

	if len(*last) != 0 {
		t.Errorf("last-surface confirm should not fire, got %v", *last)
	}
	if len(*agent) != 0 {
		t.Errorf("agent prompt should not fire for shell surface, got %v", *agent)
	}
	if msgs := mockTmux.DisplayMessages(); len(msgs) != 1 || !strings.Contains(msgs[0], "bay kept (unlanded commits)") {
		t.Fatalf("expected unlanded refusal toast, got %v", msgs)
	}

	got, _ := eng.BayShow("labs", "b1")
	if len(got.Surfaces) != 1 {
		t.Errorf("unlanded refusal should leave surface intact, got %d surfaces", len(got.Surfaces))
	}
}

func TestRunSurfaceClose_LastSurface_NonInteractiveAcceptedCloses(t *testing.T) {
	withConfirmStubs(t, func(string) bool { return true }, nil)

	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	if err := runSurfaceClose(eng, []string{"b1:shell"}, "", "", false); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	if len(bay.Surfaces) != 0 {
		t.Errorf("accepted confirmation should close surface, got %d surfaces", len(bay.Surfaces))
	}
}

func TestRunSurfaceClose_LastSurface_InteractiveSkipsDoubleTap(t *testing.T) {
	last, agent := withConfirmStubs(t,
		func(string) bool {
			t.Fatalf("last-surface confirm should not fire for interactive CLI close")
			return false
		},
		nil,
	)
	shouldConfirmLastSurfaceClose = func() bool { return false }

	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	if err := runSurfaceClose(eng, []string{"b1:shell"}, "", "", false); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}

	if len(*last) != 0 {
		t.Errorf("last-surface confirm should not fire for interactive close, got %v", *last)
	}
	if len(*agent) != 0 {
		t.Errorf("agent prompt should not fire for shell surface, got %v", *agent)
	}

	bay, _ := eng.BayShow("labs", "b1")
	if len(bay.Surfaces) != 0 {
		t.Errorf("interactive last-surface close should close surface, got %d surfaces", len(bay.Surfaces))
	}
}

func TestRunSurfaceClose_NotLastSurface_SkipsDoubleTap(t *testing.T) {
	last, _ := withConfirmStubs(t, nil, nil)

	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeShell, Name: "extra", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	// Two surfaces present — closing one shouldn't trigger last-surface.
	if err := runSurfaceClose(eng, []string{"b1:extra"}, "", "", false); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}

	if len(*last) != 0 {
		t.Errorf("last-surface confirm should not fire when other surfaces remain, got %v", *last)
	}
}

func TestRunSurfaceClose_LastSurface_ForceBypassesBoth(t *testing.T) {
	last, agent := withConfirmStubs(t,
		func(string) bool {
			t.Fatalf("last-surface confirm should not fire with --force")
			return false
		},
		func(string) bool {
			t.Fatalf("agent confirm should not fire with --force")
			return false
		},
	)

	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	if err := runSurfaceClose(eng, []string{"b1:shell"}, "", "", true); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}
	if len(*last) != 0 || len(*agent) != 0 {
		t.Errorf("--force should skip both confirms, got last=%v agent=%v", *last, *agent)
	}
}

func TestRunSurfaceClose_LastSurfaceAgent_NonInteractiveOnlyDoubleTap(t *testing.T) {
	// When the last surface is an agent, the double-tap supersedes the
	// agent y/N prompt — one confirmation, not two.
	last, agent := withConfirmStubs(t, func(string) bool { return true }, nil)

	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	// Add an agent surface alongside the default shell, then drop the
	// shell so the agent is the sole remaining surface. Closing the shell
	// while the agent exists isn't "last surface," so it goes through
	// without firing either prompt.
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeAgent, Name: "claude", Agent: "claude", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd agent: %v", err)
	}
	if err := eng.SurfaceClose("labs", "b1", "shell", true); err != nil {
		t.Fatalf("seed close shell: %v", err)
	}

	if err := runSurfaceClose(eng, []string{"b1:claude"}, "", "", false); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}

	if len(*last) != 1 {
		t.Errorf("expected one last-surface confirm, got %v", *last)
	}
	if len(*agent) != 0 {
		t.Errorf("agent confirm should be suppressed when last-surface fires, got %v", *agent)
	}
}

func TestRunSurfaceClose_LastSurfaceAgent_InteractivePromptsAgent(t *testing.T) {
	last, agent := withConfirmStubs(t,
		func(string) bool {
			t.Fatalf("last-surface confirm should not fire for interactive CLI close")
			return false
		},
		func(string) bool { return true },
	)
	shouldConfirmLastSurfaceClose = func() bool { return false }

	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeAgent, Name: "claude", Agent: "claude", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd agent: %v", err)
	}
	if err := eng.SurfaceClose("labs", "b1", "shell", true); err != nil {
		t.Fatalf("seed close shell: %v", err)
	}

	if err := runSurfaceClose(eng, []string{"b1:claude"}, "", "", false); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}

	if len(*last) != 0 {
		t.Errorf("last-surface confirm should not fire for interactive close, got %v", *last)
	}
	if len(*agent) != 1 || (*agent)[0] != "claude" {
		t.Errorf("interactive last-agent close should prompt once, got %v", *agent)
	}
}

func TestConfirmLastSurfaceClose_DefaultBehavior(t *testing.T) {
	// End-to-end of the default impl: first call returns false and writes
	// state + flashes a message; second call within the window returns
	// true and clears the file. Uses a temp DataDir via XDG_DATA_HOME so
	// we don't touch the user's real bay data.
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var msgs []string
	flash := func(msg string, _ int) error {
		msgs = append(msgs, msg)
		return nil
	}

	if confirmLastSurfaceClose(flash, "labs", "b1") {
		t.Fatal("first call should require a second tap")
	}
	if len(msgs) != 1 {
		t.Errorf("first call should flash a message, got %d", len(msgs))
	}

	if !confirmLastSurfaceClose(flash, "labs", "b1") {
		t.Fatal("second call within window should accept")
	}

	// Third call after a successful confirm starts fresh — file was cleared.
	if confirmLastSurfaceClose(flash, "labs", "b1") {
		t.Error("third call (after consume) should require a fresh tap")
	}
}

func TestConfirmLastSurfaceClose_DifferentBay_DoesNotCarry(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if confirmLastSurfaceClose(noFlash, "labs", "b1") {
		t.Fatal("first call on b1 should require second tap")
	}
	// Pressing again on a DIFFERENT bay should not consume b1's
	// pending confirmation — each bay stands alone.
	if confirmLastSurfaceClose(noFlash, "labs", "b2") {
		t.Error("close on b2 should not be accepted by b1's pending tap")
	}
}

func TestConfirmLastHomeClose_DefaultBehaviorAndMessage(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var msgs []string
	flash := func(msg string, _ int) error {
		msgs = append(msgs, msg)
		return nil
	}

	if confirmLastHomeClose(flash, "labs") {
		t.Fatal("first call should require a second tap")
	}
	if len(msgs) != 1 {
		t.Fatalf("first call should flash a message, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0], "dismiss dock") || !strings.Contains(msgs[0], "UI/session") {
		t.Fatalf("home close message = %q, want dock UI/session wording", msgs[0])
	}
	if !strings.Contains(msgs[0], "stays registered") {
		t.Fatalf("home close message = %q, want registered-dock wording", msgs[0])
	}

	if !confirmLastHomeClose(flash, "labs") {
		t.Fatal("second call within window should accept")
	}
	if confirmLastHomeClose(flash, "labs") {
		t.Error("third call should require a fresh tap")
	}
}

func TestRunSurfaceClose_LastHomeDoubleTapDismissesDock(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	eng, mockTmux, _, _ := testNavEngine(t)
	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	winID := home.Surfaces[0].Tmux.WindowID

	if err := runSurfaceClose(eng, []string{"shell"}, manifest.HomeBayID, "labs", false); err != nil {
		t.Fatalf("runSurfaceClose first: %v", err)
	}
	m1, _ := eng.LoadManifest()
	if home := m1.FindDock("labs").FindBayByID(manifest.HomeBayID); home == nil || len(home.Surfaces) != 1 {
		t.Fatalf("first attempt changed home = %+v, want unchanged", home)
	}
	if exists, _ := mockTmux.WindowExists(winID); !exists {
		t.Fatalf("first attempt killed home window %s", winID)
	}
	if has, _ := mockTmux.HasSession("labs"); !has {
		t.Fatal("first attempt killed the session")
	}
	msgs := mockTmux.DisplayMessages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "dismiss dock") || !strings.Contains(msgs[0], "UI/session") {
		t.Fatalf("first attempt messages = %v, want home-specific dismissal guidance", msgs)
	}

	mockTmux.Calls = nil
	if err := runSurfaceClose(eng, []string{"shell"}, manifest.HomeBayID, "labs", false); err != nil {
		t.Fatalf("runSurfaceClose second: %v", err)
	}
	m2, _ := eng.LoadManifest()
	dock := m2.FindDock("labs")
	if dock == nil {
		t.Fatal("dock disappeared after confirmed home close")
	}
	if dock.Path != dockPath {
		t.Fatalf("dock.Path = %q, want %q", dock.Path, dockPath)
	}
	if home := dock.FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("home persisted after confirmed close: %+v", home)
	}
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("tmux session still exists after confirmed home close")
	}
	for _, call := range mockTmux.Calls {
		if call.Method == "NewWindow" {
			t.Fatalf("confirmed home close should not create a replacement window; calls: %+v", mockTmux.Calls)
		}
		if call.Method == "SetWindowOption" && len(call.Args) >= 3 && call.Args[1] == "@bay-placeholder" {
			t.Fatalf("confirmed home close should not create a placeholder; calls: %+v", mockTmux.Calls)
		}
	}
}

func TestRunSurfaceClose_LastHomeForceSkipsConfirmation(t *testing.T) {
	origHome := confirmLastHomeClose
	origAgent := confirmAgentClose
	t.Cleanup(func() {
		confirmLastHomeClose = origHome
		confirmAgentClose = origAgent
	})
	confirmLastHomeClose = func(_ flashFunc, dock string) bool {
		t.Fatalf("home confirmation should not fire with --force for dock %q", dock)
		return false
	}
	confirmAgentClose = func(name string) bool {
		t.Fatalf("agent confirmation should not fire with --force for surface %q", name)
		return false
	}

	eng, mockTmux, _, _ := testNavEngine(t)
	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}

	if err := runSurfaceClose(eng, []string{"shell"}, manifest.HomeBayID, "labs", true); err != nil {
		t.Fatalf("runSurfaceClose --force: %v", err)
	}
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("tmux session still exists after forced home close")
	}
}

func TestRunSurfaceClose_LastHomeAgentUsesOnlyHomeConfirmation(t *testing.T) {
	origHome := confirmLastHomeClose
	origAgent := confirmAgentClose
	origShould := shouldConfirmLastSurfaceClose
	t.Cleanup(func() {
		confirmLastHomeClose = origHome
		confirmAgentClose = origAgent
		shouldConfirmLastSurfaceClose = origShould
	})
	shouldConfirmLastSurfaceClose = func() bool { return false }
	homeCalls := 0
	agentCalls := 0
	confirmLastHomeClose = func(_ flashFunc, dock string) bool {
		homeCalls++
		if dock != "labs" {
			t.Fatalf("home confirmation dock = %q, want labs", dock)
		}
		return false
	}
	confirmAgentClose = func(name string) bool {
		agentCalls++
		return true
	}

	eng, _, _, _ := testNavEngine(t)
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{
		DockName: "labs",
		BayName:  manifest.HomeBayID,
		Type:     manifest.SurfaceTypeAgent,
		Name:     "claude",
		Agent:    "claude",
	}); err != nil {
		t.Fatalf("SurfaceAdd(home agent): %v", err)
	}

	if err := runSurfaceClose(eng, []string{"claude"}, manifest.HomeBayID, "labs", false); err != nil {
		t.Fatalf("runSurfaceClose home agent: %v", err)
	}
	if homeCalls != 1 {
		t.Fatalf("home confirmation calls = %d, want 1", homeCalls)
	}
	if agentCalls != 0 {
		t.Fatalf("agent confirmation calls = %d, want 0", agentCalls)
	}
	home, _ := eng.BayShow("labs", manifest.HomeBayID)
	if len(home.Surfaces) != 1 {
		t.Fatalf("declined home confirmation should keep agent surface, got %+v", home.Surfaces)
	}
}

func TestRunBayClose_HomeForceDismissesDock(t *testing.T) {
	origHome := confirmLastHomeClose
	t.Cleanup(func() { confirmLastHomeClose = origHome })
	confirmLastHomeClose = func(_ flashFunc, dock string) bool {
		t.Fatalf("home confirmation should not fire with bay close --force for dock %q", dock)
		return false
	}

	eng, mockTmux, _, _ := testNavEngine(t)
	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path

	if err := runBayClose(eng, "labs", manifest.HomeBayID, true, false); err != nil {
		t.Fatalf("runBayClose(home --force): %v", err)
	}
	m2, _ := eng.LoadManifest()
	dock := m2.FindDock("labs")
	if dock == nil {
		t.Fatal("dock disappeared after bay close home --force")
	}
	if dock.Path != dockPath {
		t.Fatalf("dock.Path = %q, want %q", dock.Path, dockPath)
	}
	if home := dock.FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("home persisted after bay close home --force: %+v", home)
	}
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("tmux session still exists after bay close home --force")
	}
}

func TestRunBayClose_HomeDoubleTapDismissesDock(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	eng, mockTmux, _, _ := testNavEngine(t)
	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	winID := home.Surfaces[0].Tmux.WindowID

	if err := runBayClose(eng, "labs", manifest.HomeBayID, false, false); err != nil {
		t.Fatalf("runBayClose first: %v", err)
	}
	if exists, _ := mockTmux.WindowExists(winID); !exists {
		t.Fatalf("first bay close attempt killed home window %s", winID)
	}
	if home := mustBay(t, eng, "labs", manifest.HomeBayID); len(home.Surfaces) != 1 {
		t.Fatalf("first bay close attempt changed home surfaces: %+v", home.Surfaces)
	}
	msgs := mockTmux.DisplayMessages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "dismiss dock") {
		t.Fatalf("first bay close messages = %v, want home dismissal guidance", msgs)
	}

	if err := runBayClose(eng, "labs", manifest.HomeBayID, false, false); err != nil {
		t.Fatalf("runBayClose second: %v", err)
	}
	m2, _ := eng.LoadManifest()
	dock := m2.FindDock("labs")
	if dock == nil {
		t.Fatal("dock disappeared after confirmed bay close home")
	}
	if dock.Path != dockPath {
		t.Fatalf("dock.Path = %q, want %q", dock.Path, dockPath)
	}
	if home := dock.FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("home persisted after confirmed bay close home: %+v", home)
	}
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("tmux session still exists after confirmed bay close home")
	}
}

func TestRunBayClose_TapDirty_DoubleTapForceCloses(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	eng, mockTmux, mockGit, _ := testNavEngine(t)
	bay, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := os.MkdirAll(bay.Path, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	mockGit.SetDirty(bay.Path, true)

	// First tap: bay stays, flash names the blast radius.
	if err := runBayClose(eng, "labs", "b1", true, true); err != nil {
		t.Fatalf("runBayClose first tap: %v", err)
	}
	if _, err := eng.BayShow("labs", "b1"); err != nil {
		t.Fatalf("first tap should keep the bay: %v", err)
	}
	msgs := mockTmux.DisplayMessages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "discard its uncommitted changes") {
		t.Fatalf("first tap messages = %v, want uncommitted-changes warning", msgs)
	}

	// Second tap within the window: force close proceeds despite dirty.
	if err := runBayClose(eng, "labs", "b1", true, true); err != nil {
		t.Fatalf("runBayClose second tap: %v", err)
	}
	if _, err := eng.BayShow("labs", "b1"); err == nil {
		t.Fatal("second tap should force-close the dirty bay")
	}
}

func TestRunBayClose_TapUnlanded_MessageMentionsBranch(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	eng, mockTmux, mockGit, _ := testNavEngine(t)
	bay, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := os.MkdirAll(bay.Path, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	mockGit.SetUnpushed(bay.Path, true)

	if err := runBayClose(eng, "labs", "b1", true, true); err != nil {
		t.Fatalf("runBayClose first tap: %v", err)
	}
	if _, err := eng.BayShow("labs", "b1"); err != nil {
		t.Fatalf("first tap should keep the bay: %v", err)
	}
	msgs := mockTmux.DisplayMessages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "unlanded commits stay") {
		t.Fatalf("first tap messages = %v, want unlanded-commits wording", msgs)
	}
}

func TestRunBayClose_TapClean_GenericMessage(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	eng, mockTmux, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	if err := runBayClose(eng, "labs", "b1", true, true); err != nil {
		t.Fatalf("runBayClose first tap: %v", err)
	}
	msgs := mockTmux.DisplayMessages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "press again to close") {
		t.Fatalf("first tap messages = %v, want generic press-again wording", msgs)
	}
	if strings.Contains(msgs[0], "discard") || strings.Contains(msgs[0], "unlanded") {
		t.Fatalf("clean bay message = %q, should not warn about discarded work", msgs[0])
	}
}

func TestRunBayClose_TapHome_ForceDismissesAfterSecondTap(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	eng, mockTmux, _, _ := testNavEngine(t)
	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}

	if err := runBayClose(eng, "labs", manifest.HomeBayID, true, true); err != nil {
		t.Fatalf("runBayClose first tap: %v", err)
	}
	if has, _ := mockTmux.HasSession("labs"); !has {
		t.Fatal("first tap should not dismiss the dock session")
	}
	msgs := mockTmux.DisplayMessages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "dismiss dock") {
		t.Fatalf("first tap messages = %v, want home dismissal wording", msgs)
	}

	if err := runBayClose(eng, "labs", manifest.HomeBayID, true, true); err != nil {
		t.Fatalf("runBayClose second tap: %v", err)
	}
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("second tap should dismiss the dock session")
	}
}

func TestConfirmBayCloseTap_IndependentOfSurfaceCloseRecord(t *testing.T) {
	// A pending Option+w last-surface tap must never be consumed as
	// authorization for an Option+Shift+W force-close, and vice versa —
	// the two records live in separate files.
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	msg := func() string { return "msg" }
	if confirmLastSurfaceClose(noFlash, "labs", "b1") {
		t.Fatal("first surface tap should require a second tap")
	}
	if confirmBayCloseTap(noFlash, "labs", "b1", msg) {
		t.Error("pending surface tap must not authorize a force-close")
	}
	// The force tap above recorded its own first attempt, so a second
	// force tap within the window confirms — independently of the
	// surface-close record.
	if !confirmBayCloseTap(noFlash, "labs", "b1", msg) {
		t.Error("second force tap within the window should confirm")
	}
}

func TestRunBayCloseAll_UsesHomeConfirmationForFinalDismissal(t *testing.T) {
	origHome := confirmLastHomeClose
	t.Cleanup(func() { confirmLastHomeClose = origHome })

	eng, mockTmux, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: manifest.HomeBayID, Type: manifest.SurfaceTypeShell, Name: "shell"}); err != nil {
		t.Fatalf("SurfaceAdd(home): %v", err)
	}

	confirmCalls := 0
	confirmLastHomeClose = func(_ flashFunc, dock string) bool {
		confirmCalls++
		if dock != "labs" {
			t.Fatalf("confirm dock = %q, want labs", dock)
		}
		return false
	}

	closed, skipped, err := runBayCloseAll(eng, "labs", false, false)
	if err != nil {
		t.Fatalf("runBayCloseAll declined: %v", err)
	}
	if confirmCalls != 1 {
		t.Fatalf("home confirmation calls = %d, want 1", confirmCalls)
	}
	if len(closed) != 0 || len(skipped) != 0 {
		t.Fatalf("declined close-all closed=%v skipped=%v, want none", closed, skipped)
	}
	if _, err := eng.BayShow("labs", "b1"); err != nil {
		t.Fatalf("declined close-all removed non-home bay: %v", err)
	}
	if home := mustBay(t, eng, "labs", manifest.HomeBayID); len(home.Surfaces) != 1 {
		t.Fatalf("declined close-all changed home: %+v", home.Surfaces)
	}

	confirmLastHomeClose = func(_ flashFunc, dock string) bool {
		confirmCalls++
		return true
	}
	closed, skipped, err = runBayCloseAll(eng, "labs", false, false)
	if err != nil {
		t.Fatalf("runBayCloseAll confirmed: %v", err)
	}
	if confirmCalls != 2 {
		t.Fatalf("home confirmation calls = %d, want 2", confirmCalls)
	}
	if len(closed) != 2 || closed[0] != "labs:b1" || closed[1] != "labs:"+manifest.HomeBayID || len(skipped) != 0 {
		t.Fatalf("confirmed close-all closed=%v skipped=%v, want b1 then home", closed, skipped)
	}
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("tmux session still exists after confirmed close-all")
	}
}

func mustBay(t *testing.T, eng *engine.Engine, dockName, bayID string) *manifest.Bay {
	t.Helper()
	bay, err := eng.BayShow(dockName, bayID)
	if err != nil {
		t.Fatalf("BayShow(%s:%s): %v", dockName, bayID, err)
	}
	return bay
}
