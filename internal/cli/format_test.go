package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/engine"
)

func testDocks() []engine.DockInfo {
	return []engine.DockInfo{
		{
			Name: "api",
			Bays: []engine.BayInfo{
				{
					ID:           "b1",
					Name:         "auth-fix",
					Type:         "worktree",
					Path:         "~/projects/bay-wt/auth-fix",
					Branch:       "fix/login",
					Dirty:        true,
					Waiting:      true,
					SurfaceCount: 3,
					Surfaces: []engine.SurfaceInfo{
						{ID: 1, Name: "shell", Type: "shell", Backend: "tmux-pane", Status: "ok"},
						{ID: 2, Name: "agent", Type: "agent", Agent: "codex", Backend: "tmux-pane", Status: "ok"},
						{ID: 3, Name: "tests", Type: "cmd", Command: "npm test --watch=false", Backend: "tmux-pane", Status: "ok"},
					},
					SyncStatus: "ok",
				},
				{
					Name:         "cleanup",
					Type:         "worktree",
					Path:         "~/projects/bay-wt/cleanup",
					Branch:       "cleanup",
					SurfaceCount: 1,
					SyncStatus:   "stale",
					Surfaces: []engine.SurfaceInfo{
						{ID: 1, Name: "shell", Type: "shell", Backend: "tmux-pane", Status: "stale"},
					},
				},
			},
		},
		{
			Name: "web",
			Bays: []engine.BayInfo{
				{
					Name:         "landing",
					Type:         "worktree",
					Path:         "~/projects/bay-wt-web/landing",
					Branch:       "feature/landing",
					SurfaceCount: 1,
					SyncStatus:   "ok",
				},
			},
		},
		{
			Name: "ops",
			Bays: []engine.BayInfo{
				{
					Name:         "deploy",
					Type:         "external",
					Path:         "~/projects/other/deploy",
					Branch:       "main",
					Pending:      true,
					SurfaceCount: 0,
					SyncStatus:   "missing",
				},
			},
		},
	}
}

func TestBuildListView_DefaultIncludesDocks(t *testing.T) {
	view := BuildListView(testDocks(), ListViewOptions{})
	if len(view.Docks) != 3 {
		t.Fatalf("docks = %d, want 3", len(view.Docks))
	}
	if view.Focus.Kind != FocusAll {
		t.Fatalf("focus = %q, want %q", view.Focus.Kind, FocusAll)
	}
}

// TestAlignWidth_IgnoresRobayWithoutMeta verifies the load-bearing rule:
// rows with empty meta don't contribute to the alignment width, so a long
// meta-less row doesn't push the meta column out for its meta-having
// siblings. This is the case the FormatListView caller relies on, but
// it's hard to construct via the public DockInfo API — so we exercise
// alignWidth directly.
func TestAlignWidth_IgnoresRobayWithoutMeta(t *testing.T) {
	rows := []alignedRow{
		{prefix: "short", prefixWidth: 5, metaCols: []metaCol{{text: "branch=foo", width: 10}}},
		{prefix: "longer-prefix-with-no-meta", prefixWidth: 26, metaCols: nil},
		{prefix: "medium", prefixWidth: 6, metaCols: []metaCol{{text: "status=active", width: 13}}},
	}
	got := alignWidth(rows)
	if got != 6 {
		t.Errorf("alignWidth = %d, want 6 (longest-with-meta is 'medium' at width 6)", got)
	}

	// Empty input.
	if alignWidth(nil) != 0 {
		t.Errorf("alignWidth(nil) = %d, want 0", alignWidth(nil))
	}

	// All rows meta-less.
	allEmpty := []alignedRow{
		{prefix: "a", prefixWidth: 1, metaCols: nil},
		{prefix: "bb", prefixWidth: 2, metaCols: nil},
	}
	if alignWidth(allEmpty) != 0 {
		t.Errorf("alignWidth(all-meta-less) = %d, want 0", alignWidth(allEmpty))
	}
}

// TestFormatListView_AlignsMetaColumnsAcrossRows verifies that individual
// meta fields (e.g. surfaces=) align column-by-column across sibling rows,
// even when some rows have more fields than others (branch, status, etc.).
func TestFormatListView_AlignsMetaColumnsAcrossRows(t *testing.T) {
	docks := []engine.DockInfo{
		{
			Name: "api",
			Bays: []engine.BayInfo{
				{Name: "b1", SurfaceCount: 1, SyncStatus: "ok"},
				{Name: "login-bug", Branch: "fix/login-bug", Dirty: true, SurfaceCount: 1, SyncStatus: "ok"},
				{Name: "auth-refactor", Branch: "fix/auth-refactor", Dirty: true, SurfaceCount: 2, SyncStatus: "ok"},
			},
		},
	}
	view := BuildListView(docks, ListViewOptions{
		Focus: ListFocus{Kind: FocusDock, Dock: "api"},
	})
	out := stripANSI(FormatListView(view, false, false))

	// n= should appear at the same column on all three rows.
	var surfCols []int
	for _, line := range strings.Split(out, "\n") {
		if idx := strings.Index(line, "n="); idx >= 0 {
			surfCols = append(surfCols, idx)
		}
	}
	if len(surfCols) != 3 {
		t.Fatalf("expected 3 n= entries, got %d:\n%s", len(surfCols), out)
	}
	for i := 1; i < len(surfCols); i++ {
		if surfCols[i] != surfCols[0] {
			t.Errorf("surfaces= columns not aligned: %v\noutput:\n%s", surfCols, out)
			break
		}
	}
}

// TestFormatListView_AlignsBayMetaWithinDock verifies that the meta
// column on bay lines starts at the same screen column for every
// bay in a dock, regardless of name length. This is the readability
// fix that turns a key=value run-on into a scannable column.
func TestFormatListView_AlignsBayMetaWithinDock(t *testing.T) {
	view := BuildListView(testDocks(), ListViewOptions{
		Focus: ListFocus{Kind: FocusDock, Dock: "api"},
	})
	out := stripANSI(FormatListView(view, false, false))

	var authLine, cleanupLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "bay auth-fix") {
			authLine = line
		}
		if strings.Contains(line, "bay cleanup") {
			cleanupLine = line
		}
	}
	if authLine == "" || cleanupLine == "" {
		t.Fatalf("missing bay lines:\n%s", out)
	}

	authMetaStart := strings.Index(authLine, "br=")
	cleanupMetaStart := strings.Index(cleanupLine, "br=")
	if authMetaStart < 0 || cleanupMetaStart < 0 {
		t.Fatalf("could not find meta start:\nauth:    %q\ncleanup: %q", authLine, cleanupLine)
	}
	if authMetaStart != cleanupMetaStart {
		t.Errorf("bay meta columns not aligned:\n  auth-fix br= at column %d\n  cleanup  br= at column %d\n  auth:    %q\n  cleanup: %q",
			authMetaStart, cleanupMetaStart, authLine, cleanupLine)
	}
}

func TestFormatListView_TruncatesLongBayNameAndBranch(t *testing.T) {
	longName := "bay-" + strings.Repeat("long-name-", 6)
	longBranch := "feature/" + strings.Repeat("long-branch-", 5)
	docks := []engine.DockInfo{
		{
			Name: "api",
			Bays: []engine.BayInfo{
				{
					Name:         longName,
					Branch:       longBranch,
					SurfaceCount: 1,
					SyncStatus:   "ok",
				},
			},
		},
	}
	view := BuildListView(docks, ListViewOptions{
		Focus: ListFocus{Kind: FocusDock, Dock: "api"},
	})

	out := stripANSI(FormatListView(view, false, false))
	truncatedName := truncateListField(longName, listBayNameStrMax)
	truncatedBranch := truncateListField(longBranch, listBranchStrMax)
	if listBranchStrMax != 24 {
		t.Fatalf("branch display cap = %d, want 24", listBranchStrMax)
	}
	if !strings.Contains(out, "bay "+truncatedName) {
		t.Fatalf("output missing truncated bay name %q:\n%s", truncatedName, out)
	}
	if strings.Contains(out, longName) {
		t.Fatalf("output should not contain full bay name %q:\n%s", longName, out)
	}
	if !strings.Contains(out, "br="+truncatedBranch) {
		t.Fatalf("output missing truncated branch %q:\n%s", truncatedBranch, out)
	}
	if strings.Contains(out, longBranch) {
		t.Fatalf("output should not contain full branch %q:\n%s", longBranch, out)
	}
}

func TestListRowsKeepsLongBayNameAndBranchFull(t *testing.T) {
	longName := "bay-" + strings.Repeat("long-name-", 6)
	longBranch := "feature/" + strings.Repeat("long-branch-", 5)
	view := BuildListView([]engine.DockInfo{
		{
			Name: "api",
			Bays: []engine.BayInfo{
				{Name: longName, Branch: longBranch, SyncStatus: "ok"},
			},
		},
	}, ListViewOptions{Focus: ListFocus{Kind: FocusDock, Dock: "api"}})

	rows := ListRows(view)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].BayName != longName {
		t.Fatalf("BayName = %q, want full %q", rows[0].BayName, longName)
	}
	if rows[0].BayBranch != longBranch {
		t.Fatalf("BayBranch = %q, want full %q", rows[0].BayBranch, longBranch)
	}
}

// TestFormatListView_AlignsSurfaceMetaAcrossDock verifies that in tree
// mode, surface meta columns are aligned across ALL bays in a dock,
// not just within each bay. A long surface name in one bay
// pushes the meta column out for sibling surfaces under other bays
// too — the user's eye doesn't need to recalibrate as they scroll.
func TestFormatListView_AlignsSurfaceMetaAcrossDock(t *testing.T) {
	// Two bays in the same dock with different longest-surface
	// names: auth-fix has "tests" (5 chars) and shorter; cleanup has
	// only "shell" (5 chars). Both should align to the same dock-wide
	// max so the meta column is stable.
	docks := []engine.DockInfo{
		{
			Name: "api",
			Bays: []engine.BayInfo{
				{
					Name: "auth-fix",
					Surfaces: []engine.SurfaceInfo{
						{ID: 1, Name: "agent", Type: "agent", Agent: "claude"},
						{ID: 2, Name: "monitor-pane", Type: "cmd", Command: "top"},
					},
				},
				{
					Name: "cleanup",
					Surfaces: []engine.SurfaceInfo{
						{ID: 1, Name: "sh", Type: "shell"},
					},
				},
			},
		},
	}
	view := BuildListView(docks, ListViewOptions{
		Focus:     ListFocus{Kind: FocusDock, Dock: "api"},
		Recursive: true,
	})
	out := stripANSI(FormatListView(view, false, false))

	// All three surface lines (across two bays) should have
	// "type=" at the same column.
	var typeColumns []int
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "sf ") {
			continue
		}
		idx := strings.Index(line, "ty=")
		if idx < 0 {
			t.Fatalf("surface line missing ty= column: %q", line)
		}
		typeColumns = append(typeColumns, idx)
	}
	if len(typeColumns) != 3 {
		t.Fatalf("expected 3 surface lines, got %d:\n%s", len(typeColumns), out)
	}
	for i := 1; i < len(typeColumns); i++ {
		if typeColumns[i] != typeColumns[0] {
			t.Errorf("surface meta columns not aligned across dock: %v\noutput:\n%s", typeColumns, out)
		}
	}
}

func TestBuildListView_DockFocusStopsAtBaysByDefault(t *testing.T) {
	view := BuildListView(testDocks(), ListViewOptions{
		Focus: ListFocus{Kind: FocusDock, Dock: "api"},
	})
	out := stripANSI(FormatListView(view, false, false))

	if !strings.Contains(out, "dk api") {
		t.Fatalf("dock focus output missing dock header:\n%s", out)
	}
	if !strings.Contains(out, "bay auth-fix") || !strings.Contains(out, "bay cleanup") {
		t.Fatalf("dock focus output missing bays:\n%s", out)
	}
	if strings.Contains(out, "sf ") {
		t.Fatalf("dock focus default should not recurse into surfaces:\n%s", out)
	}
}

func TestBuildListView_BayFocusShowsFullTree(t *testing.T) {
	view := BuildListView(testDocks(), ListViewOptions{
		Focus: ListFocus{Kind: FocusBay, Dock: "api", BayID: "b1"},
	})
	out := stripANSI(FormatListView(view, false, false))

	for _, want := range []string{
		"dk api",
		"bay auth-fix",
		"sf agent",
		"ty=agent",
		"ag=codex",
		"ty=cmd",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("bay focus output missing %q:\n%s", want, out)
		}
	}
}

func TestBuildListView_BayFocusRestrictsToDock(t *testing.T) {
	view := BuildListView(testDocks(), ListViewOptions{
		Focus: ListFocus{Kind: FocusBay, Dock: "api", BayID: "b1"},
	})
	out := stripANSI(FormatListView(view, false, false))

	if strings.Contains(out, "bay landing") {
		t.Fatalf("bay focus should not include bays from other docks:\n%s", out)
	}
}

func TestFormatListView_SuppressesSyncOKAndShowsStale(t *testing.T) {
	view := BuildListView(testDocks(), ListViewOptions{
		Focus: ListFocus{Kind: FocusDock, Dock: "api"},
	})
	out := stripANSI(FormatListView(view, false, false))

	if strings.Contains(out, "sy=ok") {
		t.Fatalf("sy=ok should be suppressed:\n%s", out)
	}
	if !strings.Contains(out, "sy=stale") {
		t.Fatalf("stale sync state should be shown:\n%s", out)
	}
}

func TestFormatListView_ShowsPrepareSetupStatus(t *testing.T) {
	view := BuildListView([]engine.DockInfo{
		{
			Name: "api",
			Bays: []engine.BayInfo{{
				Name:       "auth-fix",
				Setup:      "running:vendors",
				SyncStatus: "ok",
			}},
		},
	}, ListViewOptions{Focus: ListFocus{Kind: FocusDock, Dock: "api"}})

	out := stripANSI(FormatListView(view, false, false))
	if !strings.Contains(out, "setup=running:vendors") {
		t.Fatalf("prepare setup status missing:\n%s", out)
	}
}

func TestFormatBayShow_RendersMultiLineDescription(t *testing.T) {
	bay := &engine.BayInfo{
		Name:        "auth-fix",
		Description: "Login flow fixes\n\nhit rebase conflict on helper.ts\ntests green except auth_test.go",
		Type:        "worktree",
		Path:        "~/x",
		SyncStatus:  "ok",
	}
	out := stripANSI(FormatBayShow("api", bay, false))
	// First line is shown inline with the description label; each body
	// line is on its own row indented to the value column.
	for _, want := range []string{
		"description Login flow fixes",
		"hit rebase conflict on helper.ts",
		"tests green except auth_test.go",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("bay show missing %q:\n%s", want, out)
		}
	}
}

func TestFormatBayShow_IncludesDefaultAgentAndSurfaces(t *testing.T) {
	bay := &engine.BayInfo{
		Name:         "auth-fix",
		Type:         "worktree",
		Path:         "~/projects/bay-wt/auth-fix",
		Branch:       "fix/login",
		Dirty:        true,
		DefaultAgent: "codex",
		SyncStatus:   "ok",
		Surfaces: []engine.SurfaceInfo{
			{ID: 1, Name: "shell", Type: "shell", Status: "ok"},
			{ID: 2, Name: "agent", Type: "agent", Agent: "codex", Status: "ok"},
		},
	}

	out := stripANSI(FormatBayShow("api", bay, false))
	for _, want := range []string{
		"bay auth-fix",
		"dock api",
		"default agent codex",
		"surface agent",
		"ty=agent",
		"ag=codex",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("bay show missing %q:\n%s", want, out)
		}
	}
}

// TestBayMetaCols_ShobayIDOnlyWhenDifferent confirms the conditional
// surfacing of the id meta column: present when ID adds info beyond name and
// dir (e.g., an external bay with a non-wN dir), suppressed when name or
// dir already carry it.
func TestBayMetaCols_ShobayIDOnlyWhenDifferent(t *testing.T) {
	// Auto-named bay with no rename: ID matches Name, suppress id.
	same := engine.BayInfo{ID: "b1", Name: "b1", SyncStatus: "ok"}
	if got := bayMetaCols(same, false, true); got[2].text != "" {
		t.Errorf("expected no id col when ID==Name; got %q", got[2].text)
	}

	// Renamed worktree bay: dir basename == ID, so dir column carries the
	// handle and id is redundant.
	renamedWorktree := engine.BayInfo{ID: "b1", Name: "auth-fix", Path: "/wt/b1", SyncStatus: "ok"}
	if got := bayMetaCols(renamedWorktree, false, false); got[2].text != "" {
		t.Errorf("expected no id col when ID==dir basename; got %q", got[2].text)
	}

	// Renamed external bay: dir basename diverges from ID, so id is the
	// only column carrying the stable handle.
	renamedExternal := engine.BayInfo{ID: "b3", Name: "frontend", Path: "/proj/myapp", SyncStatus: "ok"}
	cols := bayMetaCols(renamedExternal, false, false)
	if !strings.Contains(cols[2].text, "b3") {
		t.Errorf("expected id col to contain b3 for external bay; got %q", cols[2].text)
	}

	// No Path, no Name: dir is empty so id is the only identifier.
	empty := engine.BayInfo{ID: "b1", Name: "", SyncStatus: "ok"}
	cols = bayMetaCols(empty, false, false)
	if !strings.Contains(cols[2].text, "b1") {
		t.Errorf("expected id col to contain b1 when Name and Path are empty; got %q", cols[2].text)
	}
}

// TestFormatBayShow_IncludesIDWhenDifferent confirms bay show
// surfaces the ID row only when it adds information.
func TestFormatBayShow_IncludesIDWhenDifferent(t *testing.T) {
	baySame := &engine.BayInfo{ID: "b1", Name: "b1", Type: "worktree", SyncStatus: "ok"}
	if strings.Contains(stripANSI(FormatBayShow("api", baySame, false)), "id b1") {
		t.Error("ID row should be omitted when ID matches Name")
	}

	bayDiff := &engine.BayInfo{ID: "b1", Name: "auth-fix", Type: "worktree", SyncStatus: "ok"}
	if !strings.Contains(stripANSI(FormatBayShow("api", bayDiff, false)), "id b1") {
		t.Error("ID row should appear when ID differs from Name")
	}
}

// TestFormatBayShow_UsesCanonicalLabel pins the unification: the bay
// row carries BayInfo.Label() so unnamed bays show their ID and named
// path-backed bays show the dotted "<id>.<name>" form. The separate id
// row is then redundant for worktree bays whose ID matches the dir tag.
func TestFormatBayShow_UsesCanonicalLabel(t *testing.T) {
	unnamed := &engine.BayInfo{ID: "b2", Name: "", Type: "worktree", Path: "/wt/b2", SyncStatus: "ok"}
	out := stripANSI(FormatBayShow("api", unnamed, false))
	if !strings.Contains(out, "bay b2") {
		t.Fatalf("unnamed bay should render label 'b2':\n%s", out)
	}
	if strings.Contains(out, "id b2") {
		t.Errorf("id row is redundant when label already carries it:\n%s", out)
	}

	named := &engine.BayInfo{ID: "b1", Name: "auth-fix", Type: "worktree", Path: "/wt/b1", SyncStatus: "ok"}
	out = stripANSI(FormatBayShow("api", named, false))
	if !strings.Contains(out, "bay b1.auth-fix") {
		t.Fatalf("named bay should render dotted label 'b1.auth-fix':\n%s", out)
	}
	if strings.Contains(out, "id b1") {
		t.Errorf("id row is redundant when label already carries it:\n%s", out)
	}
}

// TestFormatListView_UsesCanonicalBayLabel confirms the same unification
// in the table view: unnamed bays fall back to ID instead of rendering
// blank, and named bays show the dotted "<id>.<name>" form.
func TestFormatListView_UsesCanonicalBayLabel(t *testing.T) {
	docks := []engine.DockInfo{
		{
			Name: "api",
			Bays: []engine.BayInfo{
				{ID: "b1", Name: "auth-fix", Path: "/wt/b1", SyncStatus: "ok"},
				{ID: "b2", Name: "", Path: "/wt/b2", SyncStatus: "ok"},
			},
		},
	}
	view := BuildListView(docks, ListViewOptions{
		Focus: ListFocus{Kind: FocusDock, Dock: "api"},
	})
	out := stripANSI(FormatListView(view, false, false))

	if !strings.Contains(out, "bay b1.auth-fix") {
		t.Errorf("named path-backed bay should render dotted label:\n%s", out)
	}
	if !strings.Contains(out, "bay b2") {
		t.Errorf("unnamed bay should fall back to ID:\n%s", out)
	}
	// The label already carries the dir tag, so no separate dir column.
	if strings.Contains(out, "dir=b1") || strings.Contains(out, "dir=b2") {
		t.Errorf("dir column is redundant once label carries it:\n%s", out)
	}
}

func TestLabelValueFormatsHumanReadableLabels(t *testing.T) {
	if got := stripANSI(labelValue("dock", "api")); got != "dock api" {
		t.Fatalf("labelValue() = %q, want %q", got, "dock api")
	}
}

func TestFormatListRows_DenormalizesSurfaceRows(t *testing.T) {
	view := BuildListView(testDocks(), ListViewOptions{
		Focus:     ListFocus{Kind: FocusBay, Dock: "api", BayID: "b1"},
		Recursive: true,
	})
	rows := ListRows(view)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	s := string(data)
	for _, want := range []string{`"surface_type":"agent"`, `"surface_agent":"codex"`, `"surface_type":"cmd"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("rows JSON missing %q:\n%s", want, s)
		}
	}
	if !strings.Contains(s, `"bay_waiting":true`) {
		t.Fatalf("rows JSON missing bay waiting flag:\n%s", s)
	}
}

func TestBuildListView_FocusDockFiltersDocks(t *testing.T) {
	view := BuildListView(testDocks(), ListViewOptions{
		Focus: ListFocus{Kind: FocusDock, Dock: "api"},
	})
	out := stripANSI(FormatListView(view, false, false))

	if !strings.Contains(out, "dk api") {
		t.Fatalf("dock-focused output missing target dock:\n%s", out)
	}
	if strings.Contains(out, "dk web") || strings.Contains(out, "dk ops") {
		t.Fatalf("dock-focused output should not include other docks:\n%s", out)
	}
}

func TestFormatListView_ShobayWaitingIndicator(t *testing.T) {
	view := BuildListView(testDocks(), ListViewOptions{
		Focus: ListFocus{Kind: FocusDock, Dock: "api"},
	})
	out := stripANSI(FormatListView(view, false, false))

	if !strings.Contains(out, "\u23f3") {
		t.Fatalf("expected waiting indicator in output:\n%s", out)
	}
}

func TestFormatListView_HighlightsCurrentContext(t *testing.T) {
	view := BuildListView(testDocks(), ListViewOptions{
		Focus: ListFocus{Kind: FocusDock, Dock: "api"},
	})
	view.CurrentDock = "api"
	view.CurrentBayID = "b1"

	out := stripANSI(FormatListView(view, false, false))

	if !strings.Contains(out, "dk api *") {
		t.Fatalf("current dock should have * marker:\n%s", out)
	}
	if !strings.Contains(out, "bay auth-fix *") {
		t.Fatalf("current bay should have * marker:\n%s", out)
	}
	// Non-current bay should NOT have marker.
	if strings.Contains(out, "bay cleanup *") {
		t.Fatalf("non-current bay should not have * marker:\n%s", out)
	}
}

func TestFormatListView_NoHighlightWithoutContext(t *testing.T) {
	view := BuildListView(testDocks(), ListViewOptions{
		Focus: ListFocus{Kind: FocusDock, Dock: "api"},
	})
	// No CurrentDock/CurrentBay set.

	out := stripANSI(FormatListView(view, false, false))

	if strings.Contains(out, " *") {
		t.Fatalf("should have no * markers without current context:\n%s", out)
	}
}

func TestDetectStdoutWidth_ColumnsOverride(t *testing.T) {
	t.Setenv("COLUMNS", "200")
	if got := detectStdoutWidth(); got != 200 {
		t.Errorf("detectStdoutWidth() with COLUMNS=200 = %d, want 200", got)
	}

	t.Setenv("COLUMNS", "0")
	// COLUMNS=0 is ignored; real tty width (or 0 if piped) wins.
	if got := detectStdoutWidth(); got < 0 {
		t.Errorf("detectStdoutWidth() with COLUMNS=0 = %d, want >=0 (fallback)", got)
	}

	t.Setenv("COLUMNS", "garbage")
	if got := detectStdoutWidth(); got < 0 {
		t.Errorf("detectStdoutWidth() with COLUMNS=garbage = %d, want >=0 (fallback)", got)
	}
}

func TestDescStrMaxForWidth(t *testing.T) {
	// Long mode: overhead is 6 (1 separator + 2 quotes + 3 "ds=").
	tests := []struct {
		name         string
		termWidth    int
		nonDescWidth int
		short        bool
		want         int
	}{
		{"piped returns full cap", 0, 100, false, listDescPipedStrMax},
		{"narrow terminal floors at min", 60, 80, false, listDescMinStrLen},
		{"medium terminal gets moderate budget", 120, 60, false, 54},
		{"wide terminal caps at max", 400, 60, false, listDescPipedStrMax},
		{"exactly min available", 80, 59, false, listDescMinStrLen},
		{"just above min", 100, 70, false, 24},
		// Short mode: overhead is 3 (separator + quotes only, no "ds=").
		{"short mode gets extra 3 chars", 120, 60, true, 57},
	}
	for _, tc := range tests {
		got := descStrMaxForWidth(tc.termWidth, tc.nonDescWidth, tc.short)
		if got != tc.want {
			t.Errorf("%s: descStrMaxForWidth(%d, %d, short=%v) = %d, want %d",
				tc.name, tc.termWidth, tc.nonDescWidth, tc.short, got, tc.want)
		}
	}
}

func TestFormatDockTree_IncludesDocksWithoutBays(t *testing.T) {
	docks := []engine.DockInfo{
		{
			Name: "tools",
			Bays: []engine.BayInfo{
				{Name: "scratch", Branch: "notes", SurfaceCount: 1, SyncStatus: "ok"},
			},
		},
	}

	out := stripANSI(FormatListView(BuildListView(docks, ListViewOptions{}), false, false))
	if !strings.Contains(out, "dk tools") {
		t.Fatalf("missing dock:\n%s", out)
	}
}
