package imagefetch

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

// The source check is registered with its cases, each wanting a verdict per
// source; a case goes through the judge's own prompt (the instruction, each
// look, the result's look, the three-line form) without thinking, on the
// check's caller, and its lines are read as the live check reads them.
func TestTheSourceCheckCasesRunThroughTheJudge(t *testing.T) {
	var check checktest.Check
	for _, ch := range checktest.Checks() {
		if ch.Key == "imagefetch/source_check" {
			check = ch
		}
	}
	if check.Run == nil || check.Structured {
		t.Fatalf("the source check is not registered as a reading: %+v", check)
	}
	byID := map[string]checktest.Case{}
	for _, c := range check.Cases() {
		byID[c.ID] = c
		for _, part := range strings.Split(c.Want, ", ") {
			if part != "1 yes" && part != "1 no" && part != "2 yes" && part != "2 no" {
				t.Errorf("%s wants %q", c.ID, c.Want)
			}
		}
	}
	c := byID["blend-one-lost"]
	llm := &FakeLLM{Turns: []FakeTurn{{Content: "**Source 1 takes:** the white ship\nSource 1 in result: a wedge-shaped ship stands in for it\nSource 1 FOUND: no\nSource 2 takes: the battleship\nSource 2 in result: the battleship on the right\nSource 2 FOUND: yes"}}}
	o := check.Run(context.Background(), llm, c)
	if o.Got != "1 no, 2 yes" || !strings.Contains(o.Reason, "wedge-shaped ship stands in") {
		t.Errorf("got %q (%s)", o.Got, o.Reason)
	}
	p := llm.Prompt(0)
	for _, want := range []string{
		"made from 2 source picture(s). The instruction for it was:\n" + c.Input["instruction"],
		"SOURCE 1, as described by someone who saw only that picture:\n" + c.Input["s1"],
		"SOURCE 2, as described by someone who saw only that picture:\n" + c.Input["s2"],
		"The RESULT, as described by someone who saw only the result and none of the sources:\n" + c.Input["result"],
		"Source N FOUND: yes or no",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, p)
		}
	}
	if cfg := llm.Config(0); cfg.Caller != "imagefetch/source_check" || cfg.Think == nil || *cfg.Think {
		t.Errorf("the judge's call: caller %q, think %v", cfg.Caller, cfg.Think)
	}

	// A source with no FOUND line has no verdict, and the case says so.
	llm = &FakeLLM{Turns: []FakeTurn{{Content: "Source 1 FOUND: yes\nSource 2 takes: the setting"}}}
	if o := check.Run(context.Background(), llm, byID["background-lost"]); o.Got != "1 yes, 2 none" {
		t.Errorf("a source left undecided: %q", o.Got)
	}
	llm = &FakeLLM{Turns: []FakeTurn{{Err: context.DeadlineExceeded}}}
	if o := check.Run(context.Background(), llm, c); o.Got != ScanNoVerdict {
		t.Errorf("a failed call: %q", o.Got)
	}
}

// The judge's prompt leaves out a source whose look did not come back, and
// says when there was no instruction.
func TestTheSourceJudgePromptLeavesOutAMissingLook(t *testing.T) {
	p, judged := sourceJudgePrompt("  ", []string{"", "- a cat"}, "- a cat on a sofa")
	if len(judged) != 1 || judged[0] != 2 || strings.Contains(p, "SOURCE 1") || !strings.Contains(p, "SOURCE 2, as described") {
		t.Errorf("judged %v:\n%s", judged, p)
	}
	if !strings.Contains(p, "The instruction for it was:\n(none: the pictures were combined as they are)") {
		t.Errorf("no instruction:\n%s", p)
	}
}
