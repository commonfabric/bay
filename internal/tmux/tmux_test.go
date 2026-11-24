package tmux

import (
	"strings"
	"testing"
)

// Verify Mock and Real implement Interface at compile time.
var _ Interface = (*Mock)(nil)
var _ Interface = (*Real)(nil)

func TestWaitingOrBellWindowIDsMergesSources(t *testing.T) {
	m := NewMock()
	if err := m.NewSession("work"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	waitingID, err := m.NewWindow("work", "waiting", "/tmp")
	if err != nil {
		t.Fatalf("NewWindow waiting: %v", err)
	}
	bellID, err := m.NewWindow("work", "bell", "/tmp")
	if err != nil {
		t.Fatalf("NewWindow bell: %v", err)
	}
	if err := m.SetWindowOption(waitingID, "@bay-waiting", "1"); err != nil {
		t.Fatalf("SetWindowOption waiting: %v", err)
	}
	if err := m.SetWindowOption(bellID, "@bay-bell", "1"); err != nil {
		t.Fatalf("SetWindowOption bell: %v", err)
	}

	got, err := m.WaitingOrBellWindowIDs("work")
	if err != nil {
		t.Fatalf("WaitingOrBellWindowIDs: %v", err)
	}
	for _, id := range []string{waitingID, bellID} {
		if !got[id] {
			t.Fatalf("missing waiting window %s in %#v", id, got)
		}
	}
}

func TestVisibleWidth(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"plain ascii", "hello", 5},
		{"with style", "#[bold]hello#[default]", 5},
		{"multiple styles", "#[fg=red]a#[fg=blue]bc#[default]", 3},
		{"escaped hash", "a##b", 3},
		{"unicode single-width", "│", 1},
		{"realistic status-left", "#[bold] bay #[nobold]│ mikecf ", 14},
		{"unterminated style", "#[bold", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := visibleWidth(tt.in)
			if got != tt.want {
				t.Errorf("visibleWidth(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// --- Session tests ---

func TestNewSession(t *testing.T) {
	m := NewMock()
	if err := m.NewSession("work"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	has, err := m.HasSession("work")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Error("expected HasSession to return true after NewSession")
	}
}

func TestNewSession_Duplicate(t *testing.T) {
	m := NewMock()
	if err := m.NewSession("work"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := m.NewSession("work"); err == nil {
		t.Error("expected error creating duplicate session")
	}
}

func TestHasSession_Missing(t *testing.T) {
	m := NewMock()
	has, err := m.HasSession("nope")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if has {
		t.Error("expected HasSession to return false for missing session")
	}
}

// TestHasSession_ExactMatch verifies that HasSession does not
// prefix-match. A session named "loom-old" should not cause
// HasSession("loom") to return true.
func TestHasSession_ExactMatch(t *testing.T) {
	m := NewMock()
	m.NewSession("loom-old")
	has, err := m.HasSession("loom")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if has {
		t.Error("HasSession should use exact matching, not prefix matching")
	}
}

func TestExactSession(t *testing.T) {
	if got := exactSession("loom"); got != "=loom" {
		t.Errorf("exactSession(loom) = %q, want =loom", got)
	}
}

func TestListSessions(t *testing.T) {
	m := NewMock()
	m.NewSession("alpha")
	m.NewSession("beta")
	sessions, err := m.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	names := map[string]bool{}
	for _, s := range sessions {
		names[s.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("unexpected session names: %v", sessions)
	}
}

func TestKillSession(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	if err := m.KillSession("work"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	has, _ := m.HasSession("work")
	if has {
		t.Error("session should not exist after KillSession")
	}
}

func TestKillSession_Missing(t *testing.T) {
	m := NewMock()
	if err := m.KillSession("nope"); err == nil {
		t.Error("expected error killing non-existent session")
	}
}

// --- Window tests ---

func TestNewWindow(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	id, err := m.NewWindow("work", "editor", "/home")
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if !strings.HasPrefix(id, "@") {
		t.Errorf("expected window ID starting with @, got %q", id)
	}
}

func TestNewWindow_NoSession(t *testing.T) {
	m := NewMock()
	_, err := m.NewWindow("nope", "editor", "/home")
	if err == nil {
		t.Error("expected error creating window in non-existent session")
	}
}

func TestListWindows(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	// NewSession creates a default window; NewWindow adds more
	m.NewWindow("work", "editor", "/home")
	m.NewWindow("work", "shell", "/tmp")
	windows, err := m.ListWindows("work")
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	// 1 default from NewSession + 2 explicit = 3
	if len(windows) != 3 {
		t.Fatalf("expected 3 windows (1 default + 2 added), got %d", len(windows))
	}
	// The explicitly added windows are at indices 1 and 2
	if windows[1].Name != "editor" || windows[2].Name != "shell" {
		t.Errorf("unexpected window names: %v, %v", windows[1].Name, windows[2].Name)
	}
}

func TestRenameWindow(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	id, _ := m.NewWindow("work", "old", "/home")
	if err := m.RenameWindow(id, "new"); err != nil {
		t.Fatalf("RenameWindow: %v", err)
	}
	windows, _ := m.ListWindows("work")
	found := false
	for _, w := range windows {
		if w.ID == id && w.Name == "new" {
			found = true
		}
	}
	if !found {
		t.Error("window was not renamed")
	}
}

func TestRenameWindow_Missing(t *testing.T) {
	m := NewMock()
	if err := m.RenameWindow("@999", "new"); err == nil {
		t.Error("expected error renaming non-existent window")
	}
}

func TestKillWindow(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	id, _ := m.NewWindow("work", "editor", "/home")
	if err := m.KillWindow(id); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}
	windows, _ := m.ListWindows("work")
	for _, w := range windows {
		if w.ID == id {
			t.Error("window should not exist after KillWindow")
		}
	}
}

func TestKillWindow_Missing(t *testing.T) {
	m := NewMock()
	if err := m.KillWindow("@999"); err == nil {
		t.Error("expected error killing non-existent window")
	}
}

func TestWindowExists(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	id, _ := m.NewWindow("work", "editor", "/home")
	exists, err := m.WindowExists(id)
	if err != nil {
		t.Fatalf("WindowExists: %v", err)
	}
	if !exists {
		t.Error("expected window to exist")
	}
	exists, _ = m.WindowExists("@999")
	if exists {
		t.Error("expected non-existent window to return false")
	}
}

func TestFindWindowByName(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	id, _ := m.NewWindow("work", "editor", "/home")
	m.NewWindow("work", "shell", "/tmp")
	found, err := m.FindWindowByName("work", "editor")
	if err != nil {
		t.Fatalf("FindWindowByName: %v", err)
	}
	if found != id {
		t.Errorf("expected %q, got %q", id, found)
	}
}

func TestFindWindowByName_NotFound(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	_, err := m.FindWindowByName("work", "nope")
	if err == nil {
		t.Error("expected error for missing window name")
	}
}

func TestSetGetWindowOption(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	id, _ := m.NewWindow("work", "editor", "/home")
	if err := m.SetWindowOption(id, "remain-on-exit", "on"); err != nil {
		t.Fatalf("SetWindowOption: %v", err)
	}
	val, err := m.GetWindowOption(id, "remain-on-exit")
	if err != nil {
		t.Fatalf("GetWindowOption: %v", err)
	}
	if val != "on" {
		t.Errorf("expected option value %q, got %q", "on", val)
	}
}

func TestGetWindowOption_Missing(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	id, _ := m.NewWindow("work", "editor", "/home")
	_, err := m.GetWindowOption(id, "nonexistent")
	if err == nil {
		t.Error("expected error for missing option")
	}
}

func TestSelectWindow(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	id, _ := m.NewWindow("work", "editor", "/home")
	if err := m.SelectWindow("work", id); err != nil {
		t.Fatalf("SelectWindow: %v", err)
	}
	// Verify the call was recorded.
	if len(m.Calls) == 0 {
		t.Fatal("expected calls to be tracked")
	}
	last := m.Calls[len(m.Calls)-1]
	if last.Method != "SelectWindow" {
		t.Errorf("expected last call to be SelectWindow, got %q", last.Method)
	}
	if len(last.Args) != 2 || last.Args[0] != id || last.Args[1] != "work" {
		t.Errorf("SelectWindow args = %v, want [%s work]", last.Args, id)
	}
}

// --- Pane tests ---

func TestSplitWindow(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	paneID, err := m.SplitWindow(winID, "h", "/home", false)
	if err != nil {
		t.Fatalf("SplitWindow: %v", err)
	}
	if !strings.HasPrefix(paneID, "%") {
		t.Errorf("expected pane ID starting with %%, got %q", paneID)
	}
}

func TestSplitWindow_TargetPane(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	panes, err := m.ListPanes(winID)
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("expected 1 initial pane, got %d", len(panes))
	}

	paneID, err := m.SplitWindow(panes[0].ID, "h", "/home", false)
	if err != nil {
		t.Fatalf("SplitWindow(target pane): %v", err)
	}

	panes, err = m.ListPanes(winID)
	if err != nil {
		t.Fatalf("ListPanes after split: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes after split, got %d", len(panes))
	}
	if panes[1].ID != paneID {
		t.Fatalf("new pane order = %q, want %q immediately after target", panes[1].ID, paneID)
	}
}

func TestSplitWindow_MissingWindow(t *testing.T) {
	m := NewMock()
	_, err := m.SplitWindow("@999", "h", "/home", false)
	if err == nil {
		t.Error("expected error splitting non-existent window")
	}
}

func TestListPanes(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	m.SplitWindow(winID, "h", "/home", false)
	panes, err := m.ListPanes(winID)
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	// NewWindow creates one initial pane, SplitWindow adds another.
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes, got %d", len(panes))
	}
}

func TestKillPane(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	paneID, _ := m.SplitWindow(winID, "v", "/home", false)
	if err := m.KillPane(paneID); err != nil {
		t.Fatalf("KillPane: %v", err)
	}
	panes, _ := m.ListPanes(winID)
	for _, p := range panes {
		if p.ID == paneID {
			t.Error("pane should not exist after KillPane")
		}
	}
}

func TestKillPane_Missing(t *testing.T) {
	m := NewMock()
	if err := m.KillPane("%999"); err == nil {
		t.Error("expected error killing non-existent pane")
	}
}

func TestSelectPane(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	paneID, _ := m.SplitWindow(winID, "v", "/home", false)

	if err := m.SelectPane(paneID); err != nil {
		t.Fatalf("SelectPane: %v", err)
	}
}

func TestSelectPane_Missing(t *testing.T) {
	m := NewMock()
	if err := m.SelectPane("%999"); err == nil {
		t.Error("expected error selecting non-existent pane")
	}
}

// --- Layout tree tests ---

func TestMockLayout_NewWindowIsSinglePane(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")

	got, err := m.LayoutString(winID)
	if err != nil {
		t.Fatalf("LayoutString: %v", err)
	}
	if !strings.HasPrefix(got, "%") || strings.ContainsAny(got, "/|()") {
		t.Errorf("expected single-pane layout (one pane ID), got %q", got)
	}
}

func TestMockLayout_SplitNestsBinaryTree(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	panes, _ := m.ListPanes(winID)
	root := panes[0].ID

	a, _ := m.SplitWindow(root, "v", "/home", false)
	b, _ := m.SplitWindow(a, "v", "/home", false)

	// The second split splits a's region, not the whole window — binary
	// nesting, not a flat 3-pane stack.
	got, _ := m.LayoutString(winID)
	want := root + "/(" + a + "/" + b + ")"
	if got != want {
		t.Errorf("layout = %q, want %q", got, want)
	}
}

func TestMockLayout_SplitBeforeWrapsRoot(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	panes, _ := m.ListPanes(winID)
	root := panes[0].ID

	// First, build a vertical split inside the window.
	b, _ := m.SplitWindow(root, "v", "/home", false)
	// Then "split before" at the root level should wrap the entire tree.
	prepended, _ := m.SplitWindow(root, "v", "/home", true)

	got, _ := m.LayoutString(winID)
	want := prepended + "/(" + root + "/" + b + ")"
	if got != want {
		t.Errorf("layout = %q, want %q", got, want)
	}
}

func TestMockLayout_KillPaneCollapsesBinarySplit(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	panes, _ := m.ListPanes(winID)
	root := panes[0].ID

	other, _ := m.SplitWindow(root, "v", "/home", false)

	if err := m.KillPane(other); err != nil {
		t.Fatalf("KillPane: %v", err)
	}
	// Singleton split must collapse — layout is just the root pane again.
	got, _ := m.LayoutString(winID)
	if got != root {
		t.Errorf("after kill, layout = %q, want plain %q", got, root)
	}
}

func TestMockLayout_KillLastPaneEmptiesWindow(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	panes, _ := m.ListPanes(winID)
	root := panes[0].ID

	if err := m.KillPane(root); err != nil {
		t.Fatalf("KillPane: %v", err)
	}
	got, _ := m.LayoutString(winID)
	if got != "" {
		t.Errorf("after killing only pane, layout = %q, want empty", got)
	}
}

func TestSendKeys(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	panes, _ := m.ListPanes(winID)
	paneID := panes[0].ID
	if err := m.SendKeys(paneID, "echo hello"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	found := false
	for _, c := range m.Calls {
		if c.Method == "SendKeys" && c.Args[0] == paneID && c.Args[1] == "echo hello" {
			found = true
		}
	}
	if !found {
		t.Error("expected SendKeys call to be recorded")
	}
}

func TestCapturePane(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	panes, _ := m.ListPanes(winID)
	paneID := panes[0].ID
	// Set capture content on the mock.
	m.SetCaptureContent(paneID, "line1\nline2\nline3")
	output, err := m.CapturePane(paneID, 10)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if output != "line1\nline2\nline3" {
		t.Errorf("unexpected capture output: %q", output)
	}
}

func TestRespawnPane(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	panes, _ := m.ListPanes(winID)
	paneID := panes[0].ID
	if err := m.RespawnPane(paneID, "/tmp", "bash"); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	found := false
	for _, c := range m.Calls {
		if c.Method == "RespawnPane" {
			found = true
		}
	}
	if !found {
		t.Error("expected RespawnPane call to be recorded")
	}
}

func TestGetPanePID(t *testing.T) {
	m := NewMock()
	m.NewSession("work")
	winID, _ := m.NewWindow("work", "editor", "/home")
	panes, _ := m.ListPanes(winID)
	paneID := panes[0].ID
	pid, err := m.GetPanePID(paneID)
	if err != nil {
		t.Fatalf("GetPanePID: %v", err)
	}
	if pid <= 0 {
		t.Errorf("expected positive PID, got %d", pid)
	}
}

func TestGetPanePID_Missing(t *testing.T) {
	m := NewMock()
	_, err := m.GetPanePID("%999")
	if err == nil {
		t.Error("expected error for non-existent pane")
	}
}

// --- Current context tests ---

func TestCurrentSession(t *testing.T) {
	m := NewMock()
	m.SetCurrentSession("work")
	name, err := m.CurrentSession()
	if err != nil {
		t.Fatalf("CurrentSession: %v", err)
	}
	if name != "work" {
		t.Errorf("expected %q, got %q", "work", name)
	}
}

func TestCurrentSession_NotSet(t *testing.T) {
	m := NewMock()
	_, err := m.CurrentSession()
	if err == nil {
		t.Error("expected error when current session not set")
	}
}

func TestCurrentWindowID(t *testing.T) {
	m := NewMock()
	m.SetCurrentWindowID("@3")
	id, err := m.CurrentWindowID()
	if err != nil {
		t.Fatalf("CurrentWindowID: %v", err)
	}
	if id != "@3" {
		t.Errorf("expected @3, got %q", id)
	}
}

func TestCurrentWindowID_NotSet(t *testing.T) {
	m := NewMock()
	_, err := m.CurrentWindowID()
	if err == nil {
		t.Error("expected error when current window ID not set")
	}
}

func TestCurrentPaneID(t *testing.T) {
	m := NewMock()
	m.SetCurrentPaneID("%7")
	id, err := m.CurrentPaneID()
	if err != nil {
		t.Fatalf("CurrentPaneID: %v", err)
	}
	if id != "%7" {
		t.Errorf("expected %%7, got %q", id)
	}
}

func TestCurrentPaneID_NotSet(t *testing.T) {
	m := NewMock()
	_, err := m.CurrentPaneID()
	if err == nil {
		t.Error("expected error when current pane ID not set")
	}
}

// --- Call tracking tests ---

func TestCallTracking(t *testing.T) {
	m := NewMock()
	m.NewSession("work") // also records an internal NewWindow call
	m.HasSession("work")
	m.ListSessions()
	// NewSession records 2 calls (NewSession + NewWindow for default window),
	// then HasSession and ListSessions = 4 total
	if len(m.Calls) != 4 {
		t.Fatalf("expected 4 calls, got %d: %v", len(m.Calls), m.Calls)
	}
	if m.Calls[0].Method != "NewSession" {
		t.Errorf("expected first call NewSession, got %q", m.Calls[0].Method)
	}
	if m.Calls[2].Method != "HasSession" {
		t.Errorf("expected call[2] HasSession, got %q", m.Calls[2].Method)
	}
	if m.Calls[3].Method != "ListSessions" {
		t.Errorf("expected call[3] ListSessions, got %q", m.Calls[3].Method)
	}
}

// --- currentTarget tests ---

// currentTarget decides what "current" means for CurrentSession /
// CurrentWindowID / CurrentPaneID. An untargeted tmux query resolves
// against the most recently active session on the server, which is not
// necessarily the one that invoked bay, so these anchors are what keep
// keybindings acting on the dock the user pressed the key in.
func TestCurrentTarget(t *testing.T) {
	tests := []struct {
		name     string
		tmux     string
		tmuxPane string
		want     string
	}{
		{
			name:     "pane wins when both are set",
			tmux:     "/tmp/tmux-501/default,29529,1",
			tmuxPane: "%241",
			want:     "%241",
		},
		{
			name: "falls back to the session id from TMUX",
			tmux: "/tmp/tmux-501/default,29529,1",
			want: "$1",
		},
		{
			name: "socket path containing a comma still yields the session",
			tmux: "/tmp/od,d/default,29529,7",
			want: "$7",
		},
		{
			name: "session id zero is a valid target",
			tmux: "/tmp/tmux-501/default,29529,0",
			want: "$0",
		},
		{
			name: "no anchor outside tmux",
			want: "",
		},
		{
			name: "malformed TMUX yields no anchor",
			tmux: "/tmp/tmux-501/default,29529",
			want: "",
		},
		{
			name: "non-numeric session field yields no anchor",
			tmux: "/tmp/tmux-501/default,29529,abc",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TMUX", tt.tmux)
			t.Setenv("TMUX_PANE", tt.tmuxPane)
			if got := currentTarget(); got != tt.want {
				t.Errorf("currentTarget() = %q, want %q", got, tt.want)
			}
		})
	}
}
