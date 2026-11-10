package orchestrate

import (
	"testing"
)

// The turn judge's cases go through both of its readings.
func TestTheTurnJudgeCasesRunThroughTheJudge(t *testing.T) {
	unkept := `{"verdict":"UNKEPT","claim":"Here's your lighthouse at dusk","why":"no file sent","requested":"YES","machinery":""}`
	caseAnswers(t, turnJudgeRoute, []string{"unkept", "machinery", "kept"},
		caseReply{"no-picture", "unkept", []string{unkept, unkept}},
		caseReply{"an-answer", "kept", []string{`{"verdict":"KEPT","claim":"","why":"","requested":"NO","machinery":""}`}},
		caseReply{"still-running", "kept", []string{`{"verdict":"KEPT","claim":"","why":"","requested":"NO","machinery":""}`}})
}
