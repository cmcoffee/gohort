package orchestrate

import (
	"strings"
	"testing"
)

func briefFixture() string { return briefWithReason("") }

func briefWithReason(reason string) string {
	return buildBuilderBrief(
		AgentRecord{ID: "a-support", Name: "Support bot", Description: "answers customer questions"},
		ChatSession{ID: "s1", Messages: []ChatMessage{
			{Role: "user", Content: "where is my order"},
			{Role: "assistant", Content: "I cannot help with that."},
			{Role: "user", Content: "no, you should look it up"},
		}},
		reason,
		true, // the owner: Builder cannot diagnose from the calls alone
	)
}

// What the user said is wrong leads the brief. Without it Builder is told a
// failure exists and left to pick which one, so it diagnoses whichever it
// notices first — confidently, and not necessarily the one that mattered.
func TestTheBriefLeadsWithTheUsersOwnReason(t *testing.T) {
	reason := "it kept summarising when I asked for the raw numbers"
	brief := briefWithReason(reason)

	if !strings.Contains(brief, reason) {
		t.Fatalf("the user's reason never reached the brief:\n%s", brief)
	}
	// Before the instructions, and far before the transcript: it is the thing
	// the rest of the brief is in service of.
	if strings.Index(brief, reason) > strings.Index(brief, "Please:") {
		t.Error("the reason must come before the instructions, not after them")
	}
	if strings.Index(brief, reason) > strings.Index(brief, "session transcript to analyze") {
		t.Error("the reason must not land inside the fenced transcript")
	}
	// Quoted, so a multi-line reason cannot be misread as brief structure.
	if !strings.Contains(brief, "> "+reason) {
		t.Error("the reason should be quoted in the brief")
	}
	// And Builder is told to check the claim rather than accept it: the user
	// can be wrong about which turn went bad.
	if !strings.Contains(brief, "tell me if what I described is not what you find there") {
		t.Error("Builder should be asked to say when the transcript does not show the described problem")
	}
	// With a reason given, the generic "find where it fell short" framing must
	// not also be present — two different problems to solve is worse than one.
	if strings.Contains(brief, "pinpoint where its behavior fell short") {
		t.Error("the unexplained framing should not survive alongside a stated reason")
	}
}

// No reason given is stated plainly, so Builder asks rather than assuming one.
// The old brief asserted "I had to correct it" whether or not that happened.
func TestAnUnexplainedBriefSaysSo(t *testing.T) {
	brief := briefFixture()
	if !strings.Contains(brief, "I have NOT told you what went wrong") {
		t.Errorf("an unexplained handoff must say it is unexplained:\n%s", brief)
	}
	if !strings.Contains(brief, "ask me which before you change anything") {
		t.Error("with no stated reason, Builder should ask before changing anything")
	}
	if strings.Contains(brief, "had to correct it during the session") {
		t.Error("the brief still asserts a reason the user never gave")
	}
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
