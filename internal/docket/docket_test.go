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
	// The removed item-step keys do nothing.
	if a := s.Key("n"); a != None || !s.Current().Desk {
		t.Errorf("n: %v on %s", a, s.Current().Name)
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
	for _, key := range []string{"M-l j", "M-h k", "M-J j", "M-K k", "M-Space Space"} {
		k, sent, _ := strings.Cut(key, " ")
		// Each step key does nothing while a pane of the view is zoomed.
		want := "bind-key -T docket " + k + " if-shell -F -t =docket:view #{window_zoomed_flag}  send-keys -t '=docket:view.{left}' " + sent
		if f.ran(want) != 1 {
			t.Errorf("step key %s not bound as %q: %v", k, want, f.calls)
		}
	}
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
	if a := s.Click(2); a != Show || s.Current().Name != "restart" {
		t.Errorf("click on an item: %v on %s", a, s.Current().Name)
	}
	if a := s.Click(1); a != None || s.Current().Name != "people" || !s.Current().Folded {
		t.Errorf("click on an area: %v on %s folded %v", a, s.Current().Name, s.Current().Folded)
	}
	at := s.Current().Name
	for _, line := range []int{-1, len(s.Rows()), 19, 40} {
		if a := s.Click(line); a != None || s.Current().Name != at {
			t.Errorf("click on line %d: %v on %s", line, a, s.Current().Name)
		}
	}
	// A click counts from the first row drawn, not the first row.
	s = NewSidebar(tree())
	s.Key("G")
	s.Render(30, 3) // room for the last two rows
	rows := s.Rows()
	last := len(rows) - 1
	if rows[last].Area || rows[last-1].Area {
		t.Fatalf("the test needs two rows that are not areas at the end")
	}
	if a := s.Click(0); a != Show || s.Current().Name != rows[last-1].Name {
		t.Errorf("click on the first row drawn: %v on %s", a, s.Current().Name)
	}
	if a := s.Click(1); a != Show || s.Current().Name != rows[last].Name {
		t.Errorf("click on the second row drawn: %v on %s", a, s.Current().Name)
	}
	if a := s.Key("wheelup"); a != Show || s.Current().Name != rows[last-1].Name {
		t.Errorf("wheel: %v on %s", a, s.Current().Name)
	}
}
