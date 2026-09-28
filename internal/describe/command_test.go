package describe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/config"
)

func TestCommandSummarizerFindsRuntimeBesideResolvedSymlink(t *testing.T) {
	commandDir := t.TempDir()
	installDir := t.TempDir()

	runtimePath := filepath.Join(installDir, "bay-test-runtime")
	if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\nprintf 'Recovered description\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	realCommand := filepath.Join(installDir, "bay-test-summarizer")
	if err := os.WriteFile(realCommand, []byte("#!/usr/bin/env bay-test-runtime\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkedCommand := filepath.Join(commandDir, "bay-test-summarizer")
	if err := os.Symlink(realCommand, linkedCommand); err != nil {
		t.Fatal(err)
	}

	// The daemon can find the stable CLI symlink, but not the runtime named
	// by its shebang. summarizerEnv must add the symlink target's directory.
	t.Setenv("PATH", commandDir)
	cfg := &config.Config{Describe: config.DescribeConfig{Command: []string{"bay-test-summarizer"}}}
	got, err := (commandSummarizer{config: cfg}).Summarize(context.Background(), "ignored prompt")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got != "Recovered description\n" {
		t.Fatalf("output = %q, want recovered description", got)
	}
}

func TestCommandSummarizerFindsSeparateManagedRuntime(t *testing.T) {
	root := t.TempDir()
	commandDir := filepath.Join(root, "installs", "test-cli", "latest", "bin")
	runtimeDir := filepath.Join(root, "installs", "bay-test-runtime", "latest", "bin")
	if err := os.MkdirAll(commandDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runtimePath := filepath.Join(runtimeDir, "bay-test-runtime")
	if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\nprintf 'Recovered description\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(commandDir, "bay-test-summarizer")
	if err := os.WriteFile(commandPath, []byte("#!/usr/bin/env bay-test-runtime\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The daemon can find the managed CLI but not its separately managed
	// shebang runtime. The summarizer must locate the sibling install tree.
	t.Setenv("PATH", commandDir)
	cfg := &config.Config{Describe: config.DescribeConfig{Command: []string{"bay-test-summarizer"}}}
	got, err := (commandSummarizer{config: cfg}).Summarize(context.Background(), "ignored prompt")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got != "Recovered description\n" {
		t.Fatalf("output = %q, want recovered description", got)
	}
}

func TestEnvShebangInterpreterIgnoresDirectInterpreter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summarizer")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := envShebangInterpreter(path); got != "" {
		t.Fatalf("interpreter = %q, want empty", got)
	}
}

func TestCommandSummarizerErrorKeepsStderrTail(t *testing.T) {
	dir := t.TempDir()
	// Mimics codex: a banner, the echoed prompt, then the real error twice.
	script := "#!/bin/sh\n" +
		"echo 'OpenAI Codex banner' >&2\n" +
		"echo 'echoed prompt: private user request' >&2\n" +
		"echo 'ERROR: model not supported' >&2\n" +
		"echo 'ERROR: model not supported' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "bay-test-failing"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &config.Config{Describe: config.DescribeConfig{Command: []string{"bay-test-failing"}}}

	_, err := (commandSummarizer{config: cfg}).Summarize(context.Background(), "prompt")
	if err == nil {
		t.Fatal("Summarize succeeded, want error")
	}
	msg := err.Error()
	if got := strings.Count(msg, "ERROR: model not supported"); got != 1 {
		t.Errorf("error mentions the duplicated line %d times, want 1: %q", got, msg)
	}
	if !strings.Contains(msg, "exit status 1") {
		t.Errorf("error = %q, want the exit status", msg)
	}
}

func TestCommandSummarizerErrorWithoutStderr(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bay-test-silent-fail"), []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &config.Config{Describe: config.DescribeConfig{Command: []string{"bay-test-silent-fail"}}}

	_, err := (commandSummarizer{config: cfg}).Summarize(context.Background(), "prompt")
	if err == nil {
		t.Fatal("Summarize succeeded, want error")
	}
	if got, want := err.Error(), "bay-test-silent-fail: exit status 3"; got != want {
		t.Errorf("error = %q, want %q (no dangling colon)", got, want)
	}
}

func TestStderrTail(t *testing.T) {
	long := strings.Repeat("y", maxStderrLineLen+40)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "  \n\n", ""},
		{"short input kept whole", "one\ntwo\n", "one\ntwo"},
		{"keeps only the last lines", "1\n2\n3\n4\n5\n6\n", "3\n4\n5\n6"},
		{"drops consecutive duplicates", "a\nerr\nerr\n", "a\nerr"},
		{"keeps non-adjacent duplicates", "err\nx\nerr\n", "err\nx\nerr"},
		{"caps line length", long + "\n", long[:maxStderrLineLen]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stderrTail(tt.in); got != tt.want {
				t.Errorf("stderrTail(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
