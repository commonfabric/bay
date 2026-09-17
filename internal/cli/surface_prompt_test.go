package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
)

// --- agentClosePrompt unit tests ---

func TestAgentClosePrompt(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"y", "y\n", true},
		{"Y", "Y\n", true},
		{"yes", "yes\n", true},
		{"YES", "YES\n", true},
		{"yes with whitespace", "  yes  \n", true},
		{"n", "n\n", false},
		{"no", "no\n", false},
		{"empty (just enter)", "\n", false},
		{"EOF", "", false},
		{"random", "maybe\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			got := agentClosePrompt(strings.NewReader(c.input), &out, "claude")
			if got != c.want {
				t.Errorf("agentClosePrompt(%q) = %v, want %v", c.input, got, c.want)
			}
			// The prompt text should contain the surface name.
			if !strings.Contains(out.String(), "claude") {
				t.Errorf("prompt output should mention surface name: %q", out.String())
			}
		})
	}
}

// --- runSurfaceClose prompt wiring ---

func TestRunSurfaceClose_NonAgentSkipsPrompt(t *testing.T) {
	// Closing a non-agent surface should never call confirmAgentClose,
	// even with force=false. Verify directly by mocking the prompt to
	// fail the test if invoked.
	orig := confirmAgentClose
	defer func() { confirmAgentClose = orig }()
	confirmAgentClose = func(name string) bool {
		t.Fatalf("confirmAgentClose should not be called for a non-agent surface (got name=%q)", name)
		return true
	}

	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeShell, Name: "extra", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	if err := runSurfaceClose(eng, []string{"b1:extra"}, "", "", false); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	for _, s := range bay.Surfaces {
		if s.Name == "extra" {
			t.Errorf("extra shell surface should be closed (no prompt for non-agent)")
		}
	}
}

func TestRunSurfaceClose_DeclinedKeepsAgentSurface(t *testing.T) {
	// User declines the prompt → surface stays open and runSurfaceClose
	// returns nil (decline isn't an error).
	orig := confirmAgentClose
	defer func() { confirmAgentClose = orig }()
	confirmAgentClose = func(name string) bool { return false }

	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeAgent, Name: "claude", Agent: "claude", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	if err := runSurfaceClose(eng, []string{"b1:claude"}, "", "", false); err != nil {
		t.Fatalf("runSurfaceClose declined: expected nil error, got %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	foundAgent := false
	for _, s := range bay.Surfaces {
		if s.Name == "claude" {
			foundAgent = true
			break
		}
	}
	if !foundAgent {
		t.Error("agent surface should still exist after user declined the prompt")
	}
}

func TestRunSurfaceClose_ConfirmedClosesAgentSurface(t *testing.T) {
	// User confirms the prompt → surface gets closed normally.
	orig := confirmAgentClose
	defer func() { confirmAgentClose = orig }()
	confirmAgentClose = func(name string) bool { return true }

	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeAgent, Name: "claude", Agent: "claude", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	if err := runSurfaceClose(eng, []string{"b1:claude"}, "", "", false); err != nil {
		t.Fatalf("runSurfaceClose confirmed: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	for _, s := range bay.Surfaces {
		if s.Name == "claude" {
			t.Error("agent surface should be closed after user confirmed the prompt")
		}
	}
}

func TestRunSurfaceClose_ForceSkipsConfirmEntirely(t *testing.T) {
	// force=true should bypass confirmAgentClose entirely — if it's called,
	// fail the test.
	orig := confirmAgentClose
	defer func() { confirmAgentClose = orig }()
	confirmAgentClose = func(name string) bool {
		t.Fatalf("confirmAgentClose should not be called when force=true (got name=%q)", name)
		return true
	}

	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeAgent, Name: "claude", Agent: "claude", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	if err := runSurfaceClose(eng, []string{"b1:claude"}, "", "", true); err != nil {
		t.Fatalf("runSurfaceClose force: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	for _, s := range bay.Surfaces {
		if s.Name == "claude" {
			t.Error("agent surface should be closed when force=true")
		}
	}
}
