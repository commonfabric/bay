// Package occupant finds the Claude Code session running in a tmux pane
// and the messaging address other sessions can reach it at.
//
// The lookup needs no knowledge of Claude login directories:
//
//	pane root pid -> the pane's process or a descendant named "claude"
//	              -> the unix socket that process has open (uds: address)
//
// The login directory is read from the process's CLAUDE_CONFIG_DIR
// (unset means ~/.claude) and is used only to read the session record
// for display fields: session name, status, version.
//
// None of what is read here is a documented Claude Code interface. The
// session record layout and the uds: addressing were observed on
// Claude Code 2.1.329.
package occupant

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Process is one row of the process table.
type Process struct {
	PID   int
	PPID  int
	Start time.Time // process start time, UTC, second resolution
	Comm  string    // executable name or path as ps reports it
}

// System is the operating-system surface the resolver reads. The real
// implementation shells out to ps and lsof; tests supply fakes.
type System interface {
	// Processes returns the full process table.
	Processes() ([]Process, error)
	// UnixSockets returns the filesystem paths of unix sockets the
	// process has open. Unnamed and peer-only endpoints are omitted.
	UnixSockets(pid int) ([]string, error)
	// Getenv returns the value of key in the process's environment,
	// or "" when it is unset.
	Getenv(pid int, key string) (string, error)
	// ReadFile reads a file.
	ReadFile(path string) ([]byte, error)
	// HomeDir returns the current user's home directory.
	HomeDir() (string, error)
}

// Occupant describes a live Claude session.
type Occupant struct {
	PID       int
	Start     time.Time
	LoginDir  string // CLAUDE_CONFIG_DIR, or ~/.claude when unset
	Socket    string // messaging socket path
	SessionID string // from the session record; may be empty
	Name      string // session name, from the session record; may be empty
	Status    string // "busy" / "idle", from the session record; may be empty
	Version   string // Claude Code version, from the session record; may be empty
}

// Address is the SendMessage `to` value for this occupant.
func (o *Occupant) Address() string {
	if o.Socket == "" {
		return ""
	}
	return "uds:" + o.Socket
}

// claudeComm is the executable name Claude Code runs under.
const claudeComm = "claude"

func isClaude(p Process) bool {
	return filepath.Base(p.Comm) == claudeComm
}

// FindClaude returns the Claude process occupying the pane whose root
// process is panePID: the root itself when it is Claude, otherwise the
// nearest Claude descendants (a Claude started from a shell in the pane).
// Descendants of a Claude process are not considered, so a `claude -p`
// run by an agent's tool is not mistaken for the occupant. When several
// qualify, the most recently started wins. Returns nil when none does.
func FindClaude(procs []Process, panePID int) *Process {
	children := make(map[int][]int, len(procs))
	byPID := make(map[int]Process, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
		if p.PID != p.PPID {
			children[p.PPID] = append(children[p.PPID], p.PID)
		}
	}
	root, ok := byPID[panePID]
	if !ok {
		return nil
	}

	var found []Process
	seen := map[int]bool{}
	queue := []Process{root}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p.PID] {
			continue
		}
		seen[p.PID] = true
		if isClaude(p) {
			found = append(found, p)
			continue
		}
		for _, c := range children[p.PID] {
			queue = append(queue, byPID[c])
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].Start.Equal(found[j].Start) {
			return found[i].Start.After(found[j].Start)
		}
		return found[i].PID > found[j].PID
	})
	return &found[0]
}

// sessionRecord is the subset of <login>/sessions/<pid>.json read here.
type sessionRecord struct {
	PID                 int    `json:"pid"`
	SessionID           string `json:"sessionId"`
	ProcStart           string `json:"procStart"`
	Version             string `json:"version"`
	Name                string `json:"name"`
	Status              string `json:"status"`
	MessagingSocketPath string `json:"messagingSocketPath"`
}

// psTimeLayout matches `ps -o lstart` under LC_ALL=C and the session
// record's procStart, once runs of spaces are collapsed.
const psTimeLayout = "Mon Jan 2 15:04:05 2006"

// ParseStart parses a ps lstart / procStart timestamp, which is UTC
// when ps runs with TZ=UTC.
func ParseStart(s string) (time.Time, error) {
	return time.ParseInLocation(psTimeLayout, strings.Join(strings.Fields(s), " "), time.UTC)
}

// ErrNoSocket means the Claude process was found but has no messaging
// socket open and its session record names none.
var ErrNoSocket = errors.New("claude process has no messaging socket")

// Describe fills in an Occupant for a Claude process found by FindClaude.
func Describe(sys System, p Process) (*Occupant, error) {
	o := &Occupant{PID: p.PID, Start: p.Start}

	login, err := sys.Getenv(p.PID, "CLAUDE_CONFIG_DIR")
	if err != nil {
		return nil, fmt.Errorf("reading environment of pid %d: %w", p.PID, err)
	}
	if login == "" {
		home, err := sys.HomeDir()
		if err != nil {
			return nil, err
		}
		login = filepath.Join(home, ".claude")
	}
	o.LoginDir = login

	rec := readRecord(sys, login, p)
	if rec != nil {
		o.SessionID = rec.SessionID
		o.Name = rec.Name
		o.Status = rec.Status
		o.Version = rec.Version
	}

	socks, sockErr := sys.UnixSockets(p.PID)
	o.Socket = pickSocket(socks, rec)
	if o.Socket == "" {
		if sockErr != nil {
			return o, fmt.Errorf("%w (lsof: %v)", ErrNoSocket, sockErr)
		}
		return o, ErrNoSocket
	}
	return o, nil
}

// readRecord returns the session record for p, or nil when it is
// missing, unreadable, or belongs to an earlier process that had the
// same pid (its procStart differs from the live process's start).
func readRecord(sys System, login string, p Process) *sessionRecord {
	data, err := sys.ReadFile(filepath.Join(login, "sessions", fmt.Sprintf("%d.json", p.PID)))
	if err != nil {
		return nil
	}
	var rec sessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil
	}
	if rec.PID != 0 && rec.PID != p.PID {
		return nil
	}
	if rec.ProcStart != "" && !p.Start.IsZero() {
		if start, err := ParseStart(rec.ProcStart); err == nil && !start.Equal(p.Start) {
			return nil
		}
	}
	return &rec
}

// pickSocket chooses the messaging socket among the process's open unix
// sockets. The process's own sockets are authoritative; the record's
// messagingSocketPath breaks ties and is the fallback when lsof is
// unavailable. readRecord has already checked that the record belongs to
// this process, so its path is current.
func pickSocket(socks []string, rec *sessionRecord) string {
	recPath := ""
	if rec != nil {
		recPath = rec.MessagingSocketPath
	}
	for _, s := range socks {
		if s == recPath {
			return s
		}
	}
	for _, s := range socks {
		if strings.HasSuffix(s, ".sock") {
			return s
		}
	}
	if len(socks) > 0 {
		return socks[0]
	}
	return recPath
}
