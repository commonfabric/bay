package cli

import (
	"fmt"
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
read its files and do not change them.`,
	}
	cmd.AddCommand(newDocketViewCmd(), newDocketSidebarCmd(), newDocketKeysCmd(), newDocketStateCmd())
	return cmd
}

func realTmux(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func newDocketView() (*docket.View, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	manifestFile := bayPaths().ManifestFile
	return &docket.View{
		Tmux:           realTmux,
		SidebarCommand: shellQuote(self) + " docket sidebar",
		TmuxCommand:    "tmux",
		BayWindow: func(ref string) (string, bool) {
			dockName, bayID, ok := strings.Cut(ref, "/")
			if !ok {
				return "", false
			}
			m, err := manifest.Load(manifestFile)
			if err != nil {
				return "", false
			}
			dock := m.FindDock(dockName)
			if dock == nil {
				return "", false
			}
			bay := dock.FindBayByID(bayID)
			if bay == nil {
				return "", false
			}
			for _, s := range bay.Surfaces {
				if s.Tmux != nil && s.Tmux.WindowID != "" {
					return s.Tmux.WindowID, true
				}
			}
			return "", false
		},
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
		Long: `Open the tmux session named "docket".

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
	"area":     "\x1b[1;38;5;75m",
	"desk":     "\x1b[35m",
	"bay":      "\x1b[36m",
	"dim":      "\x1b[2m",
	"running":  "\x1b[32m",
	"ready":    "\x1b[33m",
	"held":     "\x1b[31m",
	"question": "\x1b[1;33m",
	"blocking": "\x1b[1;31m",
	"heading":  "\x1b[1m",
}

// barStyle draws the lines that describe the view: light text on a grey
// background.
const barStyle = "\x1b[48;5;237;38;5;250m"

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
			return page(keysPage())
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

// keysPage is the text of the keys popup: the keys, then the legend.
func keysPage() []docket.Line {
	var lines []docket.Line
	for _, text := range strings.Split(docket.KeysHelp, "\n") {
		style := ""
		if strings.HasSuffix(text, ":") && !strings.HasPrefix(text, " ") {
			style = "heading"
		}
		lines = append(lines, docket.Line{Spans: []docket.Span{{Text: text, Style: style}}})
	}
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
			text, err := docket.Summary(stateDir, args[0])
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
func showKeys() error {
	lines := keysPage()
	width := 0
	for _, line := range lines {
		width = max(width, len([]rune(line.String())))
	}
	return popup("docket view keys", fmt.Sprint(width+4), fmt.Sprint(len(lines)+2), "keys")
}

// readKeys sends the name of each key and mouse press read from the
// terminal, as docket.ParseKeys names them.
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

	items, err := docket.LoadItems(stateDir)
	if err != nil {
		return err
	}
	sidebar := docket.NewSidebar(items)
	note := ""
	draw := func() {
		width, height, err := term.GetSize(fd)
		if err != nil {
			width, height = docket.SidebarWidth, 40
		}
		lines, cursor := sidebar.RenderLines(width, height)
		if note != "" && !sidebar.Collapsed() {
			lines[len(lines)-1] = docket.Line{Spans: []docket.Span{{Text: note}}, Bar: true}
		}
		var b strings.Builder
		b.WriteString("\x1b[H")
		for k, line := range lines {
			text := line.String()
			pad := strings.Repeat(" ", max(0, width-len([]rune(text))))
			switch {
			case k == cursor:
				b.WriteString("\x1b[7m" + text + pad + "\x1b[0m")
			case line.Bar:
				b.WriteString(barStyle + text + pad + "\x1b[0m")
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
		items, err := docket.LoadItems(stateDir)
		if err != nil {
			note = err.Error()
			return
		}
		sidebar.SetItems(items)
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
			if !ok || key == "ctrl-c" {
				return nil
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
				err = showKeys()
			case docket.State:
				row := sidebar.Current()
				if row.Desk {
					note = "the desk has no item to show"
				} else {
					item, _, _ := strings.Cut(row.Name, " ")
					err = showState(item)
				}
			case docket.Collapse:
				err = view.Collapse()
			case docket.Expand:
				err = view.Expand()
			}
			if err != nil {
				note = err.Error()
			}
			if action != docket.Show && action != docket.ShowAndFocus {
				action = docket.None
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
				shown, err := view.Show(sidebar.Current(), action == docket.ShowAndFocus)
				note = shown
				if err != nil {
					note = err.Error()
				}
				// A collapsed list cannot show where the cursor is, so
				// the row's name is shown for a moment.
				if sidebar.Collapsed() {
					row := sidebar.Current()
					name := row.Name
					if row.Area {
						name = "area: " + row.Title
					}
					_, _ = realTmux("display-message", "-d", "1500", name)
				}
			}
		case <-resized:
		case <-tick.C:
			info, err := os.Stat(stamp)
			if err != nil || !info.ModTime().After(seen) {
				continue
			}
			seen = info.ModTime()
			reload()
		}
		draw()
	}
}
