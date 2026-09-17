package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"golang.org/x/term"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// stripANSI removes ANSI escape sequences from s. Used when we need to
// hand bay's colored output to a consumer that renders plain text only
// (e.g. tmux display-message, which does not interpret ANSI).
func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

type FocusKind string

const (
	FocusAll  FocusKind = "all"
	FocusDock FocusKind = "dock"
	FocusBay  FocusKind = "bay"
)

type ListFocus struct {
	Kind  FocusKind `json:"kind"`
	Dock  string    `json:"dock,omitempty"`
	BayID string    `json:"bay_id,omitempty"`
}

type ListViewOptions struct {
	Focus     ListFocus
	Recursive bool
}

type ListView struct {
	Focus          ListFocus         `json:"focus"`
	Recursive      bool              `json:"recursive"`
	Docks          []engine.DockInfo `json:"docks"`
	CurrentDock    string            `json:"-"` // for highlighting; not serialized
	CurrentBayID   string            `json:"-"`
	CurrentSurface string            `json:"-"`
}

// SetCurrentContext populates the current dock/bay/surface for highlighting.
func (v *ListView) SetCurrentContext(eng *engine.Engine) {
	if session, err := eng.Tmux.CurrentSession(); err == nil {
		v.CurrentDock = session
	}
	if dock, bay, err := eng.ResolveSelf(); err == nil {
		v.CurrentDock = dock
		v.CurrentBayID = bay
	}
	// Resolve current surface from tmux pane.
	if ctx, err := eng.CurrentContext(); err == nil {
		v.CurrentSurface = ctx.Surface
	}
}

type ListRow struct {
	Dock           string `json:"dock,omitempty"`
	BayName        string `json:"bay_name,omitempty"`
	BayBranch      string `json:"bay_branch,omitempty"`
	BayDirty       bool   `json:"bay_dirty,omitempty"`
	BayPending     bool   `json:"bay_pending,omitempty"`
	BaySetup       string `json:"bay_setup,omitempty"`
	BaySyncStatus  string `json:"bay_sync_status,omitempty"`
	BayWaiting     bool   `json:"bay_waiting,omitempty"`
	SurfaceID      int    `json:"surface_id,omitempty"`
	SurfaceName    string `json:"surface_name,omitempty"`
	SurfaceType    string `json:"surface_type,omitempty"`
	SurfaceAgent   string `json:"surface_agent,omitempty"`
	SurfaceCommand string `json:"surface_command,omitempty"`
	SurfaceStatus  string `json:"surface_status,omitempty"`
}

// BuildListView builds the dock -> bay hierarchy from DockInfo data.
func BuildListView(docks []engine.DockInfo, opts ListViewOptions) ListView {
	focus := opts.Focus
	if focus.Kind == "" {
		focus.Kind = FocusAll
	}
	recursive := opts.Recursive || focus.Kind == FocusBay

	dockMap := map[string]engine.DockInfo{}
	for _, dock := range docks {
		dockMap[dock.Name] = dock
	}

	view := ListView{Focus: focus, Recursive: recursive}
	var dockNames []string
	for dockName := range dockMap {
		dockNames = append(dockNames, dockName)
	}
	sort.Strings(dockNames)

	for _, dockName := range dockNames {
		if (focus.Kind == FocusDock || focus.Kind == FocusBay) && focus.Dock != "" && focus.Dock != dockName {
			continue
		}
		dock := dockMap[dockName]
		filtered := dock
		filtered.Bays = nil
		for _, bay := range dock.Bays {
			if focus.Kind == FocusBay && focus.BayID != "" && focus.BayID != bay.ID {
				continue
			}
			filtered.Bays = append(filtered.Bays, trimBay(bay, recursive))
		}
		if focus.Kind == FocusBay && len(filtered.Bays) == 0 {
			continue
		}
		view.Docks = append(view.Docks, filtered)
	}

	return view
}

func trimBay(bay engine.BayInfo, recursive bool) engine.BayInfo {
	if recursive {
		return bay
	}
	bay.Surfaces = nil
	return bay
}

// printListViewJSON marshals a ListView (or its denormalized rows) to stdout.
func printListViewJSON(view ListView, rows bool) error {
	var payload interface{} = view
	if rows {
		payload = ListRows(view)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func ListRows(view ListView) []ListRow {
	var rows []ListRow
	for _, dock := range view.Docks {
		for _, bay := range dock.Bays {
			if len(bay.Surfaces) == 0 {
				rows = append(rows, ListRow{
					Dock:          dock.Name,
					BayName:       bay.Name,
					BayBranch:     bay.Branch,
					BayDirty:      bay.Dirty,
					BayPending:    bay.Pending,
					BaySetup:      bay.Setup,
					BaySyncStatus: bay.SyncStatus,
					BayWaiting:    bay.Waiting,
				})
				continue
			}
			for _, s := range bay.Surfaces {
				rows = append(rows, ListRow{
					Dock:           dock.Name,
					BayName:        bay.Name,
					BayBranch:      bay.Branch,
					BayDirty:       bay.Dirty,
					BayPending:     bay.Pending,
					BaySetup:       bay.Setup,
					BaySyncStatus:  bay.SyncStatus,
					BayWaiting:     bay.Waiting,
					SurfaceID:      s.ID,
					SurfaceName:    s.Name,
					SurfaceType:    s.Type,
					SurfaceAgent:   s.Agent,
					SurfaceCommand: s.Command,
					SurfaceStatus:  s.Status,
				})
			}
		}
	}
	return rows
}

func dim(s string) string {
	return "\x1b[38;5;243m" + s + "\x1b[0m"
}

func kv(key, value string) string {
	return dim(key+"=") + value
}

// metaCol is a single field in a metadata row (e.g., "branch=fix/login").
// width is the visible column count, excluding ANSI escape codes.
type metaCol struct {
	text  string
	width int
}

// kvCol builds a metaCol from a key=value pair. Returns a zero metaCol when
// value is empty.
func kvCol(key, value string) metaCol {
	if value == "" {
		return metaCol{}
	}
	return metaCol{
		text:  kv(key, value),
		width: utf8.RuneCountInString(key) + 1 + utf8.RuneCountInString(value),
	}
}

// bareCol builds a metaCol from a bare value (no key= prefix).
func bareCol(value string) metaCol {
	if value == "" {
		return metaCol{}
	}
	return metaCol{text: value, width: utf8.RuneCountInString(value)}
}

// metaField returns a kvCol or bareCol depending on whether short mode is on.
func metaField(key, value string, short bool) metaCol {
	if short {
		return bareCol(value)
	}
	return kvCol(key, value)
}

func labelValue(label, value string) string {
	return dim(label) + " " + value
}

// labelValueWithWidth returns the labelValue prefix and its visible column
// indentedPrefix builds an indented prefix with a dimmed label and name.
// In short mode, the label is omitted entirely.
func indentedPrefix(level int, label, name string, short bool) (string, int) {
	indent := strings.Repeat("  ", level)
	if short {
		return indent + name, level*2 + utf8.RuneCountInString(name)
	}
	prefix := indent + dim(label) + " " + name
	width := level*2 + utf8.RuneCountInString(label) + 1 + utf8.RuneCountInString(name)
	return prefix, width
}

func dimmedSeparator(sep string) string {
	return dim(sep)
}

func truncateCommand(cmd string) string {
	if len(cmd) <= 32 {
		return cmd
	}
	return cmd[:29] + "..."
}

const (
	listBayNameStrMax = 32
	listBranchStrMax  = 24
)

func truncateListField(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 2 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-2]) + ".."
}

// bayMetaCols returns fixed-position columns for bay metadata.
// Column positions: 0=description, 1=branch, 2=id (only when it adds
// info beyond the bay label), 3=status, 4=setup, 5=count, 6=sync/waiting.
//
// The bay column itself renders BayInfo.Label() (e.g. "b1.auth-fix"),
// which already carries the path-derived dir tag — so no separate `dir`
// column is needed. The `id` slot is preserved for external bays whose
// ID diverges from both the dir tag and the name (rare).
//
// Descriptions come through at full stored length; FormatListView shrinks
// them in place (index 0) if the computed terminal budget is tighter. Only
// the first line of the description is shown — bodies are opt-in and appear
// in the M-? popup, not on the list row.
func bayMetaCols(bay engine.BayInfo, showCounts, short bool) []metaCol {
	cols := make([]metaCol, 7)
	if first := engine.DescriptionFirstLine(bay.Description); first != "" {
		cols[0] = descField(first, engine.MaxDescriptionFirstLineLen, short)
	}
	cols[1] = metaField("br", truncateListField(bay.Branch, listBranchStrMax), short)
	// Surface the raw ID only when the label hides it — i.e. external bays
	// whose ID isn't reflected in either the path-derived dir tag or Name.
	dir := ""
	if bay.Path != "" {
		dir = filepath.Base(bay.Path)
	}
	if bay.ID != "" && bay.ID != bay.Name && bay.ID != dir {
		cols[2] = metaField("id", bay.ID, short)
	}
	if bay.Dirty {
		cols[3] = metaField("st", "dirty", short)
	} else if bay.Pending {
		cols[3] = metaField("st", "pending", short)
	}
	cols[4] = metaField("setup", bay.Setup, short)
	if showCounts {
		cols[5] = metaField("n", fmt.Sprintf("%d", bay.SurfaceCount), short)
	}
	// Sync and waiting indicators share the trailing column.
	var parts []string
	var tailWidth int
	if sync := bay.SyncStatus; sync != "" && sync != manifest.SyncStatusOK {
		if short {
			parts = append(parts, sync)
			tailWidth = utf8.RuneCountInString(sync)
		} else {
			parts = append(parts, kv("sy", sync))
			tailWidth = 3 + utf8.RuneCountInString(sync)
		}
	}
	if bay.Waiting {
		parts = append(parts, "\u23f3")
		if tailWidth > 0 {
			tailWidth += 2 // space + emoji
		} else {
			tailWidth = 1
		}
	}
	if len(parts) > 0 {
		cols[6] = metaCol{text: strings.Join(parts, " "), width: tailWidth}
	}
	return cols
}

// listDescMinStrLen floors the description string length so even a narrow
// terminal shows a useful 15-char hint instead of near-nothing.
const listDescMinStrLen = 15

// listDescPipedStrMax is the cap applied when stdout isn't a terminal
// (pipe, redirect, CI). Pipes have no natural width; downstream consumers
// should see the complete first-line value (bodies are opt-in popup-only).
const listDescPipedStrMax = engine.MaxDescriptionFirstLineLen

// descColLabel is the key-prefix for a description column in long format.
const descColLabel = "ds="

// descColOverhead returns the non-string characters in a rendered
// description column: two quotes, plus the "ds=" prefix in long format.
// Used by the width budget to map a column budget back to a string budget.
func descColOverhead(short bool) int {
	if short {
		return 2
	}
	return 2 + len(descColLabel)
}

// descField renders a description as ds="..." (or a bare quoted string in
// short mode), truncating the string to strMax characters and quoting so
// embedded spaces don't blur the column boundary.
func descField(desc string, strMax int, short bool) metaCol {
	quoted := `"` + engine.TruncateName(desc, strMax) + `"`
	w := utf8.RuneCountInString(quoted)
	if short {
		return metaCol{text: quoted, width: w}
	}
	return metaCol{text: dim(descColLabel) + quoted, width: len(descColLabel) + w}
}

// surfaceMetaCols returns fixed-position columns for surface metadata.
// Column positions: 0=type, 1=agent, 2=command, 3=sync.
func surfaceMetaCols(s engine.SurfaceInfo, long, short bool) []metaCol {
	cols := make([]metaCol, 4)
	cols[0] = metaField("ty", s.Type, short)
	cols[1] = metaField("ag", s.Agent, short)
	cols[2] = metaField("cm", truncateCommand(s.Command), short)
	if s.Status != "" && s.Status != manifest.SyncStatusOK {
		cols[3] = metaField("sy", s.Status, short)
	}
	return cols
}

// writeAlignedLine writes "<prefix><pad><col0><pad><col1>...".
// prefixAlign controls where the first column starts (max prefixWidth across
// sibling rows). colWidths controls per-column alignment across sibling rows.
// Lines with all-empty meta columns omit padding entirely.
func writeAlignedLine(b *strings.Builder, prefix string, prefixWidth int, metaCols []metaCol, prefixAlign int, colWidths []int) {
	b.WriteString(prefix)

	// Find last column this row actually populates.
	lastPopulated := -1
	for i := len(metaCols) - 1; i >= 0; i-- {
		if metaCols[i].text != "" {
			lastPopulated = i
			break
		}
	}
	if lastPopulated < 0 {
		b.WriteString("\n")
		return
	}

	// Pad prefix to alignment point.
	if prefixAlign > 0 {
		b.WriteString(strings.Repeat(" ", prefixAlign-prefixWidth+2))
	} else {
		b.WriteString(" ")
	}

	// Write columns with inter-column padding.
	first := true
	for i := 0; i <= lastPopulated; i++ {
		cw := 0
		if i < len(colWidths) {
			cw = colWidths[i]
		}
		if cw == 0 {
			continue // column unused across all rows
		}
		if !first {
			b.WriteString(" ")
		}
		first = false

		col := metaCol{}
		if i < len(metaCols) {
			col = metaCols[i]
		}
		b.WriteString(col.text)

		// Pad to column width, except for the last populated column.
		if i < lastPopulated {
			b.WriteString(strings.Repeat(" ", cw-col.width))
		}
	}
	b.WriteString("\n")
}

// alignedRow holds a precomputed render row. The optional surfaceRows
// field is populated only on bay rows in tree mode and carries the
// pre-built surface render rows for that bay, so the render pass
// can iterate bay rows without indexing back into dock.Bays.
type alignedRow struct {
	prefix      string
	prefixWidth int
	metaCols    []metaCol
	surfaceRows []alignedRow // bay rows only, in tree mode
}

func hasMetaCols(cols []metaCol) bool {
	for _, c := range cols {
		if c.text != "" {
			return true
		}
	}
	return false
}

// alignWidth returns the max prefix width across rows that have non-empty
// meta. Rows without meta don't contribute, so they don't push the meta
// column out for their siblings.
func alignWidth(rows []alignedRow) int {
	max := 0
	for _, r := range rows {
		if !hasMetaCols(r.metaCols) {
			continue
		}
		if r.prefixWidth > max {
			max = r.prefixWidth
		}
	}
	return max
}

// alignColumnWidths returns the max visible width for each column position
// across all rows. Columns that no row populates have width 0.
func alignColumnWidths(rows []alignedRow) []int {
	var widths []int
	for _, r := range rows {
		for i, c := range r.metaCols {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			if c.width > widths[i] {
				widths[i] = c.width
			}
		}
	}
	return widths
}

// detectStdoutWidth returns the current terminal width of stdout. Returns 0
// when stdout is not a terminal (pipe, redirect, CI) so callers can switch to
// pipe-friendly behavior (no width-aware truncation).
//
// `COLUMNS` wins when it's set to a positive integer — standard Unix override
// that also works through pipes (e.g. `COLUMNS=80 bay tree | less -S`).
func detectStdoutWidth() int {
	if c := os.Getenv("COLUMNS"); c != "" {
		if w, err := strconv.Atoi(c); err == nil && w > 0 {
			return w
		}
	}
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return 0
	}
	w, _, err := term.GetSize(fd)
	if err != nil || w <= 0 {
		return 0
	}
	return w
}

// descStrMaxForWidth returns the max description string length that fits
// alongside the rest of a bay row at the given terminal width.
// termWidth == 0 means "not a terminal" (pipe/redirect) and returns the
// storage cap so consumers see the full stored value.
//
// nonDescWidth is the precomputed width of everything in the row except the
// description column and its leading inter-column separator.
func descStrMaxForWidth(termWidth, nonDescWidth int, short bool) int {
	if termWidth <= 0 {
		return listDescPipedStrMax
	}
	const interColSeparator = 1
	budget := termWidth - nonDescWidth - interColSeparator - descColOverhead(short)
	if budget < listDescMinStrLen {
		return listDescMinStrLen
	}
	if budget > listDescPipedStrMax {
		return listDescPipedStrMax
	}
	return budget
}

// nonDescRowWidth returns the displayed row width excluding the description
// column (index 0), using the already-computed prefix alignment and
// per-column max widths.
func nonDescRowWidth(prefixAlign int, colWidths []int) int {
	total := prefixAlign + 2 // prefix + min padding to first col
	first := true
	for i := 1; i < len(colWidths); i++ {
		if colWidths[i] == 0 {
			continue
		}
		if !first {
			total++
		}
		first = false
		total += colWidths[i]
	}
	return total
}

func FormatListView(view ListView, long, short bool) string {
	termWidth := detectStdoutWidth()
	var b strings.Builder
	for _, dock := range view.Docks {
		dockMarker := ""
		if dock.Name == view.CurrentDock {
			dockMarker = " *"
		}
		p, _ := indentedPrefix(0, "dk", dock.Name+dockMarker, short)
		fmt.Fprintln(&b, p)

		// Dock-level surfaces (e.g., dock editor).
		if len(dock.Surfaces) > 0 {
			var dockSfRows []alignedRow
			for _, s := range dock.Surfaces {
				sfP, sfW := indentedPrefix(1, "sf", s.Name, short)
				dockSfRows = append(dockSfRows, alignedRow{
					prefix:      sfP,
					prefixWidth: sfW,
					metaCols:    surfaceMetaCols(s, long, short),
				})
			}
			dockSfAlign := alignWidth(dockSfRows)
			dockSfColWidths := alignColumnWidths(dockSfRows)
			for _, sr := range dockSfRows {
				writeAlignedLine(&b, sr.prefix, sr.prefixWidth, sr.metaCols, dockSfAlign, dockSfColWidths)
			}
		}

		if len(dock.Bays) == 0 && len(dock.Surfaces) == 0 {
			p, _ := indentedPrefix(1, "", "(no bays)", short)
			fmt.Fprintln(&b, p)
			continue
		}
		if len(dock.Bays) == 0 {
			continue
		}
		showChildren := view.Recursive || view.Focus.Kind == FocusBay

		bayRows := make([]alignedRow, len(dock.Bays))
		var allSfRows []alignedRow
		for i, bay := range dock.Bays {
			isCurrentBay := dock.Name == view.CurrentDock && bay.ID == view.CurrentBayID
			bayLabel := truncateListField(bay.Label(), listBayNameStrMax)
			if isCurrentBay {
				bayLabel += " *"
			}
			p, w := indentedPrefix(1, "bay", bayLabel, short)
			bayRows[i] = alignedRow{
				prefix:      p,
				prefixWidth: w,
				metaCols:    bayMetaCols(bay, !showChildren, short),
			}
			if !showChildren {
				continue
			}
			sfRows := make([]alignedRow, len(bay.Surfaces))
			for j, s := range bay.Surfaces {
				sName := s.Name
				if isCurrentBay && s.Name == view.CurrentSurface {
					sName += " *"
				}
				sfP, sfW := indentedPrefix(2, "sf", sName, short)
				sfRows[j] = alignedRow{
					prefix:      sfP,
					prefixWidth: sfW,
					metaCols:    surfaceMetaCols(s, long, short),
				}
			}
			bayRows[i].surfaceRows = sfRows
			allSfRows = append(allSfRows, sfRows...)
		}

		bayAlign := alignWidth(bayRows)
		bayColWidths := alignColumnWidths(bayRows)

		// Fit description column (index 0) to terminal width by shrinking
		// too-long rows in place. No-op when no bay has a description.
		if bayColWidths[0] > 0 {
			strMax := descStrMaxForWidth(termWidth, nonDescRowWidth(bayAlign, bayColWidths), short)
			shrunk := false
			for i, bay := range dock.Bays {
				first := engine.DescriptionFirstLine(bay.Description)
				if first == "" || len(first) <= strMax {
					continue
				}
				bayRows[i].metaCols[0] = descField(first, strMax, short)
				shrunk = true
			}
			if shrunk {
				bayColWidths = alignColumnWidths(bayRows)
			}
		}

		sfAlign := alignWidth(allSfRows)
		sfColWidths := alignColumnWidths(allSfRows)

		for _, bayRow := range bayRows {
			writeAlignedLine(&b, bayRow.prefix, bayRow.prefixWidth, bayRow.metaCols, bayAlign, bayColWidths)
			for _, sr := range bayRow.surfaceRows {
				writeAlignedLine(&b, sr.prefix, sr.prefixWidth, sr.metaCols, sfAlign, sfColWidths)
			}
		}
	}
	return b.String()
}

// showRow is a label/value pair for detail views (bay show, dock show, etc.).
type showRow struct{ label, value string }

// maxShowRowLabel returns the max label width across rows.
func maxShowRowLabel(rows []showRow) int {
	max := 0
	for _, r := range rows {
		if len(r.label) > max {
			max = len(r.label)
		}
	}
	return max
}

// printAlignedRows prints rows with right-aligned labels.
func printAlignedRows(rows []showRow) {
	maxLabel := maxShowRowLabel(rows)
	for _, r := range rows {
		pad := strings.Repeat(" ", maxLabel-len(r.label))
		fmt.Printf("%s%s %s\n", pad, dim(r.label), r.value)
	}
}

// writeAlignedRows writes rows with right-aligned labels to a builder.
func writeAlignedRows(b *strings.Builder, rows []showRow, minWidth int) {
	maxLabel := maxShowRowLabel(rows)
	if minWidth > maxLabel {
		maxLabel = minWidth
	}
	for _, r := range rows {
		pad := strings.Repeat(" ", maxLabel-len(r.label))
		fmt.Fprintf(b, "%s%s %s\n", pad, dim(r.label), r.value)
	}
}

func FormatBayShow(dockName string, bay *engine.BayInfo, long bool) string {
	var b strings.Builder

	var rows []showRow
	rows = append(rows, showRow{"bay", bay.Label()})
	// Surface the raw ID only when the label hides it — external bays whose
	// ID matches neither the path-derived dir tag nor the Name.
	dir := ""
	if bay.Path != "" {
		dir = filepath.Base(bay.Path)
	}
	if bay.ID != "" && bay.ID != bay.Name && bay.ID != dir {
		rows = append(rows, showRow{"id", bay.ID})
	}
	if bay.Description != "" {
		// Show the first line as the aligned "description" row; render any
		// body as follow-on unlabeled rows so alignment stays clean.
		first, body, _ := strings.Cut(bay.Description, "\n")
		rows = append(rows, showRow{"description", first})
		if body != "" {
			for _, line := range strings.Split(body, "\n") {
				rows = append(rows, showRow{"", line})
			}
		}
	}
	rows = append(rows, showRow{"dock", dockName})
	if bay.Type != "" && bay.Type != "worktree" {
		rows = append(rows, showRow{"type", bay.Type})
	}
	rows = append(rows, showRow{"path", bay.Path})
	if bay.Branch != "" {
		rows = append(rows, showRow{"branch", bay.Branch})
	}
	if bay.PR != "" {
		rows = append(rows, showRow{"pr", bay.PR})
	}
	if bay.Dirty {
		rows = append(rows, showRow{"dirty", "yes"})
	}
	if bay.Pending {
		rows = append(rows, showRow{"pending", "yes"})
	}
	if bay.Setup != "" {
		rows = append(rows, showRow{"setup", bay.Setup})
	}
	if bay.DefaultAgent != "" {
		rows = append(rows, showRow{"default agent", bay.DefaultAgent})
	}
	if bay.SyncStatus != "" && bay.SyncStatus != manifest.SyncStatusOK {
		rows = append(rows, showRow{"sync", bay.SyncStatus})
	}

	// Write rows, ensuring "surface" label fits in the alignment.
	sfLabel := "surface"
	writeAlignedRows(&b, rows, len(sfLabel))
	maxLabel := maxShowRowLabel(rows)
	if len(sfLabel) > maxLabel {
		maxLabel = len(sfLabel)
	}

	// Surface rows: right-align "surface" label to same column.
	sfPad := strings.Repeat(" ", maxLabel-len(sfLabel))
	sfRows := make([]alignedRow, len(bay.Surfaces))
	for i, s := range bay.Surfaces {
		prefix := sfPad + dim(sfLabel) + " " + s.Name
		width := maxLabel + 1 + utf8.RuneCountInString(s.Name)
		sfRows[i] = alignedRow{
			prefix:      prefix,
			prefixWidth: width,
			metaCols:    surfaceMetaCols(s, long, false),
		}
	}
	sfAlign := alignWidth(sfRows)
	sfColWidths := alignColumnWidths(sfRows)
	for _, sr := range sfRows {
		writeAlignedLine(&b, sr.prefix, sr.prefixWidth, sr.metaCols, sfAlign, sfColWidths)
	}
	return b.String()
}

// FormatSurfaceList renders a flat list of surfaces using the shared
// surfaceMetaCols / aligned-row machinery. currentSurface is highlighted
// with " *" when non-empty.
func FormatSurfaceList(surfaces []engine.SurfaceInfo, currentSurface string, short bool) string {
	if len(surfaces) == 0 {
		return ""
	}
	rows := make([]alignedRow, len(surfaces))
	for i, s := range surfaces {
		name := s.Name
		if s.Name == currentSurface {
			name += " *"
		}
		p, w := indentedPrefix(0, "sf", name, short)
		rows[i] = alignedRow{
			prefix:      p,
			prefixWidth: w,
			metaCols:    surfaceMetaCols(s, false, short),
		}
	}
	align := alignWidth(rows)
	colWidths := alignColumnWidths(rows)
	var b strings.Builder
	for _, r := range rows {
		writeAlignedLine(&b, r.prefix, r.prefixWidth, r.metaCols, align, colWidths)
	}
	return b.String()
}

// FormatFullTree formats the full dock -> bay hierarchy.
func FormatFullTree(docks []engine.DockInfo) string {
	return FormatListView(BuildListView(docks, ListViewOptions{}), false, false)
}

// FormatDockTree formats dock -> bay hierarchy for one or more docks.
func FormatDockTree(docks []engine.DockInfo) string {
	focus := ListFocus{Kind: FocusAll}
	if len(docks) == 1 {
		focus = ListFocus{Kind: FocusDock, Dock: docks[0].Name}
	}
	return FormatListView(BuildListView(docks, ListViewOptions{Focus: focus}), false, false)
}
