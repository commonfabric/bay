# Bay — Tutorial

You're working on a bug fix when you realize the auth refactor should
really be a separate PR. Or a teammate asks for a review while you've
got uncommitted changes and a dev server running. Either way, you need
multiple copies of the repo, each on its own branch, each with their
own state — without stashing, cloning, or losing your place.

Bay gives each task its own **bay** — an isolated git worktree
inside a **dock** (a tmux session for one project). Each bay has
**surfaces**: the shells, AI agents, and editors you work in and
navigate between. Jump between tasks instantly, and pick up right
where you left off — even after a reboot.

This tutorial walks through the full lifecycle in about 10 minutes.

**You'll need:** bay (install with `go install ./cmd/bay`), tmux, and git.

**Cleaning up:** When you're done, `bay dock close bay-tutorial --force`
removes all bays and bay state. Then `rm -rf /tmp/bay-tutorial`
removes the checkout itself.

---

## 1. Create your first bay

Grab a throwaway repo to experiment with. If you're already inside
tmux, open a new terminal window first — bay creates its own tmux
session and you'll attach to it in a moment.

```
git clone https://github.com/octocat/Hello-World.git /tmp/bay-tutorial
cd /tmp/bay-tutorial
bay new
```

Bay creates a bay with a clean copy of your default branch.
Attach to the dock:

```
tmux attach -t bay-tutorial
```

You're now in a tmux session managed by bay, with a shell cd'd into
your bay's worktree. Run `bay ls` to see what bay created:

```
bay ls
```

```
dk bay-tutorial *
  bay b1 *  n=1
```

One dock, one bay with a shell surface. Everything
you do from here — creating more bays, adding shells,
navigating — happens inside this session.

> **What just happened?** Bay detected your git repo, created a
> worktree at `/tmp/bay-tutorial-worktrees/<name>`, and started
> a tmux session called `bay-tutorial` with a window for your
> bay. The worktree starts on detached HEAD — it has the
> default branch's content but isn't on any named branch yet. Bay
> tracks all of this in its state file at
> `~/.local/share/bay/manifest.json` — you don't need to touch it.

## 2. Work in your bay

Your bay is a normal directory with a normal git checkout. To
start working on a branch, use `--branch` when creating a bay:

```
bay new --branch fix/login-bug
```

That creates a second bay on the `fix/login-bug` branch (or
checks it out if it already exists on the remote).
Within a few seconds the tmux tab name updates to `b2.login-bug` — bay
watches your branch in the background and keeps the name in sync.

You can also create a branch manually in an existing bay with
`git checkout -b`, and bay picks up the change the same way.

Now you realize the auth refactor should be its own PR. Create a third
bay:

```
bay new --branch fix/auth-refactor
```

Each bay is completely independent — different branch, different
files, different tmux tab. Your login bug work is untouched.

A teammate pings you for a review? Create a bay for it:

```
bay new review
```

That creates a bay with no branch — useful for scratch work,
exploring code, or running tests against main.

To see all bays in the dock, use `bay ls`:

```
bay ls
```

```
dk bay-tutorial *
  bay b1             n=1
  bay login-bug      br=fix/login-bug  n=1
  bay auth-refactor  br=fix/auth-refactor  n=1
  bay review *       n=1
```

Four bays in creation order. The two with `--branch` show their
branch; `review` and `b1` have no branch. The `*` marks where you
are now.

Switch between them with `Option+g` (bay picker, requires
`bay setup`) or `bay go`.

## 3. Add tools to your bay

A bay starts with one shell, but you can add more surfaces.

Split a second shell (for running tests while you edit):

```
bay shell
```

Run a long-lived command in its own pane:

```
bay surface new cmd "top" monitor
```

(`bay surface new cmd` creates a `cmd` surface; the second argument is its display
name.)

Open your editor on the bay directory:

```
bay edit
```

This opens your editor on the bay's root directory — use the
editor's file browser to navigate within the project. Terminal editors
(nvim, vim) run as tracked surfaces in tmux. GUI editors (Cursor,
VS Code, Zed) launch and return — run `bay edit` (or `Option+e`)
again to switch back to the editor window.

Run `bay tree` to see the current bay's surfaces:

```
bay tree
```

```
dk bay-tutorial *
  bay login-bug *  br=fix/login-bug  dir=b2
    sf shell *    ty=shell
    sf shell-2    ty=shell
    sf monitor    ty=cmd
```

The `dir=b2` column shows the on-disk worktree directory basename,
which doubles as the bay's stable handle for worktree bays. It only
appears when the dir basename and Name differ; auto-numbered bays
(no Name yet) display the handle directly in the `bay` column. For
external bays (`bay new --dir`), an `id=...` column appears alongside
when the directory basename doesn't match the bay's stable handle.

Three surfaces, all navigable. `bay ls` lists bays in the current dock;
`bay tree` shows bays and their surfaces.

## 4. Navigate

- `Option+h` / `Option+l` — previous / next window (vim-style left/right)
- `Option+j` / `Option+k` — select pane down / up
- `Option+Shift+H/L` — select pane left / right (for horizontal splits)
- `Option+g` — bay picker (type to filter, Enter to select)

With only a handful of tabs, `Option+h/l` cycles fine. Once you have
more than a few bays, the picker is usually faster — especially
with descriptions. Bay fills those in for you: it summarizes each bay's
agent conversation in the background, so the picker is meaningful even
if you never run `bay describe` yourself.

All of these work without the Option-key shortcuts too:

```
bay surface go shell        # jump to the shell surface by name
bay go review    # bay picker filters by ID, Name, branch, or PR
```

## 5. Add an AI agent

```
bay agent
```

This launches your default agent (bay auto-detects Claude Code, Codex,
or Antigravity on your PATH) as a split pane. Use `bay agent --window` if
you want a separate tmux window. You can also create a
bay with an agent as the first surface: `bay new --agent`.

Add a shell alongside it
with `Option+s` (or `bay shell`).

When the agent waits for your input, bay highlights it. Use `Option+h`
/ `Option+l` to cycle between windows.

## 6. Inspect

To see every bay and surface at once, use `bay tree`:

```
bay tree
```

```
dk bay-tutorial *
  bay login-bug *  br=fix/login-bug
    sf shell    ty=shell
    sf monitor  ty=cmd
  bay review
    sf agent    ty=agent ag=claude
    sf shell    ty=shell
```

Other useful commands:

- `bay pwd` — where am I? (dock, bay, surface)
- `bay show` — detailed info about the current bay
- `bay doctor` — health checks (keybindings, agents, dock checkouts)

## 7. Clean up

Close a bay when you're done with it. Use the bay **ID**
(`b1`, `b2`, etc.) — visible in `bay ls` and `bay tree`. Friendly
Names like "review" are display labels; the ID is what every command
takes:

```
bay close b1     # by ID
bay close self   # close the current bay
```

Bay checks for uncommitted changes and unlanded commits first. If
everything is pushed, included in a merged PR, or already present on
the default branch as an equivalent patch, bay also deletes the local
git branch — no stale branches left behind. For review bays where PR
code was applied as dirty files, bay can still close when the whole
worktree exactly matches a recoverable git ref. Use `--force`
to skip the safety checks.

Closing an *individual* agent surface (`bay sf close agent`) prompts for
confirmation, since agents carry valuable conversation context. Use
`--force` (or `-f`) to skip the prompt.

When a PR is merged, bay detects it in the background. Bays with
unmerged branches show `st=pending`. Clean up all finished bays:

```
bay close --done             # close bays not dirty or pending
bay close --done --dry-run   # preview first
```

## 8. Recovery

Reboot? Tmux sessions are gone, but bay remembers everything:

```
bay recover
```

Recreates all tmux sessions and surfaces. Agents resume with
`--continue`. Terminal editor surfaces relaunch. GUI editors are
not tracked — reopen them with `bay edit`.

---

## Going further

**Full setup:** `bay setup` installs keybindings and shell completions.
It's optional — everything works without it, but the keybindings make
navigation instant. Bay's command keys only fire inside bay's own tmux
sessions — in any other session the key reaches your application
untouched — while the `Option+h/l/j/k` navigation keys work everywhere.

**Multiple checkouts:** create a separate dock for another checkout:
`bay dock new backend --path ~/projects/backend`. Explicit dock
creation opens a `home` shell in that checkout.

**Agent awareness:** new docks write a one-line `CLAUDE.md` pointer
into the dock's worktree directory so agents in every bay know about
bay commands — nothing is added inside your repo unless it's already
gitignored.

**Explicit control:** The zero-config flow creates docks automatically.
For more control: `bay dock new myproject --path ~/projects/myproject`
creates the dock and opens its checkout as `home`.

**Status line:** Add `#(bay status-line full --window #{window_id})` to
your tmux `status-right` to always see your current bay label, branch,
PR, and status. Pass `#{window_id}` so each window gets its own tmux
`#()` cache entry — without it, tmux may show stale status from a
different window. Other field names: `id`, `name`, `dir`, `branch`,
`pr`, `status`, `dock`, `merged`. See the human guide for details.

---

**Next:**

- **[Human Guide](human-guide.md)** — full command reference, config
  options, and advanced features.
- **[Agent Reference](agent-reference.md)** — reference for AI agents
  operating inside bays.
