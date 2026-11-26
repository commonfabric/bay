package docket

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"unicode"
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
	Open     int  // open questions; for an area, its own and those of the shown items beneath it
	Blocking bool // an open question holds up all work
	Bays     []string
	Bay      string // set on a row that stands for one bay of an item with several
	Gone     bool   // none of the row's bays exists
	Waiting  bool   // a bay of the row waits for the person; on an area, also a bay beneath it
	Hot      int    // the row's place in the hotlist, counted from 1; 0 when it is not in it
	HotRow   bool   // the row is the one in the hotlist, not the one in its area
}

// Sidebar is the state of the list: which items are shown, and where the
// cursor is. It has no terminal or tmux dependency.
type Sidebar struct {
	items     map[string]Item
	rootOrder []string // the order of the items at the top of the tree
	hotlist   []string // the names of the rows in the hotlist, in order
	sibling   SiblingMove
	folded    map[string]bool
	levels    int             // 0 front; 1 front and back; 2 every level
	allBays   bool            // every item and area with a bay gets a row for each bay
	saved     map[string]bool // the folds to restore while every area is unfolded; else nil
	filter    string
	typing    bool // the filter is being typed
	rows      []Row
	marks     map[string]string // for each mark, a letter or digit, the name of its row
	pending   string            // "m", "M" (from the right side) or "'" when the next key names a mark; "x" when it says how an item closes; else ""
	closing   string            // the item x asks to close; else ""
	bayClose  BayClose          // the bay x or X asks to close; its Ref is "" when none
	dropping  bool              // the reason for dropping closing is being typed
	reason    string            // the reason typed so far
	outcome   string            // "finished" or "dropped", once chosen
	command   bool              // F12 came last: the next key is a command even while a filter is typed
	back      string            // the key (rowKey) of the row the cursor left on the last jump
	notice    string            // what the last key has to tell the user
	missing   map[string]bool   // the bays, as dock/bay, that do not exist
	waiting   map[string]bool   // the bays, as dock/bay, whose agent waits for the person
	first     map[string]Row    // for each area row, the first row beneath it that is not an area
	cursor    int
	showing   string // the name of the row the right side was last asked to show
	moved     bool   // the cursor's row left the list, and the cursor is on another row
	top       int    // first row drawn
	body      int    // rows the last Render had room for
	scrolled  bool   // the wheel moved the rows since the last key
	width     int    // columns the last Render had
	full      int    // columns the last Render had when it was not collapsed
}

// NewSidebar returns a sidebar over items with the cursor on the first row.
func NewSidebar(items map[string]Item) *Sidebar {
	s := &Sidebar{folded: map[string]bool{}, marks: map[string]string{}}
	s.SetItems(items)
	return s
}

// SetMarks replaces the marks: for each mark, the name of its row.
func (s *Sidebar) SetMarks(marks map[string]string) {
	s.marks = map[string]string{}
	for mark, name := range marks {
		s.marks[mark] = name
	}
}

// Marks returns the marks: for each mark, the name of its row.
func (s *Sidebar) Marks() map[string]string { return s.marks }

// ForgetMarks drops the marks whose item is closed or no longer exists,
// and reports whether it dropped any. It drops none when there are no
// items at all.
func (s *Sidebar) ForgetMarks() bool {
	dropped := false
	if len(s.items) == 0 {
		return false
	}
	for mark, name := range s.marks {
		if !markLives(name, s.items) {
			delete(s.marks, mark)
			dropped = true
		}
	}
	return dropped
}

// markLives reports whether a mark's row can still be reached: it is the
// desk, or its item is open.
func markLives(name string, items map[string]Item) bool {
	if name == DeskRow {
		return true
	}
	itemName, _, _ := strings.Cut(name, " ")
	item, ok := items[itemName]
	return ok && item.Status == "open"
}

// MarkLines lists the marks, one line each: the mark as it is typed to go
// to it, the name of its row, and its item's title.
func MarkLines(marks map[string]string, items map[string]Item) []Line {
	var keys []string
	for mark := range marks {
		keys = append(keys, mark)
	}
	sort.Strings(keys)
	var lines []Line
	for _, mark := range keys {
		name := marks[mark]
		itemName, _, _ := strings.Cut(name, " ")
		title := ""
		switch item, ok := items[itemName]; {
		case name == DeskRow:
		case !ok || item.Status != "open":
			title = "closed or gone"
		default:
			title = item.Title
		}
		spans := []Span{{"  ", ""}, {"'" + mark, "key"}, {"  " + name, ""}}
		if title != "" {
			spans = append(spans, Span{"  " + title, "dim"})
		}
		lines = append(lines, Line{Spans: spans})
	}
	if len(lines) == 0 {
		lines = []Line{{Spans: []Span{{"  none; m and a letter marks the row under the cursor", "dim"}}}}
	}
	return lines
}

// Notice returns what the last key has to tell the user, or "".
func (s *Sidebar) Notice() string { return s.notice }

// markKey reports whether a key can name a mark: one letter.
func markKey(key string) bool {
	r := []rune(key)
	return len(r) == 1 && unicode.IsLetter(r[0])
}

// digitKey returns the number a key of one digit from 1 to 9 stands for,
// and 0 for any other key.
func digitKey(key string) int {
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		return int(key[0] - '0')
	}
	return 0
}

// mark applies the key that follows m or '.
func (s *Sidebar) mark(pending, key string) Action {
	switch {
	case pending == "'" && key == "'":
		if s.back == "" {
			s.notice = "no earlier row to return to"
			return None
		}
		if k := s.rowKeyed(s.back); k >= 0 && s.rows[k].HotRow {
			s.cursor, s.back = k, rowKey(s.Current())
			return Show
		}
		return s.jump("''", strings.TrimPrefix(s.back, hotPrefix))
	case (pending == "m" || pending == "M") && (digitKey(key) > 0 || key == "0"):
		s.notice = "a digit is a place in the hotlist, not a mark; * adds or removes this row"
		return None
	case pending == "'" && digitKey(key) > 0:
		from := rowKey(s.Current())
		action := s.jumpHot(digitKey(key))
		if action == Show {
			s.back = from
		}
		return action
	case !markKey(key):
		return None
	case pending == "m" || pending == "M":
		name := s.Current().Name
		if pending == "M" && s.showing != "" {
			name = s.showing
		}
		// Setting a mark on the row that has it takes the mark off.
		if s.marks[key] == name {
			delete(s.marks, key)
			s.notice = "'" + key + " removed"
		} else {
			s.marks[key] = name
			s.notice = "'" + key + " is " + name
		}
		return MarksChanged
	}
	name, ok := s.marks[key]
	if !ok {
		s.notice = "no mark '" + key
		return None
	}
	return s.jump("'"+key, name)
}

// jump moves the cursor to the row with the given name. When the row is
// not in the list it changes as little as brings the row in: it unfolds
// the areas above the row, then shows the row's level, then clears the
// filter, then shows every level. A bay's row that no longer exists
// stands for its item.
func (s *Sidebar) jump(label, name string) Action {
	from := rowKey(s.Current())
	if name == DeskRow {
		s.cursor, s.back = 0, from
		return Show
	}
	itemName, _, _ := strings.Cut(name, " ")
	item, ok := s.items[itemName]
	if !ok || item.Status != "open" {
		s.notice = label + ": " + itemName + " is closed or gone"
		return None
	}
	find := func() int {
		if k := s.rowNamed(name); k >= 0 {
			return k
		}
		return s.rowNamed(itemName)
	}
	steps := []func(){
		func() {},
		func() {
			for _, name := range s.ancestors(itemName) {
				delete(s.folded, name)
			}
			delete(s.folded, itemName)
		},
		func() {
			if rank := levelRank(item.Level); item.Kind != "area" && rank > s.levels {
				s.levels = rank
				s.notice = "now showing " + levelNames[s.levels]
			}
		},
		func() {
			if s.filter != "" {
				s.filter, s.typing = "", false
				s.notice = "filter cleared"
			}
		},
		func() {
			if s.levels != len(levelNames)-1 {
				s.levels = len(levelNames) - 1
				s.notice = "now showing " + levelNames[s.levels]
			}
		},
	}
	for _, step := range steps {
		step()
		s.rebuild()
		if k := find(); k >= 0 {
			s.cursor, s.back = k, from
			return Show
		}
	}
	s.notice = label + ": " + itemName + " has nothing to show"
	return None
}

// SetMissing replaces the set of bays, as dock/bay, that do not exist.
func (s *Sidebar) SetMissing(missing map[string]bool) {
	s.missing = missing
	s.rebuild()
}

// SetWaiting tells the list which bays, as dock/bay, have an agent waiting
// for the person, and reports whether that changed anything.
func (s *Sidebar) SetWaiting(waiting map[string]bool) bool {
	if maps.Equal(s.waiting, waiting) {
		return false
	}
	s.waiting = waiting
	s.rebuild()
	return true
}

// SetItems replaces the items. The cursor stays on the row it was on when
// that row is still shown.
func (s *Sidebar) SetItems(items map[string]Item) {
	s.items = items
	s.rebuild()
}

// SetRootOrder sets the order of the items at the top of the tree.
func (s *Sidebar) SetRootOrder(order []string) {
	s.rootOrder = order
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

// levelRank orders the levels: front 0, back 1, shelved and any other value 2.
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
var levelNames = []string{"front", "front + back", "front + back + shelved"}

// CollapsedWidth is the width of the sidebar's pane while it is collapsed.
// A pane narrower than CollapsedBelow is drawn as collapsed.
const (
	CollapsedWidth = 2
	CollapsedBelow = 10
)

// rebuild recomputes the rows. An area is shown when an item beneath it is
// shown. A filter searches the levels shown and ignores folds.
func (s *Sidebar) rebuild() {
	current := ""
	if s.cursor < len(s.rows) {
		current = rowKey(s.rows[s.cursor])
	}
	rows := []Row{{Name: DeskRow, Title: "ask about and reorganize the tree", Desk: true}}
	s.first = map[string]Row{}
	hot := s.hotRows()
	tree := s.tree()
	place := map[string]int{}
	for k, r := range hot {
		place[r.Name] = k + 1
	}
	for k := range tree {
		tree[k].Hot = place[tree[k].Name]
	}
	s.rows = append(append(rows, hot...), tree...)
	// The cursor stays on its row. When that row has left the list the
	// cursor goes to the row the right side shows. When that has left
	// too, the cursor goes to the first item a filter lists, or else to
	// the first row, and returns to the shown row when it is listed again.
	shown := s.rowNamed(s.showing)
	if k := s.rowKeyed(current); k >= 0 && !(s.moved && shown >= 0) {
		s.cursor = k
		return
	}
	if shown >= 0 {
		s.cursor, s.moved = shown, false
		return
	}
	// With no filter the cursor stays at its place in the list, on the
	// row that followed the one that left.
	s.moved = true
	at := s.cursor
	s.cursor = 0
	if s.filter != "" {
		s.moveToItem(1)
	} else if current != "" {
		s.cursor = max(0, min(at, len(s.rows)-1))
	}
}

// tree returns the rows of the items under their areas, as the filter,
// the levels and the folds show them.
func (s *Sidebar) tree() []Row {
	var walk func(parent string, depth int, inArea bool) (rows []Row, open int, blocking bool)
	walk = func(parent string, depth int, inArea bool) ([]Row, int, bool) {
		var out []Row
		open, blocking := 0, false
		for _, item := range Children(s.items, s.rootOrder, parent) {
			inArea := inArea || (item.Kind == "area" && s.filter != "" && s.matches(item))
			below, belowOpen, belowBlocking := walk(item.Name, depth+1, inArea)
			if item.Kind == "area" {
				n, b := item.OpenQuestions()
				bays := item.BayRefs()
				waiting := false
				for _, ref := range bays {
					waiting = waiting || s.waiting[ref]
				}
				if len(below) == 0 {
					// An area with no open items under it is listed, so
					// that it can be closed.
					if s.hasOpenChild(item.Name) || !s.shown(item, inArea) {
						continue
					}
					out = append(out, Row{Name: item.Name, Title: item.Title, Depth: depth, Area: true,
						Open: n, Blocking: b, Bays: bays, Waiting: waiting})
					if s.allBays {
						out = append(out, s.bayRows(item, depth)...)
					}
					open, blocking = open+n, blocking || b
					continue
				}
				for _, r := range below {
					if !r.Area {
						s.first[item.Name] = r
						break
					}
				}
				folded := s.folded[item.Name] && s.filter == ""
				for _, r := range below {
					waiting = waiting || r.Waiting
				}
				out = append(out, Row{Name: item.Name, Title: item.Title, Depth: depth, Area: true,
					Folded: folded, Open: belowOpen + n, Blocking: belowBlocking || b, Bays: bays, Waiting: waiting})
				if s.allBays {
					out = append(out, s.bayRows(item, depth)...)
				}
				if !folded {
					out = append(out, below...)
				}
				open, blocking = open+belowOpen+n, blocking || belowBlocking || b
				continue
			}
			if s.shown(item, inArea) {
				n, b := item.OpenQuestions()
				bays := item.BayRefs()
				gone, waiting := len(bays) > 0, false
				for _, ref := range bays {
					gone = gone && s.missing[ref]
					waiting = waiting || s.waiting[ref]
				}
				out = append(out, Row{Name: item.Name, Title: item.Title, Depth: depth, Work: item.Work,
					Level: item.Level, Open: n, Blocking: b, Bays: bays, Gone: gone, Waiting: waiting})
				if len(bays) > 1 || s.allBays {
					out = append(out, s.bayRows(item, depth)...)
				}
				open, blocking = open+n, blocking || b
			}
			out = append(out, below...)
			open, blocking = open+belowOpen, blocking || belowBlocking
		}
		return out, open, blocking
	}
	tree, _, _ := walk("", 0, false)
	return tree
}

// levelOrder lists the levels from the front.
var levelOrder = []string{"front", "back", "shelved"}

// LevelChange returns the item under the cursor and the level that Deeper
// or Nearer moves it to. ok is false on the desk, on an area, and when
// the item is already at the last level in that direction.
func (s *Sidebar) LevelChange(action Action) (item, level string, ok bool) {
	row := s.Current()
	if row.Desk || row.Area {
		return "", "", false
	}
	item, _, _ = strings.Cut(row.Name, " ")
	rank := levelRank(s.items[item].Level)
	if action == Deeper {
		rank++
	} else {
		rank--
	}
	if rank < 0 || rank >= len(levelOrder) {
		return item, "", false
	}
	return item, levelOrder[rank], true
}

// rowNamed returns the index of the row with the given name, or -1.
// A row of the hotlist is not found by this: it has the name of the row
// in its area.
func (s *Sidebar) rowNamed(name string) int {
	for k, r := range s.rows {
		if r.Name == name && !r.HotRow {
			return k
		}
	}
	return -1
}

// hotPrefix starts the key of a row in the hotlist.
const hotPrefix = "\x00"

// rowKey names a row so that the row in the hotlist and the one in its
// area differ.
func rowKey(r Row) string {
	if r.HotRow {
		return hotPrefix + r.Name
	}
	return r.Name
}

// rowKeyed returns the index of the row with the given key, or -1.
func (s *Sidebar) rowKeyed(key string) int {
	for k, r := range s.rows {
		if rowKey(r) == key {
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
func (s *Sidebar) Collapsed() bool { return s.width > 0 && s.width < CollapsedBelow }

// FullWidth is the width to give the sidebar when it is expanded: the
// width it last had while not collapsed, or SidebarWidth.
func (s *Sidebar) FullWidth() int {
	if s.full >= CollapsedBelow {
		return s.full
	}
	return SidebarWidth
}

// Rows returns the rows in display order.
func (s *Sidebar) Rows() []Row { return s.rows }

// Current returns the row under the cursor.
func (s *Sidebar) Current() Row { return s.rows[s.cursor] }

// Shown returns the row whose window the right side shows for the row
// under the cursor. For an area with a bay of its own that is the area
// as a row for that bay. For another area it is the first row beneath it
// that is not an area, whether or not the area is folded. For any other
// row it is the row under the cursor.
func (s *Sidebar) Shown() Row {
	row := s.rows[s.cursor]
	if row.Area && len(row.Bays) > 0 {
		return Row{Name: row.Name, Title: row.Title, Bays: row.Bays}
	}
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
	// MarksChanged asks the caller to save the marks.
	MarksChanged
	// HotlistChanged asks the caller to save the hotlist.
	HotlistChanged
	// MoveSibling asks the caller to move an item among its siblings.
	// SiblingRequest names it.
	MoveSibling
	// Deeper and Nearer ask the caller to move the item under the cursor
	// one level away from the front or toward it. LevelChange names the
	// item and its new level.
	Deeper
	Nearer
	// ResetWidth asks the caller to give the sidebar the width
	// SidebarWidth.
	ResetWidth
	// CloseItem asks the caller to close an item. CloseRequest names it.
	CloseItem
	// CloseBay asks the caller to close a bay, or to take a bay that no
	// longer exists off its item. BayCloseRequest says which.
	CloseBay
)

// SiblingMove is a move of an item to just before or just after a sibling.
type SiblingMove struct {
	Item, Sibling string
	After         bool
}

// BayClose is a bay that x or X asks to close.
type BayClose struct {
	Item  string // the item that lists the bay
	Ref   string // the bay, as dock/bay
	Gone  bool   // the bay no longer exists: take it off the item
	Force bool   // close it even with uncommitted changes
}

// move steps the cursor by delta rows and reports whether it moved.
func (s *Sidebar) move(delta int) bool {
	k := s.cursor + delta
	if k < 0 || k >= len(s.rows) {
		return false
	}
	s.cursor = k
	return true
}

// bayRows returns a row for each of item's bays, one level deeper.
func (s *Sidebar) bayRows(item Item, depth int) []Row {
	var rows []Row
	for _, ref := range item.BayRefs() {
		rows = append(rows, Row{Name: item.Name + " " + ref, Title: item.Title,
			Depth: depth + 1, Bays: []string{ref}, Bay: ref, Gone: s.missing[ref], Waiting: s.waiting[ref]})
	}
	return rows
}

// listsBays reports whether the item row at k is followed by a row for
// each of its bays.
func (s *Sidebar) listsBays(k int) bool {
	r := s.rows[k]
	return !r.Area && r.Bay == "" && len(r.Bays) > 0 && k+1 < len(s.rows) && s.rows[k+1].Bay != ""
}

// moveToItem moves the cursor to the next row in the direction of delta
// that is neither an area nor an item followed by a row for each of its
// bays, and reports whether there was one. Such an item's row shows the
// same window as the row of its first bay.
func (s *Sidebar) moveToItem(delta int) bool {
	for k := s.cursor + delta; k >= 0 && k < len(s.rows); k += delta {
		if !s.rows[k].Area && !s.listsBays(k) {
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
	// or not a filter is being typed. F12 makes the key after it act so:
	// the bookmark keys send F12, then m or ', then the mark.
	if key == "f12" {
		s.command = true
		return None
	}
	sent := map[string]string{"f5": "j", "f6": "k", "f7": " ", "f8": "s", "f9": "n", "f10": "p", "f11": "b"}[key]
	if sent != "" {
		key = sent
	} else if s.command {
		sent = key
		if key == "m" {
			// Option-M on the right side marks the row shown there.
			s.command, s.pending = false, "M"
			return None
		}
	}
	s.command = false
	s.notice = ""
	if pending := s.pending; pending != "" {
		s.pending = ""
		if sent == "" && pending == "x" {
			return s.chooseOutcome(key)
		}
		if sent == "" {
			return s.mark(pending, key)
		}
	}
	if s.dropping {
		if sent == "" {
			return s.typeReason(key)
		}
		// A key sent from the right side gives up the close.
		s.dropping, s.closing = false, ""
	}
	if s.pending == "" {
		s.closing, s.bayClose = "", BayClose{}
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
	case "B":
		s.allBays = !s.allBays
		s.rebuild()
	case "b":
		if s.Collapsed() {
			return Expand
		}
		return Collapse
	case "=":
		return ResetWidth
	case "m", "'":
		s.pending = key
	case "*":
		return s.toggleHot()
	case "o":
		return s.otherPlace()
	case "[", "]":
		return s.reorder(key == "]")
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		return s.jumpHot(digitKey(key))
	case "x":
		s.startClose(false)
	case "X":
		s.startClose(true)
	case ">":
		return Deeper
	case "<":
		return Nearer
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

// startClose asks how the item under the cursor closes, or says why it
// cannot. An item or area with open items under it is not closed, nor an
// item that lists more than one bay: the list shows those as rows under
// it.
func (s *Sidebar) startClose(force bool) {
	row := s.Current()
	if row.Desk {
		s.notice = "the desk cannot be closed"
		return
	}
	name, _, _ := strings.Cut(row.Name, " ")
	if row.Bay != "" {
		s.bayClose = BayClose{Item: name, Ref: row.Bay, Gone: row.Gone, Force: force && !row.Gone}
		s.pending = "x"
		return
	}
	if force {
		s.notice = "X closes a bay; on a bay's row"
		return
	}
	item, ok := s.items[name]
	if !ok {
		return
	}
	var open []string
	for _, kid := range Children(s.items, s.rootOrder, name) {
		if kid.Status == "open" {
			open = append(open, kid.Name)
		}
	}
	switch bays := item.BayRefs(); {
	case len(open) > 0:
		s.notice = name + " has open items under it: " + strings.Join(open, ", ")
	case len(bays) > 1:
		s.notice = fmt.Sprintf("%s lists %d bays; take all but one off it first", name, len(bays))
	default:
		s.closing, s.pending = name, "x"
	}
}

// hasOpenChild reports whether an open item is directly under name.
func (s *Sidebar) hasOpenChild(name string) bool {
	for _, kid := range Children(s.items, s.rootOrder, name) {
		if kid.Status == "open" {
			return true
		}
	}
	return false
}

// chooseOutcome applies the key after x: f closes the item as finished, d
// asks for the reason it is dropped, and any other key gives up.
func (s *Sidebar) chooseOutcome(key string) Action {
	if s.bayClose.Ref != "" {
		if key == "y" {
			return CloseBay
		}
		s.bayClose = BayClose{}
		return None
	}
	switch key {
	case "f":
		s.outcome = "finished"
		return CloseItem
	case "d":
		s.dropping, s.reason = true, ""
		return None
	}
	s.closing = ""
	return None
}

// typeReason applies one key while the reason for dropping an item is
// typed.
func (s *Sidebar) typeReason(key string) Action {
	switch key {
	case "enter":
		if strings.TrimSpace(s.reason) == "" {
			s.notice = "a dropped item needs a reason"
			return None
		}
		s.dropping, s.outcome = false, "dropped"
		return CloseItem
	case "esc":
		s.dropping, s.closing = false, ""
	case "backspace":
		if r := []rune(s.reason); len(r) > 0 {
			s.reason = string(r[:len(r)-1])
		}
	default:
		if len([]rune(key)) == 1 {
			s.reason += key
		}
	}
	return None
}

// CloseRequest returns the item that CloseItem asks to close, how it
// closes, and the reason when it is dropped.
func (s *Sidebar) CloseRequest() (item, outcome, reason string) {
	item, outcome, reason = s.closing, s.outcome, strings.TrimSpace(s.reason)
	s.closing, s.outcome, s.reason = "", "", ""
	return item, outcome, reason
}

// BayCloseRequest returns the bay that CloseBay asks to close.
func (s *Sidebar) BayCloseRequest() BayClose {
	b := s.bayClose
	s.bayClose = BayClose{}
	return b
}

// typeFilter applies one key while the filter is being typed.
func (s *Sidebar) typeFilter(key string) Action {
	switch key {
	case "enter":
		// Keeping the filter shows the row the cursor is on.
		s.typing = false
		s.rebuild()
		if s.filter != "" && len(s.rows) == 1 {
			// Only the desk row is left, so no item matches and nothing
			// is shown.
			s.notice = "nothing matches " + s.filter
			return None
		}
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
	for _, name := range s.ancestors(name) {
		delete(s.folded, name)
	}
	s.rebuild()
}

// ancestors returns the names of the items above name, nearest first. It
// stops at an item it has already passed, so a cycle of parents in
// damaged data ends the walk.
func (s *Sidebar) ancestors(name string) []string {
	var out []string
	seen := map[string]bool{name: true}
	for item, ok := s.items[name]; ok && item.Parent != nil && !seen[*item.Parent]; item, ok = s.items[*item.Parent] {
		seen[*item.Parent] = true
		out = append(out, *item.Parent)
	}
	return out
}

// Click applies a mouse press at the given line and column of the last
// Render, both counted from 0. On a row it moves the cursor there; on an
// area row the cursor was already on, it folds or unfolds it. On the mark at the start of the
// first bar it collapses the sidebar, and anywhere on a collapsed sidebar
// it expands it. Elsewhere it does nothing.
func (s *Sidebar) Click(line, column int) Action { return s.did(s.click(line, column)) }

func (s *Sidebar) click(line, column int) Action {
	if s.width < CollapsedBelow {
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
	// A click on an area folds or unfolds it only when the cursor was
	// already there; any other click only moves the cursor.
	wasThere := s.cursor == k
	s.cursor = k
	if wasThere && s.rows[k].Area {
		s.toggleFold()
	}
	return Show
}

func (s *Sidebar) toggleFold() {
	row := s.Current()
	if !row.Area || row.HotRow {
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
// sidebar narrower than CollapsedBelow is drawn as the two bars with a mark
// on the first of them, and no rows.
func (s *Sidebar) RenderLines(width, height int) ([]Line, int) {
	s.width = width
	if width < CollapsedBelow {
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
		// A row's marks are drawn at its end, and the rest of the row is
		// shortened to leave room for them.
		spans, marks := rowSpans(s.rows[k]), s.marksOf(s.rows[k].Name)
		if marks != "" {
			spans = append(clipSpans(spans, width-TextWidth(marks)-1), Span{" " + marks, "key"})
		}
		lines = append(lines, Line{Spans: clipSpans(spans, width), Shown: k == shown && k != s.cursor && s.rows[k].Name != s.rows[s.cursor].Name})
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
	case s.pending == "m":
		info = []Span{{"«", "key"}, {" mark this row: press a letter", ""}}
		hint = keyHints("esc", "cancels")
	case s.pending == "'":
		info = []Span{{"«", "key"}, {" go to a mark: press its letter", ""}}
		hint = keyHints("'", "goes back", "esc", "cancels")
	case s.pending == "x" && s.bayClose.Gone:
		info = []Span{{"«", "key"}, {" take " + s.bayClose.Ref + " off " + s.bayClose.Item + "?", ""}}
		hint = keyHints("y", "takes it off", "esc", "cancels")
	case s.pending == "x" && s.bayClose.Force:
		info = []Span{{"«", "key"}, {" close " + s.bayClose.Ref + ", losing uncommitted changes?", ""}}
		hint = keyHints("y", "closes it", "esc", "cancels")
	case s.pending == "x" && s.bayClose.Ref != "":
		info = []Span{{"«", "key"}, {" close bay " + s.bayClose.Ref + "?", ""}}
		hint = keyHints("y", "closes it", "esc", "cancels")
	case s.pending == "x":
		info = []Span{{"«", "key"}, {" close " + s.closing + "?", ""}}
		hint = keyHints("f", "finished", "d", "dropped", "esc", "cancels")
	case s.dropping:
		info = []Span{{"«", "key"}, {" why drop it: " + tailText(s.reason, width-TextWidth("« why drop it: _")) + "_", ""}}
		hint = keyHints("enter", "closes", "esc", "cancels")
	case s.typing:
		prefix := " " + levelNames[s.levels] + " · find: "
		info = []Span{{"«", "key"}, {prefix + tailText(s.filter, width-TextWidth("«"+prefix+"_")) + "_", ""}}
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
		if r.Name != name || r.HotRow {
			continue
		}
		if s.listsBays(k) {
			return k + 1
		}
		return k
	}
	return -1
}

// marksOf returns the marks on the row with the given name, each written
// as it is typed to go to it, such as "'a'q".
func (s *Sidebar) marksOf(name string) string {
	var marks []string
	for mark, row := range s.marks {
		if row == name {
			marks = append(marks, "'"+mark)
		}
	}
	sort.Strings(marks)
	return strings.Join(marks, "")
}

// rowSpans draws one row. A row of the hotlist starts with its number; a
// row in an area that is in the hotlist ends with it.
func rowSpans(r Row) []Span {
	spans := baseSpans(r)
	switch {
	case r.HotRow:
		return append([]Span{{fmt.Sprint(r.Hot) + " ", "key"}}, spans...)
	case r.Hot > 0:
		return append(spans, Span{fmt.Sprintf(" *%d", r.Hot), "key"})
	}
	return spans
}

func baseSpans(r Row) []Span {
	indent := strings.Repeat("  ", r.Depth)
	var marks []Span
	if r.Area && r.Waiting {
		marks = append(marks, Span{" " + WaitingMark, "waiting"})
	}
	if r.Open > 0 {
		marks = append(marks, Span{fmt.Sprintf(" ?%d", r.Open), "question"})
		if r.Blocking {
			marks = append(marks, Span{"!", "blocking"})
		}
	}
	switch {
	case r.Desk:
		return []Span{{"◆ " + r.Name, "desk"}}
	case r.Bay != "" && r.Gone:
		return []Span{{indent + "↳ " + r.Bay + " (gone)", "dim"}}
	case r.Bay != "" && r.Waiting:
		return []Span{{indent, ""}, {WaitingMark, "waiting"}, {" " + r.Bay, "bay"}}
	case r.Bay != "":
		return []Span{{indent + "↳ " + r.Bay, "bay"}}
	case r.Area:
		fold := "▾"
		if r.Folded {
			fold = "▸"
		}
		return append([]Span{{indent + fold + " " + r.Title, "area"}}, marks...)
	}
	symbol, style := WorkSymbol(r.Work, len(r.Bays) > 0 && !r.Gone)
	if r.Waiting {
		symbol, style = WaitingMark, "waiting"
	}
	spans := append([]Span{{indent, ""}, {symbol, style}, {" " + r.Name, ""}}, marks...)
	if r.Gone {
		spans = append(spans, Span{" (bay gone)", "dim"})
	}
	if r.Level != "front" && r.Level != "" {
		spans = append(spans, Span{" (" + r.Level + ")", "dim"})
	}
	return spans
}

// WaitingMark is drawn in place of an item's work mark, or a bay's ↳,
// while an agent in one of its bays waits for the person, and after an
// area's title while a bay beneath the area waits.
const WaitingMark = "▶"

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
		entry(Span{WaitingMark + " item", "waiting"}, "an agent in its bay waits for you;"),
		entry(Span{"", ""}, "after an area, in a bay beneath it"),
		entry(Span{"↳ dock/bay", "bay"}, "one bay of an item that has several"),
		entry(Span{"◆ the desk", "desk"}, "the agent session for the whole tree"),
		entry(Span{"?2", "question"}, "two questions wait for your answer;"),
		entry(Span{"", ""}, "on an area, the total beneath it"),
		entry(Span{"!", "blocking"}, "one of those questions holds up all work"),
		entry(Span{"(back)", "dim"}, "the item's level, when it is not front"),
		entry(Span{"(bay gone)", "dim"}, "the item's bay no longer exists"),
		entry(Span{"'a", "key"}, "a mark on the row; type 'a to go to it"),
		entry(Span{"*2", "key"}, "the row is row 2 of the hotlist; type 2 to go to it"),
		entry(Span{"shaded row", "shown"}, "the row the right side shows, when the"),
		entry(Span{"", ""}, "cursor is on its area or its item"),
	}
}

// clipSpans shortens a line to width columns, ending it with "…".
func clipSpans(spans []Span, width int) []Span {
	total := 0
	for _, span := range spans {
		total += TextWidth(span.Text)
	}
	if width <= 0 || total <= width {
		return spans
	}
	var out []Span
	room := width - 1
	for _, span := range spans {
		if w := TextWidth(span.Text); w >= room {
			out = append(out, Span{clipText(span.Text, room) + "…", span.Style})
			return out
		} else {
			room -= w
		}
		out = append(out, span)
	}
	return out
}

// TextWidth is the number of terminal columns text takes up: wide
// characters take two, and combining marks and control characters none.
func TextWidth(text string) int {
	n := 0
	for _, r := range text {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	switch {
	case r == 0 || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) || unicode.IsControl(r):
		return 0
	case r >= 0x1100 && r <= 0x115F, r >= 0x2E80 && r <= 0xA4CF, r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF, r >= 0xFE30 && r <= 0xFE6F, r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6, r >= 0x1F300 && r <= 0x1F64F, r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}

// tailText returns text when it fits in width columns, and otherwise "…"
// followed by the end of text, so that what was typed last stays visible.
func tailText(text string, width int) string {
	if TextWidth(text) <= width {
		return text
	}
	room := max(width-1, 0)
	runes := []rune(text)
	n, k := 0, len(runes)
	for k > 0 && n+runeWidth(runes[k-1]) <= room {
		k--
		n += runeWidth(runes[k])
	}
	return "…" + string(runes[k:])
}

// clipText returns the longest start of text that is at most width columns.
func clipText(text string, width int) string {
	n := 0
	for k, r := range text {
		n += runeWidth(r)
		if n > width {
			return text[:k]
		}
	}
	return text
}

// NoticeLine is a bar showing note, on one line of at most width columns.
func NoticeLine(note string, width int) Line {
	note = strings.Join(strings.Fields(note), " ")
	return Line{Spans: clipSpans([]Span{{Text: note}}, width), Bar: true}
}
