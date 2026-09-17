package monitor

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/git"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/tmux"
)

// --- TestStripANSI ---

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no escape codes",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "color code",
			input: "\033[31mred text\033[0m",
			want:  "red text",
		},
		{
			name:  "bold and color",
			input: "\033[1;32mbold green\033[0m normal",
			want:  "bold green normal",
		},
		{
			name:  "cursor movement",
			input: "\033[2Jhello\033[H",
			want:  "hello",
		},
		{
			name:  "multiple sequences",
			input: "\033[36m>\033[0m \033[1mPrompt:\033[0m ",
			want:  "> Prompt: ",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "OSC sequences",
			input: "\033]0;title\007some text",
			want:  "some text",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := StripANSI(tc.input)
			if got != tc.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// --- TestLoadPatterns ---

func TestLoadPatterns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "waiting-patterns.txt")

	content := `# This is a comment
(?i)waiting for input

# Another comment
\? \[Y/n\]
Do you want to continue
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing patterns file: %v", err)
	}

	patterns, err := LoadPatterns(path)
	if err != nil {
		t.Fatalf("LoadPatterns: %v", err)
	}
	if len(patterns) != 3 {
		t.Fatalf("expected 3 patterns, got %d", len(patterns))
	}

	if !patterns[0].MatchString("WAITING FOR INPUT") {
		t.Error("pattern 0 should match case-insensitively")
	}
	if !patterns[1].MatchString("? [Y/n]") {
		t.Error("pattern 1 should match Y/n prompt")
	}
	if !patterns[2].MatchString("Do you want to continue") {
		t.Error("pattern 2 should match continuation prompt")
	}
}

func TestLoadPatterns_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte("# only comments\n\n"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	patterns, err := LoadPatterns(path)
	if err != nil {
		t.Fatalf("LoadPatterns: %v", err)
	}
	if len(patterns) != 0 {
		t.Errorf("expected 0 patterns, got %d", len(patterns))
	}
}

func TestLoadPatterns_MissingFile(t *testing.T) {
	patterns, err := LoadPatterns("/nonexistent/file.txt")
	if err != nil {
		t.Fatalf("LoadPatterns should not error on missing file, got: %v", err)
	}
	if len(patterns) != 0 {
		t.Errorf("expected 0 patterns for missing file, got %d", len(patterns))
	}
}

func TestLoadPatterns_BadRegex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(path, []byte("[invalid\n"), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	_, err := LoadPatterns(path)
	if err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestPrunePrepareLogsRemovesOldDatesAndThrottles(t *testing.T) {
	root := t.TempDir()
	logsRoot := filepath.Join(root, "logs")
	oldDir := filepath.Join(logsRoot, "labs", "2026-04-20")
	keepDir := filepath.Join(logsRoot, "labs", "2026-05-01")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("mkdir old log dir: %v", err)
	}
	if err := os.MkdirAll(keepDir, 0o755); err != nil {
		t.Fatalf("mkdir keep log dir: %v", err)
	}
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)

	if err := prunePrepareLogs(logsRoot, now); err != nil {
		t.Fatalf("prunePrepareLogs: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old dir stat = %v, want not exist", err)
	}
	if _, err := os.Stat(keepDir); err != nil {
		t.Fatalf("keep dir stat = %v, want exist", err)
	}

	newOldDir := filepath.Join(logsRoot, "labs", "2026-04-19")
	if err := os.MkdirAll(newOldDir, 0o755); err != nil {
		t.Fatalf("mkdir second old log dir: %v", err)
	}
	if err := prunePrepareLogs(logsRoot, now.Add(time.Hour)); err != nil {
		t.Fatalf("second prunePrepareLogs: %v", err)
	}
	if _, err := os.Stat(newOldDir); err != nil {
		t.Fatalf("throttled prune should leave second old dir, stat = %v", err)
	}
}

// --- TestCheckPane ---

func TestCheckPane(t *testing.T) {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)waiting for input`),
		regexp.MustCompile(`\? \[Y/n\]`),
	}

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "matching pattern",
			content: "some output\nWaiting for input\nmore output",
			want:    true,
		},
		{
			name:    "matching Y/n prompt",
			content: "Install package? [Y/n] ",
			want:    true,
		},
		{
			name:    "no match",
			content: "compiling...\ndone.\n$",
			want:    false,
		},
		{
			name:    "empty content",
			content: "",
			want:    false,
		},
		{
			name:    "empty patterns",
			content: "anything here",
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := patterns
			if tc.name == "empty patterns" {
				p = nil
			}
			got := CheckPane(tc.content, p)
			if got != tc.want {
				t.Errorf("CheckPane(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

// --- TestPIDFile ---

func TestPIDFile(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "monitor.pid")

	if err := WritePIDFile(pidPath, 12345); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}

	pid, err := ReadPIDFile(pidPath)
	if err != nil {
		t.Fatalf("ReadPIDFile: %v", err)
	}
	if pid != 12345 {
		t.Errorf("expected PID 12345, got %d", pid)
	}

	if err := RemovePIDFile(pidPath); err != nil {
		t.Fatalf("RemovePIDFile: %v", err)
	}

	_, err = ReadPIDFile(pidPath)
	if err == nil {
		t.Error("expected error reading removed PID file")
	}
}

// --- TestCheckLoop ---

// createTestManifest builds a manifest with one dock ("dev") containing one bay
// with an agent surface and a shell surface.
func createTestManifest(t *testing.T, dir string, tmuxWindowID string) string {
	t.Helper()
	agentName := "claude"
	m := manifest.New()
	m.Docks = []manifest.Dock{
		{
			Name: "dev",
			Bays: []manifest.Bay{
				{
					Name: "test-bay",
					Type: manifest.BayTypeWorktree,
					Surfaces: []manifest.Surface{
						{
							ID: 1, Name: "agent", Type: manifest.SurfaceTypeAgent,
							Backend: manifest.SurfaceBackendTmux, Agent: &agentName,
							Tmux: &manifest.TmuxAttrs{WindowID: tmuxWindowID, PaneID: "%1", LayoutGroup: 1},
						},
						{
							ID: 2, Name: "shell", Type: manifest.SurfaceTypeShell,
							Backend: manifest.SurfaceBackendTmux,
							Tmux:    &manifest.TmuxAttrs{WindowID: tmuxWindowID, PaneID: "%2", LayoutGroup: 1},
						},
					},
				},
			},
		},
	}
	path := filepath.Join(dir, "manifest.json")
	if err := manifest.Save(path, m); err != nil {
		t.Fatalf("saving test manifest: %v", err)
	}
	return path
}

// createShellOnlyManifest builds a manifest where the bay has only a shell surface.
func createShellOnlyManifest(t *testing.T, dir string, tmuxWindowID string) string {
	t.Helper()
	m := manifest.New()
	m.Docks = []manifest.Dock{
		{
			Name: "dev",
			Bays: []manifest.Bay{
				{
					Name: "shell-bay",
					Type: manifest.BayTypeWorktree,
					Surfaces: []manifest.Surface{
						{
							ID: 1, Name: "shell", Type: manifest.SurfaceTypeShell,
							Backend: manifest.SurfaceBackendTmux,
							Tmux:    &manifest.TmuxAttrs{WindowID: tmuxWindowID, PaneID: "%1", LayoutGroup: 1},
						},
					},
				},
			},
		},
	}
	path := filepath.Join(dir, "manifest.json")
	if err := manifest.Save(path, m); err != nil {
		t.Fatalf("saving test manifest: %v", err)
	}
	return path
}

func TestCheckLoop_SetsHighlightOnMatch(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()

	mock.NewSession("dev")
	winID, _ := mock.NewWindow("dev", "test-bay", "/tmp")
	panes, _ := mock.ListPanes(winID)
	paneID := panes[0].ID

	mock.SetCaptureContent(paneID, "some output\nWaiting for input\n$")

	manifestPath := createTestManifest(t, dir, winID)

	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	if err := os.WriteFile(patternsPath, []byte("(?i)waiting for input\n"), 0o644); err != nil {
		t.Fatalf("writing patterns: %v", err)
	}

	pidPath := filepath.Join(dir, "monitor.pid")
	mon := New(mock, manifestPath, patternsPath, pidPath, 1)

	if err := mon.CheckOnce(); err != nil {
		t.Fatalf("CheckOnce: %v", err)
	}

	val, err := mock.GetWindowOption(winID, "@bay-waiting")
	if err != nil {
		t.Fatalf("GetWindowOption: %v", err)
	}
	if val != "1" {
		t.Errorf("expected @bay-waiting=1, got %q", val)
	}

	style, err := mock.GetWindowOption(winID, "window-status-style")
	if err != nil {
		t.Fatalf("GetWindowOption for style: %v", err)
	}
	if style == "" {
		t.Error("expected window-status-style to be set")
	}
}

func TestCheckLoop_ClearsHighlightWhenNoMatch(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()

	mock.NewSession("dev")
	winID, _ := mock.NewWindow("dev", "test-bay", "/tmp")
	panes, _ := mock.ListPanes(winID)
	paneID := panes[0].ID

	mock.SetCaptureContent(paneID, "Waiting for input")

	manifestPath := createTestManifest(t, dir, winID)
	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	if err := os.WriteFile(patternsPath, []byte("(?i)waiting for input\n"), 0o644); err != nil {
		t.Fatalf("writing patterns: %v", err)
	}

	pidPath := filepath.Join(dir, "monitor.pid")
	mon := New(mock, manifestPath, patternsPath, pidPath, 1)

	if err := mon.CheckOnce(); err != nil {
		t.Fatalf("CheckOnce (1): %v", err)
	}
	val, _ := mock.GetWindowOption(winID, "@bay-waiting")
	if val != "1" {
		t.Fatalf("expected highlight to be set after first check")
	}

	mock.SetCaptureContent(paneID, "compiling...\ndone.\n$")

	if err := mon.CheckOnce(); err != nil {
		t.Fatalf("CheckOnce (2): %v", err)
	}
	val, err := mock.GetWindowOption(winID, "@bay-waiting")
	if err != nil {
		t.Fatalf("GetWindowOption: %v", err)
	}
	if val != "0" {
		t.Errorf("expected @bay-waiting=0, got %q", val)
	}
}

// TestCheckLoop_ReassertsWaitingAfterFocusClear is the regression test for the
// Option-R rot: setup's after-select-window hook clears @bay-waiting whenever a
// window is focused (even just glanced at), out from under the monitor. While
// the agent's prompt is still on screen the monitor must put the flag back, or
// `bay go --next-waiting` can never return to a tab the user briefly visited.
func TestCheckLoop_ReassertsWaitingAfterFocusClear(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()

	mock.NewSession("dev")
	winID, _ := mock.NewWindow("dev", "test-bay", "/tmp")
	panes, _ := mock.ListPanes(winID)
	paneID := panes[0].ID

	mock.SetCaptureContent(paneID, "Waiting for input")

	manifestPath := createTestManifest(t, dir, winID)
	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	if err := os.WriteFile(patternsPath, []byte("(?i)waiting for input\n"), 0o644); err != nil {
		t.Fatalf("writing patterns: %v", err)
	}

	pidPath := filepath.Join(dir, "monitor.pid")
	mon := New(mock, manifestPath, patternsPath, pidPath, 1)

	if err := mon.CheckOnce(); err != nil {
		t.Fatalf("CheckOnce (1): %v", err)
	}
	if val, _ := mock.GetWindowOption(winID, "@bay-waiting"); val != "1" {
		t.Fatalf("expected @bay-waiting=1 after first check, got %q", val)
	}

	// Simulate the after-select-window hook firing when the user focuses the
	// tab: @bay-waiting is cleared, but the red style (and the prompt) remain.
	if err := mock.SetWindowOption(winID, "@bay-waiting", "0"); err != nil {
		t.Fatalf("clearing flag: %v", err)
	}

	// The prompt is still on screen, so the next cycle must re-flag the window.
	if err := mon.CheckOnce(); err != nil {
		t.Fatalf("CheckOnce (2): %v", err)
	}
	val, err := mock.GetWindowOption(winID, "@bay-waiting")
	if err != nil {
		t.Fatalf("GetWindowOption: %v", err)
	}
	if val != "1" {
		t.Errorf("expected @bay-waiting re-asserted to 1 after focus clear, got %q", val)
	}
}

func TestCheckLoop_SkipsShellOnlyWindows(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()

	mock.NewSession("dev")
	winID, _ := mock.NewWindow("dev", "shell-bay", "/tmp")
	panes, _ := mock.ListPanes(winID)
	paneID := panes[0].ID

	mock.SetCaptureContent(paneID, "Waiting for input")

	manifestPath := createShellOnlyManifest(t, dir, winID)
	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	if err := os.WriteFile(patternsPath, []byte("(?i)waiting for input\n"), 0o644); err != nil {
		t.Fatalf("writing patterns: %v", err)
	}

	pidPath := filepath.Join(dir, "monitor.pid")
	mon := New(mock, manifestPath, patternsPath, pidPath, 1)

	if err := mon.CheckOnce(); err != nil {
		t.Fatalf("CheckOnce: %v", err)
	}

	_, err := mock.GetWindowOption(winID, "@bay-waiting")
	if err == nil {
		t.Error("expected no @bay-waiting option on shell-only window")
	}
}

func TestCheckLoop_HandlesANSIInPaneContent(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()

	mock.NewSession("dev")
	winID, _ := mock.NewWindow("dev", "test-bay", "/tmp")
	panes, _ := mock.ListPanes(winID)
	paneID := panes[0].ID

	mock.SetCaptureContent(paneID, "\033[1;31mWaiting for input\033[0m")

	manifestPath := createTestManifest(t, dir, winID)
	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	if err := os.WriteFile(patternsPath, []byte("(?i)waiting for input\n"), 0o644); err != nil {
		t.Fatalf("writing patterns: %v", err)
	}

	pidPath := filepath.Join(dir, "monitor.pid")
	mon := New(mock, manifestPath, patternsPath, pidPath, 1)

	if err := mon.CheckOnce(); err != nil {
		t.Fatalf("CheckOnce: %v", err)
	}

	val, err := mock.GetWindowOption(winID, "@bay-waiting")
	if err != nil {
		t.Fatalf("GetWindowOption: %v", err)
	}
	if val != "1" {
		t.Errorf("expected @bay-waiting=1 after ANSI stripping, got %q", val)
	}
}

func TestCheckLoop_ReloadsPatterns(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()

	mock.NewSession("dev")
	winID, _ := mock.NewWindow("dev", "test-bay", "/tmp")
	panes, _ := mock.ListPanes(winID)
	paneID := panes[0].ID

	mock.SetCaptureContent(paneID, "custom prompt here")

	manifestPath := createTestManifest(t, dir, winID)
	patternsPath := filepath.Join(dir, "waiting-patterns.txt")

	if err := os.WriteFile(patternsPath, []byte("# empty\n"), 0o644); err != nil {
		t.Fatalf("writing patterns: %v", err)
	}

	pidPath := filepath.Join(dir, "monitor.pid")
	mon := New(mock, manifestPath, patternsPath, pidPath, 1)

	if err := mon.CheckOnce(); err != nil {
		t.Fatalf("CheckOnce (1): %v", err)
	}

	_, err := mock.GetWindowOption(winID, "@bay-waiting")
	if err == nil {
		t.Error("expected no @bay-waiting with empty patterns")
	}

	if err := os.WriteFile(patternsPath, []byte("custom prompt here\n"), 0o644); err != nil {
		t.Fatalf("rewriting patterns: %v", err)
	}

	if err := mon.CheckOnce(); err != nil {
		t.Fatalf("CheckOnce (2): %v", err)
	}

	val, err := mock.GetWindowOption(winID, "@bay-waiting")
	if err != nil {
		t.Fatalf("GetWindowOption: %v", err)
	}
	if val != "1" {
		t.Errorf("expected @bay-waiting=1 after pattern reload, got %q", val)
	}
}

func TestRun_CancelsOnContext(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()

	manifestPath := filepath.Join(dir, "manifest.json")
	m := manifest.New()
	manifest.Save(manifestPath, m)

	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	os.WriteFile(patternsPath, []byte(""), 0o644)

	pidPath := filepath.Join(dir, "monitor.pid")
	mon := New(mock, manifestPath, patternsPath, pidPath, 1)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- mon.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("Run returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

// --- Engine SyncAll integration ---

// Regression: when an engine is wired into the monitor via SetEngine,
// each CheckOnce cycle should call engine.SyncAll() so that branch
// changes (and the resulting tmux window renames) propagate without
// the user having to run a CLI command. Before this hookup, a fresh
// `git checkout -b ...` inside a bay pane would leave the tmux
// tab name stuck on the old bay name forever.
func TestCheckOnce_RenamesWindowOnBranchChange(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()
	mockGit := git.NewMock()

	// engine.SyncAll() only probes a bay whose Path exists on
	// disk; create a temp dir to stand in for the worktree.
	wtPath := filepath.Join(dir, "wt")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("mkdir wt: %v", err)
	}

	mock.NewSession("dev")
	winID, _ := mock.NewWindow("dev", "w1", wtPath)

	m := manifest.New()
	m.Docks = []manifest.Dock{
		{
			Name: "dev",
			Path: filepath.Join(dir, "repo"),
			Bays: []manifest.Bay{
				{
					Name:     "w1",
					Type:     manifest.BayTypeWorktree,
					Path:     wtPath,
					Worktree: &manifest.WorktreeAttrs{Branch: ""},
					Surfaces: []manifest.Surface{
						{
							ID: 1, Name: "shell", Type: manifest.SurfaceTypeShell,
							Backend: manifest.SurfaceBackendTmux,
							Tmux:    &manifest.TmuxAttrs{WindowID: winID, PaneID: "%1", LayoutGroup: 1},
						},
					},
				},
			},
		},
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if err := manifest.Save(manifestPath, m); err != nil {
		t.Fatalf("saving manifest: %v", err)
	}

	// User just ran `git checkout -b fix/login-bug` inside the
	// bay. The git mock now reports the new branch.
	mockGit.SetBranch(wtPath, "fix/login-bug")

	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	_ = os.WriteFile(patternsPath, []byte(""), 0o644)
	pidPath := filepath.Join(dir, "monitor.pid")

	mon := NewWithGit(mock, mockGit, manifestPath, patternsPath, pidPath, 1)

	// Wire the same dependencies into an engine and attach it.
	cfg := &config.Config{Agents: map[string]config.AgentConfig{}, Docks: map[string]config.DockConfig{}}
	eng := engine.New(cfg, "", manifestPath, filepath.Join(dir, "archive.json"), mock, mockGit)
	mon.SetEngine(eng)

	if err := mon.CheckOnce(); err != nil {
		t.Fatalf("CheckOnce: %v", err)
	}

	// Manifest should reflect the new branch.
	updated, err := manifest.Load(manifestPath)
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	bay := updated.FindDock("dev").FindBay("login-bug")
	if bay == nil {
		t.Fatalf("bay not found at expected post-rename name 'login-bug'; manifest: %+v", updated.Docks[0].Bays)
	}
	if bay.Worktree.Branch != "fix/login-bug" {
		t.Errorf("branch = %q, want fix/login-bug", bay.Worktree.Branch)
	}

	// Tmux window should also have been renamed.
	renamed := false
	for _, c := range mock.Calls {
		if c.Method == "RenameWindow" && len(c.Args) == 2 && c.Args[0] == winID && c.Args[1] == "wt.login-bug" {
			renamed = true
			break
		}
	}
	if !renamed {
		t.Errorf("expected RenameWindow(%s, wt.login-bug); calls: %v", winID, mock.Calls)
	}
}

func TestCheckOnce_NoEngine_DoesNotPanic(t *testing.T) {
	// Engine is optional. The monitor should still work fine without
	// one — prompt detection only.
	dir := t.TempDir()
	mock := tmux.NewMock()
	mock.NewSession("dev")
	winID, _ := mock.NewWindow("dev", "test-bay", "/tmp")

	manifestPath := createTestManifest(t, dir, winID)
	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	_ = os.WriteFile(patternsPath, []byte(""), 0o644)
	pidPath := filepath.Join(dir, "monitor.pid")

	mon := New(mock, manifestPath, patternsPath, pidPath, 1)
	// No SetEngine call.

	if err := mon.CheckOnce(); err != nil {
		t.Fatalf("CheckOnce: %v", err)
	}
}

// --- PR detection tests ---

func TestCheckOnce_DetectsPR(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()
	mockGit := git.NewMock()

	mock.NewSession("dev")
	winID, _ := mock.NewWindow("dev", "test-bay", "/tmp")

	agentName := "claude"
	m := manifest.New()
	m.Docks = []manifest.Dock{
		{
			Name: "dev",
			Bays: []manifest.Bay{
				{
					Name:     "test-bay",
					Type:     manifest.BayTypeWorktree,
					Path:     "/tmp",
					Worktree: &manifest.WorktreeAttrs{Repo: "labs", Branch: "feature/login"},
					Surfaces: []manifest.Surface{
						{
							ID: 1, Name: "agent", Type: manifest.SurfaceTypeAgent,
							Backend: manifest.SurfaceBackendTmux, Agent: &agentName,
							Tmux: &manifest.TmuxAttrs{WindowID: winID, PaneID: "%1", LayoutGroup: 1},
						},
					},
				},
			},
		},
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest.Save(manifestPath, m)

	// Configure mock: PR exists for this branch.
	mockGit.SetPR("/tmp", "feature/login", "99")

	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	os.WriteFile(patternsPath, []byte(""), 0o644)
	pidPath := filepath.Join(dir, "monitor.pid")

	mon := NewWithGit(mock, mockGit, manifestPath, patternsPath, pidPath, 1)

	// Call detectPRs directly — CheckOnce no longer invokes it (SyncAll
	// handles PR detection at the engine layer).
	changed := mon.detectPRs(m)
	if !changed {
		t.Fatal("detectPRs returned false, want true")
	}

	// Verify PR was cached in the manifest struct.
	bay := m.FindDock("dev").FindBay("test-bay")
	if bay.Worktree.PR != "99" {
		t.Errorf("PR = %q, want 99", bay.Worktree.PR)
	}
}

func TestCheckOnce_PRCheckedAtPreventsRecheckWithinTTL(t *testing.T) {
	// After a definitive "no PR found" answer, subsequent monitor cycles
	// should not re-query gh for the same bay. Otherwise we hammer
	// gh every minute for every PR-less bay forever.
	dir := t.TempDir()
	mock := tmux.NewMock()
	mockGit := git.NewMock()

	m := manifest.New()
	m.Docks = []manifest.Dock{
		{
			Name: "dev",
			Bays: []manifest.Bay{
				{
					Name:     "test-bay",
					Type:     manifest.BayTypeWorktree,
					Path:     "/tmp",
					Worktree: &manifest.WorktreeAttrs{Repo: "labs", Branch: "feature/no-pr"},
				},
			},
		},
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest.Save(manifestPath, m)

	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	os.WriteFile(patternsPath, []byte(""), 0o644)
	pidPath := filepath.Join(dir, "monitor.pid")

	mon := NewWithGit(mock, mockGit, manifestPath, patternsPath, pidPath, 1)

	// First detectPRs call — mock returns "" (no PR).
	mon.detectPRs(m)
	firstCallCount := len(mockGit.Calls("PRForBranch"))
	if firstCallCount != 1 {
		t.Fatalf("expected 1 PRForBranch call after first detectPRs, got %d", firstCallCount)
	}

	// Verify PRCheckedAt sentinel was set.
	bay := m.FindDock("dev").FindBay("test-bay")
	if bay.Worktree.PRCheckedAt == 0 {
		t.Error("PRCheckedAt should be set after first definitive 'no PR' answer")
	}

	// Call detectPRs again — NeedsPRCheck should gate it (TTL not expired).
	mon.detectPRs(m)
	if secondCount := len(mockGit.Calls("PRForBranch")); secondCount != firstCallCount {
		t.Errorf("expected PRForBranch call count to stay at %d, got %d after second detectPRs", firstCallCount, secondCount)
	}
}

func TestCheckOnce_SkipsPRDetectionForBayWithNoPath(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()
	mockGit := git.NewMock()

	m := manifest.New()
	m.Docks = []manifest.Dock{
		{
			Name: "dev",
			Bays: []manifest.Bay{
				{
					Name:     "test-bay",
					Worktree: &manifest.WorktreeAttrs{Repo: "labs", Branch: "feature/no-pr"},
					// No Path — detectPRs skips bays without a path.
				},
			},
		},
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest.Save(manifestPath, m)

	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	os.WriteFile(patternsPath, []byte(""), 0o644)
	pidPath := filepath.Join(dir, "monitor.pid")

	mon := NewWithGit(mock, mockGit, manifestPath, patternsPath, pidPath, 1)

	// Call detectPRs directly — bay has no Path so should be skipped.
	changed := mon.detectPRs(m)
	if changed {
		t.Error("detectPRs returned true for bay with no path")
	}

	// PR should remain empty — bay was skipped.
	bay := m.FindDock("dev").FindBay("test-bay")
	if bay.Worktree.PR != "" {
		t.Errorf("PR = %q, want empty", bay.Worktree.PR)
	}
}

// --- Merge detection tests ---

func TestCheckOnce_DetectsMergedBranch(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()
	mockGit := git.NewMock()

	// Bay with active branch that has been merged.
	m := manifest.New()
	m.Docks = []manifest.Dock{
		{
			Name: "dev",
			Bays: []manifest.Bay{
				{
					Name:       "feature-bay",
					Path:       "/tmp",
					LastActive: time.Now().Unix(), // recently active
					Worktree:   &manifest.WorktreeAttrs{Repo: "labs", Branch: "feature/done"},
				},
			},
		},
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest.Save(manifestPath, m)

	// Configure: branch is merged into default.
	mockGit.SetMerged("/tmp", "feature/done", true)

	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	os.WriteFile(patternsPath, []byte(""), 0o644)
	pidPath := filepath.Join(dir, "monitor.pid")

	mon := NewWithGit(mock, mockGit, manifestPath, patternsPath, pidPath, 1)

	// Run enough cycles to trigger merge check.
	for i := 0; i < MergeCheckCycles+1; i++ {
		mon.CheckOnce()
	}

	// Bay should be marked as merged.
	updated, _ := manifest.Load(manifestPath)
	dock := updated.FindDock("dev")
	bay := dock.FindBay("feature-bay")
	if !bay.Worktree.Merged {
		t.Error("Merged = false, want true")
	}
}

func TestCheckOnce_SkipsMergeCheckForInactiveBay(t *testing.T) {
	dir := t.TempDir()
	mock := tmux.NewMock()
	mockGit := git.NewMock()

	// Bay with no recent activity (LastActive = 0).
	m := manifest.New()
	m.Docks = []manifest.Dock{
		{
			Name: "dev",
			Bays: []manifest.Bay{
				{
					Name:     "old-bay",
					Path:     "/tmp",
					Worktree: &manifest.WorktreeAttrs{Repo: "labs", Branch: "feature/old"},
				},
			},
		},
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest.Save(manifestPath, m)

	mockGit.SetMerged("/tmp", "feature/old", true)

	patternsPath := filepath.Join(dir, "waiting-patterns.txt")
	os.WriteFile(patternsPath, []byte(""), 0o644)
	pidPath := filepath.Join(dir, "monitor.pid")

	mon := NewWithGit(mock, mockGit, manifestPath, patternsPath, pidPath, 1)

	for i := 0; i < MergeCheckCycles+1; i++ {
		mon.CheckOnce()
	}

	// Should NOT be marked merged — bay is inactive.
	updated, _ := manifest.Load(manifestPath)
	bay := updated.FindDock("dev").FindBay("old-bay")
	if bay.Worktree.Merged {
		t.Error("Merged = true, want false (inactive bay should be skipped)")
	}
}

func TestStatus_NotRunning(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "monitor.pid")
	mock := tmux.NewMock()
	mon := New(mock, "", "", pidPath, 1)

	running, pid, err := mon.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if running {
		t.Error("expected not running when no PID file")
	}
	if pid != 0 {
		t.Errorf("expected pid 0, got %d", pid)
	}
}

func TestMonitor_BinaryChangeDetection(t *testing.T) {
	dir := t.TempDir()
	bin := dir + "/fake-bay"
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	m := &Monitor{}

	// Before baseline: hasBinaryChanged must return false regardless
	// of file state. Otherwise the monitor could exec itself on
	// startup before it's even recorded what it started with.
	if m.hasBinaryChanged(bin) {
		t.Error("hasBinaryChanged should return false before baseline is recorded")
	}

	m.recordBinaryMTime(bin)
	if m.binaryMTime.IsZero() {
		t.Error("recordBinaryMTime should set binaryMTime")
	}

	if m.hasBinaryChanged(bin) {
		t.Error("unchanged file should not report a change")
	}

	// Bump mtime explicitly rather than relying on sleep + rewrite —
	// some filesystems have coarser mtime resolution than the sleep.
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(bin, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if !m.hasBinaryChanged(bin) {
		t.Error("expected true after mtime changed")
	}
}

func TestMonitor_BinaryChangeDetection_MissingFileIsSafe(t *testing.T) {
	m := &Monitor{}
	// Missing file during record: baseline stays zero; hasBinaryChanged
	// keeps returning false. Monitor silently stays on current code
	// rather than restart-looping on a broken path.
	m.recordBinaryMTime("/no/such/path")
	if !m.binaryMTime.IsZero() {
		t.Error("missing file should not record a baseline")
	}
	if m.hasBinaryChanged("/no/such/path") {
		t.Error("missing file should not report a change")
	}
}
