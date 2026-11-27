package docket

import (
	"github.com/commonfabric/bay/internal/manifest"
)

// BayLink is one bay that names the item it works on.
type BayLink struct {
	Ref  string // dock/bay
	Item string
	Role string // empty for the item's primary bay
}

// BayLinksFromManifest lists every bay in m that names an item, in
// manifest order.
func BayLinksFromManifest(m *manifest.Manifest) []BayLink {
	var links []BayLink
	for _, d := range m.Docks {
		for _, b := range d.Bays {
			if b.Item != "" {
				links = append(links, BayLink{Ref: d.Name + "/" + b.ID, Item: b.Item, Role: b.Role})
			}
		}
	}
	return links
}

// ApplyBayLinks replaces each item's bay sites with the bays that name the
// item, primary bay first. Sites of other kinds are kept. Bay holds the
// link between a bay and its item; the bay sites in docket's own records
// are not read.
func ApplyBayLinks(items map[string]Item, links []BayLink) {
	byItem := map[string][]Site{}
	for _, primary := range []bool{true, false} {
		for _, l := range links {
			if (l.Role == "") == primary {
				byItem[l.Item] = append(byItem[l.Item], Site{Kind: "bay", Ref: l.Ref})
			}
		}
	}
	for name, item := range items {
		sites := byItem[name]
		for _, s := range item.Sites {
			if s.Kind != "bay" {
				sites = append(sites, s)
			}
		}
		item.Sites = sites
		items[name] = item
	}
}

// LoadItemsWithBays loads docket's items and takes each item's bays from
// bay's manifest at manifestPath. When the manifest cannot be read, the
// items keep the bay sites docket recorded.
func LoadItemsWithBays(stateDir, manifestPath string) (map[string]Item, error) {
	items, err := LoadItems(stateDir)
	if err != nil {
		return nil, err
	}
	m, err := manifest.Load(manifestPath)
	if err != nil {
		return items, nil
	}
	ApplyBayLinks(items, BayLinksFromManifest(m))
	return items, nil
}
