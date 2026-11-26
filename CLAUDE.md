# Bay development

Bay is a Go CLI tool for managing concurrent bays with git worktrees and tmux.

Run `bay agent-guide` for bay CLI commands available in this bay.

## Installing during development

Bay manages its own bays, so changes to the CLI need to be installed to test interactively:

```
go install ./cmd/bay    # install your working copy
```

After merging, reinstall from main so you're not running stale code:

```
git checkout main && go install ./cmd/bay
```

## After implementation

After the initial implementation, review your changes: read the diff and check for code reuse (search for existing helpers before writing new ones), redundant state, copy-paste, and unnecessary work on hot paths. Fix any issues found.

## Before pushing

Run `scripts/presubmit.sh` before every `git push`. It mirrors CI: build, gofmt, vet, staticcheck, and tests with `-race`. Fix any failures before pushing.

## Testing

```
go test ./...           # run all tests
go test ./... -race     # with race detector (CI uses this)
go test ./internal/engine/ -run TestName  # single test
```

### Tmux integration tests

A small suite under `internal/tmux/integration_test.go` exercises real
tmux to pin the mock's behavior to ground truth. They're behind a build
tag so they don't run by default.

```
go test -tags=integration ./internal/tmux/ ./internal/detach/ -run TestIntegration_
```

Requires tmux on PATH. The tests start a private tmux server on a unique
socket — they don't touch your running tmux. CI runs them automatically
in the `integration` job.

## Code style

- `gofmt` is enforced — CI rejects unformatted code
- `go vet` and `staticcheck` must pass
- No new lint suppressions without justification

## Documentation

Every user-facing change must update the relevant docs. The project has
several documentation files that cover overlapping surfaces — a single
change often touches more than one.

| File | Audience | What it covers |
|------|----------|----------------|
| `README.md` | New users, GitHub visitors | Feature summary, install, quick examples |
| `docs/tutorial.md` | First-time users | Hands-on walkthrough of the full lifecycle |
| `docs/human-guide.md` | Daily users | Complete command reference, config, keybindings, troubleshooting |
| `internal/cli/agent-guide.md` | AI agents | Agent-oriented command reference, JSON schemas, workflows |
| `docs/design/*.md` | Contributors | Design plans and architecture decisions |

### Rules

1. **New or changed commands** — update `docs/human-guide.md` (command
   reference section) and `internal/cli/agent-guide.md` (commands section).
   If the command appears in the tutorial or README examples, update those too.

2. **New or changed config options** — update `docs/human-guide.md`
   (configuration section).

3. **New or changed keybindings** — update `docs/human-guide.md`
   (keybindings section).

4. **Changed JSON output** — update `internal/cli/agent-guide.md`
   (the JSON schema examples).

5. **New concepts or terminology** — update both `docs/human-guide.md`
   (concepts section) and `internal/cli/agent-guide.md` (key concepts).

6. **Changed CLI flags or defaults** — grep the docs for the old flag/default
   name and update every occurrence.

7. **Don't update design docs** unless the change contradicts a stated design
   decision. Design docs capture intent at a point in time.
