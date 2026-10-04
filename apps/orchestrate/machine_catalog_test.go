package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// machineCatalogWorld is an empty deployment holding one owner's tools, one of
// each kind of switch, and a credential that asks before every call.
func machineCatalogWorld(t *testing.T) (*OrchestrateApp, Database) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = root
	AuthDB = func() Database { return root }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })

	if err := Secure().Save(SecureCredential{Name: "careful", Type: SecureCredNone, BaseURL: "http://careful.test", RequiresConfirm: true}, ""); err != nil {
		t.Fatal(err)
	}
	shell := func(name string) TempTool {
		return TempTool{Name: name, Description: name, CommandTemplate: "echo " + name}
	}
	tools := []TempTool{shell("geo"), shell("denied"), shell("scoped")}
	off := shell("off")
	off.Disabled = true
	builder := shell("builder")
	builder.BuilderOnly = true
	bound := shell("bound")
	bound.BoundOnly = true
	never := shell("never")
	never.NoUnattended = true
	asks := TempTool{Name: "asks", Description: "asks", Mode: TempToolModeAPI, Credential: "careful", CommandTemplate: "http://careful.test/x"}
	tools = append(tools, off, builder, bound, never, asks)
	for _, tt := range tools {
		if err := AdminPersistTempTool(root, "owner", tt); err != nil {
			t.Fatal(err)
		}
	}
	if !SetUserToolScopeAgents(root, "owner", "scoped", []string{"agent-x"}) {
		t.Fatal("could not scope the tool")
	}
	return &OrchestrateApp{AppCore: AppCore{DB: root}}, UserDB(root, "owner")
}

// A machine run reaches its owner's tools the way an agent with no allow-list
// does, and is told why each one it does not get is out of reach.
func TestAMachineRunReachesTheOwnersTools(t *testing.T) {
	_, udb := machineCatalogWorld(t)
	def := MachineDef{Name: "m", Unattended: true, Deny: []string{"denied"}}
	pool := machineRunCatalog(udb, "owner", "", def)
	have := pool.names()
	if !have["geo"] {
		t.Fatal("the owner's own tool is not in the run's reach")
	}
	for name, why := range map[string]string{
		"off":     "turned off",
		"builder": "reserved for Builder",
		"bound":   "only where it is bound",
		"scoped":  "particular agents",
		"never":   "never to run unattended",
		"asks":    "asks before every call",
		"denied":  "denied by this machine",
	} {
		if have[name] {
			t.Errorf("%s is in reach; it should be withheld", name)
		}
		if !strings.Contains(pool.Withheld[name], why) {
			t.Errorf("%s withheld as %q, want it to say %q", name, pool.Withheld[name], why)
		}
	}
	// With the agent it is scoped to behind the run, the scoped tool is in.
	if !machineRunCatalog(udb, "owner", "agent-x", def).names()["scoped"] {
		t.Error("a tool scoped to the agent behind the run is withheld from it")
	}
}

// A tool step naming a tool the run will not have is on the checklist, with
// the reason, before anything runs. A conversational machine is checked only
// for names that exist nowhere: its tool steps run with the hosting agent's
// catalog, where never-unattended means nothing.
func TestToolStepFindingsSayWhy(t *testing.T) {
	_, udb := machineCatalogWorld(t)
	steps := []MachinePhase{
		{Name: "a", Tool: "geo", Next: "b"},
		{Name: "b", Tool: "never", Next: "c"},
		{Name: "c", Tool: "ghost"},
	}
	got := strings.Join(toolStepFindings(udb, "owner", MachineDef{Name: "m", Unattended: true, Phases: steps}), "\n")
	for _, want := range []string{
		`step b calls "never", which is marked never to run unattended`,
		`step c calls "ghost", which is not a tool you have`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("findings lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "step a") {
		t.Errorf("a reachable tool was reported:\n%s", got)
	}
	conv := strings.Join(toolStepFindings(udb, "owner", MachineDef{Name: "m", Phases: steps}), "\n")
	if strings.Contains(conv, "step b") || !strings.Contains(conv, `step c calls "ghost"`) {
		t.Errorf("conversational findings:\n%s", conv)
	}
	if probs := machineRunProblems(udb, "owner", MachineDef{Name: "m", Unattended: true, Phases: steps}); len(probs) < 2 {
		t.Errorf("the run doors would start this machine: %v", probs)
	}
}

// A tool step whose templated argument comes out empty is skipped instead of
// calling the tool with a blank, and the run moves on. {input} reaches a tool
// step's arguments: it used to be empty in every unattended run. A required
// step with a blank argument fails instead of skipping.
func TestABlankToolArgumentSkipsTheStep(t *testing.T) {
	app, _ := machineCatalogWorld(t)
	var calls []string
	lookup := AgentToolDef{
		Tool: Tool{Name: "lookup", Parameters: map[string]ToolParam{"q": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			q, _ := args["q"].(string)
			calls = append(calls, q)
			return "found " + q, nil
		},
	}
	def := MachineDef{Name: "m", Start: "find", Unattended: true, Phases: []MachinePhase{
		{Name: "find", Tool: "lookup", Args: map[string]string{"q": "{input}"}, Next: "more"},
		{Name: "more", Tool: "lookup", Args: map[string]string{"q": "{state:find}"}},
	}}
	run := func(input string, d MachineDef) (string, *MachineCursor, error) {
		cur := &MachineCursor{}
		runner := app.unattendedHost(unattendedRun{User: "owner", Tools: []AgentToolDef{lookup}, Cursor: cur}).phaseRunner()
		_, out, err := app.RunUnattended(context.Background(), d, cur, MachineTurn{Input: input, User: "owner"}, runner, nil)
		return out, cur, err
	}

	out, _, err := run("Oslo", def)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "Oslo,found Oslo" || out != "found found Oslo" {
		t.Fatalf("calls %v, result %q; {input} has to reach the first step", calls, out)
	}

	calls = nil
	out, cur, err := run("", def)
	// Both steps skip, so the run ends on a step that did not apply: that is
	// a failure naming the step, not a success with nothing in it.
	if err == nil || !strings.Contains(err.Error(), "the last step, more, did not apply") {
		t.Fatalf("a run that ended on a skipped step reported %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("the tool was called with a blank: %v", calls)
	}
	if !cur.State["find"].Skipped || !cur.State["more"].Skipped || out != "" {
		t.Fatalf("state %+v, result %q; both steps skip on a blank", cur.State, out)
	}
	if !strings.Contains(cur.State["find"].SkipReason, "q came out empty") {
		t.Fatalf("skip reason = %q", cur.State["find"].SkipReason)
	}

	def.Phases[0].Required = true
	if _, _, err := run("", def); err == nil || !strings.Contains(err.Error(), "came out empty") {
		t.Fatalf("a required step with a blank argument ran or skipped: %v", err)
	}
}

// The per-tool never-unattended mark reaches the scheduled-agent gate too,
// for every agent at once.
func TestTheToolsOwnMarkStopsAScheduledRun(t *testing.T) {
	app, _ := machineCatalogWorld(t)
	g := app.newAutonomousGate("owner", "any-agent", nil)
	if g.allows("never") {
		t.Error("a tool marked never unattended is allowed on a scheduled run")
	}
	if !g.allows("geo") {
		t.Error("an unmarked tool is refused on a scheduled run")
	}
}
