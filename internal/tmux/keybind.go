package tmux

import (
	"fmt"
	"strings"
)

// SessionIDOption is the tmux session option bay stamps on every session
// it manages (see docs/design/session-identity.md). Two things read it:
// bay itself, matching the value against the manifest to spot a tmux
// server restart, and bay's keybindings, which fire only where it is set.
const SessionIDOption = "@bay-session-id"

// ScopeCondition is the `if -F` format that tells a bay-managed session
// from any other. It resolves against the session the client is looking
// through, not the session a window was created in, so a bay window
// linked into an unmanaged session (a tmux workspace layered above bay)
// evaluates false there and true in its own dock.
const ScopeCondition = "#{" + SessionIDOption + "}"

// scopePrefixes are the spellings of the conditional bay recognizes:
// `if` is the alias bay writes into ~/.tmux.conf, `if-shell` the
// canonical name `tmux list-keys` prints back.
var scopePrefixes = []string{"if -F ", "if-shell -F "}

// ScopeBinding wraps a tmux command so it runs only in bay-managed
// sessions. Everywhere else the keystroke is handed to the pane's
// application, exactly as an unbound key would be — bay's keys stay out
// of the way in sessions it doesn't own, including a user's own
// bindings on the same keys, which they can restore by rebinding after
// bay's block.
//
// The conditional lives in the root key table rather than a bay-owned
// default key table, because a custom default table has no fallback to
// root: every personal root binding would go dead inside a dock.
func ScopeBinding(key, cmd string) string {
	// Inside `{ }` tmux lexes the contents as a command list where a
	// bare `;` separates commands. The `\;` that a top-level bind line
	// needs would be read as a literal argument here.
	cmd = strings.ReplaceAll(cmd, `\;`, ";")
	return fmt.Sprintf("if -F '%s' { %s } { send-keys %s }", ScopeCondition, cmd, key)
}

// UnwrapScope returns the command a scoped binding runs in bay-managed
// sessions, given either a whole bind-key line or just the command tail
// after the key. ok is false when the input isn't scoped by bay, in
// which case the caller should read the command as it stands.
//
// It accepts both arm syntaxes so a line survives a round trip through
// tmux: bay writes `{ ... }` groups, and `tmux list-keys` re-quotes
// arms it parsed from quoted strings.
func UnwrapScope(s string) (cmd string, ok bool) {
	rest := strings.TrimSpace(s)
	for {
		if after, found := cutAnyPrefix(rest, scopePrefixes); found {
			rest = after
			break
		}
		// Not at the conditional yet: drop one argument (which may be
		// the `bind-key -n <key>` preamble) and look again.
		_, remainder, argOK := cutArg(rest)
		if !argOK {
			return "", false
		}
		rest = strings.TrimLeft(remainder, " ")
		if rest == "" {
			return "", false
		}
	}
	cond, rest, ok := cutArg(strings.TrimLeft(rest, " "))
	if !ok || cond != ScopeCondition {
		return "", false
	}
	then, _, ok := cutArg(strings.TrimLeft(rest, " "))
	if !ok {
		return "", false
	}
	return then, true
}

func cutAnyPrefix(s string, prefixes []string) (string, bool) {
	for _, p := range prefixes {
		if after, found := strings.CutPrefix(s, p); found {
			return strings.TrimLeft(after, " "), true
		}
	}
	return "", false
}

// cutArg splits one tmux argument off the front of s and returns its
// contents plus the remainder. It handles the three forms that appear
// in a bind line: a `{ ... }` command group, a quoted string, and a
// bare word.
func cutArg(s string) (arg, rest string, ok bool) {
	if s == "" {
		return "", "", false
	}
	switch s[0] {
	case '{':
		depth := 0
		var quote byte
		for i := 0; i < len(s); i++ {
			c := s[i]
			switch {
			case quote != 0:
				if quote == '"' && c == '\\' {
					i++
					continue
				}
				if c == quote {
					quote = 0
				}
			case c == '\'' || c == '"':
				quote = c
			case c == '{':
				depth++
			case c == '}':
				depth--
				if depth == 0 {
					return strings.TrimSpace(s[1:i]), s[i+1:], true
				}
			}
		}
		return "", "", false
	case '\'', '"':
		quote := s[0]
		for i := 1; i < len(s); i++ {
			// Only double-quoted tmux strings honor backslash escapes.
			if quote == '"' && s[i] == '\\' {
				i++
				continue
			}
			if s[i] == quote {
				return s[1:i], s[i+1:], true
			}
		}
		return "", "", false
	default:
		if i := strings.IndexByte(s, ' '); i >= 0 {
			return s[:i], s[i+1:], true
		}
		return s, "", true
	}
}
