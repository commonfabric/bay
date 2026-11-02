package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/palette"
	"github.com/commonfabric/bay/internal/picker"
)

// paletteEnv collects the long-lived state the palette commands all
// need. buildPaletteEntries threads it through every Action closure.
type paletteEnv struct {
	Engine  *engine.Engine
	Scope   palette.Scope
	Ctx     *engine.Context
	Recents *palette.Recents
	Hotkeys *palette.Hotkeys
	In      *os.File
	Out     *os.File
}

// modeSplit converts a palette Mode to the split-direction string used by
// engine.SurfaceAddOptions (and the surface creator runners).
func modeSplit(m palette.Mode) string {
	if m == palette.ModePane {
		return "v"
	}
	return ""
}

// modeHotkey looks up the hotkey for a mode-sensitive signature.
func modeHotkey(h *palette.Hotkeys, window, pane string, m palette.Mode) string {
	if m == palette.ModePane {
		return h.Lookup(pane)
	}
	return h.Lookup(window)
}

// buildPaletteEntries returns all palette entries with Actions bound to
// the current env and mode. Called at startup and again on every Tab press.
func buildPaletteEntries(env *paletteEnv, mode palette.Mode) []palette.Entry {
	h := env.Hotkeys
	split := modeSplit(mode)
	agentParamValid := func(agent string) bool {
		return agentAvailable(env.Engine.Config, agent)
	}

	return []palette.Entry{
		// === Navigation ===
		{
			ID:      "go-surface",
			Title:   "Go to surface...",
			Section: palette.SectionNavigation,
			Needs:   palette.ScopeInBay,
			Hotkey:  h.Lookup("bay surface go --pick"),
			Action: func() (string, error) {
				return "", surfaceGo(env.Engine, nil, false)
			},
		},
		{
			ID:      "go-bay",
			Title:   "Go to bay...",
			Section: palette.SectionNavigation,
			Needs:   palette.ScopeInDock,
			Hotkey:  h.Lookup("bay go --pick"),
			Action: func() (string, error) {
				return "", bayGo(env.Engine, nil, false, false)
			},
		},
		{
			ID:      "go-home",
			Title:   "Go to home",
			Section: palette.SectionNavigation,
			Needs:   palette.ScopeInDock,
			Hotkey:  h.Lookup("bay home"),
			Action: func() (string, error) {
				return "", env.Engine.Home(env.Ctx.Dock)
			},
		},
		{
			ID:      "show-context",
			Title:   "Show current context",
			Section: palette.SectionNavigation,
			Needs:   palette.ScopeAnywhere,
			Action: func() (string, error) {
				ctx, err := env.Engine.CurrentContext()
				if err != nil {
					return "", err
				}
				palette.Notice(env.In, env.Out, formatPWD(ctx))
				return "", nil
			},
		},

		// === Create — surface ===
		{
			ID:      "new-shell",
			Title:   "New shell",
			Section: palette.SectionCreateSurface,
			Needs:   palette.ScopeInBay,
			Hotkey:  modeHotkey(h, "bay shell --window", "bay shell --pane", mode),
			Action: func() (string, error) {
				dock, bay, err := env.Engine.ResolveSelf()
				if err != nil {
					return "", err
				}
				return "", env.Engine.SurfaceAdd(engine.SurfaceAddOptions{
					DockName: dock,
					BayName:  bay,
					Type:     manifest.SurfaceTypeShell,
					Name:     "shell",
					SplitDir: split,
				})
			},
		},
		{
			ID:      "new-agent",
			Title:   "New agent",
			Section: palette.SectionCreateSurface,
			Needs:   palette.ScopeInBay,
			Hotkey:  modeHotkey(h, "bay agent --window", "bay agent --pane", mode),
			Action: func() (string, error) {
				dock, bay, err := env.Engine.ResolveSelf()
				if err != nil {
					return "", err
				}
				return "", runSurfaceNew(env.Engine, dock, bay, surfaceNewOpts{
					Type:     manifest.SurfaceTypeAgent,
					SplitDir: split,
				})
			},
		},
		{
			ID:         "new-agent-pick",
			Title:      "New agent...",
			Section:    palette.SectionCreateSurface,
			Needs:      palette.ScopeInBay,
			ParamValid: agentParamValid,
			TitleWithParam: func(p string) string {
				return fmt.Sprintf("New agent (%s)", p)
			},
			Action: func() (string, error) {
				agent, ok := paletteAgentPick(env)
				if !ok {
					return "", nil
				}
				return runPaletteNewAgent(env, split, agent)
			},
			ActionWithParam: func(agent string) (string, error) {
				return runPaletteNewAgent(env, split, agent)
			},
		},
		{
			ID:      "new-cmd",
			Title:   "New cmd...",
			Section: palette.SectionCreateSurface,
			Needs:   palette.ScopeInBay,
			Action: func() (string, error) {
				cmdLine, ok, err := picker.Prompt("new cmd: ", "", env.In, env.Out)
				if err != nil || !ok || strings.TrimSpace(cmdLine) == "" {
					return "", err
				}
				dock, bay, err := env.Engine.ResolveSelf()
				if err != nil {
					return "", err
				}
				return "", runSurfaceNew(env.Engine, dock, bay, surfaceNewOpts{
					Type:     manifest.SurfaceTypeCmd,
					Command:  cmdLine,
					SplitDir: split,
				})
			},
		},
		{
			ID:      "edit-bay",
			Title:   "Open editor (bay)",
			Section: palette.SectionCreateSurface,
			Needs:   palette.ScopeInBay,
			Hotkey:  h.Lookup("bay edit"),
			Action: func() (string, error) {
				return "", runEditCreate(env.Engine, "self", "", split)
			},
		},
		{
			ID:      "edit-dock",
			Title:   "Open editor (dock)",
			Section: palette.SectionCreateSurface,
			Needs:   palette.ScopeInDock,
			Hotkey:  h.Lookup("bay edit --dock"),
			Action: func() (string, error) {
				return "", runEditDock(env.Engine, "", "")
			},
		},
		{
			ID:      "home-shell",
			Title:   "Home shell",
			Section: palette.SectionCreateSurface,
			Needs:   palette.ScopeInDock,
			Hotkey:  h.Lookup("bay shell --bay home"),
			Action: func() (string, error) {
				return "", env.Engine.SurfaceAdd(engine.SurfaceAddOptions{
					DockName: env.Ctx.Dock,
					BayName:  manifest.HomeBayID,
					Type:     manifest.SurfaceTypeShell,
					Name:     "shell",
					SplitDir: split,
				})
			},
		},
		{
			ID:      "home-editor",
			Title:   "Home editor",
			Section: palette.SectionCreateSurface,
			Needs:   palette.ScopeInDock,
			Hotkey:  h.Lookup("bay edit --bay home"),
			Action: func() (string, error) {
				return "", runEditCreateAt(env.Engine, env.Ctx.Dock, manifest.HomeBayID, "", split)
			},
		},
		{
			ID:      "home-agent",
			Title:   "Home agent",
			Section: palette.SectionCreateSurface,
			Needs:   palette.ScopeInDock,
			Hotkey:  h.Lookup("bay agent --bay home"),
			Action: func() (string, error) {
				return "", runSurfaceNew(env.Engine, env.Ctx.Dock, manifest.HomeBayID, surfaceNewOpts{
					Type:     manifest.SurfaceTypeAgent,
					SplitDir: split,
				})
			},
		},
		{
			ID:         "home-agent-pick",
			Title:      "Home agent...",
			Section:    palette.SectionCreateSurface,
			Needs:      palette.ScopeInDock,
			ParamValid: agentParamValid,
			TitleWithParam: func(p string) string {
				return fmt.Sprintf("Home agent (%s)", p)
			},
			Action: func() (string, error) {
				agent, ok := paletteAgentPick(env)
				if !ok {
					return "", nil
				}
				return runPaletteHomeAgent(env, split, agent)
			},
			ActionWithParam: func(agent string) (string, error) {
				return runPaletteHomeAgent(env, split, agent)
			},
		},

		// === Create — bay ===
		{
			ID:      "new-bay",
			Title:   "New bay",
			Section: palette.SectionCreateBay,
			Needs:   palette.ScopeInDock,
			Hotkey:  h.Lookup("bay new -q"),
			Action: func() (string, error) {
				_, err := env.Engine.BayNew(engine.BayNewOptions{
					Dock:  env.Ctx.Dock,
					Shell: true,
				})
				return "", err
			},
		},
		{
			ID:      "new-bay-agent",
			Title:   "New bay with default agent",
			Section: palette.SectionCreateBay,
			Needs:   palette.ScopeInDock,
			Hotkey:  h.Lookup("bay new -q --agent"),
			Action: func() (string, error) {
				_, err := env.Engine.BayNew(engine.BayNewOptions{
					Dock:         env.Ctx.Dock,
					RequireAgent: true,
				})
				return "", err
			},
		},
		{
			ID:         "new-bay-agent-pick",
			Title:      "New bay with agent...",
			Section:    palette.SectionCreateBay,
			Needs:      palette.ScopeInDock,
			ParamValid: agentParamValid,
			TitleWithParam: func(p string) string {
				return fmt.Sprintf("New bay with agent (%s)", p)
			},
			Action: func() (string, error) {
				agent, ok := paletteAgentPick(env)
				if !ok {
					return "", nil
				}
				return runPaletteNewBayAgent(env, agent)
			},
			ActionWithParam: func(agent string) (string, error) {
				return runPaletteNewBayAgent(env, agent)
			},
		},

		// === Current bay ===
		{
			ID:      "rename-bay",
			Title:   "Rename bay...",
			Section: palette.SectionCurrentBay,
			Needs:   palette.ScopeInBay,
			Action: func() (string, error) {
				currentID := env.Ctx.BayID
				current := env.Ctx.Bay
				name, ok, err := picker.Prompt("rename bay: ", current, env.In, env.Out)
				if err != nil || !ok || name == "" || name == current {
					return "", err
				}
				return "", env.Engine.BayRename(env.Ctx.Dock, currentID, name)
			},
		},
		{
			ID:      "describe-bay",
			Title:   "Describe bay...",
			Section: palette.SectionCurrentBay,
			Needs:   palette.ScopeInBay,
			Action: func() (string, error) {
				prefill := ""
				if info, err := env.Engine.BayInfoByName(env.Ctx.Dock, env.Ctx.BayID); err == nil {
					prefill = info.Description
				}
				desc, ok, err := picker.Prompt("describe: ", prefill, env.In, env.Out)
				if err != nil || !ok {
					return "", err
				}
				return "", env.Engine.BayDescribe(env.Ctx.Dock, env.Ctx.BayID, desc)
			},
		},
		{
			ID:      "clear-bay-description",
			Title:   "Clear bay description",
			Section: palette.SectionCurrentBay,
			Needs:   palette.ScopeInBay,
			Action: func() (string, error) {
				return "", env.Engine.BayDescribe(env.Ctx.Dock, env.Ctx.BayID, "")
			},
		},
		{
			ID:      "show-bay",
			Title:   "Show bay details",
			Section: palette.SectionCurrentBay,
			Needs:   palette.ScopeInBay,
			Action: func() (string, error) {
				env.Engine.SyncAll()
				info, err := env.Engine.BayInfoByName(env.Ctx.Dock, env.Ctx.BayID)
				if err != nil {
					return "", err
				}
				text := FormatBayShow(env.Ctx.Dock, info, false)
				palette.Notice(env.In, env.Out, text)
				return "", nil
			},
		},
		{
			ID:      "tidy-bay",
			Title:   "Tidy bay (detach + drop merged branch)",
			Section: palette.SectionCurrentBay,
			Needs:   palette.ScopeInBay,
			Action: func() (string, error) {
				res, err := env.Engine.BayTidy(env.Ctx.Dock, env.Ctx.BayID)
				if err != nil {
					return "", err
				}
				msg := fmt.Sprintf("Detached at origin/%s", res.DefaultBranch)
				if res.AlreadyDetached {
					msg = fmt.Sprintf("Already detached at origin/%s", res.DefaultBranch)
				} else if res.DeletedBranch != "" {
					msg += fmt.Sprintf(", deleted branch %s", res.DeletedBranch)
				}
				palette.Notice(env.In, env.Out, msg)
				return "", nil
			},
		},
		{
			ID:      "close-bay",
			Title:   "Close bay",
			Section: palette.SectionCurrentBay,
			Needs:   palette.ScopeInBay,
			Action: func() (string, error) {
				return "", runBayClose(env.Engine, env.Ctx.Dock, env.Ctx.BayID, false, false)
			},
		},

		// === Current surface ===
		{
			ID:      "rename-surface",
			Title:   "Rename surface...",
			Section: palette.SectionCurrentSurface,
			Needs:   palette.ScopeInBay,
			Action: func() (string, error) {
				current := env.Ctx.Surface
				if current == "" {
					return "", fmt.Errorf("no current surface")
				}
				name, ok, err := picker.Prompt("rename surface: ", current, env.In, env.Out)
				if err != nil || !ok || name == "" || name == current {
					return "", err
				}
				return "", env.Engine.SurfaceRename(env.Ctx.Dock, env.Ctx.BayID, current, name)
			},
		},
		{
			ID:      "close-surface",
			Title:   "Close surface",
			Section: palette.SectionCurrentSurface,
			Needs:   palette.ScopeInBay,
			Hotkey:  h.Lookup("bay sf close self"),
			Action: func() (string, error) {
				if env.Ctx.Surface == "" {
					return "", fmt.Errorf("no current surface")
				}
				force := false
				if dismisses, err := env.Engine.SurfaceCloseWouldDismissDock(env.Ctx.Dock, env.Ctx.BayID, env.Ctx.Surface); err != nil {
					return "", err
				} else if dismisses {
					if !confirmLastHomeClose(homeCloseFlash(env.Engine), env.Ctx.Dock) {
						return "", nil
					}
					force = true
				}
				return "", env.Engine.SurfaceClose(env.Ctx.Dock, env.Ctx.BayID, env.Ctx.Surface, force)
			},
		},
		{
			ID:      "show-surface",
			Title:   "Show surface details",
			Section: palette.SectionCurrentSurface,
			Needs:   palette.ScopeInBay,
			Action: func() (string, error) {
				if env.Ctx.Surface == "" {
					return "", fmt.Errorf("no current surface")
				}
				env.Engine.SyncAll()
				bay, err := env.Engine.BayShow(env.Ctx.Dock, env.Ctx.BayID)
				if err != nil {
					return "", err
				}
				s := bay.FindSurface(env.Ctx.Surface)
				if s == nil {
					return "", fmt.Errorf("surface %q not found", env.Ctx.Surface)
				}
				palette.Notice(env.In, env.Out, formatSurfaceShow(s))
				return "", nil
			},
		},

		// === Admin ===
		{
			ID:      "edit-config",
			Title:   "Edit config",
			Section: palette.SectionAdmin,
			Needs:   palette.ScopeAnywhere,
			Action: func() (string, error) {
				return "", runConfigEdit(env.Engine)
			},
		},
		{
			ID:      "show-config",
			Title:   "Show config",
			Section: palette.SectionAdmin,
			Needs:   palette.ScopeAnywhere,
			Action: func() (string, error) {
				text, err := configShowString(env.Engine.Config)
				if err != nil {
					return "", err
				}
				palette.Notice(env.In, env.Out, text)
				return "", nil
			},
		},
		{
			ID:      "run-doctor",
			Title:   "Run doctor",
			Section: palette.SectionAdmin,
			Needs:   palette.ScopeAnywhere,
			Action: func() (string, error) {
				var sb strings.Builder
				runDoctor(env.Engine, &sb)
				palette.Notice(env.In, env.Out, sb.String())
				return "", nil
			},
		},
		{
			ID:      "monitor-status",
			Title:   "Monitor status",
			Section: palette.SectionAdmin,
			Needs:   palette.ScopeAnywhere,
			Action: func() (string, error) {
				palette.Notice(env.In, env.Out, monitorStatusString())
				return "", nil
			},
		},
	}
}

func runPaletteNewAgent(env *paletteEnv, split, agent string) (string, error) {
	dock, bay, err := env.Engine.ResolveSelf()
	if err != nil {
		return "", err
	}
	return runPaletteNewAgentIn(env, dock, bay, split, agent)
}

func runPaletteNewBayAgent(env *paletteEnv, agent string) (string, error) {
	if !agentAvailable(env.Engine.Config, agent) {
		return "", fmt.Errorf("unknown agent %q", agent)
	}
	if _, err := env.Engine.BayNew(engine.BayNewOptions{
		Dock:         env.Ctx.Dock,
		Agent:        agent,
		RequireAgent: true,
	}); err != nil {
		return "", err
	}
	recordPaletteAgentType(env, agent)
	return agent, nil
}

func runPaletteHomeAgent(env *paletteEnv, split, agent string) (string, error) {
	return runPaletteNewAgentIn(env, env.Ctx.Dock, manifest.HomeBayID, split, agent)
}

func runPaletteNewAgentIn(env *paletteEnv, dock, bayID, split, agent string) (string, error) {
	if !agentAvailable(env.Engine.Config, agent) {
		return "", fmt.Errorf("unknown agent %q", agent)
	}
	if err := runSurfaceNew(env.Engine, dock, bayID, surfaceNewOpts{
		Type:     manifest.SurfaceTypeAgent,
		Agent:    agent,
		SplitDir: split,
	}); err != nil {
		return "", err
	}
	recordPaletteAgentType(env, agent)
	return agent, nil
}

func recordPaletteAgentType(env *paletteEnv, agent string) {
	if env.Recents == nil {
		return
	}
	env.Recents.RecordAgentType(agent)
	_ = env.Recents.Save()
}

// paletteAgentPick shows a sub-picker over the available agent types,
// anchored by the recents MRU. Returns the chosen value, or ("", false)
// on cancel.
func paletteAgentPick(env *paletteEnv) (string, bool) {
	available := availableAgents(env.Engine.Config)
	availSet := make(map[string]bool, len(available))
	for _, name := range available {
		availSet[name] = true
	}
	mru := env.Recents.TopAgentTypes(availSet)

	items, values := buildAgentPickerItems(mru, available, func(name string) string {
		return agentDisplayName(env.Engine.Config, name)
	})

	sel, err := (&picker.Builtin{}).Pick(items, picker.Options{Prompt: "agent type> "})
	if err != nil || sel < 0 {
		return "", false
	}
	if sel >= len(values) || values[sel] == "" {
		return "", false
	}
	return values[sel], true
}

// agentDisplayName decorates a profile with its base client — "fable
// (claude)" — so profiles don't read as peers of the clients they wrap.
// Plain agents display as their name.
func agentDisplayName(cfg *config.Config, name string) string {
	if cfg == nil {
		return name
	}
	if base := cfg.ProfileBase(name); base != "" {
		return fmt.Sprintf("%s (%s)", name, base)
	}
	return name
}

// buildAgentPickerItems builds the sub-picker's item list. MRU entries
// go first, followed by a separator and the full alphabetical list. The
// full list always contains every available agent — including those
// also in MRU — so "pick an agent" feels like a stable directory and
// the MRU is strictly a shortcut.
//
// display maps an agent name to its picker label (e.g. decorating
// profiles with their base). Returns items paired with a parallel
// values slice keyed by Item.Value holding the raw agent names — the
// separator's value is "" — so callers resolve the chosen name
// independent of how it was displayed.
func buildAgentPickerItems(mru, available []string, display func(string) string) ([]picker.Item, []string) {
	var items []picker.Item
	var values []string
	add := func(name string) {
		items = append(items, picker.Item{Display: display(name), Value: len(items)})
		values = append(values, name)
	}
	for _, name := range mru {
		add(name)
	}
	if len(items) > 0 {
		items = append(items, picker.Item{Display: strings.Repeat("─", 6), Value: -1})
		values = append(values, "")
	}
	sortedAll := make([]string, len(available))
	copy(sortedAll, available)
	sort.Strings(sortedAll)
	for _, name := range sortedAll {
		add(name)
	}
	return items, values
}

func agentAvailable(cfg *config.Config, agent string) bool {
	if agent == "" {
		return false
	}
	if cfg == nil {
		if _, ok := config.BuiltinProfiles[agent]; ok {
			return true
		}
		_, ok := config.KnownAgents[config.CanonicalAgentName(agent)]
		return ok
	}
	_, ok := cfg.ResolveAgent(agent)
	return ok
}

// availableAgents returns the sorted list of known, built-in-profile,
// and configured agent names that resolve (disabled entries drop out).
func availableAgents(cfg *config.Config) []string {
	seen := map[string]bool{}
	for name := range config.KnownAgents {
		seen[name] = true
	}
	for name := range config.BuiltinProfiles {
		seen[name] = true
	}
	if cfg != nil {
		for name := range cfg.Agents {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		if cfg != nil {
			if _, ok := cfg.ResolveAgent(name); !ok {
				continue
			}
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// formatSurfaceShow produces a multi-line human-readable summary of s,
// matching the fields that `bay surface show` prints. Mirrors the
// label/value layout in runSurfaceShow by reusing writeAlignedRows.
func formatSurfaceShow(s *manifest.Surface) string {
	rows := []showRow{
		{"surface", s.Name},
		{"type", string(s.Type)},
	}
	if s.Tmux != nil {
		if s.Tmux.WindowID != "" {
			rows = append(rows, showRow{"window", s.Tmux.WindowID})
		}
		if s.Tmux.PaneID != "" {
			rows = append(rows, showRow{"pane", s.Tmux.PaneID})
		}
	}
	if s.Agent != nil && *s.Agent != "" {
		rows = append(rows, showRow{"agent", *s.Agent})
	}
	if s.Command != nil && *s.Command != "" {
		rows = append(rows, showRow{"command", *s.Command})
	}
	var b strings.Builder
	writeAlignedRows(&b, rows, 0)
	return b.String()
}
