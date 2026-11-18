package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// An app's memory is the app's, and each person's own: every agent of the
// app reads and writes it in the store of the person in the conversation,
// after what the app says its agents know from the start; nobody sees anyone
// else's; a person who turns it off is not remembered from then on; and an
// app that keeps no memory remembers nobody.
func TestAnAppsMemoryIsTheAppsAndEachPersonsOwn(t *testing.T) {
	turn, udb := copyTestTurn(t)
	copyTestApp(t, turn, map[string]any{"memory": true, "starting_facts": []any{"Prices are in euros."}})
	clerk, err := saveAgent(udb, AgentRecord{Owner: "u", Name: "Clerk", OrchestratorPrompt: "Help.", OwningApp: "desk", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := saveAgent(udb, AgentRecord{Owner: "u", Name: "Packer", OrchestratorPrompt: "Pack.", OwningApp: "desk", appEdit: "Builder"})
	as := func(user string, a AgentRecord) *chatTurn {
		return &chatTurn{user: user, udb: UserDB(orchestrateBaseDB, user), app: turn.app, agent: a}
	}
	notes := func(ct *chatTurn) []string {
		var out []string
		for _, f := range ct.facts() {
			out = append(out, f.Note)
		}
		return out
	}

	if _, err := as("u", clerk).storeFactNote("Owner likes tea.", ClaimDomainUnknown, MemSourceObserved, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := as("guest", clerk).storeFactNote("Guest works nights.", ClaimDomainUnknown, MemSourceObserved, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got := notes(as("u", other))
	if len(got) != 2 || got[0] != "Prices are in euros." || got[1] != "Owner likes tea." {
		t.Errorf("another of the app's agents, with the owner: %q", got)
	}
	if got := notes(as("guest", clerk)); len(got) != 2 || got[1] != "Guest works nights." {
		t.Errorf("with the guest: %q", got)
	}
	if n := len(ListMemoryFacts(udb, factsNamespace(clerk.ID))); n != 0 {
		t.Errorf("%d facts landed in the agent's own memory, not the app's", n)
	}

	guestDB, ns := UserDB(orchestrateBaseDB, "guest"), AppMemoryNamespace("u", "desk")
	if facts := ListMemoryFacts(guestDB, ns); len(facts) != 1 || AppMemoryOptedOut(guestDB, "u", "desk") {
		t.Fatalf("what the app remembers about the guest: %+v", facts)
	}
	SetAppMemoryOptOut(guestDB, "u", "desk", true)
	if _, err := as("guest", clerk).storeFactNote("Guest has a cat.", ClaimDomainUnknown, MemSourceObserved, time.Time{}); err == nil || !strings.Contains(err.Error(), "not to remember them") {
		t.Errorf("a save about a person who turned it off: %v", err)
	}
	if got := notes(as("guest", clerk)); len(got) != 2 {
		t.Errorf("turning it off forgot what was kept: %q", got)
	}
	if facts := ListMemoryFacts(udb, ns); len(facts) != 1 {
		t.Errorf("the owner's store holds %+v", facts)
	}

	spec, _ := LoadAppSpec("u", "desk")
	spec.Memory = "off"
	SaveAppSpec(spec)
	if _, err := as("u", clerk).storeFactNote("Owner likes coffee.", ClaimDomainUnknown, MemSourceObserved, time.Time{}); err == nil || !strings.Contains(err.Error(), "keeps no memory") {
		t.Errorf("a save in an app that keeps no memory: %v", err)
	}
	if got := notes(as("u", clerk)); len(got) != 1 || got[0] != "Prices are in euros." {
		t.Errorf("an app that keeps no memory still knows: %q", got)
	}
}
