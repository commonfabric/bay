// Package transcript reads coding-agent session logs and extracts what
// the auto-description backstop needs to summarize a bay's work: the
// human's typed prompts, the session-history summary written when the
// conversation was compacted, and the agent's latest reply. It supports
// Claude Code and Codex; other agents yield nothing (the backstop then
// falls back to git).
//
// Reading never returns an error: absent, unreadable, or malformed logs
// simply produce an empty Conversation. That is itself a meaningful
// signal to the caller (fall back to git), so failures stay silent here.
//
// See docs/design/auto-descriptions.md.
package transcript

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Prompt is one timestamped message extracted from a transcript.
type Prompt struct {
	Timestamp time.Time
	Text      string
}

// Conversation is what a bay's agent sessions say about its work.
type Conversation struct {
	// Prompts are the human-typed prompts, oldest first.
	Prompts []Prompt
	// Summary is the latest session-history summary the agent wrote when
	// it compacted the conversation (Claude Code only). Zero if none.
	Summary Prompt
	// LastReply is the agent's most recent text reply (for Codex, the
	// final message of the latest turn), which usually says where the
	// work stands. Zero if none.
	LastReply Prompt
}

// IsEmpty reports whether the conversation carries no text at all.
func (c Conversation) IsEmpty() bool {
	return len(c.Prompts) == 0 && c.Summary.Text == "" && c.LastReply.Text == ""
}

// merge folds other into c: prompts are concatenated (the caller sorts),
// and the later summary and reply win.
func (c *Conversation) merge(other Conversation) {
	c.Prompts = append(c.Prompts, other.Prompts...)
	c.Summary = later(c.Summary, other.Summary)
	c.LastReply = later(c.LastReply, other.LastReply)
}

func later(a, b Prompt) Prompt {
	if b.Text == "" || (a.Text != "" && b.Timestamp.Before(a.Timestamp)) {
		return a
	}
	return b
}

// Reader locates and parses agent transcripts. The zero value reads
// from the standard per-user locations; tests override the roots.
type Reader struct {
	ClaudeRoot string // default ~/.claude/projects
	CodexRoot  string // default ~/.codex/sessions
}

// Gather returns the conversation for cwd across all supported agents,
// with prompts merged and sorted by timestamp. It reads the most recent
// session per agent — a bay may use Claude, Codex, or both.
func (r Reader) Gather(cwd string) Conversation {
	resolved := resolvePath(cwd)
	var out Conversation
	out.merge(r.claudeConversation(resolved))
	out.merge(r.codexConversation(resolved))
	sort.SliceStable(out.Prompts, func(i, j int) bool {
		return out.Prompts[i].Timestamp.Before(out.Prompts[j].Timestamp)
	})
	return out
}

// --- Claude Code ---

// nonAlnum collapses every run of non-alphanumeric characters to a
// single dash, matching how Claude Code encodes a cwd into its
// per-project transcript directory name (e.g. /Users/x/p -> -Users-x-p).
var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]+`)

func encodeClaudeDir(cwd string) string {
	return nonAlnum.ReplaceAllString(cwd, "-")
}

func (r Reader) claudeRoot() string {
	if r.ClaudeRoot != "" {
		return r.ClaudeRoot
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

func (r Reader) claudeConversation(cwd string) Conversation {
	root := r.claudeRoot()
	if root == "" {
		return Conversation{}
	}
	path := newestJSONL(filepath.Join(root, encodeClaudeDir(cwd)))
	if path == "" {
		return Conversation{}
	}
	return parseClaude(path)
}

// claudeWrappers are prefixes of synthetic (non-typed) user entries —
// bash IO, slash-command expansions, background-task notifications, and
// injected reminders — that must not pollute a "what is this about"
// summary.
var claudeWrappers = []string{"<bash-", "<command-", "<local-command-", "<system-reminder>", "<task-notification>"}

// claudeCompactPreamble opens every compaction summary; it carries no
// information, so it is dropped to save the summarizer's input budget.
const claudeCompactPreamble = "This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation."

type claudeEntry struct {
	Type             string `json:"type"`
	IsMeta           bool   `json:"isMeta"`
	IsSidechain      bool   `json:"isSidechain"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Timestamp        string `json:"timestamp"`
	Message          struct {
		ID string `json:"id"`
		// Model is "<synthetic>" for client-generated assistant entries
		// (API errors, usage-limit notices), which aren't the agent's.
		Model string `json:"model"`
		// Content is a string for a typed prompt and an array of blocks
		// for tool results and assistant output.
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func parseClaude(path string) Conversation {
	var out Conversation
	// Claude Code logs each content block of an assistant message as its
	// own entry sharing the message ID, so a reply's text is the join of
	// the text blocks under the latest ID that has any.
	var replyID string
	eachLine(path, func(line []byte) {
		var e claudeEntry
		if json.Unmarshal(line, &e) != nil || e.IsMeta || e.IsSidechain {
			return
		}
		ts := parseTime(e.Timestamp)
		switch e.Type {
		case "assistant":
			if e.Message.Model == "<synthetic>" {
				return
			}
			text := claudeText(e.Message.Content)
			if text == "" {
				return
			}
			if e.Message.ID != "" && e.Message.ID == replyID {
				out.LastReply.Text += "\n\n" + text
				return
			}
			replyID = e.Message.ID
			out.LastReply = Prompt{Timestamp: ts, Text: text}
		case "user":
			var text string
			// A tool result has an array content; decoding to a string
			// fails and drops it, which is exactly what we want.
			if json.Unmarshal(e.Message.Content, &text) != nil {
				return
			}
			text = strings.TrimSpace(text)
			if e.IsCompactSummary {
				text = strings.TrimSpace(strings.TrimPrefix(text, claudeCompactPreamble))
				if text != "" {
					out.Summary = Prompt{Timestamp: ts, Text: text}
				}
				return
			}
			if text == "" || hasAnyPrefix(text, claudeWrappers) {
				return
			}
			out.Prompts = append(out.Prompts, Prompt{Timestamp: ts, Text: text})
		}
	})
	return out
}

// claudeText joins the text blocks of an assistant message's content,
// skipping thinking and tool-use blocks.
func claudeText(content json.RawMessage) string {
	var blocks []claudeBlock
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if t := strings.TrimSpace(b.Text); b.Type == "text" && t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// --- Codex ---

func (r Reader) codexRoot() string {
	if r.CodexRoot != "" {
		return r.CodexRoot
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

func (r Reader) codexConversation(cwd string) Conversation {
	root := r.codexRoot()
	if root == "" {
		return Conversation{}
	}
	path := newestCodexSession(root, cwd)
	if path == "" {
		return Conversation{}
	}
	return parseCodex(path)
}

// codexInjected are prefixes of auto-injected context that appear in the
// raw user channel (response_item messages) alongside typed prompts.
var codexInjected = []string{"<environment_context>", "<INSTRUCTIONS>", "# AGENTS.md", "<user_instructions>", "<permissions instructions>", "<turn_aborted>"}

type codexMeta struct {
	Type    string `json:"type"`
	Payload struct {
		Cwd       string `json:"cwd"`
		Timestamp string `json:"timestamp"`
		// Source is a string ("cli", "exec", ...) for a top-level
		// session and an object ({"subagent": ...}) for a subagent.
		Source json.RawMessage `json:"source"`
	} `json:"payload"`
}

// conversational reports whether a session is one the user talks to.
// Subagent sessions (such as the approval guardian) and non-interactive
// `codex exec` runs share the bay's cwd but carry machine-written
// prompts, and would otherwise displace the user's own session as the
// newest one.
func (m codexMeta) conversational() bool {
	var source string
	if len(m.Payload.Source) == 0 {
		return true
	}
	if json.Unmarshal(m.Payload.Source, &source) != nil {
		return false // an object: a subagent
	}
	return source != "exec"
}

// newestCodexSession finds the most recent conversational rollout whose
// line-1 session_meta records the target cwd. Reading only the first
// line of each file keeps the scan cheap.
func newestCodexSession(root, cwd string) string {
	var best string
	var bestTime time.Time
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		first := firstLine(p)
		if first == nil {
			return nil
		}
		var meta codexMeta
		if json.Unmarshal(first, &meta) != nil || meta.Type != "session_meta" {
			return nil
		}
		if !cwdMatches(meta.Payload.Cwd, cwd) || !meta.conversational() {
			return nil
		}
		t := parseTime(meta.Payload.Timestamp)
		if t.IsZero() {
			if info, e := d.Info(); e == nil {
				t = info.ModTime()
			}
		}
		if best == "" || t.After(bestTime) {
			best, bestTime = p, t
		}
		return nil
	})
	return best
}

type codexEvent struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type             string `json:"type"`
		Message          string `json:"message"`
		LastAgentMessage string `json:"last_agent_message"`
		Role             string `json:"role"`
		Content          []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
}

// parseCodex reads prompts from either channel Codex has used: older
// versions emit a user_message event per typed prompt (alongside a raw
// response_item copy mixed with injected context); current versions
// emit only the response_item. The event channel is preferred when
// present so prompts aren't counted twice.
func parseCodex(path string) Conversation {
	var out Conversation
	var events, items []Prompt
	eachLine(path, func(line []byte) {
		var e codexEvent
		if json.Unmarshal(line, &e) != nil {
			return
		}
		ts := parseTime(e.Timestamp)
		switch {
		case e.Type == "event_msg" && e.Payload.Type == "user_message":
			if text := strings.TrimSpace(e.Payload.Message); text != "" && !hasAnyPrefix(text, codexInjected) {
				events = append(events, Prompt{Timestamp: ts, Text: text})
			}
		case e.Type == "event_msg" && e.Payload.Type == "task_complete":
			if text := strings.TrimSpace(e.Payload.LastAgentMessage); text != "" {
				out.LastReply = Prompt{Timestamp: ts, Text: text}
			}
		case e.Type == "response_item" && e.Payload.Type == "message" && e.Payload.Role == "user":
			var parts []string
			for _, c := range e.Payload.Content {
				if text := strings.TrimSpace(c.Text); c.Type == "input_text" && text != "" && !hasAnyPrefix(text, codexInjected) {
					parts = append(parts, text)
				}
			}
			if len(parts) > 0 {
				items = append(items, Prompt{Timestamp: ts, Text: strings.Join(parts, "\n")})
			}
		}
	})
	out.Prompts = events
	if len(out.Prompts) == 0 {
		out.Prompts = items
	}
	return out
}

func cwdMatches(stored, target string) bool {
	if stored == target {
		return true
	}
	return resolvePath(stored) == target
}

// --- shared helpers ---

// resolvePath returns the symlink-resolved absolute path, or the input
// unchanged if it can't be resolved (e.g. it doesn't exist).
func resolvePath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// newestJSONL returns the newest-mtime *.jsonl in dir, or "".
func newestJSONL(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest = filepath.Join(dir, e.Name())
			newestMod = info.ModTime()
		}
	}
	return newest
}

// eachLine streams a file line by line, tolerating arbitrarily long
// lines (transcripts embed large tool results). It is silent on errors.
func eachLine(path string, fn func([]byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			fn(line)
		}
		if err != nil {
			return
		}
	}
}

func firstLine(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil
	}
	return line
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}
