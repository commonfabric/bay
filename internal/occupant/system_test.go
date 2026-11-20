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
