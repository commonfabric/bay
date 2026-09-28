# Bay — Agent Guide

Bay manages concurrent bays with surfaces across tmux and GUI
applications. This guide is for AI agents (Claude Code, Codex, etc.)
that create, monitor, and manage bays — either from an
orchestrating session or from within a bay.

## Key concepts

A **bay** is a managed working directory with metadata (branch,
PR, dirty/merged flags). Each bay has three identity concepts:

- An **ID** — the stable CLI handle. New bays use `^b[1-9]\d*$`
  (lowercase `b` + positive integer); older persisted bays may still
  use legacy `w<N>` IDs. Set at creation, never changes for the bay's
  lifetime, unique within a dock. The slot is released on close and may
  later be reused by a new bay, so don't treat IDs as globally permanent
  across close + recreate. The ID is what every command takes:
  `bay close b1`, `bay sf show b1:agent`, `bay show b1`.
- A **Name** — a mutable, optional display tag. Sticks once set. May
  be empty. Filled automatically when a branch is detected for the
  first time, or explicitly via `bay rename`. Names are not CLI keys —
  passing a Name where bay expects an ID errors with `did you mean
  "b1"?`.
- A **Label** — the canonical display string the CLI uses for human
  output. Derived from the ID and (optional) Name as:
  - `home` for the home pseudo-bay
  - `<id>` when no Name is set (e.g. `b2`)
  - `<id>.<name>` when both are set (e.g. `b1.auth-fix`)

  The same Label appears in the bay picker (M-g), tmux tabs, the
  status line (`name` and `full` fields), `bay ls`/`tree`/`show`, and
  `bay pwd`. It is never blank for a real bay, so unnamed bays render
  as `b2` rather than empty. JSON output keeps `id` and `name` as
  separate fields (Name may be empty); consumers that want the label
  can derive it from those, or use the `dir` basename of `path`.
- An optional **description** — commit-message-style text for context
  recall. The first line is a short label (cap 80) shown in the picker,
  `bay ls`/`bay tree`, and the `M-/` flash. Optional trailing lines
  (separated from the first line by a blank line) are a richer body —
  shown only in the `M-?` popup and JSON output. Does not affect tmux
  tab names.

  Descriptions are **auto-populated** in the background (opt-in; the
  auto-description backstop summarizes the bay's conversation), so you
  don't need to maintain them. If you do want to set one explicitly,
  `bay describe` overrides — a description set that way is owned and the
  backstop won't touch it.

A **full reference** is `dock:id` (e.g., `labs:b1`). A bare ID
(`b1`) resolves to the current dock first; if absent there, falls
through to a cross-dock search (which errors on ambiguity).

A bay also has a **path** — the on-disk working directory. For
worktree bays bay assigns sequential subdirs (`b1`, `b2`, `b3`,
...) under the dock's worktree dir. For worktree bays the path
basename matches the bay ID. Closing and recreating a bay can leave
gaps, and a freed trailing slot may be reused by a future bay.

A **dock** is a named tmux session grouping related bays. Each
dock owns one local git checkout and can have a default agent type
and host terminal.

A **surface** is the unit of navigation — anything you can focus and
jump to. Surfaces replace the old window/pane hierarchy. Each surface
has:
- An **ID** — integer, unique within its bay, never reused.
- A **name** — unique within its bay (e.g., `agent`, `shell`,
  `editor`).
- A **type** — semantic role: `agent`, `editor`, `shell`, or `cmd`.
- A **backend** — interaction model: `tmux-pane`.

### Bay types

| Type | CWD | Bay manages the directory |
|------|-----|--------------------------|
| **Worktree** | git worktree | yes — creates and deletes it, safety checks on close |
| **External** | any user path | no — bay remembers it for recovery |
| **Home** | dock checkout (`dock.path`) | no — bay never deletes the canonical checkout |

`home` is a reserved per-dock pseudo-bay. It materializes when it has
surfaces and is hidden from normal `bay ls`, `bay tree`, `bay go`, and
cycling while empty. Use `bay home`, `bay shell --bay home`,
`bay agent --bay home`, `bay edit --bay home`, or
`bay surface new cmd ... --bay home` to work in the dock checkout.
The human command palette exposes dock-scoped home entries ("Go to
home", "Home shell", "Home editor", "Home agent", and "Home agent..."),
and `Option+o h` opens a home keybinding submenu. Agents should invoke
the underlying commands directly.
Normal bays cannot use `home` as an ID or display name. Bay never
deletes the canonical checkout.

### Dirty and merged

- `dirty` — computed from git (uncommitted changes); shown in `bay ls`, `bay tree`, statusline
- `merged` — persisted; auto-detected when branch is merged into default

### Surface types and backends

| Type | Backend | Description |
|------|---------|-------------|
| `agent` | `tmux-pane` | AI agent (Claude Code, Codex, etc.) |
| `shell` | `tmux-pane` | Interactive shell |
| `cmd` | `tmux-pane` | One-off command |
| `editor` | `tmux-pane` | Terminal editor (nvim, vim) |

## Self resolution

Most commands accept `self` or default to it when no target is given.

**Bay resolution**: `self` matches CWD against known bay
paths. If CWD is inside a bay directory, that bay is
resolved.

**Surface resolution**: For surface-scoped commands (`bay surface
close`), `self` additionally matches the current tmux pane ID to
identify which surface within the bay.

Commands an agent inside a bay typically uses:
- `bay pwd` — confirm bay context (dock, bay, surface)
- `bay show self` — check own bay metadata
- `bay ls` — see what other bays are doing
- `bay close self` — shut down when work is complete

## Operational model

Bay is a coordination layer over:
- git worktree lifecycle
- tmux session/window/pane lifecycle
- editor launching (terminal editors as surfaces, GUI editors fire-and-forget)
- bay metadata (branch, PR, dirty/merged flags)
- recovery after tmux or host restart

Important invariants:
- A **bay** is the durable unit. Surfaces are views onto it.
- Branch, PR number, dirty, and merged are all auto-detected. Branch
  comes from the filesystem on every display. PR is looked up via `gh pr
  view` once and cached. Merged is set to true automatically when bay
  detects a merge.
- Bay does not generate config files into worktrees. Bay awareness
  comes from a `CLAUDE.md` in the worktree directory (an ancestor of
  every bay); project instructions go in the repo's own files.
  Per-bay context is discoverable via `bay pwd --json`.

## Parsing and output

Prefer JSON output for automation:
- `bay pwd --json`
- `bay ls --json`
- `bay show [id] --json` (defaults to current; pass `self` explicitly for the same effect)

Human-formatted output is for display only and may change between
versions.

### JSON: `bay pwd --json`

Returns the current bay context:

```json
{
  "dock": "labs",
  "bay_id": "b1",
  "bay": "auth-fix",
  "surface": "agent",
  "surface_id": 1,
  "path": "/Users/dev/projects/labs-worktrees/b1"
}
```

Fields:
- `dock` — dock name (tmux session).
- `bay_id` — bay ID (`b<N>`, the stable handle).
- `bay` — bay Name (optional display tag, may be empty). The human
  `bay pwd` output renders the canonical label (`<id>.<name>`, or
  `<id>` alone when Name is empty); the JSON keeps `bay_id` and `bay`
  as separate fields.
- `surface` — current surface name (resolved from tmux pane).
- `surface_id` — current surface ID.
- `path` — absolute working directory path.

Fields are omitted when empty.

### JSON: `bay ls --json`

Returns bays in the current dock:

```json
[
  {
    "id": "b1",
    "name": "auth-fix",
    "type": "worktree",
    "path": "~/projects/labs-worktrees/b1",
    "branch": "feature/auth-fix",
    "dirty": false,
    "pending": false,
    "sync_status": "ok",
    "surface_count": 2
  }
]
```

Use `bay tree --json` for the global hierarchy:

```json
{
  "focus": {
    "kind": "bay",
    "dock": "labs",
    "bay_id": "b1"
  },
  "recursive": true,
  "docks": [
    {
      "name": "labs",
      "path": "~/projects/labs",
      "agent": "claude",
      "bays": [
        {
          "id": "b1",
          "name": "auth-fix",
          "type": "worktree",
          "path": "~/projects/labs-worktrees/b1",
          "branch": "feature/auth-fix",
          "dirty": false,
          "merged": false,
          "sync_status": "ok",
          "surface_count": 2,
          "surfaces": [
            {
              "id": 1,
              "name": "agent",
              "type": "agent",
              "backend": "tmux-pane",
              "agent": "claude",
              "status": "ok"
            },
            {
              "id": 2,
              "name": "shell",
              "type": "shell",
              "backend": "tmux-pane",
              "status": "ok"
            }
          ]
        }
      ]
    }
  ]
}
```

Field semantics:
- `id` — bay ID (`b<N>`, the stable handle for this bay's lifetime).
- `name` — bay Name (optional, may be empty). To get the canonical
  display label the CLI shows (`<id>.<name>`, or `<id>` alone when
  Name is empty, or `home`), compose it from `id` + `name`, or use
  the human `bay ls`/`tree` output where the unified label appears
  in the `bay` column.
- `focus` — the scope bay inferred from CWD and tmux.
  `focus.bay_id` is the same ID surfaced by the per-bay
  `id` field; consumers can match on either.
- `recursive` — whether surfaces are expanded in the output.
- `sync_status` — `ok`, `stale` (tmux window missing), or `missing`
  (worktree directory gone).
- `waiting` — true if any surface in the bay has an agent
  waiting for input.

Use `bay tree --json --rows` for denormalized rows across the hierarchy
(easier to filter):

```json
[
  {
    "dock": "labs",
    "bay_name": "auth-fix",
    "bay_branch": "feature/auth-fix",
    "bay_dirty": false,
    "bay_merged": false,
    "bay_sync_status": "ok",
    "surface_id": 1,
    "surface_name": "agent",
    "surface_type": "agent",
    "surface_agent": "claude",
    "surface_status": "ok"
  }
]
```

Use `bay tree` to show surfaces. `bay ls` is the bay list for the
current dock.

### JSON: `bay show [id] --json`

```json
{
  "id": "b1",
  "name": "auth-fix",
  "description": "Login flow fixes",
  "dock": "labs",
  "type": "worktree",
  "path": "/Users/dev/projects/labs-worktrees/b1",
  "branch": "feature/auth-fix",
  "pr": "347",
  "dirty": false,
  "merged": false,
  "sync_status": "ok",
  "default_agent": "claude",
  "surfaces": [
    {
      "id": 1,
      "name": "agent",
      "type": "agent",
      "backend": "tmux-pane",
      "agent": "claude",
      "status": "ok"
    },
    {
      "id": 2,
      "name": "shell",
      "type": "shell",
      "backend": "tmux-pane",
      "status": "ok"
    }
  ]
}
```

Bay fields:
- `description` — commit-message-style text. First line (before the
  first `\n`) is the label shown in lists and flash; anything after
  a blank line is the optional body shown only in the popup.

Surface fields:
- `type` — `agent`, `editor`, `shell`, or `cmd`.
- `backend` — `tmux-pane`.
- `agent` — agent config key (present when type is `agent`).
- `command` — shell command (present when type is `cmd`).
- `status` — `ok` or `stale`.

Fields are omitted when empty.

## Default target rules

Across all create-verbs (`bay new`, `surface new`, `shell`, `agent`)
the positional names the **thing being created**.
The container (dock, bay) is selected via `--dock` / `--bay`
flags or, when omitted, inferred from context. In an interactive
shell the dock of the current checkout (the repo your CWD is in)
takes precedence, falling back to the current tmux session. Commands
run from a tmux keybinding (via `run-shell`) use the tmux session
instead — their working directory is the server's, not yours — so
Option+c always creates in the dock you're looking at.

Commands that target a bay take the **ID** as the positional
(e.g., `bay close b1`). The exception is `bay new [name]`, where
the positional names the new bay's display Name (or is left
empty for the auto-fill-from-branch behavior). Surfaces use a
qualified form: `id:surface-name` or `dock:id:surface-name`.

| Command | Default target when omitted |
|---------|-----------------------------|
| `bay new [name]` | current dock from CWD checkout, else tmux session, else auto-bootstrap from CWD |
| `bay ls` | bays in current dock (errors outside a dock) |
| `bay show [id]` | current bay |
| `bay close [id]` | required (no default; use `--done`/`--clean`/`--all` for batch) |
| `bay close --done` | bays not dirty or pending in current dock |
| `bay close --clean` | all non-dirty bays in current dock |
| `bay close --all` | all bays in current dock, then home after confirmation if final |
| `bay pwd` | current bay context |
| `bay surface new <kind> [name]` | current bay |
| `bay surface ls` | surfaces in current bay |
| `bay surface close <name>` | required (use `self` for current pane) |
| `bay surface show [name]` | current pane's surface |
| `bay surface tree` (none) | n/a — surfaces are leaves; use `bay surface show` |
| `bay dock ls [name]` | current dock from CWD checkout or tmux session, or all docks |
| `bay dock tree [name]` | current dock from CWD checkout or tmux session |
| `bay go [query]` | bays in current dock |
| `bay surface go [query]` | surfaces in current bay |
| `bay edit [id]` / `bay edit --bay home` | current bay; `home` targets dock checkout |
| `bay shell [name]` / `bay shell --bay home` | current bay; surface auto-named "shell" |
| `bay home` | current dock; creates/focuses home shell |

## Commands

The central bay commands live at the top level. Subordinate concepts
use explicit nouns: `surface`/`sf` and `dock`. Top-level
shorthands exist for frequent surface creation.

### Bay commands

#### `bay new [name] [--dock DOCK] [--dir PATH] [--branch NAME] [--agent [TYPE]] [--description TEXT]`

Create a bay with its first surface. The positional names the
new bay; `--dock` selects which dock to create it in. When `--dock`
is omitted, bay uses the dock of the current checkout (the repo your
CWD is in); if CWD isn't a known checkout it falls back to the current
tmux session, and otherwise auto-bootstraps a dock from CWD (creating
one automatically). Standing in a dock's checkout therefore wins over
the tmux session you're attached to. The exception is a tmux
keybinding (Option+c), which runs via `run-shell` where the CWD is the
server's, not yours — there the tmux session wins, so bays land in the
dock you're looking at.

Default behavior opens a shell. Use `--agent` for the dock's default
agent, or `--agent TYPE` for a specific one. Or create the bay
first and add an agent with `bay agent`. TYPE is a built-in agent
(`claude`, `codex`, `antigravity`), a built-in model profile
(`fable`, `opus`, `sonnet`, `haiku` — claude pinned to that model;
`opus` pins the 1M-context `opus[1m]`), or any agent/profile defined
in config (see `bay help config`).

`--description` is optional and can be set or changed later via
`bay describe`.

```
bay new                                  # auto-bootstrap from CWD
bay new auth-fix                         # named bay in current dock
bay new auth-fix --dock labs             # named bay in a specific dock
bay new auth-fix --dir ~/projects/foo    # external bay
bay new auth-fix --branch fix-auth       # checkout or create branch
bay new auth-fix --agent                 # with dock's default agent
bay new auth-fix --description "Login flow fixes"
```

`home` is reserved. `bay new home` fails with guidance to use
`bay home`.

#### `bay home`

Focus the most recent home surface in the current dock. If home has no
surfaces, create a shell at the dock checkout path.

```
bay home
bay go home
```

#### `bay close [id] [--force] [--tap] [--done] [--clean] [--all] [--dry-run]`

Close a bay and all its surfaces. For worktree bays,
checks for uncommitted changes and unlanded commits. Refuses if dirty
unless `--force` is used, except when bay can verify the dirty tree
exactly matches a recoverable git ref. In that case the dirty files are
treated as review changes bay can recreate, and close proceeds. If the
branch has been pushed, its HEAD is included in a merged PR, or its
patches are already on the default branch after a squash merge or
cherry-pick, bay deletes the local branch on close — no stale branches
left behind. Pass `self` to close the current bay. `bay close home`
closes home surfaces and leaves the dock checkout untouched when other
dock surfaces remain. Closing the last home surface in an otherwise
empty dock uses close confirmation; confirming dismisses the dock tmux
UI/session while keeping the dock registered and leaving `dock.path`
untouched.

`--tap` holds the close until a second identical invocation within
1.5 s — it backs the Option+Shift+W force-close keybinding for human
users. Agents should pass `--force` alone, never `--tap`.

Batch flags (without an ID):
- `--done`: close bays that are not dirty and not pending (have
  no unmerged branch). The conservative default for cleanup.
- `--clean`: close all non-dirty bays regardless of merge status.
- `--all`: close all worktree/external bays, then close home surfaces;
  final home dismissal uses the same confirmation/`--force` behavior as
  `bay close home`.
- `--dry-run`: preview what would be closed without closing anything.

```
bay close b1
bay close self
bay close home
bay close self --force
bay close --done
bay close --done --dry-run
bay close --clean
bay close --all
```

`bay clean-review [id]` clears dirty review changes without closing the
bay, but only after the same recoverable-ref check succeeds. It is a
targeted hidden command for intentionally clearing review changes bay can
recreate, not a normal cleanup command.

**Orphan auto-close (with grace window).** When a bay's last
surface goes away — via sync-detected tmux kill or via
`bay sf close` (no `--force`) on the last surface — bay schedules
the bay for auto-close after a 60-second grace window
(`Bay.PendingCloseAt` in the manifest). The user (or an
agent) can cancel by re-adding a surface; `SurfaceAdd` clears
`PendingCloseAt` unconditionally. If the grace expires with no
surfaces, sync runs `BayClose(force=false)` — clean + landed
finalizes the teardown; dirty or unlanded clears `PendingCloseAt`
to stop retries and leaves a permanent orphan for the user to
resolve manually.

Direct `bay close <id>`, `--done`, `--clean`, `--all`, and
`bay sf close --force` close immediately (no grace).

**Undo-close interaction.** `bay sf close` (and `Option+W`) and
sync-discovered tmux pane exits push a surface entry onto the dock's
undo-close queue regardless of whether the bay survives. When the
close is the last surface, the surface is queued and `PendingCloseAt`
is scheduled; restoring within the grace window recreates the surface
via `SurfaceAdd`, which clears `PendingCloseAt` as part of its normal
cancel path. Past the grace window, the bay is gone and a `bay sf
restore` call drops the stale entry silently.

#### `bay tidy [id] [--dock <name>]`

Return a bay's worktree to a clean detached HEAD at `origin/<default>` and
delete its local branch, keeping the bay alive for reuse. This is the
post-merge reset: run it once the bay's PR has landed instead of checking
out the default branch. Because it detaches at the remote ref rather than
the local default branch — which the dock root already holds checked out —
it never hits git's "branch is already checked out" error. The bay returns
to the same detached base `bay new` creates a worktree in.

Refuses if the worktree is dirty or carries commits that are neither pushed
nor merged, so no work is lost. A worktree that is already detached is a
no-op. Defaults to the current bay; pass an ID (optionally `--dock`-qualified)
to tidy another.

```
bay tidy            # tidy the current bay after its PR merged
bay tidy b1
bay tidy b1 --dock labs
```

Use `bay tidy` when you want to keep the bay; use `bay close` when you're
done with it entirely.

#### `bay show [id] [--json|--short|--flash|--popup] [--plain]`

Show bay details: path, branch, PR, dirty/merged, surfaces. Defaults
to the current bay; pass `self` explicitly for the same effect.

`--short` produces a one-line summary: `name — description — branch — #PR`,
skipping empty fields. Only the first line of the description is included.
Pair with `--plain` for ANSI-free output.

`--flash` sends the short summary to tmux `display-message` for 5 seconds.
Used by the `M-/` keybinding. Does not print to stdout. No-ops silently
when invoked outside a bay.

`--popup` opens a tmux popup showing the full description (including any
body). Used by the `M-?` keybinding. Dismiss with any key.

JSON output (`--json`) includes the full description, body and all.

```
bay show
bay show b1                     # by ID
bay show --short                # one-line bay summary
bay show --short --plain        # same, no ANSI colors
bay show --flash                # flash first line in tmux status bar (M-/)
bay show --popup                # full description in a tmux popup (M-?)
bay show self --json
```

#### `bay rename [id] <new-name>`

Rename a bay's display label. The new Name must match
`[a-zA-Z0-9_-]+`, cannot match the reserved generated ID patterns
`^b[1-9]\d*$` or legacy `^w[1-9]\d*$`, and cannot equal the
reserved `home` handle. With one arg, renames the current bay. After
rename, the ID is unchanged — it's still the CLI key. Renaming the
home pseudo-bay itself is rejected.

```
bay rename mem-refactor                 # rename current bay
bay rename b1 mem-refactor              # rename by ID
```

#### `bay describe [id] [<description>]` (also: `bay describe`)

Read or set a free-form description for a bay. The description
has two parts:

- **First line** — short label (cap 80) shown in the bay picker,
  `bay ls`, `bay tree`, and the `M-/` flash. Target around 40 characters.
  This is the bay's goal or scope; once set, it should rarely change.
- **Body** (optional) — trailing lines separated from the first line by
  a blank line (commit-message style). Shown in the `M-?` popup and
  JSON output only. Treat it as a standing brief — what this bay
  is for, where it stands now, and what the immediate next move is —
  written for someone (often the user) opening the bay cold.

With no args and no flags, prints the current description to stdout.
An empty string or `--clear` clears it. `--edit` opens `$EDITOR` for
interactive multi-line editing. Total length cap is 2000 chars.

Multi-line descriptions are set directly via the positional argument;
shells handle newlines in quoted strings natively — no escape
interpretation of literal `\n`.

**Agents should keep both parts current:** set the first line when
starting a task (from the prompt or PR title), and update the body
when the situation meaningfully changes (scope shift, new blocker,
approach pivot) — not on every pause. Don't narrate recent tool
calls; describe the standing context so the user can flash `M-?`
and immediately swap the bay's whole picture back in.

```
bay describe                              # print current description
bay describe "Login flow fixes"           # set first line only
bay describe "Login flow fixes

paused mid-rebase on origin/main
rename conflict on helper.ts
tests green except auth_test.go"             # set with body (actual newlines)
bay describe --edit                       # open $EDITOR
bay describe b1 "Login fixes"             # set by ID
bay describe --clear                      # clear current bay's description
bay describe "Login flow fixes"              # top-level shortcut
```

### Navigation

#### `bay go [query]`

Navigate between bays within the current dock. This is
intra-dock navigation.

- No args: opens a picker showing all bays in the dock.
- With query: fuzzy-matches ID, Name, branch, or PR number — the
  picker is friendly here even though strict CLI args aren't.
- `--waiting`: filter to bays with waiting agents.
- `--next-waiting`: jump to next waiting bay, cycling.

```
bay go                       # pick from bays
bay go auth-fix              # fuzzy: matches Name "auth-fix"
bay go b1                    # fuzzy: matches ID "b1"
bay go 347                   # match by PR number
bay go --waiting             # filter to waiting
bay go --next-waiting        # cycle through waiting
```

#### `bay surface go [query]`

Navigate between surfaces within the current bay. This is
intra-bay navigation.

- No args: opens a picker showing all surfaces in the bay.
- With query: fuzzy-matches surface name or type. One match jumps
  directly; multiple opens the picker pre-filtered.
- `--next-waiting`: jump to next waiting surface in the bay.

```
bay surface go                      # pick from surfaces
bay surface go shell                # jump to the shell surface
bay surface go --next-waiting       # jump to next waiting surface
```

#### `bay surface next` / `bay surface prev`

Cycle to the next or previous surface within the current bay.

#### `bay palette` (hidden)

Backs the `Option+p` command palette popup. Not an agent-facing
command — it opens an interactive picker inside a tmux popup and is
intended for human use. Agents should invoke the underlying commands
(`bay go`, `bay shell`, `bay describe`, etc.) directly.

Human home palette entries map to:

```
bay home
bay shell --bay home
bay edit --bay home
bay agent --bay home
bay agent <type> --bay home
```

The human `Option+o h` submenu maps to `Enter` for `bay home`, `s` for
`bay shell --bay home`, `e` for `bay edit --bay home`, and `c`/`x`/`g`
for Claude/Codex/Antigravity agents in home.

#### `bay next` / `bay prev`

Cycle to the next or previous bay within the current dock.

### Surface commands

#### `bay surface new <kind> [args] [--bay BAY] [--dock DOCK] [--window|--split h|v]`

Add a surface to a bay. `<kind>` is one of `shell`, `agent`,
`cmd`, or `edit`. `--bay` selects which bay (default: current).
Defaults to a vertical split in the current tmux window. Use
`--window` for a new tmux window.

Alias: `bay sf new`.

```
bay surface new shell                       # shell as split pane
bay surface new shell tests                 # surface named "tests"
bay surface new agent codex                 # agent as split pane
bay surface new cmd "npm test" tests        # named cmd surface
bay surface new shell --window              # shell in new tmux window
bay surface new shell --bay b1               # in a different bay
bay surface new cmd "git pull --ff-only" --bay home
bay sf new edit                             # editor surface
```

#### `bay surface close <name> [--bay BAY] [--dock DOCK] [--force]`

Close a named surface. Pass `self` to close the current pane's
surface. Two protections may apply (both skipped by `--force`):

- **Last surface in bay, non-interactive invocation**: requires
  a second close attempt for the same bay while a tmux status
  message is still displayed (~1.5 seconds; the message duration *is*
  the deadline). The first attempt flashes the message and exits 0
  without closing. Designed to keep a stray `Option+W` from collapsing
  a bay's only pane. Interactive command-line closes proceed on
  the first command. Agents invoking close programmatically should pass
  `--force`.
- **Agent surface (non-last)**: TTY y/N prompt to protect conversation
  context. Auto-confirms when stdin is not a terminal.

```
bay sf close shell-2
bay sf close self
bay sf close agent --force
```

#### `bay surface restore [--list]`

Restore the most recently closed surface in the current dock
(undo-close). Bay keeps a per-dock LRU queue of the last 10
bay-initiated closes or sync-discovered pane exits, retained for 1
hour. Pops the top entry and recreates the surface in its parent bay.
If the parent bay is already gone (e.g. the 60s orphan-cleanup grace
window elapsed), the entry is silently discarded — call again to skip
past stale entries.

Agent surfaces relaunch with the agent's configured `resume_args`,
so the prior session continues rather than starting fresh. The
agent's `launch_args` (launch-only flags such as `--model`) are not
replayed — the resumed session keeps its own session state.

```
bay sf restore            # restore most recent
bay sf restore --list     # show the queue without restoring
bay restore               # alias
```

Bound to `Option+Z` in tmux (mirror of `Option+W`).

#### `bay surface show [name] [--bay BAY] [--dock DOCK]`

Print details for a surface. Defaults to the current pane's surface.

#### `bay surface rename [old] <new> [--bay BAY] [--dock DOCK]`

Rename a surface. With one arg, renames the current surface.

### Top-level shorthands

```
bay shell [name]       → shell as split pane (--window for new window)
bay agent [type]       → agent as split pane (--window for new window)
bay edit [bay]         → bay editor (--dock for all bays)
bay home               → focus/create dock checkout shell
bay restore            → bay surface restore (undo-close)
bay ls                 → list bays in current dock
bay tree               → list everything
bay pwd                → show current bay context
bay recover            → reconstruct state after reboot
```

#### `bay edit [bay] [--dock] [--editor CMD] [--window|--split h|v]`

Open the bay's root directory in an editor. Use the editor's
file browser to navigate within the project. To edit individual files,
open a shell instead.

Terminal editors (nvim, vim) create a tracked surface, splitting the
current window by default. Use `--window` for a new tmux window. The
surface is cleaned up when the editor exits. GUI editors (Cursor, VS
Code, Zed) launch and
return — running `bay edit` again focuses the existing window.

Editor resolution: `--editor` flag > `default_editor` in config >
`$VISUAL` > `$EDITOR` > probe (cursor, code, zed, nvim, vim).

```
bay edit                    # open current bay (default)
bay edit b1                 # open specific bay by ID
bay edit --bay home         # open the dock checkout
bay edit --editor vim       # use a specific editor this time
bay edit --dock             # dock editor (all bays)
bay edit --window           # terminal editor in new window
```

For editor configuration, see `bay config editor` below.

#### `bay config edit|show|path|editor [name]`

Manage bay's config file (`~/.config/bay/config.toml`).

```
bay config                      # show config-format docs
bay config edit                 # open config in your editor
bay config show                 # print effective config (TOML)
bay config path                 # print config file path
bay config editor               # print resolved editor command
bay config editor cursor        # set the default editor
```

The `editor` subcommand replaces the old `bay edit --set` and
`bay edit --show` flags.

#### `bay shell [name] [--bay BAY] [--dock DOCK] [--window|--split h|v]`

Open a shell surface in a bay. Defaults to a vertical split in
the current tmux window. Use `--window` for a new tmux window.

```
bay shell                   # shell as split pane
bay shell logs              # named "logs"
bay shell --window          # shell in new window
bay shell logs --bay b1       # in a different bay
bay shell --bay home        # shell in dock checkout
bay agent codex --bay home  # agent in dock checkout
```

### Global commands

#### `bay ls [--json] [--rows] [-s]`

List bays in the current dock. Human output truncates long bay names and
branches; use `bay show` or `--json` for full values. Use `--json` for
automation.

```
bay ls
bay ls --json
bay ls --json --rows
bay ls -s
```

#### `bay tree [--json] [--rows] [-l] [--dirty]`

Show the full hierarchy: docks, bays, surfaces. Human output truncates
long bay names and branches; use `bay show` or `--json` for full values.

```
bay tree
bay tree --json
bay tree --json --rows
bay tree --dirty
```

#### `bay pwd [--json]`

Show the current bay context.

```
bay pwd
bay pwd --json
```

#### `bay recover`

Reconstruct all docks, bays, and surfaces after a reboot.
Recreates tmux sessions and windows, relaunches agents with
resume args (e.g., `--continue`), starts the monitor. Idempotent.

Scope depends on where it runs: inside a dock's tmux session it
recovers just that dock; outside tmux it recovers every dock. Use
`bay dock recover <name>` to target one dock from anywhere.

#### `bay doctor`

Health checks: config validity, dock checkout accessibility, agent availability,
manifest consistency, monitor status, tmux keybindings, bay awareness
in dock checkouts, and (when `[describe]` is enabled) auto-description
summarizer health: it warns after three consecutive summarizer failures,
with the last error. If bay descriptions stop appearing, run this first.

#### `bay monitor start|stop|status`

Manage the background pane monitor. Detects when agents are waiting
for input and highlights those tmux windows. Also handles
activity-gated merge detection.

`bay monitor status` reports whether the running monitor is current
with the installed `bay` binary. Use `--verbose` for version, executable,
heartbeat, and reload-generation details. Use `--json` for structured
output with `running`, `pid`, `freshness`, `reason`, `monitor`, and
`current` fields. `freshness` is one of `not_running`, `current`,
`stale`, `unknown`, or `different_executable`.

Waiting detection uses three mechanisms, all feeding into
`--next-waiting` navigation:

- **Bell** — `window_bell_flag` set when an agent sends `\a`. Used by
  Claude Code's `PermissionRequest` hook (installed by `bay setup`).
- **Turn-complete hooks** — `@bay-waiting=1` set by an agent when it
  finishes a turn. `bay setup` installs Claude's `Stop` hook in
  `~/.claude/settings.json`, Antigravity's `Stop` hook in
  `~/.gemini/config/hooks.json`, and Codex's `notify` entry in
  `~/.codex/config.toml`. The flag auto-clears on window focus via the
  `after-select-window` tmux hook (also installed by `bay setup`).
- **Pattern-based** (fallback) — the monitor matches the last few
  lines of each agent pane against regexes in
  `~/.config/bay/waiting-patterns.txt`. One regex per line; the monitor
  reloads on every cycle (default 3s), so no restart is needed.

### Dock management

```
bay dock new [name] [--path PATH] [--worktree-dir PATH] [--agent TYPE] [--terminal APP]
bay dock init [name]
bay dock ls [name] [--json] [--rows]
bay dock show <name>
bay dock close <name> [--force]
bay dock recover <name>
bay dock sync [name]
```

`bay dock new` runs `bay dock init` after creating the dock, then opens
a `home` shell at the dock checkout path. Auto-bootstrap through
`bay new` creates only the requested worktree bay. `bay dock init` sets
up bay awareness: it writes a `CLAUDE.md` pointing at `bay agent-guide`
into the dock's worktree directory (Claude Code reads `CLAUDE.md` from
ancestor directories, so every bay picks it up with no files inside the
worktrees), and appends the same pointer to each agent's project file
(e.g., `CLAUDE.local.md`) in the dock checkout — but only when the repo
already gitignores that file. Bay never edits `.gitignore` or leaves
files git would report as dirty, and non-git checkouts are left
untouched. `bay dock sync` copies
`.worktreeinclude` matches from the checkout into existing worktrees.

`.worktreeinclude` uses gitignore syntax. Each pattern is resolved by
git; matching files are copied from the dock checkout into new worktrees.
Bay refuses to sync matches that are tracked in git or not covered by
`.gitignore` (refusals are logged to stderr; valid matches are still
copied) — only files that cannot be checked in are copied.

## Typical workflows

### Spin up a bay for a task

```
bay new auth-fix --dock labs
```

Creates a worktree bay. Opens a shell by default. Use `--agent`
to launch the dock's default agent instead. Drop `--dock` to target
the current dock from your tmux session.

### Track progress from inside

Branch, PR number, dirty, and merged are all tracked automatically.
Branch comes from the worktree on every display. PR is looked up via
`gh pr view` and cached. Merged is set to true automatically when bay
detects a merge. Just commit, push, and open a PR — bay sees all of
it.

### Open a shell alongside your agent

```
bay shell
```

Opens as a split pane alongside the agent. For a separate tmux window:

```
bay shell --window
```

### Open an editor for the bay

```
bay edit
```

Terminal editors appear as surfaces in `bay go`. GUI editors launch
and return — manage them with your OS window manager.

### Check on all active work

```
bay ls
```

Shows every bay, dirty/merged flags, branch, PR, and waiting indicators.

### Navigate to a waiting agent

Within current bay:

```
bay surface go --next-waiting
```

Across bays in the dock:

```
bay surface go --next-waiting
```

### Use an existing directory

```
bay new my-foo --dock labs --dir ~/projects/foo
```

External bays let bay manage tmux recovery for directories it
does not own. Closing removes surfaces but leaves the directory
untouched.

### After a PR merges

Merged is set to true automatically when bay detects a merge. Then either
keep the bay or close it.

**Do not** run `git checkout main` / `gh pr merge --delete-branch` in a bay
worktree — the dock root already holds the default branch checked out, so
those fail. To get a clean base, detach instead.

Keep the bay for the next task — reset it to a clean detached base and drop
the merged branch:

```
bay tidy b1
```

Close the bay entirely:

```
bay close b1
```

Or batch-close finished bays (not dirty, not pending):

```
bay close --done
```

### Manage multiple concurrent PRs

```
bay new auth-fix --dock labs
bay new perf-regression --dock labs
bay new update-deps --dock labs
bay ls
```

Each bay gets its own worktree. They share the same dock checkout but
work on independent branches.

### Recover after reboot

```
bay recover
```

Reconstructs all tmux sessions, surfaces, and agent sessions (with
resume args). Prints attach commands.

### Zero-config quickstart

From any git repo directory, with no prior bay configuration:

```
bay new
```

Bay auto-detects the checkout, creates a dock, probes for an agent on
PATH, and opens a bay. No config file needed for basic usage.
