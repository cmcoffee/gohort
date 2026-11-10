package orchestrate

import (
	"testing"

)

// The memory gate's cases go through the gate's own prompt and reading.
func TestTheMemoryGateCasesRunThroughTheGate(t *testing.T) {
	caseAnswers(t, "memory.gate", nil,
		caseReply{"moved", "keep fact, replaces 1", []string{`{"relevant": true, "supersedes": [1], "kind": "fact"}`}},
		caseReply{"small-talk", "drop", []string{`{"relevant": false, "supersedes": [], "kind": "fact"}`}})
}
