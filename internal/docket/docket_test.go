package docket

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

// tree is a small work list: two areas, one with a front and a back goal,
// the other with one front goal that has two bays.
func tree() map[string]Item {
	answered := "2026-01-02"
	return map[string]Item{
		"people": {Name: "people", Title: "People", Kind: "area", Status: "open", Level: "front",
			Order: []string{"restart", "images"}},
		"restart": {Name: "restart", Title: "List survives a restart", Kind: "goal", Parent: ptr("people"),
			Status: "open", Level: "front", Work: "held", Sites: []Site{{"bay", "loom/b7"}},
			Questions: []Question{{BlocksAll: true}, {Answered: &answered}}},
		"images": {Name: "images", Title: "Person images", Kind: "goal", Parent: ptr("people"),
			Status: "open", Level: "back", Work: "ready"},
		"tooling": {Name: "tooling", Title: "Tooling", Kind: "area", Status: "open", Level: "front"},
		"sidebar": {Name: "sidebar", Title: "The sidebar", Kind: "goal", Parent: ptr("tooling"),
			Status: "open", Level: "front", Work: "running",
			Sites: []Site{{"bay", "bay/b8"}, {"bay", "loom/b19"}}, Questions: []Question{{}}},
		"old": {Name: "old", Title: "Finished work", Kind: "goal", Parent: ptr("tooling"),
			Status: "closed", Level: "front"},
	}
}

func names(s *Sidebar) string {
	var out []string
	for _, r := range s.Rows() {
		out = append(out, r.Name)
	}
	return strings.Join(out, ",")
}

func TestLoadItemsReadsEveryItemAndAnEmptyStateHasNone(t *testing.T) {
	dir := t.TempDir()
	items, err := LoadItems(dir)
	if err != nil || len(items) != 0 {
		t.Fatalf("empty state: %v %v", items, err)
	}
	path := filepath.Join(dir, "items", "restart")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"restart","title":"T","kind":"goal","parent":"people","status":"open","level":"front",
	  "work":"held","questions":[{"answered":null,"blocks_all":true}],"sites":[{"kind":"bay","ref":"loom/b7"}],
	  "a_field_added_later":1}`
	if err := os.WriteFile(filepath.Join(path, "item.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err = LoadItems(dir)
	if err != nil {
		t.Fatal(err)
	}
	item := items["restart"]
	if n, blocking := item.OpenQuestions(); n != 1 || !blocking {
		t.Errorf("questions = %d %v", n, blocking)
	}
	if got := item.BayRefs(); len(got) != 1 || got[0] != "loom/b7" {
		t.Errorf("bays = %v", got)
	}
	if err := os.WriteFile(filepath.Join(path, "item.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadItems(dir); err == nil {
		t.Error("a damaged item file was read without an error")
	}
}

func TestRowsShowFrontItemsUnderTheirAreasAndHideBackAndClosedOnes(t *testing.T) {
	s := NewSidebar(tree())
	want := DeskRow + ",people,restart,tooling,sidebar,sidebar bay/b8,sidebar loom/b19"
	if got := names(s); got != want {
		t.Errorf("rows = %s\nwant   %s", got, want)
	}
	people := s.Rows()[1]
	if !people.Area || people.Open != 1 || !people.Blocking {
		t.Errorf("area roll-up = %+v", people)
	}
	s.Key("a")
	if got := names(s); !strings.Contains(got, "images") || strings.Contains(got, "old") {
		t.Errorf("all levels = %s", got)
	}
}

func TestMovingTheCursorAsksForTheRowToBeShownAndSpaceFoldsAnArea(t *testing.T) {
	s := NewSidebar(tree())
	// Rows: the desk, people, restart, tooling, sidebar and its bays.
	// n and p pass over areas; f9 and f10, which tmux sends, do the same.
	for _, c := range []struct{ key, want string }{{"n", "restart"}, {"f9", "sidebar"}, {"p", "restart"}, {"f10", DeskRow}} {
		if a := s.Key(c.key); a != Show || s.Current().Name != c.want {
			t.Errorf("%s: %v on %s, want %s", c.key, a, s.Current().Name, c.want)
		}
	}
	if a := s.Key("p"); a != None || !s.Current().Desk {
		t.Errorf("p at the top: %v on %s", a, s.Current().Name)
	}
	if a := s.Key("?"); a != Help {
		t.Errorf("?: %v", a)
	}
	// j and k show the row they land on, and report nothing at an end.
	if a := s.Key("k"); a != None {
		t.Errorf("k at the top: %v", a)
	}
	if a := s.Key("j"); a != Show || !s.Current().Area {
		t.Errorf("j onto an area: %v on %s", a, s.Current().Name)
	}
	if a := s.Key("G"); a != Show {
		t.Errorf("G: %v", a)
	}
	if a := s.Key("G"); a != None {
		t.Errorf("G when already last: %v", a)
	}
	if a := s.Key("j"); a != None {
		t.Errorf("j at the bottom: %v", a)
	}
	s.Key("g")
	s.Key("j")
	if a := s.Key("enter"); a != None || s.Rows()[1].Folded {
		t.Errorf("enter on an area: %v, folded %v", a, s.Rows()[1].Folded)
	}
	if a := s.Key(" "); a != None || !s.Rows()[1].Folded {
		t.Errorf("space on an area: %v, folded %v", a, s.Rows()[1].Folded)
	}
	if got := names(s); strings.Contains(got, "restart") {
		t.Errorf("a folded area still lists its items: %s", got)
	}
	if s.Rows()[1].Open != 1 {
		t.Errorf("a folded area lost its question count")
	}
}

func TestFilterSearchesEveryLevelAndEscapeRestoresTheList(t *testing.T) {
	s := NewSidebar(tree())
	before := names(s)
	s.Key("/")
	for _, r := range "imag" {
		s.Key(string(r))
	}
	if got := names(s); got != DeskRow+",people,images" {
		t.Errorf("filtered rows = %s", got)
	}
	if a := s.Key("enter"); a != None {
		t.Errorf("enter while typing = %v", a)
	}
	s.Key("j")
	s.Key("j")
	if a := s.Key("enter"); a != ShowAndFocus || s.Current().Name != "images" {
		t.Errorf("enter on the match: %v on %s", a, s.Current().Name)
	}
	s.Key("esc")
	if got := names(s); got != before {
		t.Errorf("after escape = %s", got)
	}
}

func TestCursorStaysOnItsItemWhenTheItemsChange(t *testing.T) {
	s := NewSidebar(tree())
	for k := 0; k < 4; k++ {
		s.Key("j")
	}
	items := tree()
	items["first"] = Item{Name: "first", Title: "New", Kind: "goal", Parent: ptr("people"),
		Status: "open", Level: "front", Created: "0"}
	s.SetItems(items)
	if s.Current().Name != "sidebar" {
		t.Errorf("cursor moved to %s", s.Current().Name)
	}
}

func TestRenderKeepsTheCursorInViewAndClipsLongLines(t *testing.T) {
	s := NewSidebar(tree())
	s.Key("G")
	lines, cursor := s.Render(12, 4)
	if len(lines) != 4 {
		t.Fatalf("%d lines", len(lines))
	}
	if !strings.Contains(lines[cursor], "↳ loom") {
		t.Errorf("cursor line = %q", lines[cursor])
	}
	for _, line := range lines {
		if len([]rune(line)) > 12 {
			t.Errorf("line wider than the pane: %q", line)
		}
	}
}

// fakeTmux records the commands a View runs and answers the reads it makes.
type fakeTmux struct {
	calls   []string
	exists  map[string]bool // window ids and "=session:window" targets that exist
	panes   int             // panes in the view window: the sidebar, then the client
	noList  bool            // the sidebar's pane has gone and the client's remains
	clients string          // ttys list-clients prints
	aux     []string        // ids of the linked windows in the aux session
	stuck   map[string]bool // ids unlink-window refuses to remove
}

func (f *fakeTmux) run(args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "display-message":
		return "/dev/ttys9\n", nil
	case "list-windows":
		if args[1] == "-t" {
			out := ""
			if f.exists[recordTarget] {
				out = "@record\n"
			}
			return out + strings.Join(f.aux, "\n") + "\n", nil
		}
		// A window named like the docket sessions but in another session
		// must not be taken for one of them.
		out := "@1 =docket-desk-old:view\n"
		for target := range f.exists {
			id := target
			if strings.HasPrefix(target, "=") {
				id = "@" + target[strings.Index(target, ":")+1:]
			}
			out += id + " " + target + "\n"
		}
		return out, nil
	case "list-panes":
		started := []string{"\"bay docket sidebar\"", "\"env -u TMUX tmux attach-session -t '=docket-aux'\""}
		if f.noList {
			return started[1] + "\n", nil
		}
		return strings.Join(started[:f.panes], "\n") + "\n", nil
	case "list-clients":
		return f.clients, nil
	case "new-session":
		f.exists["="+args[3]+":"+args[5]] = true
		if args[3] == ViewSession {
			f.panes = 1
		}
	case "split-window":
		if f.noList {
			f.noList, f.panes = false, 2
		} else {
			f.panes++
		}
	case "link-window":
		f.aux = append(f.aux, args[3])
	case "unlink-window":
		id := strings.TrimPrefix(args[2], auxTarget)
		if f.stuck[id] {
			return "", fmt.Errorf("window only linked to one session")
		}
		kept := f.aux[:0]
		for _, w := range f.aux {
			if w != id {
				kept = append(kept, w)
			}
		}
		f.aux = kept
	}
	return "", nil
}

func (f *fakeTmux) ran(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func newFakeView(f *fakeTmux) *View {
	return &View{
		Tmux: f.run, SidebarCommand: "bay docket sidebar", TmuxCommand: "tmux",
		BayWindow: func(ref string) (string, bool) {
			if ref == "loom/b7" {
				return "@7", true
			}
			if ref == "loom/b9" {
				return "@9", true
			}
			return "", false
		},
		RecordCommand: func(item string) string { return "docket show " + item },
		StartDesk:     func() error { f.exists[deskTarget] = true; f.calls = append(f.calls, "start-desk"); return nil },
	}
}

func TestSetupCreatesTheSessionsOnceAndIsSafeToRepeat(t *testing.T) {
	f := &fakeTmux{exists: map[string]bool{}}
	v := newFakeView(f)
	if err := v.Setup(); err != nil {
		t.Fatal(err)
	}
	if f.ran("new-session") != 2 || f.ran("split-window") != 1 {
		t.Fatalf("first setup: %v", f.calls)
	}
	if f.ran("set-option -t =docket: prefix None") != 1 || f.ran("set-option -t =docket: key-table docket") != 1 {
		t.Errorf("session options not set: %v", f.calls)
	}
	for _, key := range []string{"M-l F5", "M-h F6", "M-J F5", "M-K F6", "M-N F9", "M-P F10", "M-Space F7"} {
		k, sent, _ := strings.Cut(key, " ")
		// Each of these keys does nothing while a pane of the view is
		// zoomed. It works while the list is collapsed.
		want := "bind-key -T docket " + k + " if-shell -F -t =docket:view.{left} #{window_zoomed_flag}  send-keys -t '=docket:view.{left}' " + sent
		if f.ran(want) != 1 {
			t.Errorf("step key %s not bound as %q: %v", k, want, f.calls)
		}
	}
	if f.ran("bind-key -T docket M-? send-keys -t =docket:view.{left} F8") != 1 {
		t.Errorf("the state key is not bound: %v", f.calls)
	}
	for _, want := range []string{
		"bind-key -T docket M-B send-keys -t =docket:view.{left} F11",
		"bind-key -T docket MouseDrag1Border resize-pane -M",
		"bind-key -T docket DoubleClick1Border resize-pane -t =docket:view.{left} -x 36",
	} {
		if f.ran(want) != 1 {
			t.Errorf("not bound: %q in %v", want, f.calls)
		}
	}
	// The width is set when the panes are started and not again, so a
	// width chosen later is kept.
	if f.ran("resize-pane -t =docket:view.{left} -x 36") != 1 {
		t.Errorf("first setup did not set the width once: %v", f.calls)
	}
	defer func() {
		if f.ran("resize-pane") != 1 {
			t.Errorf("a later setup changed the width: %v", f.calls)
		}
	}()
	if f.ran("unbind-key -a -T docket") != 1 || f.ran("unbind-key") != 1 {
		t.Errorf("the view's key table was not emptied, or another was: %v", f.calls)
	}
	leaveBound := false
	defer func() {
		if !leaveBound {
			t.Errorf("the key that leaves the view is not bound: %v", f.calls)
		}
	}()
	if f.ran(`bind-key -T docket MouseDown1Pane select-pane -t = \; send-keys -M`) != 1 {
		t.Errorf("the mouse press is not bound to both commands: %v", f.calls)
	}
	for _, c := range f.calls {
		if c == "bind-key -T docket M-D if-shell -F #{client_last_session} switch-client -l detach-client" {
			leaveBound = true
		}
		if strings.HasPrefix(c, "bind-key") && !strings.HasPrefix(c, "bind-key -T docket ") {
			t.Errorf("a key was bound outside the docket table: %s", c)
		}
		if strings.HasPrefix(c, "set-option") && !strings.Contains(c, "-t =docket") {
			t.Errorf("an option was set outside the docket sessions: %s", c)
		}
	}
	if err := v.Setup(); err != nil {
		t.Fatal(err)
	}
	if f.ran("new-session") != 2 || f.ran("split-window") != 1 {
		t.Errorf("second setup created more: %v", f.calls)
	}
}

func TestShowLinksTheBayWindowIntoTheViewsSessionOrShowsTheRecord(t *testing.T) {
	f := &fakeTmux{exists: map[string]bool{"@7": true, "@9": false, recordTarget: true}, panes: 2, clients: "/dev/ttys1\n/dev/ttys9\n"}
	delete(f.exists, "@9")
	v := newFakeView(f)

	note, err := v.Show(Row{Name: "restart", Bays: []string{"loom/b7"}}, true)
	if err != nil || note != "" {
		t.Fatalf("bay window: %q %v", note, err)
	}
	if f.ran("link-window -d -s @7 -t =docket-aux:") != 1 ||
		f.ran("switch-client -c /dev/ttys9 -t =docket-aux:@7") != 1 || f.ran("select-pane") != 1 {
		t.Errorf("calls: %v", f.calls)
	}

	// Showing the same window again links nothing more.
	if _, err := v.Show(Row{Name: "restart", Bays: []string{"loom/b7"}}, false); err != nil {
		t.Fatal(err)
	}
	if f.ran("link-window") != 1 || f.ran("unlink-window") != 0 {
		t.Errorf("calls: %v", f.calls)
	}

	// loom/b9 is in the manifest but its window is gone. The record is
	// shown, and the window shown before leaves the view's session.
	note, err = v.Show(Row{Name: "gone", Bays: []string{"loom/b9"}}, false)
	if err != nil || !strings.Contains(note, "no window") {
		t.Fatalf("missing window: %q %v", note, err)
	}
	if f.ran("respawn-window -k -t =docket-aux:record docket show gone") != 1 ||
		f.ran("switch-client -c /dev/ttys9 -t =docket-aux:@record") != 1 ||
		f.ran("unlink-window -t =docket-aux:@7") != 1 || len(f.aux) != 0 {
		t.Errorf("calls: %v", f.calls)
	}
	if f.ran("unlink-window -t =docket-aux:@record") != 0 || f.ran("link-window -d -s @record") != 0 {
		t.Errorf("the record window was linked or unlinked: %v", f.calls)
	}
	if f.ran("select-pane") != 1 {
		t.Errorf("focus moved without being asked: %v", f.calls)
	}

	if _, err := v.Show(Row{Name: "sidebar bay/b8", Bays: []string{"bay/b8"}, Bay: "bay/b8"}, false); err != nil {
		t.Fatal(err)
	}
	if f.ran("respawn-window -k -t =docket-aux:record docket show sidebar") != 1 {
		t.Errorf("a bay row did not show its item's record: %v", f.calls)
	}

	before := len(f.calls)
	if note, err := v.Show(Row{Name: "people", Area: true}, true); err != nil || note != "" || len(f.calls) != before {
		t.Errorf("an area was shown: %v", f.calls[before:])
	}
}

func TestShowKeepsAWindowThatOnlyTheViewHolds(t *testing.T) {
	f := &fakeTmux{exists: map[string]bool{"@7": true, "@5": true, recordTarget: true}, panes: 2, clients: "/dev/ttys9\n",
		aux: []string{"@5"}, stuck: map[string]bool{"@5": true}}
	v := newFakeView(f)
	note, err := v.Show(Row{Name: "restart", Bays: []string{"loom/b7"}}, false)
	if err != nil || !strings.Contains(note, "@5") {
		t.Fatalf("note = %q, err = %v", note, err)
	}
	if f.ran("kill-window") != 0 || f.ran("unlink-window -k") != 0 {
		t.Errorf("a window was killed: %v", f.calls)
	}
}

func TestShowStartsTheDeskOnlyWhenItIsNotRunning(t *testing.T) {
	f := &fakeTmux{exists: map[string]bool{recordTarget: true}, panes: 2, clients: "/dev/ttys9\n"}
	v := newFakeView(f)
	for k := 0; k < 2; k++ {
		if _, err := v.Show(Row{Name: DeskRow, Desk: true}, false); err != nil {
			t.Fatal(err)
		}
	}
	if f.ran("start-desk") != 1 || f.ran("link-window -d -s @desk") != 1 ||
		f.ran("switch-client -c /dev/ttys9 -t =docket-aux:@desk") != 2 {
		t.Errorf("calls: %v", f.calls)
	}
}

func TestShowRestartsTheRightPaneWhenItHasGoneAndFailsWithoutAClient(t *testing.T) {
	f := &fakeTmux{exists: map[string]bool{"@7": true}, panes: 1, clients: "/dev/ttys1\n"}
	v := newFakeView(f)
	_, err := v.Show(Row{Name: "restart", Bays: []string{"loom/b7"}}, false)
	if err == nil || f.ran("split-window") != 1 || f.ran("switch-client") != 0 {
		t.Errorf("err = %v, calls: %v", err, f.calls)
	}
}

func TestSetupRestartsTheSidebarWhenOnlyTheRightPaneRemains(t *testing.T) {
	f := &fakeTmux{exists: map[string]bool{recordTarget: true, viewTarget: true}, noList: true}
	v := newFakeView(f)
	for k := 0; k < 2; k++ {
		if err := v.Setup(); err != nil {
			t.Fatal(err)
		}
	}
	if f.ran("split-window -h -b -f -d -t =docket:view bay docket sidebar") != 1 || f.ran("split-window") != 1 {
		t.Errorf("calls: %v", f.calls)
	}
}

func TestShiftJAndKJumpTenRowsAndStopAtTheEnds(t *testing.T) {
	items := map[string]Item{}
	for k := 0; k < 25; k++ {
		name := fmt.Sprintf("item%02d", k)
		items[name] = Item{Name: name, Level: "front", Status: "open"}
	}
	s := NewSidebar(items)
	last := len(s.Rows()) - 1
	if last < 2*JumpRows {
		t.Fatalf("too few rows to test a jump: %d", last+1)
	}
	at := func() int {
		for k, r := range s.Rows() {
			if r.Name == s.Current().Name {
				return k
			}
		}
		return -1
	}
	if a := s.Key("K"); a != None || at() != 0 {
		t.Errorf("K at the top: %v at %d", a, at())
	}
	if a := s.Key("J"); a != Show || at() != JumpRows {
		t.Errorf("J: %v at %d", a, at())
	}
	s.Key("J")
	if a := s.Key("J"); a != Show || at() != last {
		t.Errorf("J near the bottom: %v at %d, last %d", a, at(), last)
	}
	if a := s.Key("J"); a != None {
		t.Errorf("J at the bottom: %v", a)
	}
	if a := s.Key("K"); a != Show || at() != last-JumpRows {
		t.Errorf("K: %v at %d", a, at())
	}
}

func TestClickGoesToTheRowFoldsAnAreaAndIgnoresLinesBelowTheRows(t *testing.T) {
	s := NewSidebar(tree())
	s.Render(30, 20)
	// Rows: the desk, people, restart, tooling, sidebar.
	if a := s.Click(2, 5); a != Show || s.Current().Name != "restart" {
		t.Errorf("click on an item: %v on %s", a, s.Current().Name)
	}
	if a := s.Click(1, 5); a != None || s.Current().Name != "people" || !s.Current().Folded {
		t.Errorf("click on an area: %v on %s folded %v", a, s.Current().Name, s.Current().Folded)
	}
	at := s.Current().Name
	for _, line := range []int{-1, len(s.Rows()), 19, 40} {
		if a := s.Click(line, 5); a != None || s.Current().Name != at {
			t.Errorf("click on line %d: %v on %s", line, a, s.Current().Name)
		}
	}
	// A click counts from the first row drawn, not the first row.
	s = NewSidebar(tree())
	s.Key("G")
	s.Render(30, 4) // room for the last two rows
	rows := s.Rows()
	last := len(rows) - 1
	if rows[last].Area || rows[last-1].Area {
		t.Fatalf("the test needs two rows that are not areas at the end")
	}
	if a := s.Click(0, 5); a != Show || s.Current().Name != rows[last-1].Name {
		t.Errorf("click on the first row drawn: %v on %s", a, s.Current().Name)
	}
	if a := s.Click(1, 5); a != Show || s.Current().Name != rows[last].Name {
		t.Errorf("click on the second row drawn: %v on %s", a, s.Current().Name)
	}
}

func TestWheelScrollsTheRowsWithoutMovingTheCursorAndStopsAtTheEnds(t *testing.T) {
	items := map[string]Item{}
	for k := 0; k < 25; k++ {
		name := fmt.Sprintf("item%02d", k)
		items[name] = Item{Name: name, Level: "front", Status: "open"}
	}
	s := NewSidebar(items)
	rows := s.Rows()
	first := func() string {
		lines, _ := s.Render(30, 7) // five rows and the two bars
		return lines[0]
	}
	top := first()
	if a := s.Key("wheelup"); a != None || first() != top {
		t.Errorf("wheel up at the top moved the rows: %v", a)
	}
	for k := 0; k < 3; k++ {
		if a := s.Key("wheeldown"); a != None {
			t.Errorf("the wheel asked for %v", a)
		}
	}
	if got := first(); !strings.Contains(got, rows[3].Name) {
		t.Errorf("after three steps the first line is %q, want %s", got, rows[3].Name)
	}
	if s.Current().Name != rows[0].Name {
		t.Errorf("the wheel moved the cursor to %s", s.Current().Name)
	}
	if _, cursor := s.Render(30, 7); cursor >= 0 && cursor < 5 {
		t.Errorf("the cursor's row is drawn at line %d after scrolling past it", cursor)
	}
	// A click lands on the row drawn under it.
	if a := s.Click(1, 5); a != Show || s.Current().Name != rows[4].Name {
		t.Errorf("click after scrolling: %v on %s", a, s.Current().Name)
	}
	for k := 0; k < 100; k++ {
		s.Key("wheeldown")
	}
	if got := first(); !strings.Contains(got, rows[len(rows)-5].Name) {
		t.Errorf("the wheel scrolled past the end: first line %q", got)
	}
	// The next key brings the cursor's row back into sight.
	s.Key("k")
	if _, cursor := s.Render(30, 7); cursor < 0 || cursor >= 5 {
		t.Errorf("after a key the cursor's row is not drawn: line %d", cursor)
	}
}

func TestParseKeysNamesKeysAndKeepsAnUnfinishedKeyForTheNextRead(t *testing.T) {
	wheel := "\x1b[<65;5;7M"
	for _, c := range []struct{ input, want, rest string }{
		{"j", "j", ""},
		{"\r", "enter", ""},
		{"\x1b", "esc", ""},
		{"\x7f\x03", "backspace ctrl-c", ""},
		{"\x1b[A\x1bOB", "up down", ""},
		{"é/", "é /", ""},
		{"\x1b[<0;5;7M", "click:6:4", ""},
		{"\x1b[<0;5;7m", "", ""}, // the button's release
		{"\x1b[<64;5;7M" + wheel, "wheelup wheeldown", ""},
		{"\x1b[<2;5;7M\x1b[1;5C\x1bj", "", ""},                // another button, another key, Option-j
		{"\x1b[15~\x1b[20~\x1b[21~\x1b[24~", "f5 f9 f10", ""}, // F12 has no name
		// A report cut at each point leaves no stray keys.
		{wheel + wheel[:1], "wheeldown", wheel[:1]},
		{wheel + wheel[:2], "wheeldown", wheel[:2]},
		{wheel + wheel[:7], "wheeldown", wheel[:7]},
		{"j\x1bO", "j", "\x1bO"},
		{"j\xc3", "j", "\xc3"}, // the first byte of é
	} {
		keys, rest := ParseKeys(c.input)
		if got := strings.Join(keys, " "); got != c.want || rest != c.rest {
			t.Errorf("ParseKeys(%q) = %q, rest %q; want %q, rest %q", c.input, got, rest, c.want, c.rest)
		}
	}
	// The rest of a cut report completes it on the next read.
	_, rest := ParseKeys(wheel[:7])
	if keys, rest := ParseKeys(rest + wheel[7:]); len(keys) != 1 || keys[0] != "wheeldown" || rest != "" {
		t.Errorf("a report in two reads gave %q, rest %q", keys, rest)
	}
}

func TestLevelsCycleAndTheHeadingSaysWhichAreShown(t *testing.T) {
	items := tree()
	items["later"] = Item{Name: "later", Kind: "goal", Parent: ptr("people"), Status: "open", Level: "back", Created: "1"}
	items["someday"] = Item{Name: "someday", Kind: "goal", Parent: ptr("people"), Status: "open", Level: "deep", Created: "2"}
	s := NewSidebar(items)
	for _, c := range []struct{ heading, has, lacks string }{
		{"« front (a)", "restart", "later someday"},
		{"« front + back (a)", "restart later", "someday"},
		{"« front + back + deep (a)", "restart later someday", ""},
		{"« front (a)", "restart", "later someday"},
	} {
		lines, _ := s.Render(40, 30)
		got := names(s)
		if lines[28] != c.heading {
			t.Errorf("the bar above the key reminder = %q, want %q", lines[28], c.heading)
		}
		for _, name := range strings.Fields(c.has) {
			if !strings.Contains(got, name) {
				t.Errorf("%s: %s is missing from %s", c.heading, name, got)
			}
		}
		for _, name := range strings.Fields(c.lacks) {
			if strings.Contains(got, name) {
				t.Errorf("%s: %s is shown in %s", c.heading, name, got)
			}
		}
		s.Key("a")
	}
}

func TestUnfoldAllShowsEveryAreaAndPutsTheFoldsBack(t *testing.T) {
	s := NewSidebar(tree())
	// Rows: the desk, people, restart, tooling, sidebar and its bays.
	s.Key("j")
	s.Key(" ") // fold people
	folded := names(s)
	if strings.Contains(folded, "restart") {
		t.Fatalf("people is not folded: %s", folded)
	}
	s.Key("u")
	if got := names(s); !strings.Contains(got, "restart") {
		t.Errorf("unfold all left people folded: %s", got)
	}
	if lines, _ := s.Render(60, 30); !strings.Contains(lines[28], "all unfolded") {
		t.Errorf("the bar does not say every area is unfolded: %q", lines[28])
	}
	// A fold made in between is dropped when the folds are put back.
	for s.Current().Name != "tooling" {
		s.Key("j")
	}
	s.Key(" ")
	s.Key("u")
	if got := names(s); got != folded {
		t.Errorf("after putting the folds back: %s, want %s", got, folded)
	}
	// The areas above the cursor's row stay unfolded.
	s.Key("u")
	for s.Current().Name != "restart" {
		s.Key("k")
	}
	s.Key("u")
	if got := names(s); !strings.Contains(got, "restart") || s.Current().Name != "restart" {
		t.Errorf("the cursor's row was folded away: %s, cursor on %s", got, s.Current().Name)
	}
}

func TestCollapseAndExpandByKeyAndByClick(t *testing.T) {
	s := NewSidebar(tree())
	s.Render(36, 20)
	if a := s.Key("b"); a != Collapse {
		t.Errorf("b: %v", a)
	}
	// Of 20 lines, 18 are rows and line 18 is the bar with the mark.
	if a := s.Click(18, 0); a != Collapse {
		t.Errorf("click on the mark: %v", a)
	}
	for _, at := range [][2]int{{18, 9}, {19, 0}} {
		if a := s.Click(at[0], at[1]); a != None || !s.Current().Desk {
			t.Errorf("click on a bar at %v: %v on %s", at, a, s.Current().Name)
		}
	}
	if s.Collapsed() {
		t.Error("a full-width list reports that it is collapsed")
	}
	lines, cursor := s.Render(CollapsedWidth, 20)
	// The mark that expands is on the line where the mark that collapses was.
	if len(lines) != 20 || lines[18] != "»" || strings.TrimSpace(strings.Join(lines[:18], "")+lines[19]) != "" || cursor != -1 {
		t.Errorf("collapsed: %q, cursor %d", lines, cursor)
	}
	if a := s.Key("b"); a != Expand {
		t.Errorf("b on a collapsed list: %v", a)
	}
	if a := s.Key("f11"); a != Expand {
		t.Errorf("f11 on a collapsed list: %v", a)
	}
	// Expanding gives back the width the list had, not the first width.
	if s.FullWidth() != 36 {
		t.Errorf("FullWidth = %d", s.FullWidth())
	}
	s.Render(50, 20)
	s.Render(CollapsedWidth, 20)
	if s.FullWidth() != 50 {
		t.Errorf("FullWidth after a wider list = %d", s.FullWidth())
	}
	if a := s.Key("="); a != ResetWidth {
		t.Errorf("=: %v", a)
	}
	if !s.Collapsed() {
		t.Error("a narrow list does not report that it is collapsed")
	}
	if a := s.Click(5, 1); a != Expand || !s.Current().Desk {
		t.Errorf("click on the collapsed list: %v on %s", a, s.Current().Name)
	}
}

func TestKeysSentByTmuxWorkWhileAFilterIsBeingTyped(t *testing.T) {
	s := NewSidebar(tree())
	s.Key("/")
	s.Key("r")
	before := names(s)
	if a := s.Key("f5"); a != Show {
		t.Errorf("f5 while typing: %v", a)
	}
	if a := s.Key("f8"); a != State {
		t.Errorf("f8 while typing: %v", a)
	}
	if names(s) != before {
		t.Errorf("a key sent by tmux changed the filter: %s", names(s))
	}
	// The same letters typed by hand go into the filter.
	s.Key("s")
	if names(s) == before {
		t.Errorf("a typed letter did not change the filter")
	}
}

func TestSummaryGivesTheStateQuestionsAndHolds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "items", "restart")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	item := `{"name":"restart","title":"Restart safely","kind":"goal","status":"open","level":"front","work":"held",
		"sites":[{"kind":"bay","ref":"loom/b7"}],"links":["loom#12"],"holds":[{"on":"a review"}],
		"questions":[{"id":"q1","text":"Old?","answered":"2026-01-01"},{"id":"q2","text":"Merge it?","blocks_all":true}]}`
	record := "# Restart\n\n## Purpose\n\nWhy.\n\n## State\n\nPR open.\nWaiting.\n\n## History\n\n- old\n"
	for name, text := range map[string]string{"item.json": item, "record.md": record} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Summary(dir, "restart")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"restart — Restart safely", "held", "bays loom/b7", "links loom#12",
		"Waits on: a review", "Question q2 (holds up all work): Merge it?", "PR open.\nWaiting."} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"Old?", "Why.", "- old"} {
		if strings.Contains(got, not) {
			t.Errorf("summary includes %q:\n%s", not, got)
		}
	}
	if _, err := Summary(dir, "nothing"); err == nil {
		t.Error("an unknown item gave no error")
	}
}

func TestRowsAreStyledByKindAndTheLastTwoLinesAreBars(t *testing.T) {
	s := NewSidebar(tree())
	lines, _ := s.RenderLines(60, 12)
	if len(lines) != 12 || !lines[10].Bar || !lines[11].Bar {
		t.Fatalf("%d lines; bars: %v %v", len(lines), lines[10].Bar, lines[11].Bar)
	}
	styleOf := func(text string) string {
		for _, line := range lines[:10] {
			if line.Bar {
				t.Errorf("a row is drawn as a bar: %q", line.String())
			}
			for _, span := range line.Spans {
				if strings.Contains(span.Text, text) {
					return span.Style
				}
			}
		}
		return "not drawn"
	}
	for text, want := range map[string]string{"the desk": "desk", "▾": "area", "↳": "bay", "?1": "question", "!": "blocking", " restart": ""} {
		if got := styleOf(text); got != want {
			t.Errorf("%q is drawn as %q, want %q", text, got, want)
		}
	}
	for _, c := range []struct {
		work   string
		hasBay bool
		symbol string
		style  string
	}{{"running", true, "●", "running"}, {"ready", true, "○", "ready"}, {"held", true, "◌", "held"}, {"running", false, "·", "dim"}} {
		if symbol, style := WorkSymbol(c.work, c.hasBay); symbol != c.symbol || style != c.style {
			t.Errorf("WorkSymbol(%q, %v) = %q %q", c.work, c.hasBay, symbol, style)
		}
	}
	// In the bars, the names of keys and the mark to click are styled as
	// keys, and what each key does is not.
	bars := map[string]string{}
	for _, line := range lines[10:] {
		for _, span := range line.Spans {
			bars[strings.TrimSpace(span.Text)] = span.Style
		}
	}
	for text, want := range map[string]string{"«": "key", "a": "key", "?": "key", "/": "key", "keys": "", "find": ""} {
		if got, ok := bars[text]; !ok || got != want {
			t.Errorf("in the bars %q is drawn as %q (present %v), want %q", text, got, ok, want)
		}
	}
	// A line too long for the pane is cut inside whichever span crosses
	// the edge, and keeps that span's style.
	narrow, _ := s.RenderLines(12, 12)
	for _, line := range narrow {
		if n := len([]rune(line.String())); n > 12 {
			t.Errorf("%q is %d columns", line.String(), n)
		}
	}
	// The legend names every style a row can have.
	legend := ""
	for _, line := range Legend() {
		for _, span := range line.Spans {
			legend += span.Style + " "
		}
	}
	for _, style := range []string{"area", "desk", "bay", "dim", "running", "ready", "held", "question", "blocking"} {
		if !strings.Contains(legend, style) {
			t.Errorf("the legend has no entry drawn as %q", style)
		}
	}
}

func TestWrapBreaksAtSpacesAndKeepsLineBreaks(t *testing.T) {
	got := Wrap("one two three\n\nfour averyveryverylongword five\n", 9)
	want := []string{"one two", "three", "", "four", "averyveryverylongword", "five"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Wrap = %q, want %q", got, want)
	}
}
