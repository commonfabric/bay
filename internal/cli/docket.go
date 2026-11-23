package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	cmd.AddCommand(newDocketViewCmd(), newDocketSidebarCmd())
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

Keys in this session:
  Option-l, Option-h   show the next or previous item
  Option-d             move between the list and the right side
  Option-D             go back to the tmux session you came from
Every other key, including the tmux prefix, goes to the window on the right.`,
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
				fmt.Printf("attach with: tmux attach -t %s\n", target)
				return nil
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

// readKeys sends one name per key press: a printable character as itself,
// and "enter", "esc", "backspace", "up" or "down" for those keys.
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
	fmt.Print("\x1b[?1049h\x1b[?25l")
	defer fmt.Print("\x1b[?25h\x1b[?1049l")

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

	keys := make(chan string)
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

	draw()
	for {
		select {
		case key, ok := <-keys:
			if !ok || key == "ctrl-c" {
				return nil
			}
			note = ""
			action := sidebar.Key(key)
			if action == docket.Quit {
				return nil
			}
			if action == docket.Show || action == docket.ShowAndFocus {
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
