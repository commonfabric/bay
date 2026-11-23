package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
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
	cmd.AddCommand(newDocketViewCmd(), newDocketSidebarCmd(), newDocketKeysCmd())
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

` + docket.KeysHelp,
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

func newDocketKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "keys",
		Short:  "Print the docket view's keys; in a terminal, wait for a key press",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fd := int(os.Stdin.Fd())
			if !term.IsTerminal(fd) {
				fmt.Println(docket.KeysHelp)
				return nil
			}
			fmt.Print(strings.ReplaceAll(docket.KeysHelp, "\n", "\r\n"))
			old, err := term.MakeRaw(fd)
			if err != nil {
				return err
			}
			defer term.Restore(fd, old)
			_, _ = os.Stdin.Read(make([]byte, 16))
			return nil
		},
	}
}

// showKeys opens a tmux popup that lists the view's keys and closes on a
// key press.
func showKeys() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	lines := strings.Split(docket.KeysHelp, "\n")
	width := 0
	for _, line := range lines {
		width = max(width, len(line))
	}
	_, err = realTmux("display-popup", "-E", "-T", " docket view keys ",
		"-w", fmt.Sprint(width+4), "-h", fmt.Sprint(len(lines)+2), shellQuote(self)+" docket keys")
	return err
}

// mousePress matches the report a terminal sends for a mouse button or
// wheel press: the button, the column and the line.
var mousePress = regexp.MustCompile(`\x1b\[<(\d+);\d+;(\d+)M`)

// readKeys sends one name per key press: a printable character as itself,
// and "enter", "esc", "backspace", "up" or "down" for those keys. A press
// of the left mouse button is "click:" and its line, counted from 0. The
// wheel is "wheelup" or "wheeldown".
func readKeys(keys chan<- string) {
	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			close(keys)
			return
		}
		chunk := string(buf[:n])
		switch chunk {
		case "\r", "\n":
			keys <- "enter"
		case "\x1b":
			keys <- "esc"
		case "\x7f", "\b":
			keys <- "backspace"
		case "\x1b[A", "\x1bOA":
			keys <- "up"
		case "\x1b[B", "\x1bOB":
			keys <- "down"
		case "\x03":
			keys <- "ctrl-c"
		default:
			for _, m := range mousePress.FindAllStringSubmatch(chunk, -1) {
				line, _ := strconv.Atoi(m[2])
				switch m[1] {
				case "0":
					keys <- fmt.Sprintf("click:%d", line-1)
				// Turning the wheel up moves the cursor down.
				case "64":
					keys <- "wheeldown"
				case "65":
					keys <- "wheelup"
				}
			}
			if !strings.HasPrefix(chunk, "\x1b") {
				for _, r := range chunk {
					keys <- string(r)
				}
			}
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
		lines, cursor := sidebar.Render(width, height)
		if note != "" {
			lines[len(lines)-1] = note
		}
		var b strings.Builder
		b.WriteString("\x1b[H")
		for k, line := range lines {
			if k == cursor {
				b.WriteString("\x1b[7m" + line + "\x1b[0m")
			} else {
				b.WriteString(line)
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
			if line, ok := strings.CutPrefix(key, "click:"); ok {
				n, _ := strconv.Atoi(line)
				action = sidebar.Click(n)
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
			if action == docket.Help {
				if err := showKeys(); err != nil {
					note = err.Error()
				}
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
