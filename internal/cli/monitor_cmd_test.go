package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/commonfabric/bay/internal/monitor"
)

func TestNewMonitorWithConfig_MissingConfigUsesDefaults(t *testing.T) {
	oldCfgPath := cfgPath
	t.Cleanup(func() {
		cfgPath = oldCfgPath
	})

	cfgPath = filepath.Join(t.TempDir(), "missing-config.toml")

	mon, err := newMonitorWithConfig()
	if err != nil {
		t.Fatalf("newMonitorWithConfig: %v", err)
	}
	if mon == nil {
		t.Fatal("expected monitor instance")
	}
}

func TestMonitorStatusJSONIncludesAssessedState(t *testing.T) {
	assessment := monitor.StatusAssessment{
		Running:   true,
		PID:       123,
		Freshness: monitor.StatusFreshnessStale,
		Reason:    monitor.StatusReasonVersionMismatch,
		Current: monitor.CurrentBayRuntime{
			Version:        "current (edcf5808)",
			BinaryIdentity: monitor.BinaryIdentity{Path: "/bin/bay", Size: 10},
		},
		Monitor: &monitor.RuntimeStatus{
			Version:        "old (11111111)",
			Generation:     2,
			BinaryIdentity: monitor.BinaryIdentity{Path: "/bin/bay", Size: 9},
		},
	}
	data, err := json.Marshal(assessment)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded["freshness"] != monitor.StatusFreshnessStale || decoded["reason"] != monitor.StatusReasonVersionMismatch {
		t.Fatalf("json decoded freshness/reason = %v/%v", decoded["freshness"], decoded["reason"])
	}
	mon, ok := decoded["monitor"].(map[string]any)
	if !ok {
		t.Fatalf("json missing monitor object: %v", decoded)
	}
	if mon["generation"] != float64(2) {
		t.Fatalf("json monitor generation = %v, want 2", mon["generation"])
	}
}

func TestMonitorStatusJSONIncludesEmptyPIDAndReason(t *testing.T) {
	assessment := monitor.StatusAssessment{
		Running:   false,
		PID:       0,
		Freshness: monitor.StatusFreshnessNotRunning,
		Reason:    "",
		Current: monitor.CurrentBayRuntime{
			Version:        "current (edcf5808)",
			BinaryIdentity: monitor.BinaryIdentity{Path: "/bin/bay", Size: 10},
		},
	}
	data, err := json.Marshal(assessment)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := decoded["pid"]; !ok {
		t.Fatalf("json missing pid field: %s", data)
	}
	if decoded["pid"] != float64(0) {
		t.Fatalf("json pid = %v, want 0", decoded["pid"])
	}
	if _, ok := decoded["reason"]; !ok {
		t.Fatalf("json missing reason field: %s", data)
	}
	if decoded["reason"] != "" {
		t.Fatalf("json reason = %v, want empty string", decoded["reason"])
	}
}

func TestFormatMonitorStatusOneLine(t *testing.T) {
	now := time.Now()
	current := monitor.CurrentBayRuntime{
		Version: "v0.0.0-20260506043208-edcf5808a4ed (edcf5808)",
		BinaryIdentity: monitor.BinaryIdentity{
			Path:          "/Users/mike/go/bin/bay",
			MTimeUnixNano: now.UnixNano(),
			Size:          10,
		},
	}

	tests := []struct {
		name       string
		assessment monitor.StatusAssessment
		wantPrefix string
	}{
		{
			name: "not running",
			assessment: monitor.StatusAssessment{
				Running:   false,
				Freshness: monitor.StatusFreshnessNotRunning,
				Current:   current,
			},
			wantPrefix: "Monitor not running",
		},
		{
			name: "current",
			assessment: monitor.StatusAssessment{
				Running:   true,
				PID:       123,
				Freshness: monitor.StatusFreshnessCurrent,
				Current:   current,
				Monitor: &monitor.RuntimeStatus{
					Version:        current.Version,
					LastSeenAt:     now,
					BinaryIdentity: current.BinaryIdentity,
				},
			},
			wantPrefix: "Monitor running (pid 123, current edcf5808, seen ",
		},
		{
			name: "current with stale heartbeat",
			assessment: monitor.StatusAssessment{
				Running:   true,
				PID:       123,
				Freshness: monitor.StatusFreshnessCurrent,
				Reason:    monitor.StatusReasonHeartbeatStale,
				Current:   current,
				Monitor: &monitor.RuntimeStatus{
					Version:        current.Version,
					LastSeenAt:     now.Add(-time.Hour),
					BinaryIdentity: current.BinaryIdentity,
				},
			},
			wantPrefix: "Monitor running (pid 123, current edcf5808, heartbeat stale: seen ",
		},
		{
			name: "stale version",
			assessment: monitor.StatusAssessment{
				Running:   true,
				PID:       123,
				Freshness: monitor.StatusFreshnessStale,
				Reason:    monitor.StatusReasonVersionMismatch,
				Current:   current,
				Monitor: &monitor.RuntimeStatus{
					Version: "old-version (11111111)",
				},
			},
			wantPrefix: "Monitor running (pid 123, stale: monitor 11111111, bay edcf5808)",
		},
		{
			name: "unknown missing metadata",
			assessment: monitor.StatusAssessment{
				Running:   true,
				PID:       123,
				Freshness: monitor.StatusFreshnessUnknown,
				Reason:    monitor.StatusReasonMetadataMissing,
				Current:   current,
			},
			wantPrefix: "Monitor running (pid 123, version unknown: metadata missing)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatMonitorStatusOneLine(tt.assessment)
			if !strings.HasPrefix(got, tt.wantPrefix) {
				t.Fatalf("formatMonitorStatusOneLine = %q, want prefix %q", got, tt.wantPrefix)
			}
		})
	}
}

func TestFormatMonitorStatusVerboseIncludesGeneration(t *testing.T) {
	assessment := monitor.StatusAssessment{
		Running:   true,
		PID:       123,
		Freshness: monitor.StatusFreshnessCurrent,
		Current: monitor.CurrentBayRuntime{
			Version:        "current (edcf5808)",
			BinaryIdentity: monitor.BinaryIdentity{Path: "/bin/bay", Size: 10},
		},
		Monitor: &monitor.RuntimeStatus{
			Version:        "current (edcf5808)",
			StartedAt:      time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC),
			LastSeenAt:     time.Date(2026, 5, 6, 12, 5, 0, 0, time.UTC),
			Generation:     3,
			BinaryIdentity: monitor.BinaryIdentity{Path: "/bin/bay", Size: 10},
		},
	}
	got := formatMonitorStatusVerbose(assessment)
	for _, want := range []string{
		"freshness: current",
		"monitor version: current (edcf5808)",
		"reload generation: 3",
		"current bay version: current (edcf5808)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("verbose status missing %q:\n%s", want, got)
		}
	}
}
