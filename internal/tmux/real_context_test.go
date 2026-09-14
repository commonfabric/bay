package tmux

import (
	"errors"
	"testing"
)

// TestReal_CurrentAccessors_OutsideTmux pins the guard that keeps
// `bay recover` (and every other "where am I?" caller) honest outside
// tmux. `display-message -p` does not fail with no client context —
// it reports the server's most recently active session — so without
// the guard a caller outside tmux gets a confident wrong answer.
//
// currentTarget() cannot help here: there is no pane or session in the
// environment to anchor to, which is exactly why this is a separate
// check rather than a stricter anchor.
func TestReal_CurrentAccessors_OutsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	r := NewReal()

	if _, err := r.CurrentSession(); !errors.Is(err, ErrNotInTmux) {
		t.Errorf("CurrentSession err = %v, want ErrNotInTmux", err)
	}
	if _, err := r.CurrentWindowID(); !errors.Is(err, ErrNotInTmux) {
		t.Errorf("CurrentWindowID err = %v, want ErrNotInTmux", err)
	}
	if _, err := r.CurrentPaneID(); !errors.Is(err, ErrNotInTmux) {
		t.Errorf("CurrentPaneID err = %v, want ErrNotInTmux", err)
	}
}
