// Package docket shows the items of a docket work list beside the tmux
// windows where the work on them happens.
//
// Docket keeps one directory per item under its state directory, each
// holding an item.json. This package reads those files and never writes
// them.
package docket

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Item is the part of a docket item that the sidebar shows.
type Item struct {
	Name      string     `json:"name"`
	Title     string     `json:"title"`
	Kind      string     `json:"kind"` // "area" or "goal"
	Parent    *string    `json:"parent"`
	Order     []string   `json:"order"`
	Status    string     `json:"status"` // "open" or "closed"
	Level     string     `json:"level"`  // "front", "back" or "shelved"
	Work      string     `json:"work"`   // "running", "ready" or "held"
	Created   string     `json:"created"`
	Questions []Question `json:"questions"`
	Sites     []Site     `json:"sites"`
	Holds     []Hold     `json:"holds"`
	Links     []string   `json:"links"`
}

// Hold is one thing an item's work waits on.
type Hold struct {
	On string `json:"on"`
}

// Question is one question an item has for its owner.
type Question struct {
	ID        string  `json:"id"`
	Text      string  `json:"text"`
	Answered  *string `json:"answered"`
	BlocksAll bool    `json:"blocks_all"`
}

// Site is a place where work on an item happens. A site of kind "bay" has
// a ref of the form "dock/bay", such as "loom/b19".
type Site struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// OpenQuestions counts the item's unanswered questions and reports whether
// any of them holds up all work on the item.
func (i Item) OpenQuestions() (count int, blocking bool) {
	for _, q := range i.Questions {
		if q.Answered == nil || *q.Answered == "" {
			count++
			blocking = blocking || q.BlocksAll
		}
	}
	return count, blocking
}

// BayRefs lists the refs of the item's bay sites.
func (i Item) BayRefs() []string {
	var refs []string
	for _, s := range i.Sites {
		if s.Kind == "bay" {
			refs = append(refs, s.Ref)
		}
	}
	return refs
}

// StateDir is docket's state directory: $DOCKET_STATE, or
// ~/.local/share/docket.
func StateDir() (string, error) {
	if dir := os.Getenv("DOCKET_STATE"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "docket"), nil
}

// LoadItems reads every item under stateDir. A state directory with no
// items directory holds no items. An item file that cannot be read or
// parsed is an error.
func LoadItems(stateDir string) (map[string]Item, error) {
	entries, err := os.ReadDir(filepath.Join(stateDir, "items"))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Item{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := make(map[string]Item, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(stateDir, "items", e.Name(), "item.json")
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue // docket does not count a folder without item.json as an item
		}
		if err != nil {
			return nil, err
		}
		var item Item
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		items[item.Name] = item
	}
	return items, nil
}

// RootItem is the name of the root of the tree. Docket stores the root as
// an item: its order is the order of the items at the top of the tree, and
// its questions are about the whole tree. It is not a row of the tree.
const RootItem = "_root"

// LoadRootOrder reads root.json, where docket keeps the order of the items
// at the top of the tree in a state directory that has no root item. A
// missing file gives no order.
func LoadRootOrder(stateDir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "root.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var root struct {
		Order []string `json:"order"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("root.json: %w", err)
	}
	return root.Order, nil
}

// Children returns the items directly under parent in docket's order: the
// parent's own order list first, then by creation time and name. An empty
// parent names the top of the tree, whose order is the root item's, or
// rootOrder when there is no root item. The root item is no one's child.
func Children(items map[string]Item, rootOrder []string, parent string) []Item {
	var kids []Item
	for _, item := range items {
		if item.Name == RootItem {
			continue
		}
		if (item.Parent == nil && parent == "") || (item.Parent != nil && *item.Parent == parent) {
			kids = append(kids, item)
		}
	}
	order := items[parent].Order
	if root, ok := items[RootItem]; parent == "" && ok {
		order = root.Order
	} else if parent == "" {
		order = rootOrder
	}
	rank := map[string]int{}
	for k, name := range order {
		rank[name] = k
	}
	pos := func(i Item) int {
		if r, ok := rank[i.Name]; ok {
			return r
		}
		return len(rank)
	}
	sort.Slice(kids, func(a, b int) bool {
		if pa, pb := pos(kids[a]), pos(kids[b]); pa != pb {
			return pa < pb
		}
		if kids[a].Created != kids[b].Created {
			return kids[a].Created < kids[b].Created
		}
		return kids[a].Name < kids[b].Name
	})
	return kids
}
