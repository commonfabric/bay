// Package describe is the auto-description backstop worker. It summarizes
// what a bay is working on — from the agent conversation first, with git
// state as enrichment and fallback — into the bay's description, without
// the working agent's involvement.
//
// It is conversation-first: the goal comes from the user's own prompts
// and the session-history summary (the things git doesn't capture), and
// the current stage from the agent's latest reply; git (branch, commits,
// diffstat) grounds the body and is the sole input only when no
// transcript is readable. It never overwrites a human- or agent-set
// description, and failures are non-fatal (logged, retried next cycle).
//
// See docs/design/auto-descriptions.md.
package describe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/transcript"
)

const (
	summarizeTimeout = 60 * time.Second

	// Input budgets, in bytes. The first prompt usually states the goal,
	// so it keeps more room than the follow-ups, which are often short
	// steps ("open pr") whose value is in showing the arc.
	maxFirstPromptLen = 600
	maxPromptLen      = 200
	maxPrompts        = 30
	maxSummaryLen     = 2500
	maxReplyLen       = 1500
	maxInputLen       = 12000

	// maxStatusErrorLen bounds the one-line error kept in the manifest's
	// describe status.
	maxStatusErrorLen = 300

	// maxStableStreak caps the no-op streak the worker records. The
	// monitor's backoff saturates well before this (see describeMaxInterval),
	// so it only keeps the stored count from growing without bound.
	maxStableStreak = 8
)

// Options identifies the bay a worker should describe.
type Options struct {
	Dock string
	Bay  string
}

// Summarizer turns a full prompt into a description string.
type Summarizer interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}

// Worker owns one describe run for one bay. The function/interface fields
// are overridable for tests; production defaults are wired lazily.
type Worker struct {
	Config       *config.Config
	ManifestPath string
	DataDir      string
	Now          func() time.Time

	Summarizer         Summarizer
	GatherConversation func(cwd string) transcript.Conversation
	GatherGit          func(ctx context.Context, bayPath string) GitSignal
}

// Run generates (or refreshes) the description for one bay.
func (w *Worker) Run(ctx context.Context, opts Options) error {
	if opts.Dock == "" || opts.Bay == "" {
		return fmt.Errorf("dock and bay are required")
	}
	if w.DataDir == "" {
		return fmt.Errorf("data dir is required")
	}
	if w.Config != nil && !w.Config.Describe.EffectiveEnabled() {
		return nil
	}

	// One describe worker per bay at a time.
	lock, acquired, err := tryBayLock(bayLockPath(w.DataDir, opts.Dock, opts.Bay))
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer lock.Unlock()

	m, err := manifest.Load(w.ManifestPath)
	if err != nil {
		return err
	}
	bay := findBay(m, opts)
	if bay == nil || !eligible(bay) || bay.Path == "" {
		return nil
	}
	cwd := bay.Path

	git := w.gitSignal(ctx, cwd)
	conv := currentWork(w.conversation(cwd), git.WorkStart)
	input, mode := assembleInput(conv, git)
	if mode == modeNone {
		// Nothing to summarize (no transcript, no git signal). Stamp so
		// the monitor's gate paces re-checks instead of re-probing every
		// cycle (and so this bay doesn't perpetually sort to the front of
		// the oldest-first dispatch queue). This is a genuine quiet run,
		// so it grows the backoff streak.
		return w.stampSummarized(opts, true)
	}

	hash := hashInput(input)
	if hash == bay.DescriptionInputHash {
		// Unchanged since the last run: bump the timestamp so the
		// monitor's gate backs off, skip the summarizer, and grow the
		// streak (the bay is quiet).
		return w.stampSummarized(opts, true)
	}

	// The input changed but we couldn't produce a description below. Each
	// such terminal path stamps DescriptionSummarizedAt (so a persistently
	// failing bay doesn't burn a summarizer call every cycle) but leaves
	// the streak reset: the content is real and still unsummarized, so the
	// retry stays at the base min-interval cadence rather than backing off.
	desc, err := w.summarize(ctx, input, mode)
	if err != nil {
		return w.stampFailed(opts, err)
	}
	desc = sanitizeDescription(desc)
	if desc == "" {
		return w.stampFailed(opts, fmt.Errorf("summarizer produced an empty description"))
	}
	if err := engine.ValidateDescription(desc); err != nil {
		return w.stampFailed(opts, fmt.Errorf("generated description invalid: %w", err))
	}
	return w.commit(opts, desc, hash)
}

func (w *Worker) conversation(cwd string) transcript.Conversation {
	if w.GatherConversation != nil {
		return w.GatherConversation(cwd)
	}
	return transcript.Reader{}.Gather(cwd)
}

func (w *Worker) gitSignal(ctx context.Context, bayPath string) GitSignal {
	if w.GatherGit != nil {
		return w.GatherGit(ctx, bayPath)
	}
	return gatherGitSignal(ctx, bayPath)
}

func (w *Worker) summarizer() Summarizer {
	if w.Summarizer != nil {
		return w.Summarizer
	}
	return commandSummarizer{config: w.Config}
}

func (w *Worker) summarize(ctx context.Context, input string, mode inputMode) (string, error) {
	sctx, cancel := context.WithTimeout(ctx, summarizeTimeout)
	defer cancel()
	prompt := instructionFor(mode) + "\n\n" + input
	return w.summarizer().Summarize(sctx, prompt)
}

func (w *Worker) now() int64 {
	if w.Now != nil {
		return w.Now().Unix()
	}
	return time.Now().Unix()
}

// stampSummarized records that we ran without writing a new description,
// so the monitor doesn't re-dispatch immediately. quiet=true means the
// bay was genuinely unchanged (no signal, or input identical): grow the
// stable streak so the monitor backs off re-probing it. quiet=false means
// the input changed but we failed to summarize it: reset the streak so the
// retry stays at the base cadence (the content is still unsummarized).
func (w *Worker) stampSummarized(opts Options, quiet bool) error {
	return w.stamp(opts, quiet, nil)
}

// stampFailed logs a summarizer failure, records it in the manifest's
// describe status (which `bay doctor` reads), and stamps the bay like any
// other changed-but-unsummarized run.
func (w *Worker) stampFailed(opts Options, err error) error {
	w.logErr(opts, err)
	return w.stamp(opts, false, err)
}

// stamp updates the bay's back-off fields. A non-nil failure is also
// recorded in the describe status. That is written even when the bay is no
// longer eligible: the summarizer still ran and failed.
func (w *Worker) stamp(opts Options, quiet bool, failure error) error {
	now := w.now()
	return manifest.LockedUpdateMaybe(w.ManifestPath, func(m *manifest.Manifest) (bool, error) {
		changed := false
		if failure != nil {
			m.RecordDescribeFailure(now, failureSummary(failure))
			changed = true
		}
		bay := findBay(m, opts)
		if bay == nil || !eligible(bay) {
			return changed, nil
		}
		bay.DescriptionSummarizedAt = now
		if quiet {
			if bay.DescriptionStableStreak < maxStableStreak {
				bay.DescriptionStableStreak++
			}
		} else {
			bay.DescriptionStableStreak = 0
		}
		return true, nil
	})
}

// failureSummary reduces a summarizer error to the single line stored in the
// describe status. It takes the last non-empty line, since CLIs such as codex
// print the actual error after their banner and echoed prompt.
func failureSummary(err error) string {
	lines := strings.Split(err.Error(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := oneLine(lines[i]); line != "" {
			return truncate(line, maxStatusErrorLen)
		}
	}
	return ""
}

// commit writes a generated description, re-checking eligibility under
// the lock since the user may have set one in the meantime. The summarizer
// succeeded either way, so that is recorded in the describe status first.
func (w *Worker) commit(opts Options, desc, hash string) error {
	now := w.now()
	return manifest.LockedUpdateMaybe(w.ManifestPath, func(m *manifest.Manifest) (bool, error) {
		m.RecordDescribeSuccess(now)
		bay := findBay(m, opts)
		if bay == nil || !eligible(bay) {
			return true, nil
		}
		bay.Description = desc
		bay.DescriptionSource = manifest.DescriptionSourceAuto
		bay.DescriptionSummarizedAt = now
		bay.DescriptionInputHash = hash
		bay.DescriptionStableStreak = 0 // content changed: reset to base cadence
		return true, nil
	})
}

// eligible reports whether the backstop may write this bay's description:
// only when it's empty or previously auto-generated, and never for the
// home pseudo-bay.
func eligible(bay *manifest.Bay) bool {
	if bay.ID == manifest.HomeBayID || bay.Type == manifest.BayTypeHome {
		return false
	}
	return bay.Description == "" || bay.DescriptionSource == manifest.DescriptionSourceAuto
}

func findBay(m *manifest.Manifest, opts Options) *manifest.Bay {
	dock := m.FindDock(opts.Dock)
	if dock == nil {
		return nil
	}
	return dock.FindBayByID(opts.Bay)
}

// --- input assembly ---

type inputMode int

const (
	modeNone inputMode = iota
	modeConversation
	modeGitOnly
)

// currentWork drops conversation from before the current work began
// (the last-merge boundary), so a bay reused after a merged PR describes
// its new focus, not the old task. If the boundary is unknown or
// excluding would empty the conversation, all of it is kept. Undated
// messages are kept.
func currentWork(conv transcript.Conversation, since time.Time) transcript.Conversation {
	if since.IsZero() {
		return conv
	}
	current := func(p transcript.Prompt) bool {
		return p.Timestamp.IsZero() || !p.Timestamp.Before(since)
	}
	var kept transcript.Conversation
	for _, p := range conv.Prompts {
		if current(p) {
			kept.Prompts = append(kept.Prompts, p)
		}
	}
	if current(conv.Summary) {
		kept.Summary = conv.Summary
	}
	if current(conv.LastReply) {
		kept.LastReply = conv.LastReply
	}
	if kept.IsEmpty() {
		return conv
	}
	return kept
}

// selectPrompts caps the prompt list at maxPrompts, keeping the first
// (which usually states the goal) and the most recent, and reports how
// many were omitted between them.
func selectPrompts(prompts []transcript.Prompt) ([]transcript.Prompt, int) {
	if len(prompts) <= maxPrompts {
		return prompts, 0
	}
	omitted := len(prompts) - maxPrompts
	kept := append([]transcript.Prompt{prompts[0]}, prompts[len(prompts)-(maxPrompts-1):]...)
	return kept, omitted
}

func assembleInput(conv transcript.Conversation, git GitSignal) (string, inputMode) {
	var b strings.Builder
	mode := modeNone

	if !conv.IsEmpty() {
		mode = modeConversation
	}
	if conv.Summary.Text != "" {
		b.WriteString("=== Session history (written by the agent when the conversation was compacted) ===\n")
		b.WriteString(truncate(conv.Summary.Text, maxSummaryLen))
		b.WriteString("\n\n")
	}
	if len(conv.Prompts) > 0 {
		prompts, omitted := selectPrompts(conv.Prompts)
		b.WriteString("=== The user's requests, oldest first (the user's own words) ===\n")
		for i, p := range prompts {
			limit := maxPromptLen
			if i == 0 {
				limit = maxFirstPromptLen
			}
			b.WriteString("- ")
			b.WriteString(truncate(oneLine(p.Text), limit))
			b.WriteString("\n")
			if i == 0 && omitted > 0 {
				fmt.Fprintf(&b, "- [%d earlier requests omitted]\n", omitted)
			}
		}
		b.WriteString("\n")
	}
	if conv.LastReply.Text != "" {
		b.WriteString("=== Where things stand (the agent's latest reply) ===\n")
		b.WriteString(truncate(conv.LastReply.Text, maxReplyLen))
		b.WriteString("\n\n")
	}

	if git.hasContent() {
		if mode == modeNone {
			mode = modeGitOnly
		}
		b.WriteString("=== Git state ===\n")
		b.WriteString(git.String())
	}

	if mode == modeNone {
		return "", modeNone
	}
	return truncate(b.String(), maxInputLen), mode
}

// baseInstruction asks for a status card that serves a developer
// returning to the workspace: the purpose of the whole effort, the stage
// it has reached, and what they need to know to continue. The emphasis
// on the overall arc counters the pull of the latest request, which is
// often a short step like "open pr".
const baseInstruction = `You are writing the status card for a developer workspace. The developer switches between many workspaces and reads this card after time away to recall (1) what the whole effort is for, (2) what stage it has reached, and (3) what they need to know to continue or finish it.

Line 1: the purpose of the entire effort, as a short imperative label (at most 80 characters, no trailing period). Short follow-ups like "open pr", "split it", or "continue" are steps, not the goal.

Then a blank line and 2-4 short lines, with no blank lines between them:
- Stage: where the work stands now (for example designing, implementing, PR #N in review, merged and cleaning up), with concrete names such as PR numbers, features, and files.
- Next: the next step, or what is blocked and on whom (a decision the developer owes, a pending review, a failing test).
- Optionally, one decision or caveat the developer would need before resuming.

Use only what the input supports; omit a line rather than guess. Don't narrate individual tool actions. Output ONLY the card, with no preamble, quotes, or code fences.`

func instructionFor(mode inputMode) string {
	if mode == modeGitOnly {
		return baseInstruction + "\n\nNo conversation is available: base the card on the branch name, commit subjects, and changed files provided, and omit Next unless the commits make it clear."
	}
	return baseInstruction + "\n\nTake the purpose from the session history, the first request, and the overall arc of the requests, not from the latest request. Take the stage mainly from the agent's latest reply; use the git state to ground it."
}

// --- description sanitation ---

func sanitizeDescription(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`")
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\t", " ")
	if s == "" {
		return ""
	}
	// Clamp the first line, then the whole thing, to the manifest limits.
	first := engine.DescriptionFirstLine(s)
	if len(first) > engine.MaxDescriptionFirstLineLen {
		clamped := truncateWords(first, engine.MaxDescriptionFirstLineLen)
		rest := ""
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			rest = s[i:]
		}
		s = clamped + rest
	}
	if len(s) > engine.MaxDescriptionLen {
		s = truncate(s, engine.MaxDescriptionLen)
	}
	return strings.TrimSpace(s)
}

// truncate returns at most maxBytes of s, cut on a rune boundary and
// space-trimmed. It drops a trailing partial rune — both orphaned
// continuation bytes and a lone leading byte whose continuations were
// cut off — so the result is always valid UTF-8.
func truncate(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	b := s[:maxBytes]
	for len(b) > 0 {
		// DecodeLastRune reports (RuneError, 1) for an incomplete or
		// invalid trailing rune; a genuine encoded U+FFFD has size 3.
		if r, size := utf8.DecodeLastRuneInString(b); r == utf8.RuneError && size <= 1 {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return strings.TrimSpace(b)
}

// truncateWords clamps to ≤ maxBytes, preferring to cut at the last
// space in the latter half so the label doesn't end mid-word.
func truncateWords(s string, maxBytes int) string {
	cut := truncate(s, maxBytes)
	if i := strings.LastIndexByte(cut, ' '); i > maxBytes/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func hashInput(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (w *Worker) logErr(opts Options, err error) {
	if w.DataDir == "" {
		return
	}
	dir := filepath.Join(w.DataDir, "logs")
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(filepath.Join(dir, "describe.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if openErr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s dock=%s bay=%s: %v\n", time.Now().Format(time.RFC3339), opts.Dock, opts.Bay, err)
}

// --- per-bay lock ---

type bayLock struct{ f *os.File }

func tryBayLock(path string) (*bayLock, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, fmt.Errorf("creating describe lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("opening describe lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("acquiring describe lock: %w", err)
	}
	return &bayLock{f: f}, true, nil
}

func (l *bayLock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
}

func bayLockPath(dataDir, dock, bay string) string {
	return filepath.Join(dataDir, "locks", "describe", dock, bay+".lock")
}
