package orchestrate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

// peopleFixture is an owner with one agent that has served two people, and
// another user and an administrator to look in from outside.
type peopleFixture struct {
	t        *testing.T
	app      *OrchestrateApp
	root     Database
	agent    AgentRecord
	ann, ben string
}

func newPeopleFixture(t *testing.T) *peopleFixture {
	t.Helper()
	root := depStores(t, "alice", "bob", "root")
	root.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	savedVec := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	InvalidateChunkCache()
	t.Cleanup(func() { VectorDB = savedVec; InvalidateChunkCache() })
	pf := &peopleFixture{t: t, app: orchRef, root: root,
		ann: agenthost.OutsideScope("key1", "ann"), ben: agenthost.OutsideScope("key1", "ann2")}
	a, err := saveAgent(UserDB(root, "alice"), AgentRecord{Name: "Desk", Owner: "alice", OrchestratorPrompt: "You answer billing questions.", OutsideAudience: true})
	if err != nil {
		t.Fatal(err)
	}
	pf.agent = a
	ns := factsNamespace(a.ID)
	StoreMemoryFact(UserDB(root, "alice"), ns, "answer billing questions only")
	now := time.Now()
	for _, who := range []string{pf.ann, pf.ben} {
		notePersonTurn(root, "alice", a.ID, who, now)
		pdb := UserDB(root, who)
		StoreMemoryFact(pdb, ns, who+" order number is 4471")
		if _, err := saveChatSession(pdb, ChatSession{ID: "c1", AgentID: a.ID, Title: "Refund", LastAt: now,
			Messages: []ChatMessage{{Role: "user", Content: "where is my refund"}, {Role: "assistant", Content: "It went out Monday."}}}); err != nil {
			t.Fatal(err)
		}
		VectorDB.Set(EmbeddedChunks, who, EmbeddedChunk{ID: who, Source: agentKnowledgePrefix(who, a.ID) + ":general", ReportID: "orch-know-" + who, Text: "a finding"})
	}
	VectorDB.Set(EmbeddedChunks, "own", EmbeddedChunk{ID: "own", Source: agentKnowledgePrefix("alice", a.ID) + ":general", ReportID: "orch-know-own", Text: "the owner's finding"})
	InvalidateChunkCache()
	return pf
}

func (pf *peopleFixture) call(user, rawURL string) *jsonapi.Call {
	return &jsonapi.Call{W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodGet, rawURL, nil), User: user, UDB: UserDB(pf.root, user)}
}

func (pf *peopleFixture) ref(scope string) string { return personRef("alice", pf.agent.ID, scope) }

func peopleStatus(err error) int {
	var je *jsonapi.Error
	if errors.As(err, &je) {
		return je.Code
	}
	if err != nil {
		return 500
	}
	return 200
}

// The index counts each person's turns and remembers when they first came.
func TestEachOutsideTurnIsIndexedForItsOwner(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	first := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	notePersonTurn(root, "alice", "a1", "outside:k:ann", first)
	notePersonTurn(root, "alice", "a1", "outside:k:ann", first.Add(time.Hour))
	notePersonTurn(root, "alice", "a1", "alice", first) // never an account
	var p outsidePerson
	if !UserDB(root, "alice").Get(outsidePeopleTable, "a1|outside:k:ann", &p) || p.Turns != 2 || !p.First.Equal(first) || !p.Last.Equal(first.Add(time.Hour)) {
		t.Errorf("index row = %+v", p)
	}
	if n := len(UserDB(root, "alice").Keys(outsidePeopleTable)); n != 1 {
		t.Errorf("%d index rows, want only the outside person's", n)
	}
}

// The people an agent served are its owner's to look after, or an
// administrator's; nobody else's, and a ref naming an account is no person.
func TestOnlyTheOwnerOrAnAdminSeesAPerson(t *testing.T) {
	pf := newPeopleFixture(t)
	notes := "/orchestrate/api/outside-people/notes?id=" + url.QueryEscape(pf.ref(pf.ann))
	for user, want := range map[string]int{"alice": 200, "root": 200, "bob": 403} {
		rows, err := pf.app.handleOutsidePersonNotes(pf.call(user, notes))
		if peopleStatus(err) != want {
			t.Errorf("%s: status %d, want %d (%v)", user, peopleStatus(err), want, err)
		}
		if want == 200 && len(rows.([]map[string]any)) != 1 {
			t.Errorf("%s saw %v", user, rows)
		}
	}
	if _, err := pf.app.handleOutsidePersonNotes(pf.call("alice", "/x?id="+url.QueryEscape(personRef("alice", pf.agent.ID, "bob")))); peopleStatus(err) != 400 {
		t.Errorf("a ref naming an account: %d, want 400", peopleStatus(err))
	}
	if _, err := pf.app.handleOutsidePeople(pf.call("bob", "/x?agent="+pf.agent.ID)); peopleStatus(err) != 404 {
		t.Errorf("another user listed alice's agent's people: %d", peopleStatus(err))
	}
	rows, err := pf.app.handleOutsidePeople(pf.call("alice", "/x?agent="+pf.agent.ID))
	if err != nil || len(rows.([]map[string]any)) != 2 {
		t.Fatalf("the owner's list: %v %v", rows, err)
	}
	row := rows.([]map[string]any)[0]
	if row["key"] != "(a key since revoked)" || row["notes"] != 1 || row["conversations"] != 1 {
		t.Errorf("a row: %v", row)
	}
	all, err := pf.app.handleAllOutsidePeople(pf.call("root", "/x"))
	if err != nil || len(all.([]map[string]any)) != 2 {
		t.Errorf("the administrator's list across owners: %v %v", all, err)
	}
}

// A conversation reads back as a transcript, to the owner only.
func TestAConversationReadsBackAsATranscript(t *testing.T) {
	pf := newPeopleFixture(t)
	convs, err := pf.app.handleOutsidePersonConversations(pf.call("alice", "/x?id="+url.QueryEscape(pf.ref(pf.ann))))
	if err != nil || len(convs.([]map[string]any)) != 1 {
		t.Fatalf("conversations: %v %v", convs, err)
	}
	cref := convs.([]map[string]any)[0]["ref"].(string)
	got, err := pf.app.handleOutsidePersonConversation(pf.call("alice", "/x?ref="+url.QueryEscape(cref)))
	if err != nil || !strings.Contains(got.(map[string]any)["transcript"].(string), "Them: where is my refund\n\nAgent: It went out Monday.") {
		t.Errorf("transcript: %v %v", got, err)
	}
	if _, err := pf.app.handleOutsidePersonConversation(pf.call("bob", "/x?ref="+url.QueryEscape(cref))); peopleStatus(err) != 403 {
		t.Errorf("another user read it: %d", peopleStatus(err))
	}
}

// Clear forgets what the agent saved and keeps the conversations; Delete
// removes the person and nobody else.
func TestClearForgetsAndDeleteRemovesOnePerson(t *testing.T) {
	pf := newPeopleFixture(t)
	ns := factsNamespace(pf.agent.ID)
	if _, err := pf.app.handleOutsidePersonClear(pf.call("alice", "/x?id="+url.QueryEscape(pf.ref(pf.ann))), struct{}{}); err != nil {
		t.Fatal(err)
	}
	if n := len(livePersonNotes(UserDB(pf.root, pf.ann), pf.agent.ID)); n != 0 {
		t.Errorf("clear left %d notes", n)
	}
	if n := len(listChatSessions(UserDB(pf.root, pf.ann), pf.agent.ID)); n != 1 {
		t.Errorf("clear took the conversations: %d left", n)
	}

	if _, err := pf.app.handleOutsidePersonDelete(pf.call("alice", "/x?id="+url.QueryEscape(pf.ref(pf.ann))), struct{}{}); err != nil {
		t.Fatal(err)
	}
	if tables := UserDB(pf.root, pf.ann).Tables(); len(tables) != 0 {
		t.Errorf("the person's store kept %v", tables)
	}
	if UserDB(pf.root, "alice").Get(outsidePeopleTable, pf.agent.ID+"|"+pf.ann, &outsidePerson{}) {
		t.Error("the person is still listed")
	}
	if !UserDB(pf.root, "alice").Get(outsidePeopleTable, pf.agent.ID+"|"+pf.ben, &outsidePerson{}) {
		t.Error("deleting ann took ann2 off the list")
	}
	// The one whose id begins the same is untouched, and so is the owner.
	if len(livePersonNotes(UserDB(pf.root, pf.ben), pf.agent.ID)) != 1 || len(listChatSessions(UserDB(pf.root, pf.ben), pf.agent.ID)) != 1 {
		t.Error("deleting ann took ann2's memory or conversations")
	}
	if len(ListMemoryFacts(UserDB(pf.root, "alice"), ns)) != 1 {
		t.Error("deleting a person took the owner's notes")
	}
	left := map[string]bool{}
	for _, id := range VectorDB.Keys(EmbeddedChunks) {
		left[id] = true
	}
	if left[pf.ann] || !left[pf.ben] || !left["own"] {
		t.Errorf("findings after delete: %v; want ann's gone, ann2's and the owner's kept", left)
	}
	if _, err := pf.app.handleOutsidePersonDelete(pf.call("bob", "/x?id="+url.QueryEscape(pf.ref(pf.ben))), struct{}{}); peopleStatus(err) != 403 {
		t.Errorf("another user deleted a person: %d", peopleStatus(err))
	}
}

// An outside turn leaves its index row, so the owner can find the person.
func TestAnOutsideTurnLeavesItsIndexRow(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Happy to help.", Repeat: true})
	df.agent.OutsideAudience = true
	a, err := saveAgent(df.udb, df.agent)
	if err != nil {
		t.Fatal(err)
	}
	df.agent = a
	person := agenthost.OutsideScope("key1", "ann")
	df.run(AgentSyncRun{RuntimeUser: person, SubSessionID: "p", Message: "hello"})
	var p outsidePerson
	if !df.udb.Get(outsidePeopleTable, a.ID+"|"+person, &p) || p.Turns != 1 {
		t.Errorf("no index row after an outside turn: %+v", p)
	}
}

// A row says its counts in words, since a table row has no header to name
// a bare number.
func TestARowSaysItsCountsInWords(t *testing.T) {
	for n, want := range map[int]string{0: "no notes", 1: "1 note", 3: "3 notes"} {
		if got := countPhrase(n, "note"); got != want {
			t.Errorf("countPhrase(%d) = %q, want %q", n, got, want)
		}
	}
}

// The list across every owner answers an administrator only, through the
// real route: the handler itself does not check.
func TestTheAllOwnersListIsAdminOnly(t *testing.T) {
	app, root, done := gateFixture(t)
	defer done()
	root.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	r := sessionReq(t, root, "alice")
	r.Method, r.URL.Path = http.MethodGet, "/api/outside-people/all"
	w := httptest.NewRecorder()
	routed(app)(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("a user who is not an administrator got %d from the list across owners", w.Code)
	}
}
