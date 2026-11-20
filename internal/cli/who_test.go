package cli

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/occupant"
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
	return eng, pids
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
