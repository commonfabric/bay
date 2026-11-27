package engine

import (
	"fmt"
	"time"

	"github.com/commonfabric/bay/internal/manifest"
)

// BaySetState sets a bay's lifecycle state, given as its word or its mark
// (see manifest.StateMarks). An empty state removes it.
func (e *Engine) BaySetState(dockName, bayID, state string) error {
	word := ""
	if state != "" {
		var ok bool
		if word, ok = manifest.ParseState(state); !ok {
			return fmt.Errorf("unknown state %q; see `bay state --help` for the states", state)
		}
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
		bay.State = word
		bay.LastActive = time.Now().Unix()
		return nil
	})
}
