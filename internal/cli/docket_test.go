package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/docket"
)

func TestFitPopupKeepsAPopupInsideTheTerminal(t *testing.T) {
	for _, c := range []struct{ want, room, got int }{{40, 60, 40}, {58, 60, 58}, {59, 60, 58}, {75, 60, 58}, {75, 0, 75}} {
		if got := fitPopup(c.want, c.room); got != c.got {
			t.Errorf("fitPopup(%d, %d) = %d, want %d", c.want, c.room, got, c.got)
		}
	}
}

func TestSavedWidthIsReadBackAndACollapsedOneIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bay", "docket-view-width.json")
	if got := loadWidth(path, ""); got != 0 {
		t.Errorf("no saved width: got %d, want 0", got)
	}
	if err := saveWidth(path, "", 52); err != nil {
		t.Fatal(err)
	}
	if got := loadWidth(path, ""); got != 52 {
		t.Errorf("saved 52: got %d", got)
	}
	if err := saveWidth(path, "", 2); err != nil {
		t.Fatal(err)
	}
	if got := loadWidth(path, ""); got != 0 {
		t.Errorf("saved a collapsed width: got %d, want 0", got)
	}
}

func TestAWidthInDocketsFolderIsReadUntilTheNewFileIsSaved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bay", "docket-view-width.json")
	old := filepath.Join(dir, "view-width.json")
	if err := os.WriteFile(old, []byte(`{"width":44}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadWidth(path, old); got != 44 {
		t.Errorf("old file only: got %d, want 44", got)
	}
	if err := saveWidth(path, old, 50); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old file still there after a save: %v", err)
	}
	if got := loadWidth(path, old); got != 50 {
		t.Errorf("after save: got %d, want 50", got)
	}
}

func TestTheListKeepsItsWidthWhenTheTerminalChangesSize(t *testing.T) {
	// The view starts detached at 80 columns with the saved width 40.
	w := listWidth{drawn: 40, saved: 40, window: 80}
	// Attaching a terminal 200 columns wide makes tmux widen the list.
	if restore, save := w.changed(98, 200, false); restore != 40 || save {
		t.Errorf("attach: restore %d, save %v", restore, save)
	}
	// The list is given back its width, which is not saved again.
	if restore, save := w.changed(40, 200, false); restore != 0 || !save || w.saved != 40 {
		t.Errorf("after attach: restore %d, save %v, saved %d", restore, save, w.saved)
	}
	// Dragging the border is a width the person chose.
	if restore, save := w.changed(50, 200, false); restore != 0 || !save || w.saved != 50 {
		t.Errorf("drag: restore %d, save %v, saved %d", restore, save, w.saved)
	}
	// A zoomed pane or a collapsed list changes nothing.
	if restore, save := w.changed(200, 200, true); restore != 0 || save || w.saved != 50 {
		t.Errorf("zoom: restore %d, save %v, saved %d", restore, save, w.saved)
	}
}

func TestTmuxTextShowsAHashAsItIs(t *testing.T) {
	if got := tmuxText("bay#355 #{session_name}"); got != "bay##355 ##{session_name}" {
		t.Errorf("tmuxText = %q", got)
	}
}

func TestTheShownRowIsSavedAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bay", "docket-view-row.json")
	if got := loadShownRow(path); got != "" {
		t.Errorf("nothing saved: %q", got)
	}
	if err := saveShownRow(path, "\x00docket"); err != nil {
		t.Fatal(err)
	}
	if got := loadShownRow(path); got != "\x00docket" {
		t.Errorf("read back %q", got)
	}
}

func TestMarksAndHotlistMoveIntoTheRootItemsFolder(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("view-marks.json", `{"a":"docket","d":"the desk"}`)
	write("view-hotlist.json", `["docket","bay-tool"]`)
	marks := loadMarks(dir)
	if len(marks) != 2 || marks["a"] != "docket" || marks["d"] != docket.DeskRow {
		t.Errorf("old marks read as %v", marks)
	}
	if got := loadHotlist(dir); len(got) != 2 || got[1] != "bay-tool" {
		t.Errorf("old hotlist read as %v", got)
	}
	marks["b"] = "bay-tool"
	if err := saveMarks(dir, marks); err != nil {
		t.Fatal(err)
	}
	if err := saveHotlist(dir, []string{"bay-tool"}); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"view-marks.json", "view-hotlist.json"} {
		if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
			t.Errorf("%s still there after a save: %v", old, err)
		}
	}
	var saved map[string]string
	data, err := os.ReadFile(filepath.Join(dir, "items", "_root", "marks.json"))
	if err != nil || json.Unmarshal(data, &saved) != nil || saved["d"] != "_root" || saved["b"] != "bay-tool" {
		t.Errorf("marks.json = %s, %v", data, err)
	}
	if got := loadHotlist(dir); len(got) != 1 || got[0] != "bay-tool" {
		t.Errorf("hotlist after save = %v", got)
	}
}

func TestViewBayKeysAreAllOnesTheViewKnowsHowToBind(t *testing.T) {
	for _, k := range viewBayKeys() {
		if !k.Tmux && k.Verb != "run-shell" && !strings.HasPrefix(k.Verb, "display-popup") {
			t.Errorf("key %s %s runs %q through %q, which the view does not bind", k.Table, k.Key, k.Command, k.Verb)
		}
	}
}
