# Bay -- Human Guide

Bay manages concurrent bays built on git worktrees and tmux. Each
bay gets its own worktree and tmux surfaces -- so you can work on
multiple branches simultaneously without stash juggling or directory
cloning. Bay handles session recovery after reboot, launches editors
and terminal panes, detects PR merges automatically, and optionally
launches AI coding agents.

## Prerequisites

- **tmux** -- bay manages tmux sessions and surfaces. Install with
  `brew install tmux` (macOS) or your system package manager. If you're
  new to tmux, the key concept is: tmux keeps terminal sessions alive
  in the background. You can detach and reattach without losing state.
  Bay leans on this heavily. See the
  [tmux Getting Started guide](https://github.com/tmux/tmux/wiki/Getting-Started).
  For a recommended starter config, see
  [Recommended tmux setup](tmux-setup.md).

- **git** -- for worktree-based bays.

- **An AI coding agent** (optional) --
  [Claude Code](https://docs.anthropic.com/en/docs/agents-and-tools/claude-code/overview),
  [Codex](https://github.com/openai/codex), or any agent that runs in a
  terminal. Agents are opt-in per bay.

- **gh** (optional) -- the [GitHub CLI](https://cli.github.com/).
  Enables automatic PR number detection and merge status monitoring.

## Installation

```
go install github.com/commonfabric/bay/cmd/bay@latest
```

For private repos, configure Go to use SSH:
```
go env -w GOPRIVATE=github.com/commonfabric/bay
git config --global url."git@github.com:".insteadOf "https://github.com/"
```

## Quick start

### Zero-config: just run it

Bay works without a config file. Navigate to any git repo and create a
bay:

```
cd ~/projects/myproject
bay new
```

Bay auto-detects the checkout, creates a dock (tmux session), probes for an
agent on your PATH, and opens a bay. If this is your first time,
it writes a minimal config for next time.

### With setup (recommended)

Run setup once to configure keybindings, completions, and your editor:

```
bay setup
```

This creates your config file, installs tmux keybindings, sets up shell
completions, and configures your default agent and editor. If Claude Code
or Codex is installed, it also installs hooks so tmux highlights the tab
when an agent needs permission or finishes a turn — Option+r then jumps
straight to it.

### Explicit dock

For more control, create a named dock for a checkout:

```
bay dock new dev --path ~/projects/myproject --agent claude
tmux attach -t dev
bay new
```

### Create bays and surfaces

From inside a dock:
```
bay new                  # shell bay (default)
bay new auth-fix         # with a display name
bay new --branch fix-it  # checkout or create a branch
bay new --agent          # agent bay (dock default, no shell)
bay new --agent=codex    # agent bay with specific agent
bay agent                   # launch default agent as a split pane
bay agent claude            # launch a specific agent as a split pane
bay shell                   # open a shell as a split pane
bay edit                    # open the editor
bay home                    # focus/create a shell in the dock checkout
```

### Navigate

```
bay go                   # pick a bay
bay surface go           # pick a surface (agent/shell/editor)
```

Or use keybindings: Option+h/l to cycle windows, Option+g for the
bay picker.

### Check on things

```
bay ls                      # bays in current dock
bay tree                    # full hierarchy with surfaces
bay pwd                     # current bay location
```

### Clean up

```
bay close b1             # by ID; safety checks for uncommitted work
bay close --done         # close bays that are not dirty or pending
bay close --clean        # close all non-dirty bays
```

For a full interactive walkthrough, see the [Tutorial](tutorial.md).

## Concepts

### The hierarchy: Dock > Bay > Surface

Bay organizes your work in three levels:

```
Dock (tmux session)
  Bay (git worktree + metadata)
    Surface (agent pane, shell pane, editor window, command pane)
```

**Surfaces** are the unit of navigation -- anything you can focus and
jump to. You don't think in "tmux windows and panes"; you think "agent",
"editor", "shell". That is what a surface is.

### Docks

A **dock** is a named tmux session that groups related bays. You
might have a `dev` dock for one project and an `ops` dock for another.
Each dock owns one local git checkout and has optional defaults for
agent and terminal.

When a dock is created explicitly, bay opens a `home` shell in the
dock checkout. Auto-bootstrap through `bay new` creates only the
requested worktree bay. If the last non-home surface in a dock closes,
bay creates or focuses a home shell instead of leaving an empty
placeholder.

```
bay dock new dev --path ~/projects/myproject
bay dock new dev --path ~/projects/myproject --agent claude
bay dock new dev --path ~/projects/myproject --terminal ghostty
bay dock ls
bay dock show dev
bay dock close dev
bay dock close dev --force
```

### Bays

A **bay** is a working directory with metadata. Three types:

- **Worktree** -- bay creates a git worktree from the dock's checkout.
  Bay owns the lifecycle: creation, safety checks on close, cleanup.
  This is the default.
- **External** -- bay points at an existing directory you own. Bay
  manages tmux surfaces and recovery, but does not create or delete
  the directory.
- **Home** -- a reserved per-dock pseudo-bay backed by the dock's
  canonical checkout (`dock.path`). Home supports shell, agent,
  command, and editor surfaces and appears in `bay ls`, `bay tree`,
  `bay go`, and cycling while it has surfaces. Empty home stays hidden
  from normal lists and pickers. Bay never deletes the canonical
  checkout.

Each bay has three identity concepts:

- An **ID** like `b1`, `b2`, `b3` — the stable handle. Set at creation,
  never changes, unique within a dock. Every command that targets a
  bay takes the ID: `bay close b1`, `bay show b1`,
  `bay edit b1`. The ID matches the on-disk directory basename for
  worktree bays (so `~/projects/myproject-worktrees/b1` is
  bay `b1`). Older bays may still have legacy `w<N>` IDs and paths;
  those persisted IDs remain valid.
- A **Name** like `auth-fix` — an optional friendly tag. Sticky once
  set. May be empty initially; sync fills it from the branch on first
  detection (`feature/refactor-memory` becomes `refactor-memory`).
  You can rename with `bay rename`. Names are not CLI keys — typing
  one returns `did you mean "b1"?` with the canonical ID.
- An optional **description** (see below) for richer context.

Together the ID and Name produce the bay's **canonical label**:
- `home` for the home pseudo-bay,
- `<id>` when the Name is empty (e.g. `b2`),
- `<id>.<name>` when both are set (e.g. `b1.auth-fix`).

This single label is what every human surface shows — the bay picker
(M-g), tmux tabs, the status line, `bay ls`/`tree`/`show`, and
`bay pwd`. Unnamed bays render as their ID rather than blank. JSON
output keeps `id` and `name` as separate fields so scripts can use
them independently.

An ID is stable for a bay's lifetime, but the slot is released
when the bay closes — the next creation may reuse a freed
trailing slot, while gaps in the middle of the sequence (`b1`, `b3`,
`b7`) stay until you fill them.

Bays can also carry a **description** — commit-message-style
text with two parts:

- **First line**: a short (~40 character, cap 80) label shown in the
  bay picker (Option-Shift-G), `bay ls` / `bay tree`, and the
  `Option+/` flash.
- **Body** (optional): trailing lines separated from the first line by
  a blank line. Shown only in the `Option+?` popup — useful for longer
  context when you step back into a bay after working elsewhere
  ("paused mid-rebase; rename conflict on helper.ts; tests green except
  auth_test.go").

Set it with `bay describe "Login flow fixes"` (or `bay describe`).
Shells handle newlines in quoted strings, so you can pass a multi-line
body directly; or use `bay describe --edit` to open `$EDITOR`.

You may not have to set one: if you enable auto-descriptions (opt-in;
see Configuration), bay fills in empty descriptions in the background by
summarizing the bay's agent conversation (Claude or Codex). Anything you
set by hand — or that an agent sets via `bay describe` — is owned and
never overwritten; `bay describe --clear` hands a bay back to the
auto-summarizer. `bay setup` offers to turn the feature on.
Descriptions don't affect tmux tab names — tabs stay short and truncate
to fit, while descriptions give you a more human-readable hint when
you're scanning for the right bay.

In `bay ls` / `bay tree`, only the first line is shown and its length
adapts to terminal width (floored at 15 characters, capped at 80). Bay
names and branches are also capped in the human table so one long field
doesn't push the whole view sideways. Pipe the output or set
`COLUMNS=200 bay tree` to force a wider description layout; piped output
always uses the full 80-character description cap. Use `bay show`,
`bay ls --json`, or `bay tree --json` for full names and branches.

### Surfaces

A **surface** is anything you can focus and jump to within a bay.
Surfaces have a type and a backend:

| Type | What it runs |
|------|-------------|
| `agent` | An AI coding agent (Claude Code, Codex, etc.) |
| `shell` | A plain shell in the bay directory |
| `cmd` | A specific command (`npm test`, `cargo watch`, etc.) |
| `editor` | A terminal editor (nvim, vim) in its own tmux pane |

All surfaces live in tmux (panes within windows). GUI editors (Cursor,
VS Code, Zed) are launched via `bay edit` but are not tracked as
surfaces — they manage their own windows.

Surfaces within a bay that share a tmux window are in the same
**layout group**. Splitting a pane creates a surface in the same group;
`--window` creates a surface in a new group.

### Dirty and merged flags

Bays have two flags instead of a status enum:

- **dirty** (computed) -- the worktree has uncommitted changes or
  untracked files. Computed from git state on every display.
- **merged** (persisted) -- the bay's branch has been merged
  into the default branch. Set by the background monitor and persisted
  in the manifest.

Bays show `st=pending` when they have an unmerged branch (work is
out for review). `bay close --done` closes bays that are not
dirty and not pending — the truly finished ones. `bay close --clean`
is broader, closing anything not dirty. Use `--dry-run` with either to
preview what would be closed.

### Orphan cleanup

If a bay's last pane disappears — via tmux-native kill
(`Ctrl-B x`, process exit), or via `Option+W`/`bay sf close` (no
`--force`) — bay schedules the bay for auto-close after a
60-second grace window. You can rescue it during that window by
adding a surface back (`bay sf new --bay <id>`); the pending close
cancels. If you don't act, bay runs `bay close` with the normal
safety gates: clean bays whose commits are pushed or already
landed on the default branch get cleaned up; dirty bays or
bays with unlanded commits stay in place.

`bay sf close --force` and direct `bay close` are unaffected —
those are explicit user actions and close immediately.

### Last-surface close confirmation

Closing the only surface in a bay from a non-interactive
invocation, such as the `Option+W` keybinding, requires a second close
attempt to keep a stray keypress from tearing down the visible pane.
The first attempt flashes a status-line message ("press again to close
last surface in …") and leaves the surface intact; the message stays
on screen for exactly as long as the second tap is accepted, so when it
disappears the confirmation window has expired. A second `Option+W`
while the message is still up proceeds normally. Interactive
command-line invocations close the last surface on the first command.

Closes that aren't the last surface in a bay are unaffected, and
`--force` skips the double-tap entirely.

### Undo-close

`Option+z` (or `bay sf restore`) restores the most recently closed
surface in the current dock. Bay keeps a per-dock LRU queue of the
last 10 bay-initiated closes or sync-discovered pane exits, retained
for 1 hour. `bay sf close`, `Option+W`, last-surface closes, and
closed tmux panes discovered by sync all push an entry; `bay sf
restore --list` shows the queue without restoring. Restore/list run a
sync first, so an agent pane exited with `Ctrl-D` can be undone
immediately from another pane; if a known undo entry is already queued,
it stays ahead of newly discovered exits.

Restore recreates the surface in its parent bay. If the
bay has since been closed (e.g. the 60s orphan-cleanup grace
window elapsed), the entry is silently discarded — hit `Option+z`
again to reach the next entry.

Restored agent surfaces relaunch with the agent's configured
`resume_args` so the prior conversation continues rather than
starting fresh — see [Session resumption](#session-resumption).

### Identifiers and references

Bays are referenced by **ID** in commands. Bare form is `b1`;
fully qualified is `dev:b1`. The keyword `self` resolves to the
bay at your current tmux pane and working directory.

A bare ID resolves to the current dock first; if absent there, falls
through to a cross-dock search (which errors on ambiguity). Pass
`--dock <name>` to target a different dock without changing context.

Bay **Names** must match `[a-zA-Z0-9_-]+`, cannot match the
reserved generated ID patterns `^b[1-9]\d*$` or legacy
`^w[1-9]\d*$`, and cannot equal the reserved `home` handle — those
namespaces are bay's. If you type a Name where bay expects an ID, the
error names the ID for you: `bay "auth-fix" not found; did you mean
"b1"?`.

## Configuration

Bay's config lives at `~/.config/bay/config.toml`. It holds user
preferences and optional overrides — not instance state. Docks and bays
are tracked in the manifest (`~/.local/share/bay/manifest.json`).

### Zero-config behavior

No config file is needed. `bay new` auto-bootstraps: it detects the
CWD git checkout, creates a dock, probes for agents and editors on PATH,
and starts working. A config file is only needed to set preferences.

### Defaults

```toml
default_agent = "claude"
default_editor = "cursor"
```

Set via `bay setup` or `bay config editor <name>`. Built-in agents
(claude, codex, antigravity) and editors (cursor, code, zed, nvim, vim)
don't need config — bay knows their commands, resume args, and GUI
detection. Run `bay help config` for the full schema.

### Custom agents (optional)

Add agents beyond the built-in three, or add default args to a built-in:

```toml
[agents.claude]
args = ["--dangerously-skip-permissions"]      # default args for claude

[agents.my-agent]
command = "my-agent-cli"
args = ["--flag"]                              # args on every invocation
launch_args = ["--profile", "work"]            # args on fresh launch only
resume_args = "--resume"
project_file = ".my-agent.md"
```

`args` apply to every invocation, including resume (recover,
undo-close restore). `launch_args` apply only to fresh launches —
that's where session-start flags like `--model` belong. Claude Code
restores a resumed session's own model, so a `--model` replayed on
resume would override any model you switched to inside the session.

Each list element is one argument. Bay shell-quotes elements
containing spaces or other special characters, so a multi-word value
(e.g. an initial prompt) reaches the agent as a single argument;
leading-tilde paths still expand.

### Model profiles

A profile is an agent that `extends` a base agent — the same client
pinned to a model, with its own name. Four come built in, no config
needed: **`fable`**, **`opus`**, **`sonnet`**, and **`haiku`**, each
launching `claude --model <name>` (`opus` pins `opus[1m]`, the
1M-context variant). So `bay agent opus --pane` or
`bay new --agent=fable` work out of the box, and `Option+o f` /
`Option+o o` launch fable / opus in the current bay.

Profiles work everywhere an agent name does: `bay new --agent=fable`,
`bay agent fable --pane`, dock defaults (`[docks.dev] agent =
"fable"`), `default_agent`, and your own tmux keybindings (pin custom
bindings with `# bay-keep:` so `bay setup` preserves them). The
palette's agent picker labels profiles with their base client —
`fable (claude)` — to keep them visually distinct from the clients
themselves.

Add your own the same way the built-ins are defined:

```toml
[agents.fable-fast]
extends = "claude"
launch_args = ["--model", "fable", "--fast"]
```

A profile inherits the base's command, args, `resume_args`, and
`project_file`; its `args`/`launch_args` are appended to the base's,
and other fields it sets override. Only one level — a profile can't
extend another profile (including the built-in ones; extend `claude`
directly instead).

Built-in profiles are yours to adjust: a same-named config entry that
sets only args/launch_args/resume_args/project_file tweaks the
profile (fields you set win, the rest keep the profile's defaults);
one that sets `command` or `extends` replaces it entirely; and
`disabled = true` removes it:

```toml
[agents.opus]
launch_args = ["--model", "opus"]       # plain opus instead of the 1M default

[agents.haiku]
disabled = true                         # drop the built-in haiku profile
```

The base stays unpinned: a plain `bay agent claude` still starts on
whatever model Claude Code would pick itself, and resumed sessions
always continue on the model they were last using (launch args are
never replayed on resume).

### Per-dock overrides (optional)

Override the default agent or agent args for a specific dock:

```toml
[docks.dev]
agent = "codex"                                # override default agent
terminal = "ghostty"                           # host terminal app

[docks.dev.agent_args]
codex = ["--full-auto"]                        # per-dock agent args override

[docks.dev.launch_agent_args]
codex = ["--model", "o3"]                      # per-dock launch-only args
```

### Auto-descriptions (optional)

Bay can fill in empty bay descriptions automatically, summarizing the
bay's agent conversation (Claude and Codex transcripts) on a slow
background cadence run by the monitor. It never overwrites a description
you (or an agent) set by hand.

It's **off by default** — it spends model calls and assumes a summarizer
CLI you have — so it's opt-in. `bay setup` offers to turn it on; or set
it directly:

```toml
[describe]
enabled = true
# Summarizer argv. The full prompt is appended as the final argument.
# If an element is "{out}", bay substitutes a temp file and reads the
# answer from it (codex prints status chatter to stdout); otherwise it
# reads stdout. Set the model by editing the command — any CLI works.
command = ["codex", "exec", "--sandbox", "read-only", "--skip-git-repo-check",
           "--ephemeral", "-c", "model_reasoning_effort=low",
           "-m", "gpt-5.4-mini", "-o", "{out}"]
```

A plain stdout filter needs no `{out}`, e.g.
`command = ["llm", "-m", "gpt-4o-mini"]`. Leaving `command` unset uses
the codex default above. The summarizer runs as a detached background
process and never blocks `bay` commands. See
`docs/design/auto-descriptions.md` for the design.

## Agent integration

### Bay awareness

Instead of generating per-bay config files, bay uses a lightweight
awareness model. New docks run this setup automatically for their
checkout.

This does two things:
1. Writes a `CLAUDE.md` with a one-line pointer to `bay agent-guide`
   into the dock's **worktree directory**. Claude Code reads `CLAUDE.md`
   from ancestor directories, so every bay — current and future — picks
   the pointer up automatically. The file lives *outside* the worktrees,
   so it never appears in `git status` and needs no gitignore entry.
2. Appends the same pointer to each built-in agent's project file in
   the dock checkout (e.g., `CLAUDE.local.md` for Claude Code), so
   agents launched in the checkout itself know about bay too. This
   happens **only when the repo already gitignores the file** — bay
   never edits `.gitignore` or leaves files git would report as
   untracked or modified. If the file isn't gitignored, `dock init`
   skips it and prints a note; add it to `.gitignore` and re-run
   `bay dock init` to enable it.

Files that already reference `bay agent-guide` are left alone, and
project files from user-configured agents are covered the same way as
built-ins. Docks whose checkout isn't a git repository are skipped
entirely — without git there are no worktree bays to set up.
`bay doctor` flags docks missing bay awareness.

### .worktreeinclude

To copy gitignored local files (`.env`, credentials, overrides) from
the dock checkout into new worktrees, create a `.worktreeinclude` file
in the checkout. Bay reads it as a **gitignore-format** file — each line
is a pattern (globs, negations, directory rules) resolved by git itself.
At worktree creation time, bay expands the patterns and copies matching
files from the dock checkout into the new worktree.

To protect against accidentally spraying checked-in files across
worktrees, bay refuses to sync any match that is **tracked in git** or
**not covered by `.gitignore`** — only files that cannot end up in git
get copied. Refusals are printed to stderr, but the worktree is still
created and other valid matches are still copied. Fix by adding the
refused files to `.gitignore` or removing the pattern from
`.worktreeinclude`.

### Agents keep descriptions current

Bay-aware agents treat the bay description as a context-recall
aid, not just a label. Expect them to:

- Set a first-line label on start if one isn't set.
- Maintain the body as a standing brief — what this bay is
  for, where it stands now, what's the immediate next move — so
  when you return after working elsewhere, `Option+?` swaps the
  whole picture back into your head without you re-reading the
  diff. They should update when the *situation* changes, not on
  every pause.

The dock setup (above) points agents at `bay agent-guide`, which
carries the full guidance — no user-global config needed.

`bay describe` only writes bay metadata, so it's safe to allowlist
and skip the permission prompt. For Claude Code, add to
`~/.claude/settings.json`:

```json
{ "permissions": { "allow": ["Bash(bay describe:*)"] } }
```

For Codex, add to `~/.codex/rules/default.rules`:

```
prefix_rule(pattern=["bay", "describe"], decision="allow")
```

### Session resumption

Built-in agents have resume args (`--continue` for Claude Code,
`resume --last` for Codex, `--continue` for Antigravity). Bay appends
these automatically on `bay recover` and on undo-close (`Option+z` /
`bay sf restore`). All three agents bind sessions to the project
directory, so the resume picks up the right conversation when the
restored pane lands back in the same worktree.

### Agent protection

Closing agent surfaces prompts for confirmation, since agents carry
valuable conversation context.

## Command reference

### Bay management

Across all create-verbs the rule is the same: the positional names the
thing being created. Container (dock, bay) is selected via flags
or, when omitted, inferred from context — in an interactive shell the
dock of the current checkout (the repo your CWD is in) takes
precedence, falling back to the current tmux session. So `bay new`
while standing in a dock's checkout targets that dock even if you're
attached to another dock's session. Keybindings (like Option+c) are
the exception: they run via tmux `run-shell`, whose working directory
is the server's rather than yours, so they use the tmux session and
create in the dock you're looking at.

```
bay new [name]                           # new bay (positional sets the display Name)
bay new [name] --agent                   # first surface is agent, not shell
bay new [name] --agent=codex             # specific agent type
bay new [name] --dock <d>                # target a specific dock
bay new [name] --branch <b>              # checkout or create branch
bay new [name] --dir <path>              # external bay
bay new [name] --description "<text>"    # set description at creation time
bay new [name] -q                        # suppress output (scripting)
bay close <id>                           # close + delete pushed branch ('self' for current)
bay close home                           # close home surfaces if other surfaces remain; keeps checkout
bay close <id> --force                   # skip safety checks (keeps unlanded branches)
bay close self --force --tap             # Option+Shift+W form: force-close after a quick second press
bay close --done                         # close bays not dirty or pending
bay close --clean                        # close all non-dirty bays
bay close --done --dry-run               # preview what --done would close
bay tidy [id]                            # post-merge: detach to clean base + delete branch, keep the bay
bay show [id]                            # detailed view (default: current)
bay show [id] --json                     # machine-readable
bay show [id] --short                    # one-line summary (id — name — branch — #PR)
bay show [id] --short --plain            # same, no ANSI (for tmux display-message etc.)
bay show [id] --flash                    # first-line flash in status bar (Option+/)
bay show [id] --popup                    # full description in popup (Option+?)
bay rename [id] <new-name>               # rename display Name (ID is unchanged)
bay describe                             # print current description
bay describe [id] [<text>]               # set (first line + optional blank-line-separated body)
bay describe --edit                      # open $EDITOR for multi-line editing
bay describe --clear                     # clear description
bay ls                                   # list bays in current dock
bay ls --json                            # machine-readable bay list
bay tree                                 # full tree (bays + surfaces)
bay go [query]                           # bay picker (intra-dock)
bay go home                              # focus/create home shell
bay go --waiting                         # filter to waiting bays
bay go --next-waiting                    # cycle to next waiting bay
bay next                                 # next bay in dock
bay prev                                 # prev bay in dock
```

### Surface management

`bay surface new` (or `bay sf new`) has subcommands for each surface type:

```
bay surface new shell [name]               # shell as split pane (default)
bay surface new agent [type] [name]        # agent as split pane
bay surface new cmd "<command>" [name]     # command as split pane
bay surface new edit [id]                  # editor surface as split pane
bay surface new shell [name] --window      # shell in a new tmux window
bay surface new shell [name] --split h     # horizontal split
bay surface new shell [name] --bay <id>    # target a different bay
bay surface new cmd "git pull --ff-only" --bay home  # command in dock checkout
bay surface close <name>                   # close a surface (prompts on agents)
bay surface close <name> --force           # skip close confirmations
bay surface restore                        # restore the most recently closed surface
bay surface restore --list                 # show the undo-close queue
bay surface ls                             # list surfaces in current bay
bay surface ls --json                      # machine-readable surface list
bay surface show [name]                    # show details (defaults to current)
bay surface rename [old] <new>             # rename (defaults to current surface)
bay surface go [query]                     # surface picker (intra-bay)
bay surface go --next-waiting              # next waiting surface
bay surface next                           # next surface in bay
bay surface prev                           # prev surface in bay
```

`sf` is an alias for `surface`:
```
bay sf new shell
bay sf close shell-2
```

### Top-level shortcuts

Common surface shortcuts:

```
bay shell [name]                # shell as split pane (default)
bay shell [name] --window       # shell in new window
bay shell --bay home            # shell in dock checkout
bay agent [type]                # agent as split pane
bay agent --window              # agent in new window
bay agent codex --bay home      # agent in dock checkout
bay edit [id]                   # open bay in editor (default)
bay edit --bay home             # edit dock checkout
bay edit --editor vim           # use a specific editor this time
bay edit --window               # editor in new window
bay edit --dock                 # dock editor (all bays)
bay home                        # focus/create dock checkout shell
bay restore                     # alias for bay surface restore (undo-close)
bay rename [id] <new-name>      # rename bay (defaults to current)
```

### Config

```
bay config                      # show config-format docs (long help)
bay config edit                 # open ~/.config/bay/config.toml in your editor
bay config show                 # print the effective config
bay config path                 # print the config file path
bay config editor               # show the resolved editor command
bay config editor <name>        # set the default editor (e.g. cursor, code, nvim)
```

### Navigation summary

| Scope | Picker | Cycle | Waiting |
|-------|--------|-------|---------|
| Surfaces (intra-bay) | `bay surface go` | `bay sf next/prev` | `bay surface go --next-waiting` |
| Bays (intra-dock) | `bay go` | `bay next/prev` | `bay go --next-waiting` |

All pickers are built-in. They support fuzzy
matching: type a query to filter, single match jumps directly, multiple
matches open an interactive picker.

### Docks

```
bay dock new <name> --path <path>           # create dock + home shell
bay dock new <name> --path <path> --agent <a>  # with default agent
bay dock new <name> --terminal <t>          # with host terminal
bay dock init [name]                        # set up bay awareness for a dock
bay dock ls [name]                          # list dock contents (default: current)
bay dock ls --json                          # machine-readable dock view
bay dock show <name>                        # detailed dock info
bay dock rename [old] <new>                 # rename (defaults to current dock)
bay dock close <name>                       # close all bays + kill session
bay dock close <name> --force               # skip safety checks
bay dock recover <name>                     # recover a single dock
bay dock sync [name]                        # copy .worktreeinclude files to worktrees
```

`dk` is an alias for `dock`.

`dock init` sets up bay awareness: a `CLAUDE.md` pointer in the
worktree directory, plus agent project files in the checkout when the
repo gitignores them (see [Bay awareness](#bay-awareness)). `dock sync`
copies `.worktreeinclude` matches into existing bay worktrees.

### Listing and context

```
bay ls                                      # list bays in current dock
bay ls -s                                   # compact output (no labels or key names)
bay ls --json                               # machine-readable bay list
bay ls --json --rows                        # denormalized rows for current dock
bay tree                                    # full hierarchy with surfaces
bay tree -l                                 # show tmux IDs and extended detail
bay tree --json                             # machine-readable tree
bay tree --json --rows                      # denormalized row output
bay tree --dirty                            # only show dirty bays
bay pwd                                     # show current bay context
bay pwd --json                              # machine-readable context
bay status-line <field>                     # output for tmux status bar
```

### Infrastructure

```
bay setup                                   # first-time setup
bay recover                                 # reconstruct all state after reboot
                                            # (inside a dock: that dock only)
bay doctor                                  # health checks
bay monitor start|stop|status               # manage background monitor
bay completion bash|zsh|fish                # generate completion script
bay version                                 # show version + commit
```

## Keybindings

Bay installs tmux keybindings using the Option (Meta) key. No tmux
prefix required — just press the key combo directly.

### Navigation

| Key | Action |
|-----|--------|
| `Option+h` / `Option+l` | Previous / next window (tmux-native) |
| `Option+j` / `Option+k` | Select pane down / up (tmux-native) |
| `Option+H` / `Option+L` | Select pane left / right (tmux-native) |
| `Option+J` / `Option+K` | Select pane down / up (mirrors `j`/`k`) |
| `Option+g` | Bay picker (popup) |
| `Option+r` | Jump to next waiting bay in dock (agent needs attention) |

### Creation (lowercase = split pane, Shift = new window)

| Key | Action |
|-----|--------|
| `Option+s` / `Option+S` | Shell pane / shell window |
| `Option+a` / `Option+A` | Agent pane / agent window |
| `Option+e` / `Option+E` | Bay editor / dock editor |
| `Option+c` / `Option+C` | Create bay in current dock (Shift adds the dock's default agent) |

### Utility

| Key | Action |
|-----|--------|
| `Option+w` | Close current pane/surface (press twice when it is the last surface in a bay) |
| `Option+W` | Force-close current bay even if dirty (press twice; the first press warns what will be discarded) |
| `Option+z` | Restore most recently closed surface (undo-close) |
| `Option+/` | Flash current bay (name — first-line description — branch — #PR) |
| `Option+?` | Popup with full bay description (including body) |
| `Option+p` | Command palette (Tab inside to flip window/pane mode) |

### Agent chord (`Option+o`)

`Option+o` opens a tmux key-table for agent shortcuts and home
checkout actions. Each chord shows a 2-second toast reminding you of
the letters.

| Chord | Action |
|-------|--------|
| `Option+o c` / `Option+o C` | Claude in current bay (pane / window) |
| `Option+o x` / `Option+o X` | Codex in current bay (pane / window) |
| `Option+o g` / `Option+o G` | Antigravity in current bay (pane / window) |
| `Option+o f` / `Option+o F` | Fable in current bay (pane / window) |
| `Option+o o` / `Option+o O` | Opus (1M) in current bay (pane / window) |
| `Option+o b c` | New bay in current dock with Claude |
| `Option+o b x` | New bay in current dock with Codex |
| `Option+o b g` | New bay in current dock with Antigravity |
| `Option+o b f` | New bay in current dock with Fable |
| `Option+o b o` | New bay in current dock with Opus (1M) |
| `Option+o h Enter` | Focus/create home shell (`bay home`) |
| `Option+o h s` | Home shell (`bay shell --bay home`) |
| `Option+o h e` | Home editor (`bay edit --bay home`) |
| `Option+o h c` | Claude in home |
| `Option+o h x` | Codex in home |
| `Option+o h g` | Antigravity in home |
| `Option+o h f` | Fable in home |
| `Option+o h o` | Opus (1M) in home |

The fable/opus chords launch the built-in model profiles; redefining
or disabling those profiles in config changes (or breaks) what the
chords run, so re-point the binding if you repurpose the name.

### Pattern

Navigation is vim-flavored hjkl. `Option+h/l` cycle windows left/right
(tabs are horizontal in the status bar); `Option+j/k` select panes
down/up (most splits stack vertically). `Option+Shift+H/L` handle the
less-common horizontal pane axis; `Option+Shift+J/K` mirror `j/k` so
a sequence like H-J-L keeps the Shift held the whole time. For
creation keys, lowercase opens a split pane in the current window and
Shift opens a new window. The bay picker (`Option+g`) is usually the fastest way to
jump across bays — fuzzy-match by name or description.

### Installing and updating

`bay setup` installs all keybindings in `~/.tmux.conf`. It detects
conflicts with existing bindings and prompts before overwriting. Run
`bay doctor` to check if keybindings are current.

When bay ships a canonical change (e.g. flipping `M-s` from window to
pane), `bay setup` on re-run detects canonical keys bound to a
non-canonical bay command and prompts to update them. Opt out per-key
by adding `# bay-keep: M-s` to the bay block — bay will stop asking
about that key, and `bay doctor` will stop reporting it as missing.
Chord sub-table bindings use a `table:key` form, e.g.
`# bay-keep: bay-agent:c bay-agent-bay:x bay-home:Enter`.

After writing changes, `bay setup` offers to reload `~/.tmux.conf` in
the running tmux server (`tmux source-file ~/.tmux.conf`) so updated
bindings take effect immediately. The prompt only appears when a tmux
server is running and the run actually changed active bindings.

## Command palette

`Option+p` opens the command palette in a tmux popup: a
fuzzy-searchable list of every bay command that doesn't have a
dedicated hotkey. Commands that *do* have a hotkey show it in the
right-hand column, so the palette doubles as a cheat-sheet.

- **Filtering**: type to narrow the list (substring match on titles).
  The section grouping collapses to a single flat list while a filter
  is active.
- **Recents**: the top "Recent" section shows the most recently used
  command as slot 1 (for `M-p, Enter = redo last`), followed by the
  most frequently used commands from the last ~20 invocations.
- **Mode toggle**: entries that create a surface respect the Mode
  shown in the footer. The palette starts in **pane** mode (split the
  current window); press `Tab` to flip to **window** mode (new tmux
  window).
- **Parametric entries** end in `...`: they chain to a sub-picker
  (e.g., "New agent..." → pick agent type) or a text prompt
  (e.g., "Rename bay..." prefilled with the current name).
- **Scope**: entries that require a bay are hidden when you
  open the palette outside one; same for dock-scoped entries.
- **Home actions**: "Go to home", "Home shell", "Home editor",
  "Home agent", and "Home agent..." are dock-scoped. They work from
  dock surfaces even when there is no current bay and home has not
  been materialized yet.

Every command in the palette can also be run directly from the
shell — the palette is not a new surface, just a discovery and
launcher layer.

## Editor integration

`bay edit` opens your editor on the bay's root directory. Use
the editor's own file browser to navigate within the project. To edit
individual files, open a shell and launch your editor from there.

```
bay edit                    # current bay (default)
bay edit b1                 # specific bay
bay edit --bay home         # dock checkout
bay edit --editor vim       # use a specific editor this time
bay edit --dock             # dock editor (all bays)
bay edit --window           # new window instead of split pane
```

**GUI editors** (Cursor, VS Code, Zed) launch and return — bay does
not track the editor window. Running `bay edit` again focuses the
existing window (these editors reuse the window for an already-open
directory), so `Option+e` works as both "launch" and "switch to."

**Terminal editors** (nvim, vim) run as tracked tmux surfaces,
splitting the current window by default. Use `--window` for a new tmux
window. When you quit the editor, the pane closes and the surface is
cleaned up automatically.

Editor resolution order: `--editor` flag, `default_editor` in config,
`$VISUAL`, `$EDITOR`, then probing for cursor/code/zed/nvim/vim.

Configure with:
```
bay config editor cursor    # save preference
bay config editor           # check current editor
```

## Monitoring

Bay runs a background monitor that provides two services:

### Waiting detection

Bay detects agents that need your attention through three mechanisms,
all unified into the `Option+R` ("next waiting") navigation:

**Bell-based.** Agents that send a terminal bell (`\a`) are detected via
tmux's `window_bell_flag`. For Claude Code, `bay setup` installs a
`PermissionRequest` hook that sends a bell when Claude asks for
permission.

**Turn-complete hooks.** When an agent finishes a response, you usually
want to know — not because it's blocked, but so you can review and move
on. `bay setup` installs hooks for any agent it finds on your PATH:

- Claude Code: `Stop` hook in `~/.claude/settings.json`.
- Antigravity: `Stop` hook in `~/.gemini/config/hooks.json`.
- Codex: `notify` entry in `~/.codex/config.toml`.

The hooks set `@bay-waiting=1` on the agent's tmux window without
emitting a terminal bell, so they're silent on the focused window and
visible only as a tab highlight on inactive windows. The flag clears
automatically when you navigate to the window — `bay setup` also
installs an `after-select-window` tmux hook in `~/.tmux.conf` that
mirrors how `window_bell_flag` self-clears on focus. Both pieces are
bundled behind a single setup prompt because they only work as a unit.

**Pattern-based (fallback).** The monitor watches agent panes by
capturing the last few lines of output, stripping ANSI codes, and
matching against regex patterns in
`~/.config/bay/waiting-patterns.txt`:

```
# Claude Code
Allow.*Deny
# Codex
\[Y/n\]
# Antigravity
Approve\? \(y/n
# Generic
\(y/n\)
Do you want to proceed
```

When you see a prompt the monitor does not catch, edit
`~/.config/bay/waiting-patterns.txt` and add a regex on its own line.
The monitor reloads patterns on every cycle (default 3s), so no
restart is needed.

### Merge detection

The monitor performs activity-gated `git fetch` for checkouts with recent
bay activity (within the last couple of hours). After fetching, it
checks if bay branches have been merged into main. When a merge is
detected:

- The bay's `merged` flag is set automatically.
- Status line shows a count of merged bays.
- `bay ls` and pickers highlight merged bays.
- Navigating to a merged bay shows a suggestion to close it.

No fetches happen for idle checkouts or bays without recent activity.

Once a PR has merged, you have two ways to move on. If you're done with
the bay, `bay close` it. If you want to reuse it for the next task, run
`bay tidy` — it returns the worktree to a clean detached HEAD at
`origin/<default>` and deletes the merged branch, the same base a fresh
bay starts from. Don't reach for `git checkout main` in a worktree: the
dock root already holds the default branch, so the checkout fails. `bay
tidy` sidesteps that by detaching at the remote ref, and it refuses to run
if the worktree is dirty or has unpushed/unmerged commits, so nothing is
lost.

### PR detection

PR numbers are detected automatically via `gh pr view` whenever a
bay has a branch but no PR recorded. The lookup happens on the
first display after creation (and again on each monitor cycle for
bays that haven't been checked yet). The result — including
"this branch has no PR" — is cached so the lookup runs at most once
per bay.

### Managing the monitor

```
bay monitor status          # check if running and current
bay monitor status --verbose # show version, binary, heartbeat details
bay monitor status --json    # machine-readable monitor health
bay monitor start           # start background monitor
bay monitor stop            # stop monitor
```

The monitor is started automatically by `bay recover`. You rarely need
to manage it directly. When running, status reports whether the monitor
is `current`, `stale`, `unknown`, or running from a different `bay`
executable path. A stale or unknown monitor can be restarted with
`bay monitor stop` followed by `bay monitor start`. If the monitor's
heartbeat is old but the version and binary match, status still reports
`current` and includes a heartbeat warning.

## Listing views

`bay pwd` shows your current location:

```
dock dev / bay b1.auth-fix / surface agent
```

`bay ls` lists bays in the current dock. The `bay` column carries
the canonical label (`<id>.<name>` for named bays, bare `<id>` for
unnamed ones), so unnamed bays never render blank:

```
dk dev
  bay b1.auth-fix  br=feature/auth  n=3
  bay b2.perf-fix  br=fix/perf      n=1
  bay b3                            n=1
```

`bay tree` shows the full hierarchy:

```
dk dev
  bay b1.auth-fix  br=feature/auth  n=3
  bay b2                            n=1
dk staging
  bay b1.deploy    br=release/v2    merged  n=1
```

Use `bay tree -l` for tmux IDs. Use `bay ls -s` for compact bay-list
output without labels or key names. Human `ls`/`tree` output truncates
long bay names and branches; `bay show` and JSON output keep the full
values.

## Machine-readable output

`bay pwd`, `bay ls`, `bay tree`, and `bay show` all accept `--json`.

```
bay pwd --json | jq '.bay_id'
bay ls --json | jq '.[] | select(.pending)'
bay tree --json | jq '.docks[].bays[] | select(.pending)'
bay tree --json --rows | jq '.[] | select(.bay_waiting)'
bay show b1 --json | jq '.branch'
```

## Status line

`bay setup` offers to install bay's tmux status line automatically — it
appends a `# Bay status line` block to your `~/.tmux.conf` if no
`status-right` is already configured. The block looks like this:

```tmux
# Bay status line
set -g status-right-length 40
set -g status-right '#(bay status-line full --window #{window_id} --width #{status-right-length})'
```

You can also add it manually if you skipped the prompt. If your
`.tmux.conf` already has a `status-right` setting, bay won't overwrite
it; setup prints the recommended lines so you can merge them yourself.

The `full` field outputs `label branch #PR status` (e.g.
`b4.auth-fix fix/login #42 dirty`). The label keeps the path-derived
worktree dir tag visible after rename. The `--width` flag enables
adaptive truncation — when space is tight, it crops the label's
bay-name portion first, then shortens branch/status metadata.
Pass `#{status-right-length}` so tmux tells bay how much space is
available.

**Pass `--window #{window_id}`.** Tmux caches `#()` output per client
keyed by the literal command string, and only refreshes on the
`status-interval` tick (default 15s). Without an interpolated
`#{window_id}`, every window and dock shares a single cache entry, so
you'll see a stale status from another window until the next tick.
Interpolating the window ID gives each window its own cache, and bay
uses the explicit ID instead of asking tmux which window is "current".

Other fields:

- `id` — raw bay ID (`b1`).
- `name` — canonical bay label, the same string the picker and
  `bay ls` show (`home`, `b1`, or `b1.auth-fix`). Never blank for a
  real bay. Equivalent to the label portion of `full`.
- `dir` — path-derived worktree dir basename (`b1`).
- `branch`, `pr`, `status` — worktree metadata (suppressed for home).
- `dock` — current tmux session name.
- `merged` — count of merged bays in the current dock (e.g.
  "2 merged"). Useful for a status-bar reminder to clean up.

### Tab name truncation

Bay automatically shortens tmux tab names when a dock has many
bays, so tabs don't overflow the status bar. The full bay
name is preserved in the manifest for navigation and completion —
only the tmux window title is truncated. Names expand again when
bays are closed.

When sibling bays in a dock share a hyphen-separated prefix (e.g.
`codex-home-mail-account-filter` and `codex-lane-scheduler-phase1`),
the shared prefix is replaced with a leading `…` so the unique tail
of each name stays visible: `b1.…home-mail-…` and `b2.…lane-sched…`
instead of two tabs that both crop to `codex-`.

## Recovery

Bay is designed for everything to be reconstructable after a reboot.

### After a reboot

```
bay recover
```

This recreates all tmux sessions, windows, and panes from the manifest.
Worktrees are already on disk. Bay relaunches shells, agents (with
`--continue` for Claude Code), and terminal editor surfaces. GUI
editors are not recovered — reopen them with `bay edit`. Starts the
monitor and prints `tmux attach` commands.

Recovery is idempotent -- run it multiple times safely. It detects and
reuses existing tmux state. Panes with live foreground processes are
left alone.

### After a tmux crash

If tmux dies and a fresh empty session of the same name comes back up
(e.g., your terminal auto-attaches and creates one), bay's monitor
won't mistake it for the original — it tags every session it manages
with an identity marker and refuses to clean up surfaces against a
session it doesn't recognize. Run `bay recover` to rebuild and re-tag.

### After closing your terminal

Same thing: `bay recover`. The tmux sessions may still be alive (tmux
survives terminal close). If they are, recovery reconnects. If they're
gone, it recreates them.

### Single-dock recovery

```
bay dock recover dev
```

Or run `bay recover` from inside a dock to recover just that dock.

## Multiple checkouts

Each dock owns one checkout. Use a separate dock for another checkout:

```
bay dock new frontend --path ~/projects/frontend --agent claude
bay dock new backend --path ~/projects/backend --agent claude

bay new ui-changes --dock frontend
bay new api-changes --dock backend
```

Each dock has its own tmux session and worktree directory.

## Shell completions

`bay setup` prints a snippet for your shell rc file. Tab completion
covers bay IDs, dock names, agent types, and
surface names.

Add to your `.zshrc` or `.bashrc`:

```bash
if command -v bay > /dev/null ; then
  source <(bay completion zsh)    # or bash
fi
```

For fish, add to `~/.config/fish/config.fish`:

```fish
if command -v bay > /dev/null
  bay completion fish | source
end
```

## Tips and troubleshooting

**"I closed my terminal and everything is gone."**
It's not gone. Run `bay recover`. Bay stores everything in the manifest
and recreates tmux state on demand. Your worktrees and code are on disk.

**"bay close refuses and I just want it gone."**
Safety checks prevent losing work. Bay treats pushed branches and
commits included in a merged PR as safe, including multi-commit squash
merges. It also accepts squash-merged/cherry-picked patches on the
default branch. For review bays where PR code was applied as dirty files,
bay also closes when it can verify the whole dirty tree exactly matches a
recoverable git ref. If a close keybinding refuses, bay could not verify
the local changes as review changes it can recreate. If you're sure
despite a refusal, press `Option+Shift+W` twice to force-close the bay
from the keyboard (uncommitted changes are discarded; unlanded commits
stay on the local branch), or use `bay close <id> --force` from a
terminal. Or use `bay close --done` to batch-close all finished bays, or
`--clean` for anything non-dirty. Add `--dry-run` to preview first.

**"The waiting indicator isn't working."**
For bell-based detection: ensure `monitor-bell` is on in tmux (it is by
default). For Claude Code, re-run `bay setup` to install the bell hook.
For pattern-based detection: check `bay monitor status`. If running, the
prompt text probably doesn't match any pattern. Add a regex to
`~/.config/bay/waiting-patterns.txt` — the monitor reloads every cycle.

**"I want to use bay with an agent that isn't Claude or Codex."**
Add an agent definition to your config:

```toml
[agents.myagent]
command = "myagent-cli"
resume_args = "--resume"
project_file = ".myagent.md"
```

Then reference it with `--agent myagent` or set it as a dock default.

**"Can two docks use the same checkout?"**
No. A checkout belongs to one dock; create another local checkout if
you need a separate dock.

**"How do I update my keybindings after a bay upgrade?"**
Run `bay setup` again. It replaces the bay keybinding block in
`~/.tmux.conf` while preserving your other settings.

**"Bay acted on, or reported, the wrong bay or dock."**
Bay works out which dock you are in by asking tmux for the current
session. Asked without a target, tmux answers with the most recently
active session on the server — including a detached one just created by
unrelated background tooling — which is not necessarily the session bay
was invoked from. Bay now anchors that question to its own session, so
the answer no longer depends on what else is running. Upgrade to pick
it up; no `bay setup` re-run is needed, the fix is entirely in the
binary.

**"`bay recover` only recovered one dock."**
`bay recover` scopes itself to where it runs: inside a dock's tmux
session it recovers that dock, outside tmux it recovers every dock.
Older versions could not tell the two apart — run from a plain shell,
they recovered whichever dock tmux happened to report and exited 0
without an error. Upgrade to pick up the fix. To recover one dock
deliberately from anywhere, use `bay dock recover <name>`.

**"What's the ~ window?"**
A legacy placeholder that keeps a tmux session alive when bay cannot
open a real surface. Checkout-backed docks now use a `home` shell for
normal empty-dock cases.

**"How does bay know my branch and PR without me telling it?"**
Branch is detected via `git rev-parse` on every sync cycle. PR number
is detected via `gh pr view` once a branch exists. Both are cached in
the manifest.

**"My agent lost context after recovery."**
Built-in agents have resume args configured automatically; bay uses
them on recovery and on undo-close. For custom agents, set
`resume_args` in the `[agents]` config section. See
[Session resumption](#session-resumption) for the full list.

**"How do I see my editor in bay surface go?"**
Terminal editors (nvim, vim) launched via `bay edit` appear as surfaces
in `bay surface go`. GUI editors (Cursor, VS Code, Zed) are fire-and-forget —
use Cmd+Tab or your OS window manager to switch to them.

## Appendix: abbreviations

| Short | Long |
|-------|------|
| `dk` | `dock` |
| `sf` | `surface` |
| `ls` | `list` |
| `mv` | `rename` |
| `rm` | `close` |
| `cat` | `show` |

These work everywhere: `bay sf ls`, `bay rename`, `bay dock rm`, etc.
