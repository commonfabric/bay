package docket

import (
	"fmt"
	"strings"
)

// Names of the tmux objects the view uses.
const (
	ViewSession  = "docket"      // holds the window with the sidebar
	ViewWindow   = "view"        //
	AuxSession   = "docket-aux"  // holds the window that shows an item's record
	RecordWindow = "record"      //
	DeskSession  = "docket-desk" // holds the desk agent's window
	DeskWindow   = "desk"        //
	KeyTable     = "docket"      // key table of clients attached to ViewSession
	SidebarWidth = 36
	// A leading "=" makes tmux match the session name exactly. Without it
	// "docket" also matches "docket-desk".
	sidebarPane   = "=" + ViewSession + ":" + ViewWindow + ".{left}"
	viewerPane    = "=" + ViewSession + ":" + ViewWindow + ".{right}"
	recordTarget  = "=" + AuxSession + ":" + RecordWindow
	deskTarget    = "=" + DeskSession + ":" + DeskWindow
	viewTarget    = "=" + ViewSession + ":" + ViewWindow
	auxTarget     = "=" + AuxSession + ":"
	recordWelcome = "printf 'Choose an item in the list on the left.\\n'; exec cat"
)

// Tmux runs one tmux command and returns its standard output.
type Tmux func(args ...string) (string, error)

// View arranges the tmux session that shows the sidebar on the left and,
// on the right, the window of whichever item is chosen.
//
// The right pane runs a second tmux client attached to AuxSession. Showing
// an item links the item's window into AuxSession and selects it there. A
// linked window is the same window in both sessions, so it keeps its panes
// and keys typed in the right pane reach it. Each session has its own
// current window, so choosing an item does not move the item's own
// session, and that session's status line is not shown. AuxSession holds
// the record window and at most one linked window.
type View struct {
	Tmux Tmux
	// SidebarCommand is the shell command that runs the sidebar.
	SidebarCommand string
	// TmuxCommand is the shell command for tmux, used to start the client
	// in the right pane.
	TmuxCommand string
	// BayWindow returns the tmux window of a bay given as "dock/bay", and
	// false when the bay is unknown or has no window.
	BayWindow func(ref string) (string, bool)
	// RecordCommand is the shell command that prints an item's record.
	RecordCommand func(item string) string
	// StartDesk starts the desk agent's window when it is not running.
	StartDesk func() error
	// Width is the width a newly started sidebar is given: the width saved
	// from the last time the list was used. 0 means SidebarWidth.
	Width int
}

// windowID returns the id of a window, and "" when it does not exist.
// target is a window id such as "@7", or one of the "=session:window"
// targets above.
func (v *View) windowID(target string) string {
	out, err := v.Tmux("list-windows", "-a", "-F", "#{window_id} =#{session_name}:#{window_name}")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		id, name, _ := strings.Cut(strings.TrimSpace(line), " ")
		if id == target || name == target {
			return id
		}
	}
	return ""
}

// has reports whether a window exists.
func (v *View) has(target string) bool {
	return v.windowID(target) != ""
}

// auxWindows returns the ids of the windows in AuxSession.
func (v *View) auxWindows() ([]string, error) {
	out, err := v.Tmux("list-windows", "-t", auxTarget, "-F", "#{window_id}")
	if err != nil {
		return nil, fmt.Errorf("list the right pane's windows: %w", err)
	}
	return strings.Fields(out), nil
}

// Setup creates what is missing of the two sessions and their panes, and
// sets the keys. It changes nothing that is already in place, so it is
// safe to run again.
func (v *View) Setup() error {
	if !v.has(recordTarget) {
		if _, err := v.Tmux("new-session", "-d", "-s", AuxSession, "-n", RecordWindow, recordWelcome); err != nil {
			return fmt.Errorf("start the record window: %w", err)
		}
		if _, err := v.Tmux("set-option", "-t", auxTarget, "status", "off"); err != nil {
			return fmt.Errorf("hide the record window's status line: %w", err)
		}
	}
	if !v.has(viewTarget) {
		if _, err := v.Tmux("new-session", "-d", "-s", ViewSession, "-n", ViewWindow, v.SidebarCommand); err != nil {
			return fmt.Errorf("start the sidebar: %w", err)
		}
	}
	if err := v.ensureViewer(); err != nil {
		return err
	}
	for _, opt := range [][2]string{{"key-table", KeyTable}, {"prefix", "None"}, {"prefix2", "None"}, {"status", "off"}} {
		if _, err := v.Tmux("set-option", "-t", "="+ViewSession+":", opt[0], opt[1]); err != nil {
			return fmt.Errorf("set %s: %w", opt[0], err)
		}
	}
	// Clients attached to the view session use the docket key table. A key
	// with no binding there goes to the pane, so in the right pane it
	// reaches the second client and the window it shows.
	//
	//
	// The keys that act on the list from either pane send it function
	// keys. They work while the list is collapsed and while a pane of the
	// view is zoomed.
	list := func(key string) []string {
		return []string{"send-keys", "-t", sidebarPane, key}
	}
	binds := [][]string{
		append([]string{"M-l"}, list("F5")...),
		append([]string{"M-h"}, list("F6")...),
		append([]string{"M-J"}, list("F5")...),
		append([]string{"M-K"}, list("F6")...),
		append([]string{"M-N"}, list("F9")...),
		append([]string{"M-P"}, list("F10")...),
		append([]string{"M-Space"}, list("F7")...),
		// Show the chosen item's state.
		{"M-?", "send-keys", "-t", sidebarPane, "F8"},
		// Collapse the list and move to the right side, or expand it.
		{"M-B", "send-keys", "-t", sidebarPane, "F11"},
		// Dragging the line between the panes changes the list's width,
		// and a double click on that line gives it the first width again.
		{"MouseDrag1Border", "resize-pane", "-M"},
		{"DoubleClick1Border", "resize-pane", "-t", sidebarPane, "-x", fmt.Sprint(SidebarWidth)},
		{"M-d", "if-shell", "-F", "#{pane_at_left}", "select-pane -t '" + viewerPane + "'", "select-pane -t '" + sidebarPane + "'"},
		// Leave the view: go back to the session the terminal came from,
		// or detach when it came from outside tmux.
		{"M-D", "if-shell", "-F", "#{client_last_session}", "switch-client -l", "detach-client"},
		// A mouse press moves the keyboard to the pane under it and is
		// passed on to that pane. The backslash keeps both commands in
		// the binding; a bare ";" would end the bind-key command.
		{"MouseDown1Pane", "select-pane", "-t", "=", "\\;", "send-keys", "-M"},
	}
	// The table belongs to the view. Emptying it first removes keys that an
	// earlier version bound.
	if _, err := v.Tmux("unbind-key", "-a", "-T", KeyTable); err != nil {
		return fmt.Errorf("clear the view's keys: %w", err)
	}
	for _, b := range binds {
		if _, err := v.Tmux(append([]string{"bind-key", "-T", KeyTable}, b...)...); err != nil {
			return fmt.Errorf("bind %s: %w", b[0], err)
		}
	}
	return nil
}

// ListHidden reports whether the right pane is zoomed, which hides the
// list.
func (v *View) ListHidden() bool {
	out, err := v.Tmux("display-message", "-p", "-t", sidebarPane, "#{window_zoomed_flag}#{pane_active}")
	return err == nil && strings.TrimSpace(out) == "10"
}

// Zoomed reports whether a pane of the view's window is zoomed.
func (v *View) Zoomed() bool {
	out, err := v.Tmux("display-message", "-p", "-t", sidebarPane, "#{window_zoomed_flag}")
	return err == nil && strings.TrimSpace(out) == "1"
}

// Collapse shrinks the sidebar to CollapsedWidth and moves the keyboard to
// the right pane.
func (v *View) Collapse() error {
	if err := v.SetWidth(CollapsedWidth); err != nil {
		return err
	}
	_, err := v.Tmux("select-pane", "-t", viewerPane)
	return err
}

// SetWidth gives the sidebar a width in columns.
func (v *View) SetWidth(width int) error {
	_, err := v.Tmux("resize-pane", "-t", sidebarPane, "-x", fmt.Sprint(width))
	return err
}

// ensureViewer starts whichever of the view window's two panes is
// missing: the sidebar on the left, or the client on the right. A pane is
// recognized by the command it was started with.
func (v *View) ensureViewer() error {
	out, err := v.Tmux("list-panes", "-t", viewTarget, "-F", "#{pane_start_command}")
	if err != nil {
		return fmt.Errorf("list the view's panes: %w", err)
	}
	client := "env -u TMUX " + v.TmuxCommand + " attach-session -t '=" + AuxSession + "'"
	sidebar, viewer := false, false
	for _, line := range strings.Split(out, "\n") {
		// tmux prints the command inside double quotes.
		command := strings.Trim(strings.TrimSpace(line), "\"")
		sidebar = sidebar || command == v.SidebarCommand
		viewer = viewer || command == client
	}
	if sidebar && viewer {
		return nil
	}
	if !viewer {
		if _, err := v.Tmux("split-window", "-h", "-d", "-t", viewTarget, client); err != nil {
			return fmt.Errorf("start the right pane: %w", err)
		}
	}
	if !sidebar {
		if _, err := v.Tmux("split-window", "-h", "-b", "-f", "-d", "-t", viewTarget, v.SidebarCommand); err != nil {
			return fmt.Errorf("start the sidebar: %w", err)
		}
	}
	// A pane was started, so the sidebar gets the saved width. A width
	// chosen later is kept.
	if v.Width >= CollapsedBelow {
		return v.SetWidth(v.Width)
	}
	return v.SetWidth(SidebarWidth)
}

// viewerClient returns the tty of the client running in the right pane.
func (v *View) viewerClient() (string, error) {
	tty, err := v.Tmux("display-message", "-p", "-t", viewerPane, "#{pane_tty}")
	if err != nil {
		return "", err
	}
	tty = strings.TrimSpace(tty)
	clients, err := v.Tmux("list-clients", "-F", "#{client_tty}")
	if err != nil {
		return "", err
	}
	for _, c := range strings.Fields(clients) {
		if c == tty {
			return tty, nil
		}
	}
	return "", fmt.Errorf("the right pane has no tmux client yet")
}

// Show makes the right pane display the row's window: the desk, the
// row's bay window, or the item's record when it has no bay window. With
// focus set it also moves the keyboard to the right pane. The returned
// note says what was shown when that is not the bay window itself.
func (v *View) Show(row Row, focus bool) (note string, err error) {
	if row.Area {
		// An area with no item to show leaves the right side as it is.
		if focus {
			_, err = v.Tmux("select-pane", "-t", viewerPane)
		}
		return "", err
	}
	if err := v.ensureViewer(); err != nil {
		return "", err
	}
	client, err := v.viewerClient()
	if err != nil {
		return "", err
	}
	window := ""
	switch {
	case row.Desk:
		if !v.has(deskTarget) {
			if err := v.StartDesk(); err != nil {
				return "", fmt.Errorf("start the desk: %w", err)
			}
		}
		window = v.windowID(deskTarget)
	case len(row.Bays) > 0:
		if id, ok := v.BayWindow(row.Bays[0]); ok && v.has(id) {
			window = id
		} else {
			note = row.Bays[0] + " has no window; showing the record"
		}
	}
	record := v.windowID(recordTarget)
	if window == "" {
		item := strings.Fields(row.Name)[0]
		if _, err := v.Tmux("respawn-window", "-k", "-t", recordTarget, v.RecordCommand(item)); err != nil {
			return "", fmt.Errorf("show the record: %w", err)
		}
		window = record
	}
	if window == "" {
		return "", fmt.Errorf("the record window is gone; run 'bay docket view' again")
	}
	held, err := v.auxWindows()
	if err != nil {
		return "", err
	}
	linked := false
	for _, id := range held {
		linked = linked || id == window
	}
	if !linked {
		if _, err := v.Tmux("link-window", "-d", "-s", window, "-t", auxTarget); err != nil {
			return "", fmt.Errorf("bring %s into the right pane: %w", window, err)
		}
	}
	if _, err := v.Tmux("switch-client", "-c", client, "-t", auxTarget+window); err != nil {
		return "", fmt.Errorf("show %s: %w", window, err)
	}
	// Remove the window shown before. unlink-window refuses to remove a
	// window from the only session that holds it, so a window whose own
	// session has gone stays here and keeps running.
	for _, id := range held {
		if id == window || id == record {
			continue
		}
		if _, err := v.Tmux("unlink-window", "-t", auxTarget+id); err != nil {
			note = strings.TrimSpace(note + " " + id + " is held only by the view and was kept")
		}
	}
	if focus {
		if _, err := v.Tmux("select-pane", "-t", viewerPane); err != nil {
			return "", err
		}
	}
	return note, nil
}
