// Two questions a machine's tool list cannot answer on its own: is the coarse
// control what this step actually meant, and can the agent it was just given to
// reach what it names.
package orchestrate

import (
	"context"
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
