package customapps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/tools/appscript"
	"github.com/cmcoffee/snugforge/kvlite"
)

func sharedSpec() AppSpec {
	return AppSpec{Slug: "game", Owner: "alice", RecordKey: "id", SharedCollections: []string{"leaderboard"},
		Actions: []AppAction{{Name: "submit", Script: "print('x')"}}}
}

// An action writes a shared collection by returning it; every script reads
// all of them as `shared`; each record is stamped with who triggered the
// write, server-side, whatever the script claimed; and a write to a
// collection the app does not declare changes nothing at all.
func TestAnActionWritesASharedCollectionEveryoneReads(t *testing.T) {
	ownerDB := &DBase{Store: kvlite.MemStore()}
	bobDB := &DBase{Store: kvlite.MemStore()}
	spec := sharedSpec()
	var gotShared []string
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	out := `{"message":"saved","shared":{"leaderboard":[{"id":"bob","score":120,"by":"mallory"}]},"records":[{"note":"mine"}]}`
	runAppScript = func(j appscript.Job) (string, error) {
		s, _ := j.Args["shared"].(string)
		gotShared = append(gotShared, s)
		return out, nil
	}
	res, err := runActionAndPersist("alice", ownerDB, bobDB, spec, spec.Actions[0], map[string]any{}, "bob")
	if err != nil || res.Message != "saved" || res.Saved != 2 || len(res.Records) != 1 {
		t.Fatalf("action: %+v %v", res, err)
	}
	board := readShared(ownerDB, spec, "leaderboard")
	if len(board) != 1 || board[0]["by"] != appscript.CallerAlias(spec, "bob") || board[0]["score"] != float64(120) || board[0]["created"] == nil {
		t.Fatalf("leaderboard = %+v (by must be the triggering user, not what the script said)", board)
	}
	if len(readShared(bobDB, spec, "leaderboard")) != 0 {
		t.Error("the shared record landed in the user's own store")
	}
	if len(gotShared) != 1 || gotShared[0] != `{"leaderboard":[]}` {
		t.Errorf("the script was handed shared = %q", gotShared)
	}
	created := board[0]["created"]

	// The next run sees what the first wrote, and an update keeps created.
	out = `{"shared":{"leaderboard":[{"id":"bob","score":150}]}}`
	runActionAndPersist("alice", ownerDB, bobDB, spec, spec.Actions[0], map[string]any{}, "bob")
	if !strings.Contains(gotShared[1], `"score":120`) {
		t.Errorf("the second run was not handed the first's record: %s", gotShared[1])
	}
	if b := readShared(ownerDB, spec, "leaderboard"); b[0]["score"] != float64(150) || b[0]["created"] != created {
		t.Errorf("after update %+v", b)
	}

	// Undeclared: refused, and the user's own records are not written either.
	out = `{"shared":{"secrets":[{"id":"x"}]},"records":[{"note":"should not land"}]}`
	before := len(bobDB.Keys(recTable("game")))
	if _, err := runActionAndPersist("alice", ownerDB, bobDB, spec, spec.Actions[0], map[string]any{}, "bob"); err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Fatalf("an undeclared collection was written: %v", err)
	}
	if len(bobDB.Keys(recTable("game"))) != before {
		t.Error("a refused shared write still saved the user's records")
	}

	// Delete.
	out = `{"shared_delete":{"leaderboard":["bob"]}}`
	runActionAndPersist("alice", ownerDB, bobDB, spec, spec.Actions[0], map[string]any{}, "bob")
	if len(readShared(ownerDB, spec, "leaderboard")) != 0 {
		t.Error("shared_delete left the record")
	}
}

// The bounds: a record over the size cap, or a collection over the count
// cap, is refused before anything is written.
func TestSharedWritesAreBounded(t *testing.T) {
	ownerDB := &DBase{Store: kvlite.MemStore()}
	spec := sharedSpec()
	big := map[string]any{"id": "a", "blob": strings.Repeat("x", maxSharedRecordBytes)}
	if _, err := applySharedWrites("alice", ownerDB, spec, "bob", map[string][]map[string]any{"leaderboard": {big}}, nil); err == nil {
		t.Error("an oversized record was written")
	}
	many := make([]map[string]any, maxSharedRecords+1)
	for i := range many {
		many[i] = map[string]any{"n": i}
	}
	if _, err := applySharedWrites("alice", ownerDB, spec, "bob", map[string][]map[string]any{"leaderboard": many}, nil); err == nil {
		t.Error("a collection over the cap was written")
	}
	if n := len(ownerDB.Keys(sharedTable("game", "leaderboard"))); n != 0 {
		t.Errorf("%d records written by refused writes", n)
	}
}

// The page reads a declared collection; it cannot write one, and an
// undeclared name is not found.
func TestThePageReadsButCannotWriteShared(t *testing.T) {
	ownerDB := &DBase{Store: kvlite.MemStore()}
	spec := sharedSpec()
	applySharedWrites("alice", ownerDB, spec, "bob", map[string][]map[string]any{"leaderboard": {{"id": "bob", "score": 3}}}, nil)
	T := &CustomApps{}
	w := httptest.NewRecorder()
	T.handleShared(w, httptest.NewRequest(http.MethodGet, "/apps/game/shared/leaderboard", nil), ownerDB, spec, "leaderboard")
	var got []map[string]any
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got) != 1 {
		t.Fatalf("read: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	T.handleShared(w, httptest.NewRequest(http.MethodPost, "/apps/game/shared/leaderboard", strings.NewReader(`[{"score":9999}]`)), ownerDB, spec, "leaderboard")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("a page write was accepted: %d", w.Code)
	}
	w = httptest.NewRecorder()
	T.handleShared(w, httptest.NewRequest(http.MethodGet, "/apps/game/shared/other", nil), ownerDB, spec, "other")
	if w.Code != http.StatusNotFound {
		t.Fatalf("an undeclared collection answered %d", w.Code)
	}
}

// A data source gets shared after the page's params, as an action does, so a
// page cannot hand its own view a collection of its choosing.
func TestDataSourceSharedCannotBeSent(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "club", Owner: "alice", SharedCollections: []string{"votes"},
		DataSources: []AppDataSource{{Name: "mine", Script: "x"}}}
	var got map[string]any
	saved := runAppScript
	runAppScript = func(j appscript.Job) (string, error) {
		got = j.Args
		return `[]`, nil
	}
	t.Cleanup(func() { runAppScript = saved })
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, `/apps/club/data/mine?shared={"votes":[{"by":"x"}]}&caller=x`, nil)
	T.handleData(w, r, "alice", "bob", T.recordBase(spec, "bob"), spec, "mine")
	if w.Code != http.StatusOK || got["shared"] != `{"votes":[]}` || got["caller"] != appscript.CallerAlias(spec, "bob") {
		t.Fatalf("%d %s: shared %v caller %v", w.Code, w.Body.String(), got["shared"], got["caller"])
	}
}

// A shared collection is read by every user of the app, so who wrote a record
// must not be their username (an email on most deployments). Writers are an
// alias per app: the same person matches themselves, nobody can read who it
// is, and the same person is someone else in another app. A record stamped
// before aliasing reads as the alias too.
func TestSharedWritersAreAliasedPerApp(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	game := AppSpec{Slug: "game", Owner: "alice", SharedCollections: []string{"board"}}
	other := AppSpec{Slug: "quiz", Owner: "alice"}
	a := appscript.CallerAlias(game, "bob@example.com")
	if !appscript.IsCallerAlias(a) || strings.Contains(a, "bob") {
		t.Fatalf("alias %q", a)
	}
	if appscript.CallerAlias(game, "bob@example.com") != a || appscript.CallerAlias(game, "carol@example.com") == a {
		t.Fatal("an alias must be stable for one person and differ between people")
	}
	if appscript.CallerAlias(other, "bob@example.com") == a {
		t.Fatal("the same person must be someone else in another app")
	}
	ownerDB := &DBase{Store: kvlite.MemStore()}
	ownerDB.Set(appscript.SharedTable("game", "board"), "old", map[string]any{"id": "old", "by": "bob@example.com"})
	board := readShared(ownerDB, game, "board")
	if len(board) != 1 || board[0]["by"] != a {
		t.Fatalf("an old record's writer reads as %v, want %s", board[0]["by"], a)
	}
	if strings.Contains(sharedInput(ownerDB, game), "example.com") {
		t.Fatal("a script's shared input still carries a username")
	}
}
