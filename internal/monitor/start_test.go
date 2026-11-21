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

const startHelperEnv = "BAY_TEST_START_HELPER_DIR"

// fakeMonitorScript stands in for `bay monitor run`. It records its PID,
// reports whether it could read a line from stdin, writes to stdout and
// stderr, and stays running.
const fakeMonitorScript = `#!/bin/sh
echo "$$" >"$(dirname "$0")/child.pid"
if read -r line; then echo "read-caller-stdin" >&2; fi
echo to-stderr >&2
echo to-stdout
exec sleep 30
`

// TestStartHelper runs only as the helper process of the tests below. It
// calls Monitor.Start, with the fake script as the bay executable and the
// PID file in the helper directory, and exits.
func TestStartHelper(t *testing.T) {
	dir := os.Getenv(startHelperEnv)
	if dir == "" {
		t.Skip("helper process only")
	}
	executable = func() (string, error) { return filepath.Join(dir, "fake-bay"), nil }
	m := New(nil, "", "", filepath.Join(dir, "data", "monitor.pid"), 1)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
}

// runStartHelper runs TestStartHelper with captured stdout and a stdin
// pipe that holds an unread line, and returns once the helper has exited
// and its stdout has reached end-of-file. It fails the test if that takes
// more than 10 seconds.
func runStartHelper(t *testing.T, dir string) (stdout []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "fake-bay"), []byte(fakeMonitorScript), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStartHelper$")
	cmd.Env = append(os.Environ(), startHelperEnv+"="+dir)
	// The line is in the pipe before the helper starts, and the test
	// holds both ends until it returns, so the line stays readable for
	// as long as anything could inherit the helper's stdin.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinR.Close()
	defer stdinW.Close()
	if _, err := io.WriteString(stdinW, "caller-input\n"); err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = stdinR
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill() // a helper blocked inside Start
		killChild(t, filepath.Join(dir, "child.pid"))
	})

	// The pipe reaches end-of-file only when every holder of its write
	// end has exited. The detached child must not be one of them.
	read := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(out)
		read <- b
	}()
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
	waitForFile(t, filepath.Join(dir, "child.pid"))
	return stdout
}

func TestStart_DetachesStdinAndStdoutAndLogsStderr(t *testing.T) {
	dir := t.TempDir()
	runStartHelper(t, dir)
	logPath := filepath.Join(dir, "data", "monitor.log")
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(logPath)
		if strings.Contains(string(data), "to-stderr") {
			if strings.Contains(string(data), "read-caller-stdin") {
				t.Errorf("child read the caller's stdin; log %q", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("log %s = %q, want the child's stderr", logPath, data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStart_UnusableLogStillStartsMonitor(t *testing.T) {
	for name, setup := range map[string]func(logPath string) error{
		"read-only file": func(p string) error { return os.WriteFile(p, nil, 0o444) },
		"named pipe":     func(p string) error { return syscall.Mkfifo(p, 0o644) },
		"directory":      func(p string) error { return os.Mkdir(p, 0o755) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := setup(filepath.Join(dir, "data", "monitor.log")); err != nil {
				t.Fatal(err)
			}
			runStartHelper(t, dir) // fails unless Start returned and the child ran
		})
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never written: the child did not start", path)
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
