package engine

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/commonfabric/bay/internal/git"
	"github.com/commonfabric/bay/internal/manifest"
)

// seedWorktreeBay adds a worktree-type bay with a branch set to
// the manifest, ensures its path exists on disk (so the sync doesn't bail
// early on os.Stat), and returns the bay path.
func seedWorktreeBay(t *testing.T, eng *Engine, dockName, bayName, branch string) string {
	t.Helper()
	bayPath := filepath.Join(t.TempDir(), bayName)
	if err := os.MkdirAll(bayPath, 0o755); err != nil {
		t.Fatalf("mkdir bay path: %v", err)
	}
	// Set the mock branch so sync doesn't see a phantom detach.
	eng.Git.(*git.Mock).SetBranch(bayPath, branch)
	err := eng.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		dock.Bays = append(dock.Bays, manifest.Bay{
			Name: bayName,
			Type: manifest.BayTypeWorktree,
			Path: bayPath,
			Worktree: &manifest.WorktreeAttrs{
				Repo:   "labs",
				Branch: branch,
			},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("seed bay: %v", err)
	}
	return bayPath
}

// --- syncBayPR ---

func TestSyncAll_PopulatesPRWhenFound(t *testing.T) {
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/login")

	mockGit := eng.Git.(*git.Mock)
	mockGit.SetPR(bayPath, "feature/login", "42")

	eng.SyncAll()

	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree.PR != "42" {
		t.Errorf("PR = %q, want 42", bay.Worktree.PR)
	}
	if bay.Worktree.PRCheckedAt == 0 {
		t.Error("PRCheckedAt should be set after successful lookup")
	}
}

func TestSyncAll_MarksCheckedWhenNoPRFound(t *testing.T) {
	eng, _ := testEngine(t)
	_ = seedWorktreeBay(t, eng, "labs", "w1", "feature/no-pr")

	// Mock returns "" (no PR) with nil error — the definitive "no PR" case.
	eng.SyncAll()

	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree.PR != "" {
		t.Errorf("PR = %q, want empty", bay.Worktree.PR)
	}
	if bay.Worktree.PRCheckedAt == 0 {
		t.Error("PRCheckedAt should be set after definitive 'no PR' answer")
	}
}

func TestSyncAll_DoesNotRecheckRecentlyCheckedBay(t *testing.T) {
	eng, _ := testEngine(t)
	_ = seedWorktreeBay(t, eng, "labs", "w1", "feature/no-pr")

	// First sync — sets PRCheckedAt with empty PR.
	eng.SyncAll()

	mockGit := eng.Git.(*git.Mock)
	before := len(mockGit.Calls("PRForBranch"))

	// Second sync — should NOT call PRForBranch again for this bay.
	eng.SyncAll()

	if after := len(mockGit.Calls("PRForBranch")); after != before {
		t.Errorf("second SyncAll should not call PRForBranch on already-checked bay: before=%d, after=%d", before, after)
	}
}

func TestSyncAll_DoesNotRecheckBayWithPRSet(t *testing.T) {
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/login")

	// Pre-set PR on the bay (simulates an existing manifest).
	_ = eng.withManifest(func(m *manifest.Manifest) error {
		bay := m.FindDock("labs").FindBayByID("w1")
		bay.Worktree.PR = "99"
		return nil
	})

	mockGit := eng.Git.(*git.Mock)
	mockGit.SetPR(bayPath, "feature/login", "42") // different from cached
	before := len(mockGit.Calls("PRForBranch"))

	eng.SyncAll()

	if after := len(mockGit.Calls("PRForBranch")); after != before {
		t.Errorf("SyncAll should not re-query PR when one is already cached: before=%d, after=%d", before, after)
	}

	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree.PR != "99" {
		t.Errorf("PR = %q, want 99 (cached value should stick)", bay.Worktree.PR)
	}
}

func TestSyncAll_RechecksAfterTTLExpires(t *testing.T) {
	// The "stale no PR" bug: after a definitive "no PR" answer, opening a PR
	// later should be picked up. We simulate this by aging the PRCheckedAt
	// timestamp past PRCheckTTL.
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/late-pr")

	// First sync — mock has no PR for the branch, so the result is empty.
	eng.SyncAll()

	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree.PR != "" {
		t.Fatalf("PR = %q, want empty after first sync", bay.Worktree.PR)
	}
	if bay.Worktree.PRCheckedAt == 0 {
		t.Fatal("PRCheckedAt should be set after first sync")
	}

	// Now: a PR is opened, and the cached PRCheckedAt is aged past the TTL.
	mockGit := eng.Git.(*git.Mock)
	mockGit.SetPR(bayPath, "feature/late-pr", "501")
	_ = eng.withManifest(func(m *manifest.Manifest) error {
		bay := m.FindDock("labs").FindBayByID("w1")
		bay.Worktree.PRCheckedAt = 1 // ancient
		return nil
	})

	eng.SyncAll()

	m, _ = eng.LoadManifest()
	bay = m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree.PR != "501" {
		t.Errorf("PR = %q, want 501 after TTL expiry + re-sync", bay.Worktree.PR)
	}
}

func TestSyncAll_BranchChangeInvalidatesCachedPR(t *testing.T) {
	// Regression: when a bay's branch changes (detected by sync from
	// the actual git state), the cached PR — which belongs to the OLD
	// branch — must be invalidated and re-queried for the new branch.
	// Otherwise users see the wrong PR after switching branches.
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/old")

	mockGit := eng.Git.(*git.Mock)
	mockGit.SetPR(bayPath, "feature/old", "100")

	// First sync — caches PR=100 for the old branch.
	eng.SyncAll()
	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree.PR != "100" {
		t.Fatalf("first sync: PR = %q, want 100", bay.Worktree.PR)
	}

	// Now: branch changes on disk to feature/new. The new branch has its
	// own PR #200. Crucially, PRCheckedAt is recent — the old TTL-based
	// logic would have skipped the re-query.
	mockGit.SetBranch(bayPath, "feature/new")
	mockGit.SetPR(bayPath, "feature/new", "200")

	eng.SyncAll()

	m, _ = eng.LoadManifest()
	// Bay got renamed by abbreviateBranch.
	bay = m.FindDock("labs").FindBay("new")
	if bay == nil {
		t.Fatalf("bay 'new' missing after rename")
	}
	if bay.Worktree.Branch != "feature/new" {
		t.Errorf("Branch = %q, want feature/new", bay.Worktree.Branch)
	}
	if bay.Worktree.PR != "200" {
		t.Errorf("PR = %q, want 200 (new branch's PR, not the cached old one)", bay.Worktree.PR)
	}
}

func TestSyncAll_BranchChangeClearsCacheWhenGHFails(t *testing.T) {
	// Belt-and-braces: even if the new gh query fails (no PR found), the
	// cached PR for the old branch must NOT remain. Otherwise users see
	// the old PR next to the new branch — strictly worse than no PR at all.
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/old")

	mockGit := eng.Git.(*git.Mock)
	mockGit.SetPR(bayPath, "feature/old", "100")

	// First sync — caches PR=100.
	eng.SyncAll()

	// Branch changes; new branch has no PR (mock default).
	mockGit.SetBranch(bayPath, "feature/new")

	eng.SyncAll()

	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBay("new")
	if bay == nil {
		t.Fatalf("bay 'new' missing after rename")
	}
	if bay.Worktree.PR != "" {
		t.Errorf("PR = %q, want empty (new branch has no PR; old cached value should not persist)", bay.Worktree.PR)
	}
}

func TestSyncAll_ParallelChecksMultipleBays(t *testing.T) {
	eng, _ := testEngine(t)
	mockGit := eng.Git.(*git.Mock)

	// Create N bays with branches and configure mock PR numbers.
	const n = 5
	for i := 0; i < n; i++ {
		bayName := "w" + strconv.Itoa(i+1)
		branch := "feature/branch-" + strconv.Itoa(i+1)
		bayPath := seedWorktreeBay(t, eng, "labs", bayName, branch)
		mockGit.SetPR(bayPath, branch, "10"+strconv.Itoa(i))
	}

	eng.SyncAll()

	// All N bays should have PR populated and PRCheckedAt set.
	m, _ := eng.LoadManifest()
	for i := 0; i < n; i++ {
		bayName := "w" + strconv.Itoa(i+1)
		bay := m.FindDock("labs").FindBay(bayName)
		if bay == nil {
			t.Errorf("bay %q missing", bayName)
			continue
		}
		wantPR := "10" + strconv.Itoa(i)
		if bay.Worktree.PR != wantPR {
			t.Errorf("%s: PR = %q, want %q", bayName, bay.Worktree.PR, wantPR)
		}
		if bay.Worktree.PRCheckedAt == 0 {
			t.Errorf("%s: PRCheckedAt should be set", bayName)
		}
	}

	// Each bay should have been queried exactly once.
	if got := len(mockGit.Calls("PRForBranch")); got != n {
		t.Errorf("PRForBranch call count = %d, want %d", got, n)
	}
}

// --- syncBayMergeStatus ---

func TestSyncAll_SetsMergedOnMergedBranch(t *testing.T) {
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/login")

	mockGit := eng.Git.(*git.Mock)
	mockGit.SetDefaultBranch(bayPath, "main")
	mockGit.SetMerged(bayPath, "feature/login", true)

	eng.SyncAll()

	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree == nil || !bay.Worktree.Merged {
		t.Errorf("Merged = %v, want true", bay.Worktree != nil && bay.Worktree.Merged)
	}
}

func TestSyncAll_DoesNotSetMergedForUnmergedBranch(t *testing.T) {
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/login")

	mockGit := eng.Git.(*git.Mock)
	mockGit.SetDefaultBranch(bayPath, "main")
	// Not merged — mock default is false.

	eng.SyncAll()

	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree != nil && bay.Worktree.Merged {
		t.Errorf("Merged = true, want false")
	}
}

func TestSyncAll_SkipsMergeCheckForAlreadyMerged(t *testing.T) {
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/login")
	_ = eng.withManifest(func(m *manifest.Manifest) error {
		bay := m.FindDock("labs").FindBayByID("w1")
		bay.Worktree.Merged = true
		return nil
	})

	mockGit := eng.Git.(*git.Mock)
	mockGit.SetDefaultBranch(bayPath, "main")
	before := len(mockGit.Calls("IsMergedIntoDefault"))

	eng.SyncAll()

	if after := len(mockGit.Calls("IsMergedIntoDefault")); after != before {
		t.Errorf("SyncAll should skip merge check for already-merged bay: before=%d, after=%d", before, after)
	}
}

func TestSyncAll_BranchChangeClearsStaleMergedFlag(t *testing.T) {
	// Regression: when a merged branch is replaced in-place by a new
	// unmerged branch (common when a worktree is reused for new work
	// after its PR merged), the old Merged=true must be cleared.
	// Without this, the flag sticks forever, blocking merge re-checks
	// and making the new branch look like it's already merged.
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/old")

	mockGit := eng.Git.(*git.Mock)
	mockGit.SetDefaultBranch(bayPath, "main")
	mockGit.SetMerged(bayPath, "feature/old", true)

	// First sync marks feature/old as merged.
	eng.SyncAll()
	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree == nil || !bay.Worktree.Merged {
		t.Fatalf("precondition: expected Merged=true after first sync")
	}

	// Switch the worktree to a new unmerged branch.
	mockGit.SetBranch(bayPath, "feature/new")
	// feature/new has no merged entry in the mock → not merged.

	eng.SyncAll()

	m, _ = eng.LoadManifest()
	bay = m.FindDock("labs").FindBay("new")
	if bay == nil {
		t.Fatalf("bay 'new' missing after rename")
	}
	if bay.Worktree.Merged {
		t.Errorf("Merged = true, want false (stale flag from old branch must be cleared)")
	}
}

func TestSyncAll_BranchChangeKeepsMergedWhenNewBranchAlsoMerged(t *testing.T) {
	// Edge case: rebase-merge or cherry-pick can land a new branch that
	// is itself already merged. The re-check on branchChanged should
	// catch this and keep Merged=true.
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/old")

	mockGit := eng.Git.(*git.Mock)
	mockGit.SetDefaultBranch(bayPath, "main")
	mockGit.SetMerged(bayPath, "feature/old", true)
	mockGit.SetMerged(bayPath, "feature/new", true)

	eng.SyncAll()
	mockGit.SetBranch(bayPath, "feature/new")
	eng.SyncAll()

	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBay("new")
	if bay == nil {
		t.Fatalf("bay 'new' missing after rename")
	}
	if !bay.Worktree.Merged {
		t.Errorf("Merged = false, want true (new branch is actually merged)")
	}
}

func TestSyncAll_BranchDetachClearsMergedFlag(t *testing.T) {
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/old")

	mockGit := eng.Git.(*git.Mock)
	mockGit.SetDefaultBranch(bayPath, "main")
	mockGit.SetMerged(bayPath, "feature/old", true)

	eng.SyncAll()

	// Detach: branch goes to "" (tests model detached HEAD).
	mockGit.SetBranch(bayPath, "")

	eng.SyncAll()

	m, _ := eng.LoadManifest()
	dock := m.FindDock("labs")
	// After detach the bay is renamed to a sequential name; find
	// it by path rather than guessing the new name.
	var bay *manifest.Bay
	for i := range dock.Bays {
		if dock.Bays[i].Path == bayPath {
			bay = &dock.Bays[i]
			break
		}
	}
	if bay == nil {
		t.Fatalf("bay with path %q missing", bayPath)
	}
	if bay.Worktree != nil && bay.Worktree.Merged {
		t.Errorf("Merged = true after detach, want false")
	}
}

func TestSyncAll_SkipsBayWithoutBranch(t *testing.T) {
	eng, _ := testEngine(t)
	bayPath := filepath.Join(t.TempDir(), "w1")
	os.MkdirAll(bayPath, 0o755)
	_ = eng.withManifest(func(m *manifest.Manifest) error {
		m.FindDock("labs").Bays = append(m.FindDock("labs").Bays, manifest.Bay{
			Name:     "w1",
			Type:     manifest.BayTypeWorktree,
			Path:     bayPath,
			Worktree: &manifest.WorktreeAttrs{Repo: "labs"}, // no Branch
		})
		return nil
	})

	mockGit := eng.Git.(*git.Mock)
	before := len(mockGit.Calls("PRForBranch"))

	eng.SyncAll()

	if after := len(mockGit.Calls("PRForBranch")); after != before {
		t.Errorf("SyncAll should not query PR for branchless bay: before=%d, after=%d", before, after)
	}
}

// --- MarkPRCheckStale ---

func TestMarkPRCheckStale_ClearsCheckedAtWhenPRMissing(t *testing.T) {
	eng, _ := testEngine(t)
	_ = seedWorktreeBay(t, eng, "labs", "w1", "feature/login")

	// First sync records "no PR found" with PRCheckedAt set to now.
	eng.SyncAll()
	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree.PRCheckedAt == 0 {
		t.Fatalf("precondition: PRCheckedAt should be set after first sync")
	}

	if err := eng.MarkPRCheckStale("labs", "w1"); err != nil {
		t.Fatalf("MarkPRCheckStale: %v", err)
	}

	m, _ = eng.LoadManifest()
	bay = m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree.PRCheckedAt != 0 {
		t.Errorf("PRCheckedAt = %d, want 0 after MarkPRCheckStale", bay.Worktree.PRCheckedAt)
	}
}

func TestMarkPRCheckStale_NoOpWhenPRAlreadyCached(t *testing.T) {
	// The stale signal is only for the "user-waiting-for-PR" case. When
	// a PR is cached, we shouldn't disturb it — NeedsPRCheck already
	// short-circuits, but an extra clear would be misleading.
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/login")
	mockGit := eng.Git.(*git.Mock)
	mockGit.SetPR(bayPath, "feature/login", "42")

	eng.SyncAll()
	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBayByID("w1")
	beforeCheckedAt := bay.Worktree.PRCheckedAt
	if bay.Worktree.PR != "42" {
		t.Fatalf("precondition: PR should be 42, got %q", bay.Worktree.PR)
	}

	if err := eng.MarkPRCheckStale("labs", "w1"); err != nil {
		t.Fatalf("MarkPRCheckStale: %v", err)
	}

	m, _ = eng.LoadManifest()
	bay = m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree.PRCheckedAt != beforeCheckedAt {
		t.Errorf("PRCheckedAt changed (%d → %d); must be a no-op when PR is cached",
			beforeCheckedAt, bay.Worktree.PRCheckedAt)
	}
}

func TestMarkPRCheckStale_TriggersReQueryOnNextSync(t *testing.T) {
	// End-to-end: user reads empty PR, marks stale, monitor tick
	// re-queries and picks up a newly-opened PR — all without waiting
	// for the PRCheckTTL to elapse.
	eng, _ := testEngine(t)
	bayPath := seedWorktreeBay(t, eng, "labs", "w1", "feature/login")

	// First sync: no PR in mock yet → records PRCheckedAt.
	eng.SyncAll()
	mockGit := eng.Git.(*git.Mock)
	beforeCalls := len(mockGit.Calls("PRForBranch"))

	// A PR is opened (faster than TTL) and the user's read marks stale.
	mockGit.SetPR(bayPath, "feature/login", "200")
	if err := eng.MarkPRCheckStale("labs", "w1"); err != nil {
		t.Fatalf("MarkPRCheckStale: %v", err)
	}

	// Next sync must re-query (no TTL aging required) and pick up 200.
	eng.SyncAll()
	afterCalls := len(mockGit.Calls("PRForBranch"))
	if afterCalls <= beforeCalls {
		t.Errorf("expected additional PRForBranch call after stale mark, got same count (%d)", afterCalls)
	}

	m, _ := eng.LoadManifest()
	bay := m.FindDock("labs").FindBayByID("w1")
	if bay.Worktree.PR != "200" {
		t.Errorf("PR = %q, want 200 (re-query after stale mark should have populated it)", bay.Worktree.PR)
	}
}
