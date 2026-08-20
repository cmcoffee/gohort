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

// twoUsers points AuthDB at a store holding craig and dana.
func twoUsers(t *testing.T) Database {
	t.Helper()
	prev := AuthDB
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "craig", "pw", false)
	AuthSetUser(db, "dana", "pw", false)
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
		name    string
		method  string
		target  string
		handler http.HandlerFunc
	}{
		{"sessions", http.MethodGet, "/api/agent/sessions", T.handleAgentSessions},
		{"session", http.MethodGet, "/api/agent/sessions/s-1", T.handleAgentSession},
		{"send", http.MethodPost, "/api/agent/send", T.handleAgentSend},
		{"confirm", http.MethodPost, "/api/agent/confirm", T.handleAgentConfirm},
		{"cancel", http.MethodPost, "/api/agent/cancel", T.handleAgentCancel},
		{"echo", http.MethodPost, "/api/echo", T.handleEcho},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		c.handler(w, httptest.NewRequest(c.method, c.target, strings.NewReader("{}")))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: anonymous request got %d, want 401", c.name, w.Code)
		}
	}
}

func TestSessionListIsPerUser(t *testing.T) {
	twoUsers(t)
	freshStore(t)
	demoSessions.GetOrCreate("craig", "s-craig")
	demoSessions.GetOrCreate("dana", "s-dana")
	T := &HelloAgent{}

	w := httptest.NewRecorder()
	T.handleAgentSessions(w, as(t, http.MethodGet, "/api/agent/sessions", "dana", ""))
	body := w.Body.String()
	if strings.Contains(body, "s-craig") {
		t.Errorf("dana saw craig's session: %s", body)
	}
	if !strings.Contains(body, "s-dana") {
		t.Errorf("dana did not see her own session: %s", body)
	}
}

func TestCannotLoadOrDeleteAnotherUsersSession(t *testing.T) {
	twoUsers(t)
	freshStore(t)
	demoSessions.GetOrCreate("craig", "s-craig")
	T := &HelloAgent{}

	w := httptest.NewRecorder()
	T.handleAgentSession(w, as(t, http.MethodGet, "/api/agent/sessions/s-craig", "dana", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("dana read craig's session (%d)", w.Code)
	}

	w = httptest.NewRecorder()
	T.handleAgentSession(w, as(t, http.MethodDelete, "/api/agent/sessions/s-craig", "dana", ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("dana's delete of craig's session got %d, want 404", w.Code)
	}
	if _, ok := demoSessions.Get("craig", "s-craig"); !ok {
		t.Fatal("dana deleted craig's session")
	}

	// The owner still reaches their own.
	w = httptest.NewRecorder()
	T.handleAgentSession(w, as(t, http.MethodGet, "/api/agent/sessions/s-craig", "craig", ""))
	if w.Code != http.StatusOK {
		t.Errorf("craig could not read his own session (%d)", w.Code)
	}
}

func TestSendWithAnotherUsersSessionIDStartsAFreshOne(t *testing.T) {
	// Not an error, just not a way in: the id lands under the caller.
	twoUsers(t)
	freshStore(t)
	craigs := demoSessions.GetOrCreate("craig", "s-craig")
	craigs.Messages = append(craigs.Messages, demoMessage{Role: "user", Content: "private"})

	danas := demoSessions.GetOrCreate("dana", "s-craig")
	if danas == craigs {
		t.Fatal("dana reached craig's session by reusing its id")
	}
	if len(danas.Messages) != 0 {
		t.Error("dana's new session inherited craig's messages")
	}
	if got, _ := demoSessions.Get("craig", "s-craig"); len(got.Messages) != 1 {
		t.Error("craig's session was overwritten")
	}
}

func TestConfirmOnlyAnswersYourOwnSession(t *testing.T) {
	twoUsers(t)
	freshStore(t)
	craigs := demoSessions.GetOrCreate("craig", "s-craig")
	ch := make(chan string, 1)
	craigs.confirmCh = ch
	T := &HelloAgent{}

	w := httptest.NewRecorder()
	T.handleAgentConfirm(w, as(t, http.MethodPost, "/api/agent/confirm", "dana",
		`{"id":"c-1","value":"allow"}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("dana's answer got %d, want 409", w.Code)
	}
	select {
	case v := <-ch:
		t.Fatalf("dana answered craig's confirm prompt (%q)", v)
	default:
	}

	// The owner's answer lands.
	w = httptest.NewRecorder()
	T.handleAgentConfirm(w, as(t, http.MethodPost, "/api/agent/confirm", "craig",
		`{"id":"c-1","value":"allow"}`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("craig's own answer got %d", w.Code)
	}
	select {
	case v := <-ch:
		if v != "allow" {
			t.Errorf("wrong value delivered: %q", v)
		}
	default:
		t.Error("craig's answer never reached his session")
	}
}

func TestStoreMethodsAllTakeAUser(t *testing.T) {
	// The structural half of the fix. Nothing reaches a session without naming
	// whose it is, so a copied handler that forgets to resolve the caller
	// fails to compile rather than serving everyone's data to everyone. If
	// this ever stops being true, the scaffold is teaching the old shape again.
	freshStore(t)
	demoSessions.GetOrCreate("craig", "s-1")

	if got := demoSessions.List("dana"); len(got) != 0 {
		t.Errorf("List is not user-scoped: %v", got)
	}
	if _, ok := demoSessions.Get("dana", "s-1"); ok {
		t.Error("Get is not user-scoped")
	}
	if demoSessions.Delete("dana", "s-1") {
		t.Error("Delete is not user-scoped")
	}
	if demoSessions.AnswerConfirm("dana", "allow") {
		t.Error("AnswerConfirm is not user-scoped")
	}
}
