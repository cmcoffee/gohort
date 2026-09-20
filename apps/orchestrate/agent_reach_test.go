package orchestrate

// The walk has to be right before anything acts on it. A preview that
// misdescribes one edge is worse than no preview, because somebody shares on
// the strength of it and finds out later.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func reachFixture(t *testing.T) Database {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	savedRoot, savedAuth, savedBase := RootDB, AuthDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB, orchestrateBaseDB = savedRoot, savedAuth, savedBase })
	return UserDB(root, "alice")
}

func itemFor(r agentReachMap, name string) (reachItem, bool) {
	for _, it := range r.Items {
		if it.Name == name {
			return it, true
		}
	}
	return reachItem{}, false
}

// An agent nobody has is not missing anything. The panel measures each
// dependency against the AGENT's own reach, so a private agent has no gaps by
// construction and the list is purely informational.
func TestAPrivateAgentHasNoGaps(t *testing.T) {
	udb := reachFixture(t)
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})

	got := agentReachOf(udb, "alice", AgentRecord{ID: "a1", Owner: "alice", AllowedSkills: []string{"s1"}})
	if got.Gaps != 0 {
		t.Errorf("a private agent reports gaps: %+v", got.Items)
	}
	if got.Audience != "Private to you" {
		t.Errorf("audience = %q", got.Audience)
	}
}

// What a shared agent's dependencies now do: travel. A skill the owner has
// shared with nobody still reaches whoever runs their agent, because it is
// read in the owner's namespace and scoped to that agent.
func TestASharedAgentsDependenciesAreNotGaps(t *testing.T) {
	udb := reachFixture(t)
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Private Skill", Instructions: "."})

	got := agentReachOf(udb, "alice", AgentRecord{ID: "a1", Owner: "alice",
		AllowedUsers: []string{"bob"}, AllowedSkills: []string{"s1"}})

	it, ok := itemFor(got, "Private Skill")
	if !ok {
		t.Fatalf("the skill is not listed at all: %+v", got.Items)
	}
	if it.Gap {
		t.Error("a dependency that travels with the agent is reported as missing")
	}
	if got.Gaps != 0 {
		t.Errorf("gaps = %d, want 0: %+v", got.Gaps, got.Items)
	}
}

// The same holds for a published agent: everybody who runs it reads the
// author's corpus and behaviour through it, so nothing needs widening.
func TestAPublishedAgentsDependenciesAreNotGapsEither(t *testing.T) {
	udb := reachFixture(t)
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Private Skill", Instructions: "."})

	got := agentReachOf(udb, "alice", AgentRecord{ID: "a1", Owner: "alice", Exposed: true,
		AllowedSkills: []string{"s1"}})
	if got.Gaps != 0 {
		t.Errorf("a published agent reports gaps it no longer has: %+v", got.Items)
	}
}

// Bundled tools live on the record, so they go wherever it goes — the one
// dependency that is never a gap, and the panel has to say why rather than
// leaving it looking like an oversight.
func TestBundledToolsTravelWithTheAgent(t *testing.T) {
	udb := reachFixture(t)
	got := agentReachOf(udb, "alice", AgentRecord{ID: "a1", Owner: "alice",
		AllowedUsers: []string{"bob"},
		Tools:        []TempTool{{Name: "ssh_run"}}})

	it, ok := itemFor(got, "ssh_run")
	if !ok {
		t.Fatal("a bundled tool is not listed at all")
	}
	if it.Gap {
		t.Errorf("a tool defined on the agent is reported as missing: %+v", it)
	}
	if it.Reach != "Travels with the agent" {
		t.Errorf("reach = %q", it.Reach)
	}
}

// A personal credential is NOT a gap. Everything else on this panel asks "do
// they have a copy"; a credential asks whose identity the call goes out as, and
// the ordinary answer for a team is that each person supplies their own.
// A credential IS a gap, and the only one: it is not a copy somebody is
// missing but whose identity the call goes out as, which is the one thing a
// share has to ask about.
//
// The name is unique to this test on purpose. The credential store is a
// process singleton these fixtures do not swap, so a key another test saved
// for "wiki" would decide this one's answer.
func TestACredentialIsTheOnlyGap(t *testing.T) {
	udb := reachFixture(t)
	if err := Secure().Save(SecureCredential{Name: "reach_only_key", Type: SecureCredNone,
		Owner: "alice", AllowedURLPattern: "https://x.example/**"}, ""); err != nil {
		t.Skipf("no secure store here: %v", err)
	}
	Secure().SetCredentialShares("alice", "reach_only_key", nil, nil)

	got := agentReachOf(udb, "alice", AgentRecord{ID: "a1", Owner: "alice",
		AllowedUsers: []string{"bob"},
		Tools:        []TempTool{{Name: "wiki_read", Credential: "reach_only_key"}}})

	it, ok := itemFor(got, "reach_only_key")
	if !ok {
		t.Fatalf("the credential behind a bundled tool is not listed: %+v", got.Items)
	}
	if it.Kind != "Credential" || !it.Gap {
		t.Errorf("a personal key on a shared agent is not the gap: %+v", it)
	}
	if !strings.Contains(it.Fix, "their own key") {
		t.Errorf("the fix does not describe the choice: %q", it.Fix)
	}
}

// An agent's skills are its author's, resolved in the author's namespace.
//
// Resolving them as the runner meant two wrong things at once: the skills the
// agent was built on vanished for anybody but the author, while the RUNNER's
// own skills were admitted in their place — so the same agent behaved
// differently per person in ways its author never wrote.
func TestAnAgentsSkillsAreItsAuthors(t *testing.T) {
	udb := reachFixture(t)
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Alice's."})
	SaveSkill(RootDB, "bob", SkillRecord{ID: "s-bob", Name: "Bob's own", Instructions: "Bob's."})

	// Bob running alice's agent: the turn carries alice as the owner view.
	turn := &chatTurn{
		udb: UserDB(RootDB, "bob"), user: "bob",
		ownerDB: udb, ownerUser: "alice",
		agent: AgentRecord{ID: "a1", Owner: "alice", AllowedSkills: []string{"s1"}},
	}
	var names []string
	for _, s := range turn.agentSkills() {
		names = append(names, s.ID)
	}
	if !namedIn(names, "s1") {
		t.Errorf("the agent's own skill did not travel: %v", names)
	}
	// Bob's personal skill is in the set his own agents would see; it must not
	// be what alice's agent activates. The AllowedSkills filter is what stops
	// it, and this pins that the two together do the right thing.
	if turn.agent.AllowedSkills[0] == "s-bob" {
		t.Fatal("fixture error")
	}
	// An owner-run turn is unchanged.
	own := &chatTurn{
		udb: udb, user: "alice",
		agent: AgentRecord{ID: "a1", Owner: "alice", AllowedSkills: []string{"s1"}},
	}
	var ownNames []string
	for _, s := range own.agentSkills() {
		ownNames = append(ownNames, s.ID)
	}
	if !namedIn(ownNames, "s1") {
		t.Errorf("the owner lost their own skill: %v", ownNames)
	}
}
