package orchestrate

// The start-up migrations run once each, leave a marker that says how much
// they changed, and do not touch Builder, which every read rebuilds.

import (
	"reflect"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// markers is the orchestrate migrations that have run, by name, with what
// each changed.
func markers() map[string]int {
	out := map[string]int{}
	for _, m := range ListMigrationMarkers() {
		if m.App == "orchestrate" && m.Done {
			out[m.Name] = m.Changed
		}
	}
	return out
}

// Each runs once: what it fixes is fixed, its marker says how much, and the
// same leftover planted again afterwards (a restored backup, say) is left for
// the read path rather than swept on every start.
func TestStartUpMigrationsRunOnce(t *testing.T) {
	app, udb := familyFixture(t)
	udb.Set(agentsTable, "seed-operator", AgentRecord{ID: "seed-operator", Owner: "alice"})
	udb.Set(agentsTable, "seed-chat", AgentRecord{ID: "seed-chat", Owner: "alice", AllowedTools: []string{"frozen"}, DisabledPersistentTools: []string{"x"}})
	saveBulletin(udb, bulletinBoard{Name: "old", Posters: []string{"a-nit"}})

	app.runMigrations()
	app.runLateMigrations()
	if udb.Get(agentsTable, "seed-operator", &AgentRecord{}) {
		t.Error("the retired Operator is still there")
	}
	var chat AgentRecord
	if udb.Get(agentsTable, "seed-chat", &chat); len(chat.AllowedTools) != 0 || len(chat.DisabledPersistentTools) != 0 || chat.OverlayRev != 1 {
		t.Errorf("Chat's frozen tools were not reset and stamped: %+v", chat)
	}
	if b, _ := loadBulletin(udb, "old"); !reflect.DeepEqual(b.Posters, []string{"a-writer"}) {
		t.Errorf("the board still names the sub-agent: %v", b.Posters)
	}
	got := markers()
	for name, want := range map[string]int{"drop_legacy_operator:v1": 1, "seed_chat_frozen_tools:v1": 1, "seed_shadow_overlays:v1": 1,
		"sub_agent_posters:v1": 1, "sub_agent_channels:v1": 0, "sub_agent_schedules:v1": 0} {
		if n, ok := got[name]; !ok || n != want {
			t.Errorf("marker %s: ran=%v changed=%d, want %d", name, ok, n, want)
		}
	}

	udb.Set(agentsTable, "seed-operator", AgentRecord{ID: "seed-operator", Owner: "alice"})
	saveBulletin(udb, bulletinBoard{Name: "older", Posters: []string{"a-critic"}})
	app.runMigrations()
	app.runLateMigrations()
	if !udb.Get(agentsTable, "seed-operator", &AgentRecord{}) {
		t.Error("the Operator pass ran again on a later start")
	}
	if b, _ := loadBulletin(udb, "older"); !reflect.DeepEqual(b.Posters, []string{"a-critic"}) {
		t.Errorf("the posters pass ran again on a later start: %v", b.Posters)
	}
}

// Builder's stored record is what its owner saved: start-up no longer
// rewrites it, and loading it still gives the code's Builder wearing the
// owner's settings.
func TestStartUpLeavesBuildersRecordAlone(t *testing.T) {
	app, udb := familyFixture(t)
	saved := AgentRecord{ID: "seed-builder", Rules: "never touch production", LeadModel: true}
	udb.Set(agentsTable, "seed-builder", saved)
	app.runMigrations()
	app.runLateMigrations()
	var stored AgentRecord
	if !udb.Get(agentsTable, "seed-builder", &stored) || !reflect.DeepEqual(stored, saved) {
		t.Errorf("start-up rewrote Builder's record: %+v", stored)
	}
	got, ok := loadAgent(udb, "seed-builder")
	seed, _ := seedAgentByID("seed-builder")
	if !ok || got.OrchestratorPrompt != seed.OrchestratorPrompt || got.Rules != saved.Rules || !got.LeadModel {
		t.Errorf("Builder did not load as the code's with the owner's settings: rules=%q lead=%v", got.Rules, got.LeadModel)
	}
}
