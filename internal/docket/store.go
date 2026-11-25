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
	"slices"
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
	Bay       string  `json:"bay"` // the bay the question was asked from, as "dock/bay"; "" when unknown
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
	return i.openQuestionsWhere(func(string) bool { return true })
}

// OpenQuestionsFrom counts the item's unanswered questions that were asked
// from the bay ref.
func (i Item) OpenQuestionsFrom(ref string) (count int, blocking bool) {
	return i.openQuestionsWhere(func(bay string) bool { return bay == ref })
}

// OpenQuestionsOutside counts the item's unanswered questions that were
// not asked from any of the bays in refs.
func (i Item) OpenQuestionsOutside(refs []string) (count int, blocking bool) {
	return i.openQuestionsWhere(func(bay string) bool { return !slices.Contains(refs, bay) })
}

func (i Item) openQuestionsWhere(match func(bay string) bool) (count int, blocking bool) {
	for _, q := range i.Questions {
		if (q.Answered == nil || *q.Answered == "") && match(q.Bay) {
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

// Children returns the items directly under parent in docket's order: the
// parent's own order list first, then by creation time and name. An empty
// parent names the top of the tree.
func Children(items map[string]Item, parent string) []Item {
	var kids []Item
	for _, item := range items {
		if (item.Parent == nil && parent == "") || (item.Parent != nil && *item.Parent == parent) {
			kids = append(kids, item)
		}
	}
	rank := map[string]int{}
	for k, name := range items[parent].Order {
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
