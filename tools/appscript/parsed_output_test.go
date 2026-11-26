package appscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A script's reply is read by the app, not shown to a model, so it comes back
// whole: a data source printing far more than a model is shown (a table of a
// few thousand records) is still valid JSON. One that prints more than a
// reply may be fails saying how much it printed, rather than being cut
// mid-value and reported as a script that printed no JSON.
func TestAScriptsReplyIsKeptWhole(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory for folders outside /tmp")
	}
	root, err := os.MkdirTemp(cache, "gohort-parsed-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	prevWS := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(root, "ws"))
	t.Cleanup(func() { SetWorkspacesDir(prevWS) })
	db := &DBase{Store: kvlite.MemStore()}

	big := `
import json
print(json.dumps([{"id": "r%04d" % i, "note": "n" * 80} for i in range(3000)]))
`
	out, err := Job{Owner: "alice", DB: db, Slug: "notes", Kind: "data", Name: "list", Script: big}.Run()
	if err != nil {
		t.Fatalf("run: %v\n%.300s", err, out)
	}
	if strings.Contains(out, "sandbox") && strings.Contains(out, "not") && !strings.Contains(out, "r0000") {
		t.Skipf("no sandbox here: %.300s", out)
	}
	var recs []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &recs); err != nil || len(recs) != 3000 {
		t.Fatalf("a %d-byte reply did not come back whole (%d records, %v): ...%s", len(out), len(recs), err, out[max(0, len(out)-200):])
	}

	huge := `
import sys
chunk = "x" * (1 << 20)
for _ in range(9):
    sys.stdout.write(chunk)
`
	out, err = Job{Owner: "alice", DB: db, Slug: "notes", Kind: "data", Name: "huge", Script: huge}.Run()
	if err == nil || !strings.Contains(err.Error(), "the script printed 9") || !strings.Contains(err.Error(), "8") {
		t.Errorf("a reply over the limit: err=%v, out %d bytes", err, len(out))
	}
}
