package orchestrate

// Builder's intake form went missing on a return to the chat, two ways: a
// reload replayed the thread before the agent's intake spec had loaded, so
// the answer stayed plain text; and opening a session cleared the form out of
// an empty log that the empty-state poll never saw change. Pinned in the intake
// script every agent chat page loads (intake_form.html), which has no browser
// here to drive.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTheIntakeFormSurvivesAReturnToTheChat(t *testing.T) {
	b, err := os.ReadFile("assets/intake_form.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for name, re := range map[string]string{
		"a replay before the spec holds the bubble": `if \(!fields\) \{[^}]*pendingIntakeBubbles\.push\(\{bubble: bubble, values: values\}\);\s*return;`,
		"the spec landing decorates what was held":  `window\.uiAgentIntakeSetAgent = function\(a\) \{[^}]*\}[^}]*drainPendingIntakeBubbles\(\);\s*renderIntakeForm\(\);`,
		"an empty log without the form redraws it":  `if \(empty && activeIntakeForm\(\) && !\(intakeWrap && intakeWrap\.isConnected\)`,
		"a submit holds the redraw off":             `submitBtn\.disabled = true;\s*intakeSentAt = Date\.now\(\);`,
	} {
		if !regexp.MustCompile(re).MatchString(src) {
			t.Errorf("%s: not found in the page script", name)
		}
	}
	if strings.Contains(src, "fail silent") {
		t.Error("the replay hook still drops an intake answer it cannot decorate yet")
	}
}
