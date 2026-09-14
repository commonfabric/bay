# Session identity marker - design plan

Captured 2026-04-30. No code has been written. This doc proposes
tagging each bay-managed tmux session with an identity bay can verify,
so the monitor's surface cleanup can distinguish "my session" from
"a fresh session with the same name." It is meant as a review artifact
before deciding whether to land any of these changes.

## Problem

When the tmux server restarts (crash, OS restart, manual kill+restart)
and a session with a bay dock's name comes back up — fresh, empty,
possibly created by the user attaching, by a wrapper script, or by
bay's own `ensureSession` from `bay recover` — every pane ID recorded
in the manifest dangles. They were issued by the dead tmux server.

The monitor's `SyncAll` checks for this with `HasSession(dock.Name)`.
The check is name-level: a session with that name exists, so the gate
opens. Each surface's `PaneExists(s.Tmux.PaneID)` then misses.
`deadSurfaceIDs` returns every surface in the workspace. Apply strips
them and schedules `PendingCloseAt = now + 60`. Sixty seconds later
`WsClose` removes the workspace from the manifest entirely.

Result: a tmux crash plus any path that creates an empty session of
the same name before the next monitor cycle finishes irreversibly
destroys workspace state. `bay recover` afterwards has nothing to
recover.

The "session fully gone" branch is correctly handled —
`HasSession=false`, gate stays closed, surfaces preserved
(`internal/engine/sync.go:243-251`). Only the "session resurrected by
another party" case is broken.

## Proposal

When bay creates a tmux session, set a session-scoped tmux user option
`@bay-session-id` to a UUID. Persist the same UUID in the manifest as
`Dock.SessionID`. On sync, read the option back and compare. Mismatch
(or marker missing while manifest has one, or marker present while
manifest is empty) means the session is foreign or in-flight — skip
surface cleanup the same way a dead session does today.

The marker is the source of truth for "is this still my session."
Pane IDs are the diagnostic for "is this surface still alive within my
session." Today's code conflates them.

## Manifest change

```go
type Dock struct {
    Name      string
    SessionID string `json:"session_id,omitempty"`  // new
    ...
}
```

Empty-string is legacy / unknown. Old manifests load with
`SessionID == ""` everywhere; behavior is backward-compatible. No
schema bump — additive field with empty zero-value semantics.

## Tmux interface

Two new methods on `tmux.Interface` and the mock:

```go
SetSessionOption(session, key, value string) error
GetSessionOption(session, key string) (string, error)  // "" if unset
```

Wrap `tmux set-option -t <session>` and
`tmux show-options -t <session> -v <key>`. The existing window-option
helpers (`SetWindowOption`, `UnsetWindowOption`) are the template.

The option name is `@bay-session-id`, matching the `@bay-` namespace
used elsewhere (`@bay-waiting`, `@bay-dock-editor`).

## The gate

A single helper resolves the cleanup decision:

```go
// verifySessionOwnership reports whether the session named dock.Name
// is the one bay last set up, by comparing its @bay-session-id marker
// against dock.SessionID.
func (e *Engine) verifySessionOwnership(dock *manifest.Dock) (exists, owned bool)
```

Decision matrix:

| Tmux marker | Manifest `SessionID` | Owned? | Why |
|---|---|---|---|
| empty | empty | true | legacy / pre-rollout |
| empty | set | false | session was recreated outside bay |
| set | empty | false | tmux tagged but manifest hasn't caught up — recover in flight, or partial failure |
| set, matches | set | true | normal |
| set, mismatched | set | false | session was recreated; new identity differs |

The two `HasSession` gate sites (`probeWorkspaceSync` at sync.go:248
and `SyncAll`'s dock-surface loop at sync.go:117) swap to
`verifySessionOwnership`. If `!exists || !owned`, skip cleanup. The
existing comment at sync.go:243-247 widens to include "or the session
has been recreated, or recover is in flight."

## ensureSession contract

`ensureSession`'s job is tmux-side only: ensure a session exists with
the right name and a known marker. It does **not** persist the
manifest. The caller persists `Dock.SessionID` at the right moment
(see "Race-safe ordering" below).

Behavior:

| State found | Action | UUID returned |
|---|---|---|
| no session | create session, tag with new UUID | new |
| session present, marker empty | tag with new UUID (claim) | new |
| session present, marker matches dock.SessionID | no-op | existing |
| session present, marker mismatched | re-tag with new UUID (claim) | new |
| session present, marker present, dock.SessionID empty | adopt existing marker | existing |

`ensureSession` returns the UUID it set or found. The caller writes it
to the manifest when surface state is in a consistent place to be
checked.

The "claim" entries are deliberate: `bay recover` is the user's "fix
my tmux state" tool. The matrix governs `ensureSession`'s tmux-side
behavior in isolation — callers can layer additional checks on top
(see `DockNew` below).

Callers of `ensureSession` and what they pass / persist:

| Caller | Expected `SessionID` in | When persisted |
|---|---|---|
| `DockNew` (`internal/engine/dock.go:79`) | empty (fresh dock) | in the initial `AddDock` manifest write |
| `Recover` (`internal/engine/recover.go:40`) | the dock's existing `SessionID` | in `mergeRecoveredDockState`, alongside new pane IDs |
| `DockRecover` (`internal/engine/recover.go:80`) | same as `Recover` | same |
| `WsNew` (`internal/engine/workspace.go:158`) | the dock's existing `SessionID` | normally a no-op (marker matches); on rare backfill, in the same `withManifest` block that records the new workspace |

In normal operation `WsNew`'s call resolves to the matching-marker
no-op. The plumbing is there for completeness so that a workspace
created against a dock whose marker has somehow drifted picks up the
correction without a separate code path.

### `DockNew`'s existing pre-flight check

`DockNew` at `internal/engine/dock.go:71-77` errors if a tmux session
with the new dock's name already exists. This check stays. Reasons:

- A user running `bay dock new labs` against a foreign tmux session
  named `labs` (an editor's tmux setup, a sibling tool) shouldn't
  silently have it taken over.
- Recover is the right place for "take over an existing session";
  `bay dock new` is for fresh docks.
- The marker doesn't help here — a brand-new dock can't have a
  matching marker on an existing session.

If a future workflow needs "create a new dock against an existing
session," it's a new explicit command (`bay dock adopt` or similar),
not an implicit relaxation of `dock new`.

## Race-safe ordering

The race that motivates the ordering: `ensureSession` runs at the top
of `Recover` (`internal/engine/recover.go:40`), but per-workspace pane
recreation happens after, with new pane IDs only persisted in the
final `mergeRecoveredDockState` (recover.go:48-52). If `ensureSession`
*also* persisted `SessionID` upfront, there'd be a multi-second window
where:

- tmux marker = new UUID
- manifest `SessionID` = new UUID (matches → owned)
- manifest pane IDs = pre-crash, dangling

A monitor probe in that window would see `owned=true`, find every
pane dead, strip, and orphan-close. The fix we just landed would arm
the gun and shoot.

The persistence sites are listed in the caller table above. The
race-critical case is `Recover` / `DockRecover`: `SessionID` lands in
`mergeRecoveredDockState` *together with* the new pane IDs, in the
same `withManifest` transaction. The other callers either have no
surfaces in flight (`DockNew`) or normally hit the no-op branch
(`WsNew`).

For the recover window, probes see `(marker=newUUID, manifest=oldUUID)`
→ mismatch → preserve. Cleanup unfreezes only when both writes have
landed atomically (the manifest write puts the new SessionID and new
pane IDs in the same `withManifest` transaction).

Partial-failure behavior:

- `ensureSession` tags tmux, then crashes / errors before manifest
  update: future probes see `(set, empty)` → preserve (transient
  in-flight state). Next `ensureSession` adopts the existing marker.
- Manifest update fails after tmux is tagged: same as above.
- Tmux tagging fails: nothing changed, retry on next call.

The gate is conservative: any inconsistency between tmux and manifest
means "don't touch surfaces."

## Lifecycle

| Event | Behavior |
|---|---|
| `bay dock new` | `ensureSession` creates+tags; manifest write records `SessionID` + dock fields atomically |
| `bay recover` | `ensureSession` ensures tmux is tagged (returns UUID); recover rebuilds windows/panes in-memory; final `mergeRecoveredDockState` persists `SessionID` + new pane IDs together |
| Sync probe | `verifySessionOwnership` decides; gate cleanup |
| Tmux server restart | next probe before recover sees mismatch (or empty marker against stored UUID) and preserves surfaces; recover later does the rebuild and atomic update |

## Migration

Additive only. Existing manifests load with empty `SessionID`. Live
tmux sessions for existing docks have no marker. First probe sees
`(empty, empty)` → owned=true, no behavior change.

The marker populates on the next `ensureSession` for that dock —
typically the next `bay recover`, but also any code path that creates
or re-establishes the session. Pre-existing docks running normally
without a recover stay in `(empty, empty)` indefinitely; that's fine,
since the gate degrades to today's behavior in that state.

No proactive lazy backfill on sync — keeps the probe a pure read and
keeps the rule "manifest writes happen at known points" intact.

## Implementation notes

- **UUID source:** `crypto/rand` + hex encoding. ~10-line helper in
  the engine package; no new dependency needed.
- **Test injectability:** the UUID generator is a package-level `var
  newSessionID = func() string { ... }` so tests can swap in
  deterministic IDs. Same pattern as `orphanGraceSeconds` at
  sync.go:23.
- **`DockClose`:** no marker cleanup needed. The session-scoped tmux
  option dies when the session is killed; the manifest entry goes
  away with the dock record.

## Edge cases

- **Two bay processes / manifests collide on a session name.**
  Practically impossible (one user, one manifest). The mismatched-
  marker path handles it without harm.
- **User manually clears `@bay-session-id`.** Marker empty, manifest
  has UUID → `owned=false`, cleanup suppressed. Running `bay recover`
  re-tags. Acceptable: someone going out of their way to clear bay's
  marker is opting out. (Since then the marker has gained a second
  consumer: bay's tmux keybindings are conditioned on it, so an
  untagged session also runs none of bay's command keys. `bay doctor`
  reports a dock in that state.)
- **Marker surviving across server restarts.** It can't — session-
  scoped tmux options die with the session. That's exactly the
  property we want.
- **Concurrent `bay recover` and old monitor.** Old monitor sees
  `(newUUID, oldUUID)` → preserve. Safe.

## Testing

- Mock tmux: a `map[session]map[key]string` for session options.
- `TestSyncAll_PreservesSurfacesAfterServerRestart` — manifest has
  surfaces and `SessionID`; mock drops session and marker; probe →
  surfaces preserved.
- `TestSyncAll_PreservesSurfacesWhenSessionRecreatedWithoutMarker` —
  session exists, marker absent, manifest has UUID → preserved.
- `TestSyncAll_PreservesSurfacesWhenMarkerSetButManifestEmpty` —
  in-flight recover state → preserved.
- `TestSyncAll_PreservesSurfacesOnMismatchedMarker` — different
  UUIDs → preserved.
- `TestSyncAll_ProceedsWithMatchingMarker` — matching → cleanup as
  today.
- `TestSyncAll_ProceedsWithLegacyEmptyState` — `(empty, empty)` →
  owned, normal cleanup (backward-compat).
- `TestEnsureSession_TagsNewSession` — `bay dock new` ends with
  marker set and manifest `SessionID` populated.
- `TestEnsureSession_AdoptsExistingMarker` — session has marker,
  manifest empty → ensureSession returns existing UUID, caller
  persists.
- `TestEnsureSession_ReclaimsMismatchedMarker` — `bay recover`
  re-tags with new UUID; manifest update happens later in
  `mergeRecoveredDockState`.
- `TestRecover_NoMidFlightCleanup` — simulate a probe firing in the
  gap between `ensureSession` and `mergeRecoveredDockState`; surfaces
  preserved.

## Incremental path

### Step 1: Manifest field [SMALL]
Add `Dock.SessionID`. No migration.

### Step 2: Tmux interface methods [SMALL]
`SetSessionOption`, `GetSessionOption` on the interface, real impl,
mock impl.

### Step 3: ensureSession sets the marker [SMALL]
`ensureSession` generates UUIDs for new/foreign sessions, returns the
UUID. Does not persist manifest.

### Step 4: Caller persists at the right moment [SMALL]
- `DockNew`: include `SessionID` in the dock-record write.
- `Recover` / `DockRecover`: include `SessionID` in
  `mergeRecoveredDockState`.

### Step 5: Sync gate [SMALL]
Add `verifySessionOwnership`. Replace the two `HasSession` gate sites.

### Step 6: Tests [SMALL]
Cases above.

### Step 7: Docs [SMALL]
Brief mention in `docs/human-guide.md` (recovery section): bay can
distinguish a returned tmux server from its own; in practice this is
silent and means `bay recover` is the right tool after a crash. No
new flags or commands. Possibly a line in `internal/cli/agent-guide.md`
if recovery is mentioned there.
