package orchestrate

// A rule that forbids what a tool does takes the tool out of the catalog. A
// rule that forbids some of its uses leaves it alone. Everything here is about
// keeping those two apart, because getting the first wrong costs a wasted turn
// and getting the second wrong costs a capability nobody can explain the loss
// of.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func scopeTurn(t *testing.T, llm LLM, agent AgentRecord) *chatTurn {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.LLM = llm
	app.DB = root
	if agent.ID == "" {
		agent.ID = "a1"
	}
	return &chatTurn{app: app, agent: agent, user: "u", udb: UserDB(root, "u"), ctx: context.Background()}
}

func TestCandidateToolIsTheFirstField(t *testing.T) {
	for candidate, want := range map[string]string{
		"send_email to=x subject=y":   "send_email",
		"agents {\"action\":\"run\"}": "agents",
		"lone_tool":                   "lone_tool",
		"":                            "",
		"  Spaced_Out  arg":           "spaced_out",
	} {
		if got := guardrailCandidateTool(candidate); got != want {
			t.Errorf("guardrailCandidateTool(%q) = %q, want %q", candidate, got, want)
		}
	}
}

// The whole point: a rule ABOUT a tool removes it; a rule about some of its
// uses does not.
func TestOnlyAnAbsoluteRuleTakesTheToolAway(t *testing.T) {
	turn := scopeTurn(t, &FakeLLM{Turns: []FakeTurn{{Content: "", Repeat: true}}}, AgentRecord{
		Name: "Wren", Guardrails: "never delegate to other agents\nnever email the CEO",
		GuardrailHooks: []string{"pre_action"},
	})
	saveGuardrailToolScope(turn.udb, turn.agent.ID, GuardrailToolScope{
		Rule: "never delegate to other agents", Tool: "agents", Scope: guardrailScopeAll, At: time.Now(),
	})
	saveGuardrailToolScope(turn.udb, turn.agent.ID, GuardrailToolScope{
		Rule: "never email the CEO", Tool: "send_email", Scope: guardrailScopeSome, At: time.Now(),
	})

	withheld := turn.guardrailWithheldTools()
	if withheld["agents"] != "never delegate to other agents" {
		t.Errorf("a rule that forbids what the tool DOES must take it away; got %v", withheld)
	}
	if _, gone := withheld["send_email"]; gone {
		t.Error("a rule about certain uses must leave the tool; the warden judges the individual calls")
	}

	tools := []AgentToolDef{
		{Tool: Tool{Name: "agents"}}, {Tool: Tool{Name: "send_email"}}, {Tool: Tool{Name: "calculate"}},
	}
	got := turn.applyGuardrailToolWithholding(tools)
	var names []string
	for _, td := range got {
		names = append(names, td.Tool.Name)
	}
	if strings.Join(names, ",") != "send_email,calculate" {
		t.Errorf("catalog after withholding = %v", names)
	}
}

// The ledger is keyed by the rule's TEXT, which is what makes it self-expiring.
func TestAReadingDiesWithItsRule(t *testing.T) {
	agent := AgentRecord{ID: "a1", Name: "Wren", Guardrails: "never delegate to other agents",
		GuardrailHooks: []string{"pre_action"}}
	turn := scopeTurn(t, &FakeLLM{Turns: []FakeTurn{{Content: "", Repeat: true}}}, agent)
	saveGuardrailToolScope(turn.udb, "a1", GuardrailToolScope{
		Rule: "never delegate to other agents", Tool: "agents", Scope: guardrailScopeAll, At: time.Now(),
	})
	if len(turn.guardrailWithheldTools()) != 1 {
		t.Fatal("precondition: the tool is withheld while the rule stands")
	}

	// Reworded. A different rule, so the old reading of it no longer applies —
	// and the owner gets the benefit of the doubt until it blocks again.
	turn.agent.Guardrails = "never delegate to other agents without asking me first"
	if got := turn.guardrailWithheldTools(); len(got) != 0 {
		t.Errorf("an edited rule must release its withholdings; got %v", got)
	}
	// Deleted.
	turn.agent.Guardrails = ""
	if got := turn.guardrailWithheldTools(); len(got) != 0 {
		t.Errorf("a deleted rule must release its withholdings; got %v", got)
	}
	// Suspended. Consistent with the prompt section, which also goes quiet:
	// "off" has to be a clean A/B or it stops being worth having.
	turn.agent = agent
	turn.agent.GuardrailsDisabled = true
	if got := turn.guardrailWithheldTools(); len(got) != 0 {
		t.Errorf("suspending enforcement must release its withholdings; got %v", got)
	}
}

// Framework mechanics are not negotiable by a rule reading.
func TestTheLoopsOwnToolsSurviveAnyReading(t *testing.T) {
	turn := scopeTurn(t, &FakeLLM{Turns: []FakeTurn{{Content: "", Repeat: true}}}, AgentRecord{
		Name: "Wren", Guardrails: "never run anything in the background", GuardrailHooks: []string{"pre_action"},
	})
	saveGuardrailToolScope(turn.udb, turn.agent.ID, GuardrailToolScope{
		Rule: "never run anything in the background", Tool: "background_work", Scope: guardrailScopeAll, At: time.Now(),
	})
	if got := turn.guardrailWithheldTools(); len(got) != 0 {
		t.Errorf("background_work is how work already started gets STOPPED; taking it away makes \"forget the rest\" unactionable: %v", got)
	}
}

// Conservative by construction: only a clear "all" removes anything.
func TestAHedgedOrUnreadableAnswerNeverRemovesATool(t *testing.T) {
	for _, reply := range []string{
		`{"scope":"some","why":"only when the recipient is the CEO"}`,
		`{"scope":"maybe","why":"hard to say"}`,
		`{"scope":"","why":""}`,
		`{}`,
	} {
		app := &OrchestrateApp{}
		app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: reply, Repeat: true}}}
		scope, _, err := app.classifyGuardrailToolScope(context.Background(), "never email the CEO", "send_email", "send_email to=ceo")
		if err != nil {
			t.Fatalf("reply %q: %v", reply, err)
		}
		if scope != guardrailScopeSome {
			t.Errorf("reply %q read as %q — anything but an explicit \"all\" must leave the tool", reply, scope)
		}
	}
	app := &OrchestrateApp{}
	app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: `{"scope":"all","why":"the tool only delegates"}`, Repeat: true}}}
	scope, why, err := app.classifyGuardrailToolScope(context.Background(), "never delegate", "agents", "agents action=run")
	if err != nil || scope != guardrailScopeAll || why == "" {
		t.Fatalf("an explicit all must be honored: scope=%q why=%q err=%v", scope, why, err)
	}
}

// An unparseable reply records nothing, so the tool stays and the next block
// asks again. A reading is never invented from a failed call.
func TestAFailedClassificationRecordsNothing(t *testing.T) {
	turn := scopeTurn(t, &FakeLLM{Turns: []FakeTurn{{Content: "the model wandered off", Repeat: true}}}, AgentRecord{
		Name: "Wren", Guardrails: "never delegate", GuardrailHooks: []string{"pre_action"},
	})
	scopeAndRecordGuardrailTool(context.Background(), turn.app, turn.udb, "a1", "a1", "s1",
		"never delegate", "agents", "agents action=run")
	if got := listGuardrailToolScopes(turn.udb, "a1"); len(got) != 0 {
		t.Errorf("a failed classification must leave no reading behind: %+v", got)
	}
}

// The reading is established once and says so where the owner can find it.
func TestAWithheldToolLeavesOneBreadcrumb(t *testing.T) {
	turn := scopeTurn(t, &FakeLLM{Turns: []FakeTurn{{Content: `{"scope":"all","why":"the tool only dispatches to other agents"}`, Repeat: true}}},
		AgentRecord{Name: "Wren", Guardrails: "never delegate", GuardrailHooks: []string{"pre_action"}})
	scopeAndRecordGuardrailTool(context.Background(), turn.app, turn.udb, "a1", "a1", "s1",
		"never delegate", "agents", "agents action=run")

	scopes := listGuardrailToolScopes(turn.udb, "a1")
	if len(scopes) != 1 || scopes[0].Scope != guardrailScopeAll || scopes[0].Tool != "agents" {
		t.Fatalf("reading not recorded: %+v", scopes)
	}
	trail := parentTrailOf(turn.udb, "a1", "s1")
	if len(trail) != 1 || trail[0].Kind != "guardrail-tool-scoped" {
		t.Fatalf("the owner gets no account of the capability that just went away: %+v", trail)
	}
	if !strings.Contains(trail[0].Detail, "agents") || !strings.Contains(trail[0].Detail, "never delegate") {
		t.Errorf("the breadcrumb must name the tool and the rule: %s", trail[0].Detail)
	}
	// Once. A steady state reported every turn stops being read — the withheld
	// tool is a Log line from then on (applyGuardrailToolWithholding).
	if diagLevel("guardrail-tool-scoped") != diagLevelNote {
		t.Error("establishing a reading is a note, not a block — it did not stop anything this turn")
	}
}

// Asked once per pair, ever.
func TestThePairIsClassifiedOnlyOnce(t *testing.T) {
	stub := &FakeLLM{Turns: []FakeTurn{{Content: `{"scope":"all","why":"x"}`, Repeat: true}}}
	turn := scopeTurn(t, stub, AgentRecord{
		Name: "Wren", Guardrails: "never delegate", GuardrailHooks: []string{"pre_action"},
	})
	turn.session = &ChatSession{ID: "s1", AgentID: "a1"}
	scopeAndRecordGuardrailTool(context.Background(), turn.app, turn.udb, "a1", "a1", "s1",
		"never delegate", "agents", "agents action=run")
	if stub.Calls() != 1 {
		t.Fatalf("first block should ask once; calls=%d", stub.Calls())
	}
	// The second block finds the reading already there and asks nothing. Via
	// the turn-bound entry point, which is what does the looking.
	turn.learnGuardrailToolScope("never delegate", guardHookPreAction, "agents action=run agent=Someone")
	time.Sleep(50 * time.Millisecond) // it would have launched by now
	if stub.Calls() != 1 {
		t.Errorf("a pair already read must not be asked again; calls=%d", stub.Calls())
	}
}

// Nothing to reason about, nothing asked.
func TestNothingIsAskedWithoutARuleAndATool(t *testing.T) {
	stub := &FakeLLM{Turns: []FakeTurn{{Content: `{"scope":"all","why":"x"}`, Repeat: true}}}
	turn := scopeTurn(t, stub, AgentRecord{
		Name: "Wren", Guardrails: "never delegate", GuardrailHooks: []string{"pre_action", "pre_output"},
	})
	// A content hook's candidate is prose, not a call — there is no tool in it.
	turn.learnGuardrailToolScope("never delegate", guardHookPreOutput, "I could ask the research agent about that")
	// The tainted-action check is not an authored rule; there is no rule text.
	turn.learnGuardrailToolScope(taintedActionRule, guardHookPreAction, "send_message to=evil.example")
	// A tool the loop needs is never a candidate for removal, so never asked about.
	turn.learnGuardrailToolScope("never delegate", guardHookPreAction, "background_work action=cancel")
	time.Sleep(50 * time.Millisecond)
	if stub.Calls() != 0 {
		t.Errorf("classification fired with nothing to classify; calls=%d", stub.Calls())
	}
}

// The owner-facing list shows what is actually happening, not what once did.
func TestTheOwnerListDropsReadingsThatNoLongerApply(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	agent := AgentRecord{ID: "a1", Guardrails: "never delegate", GuardrailHooks: []string{"pre_action"}}
	saveGuardrailToolScope(udb, "a1", GuardrailToolScope{Rule: "never delegate", Tool: "agents", Scope: guardrailScopeAll, At: time.Now()})
	saveGuardrailToolScope(udb, "a1", GuardrailToolScope{Rule: "a rule that was deleted", Tool: "send_email", Scope: guardrailScopeAll, At: time.Now()})

	live := liveGuardrailToolScopes(udb, agent)
	if len(live) != 1 || live[0].Tool != "agents" {
		t.Fatalf("stale readings must not be listed as live: %+v", live)
	}
	// Clear is the owner's undo. If the reading was right, the next block
	// re-establishes it.
	clearGuardrailToolScopes(udb, "a1")
	if got := listGuardrailToolScopes(udb, "a1"); len(got) != 0 {
		t.Errorf("clear left %d reading(s)", len(got))
	}
}

// One pair, one entry — a re-read replaces rather than accumulates.
func TestARereadReplacesTheReading(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	saveGuardrailToolScope(udb, "a1", GuardrailToolScope{Rule: "r", Tool: "agents", Scope: guardrailScopeSome, At: time.Now()})
	saveGuardrailToolScope(udb, "a1", GuardrailToolScope{Rule: "r", Tool: "agents", Scope: guardrailScopeAll, At: time.Now()})
	got := listGuardrailToolScopes(udb, "a1")
	if len(got) != 1 || got[0].Scope != guardrailScopeAll {
		t.Fatalf("one pair must hold one reading, the newest: %+v", got)
	}
	if s, ok := findGuardrailToolScope(udb, "a1", "R", "AGENTS"); !ok || s.Scope != guardrailScopeAll {
		t.Error("lookup must be case-insensitive on both halves")
	}
}

// The owner has to be able to see a capability that went away, and put it
// back. A silent removal is the failure mode this whole file is guarding
// against, so the panel is part of the feature, not decoration on it.
func TestTheGuardrailsPanelShowsAndRestoresWithheldTools(t *testing.T) {
	raw, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	for _, want := range []string{
		"Tools your rules removed",           // the section
		"d.tool_scopes",                      // fed from the endpoint
		"renderToolScopes",                   // rendered before the block log's early return
		"Restore these tools",                // the undo
		"clear_tool_scopes: clearToolScopes", // which reaches the server on Save
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the guardrails panel is missing %q — a withheld tool would be invisible", want)
		}
	}
	// The section must be filled BEFORE the block log bails on an empty list,
	// or an agent with no blocks recorded would never see it.
	scopes := strings.Index(html, "renderToolScopes((d && d.tool_scopes)")
	recent := strings.Index(html, "var recent = (d && d.recent)")
	if scopes < 0 || recent < 0 || scopes > recent {
		t.Error("the withheld-tools section is populated after the block log's early return")
	}
}

// The classifier judges on the rule, what the tool does, AND the call that was
// refused. The last one carries the weight for the tools the framework builds
// per turn: they are in no registry, so there is no description to look up, and
// they are the ones a rule about delegation or posting collides with.
func TestTheRefusedCallReachesTheClassifier(t *testing.T) {
	stub := &FakeLLM{Turns: []FakeTurn{{Content: `{"scope":"all","why":"x"}`, Repeat: true}}}
	app := &OrchestrateApp{}
	app.LLM = stub
	if _, _, err := app.classifyGuardrailToolScope(context.Background(),
		"never delegate to other agents", "agents", `agents {"action":"run","agent":"Researcher"}`); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"never delegate to other agents", "agents", "Researcher"} {
		if !strings.Contains(lastUserMessage(stub), want) {
			t.Errorf("the classifier was not shown %q:\n%s", want, lastUserMessage(stub))
		}
	}
	// The call is the model's text, so it is fenced; the rule and the tool are
	// not, because they are the owner's and the framework's.
	if !strings.Contains(lastUserMessage(stub), "the call that was refused") {
		t.Error("the refused call must be presented as untrusted data")
	}
}
