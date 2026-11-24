package cli

import "testing"

func TestFitPopupKeepsAPopupInsideTheTerminal(t *testing.T) {
	for _, c := range []struct{ want, room, got int }{{40, 60, 40}, {58, 60, 58}, {59, 60, 58}, {75, 60, 58}, {75, 0, 75}} {
		if got := fitPopup(c.want, c.room); got != c.got {
			t.Errorf("fitPopup(%d, %d) = %d, want %d", c.want, c.room, got, c.got)
		}
	}
}
