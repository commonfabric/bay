package cli

import (
	"testing"

	"github.com/commonfabric/bay/internal/git"
	"github.com/commonfabric/bay/internal/manifest"
)

func statusLineTestBay() *manifest.Bay {
	return &manifest.Bay{Name: "auth-fix", Path: "/tmp/bay-worktrees/b4"}
}

func TestFormatStatusLine_NoWidth(t *testing.T) {
	tests := []struct {
		name   string
		bay    *manifest.Bay
		branch string
		pr     string
		status string
		want   string
	}{
		{"full", statusLineTestBay(), "feature/auth-fix", "42", "dirty", "b4.auth-fix feature/auth-fix #42 dirty"},
		{"no pr", statusLineTestBay(), "feature/auth-fix", "", "dirty", "b4.auth-fix feature/auth-fix dirty"},
		{"clean", statusLineTestBay(), "feature/auth-fix", "42", "", "b4.auth-fix feature/auth-fix #42"},
		{"merged", statusLineTestBay(), "main", "", "merged", "b4.auth-fix main merged"},
		{"no branch", statusLineTestBay(), "", "", "", "b4.auth-fix"},
		{"dir tag only", &manifest.Bay{Name: "", Path: "/tmp/bay-worktrees/b4"}, "", "", "", "b4"},
		{"empty", &manifest.Bay{}, "", "", "", ""},
		{"status only", &manifest.Bay{}, "", "", "dirty", "dirty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStatusLine(tt.bay, tt.branch, tt.pr, tt.status, 0)
			if got != tt.want {
				t.Errorf("formatStatusLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatStatusLine_WithWidth(t *testing.T) {
	tests := []struct {
		name   string
		bay    *manifest.Bay
		branch string
		pr     string
		status string
		width  int
		want   string
	}{
		{
			"fits in width",
			statusLineTestBay(), "main", "42", "dirty",
			40,
			"b4.auth-fix main #42 dirty",
		},
		{
			"crops label before branch",
			statusLineTestBay(), "feature/auth-fix", "42", "dirty",
			34,
			"b4.auth feature/auth-fix #42 dirty",
		},
		{
			"truncates branch after label",
			statusLineTestBay(), "feature/auth-fix", "42", "dirty",
			24,
			"b4.auth-f fe.. #42 dirty",
		},
		{
			"abbreviates status after branch",
			statusLineTestBay(), "feature/auth-fix", "42", "dirty",
			16,
			"b4.au fe.. #42 *",
		},
		{
			"very tight keeps dir tag",
			statusLineTestBay(), "fix/x", "", "dirty",
			2,
			"b4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStatusLine(tt.bay, tt.branch, tt.pr, tt.status, tt.width)
			if got != tt.want {
				t.Errorf("formatStatusLine(%d) = %q, want %q", tt.width, got, tt.want)
			}
			if tt.width > 0 && len(got) > tt.width {
				t.Errorf("output %q exceeds width %d (len=%d)", got, tt.width, len(got))
			}
		})
	}
}

func TestFormatStatusLine_EmptyBranch(t *testing.T) {
	tests := []struct {
		name   string
		bay    *manifest.Bay
		status string
		width  int
		want   string
	}{
		{"label + dirty fits", statusLineTestBay(), "dirty", 20, "b4.auth-fix dirty"},
		{"label cropped with status", statusLineTestBay(), "dirty", 11, "b4.au dirty"},
		{"label cropped tight", statusLineTestBay(), "dirty", 7, "b4.au *"},
		{"dir tag only", statusLineTestBay(), "dirty", 2, "b4"},
		{"no status, no label", &manifest.Bay{}, "", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStatusLine(tt.bay, "", "", tt.status, tt.width)
			if got != tt.want {
				t.Errorf("formatStatusLine(repo=%q, status=%q, width=%d) = %q, want %q",
					tt.bay.Name, tt.status, tt.width, got, tt.want)
			}
			if tt.width > 0 && len(got) > tt.width {
				t.Errorf("output %q exceeds width %d", got, tt.width)
			}
		})
	}
}

func TestAbbreviateStatus(t *testing.T) {
	if got := abbreviateStatus("dirty"); got != "*" {
		t.Errorf("dirty = %q, want *", got)
	}
	if got := abbreviateStatus("merged"); got != "M" {
		t.Errorf("merged = %q, want M", got)
	}
	if got := abbreviateStatus(""); got != "" {
		t.Errorf("empty = %q, want empty", got)
	}
}

func TestStatusLineOutput_HomeSkipsWorktreeMetadataAndGitChecks(t *testing.T) {
	m := &manifest.Manifest{Docks: []manifest.Dock{{
		Name: "labs",
		Bays: []manifest.Bay{
			{
				ID:       manifest.HomeBayID,
				Name:     manifest.HomeBayID,
				Type:     manifest.BayTypeHome,
				Path:     "/repo/labs",
				Worktree: &manifest.WorktreeAttrs{Branch: "feature/not-home", PR: "99", Merged: true},
			},
			{
				ID:       "b1",
				Name:     "done",
				Path:     "/repo/worktrees/b1",
				Worktree: &manifest.WorktreeAttrs{Merged: true},
			},
		},
	}}}
	home := &m.Docks[0].Bays[0]
	g := git.NewMock()
	g.SetDirty(home.Path, true)

	tests := []struct {
		field string
		want  string
	}{
		{"branch", ""},
		{"pr", ""},
		{"status", ""},
		{"full", "home"},
		{"merged", "1 merged"},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			got, err := statusLineOutput(tt.field, "labs", home, m, g, "")
			if err != nil {
				t.Fatalf("statusLineOutput: %v", err)
			}
			if got != tt.want {
				t.Fatalf("statusLineOutput(%q) = %q, want %q", tt.field, got, tt.want)
			}
		})
	}
	if calls := g.Calls("IsDirty"); len(calls) != 0 {
		t.Fatalf("home status line checked git dirty state: %+v", calls)
	}
}

// TestStatusLineOutput_NameRendersCanonicalLabel pins that the `name`
// field follows the unified labeling: dotted "<id>.<name>" for named
// bays, bare ID for unnamed ones, "home" for the home bay. Matches the
// picker and `bay ls` so users see the same string everywhere.
func TestStatusLineOutput_NameRendersCanonicalLabel(t *testing.T) {
	m := &manifest.Manifest{Docks: []manifest.Dock{{
		Name: "labs",
		Bays: []manifest.Bay{
			{ID: "b1", Name: "auth-fix", Path: "/wt/b1"},
			{ID: "b2", Name: "", Path: "/wt/b2"},
			{ID: manifest.HomeBayID, Name: manifest.HomeBayID, Type: manifest.BayTypeHome, Path: "/repo/labs"},
		},
	}}}
	g := git.NewMock()
	tests := []struct {
		bayIdx int
		want   string
	}{
		{0, "b1.auth-fix"},
		{1, "b2"},
		{2, "home"},
	}
	for _, tt := range tests {
		bay := &m.Docks[0].Bays[tt.bayIdx]
		got, err := statusLineOutput("name", "labs", bay, m, g, "")
		if err != nil {
			t.Fatalf("statusLineOutput: %v", err)
		}
		if got != tt.want {
			t.Errorf("name(bay %d) = %q, want %q", tt.bayIdx, got, tt.want)
		}
	}
}
