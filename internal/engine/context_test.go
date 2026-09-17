package engine

import (
	"testing"

	"github.com/commonfabric/bay/internal/manifest"
)

// TestContextBayLabel pins the canonical-label rules used everywhere
// human-facing in the CLI (picker, ls, pwd, status line): never blank
// for a bay with either ID or Name, dotted form when both are set, and
// "home" for the home pseudo-bay regardless of which field carries the
// reserved value.
func TestContextBayLabel(t *testing.T) {
	tests := []struct {
		name string
		ctx  Context
		want string
	}{
		{"unnamed bay falls back to ID", Context{BayID: "b2"}, "b2"},
		{"named bay uses dotted form", Context{BayID: "b1", Bay: "auth-fix"}, "b1.auth-fix"},
		{"name matching ID is collapsed", Context{BayID: "b1", Bay: "b1"}, "b1"},
		{"home via BayID", Context{BayID: manifest.HomeBayID}, manifest.HomeBayID},
		{"home via Bay", Context{Bay: manifest.HomeBayID}, manifest.HomeBayID},
		{"legacy: no ID", Context{Bay: "auth-fix"}, "auth-fix"},
		{"no bay returns empty", Context{Dock: "labs"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ctx.BayLabel(); got != tt.want {
				t.Fatalf("BayLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBayInfoLabel mirrors TestContextBayLabel for the engine.BayInfo
// surface used by `bay ls`, `tree`, and `show`. The label is derived
// from the path-backed dir tag and the optional Name.
func TestBayInfoLabel(t *testing.T) {
	tests := []struct {
		name string
		info BayInfo
		want string
	}{
		{"unnamed worktree falls back to dir tag", BayInfo{Path: "/wt/b2"}, "b2"},
		{"named worktree uses dotted form", BayInfo{Name: "auth-fix", Path: "/wt/b1"}, "b1.auth-fix"},
		{"name matching dir is collapsed", BayInfo{Name: "b1", Path: "/wt/b1"}, "b1"},
		{"home type short-circuits", BayInfo{Type: string(manifest.BayTypeHome), Name: manifest.HomeBayID, Path: "/repo"}, manifest.HomeBayID},
		{"no path falls back to name", BayInfo{Name: "auth-fix"}, "auth-fix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.Label(); got != tt.want {
				t.Fatalf("Label() = %q, want %q", got, tt.want)
			}
		})
	}
}
