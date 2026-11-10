package orchestrate

// The objective judge's check tests (core/checktest): a recurring task's
// goal, what one attempt ran and reported, with whether the goal is met,
// asked through the judge's own prompt (judgeObjective) on the model a run
// is for.

import (
	"context"
	"strconv"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

func init() {
	checktest.Register(checktest.Check{
		Key:   objectiveJudgeRoute,
		Label: "Objective judge",
		Cases: objectiveCases,
		Run:   runObjectiveCase,
	})
}

// objectiveCases are the judge's built-in cases. Input: goal, tools (comma
// separated), errors (failed calls), reply. Want: "met" or "not_yet".
func objectiveCases() []checktest.Case {
	c := func(id, title, want, goal, tools, errors, reply string) checktest.Case {
		return checktest.Case{ID: id, Title: title, Want: want,
			Input: map[string]string{"goal": goal, "tools": tools, "errors": errors, "reply": reply}}
	}
	post := "Post the weekly status update to the team channel."
	return []checktest.Case{
		c("done", "The action that meets it ran", "met", post, "post_message", "0",
			"Posted the weekly status update to the team channel."),
		c("claimed", "Reported done with nothing run", "not_yet", post, "", "0",
			"Posted the weekly status update."),
		c("waiting", "Asked, with no answer yet", "not_yet", "Get a reply from the venue confirming the booking.", "send_email", "0",
			"Emailed the venue asking them to confirm the booking. No reply yet."),
		c("an-answer", "A goal that asks only for an answer", "met", "Say in one sentence why yesterday's build failed.", "read_log", "0",
			"The build failed because a test timed out waiting for the database to start."),
		c("part-way", "Part of the work done", "not_yet", "Translate all five pages of the guide into Spanish.", "translate, translate, translate", "0",
			"Translated pages 1 to 3 into Spanish. Pages 4 and 5 are next."),
		c("failed", "The one action failed", "not_yet", "Upload the report to the shared folder.", "upload_file", "1",
			"Uploaded the report to the shared folder."),
	}
}

// runObjectiveCase asks one case on llm through the judge itself.
func runObjectiveCase(ctx context.Context, llm LLM, tc checktest.Case) checktest.Outcome {
	app := &OrchestrateApp{}
	app.LLM = llm
	in := tc.Input
	errors, _ := strconv.Atoi(in["errors"])
	v, ok := app.judgeObjective(ctx, objectiveEvidence{
		Objective: in["goal"], Reply: in["reply"], ToolCalls: caseList(in["tools"]), ToolErrors: errors, Attempt: 1, MaxAttempts: 3,
	})
	switch {
	case !ok:
		return checktest.Outcome{Got: "no opinion"}
	case v.Met:
		return checktest.Outcome{Got: "met", Reason: v.Reason}
	}
	return checktest.Outcome{Got: "not_yet", Reason: v.Reason}
}
