package cli

// configHelpText is the long-help body of the `bay config` parent
// command — describes the schema users hand-edit. Lives in its own
// file so the parent command code stays focused.
const configHelpText = `Bay Configuration — ~/.config/bay/config.toml

Bay's config file holds user preferences. Docks and bays
are tracked automatically in the manifest (~/.local/share/bay/manifest.json).
A config file is only needed for customization — bay works without one.

DEFAULTS

  default_agent = "claude"        # agent for 'bay agent' (probed on setup)
  default_editor = "cursor"       # editor for 'bay edit' (probed on setup)

  Set via CLI: 'bay config editor cursor' or 'bay setup'.

  Built-in agents: claude, codex, antigravity. Built-in editors: cursor,
  code, zed, nvim, vim. These don't need config entries — bay knows
  their commands, resume args, and GUI detection.

CUSTOM AGENTS (optional)

  Override a built-in or add a new agent:

  [agents.my-agent]
  command = "my-agent-cli"
  args = ["--flag", "value"]      # args on every invocation (incl. resume)
  launch_args = ["--x", "y"]      # args on fresh launch only, not resume
  resume_args = "--continue"      # added on recovery
  project_file = ".my-agent.md"   # checked when creating a dock

  Session-start flags like --model belong in launch_args: resumed
  sessions (recover, undo-close) keep their own model, and replaying
  --model would override it. Each list element is one argument —
  elements with spaces or special characters are shell-quoted.

MODEL PROFILES

  A profile extends a base agent — same client, pinned model, its own
  name. Built-in: fable, opus, sonnet, haiku (claude --model <name>;
  opus pins opus[1m], the 1M-context variant), usable with no config
  anywhere an agent name works: 'bay agent fable', 'bay new
  --agent=opus', dock defaults, default_agent.

  Define your own the same way (inherits command/args/resume_args/
  project_file from the base; args and launch_args append; one level
  only — extend claude, not another profile):

  [agents.fable-fast]
  extends = "claude"
  launch_args = ["--model", "fable", "--fast"]

  Adjust a built-in by redefining fields ([agents.opus] launch_args =
  ["--model", "opus"] for plain opus), replace it by setting command
  or extends, or remove it with disabled = true.

CUSTOM EDITORS (optional)

  Set default_editor to any command on your PATH. Built-in editors
  (cursor, code, zed) are detected as GUI (fire-and-forget). All
  others are treated as terminal editors (tracked tmux pane).

  To mark a custom editor as GUI:

  [editors.sublime]
  gui = true

PER-DOCK OVERRIDES (optional)

  Override defaults for a specific dock:

  [docks.myproject]
  agent = "codex"                 # override default agent
  terminal = "ghostty"            # host terminal app

  [docks.myproject.agent_args]
  claude = ["--dangerously-skip-permissions"]  # per-agent args override

  [docks.myproject.launch_agent_args]
  codex = ["--model", "o3"]       # per-agent launch-only args override

MONITOR (optional)

  [monitor]
  interval_seconds = 5            # default: 3 seconds

AUTO-DESCRIPTIONS (optional)

  [describe]
  enabled = true                  # default: false (opt-in); 'bay setup' offers this
  # Summarizer argv. The prompt is appended as the final arg. An "{out}"
  # element becomes a temp file bay reads the answer from; otherwise it
  # reads stdout. Set the model by editing the command. Unset = codex default.
  command = ["codex", "exec", "--sandbox", "read-only", "--skip-git-repo-check",
             "--ephemeral", "-c", "model_reasoning_effort=low",
             "-m", "gpt-6-luna", "-o", "{out}"]

EXAMPLE MINIMAL CONFIG

  An empty file works — bay auto-detects agents and editors.
  Run 'bay setup' to set preferences interactively.
`
