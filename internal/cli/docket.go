package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/commonfabric/bay/internal/docket"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newDocketCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docket",
		Short: "Navigate docket's work items beside their bay windows",
		Long: `Show docket's work items in a list beside the bay window of the chosen item.

docket is a separate tool that keeps a tree of work items. These commands
read its files. The view changes an item's level, by running
'docket set', when > or < is pressed in the list, and closes an item, by
running 'docket close', when x is pressed. It keeps its marks and
the list's width in files of its own in docket's folder.`,
	}
	cmd.AddCommand(newDocketViewCmd(), newDocketSidebarCmd(), newDocketKeysCmd(), newDocketStateCmd())
	return cmd
}

func realTmux(args ...string) (string, error) {
	var stderr strings.Builder
	cmd := exec.Command("tmux", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("tmux %s: %s", args[0], msg)
	}
	return string(out), nil
}

// tmuxText returns text that tmux's display-message shows as it is: tmux
// reads "#" there as the start of a format.
func tmuxText(text string) string { return strings.ReplaceAll(text, "#", "##") }

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shownOnTheRight reports whether the calling pane's window is one the
// view's right side holds. A client switched from there would be the
// right side's own client, so the caller is already in the view.
func shownOnTheRight() bool {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return false
	}
	window, err := realTmux("display-message", "-p", "-t", pane, "#{window_id}")
	if err != nil {
		return false
	}
	held, err := realTmux("list-windows", "-t", "="+docket.AuxSession+":", "-F", "#{window_id}")
	if err != nil {
		return false
	}
	for _, id := range strings.Fields(held) {
		if id == strings.TrimSpace(window) {
			return true
		}
	}
	return false
}

func newDocketView() (*docket.View, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	manifestFile := bayPaths().ManifestFile
	oldWidth := ""
	if stateDir, err := docket.StateDir(); err == nil {
		oldWidth = oldWidthFile(stateDir)
	}
	width := loadWidth(bayPaths().DocketViewWidth, oldWidth)
	return &docket.View{
		Width:          width,
		Tmux:           realTmux,
		SidebarCommand: shellQuote(self) + " docket sidebar",
		TmuxCommand:    "tmux",
		BayWindow: func(ref string) (string, bool) {
			dockName, _, _ := strings.Cut(ref, "/")
			bay := lookupBay(manifestFile, ref)
			if bay == nil {
				return "", false
			}
			var windows []string
			for _, s := range bay.Surfaces {
				if s.Tmux != nil && s.Tmux.WindowID != "" {
					windows = append(windows, s.Tmux.WindowID)
				}
			}
			if len(windows) == 0 {
				return "", false
			}
			if len(windows) > 1 {
				flagged := flaggedWindows()
				for _, id := range windows {
					if flagged[dockName+"\t"+id] {
						return id, true
					}
				}
			}
			return windows[0], true
		},
		BayDir: func(ref string) (string, bool) {
			if bay := lookupBay(manifestFile, ref); bay != nil && bay.Path != "" {
				return bay.Path, true
			}
			return "", false
		},
		BayKeys: viewBayKeys(),
		RecordCommand: func(item string) string {
			return "docket show " + shellQuote(item) + " 2>&1 | less -R"
		},
		StartDesk: func() error {
			out, err := exec.Command("docket", "desk", "--no-switch").CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s", strings.TrimSpace(string(out)))
			}
			return nil
		},
	}, nil
}

func newDocketViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "view",
		Short: "Open the docket view: the item list, and the chosen item's bay window",
		Long: `Open the tmux session named "_docket".

Its window has the item list on the left. The right side shows the bay
window of the item chosen in the list, with all of that window's panes.
The bay window stays in its own tmux session and is not changed.

` + docket.KeysHelp + "\n\nMarks:\n" + legendText(),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			view, err := newDocketView()
			if err != nil {
				return err
			}
			if err := view.Setup(); err != nil {
				return err
			}
			target := "=" + docket.ViewSession + ":" + docket.ViewWindow
			if os.Getenv("TMUX") == "" {
				attach := exec.Command("tmux", "attach-session", "-t", target)
				attach.Stdin, attach.Stdout, attach.Stderr = os.Stdin, os.Stdout, os.Stderr
				return attach.Run()
			}
			if shownOnTheRight() {
				_, err = realTmux("select-pane", "-t", "="+docket.ViewSession+":"+docket.ViewWindow+".{left}")
				return err
			}
			_, err = realTmux("switch-client", "-t", target)
			return err
		},
	}
}

func newDocketSidebarCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "sidebar",
		Short:  "Run the item list in this terminal (started by 'bay docket view')",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			view, err := newDocketView()
			if err != nil {
				return err
			}
			stateDir, err := docket.StateDir()
			if err != nil {
				return err
			}
			return runSidebar(view, stateDir)
		},
	}
}

// styles maps a docket.Span style to the terminal codes that draw it.
var styles = map[string]string{
	"area":     "\x1b[1;38;5;111m",
	"desk":     "\x1b[35m",
	"bay":      "\x1b[36m",
	"dim":      "\x1b[2m",
	"running":  "\x1b[32m",
	"ready":    "\x1b[33m",
	"held":     "\x1b[31m",
	"question": "\x1b[1;33m",
	"blocking": "\x1b[1;31m",
	"key":      barKey,
	"waiting":  "\x1b[1;38;5;208m",
	"shown":    shownStyle,
	"match":    "\x1b[1;7m",
	"heading":  "\x1b[1m",
}

// bayWindows maps each bay, as dock/bay, to the ids of its windows. It
// reads bay's state file again only when the file has changed.
type bayWindows struct {
	read    time.Time
	windows map[string][]string
}

func (b *bayWindows) load() map[string][]string {
	file := bayPaths().ManifestFile
	info, err := os.Stat(file)
	if err != nil {
		return nil
	}
	if b.windows != nil && info.ModTime().Equal(b.read) {
		return b.windows
	}
	m, err := manifest.Load(file)
	if err != nil {
		return b.windows
	}
	b.read, b.windows = info.ModTime(), map[string][]string{}
	for _, dock := range m.Docks {
		for _, bay := range dock.Bays {
			for _, s := range bay.Surfaces {
				if s.Tmux != nil && s.Tmux.WindowID != "" {
					ref := dock.Name + "/" + bay.ID
					b.windows[ref] = append(b.windows[ref], dock.Name+"\t"+s.Tmux.WindowID)
				}
			}
		}
	}
	return b.windows
}

// waitingBays returns the bays, as dock/bay, with a window that bay marks
// as waiting for the person or that rang the terminal bell, as bay's own
// lists of waiting bays count them. A window is looked up in its dock's
// session, because a bell is recorded for each session the window is in.
func waitingBays(windows map[string][]string) map[string]bool {
	flagged := flaggedWindows()
	if flagged == nil {
		return nil
	}
	waiting := map[string]bool{}
	for ref, ids := range windows {
		for _, id := range ids {
			if flagged[id] {
				waiting[ref] = true
			}
		}
	}
	return waiting
}

// flaggedWindows returns the windows, as session and window id joined by a
// tab, that bay marks as waiting or that rang the terminal bell. It
// returns nil when tmux cannot list them.
func flaggedWindows() map[string]bool {
	out, err := realTmux("list-windows", "-a", "-F", "#{session_name}\t#{window_id}\t#{@bay-waiting}\t#{window_bell_flag}")
	if err != nil {
		return nil
	}
	flagged := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) == 4 && (parts[2] == "1" || parts[3] == "1") {
			flagged[parts[0]+"\t"+parts[1]] = true
		}
	}
	return flagged
}

// missingBays returns the bays the items name, as dock/bay, that bay's
// own list does not hold. It returns none when that list cannot be read.
func missingBays(items map[string]docket.Item) map[string]bool {
	missing := map[string]bool{}
	m, err := manifest.Load(bayPaths().ManifestFile)
	if err != nil {
		return missing
	}
	for _, item := range items {
		for _, ref := range item.BayRefs() {
			dockName, bayID, _ := strings.Cut(ref, "/")
			if dock := m.FindDock(dockName); dock == nil || dock.FindBayByID(bayID) == nil {
				missing[ref] = true
			}
		}
	}
	return missing
}

// barStyle draws the lines that describe the view: light grey text on a
// dark grey background. barKey draws the name of a key in such a line in
// bold white, and barText returns to the bar's own text.
const (
	barStyle = "\x1b[48;5;237;38;5;250m"
	// shownStyle draws the row the right side shows while the cursor is
	// on another row: white text on a grey background.
	shownStyle = "\x1b[48;5;240;38;5;255m"
	barKey     = "\x1b[1;97m"
	barText    = "\x1b[22;38;5;250m"
)

// styled returns a line with the terminal codes for its styles.
func styled(line docket.Line) string {
	var b strings.Builder
	for _, span := range line.Spans {
		if code := styles[span.Style]; code != "" {
			b.WriteString(code + span.Text + "\x1b[0m")
		} else {
			b.WriteString(span.Text)
		}
	}
	return b.String()
}

func newDocketKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "keys",
		Short:  "Show the docket view's keys and what the list's marks mean",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stateDir, err := docket.StateDir()
			if err != nil {
				return err
			}
			return page(keysPage(stateDir))
		},
	}
}

// legendText is the legend of the list's marks as plain text.
func legendText() string {
	var b strings.Builder
	for _, line := range docket.Legend() {
		b.WriteString(line.String() + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// The list keeps its marks and its hotlist in the root item's folder, as
// docket data. The view is the only program that writes these files.
// Before the root item existed they were view-marks.json and
// view-hotlist.json in docket's folder; each is read from there when the
// new file is missing, and removed when the new file is written.
const (
	marksName      = "marks.json"
	oldMarksName   = "view-marks.json"
	hotlistName    = "hotlist.json"
	oldHotlistName = "view-hotlist.json"
)

// rootFile is the path of a file in the root item's folder.
func rootFile(stateDir, name string) string {
	return filepath.Join(stateDir, "items", docket.RootItem, name)
}

// loadViewData reads a file the list keeps in the root item's folder into
// v, or the file's old copy in docket's folder when the new one is missing.
func loadViewData(stateDir, name, oldName string, v any) {
	data, err := os.ReadFile(rootFile(stateDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		data, err = os.ReadFile(filepath.Join(stateDir, oldName))
	}
	if err == nil {
		_ = json.Unmarshal(data, v)
	}
}

// saveViewData writes v to a file in the root item's folder, replacing the
// file atomically, and removes the file's old copy.
func saveViewData(stateDir, name, oldName string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	path := rootFile(stateDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(stateDir, oldName))
	return nil
}

// loadMarks reads the list's marks: for each mark, the name of its row. A
// mark on docket.OldDeskRow is a mark on the desk.
func loadMarks(stateDir string) map[string]string {
	marks := map[string]string{}
	loadViewData(stateDir, marksName, oldMarksName, &marks)
	for mark, name := range marks {
		if name == docket.OldDeskRow {
			marks[mark] = docket.DeskRow
		}
	}
	return marks
}

func saveMarks(stateDir string, marks map[string]string) error {
	return saveViewData(stateDir, marksName, oldMarksName, marks)
}

// loadHotlist reads the list's hotlist: the names of its rows, in order.
func loadHotlist(stateDir string) []string {
	var names []string
	loadViewData(stateDir, hotlistName, oldHotlistName, &names)
	return names
}

func saveHotlist(stateDir string, names []string) error {
	return saveViewData(stateDir, hotlistName, oldHotlistName, names)
}

// oldWidthFile is where the list's width was kept before it moved to bay's
// data folder. loadWidth reads it when the new file is missing, and
// saveWidth removes it.
func oldWidthFile(stateDir string) string { return filepath.Join(stateDir, "view-width.json") }

// loadWidth reads the list's saved width, and 0 when none is saved.
func loadWidth(path, oldPath string) int {
	var saved struct {
		Width int `json:"width"`
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && oldPath != "" {
		data, err = os.ReadFile(oldPath)
	}
	if err == nil {
		_ = json.Unmarshal(data, &saved)
	}
	if saved.Width < docket.CollapsedBelow {
		return 0
	}
	return saved.Width
}

func saveWidth(path, oldPath string, width int) error {
	data, err := json.Marshal(map[string]int{"width": width})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	if oldPath != "" {
		_ = os.Remove(oldPath)
	}
	return nil
}

// loadShownRow reads the key of the row the right side last showed, and ""
// when none is saved.
func loadShownRow(path string) string {
	var saved struct {
		Row string `json:"row"`
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &saved)
	}
	return saved.Row
}

func saveShownRow(path, key string) error {
	data, err := json.Marshal(map[string]string{"row": key})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// keysPage is the text of the keys popup: the keys, the marks that are
// set, then the legend.
func keysPage(stateDir string) []docket.Line {
	var lines []docket.Line
	for _, text := range strings.Split(docket.KeysHelp, "\n") {
		style := ""
		if strings.HasSuffix(text, ":") && !strings.HasPrefix(text, " ") {
			style = "heading"
		}
		lines = append(lines, docket.Line{Spans: []docket.Span{{Text: text, Style: style}}})
	}
	items, _ := docket.LoadItemsWithBays(stateDir, bayPaths().ManifestFile)
	lines = append(lines, docket.Line{}, docket.Line{Spans: []docket.Span{{Text: "Your bookmarks:", Style: "heading"}}})
	lines = append(lines, docket.MarkLines(loadMarks(stateDir), items)...)
	lines = append(lines, docket.Line{}, docket.Line{Spans: []docket.Span{{Text: "Marks and colors:", Style: "heading"}}})
	return append(lines, docket.Legend()...)
}

func newDocketStateCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "state <item>",
		Short:  "Show where a docket item's work stands",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stateDir, err := docket.StateDir()
			if err != nil {
				return err
			}
			text, err := docket.Summary(stateDir, bayPaths().ManifestFile, args[0])
			if err != nil {
				return err
			}
			width := 100
			if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
				width = w - 2
			}
			var lines []docket.Line
			for k, text := range docket.Wrap(text, width) {
				style := ""
				switch {
				case k == 0, text == "State":
					style = "heading"
				case strings.HasPrefix(text, "Question "):
					style = "question"
				case strings.HasPrefix(text, "Waits on:"):
					style = "held"
				case k == 1:
					style = "dim"
				}
				lines = append(lines, docket.Line{Spans: []docket.Span{{Text: " " + text, Style: style}}})
			}
			return page(lines)
		},
	}
}

// page shows lines in the terminal and waits. j, k, Space, the arrows and
// the wheel scroll. q, Esc or Enter closes. Without a terminal it prints
// the lines and returns.
func page(lines []docket.Line) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) || !term.IsTerminal(int(os.Stdout.Fd())) {
		for _, line := range lines {
			fmt.Println(line.String())
		}
		return nil
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, old)
	fmt.Print("\x1b[?25l\x1b[?1000h\x1b[?1006h")
	defer fmt.Print("\x1b[?1006l\x1b[?1000l\x1b[?25h")
	top := 0
	buf := make([]byte, 256)
	pending := ""
	for {
		_, height, err := term.GetSize(fd)
		if err != nil {
			height = 24
		}
		top = max(0, min(top, len(lines)-height))
		var b strings.Builder
		b.WriteString("\x1b[H")
		for k := 0; k < height; k++ {
			if top+k < len(lines) {
				b.WriteString(styled(lines[top+k]))
			}
			b.WriteString("\x1b[K")
			if k < height-1 {
				b.WriteString("\r\n")
			}
		}
		fmt.Print(b.String())
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return nil
		}
		var keys []string
		keys, pending = docket.ParseKeys(pending + string(buf[:n]))
		for _, key := range keys {
			switch key {
			case "q", "esc", "enter", "ctrl-c":
				return nil
			case "j", "down", "wheeldown":
				top++
			case "k", "up", "wheelup":
				top--
			case " ":
				top += height - 1
			case "g":
				top = 0
			case "G":
				top = len(lines)
			}
		}
	}
}

// popup opens a tmux popup that runs one of this program's docket
// commands.
func popup(title, width, height, command string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	_, err = realTmux("display-popup", "-E", "-T", " "+title+" ", "-w", width, "-h", height, shellQuote(self)+" docket "+command)
	return err
}

// showState opens a popup that says where the item's work stands.
func showState(item string) error {
	return popup(item, "80%", "70%", "state "+shellQuote(item))
}

// showKeys opens a popup that lists the view's keys and the meaning of
// the list's marks.
func showKeys(stateDir string) error {
	lines := keysPage(stateDir)
	width := 0
	for _, line := range lines {
		width = max(width, len([]rune(line.String())))
	}
	// The popup is as large as its text, and no larger than the terminal
	// allows. Text that does not fit is scrolled.
	maxWidth, maxHeight := 0, 0
	if out, err := realTmux("display-message", "-p", "#{client_width} #{client_height}"); err == nil {
		_, _ = fmt.Sscan(out, &maxWidth, &maxHeight)
	}
	return popup("docket view keys", fmt.Sprint(fitPopup(width+4, maxWidth)), fmt.Sprint(fitPopup(len(lines)+2, maxHeight)), "keys")
}

// fitPopup returns the size to give a popup that wants want cells along
// one side of a terminal with room cells: want, or two fewer than room
// when want is larger. With room unknown, 0, it returns want.
func fitPopup(want, room int) int {
	if room > 2 && want > room-2 {
		return room - 2
	}
	return want
}

// readKeys sends the name of each key and mouse press read from the
// terminal, as docket.ParseKeys names them.
// closeBay closes a bay with 'bay close', which refuses a bay with
// uncommitted changes unless it is forced. A bay that no longer exists is
// taken off its item instead.
func closeBay(b docket.BayClose) (note string, err error) {
	if b.Gone {
		out, err := exec.Command("docket", "site", b.Item, "bay", b.Ref, "--remove").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("docket site --remove: %s", strings.TrimSpace(string(out)))
		}
		return b.Ref + " is off " + b.Item, nil
	}
	dock, bayID, ok := strings.Cut(b.Ref, "/")
	if !ok {
		return "", fmt.Errorf("%s does not name a dock and a bay", b.Ref)
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	args := []string{"close", bayID, "--dock", dock}
	if b.Force {
		args = append(args, "--force")
	}
	out, err := exec.Command(self, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if !b.Force {
			msg += "; X closes it anyway"
		}
		return "", fmt.Errorf("bay close %s: %s", b.Ref, msg)
	}
	return b.Ref + " is closed", nil
}

// listWidth follows the width of the list's pane, to keep the width the
// list is given and not one that a change in the terminal's size gives it.
type listWidth struct {
	drawn  int // the list's width when it was last drawn
	window int // the width of the view's window at that time
	saved  int // the width saved for the next start
}

// changed is called when the list is drawn at a new width. window is the
// width of the view's window, and passing is true while the list is
// collapsed or has the window to itself. It returns a width to give the list back,
// or 0, and whether to save the new width.
func (w *listWidth) changed(width, window int, passing bool) (restore int, save bool) {
	w.drawn = width
	resized := window != w.window
	w.window = window
	switch {
	case passing:
		return 0, false
	case resized && w.saved >= docket.CollapsedBelow:
		if width != w.saved {
			return w.saved, false
		}
		return 0, false
	}
	w.saved = width
	return 0, true
}

func readKeys(keys chan<- string) {
	buf := make([]byte, 256)
	pending := ""
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			close(keys)
			return
		}
		var names []string
		names, pending = docket.ParseKeys(pending + string(buf[:n]))
		for _, name := range names {
			keys <- name
		}
	}
}

func runSidebar(view *docket.View, stateDir string) error {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("the sidebar needs a terminal: %w", err)
	}
	defer term.Restore(fd, old)
	// Use the alternate screen, hide the cursor, and ask for mouse presses.
	fmt.Print("\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h")
	defer fmt.Print("\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[?1049l")

	items, err := docket.LoadItemsWithBays(stateDir, bayPaths().ManifestFile)
	if err != nil {
		return err
	}
	sidebar := docket.NewSidebar(items)
	if order, err := docket.LoadRootOrder(stateDir); err == nil {
		sidebar.SetRootOrder(order)
	}
	sidebar.SetMissing(missingBays(items))
	var windows bayWindows
	sidebar.SetWaiting(waitingBays(windows.load()))
	// The marks are kept in a file so that they outlast the list. A mark
	// whose item is closed or gone is forgotten.
	sidebar.SetMarks(loadMarks(stateDir))
	forgetMarks := func() {
		if sidebar.ForgetMarks() {
			_ = saveMarks(stateDir, sidebar.Marks())
		}
	}
	forgetMarks()
	sidebar.SetHotlist(loadHotlist(stateDir))
	forgetHotlist := func() {
		if sidebar.ForgetHotlist() {
			_ = saveHotlist(stateDir, sidebar.Hotlist())
		}
	}
	forgetHotlist()
	// docket doctor checks docket's files after each load, in the
	// background, and the list shows a line while it finds problems. A load
	// during a check starts one more check when it ends.
	problems := 0
	checked := make(chan int, 1)
	checking, again := false, false
	check := func() {
		if checking {
			again = true
			return
		}
		checking = true
		go func() {
			out, err := exec.Command(docket.DoctorCommand[0], docket.DoctorCommand[1:]...).Output()
			n, parseErr := docket.DoctorProblems(out)
			if err != nil || parseErr != nil {
				n = 0
			}
			checked <- n
		}()
	}
	check()
	// The list starts on the row the right side still shows from before.
	sidebar.ReturnTo(loadShownRow(bayPaths().DocketViewRow))
	// The list starts at the width it had when it was last used. A width
	// the list is later given is saved, except a collapsed one and one a
	// zoomed pane gives it. When the terminal changes size, tmux changes
	// the list's width too; the saved width is then given back to it.
	view.ListPane = os.Getenv("TMUX_PANE")
	if view.Width >= docket.CollapsedBelow {
		_ = view.SetWidth(view.Width)
	}
	widths := listWidth{drawn: view.Width, saved: view.Width, window: view.WindowWidth()}
	note := ""
	draw := func() {
		width, height, err := term.GetSize(fd)
		if err != nil {
			width, height = docket.SidebarWidth, 40
		}
		lines, cursor := sidebar.RenderLines(width, height)
		if err == nil && width != widths.drawn {
			restore, save := widths.changed(width, view.WindowWidth(), sidebar.Collapsed() || view.FillsWindow())
			if restore > 0 {
				_ = view.SetWidth(restore)
			}
			if save {
				_ = saveWidth(bayPaths().DocketViewWidth, oldWidthFile(stateDir), width)
			}
		}
		shown := note
		if shown == "" {
			shown = docket.DoctorNotice(problems)
		}
		if shown != "" && !sidebar.Collapsed() {
			lines[len(lines)-1] = docket.NoticeLine(shown, width)
		}
		var b strings.Builder
		b.WriteString("\x1b[H")
		for k, line := range lines {
			text := line.String()
			pad := strings.Repeat(" ", max(0, width-docket.TextWidth(text)))
			switch {
			case k == cursor:
				b.WriteString("\x1b[7m" + text + pad + "\x1b[0m")
			case line.Shown:
				b.WriteString(shownStyle + text + pad + "\x1b[0m")
			case line.Bar:
				b.WriteString(barStyle)
				for _, span := range line.Spans {
					if span.Style == "key" {
						b.WriteString(barKey + span.Text + barText)
					} else {
						b.WriteString(span.Text)
					}
				}
				b.WriteString(pad + "\x1b[0m")
			default:
				b.WriteString(styled(line))
			}
			b.WriteString("\x1b[K")
			if k < len(lines)-1 {
				b.WriteString("\r\n")
			}
		}
		fmt.Print(b.String())
	}
	reload := func() {
		items, err := docket.LoadItemsWithBays(stateDir, bayPaths().ManifestFile)
		if err != nil {
			note = err.Error()
			return
		}
		sidebar.SetItems(items)
		if order, err := docket.LoadRootOrder(stateDir); err == nil {
			sidebar.SetRootOrder(order)
		}
		sidebar.SetMissing(missingBays(items))
		forgetMarks()
		forgetHotlist()
		check()
	}

	keys := make(chan string, 64)
	go readKeys(keys)
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	// docket rewrites this file whenever it changes an item.
	stamp := filepath.Join(stateDir, "changed")
	var seen time.Time
	if info, err := os.Stat(stamp); err == nil {
		seen = info.ModTime()
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	owed := false // the cursor moved and its row has not been shown
	draw()
	for {
		select {
		case key, ok := <-keys:
			if !ok {
				return nil
			}
			if key == "ctrl-c" {
				// The view's tmux keys send keys to this pane, so the list
				// keeps running.
				note = "the list keeps running; ? shows how to leave the view"
				draw()
				continue
			}
			note = ""
			var action docket.Action
			if at, ok := strings.CutPrefix(key, "click:"); ok {
				line, column, _ := strings.Cut(at, ":")
				l, _ := strconv.Atoi(line)
				c, _ := strconv.Atoi(column)
				action = sidebar.Click(l, c)
			} else {
				action = sidebar.Key(key)
			}
			note = sidebar.Notice()
			if action == docket.Quit {
				// Leave the view without closing it: return the terminal
				// to the tmux session it came from, or detach it when it
				// came from outside tmux. The list keeps running.
				if _, err := realTmux("switch-client", "-l"); err != nil {
					if _, err := realTmux("detach-client"); err != nil {
						note = err.Error()
					}
				}
				action = docket.None
			}
			var err error
			switch action {
			case docket.Help:
				err = showKeys(stateDir)
			case docket.State:
				row := sidebar.Current()
				if _, statErr := os.Stat(rootFile(stateDir, "item.json")); row.Desk && statErr != nil {
					note = "the desk has no item to show"
				} else {
					item, _, _ := strings.Cut(row.Name, " ")
					err = showState(item)
				}
			case docket.Collapse:
				err = view.Collapse()
			case docket.Expand:
				err = view.SetWidth(sidebar.FullWidth())
			case docket.ResetWidth:
				err = view.SetWidth(docket.SidebarWidth)
			case docket.MarksChanged:
				err = saveMarks(stateDir, sidebar.Marks())
			case docket.HotlistChanged:
				err = saveHotlist(stateDir, sidebar.Hotlist())
			case docket.MoveSibling:
				item, flag, sibling := sidebar.SiblingRequest()
				out, runErr := exec.Command("docket", "set", item, flag, sibling).CombinedOutput()
				if runErr != nil {
					err = fmt.Errorf("docket set %s %s %s: %s", item, flag, sibling, strings.TrimSpace(string(out)))
				} else {
					note = item + " moved " + strings.TrimPrefix(flag, "--") + " " + sibling
					reload()
				}
			case docket.CloseItem:
				item, outcome, reason := sidebar.CloseRequest()
				args := []string{"close", item, outcome}
				if reason != "" {
					args = append(args, reason)
				}
				out, runErr := exec.Command("docket", args...).CombinedOutput()
				if runErr != nil {
					err = fmt.Errorf("docket close %s: %s", item, strings.TrimSpace(string(out)))
				} else {
					note = item + " is closed as " + outcome
					reload()
				}
			case docket.CloseBay:
				note, err = closeBay(sidebar.BayCloseRequest())
				reload()
			case docket.Deeper, docket.Nearer:
				item, level, ok := sidebar.LevelChange(action)
				switch {
				case item == "":
					note = "only an item has a level"
				case !ok:
					note = item + " is already at that end"
				default:
					out, runErr := exec.Command("docket", "set", item, "--level", level).CombinedOutput()
					if runErr != nil {
						err = fmt.Errorf("docket set %s --level %s: %s", item, level, strings.TrimSpace(string(out)))
					} else {
						note = item + " is now " + level
						reload()
					}
				}
			}
			if err != nil {
				note = err.Error()
			}
			if action != docket.Show && action != docket.ShowAndFocus {
				action = docket.None
				// A note the list cannot show, such as the answer to a
				// bookmark key pressed on the right side, is shown for a
				// moment at the bottom of the terminal.
				if note != "" && (sidebar.Collapsed() || view.ListHidden()) {
					_, _ = realTmux("display-message", "-d", "1500", tmuxText(note))
				}
			}
			// While more keys are waiting, only the cursor moves. The row
			// is shown once the keys stop, so a held key does not show
			// every row it passes.
			if action == docket.Show && len(keys) > 0 {
				owed = true
				action = docket.None
			} else if action == docket.None && owed && len(keys) == 0 {
				action = docket.Show
			}
			if action == docket.Show || action == docket.ShowAndFocus {
				owed = false
				shown, err := view.Show(sidebar.Shown(), action == docket.ShowAndFocus)
				note = shown
				if err != nil {
					note = err.Error()
				} else {
					_ = saveShownRow(bayPaths().DocketViewRow, sidebar.ShownKey())
				}
				// A collapsed list, or one hidden by a zoomed right
				// pane, cannot show where the cursor is, so the row's
				// name is shown for a moment.
				if sidebar.Collapsed() || view.ListHidden() {
					row := sidebar.Current()
					name := row.Name
					if row.Area {
						name = "area: " + row.Title
					}
					_, _ = realTmux("display-message", "-d", "1500", tmuxText(name))
				}
			}
		case <-resized:
		case n := <-checked:
			checking = false
			if again {
				again = false
				check()
			}
			if n == problems {
				continue
			}
			problems = n
		case <-tick.C:
			changed := sidebar.SetWaiting(waitingBays(windows.load()))
			if info, err := os.Stat(stamp); err == nil && info.ModTime().After(seen) {
				seen = info.ModTime()
				reload()
				changed = true
			}
			if !changed {
				continue
			}
		}
		draw()
	}
}

// lookupBay returns the bay named "dock/bay" in the manifest, or nil.
func lookupBay(manifestFile, ref string) *manifest.Bay {
	dockName, bayID, ok := strings.Cut(ref, "/")
	if !ok {
		return nil
	}
	m, err := manifest.Load(manifestFile)
	if err != nil {
		return nil
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return nil
	}
	return dock.FindBayByID(bayID)
}

// viewBayKeys lists bay's own keys for the docket view to bind again.
func viewBayKeys() []docket.BayKey {
	var keys []docket.BayKey
	for _, kb := range bayKeybindings {
		keys = append(keys, docket.BayKey{
			Table: kb.table, Key: kb.key, Command: kb.cmd,
			Tmux: kb.isTmuxCommand, Verb: kb.tmuxVerb, Global: kb.global,
		})
	}
	return keys
}
