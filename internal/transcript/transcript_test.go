package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines []string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEncodeClaudeDir(t *testing.T) {
	cases := map[string]string{
		"/Users/mike/projects/bay-worktrees/b2": "-Users-mike-projects-bay-worktrees-b2",
		"/work/proj":                            "-work-proj",
		"/a/b.c_d e":                            "-a-b-c-d-e",
	}
	for in, want := range cases {
		if got := encodeClaudeDir(in); got != want {
			t.Errorf("encodeClaudeDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGather exercises both readers, their filters, newest-session
// selection, cwd matching, and the merge-by-timestamp.
func TestGather(t *testing.T) {
	claudeRoot := t.TempDir()
	codexRoot := t.TempDir()
	const cwd = "/work/proj"
	const encoded = "-work-proj"

	// Older Claude session — ignored (older mtime).
	writeLines(t, filepath.Join(claudeRoot, encoded, "old.jsonl"), []string{
		`{"type":"user","timestamp":"2026-06-01T09:00:00Z","message":{"content":"OLD claude ignore me"}}`,
	}, time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC))

	// Newer Claude session — the one we read. Only the plain typed
	// prompt should survive the filters.
	writeLines(t, filepath.Join(claudeRoot, encoded, "new.jsonl"), []string{
		`{"type":"user","isMeta":true,"timestamp":"2026-06-02T10:01:00Z","message":{"content":"<local-command-caveat>meta</local-command-caveat>"}}`,
		`{"type":"user","isSidechain":true,"timestamp":"2026-06-02T10:02:00Z","message":{"content":"sidechain prompt"}}`,
		`{"type":"user","timestamp":"2026-06-02T10:03:00Z","message":{"content":"<bash-input>b ls</bash-input>"}}`,
		`{"type":"assistant","timestamp":"2026-06-02T10:04:00Z","message":{"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"user","timestamp":"2026-06-02T10:06:00Z","message":{"content":[{"type":"tool_result","tool_use_id":"x","content":"out"}]}}`,
		`{"type":"user","timestamp":"2026-06-02T10:05:00Z","message":{"content":"fix the auth token refresh"}}`,
	}, time.Date(2026, 6, 2, 10, 10, 0, 0, time.UTC))

	// Matching Codex session, older — ignored (older session_meta ts).
	writeLines(t, filepath.Join(codexRoot, "2026/06/01/rollout-a.jsonl"), []string{
		`{"timestamp":"2026-06-01T08:00:00Z","type":"session_meta","payload":{"cwd":"/work/proj","timestamp":"2026-06-01T08:00:00Z"}}`,
		`{"timestamp":"2026-06-01T08:01:00Z","type":"event_msg","payload":{"type":"user_message","message":"OLD codex ignore"}}`,
	}, time.Time{})

	// Matching Codex session, newer — the one we read. Only the
	// user_message survives (agent_message + raw injected context drop).
	writeLines(t, filepath.Join(codexRoot, "2026/06/02/rollout-b.jsonl"), []string{
		`{"timestamp":"2026-06-02T09:59:00Z","type":"session_meta","payload":{"cwd":"/work/proj","timestamp":"2026-06-02T09:59:00Z"}}`,
		`{"timestamp":"2026-06-02T10:00:00Z","type":"event_msg","payload":{"type":"user_message","message":"investigate flaky CI"}}`,
		`{"timestamp":"2026-06-02T10:00:30Z","type":"event_msg","payload":{"type":"agent_message","message":"working on it"}}`,
		`{"timestamp":"2026-06-02T10:00:40Z","type":"event_msg","payload":{"type":"user_message","message":"<environment_context>x</environment_context>"}}`,
	}, time.Time{})

	// Non-matching cwd — ignored entirely.
	writeLines(t, filepath.Join(codexRoot, "2026/06/02/rollout-c.jsonl"), []string{
		`{"timestamp":"2026-06-02T11:00:00Z","type":"session_meta","payload":{"cwd":"/other/proj","timestamp":"2026-06-02T11:00:00Z"}}`,
		`{"timestamp":"2026-06-02T11:01:00Z","type":"event_msg","payload":{"type":"user_message","message":"other proj prompt"}}`,
	}, time.Time{})

	r := Reader{ClaudeRoot: claudeRoot, CodexRoot: codexRoot}
	got := r.Gather(cwd).Prompts

	want := []string{"investigate flaky CI", "fix the auth token refresh"}
	if len(got) != len(want) {
		t.Fatalf("Gather returned %d prompts, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Text != w {
			t.Errorf("prompt[%d] = %q, want %q", i, got[i].Text, w)
		}
	}
	if !got[0].Timestamp.Before(got[1].Timestamp) {
		t.Errorf("prompts not sorted by timestamp: %v then %v", got[0].Timestamp, got[1].Timestamp)
	}
}

func TestGatherNoLogs(t *testing.T) {
	r := Reader{ClaudeRoot: t.TempDir(), CodexRoot: t.TempDir()}
	if got := r.Gather("/nonexistent/path"); !got.IsEmpty() {
		t.Errorf("expected an empty conversation, got %+v", got)
	}
}

// TestClaudeSummaryAndReply covers the compaction summary, the filtered
// task notification, and the last reply assembled from blocks that share
// a message ID.
func TestClaudeSummaryAndReply(t *testing.T) {
	claudeRoot := t.TempDir()
	writeLines(t, filepath.Join(claudeRoot, "-work-proj", "s.jsonl"), []string{
		`{"type":"user","timestamp":"2026-06-02T10:00:00Z","message":{"content":"design parallel bays"}}`,
		`{"type":"assistant","timestamp":"2026-06-02T10:01:00Z","message":{"id":"m1","content":[{"type":"text","text":"Early reply."}]}}`,
		`{"type":"user","isCompactSummary":true,"timestamp":"2026-06-02T11:00:00Z","message":{"content":"This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation.\n\nSummary: wrote the design doc."}}`,
		`{"type":"user","timestamp":"2026-06-02T11:01:00Z","message":{"content":"<task-notification><task-id>x</task-id></task-notification>"}}`,
		`{"type":"user","timestamp":"2026-06-02T11:02:00Z","message":{"content":"open pr"}}`,
		`{"type":"assistant","timestamp":"2026-06-02T11:03:00Z","message":{"id":"m2","content":[{"type":"thinking","thinking":"hmm"}]}}`,
		`{"type":"assistant","timestamp":"2026-06-02T11:03:01Z","message":{"id":"m2","content":[{"type":"text","text":"Opened PR #12."}]}}`,
		`{"type":"assistant","timestamp":"2026-06-02T11:03:02Z","message":{"id":"m2","content":[{"type":"tool_use","name":"Bash"}]}}`,
		`{"type":"assistant","timestamp":"2026-06-02T11:03:03Z","message":{"id":"m2","content":[{"type":"text","text":"CI is green."}]}}`,
		`{"type":"assistant","isSidechain":true,"timestamp":"2026-06-02T11:04:00Z","message":{"id":"m3","content":[{"type":"text","text":"subagent chatter"}]}}`,
		`{"type":"assistant","timestamp":"2026-06-02T11:05:00Z","message":{"id":"m4","model":"<synthetic>","content":[{"type":"text","text":"You've hit your limit"}]}}`,
	}, time.Time{})

	got := Reader{ClaudeRoot: claudeRoot, CodexRoot: t.TempDir()}.Gather("/work/proj")

	if len(got.Prompts) != 2 || got.Prompts[0].Text != "design parallel bays" || got.Prompts[1].Text != "open pr" {
		t.Errorf("prompts = %+v, want [design parallel bays, open pr]", got.Prompts)
	}
	if got.Summary.Text != "Summary: wrote the design doc." {
		t.Errorf("summary = %q, want the preamble stripped", got.Summary.Text)
	}
	if got.LastReply.Text != "Opened PR #12.\n\nCI is green." {
		t.Errorf("last reply = %q, want both text blocks of m2", got.LastReply.Text)
	}
}

// TestCodexCurrentFormat covers Codex versions that record typed prompts
// only as response_item messages, the task_complete reply, and skipping
// subagent and exec sessions that share the cwd.
func TestCodexCurrentFormat(t *testing.T) {
	codexRoot := t.TempDir()
	writeLines(t, filepath.Join(codexRoot, "2026/06/02/rollout-main.jsonl"), []string{
		`{"timestamp":"2026-06-02T10:00:00Z","type":"session_meta","payload":{"cwd":"/work/proj","timestamp":"2026-06-02T10:00:00Z","source":"cli"}}`,
		`{"timestamp":"2026-06-02T10:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /work/proj"},{"type":"input_text","text":"<environment_context>x</environment_context>"}]}}`,
		`{"timestamp":"2026-06-02T10:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"speed up recompute"}]}}`,
		`{"timestamp":"2026-06-02T10:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Looking."}]}}`,
		`{"timestamp":"2026-06-02T10:05:00Z","type":"event_msg","payload":{"type":"task_complete","last_agent_message":"Recompute is 38% faster."}}`,
		`{"timestamp":"2026-06-02T10:06:00Z","type":"event_msg","payload":{"type":"task_complete","last_agent_message":null}}`,
	}, time.Time{})
	// Newer, same cwd, but not the user's conversation.
	writeLines(t, filepath.Join(codexRoot, "2026/06/02/rollout-guardian.jsonl"), []string{
		`{"timestamp":"2026-06-02T11:00:00Z","type":"session_meta","payload":{"cwd":"/work/proj","timestamp":"2026-06-02T11:00:00Z","source":{"subagent":{"other":"guardian"}}}}`,
		`{"timestamp":"2026-06-02T11:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"assess this action"}]}}`,
	}, time.Time{})
	writeLines(t, filepath.Join(codexRoot, "2026/06/02/rollout-exec.jsonl"), []string{
		`{"timestamp":"2026-06-02T12:00:00Z","type":"session_meta","payload":{"cwd":"/work/proj","timestamp":"2026-06-02T12:00:00Z","source":"exec"}}`,
		`{"timestamp":"2026-06-02T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"review this diff"}}`,
	}, time.Time{})

	got := Reader{ClaudeRoot: t.TempDir(), CodexRoot: codexRoot}.Gather("/work/proj")

	if len(got.Prompts) != 1 || got.Prompts[0].Text != "speed up recompute" {
		t.Errorf("prompts = %+v, want [speed up recompute]", got.Prompts)
	}
	if got.LastReply.Text != "Recompute is 38% faster." {
		t.Errorf("last reply = %q, want the last non-empty task_complete message", got.LastReply.Text)
	}
}

// TestCodexPrefersEventChannel guards against double-counting in older
// Codex logs, which record each prompt as both an event and an item.
func TestCodexPrefersEventChannel(t *testing.T) {
	codexRoot := t.TempDir()
	writeLines(t, filepath.Join(codexRoot, "2026/06/02/rollout.jsonl"), []string{
		`{"timestamp":"2026-06-02T10:00:00Z","type":"session_meta","payload":{"cwd":"/work/proj","timestamp":"2026-06-02T10:00:00Z"}}`,
		`{"timestamp":"2026-06-02T10:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix it"}]}}`,
		`{"timestamp":"2026-06-02T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"fix it"}}`,
	}, time.Time{})

	got := Reader{ClaudeRoot: t.TempDir(), CodexRoot: codexRoot}.Gather("/work/proj")
	if len(got.Prompts) != 1 {
		t.Errorf("prompts = %+v, want exactly one", got.Prompts)
	}
}

func TestGatherMergesLatestReply(t *testing.T) {
	claudeRoot, codexRoot := t.TempDir(), t.TempDir()
	writeLines(t, filepath.Join(claudeRoot, "-work-proj", "s.jsonl"), []string{
		`{"type":"assistant","timestamp":"2026-06-02T10:00:00Z","message":{"id":"m1","content":[{"type":"text","text":"claude reply"}]}}`,
	}, time.Time{})
	writeLines(t, filepath.Join(codexRoot, "2026/06/02/rollout.jsonl"), []string{
		`{"timestamp":"2026-06-02T09:00:00Z","type":"session_meta","payload":{"cwd":"/work/proj","timestamp":"2026-06-02T09:00:00Z","source":"cli"}}`,
		`{"timestamp":"2026-06-02T11:00:00Z","type":"event_msg","payload":{"type":"task_complete","last_agent_message":"codex reply"}}`,
	}, time.Time{})

	got := Reader{ClaudeRoot: claudeRoot, CodexRoot: codexRoot}.Gather("/work/proj")
	if got.LastReply.Text != "codex reply" {
		t.Errorf("last reply = %q, want the later (codex) one", got.LastReply.Text)
	}
}
