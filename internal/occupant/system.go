package occupant

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// RealSystem reads the live machine with ps and lsof.
type RealSystem struct{}

var _ System = RealSystem{}

// psEnv pins ps output to UTC and the C locale, so lstart parses and
// compares with the session record's UTC procStart.
func psEnv() []string {
	return append(os.Environ(), "TZ=UTC", "LC_ALL=C")
}

// Processes walks the whole table with ps rather than `pgrep -P`, which
// returns nothing inside some sandboxes.
func (RealSystem) Processes() ([]Process, error) {
	cmd := exec.Command("ps", "-A", "-o", "pid=,ppid=,lstart=,comm=")
	cmd.Env = psEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return ParsePS(out), nil
}

// ParsePS parses `ps -A -o pid=,ppid=,lstart=,comm=` output. lstart is
// five fields ("Fri Nov 20 18:20:59 2026"); comm is the rest of the line
// and may contain spaces. Unparseable lines are skipped.
func ParsePS(out []byte) []Process {
	var procs []Process
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		start, err3 := ParseStart(strings.Join(f[2:7], " "))
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		procs = append(procs, Process{PID: pid, PPID: ppid, Start: start, Comm: strings.Join(f[7:], " ")})
	}
	return procs
}

// UnixSockets lists the named unix sockets the process has open.
func (RealSystem) UnixSockets(pid int) ([]string, error) {
	out, err := exec.Command("lsof", "-a", "-U", "-p", strconv.Itoa(pid), "-F", "n").Output()
	return lsofResult(out, err)
}

// lsofResult interprets an lsof run. lsof exits 1 with no output at all
// when nothing matches, which includes a process that has exited: that
// is an empty list. Exit 1 with a diagnostic on stderr (a permission
// problem, say), or any other failure, is an inspection error.
func lsofResult(out []byte, err error) ([]string, error) {
	if err != nil {
		var exitErr *exec.ExitError
		noMatch := errors.As(err, &exitErr) && exitErr.ExitCode() == 1 &&
			len(bytes.TrimSpace(out)) == 0 && len(bytes.TrimSpace(exitErr.Stderr)) == 0
		if !noMatch {
			return nil, fmt.Errorf("lsof: %w", err)
		}
	}
	return ParseLsofNames(out), nil
}

// ParseLsofNames extracts socket paths from `lsof -F n` output: lines
// "n/path" are named sockets; "n->0x…" are peer endpoints and skipped.
// Linux lsof appends " type=STREAM" (or another socket type) to a unix
// socket's name; that annotation is not part of the path. Only the exact
// socket-type names are stripped, so a path that happens to contain
// " type=" is kept whole.
func ParseLsofNames(out []byte) []string {
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "n/") {
			continue
		}
		paths = append(paths, linuxSocketType.ReplaceAllString(line[1:], ""))
	}
	return paths
}

// linuxSocketType matches the socket-type annotation Linux lsof appends.
var linuxSocketType = regexp.MustCompile(` type=(?:STREAM|DGRAM|SEQPACKET|RAW|RDM|PACKET|UNKNOWN)$`)

// Getenv reads one variable from another process's environment: from
// /proc on Linux, from `ps -E` on macOS.
func (RealSystem) Getenv(pid int, key string) (string, error) {
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)); err == nil {
		for _, kv := range bytes.Split(data, []byte{0}) {
			if v, ok := strings.CutPrefix(string(kv), key+"="); ok {
				return v, nil
			}
		}
		return "", nil
	}
	cmd := exec.Command("ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid))
	cmd.Env = psEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ps -E: %w", err)
	}
	return EnvFromPSE(out, key), nil
}

// EnvFromPSE finds key in `ps -E -o command=` output, where the
// environment follows the command's arguments, space-separated with no
// quoting. The last occurrence wins, since environment entries come
// after the arguments. A value runs up to the next " NAME=" that looks
// like another variable, so a path containing spaces survives; a value
// that itself contains " NAME=" is cut there.
func EnvFromPSE(out []byte, key string) string {
	line := strings.TrimRight(string(out), "\n")
	starts := envStartRE.FindAllStringSubmatchIndex(line, -1)
	val := ""
	for i, m := range starts {
		if line[m[2]:m[3]] != key {
			continue
		}
		end := len(line)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		val = line[m[1]:end]
	}
	return val
}

// envStartRE matches the start of one environment entry: start of line
// or a space, then NAME=. Group 1 is NAME.
var envStartRE = regexp.MustCompile(`(?:^| )([A-Za-z_][A-Za-z0-9_]*)=`)

func (RealSystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (RealSystem) HomeDir() (string, error) { return os.UserHomeDir() }
