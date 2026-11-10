package orchestrate

import (
	"testing"

)

// The objective judge's cases go through the judge itself.
func TestTheObjectiveCasesRunThroughTheJudge(t *testing.T) {
	caseAnswers(t, objectiveJudgeRoute, []string{"met", "not_yet"},
		caseReply{"done", "met", []string{`{"verdict":"MET","reason":"the post ran"}`}},
		caseReply{"claimed", "not_yet", []string{`{"verdict":"NOT_YET","reason":"nothing was posted"}`}})
}
