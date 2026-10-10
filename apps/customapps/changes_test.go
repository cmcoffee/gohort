package customapps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/tools/appscript"
	"github.com/cmcoffee/snugforge/kvlite"
)

func askChanges(t *testing.T, owner, user, slug, shared, records string) (map[string]string, time.Duration) {
	t.Helper()
	T := &CustomApps{}
	w := httptest.NewRecorder()
	start := time.Now()
	T.handleChanges(w, httptest.NewRequest(http.MethodGet, "/apps/"+slug+"/changes?shared="+shared+"&records="+records, nil), owner, user, slug)
	var v map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("changes answered %d %s", w.Code, w.Body.String())
	}
	return v, time.Since(start)
}

// The long poll answers at once when the page is behind, waits for a change
// when it is current, and answers with the same versions when nothing changed
// in time, so the page asks again.
func TestChangesWaitsForAChange(t *testing.T) {
	prev := changesWait
	changesWait = 300 * time.Millisecond
	t.Cleanup(func() { changesWait = prev })

	first, took := askChanges(t, "alice", "bob", "game", "", "")
	if took > 100*time.Millisecond || first["shared"] == "" || first["records"] == "" {
		t.Fatalf("a page with no versions should be answered at once: %v after %v", first, took)
	}
	go func() { time.Sleep(50 * time.Millisecond); noteChange(sharedChangeKey("alice", "game")) }()
	next, took := askChanges(t, "alice", "bob", "game", first["shared"], first["records"])
	if next["shared"] == first["shared"] || next["records"] != first["records"] || took > 250*time.Millisecond {
		t.Fatalf("a shared change should wake the wait: %v -> %v after %v", first, next, took)
	}
	quiet, took := askChanges(t, "alice", "bob", "game", next["shared"], next["records"])
	if quiet["shared"] != next["shared"] || took < 250*time.Millisecond {
		t.Fatalf("with nothing changing it should wait the full time and repeat the versions: %v after %v", quiet, took)
	}
	// Another user's records are not this user's change.
	noteChange(recordsChangeKey("alice", "game", "carol"))
	mine, _ := askChanges(t, "alice", "bob", "game", "", "")
	if mine["records"] != next["records"] {
		t.Errorf("carol's records changed bob's version")
	}
}

// An action that writes a shared collection changes it for everyone; one
// that writes the user's records changes them for that user.
func TestAnActionSignalsWhatItChanged(t *testing.T) {
	ownerDB, bobDB := &DBase{Store: kvlite.MemStore()}, &DBase{Store: kvlite.MemStore()}
	spec := sharedSpec()
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	sv0, _ := changeVersion(sharedChangeKey("alice", "game"))
	rv0, _ := changeVersion(recordsChangeKey("alice", "game", "bob"))
	runAppScript = func(appscript.Job) (string, error) {
		return `{"shared":{"leaderboard":[{"id":"bob","score":1}]}}`, nil
	}
	runActionAndPersist("alice", ownerDB, bobDB, spec, spec.Actions[0], map[string]any{}, "bob")
	sv1, _ := changeVersion(sharedChangeKey("alice", "game"))
	rv1, _ := changeVersion(recordsChangeKey("alice", "game", "bob"))
	if sv1 == sv0 || rv1 != rv0 {
		t.Fatalf("a shared-only write: shared %s->%s, records %s->%s", sv0, sv1, rv0, rv1)
	}
	runAppScript = func(appscript.Job) (string, error) {
		return `{"records":[{"note":"x"}]}`, nil
	}
	runActionAndPersist("alice", ownerDB, bobDB, spec, spec.Actions[0], map[string]any{}, "bob")
	sv2, _ := changeVersion(sharedChangeKey("alice", "game"))
	rv2, _ := changeVersion(recordsChangeKey("alice", "game", "bob"))
	if sv2 != sv1 || rv2 == rv1 {
		t.Fatalf("a records-only write: shared %s->%s, records %s->%s", sv1, sv2, rv1, rv2)
	}
}
