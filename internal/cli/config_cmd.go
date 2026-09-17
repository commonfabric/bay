package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	"github.com/spf13/cobra"
)

// newConfigCmd is the parent for everything that touches the bay
// config file. The long help reproduces the config-format docs that
// used to live in `bay help config`, so users can still discover
// the schema via `bay config --help` or `bay help config`.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage bay's config file",
		Long:  configHelpText,
	}

	cmd.AddCommand(
		newConfigEditCmd(),
		newConfigShowCmd(),
		newConfigPathCmd(),
		newConfigEditorCmd(),
	)

	return cmd
}

func newConfigEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the bay config file in your editor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return runConfigEdit(eng)
		},
	}
}

// runConfigEdit opens the bay config file in the configured editor. Shared
// by `bay config edit` and the palette's "Edit config" entry.
func runConfigEdit(eng *engine.Engine) error {
	editorCmd, isGUI := resolveEditor(eng.Config)
	if editorCmd == "" {
		return fmt.Errorf("no editor found; set with 'bay config editor <name>', or via $VISUAL/$EDITOR")
	}
	path := bayPaths().ConfigFile
	// Fresh-install safety: create the parent directory and seed a default
	// config if the file doesn't exist, so the user opens a real file with
	// the right structure instead of an empty buffer at a non-existent path.
	if err := prepareConfigFileForEdit(path); err != nil {
		return err
	}
	_, err := launchEditor(editorCmd, isGUI, []string{path})
	return err
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the effective bay config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			out, err := configShowString(eng.Config)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the path to the bay config file",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(configPathString(bayPaths().ConfigFile))
		},
	}
}

func newConfigEditorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "editor [name]",
		Short: "Print or set the default editor",
		Long: `With no argument, prints the resolved editor command and type.
With a positional argument, sets the editor command in the config.

  bay config editor             show the current editor
  bay config editor cursor      set the editor to "cursor"
  bay config editor "nvim -u NONE"  set with arguments`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Set path: no engine needed, just touch the config file.
			if len(args) > 0 {
				if err := runConfigEditorSet(bayPaths().ConfigFile, args[0]); err != nil {
					return err
				}
				fmt.Printf("Editor set to %q\n", args[0])
				return nil
			}
			// Get path: needs the loaded config (with defaults +
			// env-var fallbacks applied) to resolve which editor bay
			// would actually use.
			eng, err := newEngine()
			if err != nil {
				return err
			}
			fmt.Println(configEditorGetString(eng.Config))
			return nil
		},
	}
}

// runConfigEditorSet loads the config at configPath, updates the
// editor command, and saves. Seeds a DefaultConfig on fresh installs.
//
// Uses errors.Is because config.Load wraps its errors with fmt.Errorf,
// and os.IsNotExist doesn't unwrap.
func runConfigEditorSet(configPath, name string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg = config.DefaultConfig()
		} else {
			return fmt.Errorf("loading config: %w", err)
		}
	}
	cfg.DefaultEditor = name
	if err := ensureConfigFileDir(configPath); err != nil {
		return err
	}
	if err := config.Save(configPath, cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	return nil
}

// configEditorGetString returns the human-readable rendering of the
// resolved editor: "<command> (terminal|GUI)" or a no-editor message.
// Format matches the old `bay edit --show` output exactly so anyone
// scripting against it sees the same lines.
func configEditorGetString(cfg *config.Config) string {
	editorCmd, isGUI := resolveEditor(cfg)
	if editorCmd == "" {
		return "No editor configured or detected."
	}
	editorType := "terminal"
	if isGUI {
		editorType = "GUI"
	}
	return fmt.Sprintf("%s (%s)", editorCmd, editorType)
}

// configPathString returns the config file path. Trivial wrapper that
// exists so the cobra Run can stay tiny and the test can target the
// pure-string layer.
func configPathString(p string) string {
	return p
}

// ensureConfigFileDir creates the parent directory for configPath
// if it doesn't already exist. Used before any operation that might
// write the config file (open in editor, save) so a fresh install
// without a ~/.config/bay/ directory doesn't fail at write time.
func ensureConfigFileDir(configPath string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	return nil
}

// prepareConfigFileForEdit ensures `bay config edit` opens a real
// file with the default config structure, not an empty buffer:
//
//  1. Creates the parent directory if missing.
//  2. If the config file doesn't exist, writes a DefaultConfig to
//     it so the user has something to start from (matches
//     `git config --edit` and the seeding `bay config editor <name>`
//     already does).
//
// If the file already exists, leaves it untouched.
func prepareConfigFileForEdit(configPath string) error {
	if err := ensureConfigFileDir(configPath); err != nil {
		return err
	}
	if _, err := os.Stat(configPath); err == nil {
		return nil // file exists, nothing to do
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking config file: %w", err)
	}
	if err := config.Save(configPath, config.DefaultConfig()); err != nil {
		return fmt.Errorf("seeding default config: %w", err)
	}
	return nil
}

// configShowString returns the effective config as a TOML document.
// Re-uses the same encoder bay's config save path uses, so the output
// round-trips back through `bay config edit`.
func configShowString(cfg *config.Config) (string, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return "", fmt.Errorf("encoding config: %w", err)
	}
	return buf.String(), nil
}
