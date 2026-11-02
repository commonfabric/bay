package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/manifest"
)

func TestFormatBayShort_AllFields(t *testing.T) {
	bay := &manifest.Bay{
		Name:        "auth-fix",
		Path:        "/tmp/bay-worktrees/b4",
		Description: "Login flow fixes",
		Worktree: &manifest.WorktreeAttrs{
			Branch: "fix/login",
			PR:     "123",
		},
	}
	got := stripANSI(formatBayShort(bay))
	want := "b4.auth-fix — Login flow fixes — fix/login — PR#123"
	if got != want {
		t.Errorf("formatBayShort = %q, want %q", got, want)
	}
}

func TestRunDescribeRejectsHomeReadOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	oldCfgPath := cfgPath
	cfgPath = ""
	t.Cleanup(func() { cfgPath = oldCfgPath })

	repoDir := filepath.Join(dir, "repos", "labs")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	manifestPath := filepath.Join(dir, "data", "bay", "manifest.json")
	if err := manifest.Save(manifestPath, &manifest.Manifest{
		Version: manifest.CurrentVersion,
		Docks: []manifest.Dock{
			{Name: "labs", Path: repoDir, Bays: []manifest.Bay{}},
		},
	}); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	for _, arg := range []string{manifest.HomeBayID, "labs:" + manifest.HomeBayID} {
		err := runDescribe([]string{arg}, "", false, false)
		if err == nil || !strings.Contains(err.Error(), "describe is not supported") {
			t.Fatalf("runDescribe(%q) error = %v, want home describe rejection", arg, err)
		}
	}
}

func TestFormatBayShort_SkipsEmptyFields(t *testing.T) {
	cases := []struct {
		name string
		bay  *manifest.Bay
		want string
	}{
		{
			name: "name only",
			bay:  &manifest.Bay{Name: "bay1"},
			want: "bay1",
		},
		{
			name: "no description, has branch+pr",
			bay: &manifest.Bay{
				Name:     "bay1",
				Worktree: &manifest.WorktreeAttrs{Branch: "main", PR: "42"},
			},
			want: "bay1 — main — PR#42",
		},
		{
			name: "description, no branch",
			bay: &manifest.Bay{
				Name:        "bay1",
				Description: "cleanup",
			},
			want: "bay1 — cleanup",
		},
		{
			name: "description + branch, no pr",
			bay: &manifest.Bay{
				Name:        "bay1",
				Description: "cleanup",
				Worktree:    &manifest.WorktreeAttrs{Branch: "cleanup-branch"},
			},
			want: "bay1 — cleanup — cleanup-branch",
		},
		{
			name: "non-worktree bay (no Worktree)",
			bay: &manifest.Bay{
				Name:        "external",
				Description: "external dir",
			},
			want: "external — external dir",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stripANSI(formatBayShort(c.bay))
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestFormatBayShort_NilBay(t *testing.T) {
	if got := formatBayShort(nil); got != "" {
		t.Errorf("nil bay should produce empty string, got %q", got)
	}
}

func TestFormatBayShort_SilencesBranchWhenEqualToName(t *testing.T) {
	// bay auto-derives bay names from branches; when they match,
	// showing both duplicates the identifier.
	bay := &manifest.Bay{
		Name:        "auth-fix",
		Description: "Login flow fixes",
		Worktree:    &manifest.WorktreeAttrs{Branch: "auth-fix", PR: "123"},
	}
	got := stripANSI(formatBayShort(bay))
	want := "auth-fix — Login flow fixes — PR#123"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatBayShort_SeparatorsAreDimmed(t *testing.T) {
	// The raw output should contain ANSI-dimmed separators; stripping
	// them yields the plain-text form used by the M-/ flash.
	bay := &manifest.Bay{
		Name:        "bay1",
		Description: "desc",
	}
	raw := formatBayShort(bay)
	if !strings.Contains(raw, "\x1b[") {
		t.Errorf("expected ANSI codes in raw output, got %q", raw)
	}
	if stripANSI(raw) != "bay1 — desc" {
		t.Errorf("stripped output mismatch: got %q", stripANSI(raw))
	}
}

func TestFormatBayShort_TruncatesMultiLineDescription(t *testing.T) {
	// --flash must render on a single tmux status row. Any body below the
	// first line must be dropped; only the label participates.
	bay := &manifest.Bay{
		Name:        "auth-fix",
		Description: "Login flow fixes\n\nhit rebase conflict on helper.ts\ntests green",
		Worktree:    &manifest.WorktreeAttrs{Branch: "fix/login"},
	}
	got := stripANSI(formatBayShort(bay))
	want := "auth-fix — Login flow fixes — fix/login"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildPopupContentIncludesCompactLabelAndPath(t *testing.T) {
	bay := &manifest.Bay{
		Name:        "auth-fix",
		Path:        "/tmp/bay-worktrees/b4",
		Description: "Login flow fixes",
	}
	got := stripANSI(buildPopupContent(bay, 80, 10))
	if !strings.Contains(got, "b4.auth-fix") {
		t.Errorf("popup missing compact label:\n%s", got)
	}
	if !strings.Contains(got, "/tmp/bay-worktrees/b4") {
		t.Errorf("popup missing path:\n%s", got)
	}
}

func TestClipBody(t *testing.T) {
	// clipHint returns the dim-wrapped truncation hint with a specific count,
	// matching what clipBody appends. Centralizing keeps tests in lockstep
	// with the production format string.
	clipHint := func(n int) string {
		return dim(fmt.Sprintf("(+%d more lines — run `bay describe` for full)", n))
	}
	cases := []struct {
		name        string
		body        string
		termHeight  int
		headerLines int
		want        string
	}{
		{
			name:        "zero height returns body unchanged",
			body:        "line 1\nline 2\nline 3",
			termHeight:  0,
			headerLines: 1,
			want:        "line 1\nline 2\nline 3",
		},
		{
			name:        "body fits in budget is unchanged",
			body:        "a\nb\nc",
			termHeight:  10, // 10 - 1 header - 2 reserved = 7 budget
			headerLines: 1,
			want:        "a\nb\nc",
		},
		{
			name:        "body overflows is truncated with counted hint",
			body:        "1\n2\n3\n4\n5\n6\n7\n8",
			termHeight:  7, // 7 - 1 - 2 = 4 budget; keep 3 + hint (8-3=5 hidden)
			headerLines: 1,
			want:        "1\n2\n3\n" + clipHint(5),
		},
		{
			name:        "two-line header reduces budget",
			body:        "1\n2\n3\n4\n5",
			termHeight:  7, // 7 - 2 - 2 = 3 budget; keep 2 + hint (5-2=3 hidden)
			headerLines: 2,
			want:        "1\n2\n" + clipHint(3),
		},
		{
			name:        "tiny height still shows at least one body line plus hint",
			body:        "a\nb\nc",
			termHeight:  3, // budget clamps to 1, keep clamps to 1 → 1 body line + hint
			headerLines: 1,
			want:        "a\n" + clipHint(2),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clipBody(c.body, c.termHeight, c.headerLines)
			if got != c.want {
				t.Errorf("clipBody =\n%q\nwant:\n%q", got, c.want)
			}
		})
	}
}

func TestWrapText(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{
			name:  "short line unchanged",
			in:    "hello world",
			width: 40,
			want:  "hello world",
		},
		{
			name:  "long line wraps on word boundary",
			in:    "the quick brown fox jumps over the lazy dog",
			width: 20,
			want:  "the quick brown fox\njumps over the lazy\ndog",
		},
		{
			name:  "blank lines preserved",
			in:    "para one\n\npara two",
			width: 40,
			want:  "para one\n\npara two",
		},
		{
			name:  "explicit line breaks preserved (not reflowed)",
			in:    "- bullet one\n- bullet two\n- bullet three",
			width: 80,
			want:  "- bullet one\n- bullet two\n- bullet three",
		},
		{
			name:  "indented continuation lines keep indent",
			in:    "  indented text that wraps across multiple visual lines",
			width: 20,
			want:  "  indented text that\n  wraps across\n  multiple visual\n  lines",
		},
		{
			name:  "word longer than width stays unbroken",
			in:    "short https://example.com/really/long/url/path more",
			width: 20,
			want:  "short\nhttps://example.com/really/long/url/path\nmore",
		},
		{
			name:  "zero width is pass-through",
			in:    "unchanged\nat\nzero",
			width: 0,
			want:  "unchanged\nat\nzero",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := wrapText(c.in, c.width)
			if got != c.want {
				t.Errorf("wrapText(%q, %d) =\n%q\nwant:\n%q", c.in, c.width, got, c.want)
			}
		})
	}
}
