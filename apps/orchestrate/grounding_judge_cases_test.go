package orchestrate

import (
	"testing"

)

// The grounding judge's cases go through the judge itself.
func TestTheGroundingCasesRunThroughTheJudge(t *testing.T) {
	caseAnswers(t, groundingJudgeRoute, []string{"asserted", "clean"},
		caseReply{"stated-as-fact", "asserted", []string{`{"verdict":"ASSERTED","claim":"The office move is set for March 3","basis":"Alex said the office move is set for March 3."}`}},
		caseReply{"attributed", "clean", []string{`{"verdict":"CLEAN","claim":"","basis":""}`}})
}
