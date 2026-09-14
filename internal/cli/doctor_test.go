package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/commontoolsinc/bay/internal/config"
	"github.com/commontoolsinc/bay/internal/engine"
	"github.com/commontoolsinc/bay/internal/git"
	"github.com/commontoolsinc/bay/internal/manifest"
)

func TestMissingKeybindings(t *testing.T) {
	content := strings.Join(tmuxKeybindingLines()[:2], "\n")
	missing := missingKeybindings(content)
	if len(missing) != len(tmuxKeybindingLines())-2 {
		t.Fatalf("missing = %d, want %d", len(missing), len(tmuxKeybindingLines())-2)
	}
}

func TestMissingKeybindings_HonorsBayKeep(t *testing.T) {
	// A user who pinned M-s via `# bay-keep:` has chosen a non-canonical
	// binding on purpose. Doctor should not warn about the canonical
	// M-s line being "missing" — otherwise the pin has no end-to-end
	// effect (setup stops asking but doctor still nags).
	content := "# Bay keybindings\n# bay-keep: M-s\n" +
		"bind-key -n M-s run-shell 'bay shell --window || true'\n"
	for _, kb := range missingKeybindings(content) {
		if kb.id() == "M-s" {
			t.Errorf("missingKeybindings reported pinned key M-s as missing: %q", kb.canonicalLine())
		}
	}
}

func TestMissingKeybindings_DetectsCommentedHomeBinding(t *testing.T) {
	var lines []string
	var homeEnter string
	for _, line := range tmuxKeybindingLines() {
		if strings.Contains(line, "bind-key -T bay-home Enter ") {
			homeEnter = line
			lines = append(lines, "# "+line)
			continue
		}
		lines = append(lines, line)
	}
	if homeEnter == "" {
		t.Fatal("bay-home Enter binding not found in canonical keybindings")
	}

	missing := missingKeybindings(strings.Join(lines, "\n"))
	if len(missing) != 1 || missing[0].canonicalLine() != homeEnter {
		t.Fatalf("missingKeybindings = %v; want only commented home Enter binding %q", missing, homeEnter)
	}
}

func TestKeybindingsIncludeSurfaceNavigation(t *testing.T) {
	lines := tmuxKeybindingLines()
	joined := strings.Join(lines, "\n")

	// Navigation + creation keybindings must be present.
	for _, want := range []string{
		"M-h", "M-l", // prev/next window (tmux-native)
		"M-j", "M-k", // pane down/up (tmux-native)
		"M-H", "M-L", "M-J", "M-K", // pane left/right + shift-mirror of j/k
		"M-g", // bay picker
		"M-a", // create agent
		"M-c", // create bay
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("keybindings missing %q", want)
		}
	}
	if !strings.Contains(joined, "display-popup -w 80% -h 80% -E 'bay palette --split pane || true'") {
		t.Errorf("keybindings should default Option+p palette to pane mode; got:\n%s", joined)
	}
	if !strings.Contains(joined, "bind-key -T bay-home Enter run-shell 'bay home || true'") {
		t.Errorf("keybindings should include bay-home Enter binding; got:\n%s", joined)
	}
}

func TestCheckDockAwareness(t *testing.T) {
	const pointer = "Run `bay agent-guide` for commands.\n"

	write := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}

	cases := []struct {
		name  string
		setup func(t *testing.T, checkout, wtDir string, m *git.Mock)
		want  []string // substrings that must appear in the output
		quiet bool     // no output at all
	}{
		{
			name: "fully initialized dock is quiet",
			setup: func(t *testing.T, checkout, wtDir string, m *git.Mock) {
				m.SetGlobalIgnored(true)
				write(t, filepath.Join(wtDir, engine.WorktreeAwarenessFile), pointer)
				write(t, filepath.Join(checkout, "CLAUDE.local.md"), pointer)
			},
			quiet: true,
		},
		{
			name: "non-git checkout is left alone",
			setup: func(t *testing.T, checkout, wtDir string, m *git.Mock) {
				m.SetIsGitRepo(checkout, false)
			},
			quiet: true,
		},
		{
			name: "worktree dir missing awareness",
			setup: func(t *testing.T, checkout, wtDir string, m *git.Mock) {
				m.SetGlobalIgnored(true)
				write(t, filepath.Join(checkout, "CLAUDE.local.md"), pointer)
			},
			want: []string{"worktree dir missing bay awareness"},
		},
		{
			name: "project file not gitignored",
			setup: func(t *testing.T, checkout, wtDir string, m *git.Mock) {
				m.SetGlobalIgnored(false)
				write(t, filepath.Join(wtDir, engine.WorktreeAwarenessFile), pointer)
			},
			want: []string{"CLAUDE.local.md not gitignored"},
		},
		{
			name: "gitignored project file missing",
			setup: func(t *testing.T, checkout, wtDir string, m *git.Mock) {
				m.SetGlobalIgnored(true)
				write(t, filepath.Join(wtDir, engine.WorktreeAwarenessFile), pointer)
			},
			want: []string{"CLAUDE.local.md not found"},
		},
		{
			name: "project file missing the pointer",
			setup: func(t *testing.T, checkout, wtDir string, m *git.Mock) {
				m.SetGlobalIgnored(true)
				write(t, filepath.Join(wtDir, engine.WorktreeAwarenessFile), pointer)
				write(t, filepath.Join(checkout, "CLAUDE.local.md"), "# My project\n")
			},
			want: []string{"CLAUDE.local.md missing bay awareness for claude"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			checkout := filepath.Join(dir, "labs")
			wtDir := filepath.Join(dir, "labs-worktrees")
			for _, d := range []string{checkout, wtDir} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			mockGit := git.NewMock()
			cfg := &config.Config{Agents: map[string]config.AgentConfig{}}
			eng := engine.New(cfg, "", "", "", nil, mockGit)
			dock := &manifest.Dock{Name: "labs", Path: checkout, WorktreeDir: wtDir}
			tc.setup(t, checkout, wtDir, mockGit)

			var out strings.Builder
			checkDockAwareness(eng, dock, checkout, &out)

			if tc.quiet && out.Len() > 0 {
				t.Fatalf("expected no output, got:\n%s", out.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output missing %q, got:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestCheckManifestConsistency(t *testing.T) {
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"claude": {Command: "claude"},
		},
	}
	m := manifest.New()
	agentName := "missing-agent"
	m.Docks = []manifest.Dock{
		{
			Name: "labs",
			Bays: []manifest.Bay{
				{
					Name: "dup",
					Surfaces: []manifest.Surface{
						{ID: 1, Name: "s1", Type: manifest.SurfaceTypeAgent, Backend: manifest.SurfaceBackendTmux, Agent: &agentName, Tmux: &manifest.TmuxAttrs{}},
						{ID: 1, Name: "s1", Type: manifest.SurfaceTypeShell, Backend: manifest.SurfaceBackendTmux, Tmux: &manifest.TmuxAttrs{}},
					},
				},
				{
					Name: "dup",
					Surfaces: []manifest.Surface{
						{ID: 2, Name: "s2", Type: manifest.SurfaceTypeShell, Backend: manifest.SurfaceBackendTmux, Tmux: &manifest.TmuxAttrs{}},
					},
				},
			},
		},
	}

	warnings := checkManifestConsistency(m, cfg)
	if len(warnings) < 3 {
		t.Fatalf("warnings = %v, want at least 3 consistency warnings (dup bay name, dup surface id, dup surface name)", warnings)
	}
}
