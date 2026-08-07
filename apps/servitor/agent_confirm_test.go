// An agent hitting the confirmation gate must be told, not parked.
//
// The gate blocks on a channel that servitor's own UI feeds. An agent has
// nobody watching that stream, so parking a command there means five minutes of
// silence and a timeout — and the only way to make agent-driven work usable
// would be to grant broad auto-run categories up front, which hollows out the
// gate entirely.
package servitor

import (
	"strings"
	"testing"
)

func TestRefusalNamesWhatWouldPermitIt(t *testing.T) {
	err := agentCommandRefusal("apt-get install nginx", AllRiskCategories[0], "installs software", "lab-box")
	if err == nil {
		t.Fatal("an ungranted command must be refused")
	}
	msg := err.Error()
	for _, want := range []string{
		"apt-get install nginx",      // what
		string(AllRiskCategories[0]), // the unit a grant is written in
		"installs software",          // why the classifier flagged it
		"lab-box",                    // where
		"the owner can allow",        // what would change the answer
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal should carry %q, got %q", want, msg)
		}
	}
}

// Nothing ran and nothing is pending — the agent must not report it as
// "waiting for approval", which would leave the person expecting it to happen.
func TestRefusalSaysNothingIsPending(t *testing.T) {
	msg := agentCommandRefusal("rm -rf /tmp/x", AllRiskCategories[0], "", "prod").Error()
	if !strings.Contains(msg, "Nothing was executed and nothing is waiting") {
		t.Errorf("the refusal must be unambiguous about state, got %q", msg)
	}
	// And it must close the obvious workaround.
	if !strings.Contains(msg, "Do NOT retry it, reword it") {
		t.Errorf("the refusal should forbid rewording around the gate, got %q", msg)
	}
}

// A missing reason must not render as an empty bracket.
func TestRefusalWithoutAReasonReadsCleanly(t *testing.T) {
	msg := agentCommandRefusal("systemctl restart nginx", AllRiskCategories[0], "", "lab-box").Error()
	if strings.Contains(msg, "()") {
		t.Errorf("an absent reason should be omitted, got %q", msg)
	}
}

// An appliance with no name falls back to its id, and with neither to a phrase
// — never to "on ".
func TestApplianceLabelAlwaysReadsAsSomething(t *testing.T) {
	if got := applianceLabel("Lab Box", "lab-1"); got != "Lab Box" {
		t.Errorf("the name should win, got %q", got)
	}
	if got := applianceLabel("  ", "lab-1"); got != "lab-1" {
		t.Errorf("an unnamed box should use its id, got %q", got)
	}
	if got := applianceLabel("", ""); got != "this system" {
		t.Errorf("with neither, the label must still read, got %q", got)
	}
}

func TestLongCommandIsTruncatedInTheRefusal(t *testing.T) {
	long := strings.Repeat("a", 400)
	msg := agentCommandRefusal(long, AllRiskCategories[0], "", "box").Error()
	if strings.Contains(msg, long) {
		t.Error("a 400-char pipeline should not be quoted whole")
	}
	if !strings.Contains(msg, "…") {
		t.Error("truncation should be visible")
	}
}
