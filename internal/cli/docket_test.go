package cli

import "testing"

func TestFitPopupKeepsAPopupInsideTheTerminal(t *testing.T) {
	for _, c := range []struct{ want, room, got int }{{40, 60, 40}, {58, 60, 58}, {59, 60, 58}, {75, 60, 58}, {75, 0, 75}} {
		if got := fitPopup(c.want, c.room); got != c.got {
			t.Errorf("fitPopup(%d, %d) = %d, want %d", c.want, c.room, got, c.got)
		}
	}
}

func TestSavedWidthIsReadBackAndACollapsedOneIsIgnored(t *testing.T) {
	dir := t.TempDir()
	if got := loadWidth(dir); got != 0 {
		t.Errorf("no saved width: got %d, want 0", got)
	}
	if err := saveWidth(dir, 52); err != nil {
		t.Fatal(err)
	}
	if got := loadWidth(dir); got != 52 {
		t.Errorf("saved 52: got %d", got)
	}
	if err := saveWidth(dir, 2); err != nil {
		t.Fatal(err)
	}
	if got := loadWidth(dir); got != 0 {
		t.Errorf("saved a collapsed width: got %d, want 0", got)
	}
}
