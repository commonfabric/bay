package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/occupant"
	"github.com/commonfabric/bay/internal/tmux"
)

// failingSockets is a whoFakeSystem whose socket inspection fails.
type failingSockets struct{ *whoFakeSystem }

func (failingSockets) UnixSockets(int) ([]string, error) { return nil, errors.New("lsof: not found") }

// peersFixture: labs:b1 with surfaces shell + agent, labs:b2 with shell.
func peersFixture(t *testing.T) (*engine.Engine, map[string]int) {
	t.Helper()
	eng, mockTmux, pids := whoFixtureTmux(t, "agent")
	b2, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew b2: %v", err)
	}
	os.MkdirAll(b2.Path, 0o755)
	pid, err := mockTmux.GetPanePID(b2.Surfaces[0].Tmux.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	pids["b2:shell"] = pid
	bay, _ := eng.BayShow("labs", "b1")
	mockTmux.SetCurrentWindowID(bay.FindSurface("agent").Tmux.WindowID)
	mockTmux.SetCurrentPaneID(bay.FindSurface("agent").Tmux.PaneID)
	return eng, pids
}

func TestRunPeers_ListsClaudeSurfacesAcrossLogins(t *testing.T) {
	eng, pids := peersFixture(t)
	procs := append(shellWithClaude(pids["agent"], 500), shellWithClaude(pids["b2:shell"], 600)...)
	procs = append(procs, occupant.Process{PID: pids["shell"], PPID: 1, Comm: "-zsh"}) // no Claude
	sys := &whoFakeSystem{
		procs: procs,
		env:   map[int]map[string]string{600: {"CLAUDE_CONFIG_DIR": "/home/u/.claude2"}},
		files: map[string]string{
			"/home/u/.claude/sessions/500.json":  `{"pid":500,"name":"b1-aa","status":"busy"}`,
			"/home/u/.claude2/sessions/600.json": `{"pid":600,"name":"b2-bb","status":"waiting"}`,
		},
	}
	var warn bytes.Buffer
	rows, err := runPeers(eng, sys, "", &warn)
	if err != nil {
		t.Fatal(err)
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warnings: %s", warn.String())
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	a, b := rows[0], rows[1]
	if a.BayAddress != "labs:b1:agent" || !a.Self || a.Login != "/home/u/.claude" || a.SessionName != "b1-aa" ||
		a.Address != "uds:/tmp/cc-socks/500.sock" || a.Agent != "claude" || a.Transport != "claude-uds" {
		t.Errorf("row 0: %+v", a)
	}
	if b.BayAddress != "labs:b2:shell" || b.Self || b.Login != "/home/u/.claude2" || b.Status != "waiting" ||
		b.Address != "uds:/tmp/cc-socks/600.sock" {
		t.Errorf("row 1: %+v", b)
	}
}

func TestRunPeers_DockFilter(t *testing.T) {
	eng, pids := peersFixture(t)
	sys := &whoFakeSystem{procs: shellWithClaude(pids["agent"], 500)}
	if _, err := runPeers(eng, sys, "nosuch", &bytes.Buffer{}); err == nil {
		t.Error("unknown dock: expected an error")
	}
	rows, err := runPeers(eng, sys, "labs", &bytes.Buffer{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %+v, err %v", rows, err)
	}
}

func TestRunPeers_TmuxFailureSkipsDockWithWarning(t *testing.T) {
	eng, pids := peersFixture(t)
	eng.Tmux = failingListPanes{eng.Tmux.(*tmux.Mock)}
	var warn bytes.Buffer
	rows, err := runPeers(eng, &whoFakeSystem{procs: shellWithClaude(pids["agent"], 500)}, "", &warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || !strings.Contains(warn.String(), "skipping dock labs") {
		t.Fatalf("rows %+v, warn %q", rows, warn.String())
	}
}

func TestRunPeers_InspectionFailureListsRowWithoutAddress(t *testing.T) {
	eng, pids := peersFixture(t)
	sys := failingSockets{&whoFakeSystem{procs: shellWithClaude(pids["agent"], 500)}}
	var warn bytes.Buffer
	rows, err := runPeers(eng, sys, "", &warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Occupied || rows[0].Address != "" || rows[0].Reason == "" {
		t.Fatalf("rows %+v", rows)
	}
	if !strings.Contains(warn.String(), "labs:b1:agent") {
		t.Fatalf("warn %q", warn.String())
	}
}

func TestRunPeers_DoesNotWriteManifest(t *testing.T) {
	eng, pids := peersFixture(t)
	m, _ := eng.LoadManifest()
	before := mustJSON(t, m)
	if _, err := runPeers(eng, &whoFakeSystem{procs: shellWithClaude(pids["agent"], 500)}, "", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	m, _ = eng.LoadManifest()
	if after := mustJSON(t, m); before != after {
		t.Fatalf("manifest changed")
	}
}

func mustJSON(t *testing.T, m *manifest.Manifest) string {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
