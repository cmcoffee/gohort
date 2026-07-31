package orchestrate

// Breadcrumbs from a DISPATCHED turn — a channel inbound, a cortex thread, a
// sub-agent run.
//
// These turns have no *session: the record belongs to the caller, so the turn
// carries only the ids of the trail it should write to. When those ids were
// never set, turnDiag hit its "no trail to write to" return and every guard on
// the path went silent — including the guardrail block, which had already
// worked out WHICH rule fired and then threw the answer away. The owner got a
// decline on their phone and no way to find out what stopped it.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// dispatchLikeTurn is a turn shaped the way agent_dispatch builds one: no
// session, a runtime identity that may differ from the owner, and the trail
// named explicitly.
func dispatchLikeTurn(root Database, owner, runtimeUser, agentID, sessionID string) *chatTurn {
	app := &OrchestrateApp{}
	app.DB = root
	return &chatTurn{
		app:           app,
		agent:         AgentRecord{ID: agentID, Name: "X", Owner: owner},
		user:          runtimeUser,
		udb:           UserDB(root, runtimeUser),
		ownerUser:     owner,
		ownerDB:       UserDB(root, owner),
		diagAgentID:   agentID,
		diagSessionID: sessionID,
	}
}

// TestDispatchedTurnLeavesABreadcrumb — the trail ids are what make a
// session-less turn accountable at all.
func TestDispatchedTurnLeavesABreadcrumb(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := dispatchLikeTurn(root, "u", "u", "a1", "chat-42")
	turn.turnDiag("guardrail-blocked", `Guardrail "never discuss pay" blocked a pre_output check`)

	got := sessionDiags(UserDB(root, "u"), "a1", "chat-42")
	if len(got) != 1 {
		t.Fatalf("expected one breadcrumb, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "guardrail-blocked" {
		t.Errorf("kind = %q", got[0].Kind)
	}
	// The rule is the whole point — a trail that says "something blocked this"
	// is barely better than silence.
	if !strings.Contains(got[0].Detail, "never discuss pay") {
		t.Errorf("the breadcrumb must name the rule, got %q", got[0].Detail)
	}
}

// TestBreadcrumbLandsInTheOwnersStore — a channel turn can run as a synthetic
// per-chat identity. The trail is read back through the REQUESTING user's own
// store, so a breadcrumb filed under the synthetic identity is filed where
// nobody will ever look.
func TestBreadcrumbLandsInTheOwnersStore(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := dispatchLikeTurn(root, "u", "phantom:chat123", "a1", "chat-42")
	turn.turnDiag("guardrail-input-blocked", `Guardrail "no addresses" refused the request`)

	if got := sessionDiags(UserDB(root, "u"), "a1", "chat-42"); len(got) != 1 {
		t.Errorf("owner cannot see the breadcrumb: %+v", got)
	}
	if got := sessionDiags(UserDB(root, "phantom:chat123"), "a1", "chat-42"); len(got) != 0 {
		t.Errorf("breadcrumb was filed under the synthetic identity: %+v", got)
	}
}

// TestNoTrailIdsStillSilent — the guard against writing breadcrumbs nowhere
// stays; a turn with genuinely no session to name must not invent one.
func TestNoTrailIdsStillSilent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := dispatchLikeTurn(root, "u", "u", "a1", "")
	turn.turnDiag("guardrail-blocked", "something")
	if got := sessionDiags(UserDB(root, "u"), "a1", ""); len(got) != 0 {
		t.Errorf("wrote a breadcrumb with no trail to write to: %+v", got)
	}
}

// sessionDiags reads a trail back the way the HTTP handler does.
func sessionDiags(udb Database, agentID, sessionID string) []SessionDiag {
	var list []SessionDiag
	if udb == nil {
		return nil
	}
	udb.Get(sessionDiagTable, agentID+":"+sessionID, &list)
	return list
}
