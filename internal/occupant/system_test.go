package occupant

import (
	"os/exec"
	"testing"
)

// TestRealUnixSockets_ExitedProcess checks against the real lsof that a
// process which has exited reads as "no sockets", not as a failed
// inspection; otherwise Describe would fall back to the exited
// session's recorded socket and report it reachable.
func TestRealUnixSockets_ExitedProcess(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not installed")
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot run true: %v", err)
	}
	socks, err := RealSystem{}.UnixSockets(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("exited process: err = %v, want nil", err)
	}
	if len(socks) != 0 {
		t.Fatalf("exited process: sockets %v", socks)
	}
}

// TestLsofResult checks how an lsof run is read, using real exit errors
// from sh: exit 1 with no output at all is "no match"; exit 1 with a
// diagnostic on stderr, or any other exit, is an inspection error. Exit
// 1 with output (some requested processes matched nothing) keeps it.
func TestLsofResult(t *testing.T) {
	run := func(script string) ([]byte, error) {
		return exec.Command("sh", "-c", script).Output()
	}
	tests := []struct {
		name    string
		script  string
		want    []string
		wantErr bool
	}{
		{"match", `printf 'p1\nf3\nn/tmp/1.sock\n'`, []string{"/tmp/1.sock"}, false},
		{"no match", `exit 1`, nil, false},
		{"partial match", `printf 'p1\nf3\nn/tmp/1.sock\n'; exit 1`, []string{"/tmp/1.sock"}, false},
		{"exit 1 with diagnostic", `echo 'lsof: WARNING: permission denied' >&2; exit 1`, nil, true},
		{"other exit", `exit 2`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := lsofResult(run(tt.script))
			got := m[1]
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if len(got) != len(tt.want) || (len(got) > 0 && got[0] != tt.want[0]) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
