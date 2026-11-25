package docket

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Names of the tmux objects the view uses.
const (
	// The sessions' names begin with "_", which bay refuses for a dock.
	ViewSession  = "_docket"      // holds the window with the sidebar
	ViewWindow   = "view"         //
	AuxSession   = "_docket-aux"  // holds the window that shows an item's record
	RecordWindow = "record"       //
	DeskSession  = "_docket-desk" // holds the desk agent's window
	DeskWindow   = "desk"         //
	KeyTable     = "docket"       // key table of clients attached to ViewSession
	SidebarWidth = 36
	// A leading "=" makes tmux match the session name exactly. Without it
	// "_docket" also matches "_docket-desk".
	sidebarPane   = "=" + ViewSession + ":" + ViewWindow + ".{left}"
	viewerPane    = "=" + ViewSession + ":" + ViewWindow + ".{right}"
	recordTarget  = "=" + AuxSession + ":" + RecordWindow
	deskTarget    = "=" + DeskSession + ":" + DeskWindow
	viewTarget    = "=" + ViewSession + ":" + ViewWindow
	auxTarget     = "=" + AuxSession + ":"
	recordWelcome = "printf 'Choose an item in the list on the left.\\n'; exec cat"
	// The ends of the commands that start the sidebar and the client in
	// the right pane.
	sidebarSuffix = " docket sidebar"
	clientAttach  = "attach-session -t '=" + AuxSession + "'"
	// The end of that command when the session was named docket-aux. A
	// client started so follows the session when it is renamed.
	oldClientAttach = "attach-session -t '=docket-aux'"
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
	// ListPane is the id of the list's own pane, such as "%3", when the
	// list itself runs the View. The other commands find the list's pane.
	ListPane string
}

// listTarget returns the tmux target of the list's pane.
func (v *View) listTarget() string {
	if v.ListPane != "" {
		return v.ListPane
	}
	return sidebarPane
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

// ensureAux starts the right pane's session when it is not running, as
// when it has been killed, and reports whether it started it.
func (v *View) ensureAux() (started bool, err error) {
	if v.has(recordTarget) {
		return false, nil
	}
	if _, err := v.Tmux("new-session", "-d", "-s", AuxSession, "-n", RecordWindow, recordWelcome); err != nil {
		return false, fmt.Errorf("start the record window: %w", err)
	}
	if _, err := v.Tmux("set-option", "-t", auxTarget, "status", "off"); err != nil {
		return true, fmt.Errorf("hide the record window's status line: %w", err)
	}
	return true, nil
}

// setAux sets the options and keys of the right pane's session.
func (v *View) setAux() error {
	// The record window stays when the record's pager is quit, so the
	// next item's record can be shown in it.
	if _, err := v.Tmux("set-option", "-w", "-t", recordTarget, "remain-on-exit", "on"); err != nil {
		return fmt.Errorf("keep the record window: %w", err)
	}
	return v.bindAuxPrefix()
}

// oldSessions maps the names the view's sessions had before they began
// with "_" to their names now.
var oldSessions = [][3]string{
	{"docket", ViewSession, ViewWindow},
	{"docket-aux", AuxSession, RecordWindow},
	{"docket-desk", DeskSession, DeskWindow},
}

// renameOldSessions gives a session of the view that still has its old
// name its name now, so that what runs in it, such as the desk's agent,
// keeps running.
func (v *View) renameOldSessions() {
	for _, s := range oldSessions {
		if v.has("="+s[0]+":"+s[2]) && !v.has("="+s[1]+":"+s[2]) {
			_, _ = v.Tmux("rename-session", "-t", "="+s[0], s[1])
		}
	}
}

// Setup creates what is missing of the two sessions and their panes, and
// sets the keys. It changes nothing that is already in place, so it is
// safe to run again.
func (v *View) Setup() error {
	v.renameOldSessions()
	if _, err := v.ensureAux(); err != nil {
		return err
	}
	if !v.has(viewTarget) {
		if _, err := v.Tmux("new-session", "-d", "-s", ViewSession, "-n", ViewWindow, v.SidebarCommand); err != nil {
			return fmt.Errorf("start the sidebar: %w", err)
		}
	}
	pane, err := v.ensureViewer()
	if err != nil {
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
	// view is zoomed. They name the list's pane by its id, so they do
	// nothing once the list has exited.
	list := func(key string) []string {
		return []string{"send-keys", "-t", pane, key}
	}
	binds := [][]string{
		append([]string{"M-j"}, list("F5")...),
		append([]string{"M-k"}, list("F6")...),
		append([]string{"M-J"}, list("F5")...),
		append([]string{"M-K"}, list("F6")...),
		append([]string{"M-N"}, list("F9")...),
		append([]string{"M-P"}, list("F10")...),
		append([]string{"M-Space"}, list("F7")...),
		// Show the chosen item's state.
		append([]string{"M-?"}, list("F8")...),
		// Collapse the list and move to the right side, or expand it.
		append([]string{"M-B"}, list("F11")...),
		// Bookmarks: the next key is read in a table of its own, which
		// sends the list F12, the list's key and the mark.
		{"M-'", "switch-client", "-T", markGoTable, "\\;", "display-message", "go to a mark: press its letter, or ' to go back"},
		{"M-M", "switch-client", "-T", markSetTable, "\\;", "display-message", "mark the shown row: press a letter"},
		// Dragging the line between the panes changes the list's width,
		// and a double click on that line gives it the first width again.
		{"MouseDrag1Border", "resize-pane", "-M"},
		{"DoubleClick1Border", "resize-pane", "-t", pane, "-x", fmt.Sprint(SidebarWidth)},
		// Option-h and Option-l move the keyboard to the list on the
		// left and to the right side.
		{"M-h", "select-pane", "-t", pane},
		{"M-l", "select-pane", "-t", viewerPane},
		{"M-d", "if-shell", "-F", "#{pane_at_left}", "select-pane -t '" + viewerPane + "'", "select-pane -t '" + pane + "'"},
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
	if err := v.bindMarks(pane); err != nil {
		return err
	}
	return v.setAux()
}

// The key tables of the client in the right pane.
const (
	auxKeyTable    = "docket-aux"
	auxPrefixTable = "docket-aux-prefix"
)

// auxBlocked are the tmux commands the right pane's prefix keys do not
// run. Each acts on the session or the client of the right pane, which
// belong to the view: it would detach the client, attach it to another
// session, or add to, rename or end the view's session.
var auxBlocked = map[string]bool{
	"detach-client": true, "switch-client": true, "attach-session": true,
	"choose-tree": true, "choose-client": true, "choose-session": true,
	"new-session": true, "rename-session": true, "kill-session": true, "kill-server": true,
	"new-window": true, "break-pane": true, "move-window": true, "link-window": true,
}

// auxPrefixBindings returns tmux commands that copy the prefix table
// listed by list-keys into auxPrefixTable. A binding that runs a command
// of auxBlocked shows a message instead.
func auxPrefixBindings(listing string) string {
	var b strings.Builder
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		at := slices.Index(fields, "prefix")
		if len(fields) < at+3 || at < 1 || fields[at-1] != "-T" {
			continue
		}
		key := fields[at+1]
		blocked := false
		for _, f := range fields[at+2:] {
			blocked = blocked || auxBlocked[f]
		}
		if blocked {
			fmt.Fprintf(&b, "bind-key -T %s %s display-message 'not in the docket view: this key acts on the view'\n", auxPrefixTable, key)
			continue
		}
		fmt.Fprintln(&b, strings.Replace(line, "-T prefix ", "-T "+auxPrefixTable+" ", 1))
	}
	return b.String()
}

// bindAuxPrefix gives the right pane's client a prefix of its own. tmux
// has one prefix table for every session, so the session of the right
// pane turns tmux's prefix off and binds the same prefix keys to a copy
// of the prefix table, from which the commands of auxBlocked are left
// out. The copy is made each time the view is set up.
func (v *View) bindAuxPrefix() error {
	for _, opt := range [][2]string{{"key-table", auxKeyTable}, {"prefix", "None"}, {"prefix2", "None"}} {
		if _, err := v.Tmux("set-option", "-t", auxTarget, opt[0], opt[1]); err != nil {
			return fmt.Errorf("set %s for the right pane: %w", opt[0], err)
		}
	}
	listing, err := v.Tmux("list-keys", "-T", "prefix")
	if err != nil {
		return fmt.Errorf("read the prefix keys: %w", err)
	}
	commands := auxPrefixBindings(listing)
	for _, opt := range []string{"prefix", "prefix2"} {
		out, err := v.Tmux("show-options", "-gv", opt)
		if key := strings.TrimSpace(out); err == nil && key != "" && key != "None" {
			commands += fmt.Sprintf("bind-key -T %s %s switch-client -T %s\n", auxKeyTable, key, auxPrefixTable)
		}
	}
	for _, table := range []string{auxKeyTable, auxPrefixTable} {
		if _, err := v.Tmux("unbind-key", "-q", "-a", "-T", table); err != nil {
			return fmt.Errorf("clear the keys of %s: %w", table, err)
		}
	}
	f, err := os.CreateTemp("", "bay-docket-keys-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, werr := f.WriteString(commands)
	if err := f.Close(); werr != nil || err != nil {
		return fmt.Errorf("write the right pane's keys: %w", errors.Join(werr, err))
	}
	if _, err := v.Tmux("source-file", f.Name()); err != nil {
		return fmt.Errorf("bind the right pane's keys: %w", err)
	}
	return nil
}

// The key tables that read the mark after Option-' and Option-M.
const (
	markGoTable  = "docket-mark-go"
	markSetTable = "docket-mark-set"
)

// markKeys are the keys that can name a mark.
const markKeys = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// bindMarks empties and fills the two bookmark tables. Each table is filled
// by one tmux command. Emptying it is a command of its own: tmux drops
// keys bound in the same command that empties their table. A key that is
// not bound in the table ends the wait and does nothing.
func (v *View) bindMarks(pane string) error {
	for _, t := range []struct{ table, key string }{{markGoTable, "'"}, {markSetTable, "m"}} {
		if _, err := v.Tmux("unbind-key", "-q", "-a", "-T", t.table); err != nil {
			return fmt.Errorf("clear the keys of %s: %w", t.table, err)
		}
		keys := markKeys
		if t.key == "'" {
			// ' after Option-' returns to the row the last jump left.
			keys += "'"
		}
		var args []string
		for _, k := range keys {
			if args != nil {
				args = append(args, ";")
			}
			args = append(args, "bind-key", "-T", t.table, string(k), "send-keys", "-t", pane, "F12", t.key, string(k))
		}
		if _, err := v.Tmux(args...); err != nil {
			return fmt.Errorf("bind the keys of %s: %w", t.table, err)
		}
	}
	return nil
}

// ListHidden reports whether the right pane is zoomed, which hides the
// list.
func (v *View) ListHidden() bool {
	out, err := v.Tmux("display-message", "-p", "-t", v.listTarget(), "#{window_zoomed_flag}#{pane_active}")
	return err == nil && strings.TrimSpace(out) == "10"
}

// FillsWindow reports whether the list has the view's window to itself:
// a pane is zoomed, or the right pane has gone.
func (v *View) FillsWindow() bool {
	out, err := v.Tmux("display-message", "-p", "-t", v.listTarget(), "#{window_zoomed_flag} #{window_panes}")
	if err != nil {
		return false
	}
	zoomed, panes, _ := strings.Cut(strings.TrimSpace(out), " ")
	return zoomed == "1" || panes == "1"
}

// WindowWidth returns the width of the view's window, which follows the
// terminal attached to it, and 0 when tmux cannot say.
func (v *View) WindowWidth() int {
	out, err := v.Tmux("display-message", "-p", "-t", v.listTarget(), "#{window_width}")
	if err != nil {
		return 0
	}
	width, _ := strconv.Atoi(strings.TrimSpace(out))
	return width
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
	_, err := v.Tmux("resize-pane", "-t", v.listTarget(), "-x", fmt.Sprint(width))
	return err
}

// ensureViewer starts whichever of the view window's two panes is
// missing: the sidebar on the left, or the client on the right, and
// returns the id of the sidebar's pane. A pane is recognized by the end of
// the command it was started with, so a sidebar started by another copy of
// bay is recognized too.
func (v *View) ensureViewer() (string, error) {
	out, err := v.Tmux("list-panes", "-t", viewTarget, "-F", "#{pane_id}\t#{pane_start_command}")
	if err != nil {
		return "", fmt.Errorf("list the view's panes: %w", err)
	}
	// The client attaches to the tmux server the view runs on. Its socket
	// is written into the command, which then needs no shell's variables.
	socket, err := v.Tmux("display-message", "-p", "#{socket_path}")
	if err != nil {
		return "", fmt.Errorf("find the tmux server's socket: %w", err)
	}
	client := "env -u TMUX " + v.TmuxCommand + " -S '" + strings.ReplaceAll(strings.TrimSpace(socket), "'", `'\''`) + "' " + clientAttach
	sidebar, viewer := "", false
	for _, line := range strings.Split(out, "\n") {
		id, command, _ := strings.Cut(strings.TrimSpace(line), "\t")
		// tmux prints the command inside double quotes.
		command = strings.Trim(command, "\"")
		if sidebar == "" && strings.HasSuffix(command, sidebarSuffix) {
			sidebar = id
		}
		viewer = viewer || strings.HasSuffix(command, clientAttach) || strings.HasSuffix(command, oldClientAttach)
	}
	if sidebar != "" && viewer {
		return sidebar, nil
	}
	if !viewer {
		if _, err := v.Tmux("split-window", "-h", "-d", "-t", viewTarget, client); err != nil {
			return "", fmt.Errorf("start the right pane: %w", err)
		}
	}
	if sidebar == "" {
		id, err := v.Tmux("split-window", "-h", "-b", "-f", "-d", "-P", "-F", "#{pane_id}", "-t", viewTarget, v.SidebarCommand)
		if err != nil {
			return "", fmt.Errorf("start the sidebar: %w", err)
		}
		sidebar = strings.TrimSpace(id)
	}
	// A pane was started, so the sidebar gets the saved width. A width
	// chosen later is kept.
	width := SidebarWidth
	if v.Width >= CollapsedBelow {
		width = v.Width
	}
	_, err = v.Tmux("resize-pane", "-t", sidebar, "-x", fmt.Sprint(width))
	return sidebar, err
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
// markSeen clears what selecting a window in its dock clears: bay's
// waiting mark, and the bell flag on the dock's tab. tmux clears a tab's
// bell flag only when the tab is selected, so when the flag is set the
// window is selected in the dock's session and the window that was
// current there is selected again. The dock's previous window, used by
// tmux's last-window, becomes this window. The monitor sets the waiting
// mark again while the agent's prompt is still on screen.
func (v *View) markSeen(dock, window string) {
	target := "=" + dock + ":" + window
	_, _ = v.Tmux("set-window-option", "-t", target, "@bay-waiting", "0")
	bell, err := v.Tmux("display-message", "-p", "-t", target, "#{window_bell_flag}")
	if err != nil || strings.TrimSpace(bell) != "1" {
		return
	}
	current, err := v.Tmux("display-message", "-p", "-t", "="+dock+":", "#{window_id}")
	if err != nil || strings.TrimSpace(current) == "" {
		return
	}
	if _, err := v.Tmux("select-window", "-t", target); err == nil {
		_, _ = v.Tmux("select-window", "-t", "="+dock+":"+strings.TrimSpace(current))
	}
}

func (v *View) Show(row Row, focus bool) (note string, err error) {
	if row.Area {
		// An area with no item to show leaves the right side as it is.
		if focus {
			_, err = v.Tmux("select-pane", "-t", viewerPane)
		}
		return "", err
	}
	if started, err := v.ensureAux(); err != nil {
		return "", err
	} else if started {
		if err := v.setAux(); err != nil {
			return "", err
		}
	}
	if _, err := v.ensureViewer(); err != nil {
		return "", err
	}
	client, err := v.viewerClient()
	if err != nil {
		return "", err
	}
	window, dock := "", ""
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
			dock, _, _ = strings.Cut(row.Bays[0], "/")
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
		return "", fmt.Errorf("the record window was closed; run 'bay docket view' again")
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
	if dock != "" {
		v.markSeen(dock, window)
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
