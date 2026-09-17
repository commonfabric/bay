// Package engine implements bay's core business logic, coordinating
// config, manifest, tmux, and git operations.
package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/git"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/tmux"
)

// Engine is the core coordinator for all bay operations.
type Engine struct {
	Config       *config.Config
	Tmux         tmux.Interface
	Git          git.Interface
	configPath   string
	manifestPath string
	archivePath  string

	// startWorker dispatches a detached prepare-worker process. Tests
	// override this to record calls without forking.
	startWorker func(exe string, args []string) error
}

// New creates a new Engine.
func New(cfg *config.Config, configPath, manifestPath, archivePath string, t tmux.Interface, g git.Interface) *Engine {
	return &Engine{
		Config:       cfg,
		configPath:   configPath,
		manifestPath: manifestPath,
		archivePath:  archivePath,
		Tmux:         t,
		Git:          g,
		startWorker:  startWorkerProcess,
	}
}

// LoadManifest loads the manifest, creating an empty one if it doesn't exist.
// Callers that mutate the manifest should prefer withManifest so the full
// read-modify-write cycle stays atomic under a file lock.
func (e *Engine) LoadManifest() (*manifest.Manifest, error) {
	m, err := manifest.Load(e.manifestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return manifest.New(), nil
		}
		return nil, err
	}
	return m, nil
}

// saveManifest saves the manifest with file locking.
func (e *Engine) saveManifest(m *manifest.Manifest) error {
	return manifest.Save(e.manifestPath, m)
}

// withManifest loads the manifest, passes it to fn for modification, and saves.
// The entire read-modify-write cycle is done under a file lock.
func (e *Engine) withManifest(fn func(m *manifest.Manifest) error) error {
	return manifest.LockedUpdate(e.manifestPath, fn)
}

// withManifestMaybe loads the manifest, passes it to fn, and saves only if fn reports changes.
func (e *Engine) withManifestMaybe(fn func(m *manifest.Manifest) (bool, error)) error {
	return manifest.LockedUpdateMaybe(e.manifestPath, fn)
}

// SaveConfig writes the current config to disk.
func (e *Engine) SaveConfig() error {
	if e.configPath == "" {
		return nil
	}
	return config.Save(e.configPath, e.Config)
}

// ReloadConfig re-reads the config file into e.Config so a long-running
// process (the monitor) picks up edits — e.g. enabling auto-descriptions
// via `bay setup` — without a restart. On read/parse error or with no
// config path, the existing config is kept.
func (e *Engine) ReloadConfig() error {
	if e.configPath == "" {
		return nil
	}
	cfg, err := config.Load(e.configPath)
	if err != nil {
		return err
	}
	e.Config = cfg
	return nil
}

// resolvedDockAgent returns the effective agent for a dock,
// checking config overrides first, then the manifest default.
func (e *Engine) resolvedDockAgent(dockName string, m *manifest.Manifest) string {
	dock := m.FindDock(dockName)
	manifestDefault := ""
	if dock != nil {
		manifestDefault = dock.Agent
	}
	return e.Config.ResolvedDockAgent(dockName, manifestDefault)
}

// resolvedAgentArgs returns the effective args for a specific agent in
// a dock: persistent args (every invocation) and launch-only args
// (fresh launches, dropped on resume — e.g. --model).
func (e *Engine) resolvedAgentArgs(dockName, agentName string, m *manifest.Manifest) (args, launchArgs []string) {
	dock := m.FindDock(dockName)
	var manifestDefault map[string][]string
	if dock != nil {
		manifestDefault = dock.AgentArgs
	}
	return e.Config.ResolvedAgentArgs(dockName, agentName, manifestDefault),
		e.Config.ResolvedAgentLaunchArgs(dockName, agentName)
}

var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ValidateName checks that a name matches the naming constraints.
func ValidateName(name string) error {
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match [a-zA-Z0-9_-]+", name)
	}
	return nil
}

// ValidateBayName extends ValidateName by reserving generated bay ID
// patterns (^b[1-9]\d*$ and legacy ^w[1-9]\d*$) plus the reserved home
// handle. Bay IDs and Names share the same identifier namespace, so allowing
// a Name to look like an ID or reserved target would create ambiguous CLI
// references. Names like b1 / w1 / home are rejected; b0, w01, my-w1, bay1,
// W1 are fine.
func ValidateBayName(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if manifest.IsBayID(name) {
		return fmt.Errorf("invalid bay name %q: matches a reserved bay ID pattern (^b[1-9]\\d*$ or ^w[1-9]\\d*$); pick a different name", name)
	}
	if manifest.IsReservedBayID(name) {
		return fmt.Errorf("invalid bay name %q: reserved for the dock's home pseudo-bay; pick a different name", name)
	}
	return nil
}

// MaxDescriptionFirstLineLen caps the first line of a bay description.
// The first line is the glanceable label — shown in picker, ls/tree, and the
// M-/ flash — and competes with other metadata for terminal width.
const MaxDescriptionFirstLineLen = 80

// MaxDescriptionLen caps the total description length (first line plus
// optional body). The body is opt-in context for return-to-bay recall
// and is surfaced only via the M-? popup and JSON output.
const MaxDescriptionLen = 2000

// DescriptionFirstLine returns the first line of a description (the part
// before the first newline), or the whole string if it is single-line.
// Used in contexts where descriptions must fit on one row (picker, ls/tree,
// flash).
func DescriptionFirstLine(desc string) string {
	first, _, _ := strings.Cut(desc, "\n")
	return first
}

// ValidateDescription rejects descriptions with disallowed control characters,
// a first line longer than MaxDescriptionFirstLineLen, or total length
// exceeding MaxDescriptionLen. Newlines are allowed (for the optional body);
// carriage returns and tabs are not. An empty string is valid and means
// "clear".
func ValidateDescription(desc string) error {
	if len(desc) > MaxDescriptionLen {
		return fmt.Errorf("description too long (%d > %d)", len(desc), MaxDescriptionLen)
	}
	first := DescriptionFirstLine(desc)
	if len(first) > MaxDescriptionFirstLineLen {
		return fmt.Errorf("description first line too long (%d > %d)", len(first), MaxDescriptionFirstLineLen)
	}
	for _, r := range desc {
		if r == '\r' || r == '\t' {
			return fmt.Errorf("description must not contain tabs or carriage returns")
		}
	}
	return nil
}

func uniqueBayName(dock *manifest.Dock, current *manifest.Bay, base string) string {
	candidate := base
	for i := 2; ; i++ {
		existing := dock.FindBay(candidate)
		if existing == nil || existing == current {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

// isPlaceholderName reports whether a bay's Name is a placeholder
// — empty, meaning Name has never been set. Sync's auto-rename fills
// from the branch only when Name is a placeholder; user-set Names are
// sticky.
//
// We also treat generated ID-shaped names (^b[1-9]\d*$ or legacy
// ^w[1-9]\d*$) as placeholders so that
// any pre-Phase-6 manifest still in the wild (where unbranched bays
// got w<N> Names from the old path-basename default) gets its Name filled
// from the branch on next sync, rather than carrying a stale ID-shaped
// label that ValidateBayName would now reject for new entries.
func isPlaceholderName(name string) bool {
	return name == "" || manifest.IsBayID(name)
}

// abbreviateBranch strips common prefixes from branch names for display.
// The result is sanitized to be a valid display name.
func abbreviateBranch(branch string) string {
	prefixes := []string{
		"feature/", "fix/", "chore/", "bugfix/", "hotfix/",
		"release/", "refactor/", "docs/", "test/", "ci/",
	}
	name := branch
	for _, p := range prefixes {
		if strings.HasPrefix(branch, p) {
			name = branch[len(p):]
			break
		}
	}
	// Sanitize: replace invalid characters with hyphens
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	result := b.String()
	// Trim leading/trailing hyphens and collapse multiple hyphens
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	result = strings.Trim(result, "-")
	if result == "" {
		return branch // fallback to raw branch if sanitization empties it
	}
	// Names that match a reserved generated bay ID pattern (^b[1-9]\d*$,
	// legacy ^w[1-9]\d*$) or a reserved bay target are rejected by
	// ValidateBayName, so branches like fix/b1, fix/w1, or feature/home
	// would otherwise produce an unnameable bay.
	// Prefix so the result is always a legal Name.
	if manifest.IsBayID(result) || manifest.IsReservedBayID(result) {
		result = branchAbbrevReservedPrefix + result
	}
	return result
}

// branchAbbrevReservedPrefix is prepended to abbreviateBranch results that
// would otherwise collide with a reserved bay ID pattern. "br-"
// reads as a hint that the Name was branch-derived.
const branchAbbrevReservedPrefix = "br-"

// launchSurfaceInTmux launches the appropriate command in a tmux pane
// based on the surface type and returns a populated Surface.
// Used by bay creation and surface add operations. When resume is
// true and the surface is an agent, the agent is launched with its
// configured resume_args so the prior session is picked up.
func (e *Engine) launchSurfaceInTmux(tmuxPaneID, dockName string, surfaceType manifest.SurfaceType, agent, cmd, cwd string, agentArgs, launchArgs []string, resume bool) (manifest.Surface, error) {
	s := manifest.Surface{
		Type:    surfaceType,
		Backend: manifest.SurfaceBackendTmux,
		Tmux:    &manifest.TmuxAttrs{},
	}

	switch surfaceType {
	case manifest.SurfaceTypeAgent:
		s.Agent = &agent
		agentCmd, err := e.buildAgentCommand(agent, agentArgs, launchArgs, resume)
		if err != nil {
			return manifest.Surface{}, err
		}
		_ = e.Tmux.RespawnPane(tmuxPaneID, cwd, agentCmd)
	case manifest.SurfaceTypeCmd:
		s.Command = &cmd
		_ = e.Tmux.RespawnPane(tmuxPaneID, cwd, cmd)
	case manifest.SurfaceTypeShell:
		// Shell — no command to send.
	case manifest.SurfaceTypeEditor:
		if cmd != "" {
			s.Command = &cmd
			// Use RespawnPane so the pane dies when the editor exits,
			// rather than falling back to a shell.
			_ = e.Tmux.RespawnPane(tmuxPaneID, cwd, cmd)
		}
	}

	return s, nil
}
