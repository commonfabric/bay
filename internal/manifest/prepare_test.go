package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse_MigratesV6ToV7AddsPrepareState(t *testing.T) {
	data := []byte(`{
		"version": 6,
		"docks": [
			{
				"name": "loom",
				"path": "/repo/loom",
				"bays": [
					{"id": "b1", "name": "vendor-fix", "type": "worktree", "surfaces": []}
				]
			}
		]
	}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != CurrentVersion {
		t.Fatalf("version = %d, want %d", m.Version, CurrentVersion)
	}
	dock := m.FindDock("loom")
	if dock == nil {
		t.Fatal("dock not found")
	}
	if dock.TrustPromptDismissed {
		t.Fatal("TrustPromptDismissed = true, want false after migration")
	}
	bay := dock.FindBayByID("b1")
	if bay == nil {
		t.Fatal("bay not found")
	}
	if bay.Prepare == nil || len(bay.Prepare) != 0 {
		t.Fatalf("Prepare = %#v, want empty non-nil slice", bay.Prepare)
	}
	if bay.PendingSurfaces == nil || len(bay.PendingSurfaces) != 0 {
		t.Fatalf("PendingSurfaces = %#v, want empty non-nil slice", bay.PendingSurfaces)
	}

	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal migrated manifest: %v", err)
	}
	roundTripped, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse migrated manifest round-trip: %v", err)
	}
	if roundTripped.Version != CurrentVersion {
		t.Fatalf("round-trip version = %d, want %d", roundTripped.Version, CurrentVersion)
	}
}

func TestSave_WritesV7EmptyPrepareArrays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	m := &Manifest{
		Version: CurrentVersion,
		Docks: []Dock{
			{
				Name: "loom",
				Bays: []Bay{
					{ID: "b1", Name: "vendor-fix", Type: BayTypeWorktree, Surfaces: []Surface{}},
				},
			},
		},
	}
	if err := save(path, m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"prepare": []`) {
		t.Fatalf("saved manifest missing empty prepare array:\n%s", text)
	}
	if !strings.Contains(text, `"pending_surfaces": []`) {
		t.Fatalf("saved manifest missing empty pending_surfaces array:\n%s", text)
	}
}

func TestParse_V7PrepareStateRoundTrip(t *testing.T) {
	data := []byte(`{
		"version": 7,
		"docks": [
			{
				"name": "loom",
				"trust_prompt_dismissed": true,
				"bays": [
					{
						"id": "b1",
						"name": "vendor-fix",
						"type": "worktree",
						"surfaces": [],
						"prepare": [
							{
								"name": "vendors",
								"status": "ready",
								"started_at": 1777248000,
								"finished_at": 1777248030,
								"heartbeat_at": 1777248010,
								"pid": 1234,
								"definition_hash": "sha256:abc",
								"run_log_offset": 42
							}
						],
						"pending_surfaces": [
							{
								"created_at": 1777248040,
								"kind": "agent",
								"name": "codex",
								"agent": "codex",
								"split_dir": "v",
								"tmux": {"pane_id": "%1", "window_id": "@2", "layout_group": 1}
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
	dock := m.FindDock("loom")
	if dock == nil || !dock.TrustPromptDismissed {
		t.Fatalf("dock trust_prompt_dismissed not parsed: %#v", dock)
	}
	bay := dock.FindBayByID("b1")
	if bay == nil {
		t.Fatal("bay not found")
	}
	if len(bay.Prepare) != 1 {
		t.Fatalf("Prepare len = %d, want 1", len(bay.Prepare))
	}
	step := bay.Prepare[0]
	if step.Name != "vendors" || step.Status != PrepareStatusReady || step.PID != 1234 || step.RunLogOffset != 42 {
		t.Fatalf("prepare step parsed incorrectly: %#v", step)
	}
	if len(bay.PendingSurfaces) != 1 {
		t.Fatalf("PendingSurfaces len = %d, want 1", len(bay.PendingSurfaces))
	}
	pending := bay.PendingSurfaces[0]
	if pending.Kind != SurfaceTypeAgent || pending.Agent != "codex" || pending.Tmux == nil || pending.Tmux.PaneID != "%1" {
		t.Fatalf("pending launch parsed incorrectly: %#v", pending)
	}
}
