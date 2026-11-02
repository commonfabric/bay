package engine

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/commonfabric/bay/internal/manifest"
)

func (e *Engine) validateAgentName(agentName string) error {
	if agentName == "" {
		return fmt.Errorf("agent name is required")
	}
	info, ok := e.Config.ResolveAgent(agentName)
	if !ok {
		return fmt.Errorf("unknown agent %q", agentName)
	}
	if strings.TrimSpace(info.Command) == "" {
		return fmt.Errorf("agent %q has no command configured", agentName)
	}
	return nil
}

// DefaultAgent returns the effective default agent for a dock, or "" if none.
func (e *Engine) DefaultAgent(dockName string) string {
	m, err := e.LoadManifest()
	if err != nil {
		return ""
	}
	return e.resolvedDockAgent(dockName, m)
}

func (e *Engine) resolveBayAgent(dockName string, m *manifest.Manifest, requested string, requireAgent bool) (string, error) {
	if requested != "" {
		if err := e.validateAgentName(requested); err != nil {
			return "", err
		}
		return requested, nil
	}

	agentName := e.resolvedDockAgent(dockName, m)
	if agentName == "" {
		if requireAgent {
			return "", fmt.Errorf("dock %q has no default agent configured", dockName)
		}
		return "", nil
	}
	if err := e.validateAgentName(agentName); err != nil {
		return "", err
	}
	return agentName, nil
}

// buildAgentCommand assembles the agent launch command. When resume is
// true, the agent's configured resume_args are spliced in (e.g.
// "--continue" for Claude Code) so the prior session is picked up;
// used by recovery and undo-close. launchArgs are dropped on resume:
// they carry session-start flags like --model, and replaying those
// would override state the resumed session restores itself (Claude
// keeps a resumed session on its own last-selected model).
func (e *Engine) buildAgentCommand(agentName string, agentArgs, launchArgs []string, resume bool) (string, error) {
	if err := e.validateAgentName(agentName); err != nil {
		return "", err
	}
	info, _ := e.Config.ResolveAgent(agentName)
	parts := []string{info.Command}
	if resume && info.ResumeArgs != "" {
		parts = append(parts, strings.Fields(info.ResumeArgs)...)
	}
	parts = append(parts, quoteArgs(agentArgs)...)
	if !resume {
		parts = append(parts, quoteArgs(launchArgs)...)
	}
	return strings.Join(parts, " "), nil
}

// shellSafeRe matches args that need no quoting when joined into a
// shell command line. Conservative: anything outside this set gets
// single-quoted. Deliberately keeps ~ unquoted so leading-tilde paths
// still expand.
var shellSafeRe = regexp.MustCompile(`^[a-zA-Z0-9@%+=:,./_^~-]+$`)

// quoteArgs shell-quotes each configured arg as a single word. Args
// come from TOML lists where one element is one argument, so an
// element containing spaces or quotes (e.g. an initial prompt in
// launch_args) must survive the shell as one word.
func quoteArgs(args []string) []string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if a != "" && shellSafeRe.MatchString(a) {
			quoted[i] = a
			continue
		}
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return quoted
}
