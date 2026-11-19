package scribe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/snugforge/kvlite"
)

// curatorWorld is alice's guides store, with an auth store that lists her,
// and no orchestrate host: a curator run started here fails at once, and the
// sweep notes that it started one (lastCuratorRun).
func curatorWorld(t *testing.T) (*Scribe, Database, Database) {
	t.Helper()
	prevRoot, prevAuth, prevHost := RootDB, AuthDB, agenthost.Get()
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	agenthost.Register(nil)
	t.Cleanup(func() {
		RootDB, AuthDB = prevRoot, prevAuth
		agenthost.Register(prevHost)
		lastCuratorRun.Delete("alice")
	})
	auth.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	T := &Scribe{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	return T, UserDB(T.DB, "alice"), auth
}

// heldRun is a finished curator run that held each of ids, finished at.
func heldRun(udb Database, at time.Time, ids ...string) {
	r := CuratorRun{ID: newID(), Started: at.UTC().Format(time.RFC3339), Finished: at.UTC().Format(time.RFC3339), Owner: "alice", Findings: len(ids)}
	for _, id := range ids {
		r.Entries = append(r.Entries, CuratorEntry{Kind: OutcomeHeld, FindingID: id, Note: "no guide fits it"})
	}
	saveCuratorRun(udb, r)
}

func swept(T *Scribe) bool {
	lastCuratorRun.Delete("alice")
	T.sweepCurator(context.Background())
	_, ran := lastCuratorRun.Load("alice")
	return ran
}

// The sweep does not run the curator over findings it only held last time
// with nothing new beside them: it would hold them again, each hour and on
// every restart. A finding filed since, or a day gone by, runs it.
func TestTheSweepLeavesFindingsTheCuratorOnlyHolds(t *testing.T) {
	T, udb, _ := curatorWorld(t)
	for _, id := range []string{"f1", "f2"} {
		f := mkFinding("disk", "the disk fills at night", "observed")
		f.ID = id
		udb.Set(findingsTable, id, f)
	}
	heldRun(udb, time.Now(), "f1", "f2")
	if swept(T) {
		t.Error("the sweep ran the curator over findings it only held, with nothing new")
	}

	f := mkFinding("disk", "a third note on the disk", "observed")
	f.ID = "f3"
	udb.Set(findingsTable, "f3", f)
	if !swept(T) {
		t.Error("a new finding beside the held ones did not run the curator")
	}

	udb.Unset(findingsTable, "f3")
	heldRun(udb, time.Now().Add(-25*time.Hour), "f1", "f2")
	for _, r := range listCuratorRuns(udb) {
		if r.Finished > time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) {
			udb.Unset(curatorRunsTable, r.ID)
		}
	}
	if !swept(T) {
		t.Error("held findings were never offered again after a day")
	}
}

// The person discards a finding that is waiting, one the curator keeps
// holding: it leaves the queue, and the digest records the discard as
// theirs. One not waiting is refused, and so is a discard while the curator
// is working through the queue.
func TestThePersonDiscardsAWaitingFinding(t *testing.T) {
	T, udb, auth := curatorWorld(t)
	f := mkFinding("disk", "the disk fills at night", "observed")
	f.ID = "f1"
	udb.Set(findingsTable, "f1", f)
	h := routed(T)
	post := func(body string) int {
		r := httptest.NewRequest(http.MethodPost, "/curator/discard", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, "alice")})
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}

	mu := curatorLock("alice")
	mu.Lock()
	if code := post(`{"finding_id":"f1"}`); code != http.StatusConflict {
		t.Errorf("a discard while the curator runs: %d", code)
	}
	mu.Unlock()

	if code := post(`{"finding_id":"f1"}`); code != http.StatusOK {
		t.Fatalf("discard: %d", code)
	}
	if len(udb.Keys(findingsTable)) != 0 {
		t.Error("the discarded finding is still waiting")
	}
	runs := listCuratorRuns(udb)
	if len(runs) != 1 || len(runs[0].Entries) != 1 || runs[0].Entries[0].Kind != OutcomeDiscarded || !strings.Contains(runs[0].Entries[0].Note, "discarded by alice") {
		t.Errorf("the digest does not record the discard as alice's: %+v", runs)
	}
	if code := post(`{"finding_id":"f1"}`); code != http.StatusNotFound {
		t.Errorf("discarding one no longer waiting: %d", code)
	}
}
