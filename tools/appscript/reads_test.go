package appscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/recquery"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A script that declares what it reads is handed those records and no
// others; the caller's arguments are not changed; a script with no reads
// gets every record, as before.
func TestAScriptIsHandedWhatItReads(t *testing.T) {
	all := `[{"id":"a","kind":"entry","n":3},{"id":"b","kind":"note","n":10},{"id":"c","kind":"entry","n":9}]`
	args := map[string]any{"records": all, "other": "x"}
	q, err := recquery.Parse(`{"where":{"kind":"entry"},"order":"-n"}`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readsApplied(args, q, "id")
	if err != nil {
		t.Fatal(err)
	}
	var recs []map[string]any
	json.Unmarshal([]byte(got["records"].(string)), &recs)
	if len(recs) != 2 || recs[0]["id"] != "c" || recs[1]["id"] != "a" || got["other"] != "x" {
		t.Errorf("handed %v", got)
	}
	if args["records"] != all {
		t.Error("the caller's arguments were changed")
	}
	if same, _ := readsApplied(map[string]any{"other": "x"}, q, "id"); same["other"] != "x" || same["records"] != nil {
		t.Errorf("arguments with no records: %v", same)
	}
	if _, err := readsApplied(map[string]any{"records": "not json"}, q, "id"); err == nil {
		t.Error("records that are not a list were taken")
	}
}

// End to end in the sandbox: a data source that declares its reads gets
// those records from records(), and asks for others with query() while it
// runs; a bad query says what is wrong.
func TestADataSourceReadsWhatItDeclaresAndAsksForMore(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory for folders outside /tmp")
	}
	root, err := os.MkdirTemp(cache, "gohort-reads-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	prevWS := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(root, "ws"))
	t.Cleanup(func() { SetWorkspacesDir(prevWS) })
	db := &DBase{Store: kvlite.MemStore()}
	for _, r := range []map[string]any{
		{"id": "a", "kind": "entry", "day": "2026-11-09", "title": "old one"},
		{"id": "b", "kind": "note", "day": "2026-11-10", "title": "a note"},
		{"id": "c", "kind": "entry", "day": "2026-11-12", "title": "this week"},
	} {
		db.Set(RecordsTable("prep"), r["id"].(string), r)
	}
	recJSON, _ := json.Marshal(ReadRecords(db, "prep"))
	reads, _ := recquery.Parse(`{"where":{"kind":"entry"}}`)
	script := `
import json
from gohort import records, query
mine = [r["id"] for r in records()]
week = [r["id"] for r in query({"where": {"day": {">=": "2026-11-10"}}, "order": "-day"})]
try:
    query({"where": {"day": {"~": 1}}})
    bad = "taken"
except Exception as e:
    bad = str(e)
print(json.dumps({"mine": mine, "week": week, "bad": bad}))
`
	out, err := Job{Owner: "alice", DB: db, Slug: "prep", Kind: "data", Name: "list", Script: script,
		Args: map[string]any{"records": string(recJSON)}, Reads: &reads, KeyField: "id"}.Run()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if strings.Contains(out, "sandbox") && strings.Contains(out, "not") && !strings.Contains(out, "mine") {
		t.Skipf("no sandbox here: %s", out)
	}
	if !strings.Contains(out, `"mine": ["a", "c"]`) || !strings.Contains(out, `"week": ["c", "b"]`) || !strings.Contains(out, "not a comparison") {
		t.Errorf("the script saw: %s", out)
	}
}
