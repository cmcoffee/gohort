package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A published agent chatted by a visitor writes its memory into the VISITOR's
// store under the author's agent id (handleSend: "memory stays with whoever is
// typing"). The author's pane reads the author's store and the record never
// exists in the visitor's, so before memoryAgent that memory was readable by
// nobody. The visitor's pane must resolve the author's record and read the
// visitor's data; a user the agent is not shared with must still get nothing.
func TestPublishedAgentMemoryReadableByVisitor(t *testing.T) {
	T, aliceDB, _ := newTestOrchestrate(t)
	adb := AuthDB()
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	adb.Set(AuthTable, "user:carol", AuthUser{Username: "carol"})

	ag, err := saveAgent(aliceDB, AgentRecord{
		Owner: "alice", Name: "Helper", OrchestratorPrompt: "help",
		AllowedUsers: []string{"bob"}, EnableNotes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Bob's turns left a fact and working notes in HIS store.
	bobDB := UserDB(T.DB, "bob")
	ns := factsNamespace(ag.ID)
	if _, ok, _ := StoreMemoryFact(bobDB, ns, "Bob prefers tea over coffee."); !ok {
		t.Fatal("seed fact not stored")
	}
	SaveOperatingNotes(bobDB, ns, "Waiting on Bob's order number.")

	get := func(user, path string, h func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	facts := func(w http.ResponseWriter, r *http.Request) { T.PublicHandleAgentFacts(w, r, ag.ID) }
	notes := func(w http.ResponseWriter, r *http.Request) { T.PublicHandleAgentNotes(w, r, ag.ID) }

	// The shared-with visitor sees their own scope.
	w := get("bob", "/agents/helper/api/facts", facts)
	if w.Code != http.StatusOK {
		t.Fatalf("bob facts: %d %s", w.Code, w.Body.String())
	}
	var fd struct {
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &fd); err != nil {
		t.Fatal(err)
	}
	if len(fd.Notes) != 1 || fd.Notes[0] != "Bob prefers tea over coffee." {
		t.Fatalf("bob facts = %v, want his one fact", fd.Notes)
	}
	w = get("bob", "/agents/helper/api/notes", notes)
	if w.Code != http.StatusOK {
		t.Fatalf("bob notes: %d %s", w.Code, w.Body.String())
	}
	var nd struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &nd); err != nil {
		t.Fatal(err)
	}
	if nd.Text != "Waiting on Bob's order number." {
		t.Fatalf("bob notes = %q", nd.Text)
	}

	// A user the agent is not shared with cannot resolve it at all.
	if w = get("carol", "/agents/helper/api/facts", facts); w.Code != http.StatusNotFound {
		t.Fatalf("carol facts: %d, want 404", w.Code)
	}

	// The author still reads the author's scope — Bob's fact does not leak.
	w = get("alice", "/agents/helper/api/facts", facts)
	if w.Code != http.StatusOK {
		t.Fatalf("alice facts: %d %s", w.Code, w.Body.String())
	}
	fd.Notes = nil
	if err := json.Unmarshal(w.Body.Bytes(), &fd); err != nil {
		t.Fatal(err)
	}
	if len(fd.Notes) != 0 {
		t.Fatalf("alice sees bob's memory: %v", fd.Notes)
	}
}

// An unpublished agent stays private to its author: the fallback must not
// resolve a record for a visitor just because they know the id.
func TestUnpublishedAgentMemoryStaysPrivate(t *testing.T) {
	T, aliceDB, _ := newTestOrchestrate(t)
	adb := AuthDB()
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	ag, err := saveAgent(aliceDB, AgentRecord{Owner: "alice", Name: "Private", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/agents/private/api/facts", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, "bob")})
	w := httptest.NewRecorder()
	T.PublicHandleAgentFacts(w, r, ag.ID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob on an unpublished agent: %d, want 404", w.Code)
	}
}
