package docket

import (
	"encoding/json"
	"fmt"
)

// DoctorCommand is the command the view runs to check docket's files. It
// leaves out the check that each bay exists, which is slow; the list marks
// a bay that is gone itself.
var DoctorCommand = []string{"docket", "doctor", "--files", "--json"}

// DoctorProblems returns the number of problems in the output of
// docket doctor --json.
func DoctorProblems(out []byte) (int, error) {
	var report struct {
		Problems []json.RawMessage `json:"problems"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		return 0, fmt.Errorf("reading docket doctor's output: %w", err)
	}
	return len(report.Problems), nil
}

// DoctorNotice is the line the list shows while docket doctor finds n
// problems, or "" when it finds none.
func DoctorNotice(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "docket doctor finds 1 problem in docket's files"
	default:
		return fmt.Sprintf("docket doctor finds %d problems in docket's files", n)
	}
}
