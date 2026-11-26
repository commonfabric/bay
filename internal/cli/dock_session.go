package cli

import (
	"github.com/commonfabric/bay/internal/manifest"
	tmuxpkg "github.com/commonfabric/bay/internal/tmux"
)

// dockTmux answers CurrentSession with the dock that owns the current
// window. A bay's window can be shown in a tmux session that is not its
// dock, as the docket view does by linking the window into its own
// session. Commands run from such a session act on the window's dock.
type dockTmux struct {
	tmuxpkg.Interface
	manifestFile string
}

func (d dockTmux) CurrentSession() (string, error) {
	session, err := d.Interface.CurrentSession()
	if err != nil {
		return session, err
	}
	windowID, err := d.Interface.CurrentWindowID()
	if err != nil || windowID == "" {
		return session, nil
	}
	m, err := manifest.Load(d.manifestFile)
	if err != nil {
		return session, nil
	}
	return dockOfWindow(m, session, windowID), nil
}

// dockOfWindow returns session when it names a dock. Otherwise it returns
// the dock that has a surface in the window, and session when no dock
// does.
func dockOfWindow(m *manifest.Manifest, session, windowID string) string {
	if m.FindDock(session) != nil {
		return session
	}
	for i := range m.Docks {
		for j := range m.Docks[i].Bays {
			for _, s := range m.Docks[i].Bays[j].Surfaces {
				if s.Tmux != nil && s.Tmux.WindowID == windowID {
					return m.Docks[i].Name
				}
			}
		}
	}
	return session
}
