package orchestrate

// A segmented control must not offer a state its row cannot hold.
//
// Agent and contact rows have three stored states. A tool grant used to have
// one and a half: AutoApproveTools is a LIST, so it can only say yes, and
// "needs approval" was the absence of an entry — indistinguishable from never
// having decided. The control offered the segment anyway, so choosing it
// removed the entry and the row vanished, because the row existed only while
// the entry did.
//
// Fixed by giving the decision a record rather than by removing the segment:
// allow (and the grant is in the list) or ask (and it is not). Either way the
// row stays and shows which.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The round trip, driven: choosing "Needs approval" has to leave something
// behind, or the row goes and we are back where we started.
func TestNeedsApprovalIsAStateAToolRowCanHold(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	setAutoToolPolicy(db, nil, "alice", "agent-1", "send_email", PolicyAsk)

	got := listAutoToolPolicies(db, "alice")
	if len(got) != 1 {
		t.Fatalf("%d records, want 1: the decision was not stored, so the row leaves the page", len(got))
	}
	if got[0].AgentID != "agent-1" || got[0].Tool != "send_email" || got[0].Policy != PolicyAsk {
		t.Errorf("record = %+v", got[0])
	}

	// Flipping back is a state change, not a re-grant from nothing.
	setAutoToolPolicy(db, nil, "alice", "agent-1", "send_email", PolicyAllow)
	if got = listAutoToolPolicies(db, "alice"); len(got) != 1 || got[0].Policy != PolicyAllow {
		t.Fatalf("after allow: %+v", got)
	}

	// Remove is the one that ends it. That is what Remove means on every other
	// row of this page.
	removeAutoToolPolicy(db, nil, "alice", "agent-1", "send_email")
	if got = listAutoToolPolicies(db, "alice"); len(got) != 0 {
		t.Errorf("%d records survive a Remove: %+v", len(got), got)
	}
}

// There is no block for a tool anywhere in the runtime. A segment that reads
// "never" while the tool goes on queueing for approval is worse than no segment.
func TestAToolRowIsNeverOfferedBlocked(t *testing.T) {
	page := readFile(t, "page_chat.go")
	i := strings.Index(page, `{Label: "Blocked", Value: "block"`)
	if i < 0 {
		t.Fatal("the Blocked segment is gone entirely; agent and contact rows need it")
	}
	line := page[i:]
	if j := strings.Index(line, "\n"); j > 0 {
		line = line[:j]
	}
	if !strings.Contains(line, `HideIf: "_autotool"`) {
		t.Errorf("Blocked is offered on tool rows, which cannot store it: %s", line)
	}
	// And a tool policy write never stores it either, whatever arrives.
	db := &DBase{Store: kvlite.MemStore()}
	setAutoToolPolicy(db, nil, "alice", "a", "t", PolicyBlock)
	got := listAutoToolPolicies(db, "alice")
	if len(got) != 1 || got[0].Policy != PolicyAsk {
		t.Errorf("a block was stored as %+v, want it folded to ask", got)
	}
}

// The record and the grant are two halves of one fact. The page reads the
// record; the unattended runner reads the list. A write that updated only one
// would show a state the deployment does not have.
func TestTheRecordAndTheGrantAgree(t *testing.T) {
	src := readFile(t, "tool_policy.go")
	body := src[strings.Index(src, "func setAutoToolPolicy("):]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "addAutoApproveTool(") || !strings.Contains(body, "removeAutoApproveTool(") {
		t.Error("setting a policy does not move the grant, so the runner and the page would disagree")
	}
	rm := src[strings.Index(src, "func removeAutoToolPolicy("):]
	if j := strings.Index(rm, "\nfunc "); j > 0 {
		rm = rm[:j]
	}
	if !strings.Contains(rm, "removeAutoApproveTool(") {
		t.Error("removing the record leaves the grant standing, so the tool still runs unattended")
	}
}

// A grant made before this table existed, or by approving a request (which
// writes the list directly), must still appear.
func TestAGrantWithNoRecordIsStillListed(t *testing.T) {
	src := readFile(t, "console_permissions.go")
	i := strings.Index(src, `for _, ag := range listAgents(udb, user) {`)
	if i < 0 {
		t.Fatal("the tool rows are gone")
	}
	zone := src[i:]
	if j := strings.Index(zone, "writeJSON(w, out)"); j > 0 {
		zone = zone[:j]
	}
	if !strings.Contains(zone, "AutoApproveTools") {
		t.Error("the rows no longer read the grant list, so a grant with no record vanishes")
	}
	if !strings.Contains(zone, "listAutoToolPolicies(") {
		t.Error("the rows no longer read the records, so a needs-approval row cannot exist")
	}
	if !strings.Contains(zone, "seenTool[") {
		t.Error("the union is not de-duplicated; a granted tool with a record would list twice")
	}
}

// The control's middle value has to persist for the OTHER rows too. If "ask"
// ever stopped normalising to a non-empty stored value, agent and contact rows
// would start vanishing for exactly the reason the tool rows just did.
func TestAskIsAStoredStateNotAnAbsence(t *testing.T) {
	src := readFileForTest(t, "../../core/authorizations.go")
	i := strings.Index(src, "func normPolicy(")
	if i < 0 {
		t.Fatal("normPolicy is gone")
	}
	if !strings.Contains(src[i:i+300], "PolicyAsk") {
		t.Error("normPolicy no longer returns PolicyAsk, so Needs approval would store nothing and drop the row")
	}
	j := strings.Index(src, "func listPolicies(")
	if !strings.Contains(src[j:j+900], `p != ""`) {
		t.Error("listPolicies no longer filters on a non-empty stored value; this test's premise has moved")
	}
}
