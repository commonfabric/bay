package monitor

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const detachHelperEnv = "BAY_TEST_START_DETACHED_HELPER"

// TestStartDetachedHelper runs only as the helper process of
// TestStartDetached_CapturedOutputEndsWhenCallerExits. It starts a
// long-lived child the way Start does, prints the child's PID, and exits.
func TestStartDetachedHelper(t *testing.T) {
	logPath := os.Getenv(detachHelperEnv)
	if logPath == "" {
		t.Skip("helper process only")
	}
	script := `echo "$$" >"$1.pid"; echo to-stderr >&2; echo to-stdout; exec sleep 30`
	if err := startDetached("/bin/sh", []string{"sh", "-c", script, "sh", logPath}, logPath); err != nil {
		t.Fatal(err)
	}
}

func TestStartDetached_CapturedOutputEndsWhenCallerExits(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "state", "monitor.log")
	cmd := exec.Command(os.Args[0], "-test.run=^TestStartDetachedHelper$")
	cmd.Env = append(os.Environ(), detachHelperEnv+"="+logPath)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killChild(t, logPath+".pid") })

	// The pipe reaches end-of-file only when every holder of its write
	// end has exited. The detached child must not be one of them.
	read := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(out)
		read <- b
	}()
	var stdout []byte
	select {
	case stdout = <-read:
	case <-time.After(10 * time.Second):
		t.Fatal("captured stdout still open after the caller exited: the detached child holds it")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v\n%s", err, stdout)
	}
	if bytes.Contains(stdout, []byte("to-stdout")) {
		t.Errorf("child wrote to the caller's stdout: %q", stdout)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(logPath)
		if strings.Contains(string(data), "to-stderr") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("log %s = %q, want the child's stderr", logPath, data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func killChild(t *testing.T, pidFile string) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(pidFile)
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && perr == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			return
		}
		if time.Now().After(deadline) {
			t.Logf("no child PID in %s; nothing to kill", pidFile)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLogPathBesidePIDFile(t *testing.T) {
	m := New(nil, "", "", "/data/bay/monitor.pid", 1)
	if got, want := m.LogPath(), "/data/bay/monitor.log"; got != want {
		t.Errorf("LogPath() = %q, want %q", got, want)
	}
}
