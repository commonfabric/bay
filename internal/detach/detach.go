// Package detach starts processes that keep running when the tmux
// window they were started from is killed.
//
// Killing a tmux window hangs up the window's terminal, and the hangup
// signal reaches the processes whose controlling terminal it is. A
// process started by Start runs in a new session with no controlling
// terminal, so the hangup does not reach it.
package detach

import (
	"os/exec"
	"syscall"
)

// Start starts cmd in a new session. The caller must not connect cmd's
// standard input, output or error to the terminal: give it files or
// leave them nil.
func Start(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	return cmd.Start()
}
