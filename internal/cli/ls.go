package cli

import (
	"github.com/commonfabric/bay/internal/engine"
)

// filterDirtyBays filters dock info to only include bays with
// uncommitted changes. Relies on Dirty being populated by engine.List().
func filterDirtyBays(docks []engine.DockInfo) []engine.DockInfo {
	var result []engine.DockInfo
	for _, d := range docks {
		filtered := engine.DockInfo{
			Name:        d.Name,
			Agent:       d.Agent,
			Path:        d.Path,
			WorktreeDir: d.WorktreeDir,
		}
		for _, bay := range d.Bays {
			if bay.Dirty {
				filtered.Bays = append(filtered.Bays, bay)
			}
		}
		if len(filtered.Bays) > 0 {
			result = append(result, filtered)
		}
	}
	return result
}

func resolveListFocus(eng *engine.Engine, dirtyOnly bool) ListFocus {
	if dirtyOnly {
		return ListFocus{Kind: FocusAll}
	}

	return inferListFocus(eng)
}

func inferListFocus(eng *engine.Engine) ListFocus {
	ctx, err := eng.CurrentContext()
	if err != nil {
		return ListFocus{Kind: FocusAll}
	}
	switch {
	case ctx.BayID != "":
		return ListFocus{Kind: FocusBay, Dock: ctx.Dock, BayID: ctx.BayID}
	case ctx.Dock != "":
		return ListFocus{Kind: FocusDock, Dock: ctx.Dock}
	default:
		return ListFocus{Kind: FocusAll}
	}
}
