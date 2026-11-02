# Bay prepare — implementation plan

Captured 2026-05-04. Companion to `prepare.md`. Sequences the v1
build into landable phases, points at the files each phase touches,
and flags decisions that need to settle before coding starts.

The design doc is the source of truth for *what* and *why*. This doc
covers *how* and *in what order*.

## Locked decisions

These were settled in design-review and reflected in `prepare.md`:

1. **Trust hierarchy: 2 levels (bay-level default, dock-level
   override).** dock-checkout-merge (#281) made `Repo` 1:1 with
   `Dock`, so the repo-level tier collapsed into dock.
   `TrustPromptDismissed` lives on `Dock`.
2. **Worker entrypoint: hidden top-level subcommand
   `bay prepare-worker`** (`Hidden: true`, no `bay internal`
   namespace). Single-binary re-exec via `os.Executable()`
   structurally rules out version skew.
3. **Worker granularity: one controller per bay**, iterating
   steps serially. One pid per in-flight prepare. Per-step
   flock granularity preserved so future parallelism isn't
   foreclosed.
4. **Logs: per-dock per-day file**, layout
   `<bay-data>/logs/<dock>/YYYY-MM-DD/prepare.log`. Run
   separators inside the file include bay ID and step name.
   Bay IDs are reused, so the file is the unit, not bay-named
   sub-paths. Layout generalizes to `close-check.log` etc. for
   future lifecycle features.
5. **Log retention: 14-day directory-level prune by monitor**,
   throttled to once per 23h. `bay dock close` rm -rf's
   `logs/<dock>/`. `bay close <bay>` does not touch logs.

## Phasing

Each phase is one or two PRs. Each leaves the tree shippable
(`presubmit.sh` green, no half-wired user-visible behavior). The
full bay-prepare surface is too large for a single PR; phasing
splits review burden without violating the design's "ship together"
rule — no phase exposes a partial feature to users.

### Phase A — Schema + config plumbing

**Lands:** types, parsing, validation, manifest migration. No
runtime behavior.

- `internal/manifest/manifest.go`: bump `CurrentVersion` to 7.
  Add `migrateV6ToV7` (additive only). Add `manifest.PrepareStep`
  struct (name, status, started_at, finished_at, heartbeat_at,
  pid, definition_hash, run_log_offset). Add
  `Bay.Prepare []PrepareStep` and `Bay.PendingSurfaces
  []PendingLaunch` (used in Phase E; reserved in v7 schema).
  Add `Dock.TrustPromptDismissed bool`.
- `internal/config/config.go`: add `BayPrepareConfig` (name,
  command, ready_command, blocks, run, timeout). Add
  `TrustRepoBayToml *bool` on `Config` and `DockConfig` (pointer
  to distinguish unset from explicit false). Extend `DockConfig`
  with `BayPrepare []BayPrepareConfig`.
- New: `internal/config/repolocal.go`. Functions for loading and
  parsing `.bay.toml` from a checkout root. Returns nil silently
  if absent. Same `BayPrepare` shape as dock-level.
- New: `internal/config/prepare_merge.go`. `Merge(repoLocal,
  dockLevel) []BayPrepareConfig` per the design's per-field
  override rules. Same `name` across sources merges; per-field
  override; same name within one source is a validation error.
- New: `Validate(merged)` enforcing every rule in the design's
  Configuration validation section.
- New: `ResolveTrust(cfg, dockName) bool` walking the (now
  2-level) hierarchy.
- Add a flock-based manifest write lock now, even though no
  out-of-process writers exist yet — Phase C will need it.
  `internal/manifest/lock.go` with `WithLock(path, fn)` wrapping
  every read-modify-write cycle. Update `engine.SaveManifest`
  callers.

**Bar:** unit tests for migration round-trip, config parsing
(repo-local and dock-level), merge precedence, every validation
rejection case in the design, every trust-resolution combination.

**Exit:** types compile and parse. `bay new` is unchanged because
nobody dispatches the worker yet.

### Phase B — Surgical TOML writer

**Lands:** infrastructure for writing user config without
clobbering comments.

- New package `internal/configedit`. Two functions:
  - `AppendBlock(path, header, lines) error` — append
    `[<header>]` plus the given lines to end of file. Used by
    `bay dock new` (Phase G) and as a fallback by `SetField`.
  - `SetField(path, header, key, value) error` — find the
    `[<header>]` section, set or insert one field line, leave
    everything else byte-identical. If the section is missing,
    delegate to `AppendBlock`. Used by `bay setup` (Phase G).
- No `toml.Marshal`. A small purpose-built scanner that
  recognizes `[section]` headers, comments, blank lines, and
  field assignments is enough.

**Bar:** round-trip tests preserving comments, blank lines,
ordering, quoted strings exactly. Tests for append-on-empty,
file-without-trailing-newline, set-existing-field,
set-missing-field-in-existing-block, set-into-missing-block.

**Exit:** library is ready. No callers yet.

### Phase C — Worker process

**Lands:** the prepare-worker subcommand and the lifecycle around
it. Not yet wired into `bay new`.

- New: `internal/cli/prepare_worker.go` adds top-level
  `bay prepare-worker --dock <name> --bay <id>` with `Hidden:
  true`. Controller iterates steps internally.
- New: `internal/prepare/worker.go` — the controller logic. For
  each step in declared order: acquire per-step flock at
  `<bay-data>/locks/prepare/<dock>/<bay>/<step>.lock`; write
  pid + started_at + heartbeat_at to the step's manifest entry
  under the manifest write-lock; spawn a 5-second heartbeat
  goroutine; open today's `<bay-data>/logs/<dock>/YYYY-MM-DD/prepare.log`
  in append mode; write the run-start separator; record byte
  offset as `run_log_offset` in manifest; stream stdout/stderr;
  on command exit, write run-finished separator, atomically
  update terminal status + finished_at + definition_hash, clear
  `run_log_offset`. Stop on first failure; later steps are not
  run.
- New: `engine.DispatchPrepareWorker(dockName, bayID)` — re-execs
  `os.Executable() prepare-worker ...` detached (`Setpgid: true`,
  waited on in a goroutine so a long-lived parent reaps it). Returns
  immediately. Same-binary re-exec rules out
  version skew.
- Worker, on success of all steps, exits cleanly. Phase E will
  add the queued-surface dispatch on success.

**Bar:** unit tests with `/bin/true` and `/bin/false` as the
command. Lock-contention test: a second worker started while the
first holds the lock exits cleanly without modifying the
manifest. One build-tagged integration test exercising real
`flock` across two processes.

**Exit:** the worker can run end-to-end in isolation. No CLI path
dispatches it yet.

### Phase D — bay new dispatch + bay prepare command

**Lands:** `bay new` starts the worker. Users can inspect status
and re-run.

- `internal/engine/bay.go`: after `BayNew` records the new bay,
  if effective `bay_prepare` config is non-empty and trust is
  granted, call `DispatchPrepareWorker`.
- `internal/cli/bay.go`: TTY-only dispatch summary line — `prepare
  started: <names>; follow with 'bay prepare --log', stop with
  'bay prepare --kill'`. Suppress when `!isatty(stdout)`.
- New: `internal/cli/prepare.go` with `bay prepare [bay-id]`:
  - bare: status table for the (optionally specified) bay.
  - `--retry`: marks failed/stale steps pending; redispatches.
  - `--wait`: polls until terminal status; respects `--timeout`.
  - `--log`: prints today's `prepare.log` filtered by `bay=<id>`
    via run separators. With `-f` and an in-flight worker, seeks
    to the manifest's `run_log_offset` and tails until prepare
    exits.
  - `--kill`: reads pid, sends SIGTERM, waits up to 5s for lock
    release. Marks step `stale` if worker doesn't write a
    terminal status.
- `internal/monitor`: add the daily log-prune pass. Walks
  `<bay-data>/logs/*/`, parses `YYYY-MM-DD` from each subdir
  name, `rm -rf`'s any directory >14 days old. Throttled to once
  per 23h via a `<bay-data>/logs/.last-prune` sentinel file
  (mtime check).
- `internal/engine/dock.go` (`DockClose`): add
  `os.RemoveAll(logs/<dock>/)` to dock teardown.
- `bay ls`/picker formatting (`internal/cli/format.go`,
  `ls.go`): show compact `setup=running:vendors` or
  `setup=failed:vendors` when applicable.

**Bar:** unit + integration tests covering: dispatch on a dock
with config, no-op on a dock without; status read across all
five status states; `--retry` happy path; `--kill` happy path;
status appears in `bay ls` output.

**Exit:** prepare runs end to end. Surfaces are not yet blocked,
so an agent launched into a half-prepared bay still starts
(intended state until Phase E).

### Phase E — Blocked surfaces + placeholder pane

**Lands:** agent and cmd surfaces respect prepare state.

- Surface launch (`internal/engine/surface_*.go` — confirm exact
  files at impl time): when launching an `agent` or `cmd`, look
  up the bay's effective `bay_prepare`. If any step lists this
  surface class in `blocks` and isn't `ready`, create a
  placeholder pane instead.
- Placeholder pane: a tmux pane that runs
  `bay prepare --log -f` against the relevant log. The pane is
  associated with the pending surface request via a marker on
  the bay's manifest entry: `Bay.PendingSurfaces []PendingLaunch`
  (kind, agent name, args).
- Worker, on transition to `ready` for a step that blocks
  surfaces, scans `Bay.PendingSurfaces` and dispatches any whose
  `kind` matches. The worker is responsible for swapping the
  placeholder pane for the real surface — done via tmux
  scripting from inside the worker process.
- On `failed`, placeholder stays put with a failure summary line
  and retry hint. Re-issuing the same surface request while a
  placeholder is in place is a no-op (already queued).
- Stale check at launch: if the matching step is `ready` and has
  a `ready_command`, run it. Non-zero → mark `stale`, dispatch
  one rerun under `run = "auto"`. Two consecutive failures stop
  the loop and leave the step `failed`.

**Bar:** integration tests covering: blocked agent → placeholder
pane present, no agent surface; ready transition swaps
placeholder for agent; failure transition keeps placeholder with
retry text; ready_command rerun bounded at one retry.

**Exit:** the design's blocked-surfaces guarantee is honored.

### Phase F — Close-time worker termination

**Lands:** `bay close` cleanly stops in-flight prepare workers.

- `internal/engine/bay.go` (`BayClose`): before today's
  dirty/unpushed checks, scan the bay's prepare entries for a
  live worker (status `running` with held lock or fresh
  heartbeat). If one is alive, send SIGTERM, wait up to 5s for
  the worker to exit. On timeout without `--force`: refuse
  close, print pid + log path + suggestion. With `--force`:
  SIGKILL, wait briefly, mark step `stale`, continue.

**Bar:** tests covering: graceful TERM during running prepare;
wedged worker without `--force` blocks close; wedged worker with
`--force` SIGKILLs and proceeds.

**Exit:** close races resolved. Feature is functionally
complete.

### Phase G — Trust UX (setup + creation flags)

**Lands:** the discoverable path for trust.

- `internal/cli/setup.go`: add a step that walks docks. For each
  dock that **(a)** has a `.bay.toml` in its checkout, **(b)** has
  no `trust_repo_bay_toml` in user config, and **(c)** has no
  `Dock.TrustPromptDismissed` flag, prompt `Trust this checkout's
  .bay.toml? [y/N/skip]`. y/N writes via `configedit.SetField`
  (Phase B); skip sets `TrustPromptDismissed = true`.
- New flag `bay setup --reset-trust-prompts`: clear all
  `TrustPromptDismissed` flags, leave config untouched.
- `internal/cli/dock.go` (`bay dock new`): add
  `--trust-repo-bay-toml` / `--no-trust-repo-bay-toml`. When
  set, emit the field into the new dock block (via
  `configedit.AppendBlock` extended to take initial fields). When
  unset, omit the field; dock inherits.
- `internal/cli/bay.go` (`bay new`): same flag pair. Applies
  only when the command auto-creates a dock; if a dock already
  exists for the resolved repo, print a TTY warning and ignore.

**Bar:** setup walks the right docks (skips ones without
`.bay.toml`, skips dismissed); each answer writes correct state;
`--reset-trust-prompts` re-surfaces prompts; flag on `dock new`
emits correct toml; flag on `bay new` only applies on
auto-create.

**Exit:** v1 is feature-complete.

### Phase H — Docs + integration testing

**Lands:** user-facing documentation, end-to-end tests, polish.

- `docs/human-guide.md`: command reference for `bay prepare`,
  configuration section for `[[bay_prepare]]` and
  `trust_repo_bay_toml`, troubleshooting for failed prepare and
  blocked surfaces.
- `internal/cli/agent-guide.md`: bay prepare commands and JSON
  output schema for status reads.
- `docs/tutorial.md`: only if the example flow is meaningfully
  different.
- One end-to-end integration test against a temp dock with a
  real `.bay.toml` running a 1-step prepare. Build-tagged like
  the existing tmux integration tests.
- Diff the design doc against shipped behavior; update only
  contradictions per project rules (don't churn for prose
  cleanup).

**Exit:** ready to merge to main, docs current.

## Cross-cutting

### Manifest write contention

Once the worker exists, two processes (CLI + worker) write the
manifest. Phase A introduces `manifest.WithLock` proactively so
Phase C can land on a stable substrate. The lock is a single file
flock; uncontended cases pay one syscall.

### TOML write safety

`bay setup` and `bay dock new` write user config for the first
time in bay's history. Comments + ordering + blanks must survive.
Phase B is the riskiest novel piece; nail it before later phases
build on it.

### Feature flag

Not used. The feature is opt-in via config. A bay with no
`bay_prepare` runs unchanged.

### Worker process portability

`Setpgid` + a background `Wait` works on Linux and macOS. Bay
isn't shipped for Windows.

## Open items

1. `bay prepare` positional grammar: `bay prepare [bay-id]`
   matching `bay show w1` / `bay close w1`. Confirm at Phase D.
2. Phase E pending-surface persistence shape (slice on
   `Bay.PendingSurfaces`) — confirm during Phase A schema work
   so it lands in v7.
3. Whether `bay prepare --log` defaults to today's full file or
   filters by current bay. Both are reasonable; pick at Phase D.

## Risk register

- **Manifest schema acceptance.** Verify current `Parse` behavior
  on unknown fields before bumping. v7 is additive but old bay
  reading new manifest must round-trip safely. *Quick code read
  in Phase A.*
- **Comment-preserving TOML.** Phase B is the one place a bug
  loses user data (their config). Belt-and-braces tests.
- **Tmux pane swap.** Placeholder → real surface in Phase E
  needs care to avoid race where the user closes the placeholder
  pane manually before the worker finishes. Handle "placeholder
  vanished" as "user cancelled, drop the pending surface."
- **Trust hierarchy churn.** If the design doc isn't updated for
  decision 1 before code lands, the implementation and design
  diverge. Don't skip the design edit.
