package occupant

import (
	"errors"
	"io/fs"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := ParseStart(s)
	if err != nil {
		panic(err)
	}
	return t
}

// fakeSystem is an in-memory System.
type fakeSystem struct {
	procs   []Process
	sockets map[int][]string
	sockErr error
	env     map[int]map[string]string
	envErr  error
	files   map[string]string
	home    string
}

func (f *fakeSystem) Processes() ([]Process, error) { return f.procs, nil }
func (f *fakeSystem) UnixSockets(pid int) ([]string, error) {
	return f.sockets[pid], f.sockErr
}
func (f *fakeSystem) Getenv(pid int, key string) (string, error) {
	return f.env[pid][key], f.envErr
}
func (f *fakeSystem) ReadFile(path string) ([]byte, error) {
	if s, ok := f.files[path]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}
func (f *fakeSystem) HomeDir() (string, error) { return f.home, nil }

func TestFindClaude(t *testing.T) {
	t0 := at("Fri Nov 20 10:00:00 2026")
	t1 := at("Fri Nov 20 11:00:00 2026")
	tests := []struct {
		name  string
		procs []Process
		pane  int
		want  int // 0 = nil
	}{
		{
			name:  "pane process is claude",
			procs: []Process{{PID: 10, PPID: 1, Comm: "claude"}},
			pane:  10,
			want:  10,
		},
		{
			name: "claude started from a shell in the pane",
			procs: []Process{
				{PID: 10, PPID: 1, Comm: "-zsh"},
				{PID: 11, PPID: 10, Comm: "-zsh"},
				{PID: 12, PPID: 10, Comm: "claude"},
			},
			pane: 10,
			want: 12,
		},
		{
			name: "claude reported by full path",
			procs: []Process{
				{PID: 10, PPID: 1, Comm: "/bin/zsh"},
				{PID: 12, PPID: 10, Comm: "/Users/x/.local/bin/claude"},
			},
			pane: 10,
			want: 12,
		},
		{
			name: "grandchild",
			procs: []Process{
				{PID: 10, PPID: 1, Comm: "zsh"},
				{PID: 11, PPID: 10, Comm: "bash"},
				{PID: 12, PPID: 11, Comm: "claude"},
			},
			pane: 10,
			want: 12,
		},
		{
			name: "claude run by the occupant's tool is not the occupant",
			procs: []Process{
				{PID: 10, PPID: 1, Comm: "zsh"},
				{PID: 12, PPID: 10, Comm: "claude", Start: t0},
				{PID: 13, PPID: 12, Comm: "bash", Start: t1},
				{PID: 14, PPID: 13, Comm: "claude", Start: t1},
			},
			pane: 10,
			want: 12,
		},
		{
			name: "two sessions in one pane: newest wins",
			procs: []Process{
				{PID: 10, PPID: 1, Comm: "zsh"},
				{PID: 20, PPID: 10, Comm: "claude", Start: t1},
				{PID: 12, PPID: 10, Comm: "claude", Start: t0},
			},
			pane: 10,
			want: 20,
		},
		{
			name: "claude elsewhere on the machine is ignored",
			procs: []Process{
				{PID: 10, PPID: 1, Comm: "zsh"},
				{PID: 11, PPID: 10, Comm: "vim"},
				{PID: 30, PPID: 1, Comm: "claude"},
			},
			pane: 10,
			want: 0,
		},
		{
			name:  "pane pid not in table",
			procs: []Process{{PID: 30, PPID: 1, Comm: "claude"}},
			pane:  10,
			want:  0,
		},
		{
			name:  "self-parented root does not loop",
			procs: []Process{{PID: 0, PPID: 0, Comm: "kernel_task"}},
			pane:  0,
			want:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindClaude(tt.procs, tt.pane)
			switch {
			case tt.want == 0 && got != nil:
				t.Fatalf("got pid %d, want none", got.PID)
			case tt.want != 0 && got == nil:
				t.Fatalf("got none, want pid %d", tt.want)
			case tt.want != 0 && got.PID != tt.want:
				t.Fatalf("got pid %d, want %d", got.PID, tt.want)
			}
		})
	}
}

const rec88226 = `{"pid":88226,"sessionId":"ef99","procStart":"Fri Nov 20 18:20:59 2026","version":"2.1.329",
"name":"b7-fe","status":"busy","messagingSocketPath":"/tmp/cc-socks/88226.sock"}`

func TestDescribe(t *testing.T) {
	p := Process{PID: 88226, PPID: 1, Comm: "claude", Start: at("Fri Nov 20 18:20:59 2026")}

	t.Run("other login via CLAUDE_CONFIG_DIR", func(t *testing.T) {
		sys := &fakeSystem{
			home:    "/home/u",
			env:     map[int]map[string]string{88226: {"CLAUDE_CONFIG_DIR": "/home/u/.claude2"}},
			sockets: map[int][]string{88226: {"/tmp/other.sock", "/tmp/cc-socks/88226.sock"}},
			files:   map[string]string{"/home/u/.claude2/sessions/88226.json": rec88226},
		}
		o, err := Describe(sys, p)
		if err != nil {
			t.Fatal(err)
		}
		if o.LoginDir != "/home/u/.claude2" || o.Name != "b7-fe" || o.Status != "busy" ||
			o.Version != "2.1.329" || o.SessionID != "ef99" {
			t.Fatalf("unexpected occupant %+v", o)
		}
		if got := o.Address(); got != "uds:/tmp/cc-socks/88226.sock" {
			t.Fatalf("address %q", got)
		}
	})

	t.Run("unset CLAUDE_CONFIG_DIR means ~/.claude", func(t *testing.T) {
		sys := &fakeSystem{
			home:    "/home/u",
			sockets: map[int][]string{88226: {"/tmp/cc-socks/88226.sock"}},
			files:   map[string]string{"/home/u/.claude/sessions/88226.json": rec88226},
		}
		o, err := Describe(sys, p)
		if err != nil {
			t.Fatal(err)
		}
		if o.LoginDir != "/home/u/.claude" || o.Name != "b7-fe" {
			t.Fatalf("unexpected occupant %+v", o)
		}
	})

	t.Run("record from an earlier process with the same pid is ignored", func(t *testing.T) {
		later := p
		later.Start = at("Sat Nov 21 09:00:00 2026")
		sys := &fakeSystem{
			home:    "/home/u",
			sockets: map[int][]string{88226: {"/tmp/cc-socks/88226.sock"}},
			files:   map[string]string{"/home/u/.claude/sessions/88226.json": rec88226},
		}
		o, err := Describe(sys, later)
		if err != nil {
			t.Fatal(err)
		}
		if o.Name != "" || o.Status != "" {
			t.Fatalf("stale record used: %+v", o)
		}
		if o.Socket != "/tmp/cc-socks/88226.sock" {
			t.Fatalf("socket %q", o.Socket)
		}
	})

	t.Run("no record: socket from lsof alone", func(t *testing.T) {
		sys := &fakeSystem{home: "/home/u", sockets: map[int][]string{88226: {"/x/y.sock"}}}
		o, err := Describe(sys, p)
		if err != nil {
			t.Fatal(err)
		}
		if o.Address() != "uds:/x/y.sock" || o.Name != "" {
			t.Fatalf("unexpected occupant %+v", o)
		}
	})

	t.Run("lsof fails: record's socket is the fallback", func(t *testing.T) {
		sys := &fakeSystem{
			home:    "/home/u",
			sockErr: errors.New("lsof: not found"),
			files:   map[string]string{"/home/u/.claude/sessions/88226.json": rec88226},
		}
		o, err := Describe(sys, p)
		if err != nil {
			t.Fatal(err)
		}
		if o.Socket != "/tmp/cc-socks/88226.sock" {
			t.Fatalf("socket %q", o.Socket)
		}
	})

	t.Run("lsof fails and no record: an error, not unoccupied", func(t *testing.T) {
		sys := &fakeSystem{home: "/home/u", sockErr: errors.New("lsof: not found")}
		o, err := Describe(sys, p)
		if err == nil || errors.Is(err, ErrNoSocket) {
			t.Fatalf("err = %v, want an inspection error other than ErrNoSocket", err)
		}
		if o == nil || o.PID != 88226 || o.Address() != "" {
			t.Fatalf("unexpected occupant %+v", o)
		}
	})

	t.Run("lsof finds no socket: record path not trusted", func(t *testing.T) {
		sys := &fakeSystem{
			home:  "/home/u",
			files: map[string]string{"/home/u/.claude/sessions/88226.json": rec88226},
		}
		o, err := Describe(sys, p)
		if !errors.Is(err, ErrNoSocket) {
			t.Fatalf("err = %v, want ErrNoSocket", err)
		}
		if o.Address() != "" {
			t.Fatalf("address %q, want none", o.Address())
		}
	})

	t.Run("record names a socket that is not open: no other socket is used", func(t *testing.T) {
		sys := &fakeSystem{
			home:    "/home/u",
			sockets: map[int][]string{88226: {"/tmp/other.sock"}},
			files:   map[string]string{"/home/u/.claude/sessions/88226.json": rec88226},
		}
		o, err := Describe(sys, p)
		if !errors.Is(err, ErrNoSocket) {
			t.Fatalf("err = %v, want ErrNoSocket", err)
		}
		if o.Address() != "" {
			t.Fatalf("address %q, want none", o.Address())
		}
	})

	t.Run("no record: <pid>.sock chosen over an unrelated socket listed first", func(t *testing.T) {
		sys := &fakeSystem{home: "/home/u", sockets: map[int][]string{88226: {"/tmp/other.sock", "/tmp/cc-socks/88226.sock"}}}
		o, err := Describe(sys, p)
		if err != nil {
			t.Fatal(err)
		}
		if o.Socket != "/tmp/cc-socks/88226.sock" {
			t.Fatalf("socket %q", o.Socket)
		}
	})

	t.Run("several unidentifiable sockets: an error, not a guess", func(t *testing.T) {
		sys := &fakeSystem{home: "/home/u", sockets: map[int][]string{88226: {"/tmp/a.sock", "/tmp/b.sock"}}}
		if _, err := Describe(sys, p); err == nil || errors.Is(err, ErrNoSocket) {
			t.Fatalf("err = %v, want an ambiguity error", err)
		}
	})

	t.Run("unreadable environment still finds the socket", func(t *testing.T) {
		sys := &fakeSystem{
			home:    "/home/u",
			envErr:  errors.New("permission denied"),
			sockets: map[int][]string{88226: {"/tmp/cc-socks/88226.sock"}},
		}
		o, err := Describe(sys, p)
		if err != nil {
			t.Fatal(err)
		}
		if o.Address() != "uds:/tmp/cc-socks/88226.sock" || o.LoginDir != "" {
			t.Fatalf("unexpected occupant %+v", o)
		}
	})
}

func TestParsePS(t *testing.T) {
	out := []byte(`    1     0 Thu Jan  1 00:00:10 1970     /sbin/launchd
88226 87022 Fri Nov 20 18:20:59 2026     claude
  500   1 Fri Nov 20 18:20:59 2026     /Applications/Some App.app/Contents/MacOS/Some App
garbage line
`)
	procs := ParsePS(out)
	if len(procs) != 3 {
		t.Fatalf("got %d procs: %+v", len(procs), procs)
	}
	if procs[1].PID != 88226 || procs[1].PPID != 87022 || procs[1].Comm != "claude" ||
		!procs[1].Start.Equal(at("Fri Nov 20 18:20:59 2026")) {
		t.Fatalf("row 2: %+v", procs[1])
	}
	if procs[0].Start.Day() != 1 {
		t.Fatalf("padded day misparsed: %v", procs[0].Start)
	}
	if procs[2].Comm != "/Applications/Some App.app/Contents/MacOS/Some App" {
		t.Fatalf("comm with spaces: %q", procs[2].Comm)
	}
}

func TestParseLsofNames(t *testing.T) {
	out := []byte("p88226\nf9\nn->0x6298022bb12dbb12\nf13\nn/tmp/cc-socks/88226.sock\nf14\nn->0xac9d\n")
	got := ParseLsofNames(out)
	if len(got) != 1 || got[0] != "/tmp/cc-socks/88226.sock" {
		t.Fatalf("got %v", got)
	}
}

func TestEnvFromPSE(t *testing.T) {
	out := []byte("claude --flag CLAUDE_CONFIG_DIR=/arg HOME=/Users/u CLAUDE_CONFIG_DIR=/Users/u/.claude2 TERM=xterm\n")
	if got := EnvFromPSE(out, "CLAUDE_CONFIG_DIR"); got != "/Users/u/.claude2" {
		t.Fatalf("got %q", got)
	}
	spaced := []byte("claude HOME=/Users/u CLAUDE_CONFIG_DIR=/Users/u/Claude Profiles/second TERM=xterm\n")
	if got := EnvFromPSE(spaced, "CLAUDE_CONFIG_DIR"); got != "/Users/u/Claude Profiles/second" {
		t.Fatalf("spaced: got %q", got)
	}
	last := []byte("claude CLAUDE_CONFIG_DIR=/a b\n")
	if got := EnvFromPSE(last, "CLAUDE_CONFIG_DIR"); got != "/a b" {
		t.Fatalf("last entry: got %q", got)
	}
	if got := EnvFromPSE([]byte("claude HOME=/Users/u\n"), "CLAUDE_CONFIG_DIR"); got != "" {
		t.Fatalf("unset: got %q", got)
	}
}
