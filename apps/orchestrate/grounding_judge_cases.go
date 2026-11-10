package orchestrate

// The grounding judge's check tests (core/checktest): a reply beside the
// unverified notes in its prompt and what the turn ran, with whether the
// reply states one of those notes as established fact, asked through the
// judge's own prompt (judgeTurnGrounding) on the model a run is for.

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

func init() {
	checktest.Register(checktest.Check{
		Key:   groundingJudgeRoute,
		Label: "Grounding judge",
		Cases: groundingCases,
		Run:   runGroundingCase,
	})
}

// groundingCases are the judge's built-in cases. Input: notes (one a line),
// tools (comma separated), returns (one tool result a line), reply. Want:
// "asserted" or "clean".
func groundingCases() []checktest.Case {
	c := func(id, title, want, notes, tools, returns, reply string) checktest.Case {
		return checktest.Case{ID: id, Title: title, Want: want,
			Input: map[string]string{"notes": notes, "tools": tools, "returns": returns, "reply": reply}}
	}
	move := "Alex said the office move is set for March 3."
	return []checktest.Case{
		c("stated-as-fact", "An unverified note stated as fact", "asserted", move, "", "",
			"The office move is set for March 3, so plan your packing for the week before."),
		c("attributed", "The same note, said to be what was heard", "clean", move, "", "",
			"You mentioned the office move is set for March 3. I haven't confirmed it, but if it holds, plan your packing for the week before."),
		c("checked", "A note the turn looked up", "clean", "The library closes at 6 pm on Fridays.", "fetch_url",
			"fetch_url: Library hours: Monday to Thursday 9 am to 8 pm, Friday 9 am to 6 pm, closed weekends.",
			"The library closes at 6 pm on Fridays, so you have until then."),
		c("described", "A claim in something shared, described", "clean", "A meme Sam sent says the moon landing was staged.", "", "",
			"Sam's meme claims the moon landing was staged, which is the running joke between you two."),
		c("out-of-scope", "A confident reply about something else", "clean", "Jordan's flight lands at 4:10 pm.", "", "",
			"At sea level, water boils at 100 degrees Celsius."),
		c("road-closed", "A rumour passed on as fact", "asserted", "Someone in the group chat said the bridge on Route 9 is closed for repairs.", "", "",
			"Take the long way round today: the bridge on Route 9 is closed for repairs."),
	}
}

// runGroundingCase asks one case on llm through the judge itself.
func runGroundingCase(ctx context.Context, llm LLM, tc checktest.Case) checktest.Outcome {
	app := &OrchestrateApp{}
	app.LLM = llm
	in := tc.Input
	v, ok := app.judgeTurnGrounding(ctx, TurnGroundingEvidence{
		Reply:       in["reply"],
		Unchecked:   caseLines(in["notes"]),
		ToolCalls:   caseList(in["tools"]),
		ToolOutputs: caseLines(in["returns"]),
	})
	switch {
	case !ok:
		return checktest.Outcome{Got: "no opinion"}
	case v.Asserted:
		return checktest.Outcome{Got: "asserted", Reason: v.Claim + " (from: " + v.Basis + ")"}
	}
	return checktest.Outcome{Got: "clean"}
}

// caseLines is a case field of one item a line, blanks dropped.
func caseLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

// caseList is a case field of comma-separated items, blanks dropped.
func caseList(s string) []string {
	var out []string
	for _, it := range strings.Split(s, ",") {
		if it = strings.TrimSpace(it); it != "" {
			out = append(out, it)
		}
	}
	return out
}
