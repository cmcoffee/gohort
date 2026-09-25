package orchestrate

// "Consult the Lead" is its own per-agent setting. It used to ride along with
// the authoring toolset, so only authoring agents had it, and dispatched runs
// (which build that toolset with no turn) got a copy that could only fail.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestConsultTheLeadResolves(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = prev })

	plain := AgentRecord{ID: "a1"}
	author := AgentRecord{ID: "a2", Author: true}
	if settingIsOn(db, plain, defaultConsultLead) {
		t.Error("an ordinary agent does not consult by default")
	}
	if !settingIsOn(db, author, defaultConsultLead) {
		t.Error("an authoring agent keeps consulting, as it always has")
	}
	author.ConsultLead = settingOff
	if settingIsOn(db, author, defaultConsultLead) {
		t.Error("an explicit Off beats the old authoring default")
	}
	plain.ConsultLead = settingOn
	if !settingIsOn(db, plain, defaultConsultLead) {
		t.Error("any agent can be switched on")
	}
	// An administrator's limit of Off holds everybody on the worker.
	setDeploymentSetting(db, deploymentMaximum, defaultConsultLead, settingOff)
	if settingIsOn(db, plain, defaultConsultLead) || settingIsOn(db, AgentRecord{ID: "a3", Author: true}, defaultConsultLead) {
		t.Error("the deployment limit keeps every agent from consulting")
	}
}

func TestTheAuthoringToolsNoLongerCarryConsult(t *testing.T) {
	for _, td := range builderAuthoringTools(&ToolSession{Username: "u"}, nil) {
		if td.Tool.Name == "consult" {
			t.Fatal("consult rides its own setting now; a dispatched run's copy could never work")
		}
	}
}
