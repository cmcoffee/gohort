package core

// The page's change feed (/api/changes, core/changes): work starting or
// ending answers it at once, through the live watcher; a person's sessions
// topic is theirs alone; and a store can say whose it is, so a session save
// knows whose pages to wake.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/changes"
	"github.com/cmcoffee/snugforge/kvlite"
)

func askChanges(t *testing.T, target, user string) (int, map[string]string) {
	t.Helper()
	w := httptest.NewRecorder()
	handleChanges(w, asUser(t, target, user))
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestTheFeedNamesOnlyTheAskersOwnTopics(t *testing.T) {
	withUsers(t)
	AuthSetUser(AuthDB(), "dana", "pw", false)
	if code, _ := askChanges(t, "/api/changes?sessions=", ""); code != http.StatusUnauthorized {
		t.Errorf("a signed-out ask: %d", code)
	}
	if code, _ := askChanges(t, "/api/changes?nothing=", "craig"); code != http.StatusBadRequest {
		t.Errorf("an ask naming no topic: %d", code)
	}
	_, craig := askChanges(t, "/api/changes?sessions=", "craig")
	_, dana := askChanges(t, "/api/changes?sessions=", "dana")
	changes.Note(changes.ForUser("sessions", "dana"))
	_, craig2 := askChanges(t, "/api/changes?sessions=", "craig")
	_, dana2 := askChanges(t, "/api/changes?sessions=", "dana")
	if craig2["sessions"] != craig["sessions"] || dana2["sessions"] == dana["sessions"] {
		t.Errorf("one person's sessions moved another's: craig %v -> %v, dana %v -> %v", craig, craig2, dana, dana2)
	}
}

// A live session starting answers a page waiting on "live", well inside the
// ten seconds the pill used to take.
func TestWorkStartingAnswersAWaitingPage(t *testing.T) {
	withUsers(t)
	// The kick has to answer it: the slow reconcile would too, in time.
	liveReconcileEvery = time.Hour
	t.Cleanup(func() { liveReconcileEvery = 2 * time.Second })
	m := NewLiveSessionMap[string](0)
	RegisterLiveProvider(m.ActiveSessions)
	_, seen := askChanges(t, "/api/changes?live=", "craig")

	got := make(chan map[string]string, 1)
	go func() {
		_, v := askChanges(t, "/api/changes?wait=1&live="+seen["live"], "craig")
		got <- v
	}()
	time.Sleep(50 * time.Millisecond)
	m.Register("feed-test-run", "a run", func() {})
	select {
	case v := <-got:
		if v["live"] == seen["live"] {
			t.Errorf("the wait was answered without the version moving: %v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("work starting did not answer the waiting page")
	}

	// Ending it moves the version again.
	_, seen = askChanges(t, "/api/changes?live=", "craig")
	m.AppendEvent("feed-test-run", "done", true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	v := changes.Wait(ctx, map[string]string{liveTopic: seen["live"]}, time.Second)
	if v[liveTopic] == seen["live"] {
		t.Error("work ending did not move the live version")
	}
}

// The live version moves when the set does, not on every kick.
func TestALiveKickWithNothingMovedIsQuiet(t *testing.T) {
	reconcileLive()
	before, _ := changes.Version(liveTopic)
	LiveChanged()
	time.Sleep(400 * time.Millisecond)
	if after, _ := changes.Version(liveTopic); after != before {
		t.Errorf("a kick with the same live set moved the version: %s -> %s", before, after)
	}
}

func TestAStoreSaysWhoseItIs(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	alice := UserDB(root, "alice")
	if got := UserOfDB(alice); got != "alice" {
		t.Errorf("a user's store: %q", got)
	}
	if got := UserOfDB(alice.Sub("orchestrate")); got != "alice" {
		t.Errorf("a store inside a user's: %q", got)
	}
	if got := UserOfDB(root); got != "" {
		t.Errorf("a store that is nobody's: %q", got)
	}
	if got := UserOfDB(root.Sub("users")); got != "" {
		t.Errorf("a store named like one: %q", got)
	}
}
