package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// bundleSource is a reference source shaped like the one that surfaced this:
// a file store whose whole contribution is a set of NAMED tools minted per
// attachment (search_<store>), registered in no global tool registry.
type bundleSource struct{}

func (bundleSource) Kind() string  { return "testfiles" }
func (bundleSource) Label() string { return "Test file stores" }

func (bundleSource) List(user string) []ReferenceItem {
	return []ReferenceItem{{ID: "support_bundles", Name: "Support bundles", Desc: "diagnostic dumps"}}
}

func (bundleSource) Fetch(ctx context.Context, user, itemID, query string) string { return "" }

func (bundleSource) ItemTools(user, itemID string) []AgentToolDef {
	if itemID != "support_bundles" {
		return nil
	}
	return []AgentToolDef{
		{Tool: Tool{Name: "search_support_bundles", Description: "Search the bundles.", Caps: []Capability{CapRead}},
			Handler: func(map[string]any) (string, error) { return "", nil }},
		{Tool: Tool{Name: "read_support_bundles", Description: "Read a window of one file.", Caps: []Capability{CapRead}},
			Handler: func(map[string]any) (string, error) { return "", nil }},
	}
}

func withBundleSource(t *testing.T) {
	t.Helper()
	RegisterReferenceSource(bundleSource{})
}

// The live failure: an agent attached to a file store, running a machine whose
// step names that store's tools, reached NONE of them — resolveWorkerTools
// assembles the registered pool, and an attachment's tools are appended by the
// turn's own catalog build and by nothing else. The step then answered that the
// logs "were not provided in the input", which was true and unexplained.
func TestAStepReachesTheAgentsAttachedSources(t *testing.T) {
	withBundleSource(t)
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.agent.AttachedSources = []ReferenceSelection{{Kind: "testfiles", ItemID: "support_bundles"}}

	pool := turn.machineCatalog(MachinePhase{Name: "scan", Tools: []string{"search_support_bundles"}})
	var names []string
	for _, td := range pool {
		names = append(names, td.Tool.Name)
	}
	if !strings.Contains(strings.Join(names, " "), "search_support_bundles") {
		t.Fatalf("a step naming an attached source's tool must be able to reach it; pool held %d tools", len(pool))
	}

	// And the narrowing the runner applies to that pool keeps it.
	narrowed := PhaseTools(MachinePhase{Tools: []string{"search_support_bundles"}}, pool)
	if len(narrowed) != 1 || narrowed[0].Tool.Name != "search_support_bundles" {
		t.Errorf("the step should reach exactly what it named, got %+v", narrowed)
	}
}

// A name the pool does not carry is subtracted in silence on the transient
// path — there is no narrowCatalog here to report it. Say so, or the step
// looks like a tool that stopped working.
func TestAStepSaysWhichNamesItCouldNotReach(t *testing.T) {
	withBundleSource(t)
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.agent.AttachedSources = []ReferenceSelection{{Kind: "testfiles", ItemID: "support_bundles"}}

	turn.machineCatalog(MachinePhase{Name: "scan",
		Tools: []string{"search_support_bundles", "search_support_bundle" /* typo'd */}})

	var list []SessionDiag
	turn.udb.Get(sessionDiagTable, "a1:"+turn.session.ID, &list)
	var found string
	for _, d := range list {
		if d.Kind == "machine_step_tools_missing" {
			found = d.Detail
		}
	}
	if found == "" {
		t.Fatalf("the step should leave a breadcrumb naming what it could not reach; diags: %+v", list)
	}
	if !strings.Contains(found, "search_support_bundle,") && !strings.Contains(found, "search_support_bundle ") {
		t.Errorf("the breadcrumb must name the missed tool: %q", found)
	}
	if strings.Contains(found, "NONE") {
		t.Errorf("one name missing is not a total miss: %q", found)
	}
}

// The other half of the same gap: the phase editor's checklist offers the pool
// a step may narrow to, and an attached source's tool names were in no picker
// anywhere. An author ticking that list stripped every attached source from the
// turn and had no box to tick to put it back.
func TestThePhaseToolPickerOffersAttachedSourceTools(t *testing.T) {
	withBundleSource(t)
	var offered []string
	for _, o := range phaseToolOptions("u") {
		offered = append(offered, o.Value)
	}
	joined := strings.Join(offered, " ")
	if !strings.Contains(joined, "search_support_bundles") {
		t.Error("a phase's tool list must be able to name an attached source's tools")
	}
	// The agent editor's own list is deliberately unchanged: attached sources
	// are chosen in the Sources picker, not by ticking tools.
	var agentSide []string
	for _, o := range availableWorkerToolOptions("u") {
		agentSide = append(agentSide, o.Value)
	}
	if strings.Contains(strings.Join(agentSide, " "), "search_support_bundles") {
		t.Error("the agent tools modal should not offer source tools — attaching is what grants them")
	}
}

// And the save-time checklist must not report those names as typos.
func TestTheSaveChecklistKnowsAttachedSourceTools(t *testing.T) {
	withBundleSource(t)
	_, def := machineTurnFixture(t, MachineDef{
		Name: "diag", Start: "scan",
		Phases: []MachinePhase{{Name: "scan", Desc: "Go and read.", Resident: true,
			Prompt: "Search the bundles.", Tools: []string{"search_support_bundles"}}},
	})
	root := &DBase{Store: kvlite.MemStore()}
	if got := unknownPhaseToolFindings(UserDB(root, "u"), "u", def); len(got) != 0 {
		t.Errorf("a real attached-source tool name was reported as unreachable: %v", got)
	}
}
