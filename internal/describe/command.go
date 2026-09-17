package describe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/commonfabric/bay/internal/config"
)

// commandSummarizer is the default Summarizer: it runs the configured
// argv (DescribeConfig.Command) with the prompt appended as the final
// argument, then returns the result. Two optional tokens make it work
// across CLIs without bay knowing their flags:
//
//   - "{prompt}" — if present in any element, the prompt is substituted
//     there instead of appended.
//   - "{out}" — if present, bay substitutes a temp file path and reads
//     the answer from that file (codex's stdout carries status chatter,
//     so the default writes the final message to {out}); otherwise the
//     answer is read from stdout.
type commandSummarizer struct {
	config *config.Config
}

func (c commandSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	argv := config.DescribeConfig{}.EffectiveCommand()
	if c.config != nil {
		argv = c.config.Describe.EffectiveCommand()
	}
	if len(argv) == 0 {
		return "", fmt.Errorf("describe command is empty")
	}

	// If the command captures to a file via {out}, create it up front.
	var outPath string
	for _, a := range argv {
		if strings.Contains(a, "{out}") {
			f, err := os.CreateTemp("", "bay-describe-*.txt")
			if err != nil {
				return "", fmt.Errorf("creating output file: %w", err)
			}
			outPath = f.Name()
			f.Close()
			defer os.Remove(outPath)
			break
		}
	}

	final := make([]string, 0, len(argv)+1)
	usesPrompt := false
	for _, a := range argv {
		if outPath != "" {
			a = strings.ReplaceAll(a, "{out}", outPath)
		}
		if strings.Contains(a, "{prompt}") {
			a = strings.ReplaceAll(a, "{prompt}", prompt)
			usesPrompt = true
		}
		final = append(final, a)
	}
	if !usesPrompt {
		final = append(final, prompt)
	}

	cmd := exec.CommandContext(ctx, final[0], final[1:]...)
	cmd.Dir = "/" // the summarizer runs nowhere in particular
	cmd.Env = summarizerEnv(final[0])
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", final[0], err, strings.TrimSpace(stderr.String()))
	}

	out := stdout.String()
	if outPath != "" {
		data, err := os.ReadFile(outPath)
		if err != nil {
			return "", fmt.Errorf("reading summarizer output: %w", err)
		}
		out = string(data)
	}
	// A clean exit with no output means the command ran but produced
	// nothing — usually a misconfigured command/model (e.g. it printed
	// to stdout while {out} captured a file). Surface it as an error so
	// it's logged and paced, not silently mistaken for "nothing to say".
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("%s produced no output", final[0])
	}
	return out, nil
}

// summarizerEnv repairs the stale PATH inherited by a long-running monitor
// when a configured CLI uses a runtime-manager install. Managed CLIs may put
// their env-based shebang runtime beside the resolved script, or—as mise does
// for separately managed tools—in a sibling install tree. For example:
//
//	.../mise/installs/npm-openai-codex/latest/bin/codex
//	.../mise/installs/node/latest/bin/node
//
// A monitor started before Node was activated can find codex but not node.
// Detect both layouts from the command's shebang and prepend the matching
// runtime directory for the child only.
func summarizerEnv(command string) []string {
	env := os.Environ()
	executable, err := exec.LookPath(command)
	if err != nil {
		return env
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return env
	}
	interpreter := envShebangInterpreter(executable)
	if interpreter == "" {
		return env
	}
	if _, err := exec.LookPath(interpreter); err == nil {
		return env
	}
	for _, dir := range []string{
		filepath.Dir(resolved),
		managedRuntimeDir(executable, interpreter),
	} {
		if isExecutable(filepath.Join(dir, interpreter)) {
			return withPathPrefix(env, dir)
		}
	}
	return env
}

// envShebangInterpreter returns the command named after /usr/bin/env in an
// executable's shebang. EvalSymlinks is important because managed commands are
// commonly stable symlinks to scripts elsewhere in the install.
func envShebangInterpreter(executable string) string {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return ""
	}
	line := string(data)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "#!")))
	if len(fields) < 2 || filepath.Base(fields[0]) != "env" {
		return ""
	}
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") || strings.Contains(field, "=") {
			continue
		}
		return field
	}
	return ""
}

func managedRuntimeDir(executable, interpreter string) string {
	clean := filepath.Clean(executable)
	separator := string(filepath.Separator) + "installs" + string(filepath.Separator)
	i := strings.Index(clean, separator)
	if i < 0 {
		return ""
	}
	installRoot := clean[:i+len(separator)]
	return filepath.Join(installRoot, interpreter, "latest", "bin")
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func withPathPrefix(env []string, dir string) []string {
	currentPath := os.Getenv("PATH")
	for _, entry := range filepath.SplitList(currentPath) {
		if entry == dir {
			return env
		}
	}
	path := dir
	if currentPath != "" {
		path += string(os.PathListSeparator) + currentPath
	}
	for i, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			env[i] = "PATH=" + path
			return env
		}
	}
	return append(env, "PATH="+path)
}
