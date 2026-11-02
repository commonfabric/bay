package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNew_ReturnsEmptyManifest(t *testing.T) {
	m := New()
	if m.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", m.Version, CurrentVersion)
	}
	if m.Docks == nil {
		t.Fatal("docks is nil")
	}
	if len(m.Docks) != 0 {
		t.Errorf("docks length = %d, want 0", len(m.Docks))
	}
}

func TestParse_EmptyJSON(t *testing.T) {
	m, err := Parse([]byte(`{"version":2,"repos":[],"docks":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Docks) != 0 {
		t.Errorf("docks length = %d, want 0", len(m.Docks))
	}
	if len(m.Repos) != 0 {
		t.Errorf("repos length = %d, want 0", len(m.Repos))
	}
}

func TestParse_InvalidJSON(t *testing.T) {
	_, err := Parse([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParse_FullManifest(t *testing.T) {
	data := []byte(`{
		"version": 1,
		"repos": [{"name": "labs", "path": "/repo/labs"}],
		"docks": [
			{
				"name": "labs",
				"repo": "labs",
				"bays": [
					{
						"name": "auth-fix",
						"type": "worktree",
						"path": "/tmp/bay1",
						"status": "active",
						"last_focused": 1,
						"worktree": {
							"repo": "labs",
							"branch": "fix-auth",
							"pr": "52"
						},
						"surfaces": [
							{
								"id": 1,
								"name": "agent",
								"type": "agent",
								"backend": "tmux-pane",
								"agent": "claude-code",
								"tmux": {
									"pane_id": "%42",
									"window_id": "@15",
									"layout_group": 1
								}
							},
							{
								"id": 2,
								"name": "shell",
								"type": "shell",
								"backend": "tmux-pane",
								"tmux": {
									"layout_group": 1,
									"split_from": 1,
									"split_dir": "h"
								}
							},
							{
								"id": 3,
								"name": "editor",
								"type": "editor",
								"backend": "gui-app",
								"gui": {
									"app_command": "cursor",
									"bundle_id": "com.todesktop.230313mzl4w4u92"
								}
							}
						]
					}
				]
			}
		]
	}`)

	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(m.Docks) != 1 {
		t.Fatalf("docks length = %d, want 1", len(m.Docks))
	}

	dock := &m.Docks[0]
	if dock.Name != "labs" {
		t.Errorf("dock name = %q, want %q", dock.Name, "labs")
	}
	if len(dock.Bays) != 1 {
		t.Fatalf("bays length = %d, want 1", len(dock.Bays))
	}

	bay := &dock.Bays[0]
	if bay.Name != "auth-fix" {
		t.Errorf("bay name = %q, want %q", bay.Name, "auth-fix")
	}
	// Path basename "bay1" is non-canonical, so pass 1 skips it and pass 2
	// assigns the first current-prefix sequential ID.
	if bay.ID != "b1" {
		t.Errorf("bay ID = %q, want %q", bay.ID, "b1")
	}
	if bay.Type != BayTypeWorktree {
		t.Errorf("bay type = %q, want %q", bay.Type, BayTypeWorktree)
	}
	// v2 manifest with status=active should not set Merged
	if bay.Worktree != nil && bay.Worktree.Merged {
		t.Error("bay should not be merged")
	}
	if bay.LastFocused != 1 {
		t.Errorf("last_focused = %d, want 1", bay.LastFocused)
	}
	if bay.Worktree == nil {
		t.Fatal("worktree attrs is nil")
	}
	if dock.Path != "/repo/labs" {
		t.Errorf("dock path = %q, want /repo/labs", dock.Path)
	}
	if bay.Worktree.Repo != "" {
		t.Errorf("worktree repo = %q, want empty after v6 migration", bay.Worktree.Repo)
	}
	if bay.Worktree.Branch != "fix-auth" {
		t.Errorf("worktree branch = %q, want %q", bay.Worktree.Branch, "fix-auth")
	}
	if bay.Worktree.PR != "52" {
		t.Errorf("worktree pr = %q, want %q", bay.Worktree.PR, "52")
	}

	if len(bay.Surfaces) != 3 {
		t.Fatalf("surfaces length = %d, want 3", len(bay.Surfaces))
	}

	// Agent surface
	s := &bay.Surfaces[0]
	if s.ID != 1 || s.Name != "agent" || s.Type != SurfaceTypeAgent || s.Backend != SurfaceBackendTmux {
		t.Errorf("surface 0: got id=%d name=%q type=%q backend=%q", s.ID, s.Name, s.Type, s.Backend)
	}
	if s.Agent == nil || *s.Agent != "claude-code" {
		t.Errorf("surface 0: agent = %v, want %q", s.Agent, "claude-code")
	}
	if s.Tmux == nil {
		t.Fatal("surface 0: tmux attrs is nil")
	}
	if s.Tmux.PaneID != "%42" || s.Tmux.LayoutGroup != 1 {
		t.Errorf("surface 0 tmux: pane_id=%q layout_group=%d", s.Tmux.PaneID, s.Tmux.LayoutGroup)
	}

	// Shell surface
	s = &bay.Surfaces[1]
	if s.Type != SurfaceTypeShell || s.Tmux.SplitFrom != 1 || s.Tmux.SplitDir != "h" {
		t.Errorf("surface 1: type=%q split_from=%d split_dir=%q", s.Type, s.Tmux.SplitFrom, s.Tmux.SplitDir)
	}

	// Editor surface
	s = &bay.Surfaces[2]
	if s.Type != SurfaceTypeEditor || s.Backend != SurfaceBackendGUI {
		t.Errorf("surface 2: type=%q backend=%q", s.Type, s.Backend)
	}
	if s.GUI == nil || s.GUI.AppCommand != "cursor" {
		t.Errorf("surface 2: gui = %v", s.GUI)
	}
}

func TestRoundTrip(t *testing.T) {
	agent := "claude-code"
	original := &Manifest{
		Version: CurrentVersion,
		Docks: []Dock{
			{
				Name: "labs",
				Path: "/repo/labs",
				Bays: []Bay{
					{
						Name: "auth-fix",
						Type: BayTypeWorktree,
						Path: "/tmp/bay1",
						Worktree: &WorktreeAttrs{
							Branch: "fix-auth",
							PR:     "52",
						},
						Surfaces: []Surface{
							{
								ID:      1,
								Name:    "agent",
								Type:    SurfaceTypeAgent,
								Backend: SurfaceBackendTmux,
								Agent:   &agent,
								Tmux:    &TmuxAttrs{LayoutGroup: 1},
							},
						},
					},
				},
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(restored.Docks) != 1 {
		t.Fatalf("docks length = %d, want 1", len(restored.Docks))
	}
	if restored.Docks[0].Name != "labs" {
		t.Errorf("dock name = %q, want %q", restored.Docks[0].Name, "labs")
	}
	bay := &restored.Docks[0].Bays[0]
	if bay.Name != "auth-fix" {
		t.Errorf("bay name = %q, want %q", bay.Name, "auth-fix")
	}
	if bay.Surfaces[0].Agent == nil || *bay.Surfaces[0].Agent != "claude-code" {
		t.Error("agent not round-tripped")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	original := New()
	original.Docks = append(original.Docks, Dock{
		Name: "test",
		Bays: []Bay{
			{Name: "bay1", Type: BayTypeWorktree},
		},
	})

	if err := Save(path, original); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded.Docks) != 1 || loaded.Docks[0].Name != "test" {
		t.Errorf("loaded dock: got %v", loaded.Docks)
	}
	if len(loaded.Docks[0].Bays) != 1 || loaded.Docks[0].Bays[0].Name != "bay1" {
		t.Errorf("loaded bay: got %v", loaded.Docks[0].Bays)
	}
}

func TestSave_CreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "manifest.json")

	if err := Save(path, New()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestSave_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	// Save initial version
	m := New()
	m.Docks = append(m.Docks, Dock{Name: "first"})
	if err := Save(path, m); err != nil {
		t.Fatal(err)
	}

	// No backup on first save (no previous file to back up).
	backupDir := filepath.Join(dir, "backups")
	backups, _ := ListBackups(path)
	if len(backups) != 0 {
		t.Error("no backups should exist on first save")
	}

	// Save again — should create a backup of the first version.
	m.Docks[0].Name = "second"
	if err := Save(path, m); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(backupDir); err != nil {
		t.Error("backup directory should exist after second save")
	}
	backups, _ = ListBackups(path)
	if len(backups) == 0 {
		t.Error("backup should exist after second save")
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/manifest.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLockedUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	// First update creates the manifest
	err := LockedUpdate(path, func(m *Manifest) error {
		m.Docks = append(m.Docks, Dock{Name: "labs"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Second update modifies it
	err = LockedUpdate(path, func(m *Manifest) error {
		if len(m.Docks) != 1 || m.Docks[0].Name != "labs" {
			t.Errorf("expected dock 'labs', got %v", m.Docks)
		}
		m.Docks = append(m.Docks, Dock{Name: "research"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify final state
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Docks) != 2 {
		t.Fatalf("docks length = %d, want 2", len(m.Docks))
	}
}

// --- Dock operations ---

func TestFindDock(t *testing.T) {
	m := New()
	m.Docks = []Dock{{Name: "labs"}, {Name: "research"}}

	d := m.FindDock("labs")
	if d == nil || d.Name != "labs" {
		t.Errorf("FindDock(labs) = %v", d)
	}

	d = m.FindDock("nonexistent")
	if d != nil {
		t.Errorf("FindDock(nonexistent) = %v, want nil", d)
	}
}

func TestFindDock_ReturnsMutablePointer(t *testing.T) {
	m := New()
	m.Docks = []Dock{{Name: "labs"}}

	d := m.FindDock("labs")
	d.Bays = append(d.Bays, Bay{Name: "bay1"})

	if len(m.Docks[0].Bays) != 1 {
		t.Error("mutation through FindDock pointer did not affect manifest")
	}
}

func TestAddDock(t *testing.T) {
	m := New()

	if err := m.AddDock(Dock{Name: "labs"}); err != nil {
		t.Fatal(err)
	}
	if len(m.Docks) != 1 || m.Docks[0].Name != "labs" {
		t.Errorf("after add: %v", m.Docks)
	}
}

func TestAddDock_DuplicateName(t *testing.T) {
	m := New()
	m.Docks = []Dock{{Name: "labs"}}

	err := m.AddDock(Dock{Name: "labs"})
	if err == nil {
		t.Fatal("expected error for duplicate dock name")
	}
}

func TestRemoveDock(t *testing.T) {
	m := New()
	m.Docks = []Dock{{Name: "labs"}, {Name: "research"}}

	if err := m.RemoveDock("labs"); err != nil {
		t.Fatal(err)
	}
	if len(m.Docks) != 1 || m.Docks[0].Name != "research" {
		t.Errorf("after remove: %v", m.Docks)
	}
}

func TestRemoveDock_NotFound(t *testing.T) {
	m := New()
	if err := m.RemoveDock("nonexistent"); err == nil {
		t.Fatal("expected error for missing dock")
	}
}

// --- Bay operations ---

func TestFindBay(t *testing.T) {
	d := &Dock{
		Name: "labs",
		Bays: []Bay{{Name: "auth-fix"}, {Name: "perf"}},
	}

	bay := d.FindBay("auth-fix")
	if bay == nil || bay.Name != "auth-fix" {
		t.Errorf("FindBay(auth-fix) = %v", bay)
	}

	bay = d.FindBay("nonexistent")
	if bay != nil {
		t.Errorf("FindBay(nonexistent) = %v, want nil", bay)
	}
}

func TestAddBay(t *testing.T) {
	d := &Dock{Name: "labs"}

	if err := d.AddBay(Bay{Name: "auth-fix"}); err != nil {
		t.Fatal(err)
	}
	if len(d.Bays) != 1 || d.Bays[0].Name != "auth-fix" {
		t.Errorf("after add: %v", d.Bays)
	}
}

func TestAddBay_DuplicateName(t *testing.T) {
	d := &Dock{
		Name: "labs",
		Bays: []Bay{{Name: "auth-fix"}},
	}

	err := d.AddBay(Bay{Name: "auth-fix"})
	if err == nil {
		t.Fatal("expected error for duplicate bay name")
	}
}

func TestAddBay_RejectsReservedHomeForNormalBays(t *testing.T) {
	d := &Dock{Name: "labs"}
	cases := []Bay{
		{ID: HomeBayID, Type: BayTypeWorktree, Path: "/repo-worktrees/w1"},
		{Name: HomeBayID, Type: BayTypeExternal, Path: "/projects/external"},
	}
	for _, b := range cases {
		if err := d.AddBay(b); err == nil {
			t.Errorf("AddBay(%+v) = nil, want reserved-home error", b)
		}
	}
}

func TestAddBay_AllowsHomeShape(t *testing.T) {
	d := &Dock{Name: "labs", Path: "/projects/labs"}
	err := d.AddBay(Bay{
		ID:   HomeBayID,
		Name: HomeBayID,
		Type: BayTypeHome,
		Path: "/projects/labs",
	})
	if err != nil {
		t.Fatalf("AddBay(home) error = %v", err)
	}
	if got := d.FindBayByID(HomeBayID); got == nil || got.Type != BayTypeHome {
		t.Fatalf("home bay not inserted correctly: %+v", got)
	}
}

func TestAddBay_RejectsMalformedHomeShape(t *testing.T) {
	d := &Dock{Name: "labs", Path: "/projects/labs"}
	cases := []Bay{
		{ID: "", Name: HomeBayID, Type: BayTypeHome, Path: "/projects/labs"},
		{ID: HomeBayID, Name: "checkout", Type: BayTypeHome, Path: "/projects/labs"},
		{ID: HomeBayID, Name: HomeBayID, Type: BayTypeHome, Path: "/projects/other"},
		{ID: HomeBayID, Name: HomeBayID, Type: BayTypeHome, Path: "/projects/labs", Worktree: &WorktreeAttrs{}},
	}
	for _, b := range cases {
		if err := d.AddBay(b); err == nil {
			t.Errorf("AddBay(%+v) = nil, want malformed-home error", b)
		}
	}
}

func TestRemoveBay(t *testing.T) {
	d := &Dock{
		Name: "labs",
		Bays: []Bay{
			{ID: "w1", Name: "auth-fix"},
			{ID: "w2", Name: "perf"},
		},
	}

	if err := d.RemoveBay("w1"); err != nil {
		t.Fatal(err)
	}
	if len(d.Bays) != 1 || d.Bays[0].Name != "perf" {
		t.Errorf("after remove: %v", d.Bays)
	}
}

func TestRemoveBay_NotFound(t *testing.T) {
	d := &Dock{Name: "labs"}
	if err := d.RemoveBay("nonexistent"); err == nil {
		t.Fatal("expected error for missing bay")
	}
}

// Bay IDs are reassigned from a max-of-current pool, so a stale
// undo-close entry would silently restore into a future bay that
// reclaims the same ID. RemoveBay must purge those entries.
func TestRemoveBay_PurgesClosedEntriesForBay(t *testing.T) {
	d := &Dock{
		Name: "labs",
		Bays: []Bay{
			{ID: "w1", Name: "auth-fix"},
			{ID: "w2", Name: "perf"},
		},
		ClosedEntries: []ClosedEntry{
			{ClosedAt: 100, Kind: ClosedKindSurface, Surface: &ClosedSurface{Bay: "w1", Name: "claude"}},
			{ClosedAt: 200, Kind: ClosedKindSurface, Surface: &ClosedSurface{Bay: "w2", Name: "shell"}},
			{ClosedAt: 300, Kind: ClosedKindSurface, Surface: &ClosedSurface{Bay: "w1", Name: "logs"}},
		},
	}

	if err := d.RemoveBay("w1"); err != nil {
		t.Fatal(err)
	}
	if len(d.ClosedEntries) != 1 {
		t.Fatalf("ClosedEntries: got %d, want 1: %+v", len(d.ClosedEntries), d.ClosedEntries)
	}
	if d.ClosedEntries[0].Surface.Bay != "w2" {
		t.Errorf("survivor: got %q, want %q", d.ClosedEntries[0].Surface.Bay, "w2")
	}
}

// --- Surface operations ---

func TestNextSurfaceID_Empty(t *testing.T) {
	bay := &Bay{}
	if id := bay.NextSurfaceID(); id != 1 {
		t.Errorf("NextSurfaceID() = %d, want 1", id)
	}
}

func TestNextSurfaceID_WithExisting(t *testing.T) {
	bay := &Bay{
		Surfaces: []Surface{{ID: 1}, {ID: 3}},
	}
	if id := bay.NextSurfaceID(); id != 4 {
		t.Errorf("NextSurfaceID() = %d, want 4", id)
	}
}

func TestFindSurface(t *testing.T) {
	bay := &Bay{
		Surfaces: []Surface{{ID: 1, Name: "agent"}, {ID: 2, Name: "shell"}},
	}

	s := bay.FindSurface("agent")
	if s == nil || s.Name != "agent" {
		t.Errorf("FindSurface(agent) = %v", s)
	}

	s = bay.FindSurface("nonexistent")
	if s != nil {
		t.Errorf("FindSurface(nonexistent) = %v, want nil", s)
	}
}

func TestFindSurfaceByID(t *testing.T) {
	bay := &Bay{
		Surfaces: []Surface{{ID: 1, Name: "agent"}, {ID: 2, Name: "shell"}},
	}

	s := bay.FindSurfaceByID(2)
	if s == nil || s.Name != "shell" {
		t.Errorf("FindSurfaceByID(2) = %v", s)
	}

	s = bay.FindSurfaceByID(99)
	if s != nil {
		t.Errorf("FindSurfaceByID(99) = %v, want nil", s)
	}
}

func TestAddSurface(t *testing.T) {
	bay := &Bay{}

	id, err := bay.AddSurface(Surface{
		Name:    "agent",
		Type:    SurfaceTypeAgent,
		Backend: SurfaceBackendTmux,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Errorf("assigned id = %d, want 1", id)
	}
	if len(bay.Surfaces) != 1 || bay.Surfaces[0].ID != 1 {
		t.Errorf("after add: %v", bay.Surfaces)
	}

	// Add a second surface
	id, err = bay.AddSurface(Surface{Name: "shell", Type: SurfaceTypeShell, Backend: SurfaceBackendTmux})
	if err != nil {
		t.Fatal(err)
	}
	if id != 2 {
		t.Errorf("second id = %d, want 2", id)
	}
}

func TestAddSurface_DuplicateName(t *testing.T) {
	bay := &Bay{
		Surfaces: []Surface{{ID: 1, Name: "agent"}},
	}

	_, err := bay.AddSurface(Surface{Name: "agent"})
	if err == nil {
		t.Fatal("expected error for duplicate surface name")
	}
}

func TestAddSurface_IDsNeverReused(t *testing.T) {
	bay := &Bay{
		Surfaces: []Surface{{ID: 1, Name: "agent"}, {ID: 3, Name: "shell"}},
	}

	id, err := bay.AddSurface(Surface{Name: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	if id != 4 {
		t.Errorf("id = %d, want 4 (should not reuse 2)", id)
	}
}

func TestRemoveSurface(t *testing.T) {
	bay := &Bay{
		Surfaces: []Surface{
			{ID: 1, Name: "agent"},
			{ID: 2, Name: "shell"},
			{ID: 3, Name: "editor"},
		},
	}

	if err := bay.RemoveSurface("shell"); err != nil {
		t.Fatal(err)
	}
	if len(bay.Surfaces) != 2 {
		t.Fatalf("surfaces length = %d, want 2", len(bay.Surfaces))
	}
	if bay.Surfaces[0].Name != "agent" || bay.Surfaces[1].Name != "editor" {
		t.Errorf("after remove: %v", bay.Surfaces)
	}
}

func TestRemoveSurface_NotFound(t *testing.T) {
	bay := &Bay{}
	if err := bay.RemoveSurface("nonexistent"); err == nil {
		t.Fatal("expected error for missing surface")
	}
}

// --- Bay resolution ---

func TestResolveBay_ByDockColonID(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{
			{Name: "labs", Bays: []Bay{{ID: "w1", Name: "auth-fix"}}},
		},
	}

	bay, dock, err := m.ResolveBay("labs:w1")
	if err != nil {
		t.Fatal(err)
	}
	if bay.ID != "w1" || dock.Name != "labs" {
		t.Errorf("resolve = bay=%q dock=%q", bay.ID, dock.Name)
	}
}

func TestResolveBay_ByID(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{
			{Name: "labs", Bays: []Bay{{ID: "w1", Name: "auth-fix"}}},
		},
	}

	bay, dock, err := m.ResolveBay("w1")
	if err != nil {
		t.Fatal(err)
	}
	if bay.ID != "w1" || dock.Name != "labs" {
		t.Errorf("resolve = bay=%q dock=%q", bay.ID, dock.Name)
	}
}

// TestResolveBay_ByNameRedirectsToID covers the strict-resolver
// hint: typing a Name returns an error that points at the canonical ID.
func TestResolveBay_ByNameRedirectsToID(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{
			{Name: "labs", Bays: []Bay{{ID: "w1", Name: "auth-fix"}}},
		},
	}
	_, _, err := m.ResolveBay("auth-fix")
	if err == nil {
		t.Fatal("expected error for Name lookup under strict resolution")
	}
	if !strings.Contains(err.Error(), `did you mean "w1"`) {
		t.Errorf("error missing did-you-mean hint: %v", err)
	}
}

func TestResolveBay_AmbiguousName(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{
			{Name: "labs", Bays: []Bay{{Name: "shared"}}},
			{Name: "research", Bays: []Bay{{Name: "shared"}}},
		},
	}

	_, _, err := m.ResolveBay("shared")
	if err == nil {
		t.Fatal("expected error for ambiguous name")
	}
}

func TestResolveBay_NotFound(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{{Name: "labs", Bays: []Bay{{Name: "auth-fix"}}}},
	}

	_, _, err := m.ResolveBay("nonexistent")
	if err == nil {
		t.Fatal("expected error for not found")
	}
}

func TestIsReservedBayID(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{HomeBayID, true},
		{"Home", false},
		{"home1", false},
		{"w1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsReservedBayID(c.s); got != c.want {
			t.Errorf("IsReservedBayID(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestIsHomeBay(t *testing.T) {
	cases := []struct {
		name string
		bay  *Bay
		want bool
	}{
		{name: "nil", bay: nil, want: false},
		{name: "home type", bay: &Bay{Type: BayTypeHome}, want: true},
		{name: "reserved ID", bay: &Bay{ID: HomeBayID, Type: BayTypeWorktree}, want: true},
		{name: "reserved name only", bay: &Bay{Name: HomeBayID, Type: BayTypeWorktree}, want: false},
		{name: "normal bay", bay: &Bay{ID: "w1", Name: "work", Type: BayTypeWorktree}, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsHomeBay(c.bay); got != c.want {
				t.Fatalf("IsHomeBay(%+v) = %v, want %v", c.bay, got, c.want)
			}
		})
	}
}

func TestIsHomeLikeBay(t *testing.T) {
	cases := []struct {
		name string
		bay  *Bay
		want bool
	}{
		{name: "nil", bay: nil, want: false},
		{name: "home type", bay: &Bay{Type: BayTypeHome}, want: true},
		{name: "reserved ID", bay: &Bay{ID: HomeBayID, Type: BayTypeWorktree}, want: true},
		{name: "reserved name only", bay: &Bay{Name: HomeBayID, Type: BayTypeWorktree}, want: true},
		{name: "normal bay", bay: &Bay{ID: "w1", Name: "work", Type: BayTypeWorktree}, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsHomeLikeBay(c.bay); got != c.want {
				t.Fatalf("IsHomeLikeBay(%+v) = %v, want %v", c.bay, got, c.want)
			}
		})
	}
}

func TestResolveBay_HomeWithDockPrefixSynthesizesEmpty(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{
			{Name: "labs", Path: "/projects/labs"},
		},
	}

	bay, dock, err := m.ResolveBay("labs:home")
	if err != nil {
		t.Fatalf("ResolveBay(labs:home): %v", err)
	}
	if dock == nil || dock.Name != "labs" {
		t.Fatalf("dock = %+v, want labs", dock)
	}
	if bay.ID != HomeBayID || bay.Name != HomeBayID {
		t.Errorf("ID/Name = %q/%q, want home/home", bay.ID, bay.Name)
	}
	if bay.Type != BayTypeHome {
		t.Errorf("Type = %q, want %q", bay.Type, BayTypeHome)
	}
	if bay.Path != "/projects/labs" {
		t.Errorf("Path = %q, want /projects/labs", bay.Path)
	}
	if bay.Worktree != nil {
		t.Errorf("Worktree = %+v, want nil", bay.Worktree)
	}
	if bay.Surfaces == nil {
		t.Error("Surfaces is nil, want non-nil empty slice")
	}
}

func TestSynthesizeHomeBay_ShapeIsPhase2Compatible(t *testing.T) {
	d := &Dock{Name: "labs", Path: "/projects/labs"}

	bay := SynthesizeHomeBay(d)
	if bay.ID != HomeBayID || bay.Name != HomeBayID {
		t.Errorf("ID/Name = %q/%q, want home/home", bay.ID, bay.Name)
	}
	if bay.Type != BayTypeHome {
		t.Errorf("Type = %q, want %q", bay.Type, BayTypeHome)
	}
	if bay.Path != "/projects/labs" {
		t.Errorf("Path = %q, want /projects/labs", bay.Path)
	}
	if bay.Worktree != nil {
		t.Errorf("Worktree = %+v, want nil", bay.Worktree)
	}
	if bay.Surfaces == nil || len(bay.Surfaces) != 0 {
		t.Errorf("Surfaces = %+v, want non-nil empty slice", bay.Surfaces)
	}
}

func TestResolveBay_HomePersistedWins(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{
			{
				Name: "labs",
				Path: "/projects/labs",
				Bays: []Bay{
					{
						ID:   HomeBayID,
						Name: HomeBayID,
						Type: BayTypeHome,
						Path: "/projects/labs",
						Surfaces: []Surface{
							{ID: 7, Name: "shell", Type: SurfaceTypeShell, Backend: SurfaceBackendTmux, Tmux: &TmuxAttrs{}},
						},
					},
				},
			},
		},
	}

	bay, _, err := m.ResolveBay("labs:home")
	if err != nil {
		t.Fatalf("ResolveBay(labs:home): %v", err)
	}
	if len(bay.Surfaces) != 1 || bay.Surfaces[0].ID != 7 {
		t.Fatalf("got synthesized or wrong home bay: %+v", bay)
	}
}

func TestResolveBay_HomeConflictingManifestFailsClearly(t *testing.T) {
	tests := []struct {
		name string
		dock Dock
		want string
	}{
		{
			name: "reserved ID used by worktree",
			dock: Dock{Name: "labs", Path: "/projects/labs", Bays: []Bay{
				{ID: HomeBayID, Name: "bad", Type: BayTypeWorktree},
			}},
			want: "non-home bay with reserved ID",
		},
		{
			name: "reserved name used by normal bay",
			dock: Dock{Name: "labs", Path: "/projects/labs", Bays: []Bay{
				{ID: "w1", Name: HomeBayID, Type: BayTypeExternal},
			}},
			want: "non-home bay named",
		},
		{
			name: "home path differs from dock path",
			dock: Dock{Name: "labs", Path: "/projects/labs", Bays: []Bay{
				{ID: HomeBayID, Name: HomeBayID, Type: BayTypeHome, Path: "/projects/other"},
			}},
			want: "expected dock path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manifest{Docks: []Dock{tt.dock}}
			_, _, err := m.ResolveBay("labs:home")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ResolveBay(labs:home) error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestResolveBay_BareHomeRequiresDock(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{
			{Name: "labs", Path: "/projects/labs"},
			{Name: "docs", Path: "/projects/docs"},
		},
	}

	_, _, err := m.ResolveBay(HomeBayID)
	if err == nil || !strings.Contains(err.Error(), "dock context") {
		t.Fatalf("ResolveBay(home) error = %v, want dock-context guidance", err)
	}
}

// --- AllBays ---

func TestAllBays(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{
			{Name: "research", Bays: []Bay{{Name: "beta"}, {Name: "alpha"}}},
			{Name: "labs", Bays: []Bay{{Name: "bay1"}}},
		},
	}

	refs := AllBays(m)
	if len(refs) != 3 {
		t.Fatalf("AllBays length = %d, want 3", len(refs))
	}
	// Sorted by dock then bay name
	if refs[0].Dock != "labs" || refs[0].Bay.Name != "bay1" {
		t.Errorf("refs[0] = %q/%q", refs[0].Dock, refs[0].Bay.Name)
	}
	if refs[1].Dock != "research" || refs[1].Bay.Name != "alpha" {
		t.Errorf("refs[1] = %q/%q", refs[1].Dock, refs[1].Bay.Name)
	}
	if refs[2].Dock != "research" || refs[2].Bay.Name != "beta" {
		t.Errorf("refs[2] = %q/%q", refs[2].Dock, refs[2].Bay.Name)
	}
}

func TestAllBays_ReturnsMutablePointers(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{
			{Name: "labs", Bays: []Bay{{Name: "bay1"}}},
		},
	}

	refs := AllBays(m)
	refs[0].Bay.Name = "bay1-renamed"

	if m.Docks[0].Bays[0].Name != "bay1-renamed" {
		t.Error("mutation through AllBays pointer did not affect manifest")
	}
}

// --- LockedUpdate atomic write ---

func TestLockedUpdate_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	// First update — no backup expected
	err := LockedUpdate(path, func(m *Manifest) error {
		m.Docks = append(m.Docks, Dock{Name: "first"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	backups, _ := ListBackups(path)
	if len(backups) != 0 {
		t.Error("no backups should exist after first LockedUpdate")
	}

	// Second update — backup should be created
	err = LockedUpdate(path, func(m *Manifest) error {
		m.Docks[0].Name = "second"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	backups, _ = ListBackups(path)
	if len(backups) == 0 {
		t.Error("backup should exist after second LockedUpdate")
	}

	// Verify the temp file was cleaned up
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file should not exist after LockedUpdate")
	}
}

// --- Validation ---

func TestSurface_Validate_Valid(t *testing.T) {
	agent := "claude-code"
	s := Surface{
		Name:    "agent",
		Type:    SurfaceTypeAgent,
		Backend: SurfaceBackendTmux,
		Agent:   &agent,
		Tmux:    &TmuxAttrs{LayoutGroup: 1},
	}
	if errs := s.Validate(); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestSurface_Validate_MissingTmuxAttrs(t *testing.T) {
	s := Surface{Name: "shell", Type: SurfaceTypeShell, Backend: SurfaceBackendTmux}
	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %v", errs)
	}
}

func TestSurface_Validate_MissingGUIAttrs(t *testing.T) {
	s := Surface{Name: "editor", Type: SurfaceTypeEditor, Backend: SurfaceBackendGUI}
	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %v", errs)
	}
}

func TestSurface_Validate_BothBackendAttrs(t *testing.T) {
	s := Surface{
		Name:    "bad",
		Type:    SurfaceTypeShell,
		Backend: SurfaceBackendTmux,
		Tmux:    &TmuxAttrs{},
		GUI:     &GUIAttrs{AppCommand: "cursor"},
	}
	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %v", errs)
	}
}

func TestSurface_Validate_AgentMissing(t *testing.T) {
	s := Surface{Name: "agent", Type: SurfaceTypeAgent, Backend: SurfaceBackendTmux, Tmux: &TmuxAttrs{}}
	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %v", errs)
	}
}

func TestSurface_Validate_AgentOnWrongType(t *testing.T) {
	agent := "claude"
	s := Surface{Name: "shell", Type: SurfaceTypeShell, Backend: SurfaceBackendTmux, Tmux: &TmuxAttrs{}, Agent: &agent}
	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %v", errs)
	}
}

func TestResolveBay_DockColonNotFound(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{{Name: "labs", Bays: []Bay{{Name: "auth-fix"}}}},
	}

	_, _, err := m.ResolveBay("labs:nonexistent")
	if err == nil {
		t.Fatal("expected error for not found")
	}

	_, _, err = m.ResolveBay("baddock:auth-fix")
	if err == nil {
		t.Fatal("expected error for bad dock")
	}
}

func TestDockCheckoutAndAgentRoundTrip(t *testing.T) {
	original := &Manifest{
		Version: CurrentVersion,
		Docks: []Dock{
			{
				Name:      "dev",
				Path:      "/p/labs",
				Agent:     "claude",
				AgentArgs: map[string][]string{"claude": {"--add-dir", "/extra"}},
				Bays:      []Bay{},
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	dock := restored.FindDock("dev")
	if dock == nil {
		t.Fatal("dock not found")
	}
	if dock.Path != "/p/labs" {
		t.Errorf("dock.Path = %q, want /p/labs", dock.Path)
	}
	if dock.Agent != "claude" {
		t.Errorf("dock.Agent = %q, want claude", dock.Agent)
	}
	if len(dock.AgentArgs["claude"]) != 2 || dock.AgentArgs["claude"][0] != "--add-dir" {
		t.Errorf("dock.AgentArgs[claude] = %v, want [--add-dir /extra]", dock.AgentArgs["claude"])
	}
}

func TestParse_MigratesV5ReposToDockCheckouts(t *testing.T) {
	data := []byte(`{
		"version": 5,
		"repos": [{"name": "labs", "path": "/p/labs", "worktree_dir": "/wt/labs"}],
		"docks": [
			{
				"name": "dev",
				"repo": "labs",
				"bays": [
					{
						"id": "w1",
						"name": "auth",
						"type": "worktree",
						"path": "/wt/labs/w1",
						"worktree": {"repo": "labs", "branch": "fix/auth"}
					}
				]
			}
		]
	}`)

	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", m.Version, CurrentVersion)
	}
	if len(m.Repos) != 0 {
		t.Errorf("repos = %v, want empty after v6 migration", m.Repos)
	}
	dock := m.FindDock("dev")
	if dock == nil {
		t.Fatal("dock not found")
	}
	if dock.Path != "/p/labs" || dock.WorktreeDir != "/wt/labs" || dock.Repo != "" {
		t.Fatalf("dock migration mismatch: %#v", dock)
	}
	bay := dock.FindBayByID("w1")
	if bay == nil || bay.Worktree == nil {
		t.Fatalf("bay not migrated: %#v", dock.Bays)
	}
	if bay.Worktree.Repo != "" {
		t.Errorf("worktree repo = %q, want empty after v6 migration", bay.Worktree.Repo)
	}
}

func TestParse_MigratesV5RejectsSharedRepo(t *testing.T) {
	data := []byte(`{
		"version": 5,
		"repos": [{"name": "labs", "path": "/p/labs"}],
		"docks": [
			{"name": "dev", "repo": "labs", "bays": []},
			{"name": "ops", "repo": "labs", "bays": []}
		]
	}`)

	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected shared repo migration error")
	}
	if !strings.Contains(err.Error(), "multiple docks share one repo") {
		t.Fatalf("error = %q, want shared repo message", err)
	}
}

func TestParse_MigratesV5RejectsCrossRepoBay(t *testing.T) {
	data := []byte(`{
		"version": 5,
		"repos": [
			{"name": "labs", "path": "/p/labs"},
			{"name": "other", "path": "/p/other"}
		],
		"docks": [
			{
				"name": "dev",
				"repo": "labs",
				"bays": [
					{
						"id": "w1",
						"name": "api",
						"type": "worktree",
						"path": "/p/other-worktrees/w1",
						"worktree": {"repo": "other", "branch": "fix/api"}
					}
				]
			}
		]
	}`)

	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected cross-repo bay migration error")
	}
	if !strings.Contains(err.Error(), "cross-repo bay dev:w1 uses repo \"other\" but dock uses repo \"labs\"") {
		t.Fatalf("error = %q, want cross-repo bay message", err)
	}
}

func TestParse_MigratesV2StatusDoneToMerged(t *testing.T) {
	data := []byte(`{
		"version": 2,
		"repos": [{"name": "labs", "path": "/repo"}],
		"docks": [
			{
				"name": "labs",
				"repo": "labs",
				"bays": [
					{
						"name": "bay-done",
						"type": "worktree",
						"path": "/tmp/bay1",
						"status": "done",
						"worktree": {"repo": "labs", "branch": "feat-x"},
						"surfaces": []
					},
					{
						"name": "bay-active",
						"type": "worktree",
						"path": "/tmp/bay2",
						"status": "active",
						"worktree": {"repo": "labs", "branch": "feat-y"},
						"surfaces": []
					},
					{
						"name": "bay-no-worktree",
						"type": "external",
						"path": "/tmp/bay3",
						"status": "done",
						"surfaces": []
					}
				]
			}
		]
	}`)

	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	bay0 := &m.Docks[0].Bays[0]
	if bay0.Worktree == nil || !bay0.Worktree.Merged {
		t.Errorf("bay-done: expected Merged=true, got Merged=%v", bay0.Worktree != nil && bay0.Worktree.Merged)
	}

	bay1 := &m.Docks[0].Bays[1]
	if bay1.Worktree == nil || bay1.Worktree.Merged {
		t.Errorf("bay-active: expected Merged=false, got Merged=%v", bay1.Worktree != nil && bay1.Worktree.Merged)
	}

	// External bay with status=done but no worktree — Merged should not be set
	bay2 := &m.Docks[0].Bays[2]
	if bay2.Worktree != nil {
		t.Errorf("bay-no-worktree: expected no worktree attrs")
	}

	if m.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", m.Version, CurrentVersion)
	}
}

func TestIsBayID(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"b1", true},
		{"b42", true},
		{"b999", true},
		{"w1", true},
		{"w42", true},
		{"w999", true},
		{"", false},
		{"b", false},
		{"b0", false},  // n must be >= 1
		{"b01", false}, // leading zero rejected
		{"B1", false},  // case-sensitive
		{"b1a", false}, // trailing non-digit
		{"w", false},
		{"w0", false},  // n must be >= 1
		{"w01", false}, // leading zero rejected
		{"W1", false},  // case-sensitive
		{"w1a", false}, // trailing non-digit
		{"v1", false},  // wrong prefix
		{"auth-fix", false},
		{"w-1", false},
		{"w+1", false}, // strconv.Atoi accepts +/- prefixes; we don't
		{"w1.5", false},
	}
	for _, c := range cases {
		if got := IsBayID(c.s); got != c.want {
			t.Errorf("IsBayID(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

// TestParse_FillsMissingIDsFromPathBasename covers the common legacy
// case: a v3 manifest where every bay's path is already w<N>-shaped
// (because bay's worktree dirs have always been). Each bay's ID
// should equal its path basename — no renumbering, no surprises.
func TestParse_FillsMissingIDsFromPathBasename(t *testing.T) {
	data := []byte(`{
		"version": 3,
		"repos": [{"name": "labs", "path": "/repo"}],
		"docks": [
			{
				"name": "labs",
				"repo": "labs",
				"bays": [
					{"name": "auth-fix", "type": "worktree", "path": "/repo-worktrees/w1", "surfaces": []},
					{"name": "cache-ttl", "type": "worktree", "path": "/repo-worktrees/w2", "surfaces": []},
					{"name": "api-log",  "type": "worktree", "path": "/repo-worktrees/w5", "surfaces": []}
				]
			}
		]
	}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, bay := range m.Docks[0].Bays {
		got = append(got, bay.ID)
	}
	want := []string{"w1", "w2", "w5"}
	if !slices.Equal(got, want) {
		t.Errorf("IDs = %v, want %v", got, want)
	}
}

// TestParse_AssignsSequentialIDsForUnusablePaths covers external
// bays (or any with non-generated path basenames): they fall through
// to pass 2 and get the next available b<N> ID. Legacy w<N> claims do
// not advance the b<N> sequence.
func TestParse_AssignsSequentialIDsForUnusablePaths(t *testing.T) {
	data := []byte(`{
		"version": 3,
		"repos": [{"name": "mixed", "path": "/repo"}],
		"docks": [
			{
				"name": "mixed",
				"repo": "mixed",
				"bays": [
					{"name": "wt", "type": "worktree", "path": "/repo-worktrees/w3", "surfaces": []},
					{"name": "ext1", "type": "external", "path": "/Users/me/projects/foo", "surfaces": []},
					{"name": "ext2", "type": "external", "path": "/Users/me/projects/bar", "surfaces": []}
				]
			}
		]
	}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	// wt claims legacy w3 from its path; externals get current b1, b2.
	got := []string{}
	for _, bay := range m.Docks[0].Bays {
		got = append(got, bay.ID)
	}
	want := []string{"w3", "b1", "b2"}
	if !slices.Equal(got, want) {
		t.Errorf("IDs = %v, want %v", got, want)
	}
}

func TestParse_FillsMissingIDsFromCurrentPathBasename(t *testing.T) {
	data := []byte(`{
		"version": 6,
		"docks": [
			{
				"name": "labs",
				"path": "/repo",
				"bays": [
					{"name": "auth-fix", "type": "worktree", "path": "/repo-worktrees/b1", "surfaces": []},
					{"name": "cache-ttl", "type": "worktree", "path": "/repo-worktrees/b3", "surfaces": []}
				]
			}
		]
	}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, bay := range m.Docks[0].Bays {
		got = append(got, bay.ID)
	}
	want := []string{"b1", "b3"}
	if !slices.Equal(got, want) {
		t.Errorf("IDs = %v, want %v", got, want)
	}
}

// TestParse_HandlesIDCollisions covers the pathological case where two
// bays claim the same w<N> path basename (shouldn't normally
// happen, but guard against it). First one to be processed wins; the
// second falls through to sequential assignment.
func TestParse_HandlesIDCollisions(t *testing.T) {
	data := []byte(`{
		"version": 3,
		"repos": [{"name": "labs", "path": "/repo"}],
		"docks": [
			{
				"name": "labs",
				"repo": "labs",
				"bays": [
					{"name": "a", "type": "worktree", "path": "/x/w1", "surfaces": []},
					{"name": "b", "type": "worktree", "path": "/x/w1", "surfaces": []}
				]
			}
		]
	}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Docks[0].Bays[0].ID != "w1" {
		t.Errorf("bay[0].ID = %q, want w1", m.Docks[0].Bays[0].ID)
	}
	if m.Docks[0].Bays[1].ID != "b1" {
		t.Errorf("bay[1].ID = %q, want b1 (collision fallback)", m.Docks[0].Bays[1].ID)
	}
}

// TestParse_PreservesExistingIDs ensures Parse never overwrites an ID
// already present in the manifest. Migration is fill-only, idempotent.
func TestParse_PreservesExistingIDs(t *testing.T) {
	data := []byte(`{
		"version": 4,
		"repos": [{"name": "labs", "path": "/repo"}],
		"docks": [
			{
				"name": "labs",
				"repo": "labs",
				"bays": [
					{"id": "w7", "name": "a", "type": "worktree", "path": "/x/w1", "surfaces": []}
				]
			}
		]
	}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Docks[0].Bays[0].ID != "w7" {
		t.Errorf("ID = %q, want w7 (preserved)", m.Docks[0].Bays[0].ID)
	}
}

// TestParse_BumpsToV4 confirms a v3 manifest is upgraded to the current
// version after the ID-fill migration runs.
func TestParse_BumpsToV4(t *testing.T) {
	data := []byte(`{"version": 3, "repos": [], "docks": []}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", m.Version, CurrentVersion)
	}
}

// TestFindBayByID confirms ID lookups: empty input always returns
// nil, a hit returns a pointer into the dock's slice, a miss returns nil.
func TestFindBayByID(t *testing.T) {
	dock := &Dock{
		Name: "labs",
		Bays: []Bay{
			{ID: "w1", Name: "auth-fix"},
			{ID: "w3", Name: "cache-ttl"},
		},
	}
	if dock.FindBayByID("") != nil {
		t.Error("FindBayByID(\"\") should return nil")
	}
	if dock.FindBayByID("w99") != nil {
		t.Error("FindBayByID for missing ID should return nil")
	}
	hit := dock.FindBayByID("w3")
	if hit == nil || hit.Name != "cache-ttl" {
		t.Errorf("FindBayByID(\"w3\"): got %+v, want bay with Name cache-ttl", hit)
	}
	// Confirm the returned pointer aliases the slice element.
	hit.Description = "touched"
	if dock.Bays[1].Description != "touched" {
		t.Error("FindBayByID should return a pointer into the slice, not a copy")
	}
}

// TestSaveAndLoad_PreservesID confirms IDs round-trip through the
// JSON encoder/decoder without modification.
func TestSaveAndLoad_PreservesID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	original := New()
	original.Docks = append(original.Docks, Dock{
		Name: "test",
		Bays: []Bay{
			{ID: "w3", Name: "alpha", Type: BayTypeWorktree, Path: "/repo-worktrees/w3"},
		},
	})
	if err := Save(path, original); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Docks[0].Bays[0].ID != "w3" {
		t.Errorf("ID after round-trip = %q, want w3", loaded.Docks[0].Bays[0].ID)
	}
}

func TestDockEffectiveWorktreeDir(t *testing.T) {
	// Explicit worktree_dir
	d := Dock{Name: "labs", Path: "/projects/labs", WorktreeDir: "/custom/worktrees"}
	if got := d.EffectiveWorktreeDir(); got != "/custom/worktrees" {
		t.Errorf("expected /custom/worktrees, got %q", got)
	}

	// Default: path + "-worktrees"
	d2 := Dock{Name: "labs", Path: "/projects/labs"}
	if got := d2.EffectiveWorktreeDir(); got != "/projects/labs-worktrees" {
		t.Errorf("expected /projects/labs-worktrees, got %q", got)
	}
}

// TestResolveBay_NameHintMultipleDocks covers the cross-dock
// hint format when the same Name appears in multiple docks.
func TestResolveBay_NameHintMultipleDocks(t *testing.T) {
	m := &Manifest{
		Docks: []Dock{
			{Name: "labs", Bays: []Bay{{ID: "w1", Name: "shared"}}},
			{Name: "labs2", Bays: []Bay{{ID: "w1", Name: "shared"}}},
		},
	}
	_, _, err := m.ResolveBay("shared")
	if err == nil {
		t.Fatal("expected error for multi-dock Name lookup")
	}
	msg := err.Error()
	if !strings.Contains(msg, `did you mean one of`) ||
		!strings.Contains(msg, `"w1" (labs)`) ||
		!strings.Contains(msg, `"w1" (labs2)`) {
		t.Errorf("expected multi-dock hint listing both candidates, got %v", err)
	}
}

func TestDescribeStatus_FailureStreakAndReset(t *testing.T) {
	m := New()
	if m.Describe != nil {
		t.Fatalf("new manifest has describe status %+v, want nil", m.Describe)
	}

	m.RecordDescribeFailure(100, "first")
	m.RecordDescribeFailure(200, "second")
	got := *m.Describe
	want := DescribeStatus{ConsecutiveFailures: 2, FailingSince: 100, LastFailureAt: 200, LastError: "second"}
	if got != want {
		t.Fatalf("after two failures = %+v, want %+v", got, want)
	}

	m.RecordDescribeSuccess(300)
	if *m.Describe != (DescribeStatus{LastSuccessAt: 300}) {
		t.Fatalf("after success = %+v, want only LastSuccessAt=300", *m.Describe)
	}

	// A failure after a success starts a new streak, and keeps the success.
	m.RecordDescribeFailure(400, "again")
	want = DescribeStatus{LastSuccessAt: 300, ConsecutiveFailures: 1, FailingSince: 400, LastFailureAt: 400, LastError: "again"}
	if *m.Describe != want {
		t.Fatalf("new streak = %+v, want %+v", *m.Describe, want)
	}
}

func TestDescribeStatus_JSON(t *testing.T) {
	data, err := json.Marshal(New())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "describe_status") {
		t.Errorf("empty manifest serialized describe_status: %s", data)
	}

	m := New()
	m.RecordDescribeFailure(100, "boom")
	data, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Describe == nil || *parsed.Describe != *m.Describe {
		t.Errorf("round trip = %+v, want %+v", parsed.Describe, m.Describe)
	}
}
