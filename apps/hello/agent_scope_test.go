// This package is the scaffold. Whatever shape it shows is the shape that gets
// copied, so its handlers are worth the same tests a real app's would get.
//
// It shipped with five API endpoints that never resolved the caller, over a
// process-global session map with no user dimension. The confirm handler fed
// the operator's decision to the first session in the process with a pending
// prompt — the same routing servitor inherited, where the sessions were SSH
// investigations and either user's Allow released the other's command.
package hello

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// twoUsers points AuthDB at a store holding alice and bob.
func twoUsers(t *testing.T) Database {
	t.Helper()
	prev := AuthDB
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "alice", "pw", false)
	AuthSetUser(db, "bob", "pw", false)
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	return db
}

// as builds a request carrying a valid session cookie for username.
func as(t *testing.T, method, target, username, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	token := AuthCreateSession(AuthDB(), username)
	t.Cleanup(func() { AuthDestroySession(AuthDB(), token) })
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
	return r
}

// routed is the app's real routes, under its sub-mux.
func routed(T *HelloAgent) http.HandlerFunc {
	mux := http.NewServeMux()
	T.SetWebMux(mux, "")
	T.Routes()
	return mux.ServeHTTP
}

// freshStore swaps in an empty store so tests do not see each other's state.
func freshStore(t *testing.T) {
	t.Helper()
	prev := demoSessions
	demoSessions = &demoStore{byUser: map[string]map[string]*demoSession{}}
	t.Cleanup(func() { demoSessions = prev })
}

func TestEveryAPIHandlerRequiresAUser(t *testing.T) {
	// The finding in one test: an anonymous request must reach none of them.
	twoUsers(t)
	freshStore(t)
	T := &HelloAgent{}

	cases := []struct {
		name   string
		method string
		target string
	}{
		{"sessions", http.MethodGet, "/api/agent/sessions"},
		{"session", http.MethodGet, "/api/agent/sessions/s-1"},
		{"session delete", http.MethodDelete, "/api/agent/sessions/s-1"},
		{"send", http.MethodPost, "/api/agent/send"},
		{"confirm", http.MethodPost, "/api/agent/confirm"},
		{"cancel", http.MethodPost, "/api/agent/cancel"},
		{"echo", http.MethodPost, "/api/echo"},
	}
	h := routed(T)
	for _, c := range cases {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(c.method, c.target, strings.NewReader("{}")))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: anonymous request got %d, want 401", c.name, w.Code)
		}
	}
}

func TestSessionListIsPerUser(t *testing.T) {
	twoUsers(t)
	freshStore(t)
	demoSessions.GetOrCreate("alice", "s-alice")
	demoSessions.GetOrCreate("bob", "s-bob")
	T := &HelloAgent{}

	w := httptest.NewRecorder()
	routed(T)(w, as(t, http.MethodGet, "/api/agent/sessions", "bob", ""))
	body := w.Body.String()
	if strings.Contains(body, "s-alice") {
		t.Errorf("bob saw alice's session: %s", body)
	}
	if !strings.Contains(body, "s-bob") {
		t.Errorf("bob did not see their own session: %s", body)
	}
}

func TestCannotLoadOrDeleteAnotherUsersSession(t *testing.T) {
	twoUsers(t)
	freshStore(t)
	demoSessions.GetOrCreate("alice", "s-alice")
	T := &HelloAgent{}

	w := httptest.NewRecorder()
	routed(T)(w, as(t, http.MethodGet, "/api/agent/sessions/s-alice", "bob", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("bob read alice's session (%d)", w.Code)
	}

	w = httptest.NewRecorder()
	routed(T)(w, as(t, http.MethodDelete, "/api/agent/sessions/s-alice", "bob", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("bob's delete of alice's session got %d, want 404", w.Code)
	}
	if _, ok := demoSessions.Get("alice", "s-alice"); !ok {
		t.Fatal("bob deleted alice's session")
	}

	// The owner still reaches their own.
	w = httptest.NewRecorder()
	routed(T)(w, as(t, http.MethodGet, "/api/agent/sessions/s-alice", "alice", ""))
	if w.Code != http.StatusOK {
		t.Errorf("alice could not read their own session (%d)", w.Code)
	}
}

func TestSendWithAnotherUsersSessionIDStartsAFreshOne(t *testing.T) {
	// Not an error, just not a way in: the id lands under the caller.
	twoUsers(t)
	freshStore(t)
	alices := demoSessions.GetOrCreate("alice", "s-alice")
	alices.Messages = append(alices.Messages, demoMessage{Role: "user", Content: "private"})

	bobs := demoSessions.GetOrCreate("bob", "s-alice")
	if bobs == alices {
		t.Fatal("bob reached alice's session by reusing its id")
	}
	if len(bobs.Messages) != 0 {
		t.Error("bob's new session inherited alice's messages")
	}
	if got, _ := demoSessions.Get("alice", "s-alice"); len(got.Messages) != 1 {
		t.Error("alice's session was overwritten")
	}
}

func TestConfirmOnlyAnswersYourOwnSession(t *testing.T) {
	twoUsers(t)
	freshStore(t)
	alices := demoSessions.GetOrCreate("alice", "s-alice")
	ch := make(chan string, 1)
	alices.confirmCh = ch
	T := &HelloAgent{}

	w := httptest.NewRecorder()
	routed(T)(w, as(t, http.MethodPost, "/api/agent/confirm", "bob",
		`{"id":"c-1","value":"allow"}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("bob's answer got %d, want 409", w.Code)
	}
	select {
	case v := <-ch:
		t.Fatalf("bob answered alice's confirm prompt (%q)", v)
	default:
	}

	// The owner's answer lands.
	w = httptest.NewRecorder()
	routed(T)(w, as(t, http.MethodPost, "/api/agent/confirm", "alice",
		`{"id":"c-1","value":"allow"}`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("alice's own answer got %d", w.Code)
	}
	select {
	case v := <-ch:
		if v != "allow" {
			t.Errorf("wrong value delivered: %q", v)
		}
	default:
		t.Error("alice's answer never reached their session")
	}
}

func TestStoreMethodsAllTakeAUser(t *testing.T) {
	// The structural half of the fix. Nothing reaches a session without naming
	// whose it is, so a copied handler that forgets to resolve the caller
	// fails to compile rather than serving everyone's data to everyone. If
	// this ever stops being true, the scaffold is teaching the old shape again.
	freshStore(t)
	demoSessions.GetOrCreate("alice", "s-1")

	if got := demoSessions.List("bob"); len(got) != 0 {
		t.Errorf("List is not user-scoped: %v", got)
	}
	if _, ok := demoSessions.Get("bob", "s-1"); ok {
		t.Error("Get is not user-scoped")
	}
	if demoSessions.Delete("bob", "s-1") {
		t.Error("Delete is not user-scoped")
	}
	if demoSessions.AnswerConfirm("bob", "allow") {
		t.Error("AnswerConfirm is not user-scoped")
	}
}

// The echo endpoint answers with the name it was sent, or the world when it
// was sent none, and refuses a method it does not take.
func TestEchoAnswersWithTheName(t *testing.T) {
	twoUsers(t)
	h := routed(&HelloAgent{})
	for body, want := range map[string]string{`{"name":"Alex"}`: `{"message":"hello, Alex"}`, `{}`: `{"message":"hello, world"}`} {
		w := httptest.NewRecorder()
		h(w, as(t, http.MethodPost, "/api/echo", "alice", body))
		if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != want {
			t.Errorf("echo %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h(w, as(t, http.MethodGet, "/api/echo", "alice", ""))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("a GET to echo answered %d, want 405", w.Code)
	}
}
