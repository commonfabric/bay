package cli

import (
	"bytes"
	"testing"
)

func TestRunState_SetReadClear(t *testing.T) {
	eng := itemFixture(t)
	var out bytes.Buffer
	if err := runState(eng, &out, []string{"b1", "pr"}, "labs", false); err != nil {
		t.Fatal(err)
	}
	if err := runState(eng, &out, []string{"b1"}, "labs", false); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "🦚 pr\n" {
		t.Fatalf("read = %q", got)
	}
	// A mark sets the state its word names.
	if err := runState(eng, &out, []string{"b1", "🫖"}, "labs", false); err != nil {
		t.Fatal(err)
	}
	bay, err := eng.BayShow("labs", "b1")
	if err != nil || bay.State != "done" {
		t.Fatalf("state = %q, %v", bay.State, err)
	}
	if err := runState(eng, &out, []string{"b1", "napping"}, "labs", false); err == nil {
		t.Error("an unknown state should be refused")
	}
	if err := runState(eng, &out, []string{"b1", "pr"}, "labs", true); err == nil {
		t.Error("--clear with a state should be refused")
	}
	if err := runState(eng, &out, []string{"b1"}, "labs", true); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runState(eng, &out, []string{"b1"}, "labs", false); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("cleared state reads %q", out.String())
	}
}
