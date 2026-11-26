package cli

import (
	"os"
	"path/filepath"
	"testing"
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
