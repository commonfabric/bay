package docket

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Summary describes one item for a reader who wants to know where its work
// stands: its title, its work state and what it waits on, its open
// questions, and the State section of its record. The item's bays come
// from bay's manifest at manifestPath (see LoadItemsWithBays).
func Summary(stateDir, manifestPath, name string) (string, error) {
	items, err := LoadItemsWithBays(stateDir, manifestPath)
	if err != nil {
		return "", err
	}
	item, ok := items[name]
	if !ok {
		return "", fmt.Errorf("docket has no item named %q", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n", item.Name, item.Title)
	facts := []string{}
	for _, fact := range []string{item.Kind, item.Level, item.Work} {
		if fact != "" {
			facts = append(facts, fact)
		}
	}
	if refs := item.BayRefs(); len(refs) > 0 {
		facts = append(facts, "bays "+strings.Join(refs, ", "))
	}
	if len(item.Links) > 0 {
		facts = append(facts, "links "+strings.Join(item.Links, ", "))
	}
	fmt.Fprintf(&b, "%s\n", strings.Join(facts, " · "))
	for _, hold := range item.Holds {
		fmt.Fprintf(&b, "\nWaits on: %s\n", hold.On)
	}
	for _, q := range item.Questions {
		if q.Answered != nil && *q.Answered != "" {
			continue
		}
		mark := ""
		if q.BlocksAll {
			mark = " (holds up all work)"
		}
		fmt.Fprintf(&b, "\nQuestion %s%s: %s\n", q.ID, mark, q.Text)
	}
	state, err := recordSection(filepath.Join(stateDir, "items", name, "record.md"), "State")
	if err != nil {
		return "", err
	}
	if state == "" {
		state = "The record has no State section."
	}
	fmt.Fprintf(&b, "\nState\n\n%s\n", state)
	return b.String(), nil
}

// recordSection returns the text under the "## title" heading of a record
// file, up to the next "## " heading. A missing file or section gives "".
func recordSection(path, title string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var lines []string
	inside := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "## ") {
			inside = strings.TrimSpace(strings.TrimPrefix(line, "## ")) == title
			continue
		}
		if inside {
			lines = append(lines, line)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), nil
}
