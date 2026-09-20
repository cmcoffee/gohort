package orchestrate

// The credential fork is the reason the guided flow exists: four answers, and
// the difference between them is whose name a call goes out under. These pin
// that it is ASKED rather than assumed, and that each answer does what its
// label says.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func guidedFixture(t *testing.T) (Database, AgentRecord) {
	t.Helper()
	udb := reachFixture(t)
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})
	if err := Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredNone, Owner: "alice",
		AllowedURLPattern: "https://wiki.example/**"}, ""); err != nil {
		t.Skipf("no secure store here: %v", err)
	}
	// Cleared explicitly, and this is not fixture noise. The credential store
	// is a process singleton that these tests do not swap, and Save
	// deliberately PRESERVES the share lists — so a lend made by one test
	// would still be there for the next, which then sees no gap at all and
	// quietly asserts nothing.
	if err := Secure().SetCredentialShares("alice", "wiki", nil, nil); err != nil {
		t.Fatalf("clearing the lend: %v", err)
	}
	// The tool lives in the owner's pool and the agent names it, which is the
	// path a real agent takes: a record's inline Tools migrate into the store
	// on save, so a fixture that set them there would be testing a shape that
	// does not survive its first write.
	if err := AdminPersistTempTool(AuthDB(), "alice", TempTool{Name: "wiki_read", Credential: "wiki"}); err != nil {
		t.Skipf("no persistent tool store here: %v", err)
	}
	a := AgentRecord{ID: "a1", Name: "Troubleshooter", Owner: "alice", OrchestratorPrompt: "help",
		AllowedSkills: []string{"s1"},
		AllowedTools:  []string{"wiki_read"}}
	saveAgent(udb, a)
	return udb, a
}

// Only the gaps, and a question per credential. A plan that asked about
// everything would bury the two that matter in eight that do not.
func TestThePlanAsksAboutWhatTheyCannotReach(t *testing.T) {
	guidedFixture(t)
	plan := planAgentShare("alice", "a1", []string{"bob"})

	var keys []string
	for _, d := range plan {
		keys = append(keys, d.Key)
	}
	// The skill, the tool and the key underneath it.
	if len(plan) != 3 {
		t.Fatalf("decisions = %v", keys)
	}
	joined := strings.Join(keys, " ")
	if !strings.Contains(joined, "skill:s1") || !strings.Contains(joined, "cred:wiki") {
		t.Errorf("the plan does not ask about both gaps: %v", keys)
	}
	// The credential comes last: deciding about a key before deciding whether
	// the tool that spends it goes at all is the wrong order to be asked in.
	if !strings.HasPrefix(keys[len(keys)-1], "cred:") {
		t.Errorf("the credential is not the last question: %v", keys)
	}
	// Four answers, defaulting to the one that keeps each person's calls
	// going out as themselves.
	for _, d := range plan {
		if !strings.HasPrefix(d.Key, "cred:") {
			continue
		}
		if len(d.Options) != 4 {
			t.Errorf("the credential fork has %d options", len(d.Options))
		}
		if d.Default != credOwn {
			t.Errorf("the credential defaults to %q rather than their own key", d.Default)
		}
		for _, o := range d.Options {
			if o.Help == "" {
				t.Errorf("option %q has no consequence written under it", o.Value)
			}
		}
	}
}

// "They bring their own" must actually leave the key alone. It is the default,
// so a bug here would quietly lend every credential in the deployment.
func TestBringingTheirOwnLendsNothing(t *testing.T) {
	udb, _ := guidedFixture(t)
	lines := shareAgentGuided("alice", "a1", []string{"bob"},
		map[string]string{"skill:s1": shareSend, "cred:wiki": credOwn})

	if got := Secure().SharedWithUser("bob"); len(got) != 0 {
		t.Errorf("the key was lent anyway: %+v", got)
	}
	// And it is SAID, because doing nothing looks identical to nothing
	// happening unless the report says which it was.
	if !strings.Contains(strings.Join(lines, " | "), "supply their own") {
		t.Errorf("the report does not say the recipient needs their own: %v", lines)
	}
	// The agent and the skill did go.
	a, _ := loadAgent(udb, "a1")
	if !namedIn(a.AllowedUsers, "bob") {
		t.Error("the agent was not shared")
	}
	if got := AvailableSkills(udb, "bob"); len(got) != 1 {
		t.Errorf("the skill did not go: %+v", got)
	}
}

// A write lend is the one with a consequence nobody can undo afterwards, so
// the report has to say what it did in those terms.
func TestAWriteLendSaysWhoseNameTheWritesCarry(t *testing.T) {
	guidedFixture(t)
	lines := shareAgentGuided("alice", "a1", []string{"bob"},
		map[string]string{"skill:s1": shareSkip, "cred:wiki": credWrite})

	c, ok := Secure().LoadUser("alice", "wiki")
	if !ok || !namedIn(c.SharedReadWrite, "bob") {
		t.Fatalf("the write lend did not happen: %+v", c.SharedReadWrite)
	}
	joined := strings.Join(lines, " | ")
	if !strings.Contains(joined, "arrive as you") {
		t.Errorf("the report does not say whose name the writes carry: %v", lines)
	}
	// And the skill this run was told to skip stayed put.
	if namedIn(skillRecipients(t, "s1"), "bob") {
		t.Error("a dependency marked skip was shared anyway")
	}
	if !strings.Contains(joined, "left out") {
		t.Errorf("the report does not say what it left out: %v", lines)
	}
}

// Read-only is a narrower lend, not a different one: the key travels but the
// recipient is held to GET and HEAD.
func TestAReadLendIsRecordedAsReadOnly(t *testing.T) {
	guidedFixture(t)
	shareAgentGuided("alice", "a1", []string{"bob"}, map[string]string{"cred:wiki": credRead})
	c, _ := Secure().LoadUser("alice", "wiki")
	if !namedIn(c.SharedReadOnly, "bob") || namedIn(c.SharedReadWrite, "bob") {
		t.Errorf("read-only landed wrong: read=%v write=%v", c.SharedReadOnly, c.SharedReadWrite)
	}
}

// Leaving a credential out turns it off for the agent — for everybody,
// including the owner, which the option says in as many words.
func TestLeavingACredentialOutTurnsItOff(t *testing.T) {
	udb, _ := guidedFixture(t)
	shareAgentGuided("alice", "a1", []string{"bob"}, map[string]string{"cred:wiki": shareSkip})
	a, _ := loadAgent(udb, "a1")
	if !namedIn(a.DisabledCredentials, "wiki") {
		t.Errorf("the credential is still live for the agent: %+v", a.DisabledCredentials)
	}
	if got := Secure().SharedWithUser("bob"); len(got) != 0 {
		t.Errorf("it was lent as well as turned off: %+v", got)
	}
}

func skillRecipients(t *testing.T, id string) []string {
	t.Helper()
	for _, s := range LoadSkills(RootDB, "alice") {
		if s.ID == id {
			return s.AllowedUsers
		}
	}
	return nil
}

// The other half of a share, and the half that was missing: the person on the
// receiving end being told what arrived and what it still needs from them.

func TestTheRecipientIsToldWhatTheyMustSupply(t *testing.T) {
	guidedFixture(t)
	// They bring their own key, which is the default and the case where the
	// recipient has something to do.
	shareAgentGuided("alice", "a1", []string{"bob"},
		map[string]string{"skill:s1": shareSend, "tool:wiki_read": shareSend, "cred:wiki": credOwn})

	need := manifestForAgent("alice", "a1", "bob")
	joined := strings.Join(need, " | ")
	if !strings.Contains(joined, "wiki") {
		t.Fatalf("the manifest does not name the credential they need: %v", need)
	}
	// By NAME and with the place to add it: "you need a credential" is a
	// support ticket, "add one called wiki in Extensions" is something to do.
	if !strings.Contains(joined, "Extensions") {
		t.Errorf("the manifest does not say where to fix it: %v", need)
	}
}

// Nothing to do means nothing said. A manifest that always found something
// would train people to ignore it.
func TestACompleteShareAsksNothingOfTheRecipient(t *testing.T) {
	udb, _ := guidedFixture(t)
	// Lend the key and share everything else: bob is left with nothing to do.
	shareAgentGuided("alice", "a1", []string{"bob"},
		map[string]string{"skill:s1": shareSend, "tool:wiki_read": shareSend, "cred:wiki": credRead})
	// Taking the tool is the recipient's own step, so stand in for it.
	SetGlobalToolAdopted(AuthDB(), "bob", "wiki_read", true)

	if need := manifestForAgent("alice", "a1", "bob"); len(need) != 0 {
		t.Errorf("a complete share still asks something of them: %v", need)
	}
	_ = udb
}

// The manifest is per PERSON. One colleague having a key of that name says
// nothing about another, and a line written once at share time would be wrong
// for one of them.
func TestTheManifestIsPerRecipient(t *testing.T) {
	guidedFixture(t)
	shareAgentGuided("alice", "a1", []string{"bob", "carol"},
		map[string]string{"cred:wiki": credRead, "skill:s1": shareSend, "tool:wiki_read": shareSend})
	SetGlobalToolAdopted(AuthDB(), "bob", "wiki_read", true)

	if need := manifestForAgent("alice", "a1", "bob"); len(need) != 0 {
		t.Errorf("bob took the tool and is still being asked: %v", need)
	}
	if need := manifestForAgent("alice", "a1", "carol"); len(need) == 0 {
		t.Error("carol has not taken the tool and is being told nothing")
	}
}
