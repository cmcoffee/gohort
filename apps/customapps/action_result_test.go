package customapps

import (
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/tools/appscript"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A game's turn as one action: it changes the state, which is saved, and says
// what happened, which the page gets back with the state as saved.
func TestAnActionHandsBackItsResultAndTheRecordsAsSaved(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	spec := AppSpec{Slug: "game", Owner: "alice", RecordKey: "id", Actions: []AppAction{{Name: "turn", Script: "print('x')"}}}
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	runAppScript = func(j appscript.Job) (string, error) {
		return `{"records":[{"hull":70}],"result":{"narrative":"A barge drifts close."}}`, nil
	}
	res, err := runActionAndPersist("alice", db, db, spec, spec.Actions[0], map[string]any{}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 || res.Records[0]["id"] == nil || res.Records[0]["created"] == nil || res.Records[0]["hull"] != float64(70) {
		t.Errorf("the records come back as saved, key and stamp filled in: %+v", res.Records)
	}
	if r, _ := res.Result.(map[string]any); r["narrative"] != "A barge drifts close." {
		t.Errorf("the script's result comes back: %+v", res.Result)
	}
	id, _ := res.Records[0]["id"].(string)
	var saved map[string]any
	if !db.Get(recTable("game"), id, &saved) || saved["hull"] != float64(70) {
		t.Errorf("the record was saved under the key handed back: %+v", saved)
	}

	runAppScript = func(j appscript.Job) (string, error) { return `{"message":"ok"}`, nil }
	res, _ = runActionAndPersist("alice", db, db, spec, spec.Actions[0], map[string]any{}, "alice")
	if res.Records == nil || len(res.Records) != 0 || res.Result != nil {
		t.Errorf("no records reads as an empty list and no result as none: %+v", res)
	}
}
