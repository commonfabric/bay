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
	recordWelcome = "printf 'Choose an item in the list on the left.\\n'; exec cat"
)

// Tmux runs one tmux command and returns its standard output.
type Tmux func(args ...string) (string, error)

// View arranges the tmux session that shows the sidebar on the left and,
// on the right, the window of whichever item is chosen.
//
// The right pane runs a second tmux client. Showing an item switches that
// client to the item's window, so the window stays in its own session and
// keeps its panes. Keys typed in the right pane reach that window.
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
}

// has reports whether a window exists. target is a window id such as
// "@7", or one of the "=session:window" targets above.
func (v *View) has(target string) bool {
	out, err := v.Tmux("list-windows", "-a", "-F", "#{window_id} =#{session_name}:#{window_name}")
	if err != nil {
		return false
	}
	for _, f := range strings.Fields(out) {
		if f == target {
			return true
		}
	}
	return false
}

// Setup creates what is missing of the two sessions and their panes, and
// sets the keys. It changes nothing that is already in place, so it is
// safe to run again.
func (v *View) Setup() error {
	if !v.has(recordTarget) {
		if _, err := v.Tmux("new-session", "-d", "-s", AuxSession, "-n", RecordWindow, recordWelcome); err != nil {
			return fmt.Errorf("start the record window: %w", err)
		}
		if _, err := v.Tmux("set-option", "-t", "="+AuxSession+":", "status", "off"); err != nil {
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
	binds := [][]string{
		{"M-l", "send-keys", "-t", sidebarPane, "J"},
		{"M-h", "send-keys", "-t", sidebarPane, "K"},
		{"M-d", "if-shell", "-F", "#{pane_at_left}", "select-pane -t '" + viewerPane + "'", "select-pane -t '" + sidebarPane + "'"},
		{"M-D", "switch-client", "-l"},
	}
	for _, b := range binds {
		if _, err := v.Tmux(append([]string{"bind-key", "-T", KeyTable}, b...)...); err != nil {
			return fmt.Errorf("bind %s: %w", b[0], err)
		}
	}
	return nil
}

// ensureViewer starts the right pane's client when the view window has
// only the sidebar's pane.
func (v *View) ensureViewer() error {
	out, err := v.Tmux("list-panes", "-t", viewTarget, "-F", "#{pane_id}")
	if err != nil {
		return fmt.Errorf("list the view's panes: %w", err)
	}
	if len(strings.Fields(out)) > 1 {
		return nil
	}
	client := "env -u TMUX " + v.TmuxCommand + " attach-session -t '=" + AuxSession + "'"
	if _, err := v.Tmux("split-window", "-h", "-d", "-t", viewTarget, client); err != nil {
		return fmt.Errorf("start the right pane: %w", err)
	}
	_, err = v.Tmux("resize-pane", "-t", sidebarPane, "-x", fmt.Sprint(SidebarWidth))
	return err
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

// Show makes the right pane display the row's session: the desk, the
// row's bay window, or the item's record when it has no bay window. With
// focus set it also moves the keyboard to the right pane. The returned
// note says what was shown when that is not the bay window itself.
func (v *View) Show(row Row, focus bool) (note string, err error) {
	if row.Area {
		return "", nil
	}
	if err := v.ensureViewer(); err != nil {
		return "", err
	}
	client, err := v.viewerClient()
	if err != nil {
		return "", err
	}
	target := ""
	switch {
	case row.Desk:
		if !v.has(deskTarget) {
			if err := v.StartDesk(); err != nil {
				return "", fmt.Errorf("start the desk: %w", err)
			}
		}
		target = deskTarget
	case len(row.Bays) > 0:
		if window, ok := v.BayWindow(row.Bays[0]); ok && v.has(window) {
			target = window
		} else {
			note = row.Bays[0] + " has no window; showing the record"
		}
	}
	if target == "" {
		item := strings.Fields(row.Name)[0]
		if _, err := v.Tmux("respawn-window", "-k", "-t", recordTarget, v.RecordCommand(item)); err != nil {
			return "", fmt.Errorf("show the record: %w", err)
		}
		target = recordTarget
	}
	if _, err := v.Tmux("switch-client", "-c", client, "-t", target); err != nil {
		return "", fmt.Errorf("show %s: %w", target, err)
	}
	if focus {
		if _, err := v.Tmux("select-pane", "-t", viewerPane); err != nil {
			return "", err
		}
	}
	return note, nil
}
