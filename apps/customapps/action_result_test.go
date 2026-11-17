package customapps

import (
	"encoding/json"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
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
	res, err := runActionAndPersist("alice", db, db, spec, spec.Actions[0], map[string]any{}, "alice", scriptRun{Interactive: true})
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

	// A record keyed by a number (a timestamp) keeps it: stored under its
	// text, the number left as the script wrote it, and the same number again
	// updates that record rather than adding one. It used to read as no key,
	// and was replaced with a random id.
	tsSpec := AppSpec{Slug: "prices", Owner: "alice", RecordKey: "ts", Actions: []AppAction{{Name: "log", Script: "print('x')"}}}
	runAppScript = func(j appscript.Job) (string, error) {
		return `{"records":[{"ts":1731828315,"price":64554}]}`, nil
	}
	runActionAndPersist("alice", db, db, tsSpec, tsSpec.Actions[0], map[string]any{}, "alice", scriptRun{Interactive: true})
	res, _ = runActionAndPersist("alice", db, db, tsSpec, tsSpec.Actions[0], map[string]any{}, "alice", scriptRun{Interactive: true})
	if len(res.Records) != 1 || res.Records[0]["ts"] != float64(1731828315) {
		t.Errorf("a numeric key was not kept: %+v", res.Records)
	}
	if keys := db.Keys(recTable("prices")); len(keys) != 1 || keys[0] != "1731828315" {
		t.Errorf("stored under %v, want one record under 1731828315", keys)
	}
	for in, want := range map[string]string{`"ab"`: "ab", `"  "`: "", `2.5`: "2.5", `true`: "", `null`: ""} {
		var v any
		json.Unmarshal([]byte(in), &v)
		if got := recordKeyOf(map[string]any{"k": v}, "k"); got != want {
			t.Errorf("key %s reads %q, want %q", in, got, want)
		}
	}

	runAppScript = func(j appscript.Job) (string, error) { return `{"message":"ok"}`, nil }
	res, _ = runActionAndPersist("alice", db, db, spec, spec.Actions[0], map[string]any{}, "alice", scriptRun{Interactive: true})
	if res.Records == nil || len(res.Records) != 0 || res.Result != nil {
		t.Errorf("no records reads as an empty list and no result as none: %+v", res)
	}
}
