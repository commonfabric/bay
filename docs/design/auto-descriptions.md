# Auto-populated bay descriptions

Captured 2026-06-03. Revised 2026-06-05: conversation-first input,
read **both** Claude and Codex transcripts, git as body-enrichment and
fallback; summarizer is a configured command (default `codex exec`);
opt-in (off by default, surfaced in `bay setup`).

Related:
- `workspace-identity.md` defines Description as one of the three
  identity fields (ID / Name / Description). This doc is about *how it
  gets filled*, not what it means.
- `prepare.md` / `prepare-impl.md` — the detached-worker dispatch
  pattern this design reuses.
- `monitor-version-status.md` — the long-lived monitor process that
  owns the new cadence.

## The problem

Descriptions work end to end — stored in the manifest
(`Bay.Description`), validated, rendered in `bay ls`/`tree`/`show`, the
`M-/` flash, the `M-?` popup, and `--json`. But in practice they're
empty, so the dashboard shows nothing useful.

The only population mechanism today is a passive instruction that
`ensureBayAwareness` appends to `CLAUDE.local.md`
(`internal/engine/repo.go:100`): *"At session start: check the
workspace description with `bay describe`. If empty, set one..."* plus
the matching guidance in `agent-guide.md`. That instruction does not
work, for structural reasons:

- **No trigger.** At literal session start the agent has no task yet —
  the user hasn't said anything — so there's nothing to describe. By
  the time a goal exists, the instruction is buried off-screen.
- **No forcing function.** It's a low-salience meta-instruction
  competing with the user's actual request. Agents reliably skip it.
- **No feedback.** "Check the description" needs a tool call the agent
  won't spend when it assumes the field is empty anyway.

And agents launch cold: `buildAgentCommand` (`internal/engine/agent.go:54`)
sends no initial prompt, so nothing nudges the agent at the right
moment. Relying on agent goodwill via a static project-file line can't
populate descriptions reliably.

## The model

Populate descriptions from a backstop that requires no agent
cooperation: **summarize what the bay is working on from the agent
conversation**, on a slow cadence, out of band from any command.

The conversation is the richest signal of *intent* — the first
substantive user prompt *is* the goal, and recent prompts are the
current context. Intent is the one thing git doesn't capture, which is
exactly why the description should come from it (git history already
records what was *done*). We read **both** the Claude and Codex
transcripts for the bay's working directory and merge their typed
prompts by timestamp, since a bay may use either agent or both. Git
state (branch, recent commit subjects, a diffstat) only *grounds the
body* — "what's in flight" — and is the fallback input when no
transcript is readable. A cheap LLM call turns this into the "standing
brief" the `agent-guide.md` already describes (a stable first-line
label plus an optional short body), without the working agent ever
being distracted.

This is a *backstop*, not a replacement for human/agent ownership. Any
explicit `bay describe` (by the user, or by an in-session agent
following the existing guidance) marks the description as owned, and
the backstop never touches it again. Explicit beats automatic.

### Who owns what

| Concern | Owner | Why |
|---------|-------|-----|
| Cadence + staleness decision | the monitor | already a long-lived loop, iterates every bay, gates slow work on a cycle counter (merge detection) |
| Generating the description | a detached describe-worker | an LLM call is too slow/risky to run inline in the monitor's fast 3 s highlight loop |

The monitor *decides and dispatches*; the worker *executes*. This is
exactly how prepare already splits decision from work.

### Why not the alternatives (decided)

- **Inline in the monitor loop.** Rejected: a summarizer (`codex exec`)
  call is ~5–30 s and there may be many bays; that would stall the 3 s
  highlight path. Worse, the monitor hot-swaps its own binary mid-run
  via `syscall.Exec` (`monitor.go:144`), which would kill a long inline
  call. Detached workers survive a swap and isolate failures.
- **Inject into the working agent every turn.** Rejected: pollutes the
  user's working context on every turn, ties bookkeeping to the agent
  vendor's hook system, and still depends on the agent acting.
- **Lazy-on-view (refresh from `bay ls`/`show`).** Rejected once the
  monitor was in scope — it manufactured a trigger the monitor already
  provides, and tied refresh to read commands.

### Conversation-first, both agents, git as fallback (decided)

Input priority is **conversation first, git as enrichment/fallback** —
not the reverse. The description's job is *intent*, which only the
conversation holds; the diff is *mechanism*, which git already shows on
demand. Diff-first also fails exactly when descriptions matter most:
early bays (no diff yet, but the first prompt states the goal) and
research/review/debugging bays (clear intent, little or no diff). So
the goal line comes from the conversation; git (branch + commit
subjects + diffstat — the *intent-bearing* git signals, not a raw
diff) grounds the body and is the sole input only when no transcript is
readable.

We read **both Claude and Codex** transcripts (both formats verified)
and merge typed prompts by timestamp — a bay may use either or both.
Antigravity and any other agent have no reader yet, so they fall back
to the git-only path until a reader is added. Reading is independent of
which model *writes* the summary (codex — see below); the input is just
text.

### Summarizer: a configured command, defaulting to codex exec

The summarizer is a **configured argv** (`[describe].command`), run with
the full prompt appended as the final argument. The default is
`codex exec` at a cheap model (`gpt-5.4-mini`) and low reasoning effort,
chosen because unattended CLI calls have been reliable with it here.

Rather than encode codex's flags in Go (a `command` + `model` split), a
single argv is simpler and lets any CLI work — `llm`, `ollama`, a
wrapper script — by editing config. Two optional tokens bridge the gap
without bay knowing each tool's flags:

- `{prompt}` — substituted with the prompt if present; otherwise the
  prompt is appended as the last arg.
- `{out}` — substituted with a temp file path that bay reads the answer
  from; otherwise bay reads stdout. The codex default uses `-o {out}`
  because `codex exec` prints status chatter to stdout, not just the
  answer; clean stdout filters (e.g. `llm`) omit it.

The call still goes through a `Summarizer` interface (one concrete
command backend today) so tests stub it and a future API-key/local
backend can replace it without touching the monitor/worker plumbing.

**Off by default.** The backstop spends model calls and assumes a CLI
the user has, so it's opt-in: `enabled` defaults to false, and
`bay setup` surfaces it and offers to turn it on.

## Mechanism

### Eligibility (no clobber, no migration needed)

The worker writes a description only when:

```
Bay.Description == "" || Bay.DescriptionSource == "auto"
```

- Empty description → fill it (and stamp `source = "auto"`).
- Existing non-empty description with empty source (legacy, or a
  pre-existing manual one) → never touched. No migration write needed;
  empty source is treated as "not ours."
- Auto-set description → eligible for refresh.
- Explicit `bay describe` / `bay new --description` → `source = "user"`
  → never auto-touched.

There's no agent-type gate: the worker tries both transcript readers
and the git fallback for the bay's working directory, and no-ops if
none of them yield any signal.

### Staleness: coarse trigger in the monitor, precise dedup in the worker

To keep the monitor cheap (no transcript/log I/O in its loop), it uses
a **coarse** trigger. Never-described bays and empty descriptions left
by a failed summarizer form a bounded oldest-first backlog even after
their activity window expires. Once a quiet worker run establishes that
an inactive bay has no signal, it leaves the backlog. Auto-description
refreshes otherwise require activity within `activityWindow`. The
monitor does *not* read transcripts to decide. Due-ness is activity-aware:

- If the user has touched the bay since the last run
  (`LastActive > DescriptionSummarizedAt`), re-describe at the base
  `minInterval` cadence — a bay command is a strong hint the focus may
  have shifted.
- Otherwise we're only probing for *agent-only* activity the monitor
  can't see without reading logs, so we **back off**: the worker counts
  consecutive unchanged runs in `DescriptionStableStreak`, and the
  required interval doubles per quiet run (`minInterval` →
  `maxInterval` cap). A real change resets the streak to the base
  cadence.

The **worker** is the precise gate. It gathers the merged prompts + git
signal, hashes that assembled input, and compares to the hash stored in
a small per-bay state file under `DataDir`. If unchanged, it bumps
`DescriptionSummarizedAt`, increments `DescriptionStableStreak`, and
exits *without* calling the summarizer. An over-eager dispatch is
therefore cheap — it reads a few files and returns; the costly `codex`
call happens only when the input actually changed.

This split means the monitor never needs to know where Claude or Codex
store their logs. The cost is that `LastActive` tracks bay *commands*,
not agent typing: a busy agent in a bay the user isn't navigating gets
its description refreshed only when a probe fires (at the backed-off
cadence) or the user next touches the bay. That's an accepted trade for
keeping the monitor out of the logs — without the backoff, a bay you
focused and walked away from would re-dispatch a worker (process spawn +
transcript walk + git) every `minInterval` for the whole
`activityWindow`. If the latency on agent-only refresh ever bites, the
activity gate can be upgraded to a cheap cwd→log-mtime index.

### Cadence and bounds

- Reuse the cycle-gate pattern: a new `DescribeCheckCycles` constant
  (start at 100 ≈ 5 min at the default 3 s interval, matching
  `MergeCheckCycles`).
- Backfill never-described bays (and retry failed empty descriptions)
  regardless of `activityWindow`, capped by the per-cycle dispatch
  limit. Successfully probed quiet bays leave this backlog.
- Only refresh existing auto descriptions for bays active within
  `activityWindow` (the existing 2-hour window).
- A bay re-described at the base `minInterval` only while it sees fresh
  activity; an unchanging bay backs off toward `maxInterval` (see
  Staleness) so a focused-then-abandoned bay isn't re-probed every cycle.
- Per gate cycle, cap how many describe-workers are dispatched (avoid a
  thundering herd on monitor restart). A per-bay dispatch lock prevents
  two workers racing on the same bay.

### Worker steps (`bay describe-worker --dock <d> --bay <b>`)

1. Load manifest; re-check eligibility (description still empty or
   `auto`). Acquire a per-bay describe lock.
2. Gather conversation prompts for the bay's symlink-resolved `Path`
   from **both** readers and merge by timestamp:
   - **Claude:** encode the cwd (every run of non-alphanumerics → `-`)
     under `~/.claude/projects/`; from its `*.jsonl` keep `type=="user"`
     entries with **string** `content` (drops tool-result arrays), minus
     `isMeta`/`isSidechain` and content starting with `<bash-` /
     `<command-` / `<local-command-` / `<system-reminder>`.
   - **Codex:** scan `~/.codex/sessions/**/*.jsonl` and match line-1
     `session_meta.payload.cwd == cwd`; keep `event_msg` entries with
     `payload.type == "user_message"` (text at `payload.message` — this
     channel already excludes injected AGENTS.md/environment context).
   Both carry per-entry ISO-8601 timestamps.
3. Gather the git signal for the body: current branch (omitted when
   detached — `rev-parse --abbrev-ref HEAD` returns the literal `HEAD`,
   which is the normal state for a fresh worktree and after `bay tidy`),
   recent commit subjects (`base..HEAD`), a `--stat` diff against the
   merge-base, and the **work-start boundary** — the commit date of
   `merge-base(HEAD, default)`, i.e. where the current line of work
   diverged, just after the last merge the bay incorporated.
4. **Scope to the current work, then assemble.** Drop prompts from
   before the work-start boundary so a bay reused after a merged PR
   describes its new focus, not the prior task (keep all if the boundary
   is unknown or would empty the set). Of what remains, the earliest
   prompt is the goal anchor and the last few are current context.
   Conversation drives the goal, git grounds the body. No prompts → fall
   back to git-only.

   *Revised 2026-10:* typed prompts alone proved too thin. Follow-ups
   are mostly short steps ("open pr", "split it") that only make sense
   next to the agent's replies, so descriptions tracked the latest step
   and could not say where the work stood. The input now also carries
   the compaction summary Claude Code writes on `/compact` (the session
   history) and the agent's latest turn-final reply (the current stage),
   and keeps up to 30 prompts (first plus most recent) instead of the
   first plus the last six. Codex subagent and `codex exec` sessions are
   skipped, since they share the bay's cwd but are not the user's
   conversation. No signal at all (no prompts, no commits, empty
   diff) → stamp `DescriptionSummarizedAt` (so the monitor paces
   re-checks) and leave the description empty.
5. If the assembled input's hash matches the stored hash (per-bay state
   file under `DataDir`), bump `DescriptionSummarizedAt`, increment
   `DescriptionStableStreak` (feeding the monitor's backoff), and exit
   (no summarizer call).
6. Summarize: build one prompt (instruction + assembled input) and run
   the configured `command` argv with it (appended, or via `{prompt}`),
   reading the answer from the `{out}` temp file or stdout, with a
   timeout. The default `codex exec` argv is non-interactive and runs
   read-only, `--skip-git-repo-check`, `--ephemeral` (no persisted
   session), low reasoning effort, and `-o {out}` (codex's stdout
   carries chatter).

   Auth comes from the configured CLI's own login (for codex,
   `~/.codex/auth.json`, auto-refreshing), so a detached worker running
   as the logged-in user just works. **Failure is non-fatal**: if the
   call errors, times out, returns nothing, or auth has lapsed, the
   worker logs and stamps `DescriptionSummarizedAt`, leaving the
   existing description untouched. Stamping on *every* non-success
   terminal path is what paces retries to the min-interval instead of
   every cycle — and stops a failing or signal-less bay from sitting at
   `summarizedAt == 0` and starving healthy bays in the oldest-first
   dispatch queue. A failed run also **resets** `DescriptionStableStreak`
   (unlike a genuine no-op, which grows it): the input changed but stayed
   unsummarized, so the retry should hold at the base cadence rather than
   backing off as if the bay were quiet.
7. Sanitize and clamp the output to the description limits (first line
   ≤ 80 on a word boundary, total ≤ 2000, no tabs/CR), then guard with
   `ValidateDescription`. An empty or (defensively) still-invalid result
   is logged and stamped, not written.
8. `manifest.LockedUpdate`: re-check `source != "user"` under the lock
   (the user may have set it meanwhile), then write `Description`,
   `DescriptionSource = "auto"`, `DescriptionSummarizedAt = now`,
   `DescriptionInputHash = hash`, and reset `DescriptionStableStreak = 0`
   (content changed → back to the base cadence).

### Summarization prompt shape

Instruct the model to emit only the description (no preamble), as a
standing brief: the first line is a short imperative goal/scope label
(≤ 80, no trailing period) derived from the **conversation** intent; an
optional blank line then 1–3 lines of current context, which may draw
on the git signal for what's in flight. Explicitly *not* a changelog of
actions — git history already records what was done, so the body is
"where things stand," not "what changed."

*Revised 2026-10:* the body is now a status card for a developer
returning to the bay: a `Stage:` line (where the work stands, with PR
numbers and other concrete names), a `Next:` line (the next step, or
what is blocked and on whom), and optionally one caveat. The first line
is the purpose of the whole effort, taken from the overall arc rather
than the latest request.

## Cascade

### Manifest (`internal/manifest`)

Add to `Bay`:
- `DescriptionSource string json:"description_source,omitempty"` —
  `""` (not ours) | `"user"` | `"auto"`.
- `DescriptionSummarizedAt int64 json:"description_summarized_at,omitempty"`.
- `DescriptionInputHash string json:"description_input_hash,omitempty"` —
  hash of the last summarized input, for the no-op skip. (Kept in the
  manifest rather than a sidecar so all backstop state is atomic under
  the manifest lock.)
- `DescriptionStableStreak int json:"description_stable_streak,omitempty"` —
  consecutive worker runs that found nothing changed; drives the
  monitor's re-probe backoff for agent-only activity (see Staleness).

No migration pass; empty source is interpreted as "don't touch."

`bay tidy` (reset a worktree to a clean detached base for reuse) clears
an `auto` description — Description/Source/SummarizedAt/InputHash/
StableStreak — so a reused bay doesn't keep advertising the now-merged
task; a user-set
description is left alone. The backstop then regenerates from the new
work (whose recency boundary is the fresh detached base).

### Config (`internal/config`)

New `DescribeConfig` (TOML `[describe]`):
- `enabled *bool` — pointer so absent ≠ explicit false. **Default
  off** (opt-in); `bay setup` offers to enable it.
- `command []string` — summarizer argv (prompt appended or via
  `{prompt}`; answer from `{out}` temp file or stdout). Default
  `DefaultDescribeCommand` = `codex exec … -m gpt-5.4-mini -o {out}`.

(Cadence/min-interval constants stay in code unless a need to tune them
appears. The summarizer is invoked behind an interface; the configured
command is the only concrete backend today, but an API-key or local
backend can be added without touching the worker.)

### Engine + Monitor

- `DispatchDescribeWorker(dock, bayID)` mirroring
  `DispatchPrepareWorker`; the detached-process helper is generalized to
  `startWorkerProcess` / `e.startWorker` so both share it.
- Monitor `CheckOnce`: on the `DescribeCheckCycles` gate, dispatch
  workers for eligible bays (coarse trigger — no transcript I/O; see
  Staleness), respecting `activityWindow`, the per-cycle cap, and an
  activity-aware due check: base `minInterval` when the user has touched
  the bay since the last run, otherwise a `DescriptionStableStreak`-driven
  backoff up to `maxInterval`.

### Reader + worker packages

- `internal/transcript` — per-agent readers (Claude + Codex) producing
  a normalized `[]Prompt{Timestamp, Text}`, plus cwd→session lookup and
  merge-by-timestamp. Pure and fixture-testable.
- `internal/describe` — the worker: gathers prompts + git signal,
  assembles the input, and calls the summarizer behind an interface so
  tests stub it (no real `codex` call in tests), mirroring
  `prepare.Worker`'s injectable runner. Git-signal gathering reuses the
  engine's existing git helpers.

### CLI

- New hidden `describe-worker` command (like `prepare-worker`; opts out
  of monitor autostart).
- `bay describe` and `bay new --description` set
  `DescriptionSource = "user"`.

### `bay describe` reconciliation

The existing path is unchanged for users and agents; it just stamps
`source = "user"`. This is what reconciles the two mechanisms: an agent
that follows the `agent-guide.md` guidance and actively maintains the
description takes ownership, and the backstop steps aside. Known
limitation: an agent that sets it once and then goes stale keeps an
owned-but-stale description; the human can clear it (→ empty → eligible
again).

### Tests

- `transcript`: fixture JSONL for **both** agents → Claude path
  encoding + filter (drops tool-result arrays, `isMeta`, `isSidechain`,
  `<bash-`/`<command-`/`<system-reminder>`); Codex cwd match on line-1
  `session_meta` + `user_message` extraction; and merge-by-timestamp
  across the two.
- `describe`: stubbed summarizer + fixture manifest/transcripts/git →
  eligibility (won't clobber `user` or legacy), conversation-first vs.
  git-only fallback selection, validation/truncation, the input-hash
  skip (no summarizer call), and the per-bay lock preventing double-run.
- `monitor`: fake cycle/clock → dispatches only for eligible bays,
  honors the per-cycle cap, `minInterval`, and `activityWindow`.

### Docs

- `docs/human-guide.md` — configuration section (`[describe]`), and the
  `bay describe` reference (auto-populated by default, how to disable,
  manual `bay describe` is authoritative).
- `internal/cli/agent-guide.md` — the description section: note that
  bay auto-drafts a description from the transcript when none is set,
  and that an agent keeping it current still wins.
- `README.md` / `docs/tutorial.md` — one line that descriptions
  populate automatically.

## Non-goals

- **Not real-time.** A ~5-minute backstop cadence; descriptions need to
  be current when you glance at `bay ls`, not instantly.
- **Reads Claude + Codex, not every agent.** Antigravity and others
  have no transcript reader yet; those bays use the git-only fallback
  until a reader is added.
- **Never clobbers** a human- or agent-set description.
- **Never inline** in the monitor loop, and never blocks `bay new` or
  any other command (consistent with keeping creation instant — there's
  no transcript at creation anyway, so the first summary lands on a
  later cycle once the agent has been used).
- **Not a work log.** A standing-brief goal, not a changelog of actions.
