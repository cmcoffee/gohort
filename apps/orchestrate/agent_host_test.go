package orchestrate

import (
	"reflect"
	"testing"

	"github.com/cmcoffee/gohort/core/agenthost"
)

// An app holds its agent as an agenthost.Agent for a turn and may change any of
// its fields: Scribe adds a prompt section and sets the dispatch policy, Anvil
// scopes the id and the reasoning. Every one of them has to land on the record
// the turn runs, and the rest of the record has to come through untouched. A
// field added to agenthost.Agent and not copied back would be a setting an app
// makes that silently does nothing, so this walks the struct rather than
// naming the fields.
func TestAnAppsTurnAdjustmentsReachTheRecord(t *testing.T) {
	rec := AgentRecord{ID: "orig", Name: "Orig", Description: "kept as it was", Owner: "alice", MCPExposed: true}
	a := AppAgentOf(rec)

	av := reflect.ValueOf(&a).Elem()
	for i := 0; i < av.NumField(); i++ {
		f := av.Type().Field(i)
		if f.Name == "Record" {
			continue
		}
		switch fv := av.Field(i); fv.Kind() {
		case reflect.String:
			fv.SetString("set-" + f.Name)
		case reflect.Bool:
			fv.SetBool(true)
		case reflect.Int:
			fv.SetInt(42)
		case reflect.Slice:
			fv.Set(reflect.ValueOf([]string{"set-" + f.Name}))
		default:
			t.Fatalf("agenthost.Agent.%s is a %s; teach this test to set it", f.Name, fv.Kind())
		}
	}

	got := reflect.ValueOf(appRecord(a))
	for i := 0; i < av.NumField(); i++ {
		name := av.Type().Field(i).Name
		if name == "Record" {
			continue
		}
		rf := got.FieldByName(name)
		if !rf.IsValid() {
			t.Errorf("agenthost.Agent.%s has no AgentRecord field of that name", name)
			continue
		}
		if !reflect.DeepEqual(rf.Interface(), av.Field(i).Interface()) {
			t.Errorf("%s did not reach the record: got %v, set %v", name, rf.Interface(), av.Field(i).Interface())
		}
	}
	back := appRecord(a)
	if back.Description != rec.Description || back.Owner != rec.Owner || !back.MCPExposed {
		t.Errorf("the rest of the record changed: %q, owner %q, exposed %v", back.Description, back.Owner, back.MCPExposed)
	}

	// And the other way: what LookupAppAgent hands an app is the record's.
	if fresh := AppAgentOf(rec); fresh.ID != "orig" || fresh.Name != "Orig" {
		t.Errorf("AppAgentOf = %+v", fresh)
	}
	var _ agenthost.Agent = a
}
