package orchestrate

// The walk has to be right before anything acts on it. A preview that
// misdescribes one edge is worse than no preview, because somebody shares on
// the strength of it and finds out later.

import (
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

// The case the panel exists for: shared with somebody who does not have what
// it needs. And the answer names THEM, because "warning" is not something to
// act on and "bob does not have this" is.
func TestASharedAgentReportsWhatItsRecipientLacks(t *testing.T) {
	udb := reachFixture(t)
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Private Skill", Instructions: "."})
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s2", Name: "Given Skill", Instructions: ".",
		AllowedUsers: []string{"bob"}})

	got := agentReachOf(udb, "alice", AgentRecord{ID: "a1", Owner: "alice",
		AllowedUsers:  []string{"bob"},
		AllowedSkills: []string{"s1", "s2"}})

	priv, ok := itemFor(got, "Private Skill")
	if !ok || !priv.Gap {
		t.Fatalf("a private skill on a shared agent is not flagged: %+v", got.Items)
	}
	if priv.Missing != "bob does not have this" {
		t.Errorf("the gap does not name who lacks it: %q", priv.Missing)
	}
	given, ok := itemFor(got, "Given Skill")
	if !ok || given.Gap {
		t.Errorf("a skill shared with the same person is flagged anyway: %+v", given)
	}
	if got.Gaps != 1 {
		t.Errorf("gaps = %d, want 1", got.Gaps)
	}
}

// A published agent is measured against everybody, so a skill shared with one
// person is still short.
func TestAPublishedAgentNeedsDeploymentWideDependencies(t *testing.T) {
	udb := reachFixture(t)
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Given Skill", Instructions: ".",
		AllowedUsers: []string{"bob"}})

	got := agentReachOf(udb, "alice", AgentRecord{ID: "a1", Owner: "alice", Exposed: true,
		AllowedSkills: []string{"s1"}})
	it, ok := itemFor(got, "Given Skill")
	if !ok || !it.Gap {
		t.Fatalf("a peer-shared skill on a published agent is not flagged: %+v", got.Items)
	}
	if it.Missing != "Not everybody has this" {
		t.Errorf("missing = %q", it.Missing)
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
func TestAPersonalCredentialIsNotReportedAsMissing(t *testing.T) {
	udb := reachFixture(t)
	got := agentReachOf(udb, "alice", AgentRecord{ID: "a1", Owner: "alice",
		AllowedUsers: []string{"bob"},
		Tools:        []TempTool{{Name: "wiki_read", Credential: "wiki"}}})

	it, ok := itemFor(got, "wiki")
	if !ok {
		t.Fatal("the credential behind a bundled tool is not listed")
	}
	if it.Kind != "Credential" {
		t.Errorf("kind = %q", it.Kind)
	}
	// Nothing of alice's answers to "wiki" here, so the runner resolves it
	// exactly as she does — which is not something she can fix by sharing.
	if it.Gap {
		t.Errorf("a credential nobody owns is reported as a gap: %+v", it)
	}
}
