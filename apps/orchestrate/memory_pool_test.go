package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// poolFixture is an agent alice owns and shares with bob and carol, with dave
// as its maintainer, and a vector store of its own.
type poolFixture struct {
	app   *OrchestrateApp
	agent AgentRecord
}

func newPoolFixture(t *testing.T, pool string) poolFixture {
	t.Helper()
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	savedVec := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	InvalidateChunkCache()
	t.Cleanup(func() { VectorDB = savedVec; InvalidateChunkCache() })
	rec := AgentRecord{ID: "a1", Owner: "alice", Name: "Troubleshooter", OrchestratorPrompt: "fix things",
		MemoryMode: memoryModeAgent, MemoryPool: pool,
		AllowedUsers: []string{"bob", "carol"}, Maintainers: []string{"dave"}}
	saved, err := saveAgent(udb, rec)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// An empty setting is an agent stored before the field existed; a new
	// one is given its setting on creation, so it is put back here.
	saved.MemoryPool = pool
	udb.Set(agentsTable, saved.ID, saved)
	return poolFixture{app: app, agent: saved}
}

// turn is a run of the fixture's agent by user.
func (f poolFixture) turn(user string) *chatTurn {
	owner := f.agent.Owner
	if user == owner {
		owner = ""
	}
	return &chatTurn{user: user, ownerUser: owner, app: f.app, agent: f.agent,
		udb: UserDB(f.app.DB, user), session: &ChatSession{ID: "s-" + user}}
}

// chunksIn is every chunk in user's copy of the agent's corpus.
func (f poolFixture) chunksIn(user string) []EmbeddedChunk {
	prefix := knowledgeSource(user, f.agent.ID, "")
	return ChunksWhere(VectorDB, func(c EmbeddedChunk) bool { return sourceInScope(c.Source, prefix) })
}

// addToPool puts a finding by `by` straight into the owner's copy.
func (f poolFixture) addToPool(id, by string, at time.Time) {
	VectorDB.Set(EmbeddedChunks, id, EmbeddedChunk{ID: id, Source: knowledgeSource(f.agent.Owner, f.agent.ID, "general"),
		ReportID: "orch-know-" + id, Section: "## note", Text: "finding " + id, By: by, Date: at.Format(time.RFC3339)})
	InvalidateChunkCache()
}

func (f poolFixture) save(t *testing.T, user, content string) string {
	t.Helper()
	out, err := f.turn(user).rememberToolDef().Handler(context.Background(), map[string]any{"content": content})
	if err != nil {
		t.Fatalf("%s saving %q: %v", user, content, err)
	}
	return out
}

// On a pooled agent, what a recipient works out goes into the owner's copy,
// marked as theirs, and nothing goes into their own.
func TestAPooledFindingGoesToTheOwnersCopyMarkedWithWhoAddedIt(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	f.save(t, "bob", "The printer queue stalls when the spooler disk is full; clearing /var/spool fixes it.")
	pool := f.chunksIn("alice")
	if len(pool) == 0 {
		t.Fatal("the finding did not reach the pool")
	}
	for _, c := range pool {
		if c.By != "bob" {
			t.Errorf("a pooled chunk does not say who added it: By=%q", c.By)
		}
	}
	if got := f.chunksIn("bob"); len(got) != 0 {
		t.Errorf("a pooled finding was also kept in the recipient's copy: %d chunk(s)", len(got))
	}
	// The owner's own run writes the same copy, unmarked: it is theirs.
	f.save(t, "alice", "The badge reader on floor two needs a power cycle after an outage.")
	for _, c := range f.chunksIn("alice") {
		if strings.Contains(c.Text, "badge reader") && c.By != "" {
			t.Errorf("the owner's own finding is marked as someone else's: By=%q", c.By)
		}
	}
}

// An agent that does not pool, which is every agent made before pooling
// existed, keeps each person's findings in their own copy.
func TestAnAgentThatDoesNotPoolKeepsFindingsPrivate(t *testing.T) {
	for _, pool := range []string{"", memoryPoolOff} {
		f := newPoolFixture(t, pool)
		f.save(t, "bob", "The VPN drops when the laptop sleeps; turning off power saving keeps it up.")
		if len(f.chunksIn("bob")) == 0 || len(f.chunksIn("alice")) != 0 {
			t.Errorf("MemoryPool=%q: a finding left the recipient's own copy", pool)
		}
	}
}

// A clean room saves nothing durable, a person outside gohort keeps their own
// store, and an app's agent follows its app: none of them write the pool.
func TestSomeRunsNeverWriteThePool(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	incognito := f.turn("bob")
	incognito.session.Incognito = true
	if incognito.poolOwner() != "" {
		t.Error("a clean-room run would write the pool")
	}
	outside := f.turn("outside:web:visitor1")
	outside.user = "outside:web:visitor1"
	if !outside.outsideAudience() {
		t.Skip("the outside-scope form changed; update this test")
	}
	if outside.poolOwner() != "" {
		t.Error("a person outside gohort would write the pool")
	}
	app := f.turn("bob")
	app.agent.OwningApp = "helpdesk"
	if app.poolOwner() != "" {
		t.Error("an app's agent would write the pool")
	}
	if f.turn("alice").poolOwner() != "" {
		t.Error("the owner's run is treated as a contribution to their own copy")
	}
}

// One person adds at most poolDailyCap findings a day; past that a finding
// goes to their own copy, and the reply says why.
func TestAPersonsDailyContributionsAreCapped(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	now := time.Now()
	for i := 0; i < poolDailyCap; i++ {
		f.addToPool(fmt.Sprintf("b%d", i), "bob", now)
	}
	// Yesterday's do not count against today.
	f.addToPool("old", "carol", now.Add(-25*time.Hour))
	out := f.save(t, "bob", "The scanner needs its driver reinstalled after every major update.")
	if !strings.Contains(out, "saved to your own memory only") {
		t.Errorf("the reply does not say the finding stayed private: %q", out)
	}
	if len(f.chunksIn("bob")) == 0 {
		t.Error("a finding past the cap was lost instead of kept in the person's own copy")
	}
	if n := poolContributionsToday("alice", "a1", "carol", now); n != 0 {
		t.Errorf("a finding from 25 hours ago counted as today's: %d", n)
	}
	f.save(t, "carol", "The shared drive maps only after the user signs in twice.")
	if n := poolContributionsToday("alice", "a1", "carol", now); n != 1 {
		t.Errorf("another person's first finding of the day did not reach the pool: %d", n)
	}
}

// Anyone removes what they added; only the owner and maintainers remove
// anyone else's. Checked in the tool, whatever the model was told.
func TestOnlyTheContributorOrAManagerRemovesAPooledFinding(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	now := time.Now()
	f.addToPool("c1", "carol", now)
	f.addToPool("b1", "bob", now)
	f.addToPool("o1", "", now)

	bob := f.turn("bob")
	for _, id := range []string{"orch-know-c1", "orch-know-o1"} {
		msg, err := bob.forgetFindingByReportID(id)
		if err != nil || !strings.Contains(msg, "only the owner or a maintainer can remove it") {
			t.Errorf("bob removing %s: %q, %v", id, msg, err)
		}
	}
	if msg, _ := bob.forgetFindingByReportID("orch-know-b1"); !strings.Contains(msg, "Forgot") {
		t.Errorf("bob cannot remove his own pooled finding: %q", msg)
	}
	// The other tool path refuses the same way.
	if msg, _ := bob.memoryForget(map[string]any{"id": "c1"}); !strings.Contains(msg, "added by carol") {
		t.Errorf("memory(forget) let bob remove carol's finding: %q", msg)
	}
	var kept EmbeddedChunk
	if !VectorDB.Get(EmbeddedChunks, "c1", &kept) || !VectorDB.Get(EmbeddedChunks, "o1", &kept) {
		t.Fatal("a refused removal removed something")
	}
	// A maintainer removes anyone's.
	if msg, _ := f.turn("dave").forgetFindingByReportID("orch-know-c1"); !strings.Contains(msg, "Forgot") {
		t.Errorf("a maintainer cannot remove a pooled finding: %q", msg)
	}
	// On an agent that does not pool, the owner's copy stays out of reach.
	f.agent.MemoryPool = memoryPoolOff
	if msg, _ := f.turn("bob").forgetFindingByReportID("orch-know-o1"); strings.Contains(msg, "Forgot") {
		t.Error("a recipient removed the owner's finding on an agent that does not pool")
	}
}

// A finding another person added reads as theirs; the reader's own and the
// owner's read plain.
func TestAPooledFindingIsLabelledWithWhoAddedIt(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	hits := f.turn("bob").labelPoolHits([]SearchHit{
		{Text: "a", By: "carol"}, {Text: "b", By: "bob"}, {Text: "c"},
	})
	if hits[0].Text != "From carol: a" || hits[1].Text != "b" || hits[2].Text != "c" {
		t.Errorf("labels: %q %q %q", hits[0].Text, hits[1].Text, hits[2].Text)
	}
}

// On a pooled agent "what it worked out" is always read: a pool nobody reads
// is not one. Off again, the owner's switch decides as before.
func TestAPooledAgentAlwaysReadsTheOwnersFindings(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	f.agent.ShareReference = "off"
	if !f.turn("bob").readsOwnerReference() {
		t.Error("a pooled agent's recipient does not read the pool")
	}
	f.agent.MemoryPool = memoryPoolOff
	if f.turn("bob").readsOwnerReference() {
		t.Error("the owner's switch stopped mattering on an agent that does not pool")
	}
}

// A recipient's pinned note stays theirs, and the reply says so.
func TestAPinnedNoteOnAPooledAgentStaysWithThePerson(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	out, err := f.turn("bob").rememberToolDef().Handler(context.Background(), map[string]any{
		"content": "Bob prefers answers as numbered steps.", "pin": true, "domain": "self"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "This note is yours only") {
		t.Errorf("the reply does not say the note stays private: %q", out)
	}
	if notes := ListMemoryFacts(UserDB(f.app.DB, "alice"), factsNamespace("a1")); len(notes) != 0 {
		t.Errorf("a recipient's pinned note reached the owner: %+v", notes)
	}
}

// A new agent's pool setting is written once, at creation, by what kind of
// agent it is. An agent already stored keeps what it has, empty included.
func TestANewAgentIsCreatedPoolingByItsKind(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	make := func(id, mode string) AgentRecord {
		a, err := saveAgent(udb, AgentRecord{ID: id, Owner: "alice", Name: id, OrchestratorPrompt: "p", MemoryMode: mode})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if a := make("spec", memoryModeAgent); a.MemoryPool != memoryPoolOn {
		t.Errorf("a new specialist does not pool: %q", a.MemoryPool)
	}
	if a := make("bot", memoryModeChatbot); a.MemoryPool != memoryPoolOff {
		t.Errorf("a new chatbot pools: %q", a.MemoryPool)
	}
	if a := make("plain", ""); a.MemoryPool != memoryPoolOff {
		t.Errorf("a new assistant pools: %q", a.MemoryPool)
	}
	// An agent stored before the field existed is left empty on its next
	// save, which reads as off.
	legacy := AgentRecord{ID: "old", Owner: "alice", Name: "old", OrchestratorPrompt: "p", MemoryMode: memoryModeAgent}
	udb.Set(agentsTable, legacy.ID, legacy)
	if a, err := saveAgent(udb, legacy); err != nil || a.MemoryPool != "" {
		t.Errorf("an existing agent started pooling on save: %q, %v", a.MemoryPool, err)
	}
	app := AgentRecord{Owner: "alice", Name: "app", OrchestratorPrompt: "p", MemoryMode: memoryModeAgent, OwningApp: "helpdesk"}
	settleNewAgentMemoryPool(udb, &app)
	if app.MemoryPool != "" {
		t.Errorf("an app's agent got a pool setting: %q", app.MemoryPool)
	}
}

// The Memory window's routes: a recipient sees the pool apart from their own
// copy, with a delete only on what they added, and the routes refuse the rest
// whatever the page shows.
func TestTheMemoryWindowShowsThePoolAndEnforcesItsRules(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	adb := AuthDB()
	for _, u := range []string{"bob", "carol", "dave"} {
		adb.Set(AuthTable, "user:"+u, AuthUser{Username: u})
	}
	now := time.Now()
	f.addToPool("c1", "carol", now)
	f.addToPool("b1", "bob", now)

	call := func(user, method, path string, h http.HandlerFunc, item string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		r.SetPathValue("id", f.agent.ID)
		r.SetPathValue("item", item)
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	w := call("bob", "GET", "/api/agents/a1/inferred", f.app.inferredListRoute(ownAgent), "")
	var got struct {
		Items  []inferredRow `json:"items"`
		Pool   []inferredRow `json:"pool"`
		Manage bool          `json:"pool_manage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if len(got.Pool) != 2 || got.Manage {
		t.Fatalf("bob's view of the pool: %+v manage=%v", got.Pool, got.Manage)
	}
	for _, row := range got.Pool {
		want := row.By == "bob"
		if row.CanDelete == nil || *row.CanDelete != want {
			t.Errorf("entry by %s: can_delete=%v, want %v", row.By, row.CanDelete, want)
		}
	}

	if w := call("bob", "DELETE", "/api/agents/a1/inferred/c1", f.app.inferredDeleteRoute(ownAgent), "c1"); w.Code != http.StatusForbidden {
		t.Errorf("bob deleting carol's entry: %d %s", w.Code, w.Body.String())
	}
	if w := call("bob", "DELETE", "/api/agents/a1/knowledge/auto-inferred?pool=1", f.app.autoInferredRoute(ownAgent), ""); w.Code != http.StatusForbidden {
		t.Errorf("bob clearing the pool: %d %s", w.Code, w.Body.String())
	}
	if w := call("bob", "DELETE", "/api/agents/a1/knowledge/auto-inferred?pool=1&by=bob", f.app.autoInferredRoute(ownAgent), ""); w.Code != 200 {
		t.Errorf("bob removing everything he added: %d %s", w.Code, w.Body.String())
	}
	var c EmbeddedChunk
	if VectorDB.Get(EmbeddedChunks, "b1", &c) {
		t.Error("bob's own entry survived removing everything he added")
	}
	if !VectorDB.Get(EmbeddedChunks, "c1", &c) {
		t.Fatal("removing bob's entries removed carol's")
	}
	// The owner's copy is the pool, so the owner deletes from it as their own.
	if w := call("alice", "DELETE", "/api/agents/a1/inferred/c1", f.app.inferredDeleteRoute(ownAgent), "c1"); w.Code != http.StatusNoContent {
		t.Errorf("the owner deleting an entry: %d %s", w.Code, w.Body.String())
	}
}

// The label is on what the search hands the model, not only in the helper.
func TestARecipientsRecallLabelsAnotherPersonsFinding(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	f.addToPool("c1", "carol", time.Now())
	bob := f.turn("bob")
	hits := bob.searchOwnKnowledge(context.Background(), generalTopic, "finding c1", nil, 5, nil, ChunkScopeDerivedOnly)
	if len(hits) == 0 {
		t.Skip("no search without an embedder here")
	}
	for _, h := range hits {
		if h.ID == "c1" && !strings.HasPrefix(h.Text, "From carol: ") {
			t.Errorf("a recipient's search returned carol's finding unlabelled: %q", h.Text)
		}
	}
}
