// Package palette implements a VS Code-style command palette for bay.
//
// The palette is rendered inside a tmux display-popup via `bay palette`. It
// hosts a fuzzy-filterable list of every command that has no dedicated
// keybinding — or every command at all, when the user is curious. Commands
// that have a direct tmux hotkey show the hotkey in a right-aligned column,
// so the palette doubles as self-documentation.
//
// The palette is built on internal/picker but needs richer rendering than
// Run provides (sections, recents dedupe, live hotkey annotations, mode
// toggle), so it has its own read-loop.
package palette

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/commonfabric/bay/internal/picker"
	"golang.org/x/term"
)

// Mode controls how "create surface" entries launch — a new tmux window
// (ModeWindow) or a split of the current window (ModePane). Mode is a
// no-op for every non-create entry, but the footer shows it for consistency.
type Mode int

const (
	ModeWindow Mode = iota
	ModePane
)

func (m Mode) String() string {
	if m == ModePane {
		return "pane"
	}
	return "window"
}

// Scope is where the palette was invoked from. Entries whose Needs is
// stricter than the current scope are hidden entirely — simpler than
// greyed-out and keeps the palette short.
type Scope int

const (
	ScopeAnywhere Scope = iota
	ScopeInDock
	ScopeInBay
)

// satisfies reports whether `current` is at least as specific as `needed`.
// InBay satisfies InDock and Anywhere; InDock satisfies Anywhere.
func (current Scope) satisfies(needed Scope) bool {
	return current >= needed
}

// Section groups palette entries under a header. Order here is render order.
type Section int

const (
	SectionNavigation Section = iota
	SectionCreateSurface
	SectionCreateBay
	SectionCurrentBay
	SectionCurrentSurface
	SectionAdmin
)

// Title is the header text printed above the section.
func (s Section) Title() string {
	switch s {
	case SectionNavigation:
		return "Navigation"
	case SectionCreateSurface:
		return "Create — surface"
	case SectionCreateBay:
		return "Create — bay"
	case SectionCurrentBay:
		return "Current bay"
	case SectionCurrentSurface:
		return "Current surface"
	case SectionAdmin:
		return "Admin"
	}
	return ""
}

// Entry is one palette command. The cli layer constructs these — the
// palette package itself has no knowledge of specific commands.
type Entry struct {
	// ID is the stable recents key. Keep it stable across refactors: if it
	// changes, every user's existing recent for this entry becomes stale
	// and is silently dropped on read.
	ID string

	// Title is the primary display string.
	Title string

	// Section groups this entry under its section header.
	Section Section

	// Needs is the minimum scope required for this entry to be visible.
	Needs Scope

	// Hotkey is rendered right-aligned. Empty leaves the column blank.
	Hotkey string

	// Action runs the entry. Return (param, nil) on success; param binds
	// the chosen value for recents storage (used for sub-picker
	// parametrics — "codex" for "New agent (codex)"). Return ("", nil)
	// for non-parametric or text-input entries.
	Action func() (param string, err error)

	// ActionWithParam runs a recent row that already has a param bound.
	// If nil, bound recent rows fall back to Action.
	ActionWithParam func(boundParam string) (param string, err error)

	// ParamValid returns whether a stored param still resolves to a live
	// command. Nil accepts all params.
	ParamValid func(param string) bool

	// TitleWithParam formats the title when a recents entry has a param
	// bound. Nil falls back to Title. Used for display only; the main
	// categorical list always uses Title.
	TitleWithParam func(param string) string
}

// displayTitle returns the title to show for this entry, using param when
// TitleWithParam is set.
func (e *Entry) displayTitle(param string) string {
	if param != "" && e.TitleWithParam != nil {
		return e.TitleWithParam(param)
	}
	return e.Title
}

// RunOptions bundles what Run needs to display and dispatch the palette.
type RunOptions struct {
	// Scope is the current bay scope (affects which Needs-gated entries
	// are visible).
	Scope Scope

	// Mode is the current mode. Tab flips it.
	Mode Mode

	// Entries is called to build the current palette. It is called once at
	// start and again on every Tab press — Hotkeys and Titles can vary
	// with Mode, and the cli layer rebuilds with fresh Action closures.
	Entries func(Mode) []Entry

	// Recents, if non-nil, is consulted for the top-of-list recents
	// section and updated on successful action completion.
	Recents *Recents

	// Width bounds the hotkey column's right-alignment. If zero, a
	// reasonable default is used. Set to the popup width to keep the
	// hotkey column anchored near the right edge regardless of popup
	// size.
	Width int

	In  *os.File
	Out *os.File
}

// Run displays the palette, dispatches the selected entry, and records the
// result in Recents. Returns any error from the Action; Esc / no-selection
// returns nil.
func Run(opts RunOptions) error {
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Width <= 0 {
		opts.Width = 80
	}

	fd := int(opts.In.Fd())
	oldState, rawErr := term.MakeRaw(fd)
	if rawErr == nil {
		defer term.Restore(fd, oldState)
	}

	state := newRunState(opts)
	reader := picker.NewKeyReader(opts.In)
	prevHeight := 0

	for {
		rendered, cursorRow := state.render(opts.Width)
		prevHeight = paintLines(opts.Out, rendered, cursorRow, prevHeight)

		key := reader.Read()
		switch {
		case key == picker.KeyEnter:
			idx := state.selectedEntryIndex()
			if idx < 0 {
				continue
			}
			entry := state.entries[idx]
			param := state.paramForRow(state.cursor)
			clearLines(opts.Out, prevHeight)
			return state.runEntry(entry, param)

		case key == picker.KeyEscape || key == picker.KeyCtrlC || key == picker.KeyEOF:
			clearLines(opts.Out, prevHeight)
			return nil

		case key == picker.KeyTab:
			state.flipMode()

		case key == picker.KeyDown:
			state.moveCursor(1)

		case key == picker.KeyUp:
			state.moveCursor(-1)

		case key == picker.KeyBackspace:
			if len(state.query) > 0 {
				state.query = state.query[:len(state.query)-1]
				state.cursor = 0
			}

		case key == picker.KeyCtrlU:
			state.query = ""
			state.cursor = 0

		case key >= 0x20 && key < 0x7f:
			state.query += string(rune(key))
			state.cursor = 0
		}
	}
}

// runEntry invokes the entry's Action and records success in Recents. Param
// is the binding stored in recents (from a sub-picker) — it overrides any
// value the action returns.
func (s *runState) runEntry(entry Entry, paramFromRecents string) error {
	var (
		returnedParam string
		err           error
	)
	if paramFromRecents != "" && entry.ActionWithParam != nil {
		returnedParam, err = entry.ActionWithParam(paramFromRecents)
	} else {
		returnedParam, err = entry.Action()
	}
	if err != nil {
		return err
	}
	if s.opts.Recents != nil {
		param := returnedParam
		if paramFromRecents != "" {
			// Re-running a bound recents row — preserve its binding even
			// if the action elected not to return one.
			param = paramFromRecents
		}
		s.opts.Recents.Record(entry.ID, param)
		_ = s.opts.Recents.Save()
	}
	return nil
}

// runState carries the live palette state between reads.
type runState struct {
	opts     RunOptions
	entries  []Entry
	query    string
	cursor   int
	rendered []renderedRow // last render's rows, in display order
}

// renderedRow is a single line in the rendered palette. entryIdx >= 0 means
// this row corresponds to an entry (selectable); entryIdx == -1 is a
// header/separator/footer (skipped by the cursor).
type renderedRow struct {
	text     string
	entryIdx int    // index into s.entries; -1 for non-selectable
	param    string // recents-bound param (for binding playback)
}

func newRunState(opts RunOptions) *runState {
	s := &runState{opts: opts}
	s.entries = opts.Entries(opts.Mode)
	return s
}

func (s *runState) flipMode() {
	if s.opts.Mode == ModeWindow {
		s.opts.Mode = ModePane
	} else {
		s.opts.Mode = ModeWindow
	}
	s.entries = s.opts.Entries(s.opts.Mode)
}

// moveCursor advances the cursor by delta rows, skipping non-selectable
// rows (headers, separators). A row's selectability is recorded at render
// time in s.rendered — so moveCursor simply walks that slice.
func (s *runState) moveCursor(delta int) {
	if len(s.rendered) == 0 {
		return
	}
	step := 1
	if delta < 0 {
		step = -1
		delta = -delta
	}
	for ; delta > 0; delta-- {
		next := s.cursor + step
		// Walk past any non-selectable rows.
		for next >= 0 && next < len(s.rendered) && s.rendered[next].entryIdx < 0 {
			next += step
		}
		if next < 0 || next >= len(s.rendered) {
			break
		}
		s.cursor = next
	}
}

// selectedEntryIndex returns the entries index of the row under the cursor,
// or -1 if the cursor is on a non-selectable row.
func (s *runState) selectedEntryIndex() int {
	if s.cursor < 0 || s.cursor >= len(s.rendered) {
		return -1
	}
	return s.rendered[s.cursor].entryIdx
}

func (s *runState) paramForRow(row int) string {
	if row < 0 || row >= len(s.rendered) {
		return ""
	}
	return s.rendered[row].param
}

// visibleEntries returns indices of entries whose scope is satisfied.
func (s *runState) visibleEntries() []int {
	out := make([]int, 0, len(s.entries))
	for i := range s.entries {
		if s.opts.Scope.satisfies(s.entries[i].Needs) {
			out = append(out, i)
		}
	}
	return out
}

// render produces the rows for display and returns (rows, selected row).
// It also caches the rows in s.rendered for moveCursor / selection.
func (s *runState) render(width int) ([]string, int) {
	s.rendered = nil

	// Prompt line first — a header line, not selectable.
	s.rendered = append(s.rendered, renderedRow{text: "> " + s.query, entryIdx: -1})

	visible := s.visibleEntries()
	entryByID := make(map[string]int, len(s.entries))
	for i, e := range s.entries {
		entryByID[e.ID] = i
	}

	if s.query == "" {
		// Empty query: recents section + categorical sections.
		s.renderRecents(visible, entryByID, width)
		s.renderSections(visible, width)
	} else {
		s.renderFiltered(visible, width)
	}

	// Footer — mode + Tab hint.
	s.rendered = append(s.rendered,
		renderedRow{text: "", entryIdx: -1},
		renderedRow{text: fmt.Sprintf("[mode: %s · Tab: flip · Esc: close]", s.opts.Mode), entryIdx: -1},
	)

	// Clamp cursor to a selectable row.
	s.cursor = clampToSelectable(s.rendered, s.cursor)

	rows := make([]string, len(s.rendered))
	for i, r := range s.rendered {
		rows[i] = r.text
	}
	return rows, s.cursor
}

// clampToSelectable ensures cursor points to a selectable row. Prefers the
// nearest selectable row on or after the requested index; falls back to the
// first selectable row overall.
func clampToSelectable(rows []renderedRow, cursor int) int {
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	for i := cursor; i < len(rows); i++ {
		if rows[i].entryIdx >= 0 {
			return i
		}
	}
	for i := range rows {
		if rows[i].entryIdx >= 0 {
			return i
		}
	}
	return 0
}

func (s *runState) renderRecents(visible []int, entryByID map[string]int, width int) {
	if s.opts.Recents == nil {
		return
	}
	valid := func(id, param string) bool {
		idx, ok := entryByID[id]
		if !ok {
			return false
		}
		e := s.entries[idx]
		if !s.opts.Scope.satisfies(e.Needs) {
			return false
		}
		if param != "" && e.ParamValid != nil {
			return e.ParamValid(param)
		}
		return true
	}
	recents := s.opts.Recents.TopRecents(valid)
	if len(recents) == 0 {
		return
	}
	s.rendered = append(s.rendered, renderedRow{text: "", entryIdx: -1})
	s.rendered = append(s.rendered, renderedRow{text: boldHeader("Recent"), entryIdx: -1})
	for _, r := range recents {
		idx := entryByID[r.ID]
		e := s.entries[idx]
		title := "  " + e.displayTitle(r.Param)
		text := rightAlign(title, e.Hotkey, width)
		s.rendered = append(s.rendered, renderedRow{text: text, entryIdx: idx, param: r.Param})
	}
	s.rendered = append(s.rendered, renderedRow{text: strings.Repeat("─", 6), entryIdx: -1})
	_ = visible
}

func (s *runState) renderSections(visible []int, width int) {
	bySection := map[Section][]int{}
	for _, idx := range visible {
		e := &s.entries[idx]
		bySection[e.Section] = append(bySection[e.Section], idx)
	}
	sections := []Section{
		SectionNavigation,
		SectionCreateSurface,
		SectionCreateBay,
		SectionCurrentBay,
		SectionCurrentSurface,
		SectionAdmin,
	}
	for _, sec := range sections {
		idxs := bySection[sec]
		if len(idxs) == 0 {
			continue
		}
		// Stable-sort by the original order in entries.
		sort.Ints(idxs)
		s.rendered = append(s.rendered, renderedRow{text: boldHeader(sec.Title()), entryIdx: -1})
		for _, idx := range idxs {
			e := s.entries[idx]
			title := "  " + e.Title
			text := rightAlign(title, e.Hotkey, width)
			s.rendered = append(s.rendered, renderedRow{text: text, entryIdx: idx})
		}
	}
}

// renderFiltered flattens visible entries into a single fuzzy-matched
// list. Deduped by Entry.ID (each entry appears at most once). Ordering
// is by fuzzy score descending; the original categorical order is the
// tie-breaker so results feel stable when multiple entries share a rank.
func (s *runState) renderFiltered(visible []int, width int) {
	seen := map[string]bool{}
	type scored struct {
		idx   int
		score int
	}
	var matches []scored
	for _, idx := range visible {
		e := &s.entries[idx]
		if seen[e.ID] {
			continue
		}
		score, ok := fuzzyScore(e.Title, s.query)
		if !ok {
			continue
		}
		seen[e.ID] = true
		matches = append(matches, scored{idx: idx, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})
	for _, m := range matches {
		e := s.entries[m.idx]
		text := rightAlign("  "+e.Title, e.Hotkey, width)
		s.rendered = append(s.rendered, renderedRow{text: text, entryIdx: m.idx})
	}
}

// fuzzyScore reports whether every rune of query appears in target in
// order (case-insensitive) and returns a score where higher is better.
//
// The scoring follows the fzf / VS Code command-palette convention:
//
//   - match at string start gets the biggest bonus — so "pw" ranks
//     "pwd" above "Show pw".
//   - adjacency ranks next: "ns-cmd" (adjacent) outranks "New shell"
//     (boundary + gap).
//   - word-boundary matches rank next: the first char after a space,
//     hyphen, underscore, or punctuation gets a boundary bonus. "nb"
//     against "New bay" prefers the 'w' at the start of
//     "bay" over the 'w' inside "New", which is critical to
//     ranking "New bay" above "New shell" on an "nb" query.
//   - within-word matches rank last: a middle-of-word match receives
//     only the base per-char credit plus a small gap penalty.
//
// Uses dynamic programming (O(|q|·|t|²)) so the best alignment is
// chosen across all match positions. For the short titles and queries
// the palette deals with this is plenty fast, and getting the word-
// boundary preference right requires looking past the greedy first
// match.
func fuzzyScore(target, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	tLower := strings.ToLower(target)
	qRunes := []rune(strings.ToLower(query))
	tRunes := []rune(tLower)
	if len(qRunes) > len(tRunes) {
		return 0, false
	}

	const neg = -1 << 30

	// dp[qi][ti] = best score to have matched q[0..qi+1] with the last
	// match at target position ti. neg means "unreachable".
	dp := make([][]int, len(qRunes))
	for qi := range dp {
		dp[qi] = make([]int, len(tRunes))
		for ti := range dp[qi] {
			dp[qi][ti] = neg
		}
	}

	// Seed the first query char.
	for ti, tc := range tRunes {
		if tc == qRunes[0] {
			dp[0][ti] = transitionScore(tLower, ti, -1)
		}
	}

	// Fill the remaining query chars.
	for qi := 1; qi < len(qRunes); qi++ {
		for ti, tc := range tRunes {
			if tc != qRunes[qi] {
				continue
			}
			best := neg
			for prevTi := 0; prevTi < ti; prevTi++ {
				if dp[qi-1][prevTi] == neg {
					continue
				}
				cand := dp[qi-1][prevTi] + transitionScore(tLower, ti, prevTi)
				if cand > best {
					best = cand
				}
			}
			if best > neg {
				dp[qi][ti] = best
			}
		}
	}

	best := neg
	for _, s := range dp[len(qRunes)-1] {
		if s > best {
			best = s
		}
	}
	if best == neg {
		return 0, false
	}
	return best, true
}

// transitionScore is the per-match contribution to a fuzzy score. prevPos
// is the previous match's position in the target, or -1 if this is the
// first matched char.
func transitionScore(target string, pos, prevPos int) int {
	const (
		startBonus    = 20
		adjacentBonus = 15
		boundaryBonus = 10
		matchBase     = 1
		gapPenalty    = -1
	)
	score := matchBase
	switch {
	case pos == 0:
		score += startBonus
	case prevPos >= 0 && pos == prevPos+1:
		score += adjacentBonus
	case isWordBoundary(target, pos):
		score += boundaryBonus
	}
	if prevPos >= 0 && pos > prevPos+1 {
		score += gapPenalty * (pos - prevPos - 1)
	}
	return score
}

// isWordBoundary reports whether the rune at index i in the lowercased
// string s starts a new "word". A word boundary follows whitespace, a
// hyphen, underscore, or common punctuation. Since s is already
// lowercased, camel-case transitions aren't detectable — palette titles
// are lowercase prose so this is fine in practice.
func isWordBoundary(s string, i int) bool {
	if i == 0 {
		return true
	}
	prev := s[i-1]
	switch prev {
	case ' ', '-', '_', '.', '(', '/', ':':
		return true
	}
	return false
}

// boldHeader styles section headers so they stand out from entry rows.
// Uses cyan foreground + underline: bold alone wasn't visually distinct
// in some terminal/font combos (the weight was already regular-ish), so
// underline is the belt-and-suspenders signal. Hotkey alignment isn't
// affected because headers have no hotkey column, so the escape codes
// don't break rightAlign's width math.
func boldHeader(text string) string {
	return "\x1b[4;36m" + text + "\x1b[0m"
}

// rightAlign pads left with spaces so the row fills the palette width.
// When hotkey is set, it sits flush with the right margin; otherwise
// trailing spaces extend the row to the full width so the selected-row
// highlight covers the whole line instead of just the title text.
// Lines already wider than width render without padding — callers expect
// the text to wrap rather than be truncated.
func rightAlign(left, hotkey string, width int) string {
	if hotkey == "" {
		pad := width - len(left)
		if pad <= 0 {
			return left
		}
		return left + strings.Repeat(" ", pad)
	}
	pad := width - len(left) - len(hotkey)
	if pad < 2 {
		// No room; put at least a single space between.
		return left + "  " + hotkey
	}
	return left + strings.Repeat(" ", pad) + hotkey
}

// paintLines draws the rows to out with the selected row highlighted, and
// erases any tail from a longer previous render. Returns the number of
// rows written (so the next call knows how much history to clear).
func paintLines(out io.Writer, rows []string, cursor, prevHeight int) int {
	for i, r := range rows {
		if i == cursor {
			fmt.Fprintf(out, "\r\x1b[K\x1b[7m%s\x1b[0m\r\n", r)
		} else {
			fmt.Fprintf(out, "\r\x1b[K%s\r\n", r)
		}
	}
	// Clear any leftover rows from a taller previous render.
	for i := len(rows); i < prevHeight; i++ {
		fmt.Fprint(out, "\r\x1b[K\r\n")
	}
	height := len(rows)
	if prevHeight > height {
		height = prevHeight
	}
	fmt.Fprintf(out, "\x1b[%dA", height)
	return len(rows)
}

func clearLines(out io.Writer, lines int) {
	fmt.Fprint(out, "\r")
	for range lines {
		fmt.Fprint(out, "\x1b[K\r\n")
	}
	if lines > 0 {
		fmt.Fprintf(out, "\x1b[%dA", lines)
	}
}

// Notice prints text to out and waits for any keypress before returning.
// Used by palette actions that have something to display (current context,
// bay details, doctor output) but don't otherwise prompt the user.
// The popup closes as soon as this returns.
func Notice(in, out *os.File, text string) {
	fd := int(in.Fd())
	oldState, rawErr := term.MakeRaw(fd)
	if rawErr == nil {
		defer term.Restore(fd, oldState)
	}
	lines := strings.Split(text, "\n")
	for _, l := range lines {
		fmt.Fprintf(out, "%s\r\n", l)
	}
	fmt.Fprint(out, "\r\n[press any key]\r\n")
	reader := picker.NewKeyReader(in)
	_ = reader.Read()
}
