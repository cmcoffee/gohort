package orchestrate

// A segmented control must not offer a state its row cannot hold.
//
// Agent and contact rows have three real stored states, and picking the middle
// one persists "ask" and leaves the row on the page showing it. An autonomous
// tool grant has two: the tool is in the agent's AutoApproveTools or it is not.
// It used to carry the same three-way control anyway, so choosing "Needs
// approval" revoked the grant and the row vanished — which reads as the click
// failing or the setting being lost, not as the deliberate revoke it was.

import (
	"strings"
	"testing"
)

// The rule, stated where it can fail: a row offering the segmented control has
// somewhere to put every value the control can produce.
func TestOnlyRowsWithAStoredPolicyGetTheSegmentedControl(t *testing.T) {
	src := readFile(t, "console_permissions.go")
	// Bounded on the LOOP, not on the "Zone 3" comment: that phrase also
	// appears where the zones are introduced, and slicing from there swept in
	// the agent and contact rows, which carry a Policy correctly.
	i := strings.Index(src, `for _, ag := range listAgents(udb, user) {`)
	if i < 0 {
		t.Fatal("the autonomous-tool rows are gone")
	}
	zone := src[i:]
	if j := strings.Index(zone, "writeJSON(w, out)"); j > 0 {
		zone = zone[:j]
	}
	if strings.Contains(zone, "Policy:") {
		t.Error("an autonomous-tool row carries a Policy again, so it renders a three-way control over a binary grant")
	}
	if !strings.Contains(zone, "AutoTool: true") {
		t.Error("autonomous-tool rows are not marked, so the page cannot tell them from the ones with stored policies")
	}
}

// And the control's middle value is a state the other rows really keep. If
// "ask" ever stopped persisting, those rows would start vanishing too, for the
// same reason and with no test between it and the user.
func TestAskIsAStoredStateNotAnAbsence(t *testing.T) {
	if normPolicyIsAsk := "ask"; normPolicyIsAsk == "" {
		t.Skip()
	}
	// listPolicies keeps an entry whose stored value is non-empty, so "ask"
	// has to normalise to a non-empty string or the row drops out of the list.
	src := readFileForTest(t, "../../core/authorizations.go")
	i := strings.Index(src, "func normPolicy(")
	if i < 0 {
		t.Fatal("normPolicy is gone")
	}
	body := src[i : i+300]
	if !strings.Contains(body, "PolicyAsk") {
		t.Error("normPolicy no longer returns PolicyAsk, so choosing Needs approval would store nothing and drop the row")
	}
	j := strings.Index(src, "func listPolicies(")
	if j < 0 {
		t.Fatal("listPolicies is gone")
	}
	if !strings.Contains(src[j:j+900], `p != ""`) {
		t.Error("listPolicies no longer filters on a non-empty stored value; this test's premise has moved")
	}
}

// The revoke says the row will go. A row leaving the page is the one outcome a
// click on it has, and it should be chosen rather than discovered.
func TestRevokingAToolGrantSaysTheRowWillGo(t *testing.T) {
	page := readFile(t, "page_chat.go")
	i := strings.Index(page, `OnlyIf: "_autotool"`)
	if i < 0 {
		t.Fatal("autonomous-tool rows have no action of their own")
	}
	line := page[strings.LastIndex(page[:i], "{Label:"):]
	if j := strings.Index(line, "\n"); j > 0 {
		line = line[:j]
	}
	if !strings.Contains(line, "Confirm:") {
		t.Error("revoking a standing grant does not ask first")
	}
	for _, says := range []string{"leaves this page", "queues"} {
		if !strings.Contains(line, says) {
			t.Errorf("the confirm does not say %q: %s", says, line)
		}
	}
	// And the generic Remove no longer doubles up on these rows.
	if !strings.Contains(page, `OnlyIf: "_managed", HideIf: "_autotool"`) {
		t.Error("Remove still shows on autonomous-tool rows beside Revoke")
	}
}
