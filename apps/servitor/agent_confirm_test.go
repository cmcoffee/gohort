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
	err := agentRefusal{Cmd: "apt-get install nginx", Cat: AllRiskCategories[0], Reason: "installs software", Appliance: "lab-box"}.err()
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
	msg := agentRefusal{Cmd: "rm -rf /tmp/x", Cat: AllRiskCategories[0], Reason: "", Appliance: "prod"}.err().Error()
	if !strings.Contains(msg, "Nothing was executed and nothing is waiting") {
		t.Errorf("the refusal must be unambiguous about state, got %q", msg)
	}
	// And it must close the obvious workaround, without reading as a
	// standing answer: a refusal kept in a long conversation was repeated
	// after the owner had allowed the command.
	if !strings.Contains(msg, "Do NOT reword it or reach the same effect another way") || !strings.Contains(msg, "try it again rather than repeating this") {
		t.Errorf("the refusal should forbid rewording around the gate and leave a later try open, got %q", msg)
	}
}

// A missing reason must not render as an empty bracket.
func TestRefusalWithoutAReasonReadsCleanly(t *testing.T) {
	msg := agentRefusal{Cmd: "systemctl restart nginx", Cat: AllRiskCategories[0], Reason: "", Appliance: "lab-box"}.err().Error()
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
	msg := agentRefusal{Cmd: long, Cat: AllRiskCategories[0], Reason: "", Appliance: "box"}.err().Error()
	if strings.Contains(msg, long) {
		t.Error("a 400-char pipeline should not be quoted whole")
	}
	if !strings.Contains(msg, "...") {
		t.Error("truncation should be visible")
	}
}
