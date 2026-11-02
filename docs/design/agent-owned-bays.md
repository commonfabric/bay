# Agent-owned bays - design plan

Captured 2026-07-24. No code has been written. This doc records the
design for two related additions: **detached bays** (a bay-owned
worktree with no tmux window until someone wants one) and **bay
ownership** (a claim that scopes who may close a bay).

The trigger is parallel PR work. An orchestrating agent fans out
subagents across several PRs, each needing its own worktree. When the
user wants to look at one of them, there should already be a bay there
— not a directory that has to be reconciled with bay after the fact.

## Problem

Two shapes were considered for how agent worktrees relate to bays:

1. **Eager** — the agent calls `bay new` for every parallel task, so
   every task is a bay from the start.
2. **Lazy** — the agent manages plain `git worktree add` directories,
   and bay wraps one only when the user asks to see it.

Eager gets discoverability, cleanup, prepare, and metadata for free but
costs a tmux window and a tab per speculative task. Lazy costs nothing
until the user cares but leaves every worktree outside bay's lifecycle.

The tab bar is the scarce resource, so neither is satisfying as stated.
Detached bays split the difference: bay owns the worktree and the
manifest entry from creation, but materializes tmux state only on
demand.

Ownership is a separate concern that fan-out surfaces: once agents
create bays, "who is allowed to close this" stops being obvious.

## Constraints from today's code

These pin most of the design and are worth restating.

- **Git allows one worktree per branch.** There is no way to give the
  user a viewing copy of an agent's branch. Any "let me look at it"
  path must resolve to the agent's exact directory.
- **Worktree dir names are `b<N>`, allocated by `nextBayDir`**
  (`internal/engine/bay.go:1816`), which skips names claimed in the
  manifest *and* names already present on disk. Foreign directories in
  the worktree dir are already tolerated; the only rule outside
  callers need is "don't use `^b[1-9]\d*$`".
- **Dir basename == bay ID** for worktree bays. `assignBayIDs`
  (`internal/manifest/manifest.go:355`) adopts the path basename as
  the ID when it is a valid bay ID.
- **`bay new --dir` is not an adoption tool.** It creates an
  *external* bay, and all branch/PR/merged tracking is gated on
  `bay.Worktree != nil` (`internal/engine/sync.go:288`, `:373`,
  `:425`, `:433`), which external bays never have. An external bay
  over a worktree reads as "no branch" and gets swept by
  `bay close --done` on the first run.
- **`bay new` always builds a tmux window plus a launched surface.**
  There is no headless path today.
- **Prepare and `.worktreeinclude` only run on the `bay new` path**
  (`internal/engine/bay.go:123-130`). A hand-rolled `git worktree add`
  produces a worktree missing repo-defined readiness work.
- **CWD under a dock's worktree dir already resolves to that dock**
  (`internal/engine/context.go:146`), and the worktree dir carries the
  bay-awareness `CLAUDE.md` every worktree inherits by ancestry
  (`internal/cli/doctor.go:201`). Keeping agent worktrees inside the
  worktree dir is worth real ergonomics.

## Goals

- An agent can create a worktree that is a first-class bay without
  spending a tmux tab.
- The user can go from "I want to see it" to seeing it in one command
  or keystroke, because it is already in `bay ls` and the picker.
- Bay retains ownership of the directory, so close safety gates,
  prepare, `.worktreeinclude`, and branch/PR/merged tracking all apply.
- A bay an agent is actively using cannot be closed out from under it
  by a human command *or* by bay's own background cleanup.
- Claims expire when the claimant does, without a timeout heuristic.

## Non-goals

- Bay does not gain a way to message or stop an owning agent. Taking a
  bay away from a stuck agent is a claim transfer, not an interrupt.
- Read-only or isolated peering. A user shell in an agent-owned bay
  shares the working tree with a live agent.
- Adopting worktrees created outside bay. See
  [Deferred: `bay adopt`](#deferred-bay-adopt).

## Model

### Detached bays

A **detached** bay is a worktree bay with no surfaces and no tmux
window. It has a path, a branch, a description, and full metadata; it
simply has not been materialized.

```
bay new --detached --description "Fix auth redirect loop (#412)"
```

State transitions:

The common case never materializes at all — the agent creates a bay,
works in the worktree via its own tooling, and closes it:

```text
    bay new --detached  -->  detached  -->  bay close  -->  gone
                                            (by owner)
```

Materializing is what happens when the user wants to look:

```text
                    bay go / bay shell
        detached  ----------------------->  materialized
            ^                                     |
            +-------------------------------------+
                  last surface closes
                  (live claim only)
```

That return arrow is conditional — it applies only when the claim is
live. A bay with no claim, which is every bay a human creates today,
keeps its current behavior when its last surface goes away. The exact
rule is specified in [Lifecycle rules](#materialize-and-de-materialize).

Detached bays appear in `bay ls`, `bay tree`, and the `M-g` picker with
a distinct marker. Detached is orthogonal to the existing `st=` values
— a detached bay can also be dirty or pending — so it needs its own
glyph rather than competing for the status column.

`--description` is **required** for `--detached`. The auto-description
backstop summarizes a pane transcript, and a detached bay has no pane,
so nothing will ever fill the field in. A detached bay without a
description is an unidentifiable row in the list, which defeats the
entire discoverability goal.

### Ownership

Ownership is an optional claim on a bay. The rule:

> **Closing requires ownership.** No claim means today's behavior —
> anyone closes. A claim means only the claimant closes.
> `bay claim <id>` makes you the claimant.

`bay new --detached` claims implicitly for the calling agent. A human
`bay new` claims nothing, so existing behavior is unchanged.

This generalizes rather than adding a special human escape hatch.
`bay claim` is both the user's way to take a bay away from a stuck
agent and an agent's way to hand a bay to another agent. `bay release`
is the giving-end counterpart, used by an agent that has finished but
wants to leave the worktree for the user rather than delete it.

The failure message carries the remedy:

```text
bay close b10: owned by b3:agent (use 'bay claim b10' to take it)
```

### Ownership token

```go
// Owner records a claim on a bay. Nil means unowned.
type Owner struct {
    Token     string `json:"token"`               // "pane:%47" — authoritative
    Label     string `json:"label,omitempty"`     // "b3:agent" — display only
    ClaimedAt int64  `json:"claimed_at,omitempty"`
}
```

The token is scheme-prefixed. Each scheme carries its own liveness
rule:

| Token | Liveness check | Claimant |
|---|---|---|
| `pane:%47` | `PaneExists`, gated on the session marker | agent in a bay surface (default) |
| `proc:<pid>` | `kill -0` | `codex exec` workers, monitor-dispatched jobs |
| `user:<name>` | never expires | a human who ran `bay claim` |
| *anything else* | never expires, display only | future / foreign claimants |

Unknown schemes degrade to "never auto-expires, show the label." A
claimant bay does not understand can still hold a bay, and the user can
still take it with `bay claim`. That is the correct failure mode:
conservative about destroying work, never permanently stuck.

**Why the tmux pane ID and not the creating bay's ID.** Bay IDs are
recycled — the agent guide states that a closed bay's slot may be
reused. An `owner: b3` claim would silently transfer to whatever next
occupies the `b3` slot, giving an unrelated agent a claim it never
made, with no symptom until someone cannot close something.
Tmux never reuses `%N` within a server generation.

**Why the pane ID and not an agent session UUID.** Liveness comes free:
sync already calls `PaneExists` on every surface every cycle. If the
owning pane is gone, the claim is stale — same pass, same data, no TTL
heuristic. A reboot kills every pane, so every claim goes stale on the
next sync, which dissolves the "ownership outlives the owner" problem
instead of papering over it with a timer. A session UUID would require
bay to learn the session-id mechanism of every supported agent and
would buy nothing.

**Why `Label` is stored rather than derived.** The moment you most want
to know who held a bay is when the owning pane is gone and it can no
longer be looked up. Storing `b3:agent` at claim time lets `bay ls`
render `owned by b3:agent (gone)` — which is exactly the row that tells
the user it is safe to take. Deriving would show an empty string in
that case.

`ClaimedAt` is for display (`owned by b3:agent, 4h ago`) and as a
tiebreaker for a stale-claim sweep. It is not the primary expiry
mechanism.

#### Interaction with session identity

`session-identity.md` notes that pane death is ambiguous: when the tmux
server restarts and a same-named session comes back, every recorded
pane ID dangles even though nothing legitimately died, which is why
sync deliberately skips surface cleanup in that case.

Claim staleness must ride the same gate, or a tmux restart strips
claims from agents that are fine. Until `@bay-session-id` lands, treat
"pane missing" as stale only when the session is verifiably still the
one bay created. This makes session identity a soft prerequisite.

## Manifest changes

Additive; empty zero-values are legacy-compatible. No schema bump.

```go
type Bay struct {
    // ...
    Owner *Owner `json:"owner,omitempty"`
}
```

One field. **Detached is not stored** — it is derived:

```text
detached := bay.Type == BayTypeWorktree &&
            len(bay.Surfaces) == 0 &&
            bay.PendingCloseAt == 0
```

An earlier draft carried a `Detached bool` to distinguish the
never-materialized state from the accidentally-emptied one. That
justification does not survive the rule above: once a live claim
returns a de-materialized bay to detached, those two states are
deliberately identical, and every branch keys off claim liveness rather
than history. `PendingCloseAt` already separates a detached bay from an
orphan awaiting close, so the stored bool would be redundant state that
can disagree with the surface list.

## Lifecycle rules

### Materialize and de-materialize

- `bay go`, `bay shell`, `bay edit`, and `bay surface new` on a
  detached bay create the tmux window. The bay is materialized by
  virtue of having a surface; no flag is flipped.
- Materializing an owned bay **defaults to a shell**. Spawning an agent
  surface would put two agents in one worktree editing the same files.
  `--agent` on an owned bay is an explicit override and should warn.

When a bay's last surface goes away:

| Claim state | Result |
|---|---|
| Live | returns to detached; leave `PendingCloseAt` at 0 |
| None | unchanged: 60s grace → `BayClose(force=false)` |
| Stale | treated as unclaimed; takes the unclaimed path |

The stale row matters: an expired claim stops protecting the bay from
cleanup, so a dead agent's clean, landed bay is collected normally
rather than accumulating as a locked orphan. Dirty or unlanded bays are
still refused by the existing gates, so nothing unsafe is collected.

The user-facing protections on the unclaimed path are unchanged — the
`Option+W` last-surface double-tap still fires, then the grace window.
For a live-claim bay that double-tap now guards a non-destructive
transition; leaving it uniform is simpler than making the same
keystroke behave differently based on invisible ownership state, but it
is a deliberate choice rather than a requirement.

The live-claim row is the highest-risk correctness requirement in this
design,
and it is a narrow carve-out rather than a change to the default.
Today, `internal/engine/sync.go:452-473` schedules
`PendingCloseAt = now + 60s` whenever a sync pass strips a bay's last
surface. That remains correct for every unclaimed bay. Without the
carve-out for claimed ones, this happens:

1. User runs `bay go b10` to look at a subagent's work.
2. User closes the pane with `Ctrl-B x` or `Option+W`, as with any pane.
3. Sync strips the surface; the bay is empty; a close is scheduled.
4. Sixty seconds later `BayClose(force=false)` runs. If the agent has
   pushed its branch, the dirty and unlanded gates
   (`internal/engine/bay.go:403-427`) both pass.
5. The worktree is removed while the agent is still working in it. No
   prompt, no error.

**The ownership check must live in the engine and the sync finalizer,
not only in the CLI.** An ownership gate implemented in `internal/cli`
is decorative; the destructive path here is the monitor.

### Close gates

Ownership is checked in `closeBayState`, before the dirty and unlanded
checks, and applies to every caller:

| Path | Behavior on an owned bay |
|---|---|
| `bay close <id>` | refuse, name the owner, suggest `bay claim` |
| `bay close --done` / `--clean` / `--all` | skip and report |
| sync orphan finalizer | never fires while the claim is live; bay returns to detached |
| `bay sf close` last-surface cascade | returns to detached while the claim is live |
| `bay close --force` | proceeds |

`bay tidy` and `bay clean-review` need the same gate. `tidy` detaches
the worktree to `origin/<default>` and deletes the local branch,
destroying an agent's in-progress work as thoroughly as a close — and
it is the command muscle memory reaches for after a merge.

### Batch closes

`bayCloseBatch` already returns a `skipped` list and accepts a
`baySkipFunc` (`internal/engine/bay.go:817-836`), so the plumbing
exists. Two requirements:

- Skips must be **reported**, not silent. `bay close --all` reporting
  success while leaving three agent-owned bays behind is a result the
  user will act on incorrectly.
- `BayCloseAllWouldDismissDock` (`internal/engine/bay.go:848`) must not
  dismiss the dock session when owned bays survive `--all`, or the tmux
  session dies under running agents.

### Force-close

`git worktree remove --force` on a live worktree yanks the directory
out from under a running process; the agent's next tool call gets
ENOENT mid-task and it will emit confused output rather than fail
cleanly. Nothing to fix, but the confirmation should name the owner and
last-activity time so the user knows what they are interrupting.

## Monitor changes

**Merge detection must not skip detached bays.** `detectMerges` gates
on `bay.LastActive` within `activityWindow`
(`internal/monitor/monitor.go:533`, `:70` — two hours), and
`LastActive` is bumped by bay *commands*, never by agent activity. A
detached bay is stamped once at creation and goes cold after two hours.
It would never be marked merged, so `bay close --done` would skip it
forever even after its claim is released, and detached bays would
accumulate permanently.

Carve-out: detached bays are always merge-eligible. They are few, and
the fetch is already deduplicated per repo path.

**Auto-descriptions will not fire** for detached bays — the same
activity gate at `internal/monitor/monitor.go:380`, plus there is no
pane and therefore no transcript to summarize. This is the reason
`--description` is required at creation rather than optional.

**`bay recover`** iterates surfaces (`internal/engine/recover.go:211`),
so a detached bay is already a no-op. Make that intentional rather than
incidental: recover must not materialize detached bays. Note that after
a reboot every claim is stale by construction, which is the correct
outcome.

## Concurrency

Fan-out is the target workload, so the creation path needs to be safe
under it. Today it is not: `nextBayDir` is called at
`internal/engine/bay.go:101`, outside the manifest lock, while `AddBay`
runs roughly a hundred lines later. Two agents calling
`bay new --detached` concurrently can both select `b10`; the loser's
`git worktree add` fails.

One-bay-at-a-time usage hides this completely. Fix by allocating the
directory name inside `withManifest`, or by making directory creation
itself the claim.

Note that `git worktree add` against a single checkout serializes on
git's index lock regardless, so concurrent creation is not actually
parallel — only correct.

## CLI surface

```text
bay new --detached --description TEXT   create without a tmux window
bay claim [id]                          take ownership
bay release [id]                        drop ownership, keep the bay
```

`bay show --json` and `bay ls --json` gain `detached` and `owner`
fields. Per `CLAUDE.md`, implementation must update
`docs/human-guide.md` (command reference), `internal/cli/agent-guide.md`
(commands plus JSON schemas), and the concepts sections of both.

The agent guide additionally needs a workflow section: the
orchestrator's fan-out pattern, the requirement to set a description at
creation, and the expectation that the creating agent closes what it
created.

## Open questions

- Should `bay go` on a detached bay materialize it, or should
  navigation be non-mutating and require an explicit
  `bay shell --bay b10`? Materializing on `go` is fewer keystrokes but
  means the picker has a side effect.
- Should a detached bay hold a tmux window slot in manifest order, so
  materializing lands the tab in a stable position rather than at the
  end?
- Does `bay release` on a bay whose owner is already stale need to
  differ from `bay claim` followed by nothing?

## Deferred: `bay adopt`

Wrapping a worktree created outside bay (for example the stale
`/private/tmp/bay-main-review` entry that shows up in `git worktree
list`) is a real but separate need. Sketch, not designed:

- Must adopt as `BayTypeWorktree`, never external, or branch/PR/merged
  tracking is lost and `--done` sweeps it.
- Must run prepare and `.worktreeinclude` retroactively.
- Breaks the dir-basename-equals-ID invariant: a directory named
  `pr-auth-fix` is not a valid bay ID, so `assignBayIDs` assigns
  `b<max+1>` and the tab label stops matching the editor root — the
  exact correspondence `worktree-dir-visibility.md` exists to preserve.
  Either rename the directory on adopt or accept the divergence.

Detached bays make adoption unnecessary for the fan-out case, which is
why it is deferred rather than designed here.

## Relationship to other designs

- `orphan-hygiene.md` — this design adds the first exception to the
  grace-window cascade. Owned bays return to detached instead of being
  scheduled for close.
- `session-identity.md` — soft prerequisite. Claim expiry needs the
  session marker to distinguish "agent exited" from "tmux restarted."
- `worktree-dir-visibility.md` — unaffected by detached bays (they are
  bay-created, so dir basename still equals the ID), but constrains any
  future `bay adopt`.
- `auto-descriptions.md` — detached bays are outside its reach, which
  is why descriptions are mandatory at creation.
- `prepare.md` — detached creation still runs prepare; a detached bay
  may be in a not-yet-ready state when the user materializes it.
