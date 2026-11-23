package docket

import (
	"fmt"
	"strings"
)

// DeskRow is the name of the row for the desk, the agent session attached
// to the whole tree. No item can have this name: item names hold no spaces.
const DeskRow = "the desk"

// Row is one line of the sidebar.
type Row struct {
	Name     string
	Title    string
	Depth    int
	Area     bool
	Folded   bool
	Desk     bool
	Work     string
	Level    string
	Open     int  // open questions; for an area, those of the shown items beneath it
	Blocking bool // an open question holds up all work
	Bays     []string
	Bay      string // set on a row that stands for one bay of an item with several
}

// Sidebar is the state of the list: which items are shown, and where the
// cursor is. It has no terminal or tmux dependency.
type Sidebar struct {
	items    map[string]Item
	folded   map[string]bool
	allLevel bool
	filter   string
	typing   bool // the filter is being typed
	rows     []Row
	cursor   int
	top      int // first row drawn
	body     int // rows the last Render had room for
}

// NewSidebar returns a sidebar over items with the cursor on the first row.
func NewSidebar(items map[string]Item) *Sidebar {
	s := &Sidebar{folded: map[string]bool{}}
	s.SetItems(items)
	return s
}

// SetItems replaces the items. The cursor stays on the row it was on when
// that row is still shown.
func (s *Sidebar) SetItems(items map[string]Item) {
	s.items = items
	s.rebuild()
}

func (s *Sidebar) shown(item Item) bool {
	if item.Status != "open" {
		return false
	}
	if s.filter != "" {
		text := strings.ToLower(item.Name + " " + item.Title + " " + strings.Join(item.BayRefs(), " "))
		return strings.Contains(text, strings.ToLower(s.filter))
	}
	return s.allLevel || item.Level == "front"
}

// rebuild recomputes the rows. An area is shown when an item beneath it is
// shown. A filter searches every level and ignores folds.
func (s *Sidebar) rebuild() {
	current := ""
	if s.cursor < len(s.rows) {
		current = s.rows[s.cursor].Name
	}
	rows := []Row{{Name: DeskRow, Title: "ask about and reorganize the tree", Desk: true}}
	var walk func(parent string, depth int) (rows []Row, open int, blocking bool)
	walk = func(parent string, depth int) ([]Row, int, bool) {
		var out []Row
		open, blocking := 0, false
		for _, item := range Children(s.items, parent) {
			below, belowOpen, belowBlocking := walk(item.Name, depth+1)
			if item.Kind == "area" {
				if len(below) == 0 {
					continue
				}
				folded := s.folded[item.Name] && s.filter == ""
				out = append(out, Row{Name: item.Name, Title: item.Title, Depth: depth, Area: true,
					Folded: folded, Open: belowOpen, Blocking: belowBlocking})
				if !folded {
					out = append(out, below...)
				}
				open, blocking = open+belowOpen, blocking || belowBlocking
				continue
			}
			if s.shown(item) {
				n, b := item.OpenQuestions()
				bays := item.BayRefs()
				out = append(out, Row{Name: item.Name, Title: item.Title, Depth: depth, Work: item.Work,
					Level: item.Level, Open: n, Blocking: b, Bays: bays})
				if len(bays) > 1 {
					for _, ref := range bays {
						out = append(out, Row{Name: item.Name + " " + ref, Title: item.Title,
							Depth: depth + 1, Bays: []string{ref}, Bay: ref})
					}
				}
				open, blocking = open+n, blocking || b
			}
			out = append(out, below...)
			open, blocking = open+belowOpen, blocking || belowBlocking
		}
		return out, open, blocking
	}
	tree, _, _ := walk("", 0)
	s.rows = append(rows, tree...)
	s.cursor = 0
	for k, r := range s.rows {
		if r.Name == current {
			s.cursor = k
		}
	}
}

// Rows returns the rows in display order.
func (s *Sidebar) Rows() []Row { return s.rows }

// Current returns the row under the cursor.
func (s *Sidebar) Current() Row { return s.rows[s.cursor] }

// Action is what the caller must do after a key.
type Action int

const (
	// None needs nothing beyond a redraw.
	None Action = iota
	// Show displays the current row's session beside the sidebar.
	Show
	// ShowAndFocus displays it and moves the keyboard focus to it.
	ShowAndFocus
	// Quit asks the caller to take the terminal out of the view.
	Quit
	// Help asks the caller to show KeysHelp.
	Help
)

// move steps the cursor by delta rows and reports whether it moved.
func (s *Sidebar) move(delta int) bool {
	k := s.cursor + delta
	if k < 0 || k >= len(s.rows) {
		return false
	}
	s.cursor = k
	return true
}

// JumpRows is how many rows J and K move the cursor.
const JumpRows = 10

// Key applies one key press and reports what the caller must do.
//
// Moving the cursor shows the row it lands on. KeysHelp lists the keys.
func (s *Sidebar) Key(key string) Action {
	if s.typing {
		switch key {
		case "enter":
			s.typing = false
		case "esc":
			s.typing, s.filter = false, ""
		case "backspace":
			if s.filter != "" {
				r := []rune(s.filter)
				s.filter = string(r[:len(r)-1])
			}
		default:
			if len([]rune(key)) == 1 {
				s.filter += key
			}
		}
		s.rebuild()
		return None
	}
	switch key {
	case "j", "down", "wheeldown":
		if s.move(1) {
			return Show
		}
	case "k", "up", "wheelup":
		if s.move(-1) {
			return Show
		}
	case "J", "K":
		was := s.cursor
		if key == "J" {
			s.cursor = min(s.cursor+JumpRows, len(s.rows)-1)
		} else {
			s.cursor = max(s.cursor-JumpRows, 0)
		}
		if s.cursor != was {
			return Show
		}
	case "g", "G":
		was := s.cursor
		s.cursor = 0
		if key == "G" {
			s.cursor = len(s.rows) - 1
		}
		if s.cursor != was {
			return Show
		}
	case "enter":
		if s.Current().Area {
			return None
		}
		return ShowAndFocus
	case " ":
		s.toggleFold()
	case "a":
		s.allLevel = !s.allLevel
		s.rebuild()
	case "/":
		s.typing, s.filter = true, ""
		s.rebuild()
	case "esc":
		if s.filter != "" {
			s.filter = ""
			s.rebuild()
		}
	case "?":
		return Help
	case "q":
		return Quit
	}
	return None
}

// Click applies a mouse press on the given line of the last Render, where
// the first line is 0. It moves the cursor to that line's row. On an area
// it also folds or unfolds it. A press below the rows does nothing.
func (s *Sidebar) Click(line int) Action {
	k := s.top + line
	if line < 0 || line >= s.body || k >= len(s.rows) {
		return None
	}
	s.cursor = k
	if s.rows[k].Area {
		s.toggleFold()
		return None
	}
	return Show
}

func (s *Sidebar) toggleFold() {
	row := s.Current()
	if !row.Area {
		return
	}
	s.folded[row.Name] = !s.folded[row.Name]
	s.rebuild()
}

// Render draws the sidebar as height lines of at most width columns. The
// last line is the filter or a key reminder. The second result is the
// index of the cursor's line.
func (s *Sidebar) Render(width, height int) ([]string, int) {
	body := height - 1
	if body < 1 {
		body = 1
	}
	s.body = body
	if s.cursor < s.top {
		s.top = s.cursor
	}
	if s.cursor >= s.top+body {
		s.top = s.cursor - body + 1
	}
	var lines []string
	for k := s.top; k < len(s.rows) && k < s.top+body; k++ {
		lines = append(lines, clip(rowText(s.rows[k]), width))
	}
	for len(lines) < body {
		lines = append(lines, "")
	}
	foot := "? keys  / find  a all  space fold"
	if s.typing {
		foot = "/" + s.filter + "_"
	} else if s.filter != "" {
		foot = "/" + s.filter + "  (esc clears)"
	} else if s.allLevel {
		foot = "all levels  a front only  / find"
	}
	return append(lines, clip(foot, width)), s.cursor - s.top
}

func rowText(r Row) string {
	indent := strings.Repeat("  ", r.Depth)
	marks := ""
	if r.Open > 0 {
		marks = fmt.Sprintf(" ?%d", r.Open)
		if r.Blocking {
			marks += "!"
		}
	}
	switch {
	case r.Desk:
		return "◆ " + r.Name
	case r.Bay != "":
		return indent + "↳ " + r.Bay
	case r.Area:
		fold := "▾"
		if r.Folded {
			fold = "▸"
		}
		return fmt.Sprintf("%s%s %s%s", indent, fold, r.Title, marks)
	}
	state := map[string]string{"running": "●", "ready": "○", "held": "◌"}[r.Work]
	if state == "" {
		state = " "
	}
	if len(r.Bays) == 0 {
		state = "·"
	}
	level := ""
	if r.Level != "front" && r.Level != "" {
		level = " (" + r.Level + ")"
	}
	return fmt.Sprintf("%s%s %s%s%s", indent, state, r.Name, marks, level)
}

func clip(text string, width int) string {
	r := []rune(text)
	if width > 0 && len(r) > width {
		return string(r[:width-1]) + "…"
	}
	return text
}
