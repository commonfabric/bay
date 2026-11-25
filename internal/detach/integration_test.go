//go:build integration

package detach

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The test binary plays the program running in the tmux window when
// DETACH_TEST_ROLE is set. It starts a child that writes a marker file
// after a second, kills its own window, and waits to be killed.
func TestMain(m *testing.M) {
	if role := os.Getenv("DETACH_TEST_ROLE"); role != "" {
		child := exec.Command("sh", "-c", "sleep 1; touch \"$DETACH_TEST_MARK\"")
		var err error
		if role == "detached" {
			err = Start(child)
		} else {
			err = child.Start()
		}
		if err != nil {
			os.Exit(2)
		}
		_ = exec.Command("tmux", "-S", os.Getenv("DETACH_TEST_SOCK"), "kill-window", "-t", os.Getenv("DETACH_TEST_WINDOW")).Run()
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runInKilledWindow runs the test binary in a tmux window on a private
// server, in the given role, and reports whether the child it started
// wrote its marker after the window was killed.
func runInKilledWindow(t *testing.T, role string) bool {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	// tmux limits socket paths to about 104 bytes, so the socket lives
	// under /tmp rather than t.TempDir.
	dir, err := os.MkdirTemp("/tmp", "baydt-")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	mark := filepath.Join(dir, "mark")
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", sock, "kill-server").Run()
		_ = os.RemoveAll(dir)
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tmux := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("tmux", append([]string{"-S", sock, "-f", "/dev/null"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	tmux("new-session", "-d", "-s", "keep")
	tmux("new-session", "-d", "-s", "dock", "-n", "w",
		"-e", "DETACH_TEST_ROLE="+role,
		"-e", "DETACH_TEST_MARK="+mark,
		"-e", "DETACH_TEST_SOCK="+sock,
		"-e", "DETACH_TEST_WINDOW==dock:w",
		exe)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(mark); err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func TestIntegration_DetachedChildOutlivesKilledWindow(t *testing.T) {
	if !runInKilledWindow(t, "detached") {
		t.Fatal("the detached child did not finish after its window was killed")
	}
}

// The control: an ordinary child is stopped when its window is killed.
// It shows that the test above can fail.
func TestIntegration_OrdinaryChildDiesWithKilledWindow(t *testing.T) {
	if runInKilledWindow(t, "ordinary") {
		t.Fatal("the ordinary child finished after its window was killed; the detached test proves nothing")
	}
}
