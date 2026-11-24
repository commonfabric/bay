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
	levels   int             // 0 front; 1 front and back; 2 every level
	saved    map[string]bool // the folds to restore while every area is unfolded; else nil
	filter   string
	typing   bool // the filter is being typed
	rows     []Row
	first    map[string]Row // for each area row, the first row beneath it that is not an area
	cursor   int
	showing  string // the name of the row the right side was last asked to show
	moved    bool   // the cursor's row left the list, and the cursor is on another row
	top      int    // first row drawn
	body     int    // rows the last Render had room for
	scrolled bool   // the wheel moved the rows since the last key
	width    int    // columns the last Render had
	full     int    // columns the last Render had when it was not collapsed
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

// shown reports whether an item is listed: it is open and at one of the
// levels shown. A filter keeps, of those, the items whose name, title or
// bays contain it, and every item of an area whose name or title contains
// it; inArea says the item is in such an area.
func (s *Sidebar) shown(item Item, inArea bool) bool {
	if item.Status != "open" || levelRank(item.Level) > s.levels {
		return false
	}
	return s.filter == "" || inArea || s.matches(item)
}

// matches reports whether the filter is in the item's name, title or bays.
func (s *Sidebar) matches(item Item) bool {
	text := strings.ToLower(item.Name + " " + item.Title + " " + strings.Join(item.BayRefs(), " "))
	return strings.Contains(text, strings.ToLower(s.filter))
}

// levelRank orders the levels: front 0, back 1, deep and any other value 2.
func levelRank(level string) int {
	switch level {
	case "front", "":
		return 0
	case "back":
		return 1
	}
	return 2
}

// levelNames says which levels each setting of Sidebar.levels shows.
var levelNames = []string{"front", "front + back", "front + back + deep"}

// CollapsedWidth is the width of the sidebar's pane while it is collapsed.
// A pane narrower than collapsedBelow is drawn as collapsed.
const (
	CollapsedWidth = 2
	collapsedBelow = 10
)

// rebuild recomputes the rows. An area is shown when an item beneath it is
// shown. A filter searches the levels shown and ignores folds.
func (s *Sidebar) rebuild() {
	current := ""
	if s.cursor < len(s.rows) {
		current = s.rows[s.cursor].Name
	}
	rows := []Row{{Name: DeskRow, Title: "ask about and reorganize the tree", Desk: true}}
	s.first = map[string]Row{}
	var walk func(parent string, depth int, inArea bool) (rows []Row, open int, blocking bool)
	walk = func(parent string, depth int, inArea bool) ([]Row, int, bool) {
		var out []Row
		open, blocking := 0, false
		for _, item := range Children(s.items, parent) {
			inArea := inArea || (item.Kind == "area" && s.filter != "" && s.matches(item))
			below, belowOpen, belowBlocking := walk(item.Name, depth+1, inArea)
			if item.Kind == "area" {
				if len(below) == 0 {
					continue
				}
				for _, r := range below {
					if !r.Area {
						s.first[item.Name] = r
						break
					}
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
			if s.shown(item, inArea) {
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
	tree, _, _ := walk("", 0, false)
	s.rows = append(rows, tree...)
	// The cursor stays on its row. When that row has left the list the
	// cursor goes to the row the right side shows. When that has left
	// too, the cursor goes to the first item a filter lists, or else to
	// the first row, and returns to the shown row when it is listed again.
	shown := s.rowNamed(s.showing)
	if k := s.rowNamed(current); k >= 0 && !(s.moved && shown >= 0) {
		s.cursor = k
		return
	}
	if shown >= 0 {
		s.cursor, s.moved = shown, false
		return
	}
	s.moved = true
	s.cursor = 0
	if s.filter != "" {
		s.moveToItem(1)
	}
}

// rowNamed returns the index of the row with the given name, or -1.
func (s *Sidebar) rowNamed(name string) int {
	for k, r := range s.rows {
		if r.Name == name {
			return k
		}
	}
	return -1
}

// did records the row the right side is asked to show.
func (s *Sidebar) did(action Action) Action {
	if action == Show || action == ShowAndFocus {
		s.showing, s.moved = s.Shown().Name, false
	}
	return action
}

// Collapsed reports whether the last Render drew the sidebar collapsed.
func (s *Sidebar) Collapsed() bool { return s.width > 0 && s.width < collapsedBelow }

// FullWidth is the width to give the sidebar when it is expanded: the
// width it last had while not collapsed, or SidebarWidth.
func (s *Sidebar) FullWidth() int {
	if s.full >= collapsedBelow {
		return s.full
	}
	return SidebarWidth
}

// Rows returns the rows in display order.
func (s *Sidebar) Rows() []Row { return s.rows }

// Current returns the row under the cursor.
func (s *Sidebar) Current() Row { return s.rows[s.cursor] }

// Shown returns the row whose window the right side shows for the row
// under the cursor. For an area that is the first row beneath it that is
// not an area, whether or not the area is folded. For any other row it
// is the row under the cursor.
func (s *Sidebar) Shown() Row {
	row := s.rows[s.cursor]
	if first, ok := s.first[row.Name]; ok && row.Area {
		return first
	}
	return row
}

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
	// State asks the caller to show the current row's item.
	State
	// Collapse asks the caller to shrink the sidebar to CollapsedWidth.
	Collapse
	// Expand asks the caller to give the sidebar the width FullWidth.
	Expand
	// ResetWidth asks the caller to give the sidebar the width
	// SidebarWidth.
	ResetWidth
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

// moveToItem moves the cursor to the next row in the direction of delta
// that is neither an area nor an item followed by a row for each of its
// bays, and reports whether there was one. Such an item's row shows the
// same window as the row of its first bay.
func (s *Sidebar) moveToItem(delta int) bool {
	for k := s.cursor + delta; k >= 0 && k < len(s.rows); k += delta {
		r := s.rows[k]
		if !r.Area && (r.Bay != "" || len(r.Bays) < 2) {
			s.cursor = k
			return true
		}
	}
	return false
}

// JumpRows is how many rows J and K move the cursor.
const JumpRows = 10

// Key applies one key press and reports what the caller must do.
//
// Moving the cursor shows the row it lands on. KeysHelp lists the keys.
func (s *Sidebar) Key(key string) Action { return s.did(s.key(key)) }

func (s *Sidebar) key(key string) Action {
	// The wheel scrolls the rows and leaves the cursor where it is, so
	// the cursor's row can go out of sight. Any other key brings the
	// cursor's row back into sight.
	switch key {
	case "wheelup":
		s.top, s.scrolled = s.top-1, true
		return None
	case "wheeldown":
		s.top, s.scrolled = s.top+1, true
		return None
	}
	s.scrolled = false
	// The view's tmux keys send function keys, which act the same whether
	// or not a filter is being typed.
	sent := map[string]string{"f5": "j", "f6": "k", "f7": " ", "f8": "s", "f9": "n", "f10": "p", "f11": "b"}[key]
	if sent != "" {
		key = sent
	}
	if s.typing && sent == "" {
		return s.typeFilter(key)
	}
	switch key {
	case "j", "down":
		if s.move(1) {
			return Show
		}
	case "k", "up":
		if s.move(-1) {
			return Show
		}
	case "n", "p":
		if s.moveToItem(map[string]int{"n": 1, "p": -1}[key]) {
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
		s.levels = (s.levels + 1) % len(levelNames)
		s.rebuild()
	case "u":
		s.toggleUnfoldAll()
	case "s":
		return State
	case "b":
		if s.Collapsed() {
			return Expand
		}
		return Collapse
	case "=":
		return ResetWidth
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

// typeFilter applies one key while the filter is being typed.
func (s *Sidebar) typeFilter(key string) Action {
	switch key {
	case "enter":
		// Keeping the filter shows the row the cursor is on.
		s.typing = false
		s.rebuild()
		return Show
	case "esc":
		// Leaving the filter returns the cursor to the row the right
		// side shows, when that row is in the list.
		s.typing, s.filter = false, ""
		s.rebuild()
		if k := s.rowNamed(s.showing); k >= 0 {
			s.cursor = k
		}
		return None
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

// toggleUnfoldAll unfolds every area, or puts the folds back as they were
// before it did. Folds made in between are dropped. The areas above the
// cursor's row stay unfolded, so that the row stays in the list.
func (s *Sidebar) toggleUnfoldAll() {
	if s.saved == nil {
		s.saved, s.folded = s.folded, map[string]bool{}
		s.rebuild()
		return
	}
	s.folded, s.saved = s.saved, nil
	name, _, _ := strings.Cut(s.Current().Name, " ")
	for item, ok := s.items[name]; ok && item.Parent != nil; item, ok = s.items[*item.Parent] {
		delete(s.folded, *item.Parent)
	}
	s.rebuild()
}

// Click applies a mouse press at the given line and column of the last
// Render, both counted from 0. On a row it moves the cursor there, and on
// an area it also folds or unfolds it. On the mark at the start of the
// first bar it collapses the sidebar, and anywhere on a collapsed sidebar
// it expands it. Elsewhere it does nothing.
func (s *Sidebar) Click(line, column int) Action { return s.did(s.click(line, column)) }

func (s *Sidebar) click(line, column int) Action {
	if s.width < collapsedBelow {
		return Expand
	}
	if line == s.body {
		if column < 2 {
			return Collapse
		}
		return None
	}
	k := s.top + line
	if line < 0 || line >= s.body || k >= len(s.rows) {
		return None
	}
	s.cursor = k
	if s.rows[k].Area {
		s.toggleFold()
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

// Span is a run of text drawn in one style. The styles are "" for plain
// text, "area", "desk", "bay", "dim", the work states "running", "ready"
// and "held", "question" and "blocking", and in a bar "key".
type Span struct {
	Text  string
	Style string
}

// Line is one line of the sidebar. Bar marks the lines at the bottom that
// describe the view and are not rows.
type Line struct {
	Spans []Span
	Bar   bool
	Shown bool // the row the right side shows, when the cursor is on another row
}

// String returns the line's text without its styles.
func (l Line) String() string {
	var b strings.Builder
	for _, span := range l.Spans {
		b.WriteString(span.Text)
	}
	return b.String()
}

// Render draws the sidebar as plain text. See RenderLines.
func (s *Sidebar) Render(width, height int) ([]string, int) {
	lines, cursor := s.RenderLines(width, height)
	text := make([]string, len(lines))
	for k, line := range lines {
		text[k] = line.String()
	}
	return text, cursor
}

// RenderLines draws the sidebar as height lines of at most width columns.
// The rows come first. The last two lines are bars: one says which levels
// are shown, or shows the filter, and the other reminds of keys. The
// second result is the index of the cursor's line, which is outside the
// rows when the wheel has scrolled the cursor's row out of sight. A
// sidebar narrower than collapsedBelow is drawn as the two bars with a mark
// on the first of them, and no rows.
func (s *Sidebar) RenderLines(width, height int) ([]Line, int) {
	s.width = width
	if width < collapsedBelow {
		// The mark that expands the sidebar is where the mark that
		// collapses it was: at the start of the first bar.
		lines := make([]Line, max(height, 2))
		lines[len(lines)-2] = Line{Spans: clipSpans([]Span{{"»", "key"}}, width), Bar: true}
		lines[len(lines)-1] = Line{Bar: true}
		return lines, -1
	}
	s.full = width
	body := height - 2
	if body < 1 {
		body = 1
	}
	s.body = body
	if !s.scrolled {
		if s.cursor < s.top {
			s.top = s.cursor
		}
		if s.cursor >= s.top+body {
			s.top = s.cursor - body + 1
		}
	}
	s.top = max(0, min(s.top, len(s.rows)-body))
	var lines []Line
	shown := s.shownRow()
	for k := s.top; k < len(s.rows) && k < s.top+body; k++ {
		lines = append(lines, Line{Spans: clipSpans(rowSpans(s.rows[k]), width), Shown: k == shown && k != s.cursor})
	}
	for len(lines) < body {
		lines = append(lines, Line{})
	}
	// In the bars, the names of keys and the mark that can be clicked
	// have the style "key". The key that changes the levels is named
	// beside them.
	info := []Span{{"«", "key"}, {" " + levelNames[s.levels] + " (", ""}, {"a", "key"}, {")", ""}}
	hint := keyHints("?", "keys", "/", "find", "u", "unfold", "s", "state")
	switch {
	case s.typing:
		info = []Span{{"«", "key"}, {" " + levelNames[s.levels] + " · find: " + s.filter + "_", ""}}
		hint = keyHints("enter", "keeps", "esc", "clears")
	case s.filter != "":
		info = append(info, Span{" · find: " + s.filter, ""})
		hint = keyHints("esc", "clears", "/", "find again", "?", "keys")
	case s.saved != nil:
		info = append(info, Span{", all unfolded", ""})
		hint = keyHints("u", "puts the folds back", "?", "keys")
	}
	for _, spans := range [][]Span{info, hint} {
		lines = append(lines, Line{Spans: clipSpans(spans, width), Bar: true})
	}
	return lines, s.cursor - s.top
}

// keyHints draws a reminder of keys from pairs of a key's name and what
// the key does.
func keyHints(pairs ...string) []Span {
	var spans []Span
	for k := 0; k+1 < len(pairs); k += 2 {
		if k > 0 {
			spans = append(spans, Span{"  ", ""})
		}
		spans = append(spans, Span{pairs[k], "key"}, Span{" " + pairs[k+1], ""})
	}
	return spans
}

// shownRow is the index of the row whose window the right side shows, or
// -1 when that row is not in the list, as under a folded area. For an
// item followed by a row for each of its bays it is the first bay's row.
func (s *Sidebar) shownRow() int {
	name := s.showing
	if name == "" {
		name = s.Shown().Name
	}
	for k, r := range s.rows {
		if r.Name != name {
			continue
		}
		if r.Bay == "" && len(r.Bays) > 1 && k+1 < len(s.rows) && s.rows[k+1].Bay != "" {
			return k + 1
		}
		return k
	}
	return -1
}

// rowSpans draws one row.
func rowSpans(r Row) []Span {
	indent := strings.Repeat("  ", r.Depth)
	var marks []Span
	if r.Open > 0 {
		marks = append(marks, Span{fmt.Sprintf(" ?%d", r.Open), "question"})
		if r.Blocking {
			marks = append(marks, Span{"!", "blocking"})
		}
	}
	switch {
	case r.Desk:
		return []Span{{"◆ " + r.Name, "desk"}}
	case r.Bay != "":
		return []Span{{indent + "↳ " + r.Bay, "bay"}}
	case r.Area:
		fold := "▾"
		if r.Folded {
			fold = "▸"
		}
		return append([]Span{{indent + fold + " " + r.Title, "area"}}, marks...)
	}
	symbol, style := WorkSymbol(r.Work, len(r.Bays) > 0)
	spans := append([]Span{{indent, ""}, {symbol, style}, {" " + r.Name, ""}}, marks...)
	if r.Level != "front" && r.Level != "" {
		spans = append(spans, Span{" (" + r.Level + ")", "dim"})
	}
	return spans
}

// WorkSymbol returns the mark drawn before an item and its style: one for
// each work state, and a dot for an item that has no bay.
func WorkSymbol(work string, hasBay bool) (symbol, style string) {
	if !hasBay {
		return "·", "dim"
	}
	switch work {
	case "running":
		return "●", "running"
	case "ready":
		return "○", "ready"
	case "held":
		return "◌", "held"
	}
	return " ", ""
}

// Legend explains the marks and styles of the rows, one line each.
func Legend() []Line {
	entry := func(mark Span, meaning string) Line {
		pad := strings.Repeat(" ", max(1, 16-len([]rune(mark.Text))))
		return Line{Spans: []Span{{"  ", ""}, mark, {pad + meaning, ""}}}
	}
	return []Line{
		entry(Span{"▾ area", "area"}, "an area; ▸ when it is folded"),
		entry(Span{"● item", "running"}, "its agent is working"),
		entry(Span{"○ item", "ready"}, "its agent is waiting for its next instruction"),
		entry(Span{"◌ item", "held"}, "its work is held until something else happens"),
		entry(Span{"· item", "dim"}, "it has no bay"),
		entry(Span{"↳ dock/bay", "bay"}, "one bay of an item that has several"),
		entry(Span{"◆ the desk", "desk"}, "the agent session for the whole tree"),
		entry(Span{"?2", "question"}, "two questions wait for your answer;"),
		entry(Span{"", ""}, "on an area, the total beneath it"),
		entry(Span{"!", "blocking"}, "one of those questions holds up all work"),
		entry(Span{"(back)", "dim"}, "the item's level, when it is not front"),
		entry(Span{"shaded row", "shown"}, "the row the right side shows, when the"),
		entry(Span{"", ""}, "cursor is on its area or its item"),
	}
}

// clipSpans shortens a line to width columns, ending it with "…".
func clipSpans(spans []Span, width int) []Span {
	total := 0
	for _, span := range spans {
		total += len([]rune(span.Text))
	}
	if width <= 0 || total <= width {
		return spans
	}
	var out []Span
	room := width - 1
	for _, span := range spans {
		r := []rune(span.Text)
		if len(r) >= room {
			out = append(out, Span{string(r[:room]) + "…", span.Style})
			return out
		}
		out = append(out, span)
		room -= len(r)
	}
	return out
}
