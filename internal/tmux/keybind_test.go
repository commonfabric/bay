package tmux

import "testing"

func TestScopeBinding(t *testing.T) {
	got := ScopeBinding("M-c", "run-shell 'bay new -q || true'")
	want := `if -F '#{@bay-session-id}' { run-shell 'bay new -q || true' } { send-keys M-c }`
	if got != want {
		t.Errorf("ScopeBinding() = %q, want %q", got, want)
	}
}

// A top-level bind line separates commands with `\;`, but inside the
// braces of a command group tmux reads that as a literal argument and
// rejects the line ("too many arguments"). The bare `;` is what works
// there.
func TestScopeBinding_RewritesEscapedSemicolon(t *testing.T) {
	got := ScopeBinding("M-o", `display-message "hint" \; switch-client -T bay-agent`)
	want := `if -F '#{@bay-session-id}' { display-message "hint" ; switch-client -T bay-agent } { send-keys M-o }`
	if got != want {
		t.Errorf("ScopeBinding() = %q, want %q", got, want)
	}
}

func TestUnwrapScope(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		want   string
		scoped bool
	}{
		{
			name:   "round-trips what ScopeBinding writes",
			line:   ScopeBinding("M-c", "run-shell 'bay new -q || true'"),
			want:   "run-shell 'bay new -q || true'",
			scoped: true,
		},
		{
			name:   "whole bind line, not just the command tail",
			line:   "bind-key -n M-c " + ScopeBinding("M-c", "run-shell 'bay new -q || true'"),
			want:   "run-shell 'bay new -q || true'",
			scoped: true,
		},
		{
			// `tmux list-keys` prints the canonical command name and
			// re-quotes arms it parsed from quoted strings.
			name:   "tmux list-keys rendering",
			line:   `bind-key  -T root M-c   if-shell -F "#{@bay-session-id}" "run-shell 'bay new -q || true'" "send-keys M-c"`,
			want:   "run-shell 'bay new -q || true'",
			scoped: true,
		},
		{
			name:   "braces survive a list-keys round trip",
			line:   `bind-key  -T root M-c   if-shell -F "#{@bay-session-id}" { run-shell "bay new -q || true" } { send-keys M-c }`,
			want:   `run-shell "bay new -q || true"`,
			scoped: true,
		},
		{
			name:   "chord entry keeps its command list",
			line:   ScopeBinding("M-o", `display-message -d 2000 "agent: c Claude" \; switch-client -T bay-agent`),
			want:   `display-message -d 2000 "agent: c Claude" ; switch-client -T bay-agent`,
			scoped: true,
		},
		{
			name: "unscoped binding",
			line: "bind-key -n M-l next-window",
		},
		{
			// Someone else's conditional is not bay's scoping, even on
			// a key bay owns.
			name: "conditional on a different format",
			line: `bind-key -n M-c if -F '#{pane_in_mode}' { send-keys -X cancel } { run-shell 'bay new -q' }`,
		},
		{
			name: "empty",
			line: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := UnwrapScope(tc.line)
			if ok != tc.scoped {
				t.Fatalf("UnwrapScope(%q) ok = %v, want %v (got %q)", tc.line, ok, tc.scoped, got)
			}
			if got != tc.want {
				t.Errorf("UnwrapScope(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

// The scope condition must name the option bay actually stamps on its
// sessions: if the two drift, every scoped binding goes silently dead.
func TestScopeConditionUsesSessionIDOption(t *testing.T) {
	if want := "#{" + SessionIDOption + "}"; ScopeCondition != want {
		t.Errorf("ScopeCondition = %q, want %q", ScopeCondition, want)
	}
}
