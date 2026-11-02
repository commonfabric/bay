package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Real implements Interface by executing tmux commands.
type Real struct{}

// NewReal creates a new Real tmux implementation.
func NewReal() *Real {
	return &Real{}
}

func run(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func runSilent(args ...string) error {
	_, err := run(args...)
	return err
}

// --- Sessions ---

// exactSession returns a tmux target that matches the session name
// exactly. Without the "=" prefix, tmux prefix-matches: "-t loom"
// would match a session named "loom-old".
func exactSession(name string) string {
	return "=" + name
}

func (r *Real) HasSession(name string) (bool, error) {
	cmd := exec.Command("tmux", "has-session", "-t", exactSession(name))
	err := cmd.Run()
	if err != nil {
		// tmux returns exit code 1 if the session does not exist; that is not an error for us.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("tmux has-session -t %s: %w", name, err)
	}
	return true, nil
}

func (r *Real) NewSession(name string) error {
	return runSilent("new-session", "-d", "-s", name)
}

func (r *Real) KillSession(name string) error {
	return runSilent("kill-session", "-t", exactSession(name))
}

func (r *Real) RenameSession(oldName string, newName string) error {
	return runSilent("rename-session", "-t", exactSession(oldName), newName)
}

// resolveSessionTarget returns a tmux $session_id suitable for use
// with set-option and show-options. Those commands ignore the "=name"
// exact-match prefix that other tmux commands respect (verified on
// tmux 3.6a), so a bare name target like "loom" would prefix-match a
// sibling "loom-old" and silently write to the wrong session. The
// $session_id format ($1, $2, ...) is unambiguous.
//
// Returns "" with nil error when no session of that name exists; the
// option callers map that to "no marker" semantics.
func resolveSessionTarget(name string) (string, error) {
	out, err := run("list-sessions", "-F", "#{session_id}\t#{session_name}")
	if err != nil {
		// No server / no sessions — propagate as "not found."
		return "", nil
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && parts[1] == name {
			return parts[0], nil
		}
	}
	return "", nil
}

func (r *Real) SetSessionOption(session string, option string, value string) error {
	target, err := resolveSessionTarget(session)
	if err != nil {
		return fmt.Errorf("resolving session %q: %w", session, err)
	}
	if target == "" {
		return fmt.Errorf("session %q not found", session)
	}
	return runSilent("set-option", "-t", target, option, value)
}

// GetSessionOption deviates from GetWindowOption's error-propagating
// shape: tmux exits non-zero when the option is unset (and on some
// versions also when the session is gone), but for the
// session-identity caller the empty marker IS the answer, not a
// failure. Returning ("", nil) keeps the gate decision compact at
// the cost of conflating "unset" with "session disappeared mid-call"
// — which is fine because callers guard with HasSession first.
func (r *Real) GetSessionOption(session string, option string) (string, error) {
	target, err := resolveSessionTarget(session)
	if err != nil || target == "" {
		return "", nil
	}
	out, err := run("show-options", "-t", target, "-v", option)
	if err != nil {
		return "", nil
	}
	return out, nil
}

func (r *Real) ListSessions() ([]Session, error) {
	out, err := run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		// If no server is running, treat as empty list.
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "no current") {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var sessions []Session
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			sessions = append(sessions, Session{Name: line})
		}
	}
	return sessions, nil
}

// --- Windows ---

func (r *Real) NewWindow(session string, name string, cwd string) (string, error) {
	out, err := run("new-window", "-a", "-t", exactSession(session), "-n", name, "-c", cwd, "-P", "-F", "#{window_id}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Real) KillWindow(windowID string) error {
	return runSilent("kill-window", "-t", windowID)
}

func (r *Real) RenameWindow(windowID string, name string) error {
	return runSilent("rename-window", "-t", windowID, name)
}

func (r *Real) SetWindowOption(windowID string, option string, value string) error {
	return runSilent("set-option", "-w", "-t", windowID, option, value)
}

func (r *Real) UnsetWindowOption(windowID string, option string) error {
	return runSilent("set-option", "-w", "-u", "-t", windowID, option)
}

func (r *Real) GetWindowOption(windowID string, option string) (string, error) {
	out, err := run("show-options", "-w", "-t", windowID, "-v", option)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Real) WaitingOrBellWindowIDs(session string) (map[string]bool, error) {
	out, err := run("list-windows", "-t", exactSession(session), "-F", "#{window_id}\t#{@bay-waiting}\t#{window_bell_flag}")
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) == 3 && (parts[1] == "1" || parts[2] == "1") {
			result[parts[0]] = true
		}
	}
	return result, nil
}

func (r *Real) ListWindows(session string) ([]Window, error) {
	out, err := run("list-windows", "-t", exactSession(session), "-F", "#{window_id}\t#{window_name}\t#{window_index}")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var windows []Window
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected list-windows output: %q", line)
		}
		idx, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil, fmt.Errorf("parsing window index %q: %w", parts[2], err)
		}
		windows = append(windows, Window{
			ID:    parts[0],
			Name:  parts[1],
			Index: idx,
		})
	}
	return windows, nil
}

func (r *Real) WindowExists(windowID string) (bool, error) {
	cmd := exec.Command("tmux", "list-windows", "-t", windowID, "-F", "#{window_id}")
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("tmux list-windows -t %s: %w", windowID, err)
	}
	return true, nil
}

func (r *Real) FindWindowByName(session string, name string) (string, error) {
	windows, err := r.ListWindows(session)
	if err != nil {
		return "", err
	}
	for _, w := range windows {
		if w.Name == name {
			return w.ID, nil
		}
	}
	return "", fmt.Errorf("window %q not found in session %q", name, session)
}

func (r *Real) SelectWindow(windowID string) error {
	return runSilent("select-window", "-t", windowID)
}

// --- Panes ---

func (r *Real) SelectPane(paneID string) error {
	return runSilent("select-pane", "-t", paneID)
}

func (r *Real) SplitWindow(targetID string, dir string, cwd string, before bool) (string, error) {
	flag := "-v"
	if dir == "h" {
		flag = "-h"
	}
	args := []string{"split-window"}
	if before {
		args = append(args, "-fb")
	}
	args = append(args, "-t", targetID, flag, "-c", cwd, "-P", "-F", "#{pane_id}")
	out, err := run(args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Real) KillPane(paneID string) error {
	return runSilent("kill-pane", "-t", paneID)
}

func (r *Real) SendKeys(paneID string, keys string) error {
	return runSilent("send-keys", "-t", paneID, keys, "Enter")
}

func (r *Real) CapturePane(paneID string, lines int) (string, error) {
	out, err := run("capture-pane", "-t", paneID, "-p", "-S", fmt.Sprintf("-%d", lines))
	if err != nil {
		return "", err
	}
	return out, nil
}

func (r *Real) RespawnPane(paneID string, cwd string, command string) error {
	// -k kills the pane's process if still alive before respawning.
	// Without -k, respawn-pane fails on live panes with "pane still active".
	args := []string{"respawn-pane", "-k", "-t", paneID, "-c", cwd}
	if command != "" {
		args = append(args, command)
	}
	return runSilent(args...)
}

func (r *Real) ListPanes(windowID string) ([]Pane, error) {
	out, err := run("list-panes", "-t", windowID, "-F", "#{pane_id}\t#{pane_pid}\t#{pane_active}")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var panes []Pane
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected list-panes output: %q", line)
		}
		pid, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("parsing pane PID %q: %w", parts[1], err)
		}
		panes = append(panes, Pane{
			ID:     parts[0],
			PID:    pid,
			Active: parts[2] == "1",
		})
	}
	return panes, nil
}

func (r *Real) GetPanePID(paneID string) (int, error) {
	out, err := run("display-message", "-t", paneID, "-p", "#{pane_pid}")
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parsing pane PID %q: %w", out, err)
	}
	return pid, nil
}

func (r *Real) PaneExists(paneID string) (bool, error) {
	out, err := run("display-message", "-t", paneID, "-p", "#{pane_dead}")
	if err != nil {
		return false, nil // pane doesn't exist at all
	}
	// pane_dead is "1" for dead panes (remain-on-exit), "0" for live
	return strings.TrimSpace(out) == "0", nil
}

func (r *Real) GetPaneCursorY(paneID string) (int, error) {
	out, err := run("display-message", "-t", paneID, "-p", "#{cursor_y}")
	if err != nil {
		return 0, err
	}
	y, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parsing cursor_y %q: %w", out, err)
	}
	return y, nil
}

// --- Current context ---

// currentTarget returns a tmux target anchoring "current" queries to
// this process's own tmux context, or "" when there is nothing to
// anchor to.
//
// An untargeted `display-message -p` does NOT report the session that
// invoked bay. With no client context tmux falls back to the most
// recently active session on the server — attached or not — so any
// unrelated session that happens to be newer silently captures the
// answer. Background tooling that spawns detached sessions (an agent
// review harness, a build script) reliably outranks the user's dock.
//
// That fallback reaches every keybinding: bay's bindings run via tmux
// run-shell, which exports no TMUX_PANE, so an unanchored query is
// resolved against whatever session sorted first. The user presses a
// key in one dock and bay acts on another, or errors out with "not in
// a bay context" and the binding's trailing `|| true` swallows it.
//
// Two anchors, most precise first:
//
//   - $TMUX_PANE — an interactive shell's own pane.
//   - $TMUX ("socket,pid,session-id") — run-shell propagates the
//     session the binding fired in, so the trailing field identifies
//     the pressing client's session even with no pane in the
//     environment.
func currentTarget() string {
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		return pane
	}
	// Take the last field rather than index 2: a socket path may itself
	// contain a comma. The session ID is always last and always numeric.
	if parts := strings.Split(os.Getenv("TMUX"), ","); len(parts) >= 3 {
		if id := parts[len(parts)-1]; id != "" {
			if _, err := strconv.Atoi(id); err == nil {
				return "$" + id
			}
		}
	}
	return ""
}

// ErrNotInTmux is returned by the Current* accessors when the calling
// process is not running inside tmux at all.
var ErrNotInTmux = errors.New("not running inside tmux")

// currentFormat evaluates a tmux format against currentTarget().
//
// Outside tmux there is no "current" anything, and the untargeted
// query below would answer anyway — `display-message -p` does not fail
// with no client context, it reports the most recently active session
// on the server. Same fallback currentTarget() exists to dodge, except
// no anchor can help: the process has no tmux context to anchor to. So
// refuse the question instead. $TMUX is the signal, not $TMUX_PANE:
// run-shell and status-line #() jobs both export TMUX while leaving
// TMUX_PANE empty.
//
// On failure it retries untargeted. A stale $TMUX_PANE (its pane
// killed out from under a still-running shell) is the case that needs
// it, and the retry cannot resurrect the bug currentTarget() exists to
// fix: there the anchored query succeeds and simply returns the right
// answer.
func currentFormat(format string) (string, error) {
	if os.Getenv("TMUX") == "" {
		return "", ErrNotInTmux
	}
	if target := currentTarget(); target != "" {
		if out, err := run("display-message", "-t", target, "-p", format); err == nil {
			return strings.TrimSpace(out), nil
		}
	}
	out, err := run("display-message", "-p", format)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Real) CurrentSession() (string, error) {
	return currentFormat("#{session_name}")
}

func (r *Real) CurrentWindowID() (string, error) {
	return currentFormat("#{window_id}")
}

func (r *Real) CurrentPaneID() (string, error) {
	return currentFormat("#{pane_id}")
}

// --- Client display ---

// DisplayMessage shows a transient message in the tmux status line. The
// message is rendered with tmux format strings, so it may contain attribute
// codes like #[bold]name#[default]. Pass durationMs=0 to use tmux's
// display-time default; otherwise the message stays for that many
// milliseconds.
// DisplayMessageAsync fires tmux display-message without waiting for the
// tmux client process to return. Used when bay is running inside a tmux
// run-shell keybinding that's also doing visible tmux work (e.g. creating
// a pane): a synchronous DisplayMessage delays run-shell's exit, which
// delays tmux's redraw of the new pane.
//
// The child's stdio MUST be redirected to /dev/null. If it inherits bay's
// stdin/stdout/stderr, the child tmux client holds those pipes open until
// the message clears; tmux's run-shell waits on those pipes to close
// before considering bay "done," so the parent pane update stays blocked
// for the full display duration — defeating the point of the async call.
func (r *Real) DisplayMessageAsync(msg string, durationMs int) error {
	args := []string{"display-message"}
	if durationMs > 0 {
		args = append(args, "-d", strconv.Itoa(durationMs))
	}
	args = append(args, msg)
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open /dev/null: %w", err)
	}
	defer devNull.Close()
	cmd := exec.Command("tmux", args...)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("tmux display-message: %w", err)
	}
	return cmd.Process.Release()
}

func (r *Real) DisplayMessage(msg string, durationMs int) error {
	if durationMs > 0 {
		return runSilent("display-message", "-d", strconv.Itoa(durationMs), msg)
	}
	return runSilent("display-message", msg)
}

// FindDockEditorWindow returns the window ID of the dock-level editor
// window (tagged with @bay-dock-editor=1), or ("", false) if none exists.
func (r *Real) FindDockEditorWindow(session string) (string, bool) {
	out, err := run("list-windows", "-t", exactSession(session), "-F", "#{window_id}\t#{@bay-dock-editor}")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) == 2 && parts[1] == "1" {
			return parts[0], true
		}
	}
	return "", false
}

// MoveWindow moves a window to a specific index.
func (r *Real) MoveWindow(windowID string, targetIndex int) error {
	return runSilent("move-window", "-s", windowID, "-t", fmt.Sprintf("%d", targetIndex))
}

func (r *Real) MoveWindowAfter(windowID string, afterWindowID string) error {
	return runSilent("move-window", "-a", "-s", windowID, "-t", afterWindowID)
}

// DisplayPopup opens a tmux popup running the given command.
func (r *Real) DisplayPopup(cmd string) error {
	return runSilent("display-popup", "-E", cmd)
}

// ClientWidth returns the width in cells of the attached tmux client.
// Returns 100 (a reasonable default) if tmux can't report a width — e.g.
// no client attached, or the value can't be parsed.
func (r *Real) ClientWidth() (int, error) {
	out, err := run("display-message", "-p", "#{client_width}")
	if err != nil {
		return 100, nil
	}
	w, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || w <= 0 {
		return 100, nil
	}
	return w, nil
}

// StatusReservedCells returns the actual visible width of the rendered
// status-left and status-right sections (capped at their respective *-length
// limits). This is the cells that are unavailable for window tabs.
//
// Using actual rendered widths instead of the configured *-length limits
// avoids wasting budget when limits are set high but content is short.
//
// Caveat: tmux evaluates `#(shell-command)` substitutions asynchronously and
// caches the result. On the first query, the cache may be cold and return
// empty for those segments. We fall back to the configured *-length in that
// case to stay conservative and avoid tabs overflowing into the empty region
// once the shell-substitution populates.
func (r *Real) StatusReservedCells() (int, error) {
	out, err := run("display-message", "-p", "#{T:status-left}\t#{T:status-right}")
	if err != nil {
		return 0, err
	}
	parts := strings.SplitN(out, "\t", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("unexpected output: %q", out)
	}
	left := visibleWidth(parts[0])
	right := visibleWidth(parts[1])

	// Per-side: cap measured by configured length; fall back to length when
	// measured is 0 (likely an uncached `#(...)` substitution).
	leftLen, leftLenErr := optInt("status-left-length")
	rightLen, rightLenErr := optInt("status-right-length")
	if leftLenErr == nil {
		if left == 0 || left > leftLen {
			left = leftLen
		}
	}
	if rightLenErr == nil {
		if right == 0 || right > rightLen {
			right = rightLen
		}
	}
	return left + right, nil
}

// optInt fetches a tmux integer option via `show -gv`.
func optInt(name string) (int, error) {
	out, err := run("show", "-gv", name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// visibleWidth returns the number of visible cells in a tmux-evaluated string,
// stripping #[...] style directives. Assumes most chars are single-width;
// multi-byte runes count as one cell each (close enough for status-bar text).
func visibleWidth(s string) int {
	n := 0
	for i := 0; i < len(s); {
		// Handle ## (literal #) and #[...] style directives.
		if i+1 < len(s) && s[i] == '#' {
			if s[i+1] == '#' {
				n++
				i += 2
				continue
			}
			if s[i+1] == '[' {
				end := strings.IndexByte(s[i+2:], ']')
				if end == -1 {
					break
				}
				i += 2 + end + 1
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 {
			break
		}
		n++
		i += size
	}
	return n
}
