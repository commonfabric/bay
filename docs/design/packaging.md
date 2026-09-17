# Packaging & distribution — design plan

Captured 2026-04-26. No code has been written for any of this. The
design emerged from wanting teammates to install bay without having to
`go install` from source on every dev box.

Today, the README's install section reads:

```
git clone git@github.com:commonfabric/bay.git
cd bay
go install ./cmd/bay
```

That requires Go on every machine, doesn't pin versions, and gives no
signal when the binary on disk is stale. We want versioned artifacts
that drop a binary into place without a build toolchain.

## Goals & non-goals

**Goals**
- Versioned binary artifacts the team can install without Go.
- macOS + Linux, amd64 + arm64.
- No Gatekeeper "unidentified developer" warning on first run.
- Lightweight nag when a new version is available.
- Path that scales gracefully if the repo goes public later.

**Non-goals**
- Windows support.
- Apple Developer Program signing/notarization ($99/yr + CI complexity).
  We'll lean on install methods that don't trigger Gatekeeper instead.
- Landing in `homebrew-core` (notability bar, maintainer review).
- Self-update that mutates the installed binary. Nag only — user runs
  the upgrade command themselves.

## The constraint that shapes everything: private repo

`github.com/commonfabric/bay` is private and has to stay that way for
now (pending team discussion). Most "frictionless install" channels
assume a public repo — none of GitHub's release artifacts are
anonymously fetchable for private repos.

How each channel fares:

| Channel | Private repo | Public repo |
|---|---|---|
| `go install` | Works with `GOPRIVATE` + GitHub token in netrc, per-dev setup | Works |
| GitHub Releases (browser/curl) | 404 without auth header | Works |
| `curl \| sh` install script (anonymous) | No | Yes |
| Install script using `gh release download` | Yes — devs already have `gh` authed | Yes |
| Homebrew tap (public formula → private asset) | No — `brew` can't auth to GitHub for the tarball | Yes |
| Homebrew tap, fully private | Possible, every dev must auth their tap | N/A |

The design below assumes **private**, with an explicit upgrade path if
the repo later goes public.

## Tooling choice: GoReleaser

GoReleaser is the standard for Go-CLI distribution. One YAML config:
- builds a matrix of OS/arch
- embeds version/commit/date via ldflags
- emits checksummed archives
- uploads to GitHub Releases on tag push
- (when public) auto-publishes a Homebrew formula to a tap repo
- (optional) emits `.deb`/`.rpm` via `nfpm`

We'll use it. Triggered from a `v*` tag push via a new GitHub Actions
workflow.

The existing `cmd/bay/main.go` already has `var version` ready for
ldflags injection (`main.go:11-49`). We'll add `commit` and `date` vars
and extend `internal/cli/version.go` to print all three.

## Recommended path under "private"

### Install: `gh`-authenticated script

Every teammate already has `gh` authed against `commonfabric`. The
install command becomes:

```sh
gh release download --repo commonfabric/bay --pattern "*$(uname -s)_$(uname -m)*.tar.gz" -O - \
  | tar -xz -C "$HOME/.local/bin" bay
```

We ship this as `install.sh` in the repo root (devs have repo access,
so they can `curl -fsSL` the raw URL with `gh auth token` in the header,
or just `git pull && ./install.sh`). The script:
- detects OS/arch and normalizes to GoReleaser's archive naming
- prefers `~/.local/bin`, falls back to `/usr/local/bin` with `--prefix`
- verifies the checksum from the release's `checksums.txt`
- prints the next steps (`bay setup`, completions)

Because the install path is `gh` → curl (no Finder, no Safari), the
downloaded binary never gets the `com.apple.quarantine` xattr, so
**Gatekeeper does not warn**. This is the same trick Homebrew uses.

If a teammate downloads a tarball from the GitHub web UI manually,
that one *will* be quarantined — we document the
`xattr -d com.apple.quarantine ~/.local/bin/bay` workaround in the
README, `docs/human-guide.md`, and `internal/cli/agent-guide.md`.

### Update nag

`internal/version/check.go` (new):

- On every CLI start, read `~/.cache/bay/version-check.json`
  (`{ checked_at, latest_version }`, XDG-aware).
- If `checked_at` is >24h old, fire an async goroutine to refresh:
  shell out to `gh api repos/commonfabric/bay/releases/latest`,
  write the result back to the cache. We do **not** wait on the
  goroutine — the current invocation never blocks on network.
- If cached `latest_version` > current `version`, print one nag line
  to stderr at exit:
  ```
  bay v0.2.0 available — run `./install.sh` to upgrade
  ```
- Skipped when `version == "dev"` (local builds via `go install`).
- Suppressible via `BAY_NO_VERSION_CHECK=1`.
- If `gh` isn't on PATH, the nag silently no-ops. (The install
  script requires `gh`, so on any properly-set-up dev box it's there.)

The goroutine-based refresh means the *first* run after a release
still shows a stale cache, but the *next* run picks up the new
version. That's an acceptable tradeoff for never blocking the CLI.

### Versioning

Start at `v0.1.0`. SemVer. Tags are the source of truth — GoReleaser
keys off them. CI workflow only fires on `v*` tag push, not on every
commit.

## If we go public later

A public repo unlocks the standard, low-friction channels with minimal
rework:

1. **Homebrew tap.** Create `commonfabric/homebrew-bay`, add a
   `brews:` block to `.goreleaser.yaml`. GoReleaser auto-publishes the
   formula on each release. Install becomes
   `brew install commonfabric/bay/bay`. Brew strips the quarantine
   xattr, so no Gatekeeper warning.
2. **Anonymous `curl | sh`.** The `install.sh` switches its download
   path from `gh release download` to a plain `curl` against
   `github.com/.../releases/download/...`. Same script, same UX, no
   `gh` requirement.
3. **Update nag.** Switches from `gh api` to anonymous
   `https://api.github.com/repos/.../releases/latest`. Drops the `gh`
   dependency.

The split is small enough that we'll write the install script and the
nag with both code paths from day one, gated on a single
`BAY_PRIVATE_DISTRIBUTION` build flag (or just runtime detection: try
anonymous first, fall back to `gh`). When the repo flips to public,
we delete the `gh` paths and add the Homebrew block.

## Phased rollout

Each phase is independently shippable.

1. **Release pipeline.** `.goreleaser.yaml`, `.github/workflows/release.yml`,
   extend version vars + `bay version` output. Tag `v0.1.0`, verify
   artifacts on the release page.
2. **Install script.** `install.sh` with `gh release download`.
   Update README install section. Document `xattr` workaround.
3. **Update nag.** `internal/version/check.go`, wire into CLI startup,
   document the env var in `docs/human-guide.md`.
4. **(If/when public)** Homebrew tap repo + `brews:` block + switch
   install script to anonymous curl + drop `gh` from the nag path.

## Open questions

- **Linux package format.** The team is mostly Mac with a couple of
  Linux users. Do we need `.deb`/`.rpm`, or is the `install.sh` →
  `~/.local/bin` flow good enough? Default: skip packages, revisit
  if a Linux teammate asks.
- **Where does `install.sh` live for the curl-pipe pattern?** Under
  private, the natural answer is "you already have the repo cloned."
  But if we want a one-liner for fresh boxes, we'd need a separate
  public bootstrap repo (overkill) or use `gh` to fetch the script
  itself. Probably acceptable to require `git clone` once.
- **Notarization escape hatch.** If we ever ship to people *outside*
  the org — open-source release, demo recipients — we'll need either
  notarization or a much louder install message. Not a Phase 1
  concern.
- **Tap location if we go public.** Personal `mike/homebrew-bay` vs
  `commonfabric/homebrew-bay`. The latter scales better for a team
  tool.
