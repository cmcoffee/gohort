package orchestrate

import (
	"bytes"
	"context"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
	"os"
	"strings"
	"testing"
)

// Two questions a machine's tool list cannot answer on its own: is the coarse
// control what this step actually meant, and can the agent it was just given to
// reach what it names.
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

// The rehearsal is tool-less by design — it exists so a machine can be watched
// before anything is attached to it — which left one question it could not
// answer at all: what would this step have had in front of it. A reach set in
// the editor was observable only on a live turn, in a log.
func TestTheRehearsalResolvesWhatEachStepWouldReach(t *testing.T) {
	udb, user := preflightFixture(t)
	withBundleSource(t)

	def := MachineDef{ID: "m1", Name: "diag", Owner: user, Phases: []MachinePhase{
		{Name: "scan", Prompt: "look", Reach: ReachRead, Next: "answer"},
		{Name: "decide", Prompt: "route", Reach: ReachNone},
		{Name: "answer", Prompt: "reply", Resident: true},
	}}
	ag := AgentRecord{ID: "a1", Name: "Wren", Owner: user, Machine: "m1",
		OrchestratorPrompt: "You are Wren.",
		AttachedSources:    []ReferenceSelection{{Kind: "testfiles", ItemID: "support_bundles"}}}
	if _, err := saveAgent(udb, ag); err != nil {
		t.Fatalf("save agent: %v", err)
	}

	cur := &MachineCursor{Phase: "scan", Log: []PhaseHop{{From: "scan", To: "answer"}}}
	landed, _ := def.Phase("answer")
	rows, note := tryReach(udb, user, def, cur, landed, 0)

	if !strings.Contains(note, "Wren") {
		t.Errorf("the preview must say whose catalog it resolved: %q", note)
	}
	byStep := map[string]map[string]any{}
	for _, r := range rows {
		byStep[r["step"].(string)] = r
	}
	// Only what this turn touched: decide was never entered.
	if _, ran := byStep["decide"]; ran {
		t.Error("a step this turn never entered should not be reported")
	}
	scan := byStep["scan"]
	if scan == nil {
		t.Fatalf("the step that ran should be reported: %+v", rows)
	}
	tools, _ := scan["tools"].([]string)
	if len(tools) == 0 {
		t.Fatal("a read-only step on an agent with a file store should reach its read tools")
	}
	for _, n := range tools {
		if n == "fetch_url" || n == "run_shell" {
			t.Errorf("a read-only step reached %q", n)
		}
	}
	if !strings.Contains(strings.Join(tools, ","), "search_support_bundles") {
		t.Errorf("the agent's attachment should be in what the step reaches: %v", tools)
	}
}

// With nothing attached there is no catalog to resolve against, and saying so
// beats an empty section that reads as "this step reaches nothing".
func TestTheRehearsalSaysWhenThereIsNoAgentToResolveAgainst(t *testing.T) {
	udb, user := preflightFixture(t)
	def := MachineDef{ID: "m9", Name: "orphan", Owner: user,
		Phases: []MachinePhase{{Name: "answer", Prompt: "reply", Resident: true}}}
	landed, _ := def.Phase("answer")

	rows, note := tryReach(udb, user, def, &MachineCursor{Phase: "answer"}, landed, 0)
	if rows != nil {
		t.Errorf("nothing to resolve against means nothing to report: %+v", rows)
	}
	if !strings.Contains(note, "No agent runs this machine") {
		t.Errorf("the reason should be stated, not left as an empty list: %q", note)
	}
}

// The one dependency an agent recipe carried without declaring. Its tools, its
// machine, its pipelines, its skills and its collections all travel or are
// warned about; an attachment rode along as two strings pointing at a store
// that exists on the exporting box and nowhere else. The agent then landed
// looking complete — the picker showed the attachment, and the tools it was
// supposed to mint were simply absent, which reads as tools that went missing
// rather than as a source nobody has.
func TestAnAgentsAttachedSourceTravelsAsADeclaredDependency(t *testing.T) {
	withBundleSource(t)
	exp := agentExport{AgentRecord: AgentRecord{
		Name:            "Wren",
		AttachedSources: []ReferenceSelection{{Kind: "testfiles", ItemID: "support_bundles"}},
	}}

	deps := agentExportDeps(nil, exp, "u", nil)
	var found bool
	for _, d := range deps {
		if d.Type == "reference_source" && d.Name == "testfiles:support_bundles" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the agent's attachment should be declared so an import can warn about it: %+v", deps)
	}

	// A sub-agent's attachments count too: the parent's recipe recreates the
	// whole tree, so a source only the child holds is still a source the
	// bundle depends on.
	withSub := agentExport{
		AgentRecord: AgentRecord{Name: "Wren"},
		SubAgents: []AgentRecord{{Name: "Scout",
			AttachedSources: []ReferenceSelection{{Kind: "testfiles", ItemID: "support_bundles"}}}},
	}
	if deps := agentExportDeps(nil, withSub, "u", nil); len(deps) == 0 {
		t.Error("a sub-agent's attachment is still the bundle's dependency")
	}

	// And an agent attached to nothing declares nothing — a bundle that warned
	// about a source no agent asked for is a bundle people stop reading.
	if deps := agentExportDeps(nil, agentExport{AgentRecord: AgentRecord{Name: "Plain"}}, "u", nil); len(deps) != 0 {
		t.Errorf("nothing attached, nothing to declare: %+v", deps)
	}
}

// The cheap runner, end to end: a step that names a tool calls it, with the
// author's keys and templated values, and hands the result on — no model, no
// tokens, no catalog assembled for a decision nobody has to make.
func TestAToolStepCallsTheToolAndHandsOnItsResult(t *testing.T) {
	udb, user := preflightFixture(t)
	var gotArgs map[string]any
	RegisterChatTool(&fakeEchoTool{name: "pf_echo", onRun: func(a map[string]any) { gotArgs = a }})

	def := MachineDef{ID: "m1", Name: "fetch", Owner: user, Phases: []MachinePhase{
		{Name: "grab", Tool: "pf_echo", Args: map[string]string{"what": "{prev}", "fixed": "yes"}, Next: "answer"},
		{Name: "answer", Prompt: "reply", Resident: true},
	}}
	ag := AgentRecord{ID: "a1", Name: "Wren", Owner: user, Machine: "m1", OrchestratorPrompt: "You are Wren."}
	if _, err := saveAgent(udb, ag); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	turn := &chatTurn{app: &OrchestrateApp{}, ctx: context.Background(), user: user, udb: udb, agent: ag}

	ph, _ := def.Phase("grab")
	out, err := turn.machineHost().runToolPhase(context.Background(), ph, "pf_echo", "what the last step said")
	if err != nil {
		t.Fatalf("tool step: %v", err)
	}
	if !strings.Contains(out, "pf_echo ran") {
		t.Errorf("the tool's result should be the step's result: %q", out)
	}
	// {prev} carries the step before it; a literal stays literal.
	if gotArgs["what"] != "what the last step said" || gotArgs["fixed"] != "yes" {
		t.Errorf("args should template values and keep the author's keys: %+v", gotArgs)
	}
}

// A machine can name a tool the agent running it does not carry — it is
// portable, and the far side's catalog is not its business. That has to be
// said rather than failing as a bare error nobody can act on.
func TestAToolStepSaysWhenTheAgentLacksTheTool(t *testing.T) {
	udb, user := preflightFixture(t)
	ag := AgentRecord{ID: "a2", Name: "Wren", Owner: user, OrchestratorPrompt: "You are Wren."}
	if _, err := saveAgent(udb, ag); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	sess := &ChatSession{ID: "s1", AgentID: "a2"}
	if stored, err := saveChatSession(udb, *sess); err == nil {
		*sess = stored
	}
	turn := &chatTurn{app: &OrchestrateApp{}, ctx: context.Background(), user: user, udb: udb,
		agent: ag, session: sess}

	ph := MachinePhase{Name: "grab", Tool: "nothing_provides_this"}
	if _, err := turn.machineHost().runToolPhase(context.Background(), ph, "nothing_provides_this", ""); err == nil {
		t.Fatal("calling a tool the agent lacks should fail the step")
	}
	var diags []SessionDiag
	udb.Get(sessionDiagTable, "a2:"+sess.ID, &diags)
	var found bool
	for _, d := range diags {
		if strings.Contains(d.Detail, "nothing_provides_this") {
			found = true
		}
	}
	if !found {
		t.Errorf("the step should leave a breadcrumb naming what it could not call: %+v", diags)
	}
}

// fakeEchoTool is a registered tool that records what it was called with.
type fakeEchoTool struct {
	name  string
	onRun func(map[string]any)
}

func (f *fakeEchoTool) Name() string                 { return f.name }
func (f *fakeEchoTool) Desc() string                 { return "echoes" }
func (f *fakeEchoTool) Params() map[string]ToolParam { return map[string]ToolParam{} }
func (f *fakeEchoTool) Caps() []Capability           { return []Capability{CapRead} }
func (f *fakeEchoTool) Run(args map[string]any) (string, error) {
	if f.onRun != nil {
		f.onRun(args)
	}
	return f.name + " ran", nil
}

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
		// A REMOTE read, declared the way core declares one (a source hook,
		// an MCP proxy tool): CapNetwork rides alongside CapRead because
		// answering means leaving the box. It only reads, and a read-only
		// reach drops it anyway — which is the whole subtlety this store
		// exists to keep honest.
		{Tool: Tool{Name: "investigate_support_bundles", Description: "Ask the far side about a bundle.",
			Caps: []Capability{CapNetwork, CapRead}},
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

// Turn-side machine wiring (machine.go). The core driver's walk is
// covered in core/machine_test.go; what these pin down is what
// orchestrate owns — pinning the def to the session, persisting the
// cursor, where the phase lands in the prompt, narrowing the catalog,
// the tier override, and the resident handoff.
//
// Every fixture starts on a RESIDENT phase, so no transient phase runs
// and no LLM is needed. That is also the case that matters most: it is
// what turns 2..N of every machine conversation actually do.

func machineTurnFixture(t *testing.T, def MachineDef) (*chatTurn, MachineDef) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	app := &OrchestrateApp{}
	app.DB = root
	// The tool catalog a step draws from reads the auth store (tool
	// groups, users), so a turn fixture needs one to be a real turn.
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:u", AuthUser{Username: "u"})
	prevAuth := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prevAuth })

	def.Owner = "u"
	if err := def.Validate(); err != nil {
		t.Fatalf("fixture machine should validate: %v", err)
	}
	saved := SaveMachineDef(udb, def)

	sess := &ChatSession{ID: "s1", AgentID: "a1"}
	if stored, err := saveChatSession(udb, *sess); err == nil {
		*sess = stored
	}
	turn := &chatTurn{
		app: app, ctx: context.Background(), user: "u", udb: udb,
		agent:   AgentRecord{ID: "a1", Name: "Wren", Owner: "u", Machine: saved.ID},
		session: sess,
	}
	return turn, saved
}

// residentMachine parks on its first phase and stays there.
func residentMachine() MachineDef {
	return MachineDef{
		Name: "desk", Start: "answer",
		Phases: []MachinePhase{
			{Name: "answer", Desc: "Reply directly.", Resident: true,
				Prompt: "Answer plainly, using what is settled below."},
		},
	}
}

func TestEnterMachine_NoMachineIsInert(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.agent.Machine = "" // the every-agent-today case

	m := turn.enterMachine("hello")
	if m.on {
		t.Fatal("an agent with no machine must not enter one")
	}
	if m.Block() != "" {
		t.Error("no machine must contribute nothing to the prompt")
	}
	if m.Tier() != TierUnset {
		t.Error("no machine must leave tier routing alone")
	}
	if m.Think(true) != true || m.Think(false) != false {
		t.Error("no machine must leave the think setting alone")
	}
	catalog := []AgentToolDef{{Tool: Tool{Name: "web_search"}}}
	if got := m.Tools(catalog); len(got) != 1 {
		t.Error("no machine must leave the catalog alone")
	}
}

func TestEnterMachine_PinsTheDefAndParksTheCursor(t *testing.T) {
	turn, def := machineTurnFixture(t, residentMachine())

	m := turn.enterMachine("what's the status?")
	if !m.on {
		t.Fatal("expected the machine to run")
	}
	if m.Name() != "answer" {
		t.Fatalf("expected to park on answer, got %s", m.Name())
	}
	if turn.session.MachineID != def.ID {
		t.Errorf("the session should pin the machine it started on, got %q", turn.session.MachineID)
	}
	if turn.session.Phase != "answer" {
		t.Errorf("the cursor should persist, got %q", turn.session.Phase)
	}
	// And it survives a reload, which is the whole point of persisting it.
	if reloaded, ok := loadChatSession(turn.udb, "a1", "s1"); !ok {
		t.Fatal("session should reload")
	} else if reloaded.Phase != "answer" || reloaded.MachineID != def.ID {
		t.Errorf("phase state did not survive the round trip: %+v", reloaded)
	}
}

func TestEnterMachine_RepointingTheAgentLeavesLiveSessionsAlone(t *testing.T) {
	// The pin exists so re-pointing an agent reshapes NEW conversations
	// and leaves ones already in flight where they are.
	turn, first := machineTurnFixture(t, residentMachine())
	turn.enterMachine("first turn")

	other := SaveMachineDef(turn.udb, MachineDef{
		Name: "other", Owner: "u", Start: "b",
		Phases: []MachinePhase{{Name: "b", Prompt: "different", Resident: true}},
	})
	turn.agent.Machine = other.ID

	m := turn.enterMachine("second turn")
	if m.def.ID != first.ID {
		t.Errorf("a live session should keep the machine it started on, got %s", m.def.Name)
	}
	if m.Name() != "answer" {
		t.Errorf("expected to stay in the original machine's phase, got %s", m.Name())
	}
}

func TestEnterMachine_MissingMachineDegradesToAPlainTurn(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.agent.Machine = "no-such-machine"
	turn.session.MachineID = ""

	m := turn.enterMachine("hello")
	if m.on {
		t.Fatal("a missing machine must not run")
	}
	// The turn still happens; the breadcrumb is what says why it was
	// different from what the author configured.
	var diags []SessionDiag
	turn.udb.Get(sessionDiagTable, "a1:s1", &diags)
	if len(diags) == 0 {
		t.Error("a missing machine must leave a breadcrumb on the session trail")
	}
}

func TestMachineBlock_CarriesTheDirectiveAndPinnedFindings(t *testing.T) {
	def := MachineDef{
		Name: "triage", Start: "answer",
		Phases: []MachinePhase{
			{Name: "decompose", Prompt: "Split {input}.", Next: "answer",
				Output: []PipelineField{{Name: "parts", Type: FieldList}}},
			{Name: "answer", Desc: "Reply.", Resident: true, Prompt: "Work from what is settled."},
		},
	}
	turn, _ := machineTurnFixture(t, def)
	turn.session.Phase = "answer"
	turn.session.MachineState = MachineState{
		"decompose": {Fields: map[string]any{"parts": []any{"cost", "timeline"}}},
	}

	m := turn.enterMachine("follow-up")
	block := m.Block()
	if !strings.Contains(block, "Current phase: answer") {
		t.Errorf("block should name the phase: %s", block)
	}
	if !strings.Contains(block, "cost") {
		t.Errorf("block should pin what an earlier phase established: %s", block)
	}
	// Byte-stability across turns is what keeps the cached prefix valid.
	if again := m.Block(); again != block {
		t.Error("the phase block must be byte-stable across renders")
	}
}

func TestMachinePhase_NarrowsToolsAndPinsTierAndThink(t *testing.T) {
	def := MachineDef{
		Name: "narrow", Start: "answer",
		Phases: []MachinePhase{
			{Name: "answer", Resident: true, Prompt: "Reply.",
				Tools: []string{"knowledge_search"}, Model: "lead", Think: "off"},
		},
	}
	turn, _ := machineTurnFixture(t, def)
	m := turn.enterMachine("q")

	catalog := []AgentToolDef{
		{Tool: Tool{Name: "web_search"}},
		{Tool: Tool{Name: "knowledge_search"}},
	}
	got := m.Tools(catalog)
	if len(got) != 1 || got[0].Tool.Name != "knowledge_search" {
		t.Errorf("the phase should narrow the catalog to what it named, got %#v", got)
	}
	if m.Tier() != LEAD {
		t.Error("the phase should pin the tier")
	}
	if m.Think(true) {
		t.Error("the phase's think setting is the most specific and should win")
	}
}

func TestChangePhaseTool_OfferedOnlyWhenThereIsAnExit(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine()) // one phase
	turn.enterMachine("hi")
	if turn.hasMachineExit() {
		t.Error("a one-phase machine has nowhere to go; the tool must not be offered")
	}

	two, _ := machineTurnFixture(t, MachineDef{
		Name: "two", Start: "a",
		Phases: []MachinePhase{
			{Name: "a", Resident: true, Prompt: "x"},
			{Name: "b", Resident: true, Prompt: "y"},
		},
	})
	two.enterMachine("hi")
	if !two.hasMachineExit() {
		t.Error("a machine with somewhere to go should offer the tool")
	}
	// And an agent with no machine at all never sees it.
	two.machine = turnMachine{}
	if two.hasMachineExit() {
		t.Error("no machine means no change_phase")
	}
}

func TestChangePhaseTool_MovesTheCursorAndReturnsTheNewDirective(t *testing.T) {
	turn, _ := machineTurnFixture(t, MachineDef{
		Name: "two", Start: "intake",
		Phases: []MachinePhase{
			{Name: "intake", Desc: "Find out what they want.", Resident: true, Prompt: "Ask."},
			{Name: "work", Desc: "Do the job.", Resident: true, Prompt: "Build it."},
		},
	})
	turn.enterMachine("hi")

	out, err := turn.changePhaseToolDef().Handler(map[string]any{
		"phase": "work", "why": "they told me what they want",
	})
	if err != nil {
		t.Fatalf("change_phase: %v", err)
	}
	if turn.session.Phase != "work" {
		t.Fatalf("the cursor should have moved, got %q", turn.session.Phase)
	}
	if turn.machine.Name() != "work" {
		t.Errorf("the rest of the turn should run under the new phase, got %q", turn.machine.Name())
	}
	// The result has to carry the new directive: the system prompt still
	// holds the old one, and the tool result is the only thing that can
	// supersede it inside this turn.
	if !strings.Contains(out, "Build it.") || !strings.Contains(out, "out of date") {
		t.Errorf("the result should replace the stale directive, got: %s", out)
	}
	// It survives to the next turn too.
	if reloaded, ok := loadChatSession(turn.udb, "a1", "s1"); !ok || reloaded.Phase != "work" {
		t.Errorf("the move must persist, got %+v", reloaded)
	}
}

func TestChangePhaseTool_RefusesUnknownPhasesAndThrashing(t *testing.T) {
	turn, _ := machineTurnFixture(t, MachineDef{
		Name: "three", Start: "a",
		Phases: []MachinePhase{
			{Name: "a", Resident: true, Prompt: "x"},
			{Name: "b", Resident: true, Prompt: "y"},
			{Name: "c", Resident: true, Prompt: "z"},
		},
	})
	turn.enterMachine("hi")
	tool := turn.changePhaseToolDef()

	if _, err := tool.Handler(map[string]any{"phase": "ghost", "why": "x"}); err == nil {
		t.Error("expected a refusal naming the available phases")
	} else if !strings.Contains(err.Error(), "b, c") && !strings.Contains(err.Error(), "a, b, c") {
		t.Errorf("the refusal should list the real choices, got: %v", err)
	}
	if turn.session.Phase != "a" {
		t.Fatalf("a refused move must not touch the cursor, got %q", turn.session.Phase)
	}

	for i := 0; i < maxPhaseChangesPerTurn; i++ {
		want := []string{"b", "c"}[i%2]
		if _, err := tool.Handler(map[string]any{"phase": want, "why": "moved on"}); err != nil {
			t.Fatalf("change %d should succeed: %v", i+1, err)
		}
	}
	if _, err := tool.Handler(map[string]any{"phase": "a", "why": "again"}); err == nil {
		t.Error("expected the cap to refuse a third change in one turn")
	}
}

func TestCompleteMachine_OneBeatPhaseHandsOffAfterItsTurn(t *testing.T) {
	def := MachineDef{
		Name: "intakeflow", Start: "intake",
		Phases: []MachinePhase{
			{Name: "intake", Resident: true, Prompt: "Ask what they need.", Next: "work"},
			{Name: "work", Resident: true, Prompt: "Do it."},
		},
	}
	turn, _ := machineTurnFixture(t, def)

	m := turn.enterMachine("hi")
	if m.Name() != "intake" {
		t.Fatalf("expected to start in intake, got %s", m.Name())
	}
	turn.completeMachine(m)
	if turn.session.Phase != "work" {
		t.Fatalf("the one-beat phase should hand off after its turn, cursor is %q", turn.session.Phase)
	}
	// Next turn opens in work, and stays there.
	m2 := turn.enterMachine("go on")
	if m2.Name() != "work" {
		t.Fatalf("expected the next turn to open in work, got %s", m2.Name())
	}
	turn.completeMachine(m2)
	if turn.session.Phase != "work" {
		t.Errorf("a phase with no next must stay put, got %q", turn.session.Phase)
	}
}

// {original_input} on a LIVE session: the cursor is rebuilt from the
// session every turn, so without a persisted home the "written once"
// guard re-latched onto the CURRENT message — the exact lie the
// variable's documentation warns about, and a cache-buster for any
// resident prompt placing it.
func TestOpeningSurvivesAcrossLiveTurns(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.enterMachine("why is the export failing?")
	if turn.session.MachineOpening != "why is the export failing?" {
		t.Fatalf("the opening should persist on the session, got %q", turn.session.MachineOpening)
	}

	// A later turn on the SAME session rebuilds the cursor from the
	// session fields; the opening must ride back rather than re-latch.
	turn.machine = turnMachine{}
	turn.enterMachine("any update?")
	if turn.session.MachineOpening != "why is the export failing?" {
		t.Errorf("the opening drifted to the latest message: %q", turn.session.MachineOpening)
	}
	if turn.machine.vars.Opening != "why is the export failing?" {
		t.Errorf("the resident block should see the ORIGINAL opening, got %q", turn.machine.vars.Opening)
	}
}

// A machine's steps run at the HEAD of a turn, before the persona is
// assembled and before a single word reaches the person — two model
// calls of silence for a decompose-then-route machine. Each one says
// what it is doing first, in the author's own words.
func TestEveryStepSaysWhatItIsDoing(t *testing.T) {
	cases := map[string]struct {
		in   MachinePhase
		want string
	}{
		"the author's description": {
			MachinePhase{Name: "triage", Desc: "Work out what kind of turn this is."},
			"triage: Work out what kind of turn this is…",
		},
		"a step that never got one": {
			MachinePhase{Name: "hunch"},
			"Working through hunch…",
		},
		// A guard is a call paid on EVERY turn spent in a step, and it
		// arrives as a synthetic phase. Naming it is the only honest
		// account of where that second went.
		"a guard": {
			MachinePhase{Name: "guard:answer", Desc: "unused"},
			"Checking whether this is still the same job…",
		},
	}
	for name, c := range cases {
		if got := phaseStatusLine(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// And the runner actually emits it — before the call, not after, or the
// line arrives with the answer it was meant to cover for.
func TestPhaseRunnerAnnouncesBeforeItRuns(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	var buf bytes.Buffer
	turn.sse = &sseWriter{live: &buf}
	turn.app.LLM = &stubLLM{reply: "worked it out"}

	run := turn.phaseRunner()
	if _, err := run(context.Background(),
		MachinePhase{Name: "triage", Desc: "Work out what kind of turn this is."}, "do it"); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "triage: Work out what kind of turn this is") {
		t.Errorf("the step should announce itself:\n%s", got)
	}
	if !strings.Contains(got, `"type":"status"`) {
		t.Errorf("it should ride the activity surface's status channel:\n%s", got)
	}
}

// A step that names tools gets them. This was the editor's one control
// that did nothing: the checklist offered the real pool, the machine
// tool documented it, and the runtime handed every step an empty
// catalog — so a step told to "go and look" could not.
func TestAStepThatNamesToolsGetsThem(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())

	// Naming none inherits the catalog, the same as it does while a
	// conversation waits in a resident phase. The two rules used to
	// disagree, on the same control in the same editor.
	if got := turn.machineCatalog(MachinePhase{Name: "triage"}); len(got) == 0 {
		t.Error("a step that names no tools should inherit the agent's catalog")
	}

	// The marker is how a step says it wants none — a decompose or route
	// step that only reshapes what it was given.
	bare, _ := machineTurnFixture(t, residentMachine())
	if got := bare.machineCatalog(MachinePhase{Name: "triage", Tools: []string{noToolsSentinel}}); got != nil {
		t.Errorf("the no-tools marker should draw nothing, got %d", len(got))
	}
	if bare.machineTools != nil {
		t.Error("and should not have built a catalog to find that out")
	}

	// Naming some resolves the pool once and reuses it — a repair retry
	// and a second step must not each pay for their own.
	first := turn.machineCatalog(MachinePhase{Name: "hunch", Tools: []string{"read_file"}})
	if first == nil {
		t.Fatal("a step that names tools must get a catalog to narrow")
	}
	if same := turn.machineCatalog(MachinePhase{Name: "verify", Tools: []string{"read_file"}}); len(same) != len(first) {
		t.Error("the catalog should be built once per turn, not per step")
	}

	// PhaseWorker narrows the pool to what the step named — that is the
	// half that was already right, and the half this feeds.
	narrowed := PhaseTools(MachinePhase{Tools: []string{"read_file"}},
		[]AgentToolDef{{Tool: Tool{Name: "read_file"}}, {Tool: Tool{Name: "web_search"}}})
	if len(narrowed) != 1 || narrowed[0].Tool.Name != "read_file" {
		t.Errorf("the step should reach exactly what it named, got %+v", narrowed)
	}
}

// Giving steps tools opened a hole under the approval card: the turn's
// own tool loop stops for a credential marked RequiresConfirm, while a
// worker stage auto-approves because pipelines run with nobody
// watching. A machine step runs with somebody waiting, so it takes the
// turn's gate.
func TestAStepsToolsGoThroughTheTurnsApprovalGate(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())

	// The marker: nothing to gate, and no session built to gate with.
	if turn.machineCatalog(MachinePhase{Name: "triage", Tools: []string{noToolsSentinel}}) != nil ||
		turn.machineConfirm() != nil {
		t.Error("a step that wants no tools should need no gate")
	}

	// Tools named: the gate exists and is built against the SAME session
	// the tools were resolved against — which credential a call rides on
	// is session state.
	turn.machineCatalog(MachinePhase{Name: "hunch", Tools: []string{"read_file"}})
	if turn.machineSess == nil {
		t.Fatal("the session the tools came from should be kept")
	}
	gate := turn.machineConfirm()
	if gate == nil {
		t.Fatal("a step with tools must carry the turn's approval gate")
	}
	// A tool riding no credential is allowed without a card, same as the
	// turn's own path.
	if !gate("read_file", "{}") {
		t.Error("an ungated tool should not stop for approval")
	}

	// And the core seam defaults to allow ONLY for callers with nobody
	// to ask: a nil hook is the unattended pipeline's, not a machine's.
	if !strings.Contains(readFileForTest(t, "../../core/machine_phase.go"), "PhaseWorkerConfirm") {
		t.Error("the host needs a way to supply its own hook")
	}
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A machine-driven agent, DISPATCHED (scheduled fire, delegation,
// phantom, sub-agent), runs without its machine: that path assembles
// its own system prompt and there is no conversation to hold a position
// in. It may be the right trade for a one-shot, but an agent that is
// not itself must not be silent about it — every dispatch entry point
// comes through one builder, so the breadcrumb is written once.
func TestADispatchedMachineAgentSaysItRanWithoutIt(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Investigation", Start: "s",
		Phases: []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}})
	agent, err := saveAgent(udb, AgentRecord{Name: "Wren", Owner: user,
		OrchestratorPrompt: "hi", Machine: def.ID})
	if err != nil {
		t.Fatal(err)
	}

	// The trail is opened where every dispatch path opens it, which is
	// what makes the note impossible to forget on a new path.
	subTurn := &chatTurn{app: app, agent: agent, user: user, udb: udb,
		ownerUser: user, ownerDB: udb, ctx: context.Background()}
	subTurn.beginDispatchDiag(agent.ID, "sub-1")

	var list []SessionDiag
	udb.Get("session_diag", agent.ID+":sub-1", &list)
	found := false
	for _, d := range list {
		if d.Kind == "machine_not_on_dispatch" {
			found = true
			// Named, not merely reported: "your agent ran without
			// something" is only actionable if it says WHICH something.
			if !strings.Contains(d.Detail, "Investigation") {
				t.Errorf("the breadcrumb should name the machine: %q", d.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("a dispatched machine agent should say it ran without its machine, got %+v", list)
	}

	// An agent with no machine has nothing to report, and should not
	// spend a line saying so.
	plain, _ := saveAgent(udb, AgentRecord{Name: "Crow", Owner: user, OrchestratorPrompt: "hi"})
	quiet := &chatTurn{app: app, agent: plain, user: user, udb: udb, ctx: context.Background()}
	quiet.beginDispatchDiag(plain.ID, "sub-2")
	var none []SessionDiag
	udb.Get("session_diag", plain.ID+":sub-2", &none)
	if len(none) != 0 {
		t.Errorf("an agent with no machine should leave no note: %+v", none)
	}
}

// scopeCatalog is a stand-in for an assembled turn catalog: the control
// plane the framework supplies, plus a couple of real capabilities.
func scopeCatalog() []AgentToolDef {
	names := []string{
		"plan_set", "stay_silent", "keep_going", "respond_directly", "change_phase",
		"web_search", "atlassian_getconfluencepage", "atlassian_search",
	}
	out := make([]AgentToolDef, 0, len(names))
	for _, n := range names {
		out = append(out, AgentToolDef{Tool: Tool{Name: n}})
	}
	return out
}

func scopeNames(tools []AgentToolDef) []string {
	out := make([]string, 0, len(tools))
	for _, td := range tools {
		out = append(out, td.Tool.Name)
	}
	return out
}

func hasScopeName(tools []AgentToolDef, name string) bool {
	for _, td := range tools {
		if td.Tool.Name == name {
			return true
		}
	}
	return false
}

// A phase narrows REACH, not the machinery. change_phase in particular is
// the way out of the phase: a narrowing that took it left the session
// standing in a step it could never leave.
func TestPhaseNarrowingKeepsTheControlPlane(t *testing.T) {
	m := turnMachine{on: true, phase: MachinePhase{Name: "investigate", Tools: []string{"web_search"}}}
	out, dropped, unmatched, fellBack := m.narrowCatalog(scopeCatalog(), nil)

	if fellBack {
		t.Fatal("a phase that matched a tool should not fall back")
	}
	if len(unmatched) != 0 {
		t.Errorf("nothing was misnamed, got unmatched %v", unmatched)
	}
	for _, n := range []string{"plan_set", "stay_silent", "keep_going", "respond_directly", "change_phase"} {
		if !hasScopeName(out, n) {
			t.Errorf("phase narrowing revoked %q — the model cannot end the turn or leave the phase without it", n)
		}
	}
	if !hasScopeName(out, "web_search") {
		t.Error("the tool the phase actually asked for is missing")
	}
	// Everything else goes, and says so.
	if hasScopeName(out, "atlassian_search") {
		t.Error("a tool the phase did not name survived the narrowing")
	}
	if len(dropped) != 2 {
		t.Errorf("dropped = %v, want the two atlassian tools", dropped)
	}
}

// The catalog order is the payload order, and a payload that reshuffles
// between turns is a cold prompt cache every turn.
func TestPhaseNarrowingPreservesCatalogOrder(t *testing.T) {
	m := turnMachine{on: true, phase: MachinePhase{Name: "investigate", Tools: []string{"atlassian_search", "web_search"}}}
	out, _, _, _ := m.narrowCatalog(scopeCatalog(), nil)

	got := scopeNames(out)
	want := []string{"plan_set", "stay_silent", "keep_going", "respond_directly", "change_phase", "web_search", "atlassian_search"}
	if len(got) != len(want) {
		t.Fatalf("catalog = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("catalog = %v, want %v (order follows the catalog, not the phase list)", got, want)
		}
	}
}

// The live failure this came from: a phase authored by copying the remote
// server's own tool names. Atlassian ships camelCase; mcpExposedName
// lowercases. Every name missed, the catalog narrowed 112 → 0, and the
// model reported its own tools as unknown and rerouted.
func TestPhaseThatMatchesNothingKeepsTheWholeCatalog(t *testing.T) {
	catalog := scopeCatalog()
	m := turnMachine{on: true, phase: MachinePhase{
		Name:  "investigate",
		Tools: []string{"atlassian_getConfluencePage", "atlassian.search"},
	}}
	out, dropped, unmatched, fellBack := m.narrowCatalog(catalog, nil)

	if !fellBack {
		t.Fatal("a phase whose every name missed should be reported as a misconfiguration")
	}
	if len(out) != len(catalog) {
		t.Errorf("catalog = %d tool(s), want the full %d — an unmatched list must not resolve to nothing", len(out), len(catalog))
	}
	if len(dropped) != 0 {
		t.Errorf("nothing was deliberately narrowed away, got dropped %v", dropped)
	}
	if len(unmatched) != 2 {
		t.Errorf("unmatched = %v, want both misnamed tools so the log can name them", unmatched)
	}
}

// A machine that never mentions tools changes nothing about the agent.
func TestPhaseWithNoToolsInheritsEverything(t *testing.T) {
	catalog := scopeCatalog()
	m := turnMachine{on: true, phase: MachinePhase{Name: "investigate"}}
	out, dropped, unmatched, fellBack := m.narrowCatalog(catalog, nil)

	if len(out) != len(catalog) || len(dropped) != 0 || len(unmatched) != 0 || fellBack {
		t.Errorf("an empty phase list should pass the catalog through untouched: %d tools, dropped %v, unmatched %v, fellBack %v",
			len(out), dropped, unmatched, fellBack)
	}
}

// An agent with no machine at all is never narrowed.
func TestNoMachineNeverNarrows(t *testing.T) {
	catalog := scopeCatalog()
	out, dropped, unmatched, fellBack := turnMachine{}.narrowCatalog(catalog, nil)

	if len(out) != len(catalog) || len(dropped) != 0 || len(unmatched) != 0 || fellBack {
		t.Errorf("no machine means no narrowing: %d tools, dropped %v, unmatched %v, fellBack %v",
			len(out), dropped, unmatched, fellBack)
	}
}

// A phase's tool list is a selection out of the WORKER POOL — that is the
// pool the picker offers, and the only one the author is choosing from. An
// attached source is a separate grant, made in a different picker for a
// different reason, and the tools it mints appear on no tool list anywhere.
//
// So a phase that names a couple of worker tools was silently revoking every
// attachment the agent had, with no box the author could tick to keep them.
// The limit was six days old and nobody chose it: before resident phases
// existed there was no way for a step to subtract anything from a live
// conversation at all.
func TestPhaseNarrowingDoesNotRevokeAttachments(t *testing.T) {
	catalog := append(scopeCatalog(),
		AgentToolDef{Tool: Tool{Name: "search_support_bundles"}},
		AgentToolDef{Tool: Tool{Name: "investigate_kiteworks"}})
	attached := map[string]bool{"search_support_bundles": true, "investigate_kiteworks": true}

	m := turnMachine{on: true, phase: MachinePhase{Name: "investigate", Tools: []string{"web_search"}}}
	out, dropped, _, _ := m.narrowCatalog(catalog, attached)

	for _, n := range []string{"search_support_bundles", "investigate_kiteworks"} {
		if !hasScopeName(out, n) {
			t.Errorf("the phase revoked %q, which attaching the source is what granted", n)
		}
	}
	for _, n := range dropped {
		if attached[n] {
			t.Errorf("%q was reported as dropped even though it survived", n)
		}
	}
	if !hasScopeName(out, "web_search") {
		t.Error("the tool the phase asked for is missing")
	}
	if hasScopeName(out, "atlassian_search") {
		t.Error("an ordinary worker tool the phase did not name should still be narrowed away")
	}
}

// The other half of the rule: a phase that names ONE attachment's tool has
// clearly been written with attachments in mind, and gets exactly what it
// wrote. "That source, not this one" has to be sayable, or the control is no
// control at all.
func TestAPhaseThatNamesAnAttachmentGovernsThemAll(t *testing.T) {
	catalog := append(scopeCatalog(),
		AgentToolDef{Tool: Tool{Name: "search_support_bundles"}},
		AgentToolDef{Tool: Tool{Name: "investigate_kiteworks"}})
	attached := map[string]bool{"search_support_bundles": true, "investigate_kiteworks": true}

	m := turnMachine{on: true, phase: MachinePhase{Name: "scan", Tools: []string{"search_support_bundles"}}}
	out, _, _, fellBack := m.narrowCatalog(catalog, attached)

	if fellBack {
		t.Fatal("the phase named a tool that exists; nothing should have fallen back")
	}
	if !hasScopeName(out, "search_support_bundles") {
		t.Fatal("the attachment the phase named is missing")
	}
	if hasScopeName(out, "investigate_kiteworks") {
		t.Error("naming one attachment must be able to mean 'that one, not the others'")
	}
}

// A list where nothing matched keeps the whole catalog (the author meant a
// selection, not emptiness). With attachments exempt there is a second way
// to be non-empty, and it must not swallow that rescue: a total miss is
// still a total miss, and still says so.
func TestATotalMissStillFallsBackWithAttachmentsPresent(t *testing.T) {
	catalog := append(scopeCatalog(), AgentToolDef{Tool: Tool{Name: "search_support_bundles"}})
	attached := map[string]bool{"search_support_bundles": true}

	m := turnMachine{on: true, phase: MachinePhase{Name: "scan", Tools: []string{"serch_web"}}}
	out, _, unmatched, fellBack := m.narrowCatalog(catalog, attached)

	if !fellBack || len(unmatched) != 1 {
		t.Fatalf("a wholly misnamed list must fall back and say so (fellBack=%v unmatched=%v)", fellBack, unmatched)
	}
	if len(out) != len(catalog) {
		t.Errorf("the fallback keeps the catalog whole, got %v", scopeNames(out))
	}
}

// Reach is the control that survives being run by a different agent: a
// capability class rather than a list of exact strings assembled per turn
// out of MCP connections, per-session credentials and per-agent
// attachments. Read-only keeps what reads and drops the rest, without the
// author having to know a single tool name.
func TestReadOnlyReachDropsWhatWrites(t *testing.T) {
	catalog := []AgentToolDef{
		{Tool: Tool{Name: "search_support_bundles", Caps: []Capability{CapRead}}},
		{Tool: Tool{Name: "fetch_url", Caps: []Capability{CapNetwork, CapRead}}},
		{Tool: Tool{Name: "run_shell", Caps: []Capability{CapExecute}}},
		{Tool: Tool{Name: "change_phase"}},
	}
	m := turnMachine{on: true, phase: MachinePhase{Name: "gather", Reach: ReachRead}}
	out, dropped, _, fellBack := m.narrowCatalog(catalog, nil)

	if fellBack {
		t.Fatal("a reach is not a name list and cannot miss")
	}
	if !hasScopeName(out, "search_support_bundles") {
		t.Error("a read tool should survive a read-only step")
	}
	for _, n := range []string{"fetch_url", "run_shell"} {
		if hasScopeName(out, n) {
			t.Errorf("%q reaches past reading and should be gone", n)
		}
	}
	if !hasScopeName(out, "change_phase") {
		t.Error("the control plane survives a reach, the same as it survives a name list")
	}
	if len(dropped) != 2 {
		t.Errorf("both should be reported as dropped, got %v", dropped)
	}
}

// Reach "none" is the explicit nothing, and it keeps what a grant gave:
// the workflow controls, and the agent's attachments.
func TestReachNoneKeepsOnlyTheGrants(t *testing.T) {
	catalog := append(scopeCatalog(), AgentToolDef{Tool: Tool{Name: "search_support_bundles"}})
	attached := map[string]bool{"search_support_bundles": true}

	m := turnMachine{on: true, phase: MachinePhase{Name: "triage", Reach: ReachNone}}
	out, _, unmatched, fellBack := m.narrowCatalog(catalog, attached)

	if fellBack || len(unmatched) != 0 {
		t.Fatalf("an explicit nothing is not a miss (fellBack=%v unmatched=%v)", fellBack, unmatched)
	}
	if hasScopeName(out, "web_search") || hasScopeName(out, "atlassian_search") {
		t.Errorf("nothing means nothing from the worker pool: %v", scopeNames(out))
	}
	if !hasScopeName(out, "change_phase") {
		t.Error("the step still has to be able to end the turn and move on")
	}
}

// The legacy marker predates the field and said exactly what reach "none"
// says. A machine saved before the field existed must keep meaning it.
func TestTheOldMarkerReadsAsReachNone(t *testing.T) {
	if got := PhaseReach(MachinePhase{Tools: []string{NoToolsMarker}}); got != ReachNone {
		t.Errorf("a stored %q should read as reach none, got %q", NoToolsMarker, got)
	}
	if got := PhaseReach(MachinePhase{}); got != ReachAll {
		t.Errorf("an untouched step inherits everything, got %q", got)
	}
}

// Two controls that argue with each other: a step whose reach is read-only
// naming a tool that writes. It has to say WHICH control won, or the author
// reads it as the tool having gone missing and goes looking for it.
func TestANameTheReachDroppedSaysSo(t *testing.T) {
	catalog := []AgentToolDef{
		{Tool: Tool{Name: "search_support_bundles", Caps: []Capability{CapRead}}},
		{Tool: Tool{Name: "fetch_url", Caps: []Capability{CapNetwork}}},
	}
	m := turnMachine{on: true, phase: MachinePhase{
		Name: "gather", Reach: ReachRead, Tools: []string{"search_support_bundles", "fetch_url"}}}
	_, _, unmatched, _ := m.narrowCatalog(catalog, nil)

	if len(unmatched) != 1 || !strings.Contains(unmatched[0], "fetch_url") ||
		!strings.Contains(unmatched[0], "reach") {
		t.Errorf("the report should name the tool AND the control that took it: %v", unmatched)
	}
}

// The step keeps everything it had, minus the names it denied — and keeps the
// control plane, which is never denied.
func TestDenyRemovesOnlyWhatItNames(t *testing.T) {
	m := turnMachine{on: true, phase: MachinePhase{Name: "investigate", Deny: []string{"web_search"}}}
	out, dropped, _, fellBack := m.narrowCatalog(scopeCatalog(), nil)

	if fellBack {
		t.Fatal("a deny is not a selection that missed")
	}
	if hasScopeName(out, "web_search") {
		t.Error("the denied tool survived the narrowing")
	}
	for _, n := range []string{"atlassian_search", "atlassian_getconfluencepage", "change_phase", "plan_set"} {
		if !hasScopeName(out, n) {
			t.Errorf("a deny must subtract only what it names; %q went missing: %v", n, scopeNames(out))
		}
	}
	if len(dropped) != 1 || dropped[0] != "web_search" {
		t.Errorf("dropped should name exactly the denied tool, got %v", dropped)
	}
}

// An attachment is a deliberate grant and so is a deny. The deny is the one
// somebody wrote about THIS step, so it is the one that wins — otherwise
// attaching a tool would quietly reopen a step that was closed on purpose.
func TestDenyOutranksAnAttachment(t *testing.T) {
	m := turnMachine{on: true, phase: MachinePhase{Name: "investigate", Deny: []string{"web_search"}}}
	attached := map[string]bool{"web_search": true}

	out, _, _, _ := m.narrowCatalog(scopeCatalog(), attached)
	if hasScopeName(out, "web_search") {
		t.Error("an attachment must not carry a denied tool back into the phase")
	}

	// Same under a reach of none, which takes the other code path.
	none := turnMachine{on: true, phase: MachinePhase{Name: "quiet", Reach: ReachNone, Deny: []string{"web_search"}}}
	out, _, _, _ = none.narrowCatalog(scopeCatalog(), attached)
	if hasScopeName(out, "web_search") {
		t.Error("reach=none plus an attachment must still honour the deny")
	}
	if !hasScopeName(out, "change_phase") {
		t.Error("the control plane survives reach=none, as it always did")
	}
}

// The total-miss rescue restores the whole catalog when an allow list matched
// nothing, on the reasoning that a selection resolving to zero is a typo. That
// reasoning must not reach a phase carrying a deny: restoring the catalog would
// hand back precisely the tool the author wrote the deny to remove.
func TestATypoedAllowListDoesNotUndoADeny(t *testing.T) {
	m := turnMachine{on: true, phase: MachinePhase{
		Name:  "investigate",
		Tools: []string{"web_serch", "atlasian_search"}, // both misspelled
		Deny:  []string{"web_search"},
	}}
	out, _, unmatched, fellBack := m.narrowCatalog(scopeCatalog(), nil)

	if fellBack {
		t.Error("a phase with a deny must not fall back to the whole catalog")
	}
	if hasScopeName(out, "web_search") {
		t.Error("the rescue handed back the denied tool")
	}
	if len(unmatched) != 2 {
		t.Errorf("the misspellings should still be reported, got %v", unmatched)
	}
	if !hasScopeName(out, "change_phase") {
		t.Error("the turn still needs its way out")
	}
}

// A tool that arrives AFTER the catalog was narrowed — authored mid-turn,
// minted by a credential, hydrated lazily — never passes through narrowCatalog.
// Without the round filter, the one control written to keep a step off a tool
// could be walked around by creating it again under the same name.
func TestDenyHoldsForToolsThatArriveLater(t *testing.T) {
	m := turnMachine{on: true, phase: MachinePhase{Name: "investigate", Deny: []string{"web_search"}}}
	if !m.Denies("web_search") {
		t.Error("the phase should deny the name wherever it turns up")
	}
	if m.Denies("atlassian_search") {
		t.Error("a deny must not spread to names it did not list")
	}
	// No machine running: a plain turn denies nothing.
	if (turnMachine{}).Denies("web_search") {
		t.Error("a turn with no machine has nothing to deny")
	}
}

// Three different things can take a named tool away from a step, and only one
// of them is a typo. A step whose reach dropped a name it also names was told
// its spelling was wrong — the message that sent an author hunting a mistake
// they had not made. Say which control did it.
func TestAStepDistinguishesAReachDropFromATypo(t *testing.T) {
	withBundleSource(t)
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.agent.AttachedSources = []ReferenceSelection{{Kind: "testfiles", ItemID: "support_bundles"}}

	// investigate_support_bundles declares CapNetwork alongside CapRead — a
	// remote read — so a read-only reach drops it however plainly it only reads.
	turn.machineCatalog(MachinePhase{Name: "scan", Reach: ReachRead,
		Tools: []string{"investigate_support_bundles"}})

	var list []SessionDiag
	turn.udb.Get(sessionDiagTable, "a1:"+turn.session.ID, &list)
	var found string
	for _, d := range list {
		if d.Kind == "machine_step_tools_missing" {
			found = d.Detail
		}
	}
	if found == "" {
		t.Fatalf("a reach that drops a named tool must leave a breadcrumb; diags: %+v", list)
	}
	if !strings.Contains(found, "tool reach") || !strings.Contains(found, "Read-only") {
		t.Errorf("the breadcrumb must blame the reach and name the setting: %q", found)
	}
	if strings.Contains(found, "does not carry under those names") ||
		strings.Contains(found, "must match the catalog exactly") {
		t.Errorf("a reach drop is not a naming problem and must not be reported as one: %q", found)
	}
	if !strings.Contains(found, "no tools at all") {
		t.Errorf("the step reached nothing, and the breadcrumb should say so: %q", found)
	}
}

// The deny list is the third cause: naming a tool it denies cannot bring it
// back, and neither the reach advice nor the spelling advice applies.
func TestAStepSaysWhenItsOwnDenyTookTheToolItNamed(t *testing.T) {
	withBundleSource(t)
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.agent.AttachedSources = []ReferenceSelection{{Kind: "testfiles", ItemID: "support_bundles"}}

	turn.machineCatalog(MachinePhase{Name: "scan",
		Tools: []string{"search_support_bundles"},
		Deny:  []string{"search_support_bundles"}})

	var list []SessionDiag
	turn.udb.Get(sessionDiagTable, "a1:"+turn.session.ID, &list)
	var found string
	for _, d := range list {
		if d.Kind == "machine_step_tools_missing" {
			found = d.Detail
		}
	}
	if !strings.Contains(found, "names and denies") {
		t.Errorf("a denied name must be reported as a deny, not a miss: %q", found)
	}
}
