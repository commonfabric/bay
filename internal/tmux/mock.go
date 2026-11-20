package tmux

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// LayoutNode is the Mock's model of a tmux window's layout tree. A leaf
// holds a pane (Pane != ""); an internal node represents a binary split
// (Dir is "h" or "v" with two Children). The flat ListPanes/Pane slice
// previously stored on a window is now derived by depth-first traversal,
// matching the order tmux prints in `list-panes`.
//
// We model only what bay can actually express via its tmux primitives:
// binary splits via SplitWindow (with or without -fb), single-pane
// windows via NewWindow, leaf removals via KillPane. tmux's richer
// layout-string vocabulary (n-ary splits, percentage sizing) is out of
// scope — bay never produces such layouts.
type LayoutNode struct {
	Pane     string        // non-empty for leaves only
	Dir      string        // "h" or "v"; non-empty for internal nodes only
	Children []*LayoutNode // nil/empty for leaves
}

// Call records a method call on the mock.
type Call struct {
	Method string
	Args   []string
}

// mockWindow holds internal mock state for a window.
type mockWindow struct {
	id      string
	name    string
	session string
	index   int
	options map[string]string
	layout  *LayoutNode // root of the window's layout tree; nil when empty
}

// mockPane holds internal mock state for a pane.
type mockPane struct {
	id       string
	windowID string
	pid      int
	active   bool
	dead     bool   // set by SetPaneDead
	capture  string // content returned by CapturePane
	cursorY  int
}

// --- LayoutNode helpers ---

// newLeaf returns a leaf node holding the given pane ID.
func newLeaf(paneID string) *LayoutNode {
	return &LayoutNode{Pane: paneID}
}

// panesInOrder returns the leaf pane IDs in left-to-right depth-first
// order, matching how tmux's list-panes orders its output.
func panesInOrder(node *LayoutNode) []string {
	if node == nil {
		return nil
	}
	if node.Pane != "" {
		return []string{node.Pane}
	}
	var out []string
	for _, c := range node.Children {
		out = append(out, panesInOrder(c)...)
	}
	return out
}

// findLeaf walks the tree looking for the leaf with the given pane ID
// and returns it along with its parent (or nil parent if it's the root).
func findLeaf(node, parent *LayoutNode, paneID string) (leaf, par *LayoutNode) {
	if node == nil {
		return nil, nil
	}
	if node.Pane == paneID {
		return node, parent
	}
	for _, c := range node.Children {
		if l, p := findLeaf(c, node, paneID); l != nil {
			return l, p
		}
	}
	return nil, nil
}

func indexOfChild(parent, child *LayoutNode) int {
	if parent == nil {
		return -1
	}
	for i, c := range parent.Children {
		if c == child {
			return i
		}
	}
	return -1
}

// firstLeaf returns any leaf in the tree (used when the split target
// is a window ID rather than a pane ID — tmux's split-window against a
// window splits the active pane; the mock approximates with the first
// leaf in depth-first order, which is sufficient for tests since callers
// who care about precise targeting always pass a pane ID).
func firstLeaf(node *LayoutNode) *LayoutNode {
	if node == nil {
		return nil
	}
	if node.Pane != "" {
		return node
	}
	for _, c := range node.Children {
		if l := firstLeaf(c); l != nil {
			return l
		}
	}
	return nil
}

// splitLeaf replaces a leaf with a binary split. The original leaf
// becomes one child; a new leaf with newPaneID becomes the other.
// before=false places the new pane after the existing one (default
// tmux split-window behavior); before=true places it ahead.
func splitLeaf(leaf *LayoutNode, newPaneID, dir string, before bool) {
	existing := newLeaf(leaf.Pane)
	addition := newLeaf(newPaneID)
	leaf.Pane = ""
	leaf.Dir = dir
	if before {
		leaf.Children = []*LayoutNode{addition, existing}
	} else {
		leaf.Children = []*LayoutNode{existing, addition}
	}
}

// removeLeaf removes a leaf from its parent and collapses the parent
// to its surviving sibling if only one remains. Returns the new tree
// root (which may have changed if the root itself was the surviving
// child of a now-collapsed split).
func removeLeaf(root, parent, leaf *LayoutNode) *LayoutNode {
	if parent == nil {
		// leaf was the root of the window — caller must drop the tree.
		return nil
	}
	idx := indexOfChild(parent, leaf)
	if idx < 0 {
		return root
	}
	parent.Children = append(parent.Children[:idx], parent.Children[idx+1:]...)
	if len(parent.Children) == 1 {
		// Collapse: promote the surviving sibling into the parent's slot.
		survivor := parent.Children[0]
		*parent = *survivor
	}
	return root
}

// layoutToString renders a layout tree as a parenthesized expression.
// Vertical splits are slash-separated, horizontal pipe-separated; nested
// internal nodes get parens for unambiguous parsing. Leaves are bare
// pane IDs.
//
//	"%1"             — single pane
//	"%1/%2/%3"       — three panes stacked vertically
//	"%1|%2"          — two panes side by side
//	"%1/(%2|%3)"     — %1 on top; %2 and %3 side-by-side below
func layoutToString(node *LayoutNode) string {
	if node == nil {
		return ""
	}
	if node.Pane != "" {
		return node.Pane
	}
	sep := "/"
	if node.Dir == "h" {
		sep = "|"
	}
	parts := make([]string, len(node.Children))
	for i, c := range node.Children {
		s := layoutToString(c)
		if c.Pane == "" {
			s = "(" + s + ")"
		}
		parts[i] = s
	}
	return strings.Join(parts, sep)
}

// Mock is a test double that implements Interface using in-memory state.
//
// The recorder (Calls + record) is protected by mu so concurrent callers
// from the engine's parallel sync paths don't race on the slice append.
// Tests that read or reset Calls directly should do so sequentially
// (between test phases), not concurrently with engine code.
type Mock struct {
	mu    sync.Mutex
	Calls []Call

	sessions       map[string]bool
	sessionOptions map[string]map[string]string // session name -> option key -> value
	windows        map[string]*mockWindow       // keyed by window ID
	panes          map[string]*mockPane         // keyed by pane ID

	windowCounter int
	paneCounter   int
	pidCounter    int

	currentSession     string
	currentWindowID    string
	currentPaneID      string
	currentSessionSet  bool
	currentWindowIDSet bool
	currentPaneIDSet   bool

	clientWidth         int
	statusReservedCells int
	displayMessages     []string // recorded DisplayMessage calls in order

	// OnKill is an optional test hook fired at the start of any
	// destructive operation (KillPane / KillWindow / KillSession)
	// before the mock mutates its own state. Tests use it to assert
	// invariants like "the manifest must already be updated by the
	// time bay issues a tmux kill" without instrumenting production
	// code. method is "KillPane" / "KillWindow" / "KillSession".
	OnKill func(method, target string)
}

// NewMock creates a new Mock with initialized state.
func NewMock() *Mock {
	return &Mock{
		sessions:       make(map[string]bool),
		sessionOptions: make(map[string]map[string]string),
		windows:        make(map[string]*mockWindow),
		panes:          make(map[string]*mockPane),
	}
}

func (m *Mock) record(method string, args ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, Call{Method: method, Args: args})
}

func (m *Mock) nextWindowID() string {
	m.windowCounter++
	return fmt.Sprintf("@%d", m.windowCounter)
}

func (m *Mock) nextPaneID() string {
	m.paneCounter++
	return fmt.Sprintf("%%%d", m.paneCounter)
}

func (m *Mock) nextPID() int {
	m.pidCounter++
	return 10000 + m.pidCounter
}

// --- Sessions ---

func (m *Mock) HasSession(name string) (bool, error) {
	m.record("HasSession", name)
	return m.sessions[name], nil
}

func (m *Mock) NewSession(name string) error {
	m.record("NewSession", name)
	if m.sessions[name] {
		return fmt.Errorf("session %q already exists", name)
	}
	m.sessions[name] = true
	// Real tmux creates a default window when a session is created.
	// Replicate that behavior so placeholder logic works correctly.
	m.NewWindow(name, "", "")
	return nil
}

func (m *Mock) KillSession(name string) error {
	m.record("KillSession", name)
	if m.OnKill != nil {
		m.OnKill("KillSession", name)
	}
	if !m.sessions[name] {
		return fmt.Errorf("session %q not found", name)
	}
	// Remove all windows (and their panes) belonging to this session.
	for wid, w := range m.windows {
		if w.session == name {
			for _, pid := range panesInOrder(w.layout) {
				delete(m.panes, pid)
			}
			delete(m.windows, wid)
		}
	}
	delete(m.sessions, name)
	delete(m.sessionOptions, name)
	return nil
}

func (m *Mock) RenameSession(oldName string, newName string) error {
	m.record("RenameSession", oldName, newName)
	if !m.sessions[oldName] {
		return fmt.Errorf("session %q not found", oldName)
	}
	if m.sessions[newName] {
		return fmt.Errorf("session %q already exists", newName)
	}
	delete(m.sessions, oldName)
	m.sessions[newName] = true
	if opts, ok := m.sessionOptions[oldName]; ok {
		m.sessionOptions[newName] = opts
		delete(m.sessionOptions, oldName)
	}
	// Update all windows belonging to this session.
	for _, w := range m.windows {
		if w.session == oldName {
			w.session = newName
		}
	}
	if m.currentSessionSet && m.currentSession == oldName {
		m.currentSession = newName
	}
	return nil
}

func (m *Mock) ListSessions() ([]Session, error) {
	m.record("ListSessions")
	var result []Session
	for name := range m.sessions {
		result = append(result, Session{Name: name})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (m *Mock) SetSessionOption(session string, option string, value string) error {
	m.record("SetSessionOption", session, option, value)
	if !m.sessions[session] {
		return fmt.Errorf("session %q not found", session)
	}
	opts, ok := m.sessionOptions[session]
	if !ok {
		opts = make(map[string]string)
		m.sessionOptions[session] = opts
	}
	opts[option] = value
	return nil
}

func (m *Mock) GetSessionOption(session string, option string) (string, error) {
	m.record("GetSessionOption", session, option)
	if !m.sessions[session] {
		return "", nil
	}
	if opts, ok := m.sessionOptions[session]; ok {
		return opts[option], nil
	}
	return "", nil
}

// --- Windows ---

func (m *Mock) NewWindow(session string, name string, cwd string) (string, error) {
	m.record("NewWindow", session, name, cwd)
	if !m.sessions[session] {
		return "", fmt.Errorf("session %q not found", session)
	}
	id := m.nextWindowID()
	index := 0
	for _, w := range m.windows {
		if w.session == session && w.index >= index {
			index = w.index + 1
		}
	}
	w := &mockWindow{
		id:      id,
		name:    name,
		session: session,
		index:   index,
		options: make(map[string]string),
	}
	// Create the initial pane for this window.
	paneID := m.nextPaneID()
	p := &mockPane{
		id:       paneID,
		windowID: id,
		pid:      m.nextPID(),
		active:   true,
	}
	w.layout = newLeaf(paneID)
	m.windows[id] = w
	m.panes[paneID] = p
	return id, nil
}

func (m *Mock) KillWindow(windowID string) error {
	m.record("KillWindow", windowID)
	if m.OnKill != nil {
		m.OnKill("KillWindow", windowID)
	}
	w, ok := m.windows[windowID]
	if !ok {
		return fmt.Errorf("window %q not found", windowID)
	}
	for _, pid := range panesInOrder(w.layout) {
		delete(m.panes, pid)
	}
	delete(m.windows, windowID)
	return nil
}

func (m *Mock) RenameWindow(windowID string, name string) error {
	m.record("RenameWindow", windowID, name)
	w, ok := m.windows[windowID]
	if !ok {
		return fmt.Errorf("window %q not found", windowID)
	}
	w.name = name
	return nil
}

func (m *Mock) SetWindowOption(windowID string, option string, value string) error {
	m.record("SetWindowOption", windowID, option, value)
	w, ok := m.windows[windowID]
	if !ok {
		return fmt.Errorf("window %q not found", windowID)
	}
	w.options[option] = value
	return nil
}

func (m *Mock) UnsetWindowOption(windowID string, option string) error {
	m.record("UnsetWindowOption", windowID, option)
	w, ok := m.windows[windowID]
	if !ok {
		return fmt.Errorf("window %q not found", windowID)
	}
	delete(w.options, option)
	return nil
}

func (m *Mock) GetWindowOption(windowID string, option string) (string, error) {
	m.record("GetWindowOption", windowID, option)
	w, ok := m.windows[windowID]
	if !ok {
		return "", fmt.Errorf("window %q not found", windowID)
	}
	val, ok := w.options[option]
	if !ok {
		return "", fmt.Errorf("option %q not set on window %q", option, windowID)
	}
	return val, nil
}

func (m *Mock) WaitingOrBellWindowIDs(session string) (map[string]bool, error) {
	m.record("WaitingOrBellWindowIDs", session)
	if !m.sessions[session] {
		return nil, fmt.Errorf("session %q not found", session)
	}
	result := make(map[string]bool)
	for id, w := range m.windows {
		if w.session != session {
			continue
		}
		if w.options["@bay-waiting"] == "1" || w.options["@bay-bell"] == "1" {
			result[id] = true
		}
	}
	return result, nil
}

func (m *Mock) ListWindows(session string) ([]Window, error) {
	m.record("ListWindows", session)
	if !m.sessions[session] {
		return nil, fmt.Errorf("session %q not found", session)
	}
	var result []Window
	for _, w := range m.windows {
		if w.session == session {
			result = append(result, Window{
				ID:    w.id,
				Name:  w.name,
				Index: w.index,
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Index < result[j].Index
	})
	return result, nil
}

func (m *Mock) WindowExists(windowID string) (bool, error) {
	m.record("WindowExists", windowID)
	_, ok := m.windows[windowID]
	return ok, nil
}

func (m *Mock) FindWindowByName(session string, name string) (string, error) {
	m.record("FindWindowByName", session, name)
	if !m.sessions[session] {
		return "", fmt.Errorf("session %q not found", session)
	}
	for _, w := range m.windows {
		if w.session == session && w.name == name {
			return w.id, nil
		}
	}
	return "", fmt.Errorf("window %q not found in session %q", name, session)
}

func (m *Mock) SelectWindow(windowID string) error {
	m.record("SelectWindow", windowID)
	if _, ok := m.windows[windowID]; !ok {
		return fmt.Errorf("window %q not found", windowID)
	}
	return nil
}

// --- Panes ---

func (m *Mock) SplitWindow(targetID string, dir string, cwd string, before bool) (string, error) {
	method := "SplitWindow"
	if before {
		method = "SplitWindowBefore"
	}
	m.record(method, targetID, dir, cwd)

	// Resolve targetID: it may be a pane ID (split that specific leaf) or
	// a window ID (split the active pane — approximated with the first
	// leaf in the tree).
	var w *mockWindow
	var targetLeaf *LayoutNode
	if p, ok := m.panes[targetID]; ok {
		w = m.windows[p.windowID]
		if w != nil {
			targetLeaf, _ = findLeaf(w.layout, nil, targetID)
		}
	} else if win, ok := m.windows[targetID]; ok {
		w = win
		targetLeaf = firstLeaf(w.layout)
	}
	if w == nil || targetLeaf == nil {
		return "", fmt.Errorf("target %q not found", targetID)
	}

	paneID := m.nextPaneID()
	p := &mockPane{
		id:       paneID,
		windowID: w.id,
		pid:      m.nextPID(),
		active:   false,
	}
	if before {
		// -fb wraps the entire window's tree in a new split, with the
		// new pane at the front (top/left). The target leaf is no longer
		// special — only the window matters.
		w.layout = &LayoutNode{
			Dir:      dir,
			Children: []*LayoutNode{newLeaf(paneID), w.layout},
		}
	} else {
		splitLeaf(targetLeaf, paneID, dir, false)
	}
	m.panes[paneID] = p
	return paneID, nil
}

func (m *Mock) SelectPane(paneID string) error {
	m.record("SelectPane", paneID)
	if _, ok := m.panes[paneID]; !ok {
		return fmt.Errorf("pane %q not found", paneID)
	}
	return nil
}

func (m *Mock) KillPane(paneID string) error {
	m.record("KillPane", paneID)
	if m.OnKill != nil {
		m.OnKill("KillPane", paneID)
	}
	p, ok := m.panes[paneID]
	if !ok {
		return fmt.Errorf("pane %q not found", paneID)
	}
	// Remove the leaf from the window's layout tree, collapsing a
	// now-singleton parent split into its surviving child. If the leaf
	// was the only pane in the window, the layout becomes nil — matching
	// real tmux, which auto-kills the window on the last pane's death.
	if w, ok := m.windows[p.windowID]; ok {
		leaf, parent := findLeaf(w.layout, nil, paneID)
		if leaf != nil {
			w.layout = removeLeaf(w.layout, parent, leaf)
		}
	}
	delete(m.panes, paneID)
	return nil
}

func (m *Mock) SendKeys(paneID string, keys string) error {
	m.record("SendKeys", paneID, keys)
	if _, ok := m.panes[paneID]; !ok {
		return fmt.Errorf("pane %q not found", paneID)
	}
	return nil
}

func (m *Mock) CapturePane(paneID string, lines int) (string, error) {
	m.record("CapturePane", paneID, fmt.Sprintf("%d", lines))
	p, ok := m.panes[paneID]
	if !ok {
		return "", fmt.Errorf("pane %q not found", paneID)
	}
	return p.capture, nil
}

func (m *Mock) RespawnPane(paneID string, cwd string, command string) error {
	m.record("RespawnPane", paneID, cwd, command)
	if _, ok := m.panes[paneID]; !ok {
		return fmt.Errorf("pane %q not found", paneID)
	}
	return nil
}

// LayoutString returns a parenthesized string representation of the
// window's layout for compact test assertions. Slash-separated for
// vertical splits, pipe-separated for horizontal, parens around nested
// internal nodes; leaves are pane IDs. Empty string for an empty window.
func (m *Mock) LayoutString(windowID string) (string, error) {
	w, ok := m.windows[windowID]
	if !ok {
		return "", fmt.Errorf("window %q not found", windowID)
	}
	return layoutToString(w.layout), nil
}

func (m *Mock) ListPanes(windowID string) ([]Pane, error) {
	m.record("ListPanes", windowID)
	w, ok := m.windows[windowID]
	if !ok {
		return nil, fmt.Errorf("window %q not found", windowID)
	}
	var result []Pane
	for _, pid := range panesInOrder(w.layout) {
		p := m.panes[pid]
		result = append(result, Pane{
			ID:     p.id,
			PID:    p.pid,
			Active: p.active,
			Dead:   p.dead,
		})
	}
	return result, nil
}

// SetPaneDead marks a pane as dead (its process exited under
// remain-on-exit), as reported by ListPanes.
func (m *Mock) SetPaneDead(paneID string, dead bool) {
	if p, ok := m.panes[paneID]; ok {
		p.dead = dead
	}
}

func (m *Mock) GetPanePID(paneID string) (int, error) {
	m.record("GetPanePID", paneID)
	p, ok := m.panes[paneID]
	if !ok {
		return 0, fmt.Errorf("pane %q not found", paneID)
	}
	return p.pid, nil
}

func (m *Mock) PaneExists(paneID string) (bool, error) {
	m.record("PaneExists", paneID)
	_, ok := m.panes[paneID]
	return ok, nil
}

func (m *Mock) GetPaneCursorY(paneID string) (int, error) {
	m.record("GetPaneCursorY", paneID)
	p, ok := m.panes[paneID]
	if !ok {
		return 0, fmt.Errorf("pane %q not found", paneID)
	}
	return p.cursorY, nil
}

// SetPaneCursorY sets the cursor Y position for a pane (test helper).
func (m *Mock) SetPaneCursorY(paneID string, y int) {
	if p, ok := m.panes[paneID]; ok {
		p.cursorY = y
	}
}

// --- Current context ---

func (m *Mock) CurrentSession() (string, error) {
	m.record("CurrentSession")
	if !m.currentSessionSet {
		return "", fmt.Errorf("not in a tmux session")
	}
	return m.currentSession, nil
}

func (m *Mock) CurrentWindowID() (string, error) {
	m.record("CurrentWindowID")
	if !m.currentWindowIDSet {
		return "", fmt.Errorf("not in a tmux window")
	}
	return m.currentWindowID, nil
}

func (m *Mock) CurrentPaneID() (string, error) {
	m.record("CurrentPaneID")
	if !m.currentPaneIDSet {
		return "", fmt.Errorf("not in a tmux pane")
	}
	return m.currentPaneID, nil
}

// --- Client display ---

func (m *Mock) DisplayMessageAsync(msg string, durationMs int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, Call{Method: "DisplayMessageAsync", Args: []string{msg, strconv.Itoa(durationMs)}})
	m.displayMessages = append(m.displayMessages, msg)
	return nil
}

func (m *Mock) DisplayMessage(msg string, durationMs int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, Call{Method: "DisplayMessage", Args: []string{msg, strconv.Itoa(durationMs)}})
	m.displayMessages = append(m.displayMessages, msg)
	return nil
}

func (m *Mock) FindDockEditorWindow(session string) (string, bool) {
	m.record("FindDockEditorWindow", session)
	return "", false
}

func (m *Mock) MoveWindow(windowID string, targetIndex int) error {
	m.record("MoveWindow", windowID, fmt.Sprintf("%d", targetIndex))
	return nil
}

func (m *Mock) MoveWindowAfter(windowID string, afterWindowID string) error {
	m.record("MoveWindowAfter", windowID, afterWindowID)
	return nil
}

func (m *Mock) DisplayPopup(cmd string) error {
	m.record("DisplayPopup", cmd)
	return nil
}

func (m *Mock) ClientWidth() (int, error) {
	m.record("ClientWidth")
	if m.clientWidth <= 0 {
		return 100, nil
	}
	return m.clientWidth, nil
}

func (m *Mock) StatusReservedCells() (int, error) {
	m.record("StatusReservedCells")
	if m.statusReservedCells <= 0 {
		return 50, nil
	}
	return m.statusReservedCells, nil
}

// --- Mock helpers (not part of Interface) ---

// SetCurrentSession sets the value returned by CurrentSession.
func (m *Mock) SetCurrentSession(name string) {
	m.currentSession = name
	m.currentSessionSet = true
}

// SetCurrentWindowID sets the value returned by CurrentWindowID.
func (m *Mock) SetCurrentWindowID(id string) {
	m.currentWindowID = id
	m.currentWindowIDSet = true
}

// SetCurrentPaneID sets the value returned by CurrentPaneID.
func (m *Mock) SetCurrentPaneID(id string) {
	m.currentPaneID = id
	m.currentPaneIDSet = true
}

// SetCaptureContent sets the content that CapturePane will return for a pane.
func (m *Mock) SetCaptureContent(paneID string, content string) {
	if p, ok := m.panes[paneID]; ok {
		p.capture = content
	}
}

// SetClientWidth sets the value returned by ClientWidth.
func (m *Mock) SetClientWidth(w int) {
	m.clientWidth = w
}

// SetStatusReservedCells sets the value returned by StatusReservedCells.
func (m *Mock) SetStatusReservedCells(c int) {
	m.statusReservedCells = c
}

// DisplayMessages returns a copy of the messages recorded via DisplayMessage,
// in call order.
func (m *Mock) DisplayMessages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.displayMessages))
	copy(out, m.displayMessages)
	return out
}

// Reset clears all tmux mock state (simulates reboot).
func (m *Mock) Reset() {
	m.Calls = nil
	m.sessions = make(map[string]bool)
	m.sessionOptions = make(map[string]map[string]string)
	m.windows = make(map[string]*mockWindow)
	m.panes = make(map[string]*mockPane)
	m.windowCounter = 0
	m.paneCounter = 0
	m.pidCounter = 0
	m.currentSessionSet = false
	m.currentWindowIDSet = false
	m.currentPaneIDSet = false
	m.clientWidth = 0
	m.displayMessages = nil
}

// HasSessionCalled returns true if NewSession was called with the given name
// (i.e. the session currently exists).
func (m *Mock) HasSessionCalled(name string) bool {
	return m.sessions[name]
}
