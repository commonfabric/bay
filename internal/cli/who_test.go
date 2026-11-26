package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/manifest/manifesttest"
	"github.com/commonfabric/bay/internal/occupant"
	"github.com/commonfabric/bay/internal/tmux"
)

// whoFakeSystem is an in-memory occupant.System.
type whoFakeSystem struct {
	procs []occupant.Process
	env   map[int]map[string]string
	files map[string]string
}

func (f *whoFakeSystem) Processes() ([]occupant.Process, error) { return f.procs, nil }
func (f *whoFakeSystem) UnixSockets(pid int) ([]string, error) {
	return []string{fmt.Sprintf("/tmp/cc-socks/%d.sock", pid)}, nil
}
func (f *whoFakeSystem) Getenv(pid int, key string) (string, error) { return f.env[pid][key], nil }
func (f *whoFakeSystem) ReadFile(path string) ([]byte, error) {
	if s, ok := f.files[path]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}
func (f *whoFakeSystem) HomeDir() (string, error) { return "/home/u", nil }

// whoFixture makes bay labs:b1 with surfaces "shell" plus the given
// extra names, and returns each surface's pane pid.
func whoFixture(t *testing.T, extra ...string) (*engine.Engine, map[string]int) {
	t.Helper()
	eng, _, pids := whoFixtureTmux(t, extra...)
	return eng, pids
}

func whoFixtureTmux(t *testing.T, extra ...string) (*engine.Engine, *tmux.Mock, map[string]int) {
	t.Helper()
	eng, mockTmux, _, _ := testNavEngine(t)
	bay, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	os.MkdirAll(bay.Path, 0o755)
	for _, name := range extra {
		if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeShell, Name: name, SplitDir: "v"}); err != nil {
			t.Fatalf("SurfaceAdd %s: %v", name, err)
		}
	}
	bay, _ = eng.BayShow("labs", "b1")
	pids := map[string]int{}
	for _, s := range bay.Surfaces {
		pid, err := mockTmux.GetPanePID(s.Tmux.PaneID)
		if err != nil {
			t.Fatalf("pane pid for %s: %v", s.Name, err)
		}
		pids[s.Name] = pid
	}
	return eng, mockTmux, pids
}

// shellWithClaude returns a pane shell process and a Claude child.
func shellWithClaude(panePID, claudePID int) []occupant.Process {
	return []occupant.Process{
		{PID: panePID, PPID: 1, Comm: "-zsh"},
		{PID: claudePID, PPID: panePID, Comm: "claude"},
	}
}

func TestRunWho_ExplicitSurfaceOtherLogin(t *testing.T) {
	eng, pids := whoFixture(t, "agent")
	sys := &whoFakeSystem{
		procs: shellWithClaude(pids["agent"], 500),
		env:   map[int]map[string]string{500: {"CLAUDE_CONFIG_DIR": "/home/u/.claude2"}},
		files: map[string]string{"/home/u/.claude2/sessions/500.json": `{"pid":500,"name":"b1-ab","status":"idle"}`},
	}
	res, err := runWho(eng, sys, "labs:b1:agent")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Occupied || res.Address != "uds:/tmp/cc-socks/500.sock" || res.Login != "/home/u/.claude2" ||
		res.SessionName != "b1-ab" || res.Status != "idle" || res.BayAddress != "labs:b1:agent" {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestRunWho_BaySurfaceShortForm(t *testing.T) {
	eng, pids := whoFixture(t, "agent2")
	res, err := runWho(eng, &whoFakeSystem{procs: shellWithClaude(pids["agent2"], 500)}, "b1:agent2")
	if err != nil {
		t.Fatal(err)
	}
	if res.Dock != "labs" || res.Bay != "b1" || res.Surface != "agent2" || res.PID != 500 {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestRunWho_DefaultPrefersAgent(t *testing.T) {
	eng, pids := whoFixture(t, "agent")
	procs := append(shellWithClaude(pids["agent"], 500), shellWithClaude(pids["shell"], 600)...)
	res, err := runWho(eng, &whoFakeSystem{procs: procs}, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Surface != "agent" || res.PID != 500 || res.Login != "/home/u/.claude" {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestRunWho_DefaultFindsClaudeInShellSurface(t *testing.T) {
	// Claude running in a surface typed and named "shell", while the
	// "agent" surface is empty: resolution goes by process, not type.
	eng, pids := whoFixture(t, "agent")
	res, err := runWho(eng, &whoFakeSystem{procs: shellWithClaude(pids["shell"], 600)}, "labs:b1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Surface != "shell" || res.PID != 600 || !res.Occupied {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestRunWho_SeveralCandidatesNoAgentFails(t *testing.T) {
	eng, pids := whoFixture(t, "agent2")
	procs := append(shellWithClaude(pids["agent2"], 500), shellWithClaude(pids["shell"], 600)...)
	_, err := runWho(eng, &whoFakeSystem{procs: procs}, "b1")
	if err == nil {
		t.Fatal("expected an error listing candidates")
	}
	for _, want := range []string{"agent2 (pid 500)", "shell (pid 600)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not list %q", err, want)
		}
	}
}

func TestRunWho_NoOccupant(t *testing.T) {
	eng, pids := whoFixture(t, "agent")
	sys := &whoFakeSystem{procs: []occupant.Process{{PID: pids["agent"], PPID: 1, Comm: "-zsh"}}}

	res, err := runWho(eng, sys, "labs:b1:agent")
	if err != nil {
		t.Fatal(err)
	}
	if res.Occupied || res.Address != "" || res.Reason == "" || res.Surface != "agent" {
		t.Fatalf("unexpected result %+v", res)
	}

	// Bare bay with nothing running anywhere reports the agent surface.
	res, err = runWho(eng, sys, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Occupied || res.Surface != "agent" {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestRunWho_NoOccupantNoAgentSurface(t *testing.T) {
	eng, _ := whoFixture(t)
	res, err := runWho(eng, &whoFakeSystem{}, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Occupied || res.Surface != "" || res.BayAddress != "labs:b1" {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestRunWho_Errors(t *testing.T) {
	eng, _ := whoFixture(t)
	for _, target := range []string{"labs:b1:nosuch", "b99", "a:b:c:d", "labs::agent"} {
		if _, err := runWho(eng, &whoFakeSystem{}, target); err == nil {
			t.Errorf("runWho(%q): expected error", target)
		}
	}
}

func TestRunWho_PaneFromAnotherSessionIsNotTrusted(t *testing.T) {
	// After a tmux restart a recorded pane ID can name a pane in some
	// other session. When the dock's session no longer carries the
	// dock's marker, the recorded pane is not followed.
	eng, mockTmux, pids := whoFixtureTmux(t, "agent")
	sys := &whoFakeSystem{procs: shellWithClaude(pids["agent"], 500)}
	if err := mockTmux.SetSessionOption("labs", tmux.SessionIDOption, "someone-else"); err != nil {
		t.Fatal(err)
	}
	res, err := runWho(eng, sys, "labs:b1:agent")
	if err != nil {
		t.Fatal(err)
	}
	if res.Occupied || res.Address != "" {
		t.Fatalf("followed an unowned pane: %+v", res)
	}
}

func TestRunWho_DoesNotWriteManifest(t *testing.T) {
	// bay who is read-only: unlike SyncAll, it must not prune or close
	// other bays. Bay b2 has lost its window, which a sync would record.
	eng, mockTmux, pids := whoFixtureTmux(t, "agent")
	b2, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew b2: %v", err)
	}
	if err := mockTmux.KillWindow(b2.Surfaces[0].Tmux.WindowID); err != nil {
		t.Fatal(err)
	}
	m, _ := eng.LoadManifest()
	before, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runWho(eng, &whoFakeSystem{procs: shellWithClaude(pids["agent"], 500)}, "labs:b1"); err != nil {
		t.Fatal(err)
	}
	m, _ = eng.LoadManifest()
	after, _ := json.Marshal(m)
	if string(before) != string(after) {
		t.Fatalf("manifest changed:\nbefore %s\nafter  %s", before, after)
	}
}

func TestRunWho_NoArgIsCurrentPaneEvenWithSelfSurface(t *testing.T) {
	eng, mockTmux, pids := whoFixtureTmux(t, "agent", "self")
	bay, _ := eng.BayShow("labs", "b1")
	agent := bay.FindSurface("agent")
	mockTmux.SetCurrentWindowID(agent.Tmux.WindowID)
	mockTmux.SetCurrentPaneID(agent.Tmux.PaneID)
	res, err := runWho(eng, &whoFakeSystem{procs: shellWithClaude(pids["agent"], 500)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.BayAddress != "labs:b1:agent" || res.PID != 500 {
		t.Fatalf("unexpected result %+v", res)
	}
}

// failingListPanes is a tmux mock whose ListPanes fails.
type failingListPanes struct{ *tmux.Mock }

func (f failingListPanes) ListPanes(string) ([]tmux.Pane, error) {
	return nil, errors.New("tmux: server busy")
}

func TestRunWho_TmuxFailureIsAnError(t *testing.T) {
	// A tmux query that fails must not make a surface look absent, or
	// the default could silently pick another surface.
	eng, mockTmux, pids := whoFixtureTmux(t, "agent")
	eng.Tmux = failingListPanes{mockTmux}
	procs := append(shellWithClaude(pids["agent"], 500), shellWithClaude(pids["shell"], 600)...)
	if res, err := runWho(eng, &whoFakeSystem{procs: procs}, "b1"); err == nil {
		t.Fatalf("expected an error, got %+v", res)
	}
}

func TestRunWho_NoArgUsesPaneNotWorkingDirectory(t *testing.T) {
	// An agent in b1's pane that has cd'd into b2's worktree is still b1.
	eng, mockTmux, pids := whoFixtureTmux(t, "agent")
	b2, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew b2: %v", err)
	}
	os.MkdirAll(b2.Path, 0o755)
	t.Chdir(b2.Path)
	bay, _ := eng.BayShow("labs", "b1")
	agent := bay.FindSurface("agent")
	mockTmux.SetCurrentWindowID(agent.Tmux.WindowID)
	mockTmux.SetCurrentPaneID(agent.Tmux.PaneID)
	res, err := runWho(eng, &whoFakeSystem{procs: shellWithClaude(pids["agent"], 500)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.BayAddress != "labs:b1:agent" {
		t.Fatalf("got %s, want labs:b1:agent", res.BayAddress)
	}
}

// failingListPanesFor fails ListPanes for one window only.
type failingListPanesFor struct {
	*tmux.Mock
	window string
}

func (f failingListPanesFor) ListPanes(windowID string) ([]tmux.Pane, error) {
	if windowID == f.window {
		return nil, errors.New("tmux: can't find window")
	}
	return f.Mock.ListPanes(windowID)
}

func TestRunWho_ExplicitSurfaceIgnoresOtherWindows(t *testing.T) {
	// An unrelated window vanishing mid-lookup must not fail an explicit
	// lookup whose own pane is live.
	eng, mockTmux, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeShell, Name: "agent"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}
	bay, _ := eng.BayShow("labs", "b1")
	shell, agent := bay.FindSurface("shell"), bay.FindSurface("agent")
	if shell.Tmux.WindowID == agent.Tmux.WindowID {
		t.Fatalf("fixture: want agent in its own window")
	}
	pid, _ := mockTmux.GetPanePID(agent.Tmux.PaneID)
	eng.Tmux = failingListPanesFor{mockTmux, shell.Tmux.WindowID}
	res, err := runWho(eng, &whoFakeSystem{procs: shellWithClaude(pid, 500)}, "labs:b1:agent")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Occupied || res.PID != 500 {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestRunWho_NoArgSkipsStalePaneRecord(t *testing.T) {
	// b1's shell record carries a pane ID that now belongs to b2's agent
	// (as after a tmux restart). From that pane, the caller is b2:agent.
	eng, mockTmux, _, dir := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew b1: %v", err)
	}
	b2, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew b2: %v", err)
	}
	os.MkdirAll(b2.Path, 0o755)
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: b2.ID, Type: manifest.SurfaceTypeShell, Name: "agent", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}
	m, _ := eng.LoadManifest()
	dock := m.FindDock("labs")
	agent := dock.FindBayByID(b2.ID).FindSurface("agent")
	dock.FindBayByID("b1").FindSurface("shell").Tmux.PaneID = agent.Tmux.PaneID
	if err := manifesttest.Save(filepath.Join(dir, "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	mockTmux.SetCurrentWindowID(agent.Tmux.WindowID)
	mockTmux.SetCurrentPaneID(agent.Tmux.PaneID)
	pid, _ := mockTmux.GetPanePID(agent.Tmux.PaneID)
	res, err := runWho(eng, &whoFakeSystem{procs: shellWithClaude(pid, 500)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.BayAddress != "labs:"+b2.ID+":agent" || !res.Occupied {
		t.Fatalf("got %+v, want labs:%s:agent", res, b2.ID)
	}
}

func TestRunWho_DeadPaneIsNotFollowed(t *testing.T) {
	// A pane kept by remain-on-exit reports its exited process's PID,
	// which the OS may have reused for a Claude in another bay.
	eng, mockTmux, pids := whoFixtureTmux(t, "agent")
	bay, _ := eng.BayShow("labs", "b1")
	mockTmux.SetPaneDead(bay.FindSurface("agent").Tmux.PaneID, true)
	sys := &whoFakeSystem{procs: []occupant.Process{{PID: pids["agent"], PPID: 1, Comm: "claude"}}}
	res, err := runWho(eng, sys, "labs:b1:agent")
	if err != nil {
		t.Fatal(err)
	}
	if res.Occupied || res.Address != "" {
		t.Fatalf("followed a dead pane: %+v", res)
	}
}
