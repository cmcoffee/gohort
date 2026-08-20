// Two questions a machine's tool list cannot answer on its own: is the coarse
// control what this step actually meant, and can the agent it was just given to
// reach what it names.
package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func preflightFixture(t *testing.T) (Database, string) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:u", AuthUser{Username: "u"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	return udb, "u"
}

// Attaching is the moment the question becomes answerable AND the moment
// somebody is looking. Before this the first report was a turnDiag on the
// first message, hours later, phrased as a tool that had gone missing.
func TestAttachingSaysWhatTheAgentCannotReach(t *testing.T) {
	udb, user := preflightFixture(t)
	def := MachineDef{ID: "m1", Name: "diag", Owner: user, Phases: []MachinePhase{
		{Name: "scan", Prompt: "look", Tools: []string{"search_support_bundles"}},
		{Name: "answer", Prompt: "reply", Resident: true},
	}}
	bare := AgentRecord{ID: "a1", Name: "Wren", Owner: user}

	gaps := machineAttachGaps(udb, user, def, bare)
	if len(gaps) != 1 || !strings.Contains(gaps[0], "search_support_bundles") {
		t.Fatalf("an agent without the store should be told before its first turn: %v", gaps)
	}
	if !strings.Contains(gaps[0], "Wren") || !strings.Contains(gaps[0], "step scan") {
		t.Errorf("the warning must name the step AND the agent: %q", gaps[0])
	}

	// The same machine on an agent that IS attached to the store is fine, and
	// must say nothing: a warning that fires on a working configuration is one
	// people learn to scroll past, and it takes the real ones with it.
	withStore := bare
	withStore.AttachedSources = []ReferenceSelection{{Kind: "testfiles", ItemID: "support_bundles"}}
	withBundleSource(t)
	if gaps := machineAttachGaps(udb, user, def, withStore); len(gaps) != 0 {
		t.Errorf("the agent holds what the step names; nothing should be reported: %v", gaps)
	}

	// A machine naming nothing cannot be missing anything, and does not pay
	// for a catalog walk to find that out.
	quiet := MachineDef{ID: "m2", Name: "chat", Owner: user,
		Phases: []MachinePhase{{Name: "answer", Prompt: "reply", Resident: true}}}
	if gaps := machineAttachGapsForAll(udb, user, quiet); gaps != nil {
		t.Errorf("a machine with no tool lists has no preflight to run: %v", gaps)
	}
}

// The nudge is not a rewrite, and must not fire as though it were. A reach and
// a list are different statements — "read" grants every read tool the agent
// has, so a step naming three on purpose is narrower by design.
func TestTheReachNudgeOnlyFiresWhereTheListIsPayingForNothing(t *testing.T) {
	udb, user := preflightFixture(t)
	withBundleSource(t)

	// Names minted by an attachment: exactly the ones that stop resolving
	// when the machine moves, with nobody having edited it.
	fragile := MachineDef{ID: "m1", Name: "diag", Owner: user, Phases: []MachinePhase{
		{Name: "scan", Prompt: "look", Tools: []string{"search_support_bundles"}},
	}}
	got := strings.Join(reachAdvice(udb, user, fragile), "\n")
	if !strings.Contains(got, "step scan") || !strings.Contains(got, "reach") {
		t.Errorf("a list of attachment-minted names should be nudged toward a reach: %q", got)
	}

	// A step that already declares a reach has made the choice; saying it
	// again is noise.
	settled := fragile
	settled.Phases[0].Reach = ReachRead
	if got := reachAdvice(udb, user, settled); len(got) != 0 {
		t.Errorf("a step with a reach set needs no advice about reaches: %v", got)
	}

	// And a step naming nothing has nothing to be advised about.
	none := MachineDef{ID: "m2", Name: "chat", Owner: user,
		Phases: []MachinePhase{{Name: "answer", Prompt: "reply", Resident: true}}}
	if got := reachAdvice(udb, user, none); got != nil {
		t.Errorf("no list, no advice: %v", got)
	}
}

// An unannotated tool is NOT read-only for the nudge's purposes. The advice
// asks somebody to trust a word; guessing "probably fine" about a tool nobody
// classified is how a step that posts ends up inside "may look, not act".
func TestUnclassifiedToolsAreNotTreatedAsReadOnly(t *testing.T) {
	if capsAreReadOnly(nil) {
		t.Error("a tool declaring no capabilities must not pass as read-only")
	}
	if !capsAreReadOnly([]Capability{CapRead}) {
		t.Error("a read tool is read-only")
	}
	if capsAreReadOnly([]Capability{CapRead, CapNetwork}) {
		t.Error("reaching the network is not only reading")
	}
}
