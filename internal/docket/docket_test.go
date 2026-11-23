package docket

import (
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

func TestStepKeysSkipAreasAndAskForTheRowToBeShown(t *testing.T) {
	s := NewSidebar(tree())
	if a := s.Key("J"); a != Show || s.Current().Name != "restart" {
		t.Fatalf("first step: %v on %s", a, s.Current().Name)
	}
	if a := s.Key("J"); a != Show || s.Current().Name != "sidebar" {
		t.Fatalf("second step: %v on %s", a, s.Current().Name)
	}
	if a := s.Key("K"); a != Show || s.Current().Name != "restart" {
		t.Fatalf("step back: %v on %s", a, s.Current().Name)
	}
	s.Key("K")
	if a := s.Key("K"); a != None || !s.Current().Desk {
		t.Errorf("step past the top: %v on %s", a, s.Current().Name)
	}
	s.Key("j")
	if a := s.Key("enter"); a != None || !s.Rows()[1].Folded {
		t.Errorf("enter on an area: %v, folded %v", a, s.Rows()[1].Folded)
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
	s.Key("J")
	s.Key("J")
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
	panes   int             // panes in the view window
	clients string          // ttys list-clients prints
}

func (f *fakeTmux) run(args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "display-message":
		return "/dev/ttys9\n", nil
	case "list-windows":
		// A window named like the docket sessions but in another session
		// must not be taken for one of them.
		out := "@1 =docket-desk-old:view\n"
		for target := range f.exists {
			out += target + " " + target + "\n"
		}
		return out, nil
	case "list-panes":
		return strings.Repeat("%1\n", f.panes), nil
	case "list-clients":
		return f.clients, nil
	case "new-session":
		f.exists["="+args[3]+":"+args[5]] = true
		if args[3] == ViewSession {
			f.panes = 1
		}
	case "split-window":
		f.panes++
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
	if f.ran("bind-key -T docket M-l send-keys") != 1 || f.ran("bind-key -T docket M-h send-keys") != 1 {
		t.Errorf("step keys not bound: %v", f.calls)
	}
	for _, c := range f.calls {
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

func TestShowSwitchesTheRightPaneToTheBayWindowOrTheRecord(t *testing.T) {
	f := &fakeTmux{exists: map[string]bool{"@7": true}, panes: 2, clients: "/dev/ttys1\n/dev/ttys9\n"}
	v := newFakeView(f)

	note, err := v.Show(Row{Name: "restart", Bays: []string{"loom/b7"}}, true)
	if err != nil || note != "" {
		t.Fatalf("bay window: %q %v", note, err)
	}
	if f.ran("switch-client -c /dev/ttys9 -t @7") != 1 || f.ran("select-pane") != 1 {
		t.Errorf("calls: %v", f.calls)
	}

	// loom/b9 is in the manifest but its window is gone.
	note, err = v.Show(Row{Name: "gone", Bays: []string{"loom/b9"}}, false)
	if err != nil || !strings.Contains(note, "no window") {
		t.Fatalf("missing window: %q %v", note, err)
	}
	if f.ran("respawn-window -k -t =docket-aux:record docket show gone") != 1 ||
		f.ran("switch-client -c /dev/ttys9 -t =docket-aux:record") != 1 {
		t.Errorf("calls: %v", f.calls)
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

func TestShowStartsTheDeskOnlyWhenItIsNotRunning(t *testing.T) {
	f := &fakeTmux{exists: map[string]bool{}, panes: 2, clients: "/dev/ttys9\n"}
	v := newFakeView(f)
	for k := 0; k < 2; k++ {
		if _, err := v.Show(Row{Name: DeskRow, Desk: true}, false); err != nil {
			t.Fatal(err)
		}
	}
	if f.ran("start-desk") != 1 || f.ran("switch-client -c /dev/ttys9 -t =docket-desk:desk") != 2 {
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
