// Package nav provides fuzzy bay navigation for "bay go" and "bay go".
package nav

import (
	"sort"
	"strings"

	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/tmux"
)

// Entry represents a navigable bay.
type Entry struct {
	DockName    string
	BayID       string
	BayName     string
	Description string
	Branch      string
	PR          string
	Pending     bool
	Setup       string
	Waiting     bool
	// TmuxWindowID of any surface in this bay (for focusing).
	TmuxWindowID string
	SurfaceCount int
}

// DisplayLabel returns the compact target label shown in bay navigation.
// Names are optional, so unnamed bays fall back to their stable ID.
func (e Entry) DisplayLabel() string {
	return manifest.BayLabel(e.BayID, e.BayName)
}

func validHomeBay(dock *manifest.Dock, bay *manifest.Bay) bool {
	return dock != nil &&
		bay != nil &&
		bay.Type == manifest.BayTypeHome &&
		bay.ID == manifest.HomeBayID &&
		bay.Name == manifest.HomeBayID &&
		bay.Path == dock.Path &&
		bay.Worktree == nil
}

// CollectEntries builds a list of all navigation entries from the manifest.
// Each bay becomes one entry.
func CollectEntries(m *manifest.Manifest, tc tmux.Interface) []Entry {
	var entries []Entry

	// Sort docks for deterministic output.
	docks := make([]string, 0, len(m.Docks))
	for i := range m.Docks {
		docks = append(docks, m.Docks[i].Name)
	}
	sort.Strings(docks)

	for _, dockName := range docks {
		dock := m.FindDock(dockName)
		if dock == nil {
			continue
		}

		waitingWindows, _ := tc.WaitingOrBellWindowIDs(dockName)

		for i := range dock.Bays {
			bay := &dock.Bays[i]
			if manifest.IsHomeLikeBay(bay) && !validHomeBay(dock, bay) {
				continue
			}

			branch := ""
			pr := ""
			if !manifest.IsHomeBay(bay) && bay.Worktree != nil {
				branch = bay.Worktree.Branch
				pr = bay.Worktree.PR
			}

			var tmuxWindowID string
			waiting := false
			for _, s := range bay.Surfaces {
				if s.Tmux != nil && s.Tmux.WindowID != "" {
					if tmuxWindowID == "" {
						tmuxWindowID = s.Tmux.WindowID
					}
					if waitingWindows[s.Tmux.WindowID] {
						waiting = true
						// Prefer the waiting window so next-waiting
						// jumps directly to it, not a sibling.
						tmuxWindowID = s.Tmux.WindowID
					}
				}
			}

			entries = append(entries, Entry{
				DockName:     dockName,
				BayID:        bay.ID,
				BayName:      bay.Name,
				Description:  bay.Description,
				Branch:       branch,
				PR:           pr,
				Pending:      !manifest.IsHomeBay(bay) && bay.Worktree != nil && bay.Worktree.Branch != "" && !bay.IsMerged(),
				Setup:        manifest.PrepareSetupSummary(bay.Prepare),
				TmuxWindowID: tmuxWindowID,
				Waiting:      waiting,
				SurfaceCount: len(bay.Surfaces),
			})
		}
	}
	return entries
}

// FuzzyMatch filters entries by case-insensitive substring match.
func FuzzyMatch(entries []Entry, query string) []Entry {
	if query == "" {
		return entries
	}
	q := strings.ToLower(query)
	var matched []Entry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.DisplayLabel()), q) ||
			strings.Contains(strings.ToLower(e.BayID), q) ||
			strings.Contains(strings.ToLower(e.BayName), q) ||
			strings.Contains(strings.ToLower(e.Description), q) ||
			strings.Contains(strings.ToLower(e.Branch), q) ||
			strings.Contains(strings.ToLower(e.PR), q) ||
			strings.Contains(strings.ToLower(e.DockName), q) {
			matched = append(matched, e)
		}
	}
	return matched
}

// FilterWaiting returns only entries with Waiting=true.
func FilterWaiting(entries []Entry) []Entry {
	var waiting []Entry
	for _, e := range entries {
		if e.Waiting {
			waiting = append(waiting, e)
		}
	}
	return waiting
}

// NextWaiting returns the next waiting entry after the given current window
// (and its index in entries). Returns (nil, -1) when no entry is waiting.
func NextWaiting(entries []Entry, currentWindowID string) (*Entry, int) {
	currentIdx := -1
	for i, e := range entries {
		if e.TmuxWindowID == currentWindowID {
			currentIdx = i
			break
		}
	}

	n := len(entries)
	if currentIdx == -1 {
		// No matching current window: return the first waiting entry.
		for i, e := range entries {
			if e.Waiting {
				return &entries[i], i
			}
		}
		return nil, -1
	}

	for offset := 1; offset <= n; offset++ {
		idx := (currentIdx + offset) % n
		if entries[idx].Waiting {
			return &entries[idx], idx
		}
	}
	return nil, -1
}

// --- Surface-level navigation (intra-bay) ---

// SurfaceEntry represents a navigable surface within a bay.
type SurfaceEntry struct {
	ID       int
	Name     string
	Type     string // "agent", "editor", "shell", "cmd"
	PaneID   string // tmux pane ID
	WindowID string // tmux window ID
	Waiting  bool
	Current  bool // true if this is the currently focused surface
}

// CollectSurfaces builds a list of surface entries for a bay.
// waitingWindows is a pre-computed set of window IDs waiting by either
// @bay-waiting or tmux's native bell flag.
func CollectSurfaces(bay *manifest.Bay, currentPaneID string, waitingWindows map[string]bool) []SurfaceEntry {
	var entries []SurfaceEntry
	for _, s := range bay.Surfaces {
		e := SurfaceEntry{
			ID:   s.ID,
			Name: s.Name,
			Type: string(s.Type),
		}
		if s.Tmux != nil {
			e.PaneID = s.Tmux.PaneID
			e.WindowID = s.Tmux.WindowID
			if e.PaneID == currentPaneID && currentPaneID != "" {
				e.Current = true
			}
			if waitingWindows[e.WindowID] {
				e.Waiting = true
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// NextSurface returns the next surface after the current one (and its index),
// wrapping around. If no surface is marked current, returns the first entry.
// Returns (nil, -1) on an empty list.
func NextSurface(entries []SurfaceEntry) (*SurfaceEntry, int) {
	if len(entries) == 0 {
		return nil, -1
	}
	cur := currentSurfaceIndex(entries)
	next := (cur + 1) % len(entries)
	return &entries[next], next
}

// PrevSurface returns the previous surface before the current one (and its
// index), wrapping around. If no surface is marked current, returns the last
// entry. Returns (nil, -1) on an empty list.
func PrevSurface(entries []SurfaceEntry) (*SurfaceEntry, int) {
	if len(entries) == 0 {
		return nil, -1
	}
	cur := currentSurfaceIndex(entries)
	if cur == -1 {
		last := len(entries) - 1
		return &entries[last], last
	}
	prev := (cur - 1 + len(entries)) % len(entries)
	return &entries[prev], prev
}

// currentSurfaceIndex returns the index of the entry marked Current, or -1.
func currentSurfaceIndex(entries []SurfaceEntry) int {
	for i, e := range entries {
		if e.Current {
			return i
		}
	}
	return -1
}

// NextWaitingSurface returns the next surface marked Waiting after the
// current one (and its index), wrapping around. If no surface is current,
// returns the first waiting entry. Returns (nil, -1) when none are waiting.
func NextWaitingSurface(entries []SurfaceEntry) (*SurfaceEntry, int) {
	cur := currentSurfaceIndex(entries)
	n := len(entries)
	if cur == -1 {
		for i, e := range entries {
			if e.Waiting {
				return &entries[i], i
			}
		}
		return nil, -1
	}
	for offset := 1; offset <= n; offset++ {
		idx := (cur + offset) % n
		if entries[idx].Waiting {
			return &entries[idx], idx
		}
	}
	return nil, -1
}
