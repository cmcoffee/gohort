package orchestrate

import (
	"testing"
)

// The conflict check's cases go through the check's own prompt and reading,
// its numbers in order whatever order the model gave them.
func TestTheConflictCasesRunThroughTheCheck(t *testing.T) {
	caseAnswers(t, "memory.conflict_check", nil,
		caseReply{"two-at-once", "contradicts 1, 3", []string{`{"findings":[{"finding":3,"why":"lowered vs no effect","answer":"contradicts"},{"finding":2,"why":"enrolment","answer":"consistent"},{"finding":1,"why":"10 points vs no effect","answer":"contradicts"}]}`}},
		caseReply{"agrees", "none", []string{`{"findings":[{"finding":1,"why":"the same headcount in other words","answer":"consistent"},{"finding":2,"why":"location","answer":"consistent"}]}`}})
}
