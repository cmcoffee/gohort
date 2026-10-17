package orchestrate

import (
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
