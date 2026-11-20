package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

// settleReply is the judge's reply ruling answer.
func settleReply(answer string) string {
	return `{"page_case":"its strongest point","document_case":"its strongest point","why":"the deciding source says so","answer":"` + answer + `"}`
}

// The settle ruling's cases go through its own prompt and reading, and want
// only the rulings it can give.
func TestTheSettleCasesRunThroughTheRuling(t *testing.T) {
	caseAnswers(t, settleVerdict.Key, []string{settlePage, settleDocument, settleBoth, settleUnsettled},
		caseReply{"primary-current", settlePage, []string{settleReply(settlePage)}},
		caseReply{"versions", settleBoth, []string{settleReply(settleBoth)}},
		caseReply{"nothing-read", settleUnsettled, []string{settleReply(settleUnsettled)}})
}

// A case reaches the judge laid out as the settle pipeline lays it out: the
// rule stage's own prompt, filled with the dispute (each side fenced, and
// named as the live run names it), the arguments, the citation check and the
// sources, on the ruling's route.
func TestASettleCaseIsLaidOutAsTheRunLaysItOut(t *testing.T) {
	if p := settleDef(false, settleVoices).Stages[2].Prompt; p != settleRulePrompt {
		t.Fatalf("the rule stage's prompt is not settleRulePrompt: %q", p)
	}
	byID := map[string]checktest.Case{}
	for _, c := range settleCases() {
		byID[c.ID] = c
	}
	for _, id := range []string{"primary-current", "outside"} {
		c := byID[id]
		llm := &FakeLLM{Turns: []FakeTurn{{Content: settleReply(c.Want)}}}
		if o := runSettleCase(context.Background(), llm, c); o.Got != c.Want {
			t.Errorf("%s: got %q", id, o.Got)
		}
		p := llm.Prompt(0)
		other := "## The collection's document: " + c.Input["other_title"]
		if id == "outside" {
			other = "## The other source: " + c.Input["other"]
		}
		for _, want := range []string{
			"## The dispute\n## The point in dispute\n" + c.Input["point"],
			"## The new page: " + c.Input["page"] + "\n",
			other + "\n",
			UntrustedFence("the new page", c.Input["page_text"]),
			"## What each side argued\n" + c.Input["argue"],
			"## How their citations held up\n" + c.Input["check"],
			"## What they read\n" + c.Input["sources"],
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s: the prompt lacks %q:\n%s", id, want, p)
			}
		}
		if cfg := llm.Config(0); cfg.RouteKey != settleVerdict.Key || cfg.MaxTokens != 900 {
			t.Errorf("%s: route %q, max tokens %d", id, cfg.RouteKey, cfg.MaxTokens)
		}
	}
	// A reply with no ruling is no verdict, not a ruling.
	llm := &FakeLLM{Turns: []FakeTurn{{Content: "I cannot say."}, {Content: "Still cannot."}}}
	if o := runSettleCase(context.Background(), llm, byID["versions"]); o.Got != ScanNoVerdict {
		t.Errorf("an unreadable reply: %q", o.Got)
	}
}
