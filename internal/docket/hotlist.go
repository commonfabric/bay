package docket

import (
	"fmt"
	"slices"
	"strings"
)

// MaxHot is how many rows the hotlist holds.
const MaxHot = 9

// SetHotlist replaces the hotlist: the names of its rows, in order.
func (s *Sidebar) SetHotlist(names []string) {
	s.hotlist = slices.Clone(names)
	if len(s.hotlist) > MaxHot {
		s.hotlist = s.hotlist[:MaxHot]
	}
	s.rebuild()
}

// Hotlist returns the names of the hotlist's rows, in order.
func (s *Sidebar) Hotlist() []string { return s.hotlist }

// ForgetHotlist takes off the hotlist the rows whose item is closed or no
// longer exists, and the rows of a bay the item no longer lists. It
// reports whether it took any off. It takes none off when there are no
// items at all.
func (s *Sidebar) ForgetHotlist() bool {
	if len(s.items) == 0 {
		return false
	}
	kept := slices.DeleteFunc(slices.Clone(s.hotlist), func(name string) bool { return !s.hotLives(name) })
	if len(kept) == len(s.hotlist) {
		return false
	}
	s.hotlist = kept
	s.rebuild()
	return true
}

// hotLives reports whether a hotlist row can still be drawn: its item is
// open, and for a bay's row, the item still lists that bay. A bay's row
// that the list does not draw, such as the one bay of an item while bays
// are not all shown, is kept and left out of the hotlist until it is drawn.
func (s *Sidebar) hotLives(name string) bool {
	itemName, ref, isBay := strings.Cut(name, " ")
	item, ok := s.items[itemName]
	if !ok || item.Status != "open" {
		return false
	}
	return !isBay || slices.Contains(item.BayRefs(), ref)
}

// hotRows returns the rows of the hotlist. Each is the row of the same
// name in the tree as it would be drawn with every level shown, no filter
// and no folds, so that the hotlist shows what is in it whatever the tree
// shows.
func (s *Sidebar) hotRows() []Row {
	if len(s.hotlist) == 0 {
		return nil
	}
	levels, filter, folded, first := s.levels, s.filter, s.folded, s.first
	s.levels, s.filter, s.folded, s.first = len(levelNames)-1, "", map[string]bool{}, map[string]Row{}
	all := s.tree()
	s.levels, s.filter, s.folded, s.first = levels, filter, folded, first
	var out []Row
	for _, name := range s.hotlist {
		for _, r := range all {
			if r.Name == name {
				r.Depth, r.HotRow, r.Hot = 0, true, len(out)+1
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// hotCount returns how many rows of the hotlist are drawn.
func (s *Sidebar) hotCount() int {
	n := 0
	for _, r := range s.rows {
		if r.HotRow {
			n++
		}
	}
	return n
}

// toggleHot adds the row under the cursor to the end of the hotlist, or
// takes it off.
func (s *Sidebar) toggleHot() Action {
	row := s.Current()
	switch {
	case row.Desk:
		s.notice = "the desk is always at the top; it is not added to the hotlist"
		return None
	case slices.Contains(s.hotlist, row.Name):
		return s.removeHot(row.Name)
	case len(s.hotlist) >= MaxHot:
		s.notice = "the hotlist holds at most nine rows; take one off first"
		return None
	}
	s.hotlist = append(s.hotlist, row.Name)
	s.rebuild()
	s.notice = fmt.Sprintf("%s is hotlist row %d", row.Name, len(s.hotlist))
	return HotlistChanged
}

// removeHot takes a row off the hotlist. When the cursor is on that row in
// the hotlist, it goes to the row that takes its place, or the one above
// when it was the last, or to the desk when the hotlist is then empty.
func (s *Sidebar) removeHot(name string) Action {
	row := s.Current()
	onHot := row.HotRow && row.Name == name
	at := s.cursor - 1
	s.hotlist = slices.DeleteFunc(s.hotlist, func(n string) bool { return n == name })
	s.rebuild()
	s.notice = name + " is off the hotlist"
	if !onHot {
		return HotlistChanged
	}
	s.cursor, s.moved = 0, false
	if n := s.hotCount(); n > 0 {
		s.cursor = 1 + min(at, n-1)
	}
	return HotlistChanged
}

// otherPlace moves the cursor between a row of the hotlist and the same
// row in its area. From the hotlist it brings the row into the list as a
// jump to a mark does, and unfolds the row itself when it is an area, so
// that what is under it is listed below the cursor. Either way it is a
// jump, so typing the quote key twice goes back.
func (s *Sidebar) otherPlace() Action {
	row := s.Current()
	if row.HotRow {
		itemName, _, _ := strings.Cut(row.Name, " ")
		delete(s.folded, itemName)
		return s.jump("o", row.Name)
	}
	for k, r := range s.rows {
		if r.HotRow && r.Name == row.Name {
			s.cursor, s.back = k, rowKey(row)
			return Show
		}
	}
	s.notice = "this row is not in the hotlist; * adds it"
	return None
}

// jumpHot moves the cursor to row n of the hotlist.
func (s *Sidebar) jumpHot(n int) Action {
	if n > s.hotCount() {
		s.notice = fmt.Sprintf("the hotlist has no row %d", n)
		return None
	}
	s.cursor = n
	return Show
}

// reorder moves the row under the cursor down or up. In the hotlist it
// changes the row's place there. Elsewhere it asks the caller to move the
// item just after the next shown sibling below it, or just before the next
// one above it, which MoveSibling asks for.
func (s *Sidebar) reorder(down bool) Action {
	row := s.Current()
	step := -1
	if down {
		step = 1
	}
	switch {
	case row.Desk:
		s.notice = "the desk has no place to move"
		return None
	case row.Bay != "":
		s.notice = "a bay's row stays under its item; move the item"
		return None
	case row.HotRow:
		next := s.cursor + step
		if next < 1 || next >= len(s.rows) || !s.rows[next].HotRow {
			s.notice = "already at the " + map[bool]string{true: "end", false: "start"}[down] + " of the hotlist"
			return None
		}
		i, j := slices.Index(s.hotlist, row.Name), slices.Index(s.hotlist, s.rows[next].Name)
		s.hotlist[i], s.hotlist[j] = s.hotlist[j], s.hotlist[i]
		s.rebuild()
		return HotlistChanged
	}
	for k := s.cursor + step; k >= 0 && k < len(s.rows); k += step {
		r := s.rows[k]
		if r.Desk || r.HotRow || r.Depth < row.Depth {
			break
		}
		if r.Depth == row.Depth && r.Bay == "" {
			s.sibling = SiblingMove{Item: row.Name, Sibling: r.Name, After: down}
			return MoveSibling
		}
	}
	s.notice = row.Name + " is already the " + map[bool]string{true: "last", false: "first"}[down] + " one shown under its parent"
	return None
}

// SiblingRequest returns the move that MoveSibling asks for: the item, the
// docket flag that places it, and the sibling.
func (s *Sidebar) SiblingRequest() (item, flag, sibling string) {
	m := s.sibling
	s.sibling = SiblingMove{}
	flag = "--before"
	if m.After {
		flag = "--after"
	}
	return m.Item, flag, m.Sibling
}
