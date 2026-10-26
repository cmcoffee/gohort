package agenthost

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The inject endpoint reaches only the caller's own run, and somebody else's
// run answers exactly as no run does: whether someone else is running
// something is not the caller's business. A queue nobody owns is nobody's.
func TestInjectReachesOnlyTheCallersRun(t *testing.T) {
	auth := &core.DBase{Store: kvlite.MemStore()}
	prev := core.AuthDB
	core.AuthDB = func() core.Database { return auth }
	t.Cleanup(func() { core.AuthDB = prev })
	core.AuthSetUser(auth, "alice", "pw", false)
	core.AuthSetUser(auth, "bob", "pw", false)

	q := core.RegisterInjectionQueue("inject-test-run", "alice", "")
	core.RegisterInjectionQueue("inject-test-orphan", "", "")
	t.Cleanup(func() {
		core.ReleaseInjectionQueue("inject-test-run")
		core.ReleaseInjectionQueue("inject-test-orphan")
	})
	var queued []string
	h := InjectRoute{
		Refuse: func(_ *core.InjectionQueue, sid string) error {
			if strings.Contains(sid, "orphan") {
				return errors.New("refuse was asked about a queue the caller cannot reach")
			}
			return nil
		},
		Queued: func(sid string, _ bool) { queued = append(queued, sid) },
	}.Handler(auth)
	do := func(user, method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/inject", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: core.AuthCreateSession(auth, user)})
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	none := do("bob", "POST", `{"id":"inject-test-none","text":"x"}`)
	theirs := do("bob", "POST", `{"id":"inject-test-run","text":"x"}`)
	if theirs.Code != http.StatusNotFound || theirs.Body.String() != none.Body.String() {
		t.Errorf("another user's run answered %d %q, no run %d %q", theirs.Code, theirs.Body.String(), none.Code, none.Body.String())
	}
	for _, user := range []string{"alice", "bob"} {
		if w := do(user, "POST", `{"id":"inject-test-orphan","text":"x"}`); w.Code != http.StatusNotFound {
			t.Errorf("%s reached a queue nobody owns: %d", user, w.Code)
		}
	}
	if q.Len() != 0 || len(queued) != 0 {
		t.Fatalf("a note reached the run: %d queued, told %v", q.Len(), queued)
	}

	if w := do("alice", "POST", `{"id":"inject-test-run","text":"check the logs too"}`); w.Code != http.StatusOK {
		t.Fatalf("the owner's note answered %d %s", w.Code, w.Body.String())
	}
	if q.Len() != 1 || len(queued) != 1 || queued[0] != "inject-test-run" {
		t.Fatalf("the owner's note: %d queued, told %v", q.Len(), queued)
	}
	if w := do("alice", "PUT", `{"id":"inject-test-run","text":"x"}`); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT answered %d", w.Code)
	}
}

// What Refuse answers is the answer, and the note does not join the queue.
func TestInjectRefuseKeepsTheNoteOut(t *testing.T) {
	auth := &core.DBase{Store: kvlite.MemStore()}
	prev := core.AuthDB
	core.AuthDB = func() core.Database { return auth }
	t.Cleanup(func() { core.AuthDB = prev })
	core.AuthSetUser(auth, "alice", "pw", false)
	q := core.RegisterInjectionQueue("inject-test-closed", "alice", "")
	t.Cleanup(func() { core.ReleaseInjectionQueue("inject-test-closed") })
	h := InjectRoute{Refuse: func(*core.InjectionQueue, string) error { return jsonapi.Forbidden("closed") }}.Handler(auth)
	r := httptest.NewRequest("POST", "/api/inject", strings.NewReader(`{"id":"inject-test-closed","text":"x"}`))
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: core.AuthCreateSession(auth, "alice")})
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusForbidden || q.Len() != 0 {
		t.Errorf("a refused note answered %d and left %d queued", w.Code, q.Len())
	}
}
