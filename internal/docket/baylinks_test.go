package docket

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/manifest/manifesttest"
)

func TestApplyBayLinks_BayOwnsTheLink(t *testing.T) {
	items := map[string]Item{
		"restart": {Name: "restart", Sites: []Site{{"bay", "loom/b7"}, {"url", "https://x"}}},
		"images":  {Name: "images"},
		"other":   {Name: "other", Sites: []Site{{"bay", "loom/b9"}}},
	}
	ApplyBayLinks(items, []BayLink{
		{Ref: "loom/b3", Item: "restart", Role: "helper"},
		{Ref: "loom/b4", Item: "restart"},
		{Ref: "labs/b1", Item: "images"},
	})
	want := map[string][]Site{
		// Primary first, then helpers; docket's own bay site (loom/b7) is not read.
		"restart": {{"bay", "loom/b4"}, {"bay", "loom/b3"}, {"url", "https://x"}},
		"images":  {{"bay", "labs/b1"}},
		"other":   nil,
	}
	for name, sites := range want {
		if got := items[name].Sites; !reflect.DeepEqual(got, sites) {
			t.Errorf("%s sites = %v, want %v", name, got, sites)
		}
	}
}

func TestLoadItemsWithBays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "items", "restart")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	item := `{"name":"restart","title":"R","kind":"goal","status":"open","sites":[{"kind":"bay","ref":"loom/b7"}]}`
	if err := os.WriteFile(filepath.Join(path, "item.json"), []byte(item), 0o644); err != nil {
		t.Fatal(err)
	}

	// No manifest: docket's own bay sites are kept.
	items, err := LoadItemsWithBays(dir, filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := items["restart"].BayRefs(); !reflect.DeepEqual(got, []string{"loom/b7"}) {
		t.Errorf("without a manifest: %v", got)
	}

	// With a manifest: bay's links replace them.
	mpath := filepath.Join(dir, "manifest.json")
	if err := manifesttest.Save(mpath, &manifest.Manifest{Version: manifest.CurrentVersion, Docks: []manifest.Dock{
		{Name: "loom", Bays: []manifest.Bay{{ID: "b2", Item: "restart"}, {ID: "b7"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	items, err = LoadItemsWithBays(dir, mpath)
	if err != nil {
		t.Fatal(err)
	}
	if got := items["restart"].BayRefs(); !reflect.DeepEqual(got, []string{"loom/b2"}) {
		t.Errorf("with a manifest: %v", got)
	}
}

func TestApplyBayLinks_ABayNamingAnOldNameIsABayOfTheItem(t *testing.T) {
	items := map[string]Item{
		"survive": {Name: "survive", Aliases: []string{"restart"}},
	}
	ApplyBayLinks(items, []BayLink{
		{Ref: "loom/b3", Item: "restart", Role: "review"},
		{Ref: "loom/b4", Item: "survive"},
	})
	want := []Site{{"bay", "loom/b4"}, {"bay", "loom/b3"}}
	if got := items["survive"].Sites; !reflect.DeepEqual(got, want) {
		t.Errorf("sites = %v, want %v", got, want)
	}
}
