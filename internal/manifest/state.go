package manifest

// StateMark is one lifecycle state a bay's session can set: the word
// stored in the bay's record, the mark lists draw for it, and what it
// means.
type StateMark struct {
	Word    string
	Mark    string
	Meaning string
}

// StateMarks lists the lifecycle states, in the order the work usually
// passes through them.
var StateMarks = []StateMark{
	{"building", "🧱", "building"},
	{"answering", "💬", "answering a question"},
	{"judging", "🐤", "waiting for the person to judge the result"},
	{"trying", "✋", "waiting for the person to try something by hand"},
	{"stacked", "📚", "stacked on unmerged work"},
	{"pr", "🦚", "looking after an open PR"},
	{"ci", "👀", "watching CI"},
	{"merged", "🪻", "merged and doing follow-ups"},
	{"main", "🚨", "fixing a broken main branch"},
	{"done", "🫖", "nothing left to do; the bay can be closed"},
}

// ParseState returns the stored word for a state given as its word or its
// mark, and false when s is neither.
func ParseState(s string) (string, bool) {
	for _, m := range StateMarks {
		if s == m.Word || s == m.Mark {
			return m.Word, true
		}
	}
	return "", false
}

// StateMarkOf returns the mark drawn for a stored state word, or "" for
// an empty or unknown word.
func StateMarkOf(word string) string {
	for _, m := range StateMarks {
		if word == m.Word {
			return m.Mark
		}
	}
	return ""
}
