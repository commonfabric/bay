package engine

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/manifest"
)

func newItemBay(t *testing.T, eng *Engine, item, role string) *manifest.Bay {
	t.Helper()
	bay, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true, Item: item, Role: role})
	if err != nil {
		t.Fatalf("BayNew(item=%q role=%q): %v", item, role, err)
	}
	return bay
}

func TestBayNew_ItemAndRole(t *testing.T) {
	eng, _ := testEngine(t)
	primary := newItemBay(t, eng, "docket", "")
	helper := newItemBay(t, eng, "docket", "helper")

	item, role, err := eng.BayItem("labs", primary.ID)
	if err != nil || item != "docket" || role != "" {
		t.Fatalf("primary = %q/%q, %v", item, role, err)
	}
	item, role, err = eng.BayItem("labs", helper.ID)
	if err != nil || item != "docket" || role != "helper" {
		t.Fatalf("helper = %q/%q, %v", item, role, err)
	}
}

func TestBayNew_RefusesSecondPrimary(t *testing.T) {
	eng, _ := testEngine(t)
	newItemBay(t, eng, "docket", "")
	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true, Item: "docket"}); err == nil || !strings.Contains(err.Error(), "already has a primary bay") {
		t.Fatalf("want primary refusal, got %v", err)
	}
	m, _ := eng.LoadManifest()
	if got := len(m.FindDock("labs").Bays); got != 1 {
		t.Fatalf("refused bay was created: %d bays", got)
	}
}

func TestBayNew_RejectsBadNames(t *testing.T) {
	eng, _ := testEngine(t)
	for _, c := range []struct{ item, role string }{
		{"Docket", ""}, {"a b", ""}, {"", "helper"}, {"docket", "Bad Role"},
	} {
		if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true, Item: c.item, Role: c.role}); err == nil {
			t.Errorf("item=%q role=%q: want error", c.item, c.role)
		}
	}
}

func TestBaySetItem_ReplaceAndClear(t *testing.T) {
	eng, _ := testEngine(t)
	b := newItemBay(t, eng, "docket", "review")

	if err := eng.BaySetItem("labs", b.ID, "other", nil, false); err == nil {
		t.Fatal("changing the item without replace should fail")
	}
	if err := eng.BaySetItem("labs", b.ID, "docket", nil, false); err != nil {
		t.Fatalf("setting the same item: %v", err)
	}
	if err := eng.BaySetItem("labs", b.ID, "other", nil, true); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if item, role, _ := eng.BayItem("labs", b.ID); item != "other" || role != "" {
		t.Fatalf("after replace = %q/%q, want other with the role cleared", item, role)
	}
	if err := eng.BayClearItem("labs", b.ID); err != nil {
		t.Fatal(err)
	}
	if item, role, _ := eng.BayItem("labs", b.ID); item != "" || role != "" {
		t.Fatalf("after clear = %q/%q", item, role)
	}
}

func TestBaySetItem_RefusesSecondPrimary(t *testing.T) {
	eng, _ := testEngine(t)
	newItemBay(t, eng, "docket", "")
	b := newItemBay(t, eng, "", "")

	if err := eng.BaySetItem("labs", b.ID, "docket", nil, false); err == nil {
		t.Fatal("second primary should be refused")
	}
	role := "helper"
	if err := eng.BaySetItem("labs", b.ID, "docket", &role, false); err != nil {
		t.Fatalf("with a role: %v", err)
	}
	if err := eng.BaySetRole("labs", b.ID, ""); err == nil {
		t.Fatal("clearing the role of a second bay should be refused")
	}
}

func TestBaySetRole_NeedsItem(t *testing.T) {
	eng, _ := testEngine(t)
	b := newItemBay(t, eng, "", "")
	if err := eng.BaySetRole("labs", b.ID, "review"); err == nil {
		t.Fatal("role without item should fail")
	}
}

func TestPrimaryIsPerItemAcrossDocks(t *testing.T) {
	m := &manifest.Manifest{Docks: []manifest.Dock{
		{Name: "a", Bays: []manifest.Bay{{ID: "b1", Item: "x"}}},
		{Name: "b", Bays: []manifest.Bay{{ID: "b1", Item: "y"}}},
	}}
	if err := checkPrimaryFree(m, "x", "", "b", "b1"); err == nil {
		t.Error("primary of x lives in dock a; a second one in dock b must be refused")
	}
	if err := checkPrimaryFree(m, "x", "", "a", "b1"); err != nil {
		t.Errorf("the bay itself is not a conflict: %v", err)
	}
}

// The manifest keeps item and role across a save and load, and a bay
// without them writes neither key.
func TestManifest_ItemRoleRoundTrip(t *testing.T) {
	eng, dir := testEngine(t)
	newItemBay(t, eng, "docket", "")
	newItemBay(t, eng, "", "")
	raw, err := os.ReadFile(dir + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int
		Docks   []struct{ Bays []map[string]any }
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != manifest.CurrentVersion {
		t.Errorf("version = %d, want %d", doc.Version, manifest.CurrentVersion)
	}
	bays := doc.Docks[0].Bays
	if bays[0]["item"] != "docket" {
		t.Errorf("first bay item = %v", bays[0]["item"])
	}
	if _, ok := bays[0]["role"]; ok {
		t.Error("primary bay should have no role key")
	}
	if _, ok := bays[1]["item"]; ok {
		t.Error("bay without an item should have no item key")
	}
}
