package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/commontoolsinc/bay/internal/tmux"
)

func TestHasUserStatusRight(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "empty config", content: "", want: false},
		{name: "set -g status-right", content: "set -g status-right '#H'\n", want: true},
		{name: "set-option -g status-right", content: "set-option -g status-right '#H'\n", want: true},
		{name: "set without -g", content: "set status-right '#H'\n", want: true},
		{name: "status-right-length only is not status-right", content: "set -g status-right-length 40\n", want: false},
		{name: "status-right-style is not status-right", content: "set -g status-right-style 'bg=blue'\n", want: false},
		{name: "commented status-right is not status-right", content: "# set -g status-right '#H'\n", want: false},
		{name: "indented commented is still skipped", content: "    # set -g status-right '#H'\n", want: false},
		{name: "status-left is not status-right", content: "set -g status-left '#S'\n", want: false},
		{name: "unrelated set", content: "set -g mouse on\n", want: false},
		{name: "mixed config with non-bay status-right", content: "# Some comment\nset -g mouse on\nset -g status-right '#(date)'\n", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasUserStatusRight(tt.content); got != tt.want {
				t.Errorf("hasUserStatusRight(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestInstallStatusRight_FreshInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	confPath := filepath.Join(home, ".tmux.conf")
	// Pre-create empty file so the installer's read returns ""
	_ = os.WriteFile(confPath, []byte("set -g mouse on\n"), 0o644)

	reader := bufio.NewReader(strings.NewReader("\n")) // accept default [Y/n]
	installStatusRight(reader)

	got, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read tmux.conf: %v", err)
	}
	for _, want := range bayStatusLineBlock {
		if !strings.Contains(string(got), want) {
			t.Errorf("after install, %s missing %q\nfull contents:\n%s", confPath, want, got)
		}
	}
}

func TestInstallStatusRight_AlreadyMarkedSkipsBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	confPath := filepath.Join(home, ".tmux.conf")
	original := "# Bay status line\nset -g status-right 'something custom'\n"
	_ = os.WriteFile(confPath, []byte(original), 0o644)

	reader := bufio.NewReader(strings.NewReader("\n"))
	installStatusRight(reader)

	got, _ := os.ReadFile(confPath)
	if string(got) != original {
		t.Errorf("file changed after no-op install:\nbefore: %q\nafter:  %q", original, got)
	}
}

func TestInstallStatusRight_RespectsUserStatusRight(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	confPath := filepath.Join(home, ".tmux.conf")
	original := "set -g status-right '#(date)'\n"
	_ = os.WriteFile(confPath, []byte(original), 0o644)

	reader := bufio.NewReader(strings.NewReader("\n"))
	installStatusRight(reader)

	got, _ := os.ReadFile(confPath)
	if string(got) != original {
		t.Errorf("user's status-right should not be modified:\nbefore: %q\nafter:  %q", original, got)
	}
}

func TestInstallStatusRight_NoConfigFileCreatesOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	confPath := filepath.Join(home, ".tmux.conf")
	if _, err := os.Stat(confPath); !os.IsNotExist(err) {
		t.Fatalf("expected no tmux.conf at start; got err=%v", err)
	}

	reader := bufio.NewReader(strings.NewReader("\n"))
	installStatusRight(reader)

	got, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("expected installer to create tmux.conf: %v", err)
	}
	if !strings.Contains(string(got), bayStatusLineMarker) {
		t.Errorf("created tmux.conf missing bay marker:\n%s", got)
	}
}

func TestInstallStatusRight_DeclineLeavesFileUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	confPath := filepath.Join(home, ".tmux.conf")
	original := "set -g mouse on\n"
	_ = os.WriteFile(confPath, []byte(original), 0o644)

	reader := bufio.NewReader(strings.NewReader("n\n"))
	installStatusRight(reader)

	got, _ := os.ReadFile(confPath)
	if string(got) != original {
		t.Errorf("declining the prompt should leave the file unchanged:\nbefore: %q\nafter:  %q", original, got)
	}
}

func TestHasMarkerLine_IgnoresMarkerInCommentText(t *testing.T) {
	// strings.Contains would have matched this — the line-aware check
	// must not, since the marker appears inside a longer comment.
	content := "# I tried the # Bay status line block once but reverted.\nset -g mouse on\n"
	if hasMarkerLine(content, bayStatusLineMarker) {
		t.Error("hasMarkerLine should require the marker on its own line")
	}
	// Same string on its own line (with surrounding whitespace) does match.
	content2 := "set -g mouse on\n   # Bay status line   \nset -g status-right 'x'\n"
	if !hasMarkerLine(content2, bayStatusLineMarker) {
		t.Error("hasMarkerLine should match a marker on its own (trimmed) line")
	}
}

func TestExtractBayBlock(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantFound bool
		wantBlock string
	}{
		{
			name:      "no block",
			content:   "set -g mouse on\nbind r source-file ~/.tmux.conf\n",
			wantFound: false,
		},
		{
			name: "block at end of file",
			content: `set -g mouse on

# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'
bind-key -n M-k run-shell 'bay surface prev'
`,
			wantFound: true,
			wantBlock: `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'
bind-key -n M-k run-shell 'bay surface prev'`,
		},
		{
			name: "block followed by other config separated by blank line",
			content: `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'

set -g status-bg blue
`,
			wantFound: true,
			wantBlock: `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'`,
		},
		{
			name: "block followed by other config with no blank line",
			content: `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'
set -g status-bg blue
`,
			wantFound: true,
			wantBlock: `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'`,
		},
		{
			name: "block at very start of file",
			content: `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'
`,
			wantFound: true,
			wantBlock: `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'`,
		},
		{
			name: "block contains commented binding (still in block)",
			content: `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'
# bind-key -n M-k run-shell 'bay surface prev'
bind-key -n M-g display-popup -E 'bay go'
`,
			wantFound: true,
			wantBlock: `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'
# bind-key -n M-k run-shell 'bay surface prev'
bind-key -n M-g display-popup -E 'bay go'`,
		},
		{
			name: "marker only — no following lines",
			content: `set -g mouse on
# Bay keybindings
`,
			wantFound: true,
			wantBlock: `# Bay keybindings`,
		},
		{
			name: "marker with leading whitespace",
			content: `set -g mouse on
   # Bay keybindings
bind-key -n M-j run-shell 'bay surface next'
`,
			wantFound: true,
			wantBlock: `   # Bay keybindings
bind-key -n M-j run-shell 'bay surface next'`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block, found := extractBayBlock(tt.content)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if found && block != tt.wantBlock {
				t.Errorf("block mismatch:\ngot:\n%s\nwant:\n%s", block, tt.wantBlock)
			}
		})
	}
}

func TestCommandsInBlock(t *testing.T) {
	block := `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'
# bind-key -n M-k run-shell 'bay surface prev'
bind-key -n M-g display-popup -E 'bay go'
bind -n M-x run-shell 'bay shell'
`
	got := commandsInBlock(block)
	want := map[string]bool{
		"bay surface next": true,
		"bay go":           true,
		"bay shell":        true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commandsInBlock() = %v, want %v", got, want)
	}
}

func TestCommandsInBlock_DoubleQuotes(t *testing.T) {
	// Users frequently mix quote styles in tmux configs. The diff
	// must recognize a double-quoted command, otherwise rebound bay
	// commands get falsely flagged as missing every setup run.
	block := `# Bay keybindings
bind-key -n M-j run-shell "bay surface next"
bind-key -n M-k run-shell 'bay surface prev'
`
	got := commandsInBlock(block)
	want := map[string]bool{
		"bay surface next": true,
		"bay surface prev": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commandsInBlock() = %v, want %v", got, want)
	}
}

func TestCommandsInBlock_TmuxNativeUnquoted(t *testing.T) {
	// Tmux-native bindings (like the M-h/l/HJKL nav set bay ships) emit
	// `bind-key -n KEY <tmux-cmd>` with no quoted shell payload. The
	// command extractor must still capture them, otherwise bay falsely
	// reports them as missing every setup run.
	block := `# Bay keybindings
bind-key -n M-h previous-window
bind-key -n M-l next-window
bind-key -n M-H select-pane -L
bind-key -n M-J select-pane -D
bind-key -n M-g run-shell 'bay go --pick || true'
`
	got := commandsInBlock(block)
	want := map[string]bool{
		"previous-window":       true,
		"next-window":           true,
		"select-pane -L":        true,
		"select-pane -D":        true,
		"bay go --pick || true": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commandsInBlock() = %v, want %v", got, want)
	}
}

func TestCommandsInBlock_IgnoresCommentedBindings(t *testing.T) {
	// Critical: the commented binding must NOT show up. This is the
	// core regression — substring-based detection treated commented
	// bindings as present.
	block := `# Bay keybindings
# bind-key -n M-j run-shell 'bay surface next'
`
	got := commandsInBlock(block)
	if got["bay surface next"] {
		t.Errorf("commented binding should not be in commandsInBlock; got %v", got)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
}

func TestConflictingKeys(t *testing.T) {
	kbs := []bayKeybinding{
		{key: "M-j", cmd: "bay surface next", tmuxVerb: "run-shell"},
		{key: "M-k", cmd: "bay surface prev", tmuxVerb: "run-shell"},
		{key: "M-g", cmd: "bay go", tmuxVerb: "display-popup -E"},
	}

	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "no conflicts",
			content: "set -g mouse on\n",
			want:    nil,
		},
		{
			name: "user has M-j bound to something else",
			content: `bind-key -n M-j send-keys "echo hi" Enter
`,
			want: []string{"M-j"},
		},
		{
			name: "user has M-k bound via short bind syntax",
			content: `bind -n M-k display-message "hi"
`,
			want: []string{"M-k"},
		},
		{
			name: "commented conflict is NOT a conflict",
			content: `# bind-key -n M-j send-keys "echo hi" Enter
`,
			want: nil,
		},
		{
			name: "two real conflicts",
			content: `bind-key -n M-j send-keys "x" Enter
bind -n M-g display-message "y"
`,
			want: []string{"M-j", "M-g"},
		},
		{
			name: "binding for similar but different key (M-J vs M-j) is not a conflict",
			content: `bind-key -n M-J send-keys "x" Enter
`,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := conflictingKeys(tt.content, kbs)
			// Order is keyed off kbs order; sort both for stable compare.
			sort.Strings(got)
			want := append([]string(nil), tt.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("conflictingKeys() = %v, want %v", got, want)
			}
		})
	}
}

func TestMissingCanonicalLines(t *testing.T) {
	kbs := []bayKeybinding{
		{key: "M-j", cmd: "bay surface next", tmuxVerb: "run-shell"},
		{key: "M-k", cmd: "bay surface prev", tmuxVerb: "run-shell"},
		{key: "M-g", cmd: "bay go", tmuxVerb: "display-popup -E"},
	}

	t.Run("user has all canonical commands (even on different keys)", func(t *testing.T) {
		// Diff is by command, not by key. The user rebound everything
		// to different keys but kept the same commands — should report
		// nothing missing.
		block := `# Bay keybindings
bind-key -n C-j run-shell 'bay surface next'
bind-key -n C-k run-shell 'bay surface prev'
bind-key -n C-g display-popup -E 'bay go'
`
		got := missingCanonicalLines(block, kbs)
		if len(got) != 0 {
			t.Errorf("expected no missing lines, got %v", got)
		}
	})

	t.Run("user is missing one canonical command", func(t *testing.T) {
		block := `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'
bind-key -n M-k run-shell 'bay surface prev'
`
		got := missingCanonicalLines(block, kbs)
		want := []string{kbs[2].canonicalLine()}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("missingCanonicalLines() = %v, want %v", got, want)
		}
	})

	t.Run("commented binding is an opt-out, not missing", func(t *testing.T) {
		// A user who removed a binding and left a commented stub is
		// signaling "don't re-add this." missingCanonicalLines must
		// treat commented bindings as present so bay doesn't nag.
		block := `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'
# bind-key -n M-k run-shell 'bay surface prev'
bind-key -n M-g display-popup -E 'bay go'
`
		got := missingCanonicalLines(block, kbs)
		if len(got) != 0 {
			t.Errorf("missingCanonicalLines() = %v, want empty (commented line should be opt-out)", got)
		}
	})

	t.Run("duplicate-command canonical bindings require each key", func(t *testing.T) {
		// M-j and M-J both run `select-pane -D` by design (Shift-held
		// chords shouldn't lose the modifier). If only M-J is bound,
		// M-j must still be reported missing — command match alone is
		// ambiguous for duplicates.
		dupKbs := []bayKeybinding{
			{key: "M-j", cmd: "select-pane -D", isTmuxCommand: true, global: true},
			{key: "M-J", cmd: "select-pane -D", isTmuxCommand: true, global: true},
			{key: "M-k", cmd: "select-pane -U", isTmuxCommand: true, global: true},
			{key: "M-K", cmd: "select-pane -U", isTmuxCommand: true, global: true},
		}
		block := `# Bay keybindings
bind-key -n M-J select-pane -D
bind-key -n M-K select-pane -U
`
		got := missingCanonicalLines(block, dupKbs)
		want := []string{
			"bind-key -n M-j select-pane -D",
			"bind-key -n M-k select-pane -U",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("missingCanonicalLines() = %v, want %v", got, want)
		}
	})

	t.Run("empty block reports everything missing", func(t *testing.T) {
		block := `# Bay keybindings`
		got := missingCanonicalLines(block, kbs)
		if len(got) != len(kbs) {
			t.Errorf("expected %d missing lines, got %d: %v", len(kbs), len(got), got)
		}
	})
}

func TestAppendToBayBlock(t *testing.T) {
	content := `set -g mouse on

# Bay keybindings
bind-key -n M-h previous-window
bind-key -n M-l next-window

# another thing
set -g base-index 1
`
	got := appendToBayBlock(content, []string{
		"bind-key -n M-j select-pane -D",
		"# bind-key -n M-k select-pane -U",
	})
	want := `set -g mouse on

# Bay keybindings
bind-key -n M-h previous-window
bind-key -n M-l next-window
bind-key -n M-j select-pane -D
# bind-key -n M-k select-pane -U

# another thing
set -g base-index 1
`
	if got != want {
		t.Errorf("appendToBayBlock:\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}

func TestAppendToBayBlock_NoBlock(t *testing.T) {
	// No bay block present — content must be returned unchanged.
	content := "set -g mouse on\n"
	if got := appendToBayBlock(content, []string{"x"}); got != content {
		t.Errorf("expected no change, got %q", got)
	}
}

func TestCommentedCommandsInBlock(t *testing.T) {
	block := `# Bay keybindings
bind-key -n M-j run-shell 'bay surface next'
# bind-key -n M-k run-shell 'bay surface prev'
#bind-key -n M-h previous-window
bind-key -n M-l next-window
`
	got := commentedCommandsInBlock(block)
	want := map[string]bool{
		"bay surface prev": true,
		"previous-window":  true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commentedCommandsInBlock() = %v, want %v", got, want)
	}
}

// runShellBind renders the line bay writes for a root binding that
// runs a bay command — including the session scoping, so drift
// fixtures differ from canonical only in the command. Built from
// canonicalLine so the fixtures follow the format rather than
// restating it.
func runShellBind(key, cmd string) string {
	return bayKeybinding{key: key, cmd: cmd, tmuxVerb: "run-shell"}.canonicalLine() + "\n"
}

// unscopedLine renders the line bay wrote for kb before it scoped its
// keys — canonicalLine minus the session guard, which is the state every
// block installed by an earlier bay is in.
func unscopedLine(kb bayKeybinding) string {
	return fmt.Sprintf("bind-key %s %s %s\n", kb.bindFlag(), kb.key, kb.tmuxCommand())
}

// unscopedRunShellBind is unscopedLine for a root binding that runs a
// bay command, matching runShellBind's shape.
func unscopedRunShellBind(key, cmd string) string {
	return unscopedLine(bayKeybinding{key: key, cmd: cmd, tmuxVerb: "run-shell"})
}

func TestMismatchedBindings(t *testing.T) {
	kbs := []bayKeybinding{
		{key: "M-s", cmd: "bay shell --pane", tmuxVerb: "run-shell"},
		{key: "M-S", cmd: "bay shell --window", tmuxVerb: "run-shell"},
		{key: "M-e", cmd: "bay edit", tmuxVerb: "run-shell"},
		{key: "M-E", cmd: "bay edit --dock", tmuxVerb: "run-shell"},
	}

	t.Run("swap-case drift is flagged", func(t *testing.T) {
		// User has the old canonical: M-s=window, M-S=pane. Canonical
		// has since flipped. Both keys are bound, so the "missing" path
		// sees nothing — mismatch is what catches this.
		block := "# Bay keybindings\n" +
			runShellBind("M-s", "bay shell --window") +
			runShellBind("M-S", "bay shell --pane") +
			runShellBind("M-e", "bay edit") +
			runShellBind("M-E", "bay edit --dock")
		got := mismatchedBindings(block, kbs)
		wantKeys := []string{"M-s", "M-S"}
		if len(got) != len(wantKeys) {
			t.Fatalf("mismatchedBindings() returned %d; want %d: %+v", len(got), len(wantKeys), got)
		}
		for i, k := range wantKeys {
			if got[i].canonical.key != k {
				t.Errorf("mismatchedBindings()[%d].key = %q; want %q", i, got[i].canonical.key, k)
			}
		}
	})

	t.Run("canonical bindings report no mismatch", func(t *testing.T) {
		block := "# Bay keybindings\n" +
			runShellBind("M-s", "bay shell --pane") +
			runShellBind("M-S", "bay shell --window") +
			runShellBind("M-e", "bay edit")
		if got := mismatchedBindings(block, kbs); len(got) != 0 {
			t.Errorf("mismatchedBindings() = %+v; want empty", got)
		}
	})

	t.Run("non-bay rebinds are not mismatches", func(t *testing.T) {
		// User rebound M-s to something unrelated. Not drift we own —
		// skip silently. Both spellings: the unscoped line every
		// pre-scoping block holds, and a scoped one. The unscoped case
		// is the dangerous one — flagging it as scope drift would offer
		// to "fix the scoping" and replace the script.
		for _, block := range []string{
			"# Bay keybindings\n" + unscopedRunShellBind("M-s", "my-custom-script"),
			"# Bay keybindings\n" + runShellBind("M-s", "my-custom-script"),
		} {
			if got := mismatchedBindings(block, kbs); len(got) != 0 {
				t.Errorf("mismatchedBindings(%q) = %+v; want empty", block, got)
			}
		}
	})

	t.Run("custom tmux verb on a bay command is not scope drift", func(t *testing.T) {
		// The command is bay's, but the user chose a popup over
		// run-shell. Rewriting the line to canonical would take that
		// away, so the line is not bay's to migrate.
		block := "# Bay keybindings\n" +
			`bind-key -n M-s display-popup -E 'bay shell --pane || true'` + "\n"
		if got := mismatchedBindings(block, kbs); len(got) != 0 {
			t.Errorf("mismatchedBindings() = %+v; want empty for a customized verb", got)
		}
	})

	t.Run("bay-keep marker suppresses mismatch", func(t *testing.T) {
		block := "# Bay keybindings\n# bay-keep: M-s\n" +
			runShellBind("M-s", "bay shell --window") +
			runShellBind("M-S", "bay shell --pane")
		got := mismatchedBindings(block, kbs)
		if len(got) != 1 || got[0].canonical.key != "M-S" {
			t.Errorf("mismatchedBindings() = %+v; want only M-S mismatch", got)
		}
	})

	t.Run("missing key is not flagged as mismatch", func(t *testing.T) {
		// M-s not bound at all → missingBindings handles it; mismatch
		// only fires when the key is actively bound to the wrong thing.
		block := "# Bay keybindings\n" + runShellBind("M-S", "bay shell --window")
		if got := mismatchedBindings(block, kbs); len(got) != 0 {
			t.Errorf("mismatchedBindings() = %+v; want empty (M-s unbound is missing, not mismatch)", got)
		}
	})

	t.Run("bay-keep with multiple keys on one line", func(t *testing.T) {
		block := "# Bay keybindings\n# bay-keep: M-s M-e\n" +
			runShellBind("M-s", "bay shell --window") +
			runShellBind("M-e", "bay edit --dock")
		if got := mismatchedBindings(block, kbs); len(got) != 0 {
			t.Errorf("mismatchedBindings() = %+v; want empty (both pinned)", got)
		}
	})

	t.Run("custom palette window binding is not previous default drift", func(t *testing.T) {
		paletteKbs := []bayKeybinding{
			{key: "M-p", cmd: "bay palette --split pane", tmuxVerb: "display-popup -w 80% -h 80% -E"},
		}
		custom := bayKeybinding{key: "M-p", cmd: "bay palette --split window", tmuxVerb: "display-popup -w 80% -h 80% -E"}
		// Unscoped first: that's how a customized binding sits in every
		// block written before bay scoped its keys, and it must not be
		// swept up as scope drift and rewritten to `--split pane`.
		for _, block := range []string{
			"# Bay keybindings\n" + unscopedLine(custom),
			"# Bay keybindings\n" + custom.canonicalLine() + "\n",
		} {
			if got := mismatchedBindings(block, paletteKbs); len(got) != 0 {
				t.Fatalf("mismatchedBindings(%q) = %+v; want empty for custom palette binding", block, got)
			}
		}
	})

	t.Run("unscoped canonical palette binding migrates", func(t *testing.T) {
		// The flip side: bay's own display-popup line, as an earlier bay
		// wrote it, still has to pick up the session guard.
		paletteKb := bayKeybinding{key: "M-p", cmd: "bay palette --split pane", tmuxVerb: "display-popup -w 80% -h 80% -E"}
		block := "# Bay keybindings\n" + unscopedLine(paletteKb)
		got := mismatchedBindings(block, []bayKeybinding{paletteKb})
		if len(got) != 1 || got[0].canonical.key != "M-p" || !got[0].scoping {
			t.Fatalf("mismatchedBindings() = %+v; want single M-p scoping mismatch", got)
		}
	})

	t.Run("unscoped chord entry migrates", func(t *testing.T) {
		// M-o is an isTmuxCommand binding, so parseBindLine reports only
		// the quoted hint as its command — never the canonical string.
		// Migration has to recognize the line by its whole command tail
		// or the chord entry silently stays global.
		chordKb := findKeybinding(t, "", "M-o")
		block := "# Bay keybindings\n" + unscopedLine(chordKb)
		got := mismatchedBindings(block, []bayKeybinding{chordKb})
		if len(got) != 1 || got[0].canonical.key != "M-o" || !got[0].scoping {
			t.Fatalf("mismatchedBindings() = %+v; want single M-o scoping mismatch", got)
		}
		// And once migrated, the scoped line is canonical: the `\;` the
		// unscoped line carries becomes a bare `;` inside the guard, and
		// that spelling must not read as drift on the next setup run.
		scoped := "# Bay keybindings\n" + chordKb.canonicalLine() + "\n"
		if got := mismatchedBindings(scoped, []bayKeybinding{chordKb}); len(got) != 0 {
			t.Fatalf("mismatchedBindings(scoped M-o) = %+v; want empty", got)
		}
	})

	t.Run("custom chord entry command is not scope drift", func(t *testing.T) {
		chordKb := findKeybinding(t, "", "M-o")
		custom := chordKb
		custom.cmd = `display-message -d 2000 "my own hint" \; switch-client -T ` + tableAgent
		block := "# Bay keybindings\n" + unscopedLine(custom)
		if got := mismatchedBindings(block, []bayKeybinding{chordKb}); len(got) != 0 {
			t.Fatalf("mismatchedBindings() = %+v; want empty for a custom chord hint", got)
		}
	})

	t.Run("unscoped binding is flagged as drift", func(t *testing.T) {
		// What every existing user's block looks like before this
		// release: the right command, bound in every tmux session.
		// Rewriting the line is what confines it to bay's own.
		block := "# Bay keybindings\n" +
			"bind-key -n M-s run-shell 'bay shell --pane || true'\n"
		got := mismatchedBindings(block, kbs)
		if len(got) != 1 || got[0].canonical.key != "M-s" || !got[0].scoping {
			t.Fatalf("mismatchedBindings() = %+v; want single M-s scoping mismatch", got)
		}
	})

	t.Run("global bindings are not flagged for lacking scope", func(t *testing.T) {
		// Navigation keys are deliberately global; an unscoped line is
		// canonical for them.
		navKbs := []bayKeybinding{{key: "M-l", cmd: "next-window", isTmuxCommand: true, global: true}}
		block := "# Bay keybindings\nbind-key -n M-l next-window\n"
		if got := mismatchedBindings(block, navKbs); len(got) != 0 {
			t.Errorf("mismatchedBindings() = %+v; want empty for a global binding", got)
		}
	})

	t.Run("previous gemini agent bindings drift to antigravity", func(t *testing.T) {
		agentKbs := []bayKeybinding{
			agentInBay("g", "antigravity", "pane"),
			newBayWithAgent("g", "antigravity"),
			homeAgent("g", "antigravity"),
		}
		block := `# Bay keybindings
bind-key -T bay-agent g run-shell 'bay agent gemini --pane || true'
bind-key -T bay-agent-bay g run-shell 'bay new -q --agent=gemini || true'
bind-key -T bay-home g run-shell 'bay agent gemini --bay home || true'
`
		got := mismatchedBindings(block, agentKbs)
		wantIDs := []string{"bay-agent:g", "bay-agent-bay:g", "bay-home:g"}
		if len(got) != len(wantIDs) {
			t.Fatalf("mismatchedBindings() = %+v; want %v", got, wantIDs)
		}
		for i, want := range wantIDs {
			if got[i].canonical.id() != want {
				t.Errorf("mismatchedBindings()[%d] = %s; want %s", i, got[i].canonical.id(), want)
			}
		}
	})

	t.Run("every canonical binding migrates from its unscoped line", func(t *testing.T) {
		// The whole shipped table at once, written the way the last
		// unscoped bay wrote it. Exactly the scoped ones must be
		// offered for migration, and nothing else may be flagged as
		// command drift on the way through.
		var b strings.Builder
		b.WriteString("# Bay keybindings\n")
		for _, kb := range bayKeybindings {
			b.WriteString(unscopedLine(kb))
		}
		flagged := map[string]bool{}
		for _, m := range mismatchedBindings(b.String(), bayKeybindings) {
			if !m.scoping {
				t.Errorf("%s flagged as command drift; want scoping only (user cmd %q)", m.canonical.id(), m.userCmd)
			}
			flagged[m.canonical.id()] = true
		}
		for _, kb := range bayKeybindings {
			if got := flagged[kb.id()]; got != kb.scoped() {
				t.Errorf("%s flagged=%v; want %v", kb.id(), got, kb.scoped())
			}
		}
	})

	t.Run("previousCmds entry catches isTmuxCommand drift", func(t *testing.T) {
		// Regression: when a tmux-command binding's display-message hint
		// changes (e.g. M-o gaining `| h=home` in home-bay phase 6), the
		// user's old line doesn't match any other canonical, so the
		// fallback path can't catch it. previousCmds is the only signal.
		// parseBindLine extracts the quoted hint region, so the entry
		// must be the old hint text, not the full bind line.
		newCmd := `display-message -d 2000 "new hint" \; switch-client -T bay-agent`
		kbs := []bayKeybinding{{
			key:           "M-o",
			cmd:           newCmd,
			previousCmds:  []string{"old hint"},
			isTmuxCommand: true,
		}}
		block := "# Bay keybindings\n" +
			bayKeybinding{
				key:           "M-o",
				cmd:           `display-message -d 2000 "old hint" \; switch-client -T bay-agent`,
				isTmuxCommand: true,
			}.canonicalLine() + "\n"
		got := mismatchedBindings(block, kbs)
		if len(got) != 1 || got[0].canonical.key != "M-o" {
			t.Fatalf("mismatchedBindings() = %+v; want single M-o mismatch", got)
		}
	})
}

// bayWroteCommandTail is the gate on rewriting a line in place, so both
// halves matter: it must recognize bay's own command in either spelling,
// and must not recognize anything else.
func TestBayWroteCommandTail(t *testing.T) {
	shell := bayKeybinding{key: "M-s", cmd: "bay shell --pane", tmuxVerb: "run-shell"}
	chord := findKeybinding(t, "", "M-o")

	tests := []struct {
		name string
		tail string
		kb   bayKeybinding
		want bool
	}{
		{"unscoped canonical", `run-shell 'bay shell --pane || true'`, shell, true},
		{"custom command", `run-shell 'my-custom-script || true'`, shell, false},
		{"canonical command, custom verb", `display-popup -E 'bay shell --pane || true'`, shell, false},
		{"chord entry as written unscoped", chord.tmuxCommand(), chord, true},
		{"chord entry as it reads inside the guard",
			strings.ReplaceAll(chord.tmuxCommand(), `\;`, ";"), chord, true},
		{"chord entry with a custom hint",
			`display-message -d 2000 "my own hint" ; switch-client -T ` + tableAgent, chord, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bayWroteCommandTail(tt.tail, tt.kb); got != tt.want {
				t.Errorf("bayWroteCommandTail(%q, %s) = %v; want %v", tt.tail, tt.kb.id(), got, tt.want)
			}
		})
	}
}

func TestReplaceBindingInBlock(t *testing.T) {
	content := "set -g mouse on\n\n# Bay keybindings\n" +
		runShellBind("M-s", "bay shell --window") +
		runShellBind("M-S", "bay shell --pane") +
		"\nset -g base-index 1\n"
	kb := bayKeybinding{key: "M-s", cmd: "bay shell --pane", tmuxVerb: "run-shell"}
	got := replaceBindingInBlock(content, kb)
	want := "set -g mouse on\n\n# Bay keybindings\n" +
		runShellBind("M-s", "bay shell --pane") +
		runShellBind("M-S", "bay shell --pane") +
		"\nset -g base-index 1\n"
	if got != want {
		t.Errorf("replaceBindingInBlock:\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}

func TestReplaceBindingInBlock_NoBlock(t *testing.T) {
	content := "set -g mouse on\n"
	kb := bayKeybinding{key: "M-s", cmd: "bay shell --pane", tmuxVerb: "run-shell"}
	if got := replaceBindingInBlock(content, kb); got != content {
		t.Errorf("expected no change, got %q", got)
	}
}

func TestKeptKeys(t *testing.T) {
	block := `# Bay keybindings
# bay-keep: M-s M-e
# bay-keep:  M-a
bind-key -n M-s run-shell 'bay shell --window || true'
`
	got := keptKeys(block)
	want := map[string]bool{"M-s": true, "M-e": true, "M-a": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keptKeys() = %v, want %v", got, want)
	}
}

func TestHomeChordKeybindings(t *testing.T) {
	lines := tmuxKeybindingLines()
	joined := strings.Join(lines, "\n")

	for _, want := range []string{
		`bind-key -n M-o if -F '#{@bay-session-id}' { display-message -d 2000 "agent: c Claude, x Codex, g Antigravity, f Fable, o Opus | Shift=window | b=bay | h=home" ; switch-client -T bay-agent } { send-keys M-o }`,
		`bind-key -T bay-agent h display-message -d 2000 "home: Enter home, s shell, e editor, c Claude, x Codex, g Antigravity, f Fable, o Opus" \; switch-client -T bay-home`,
		`bind-key -T bay-home Enter run-shell 'bay home || true'`,
		`bind-key -T bay-home s run-shell 'bay shell --bay home || true'`,
		`bind-key -T bay-home e run-shell 'bay edit --bay home || true'`,
		`bind-key -T bay-home c run-shell 'bay agent claude --bay home || true'`,
		`bind-key -T bay-home x run-shell 'bay agent codex --bay home || true'`,
		`bind-key -T bay-home g run-shell 'bay agent antigravity --bay home || true'`,
		`bind-key -T bay-home f run-shell 'bay agent fable --bay home || true'`,
		`bind-key -T bay-home o run-shell 'bay agent opus --bay home || true'`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("home chord binding missing %q\nall bindings:\n%s", want, joined)
		}
	}

	prefix := findKeybinding(t, tableAgent, "h")
	if !prefix.isTmuxCommand || strings.Contains(prefix.cmd, "bay home") {
		t.Fatalf("M-o h must be prefix-only, got %+v", prefix)
	}
	if !strings.Contains(prefix.cmd, "switch-client -T "+tableHome) {
		t.Fatalf("M-o h prefix cmd = %q; want switch-client into %s", prefix.cmd, tableHome)
	}
}

func TestMissingBindings_DetectsMissingHomeEnterBinding(t *testing.T) {
	var blockLines []string
	for _, kb := range bayKeybindings {
		if kb.table == tableHome && kb.key == "Enter" {
			continue
		}
		blockLines = append(blockLines, kb.canonicalLine())
	}
	block := bayKeybindingsMarker + "\n" + strings.Join(blockLines, "\n")

	missing := missingBindings(block, bayKeybindings)
	if len(missing) != 1 || missing[0].table != tableHome || missing[0].key != "Enter" {
		t.Fatalf("missingBindings = %+v; want only %s:Enter", missing, tableHome)
	}
}

// parseBindLine has to recognize key-table bindings (`-T <table>`) so
// chord sub-tables (M-o c → bay-agent c) participate in drift
// detection alongside root bindings.
func TestParseBindLine_KeyTable(t *testing.T) {
	cases := []struct {
		line   string
		table  string
		key    string
		cmd    string
		scoped bool
	}{
		{
			line:  `bind-key -T bay-agent c run-shell 'bay agent claude --pane || true'`,
			table: "bay-agent",
			key:   "c",
			cmd:   "bay agent claude --pane || true",
		},
		{
			line:  `bind-key -T bay-agent-bay g run-shell 'bay new -q --agent=antigravity || true'`,
			table: "bay-agent-bay",
			key:   "g",
			cmd:   "bay new -q --agent=antigravity || true",
		},
		{
			line:  `bind-key -T bay-home Enter run-shell 'bay home || true'`,
			table: "bay-home",
			key:   "Enter",
			cmd:   "bay home || true",
		},
		{
			line:  `bind-key -T bay-agent b display-message -d 2000 "bay: c Claude, x Codex, g Antigravity" \; switch-client -T bay-agent-bay`,
			table: "bay-agent",
			key:   "b",
			cmd:   "bay: c Claude, x Codex, g Antigravity", // quoted region only — matches existing tmux-cmd extraction behavior
		},
		{
			line:  `bind-key -T bay-agent h display-message -d 2000 "home: Enter home, s shell, e editor, c Claude, x Codex, g Antigravity" \; switch-client -T bay-home`,
			table: "bay-agent",
			key:   "h",
			cmd:   "home: Enter home, s shell, e editor, c Claude, x Codex, g Antigravity", // quoted region only — matches existing tmux-cmd extraction behavior
		},
		{
			// The scoped root form bay writes: the command bay runs
			// in a bay-managed session reads the same as it did
			// before scoping.
			line:   `bind-key -n M-c if -F '#{@bay-session-id}' { run-shell 'bay new -q || true' } { send-keys M-c }`,
			key:    "M-c",
			cmd:    "bay new -q || true",
			scoped: true,
		},
		{
			// The same binding after a round trip through tmux,
			// which prints `if-shell` and re-quotes the arms.
			line:   `bind-key -n M-c if-shell -F "#{@bay-session-id}" "run-shell 'bay new -q || true'" "send-keys M-c"`,
			key:    "M-c",
			cmd:    "bay new -q || true",
			scoped: true,
		},
		{
			// Scoped chord entry: a tmux-command binding, so the
			// quoted hint is what comes back, as with the
			// sub-table entries above.
			line:   `bind-key -n M-o if -F '#{@bay-session-id}' { display-message -d 2000 "agent: c Claude" ; switch-client -T bay-agent } { send-keys M-o }`,
			key:    "M-o",
			cmd:    "agent: c Claude",
			scoped: true,
		},
		{
			// A user's own `if -F` binding is not bay's scoping.
			line: `bind-key -n M-c if -F '#{pane_in_mode}' { send-keys -X cancel } { run-shell 'bay new -q || true' }`,
			key:  "M-c",
			cmd:  `#{pane_in_mode}' { send-keys -X cancel } { run-shell 'bay new -q || true`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			got, ok := parseBindLine(tc.line)
			if !ok {
				t.Fatalf("parseBindLine returned ok=false")
			}
			if got.table != tc.table || got.key != tc.key || got.cmd != tc.cmd {
				t.Errorf("parseBindLine = (%q, %q, %q), want (%q, %q, %q)",
					got.table, got.key, got.cmd, tc.table, tc.key, tc.cmd)
			}
			if got.scoped != tc.scoped {
				t.Errorf("parseBindLine scoped = %v, want %v", got.scoped, tc.scoped)
			}
		})
	}
}

func TestWriteAntigravityHook(t *testing.T) {
	// Seed an unrelated named hook to prove the merge preserves it.
	path := filepath.Join(t.TempDir(), "config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	seed := `{"user-linter": {"PreInvocation": [{"type": "command", "command": "lint.sh"}]}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if err := writeAntigravityHook(path, agentReadyCommand); err != nil {
		t.Fatalf("writeAntigravityHook: %v", err)
	}
	if !antigravityHookConfigured(path) {
		t.Fatal("antigravity hook should be configured")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Real Antigravity hooks.json schema: a top-level map of named
	// hooks, each keyed by event. No "hooks" wrapper object.
	var hooks map[string]any
	if err := json.Unmarshal(data, &hooks); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := hooks["user-linter"]; !ok {
		t.Errorf("merge dropped the existing user-linter hook: %#v", hooks)
	}
	bayHook, ok := hooks[antigravityBayHookName].(map[string]any)
	if !ok {
		t.Fatalf("%s hook = %#v, want object", antigravityBayHookName, hooks[antigravityBayHookName])
	}
	stop, ok := bayHook["Stop"].([]any)
	if !ok || len(stop) != 1 {
		t.Fatalf("Stop = %#v, want one handler", bayHook["Stop"])
	}
	handler, ok := stop[0].(map[string]any)
	if !ok {
		t.Fatalf("Stop handler = %#v, want object", stop[0])
	}
	if handler["type"] != "command" || handler["command"] != agentReadyCommand {
		t.Errorf("handler = %#v, want bay command handler", handler)
	}

	// Re-running must stay idempotent (no duplicate handlers).
	if err := writeAntigravityHook(path, agentReadyCommand); err != nil {
		t.Fatalf("writeAntigravityHook (rerun): %v", err)
	}
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &hooks)
	rerun := hooks[antigravityBayHookName].(map[string]any)["Stop"].([]any)
	if len(rerun) != 1 {
		t.Errorf("Stop after rerun = %d handlers, want 1", len(rerun))
	}
}

// canonicalLine and parseBindLine should round-trip for a chord
// sub-table binding: emit, parse, recover the same identity.
func TestChordBindingRoundTrip(t *testing.T) {
	kb := bayKeybinding{
		table:    "bay-agent-bay",
		key:      "c",
		cmd:      "bay new -q --agent=claude",
		tmuxVerb: "run-shell",
	}
	line := kb.canonicalLine()
	t.Logf("canonical line: %s", line)
	got, ok := parseBindLine(line)
	if !ok {
		t.Fatalf("parseBindLine(%q) failed", line)
	}
	if got.id() != kb.id() {
		t.Errorf("round-trip id = %q, want %q", got.id(), kb.id())
	}
}

// activeBindings keys binding identities by table+key, so a chord-
// table letter doesn't collide with the same letter at the root.
// (`c` in bay-agent must be distinct from `c` if it ever appeared
// in the root table.)
func TestActiveBindings_TableKeyDistinctFromRootKey(t *testing.T) {
	block := `# Bay keybindings
bind-key -n M-c run-shell 'bay new -q || true'
bind-key -T bay-agent c run-shell 'bay agent claude --pane || true'
bind-key -T bay-agent-bay c run-shell 'bay new -q --agent=claude || true'
bind-key -T bay-home c run-shell 'bay agent claude --bay home || true'
`
	got := activeBindings(block)
	if got["M-c"].cmd != "bay new -q || true" {
		t.Errorf("M-c missing or wrong: %q", got["M-c"].cmd)
	}
	if got["bay-agent:c"].cmd != "bay agent claude --pane || true" {
		t.Errorf("bay-agent:c missing or wrong: %q", got["bay-agent:c"].cmd)
	}
	if got["bay-agent-bay:c"].cmd != "bay new -q --agent=claude || true" {
		t.Errorf("bay-agent-bay:c missing or wrong: %q", got["bay-agent-bay:c"].cmd)
	}
	if got["bay-home:c"].cmd != "bay agent claude --bay home || true" {
		t.Errorf("bay-home:c missing or wrong: %q", got["bay-home:c"].cmd)
	}
}

func findKeybinding(t *testing.T, table, key string) bayKeybinding {
	t.Helper()
	for _, kb := range bayKeybindings {
		if kb.table == table && kb.key == key {
			return kb
		}
	}
	t.Fatalf("keybinding %s not found", bindID(table, key))
	return bayKeybinding{}
}

// The scoping policy, pinned: navigation keys stay global so they work
// in any tmux session; every other root binding fires only in a
// bay-managed one and hands its key to the application everywhere
// else; chord sub-table bindings need no guard of their own because
// their entry key carries it.
func TestKeybindingScopePolicy(t *testing.T) {
	globalKeys := map[string]bool{
		"M-h": true, "M-l": true,
		"M-j": true, "M-k": true,
		"M-H": true, "M-L": true,
		"M-J": true, "M-K": true,
	}
	seenGlobal := map[string]bool{}
	for _, kb := range bayKeybindings {
		line := kb.canonicalLine()
		scopedLine := strings.Contains(line, tmux.ScopeCondition)
		switch {
		case kb.table != "":
			if kb.scoped() || scopedLine {
				t.Errorf("chord binding %s should not be scoped: %s", kb.id(), line)
			}
		case globalKeys[kb.key]:
			seenGlobal[kb.key] = true
			if kb.scoped() || scopedLine {
				t.Errorf("navigation key %s must stay global: %s", kb.id(), line)
			}
		default:
			if !kb.scoped() || !scopedLine {
				t.Errorf("bay command %s must be scoped to bay sessions: %s", kb.id(), line)
			}
			// Out of scope the key has to reach the application, or
			// bay would swallow keystrokes in sessions it doesn't own.
			if want := "{ send-keys " + kb.key + " }"; !strings.HasSuffix(line, want) {
				t.Errorf("binding %s must pass its key through out of scope, got: %s", kb.id(), line)
			}
		}
	}
	for key := range globalKeys {
		if !seenGlobal[key] {
			t.Errorf("navigation key %s is no longer in the canonical set", key)
		}
	}
}

// setupTestHome points loadTmuxConf at a scratch ~/.tmux.conf and makes
// sure the run can't reach a real tmux server: installKeybindings ends
// by offering `tmux source-file`, and a test must never fire that at
// the developer's live server.
func setupTestHome(t *testing.T, conf string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", filepath.Join(home, "no-server"))
	path := filepath.Join(home, ".tmux.conf")
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		t.Fatalf("writing tmux.conf: %v", err)
	}
	return path
}

// A block written before bay scoped its keys binds bay commands in
// every tmux session. One `bay setup` run rewrites them in place, and a
// second changes nothing.
func TestInstallKeybindings_MigratesUnscopedBlockIdempotently(t *testing.T) {
	conf := "set -g mouse on\n\n" + bayKeybindingsMarker + "\n" +
		"bind-key -n M-h previous-window\n" +
		"bind-key -n M-c run-shell 'bay new -q || true'\n" +
		"bind-key -n M-s run-shell 'bay shell --pane || true'\n" +
		"\nset -g base-index 1\n"
	path := setupTestHome(t, conf)

	// "y" updates the drifted bindings; "n" declines the reload offer.
	installKeybindings(bufio.NewReader(strings.NewReader("y\ny\nn\n")))

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading tmux.conf: %v", err)
	}
	got := string(after)
	for _, want := range []string{
		"set -g mouse on", // user's own config untouched
		"set -g base-index 1",
		"bind-key -n M-h previous-window", // navigation stays global
		findKeybinding(t, "", "M-c").canonicalLine(),
		findKeybinding(t, "", "M-s").canonicalLine(),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("migrated conf missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "bind-key -n M-c run-shell 'bay new -q || true'\n") {
		t.Errorf("unscoped M-c survived the migration:\n%s", got)
	}

	// Re-run: nothing left to reconcile, so nothing is written.
	installKeybindings(bufio.NewReader(strings.NewReader("y\ny\nn\n")))
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-reading tmux.conf: %v", err)
	}
	if string(again) != got {
		t.Errorf("second run changed the conf:\nFIRST:\n%s\nSECOND:\n%s", got, again)
	}
}

// The migration writes the file, so the guard on what it may rewrite is
// worth pinning end to end: an unscoped line carrying a command that
// isn't bay's must survive `bay setup` verbatim, with no bay-keep marker
// needed. Scope drift and command drift are separate prompts, and a
// user answering the scoping one is not consenting to lose a command.
func TestInstallKeybindings_LeavesCustomCommandsUnscoped(t *testing.T) {
	custom := []string{
		"bind-key -n M-s run-shell 'my-custom-script || true'\n",
		"bind-key -n M-p display-popup -w 80% -h 80% -E 'bay palette --split window || true'\n",
	}
	conf := bayKeybindingsMarker + "\n" + strings.Join(custom, "")
	path := setupTestHome(t, conf)

	// "y" to every prompt: the most permissive answer a user can give.
	installKeybindings(bufio.NewReader(strings.NewReader("y\ny\ny\ny\n")))

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading tmux.conf: %v", err)
	}
	for _, line := range custom {
		if !strings.Contains(string(after), line) {
			t.Errorf("custom binding %q was rewritten:\n%s", line, after)
		}
	}
}

// `# bay-keep:` is how a user says "leave this one alone" — including a
// user who wants a bay key global on purpose.
func TestInstallKeybindings_BayKeepPreservesGlobalBinding(t *testing.T) {
	unscoped := "bind-key -n M-g run-shell 'bay go --pick || true'\n"
	conf := bayKeybindingsMarker + "\n" + "# bay-keep: M-g\n" + unscoped
	path := setupTestHome(t, conf)

	installKeybindings(bufio.NewReader(strings.NewReader("y\ny\nn\n")))

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading tmux.conf: %v", err)
	}
	if !strings.Contains(string(after), unscoped) {
		t.Errorf("pinned global M-g was rewritten:\n%s", after)
	}
	if !strings.Contains(string(after), "# bay-keep: M-g") {
		t.Errorf("bay-keep marker lost:\n%s", after)
	}
}
