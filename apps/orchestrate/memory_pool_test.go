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
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/shareledger"
	"github.com/cmcoffee/gohort/core/ui"
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

// told captures the notices people get through shareledger.NotifyRecipient.
func told(t *testing.T) *[][2]string {
	t.Helper()
	got := &[][2]string{}
	prev := shareledger.NotifyRecipient
	shareledger.NotifyRecipient = func(u, title, intro string, _ []string) {
		*got = append(*got, [2]string{u, title + " | " + intro})
	}
	t.Cleanup(func() { shareledger.NotifyRecipient = prev })
	return got
}

func (f poolFixture) patch(t *testing.T, user string, body map[string]any) (any, error) {
	t.Helper()
	r := httptest.NewRequest("PATCH", "/api/agents/a1", nil)
	return f.app.patchAgent(&jsonapi.Call{R: r, User: user, UDB: UserDB(f.app.DB, user)}, body, f.agent.ID)
}

// The owner turns pooling on and off on the Share tab, which PATCHes the
// record. Only the two named values are taken, and a maintainer cannot
// switch it.
func TestTheOwnerSwitchesPoolingOnAnExistingAgent(t *testing.T) {
	f := newPoolFixture(t, "")
	told(t)
	if _, err := f.patch(t, "alice", map[string]any{"memory_pool": "maybe"}); err == nil {
		t.Error("an unknown value was accepted")
	}
	if _, err := f.patch(t, "alice", map[string]any{"memory_pool": memoryPoolOn}); err != nil {
		t.Fatalf("the owner cannot turn pooling on: %v", err)
	}
	a, _ := loadAgent(UserDB(f.app.DB, "alice"), f.agent.ID)
	if !a.poolsMemory() {
		t.Fatalf("pooling did not stick: %q", a.MemoryPool)
	}
	// A maintainer's change leaves it as the owner set it.
	f.patch(t, "dave", map[string]any{"memory_pool": memoryPoolOff, "description": "edited"})
	if a, _ := loadAgent(UserDB(f.app.DB, "alice"), f.agent.ID); !a.poolsMemory() {
		t.Error("a maintainer switched pooling off")
	}
}

// Turning pooling on tells everyone it is shared with, maintainers in their
// own words; a later save tells nobody again; someone added later is told on
// joining; an agent that does not pool tells nobody.
func TestPeopleAreToldWhenTheyComeUnderAPool(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOff)
	got := told(t)
	udb := UserDB(f.app.DB, "alice")
	a := f.agent
	a.Description = "no pooling yet"
	saveAgent(udb, a)
	if len(*got) != 0 {
		t.Fatalf("an agent that does not pool told people something: %v", *got)
	}
	a.MemoryPool = memoryPoolOn
	saveAgent(udb, a)
	who := map[string]string{}
	for _, n := range *got {
		who[n[0]] = n[1]
	}
	if len(who) != 3 || who["bob"] == "" || who["carol"] == "" || who["dave"] == "" {
		t.Fatalf("turning pooling on told %v", who)
	}
	if !strings.Contains(who["bob"], "notes you pin stay yours") || !strings.Contains(who["dave"], "As a maintainer you can curate it") {
		t.Errorf("the wrong words: bob=%q dave=%q", who["bob"], who["dave"])
	}
	*got = nil
	a.Description = "edited again"
	saveAgent(udb, a)
	if len(*got) != 0 {
		t.Errorf("a save that changed nothing about the pool told people again: %v", *got)
	}
	a.AllowedUsers = append(a.AllowedUsers, "erin")
	saveAgent(udb, a)
	if len(*got) != 1 || (*got)[0][0] != "erin" {
		t.Errorf("adding someone to a pooled agent told %v, want erin only", *got)
	}
}

// The Share tab's switch: off first, so an agent stored before the setting
// reads as off; and a chatbot is warned about what pooling it means.
func TestTheShareTabOffersPoolingAndWarnsForAChatbot(t *testing.T) {
	spec := memoryPoolChoice(AgentRecord{MemoryMode: memoryModeAgent})
	if spec.Field != "memory_pool" || len(spec.Options) != 2 || spec.Options[0].Value != memoryPoolOff {
		t.Fatalf("the switch: %+v", spec)
	}
	if strings.Contains(spec.Help, "Careful") {
		t.Error("a specialist is warned off pooling, which is its default")
	}
	// While pooled, "what it worked out" is read by everyone, so the choice
	// is not offered: one that silently did nothing would mislead.
	app, _, _ := newTestOrchestrate(t)
	ap := &accessPage{T: app, agent: AgentRecord{ID: "a1", Owner: "alice"}, user: "alice", patchURL: "x"}
	var share ui.FormPanel
	for _, sec := range ap.shareSections() {
		if sec.Title == "What a recipient sees" {
			share, _ = sec.Body.(ui.FormPanel)
		}
	}
	shown := map[string]string{}
	for _, fl := range share.Fields {
		shown[fl.Field] = fl.ShowWhen
	}
	if _, ok := shown["memory_pool"]; !ok || shown["share_reference"] != "memory_pool:!"+memoryPoolOn {
		t.Errorf("the Share tab's memory fields: %v", shown)
	}
	bot := memoryPoolChoice(AgentRecord{MemoryMode: memoryModeChatbot})
	if !strings.Contains(bot.Help, "Careful") || !strings.Contains(bot.Help, "reaches everyone it is shared with") {
		t.Errorf("a chatbot is pooled without a warning: %+v", bot)
	}
}

// The owner hears once that someone reached the day's limit, however many
// more they try.
func TestTheOwnerHearsWhenSomeoneReachesTheDailyLimit(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	now := time.Now()
	for i := 0; i < poolDailyCap; i++ {
		f.addToPool(fmt.Sprintf("b%d", i), "bob", now)
	}
	f.save(t, "bob", "One finding past the limit.")
	f.save(t, "bob", "And another past it.")
	var mine []notices.Notice
	for _, n := range notices.List(RootDB, "alice") {
		if strings.Contains(n.Title, "bob reached the day's limit") {
			mine = append(mine, n)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("the owner got %d notices about bob's limit, want 1", len(mine))
	}
	if mine[0].Count < 2 {
		t.Errorf("a repeat did not fold into the one notice: count=%d", mine[0].Count)
	}
}

// A contribution saved at the limit counts as the contributor's from the
// start: the save marks it before the cap runs, so it never pushes out the
// owner's oldest finding.
func TestAContributionAtTheLimitKeepsTheOwnersFindings(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	tdb := &DBase{Store: kvlite.MemStore()}
	tdb.Set(WebTable, tuneFindingHardCap, float64(2))
	SetTunablesDB(tdb)
	t.Cleanup(func() { SetTunablesDB(nil) })
	f.save(t, "alice", "The backup job needs the share mounted before it starts.")
	f.save(t, "alice", "The label printer only takes the narrow rolls.")
	f.save(t, "bob", "The meeting room screen wakes only from its own remote.")
	own := 0
	for _, c := range f.chunksIn("alice") {
		if c.By == "" {
			own++
		}
	}
	if own < 2 {
		t.Errorf("a contribution evicted the owner's findings: %d of 2 left", own)
	}
}

// memCall drives one of the Memory window's routes as user.
func (f poolFixture) memCall(t *testing.T, user, method string, h http.HandlerFunc, item string) *httptest.ResponseRecorder {
	t.Helper()
	adb := AuthDB()
	adb.Set(AuthTable, "user:"+user, AuthUser{Username: user})
	r := httptest.NewRequest(method, "/api/agents/a1/x", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
	r.SetPathValue("id", f.agent.ID)
	r.SetPathValue("item", item)
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// removedFor is the Recently removed list as user sees it.
func (f poolFixture) removedFor(t *testing.T, user string) []removedRow {
	t.Helper()
	w := f.memCall(t, user, "GET", f.app.poolRemovedRoute(ownAgent), "")
	var got struct {
		Items []removedRow `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("%s listing Recently removed: %d %s", user, w.Code, w.Body.String())
	}
	return got.Items
}

func (f poolFixture) has(id string) bool {
	var c EmbeddedChunk
	return VectorDB.Get(EmbeddedChunks, id, &c)
}

// Whatever is removed from a pool by a click can be put back for 30 days: one
// finding, and a wipe as a whole. The owner and maintainers see and restore
// every removal; anyone else only what they removed.
func TestARemovalFromThePoolCanBeRestored(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	now := time.Now()
	f.addToPool("b1", "bob", now)
	f.addToPool("c1", "carol", now)
	f.addToPool("o1", "", now)

	if w := f.memCall(t, "bob", "DELETE", f.app.inferredDeleteRoute(ownAgent), "b1"); w.Code != http.StatusNoContent {
		t.Fatalf("bob deleting his own entry: %d %s", w.Code, w.Body.String())
	}
	if w := f.memCall(t, "alice", "DELETE", f.app.autoInferredRoute(ownAgent), ""); w.Code != 200 {
		t.Fatalf("the owner wiping: %d %s", w.Code, w.Body.String())
	}
	if f.has("b1") || f.has("c1") || f.has("o1") {
		t.Fatal("the removals did not remove")
	}

	mine := f.removedFor(t, "bob")
	if len(mine) != 1 || mine[0].RemovedBy != "bob" || mine[0].Entries != 1 {
		t.Fatalf("bob sees %+v, want only his own removal", mine)
	}
	if got := f.removedFor(t, "carol"); len(got) != 0 {
		t.Errorf("carol sees removals she did not make: %+v", got)
	}
	if got := f.removedFor(t, "dave"); len(got) != 2 {
		t.Errorf("the maintainer sees %d removals, want 2", len(got))
	}
	all := f.removedFor(t, "alice")
	if len(all) != 2 {
		t.Fatalf("the owner sees %d removals, want 2", len(all))
	}
	wipe := all[0]
	if wipe.RemovedBy != "alice" || wipe.Entries != 2 || wipe.What != wipeWhat("") {
		t.Fatalf("the wipe is not one entry holding both: %+v", wipe)
	}

	// Carol cannot restore what bob removed, though it is in the pool.
	if w := f.memCall(t, "carol", "POST", f.app.poolRestoreRoute(ownAgent), mine[0].ID); w.Code != http.StatusNotFound {
		t.Errorf("carol restoring bob's removal: %d %s", w.Code, w.Body.String())
	}
	if w := f.memCall(t, "bob", "POST", f.app.poolRestoreRoute(ownAgent), mine[0].ID); w.Code != 200 || !f.has("b1") {
		t.Fatalf("bob restoring his own removal: %d %s", w.Code, w.Body.String())
	}
	var back EmbeddedChunk
	VectorDB.Get(EmbeddedChunks, "b1", &back)
	if back.By != "bob" || back.Text != "finding b1" {
		t.Errorf("the restored finding is not as it was: %+v", back)
	}
	if w := f.memCall(t, "alice", "POST", f.app.poolRestoreRoute(ownAgent), wipe.ID); w.Code != 200 || !f.has("c1") || !f.has("o1") {
		t.Fatalf("the owner restoring the wipe as a whole: %d %s", w.Code, w.Body.String())
	}
	if got := f.removedFor(t, "alice"); len(got) != 0 {
		t.Errorf("a restored removal is still listed: %+v", got)
	}
}

// A forget in a run, at the person's word, is kept the same way.
func TestAForgetInARunCanBeRestored(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	f.addToPool("b1", "bob", time.Now())
	f.addToPool("b2", "bob", time.Now())
	bob := f.turn("bob")
	if msg, _ := bob.forgetFindingByReportID("orch-know-b1"); msg != "Forgot that finding." {
		t.Fatalf("forget: %q", msg)
	}
	if msg, _ := bob.memoryForget(map[string]any{"id": "b2"}); f.has("b2") {
		t.Fatalf("memory(forget): %q", msg)
	}
	got := f.removedFor(t, "bob")
	if len(got) != 2 {
		t.Fatalf("bob's forgets kept %d removals, want 2: %+v", len(got), got)
	}
	for _, e := range got {
		if e.RemovedBy != "bob" || e.What != "A finding: note" {
			t.Errorf("removal %+v", e)
		}
	}
}

// Nothing is kept for an agent that does not pool, and nothing past 30 days.
func TestRecentlyRemovedKeepsOnlyThePoolForThirtyDays(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOff)
	f.addToPool("o1", "", time.Now())
	if w := f.memCall(t, "alice", "DELETE", f.app.inferredDeleteRoute(ownAgent), "o1"); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if got := f.removedFor(t, "alice"); len(got) != 0 {
		t.Errorf("a removal on an agent that does not pool was kept: %+v", got)
	}

	f.agent.MemoryPool = memoryPoolOn
	udb := UserDB(f.app.DB, "alice")
	old := poolRemoval{ID: "old", AgentID: "a1", RemovedBy: "alice", At: time.Now().Add(-31 * 24 * time.Hour),
		What: "A finding", Chunks: []EmbeddedChunk{{ID: "x"}}}
	udb.Set(poolRemovedTable, "a1:old", old)
	if got := poolRemovalsFor(udb, f.agent, "alice", time.Now()); len(got) != 0 {
		t.Errorf("a removal past 30 days is still offered: %+v", got)
	}
	if len(udb.Keys(poolRemovedTable)) != 0 {
		t.Error("a removal past 30 days is still stored")
	}
}

// Deleting the agent takes what was removed from its pool with it.
func TestDeletingTheAgentDropsItsRemovals(t *testing.T) {
	f := newPoolFixture(t, memoryPoolOn)
	keepPoolRemoval(f.app.DB, f.agent, "alice", "A finding",
		[]EmbeddedChunk{{ID: "x", Source: knowledgeSource("alice", "a1", "general"), ReportID: "orch-know-x"}})
	udb := UserDB(f.app.DB, "alice")
	if len(udb.Keys(poolRemovedTable)) != 1 {
		t.Fatal("the removal was not kept")
	}
	dropAgentSideData(udb, "alice", "a1")
	if n := len(udb.Keys(poolRemovedTable)); n != 0 {
		t.Errorf("%d removal(s) outlived their agent", n)
	}
}
