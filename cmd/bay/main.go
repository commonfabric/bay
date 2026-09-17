package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/commonfabric/bay/internal/cli"
)

// version is set at build time via -ldflags "-X main.version=v0.1.0".
// If not set, versionString() derives it from Go's embedded build info.
var version = ""

func versionString() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}

	v := info.Main.Version
	if v == "" || v == "(devel)" {
		v = "dev"
	}

	// Append VCS revision if available
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) > 8 {
				rev = s.Value[:8]
			} else {
				rev = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev != "" {
		v += " (" + rev + dirty + ")"
	}
	return v
}

func main() {
	root := cli.NewRootCmd(versionString())
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
