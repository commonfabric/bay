package cli

import (
	"testing"

	"github.com/commonfabric/bay/internal/manifest"
)

func TestDockOfWindowPrefersADockSessionAndElseFindsTheWindowsDock(t *testing.T) {
	m := manifest.New()
	for _, d := range []manifest.Dock{
		{Name: "loom", Bays: []manifest.Bay{{ID: "b7", Surfaces: []manifest.Surface{
			{ID: 1, Tmux: &manifest.TmuxAttrs{WindowID: "@7"}},
			{ID: 2}, // a surface with no tmux window
		}}}},
		{Name: "bay", Bays: []manifest.Bay{{ID: "b8", Surfaces: []manifest.Surface{
			{ID: 1, Tmux: &manifest.TmuxAttrs{WindowID: "@8"}},
		}}}},
	} {
		if err := m.AddDock(d); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ session, window, want string }{
		{"docket-aux", "@7", "loom"},        // a bay window shown in another session
		{"docket-aux", "@8", "bay"},         //
		{"docket-aux", "@99", "docket-aux"}, // a window no dock has
		{"bay", "@7", "bay"},                // a dock session is never overridden
		{"loom", "@7", "loom"},
	} {
		if got := dockOfWindow(m, c.session, c.window); got != c.want {
			t.Errorf("dockOfWindow(%q, %q) = %q, want %q", c.session, c.window, got, c.want)
		}
	}
}
