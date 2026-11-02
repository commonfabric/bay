package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
)

// BayNewOptions are options for creating a new bay.
type BayNewOptions struct {
	Dock         string // dock name (required)
	Dir          string // external directory (makes it external type)
	Name         string // display name override
	Description  string // short free-form label shown in picker/ls/tree
	Agent        string // agent override
	RequireAgent bool   // fail if no agent can be resolved
	Shell        bool   // open shell instead of agent
	Branch       string // git branch to checkout (creates it if new)
}

// BayNew creates a new bay with surfaces.
func (e *Engine) BayNew(opts BayNewOptions) (*manifest.Bay, error) {
	dockName := opts.Dock

	m, err := e.LoadManifest()
	if err != nil {
		return nil, err
	}

	// Ensure dock exists in manifest.
	dock := m.FindDock(dockName)
	if dock == nil {
		return nil, fmt.Errorf("unknown dock %q", dockName)
	}

	// Determine bay type and path.
	var bayType manifest.BayType
	var bayPath string
	var worktreeAttrs *manifest.WorktreeAttrs
	var branchExists bool

	nameExplicit := opts.Name != ""
	if manifest.IsReservedBayID(opts.Name) {
		return nil, fmt.Errorf("home is reserved for the dock checkout; use `bay home`")
	}
	displayName := opts.Name
	if displayName == "" && opts.Branch != "" {
		displayName = uniqueBayName(dock, nil, abbreviateBranch(opts.Branch))
	}
	// Validate explicit/branch-derived name early. Names matching the
	// reserved generated ID patterns (^b[1-9]\d*$ or legacy ^w[1-9]\d*$)
	// or reserved home handle are rejected here.
	if displayName != "" {
		if err := ValidateBayName(displayName); err != nil {
			return nil, err
		}
	}
	desc := strings.TrimSpace(opts.Description)
	if err := ValidateDescription(desc); err != nil {
		return nil, err
	}
	if nameExplicit && dock.FindBay(displayName) != nil {
		return nil, fmt.Errorf("bay name %q already exists in dock %q", displayName, dockName)
	}

	agentName := ""
	if !opts.Shell {
		agentName, err = e.resolveBayAgent(dockName, m, opts.Agent, opts.RequireAgent)
		if err != nil {
			return nil, err
		}
	}

	if opts.Dir != "" {
		// External bay.
		bayType = manifest.BayTypeExternal
		bayPath = config.ExpandPath(opts.Dir)
		if _, err := os.Stat(bayPath); err != nil {
			return nil, fmt.Errorf("external directory %q: %w", bayPath, err)
		}
	} else {
		// Worktree bay.
		bayType = manifest.BayTypeWorktree
		if dock.Path == "" {
			return nil, fmt.Errorf("dock %q has no checkout; specify --dir for an external bay", dockName)
		}
		worktreeAttrs = &manifest.WorktreeAttrs{}

		wtDir := dock.EffectiveWorktreeDir()
		if err := os.MkdirAll(wtDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating worktree dir: %w", err)
		}
		// Worktree dirs are sequential (b1, b2, ...) and independent of
		// the bay name. The name can be renamed or auto-updated as
		// work pivots, but the on-disk directory stays stable.
		bayPath = filepath.Join(wtDir, nextBayDir(wtDir, dock))

		repoPath := config.ExpandPath(dock.Path)

		// Only fetch when --branch is used so the common case (no branch)
		// stays fast. The detached worktree uses origin/<default> which
		// relies on whatever was last fetched.
		if opts.Branch != "" {
			_ = e.Git.Fetch(repoPath)
			if exists, err := e.Git.BranchExists(repoPath, opts.Branch); err == nil && exists {
				branchExists = true
			}
		}

		wtBranch := ""
		if branchExists {
			wtBranch = opts.Branch
		}
		if err := e.Git.CreateWorktree(repoPath, bayPath, wtBranch); err != nil {
			return nil, fmt.Errorf("creating worktree: %w", err)
		}

		toCopy, err := e.resolveWorktreeInclude(repoPath)
		if err == nil {
			err = copyWorktreeIncludeFiles(repoPath, bayPath, toCopy)
		}
		if err != nil {
			_ = e.Git.RemoveWorktree(repoPath, bayPath, true)
			return nil, err
		}
	}

	// No explicit name and no branch → leave displayName empty. The
	// bay's ID is the stable handle (set after AddBay);
	// updateWindowNames falls back to the ID for the tab label, and
	// sync fills Name from the branch on first detection.

	// Resolve agent args.
	agentArgs, launchArgs := e.resolvedAgentArgs(dockName, agentName, m)

	// rollbackWorktree cleans up a worktree on failure.
	rollbackWorktree := func() {
		if bayType == manifest.BayTypeWorktree && dock.Path != "" {
			repoPath := config.ExpandPath(dock.Path)
			_ = e.Git.RemoveWorktree(repoPath, bayPath, true)
		}
	}

	// Ensure tmux session exists. We split the two cases: when the
	// session was already present we deliberately leave the SessionID
	// alone — existing bays may hold pane IDs from a dead tmux
	// instance, and persisting a new SessionID here would defeat the
	// gate's mismatched-marker preservation and let the next SyncAll
	// strip them. The user runs `bay recover` to reconcile after a
	// restart. When BayNew creates the session, persisting is still safe
	// only if this dock did not already have recorded tmux surfaces.
	persistCreatedSessionID := !dockHasRecordedTmuxSurfaces(dock)
	sessionID, sessionCreated, err := e.ensureSessionForBay(dockName, dock.SessionID)
	if err != nil {
		rollbackWorktree()
		return nil, err
	}

	// Create tmux window and position it at the end of existing bay
	// windows so new bays appear rightmost in the tab bar.
	initialWindowLabel := BayCompactLabel(&manifest.Bay{Name: displayName, Path: bayPath})
	windowID, err := e.Tmux.NewWindow(dockName, initialWindowLabel, bayPath)
	if err != nil {
		rollbackWorktree()
		return nil, fmt.Errorf("creating tmux window: %w", err)
	}
	e.cleanPlaceholders(dockName)
	e.positionNewWindow(dockName, windowID, "", m)

	// Get the first pane in the new window.
	panes, _ := e.Tmux.ListPanes(windowID)
	var tmuxPaneID string
	if len(panes) > 0 {
		tmuxPaneID = panes[0].ID
	}

	// Launch the initial surface.
	var surfaceType manifest.SurfaceType
	var surfaceName string
	switch {
	case opts.Shell:
		surfaceType = manifest.SurfaceTypeShell
		surfaceName = "shell"
	case agentName != "":
		surfaceType = manifest.SurfaceTypeAgent
		surfaceName = "agent"
	default:
		surfaceType = manifest.SurfaceTypeShell
		surfaceName = "shell"
	}

	surface, err := e.launchSurfaceInTmux(tmuxPaneID, dockName, surfaceType, agentName, "", bayPath, agentArgs, launchArgs, false)
	if err != nil {
		_ = e.Tmux.KillWindow(windowID)
		rollbackWorktree()
		return nil, err
	}
	surface.Name = surfaceName
	surface.Tmux.PaneID = tmuxPaneID
	surface.Tmux.WindowID = windowID
	surface.Tmux.LayoutGroup = 1

	// Build bay. Name may be empty here (no explicit name, no branch);
	// sync fills it from the branch on first detection and is sticky after,
	// matching the design's "stable yet semantic" goal.
	// A description passed at creation is user-owned, so the
	// auto-description backstop never overwrites it.
	descSource := ""
	if desc != "" {
		descSource = manifest.DescriptionSourceUser
	}
	bay := manifest.Bay{
		Name:              displayName,
		Type:              bayType,
		Path:              bayPath,
		Description:       desc,
		DescriptionSource: descSource,
		LastActive:        time.Now().Unix(),
		Worktree:          worktreeAttrs,
		Surfaces:          []manifest.Surface{},
	}

	var finalName string
	if err := e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}

		// Persist SessionID only when BayNew created the session
		// itself. See ensureSessionForBay's comment for why
		// adopting an existing session's marker is unsafe here.
		if sessionCreated && persistCreatedSessionID && dock.SessionID == "" {
			dock.SessionID = sessionID
		}

		finalName = displayName
		if !nameExplicit {
			finalName = uniqueBayName(dock, nil, displayName)
		} else if dock.FindBay(displayName) != nil {
			return fmt.Errorf("bay name %q already exists in dock %q", displayName, dockName)
		}

		bay.Name = finalName
		if err := dock.AddBay(bay); err != nil {
			return err
		}
		// Fill in the ID for the bay we just added. Lookups by Name
		// fail when finalName is empty, so resolve by path instead.
		manifest.AssignBayIDs(dock)
		addedBay := findBayByPath(dock, bayPath)
		if addedBay == nil {
			return fmt.Errorf("bay at %q not found after creation", bayPath)
		}
		if _, err := addedBay.AddSurface(surface); err != nil {
			_ = dock.RemoveBay(addedBay.ID)
			return err
		}
		return nil
	}); err != nil {
		_ = e.Tmux.KillWindow(windowID)
		rollbackWorktree()
		return nil, err
	}
	if finalName != displayName {
		finalWindowLabel := BayCompactLabel(&manifest.Bay{Name: finalName, Path: bayPath})
		_ = e.Tmux.RenameWindow(windowID, finalWindowLabel)
	}

	addedDock, err := e.LoadManifest()
	if err != nil {
		return nil, err
	}
	dock = addedDock.FindDock(dockName)
	if dock == nil {
		return nil, fmt.Errorf("unknown dock %q", dockName)
	}
	addedBay := findBayByPath(dock, bayPath)
	if addedBay == nil {
		return nil, fmt.Errorf("bay at %q not found after creation", bayPath)
	}

	// Set up git branch if requested. For existing branches the worktree
	// was already created on the branch; for new branches we create it now.
	if opts.Branch != "" && addedBay.Worktree != nil {
		if !branchExists {
			if err := e.Git.CreateBranch(bayPath, opts.Branch); err != nil {
				return addedBay, fmt.Errorf("bay created but branch creation failed: %w", err)
			}
		}
		if err := e.withManifest(func(m *manifest.Manifest) error {
			dock := m.FindDock(dockName)
			if dock == nil {
				return fmt.Errorf("unknown dock %q", dockName)
			}
			bay := findBayByPath(dock, bayPath)
			if bay == nil {
				return fmt.Errorf("bay at %q not found in dock %q", bayPath, dockName)
			}

			bay.Worktree.Branch = opts.Branch
			if isPlaceholderName(bay.Name) {
				newName := uniqueBayName(dock, bay, abbreviateBranch(opts.Branch))
				if newName != bay.Name {
					bay.Name = newName
					e.updateWindowNames(bay, "")
				}
			}
			return nil
		}); err != nil {
			return addedBay, fmt.Errorf("bay created but metadata update failed: %w", err)
		}
	}

	updatedManifest, err := e.LoadManifest()
	if err != nil {
		return nil, err
	}
	updatedDock := updatedManifest.FindDock(dockName)
	if updatedDock == nil {
		return nil, fmt.Errorf("unknown dock %q", dockName)
	}
	updatedBay := findBayByPath(updatedDock, bayPath)
	if updatedBay == nil {
		return nil, fmt.Errorf("bay at %q not found in dock %q", bayPath, dockName)
	}

	preparePlan, err := e.PreparePlan(dockName, updatedBay.ID)
	if err != nil {
		return updatedBay, fmt.Errorf("bay created but prepare plan failed: %w", err)
	}
	if len(preparePlan.Steps) > 0 {
		if err := e.DispatchPrepareWorker(dockName, updatedBay.ID); err != nil {
			return updatedBay, fmt.Errorf("bay created but prepare dispatch failed: %w", err)
		}
	}

	// Re-evaluate tab name lengths now that bay count changed.
	e.refreshDockWindowNames(updatedDock)

	return updatedBay, nil
}

type closeBayResult struct {
	windowIDs   []string
	dismissDock bool
}

// closeBayState does the safety checks, worktree removal, archive,
// and manifest update for a bay — but NOT the destructive tmux
// kill. Returns the tmux window IDs the caller should kill afterward,
// or dismissDock for the confirmed last-home close path.
//
// This split exists so callers can defer all destructive tmux work until
// every manifest update has been persisted. When bay is invoked from
// inside a pane being killed, tmux SIGHUPs bay shortly after the kill
// command is issued; if the manifest update were still pending at that
// moment, the user-visible state would be left inconsistent. By doing
// the manifest update first, the worst case is that bay dies between
// the manifest write and the kill — leaving the manifest correct and
// the pane briefly orphaned (next bay invocation will skip it).
func (e *Engine) closeBayState(dockName, bayID string, force bool) (closeBayResult, error) {
	return e.closeBayStateWithAdditionalClosingWindows(dockName, bayID, force, nil)
}

func (e *Engine) closeBayStateWithAdditionalClosingWindows(dockName, bayID string, force bool, additionalClosingWindowIDs []string) (closeBayResult, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return closeBayResult{}, err
	}

	dock := m.FindDock(dockName)
	if dock == nil {
		return closeBayResult{}, fmt.Errorf("unknown dock %q", dockName)
	}
	bay := dock.FindBayByID(bayID)
	if bay == nil {
		if manifest.IsReservedBayID(bayID) {
			return closeBayResult{}, nil
		}
		return closeBayResult{}, fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
	}
	if manifest.IsHomeBay(bay) {
		if err := validateHomeBayLifecycleShape(dock, bay); err != nil {
			return closeBayResult{}, err
		}
	}

	// Safety checks for worktree bays + determine if the branch
	// is safe to delete after the worktree is removed. The gate is
	// HasUnpushedCommits, not git's merge-into-default check: a
	// pushed-but-unmerged PR branch is safe because the work exists on
	// the remote, and squash-merged/cherry-picked patches are safe
	// because HasUnpushedCommits treats patch-equivalent commits on the
	// default branch as landed.
	branchSafeToDelete := false
	forceRemoveWorktree := force
	if bay.Type == manifest.BayTypeWorktree && !force {
		if _, statErr := os.Stat(bay.Path); statErr == nil {
			dirty, blockingDirty, err := e.CheckDirtyChanges(bay)
			if err != nil {
				return closeBayResult{}, fmt.Errorf("bay %q has uncommitted changes; %w (use --force to override)", bayID, err)
			}
			if blockingDirty {
				return closeBayResult{}, fmt.Errorf("bay %q has uncommitted changes (use --force to override)", bayID)
			}
			if dirty {
				forceRemoveWorktree = true
			}

			unpushed, err := e.HasUnlandedCommits(bay)
			if err != nil {
				return closeBayResult{}, fmt.Errorf("bay %q: could not verify push status: %w (use --force to override)", bayID, err)
			}
			if unpushed {
				return closeBayResult{}, fmt.Errorf("bay %q has unlanded commits (use --force to override)", bayID)
			}
			// Safety checks passed → branch work exists remotely or has landed.
			if bay.Worktree != nil && bay.Worktree.Branch != "" {
				branchSafeToDelete = true
			}
		}
	} else if force && bay.Type == manifest.BayTypeWorktree {
		// Force close: still check if the branch is pushed (best-effort)
		// so we can clean it up. Don't block the close if the check
		// fails — just skip the branch delete.
		if bay.Worktree != nil && bay.Worktree.Branch != "" {
			if _, statErr := os.Stat(bay.Path); statErr == nil {
				unpushed, err := e.HasUnlandedCommits(bay)
				if err == nil && !unpushed {
					branchSafeToDelete = true
				}
			}
		}
	}

	// Collect window IDs (deduped) the caller should kill once all
	// manifest state is persisted.
	windowIDs := windowIDsForBay(bay)

	if bay.Type == manifest.BayTypeHome {
		closingWindowIDs := append([]string{}, windowIDs...)
		closingWindowIDs = append(closingWindowIDs, additionalClosingWindowIDs...)
		dismissDock, err := e.homeCloseWouldDismissDock(dockName, closingWindowIDs)
		if err != nil {
			if !force {
				return closeBayResult{}, err
			}
			dismissDock = len(windowIDs) > 0
		}
		if dismissDock && !force {
			return closeBayResult{}, lastHomeCloseError(dockName)
		}
		if err := e.withManifest(func(m *manifest.Manifest) error {
			dock := m.FindDock(dockName)
			if dock == nil {
				return fmt.Errorf("unknown dock %q", dockName)
			}
			if dock.FindBayByID(bayID) == nil {
				return nil
			}
			if err := dock.RemoveBay(bayID); err != nil {
				return err
			}
			if dismissDock {
				dock.SessionID = ""
			}
			return nil
		}); err != nil {
			return closeBayResult{}, err
		}
		return closeBayResult{windowIDs: windowIDs, dismissDock: dismissDock}, nil
	}

	// Remove worktree from disk. This is destructive of the worktree
	// directory (and may invalidate the shell's CWD), but does not
	// kill bay's process.
	if bay.Type == manifest.BayTypeWorktree && bay.Worktree != nil && dock.Path != "" {
		repoPath := config.ExpandPath(dock.Path)
		if err := e.Git.RemoveWorktree(repoPath, bay.Path, forceRemoveWorktree); err != nil {
			if !force {
				return closeBayResult{}, fmt.Errorf("removing worktree: %w", err)
			}
		}
		// Delete the local branch now that the worktree is gone.
		// git refuses to delete a branch checked out in a worktree,
		// so this must come after RemoveWorktree.
		if branchSafeToDelete {
			_ = e.Git.DeleteBranch(repoPath, bay.Worktree.Branch)
		}
		_ = os.Remove(dock.EffectiveWorktreeDir())
	}

	// Archive the bay. Disambiguate name if it already exists
	// in the archive (same bay closed and recreated before).
	archive, err := manifest.LoadArchive(e.archivePath)
	if err != nil {
		archive = manifest.New()
	}
	archiveDock := archive.FindDock(dockName)
	if archiveDock == nil {
		_ = archive.AddDock(manifest.Dock{Name: dockName})
		archiveDock = archive.FindDock(dockName)
	}
	archived := *bay
	for i := 2; archiveDock.FindBay(archived.Name) != nil; i++ {
		archived.Name = fmt.Sprintf("%s-%d", bay.Name, i)
	}
	_ = archiveDock.AddBay(archived)
	_ = manifest.SaveArchive(e.archivePath, archive)

	// Remove from manifest atomically in case another command updated the dock.
	if err := e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		return dock.RemoveBay(bayID)
	}); err != nil {
		return closeBayResult{}, err
	}
	return closeBayResult{windowIDs: windowIDs}, nil
}

// HasUnlandedCommits reports whether committed work in bay is not safely
// recoverable from a remote branch, the default branch, or the bay's
// merged PR. The PR check handles multi-commit squash merges where per-commit
// patch comparison cannot prove that the old local stack landed.
func (e *Engine) HasUnlandedCommits(bay *manifest.Bay) (bool, error) {
	if bay == nil || manifest.IsHomeBay(bay) || bay.Worktree == nil || bay.Path == "" {
		return false, nil
	}
	unpushed, err := e.Git.HasUnpushedCommits(bay.Path)
	if err != nil {
		if e.localHeadInMergedPR(bay) {
			return false, nil
		}
		return false, err
	}
	if !unpushed {
		return false, nil
	}
	if e.localHeadInMergedPR(bay) {
		return false, nil
	}
	return true, nil
}

func (e *Engine) localHeadInMergedPR(bay *manifest.Bay) bool {
	if bay == nil || manifest.IsHomeBay(bay) || bay.Worktree == nil || bay.Worktree.PR == "" || bay.Path == "" {
		return false
	}
	landed, err := e.Git.LocalHeadInMergedPR(bay.Path, bay.Worktree.PR)
	return err == nil && landed
}

// HasBlockingDirtyChanges reports whether a worktree has uncommitted changes
// that normal close must preserve. Dirty changes are allowed only when the
// full working tree exactly matches a durable ref that bay can recover later.
func (e *Engine) HasBlockingDirtyChanges(bay *manifest.Bay) (bool, error) {
	_, blocking, err := e.CheckDirtyChanges(bay)
	return blocking, err
}

// CheckDirtyChanges reports whether a worktree is dirty, and whether those
// dirty changes should block normal close.
func (e *Engine) CheckDirtyChanges(bay *manifest.Bay) (dirty bool, blocking bool, err error) {
	if bay == nil || manifest.IsHomeBay(bay) || bay.Type != manifest.BayTypeWorktree || bay.Path == "" {
		return false, false, nil
	}
	dirty, err = e.Git.IsDirty(bay.Path)
	if err != nil {
		return false, false, fmt.Errorf("checking bay state: %w", err)
	}
	if !dirty {
		return false, false, nil
	}

	matches, _, err := e.Git.WorktreeMatchesRecoverableRef(bay.Path)
	if err != nil {
		return true, true, fmt.Errorf("could not verify changes against recoverable refs: %w", err)
	}
	return true, !matches, nil
}

// BayCleanReview clears dirty review changes only when the full worktree state
// exactly matches a durable ref. It returns the matched ref, or "" when the
// worktree was already clean.
func (e *Engine) BayCleanReview(dockName, bayID string) (string, error) {
	if manifest.IsReservedBayID(bayID) {
		return "", fmt.Errorf("home is reserved; clean-review does not apply to the home pseudo-bay")
	}
	m, err := e.LoadManifest()
	if err != nil {
		return "", err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return "", fmt.Errorf("unknown dock %q", dockName)
	}
	bay := dock.FindBayByID(bayID)
	if bay == nil {
		return "", fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
	}
	if bay.Type != manifest.BayTypeWorktree || bay.Path == "" {
		return "", fmt.Errorf("bay %q is not a worktree", bayID)
	}
	if _, statErr := os.Stat(bay.Path); statErr != nil {
		return "", fmt.Errorf("bay %q path: %w", bayID, statErr)
	}

	dirty, err := e.Git.IsDirty(bay.Path)
	if err != nil {
		return "", fmt.Errorf("checking bay state: %w", err)
	}
	if !dirty {
		return "", nil
	}

	matches, ref, err := e.Git.WorktreeMatchesRecoverableRef(bay.Path)
	if err != nil {
		return "", fmt.Errorf("could not verify changes against recoverable refs: %w", err)
	}
	if !matches {
		return "", fmt.Errorf("bay %q has uncommitted changes that do not exactly match a recoverable git ref", bayID)
	}
	if err := e.Git.DiscardWorktreeChanges(bay.Path); err != nil {
		return "", err
	}
	return ref, nil
}

// TidyResult reports what BayTidy did.
type TidyResult struct {
	DefaultBranch   string // the repo default, e.g. "main"
	DeletedBranch   string // branch the worktree was detached from and deleted; "" if none
	AlreadyDetached bool   // worktree was already on a detached HEAD; nothing to do
}

// BayTidy returns a bay's worktree to a clean detached HEAD at
// origin/<default> and deletes its now-idle local branch — the inverse of
// the detached base BayNew seeds a worktree with. It is the post-merge
// reset: because it detaches at the remote ref rather than checking out the
// default branch, it never collides with the dock root that holds that
// branch.
//
// It refuses to run when the worktree is dirty or carries commits that are
// neither pushed nor merged, so no work is discarded. A worktree that is
// already detached is a no-op.
// resetAutoDescription blanks a backstop-generated description so a
// reused bay starts fresh; a user-set description is left untouched (the
// no-clobber invariant). Returns whether it changed anything.
func resetAutoDescription(bay *manifest.Bay) bool {
	if bay == nil || bay.DescriptionSource != manifest.DescriptionSourceAuto {
		return false
	}
	bay.Description = ""
	bay.DescriptionSource = ""
	bay.DescriptionSummarizedAt = 0
	bay.DescriptionInputHash = ""
	bay.DescriptionStableStreak = 0
	return true
}

func (e *Engine) BayTidy(dockName, bayID string) (TidyResult, error) {
	if manifest.IsReservedBayID(bayID) {
		return TidyResult{}, fmt.Errorf("home is reserved; tidy applies only to worktree bays")
	}
	m, err := e.LoadManifest()
	if err != nil {
		return TidyResult{}, err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return TidyResult{}, fmt.Errorf("unknown dock %q", dockName)
	}
	bay := dock.FindBayByID(bayID)
	if bay == nil {
		return TidyResult{}, fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
	}
	if bay.Type != manifest.BayTypeWorktree || bay.Path == "" {
		return TidyResult{}, fmt.Errorf("bay %q is not a worktree", bayID)
	}
	if dock.Path == "" {
		return TidyResult{}, fmt.Errorf("dock %q has no checkout path", dockName)
	}
	if _, statErr := os.Stat(bay.Path); statErr != nil {
		return TidyResult{}, fmt.Errorf("bay %q path: %w", bayID, statErr)
	}

	repoPath := config.ExpandPath(dock.Path)
	defaultBranch, err := e.Git.DefaultBranch(repoPath)
	if err != nil {
		return TidyResult{}, fmt.Errorf("finding default branch: %w", err)
	}

	// Read the live branch rather than trusting metadata — the worktree may
	// have been moved out from under bay. An empty branch means HEAD is
	// already detached, so there is nothing to tidy.
	branch, err := e.Git.CurrentBranch(bay.Path)
	if err != nil {
		return TidyResult{}, fmt.Errorf("reading current branch: %w", err)
	}
	if branch == "" {
		// Already a clean base — but still drop a stale auto description so
		// a reused detached bay doesn't keep advertising old work.
		_ = e.withManifestMaybe(func(m *manifest.Manifest) (bool, error) {
			dock := m.FindDock(dockName)
			if dock == nil {
				return false, nil
			}
			return resetAutoDescription(dock.FindBayByID(bayID)), nil
		})
		return TidyResult{DefaultBranch: defaultBranch, AlreadyDetached: true}, nil
	}

	// Never discard uncommitted work. This is intentionally stricter than
	// BayClose, which tolerates a dirty tree that matches a recoverable ref
	// (review changes) because it removes the whole worktree. Tidy keeps the
	// worktree and detaches it, so any dirt would be carried onto the new
	// base — block on any dirty state instead.
	dirty, err := e.Git.IsDirty(bay.Path)
	if err != nil {
		return TidyResult{}, fmt.Errorf("checking bay state: %w", err)
	}
	if dirty {
		return TidyResult{}, fmt.Errorf("bay %q has uncommitted changes; commit or stash before tidying", bayID)
	}
	// Never strand commits that exist only locally. HasUnlandedCommits
	// treats pushed remote branches and merged PRs as safe, so a branch
	// that landed (or is still open but pushed) passes.
	unlanded, err := e.HasUnlandedCommits(bay)
	if err != nil {
		return TidyResult{}, fmt.Errorf("bay %q: could not verify push status: %w", bayID, err)
	}
	if unlanded {
		return TidyResult{}, fmt.Errorf("bay %q has unlanded commits; push or merge the branch first", bayID)
	}

	// Refresh origin so we detach at the latest default-branch tip, the way
	// BayNew seeds a fresh worktree. Best-effort: an offline fetch failure
	// still leaves a usable, if slightly stale, base to detach onto.
	_ = e.Git.Fetch(repoPath)

	if err := e.Git.CheckoutDetach(bay.Path, "origin/"+defaultBranch); err != nil {
		return TidyResult{}, fmt.Errorf("detaching worktree: %w", err)
	}

	// The branch is no longer checked out in this worktree, so git will
	// delete it. The safety checks above proved the work is landed or
	// pushed, so the local copy is recoverable from origin if needed.
	deletedBranch := ""
	if delErr := e.Git.DeleteBranch(repoPath, branch); delErr == nil {
		deletedBranch = branch
	}

	// The bay is now a clean base for reuse. Clear stale metadata so display
	// and the next sync pass converge:
	//   - The auto-generated description (it described the now-merged task).
	//     A user-set description is left alone — the no-clobber invariant —
	//     and the auto-description backstop regenerates a fresh one once new
	//     work starts (its recency boundary is the new detached base).
	//   - The branch metadata, but only on a successful delete: if the delete
	//     failed, leaving it in place lets SyncAll retry the deletion rather
	//     than orphaning a local branch whose name we'd otherwise forget.
	_ = e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return nil
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return nil
		}
		resetAutoDescription(bay)
		if deletedBranch != "" && bay.Worktree != nil {
			bay.Worktree.Branch = ""
			bay.Worktree.PR = ""
			bay.Worktree.PRCheckedAt = 0
			bay.Worktree.Merged = false
		}
		return nil
	})

	return TidyResult{DefaultBranch: defaultBranch, DeletedBranch: deletedBranch}, nil
}

func (e *Engine) BayClose(dockName, bayID string, force bool) error {
	result, err := e.closeBayState(dockName, bayID, force)
	if err != nil {
		return err
	}
	if result.dismissDock {
		_ = e.Tmux.KillSession(dockName)
		return nil
	}
	// Manifest is saved. Now kill the windows — bay may die mid-call if
	// it's running in one of these panes, but the user-visible state is
	// already correct.
	for _, id := range result.windowIDs {
		if !manifest.IsReservedBayID(bayID) {
			e.ensureHomeIfLastWindow(dockName, id)
		}
		_ = e.Tmux.KillWindow(id)
	}

	// Re-evaluate tab name lengths now that bay count changed.
	e.refreshDockWindowNamesByName(dockName)
	return nil
}

// baySkipFunc is called for each bay during batch close. It returns a
// non-empty reason string to skip the bay, or "" to include it.
type baySkipFunc func(bay *manifest.Bay, dockName string) string

// BayCloseClean closes all clean (non-dirty, no unlanded commits) bays.
func (e *Engine) BayCloseClean(dockName string, force, dryRun bool, exclude ...string) ([]string, []string, error) {
	return e.bayCloseBatch(dockName, force, dryRun, nil, exclude...)
}

// BayCloseDone closes bays that are not dirty AND not pending (have an
// unmerged branch). Only removes bays whose work has landed or that
// have no branch at all.
func (e *Engine) BayCloseDone(dockName string, force, dryRun bool, exclude ...string) ([]string, []string, error) {
	return e.bayCloseBatch(dockName, force, dryRun, func(bay *manifest.Bay, dn string) string {
		if bay.Worktree != nil && bay.Worktree.Branch != "" && !bay.IsMerged() && !e.localHeadInMergedPR(bay) {
			return "pending"
		}
		return ""
	}, exclude...)
}

// BayCloseAll closes all persisted bays in a dock, closing home after all
// worktree/external bays. force controls worktree safety gates; forceHomeDismiss
// only bypasses the final-home dismissal confirmation after the caller has
// already confirmed it.
func (e *Engine) BayCloseAll(dockName string, force, forceHomeDismiss, dryRun bool) ([]string, []string, error) {
	return e.bayCloseAll(dockName, force, forceHomeDismiss, dryRun)
}

// BayCloseAllWouldDismissDock reports whether bay close --all would close the
// last real tmux windows in the dock and therefore dismiss the dock UI/session.
func (e *Engine) BayCloseAllWouldDismissDock(dockName string) (bool, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return false, err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return false, fmt.Errorf("unknown dock %q", dockName)
	}
	hasHome := false
	var closingWindowIDs []string
	for i := range dock.Bays {
		bay := &dock.Bays[i]
		if manifest.IsHomeBay(bay) {
			if err := validateHomeBayLifecycleShape(dock, bay); err != nil {
				return false, err
			}
			hasHome = len(bay.Surfaces) > 0
		}
		closingWindowIDs = append(closingWindowIDs, windowIDsForBay(bay)...)
	}
	if !hasHome {
		return false, nil
	}
	return e.homeCloseWouldDismissDock(dockName, closingWindowIDs)
}

// bayCloseBatch is the shared implementation for batch-close operations.
// An optional skip function can pre-filter bays before the safety
// checks in closeBayState.
//
// Manifest updates for ALL targets are persisted before any tmux kill,
// so that bay invoked from inside one of the affected panes doesn't
// leave the rest of the targets half-closed when its host pane dies.
func (e *Engine) bayCloseBatch(dockName string, force, dryRun bool, skip baySkipFunc, exclude ...string) (closed []string, skipped []string, err error) {
	m, err := e.LoadManifest()
	if err != nil {
		return nil, nil, err
	}

	excludeSet := map[string]bool{}
	for _, name := range exclude {
		excludeSet[name] = true
	}

	type target struct {
		dock string
		id   string
		name string // for error/skip labels only
	}
	var targets []target

	for i := range m.Docks {
		d := &m.Docks[i]
		if dockName != "" && d.Name != dockName {
			continue
		}
		for j := range d.Bays {
			bay := &d.Bays[j]
			if manifest.IsHomeBay(bay) {
				continue
			}
			if excludeSet[bay.ID] {
				continue
			}
			if skip != nil {
				if reason := skip(bay, d.Name); reason != "" {
					skipped = append(skipped, d.Name+":"+bay.ID+" ("+reason+")")
					continue
				}
			}
			targets = append(targets, target{dock: d.Name, id: bay.ID, name: bay.Name})
		}
	}

	if len(targets) == 0 {
		if len(skipped) > 0 {
			return nil, skipped, nil
		}
		return nil, nil, fmt.Errorf("no bays found")
	}

	if dryRun {
		// Dry-run: check dirty/unlanded status but don't mutate anything.
		for _, t := range targets {
			label := t.dock + ":" + t.id
			bay := m.FindDock(t.dock).FindBayByID(t.id)
			if bay != nil && bay.Path != "" && !force {
				if dirty, err := e.HasBlockingDirtyChanges(bay); err == nil && dirty {
					skipped = append(skipped, label+" (dirty)")
					continue
				}
				if unpushed, err := e.HasUnlandedCommits(bay); err == nil && unpushed {
					skipped = append(skipped, label+" (unlanded)")
					continue
				}
			}
			closed = append(closed, label)
		}
		return closed, skipped, nil
	}

	// First pass: do all manifest mutations, collecting window IDs to kill.
	// closeBayState performs the dirty/unlanded safety checks, so
	// dirty bays are naturally skipped (added to skipped list).
	type pendingKill struct {
		dock      string
		windowIDs []string
	}
	var pending []pendingKill
	for _, t := range targets {
		label := t.dock + ":" + t.id
		result, closeErr := e.closeBayState(t.dock, t.id, force)
		if closeErr != nil {
			skipped = append(skipped, label+" ("+closeErr.Error()+")")
			continue
		}
		closed = append(closed, label)
		pending = append(pending, pendingKill{dock: t.dock, windowIDs: result.windowIDs})
	}

	// Second pass: kill tmux windows. Safe to die at any point — every
	// closed-bay's manifest entry is already persisted.
	for _, p := range pending {
		for _, id := range p.windowIDs {
			e.ensureHomeIfLastWindow(p.dock, id)
			_ = e.Tmux.KillWindow(id)
		}
	}
	return closed, skipped, nil
}

func (e *Engine) bayCloseAll(dockName string, force, forceHomeDismiss, dryRun bool) (closed []string, skipped []string, err error) {
	m, err := e.LoadManifest()
	if err != nil {
		return nil, nil, err
	}

	type target struct {
		dock string
		id   string
		home bool
	}
	var targets []target
	var homeTargets []target

	for i := range m.Docks {
		d := &m.Docks[i]
		if dockName != "" && d.Name != dockName {
			continue
		}
		for j := range d.Bays {
			bay := &d.Bays[j]
			t := target{dock: d.Name, id: bay.ID, home: manifest.IsHomeBay(bay)}
			if manifest.IsHomeBay(bay) {
				if len(bay.Surfaces) > 0 {
					homeTargets = append(homeTargets, t)
				}
				continue
			}
			targets = append(targets, t)
		}
	}
	targets = append(targets, homeTargets...)

	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("no bays found")
	}

	if dryRun {
		nonHomeSkipped := map[string]bool{}
		for _, t := range targets {
			label := t.dock + ":" + t.id
			if t.home && nonHomeSkipped[t.dock] {
				skipped = append(skipped, label+" (non-home bay skipped)")
				continue
			}
			bay := m.FindDock(t.dock).FindBayByID(t.id)
			if bay != nil && !manifest.IsHomeBay(bay) && bay.Path != "" && !force {
				if dirty, err := e.HasBlockingDirtyChanges(bay); err == nil && dirty {
					skipped = append(skipped, label+" (dirty)")
					nonHomeSkipped[t.dock] = true
					continue
				}
				if unpushed, err := e.HasUnlandedCommits(bay); err == nil && unpushed {
					skipped = append(skipped, label+" (unlanded)")
					nonHomeSkipped[t.dock] = true
					continue
				}
			}
			closed = append(closed, label)
		}
		return closed, skipped, nil
	}

	type pendingKill struct {
		dock        string
		windowIDs   []string
		dismissDock bool
	}
	var pending []pendingKill
	closingByDock := map[string][]string{}
	nonHomeSkipped := map[string]bool{}

	for _, t := range targets {
		label := t.dock + ":" + t.id
		if t.home && nonHomeSkipped[t.dock] {
			skipped = append(skipped, label+" (non-home bay skipped)")
			continue
		}
		closeForce := force
		var additionalClosing []string
		if t.home {
			closeForce = force || forceHomeDismiss
			additionalClosing = closingByDock[t.dock]
		}
		result, closeErr := e.closeBayStateWithAdditionalClosingWindows(t.dock, t.id, closeForce, additionalClosing)
		if closeErr != nil {
			skipped = append(skipped, label+" ("+closeErr.Error()+")")
			if !t.home {
				nonHomeSkipped[t.dock] = true
			}
			continue
		}
		closed = append(closed, label)
		pending = append(pending, pendingKill{dock: t.dock, windowIDs: result.windowIDs, dismissDock: result.dismissDock})
		closingByDock[t.dock] = append(closingByDock[t.dock], result.windowIDs...)
	}

	currentWindowID, _ := e.Tmux.CurrentWindowID()
	dismissDocks := map[string]bool{}
	for _, p := range pending {
		if p.dismissDock {
			dismissDocks[p.dock] = true
		}
	}
	killedSessions := map[string]bool{}
	type delayedKill struct {
		dock     string
		windowID string
	}
	var currentKills []delayedKill
	for _, p := range pending {
		if dismissDocks[p.dock] {
			if !killedSessions[p.dock] {
				_ = e.Tmux.KillSession(p.dock)
				killedSessions[p.dock] = true
			}
			continue
		}
		for _, id := range p.windowIDs {
			if id == currentWindowID {
				currentKills = append(currentKills, delayedKill{dock: p.dock, windowID: id})
				continue
			}
			_ = e.Tmux.KillWindow(id)
		}
	}
	for _, k := range currentKills {
		if dismissDocks[k.dock] {
			continue
		}
		_ = e.Tmux.KillWindow(k.windowID)
	}
	return closed, skipped, nil
}

// BayUpdate updates bay metadata (branch, PR).
func (e *Engine) BayUpdate(dockName, bayID string, branch, pr *string) error {
	if manifest.IsReservedBayID(bayID) {
		return fmt.Errorf("home is reserved; branch/PR metadata does not apply to the home pseudo-bay")
	}
	return e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
		}

		bay.LastActive = time.Now().Unix()
		nameChanged := false

		if branch != nil && bay.Worktree != nil {
			bay.Worktree.Branch = *branch
			if isPlaceholderName(bay.Name) {
				bay.Name = uniqueBayName(dock, bay, abbreviateBranch(*branch))
				nameChanged = true
			}
		}
		if pr != nil && bay.Worktree != nil {
			bay.Worktree.PR = *pr
		}

		if nameChanged {
			e.updateWindowNames(bay, "")
		}

		return nil
	})
}

// BayDescribe sets (or clears, if desc is "") a bay's description.
// Descriptions appear in the bay picker and in ls/tree output; they
// have no effect on tmux tab names, which stay short by design.
func (e *Engine) BayDescribe(dockName, bayID, desc string) error {
	if manifest.IsReservedBayID(bayID) {
		return fmt.Errorf("home is reserved; describe is not supported on the home pseudo-bay")
	}
	desc = strings.TrimSpace(desc)
	if err := ValidateDescription(desc); err != nil {
		return err
	}
	return e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
		}
		bay.Description = desc
		if desc == "" {
			// Cleared: hand the bay back to the auto-description backstop
			// and force it to regenerate from scratch.
			bay.DescriptionSource = ""
			bay.DescriptionSummarizedAt = 0
			bay.DescriptionInputHash = ""
			bay.DescriptionStableStreak = 0
		} else {
			// Explicit description (by the user or an agent) is owned and
			// never auto-overwritten.
			bay.DescriptionSource = manifest.DescriptionSourceUser
		}
		bay.LastActive = time.Now().Unix()
		return nil
	})
}

// BayRename renames a bay.
func (e *Engine) BayRename(dockName, bayID, newName string) error {
	if manifest.IsReservedBayID(bayID) {
		return fmt.Errorf("home is reserved; rename is not supported on the home pseudo-bay")
	}
	if err := ValidateBayName(newName); err != nil {
		return err
	}

	return e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
		}

		// Check uniqueness.
		if existing := dock.FindBay(newName); existing != nil {
			return fmt.Errorf("name %q already in use", newName)
		}

		bay.Name = newName
		bay.LastActive = time.Now().Unix()
		e.updateWindowNames(bay, "")

		return nil
	})
}

// BayShow returns detailed information about a bay.
// This is a read-only manifest query — it does NOT call SyncAll.
// Callers that display data to the user (bay show, bay sf ls)
// should call SyncAll first. Callers that just need bay state
// for an operation (navigation, close, restart) can skip the sync.
func (e *Engine) BayShow(dockName, bayID string) (*manifest.Bay, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return nil, err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return nil, fmt.Errorf("unknown dock %q", dockName)
	}
	bay := dock.FindBayByID(bayID)
	if bay == nil {
		return nil, fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
	}
	if err := validatePersistedHomeBayShape(dock, bay); err != nil {
		return nil, err
	}
	return bay, nil
}

// MarkPRCheckStale resets PRCheckedAt for a bay so the monitor's
// next sync tick re-queries gh, bypassing the TTL. Used as a
// user-interest signal: when someone reads bay info and the PR
// is missing, that's a signal they expect to see one soon, so push
// bay to re-check before the 5-minute TTL elapses.
//
// No-op when the PR is already populated or the branch is empty — those
// states have no useful signal to act on.
func (e *Engine) MarkPRCheckStale(dockName, bayID string) error {
	return e.withManifestMaybe(func(m *manifest.Manifest) (bool, error) {
		dock := m.FindDock(dockName)
		if dock == nil {
			return false, nil
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return false, nil
		}
		return clearPRCheckedAt(bay), nil
	})
}

// MarkAllPRChecksStale resets PRCheckedAt for every bay that has a
// branch but no PR, in a single locked manifest update. Used by `bay tree`
// to nudge the monitor without paying N file-lock cycles.
func (e *Engine) MarkAllPRChecksStale() error {
	return e.withManifestMaybe(func(m *manifest.Manifest) (bool, error) {
		changed := false
		for i := range m.Docks {
			dock := &m.Docks[i]
			for j := range dock.Bays {
				if clearPRCheckedAt(&dock.Bays[j]) {
					changed = true
				}
			}
		}
		return changed, nil
	})
}

func clearPRCheckedAt(bay *manifest.Bay) bool {
	if manifest.IsHomeBay(bay) || bay.Worktree == nil {
		return false
	}
	if bay.Worktree.Branch == "" || bay.Worktree.PR != "" {
		return false
	}
	if bay.Worktree.PRCheckedAt == 0 {
		return false
	}
	bay.Worktree.PRCheckedAt = 0
	return true
}

// ResolveBay resolves a bay query to (dockName, bayID).
// Accepts: "dock:id" or bare ID. Names are not accepted; if the query
// matches a Name, the returned error includes a "did you mean" hint
// at the canonical ID.
func (e *Engine) ResolveBay(query string) (string, string, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return "", "", err
	}
	bay, dock, resolveErr := m.ResolveBay(query)
	if resolveErr != nil {
		return "", "", resolveErr
	}
	return dock.Name, bay.ID, nil
}

// ResolveSelf resolves the current bay from CWD and tmux context.
func (e *Engine) ResolveSelf() (string, string, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return "", "", err
	}

	currentSession, _ := e.Tmux.CurrentSession()
	winID, tmuxErr := e.Tmux.CurrentWindowID()
	paneID, _ := e.Tmux.CurrentPaneID()
	if match, ok, err := currentTmuxHomeMatch(m, currentSession, winID, paneID); err != nil {
		return "", "", err
	} else if ok {
		return match.Dock.Name, match.Bay.ID, nil
	}

	// First try: match CWD against bay paths. IsPathUnder is
	// symlink-safe — needed on macOS where /tmp → /private/tmp etc.
	cwd, cwdErr := os.Getwd()
	if cwdErr == nil {
		for i := range m.Docks {
			dock := &m.Docks[i]
			for j := range dock.Bays {
				bay := &dock.Bays[j]
				if manifest.IsHomeBay(bay) {
					continue
				}
				if config.IsPathUnder(cwd, bay.Path) {
					return dock.Name, bay.ID, nil
				}
			}
		}
	}

	// Fallback: match current tmux window ID against surfaces.
	if tmuxErr == nil {
		for i := range m.Docks {
			dock := &m.Docks[i]
			for j := range dock.Bays {
				bay := &dock.Bays[j]
				for _, s := range bay.Surfaces {
					if s.Tmux != nil && s.Tmux.WindowID == winID {
						return dock.Name, bay.ID, nil
					}
				}
			}
		}
	}

	return "", "", fmt.Errorf("not in a bay")
}

// ResolveByWindowID finds the bay that owns the given tmux window ID.
func (e *Engine) ResolveByWindowID(tmuxWindowID string) (dockName, bayID string, bay *manifest.Bay, err error) {
	m, err := e.LoadManifest()
	if err != nil {
		return "", "", nil, err
	}
	for i := range m.Docks {
		dock := &m.Docks[i]
		for j := range dock.Bays {
			w := &dock.Bays[j]
			for _, s := range w.Surfaces {
				if s.Tmux != nil && s.Tmux.WindowID == tmuxWindowID {
					return dock.Name, w.ID, w, nil
				}
			}
		}
	}
	return "", "", nil, fmt.Errorf("no bay found for tmux window %s", tmuxWindowID)
}

// BayDirTag returns the path-derived display tag for a bay.
// For worktree bays this is usually the stable worktree directory
// basename (b1, b2, ...; legacy bays may still be w<N>).
func BayDirTag(bay *manifest.Bay) string {
	if bay == nil || bay.Path == "" {
		return ""
	}
	clean := filepath.Clean(bay.Path)
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

// BayCompactLabel returns the ordinary tmux/status display label for a
// bay, preserving the path-derived dir tag when the bay has a
// distinct semantic name.
func BayCompactLabel(bay *manifest.Bay) string {
	if bay == nil {
		return ""
	}
	if manifest.IsHomeBay(bay) {
		return manifest.HomeBayID
	}
	dirTag := BayDirTag(bay)
	if dirTag == "" {
		return bay.Name
	}
	if bay.Name == "" || bay.Name == dirTag {
		return dirTag
	}
	return dirTag + "." + bay.Name
}

// TruncateBayCompactLabel crops a compact bay label to maxLen
// bytes, preserving the dir tag before the semantic name.
func TruncateBayCompactLabel(bay *manifest.Bay, maxLen int) string {
	return TruncateBayCompactLabelWithSiblings(bay, maxLen, "")
}

// TruncateBayCompactLabelWithSiblings is like TruncateBayCompactLabel
// but additionally aware of a common hyphen-token prefix shared by sibling
// bay names in the same dock. When truncation is needed and bay.Name
// starts with commonPrefix + "-", the common prefix is replaced with a single
// leading "…" so the unique tail of the name has more room. commonPrefix
// should not include a trailing hyphen and is ignored when empty.
func TruncateBayCompactLabelWithSiblings(bay *manifest.Bay, maxLen int, commonPrefix string) string {
	label := BayCompactLabel(bay)
	if maxLen <= 0 || label == "" {
		return ""
	}
	if len(label) <= maxLen {
		return label
	}

	dirTag := BayDirTag(bay)
	if dirTag == "" || bay == nil || bay.Name == "" || bay.Name == dirTag {
		return TruncateName(label, maxLen)
	}

	prefix := dirTag + "."
	if maxLen < len(prefix)+2 {
		if len(dirTag) <= maxLen {
			return dirTag
		}
		return TruncateName(dirTag, maxLen)
	}
	nameBudget := maxLen - len(prefix)
	if stripped, ok := stripCommonPrefix(bay.Name, commonPrefix, nameBudget); ok {
		return prefix + stripped
	}
	if len(bay.Name) <= nameBudget {
		return prefix + bay.Name
	}
	return prefix + bay.Name[:nameBudget]
}

// stripCommonPrefix returns name with commonPrefix+"-" replaced by a leading
// "…" (and a trailing "…" if the tail still overflows nameBudget cells).
// Returns ok=false when no prefix applies or the budget is too tight.
//
// Budget is in tmux cells; truncTabEllipsis is 1 cell but 3 bytes, so the
// returned string's byte length may exceed nameBudget. That's fine — tmux
// measures cells. Tail-byte math assumes ASCII (true for branch-derived
// names).
func stripCommonPrefix(name, commonPrefix string, nameBudget int) (string, bool) {
	if commonPrefix == "" || nameBudget < 2 {
		return "", false
	}
	strip := commonPrefix + "-"
	if !strings.HasPrefix(name, strip) || len(name) <= len(strip) {
		return "", false
	}
	tail := name[len(strip):]
	if 1+len(tail) <= nameBudget {
		return truncTabEllipsis + tail, true
	}
	tailBudget := nameBudget - 2 // leading + trailing ellipsis
	if tailBudget < 1 {
		return "", false
	}
	return truncTabEllipsis + tail[:tailBudget] + truncTabEllipsis, true
}

// commonHyphenPrefixes returns, for each name, the longest hyphen-token prefix
// it shares with at least one sibling name. Each prefix leaves at least one
// token in every matched name after stripping.
func commonHyphenPrefixes(names []string) []string {
	prefixes := make([]string, len(names))
	tokenLists := make([][]string, len(names))
	for i, n := range names {
		if n != "" {
			tokenLists[i] = strings.Split(n, "-")
		}
	}
	for i, tokens := range tokenLists {
		for prefixLen := len(tokens) - 1; prefixLen >= 1; prefixLen-- {
			prefix, ok := hyphenTokenPrefix(tokens, prefixLen)
			if !ok {
				continue
			}
			matches := 0
			for _, other := range tokenLists {
				if hasHyphenTokenPrefix(other, tokens, prefixLen) {
					matches++
				}
			}
			if matches >= 2 {
				prefixes[i] = prefix
				break
			}
		}
	}
	return prefixes
}

func hyphenTokenPrefix(tokens []string, prefixLen int) (string, bool) {
	if prefixLen <= 0 || len(tokens) <= prefixLen {
		return "", false
	}
	for i := 0; i < prefixLen; i++ {
		if tokens[i] == "" {
			return "", false
		}
	}
	return strings.Join(tokens[:prefixLen], "-"), true
}

func hasHyphenTokenPrefix(tokens, prefix []string, prefixLen int) bool {
	if len(tokens) <= prefixLen {
		return false
	}
	for i := 0; i < prefixLen; i++ {
		if tokens[i] != prefix[i] {
			return false
		}
	}
	return true
}

func bayWindowLabel(bay *manifest.Bay) string {
	label := BayCompactLabel(bay)
	if label != "" {
		return label
	}
	if bay != nil {
		return bay.ID
	}
	return ""
}

// updateWindowNames renames all tmux windows for a bay's surfaces.
// Primary windows (layout group 1) get the bay compact label; secondary
// windows get ":surfacename" where surfacename is the first surface in the
// group.
//
// When primaryLabel is empty and no compact label is available, falls back to
// the bay's ID so the tab still has a stable label.
func (e *Engine) updateWindowNames(bay *manifest.Bay, primaryLabel string) {
	if primaryLabel == "" {
		primaryLabel = bayWindowLabel(bay)
	}
	// Build a map of layout group → first surface name (by slice order).
	firstInGroup := map[int]string{}
	for _, s := range bay.Surfaces {
		if s.Tmux != nil && s.Tmux.LayoutGroup > 0 {
			if _, ok := firstInGroup[s.Tmux.LayoutGroup]; !ok {
				firstInGroup[s.Tmux.LayoutGroup] = s.Name
			}
		}
	}

	seen := map[string]bool{}
	for _, s := range bay.Surfaces {
		if s.Tmux != nil && s.Tmux.WindowID != "" && !seen[s.Tmux.WindowID] {
			if s.Tmux.LayoutGroup <= 1 {
				_ = e.Tmux.RenameWindow(s.Tmux.WindowID, primaryLabel)
			} else if surfName := firstInGroup[s.Tmux.LayoutGroup]; surfName != "" {
				_ = e.Tmux.RenameWindow(s.Tmux.WindowID, ":"+surfName)
			}
			seen[s.Tmux.WindowID] = true
		}
	}
}

// Tab-name truncation tuning.
const (
	// Fallback cells reserved for status-left + status-right when tmux can't
	// report the actual values. Real tmux reports status-left-length +
	// status-right-length, which is far more accurate than a fixed estimate.
	tabStatusBarOverheadFallback = 50
	// Per-tab overhead for window index, colon separator, and one space
	// between tabs — e.g. "1:foo " is 2 chars of overhead beyond the name.
	// Set slightly higher to leave a little safety margin.
	tabPerTabOverhead = 4
	// Floor on truncation — below this, names become unreadable.
	tabMinNameLen = 3
	// Ceiling — longer names are rare and waste status-bar space.
	tabMaxNameLen = 20
)

// refreshDockWindowNames recomputes the max tab name length for a dock based
// on the terminal width, status-left/right lengths, and bay count, then
// renames all windows. This keeps tab names maximally informative without
// overflowing the status bar.
//
// If no tmux client is attached (ClientWidth errors or returns <= 0), the
// full name is used without truncation — we'd rather over-run the status bar
// when the user next attaches than clobber all names to 3 chars in CI / bay
// invocations from outside tmux.
func (e *Engine) refreshDockWindowNames(dock *manifest.Dock) {
	clientWidth, err := e.Tmux.ClientWidth()
	truncate := err == nil && clientWidth > 0
	maxLen := 0
	if truncate {
		reserved, rerr := e.Tmux.StatusReservedCells()
		if rerr != nil || reserved <= 0 {
			reserved = tabStatusBarOverheadFallback
		}
		maxLen = maxTabNameLen(clientWidth, reserved, len(dock.Bays))
	}
	var commonPrefixes []string
	if truncate {
		names := make([]string, len(dock.Bays))
		for i := range dock.Bays {
			names[i] = dock.Bays[i].Name
		}
		commonPrefixes = commonHyphenPrefixes(names)
	}
	for i := range dock.Bays {
		bay := &dock.Bays[i]
		name := BayCompactLabel(bay)
		if truncate {
			name = TruncateBayCompactLabelWithSiblings(bay, maxLen, commonPrefixes[i])
		}
		e.updateWindowNames(bay, name)
	}
}

// refreshDockWindowNamesByName loads the manifest, finds the named dock, and
// refreshes its tab names. Silently no-ops on errors (tab-name refresh is
// decorative — don't fail the calling operation for it).
func (e *Engine) refreshDockWindowNamesByName(dockName string) {
	m, err := e.LoadManifest()
	if err != nil {
		return
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return
	}
	e.refreshDockWindowNames(dock)
}

// maxTabNameLen computes the maximum tab name length given the terminal width,
// the cells reserved for status-left + status-right, and the bay count.
func maxTabNameLen(clientWidth, reservedCells, bayCount int) int {
	if bayCount <= 0 {
		return tabMaxNameLen
	}
	available := clientWidth - reservedCells
	if available < 0 {
		available = 0
	}
	maxLen := available/bayCount - tabPerTabOverhead
	if maxLen < tabMinNameLen {
		maxLen = tabMinNameLen
	}
	if maxLen > tabMaxNameLen {
		maxLen = tabMaxNameLen
	}
	return maxLen
}

// TruncateName truncates a display name to maxLen, appending ".." if shortened.
// Used by status-line formatting (where byte-length math matters because the
// output is composed by overhead-tracking code expecting ASCII).
func TruncateName(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 2 {
		return s[:maxLen]
	}
	return s[:maxLen-2] + ".."
}

// truncTabEllipsis is the single-cell, single-rune ellipsis used in tab names.
// Reclaims a cell of name space vs. ".." — important when budgets are tight.
const truncTabEllipsis = "…"

// TruncateTabName is like TruncateName but uses a single-cell "…" ellipsis
// and counts in runes. Intended for tmux tab names where tight budgets make
// every cell count and tmux renders cell-based.
func TruncateTabName(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-1]) + truncTabEllipsis
}

// SetLastFocused records which surface was last focused in a bay and
// bumps LastActive. The activity bump signals to the monitor's activity gate
// that this bay is in active use, so its repo gets fetched on the next
// merge-detection cycle.
func (e *Engine) SetLastFocused(dockName, bayID string, surfaceID int) error {
	return e.withManifestMaybe(func(m *manifest.Manifest) (bool, error) {
		dock := m.FindDock(dockName)
		if dock == nil {
			return false, fmt.Errorf("unknown dock %q", dockName)
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return false, fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
		}
		now := time.Now().Unix()
		if bay.LastFocused == surfaceID && bay.LastActive == now {
			return false, nil // no change
		}
		bay.LastFocused = surfaceID
		bay.LastActive = now
		return true, nil
	})
}

const worktreeIncludeName = ".worktreeinclude"

// resolveWorktreeInclude expands .worktreeinclude patterns, prints refusals
// to stderr (tracked matches, or matches not covered by .gitignore), and
// returns the repo-root-relative paths that should be copied.
//
// A missing .worktreeinclude yields an empty list with no error. This step
// depends only on repoRoot, so callers syncing many worktrees should call
// it once and pass the result into each copyWorktreeIncludeFiles call.
func (e *Engine) resolveWorktreeInclude(repoRoot string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(repoRoot, worktreeIncludeName)); os.IsNotExist(err) {
		return nil, nil
	}

	tracked, untracked, err := e.Git.ExpandExcludes(repoRoot, worktreeIncludeName)
	if err != nil {
		return nil, fmt.Errorf("expanding %s: %w", worktreeIncludeName, err)
	}

	var notIgnored, toCopy []string
	for _, rel := range untracked {
		ignored, err := e.Git.IsIgnored(repoRoot, rel)
		if err != nil {
			return nil, fmt.Errorf("checking ignore status of %s: %w", rel, err)
		}
		if !ignored {
			notIgnored = append(notIgnored, rel)
			continue
		}
		toCopy = append(toCopy, rel)
	}

	reportRefusal("refusing to copy tracked file(s) — remove the pattern, or untrack and .gitignore", tracked)
	reportRefusal("refusing to copy file(s) not covered by .gitignore — add them to .gitignore first, or remove the pattern", notIgnored)

	return toCopy, nil
}

func reportRefusal(reason string, files []string) {
	if len(files) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "%s: %s:\n", worktreeIncludeName, reason)
	for _, f := range files {
		fmt.Fprintf(os.Stderr, "  - %s\n", f)
	}
}

// copyWorktreeIncludeFiles copies files (repo-root-relative paths produced
// by resolveWorktreeInclude) from repoRoot into worktreePath, preserving
// file mode.
func copyWorktreeIncludeFiles(repoRoot, worktreePath string, files []string) error {
	for _, rel := range files {
		src := filepath.Join(repoRoot, rel)
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("stat %s: %w", rel, err)
		}
		srcData, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		dst := filepath.Join(worktreePath, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, srcData, info.Mode()); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
	}
	return nil
}

// nextBayDir returns the next sequential directory basename (b1, b2,
// ...) that is unclaimed — neither present on disk under wtDir nor recorded
// as the basename of any bay's Path in the dock. Worktree directory
// names are intentionally decoupled from bay names so that renaming
// or repurposing a bay doesn't leave a stale dirname on disk.
func nextBayDir(wtDir string, dock *manifest.Dock) string {
	claimed := map[string]bool{}
	if dock != nil {
		for _, bay := range dock.Bays {
			if bay.Path != "" {
				claimed[filepath.Base(bay.Path)] = true
			}
		}
	}
	for i := 1; ; i++ {
		name := fmt.Sprintf("%s%d", manifest.CurrentBayIDPrefix, i)
		if claimed[name] {
			continue
		}
		if _, err := os.Stat(filepath.Join(wtDir, name)); err == nil {
			continue
		}
		return name
	}
}

// positionNewWindow moves a newly created window so that tmux tab order
// matches manifest order. For a new bay (bayID==""), the window goes
// after the last window of the last existing bay. For a new surface in
// an existing bay, it goes after the last window of that bay.
// If m is nil the manifest is loaded; callers with a pre-loaded manifest
// can pass it to avoid a second read.
func (e *Engine) positionNewWindow(dockName, windowID, bayID string, m *manifest.Manifest) {
	if m == nil {
		var err error
		m, err = e.LoadManifest()
		if err != nil {
			return
		}
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return
	}

	var afterID string
	if bayID == "" {
		// New bay: goes after the last window of the last existing bay.
		for i := len(dock.Bays) - 1; i >= 0; i-- {
			if manifest.IsHomeBay(&dock.Bays[i]) {
				continue
			}
			if id := lastWindowIDInBay(dock, dock.Bays[i].ID); id != "" {
				afterID = id
				break
			}
		}
	} else {
		// New surface: goes after the last window of this bay,
		// falling back to the last window of the previous bay.
		afterID = lastWindowIDInBay(dock, bayID)
		if afterID == "" {
			afterID = lastWindowIDBeforeBay(dock, bayID)
		}
	}

	if afterID != "" && afterID != windowID {
		_ = e.Tmux.MoveWindowAfter(windowID, afterID)
	}
}

// lastWindowIDBeforeBay returns the tmux window ID that a new window
// for the given bay should be placed after, based on manifest order.
// It walks backwards through bays (and surfaces within the target
// bay) to find the nearest existing window. Returns "" if none found.
func lastWindowIDBeforeBay(dock *manifest.Dock, bayID string) string {
	bayIdx := -1
	for i, bay := range dock.Bays {
		if bay.ID == bayID {
			bayIdx = i
			break
		}
	}
	if bayIdx == -1 {
		return ""
	}
	// Walk backwards from the previous bay to find any existing window.
	for i := bayIdx - 1; i >= 0; i-- {
		for j := len(dock.Bays[i].Surfaces) - 1; j >= 0; j-- {
			s := dock.Bays[i].Surfaces[j]
			if s.Tmux != nil && s.Tmux.WindowID != "" {
				return s.Tmux.WindowID
			}
		}
	}
	return ""
}

// lastWindowIDInBay returns the last tmux window ID among the surfaces
// of the given bay. Returns "" if the bay has no windows.
func lastWindowIDInBay(dock *manifest.Dock, bayID string) string {
	bay := dock.FindBayByID(bayID)
	if bay == nil {
		return ""
	}
	for i := len(bay.Surfaces) - 1; i >= 0; i-- {
		if bay.Surfaces[i].Tmux != nil && bay.Surfaces[i].Tmux.WindowID != "" {
			return bay.Surfaces[i].Tmux.WindowID
		}
	}
	return ""
}

func findBayByPath(dock *manifest.Dock, path string) *manifest.Bay {
	for i := range dock.Bays {
		if dock.Bays[i].Path == path {
			return &dock.Bays[i]
		}
	}
	return nil
}
