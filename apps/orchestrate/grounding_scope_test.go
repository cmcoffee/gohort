package orchestrate

import (
	"context"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A reply telling the user their own setup is not asserting a rumour: what
// they told the agent leaves the judge's scope on a reply to them, and what
// anyone else said, or the agent concluded, stays in it.
func TestTheJudgeLeavesTheUsersOwnNotesAloneOnAReplyToThem(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	agent, err := saveAgent(udb, AgentRecord{Name: "Agent", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	ns := factsNamespace(agent.ID)
	const (
		heard   = "the user runs a local worker and a cloud lead"
		typed   = "the build host has 64GB of memory"
		guessed = "the slowdown is caused by the shared disk"
		dana    = "the release ships on Friday"
	)
	StoreMemoryFactP(udb, ns, heard, FactWritePolicy{Source: MemSourceObserved, Domain: ClaimWorld})
	StoreMemoryFactP(udb, ns, typed, FactWritePolicy{Source: MemSourceUserStated, Domain: ClaimWorld})
	StoreMemoryFactP(udb, ns, guessed, FactWritePolicy{Source: MemSourceInferred, Domain: ClaimWorld})
	StoreMemoryFactP(udb, ns, dana, FactWritePolicy{Source: MemSourceObserved, Domain: ClaimWorld, Speaker: "Dana", SpeakerHandle: "+15550100"})

	has := func(scope []string, note string) bool {
		for _, s := range scope {
			if s == note {
				return true
			}
		}
		return false
	}

	own := &chatTurn{user: "u", udb: udb, agent: agent, session: &ChatSession{ID: "s1"}}
	scope := own.uncheckedClaims(own.facts())
	if has(scope, heard) || has(scope, typed) {
		t.Errorf("the user's own notes are judged on a reply to them: %q", scope)
	}
	if !has(scope, guessed) || !has(scope, dana) {
		t.Errorf("an inference or someone else's claim left the judge's scope: %q", scope)
	}

	// A channel turn may be a room, so everything marked stays in scope.
	room := &chatTurn{user: "u", udb: udb, agent: agent, requesterChannel: "imessage", requesterHandle: "+15550199", requesterOwnerHandle: true}
	if got := room.uncheckedClaims(room.facts()); !has(got, heard) || !has(got, typed) {
		t.Errorf("a channel reply dropped the owner's notes from the judge: %q", got)
	}
}

// The remember tool asks whether a note was told or concluded, and only what
// the user told it leaves the judge's scope. A note that says neither is
// treated as concluded.
func TestRememberRecordsToldApartFromConcluded(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "NEW", Repeat: true}}}
	app.LLM, app.LeadLLM = fake, fake
	ct := driftTurn(&DBase{Store: kvlite.MemStore()})
	ct.app = app
	ct.session = &ChatSession{ID: "s1"}
	notes := map[string]string{
		"told":      "the build host runs two GPUs",
		"concluded": "the nightly job fails because the disk fills",
		"":          "the cache server restarts every Sunday",
	}
	for origin, note := range notes {
		args := map[string]any{"pin": true, "domain": "world", "content": note}
		if origin != "" {
			args["origin"] = origin
		}
		if _, err := ct.rememberToolDef().Handler(context.Background(), args); err != nil {
			t.Fatalf("remember %q: %v", note, err)
		}
	}
	if n := len(ct.facts()); n != 3 {
		t.Fatalf("%d notes saved, want 3", n)
	}
	scope := map[string]bool{}
	for _, n := range ct.uncheckedClaims(ct.facts()) {
		scope[n] = true
	}
	if scope[notes["told"]] {
		t.Error("a note the user told the agent is judged on a reply to them")
	}
	if !scope[notes["concluded"]] || !scope[notes[""]] {
		t.Errorf("a conclusion left the judge's scope: %v", scope)
	}
}
