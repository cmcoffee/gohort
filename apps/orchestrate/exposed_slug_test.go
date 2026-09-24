package orchestrate

// Two people can each own an agent called "Helper", and both can be reachable
// on /agents/. The slug is derived from the name, so before this the pair
// shared one URL: the lookup took the first match in user-listing order and
// then 404'd whoever the OTHER one was shared with, and the directory de-duped
// by walking a Go map, so which card survived changed between refreshes.

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Agent ids for the fixture. The two reviewer ids share their first six
// characters on purpose, so the suffix has to grow to tell them apart.
const (
	slugHelperA   = "aaaa1111-0000-4000-8000-000000000001"
	slugHelperB   = "bbbb2222-0000-4000-8000-000000000002"
	slugReviewerA = "cccc3333-0000-4000-8000-000000000003"
	slugReviewerB = "cccc3399-0000-4000-8000-000000000004"
)

// slugClashFixture: alice and bob are owners, carol and dave are the people
// they share with, root is an admin.
//
//   - "Helper": alice's is shared with carol, bob's with dave. Neither is
//     published, so nobody's app grant is involved.
//   - "Reviewer": alice's is published and carol holds the grant for its
//     plain path; bob's is newer and peer-shared with carol and dave.
func slugClashFixture(t *testing.T) *OrchestrateApp {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	adb.Set(AuthTable, "user:carol", AuthUser{Username: "carol", Apps: []string{"/agents/reviewer"}})
	adb.Set(AuthTable, "user:dave", AuthUser{Username: "dave"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	T := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}

	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(24 * time.Hour)
	put := func(owner string, a AgentRecord) {
		a.Owner = owner
		a.OrchestratorPrompt = "help"
		UserDB(T.DB, owner).Set(agentsTable, a.ID, a)
	}
	put("alice", AgentRecord{ID: slugHelperA, Name: "Helper", AllowedUsers: []string{"carol"}, Created: older})
	put("bob", AgentRecord{ID: slugHelperB, Name: "Helper", AllowedUsers: []string{"dave"}, Created: newer})
	put("alice", AgentRecord{ID: slugReviewerA, Name: "Reviewer", Everyone: true, ShowOnDashboard: true, Created: newer})
	put("bob", AgentRecord{ID: slugReviewerB, Name: "Reviewer", AllowedUsers: []string{"carol", "dave"}, Created: older})
	return T
}

func slugViewer(t *testing.T, who string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/agents/", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), who)})
	return r
}

// Every agent in the pool gets a URL nobody else has, and the answer is the
// same on every call.
func TestExposedSlugsAreUniqueAndStable(t *testing.T) {
	T := slugClashFixture(t)
	first := T.ListExposedAgents()
	if len(first) != 4 {
		t.Fatalf("the directory dropped a clashing agent: got %d rows, want 4: %+v", len(first), first)
	}
	seen := map[string]string{}
	for _, e := range first {
		if prior, dup := seen[e.Slug]; dup {
			t.Errorf("agents %s and %s share the slug %q", prior, e.AgentID, e.Slug)
		}
		seen[e.Slug] = e.AgentID
	}
	for i := 0; i < 50; i++ {
		if got := T.ListExposedAgents(); !reflect.DeepEqual(got, first) {
			t.Fatalf("the directory changed between calls:\n first %+v\n now   %+v", first, got)
		}
	}
}

// Who keeps the plain slug, and what the others become.
func TestClashingAgentsKeepAPlainSlugOrGetAnIDSuffix(t *testing.T) {
	T := slugClashFixture(t)
	slugOf := map[string]string{}
	for _, e := range T.ListExposedAgents() {
		slugOf[e.AgentID] = e.Slug
	}
	// Helper: neither published, so the older agent keeps it.
	if slugOf[slugHelperA] != "helper" || slugOf[slugHelperB] != "helper-bbbb22" {
		t.Errorf("helper slugs = %q, %q", slugOf[slugHelperA], slugOf[slugHelperB])
	}
	// Reviewer: the PUBLISHED one keeps the plain path even though it is the
	// newer agent, because an admin's grant names that path and moving it
	// would revoke the grant without anyone deciding to.
	if slugOf[slugReviewerA] != "reviewer" {
		t.Errorf("the published reviewer lost its plain slug: %q", slugOf[slugReviewerA])
	}
	// The ids agree for six characters, so six would not tell them apart.
	if got := slugOf[slugReviewerB]; got != "reviewer-cccc339" {
		t.Errorf("the suffix did not grow past a shared prefix: %q", got)
	}
}

// The case that 404'd: dave was shared bob's Helper, and "helper" resolved to
// alice's, which dave cannot reach.
func TestASlugResolvesAmongWhatTheViewerCanReach(t *testing.T) {
	T := slugClashFixture(t)
	cases := []struct {
		who, slug, want string
	}{
		{"carol", "helper", slugHelperA},
		// The plain slug still opens bob's for dave: it is the only Helper he
		// can reach, and it is the link he had before the clash existed.
		{"dave", "helper", slugHelperB},
		{"dave", "helper-bbbb22", slugHelperB},
		{"carol", "reviewer", slugReviewerA},
		{"carol", "reviewer-cccc339", slugReviewerB},
		{"dave", "reviewer-cccc339", slugReviewerB},
		// A longer fragment than the pool needs today still opens it, so a
		// link saved while a bigger clash had grown the suffix keeps working.
		{"dave", "reviewer-cccc3399", slugReviewerB},
		// dave has no grant for alice's published reviewer, so the only
		// reviewer he reaches is bob's, whichever form of the link he has.
		{"dave", "reviewer", slugReviewerB},
		{"alice", "helper", slugHelperA},
		{"bob", "helper", slugHelperB},
	}
	for _, c := range cases {
		a, e, ok := T.LookupExposedAgent(slugViewer(t, c.who), c.slug)
		if !ok {
			t.Errorf("%s: /agents/%s did not resolve", c.who, c.slug)
			continue
		}
		if a.ID != c.want || e.AgentID != c.want {
			t.Errorf("%s: /agents/%s opened %s, want %s", c.who, c.slug, a.ID, c.want)
		}
	}
}

// Nobody resolves an agent they cannot reach, whatever form the slug takes.
func TestASlugNeverOpensAnUnreachableAgent(t *testing.T) {
	T := slugClashFixture(t)
	// An admin governs published agents, not two colleagues' peer shares, so
	// neither Helper is theirs to open by any form of its link.
	for _, slug := range []string{"helper", "helper-bbbb22", "helper-aaaa11"} {
		if a, _, ok := T.LookupExposedAgent(slugViewer(t, "root"), slug); ok {
			t.Errorf("root opened %s through /agents/%s", a.ID, slug)
		}
	}
	// carol reaches only alice's Helper, so bob's suffixed link is a miss for
	// her, not a fallback to the one she can reach.
	if a, _, ok := T.LookupExposedAgent(slugViewer(t, "carol"), "helper-bbbb22"); ok {
		t.Errorf("carol opened %s through a link to an agent she cannot reach", a.ID)
	}
	if _, _, ok := T.LookupExposedAgent(slugViewer(t, "dave"), "helper-aaaa11"); ok {
		t.Error("dave opened alice's Helper through its suffixed link")
	}
}

// A plain slug that two reachable agents answer to, neither of them its
// keeper, opens neither: a guess would be exactly the bug, pointed the other
// way. The suffixed links still open each one.
func TestAnAmbiguousPlainSlugOpensNothing(t *testing.T) {
	T := slugClashFixture(t)
	// A third Helper, dave's, shared with carol; and bob's shared with her
	// too. alice's still keeps the plain slug (oldest) but carol cannot reach
	// it once alice narrows it to bob.
	const helperC = "dddd4444-0000-4000-8000-000000000005"
	UserDB(T.DB, "dave").Set(agentsTable, helperC, AgentRecord{
		ID: helperC, Owner: "dave", Name: "Helper", OrchestratorPrompt: "help",
		AllowedUsers: []string{"carol"}, Created: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	})
	for owner, id := range map[string]string{"alice": slugHelperA, "bob": slugHelperB} {
		var a AgentRecord
		udb := UserDB(T.DB, owner)
		udb.Get(agentsTable, id, &a)
		if owner == "alice" {
			a.AllowedUsers = []string{"bob"}
		} else {
			a.AllowedUsers = []string{"dave", "carol"}
		}
		udb.Set(agentsTable, id, a)
	}
	carol := slugViewer(t, "carol")
	if a, _, ok := T.LookupExposedAgent(carol, "helper"); ok {
		t.Errorf("an ambiguous plain slug opened %s for carol", a.ID)
	}
	if a, _, ok := T.LookupExposedAgent(carol, "helper-bbbb22"); !ok || a.ID != slugHelperB {
		t.Errorf("bob's suffixed link did not open his Helper: ok=%v id=%s", ok, a.ID)
	}
	if a, _, ok := T.LookupExposedAgent(carol, "helper-dddd44"); !ok || a.ID != helperC {
		t.Errorf("dave's suffixed link did not open his Helper: ok=%v id=%s", ok, a.ID)
	}
	// bob owns a Helper AND is on alice's list. The plain slug is exact for
	// alice's, the keeper, so it opens hers; his own is at its suffixed link.
	if a, _, ok := T.LookupExposedAgent(slugViewer(t, "bob"), "helper"); !ok || a.ID != slugHelperA {
		t.Errorf("bob's plain helper link opened ok=%v id=%s, want alice's keeper", ok, a.ID)
	}
}

// Each viewer's dashboard card opens the agent it is a card for. This is the
// sentence the fix exists for, checked end to end.
func TestEveryCardOpensTheAgentItShows(t *testing.T) {
	T := slugClashFixture(t)
	for _, who := range []string{"alice", "bob", "carol", "dave", "root"} {
		r := slugViewer(t, who)
		cards := T.DashboardCards(r)
		if who == "dave" && len(cards) != 2 {
			t.Errorf("dave should see two cards (bob's Helper and Reviewer), got %d: %+v", len(cards), cards)
		}
		if who == "carol" {
			names := map[string]bool{}
			for _, c := range cards {
				names[c.Name] = true
			}
			// Two Reviewers on one page: the owner is what tells them apart.
			if !names["Reviewer (alice)"] || !names["Reviewer (bob)"] || !names["Helper"] {
				t.Errorf("carol's cards do not tell the two Reviewers apart: %+v", cards)
			}
		}
		byPath := map[string]bool{}
		for _, c := range cards {
			if !strings.HasPrefix(c.Path, "/agents/") {
				continue
			}
			if byPath[c.Path] {
				t.Errorf("%s: two cards share %s", who, c.Path)
			}
			byPath[c.Path] = true
			slug := strings.TrimPrefix(c.Path, "/agents/")
			a, e, ok := T.LookupExposedAgent(r, slug)
			if !ok {
				t.Errorf("%s: the card %s 404s", who, c.Path)
				continue
			}
			if e.Slug != slug {
				t.Errorf("%s: the card %s opened %s (%s)", who, c.Path, e.Slug, a.ID)
			}
		}
	}
}

// memoryAgent gates a visitor with the agent's own slug. A suffixed published
// agent must not ride on the grant for the plain path someone else holds.
func TestMemoryGateUsesTheCanonicalSlug(t *testing.T) {
	T := slugClashFixture(t)
	// Publish bob's Reviewer too. carol's grant names "/agents/reviewer",
	// which is alice's; bob's is now "reviewer-cccc339" and published, so it
	// needs its own grant and carol is not on it.
	var b AgentRecord
	udb := UserDB(T.DB, "bob")
	udb.Get(agentsTable, slugReviewerB, &b)
	b.Everyone = true
	// Newer than alice's, so alice's keeps the plain path. Between two
	// published agents the older one does, since nothing records which was
	// published first.
	b.Created = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	udb.Set(agentsTable, b.ID, b)
	r := slugViewer(t, "carol")
	if _, ok := T.memoryAgent(r, UserDB(T.DB, "carol"), "carol", slugReviewerB); ok {
		t.Error("carol reached a published agent through a grant for another agent's path")
	}
	if _, ok := T.memoryAgent(r, UserDB(T.DB, "carol"), "carol", slugReviewerA); !ok {
		t.Error("carol lost the published agent she was granted")
	}
}

// Approving a publish refuses a name another published agent already uses,
// so a clash between two agents that reach everyone is a decision somebody
// made rather than something the suffix papers over.
func TestApprovingAPublishRefusesANameClash(t *testing.T) {
	T := slugClashFixture(t)
	err := T.approveAgentPublish("bob", agentPromotionName(slugReviewerB, "exposed"))
	if err == nil || !strings.Contains(err.Error(), "/agents/reviewer") {
		t.Fatalf("a clashing publish was approved: %v", err)
	}
	if got, _ := loadAgent(UserDB(T.DB, "bob"), slugReviewerB); got.Everyone {
		t.Error("the refused publish was applied anyway")
	}
	// A clash with an agent that is only peer-shared does not stand in the
	// way: its reach is two named people, and the suffix keeps them apart.
	if err := T.approveAgentPublish("bob", agentPromotionName(slugHelperB, "exposed")); err != nil {
		t.Errorf("a publish was refused over an unpublished namesake: %v", err)
	}
	// A distinct public name is the way out.
	var b AgentRecord
	udb := UserDB(T.DB, "bob")
	udb.Get(agentsTable, slugReviewerB, &b)
	b.PublicName = "Second Opinion"
	udb.Set(agentsTable, b.ID, b)
	if err := T.approveAgentPublish("bob", agentPromotionName(slugReviewerB, "exposed")); err != nil {
		t.Errorf("a renamed publish was still refused: %v", err)
	}
}
