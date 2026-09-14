package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/commontoolsinc/bay/internal/config"
	"github.com/commontoolsinc/bay/internal/tmux"
	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "First-time setup: config, docks, keybindings, completions",
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := bufio.NewReader(os.Stdin)

			p := bayPaths()
			configDir := p.ConfigDir
			configPath := p.ConfigFile

			// Create directories
			for _, dir := range []string{p.ConfigDir, p.DataDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return fmt.Errorf("creating directory %s: %w", dir, err)
				}
			}

			// Ensure config file exists (empty is fine — defaults are built-in).
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				header := "# Bay config — run 'bay help config' for documentation.\n"
				if err := os.WriteFile(configPath, []byte(header), 0o644); err != nil {
					return fmt.Errorf("writing config: %w", err)
				}
				fmt.Printf("Config created at %s\n", configPath)
			} else {
				fmt.Printf("Config at %s\n", configPath)
			}

			// Write default waiting-patterns file.
			// Migrate old name if present.
			promptsPath := filepath.Join(configDir, "waiting-patterns.txt")
			if oldPath := filepath.Join(configDir, "bay-prompts.txt"); promptsPath != oldPath {
				if _, err := os.Stat(oldPath); err == nil {
					if _, err := os.Stat(promptsPath); os.IsNotExist(err) {
						_ = os.Rename(oldPath, promptsPath)
					}
				}
			}
			if _, err := os.Stat(promptsPath); os.IsNotExist(err) {
				defaultPrompts := `# Bay agent-waiting detection patterns
# One regex per line. Comments start with #.

# Claude Code
Allow.*Deny
Enter to select.*Esc to cancel
shift\+tab to approve

# Codex
\[Y/n\]

# Antigravity
Approve\? \(y/n
Allow command.*\[y/N\]

# Generic
\(y/n\)
Do you want to proceed
`
				if err := os.WriteFile(promptsPath, []byte(defaultPrompts), 0o644); err != nil {
					return fmt.Errorf("writing prompts: %w", err)
				}
				fmt.Printf("Prompts written to %s\n", promptsPath)
			}

			// Claude Code integration (only if claude is installed)
			if _, err := exec.LookPath("claude"); err == nil {
				installBaySkill()
				installClaudeHooks(reader)
			}

			// Install shell completions
			installCompletions(cmd.Root(), reader)

			// Install tmux keybindings
			installKeybindings(reader)

			// Install tmux status-line snippet
			installStatusRight(reader)

			// Bundle: turn-complete signaling for installed agents +
			// the tmux hook that auto-clears the flag on focus. One
			// prompt, all-or-nothing — these pieces only work as a unit.
			installReadySignaling(reader)

			// Configure defaults
			configureAgent(reader, configPath)
			configureEditor(reader, configPath)
			configureAutoDescriptions(reader, configPath)

			fmt.Println("\nSetup complete. Next steps:")
			fmt.Println("  bay new                create a bay in any git repo")
			fmt.Println("  bay help               see all commands")
			fmt.Println("  bay help <command>      detailed help for a command")
			fmt.Println()
			fmt.Println("Tutorial: https://github.com/commontoolsinc/bay/blob/main/docs/tutorial.md")

			return nil
		},
	}
}

func installCompletions(_ *cobra.Command, _ *bufio.Reader) {
	fmt.Println()

	shell := os.Getenv("SHELL")
	var shellName, snippet string

	switch {
	case strings.HasSuffix(shell, "/zsh"):
		shellName = "zsh"
		snippet = `if command -v bay > /dev/null ; then
  source <(bay completion zsh)
fi`
	case strings.HasSuffix(shell, "/bash"):
		shellName = "bash"
		snippet = `if command -v bay > /dev/null ; then
  source <(bay completion bash)
fi`
	case strings.HasSuffix(shell, "/fish"):
		shellName = "fish"
		snippet = `if command -v bay > /dev/null
  bay completion fish | source
end`
	default:
		fmt.Println("Could not detect shell. Add one of these to your shell rc file:")
		fmt.Println("  Bash/Zsh:  source <(bay completion bash|zsh)")
		fmt.Println("  Fish:      bay completion fish | source")
		return
	}

	rcFile := shellRCFile(shellName)
	data, _ := os.ReadFile(rcFile)
	if strings.Contains(string(data), "bay completion") {
		fmt.Printf("Shell completions already configured in %s\n", rcFile)
		return
	}
	fmt.Printf("Add this to %s for tab completions:\n\n", rcFile)
	fmt.Println("  " + strings.ReplaceAll(snippet, "\n", "\n  "))
	fmt.Println()
}

func shellRCFile(shell string) string {
	home, _ := os.UserHomeDir()
	switch shell {
	case "zsh":
		return filepath.Join(home, ".zshrc")
	case "bash":
		return filepath.Join(home, ".bashrc")
	case "fish":
		return filepath.Join(home, ".config", "fish", "config.fish")
	default:
		return "your shell rc file"
	}
}

// bayKeybinding describes one canonical bay tmux binding.
//
// Most bindings invoke bay itself (tmuxVerb="run-shell", cmd="bay ..."), but
// bay also ships native tmux navigation bindings (next-window, swap-window)
// to give users a convenient navigation layout. For those, isTmuxCommand=true
// and cmd is the raw tmux command, not a shell invocation.
type bayKeybinding struct {
	key           string
	table         string // "" = root (-n); else binds inside `-T <table>` (chord sub-tables)
	cmd           string
	previousCmds  []string
	desc          string
	tmuxVerb      string // "run-shell" or "display-popup -E" (ignored when isTmuxCommand)
	isTmuxCommand bool   // cmd is a raw tmux command (e.g., "next-window")
	global        bool   // bind in every session, not just bay-managed ones
}

// id is the composite identity used for uniqueness: a key in the root
// table and the same letter in a chord sub-table are distinct bindings,
// so we need to qualify by table.
func (kb bayKeybinding) id() string {
	return bindID(kb.table, kb.key)
}

func bindID(table, key string) string {
	if table == "" {
		return key
	}
	return table + ":" + key
}

// bindFlag returns the tmux argument that places this binding in the
// right key-table: `-n` for the root table, `-T <table>` for chord
// sub-tables.
func (kb bayKeybinding) bindFlag() string {
	if kb.table == "" {
		return "-n"
	}
	return "-T " + kb.table
}

// scoped reports whether the canonical line guards this binding so it
// only fires in bay-managed sessions. Two kinds of binding are not
// guarded: the navigation keys, which are deliberately global (see
// `global`), and chord sub-table bindings, which are unreachable
// except through their entry key — scoping M-o scopes the whole chord.
func (kb bayKeybinding) scoped() bool {
	return kb.table == "" && !kb.global
}

// bayKeybindings defines all bay tmux keybindings.
//
// canonicalLine() appends "|| true" to bay-invoking bindings so a
// command that has nothing to do stays silent. Without it, tmux
// run-shell displays 'bay ... returned 1' in the status line when bay
// exits with an error (e.g., "not in a bay" on a dock's placeholder
// window). Stderr is already invisible in run-shell, so only the exit
// code needs masking. Sessions bay doesn't manage are handled earlier,
// by the scoping below — the command never runs there at all.
//
// Every binding that runs a bay command is scoped: it fires only in a
// session bay manages, and passes its key through to the pane's
// application everywhere else (see tmux.ScopeBinding). The navigation
// bindings below are the deliberate exception — they are tmux-native
// window/pane movement, useful in any session and harmless in one bay
// doesn't own, so they stay global.
//
// The scheme is vim-flavored hjkl:
//   - h/l: previous/next window (tabs run horizontally in the status bar)
//   - j/k: pane down/up (most pane splits stack vertically)
//   - H/L: pane left/right (horizontal splits are the secondary axis)
//   - J/K: pane down/up — duplicates j/k on purpose, so a user holding
//     Shift for a sequence like H, J, L can keep the modifier down
//     without releasing it mid-chord.
//
// Installing these is opt-in via conflict detection: if the user already
// has a binding on one of these keys, bay asks first.
var bayKeybindings = []bayKeybinding{
	// Window navigation (tabs run left-right across the status bar)
	{key: "M-h", cmd: "previous-window", desc: "Option+h: previous window", isTmuxCommand: true, global: true},
	{key: "M-l", cmd: "next-window", desc: "Option+l: next window", isTmuxCommand: true, global: true},

	// Pane navigation within the current window.
	{key: "M-j", cmd: "select-pane -D", desc: "Option+j: select pane down", isTmuxCommand: true, global: true},
	{key: "M-k", cmd: "select-pane -U", desc: "Option+k: select pane up", isTmuxCommand: true, global: true},
	{key: "M-H", cmd: "select-pane -L", desc: "Option+H: select pane left", isTmuxCommand: true, global: true},
	{key: "M-L", cmd: "select-pane -R", desc: "Option+L: select pane right", isTmuxCommand: true, global: true},
	{key: "M-J", cmd: "select-pane -D", desc: "Option+J: select pane down (mirror of j)", isTmuxCommand: true, global: true},
	{key: "M-K", cmd: "select-pane -U", desc: "Option+K: select pane up (mirror of k)", isTmuxCommand: true, global: true},

	// Bay picker
	{key: "M-g", cmd: "bay go --pick", desc: "Option+g: pick bay in dock", tmuxVerb: "run-shell"},

	// Creation
	{key: "M-c", cmd: "bay new -q", desc: "Option+c: create bay in current dock", tmuxVerb: "run-shell"},
	{key: "M-C", cmd: "bay new -q --agent", desc: "Option+C: create bay with agent in current dock", tmuxVerb: "run-shell"},
	{key: "M-s", cmd: "bay shell --pane", desc: "Option+s: shell as split pane", tmuxVerb: "run-shell"},
	{key: "M-S", cmd: "bay shell --window", desc: "Option+S: shell in a new window", tmuxVerb: "run-shell"},
	{key: "M-a", cmd: "bay agent --pane", desc: "Option+a: agent as split pane", tmuxVerb: "run-shell"},
	{key: "M-A", cmd: "bay agent --window", desc: "Option+A: agent in a new window", tmuxVerb: "run-shell"},
	{key: "M-e", cmd: "bay edit", desc: "Option+e: bay editor", tmuxVerb: "run-shell"},
	{key: "M-E", cmd: "bay edit --dock", desc: "Option+E: dock editor", tmuxVerb: "run-shell"},

	// Navigation (dock-wide)
	{key: "M-r", cmd: "bay go --next-waiting", desc: "Option+r: jump to next waiting bay", tmuxVerb: "run-shell"},

	// Utility
	{key: "M-w", cmd: "bay sf close self", desc: "Option+w: close current surface (or pane)", tmuxVerb: "run-shell"},
	{key: "M-W", cmd: "bay close self --force --tap", desc: "Option+W: force-close current bay even if dirty (press twice)", tmuxVerb: "run-shell"},
	{key: "M-z", cmd: "bay sf restore", desc: "Option+z: restore most recently closed surface (undo-close)", tmuxVerb: "run-shell"},
	{key: "M-/", cmd: "bay show --flash", desc: "Option+/: flash current bay info (first line)", tmuxVerb: "run-shell"},
	{key: "M-?", cmd: "bay show --popup", desc: "Option+?: popup with full bay description", tmuxVerb: "run-shell"},

	// Command palette
	{key: "M-p", cmd: "bay palette --split pane", desc: "Option+p: command palette (Tab inside to flip mode)", tmuxVerb: "display-popup -w 80% -h 80% -E"},

	// Agent chord (M-o ...): pick an agent for the current bay, chord
	// through `b` to create a new bay seeded with one, or chord through
	// `h` for home actions. The display-message entries act as a
	// one-tap reminder of the available letters; lowercase=pane,
	// Shift=window matches the rest of bay's creation keys.
	{
		key: "M-o",
		cmd: `display-message -d 2000 "agent: c Claude, x Codex, g Antigravity, f Fable, o Opus | Shift=window | b=bay | h=home" \; switch-client -T ` + tableAgent,
		// previousCmds for display-message bindings hold the old
		// quoted hint text, not the full bind line: parseBindLine
		// extracts only the quoted region for tmux-command bindings,
		// and that's what mismatched-binding detection compares.
		previousCmds: []string{
			"agent: c Claude, x Codex, g Gemini | Shift=window | b=bay",
			"agent: c Claude, x Codex, g Gemini | Shift=window | b=bay | h=home",
			"agent: c Claude, x Codex, g Antigravity | Shift=window | b=bay | h=home",
		},
		desc:          "Option+o: agent chord launcher",
		isTmuxCommand: true,
	},

	agentInBay("c", "claude", "pane"),
	agentInBay("C", "claude", "window"),
	agentInBay("x", "codex", "pane"),
	agentInBay("X", "codex", "window"),
	agentInBay("g", "antigravity", "pane"),
	agentInBay("G", "antigravity", "window"),
	agentInBay("f", "fable", "pane"),
	agentInBay("F", "fable", "window"),
	agentInBay("o", "opus", "pane"),
	agentInBay("O", "opus", "window"),

	{table: tableAgent, key: "b", cmd: `display-message -d 2000 "bay: c Claude, x Codex, g Antigravity, f Fable, o Opus" \; switch-client -T ` + tableAgentBay, previousCmds: []string{"bay: c Claude, x Codex, g Gemini", "bay: c Claude, x Codex, g Antigravity"}, desc: "M-o b: new-bay-with-agent submenu", isTmuxCommand: true},
	{table: tableAgent, key: "h", cmd: `display-message -d 2000 "home: Enter home, s shell, e editor, c Claude, x Codex, g Antigravity, f Fable, o Opus" \; switch-client -T ` + tableHome, previousCmds: []string{"home: Enter home, s shell, e editor, c Claude, x Codex, g Gemini", "home: Enter home, s shell, e editor, c Claude, x Codex, g Antigravity"}, desc: "M-o h: home submenu", isTmuxCommand: true},

	newBayWithAgent("c", "claude"),
	newBayWithAgent("x", "codex"),
	newBayWithAgent("g", "antigravity"),
	newBayWithAgent("f", "fable"),
	newBayWithAgent("o", "opus"),

	homeBinding("Enter", "bay home", "focus/create home shell"),
	homeBinding("s", "bay shell --bay home", "shell in home"),
	homeBinding("e", "bay edit --bay home", "editor in home"),
	homeAgent("c", "claude"),
	homeAgent("x", "codex"),
	homeAgent("g", "antigravity"),
	homeAgent("f", "fable"),
	homeAgent("o", "opus"),
}

// Tmux key-table names for the M-o chord. Used both for the `table:`
// field on chord-table bindings and as the `-T <table>` argument inside
// the `switch-client` commands that launch the chord — keeping them as
// constants prevents the two from drifting apart.
const (
	tableAgent    = "bay-agent"
	tableAgentBay = "bay-agent-bay"
	tableHome     = "bay-home"
)

func agentInBay(key, agent, mode string) bayKeybinding {
	kb := bayKeybinding{
		table:    tableAgent,
		key:      key,
		cmd:      fmt.Sprintf("bay agent %s --%s", agent, mode),
		desc:     fmt.Sprintf("M-o %s: %s in current bay (%s)", key, titleAgent(agent), mode),
		tmuxVerb: "run-shell",
	}
	if agent == "antigravity" {
		kb.previousCmds = []string{fmt.Sprintf("bay agent gemini --%s", mode)}
	}
	return kb
}

func newBayWithAgent(key, agent string) bayKeybinding {
	kb := bayKeybinding{
		table:    tableAgentBay,
		key:      key,
		cmd:      "bay new -q --agent=" + agent,
		desc:     fmt.Sprintf("M-o b %s: new bay with %s", key, titleAgent(agent)),
		tmuxVerb: "run-shell",
	}
	if agent == "antigravity" {
		kb.previousCmds = []string{"bay new -q --agent=gemini"}
	}
	return kb
}

func homeBinding(key, cmd, action string) bayKeybinding {
	return bayKeybinding{
		table:    tableHome,
		key:      key,
		cmd:      cmd,
		desc:     fmt.Sprintf("M-o h %s: %s", key, action),
		tmuxVerb: "run-shell",
	}
}

func homeAgent(key, agent string) bayKeybinding {
	kb := homeBinding(
		key,
		fmt.Sprintf("bay agent %s --bay home", agent),
		fmt.Sprintf("%s in home", titleAgent(agent)),
	)
	if agent == "antigravity" {
		kb.previousCmds = []string{"bay agent gemini --bay home"}
	}
	return kb
}

func titleAgent(agent string) string {
	if agent == "" {
		return ""
	}
	return strings.ToUpper(agent[:1]) + agent[1:]
}

const bayKeybindingsMarker = "# Bay keybindings"
const bayHooksMarker = "# Bay hooks"

// bayClearWaitingHook clears @bay-waiting when a window is selected,
// mirroring how tmux's native window_bell_flag self-clears on focus.
// Stop hooks (Claude/Codex) set the flag; this is what unsets it. The
// -a flag appends so we don't trample a user-defined after-select-window
// hook (assuming bay's line in .tmux.conf is sourced after theirs).
const bayClearWaitingHook = `set-hook -ag after-select-window 'set-window-option @bay-waiting 0'`

// bayStatusLineMarker tags the tmux status-right block bay installs.
const bayStatusLineMarker = "# Bay status line"

// bayStatusLineBlock is the canonical content bay appends to .tmux.conf.
var bayStatusLineBlock = []string{
	bayStatusLineMarker,
	"set -g status-right-length 40",
	"set -g status-right '#(bay status-line full --window #{window_id} --width #{status-right-length})'",
}

// tmuxCommand returns the tmux command this binding runs, before any
// session scoping is applied.
func (kb bayKeybinding) tmuxCommand() string {
	if kb.isTmuxCommand {
		// Raw tmux command: no shell wrapper, no "|| true" — tmux commands
		// don't fail with distracting status-line messages the way shell
		// commands do via run-shell.
		return kb.cmd
	}
	// Ensure exit 0 so keybindings are silent when bay has nothing to
	// do. tmux run-shell displays "returned N" for non-zero exits.
	return fmt.Sprintf("%s '%s || true'", kb.tmuxVerb, kb.cmd)
}

// canonicalLine returns the literal line bay would write for this binding.
func (kb bayKeybinding) canonicalLine() string {
	cmd := kb.tmuxCommand()
	if kb.scoped() {
		cmd = tmux.ScopeBinding(kb.key, cmd)
	}
	return fmt.Sprintf("bind-key %s %s %s", kb.bindFlag(), kb.key, cmd)
}

// extractBayBlock returns the bay keybindings block (everything from the
// "# Bay keybindings" marker until the first blank line or first line
// that isn't a bind-key/bind/comment) along with a flag indicating
// whether the block was found. The marker line itself is included.
func extractBayBlock(content string) (string, bool) {
	allLines := strings.Split(content, "\n")
	start := -1
	for i, l := range allLines {
		if strings.TrimSpace(l) == bayKeybindingsMarker {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	blockLines := []string{allLines[start]}
	for i := start + 1; i < len(allLines); i++ {
		trimmed := strings.TrimSpace(allLines[i])
		if trimmed == "" {
			break
		}
		if !strings.HasPrefix(trimmed, "bind-key") &&
			!strings.HasPrefix(trimmed, "bind ") &&
			!strings.HasPrefix(trimmed, "#") {
			break
		}
		blockLines = append(blockLines, allLines[i])
	}
	return strings.Join(blockLines, "\n"), true
}

// parsedBind is one tmux bind line broken into the pieces bay
// reconciles against its canonical table.
type parsedBind struct {
	table  string // "" = root table (`-n`); else the `-T <table>` name
	key    string
	cmd    string // command the binding runs, with bay's session scoping unwrapped
	tail   string // whole tmux command after the key, scoping unwrapped, nothing extracted
	scoped bool   // the line guards the command with bay's session-scope conditional
}

func (pb parsedBind) id() string { return bindID(pb.table, pb.key) }

// parseBindLine breaks a tmux bind line into its parts. Handles both
// active lines and commented lines (leading `#`), and recognizes both
// root bindings (`-n`) and key-table bindings (`-T <table>`). Returns
// ok=false if the line isn't a bind-key/bind binding we can parse.
//
// A scoped binding's conditional is unwrapped before the command is
// read, so cmd means the same thing either way — what the key does in
// a bay-managed session. Callers that care about the guard itself read
// `scoped`.
//
// For bay-invoking bindings the returned command is the quoted shell
// contents (e.g. `bay new -q || true`); for tmux-native bindings
// it's the quoted region when there is one (a display-message hint) and
// otherwise the raw tmux command (e.g. `previous-window`). `tail` keeps
// the command whole, without that extraction, for callers that need to
// recognize a line bay wrote rather than the command inside it.
func parseBindLine(line string) (pb parsedBind, ok bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
	}
	if !strings.HasPrefix(trimmed, "bind-key") && !strings.HasPrefix(trimmed, "bind ") {
		return parsedBind{}, false
	}
	// Pull off "bind-key" / "bind", then "-n" or "-T <table>", leaving
	// the key + tmux command tail. We use SplitN with a small bound so
	// the tail (which may itself contain spaces and quoted strings)
	// stays in a single piece.
	parts := strings.SplitN(trimmed, " ", 2)
	if len(parts) < 2 {
		return parsedBind{}, false
	}
	tail := strings.TrimLeft(parts[1], " ")
	switch {
	case strings.HasPrefix(tail, "-n "):
		tail = strings.TrimLeft(tail[len("-n "):], " ")
	case strings.HasPrefix(tail, "-T "):
		tail = strings.TrimLeft(tail[len("-T "):], " ")
		tParts := strings.SplitN(tail, " ", 2)
		if len(tParts) < 2 {
			return parsedBind{}, false
		}
		pb.table = tParts[0]
		tail = strings.TrimLeft(tParts[1], " ")
	default:
		return parsedBind{}, false
	}
	keyParts := strings.SplitN(tail, " ", 2)
	if len(keyParts) < 2 {
		return parsedBind{}, false
	}
	pb.key = keyParts[0]
	rest := strings.TrimLeft(keyParts[1], " ")
	if then, isScoped := tmux.UnwrapScope(rest); isScoped {
		pb.scoped = true
		rest = then
	}
	pb.tail = rest
	pb.cmd = rest
	if open := strings.IndexAny(rest, "'\""); open >= 0 {
		close := strings.LastIndexByte(rest, rest[open])
		if close > open {
			pb.cmd = rest[open+1 : close]
		}
	}
	return pb, true
}

// commandsInBlock returns the set of commands found on any active
// (non-comment) bind-key/bind line in the block.
func commandsInBlock(block string) map[string]bool {
	cmds := map[string]bool{}
	for _, l := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if pb, ok := parseBindLine(l); ok {
			cmds[pb.cmd] = true
		}
	}
	return cmds
}

// commentedCommandsInBlock returns the set of commands found on commented-
// out bind lines in the block.
func commentedCommandsInBlock(block string) map[string]bool {
	cmds := map[string]bool{}
	for _, l := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(l)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		if pb, ok := parseBindLine(l); ok {
			cmds[pb.cmd] = true
		}
	}
	return cmds
}

// activeKeysInBlock returns the set of binding identities (table+key,
// see bindID) bound on active (non-comment) bind-key/bind lines in the
// block.
func activeKeysInBlock(block string) map[string]bool {
	ids := map[string]bool{}
	for _, l := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if pb, ok := parseBindLine(l); ok {
			ids[pb.id()] = true
		}
	}
	return ids
}

// commentedKeysInBlock returns the set of binding identities from
// commented-out bind lines. Bay treats these as opt-out signals: a user
// who removed a binding and left a commented stub is saying "don't
// re-add this."
func commentedKeysInBlock(block string) map[string]bool {
	ids := map[string]bool{}
	for _, l := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(l)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		if pb, ok := parseBindLine(l); ok {
			ids[pb.id()] = true
		}
	}
	return ids
}

// activeBindings returns the map of binding identity → parsed binding
// for every active (non-comment) bind line in the block. Keys are the
// composite (table, key) identity from bindID so a chord-table letter
// doesn't collide with the same letter at the root.
func activeBindings(block string) map[string]parsedBind {
	out := map[string]parsedBind{}
	for _, l := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if pb, ok := parseBindLine(l); ok {
			out[pb.id()] = pb
		}
	}
	return out
}

// keptKeys returns the set of keys the user has pinned via
// `# bay-keep: KEY [KEY...]` comments. Those keys are exempt from the
// mismatch check so bay stops re-asking about a non-canonical binding
// the user has decided to keep.
func keptKeys(block string) map[string]bool {
	keys := map[string]bool{}
	for _, l := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(l)
		rest, ok := strings.CutPrefix(trimmed, "# bay-keep:")
		if !ok {
			continue
		}
		for _, k := range strings.Fields(rest) {
			keys[k] = true
		}
	}
	return keys
}

// conflictingKeys returns the canonical keys that are already actively
// bound in content. Walks lines and skips blanks and comments, so a
// commented-out binding does not count as a conflict. Returns
// human-facing labels (key for root bindings, "table:key" for chord
// sub-tables) so the warning text identifies which slot collided.
func conflictingKeys(content string, kbs []bayKeybinding) []string {
	canonical := map[string]bool{}
	for _, kb := range kbs {
		canonical[kb.id()] = true
	}
	bound := map[string]bool{}
	for _, l := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		pb, ok := parseBindLine(l)
		if !ok {
			continue
		}
		if canonical[pb.id()] {
			bound[pb.id()] = true
		}
	}
	var conflicts []string
	for _, kb := range kbs {
		if bound[kb.id()] {
			conflicts = append(conflicts, kb.id())
		}
	}
	return conflicts
}

// missingCanonicalLines returns the canonical bind-key lines for any
// command in kbs that is not bound in the block. Diff is by bay
// command prefix (ignoring shell suffixes like `|| true`), not by
// key, so a user who rebound a bay command to a different key is not
// flagged as missing it. Commands present only as commented-out
// bindings are ALSO treated as bound — a commented stub is an opt-out
// signal from the user ("I removed this on purpose; don't re-add").
func missingCanonicalLines(block string, kbs []bayKeybinding) []string {
	missing := missingBindings(block, kbs)
	lines := make([]string, 0, len(missing))
	for _, kb := range missing {
		lines = append(lines, kb.canonicalLine())
	}
	return lines
}

// missingBindings returns bay keybindings that aren't bound in the
// block. Matching is primarily by key:
//
//   - If the canonical key is bound (active or commented) → present.
//   - Else, if the canonical command is UNIQUE within kbs AND appears
//     bound at a different key (active or commented) → present. This
//     preserves rebind tolerance for bindings with a 1:1 key↔command
//     mapping (e.g. M-c → `bay new -q`).
//   - Else → missing.
//
// The uniqueness guard matters because bay ships intentional command-
// duplicates for ergonomics (M-j and M-J both run `select-pane -D`,
// so Shift-held chords don't lose the modifier). For duplicated
// commands, the command-match path is ambiguous, so we fall through
// to key-only matching — otherwise bay would silently consider M-j
// "present" whenever M-J is bound (and vice versa).
func missingBindings(block string, kbs []bayKeybinding) []bayKeybinding {
	activeKeys := activeKeysInBlock(block)
	commentedKeys := commentedKeysInBlock(block)
	activeCmds := commandsInBlock(block)
	commentedCmds := commentedCommandsInBlock(block)

	cmdCount := map[string]int{}
	for _, kb := range kbs {
		cmdCount[kb.cmd]++
	}

	var missing []bayKeybinding
	for _, kb := range kbs {
		if activeKeys[kb.id()] || commentedKeys[kb.id()] {
			continue
		}
		if cmdCount[kb.cmd] == 1 {
			if commandMatches(activeCmds, kb.cmd) || commandMatches(commentedCmds, kb.cmd) {
				continue
			}
		}
		missing = append(missing, kb)
	}
	return missing
}

// commandMatches returns true if want is present in cmds, either
// exactly or as a prefix of a longer string (to tolerate shell
// suffixes like `|| true` on bay-invoking bindings).
func commandMatches(cmds map[string]bool, want string) bool {
	for cmd := range cmds {
		if cmd == want || strings.HasPrefix(cmd, want+" ") {
			return true
		}
	}
	return false
}

// previousCommandMatches returns true only for the exact old command,
// allowing the shell suffix bay adds to tmux bindings. Unlike
// commandMatches, it must not prefix-match arguments because a user
// binding like `bay palette --split window` is a custom choice, not
// the old bare `bay palette` default.
func previousCommandMatches(cmd, previous string) bool {
	return cmd == previous || strings.HasPrefix(cmd, previous+" ||")
}

func canonicalCommandMatches(cmd, canonical string) bool {
	return cmd == canonical || strings.HasPrefix(cmd, canonical+" ||")
}

// bayWroteCommandTail reports whether tail — a bind line's whole command,
// scope guard already unwrapped — is what bay itself writes for kb.
//
// The comparison is on the exact bytes, not the command extracted out of
// the quoting, because this is the gate on rewriting a user's line: only
// a line bay produced verbatim may be migrated in place. `bay shell
// --pane` run through a different tmux verb, or a bay command a user
// edited, is theirs to keep.
//
// Two spellings count as bay's: the tail as written on a pre-scoping
// line, and the same command as it appears inside a scope guard, where
// ScopeBinding rewrites `\;` to `;` (inside `{ }` tmux lexes a bare `;`
// as the command separator).
func bayWroteCommandTail(tail string, kb bayKeybinding) bool {
	want := kb.tmuxCommand()
	return tail == want || tail == strings.ReplaceAll(want, `\;`, ";")
}

// bayKeybindingMismatch records a canonical key whose active binding
// has drifted from what bay ships — the silent-drift case that appears
// when bay ships a canonical change and the user hasn't re-run setup.
// Two kinds of drift: the key runs a bay command that exists in the
// canonical set but isn't the canonical one for that key, or it runs
// bay's own line for that key with the wrong session scoping.
type bayKeybindingMismatch struct {
	canonical bayKeybinding
	userCmd   string
	scoping   bool // drift is the session scoping, not the command
}

// mismatchedBindings returns canonical kbs whose key is actively bound
// to a non-canonical-for-that-key bay command, or bound with the wrong
// session scoping. Keys pinned via `# bay-keep:` are skipped, which is
// also how a user keeps a bay binding global on purpose; truly-missing
// keys are left for missingBindings. We only flag command drift
// against bay's own canonical set, and scoping drift only on a line bay
// wrote verbatim, so unrelated user rebinds aren't touched.
func mismatchedBindings(block string, kbs []bayKeybinding) []bayKeybindingMismatch {
	active := activeBindings(block)
	kept := keptKeys(block)
	canonicalCmds := map[string]bool{}
	for _, kb := range kbs {
		canonicalCmds[kb.cmd] = true
	}

	var out []bayKeybindingMismatch
	for _, kb := range kbs {
		if kept[kb.id()] {
			continue
		}
		bound, ok := active[kb.id()]
		if !ok {
			continue
		}
		// Scope migration applies only to lines bay wrote itself. A
		// binding installed before bay scoped its keys carries the
		// canonical command verbatim and runs it in every session;
		// rewriting the line is what confines it to bay's own. Anything
		// else on the key — a user's own command, or a bay command they
		// edited — is command drift at most, and the checks below decide
		// that. Testing the command first matters: a scoping prompt says
		// nothing about the command, but accepting it rewrites the whole
		// line, so it must never be offered for a line bay didn't write.
		if bayWroteCommandTail(bound.tail, kb) {
			if bound.scoped != kb.scoped() {
				out = append(out, bayKeybindingMismatch{canonical: kb, userCmd: bound.cmd, scoping: true})
			}
			continue
		}
		userCmd := bound.cmd
		if canonicalCommandMatches(userCmd, kb.cmd) {
			continue
		}
		replacesPrevious := false
		for _, prev := range kb.previousCmds {
			if previousCommandMatches(userCmd, prev) {
				out = append(out, bayKeybindingMismatch{canonical: kb, userCmd: userCmd})
				replacesPrevious = true
				break
			}
		}
		if replacesPrevious {
			continue
		}
		for c := range canonicalCmds {
			if c == kb.cmd {
				continue
			}
			if commandMatches(map[string]bool{userCmd: true}, c) {
				out = append(out, bayKeybindingMismatch{canonical: kb, userCmd: userCmd})
				break
			}
		}
	}
	return out
}

// replaceBindingInBlock rewrites the first active bind line in the bay
// block that matches kb's identity (table+key) with kb's canonical
// line. No-op if the block or matching binding isn't found.
func replaceBindingInBlock(content string, kb bayKeybinding) string {
	block, found := extractBayBlock(content)
	if !found {
		return content
	}
	lines := strings.Split(block, "\n")
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		pb, ok := parseBindLine(l)
		if !ok || pb.id() != kb.id() {
			continue
		}
		lines[i] = kb.canonicalLine()
		break
	}
	return strings.Replace(content, block, strings.Join(lines, "\n"), 1)
}

// appendToBayBlock inserts newLines at the end of the existing bay
// keybindings block in content. Returns content unchanged if no block
// is present (installKeybindings guards this before calling).
func appendToBayBlock(content string, newLines []string) string {
	block, found := extractBayBlock(content)
	if !found || len(newLines) == 0 {
		return content
	}
	updated := block + "\n" + strings.Join(newLines, "\n")
	return strings.Replace(content, block, updated, 1)
}

// promptMissingBindings returns true when active bindings were added,
// signaling installKeybindings to offer a tmux reload. Comment-stub
// writes return false: tmux ignores them, so reloading would be a
// surprising prompt with nothing to apply.
func promptMissingBindings(reader *bufio.Reader, tmuxConf, content string, missing []bayKeybinding) bool {
	fmt.Printf("%d canonical binding(s) not bound in your block:\n", len(missing))
	for _, kb := range missing {
		fmt.Printf("  %s\n", kb.canonicalLine())
	}
	fmt.Print("Add these to your block? [Y/n/c] (y=add, n=skip once, c=add commented stubs so bay stops asking) ")
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	switch answer {
	case "n":
		fmt.Println("Leaving block as-is. Bay will ask again on next setup run.")
		return false
	case "c":
		var stubs []string
		for _, kb := range missing {
			stubs = append(stubs, "# "+kb.canonicalLine())
		}
		if err := os.WriteFile(tmuxConf, []byte(appendToBayBlock(content, stubs)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not write %s: %v\n", tmuxConf, err)
			return false
		}
		fmt.Printf("Added %d commented stub(s) to %s — bay won't re-ask about these.\n", len(stubs), tmuxConf)
		return false
	default: // "" (default) or "y"
		var lines []string
		for _, kb := range missing {
			lines = append(lines, kb.canonicalLine())
		}
		if err := os.WriteFile(tmuxConf, []byte(appendToBayBlock(content, lines)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not write %s: %v\n", tmuxConf, err)
			return false
		}
		fmt.Printf("Added %d binding(s) to %s\n", len(lines), tmuxConf)
		return true
	}
}

// promptMismatchedBindings returns true when active bindings were
// rewritten to canonical, signaling installKeybindings to offer a tmux
// reload. The bay-keep marker write returns false because no active
// binding actually changed.
func promptMismatchedBindings(reader *bufio.Reader, tmuxConf, content string, mismatches []bayKeybindingMismatch) bool {
	fmt.Printf("%d binding(s) in your block don't match the current canonical:\n", len(mismatches))
	for _, m := range mismatches {
		switch {
		case m.scoping && m.canonical.scoped():
			fmt.Printf("  %s fires in every tmux session; canonical limits it to bay-managed ones\n", m.canonical.id())
		case m.scoping:
			fmt.Printf("  %s is limited to bay-managed sessions; canonical keeps it global\n", m.canonical.id())
		default:
			fmt.Printf("  %s is bound to `%s`; canonical: `%s`\n",
				m.canonical.id(), m.userCmd, m.canonical.cmd)
		}
	}
	fmt.Print("Update these to canonical? [Y/n/k] (y=update, n=skip once, k=keep yours so bay stops asking) ")
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	switch answer {
	case "n":
		fmt.Println("Leaving block as-is. Bay will ask again on next setup run.")
		return false
	case "k":
		keys := make([]string, 0, len(mismatches))
		for _, m := range mismatches {
			keys = append(keys, m.canonical.id())
		}
		marker := "# bay-keep: " + strings.Join(keys, " ")
		if err := os.WriteFile(tmuxConf, []byte(appendToBayBlock(content, []string{marker})), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not write %s: %v\n", tmuxConf, err)
			return false
		}
		fmt.Printf("Pinned %d binding(s) with bay-keep marker — bay won't re-ask.\n", len(keys))
		return false
	default: // "" (default) or "y"
		updated := content
		for _, m := range mismatches {
			updated = replaceBindingInBlock(updated, m.canonical)
		}
		if err := os.WriteFile(tmuxConf, []byte(updated), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not write %s: %v\n", tmuxConf, err)
			return false
		}
		fmt.Printf("Updated %d binding(s) in %s\n", len(mismatches), tmuxConf)
		return true
	}
}

// loadTmuxConf returns the user's ~/.tmux.conf path and current contents.
// Missing files are reported as empty content with no error so callers can
// initialize a fresh file by writing.
func loadTmuxConf() (path, content string) {
	home, _ := os.UserHomeDir()
	path = filepath.Join(home, ".tmux.conf")
	existing, _ := os.ReadFile(path)
	return path, string(existing)
}

// hasMarkerLine reports whether content contains a line whose trimmed text
// equals marker exactly. Used by the bay block installers as an
// idempotency guard that ignores incidental marker text inside comments
// or strings elsewhere in the file.
func hasMarkerLine(content, marker string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == marker {
			return true
		}
	}
	return false
}

func installKeybindings(reader *bufio.Reader) {
	fmt.Println()

	tmuxConf, content := loadTmuxConf()

	// If a "# Bay keybindings" block already exists, keep the user's
	// customizations but offer to reconcile drift. Two passes: missing
	// canonical bindings (key never bound) and mismatched bindings
	// (canonical key bound to a non-canonical bay command — silent
	// drift after a canonical change). Opt-outs: commented stubs
	// suppress "missing"; `# bay-keep: KEY` suppresses "mismatched".
	if block, found := extractBayBlock(content); found {
		fmt.Printf("Bay keybindings block found in %s.\n", tmuxConf)

		changed := false
		if missing := missingBindings(block, bayKeybindings); len(missing) > 0 {
			if promptMissingBindings(reader, tmuxConf, content, missing) {
				changed = true
			}
			_, content = loadTmuxConf()
			block, _ = extractBayBlock(content)
		}

		if mismatches := mismatchedBindings(block, bayKeybindings); len(mismatches) > 0 {
			if promptMismatchedBindings(reader, tmuxConf, content, mismatches) {
				changed = true
			}
		}
		if changed {
			promptTmuxReload(reader, tmuxConf)
		}
		return
	}

	// No existing block. Check for conflicts on canonical keys.
	conflicts := conflictingKeys(content, bayKeybindings)

	if len(conflicts) > 0 {
		fmt.Printf("Warning: these keys are already bound in %s: %s\n", tmuxConf, strings.Join(conflicts, ", "))
		fmt.Println("Bay will add its bindings, but yours will take precedence if they appear later in the file.")
		fmt.Println("Consider removing the conflicting bindings or choosing different keys.")
		fmt.Println()
	}

	// Show what we'll add.
	fmt.Printf("These tmux keybindings will be added to %s:\n\n", tmuxConf)
	for _, kb := range bayKeybindings {
		conflict := ""
		for _, c := range conflicts {
			if c == kb.key {
				conflict = " (conflicts with existing binding)"
				break
			}
		}
		fmt.Printf("  %s%s\n", kb.canonicalLine(), conflict)
		fmt.Printf("    %s\n\n", kb.desc)
	}
	fmt.Print("Add these keybindings? [Y/n] ")
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) == "n" {
		return
	}

	// Append the canonical block. We returned early above if a block
	// already existed, so there's nothing to strip.
	newBlock := "\n" + bayKeybindingsMarker + "\n"
	for _, kb := range bayKeybindings {
		newBlock += kb.canonicalLine() + "\n"
	}
	if err := os.WriteFile(tmuxConf, []byte(content+newBlock), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write %s: %v\n", tmuxConf, err)
		return
	}

	fmt.Println("Added.")
	promptTmuxReload(reader, tmuxConf)
}

// tmuxServerRunning reports whether a tmux server is reachable. We use
// this to gate the reload prompt — there's nothing to reload if no
// server is up, and asking the user would just be noise.
func tmuxServerRunning() bool {
	return exec.Command("tmux", "list-sessions").Run() == nil
}

// promptTmuxReload offers to source ~/.tmux.conf so newly written
// bindings take effect in the running tmux server. No-op when no
// server is running: nothing to reload, and prompting would be
// confusing.
func promptTmuxReload(reader *bufio.Reader, tmuxConf string) {
	if !tmuxServerRunning() {
		return
	}
	fmt.Printf("Reload %s now? [Y/n] ", tmuxConf)
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) == "n" {
		fmt.Printf("Skipped. Run `tmux source-file %s` when you're ready.\n", tmuxConf)
		return
	}
	if err := exec.Command("tmux", "source-file", tmuxConf).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: tmux source-file failed: %v\n", err)
		return
	}
	fmt.Println("Reloaded.")
}

// hasUserStatusRight reports whether content contains an active
// (non-commented) `status-right` setter — distinct from `status-right-length`
// or `status-right-style`. Used to avoid clobbering the user's existing
// status line config.
func hasUserStatusRight(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "set", "set-option", "setw", "set-window-option":
		default:
			continue
		}
		// First non-flag token after the setter is the option name.
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") {
				continue
			}
			if f == "status-right" {
				return true
			}
			break
		}
	}
	return false
}

// installStatusRight offers to add bay's status-line config to the user's
// .tmux.conf. The format string passes #{window_id} so each tmux window
// gets its own #() cache entry (avoids stale status across window
// switches), and #{status-right-length} so bay can adapt the rendered
// text to whatever space is available.
//
// Idempotent — if the bay block marker is already present on its own
// line, the function returns without prompting. If a user-defined
// status-right already exists (no bay marker), the function prints the
// suggested lines and skips, letting the user merge manually.
func installStatusRight(reader *bufio.Reader) {
	fmt.Println()

	tmuxConf, content := loadTmuxConf()

	if hasMarkerLine(content, bayStatusLineMarker) {
		fmt.Printf("Bay status line already configured in %s.\n", tmuxConf)
		return
	}

	if hasUserStatusRight(content) {
		fmt.Printf("Note: %s already sets status-right; bay won't overwrite it.\n", tmuxConf)
		fmt.Println("To use bay's status line, replace your existing status-right with:")
		for _, line := range bayStatusLineBlock[1:] {
			fmt.Printf("  %s\n", line)
		}
		return
	}

	fmt.Printf("These tmux status-line settings will be added to %s:\n\n", tmuxConf)
	for _, line := range bayStatusLineBlock {
		fmt.Printf("  %s\n", line)
	}
	fmt.Println()
	fmt.Print("Add these settings? [Y/n] ")
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) == "n" {
		return
	}

	newBlock := "\n" + strings.Join(bayStatusLineBlock, "\n") + "\n"
	if err := os.WriteFile(tmuxConf, []byte(content+newBlock), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write %s: %v\n", tmuxConf, err)
		return
	}
	fmt.Println("Added.")
}

func configureAgent(reader *bufio.Reader, configPath string) {
	fmt.Println()

	cfg, err := config.Load(configPath)
	if err != nil {
		return
	}

	current := cfg.DefaultAgent
	if current == "" {
		current = probeAgent()
	}

	if current != "" {
		fmt.Printf("Set default agent for 'bay agent' (common options: claude, codex, antigravity)\n")
		fmt.Printf("Agent [%s]: ", current)
	} else {
		fmt.Println("Set default agent for 'bay agent' (common options: claude, codex, antigravity)")
		fmt.Print("Agent: ")
	}

	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(answer)

	if answer == "" && current != "" {
		// Accept the probed default.
		answer = current
	}
	if answer == "" {
		return
	}

	cfg.DefaultAgent = answer
	if err := config.Save(configPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save agent setting: %v\n", err)
		return
	}
	fmt.Printf("Default agent set to %q\n", answer)
}

func configureEditor(reader *bufio.Reader, configPath string) {
	fmt.Println()

	cfg, err := config.Load(configPath)
	if err != nil {
		return
	}
	current, _ := resolveEditor(cfg)

	if current != "" {
		fmt.Printf("Set default editor for 'bay edit' (common options: cursor, code, zed, nvim, vim)\n")
		fmt.Printf("Editor [%s]: ", current)
	} else {
		fmt.Println("Set default editor for 'bay edit' (common options: cursor, code, zed, nvim, vim)")
		fmt.Print("Editor: ")
	}

	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(answer)

	if answer == "" {
		return
	}

	cfg.DefaultEditor = answer
	if err := config.Save(configPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save editor setting: %v\n", err)
		return
	}
	fmt.Printf("Editor set to %q\n", answer)
}

// configureAutoDescriptions offers to enable the auto-description
// backstop. It's off by default (it spends model calls), so setup is
// where we surface it and suggest turning it on.
func configureAutoDescriptions(reader *bufio.Reader, configPath string) {
	fmt.Println()

	cfg, err := config.Load(configPath)
	if err != nil {
		return
	}

	fmt.Println("Auto-descriptions (optional): bay can summarize each bay's agent")
	fmt.Println("conversation into its description in the background, so 'bay ls' and the")
	fmt.Println("picker stay meaningful without manual labeling. It runs a summarizer CLI")
	fmt.Println("periodically (default: codex exec with a cheap model), spending model")
	fmt.Println("calls on your account. Change or disable it anytime via [describe] in config.")

	_, codexErr := exec.LookPath("codex")

	// Default suggestion: on if the default summarizer is available, or
	// whatever was already chosen explicitly.
	suggestOn := codexErr == nil
	if cfg.Describe.Enabled != nil {
		suggestOn = *cfg.Describe.Enabled
	}
	if codexErr != nil && (cfg.Describe.Enabled == nil || len(cfg.Describe.Command) == 0) {
		fmt.Println("(Note: the default summarizer 'codex' isn't on your PATH — enabling")
		fmt.Println(" needs it installed, or set [describe].command to another CLI.)")
	}

	prompt := "Enable auto-descriptions? [y/N]: "
	if suggestOn {
		prompt = "Enable auto-descriptions? [Y/n]: "
	}
	fmt.Print(prompt)

	answer, _ := reader.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))

	enable := suggestOn
	switch answer {
	case "y", "yes":
		enable = true
	case "n", "no":
		enable = false
	}

	cfg.Describe.Enabled = &enable
	if err := config.Save(configPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not save auto-descriptions setting: %v\n", err)
		return
	}
	if enable {
		fmt.Println("Auto-descriptions enabled.")
	} else {
		fmt.Println("Auto-descriptions disabled.")
	}
}

const baySkillContent = `---
name: bay
description: Bay management — git worktrees and tmux windows. Use when creating, managing, or navigating bays, docks, or surfaces.
---

!` + "`bay agent-guide`" + `
`

// installClaudeHooks adds a PermissionRequest hook to Claude Code's
// settings.json that sends a terminal bell, so tmux highlights the tab
// when Claude needs permission. Merges with existing settings.
func installClaudeHooks(reader *bufio.Reader) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")

	const bellCommand = `[ -n "$TMUX" ] && printf '\a'`

	if claudeStyleHookConfigured(settingsPath, "PermissionRequest") {
		fmt.Printf("Claude Code PermissionRequest hook already configured in %s\n", settingsPath)
		return
	}

	fmt.Println()
	fmt.Println("Enable tmux tab highlighting when Claude needs permission?")
	fmt.Printf("This adds a PermissionRequest hook to %s that runs:\n", settingsPath)
	fmt.Printf("    %s\n", bellCommand)
	fmt.Println("Sends a terminal bell so tmux flags the tab; Option+R jumps to it.")
	fmt.Print("Enable? [Y/n] ")
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) == "n" {
		return
	}

	if err := writeClaudeStyleHook(settingsPath, "PermissionRequest", bellCommand); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		return
	}
	fmt.Printf("Claude Code PermissionRequest hook installed in %s\n", settingsPath)
}

// agentReadyCommand is the shell snippet every supported agent runs at
// turn-complete. Sets @bay-waiting on the current tmux window so Option-R
// finds it. The after-select-window hook (also installed by setup) clears
// the flag on focus.
const agentReadyCommand = `[ -n "$TMUX" ] && tmux set-window-option -t "$TMUX_PANE" @bay-waiting 1 2>/dev/null`

// antigravityBayHookName is the key for bay's named hook in
// Antigravity's hooks.json. A Stop hook's stdout JSON is optional —
// emitting nothing lets the agent stop normally — so the plain
// agentReadyCommand (no decision payload) is all that's needed.
const antigravityBayHookName = "bay-ready"

// agentHookSpec describes how to install a turn-complete hook for one
// supported agent. Keyed by canonical agent name (matches config.KnownAgents).
type agentHookSpec struct {
	label      string
	relPath    string
	configured func(path string) bool
	write      func(path string) error
}

// agentHookSpecs lists agents whose turn-complete event bay knows how
// to wire. Iterated against config.KnownAgents — agents that aren't in
// KnownAgents are ignored, and KnownAgents entries with no spec here
// are silently skipped (e.g., a future agent without a hook system).
var agentHookSpecs = map[string]agentHookSpec{
	"claude": {
		label:      "Claude Stop hook",
		relPath:    ".claude/settings.json",
		configured: func(p string) bool { return claudeStyleHookConfigured(p, "Stop") },
		write:      func(p string) error { return writeClaudeStyleHook(p, "Stop", agentReadyCommand) },
	},
	"antigravity": {
		label:      "Antigravity Stop hook",
		relPath:    ".gemini/config/hooks.json",
		configured: antigravityHookConfigured,
		write:      func(p string) error { return writeAntigravityHook(p, agentReadyCommand) },
	},
	"codex": {
		label:      "Codex notify entry",
		relPath:    ".codex/config.toml",
		configured: codexNotifyConfigured,
		write:      writeCodexNotify,
	},
}

// installReadySignaling bundles the turn-complete signal: hooks for any
// supported agent on PATH plus the tmux auto-clear hook. One prompt,
// all-or-nothing — the pieces only work as a unit (without auto-clear,
// flags get sticky and Option-R keeps landing on visited bays).
// Each piece is independently idempotent, so repeat runs are safe.
func installReadySignaling(reader *bufio.Reader) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	type item struct {
		label string
		write func() error
	}
	var pending []item

	// Iterate KnownAgents in sorted order so the prompt's bullet list is
	// stable across runs (Go map iteration is non-deterministic).
	for _, name := range config.KnownAgentNames() {
		spec, ok := agentHookSpecs[name]
		if !ok {
			continue
		}
		if _, err := exec.LookPath(config.KnownAgents[name].Command); err != nil {
			continue
		}
		path := filepath.Join(home, spec.relPath)
		if spec.configured(path) {
			continue
		}
		pending = append(pending, item{
			label: spec.label + " → " + path,
			write: func() error { return spec.write(path) },
		})
	}

	tmuxPath, tmuxContent := loadTmuxConf()
	if !hasMarkerLine(tmuxContent, bayHooksMarker) {
		pending = append(pending, item{
			label: "tmux auto-clear hook → " + tmuxPath,
			write: writeTmuxClearHook,
		})
	}

	if len(pending) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Install agent-ready signaling? Sets a tmux flag bay reads for Option+R when an agent finishes a turn.")
	for _, it := range pending {
		fmt.Printf("  - %s\n", it.label)
	}
	fmt.Print("Install? [Y/n] ")
	answer, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) == "n" {
		return
	}

	for _, it := range pending {
		if err := it.write(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		}
	}
	fmt.Println("Installed.")
}

// claudeStyleHookConfigured reports whether settings.json has any entry
// under hooks[eventName].
func claudeStyleHookConfigured(settingsPath, eventName string) bool {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = hooks[eventName]
	return ok
}

// writeClaudeStyleHook merges a single hook event into settings.json,
// preserving any existing top-level fields and other hook events.
func writeClaudeStyleHook(settingsPath, eventName, command string) error {
	var settings map[string]any
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse %s: %w", settingsPath, err)
		}
	} else {
		settings = make(map[string]any)
	}

	hookEntry := []any{
		map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": command},
			},
		},
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = make(map[string]any)
	}
	hooks[eventName] = hookEntry
	settings["hooks"] = hooks

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(settingsPath, append(data, '\n'), 0o644)
}

// antigravityHookConfigured reports whether Antigravity's hooks.json
// already carries bay's named Stop hook. Antigravity loads hooks from
// hooks.json files (not settings.json) as a top-level map of named
// hooks, each keyed by event — there is no "hooks" wrapper object.
func antigravityHookConfigured(hooksPath string) bool {
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		return false
	}
	var hooks map[string]any
	if err := json.Unmarshal(data, &hooks); err != nil {
		return false
	}
	hook, ok := hooks[antigravityBayHookName].(map[string]any)
	if !ok {
		return false
	}
	_, ok = hook["Stop"]
	return ok
}

// writeAntigravityHook merges bay's named Stop hook into Antigravity's
// hooks.json, preserving any other named hooks the user has. The file
// maps a hook name to its per-event handler lists:
// {"<name>": {"Stop": [{"type": "command", "command": "..."}]}}.
func writeAntigravityHook(hooksPath, command string) error {
	hooks := map[string]any{}
	if data, err := os.ReadFile(hooksPath); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &hooks); err != nil {
			return fmt.Errorf("parse %s: %w", hooksPath, err)
		}
		if hooks == nil {
			hooks = map[string]any{}
		}
	}

	hook, ok := hooks[antigravityBayHookName].(map[string]any)
	if !ok {
		hook = make(map[string]any)
	}
	hook["Stop"] = []any{
		map[string]any{"type": "command", "command": command},
	}
	hooks[antigravityBayHookName] = hook

	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(hooksPath, append(data, '\n'), 0o644)
}

func codexNotifyConfigured(configPath string) bool {
	data, err := os.ReadFile(configPath)
	if err != nil || len(data) == 0 {
		return false
	}
	var parsed map[string]any
	if _, err := toml.Decode(string(data), &parsed); err != nil {
		return false
	}
	_, ok := parsed["notify"]
	return ok
}

func writeCodexNotify(configPath string) error {
	existing, _ := os.ReadFile(configPath)
	notifyLine := fmt.Sprintf(`notify = ["sh", "-c", %q]`, agentReadyCommand)

	// Top-level TOML keys must precede any [section] header, so prepending
	// is always syntactically safe.
	var newContent string
	if len(existing) > 0 {
		newContent = notifyLine + "\n\n" + string(existing)
	} else {
		newContent = notifyLine + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(configPath, []byte(newContent), 0o644)
}

func writeTmuxClearHook() error {
	path, content := loadTmuxConf()
	// Blank line ahead of the marker so extractBayBlock (keybindings)
	// won't slurp these lines if hooks land adjacent to the keybindings
	// block.
	block := "\n" + bayHooksMarker + "\n" + bayClearWaitingHook + "\n"
	return os.WriteFile(path, []byte(content+block), 0o644)
}

func installBaySkill() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	skillDir := filepath.Join(home, ".claude", "skills", "bay")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not create skill directory: %v\n", err)
		return
	}

	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(baySkillContent), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write skill file: %v\n", err)
		return
	}

	fmt.Printf("Claude Code skill installed at %s\n", skillPath)
}
