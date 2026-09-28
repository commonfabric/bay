package describe

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/transcript"
)

type stubSummarizer struct {
	calls  int
	out    string
	err    error
	prompt string
}

func (s *stubSummarizer) Summarize(_ context.Context, prompt string) (string, error) {
	s.calls++
	s.prompt = prompt
	return s.out, s.err
}

func writeManifest(t *testing.T, path string, bay manifest.Bay) {
	t.Helper()
	m := manifest.New()
	m.Docks = append(m.Docks, manifest.Dock{Name: "labs", Path: "/repo", Bays: []manifest.Bay{bay}})
	if err := manifest.Save(path, m); err != nil {
		t.Fatal(err)
	}
}

func loadBay(t *testing.T, path string) manifest.Bay {
	t.Helper()
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d := m.FindDock("labs")
	if d == nil {
		t.Fatal("dock missing")
	}
	b := d.FindBayByID("b1")
	if b == nil {
		t.Fatal("bay missing")
	}
	return *b
}

func loadDescribeStatus(t *testing.T, path string) manifest.DescribeStatus {
	t.Helper()
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Describe == nil {
		t.Fatal("manifest has no describe status")
	}
	return *m.Describe
}

func newWorker(t *testing.T, manifestPath string, sum Summarizer, prompts []transcript.Prompt, git GitSignal) *Worker {
	t.Helper()
	return &Worker{
		ManifestPath:  manifestPath,
		DataDir:       t.TempDir(),
		Now:           func() time.Time { return time.Unix(1000, 0) },
		Summarizer:    sum,
		GatherPrompts: func(string) []transcript.Prompt { return prompts },
		GatherGit:     func(context.Context, string) GitSignal { return git },
	}
}

func eligibleBay() manifest.Bay {
	return manifest.Bay{ID: "b1", Type: manifest.BayTypeWorktree, Path: "/work/proj"}
}

func TestGeneratesFromConversation(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest(t, mp, eligibleBay())
	sum := &stubSummarizer{out: "Fix the auth bug\n\nWorking on token refresh."}
	w := newWorker(t, mp, sum, []transcript.Prompt{{Text: "fix the auth bug"}}, GitSignal{Branch: "fix/auth"})

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if sum.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", sum.calls)
	}
	if !strings.Contains(sum.prompt, "Base the goal on the user's requests") {
		t.Errorf("expected conversation-mode instruction in prompt, got %q", sum.prompt)
	}
	got := loadBay(t, mp)
	if got.Description != "Fix the auth bug\n\nWorking on token refresh." {
		t.Errorf("description = %q", got.Description)
	}
	if got.DescriptionSource != manifest.DescriptionSourceAuto {
		t.Errorf("source = %q, want auto", got.DescriptionSource)
	}
	if got.DescriptionInputHash == "" {
		t.Error("input hash not stored")
	}
	if got.DescriptionSummarizedAt != 1000 {
		t.Errorf("summarizedAt = %d, want 1000", got.DescriptionSummarizedAt)
	}
}

func TestGitOnlyFallback(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest(t, mp, eligibleBay())
	sum := &stubSummarizer{out: "Build feature X"}
	w := newWorker(t, mp, sum, nil, GitSignal{Branch: "feature/x", Commits: []string{"add x"}, DiffStat: "a.go | 2 +-"})

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if sum.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", sum.calls)
	}
	if !strings.Contains(sum.prompt, "Base it on the branch name") {
		t.Errorf("expected git-only instruction in prompt, got %q", sum.prompt)
	}
	if got := loadBay(t, mp); got.Description != "Build feature X" {
		t.Errorf("description = %q", got.Description)
	}
}

func TestNoSignalNoOp(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest(t, mp, eligibleBay())
	sum := &stubSummarizer{out: "should not be used"}
	w := newWorker(t, mp, sum, nil, GitSignal{})

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if sum.calls != 0 {
		t.Errorf("summarizer called %d times, want 0", sum.calls)
	}
	got := loadBay(t, mp)
	if got.Description != "" {
		t.Errorf("description = %q, want empty", got.Description)
	}
	// Stamped so the monitor backs off instead of re-probing every cycle.
	if got.DescriptionSummarizedAt != 1000 {
		t.Errorf("summarizedAt = %d, want 1000 (stamped on no-signal)", got.DescriptionSummarizedAt)
	}
	// Each no-op grows the stable streak so the monitor backs off.
	if got.DescriptionStableStreak != 1 {
		t.Errorf("stable streak = %d, want 1 after a no-signal run", got.DescriptionStableStreak)
	}
}

func TestStampsOnSummarizerFailure(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	bay := eligibleBay()
	bay.DescriptionStableStreak = 3 // had been quiet before this changed input
	writeManifest(t, mp, bay)
	sum := &stubSummarizer{err: errors.New("boom")}
	w := newWorker(t, mp, sum, []transcript.Prompt{{Text: "goal"}}, GitSignal{Branch: "x"})

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	got := loadBay(t, mp)
	if got.Description != "" {
		t.Errorf("description = %q, want empty on failure", got.Description)
	}
	if got.DescriptionSummarizedAt != 1000 {
		t.Errorf("summarizedAt = %d, want 1000 (stamped on failure for backoff)", got.DescriptionSummarizedAt)
	}
	// The input changed but stayed unsummarized: reset to base cadence, do
	// not grow the streak (which would back off a bay with real new work).
	if got.DescriptionStableStreak != 0 {
		t.Errorf("stable streak = %d, want 0 (reset on a failed-but-changed run)", got.DescriptionStableStreak)
	}
	st := loadDescribeStatus(t, mp)
	want := manifest.DescribeStatus{ConsecutiveFailures: 1, FailingSince: 1000, LastFailureAt: 1000, LastError: "boom"}
	if st != want {
		t.Errorf("describe status = %+v, want %+v", st, want)
	}
}

func TestStampsOnEmptyDescription(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest(t, mp, eligibleBay())
	sum := &stubSummarizer{out: "   \n  "} // sanitizes to empty
	w := newWorker(t, mp, sum, []transcript.Prompt{{Text: "goal"}}, GitSignal{Branch: "x"})

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	got := loadBay(t, mp)
	if got.Description != "" {
		t.Errorf("description = %q, want empty", got.Description)
	}
	if got.DescriptionSummarizedAt != 1000 {
		t.Errorf("summarizedAt = %d, want 1000 (stamped on empty for backoff)", got.DescriptionSummarizedAt)
	}
	if st := loadDescribeStatus(t, mp); st.ConsecutiveFailures != 1 || st.LastError != "summarizer produced an empty description" {
		t.Errorf("describe status = %+v, want one failure for the empty description", st)
	}
}

func TestWontClobberUser(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	bay := eligibleBay()
	bay.Description = "My manual brief"
	bay.DescriptionSource = manifest.DescriptionSourceUser
	writeManifest(t, mp, bay)
	sum := &stubSummarizer{out: "Auto desc"}
	w := newWorker(t, mp, sum, []transcript.Prompt{{Text: "new goal"}}, GitSignal{Branch: "x"})

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if sum.calls != 0 {
		t.Errorf("summarizer called %d times on a user-set description, want 0", sum.calls)
	}
	if got := loadBay(t, mp); got.Description != "My manual brief" {
		t.Errorf("description = %q, want unchanged", got.Description)
	}
}

func TestWontClobberLegacy(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	bay := eligibleBay()
	bay.Description = "legacy description" // non-empty, empty source
	writeManifest(t, mp, bay)
	sum := &stubSummarizer{out: "Auto desc"}
	w := newWorker(t, mp, sum, []transcript.Prompt{{Text: "goal"}}, GitSignal{})

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if sum.calls != 0 {
		t.Errorf("summarizer called %d times on a legacy description, want 0", sum.calls)
	}
	if got := loadBay(t, mp); got.Description != "legacy description" {
		t.Errorf("description = %q, want unchanged", got.Description)
	}
}

func TestRefreshesAuto(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	bay := eligibleBay()
	bay.Description = "old auto"
	bay.DescriptionSource = manifest.DescriptionSourceAuto
	bay.DescriptionInputHash = "sha256:stale"
	bay.DescriptionStableStreak = 4 // had been quiet; a real change resets it
	writeManifest(t, mp, bay)
	sum := &stubSummarizer{out: "New goal"}
	w := newWorker(t, mp, sum, []transcript.Prompt{{Text: "changed goal"}}, GitSignal{})

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if sum.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", sum.calls)
	}
	got := loadBay(t, mp)
	if got.Description != "New goal" {
		t.Errorf("description = %q, want refreshed", got.Description)
	}
	if got.DescriptionStableStreak != 0 {
		t.Errorf("stable streak = %d, want 0 (reset on a real change)", got.DescriptionStableStreak)
	}
}

func TestInputHashSkip(t *testing.T) {
	prompts := []transcript.Prompt{{Text: "stable goal"}}
	git := GitSignal{Branch: "x"}
	input, _ := assembleInput(prompts, git)
	hash := hashInput(input)

	mp := filepath.Join(t.TempDir(), "manifest.json")
	bay := eligibleBay()
	bay.Description = "existing auto"
	bay.DescriptionSource = manifest.DescriptionSourceAuto
	bay.DescriptionInputHash = hash
	bay.DescriptionStableStreak = 2
	writeManifest(t, mp, bay)
	sum := &stubSummarizer{out: "should not be called"}
	w := newWorker(t, mp, sum, prompts, git)

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if sum.calls != 0 {
		t.Errorf("summarizer called %d times on unchanged input, want 0", sum.calls)
	}
	got := loadBay(t, mp)
	if got.Description != "existing auto" {
		t.Errorf("description = %q, want unchanged", got.Description)
	}
	if got.DescriptionSummarizedAt != 1000 {
		t.Errorf("summarizedAt = %d, want bumped to 1000", got.DescriptionSummarizedAt)
	}
	if got.DescriptionStableStreak != 3 {
		t.Errorf("stable streak = %d, want 3 (incremented on no-op)", got.DescriptionStableStreak)
	}
}

func TestStableStreakCaps(t *testing.T) {
	prompts := []transcript.Prompt{{Text: "stable goal"}}
	git := GitSignal{Branch: "x"}
	input, _ := assembleInput(prompts, git)

	mp := filepath.Join(t.TempDir(), "manifest.json")
	bay := eligibleBay()
	bay.Description = "existing auto"
	bay.DescriptionSource = manifest.DescriptionSourceAuto
	bay.DescriptionInputHash = hashInput(input)
	bay.DescriptionStableStreak = maxStableStreak
	writeManifest(t, mp, bay)
	w := newWorker(t, mp, &stubSummarizer{}, prompts, git)

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if got := loadBay(t, mp); got.DescriptionStableStreak != maxStableStreak {
		t.Errorf("stable streak = %d, want capped at %d", got.DescriptionStableStreak, maxStableStreak)
	}
}

func TestDefaultDisabled(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest(t, mp, eligibleBay())
	sum := &stubSummarizer{out: "Auto desc"}
	w := newWorker(t, mp, sum, []transcript.Prompt{{Text: "goal"}}, GitSignal{})
	w.Config = &config.Config{} // describe unset → off by default

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if sum.calls != 0 {
		t.Errorf("summarizer called %d times with backstop unset, want 0 (opt-in)", sum.calls)
	}
}

func TestDisabled(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest(t, mp, eligibleBay())
	sum := &stubSummarizer{out: "Auto desc"}
	w := newWorker(t, mp, sum, []transcript.Prompt{{Text: "goal"}}, GitSignal{})
	disabled := false
	w.Config = &config.Config{Describe: config.DescribeConfig{Enabled: &disabled}}

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if sum.calls != 0 {
		t.Errorf("summarizer called %d times while disabled, want 0", sum.calls)
	}
}

func TestLockPreventsDoubleRun(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest(t, mp, eligibleBay())
	sum := &stubSummarizer{out: "Auto desc"}
	w := newWorker(t, mp, sum, []transcript.Prompt{{Text: "goal"}}, GitSignal{})

	// Hold the per-bay lock; Run should no-op.
	lock, acquired, err := tryBayLock(bayLockPath(w.DataDir, "labs", "b1"))
	if err != nil || !acquired {
		t.Fatalf("could not acquire lock: acquired=%v err=%v", acquired, err)
	}
	defer lock.Unlock()

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if sum.calls != 0 {
		t.Errorf("summarizer called %d times while lock held, want 0", sum.calls)
	}
}

func TestRecentPrompts(t *testing.T) {
	boundary := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	before := transcript.Prompt{Timestamp: boundary.Add(-time.Hour), Text: "old"}
	after := transcript.Prompt{Timestamp: boundary.Add(time.Hour), Text: "new"}
	undated := transcript.Prompt{Text: "undated"}

	// Zero boundary → no filtering.
	if got := recentPrompts([]transcript.Prompt{before, after}, time.Time{}); len(got) != 2 {
		t.Errorf("zero boundary kept %d, want 2", len(got))
	}
	// Drops pre-boundary, keeps post and undated.
	got := recentPrompts([]transcript.Prompt{before, after, undated}, boundary)
	if len(got) != 2 || got[0].Text != "new" || got[1].Text != "undated" {
		t.Errorf("filter = %+v, want [new, undated]", got)
	}
	// All before boundary → fallback to all.
	if got := recentPrompts([]transcript.Prompt{before}, boundary); len(got) != 1 {
		t.Errorf("all-before fallback kept %d, want 1 (keep all)", len(got))
	}
}

func TestExcludesPreMergeWork(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest(t, mp, eligibleBay())
	sum := &stubSummarizer{out: "Current focus"}
	boundary := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	prompts := []transcript.Prompt{
		{Timestamp: boundary.Add(-time.Hour), Text: "OLD pre-merge task"},
		{Timestamp: boundary.Add(time.Hour), Text: "NEW current task"},
	}
	w := newWorker(t, mp, sum, prompts, GitSignal{Branch: "feat", WorkStart: boundary})

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	if sum.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", sum.calls)
	}
	if strings.Contains(sum.prompt, "OLD pre-merge task") {
		t.Error("prompt included pre-merge work that should be excluded")
	}
	if !strings.Contains(sum.prompt, "NEW current task") {
		t.Error("prompt missing the current task")
	}
}

func TestTruncateValidUTF8(t *testing.T) {
	// A cut landing in the middle of a multi-byte rune must not leave
	// invalid UTF-8. Cases: boundary after the leading byte, and after a
	// continuation byte.
	cases := []struct {
		s   string
		max int
	}{
		{strings.Repeat("a", 79) + "é", 80}, // cut after é's leading byte (0xC3)
		{strings.Repeat("a", 78) + "€", 80}, // 3-byte rune, cut mid-rune
		{strings.Repeat("a", 79) + "😀", 81}, // 4-byte rune, cut mid-rune
		{"日本語のテスト", 8},                      // all multibyte
	}
	for _, c := range cases {
		got := truncate(c.s, c.max)
		if !utf8.ValidString(got) {
			t.Errorf("truncate(%q, %d) = %q: invalid UTF-8", c.s, c.max, got)
		}
		if len(got) > c.max {
			t.Errorf("truncate(%q, %d) = %d bytes, exceeds max", c.s, c.max, len(got))
		}
	}
	// Non-positive bounds return "" rather than panicking on a negative slice.
	if got := truncate("hello", -1); got != "" {
		t.Errorf("truncate(_, -1) = %q, want empty", got)
	}
	if got := truncate("hello", 0); got != "" {
		t.Errorf("truncate(_, 0) = %q, want empty", got)
	}
}

func TestSanitizeDescription(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := sanitizeDescription(long)
	first := strings.SplitN(got, "\n", 2)[0]
	if len(first) > 80 {
		t.Errorf("first line len = %d, want <= 80", len(first))
	}

	if got := sanitizeDescription("`\"Quoted goal\"`"); got != "Quoted goal" {
		t.Errorf("sanitize stripping = %q, want %q", got, "Quoted goal")
	}
	if got := sanitizeDescription("has\ttab\tand\rcr"); strings.ContainsAny(got, "\t\r") {
		t.Errorf("sanitize left control chars: %q", got)
	}

	huge := strings.Repeat("a ", 2000)
	if got := sanitizeDescription("short first line\n\n" + huge); len(got) > 2000 {
		t.Errorf("total len = %d, want <= 2000", len(got))
	}
}

func TestDescribeStatusFailureStreakClearedBySuccess(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest(t, mp, eligibleBay())
	sum := &stubSummarizer{err: errors.New("line one\nERROR: model not supported")}
	w := newWorker(t, mp, sum, []transcript.Prompt{{Text: "goal"}}, GitSignal{Branch: "x"})
	opts := Options{Dock: "labs", Bay: "b1"}

	for range 2 {
		if err := w.Run(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
	}
	st := loadDescribeStatus(t, mp)
	if st.ConsecutiveFailures != 2 || st.LastError != "ERROR: model not supported" {
		t.Fatalf("after two failures = %+v, want 2 failures with the last line as the error", st)
	}

	sum.err, sum.out = nil, "Ship the feature"
	if err := w.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	st = loadDescribeStatus(t, mp)
	if st != (manifest.DescribeStatus{LastSuccessAt: 1000}) {
		t.Errorf("after success = %+v, want failures cleared and LastSuccessAt=1000", st)
	}
}

func TestQuietRunsLeaveDescribeStatusAlone(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	writeManifest(t, mp, eligibleBay())
	sum := &stubSummarizer{out: "unused"}
	// No transcript and no git signal: the worker never calls the summarizer.
	w := newWorker(t, mp, sum, nil, GitSignal{})

	if err := w.Run(context.Background(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(mp)
	if err != nil {
		t.Fatal(err)
	}
	if m.Describe != nil {
		t.Errorf("describe status = %+v after a run that skipped the summarizer, want nil", m.Describe)
	}
}

func TestStampFailedRecordsStatusForIneligibleBay(t *testing.T) {
	mp := filepath.Join(t.TempDir(), "manifest.json")
	bay := eligibleBay()
	bay.Description = "written by hand"
	bay.DescriptionSource = manifest.DescriptionSourceUser
	writeManifest(t, mp, bay)
	w := newWorker(t, mp, &stubSummarizer{}, nil, GitSignal{})

	// The user described the bay while the summarizer was running. The
	// summarizer still failed, so that is recorded, but the bay is untouched.
	if err := w.stampFailed(Options{Dock: "labs", Bay: "b1"}, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if st := loadDescribeStatus(t, mp); st.ConsecutiveFailures != 1 || st.LastError != "boom" {
		t.Errorf("describe status = %+v, want one recorded failure", st)
	}
	got := loadBay(t, mp)
	if got.Description != "written by hand" || got.DescriptionSummarizedAt != 0 {
		t.Errorf("bay changed: description=%q summarizedAt=%d", got.Description, got.DescriptionSummarizedAt)
	}
}

func TestFailureSummary(t *testing.T) {
	long := strings.Repeat("x", maxStatusErrorLen+50)
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"single line", errors.New("boom"), "boom"},
		{"last line wins", errors.New("codex: exit status 1: banner\nprompt text\nERROR: bad model"), "ERROR: bad model"},
		{"skips trailing blanks", errors.New("first\nsecond\n\n  \n"), "second"},
		{"collapses whitespace", errors.New("a   b\t c"), "a b c"},
		{"truncates", errors.New(long), long[:maxStatusErrorLen]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := failureSummary(tt.err); got != tt.want {
				t.Errorf("failureSummary = %q, want %q", got, tt.want)
			}
		})
	}
}
