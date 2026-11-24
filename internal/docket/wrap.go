package docket

import "strings"

// Wrap breaks text into lines of at most width columns at spaces. A word
// longer than width gets a line of its own. Line breaks in the text are
// kept.
func Wrap(text string, width int) []string {
	var out []string
	for _, paragraph := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		line := ""
		for _, word := range strings.Fields(paragraph) {
			switch {
			case line == "":
				line = word
			case len([]rune(line))+1+len([]rune(word)) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}
