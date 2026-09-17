package monitor

import (
	"testing"

	"github.com/commonfabric/bay/internal/manifest"
)

func TestSelectDescribeCandidates(t *testing.T) {
	const now = 1_000_000

	mf := &manifest.Manifest{Docks: []manifest.Dock{{
		Name: "labs",
		Bays: []manifest.Bay{
			// Eligible: empty description, recently active.
			{ID: "b1", Type: manifest.BayTypeWorktree, Path: "/p1", LastActive: now - 60},
			// Eligible: auto description, due for refresh (old summary).
			{ID: "b2", Type: manifest.BayTypeWorktree, Path: "/p2", LastActive: now - 60,
				Description: "auto desc", DescriptionSource: manifest.DescriptionSourceAuto, DescriptionSummarizedAt: now - 3600},
			// Skip: user-set description.
			{ID: "b3", Type: manifest.BayTypeWorktree, Path: "/p3", LastActive: now - 60,
				Description: "mine", DescriptionSource: manifest.DescriptionSourceUser},
			// Skip: legacy (non-empty, empty source).
			{ID: "b4", Type: manifest.BayTypeWorktree, Path: "/p4", LastActive: now - 60, Description: "legacy"},
			// Eligible backlog: never described, even though not recently active.
			{ID: "b5", Type: manifest.BayTypeWorktree, Path: "/p5", LastActive: now - activityWindow - 1},
			// Skip: inactive and already probed without finding any signal.
			{ID: "quiet", Type: manifest.BayTypeWorktree, Path: "/quiet",
				LastActive: now - activityWindow - 1, DescriptionSummarizedAt: now - 3600,
				DescriptionStableStreak: 1},
			// Eligible backlog: an earlier summarizer failure leaves streak zero.
			{ID: "failed", Type: manifest.BayTypeWorktree, Path: "/failed",
				LastActive: now - activityWindow - 1, DescriptionSummarizedAt: now - 3600},
			// Skip: summarized within the min interval.
			{ID: "b6", Type: manifest.BayTypeWorktree, Path: "/p6", LastActive: now - 60,
				DescriptionSource: manifest.DescriptionSourceAuto, DescriptionSummarizedAt: now - 10},
			// Skip: home pseudo-bay.
			{ID: manifest.HomeBayID, Type: manifest.BayTypeHome, Path: "/home", LastActive: now - 60},
			// Skip: no path.
			{ID: "b7", Type: manifest.BayTypeWorktree, LastActive: now - 60},
		},
	}}}

	got := selectDescribeCandidates(mf, now, describeMaxPerCycle)
	if len(got) != 4 {
		t.Fatalf("got %d candidates, want 4: %+v", len(got), got)
	}
	ids := []string{got[0].bay, got[1].bay, got[2].bay, got[3].bay}
	if ids[0] != "b1" || ids[1] != "b5" || ids[2] != "b2" || ids[3] != "failed" {
		t.Errorf("candidate order = %v, want [b1 b5 b2 failed]", ids)
	}
}

func TestSelectDescribeCandidatesBackoff(t *testing.T) {
	const now = 1_000_000
	auto := manifest.DescriptionSourceAuto

	mk := func(id string, lastActive, summarizedAt int64, streak int) manifest.Bay {
		return manifest.Bay{
			ID: id, Type: manifest.BayTypeWorktree, Path: "/" + id,
			Description: "auto", DescriptionSource: auto,
			LastActive: lastActive, DescriptionSummarizedAt: summarizedAt,
			DescriptionStableStreak: streak,
		}
	}

	mf := &manifest.Manifest{Docks: []manifest.Dock{{Name: "labs", Bays: []manifest.Bay{
		// Agent-only (no bay command since the last run) and quiet: the base
		// interval (600) would make it due, but backoff(3)=3600 holds it.
		mk("backedoff", now-4000, now-3000, 3),
		// Agent-only and quiet, but now past the backed-off interval → due.
		mk("dueAgent", now-5000, now-4000, 3),
		// User touched it since the last run → base cadence, past the floor,
		// so it refreshes despite the streak that would otherwise back it off.
		mk("userActive", now-100, now-1000, 3),
		// User touched it but still within the base floor → not yet due.
		mk("userFloor", now-100, now-300, 3),
	}}}}

	got := map[string]bool{}
	for _, c := range selectDescribeCandidates(mf, now, describeMaxPerCycle) {
		got[c.bay] = true
	}
	if got["backedoff"] {
		t.Error("backedoff: quiet agent-only bay should be suppressed by backoff")
	}
	if !got["dueAgent"] {
		t.Error("dueAgent: bay past its backed-off interval should be a candidate")
	}
	if !got["userActive"] {
		t.Error("userActive: bay touched since last run should refresh at base cadence")
	}
	if got["userFloor"] {
		t.Error("userFloor: bay within the base floor should not be a candidate")
	}
}

func TestDescribeBackoff(t *testing.T) {
	cases := []struct {
		streak int
		want   int64
	}{
		{0, describeMinInterval},     // just changed → base cadence
		{1, 2 * describeMinInterval}, // doubles per quiet run
		{2, 4 * describeMinInterval},
		{3, describeMaxInterval},  // 8*base would exceed the cap
		{50, describeMaxInterval}, // saturates, never overflows
	}
	for _, c := range cases {
		if got := describeBackoff(c.streak); got != c.want {
			t.Errorf("describeBackoff(%d) = %d, want %d", c.streak, got, c.want)
		}
	}
}

func TestSelectDescribeCandidatesCap(t *testing.T) {
	const now = 1_000_000
	var bays []manifest.Bay
	for _, id := range []string{"b1", "b2", "b3", "b4", "b5"} {
		bays = append(bays, manifest.Bay{ID: id, Type: manifest.BayTypeWorktree, Path: "/" + id, LastActive: now - 60})
	}
	mf := &manifest.Manifest{Docks: []manifest.Dock{{Name: "labs", Bays: bays}}}

	if got := selectDescribeCandidates(mf, now, 3); len(got) != 3 {
		t.Errorf("got %d candidates, want cap of 3", len(got))
	}
}
