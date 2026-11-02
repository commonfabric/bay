package cli

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"strings"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	gitpkg "github.com/commonfabric/bay/internal/git"
	"github.com/commonfabric/bay/internal/manifest"
	tmuxpkg "github.com/commonfabric/bay/internal/tmux"
	"github.com/spf13/cobra"
)

// RegisterCompletion adds the completion subcommand and registers all
// dynamic completion functions for arguments and flags.
func RegisterCompletion(root *cobra.Command) {
	completionCmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish]",
		Short: "Generate shell completion scripts",
		Long: `Generate shell completion scripts for bay.

Add to your .zshrc or .bashrc:

  if command -v bay > /dev/null ; then
    source <(bay completion zsh)    # or bash
  fi

For fish (~/.config/fish/config.fish):

  if command -v bay > /dev/null
    bay completion fish | source
  end
`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		// Sourced from shell rc on every new shell — never fork the
		// monitor from this path.
		Annotations: map[string]string{noMonitorAutostartAnnotation: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(os.Stdout)
			case "zsh":
				return root.GenZshCompletion(os.Stdout)
			case "fish":
				return root.GenFishCompletion(os.Stdout, true)
			default:
				return cmd.Help()
			}
		},
	}

	completionCmd.GroupID = "other"
	root.AddCommand(completionCmd)

	// Register dynamic completions for all subcommands.
	// We look up commands by path from the root so this stays decoupled
	// from the individual command constructors.
	registerCompletions(root)
}

// findCmd walks the command tree to find a subcommand by space-separated path.
func findCmd(root *cobra.Command, path string) *cobra.Command {
	parts := strings.Fields(path)
	cmd := root
	for _, p := range parts {
		found := false
		for _, c := range cmd.Commands() {
			if c.Name() == p {
				cmd = c
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return cmd
}

func registerCompletions(root *cobra.Command) {
	bayCompl := bayCompletions()
	sfCompl := surfaceCompletions()
	dockCompl := dockCompletions()
	goCompl := goCompletions()
	bayFlagCompl := bayFlagCompletions()
	dockFlagCompl := dockFlagCompletions()

	// Bay-target positional: bay verbs operate on bays.
	// surface new / shell / bay new select their target via
	// --bay/--dock flags, not a positional, so they're not in this list.
	for _, path := range []string{
		"close", "tidy", "show", "rename", "prepare",
		"edit",
		"surface new edit",
	} {
		if cmd := findCmd(root, path); cmd != nil {
			cmd.ValidArgsFunction = bayCompl
		}
	}

	// Surface-name positional: sf verbs.
	for _, path := range []string{
		"surface close", "surface show", "surface rename",
	} {
		if cmd := findCmd(root, path); cmd != nil {
			cmd.ValidArgsFunction = sfCompl
		}
	}

	// bay surface new agent / bay agent — first positional is the agent type.
	for _, path := range []string{"surface new agent", "agent"} {
		if cmd := findCmd(root, path); cmd != nil {
			cmd.ValidArgsFunction = agentArgCompletions
		}
	}

	// Dock commands: dock close, dock init, dock recover, dock sync, dock tree, dock show, dock rename.
	for _, path := range []string{"dock close", "dock init", "dock recover", "dock sync", "dock tree", "dock show", "dock rename"} {
		if cmd := findCmd(root, path); cmd != nil {
			cmd.ValidArgsFunction = dockCompl
		}
	}

	// bay go is bay-scoped fuzzy match.
	if cmd := findCmd(root, "go"); cmd != nil {
		cmd.ValidArgsFunction = goCompl
	}

	// bay new — positional is the new bay's display name (free
	// text, no completion). Flags carry completions for the things bay
	// can suggest: --dock, --agent, --branch.
	if cmd := findCmd(root, "new"); cmd != nil {
		cmd.RegisterFlagCompletionFunc("dock", dockFlagCompl)
		cmd.RegisterFlagCompletionFunc("agent", agentFlagCompletions)
		cmd.RegisterFlagCompletionFunc("branch", branchCompletions)
	}
	// surface new is now a parent with shell/agent/cmd/edit subcommands
	// Flag completions are registered on those subcommands below.
	if cmd := findCmd(root, "dock new"); cmd != nil {
		cmd.RegisterFlagCompletionFunc("agent", agentFlagCompletions)
	}

	// --bay and --dock flag completions on every surface verb that
	// supports them. Bay commands get --dock too. The bay new
	// <kind> commands also get --split for symmetry with sf new.
	for _, path := range []string{
		"surface close", "surface show", "surface rename",
		"shell", "agent",
		"surface new shell", "surface new agent", "surface new cmd",
	} {
		if cmd := findCmd(root, path); cmd != nil {
			cmd.RegisterFlagCompletionFunc("bay", bayFlagCompl)
			cmd.RegisterFlagCompletionFunc("dock", dockFlagCompl)
		}
	}
	if cmd := findCmd(root, "edit"); cmd != nil {
		cmd.RegisterFlagCompletionFunc("bay", bayFlagCompl)
		cmd.RegisterFlagCompletionFunc("dock-name", dockFlagCompl)
	}
	for _, path := range []string{"surface new shell", "surface new agent", "surface new cmd"} {
		if cmd := findCmd(root, path); cmd != nil {
			cmd.RegisterFlagCompletionFunc("split", splitCompletions)
		}
	}
	for _, path := range []string{"close", "tidy", "show", "rename", "prepare"} {
		if cmd := findCmd(root, path); cmd != nil {
			cmd.RegisterFlagCompletionFunc("dock", dockFlagCompl)
		}
	}
}

// loadManifestForCompletions loads the manifest silently (errors return nil).
func loadManifestForCompletions() *manifest.Manifest {
	p := bayPaths()
	m, err := manifest.Load(p.ManifestFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return manifest.New()
		}
		return nil
	}
	return m
}

// loadConfigForCompletions loads the config silently.
func loadConfigForCompletions() *config.Config {
	p := bayPaths()
	cfg, err := config.Load(p.ConfigFile)
	if err != nil {
		return nil
	}
	return cfg
}

// --- Candidate builders ---
//
// These pure functions take manifest/config data and return ready-to-use
// completion strings. The positional and flag completers below wrap them
// with the right args-guard semantics.

// addCandidate is a helper that appends "val\tdesc" to completions, deduping
// by val. Used by all candidate builders.
func addCandidate(completions *[]string, seen map[string]bool, val, desc string) {
	if val == "" || seen[val] {
		return
	}
	seen[val] = true
	if desc != "" {
		*completions = append(*completions, val+"\t"+desc)
	} else {
		*completions = append(*completions, val)
	}
}

// addQualifiedCandidate emits a bay identifier in both bare and
// dock-qualified forms. The two forms get separate descriptions because
// the qualified form's dock prefix is already visible in the candidate
// text, so its description doesn't need to repeat the dock name.
func addQualifiedCandidate(completions *[]string, seen map[string]bool, dock, val, bareDesc, qualifiedDesc string) {
	addCandidate(completions, seen, val, bareDesc)
	addCandidate(completions, seen, dock+":"+val, qualifiedDesc)
}

// bayCandidates returns bay identifier candidates: each
// bay as ID and dock:ID, plus the "self" keyword.
//
// Names are strictly not CLI keys under the strict resolver, so they
// aren't emitted as candidate values — but the friendly Name is woven
// into each ID candidate's description so the user can see, for
// example, that `b1` is "auth-fix".
func bayCandidates(m *manifest.Manifest) []string {
	seen := make(map[string]bool)
	var completions []string
	for _, ref := range manifest.AllBays(m) {
		bay := ref.Bay

		// Branch/PR fragment for the description.
		extras := ""
		if !manifest.IsHomeBay(bay) && bay.Worktree != nil {
			if bay.Worktree.Branch != "" {
				extras += " " + bay.Worktree.Branch
			}
			if bay.Worktree.PR != "" {
				extras += " #" + bay.Worktree.PR
			}
		}

		// Include the bay's optional Name in descriptions so users can
		// tell named bays apart by their friendly tag (the candidate
		// itself is the stable ID). Empty for unnamed bays.
		nameSuffix := ""
		if bay.Name != "" {
			nameSuffix = " " + bay.Name
		}
		// Bare form's description names the dock so the user can tell
		// which dock owns this ID; the qualified form drops the dock
		// since the candidate text already shows it.
		addQualifiedCandidate(&completions, seen, ref.Dock, bay.ID,
			ref.Dock+nameSuffix+extras,
			strings.TrimSpace(nameSuffix+extras))
	}
	addCandidate(&completions, seen, "self", "current bay")
	return completions
}

// surfaceCandidates returns surface identifier candidates: each surface in
// two qualified forms (bay:name and dock:bay:name). Bare names are deliberately
// omitted because they collide across bays and would suggest a surface
// from one bay while the resolver picks one from another.
//
// includeSelf controls whether the "self" keyword is appended; callers should
// pass false when the user has already typed a colon (qualified `self` is
// always literal, not a keyword).
func surfaceCandidates(m *manifest.Manifest, tc tmuxpkg.Interface, includeSelf bool) []string {
	seen := make(map[string]bool)
	var completions []string
	if includeSelf {
		addCandidate(&completions, seen, "self", "current surface")
	}

	// Determine current bay for bare-name completions. Track by stable ID
	// so unnamed bays still match (Name can be empty).
	var currentDock, currentBayID string
	if session, err := tc.CurrentSession(); err == nil {
		currentDock = session
		if winID, err := tc.CurrentWindowID(); err == nil {
			for _, ref := range manifest.AllBays(m) {
				if ref.Dock != session {
					continue
				}
				for _, s := range ref.Bay.Surfaces {
					if s.Tmux != nil && s.Tmux.WindowID == winID {
						currentBayID = ref.Bay.ID
						break
					}
				}
				if currentBayID != "" {
					break
				}
			}
		}
	}

	// Dock-level surfaces (e.g., dock editor).
	for _, dock := range m.Docks {
		for _, s := range dock.Surfaces {
			desc := string(s.Type) + " in dock " + dock.Name
			addCandidate(&completions, seen, "dock:"+s.Name, desc)
		}
	}

	// Bay surfaces. Qualified candidates use bay.ID (the stable handle
	// the resolver accepts) so unnamed bays still produce valid refs;
	// descriptions show the canonical label so users see "b1.auth-fix"
	// rather than a blank.
	for _, ref := range manifest.AllBays(m) {
		bay := ref.Bay
		label := engine.BayCompactLabel(bay)
		for _, s := range bay.Surfaces {
			desc := string(s.Type) + " in " + ref.Dock + ":" + label
			if ref.Dock == currentDock && bay.ID == currentBayID {
				addCandidate(&completions, seen, s.Name, desc)
			}
			addCandidate(&completions, seen, bay.ID+":"+s.Name, desc)
			addCandidate(&completions, seen, ref.Dock+":"+bay.ID+":"+s.Name, desc)
		}
	}
	return completions
}

// dockCandidates returns dock name candidates with checkout/agent descriptions.
func dockCandidates(m *manifest.Manifest) []string {
	var completions []string
	for _, dock := range m.Docks {
		var parts []string
		if dock.Path != "" {
			parts = append(parts, "path="+dock.Path)
		}
		if dock.Agent != "" {
			parts = append(parts, "agent="+dock.Agent)
		}
		desc := strings.Join(parts, " ")
		if desc != "" {
			completions = append(completions, dock.Name+"\t"+desc)
		} else {
			completions = append(completions, dock.Name)
		}
	}
	return completions
}

// agentCandidates returns agent type candidates: the built-in clients and
// model profiles plus any configured under [agents.*], deduped, sorted, and
// with disabled agents removed. It reuses availableAgents — the same set the
// palette's agent picker offers — so completion and the picker never drift.
// Without this, `bay agent <TAB>` and `--agent` completion were empty for the
// common case of a user relying on the built-ins with no [agents.*] config.
func agentCandidates(cfg *config.Config) []string {
	names := availableAgents(cfg)
	completions := make([]string, 0, len(names))
	for _, name := range names {
		completions = append(completions, name+"\t"+agentCandidateDesc(cfg, name))
	}
	return completions
}

// agentCandidateDesc is the completion help text for an agent: a model
// profile shows its base client ("fable (claude)"), so it doesn't read as a
// peer of the clients it wraps; a plain agent shows the command it runs.
func agentCandidateDesc(cfg *config.Config, name string) string {
	if cfg == nil {
		return name
	}
	if base := cfg.ProfileBase(name); base != "" {
		return name + " (" + base + ")"
	}
	if info, ok := cfg.ResolveAgent(name); ok && info.Command != "" {
		return info.Command
	}
	return name
}

// --- Positional completers (with len(args) > 0 short-circuit) ---

// bayCompletions returns a positional completer for bay
// identifiers.
func bayCompletions() func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		m := loadManifestForCompletions()
		if m == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return bayCandidates(m), cobra.ShellCompDirectiveNoFileComp
	}
}

// surfaceCompletions returns a positional completer for surface identifiers.
// 'self' is only included when toComplete has no colon, since qualified
// `self` (`b1:self`, `labs:b1:self`) is always literal in the resolver.
func surfaceCompletions() func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		m := loadManifestForCompletions()
		if m == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		includeSelf := !strings.Contains(toComplete, ":")
		tc := tmuxpkg.NewReal()
		return surfaceCandidates(m, tc, includeSelf), cobra.ShellCompDirectiveNoFileComp
	}
}

// dockCompletions returns a positional completer for dock names.
func dockCompletions() func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		m := loadManifestForCompletions()
		if m == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return dockCandidates(m), cobra.ShellCompDirectiveNoFileComp
	}
}

// goCompletions returns bay names, branches, PR numbers for bay go.
func goCompletions() func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		m := loadManifestForCompletions()
		if m == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		seen := make(map[string]bool)
		var completions []string
		add := func(val, desc string) {
			if val == "" || seen[val] {
				return
			}
			seen[val] = true
			if desc != "" {
				completions = append(completions, val+"\t"+desc)
			} else {
				completions = append(completions, val)
			}
		}

		for _, ref := range manifest.AllBays(m) {
			bay := ref.Bay
			label := engine.BayCompactLabel(bay)
			add(label, ref.Dock)
			add(ref.Dock, "dock")
			if !manifest.IsHomeBay(bay) && bay.Worktree != nil {
				if bay.Worktree.Branch != "" {
					add(bay.Worktree.Branch, ref.Dock+" "+label)
				}
				if bay.Worktree.PR != "" {
					add("#"+bay.Worktree.PR, ref.Dock+" "+label)
				}
			}
		}

		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

// --- Flag completers (no args guard, since flag values aren't positionals) ---

// bayFlagCompletions returns a flag-value completer for bay
// identifiers (used for --bay on surface verbs).
func bayFlagCompletions() func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		m := loadManifestForCompletions()
		if m == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return bayCandidates(m), cobra.ShellCompDirectiveNoFileComp
	}
}

// dockFlagCompletions returns a flag-value completer for dock names (used
// for --dock on bay and surface verbs).
func dockFlagCompletions() func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		m := loadManifestForCompletions()
		if m == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return dockCandidates(m), cobra.ShellCompDirectiveNoFileComp
	}
}

// agentFlagCompletions returns a flag-value completer for agent types (used
// for --agent on bay new, surface new, dock new).
func agentFlagCompletions(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg := loadConfigForCompletions()
	if cfg == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return agentCandidates(cfg), cobra.ShellCompDirectiveNoFileComp
}

// agentArgCompletions is the positional version of agentFlagCompletions: it
// returns nothing once an agent has already been provided. Used for
// `bay surface new agent <agent>`.
func agentArgCompletions(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return agentFlagCompletions(cmd, args, toComplete)
}

// splitCompletions returns valid pane split directions.
func splitCompletions(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return []string{
		"h\thorizontal",
		"v\tvertical",
	}, cobra.ShellCompDirectiveNoFileComp
}

// branchCompletions returns local and remote branch names for the
// --branch flag, filtering out the default branch and branches already
// checked out in a bay.
func branchCompletions(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	m := loadManifestForCompletions()
	repoPath := repoPathForCompletion(cmd, m)
	if repoPath == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	// Determine default branch for filtering.
	defaultBranch := "main"
	if defOut, defErr := exec.Command("git", "-C", repoPath, "symbolic-ref", "refs/remotes/origin/HEAD").Output(); defErr == nil {
		parts := strings.Split(strings.TrimSpace(string(defOut)), "/")
		if len(parts) > 0 {
			defaultBranch = parts[len(parts)-1]
		}
	}

	// Collect branches already in use by bays.
	inUse := map[string]bool{}
	if m != nil {
		for _, ref := range manifest.AllBays(m) {
			if !manifest.IsHomeBay(ref.Bay) && ref.Bay.Worktree != nil && ref.Bay.Worktree.Branch != "" {
				inUse[ref.Bay.Worktree.Branch] = true
			}
		}
	}

	seen := map[string]bool{}
	var completions []string
	add := func(branch string) {
		if branch == "" || branch == "HEAD" || branch == defaultBranch || seen[branch] || inUse[branch] {
			return
		}
		seen[branch] = true
		completions = append(completions, branch)
	}

	// Local branches (includes branches bay failed to clean up).
	if out, err := exec.Command("git", "-C", repoPath, "branch", "--format=%(refname:short)").Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			add(line)
		}
	}

	// Remote branches (stripped of origin/ prefix).
	if out, err := exec.Command("git", "-C", repoPath, "branch", "-r", "--format=%(refname:short)").Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			add(strings.TrimPrefix(line, "origin/"))
		}
	}

	return completions, cobra.ShellCompDirectiveNoFileComp
}

// repoPathForCompletion resolves a checkout path for completion context.
// It checks the --dock flag, then falls back to the CWD git root.
func repoPathForCompletion(cmd *cobra.Command, m *manifest.Manifest) string {
	// Try --dock flag → dock checkout.
	if dockFlag, err := cmd.Flags().GetString("dock"); err == nil && dockFlag != "" && m != nil {
		if dock := m.FindDock(dockFlag); dock != nil && dock.Path != "" {
			return config.ExpandPath(dock.Path)
		}
	}

	// Try current tmux session → dock checkout.
	if m != nil {
		if sess, err := tmuxpkg.NewReal().CurrentSession(); err == nil {
			if dock := m.FindDock(sess); dock != nil && dock.Path != "" {
				return config.ExpandPath(dock.Path)
			}
		}
	}

	// Fall back to CWD git root.
	if cwd, err := os.Getwd(); err == nil {
		if root, err := gitpkg.NewReal().RepoRoot(cwd); err == nil {
			return root
		}
	}

	return ""
}
