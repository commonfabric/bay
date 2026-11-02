package cli

import (
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/engine"
)

func TestFormatPWD_OmitsMissingLevels(t *testing.T) {
	out := stripANSI(formatPWD(&engine.Context{
		Dock: "api",
		Bay:  "auth-fix",
	}))

	if !strings.Contains(out, "dock api") || !strings.Contains(out, "bay auth-fix") {
		t.Fatalf("unexpected pwd output: %q", out)
	}
	if strings.Contains(out, "surface") {
		t.Fatalf("pwd output should omit missing surface: %q", out)
	}
}

func TestFormatPWD_IncludesSurface(t *testing.T) {
	out := stripANSI(formatPWD(&engine.Context{
		Dock:      "api",
		Bay:       "auth-fix",
		Surface:   "agent",
		SurfaceID: 2,
	}))

	for _, want := range []string{"dock api", "bay auth-fix", "surface agent"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pwd output missing %q: %q", want, out)
		}
	}
}

func TestFormatPWD_HomeShowsBayAndSurface(t *testing.T) {
	out := stripANSI(formatPWD(&engine.Context{
		Dock:    "labs",
		BayID:   "home",
		Bay:     "home",
		Surface: "shell",
		Path:    "/repo/labs",
	}))

	for _, want := range []string{"dock labs", "bay home", "surface shell"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pwd output missing %q: %q", want, out)
		}
	}
}

// TestFormatPWD_UnnamedBayFallsBackToID confirms pwd never renders a
// blank bay reference for an unnamed bay — it shows the canonical label
// (the stable ID).
func TestFormatPWD_UnnamedBayFallsBackToID(t *testing.T) {
	out := stripANSI(formatPWD(&engine.Context{
		Dock:  "api",
		BayID: "b2",
		Bay:   "",
		Path:  "/wt/b2",
	}))
	if !strings.Contains(out, "bay b2") {
		t.Fatalf("unnamed bay should render 'bay b2', got %q", out)
	}
}

// TestFormatPWD_NamedBayUsesDottedLabel confirms named bays render the
// canonical "<id>.<name>" label so pwd matches the picker.
func TestFormatPWD_NamedBayUsesDottedLabel(t *testing.T) {
	out := stripANSI(formatPWD(&engine.Context{
		Dock:  "api",
		BayID: "b1",
		Bay:   "auth-fix",
		Path:  "/wt/b1",
	}))
	if !strings.Contains(out, "bay b1.auth-fix") {
		t.Fatalf("named bay should render 'bay b1.auth-fix', got %q", out)
	}
}
