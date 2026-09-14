# Bay

Multi-session bay management across tmux and git worktrees.

Bay manages concurrent bays — each an isolated git worktree with its own tmux window, AI agent, editor, and shells. Run multiple PRs in parallel and recover everything after a reboot.

For developers who work on multiple branches simultaneously and use tmux as their terminal multiplexer.

```
cd ~/projects/myproject
bay new            # auto-bootstrap a dock + create a worktree bay
bay new --agent    # ...with the dock's default AI agent
bay edit           # open your editor on the current bay
bay go             # fuzzy-pick a bay (Option+g for the picker)
bay tidy           # reset a merged bay's worktree for reuse
bay recover        # rebuild docks/bays/surfaces after a reboot
```

## What it does

- **Worktree lifecycle** — create, close, rename, and **tidy** (reset a merged worktree back to a clean base for reuse) bays backed by git worktrees or external directories. Safety checks on close (dirty files, unlanded commits). Each bay is an isolated checkout.
- **Prepare steps** — repo-defined setup commands (install deps, build, warm caches) run in the background the moment a bay is created, so a fresh worktree is ready without blocking creation.
- **Surface model** — each bay contains one or more **surfaces** (agent panes, shell panes, command panes, GUI editors). Bay tracks them all and recovers them after reboot.
- **Home checkout surfaces** — `bay home` and `--bay home` open shells, agents, editors, or command panes in the dock's canonical checkout without giving it worktree cleanup behavior.
- **Editor integration** — `bay edit` opens your bay in cursor, VS Code, zed, nvim, or vim. `bay edit --dock` for multi-root.
- **Agent support** — optionally launch AI agents (Claude Code, Codex, Antigravity). Agent surfaces resume on restart and on undo-close via configured `resume_args`. Closing an agent surface prompts for confirmation. Built-in model profiles (`fable`, `opus` (1M context), `sonnet`, `haiku`) launch Claude pinned to a model — `bay agent opus` or `Option+o o` just works — and config can add, adjust, or disable profiles, all without affecting how resumed sessions pick their model.
- **Attention signaling** — bay highlights a bay's tmux tab when its agent needs permission or finishes a turn; `Option+r` jumps to the next one waiting.
- **Scoped navigation** — `bay surface go` picks surfaces within the current bay; `bay go` picks bays within the current dock. Cycling flashes a brief position indicator.
- **Command palette** — `Option+p` opens a VS Code-style palette in a tmux popup: fuzzy-search every bay command, see its hotkey if it has one, and launch without leaving the keyboard.
- **Session-scoped keybindings** — bay's command keys are live only in the tmux sessions bay manages; in any other session the keystroke goes straight to the application, so bay's keys never shadow yours outside its own docks. Window and pane navigation keys stay global.
- **Hierarchical browsing** — `bay ls` lists bays in the current dock; `bay tree` shows the full hierarchy including surfaces.
- **Auto-descriptions** — optionally, bay summarizes each bay's agent conversation (Claude/Codex) in the background to fill in its description, so `bay ls` and the picker stay meaningful without manual labeling. Opt-in (`bay setup` offers it); anything you set by hand is left untouched.
- **Status line** — `bay status-line <field>` provides bay info for tmux status bar composition.
- **Shell completion** — tab-complete bay IDs, dock names, and flag values in bash, zsh, and fish.

## Install

This is a private repository. Build from source:

```
git clone git@github.com:commontoolsinc/bay.git
cd bay
go install ./cmd/bay
```

Then run `bay setup` to create your config and install shell completions.

## Documentation

- **[Tutorial](docs/tutorial.md)** — hands-on walkthrough from install to cleanup
- **[Human Guide](docs/human-guide.md)** — configuration, commands, and daily workflow
- **[Agent Reference](docs/agent-reference.md)** — comprehensive reference for AI agents operating inside bays
