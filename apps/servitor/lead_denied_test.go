package servitor

import (
	"testing"

	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
)

// THE RULE: with "All LLMs are private" OFF, nothing in servitor reaches the
// lead tier. Not by routing table, not by agent toggle, not by default.
//
// Servitor holds SSH credentials, log contents and system facts. Every one of
// the routes below was independently plausible and only one of them had to be
// open for that material to reach a hosted model, so they are asserted
// together rather than wherever each happens to live.

// privacyOff points the settings store at a scratch db with the toggle off,
// which is also the default for every deployment that has never seen it.
func privacyOff(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	return db
}

// TestRoutingCannotSendServitorToLead — even with the routing table explicitly
// set to lead, which an operator can do by hand or by an import.
func TestRoutingCannotSendServitorToLead(t *testing.T) {
	db := privacyOff(t)
	prev := LookupRouteFunc
	LookupRouteFunc = func(key string) string {
		var v string
		db.Get(RoutingTable, key, &v)
		return v
	}
	t.Cleanup(func() { LookupRouteFunc = prev })

	for _, key := range []string{"app.servitor", "app.servitor.orchestrator"} {
		if !IsPrivateStage(key) {
			t.Errorf("%s is not registered private — nothing stops it escalating", key)
		}
		for _, val := range []string{"lead", "lead (thinking)"} {
			db.Set(RoutingTable, key, val)
			if RouteToLead(key) {
				t.Errorf("%s routed to lead with the stored value %q and privacy off", key, val)
			}
		}
	}

	// And with the toggle ON it must actually be reachable, or the setting is
	// decorative and the pin is permanent by another name.
	SetAllLLMsPrivate(true)
	db.Set(RoutingTable, "app.servitor.orchestrator", "lead")
	if !RouteToLead("app.servitor.orchestrator") {
		t.Error("with every model private, the orchestrator still could not reach the lead tier")
	}
}

// TestTheInvestigatorAgentDeclaresItselfPrivate — the path that does not go
// through a route stage at all.
//
// The chat/doc investigator runs through orchestrate's scoped agent, whose tier
// comes from the AGENT RECORD rather than from any routing row. It stayed off
// the lead tier only because LeadModel is false in a zero record — nothing
// enforced that, and one edit in the agent editor would have undone it
// silently.
func TestTheInvestigatorAgentDeclaresItselfPrivate(t *testing.T) {
	spec, ok := appagents.AppAgentByID(servitorInvestigatorAgentID)
	if !ok {
		t.Fatalf("the investigator app agent %q is not registered", servitorInvestigatorAgentID)
	}
	if !spec.ForcePrivate {
		t.Error("the servitor investigator does not declare ForcePrivate — it reads SSH " +
			"credentials and log contents, and a record edit would let it reach a hosted model")
	}
}

// TestTheAppCorePinHolds — servitor calls T.Private(), and that must still deny
// the lead tier while the deployment is not all-private. This is the pin that
// covers every LeadChat / RunAgentLoop call in the package at once, including
// any added later that forgets a route key.
func TestTheAppCorePinHolds(t *testing.T) {
	privacyOff(t)

	var app AppCore
	app.Private()
	if !app.LeadDenied() {
		t.Fatal("a private AppCore does not deny the lead tier with privacy off")
	}
	if app.GetLeadLLM() != nil {
		t.Error("a private AppCore handed out a lead LLM with privacy off")
	}
	if app.HasDistinctLead() {
		t.Error("a private AppCore reports a usable distinct lead with privacy off")
	}

	SetAllLLMsPrivate(true)
	if app.LeadDenied() {
		t.Error("with every model private the pin still binds — the toggle does nothing for servitor, " +
			"which is the app it was added for")
	}
}

// TestTheDefaultIsWorker — a deployment that has never touched the setting must
// behave exactly as it did before the setting existed.
func TestTheDefaultIsWorker(t *testing.T) {
	privacyOff(t)
	if AllLLMsPrivate() {
		t.Fatal("a store that has never been written reports every model private")
	}
	for _, key := range []string{"app.servitor", "app.servitor.orchestrator"} {
		if !PrivateStageEnforced(key) {
			t.Errorf("%s is not enforced private by default", key)
		}
	}
}
