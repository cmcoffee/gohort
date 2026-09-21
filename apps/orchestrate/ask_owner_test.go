package orchestrate

// The recipient side of a share getting a voice.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
)

func TestAskingTheOwnerReachesTheirNotifications(t *testing.T) {
	reachFixture(t)
	out, err := askOwnerFor("bob", "alice", "a1", "Troubleshooter",
		"It cannot read the Runbooks collection.")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	list := notices.List(RootDB, "alice")
	if len(list) != 1 {
		t.Fatalf("the owner was not told: %+v", list)
	}
	// Who asked, about what, and that they cannot fix it themselves — the
	// owner cannot see the conversation it came from.
	if !strings.Contains(list[0].Title, "bob") || !strings.Contains(list[0].Title, "Troubleshooter") {
		t.Errorf("the notice does not say who asked or about what: %q", list[0].Title)
	}
	if !strings.Contains(list[0].Body, "Runbooks") || !strings.Contains(list[0].Body, "cannot grant this themselves") {
		t.Errorf("the notice does not carry the request or the reason: %q", list[0].Body)
	}
	// Waiting on them, not news: the thing they can do about it is the point.
	if list[0].Kind != notices.KindBlocked {
		t.Errorf("kind = %q", list[0].Kind)
	}
	// The asker is told there is no reply coming, or they wait for one.
	if !strings.Contains(out, "no reply") {
		t.Errorf("the result does not say it is one-way: %q", out)
	}
	// Nothing lands in the asker's own inbox.
	if got := notices.List(RootDB, "bob"); len(got) != 0 {
		t.Errorf("the asker was notified of their own request: %+v", got)
	}
}

// A shared agent with a broken dependency fails the same way for everybody who
// runs it. Without a cap the owner's inbox becomes the same sentence from eight
// people, which is the surface they then stop reading.
func TestAskingIsCappedPerPersonPerAgent(t *testing.T) {
	reachFixture(t)
	for i := 0; i < askOwnerCap; i++ {
		if _, err := askOwnerFor("bob", "alice", "a1", "Troubleshooter", "please"); err != nil {
			t.Fatalf("ask %d: %v", i, err)
		}
	}
	out, err := askOwnerFor("bob", "alice", "a1", "Troubleshooter", "please again")
	// A refusal, not an error: the model should tell the user to follow up
	// another way rather than treat it as a failure to work around.
	if err != nil {
		t.Errorf("the cap reported an error rather than an answer: %v", err)
	}
	if !strings.Contains(out, "not sent") {
		t.Errorf("the cap does not say the request was dropped: %q", out)
	}
	// The cap is per (asker, owner, agent) — somebody else's request, and the
	// same person about a DIFFERENT agent, are unaffected.
	if _, err := askOwnerFor("carol", "alice", "a1", "Troubleshooter", "mine"); err != nil {
		t.Errorf("one person's cap silenced another: %v", err)
	}
	if _, err := askOwnerFor("bob", "alice", "a2", "Other", "different agent"); err != nil {
		t.Errorf("the cap spilled onto another agent: %v", err)
	}
}

// On your own agent there is nobody to ask: you are the person who would grant
// it. The tool is not offered there, and the function refuses if it is reached.
func TestYouCannotAskYourself(t *testing.T) {
	reachFixture(t)
	if _, err := askOwnerFor("alice", "alice", "a1", "Troubleshooter", "please"); err == nil {
		t.Error("an owner could file a request against themselves")
	}
	if got := notices.List(RootDB, "alice"); len(got) != 0 {
		t.Errorf("it was recorded anyway: %+v", got)
	}
}

// The tool is only in the catalog on somebody else's agent.
func TestTheToolIsOfferedOnlyOnSomebodyElsesAgent(t *testing.T) {
	src := packageSource(t)
	i := strings.Index(src, "askOwnerToolDef(sess,")
	if i < 0 {
		t.Fatal("the tool is never added to a catalog")
	}
	window := src[max(0, i-400):i]
	if !strings.Contains(window, "t.ownerUser != \"\" && t.ownerUser != t.user") {
		t.Error("the tool is offered without checking that the agent belongs to somebody else")
	}
}

// The button follows the same rule as the tool, from the other side: the
// toolbar is built once and the agent is picked afterwards, so the entry is
// dropped for a user whose every reachable agent is their own — for whom its
// only possible answer would be "this agent is yours".
func TestTheButtonIsHiddenWhenEverythingIsYours(t *testing.T) {
	mine := []AgentRecord{
		{ID: "a1", Owner: "alice"},
		{ID: "seed-builder", Owner: ""},
	}
	if hasSomeoneElsesAgent(mine, "alice") {
		t.Error("nothing here belongs to anybody else; the entry must be dropped")
	}
	shared := append(append([]AgentRecord{}, mine...), AgentRecord{ID: "b1", Owner: "bob"})
	if !hasSomeoneElsesAgent(shared, "alice") {
		t.Error("bob's agent is reachable, so there is somebody to ask")
	}
	// A seed belongs to the framework. There is no person behind it, so it is
	// not somebody else's agent however its owner field happens to be filled.
	if hasSomeoneElsesAgent([]AgentRecord{{ID: "seed-research", Owner: "system"}}, "alice") {
		t.Error("a framework seed must not make the entry appear")
	}
	if hasSomeoneElsesAgent(shared, "") {
		t.Error("an anonymous caller has nobody to ask")
	}
}

// Both doors go through askOwnerFor, so the cap, the wording and the fold
// cannot come apart between the tool and the button.
func TestTheButtonSharesTheToolsRule(t *testing.T) {
	src := packageSource(t)
	i := strings.Index(src, "func (T *OrchestrateApp) handleAskOwner")
	if i < 0 {
		t.Fatal("the button has no endpoint")
	}
	body := src[i:]
	if j := strings.Index(body[10:], "\nfunc "); j >= 0 {
		body = body[:j+10]
	}
	if !strings.Contains(body, "askOwnerFor(") {
		t.Error("the endpoint records the request itself instead of going through askOwnerFor")
	}
	if !strings.Contains(body, "findAgentByNameOrID(") {
		t.Error("the endpoint takes the agent on trust; it must resolve it the way a run does")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
