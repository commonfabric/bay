package cli

import (
	"fmt"
	"strings"

	"github.com/commonfabric/bay/internal/engine"
)

// parseBayArg parses a bay positional argument like "b1" or "labs:b1".
// dock may be empty if no prefix was given.
func parseBayArg(arg string) (dock, bay string, err error) {
	parts := strings.Split(arg, ":")
	switch len(parts) {
	case 1:
		return "", parts[0], nil
	case 2:
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("invalid bay argument %q (expected id or dock:id)", arg)
	}
}

// parseSurfaceArg parses a surface positional argument. Accepted forms:
//
//	name
//	bay:name
//	dock:bay:name
//
// dock and bay may be empty.
func parseSurfaceArg(arg string) (dock, bay, surface string, err error) {
	parts := strings.Split(arg, ":")
	switch len(parts) {
	case 1:
		return "", "", parts[0], nil
	case 2:
		return "", parts[0], parts[1], nil
	case 3:
		return parts[0], parts[1], parts[2], nil
	default:
		return "", "", "", fmt.Errorf("invalid surface argument %q (expected name, bay:name, or dock:bay:name)", arg)
	}
}

// resolveBayArg resolves a bay positional + --dock flag into
// (dockName, bayName). The positional may be "self", a bare bay ID,
// or "dock:id". --dock disambiguates a bare ID and may not conflict
// with a dock prefix in the positional.
//
// For a bare ID, the current dock (resolved from cwd/tmux) wins if it
// has a bay with that ID. This matches user intent: typing
// `bay close b2` from inside loom means loom:b2, not "error because
// crew also has b2". Falls through to a full-manifest search when the
// current dock doesn't have a match, which may then error with
// "ambiguous" as before.
func resolveBayArg(eng *engine.Engine, posArg, dockFlag string) (string, string, error) {
	if posArg == "self" {
		if dockFlag != "" {
			return "", "", fmt.Errorf("--dock cannot be combined with self")
		}
		return eng.ResolveSelf()
	}

	dock, bay, err := parseBayArg(posArg)
	if err != nil {
		return "", "", err
	}

	if dockFlag != "" {
		if dock != "" {
			return "", "", fmt.Errorf("--dock conflicts with dock prefix in argument")
		}
		dock = dockFlag
	}

	if dock != "" {
		return eng.ResolveBay(dock + ":" + bay)
	}
	return resolveBareBay(eng, bay)
}

// resolveBareBay resolves a bare bay ID, preferring the current dock
// to disambiguate. If the current dock (from cwd/tmux) has a bay
// with this ID, that wins; otherwise falls through to a full-manifest
// search, which may error with "ambiguous" if the ID
// appears in multiple docks and none is the current one.
func resolveBareBay(eng *engine.Engine, bay string) (string, string, error) {
	if ctx, err := eng.CurrentContext(); err == nil && ctx.Dock != "" {
		if dn, wn, resolveErr := eng.ResolveBay(ctx.Dock + ":" + bay); resolveErr == nil {
			return dn, wn, nil
		}
	}
	return eng.ResolveBay(bay)
}

// resolveSurfaceArgOrSelf is like resolveSurfaceArg but treats a bare "self"
// positional (no flags, no colons) specially: it resolves to the current
// pane's surface, unless a literal surface named "self" exists in the
// resolved bay — in which case the literal wins. Qualified forms
// like "b1:self" or "labs:b1:self" are always literal — no virtual fallback.
func resolveSurfaceArgOrSelf(eng *engine.Engine, posArg, bayFlag, dockFlag string) (string, string, string, error) {
	if posArg != "self" || bayFlag != "" || dockFlag != "" {
		return resolveSurfaceArg(eng, posArg, bayFlag, dockFlag)
	}

	dockName, bayName, err := eng.ResolveSelf()
	if err != nil {
		return "", "", "", err
	}
	bay, err := eng.BayShow(dockName, bayName)
	if err != nil {
		return "", "", "", err
	}
	// Literal surface named "self" wins if it exists.
	if bay.FindSurface("self") != nil {
		return dockName, bayName, "self", nil
	}
	// Otherwise resolve to the surface owning the current tmux pane.
	paneID, tmuxErr := eng.Tmux.CurrentPaneID()
	if tmuxErr != nil {
		return "", "", "", fmt.Errorf("cannot determine current pane")
	}
	for _, s := range bay.Surfaces {
		if s.Tmux != nil && s.Tmux.PaneID == paneID {
			return dockName, bayName, s.Name, nil
		}
	}
	// Check dock-level surfaces (e.g., dock editor).
	m, loadErr := eng.LoadManifest()
	if loadErr == nil {
		if dock := m.FindDock(dockName); dock != nil {
			for _, s := range dock.Surfaces {
				if s.Tmux != nil && s.Tmux.PaneID == paneID {
					// Return a special marker so callers know this is a dock surface.
					return dockName, "", s.Name, nil
				}
			}
		}
	}
	return "", "", "", fmt.Errorf("current pane is not a tracked surface")
}

// isDockSurface returns true if the positional is "dock:surfacename",
// meaning a dock-level surface in the current dock.
func isDockSurface(posArg string) (string, bool) {
	parts := strings.SplitN(posArg, ":", 2)
	if len(parts) == 2 && parts[0] == "dock" {
		return parts[1], true
	}
	return "", false
}

// resolveSurfaceArg resolves a surface positional + --bay/--dock flags into
// (dockName, bayName, surfaceName). The positional may be "name", "bay:name",
// "dock:bay:name", or the special form "dock:name" for dock-level surfaces.
// Flags may not conflict with corresponding parts in the positional.
// If neither positional nor flags name a bay, the current bay (ResolveSelf)
// is used.
func resolveSurfaceArg(eng *engine.Engine, posArg, bayFlag, dockFlag string) (string, string, string, error) {
	dock, bay, surface, err := parseSurfaceArg(posArg)
	if err != nil {
		return "", "", "", err
	}

	if bayFlag != "" {
		if bay != "" {
			return "", "", "", fmt.Errorf("--bay conflicts with bay prefix in argument")
		}
		bay = bayFlag
	}
	if dockFlag != "" {
		if dock != "" {
			return "", "", "", fmt.Errorf("--dock conflicts with dock prefix in argument")
		}
		dock = dockFlag
	}
	if dock != "" && bay == "" {
		return "", "", "", fmt.Errorf("--dock requires --bay or a bay prefix")
	}

	var dockName, bayName string
	switch {
	case dock != "" && bay != "":
		dockName, bayName, err = eng.ResolveBay(dock + ":" + bay)
	case bay != "":
		dockName, bayName, err = resolveBareBay(eng, bay)
	default:
		dockName, bayName, err = eng.ResolveSelf()
	}
	if err != nil {
		return "", "", "", err
	}
	return dockName, bayName, surface, nil
}
