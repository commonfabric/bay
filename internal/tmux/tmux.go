// Package tmux provides an interface for tmux operations needed by bay.
package tmux

// Interface defines tmux operations that bay needs.
type Interface interface {
	// Sessions
	HasSession(name string) (bool, error)
	NewSession(name string) error
	KillSession(name string) error
	RenameSession(oldName string, newName string) error
	ListSessions() ([]Session, error)
	SetSessionOption(session string, option string, value string) error
	// GetSessionOption returns the value of a session-scoped user option.
	// Returns "" with nil error when the option is unset (the normal
	// "untagged" state for an existing session).
	GetSessionOption(session string, option string) (string, error)

	// Windows
	NewWindow(session string, name string, cwd string) (string, error) // returns tmux window ID like "@4"
	KillWindow(windowID string) error
	RenameWindow(windowID string, name string) error
	SetWindowOption(windowID string, option string, value string) error
	UnsetWindowOption(windowID string, option string) error
	GetWindowOption(windowID string, option string) (string, error)
	ListWindows(session string) ([]Window, error)
	WindowExists(windowID string) (bool, error)
	FindWindowByName(session string, name string) (string, error) // returns window ID
	SelectWindow(windowID string) error

	// Panes
	SelectPane(paneID string) error
	// SplitWindow splits a tmux window or pane, returning the new pane ID.
	// When before is true, uses `split-window -fb` to insert the new pane
	// at the root position of the layout group (used by undo-close to
	// restore a closed root pane in place).
	SplitWindow(targetID string, dir string, cwd string, before bool) (string, error)
	KillPane(paneID string) error
	SendKeys(paneID string, keys string) error
	CapturePane(paneID string, lines int) (string, error)
	RespawnPane(paneID string, cwd string, command string) error
	ListPanes(windowID string) ([]Pane, error)
	GetPanePID(paneID string) (int, error)

	// Pane state
	PaneExists(paneID string) (bool, error)
	GetPaneCursorY(paneID string) (int, error)

	// Current context
	CurrentSession() (string, error)
	CurrentWindowID() (string, error)
	CurrentPaneID() (string, error)

	// WaitingOrBellWindowIDs returns windows in the session that are waiting
	// for user attention. Two signals are merged because different agents
	// surface readiness differently: Codex rings the terminal bell natively;
	// Claude can bell via Bay's PermissionRequest hook and is also covered
	// by regex prompt patterns; other prompt-driven agents rely on
	// @bay-waiting from the generic regex monitor.
	WaitingOrBellWindowIDs(session string) (map[string]bool, error)

	MoveWindow(windowID string, targetIndex int) error
	MoveWindowAfter(windowID string, afterWindowID string) error
	FindDockEditorWindow(session string) (string, bool) // returns window ID if @bay-dock-editor=1 exists

	// Client display. durationMs=0 uses tmux's display-time default.
	DisplayMessage(msg string, durationMs int) error
	// DisplayMessageAsync fires tmux display-message without waiting for
	// the child process to return. Use from latency-sensitive contexts
	// (e.g. run-shell keybindings that also do visible tmux work) where
	// the message is purely advisory and blocking on the tmux RPC would
	// delay other UI updates tmux is holding back until run-shell exits.
	DisplayMessageAsync(msg string, durationMs int) error
	DisplayPopup(cmd string) error
	ClientWidth() (int, error)
	// StatusReservedCells returns status-left-length + status-right-length —
	// the maximum cells reserved for status-left and status-right sections.
	// Used to compute how much width is actually available for window tabs.
	StatusReservedCells() (int, error)
}

// Session represents a tmux session.
type Session struct {
	Name string
}

// Window represents a tmux window.
type Window struct {
	ID    string // tmux unique ID like "@4"
	Name  string
	Index int
}

// Pane represents a tmux pane.
type Pane struct {
	ID     string // tmux pane ID like "%5"
	PID    int
	Active bool
	// Dead is true for a pane whose process has exited but which tmux
	// keeps open (remain-on-exit). Its PID is stale and may since have
	// been given to an unrelated process.
	Dead bool
}
