package orchestrate

import (
	"strings"
	"testing"
)

func briefFixture() string {
	return buildBuilderBrief(
		AgentRecord{ID: "a-support", Name: "Support bot", Description: "answers customer questions"},
		ChatSession{ID: "s1", Messages: []ChatMessage{
			{Role: "user", Content: "where is my order"},
			{Role: "assistant", Content: "I cannot help with that."},
			{Role: "user", Content: "no, you should look it up"},
		}},
	)
}

// The correction the user made by hand IS the case, and writing it first is
// what separates improving an agent from believing you did: written afterwards
// it passes the moment it is created and proves nothing.
func TestTheBriefAsksForTheFailingCaseBeforeTheFix(t *testing.T) {
	brief := briefFixture()

	caseStep := strings.Index(brief, "add_case")
	runStep := strings.Index(brief, `eval(action="run"`)
	fixStep := strings.Index(brief, "Propose specific changes")
	switch {
	case caseStep < 0:
		t.Fatalf("the brief must ask for an eval case; got:\n%s", brief)
	case runStep < 0:
		t.Fatalf("the brief must ask for a run, or nothing is measured")
	case fixStep < 0:
		t.Fatal("the brief lost its propose-changes step")
	case !(caseStep < runStep && runStep < fixStep):
		t.Errorf("order must be case, then the BEFORE run, then the fix — got case@%d run@%d fix@%d",
			caseStep, runStep, fixStep)
	}
	if !strings.Contains(brief, "should FAIL") {
		t.Error("a case that passes before the fix captures nothing, and the brief has to say so")
	}
	if !strings.Contains(brief, "BOTH scores") {
		t.Error("the after-run is only useful against the before; ask for both")
	}
}

// A brief that says "create a suite" without saying WHICH agent leaves Builder
// to infer the target from prose, next to a fenced transcript full of other
// agents' names.
func TestTheBriefNamesTheTargetForANewSuite(t *testing.T) {
	brief := briefFixture()
	if !strings.Contains(brief, `target_kind="agent"`) || !strings.Contains(brief, `target="a-support"`) {
		t.Errorf("create_suite guidance must carry the agent's own id:\n%s", brief)
	}
}

// Suites default to stubbed and the brief should say so. Builder is being asked
// to run one against a support agent; if it believes that might send real mail,
// the right move for it is to skip the step.
func TestTheBriefSaysRunningIsSafe(t *testing.T) {
	if !strings.Contains(briefFixture(), "stubbed") {
		t.Error("say that a run is stubbed, or the measuring step reads as risky and gets skipped")
	}
}

// The transcript is another session's content arriving in front of an agent
// that holds update_agent. It must stay fenced whatever else the brief gains.
func TestTheTranscriptStaysFenced(t *testing.T) {
	brief := briefFixture()
	if !strings.Contains(brief, "session transcript to analyze") {
		t.Fatal("the transcript fence is missing")
	}
	// The instructions must come BEFORE the fenced block: anything after it is
	// inside, or reads as if it were.
	if strings.Index(brief, "Propose specific changes") > strings.Index(brief, "session transcript to analyze") {
		t.Error("instructions must precede the fenced transcript, not follow it")
	}
}
