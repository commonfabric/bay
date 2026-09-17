package describe

import (
	"context"
	"os"
	"path/filepath"
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
