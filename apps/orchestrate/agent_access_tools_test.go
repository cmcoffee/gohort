package orchestrate

// The page built to answer "what can this agent do" listed tools out of
// rec.AllowedTools, which is EMPTY on a default-pool agent because empty means
// "every catalog tool". So the commonest kind of agent showed no tools at all,
// on the one surface whose whole job is that question.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The regression, stated as the thing that was wrong: an agent with an empty
// allowlist has the WHOLE catalog, and must not read as having nothing.
func TestADefaultPoolAgentResolvesItsWholeCatalog(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a1", Name: "Wren", Owner: "alice", OrchestratorPrompt: "you are a helper"} // AllowedTools nil = default pool
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := app.resolvedAgentTools(context.Background(), udb, "alice", rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("a default-pool agent resolved to no tools, which is what the old AllowedTools read did")
	}
	// Sanity that these are real resolved names, not a placeholder.
	for _, r := range rows {
		if strings.TrimSpace(r.Name) == "" {
			t.Error("a resolved row has no name")
		}
	}
}

// EVERY loaded tool can be governed, framework ones included. The ask mark is
// keyed by name and the unattended policy by (agent, name), so neither wants a
// record behind it.
//
// This asserted the opposite until the marks moved off the tool record. That
// arrangement made web_search and browse_page - the searches, the browsing,
// the fetches - the only tools that could NOT be stopped on, which is exactly
// backwards from what somebody securing an agent wants.
func TestEveryLoadedToolCanBeGoverned(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "access_row_tool", CommandTemplate: "echo hi", ConfirmInChat: true}); err != nil {
		t.Fatal(err)
	}
	rec := AgentRecord{ID: "a2", Name: "Wren", Owner: "alice", OrchestratorPrompt: "you are a helper"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := app.resolvedAgentTools(context.Background(), udb, "alice", rec)
	if err != nil {
		t.Fatal(err)
	}
	var mine, framework int
	for _, r := range rows {
		if r.Name == "access_row_tool" {
			mine++
			if !r.Governable {
				t.Error("a tool in the owner's pool does not read as governable, so its controls are hidden")
			}
			if !r.Asks {
				t.Error("the ask-before-every-call flag did not reach the row")
			}
			if r.Origin != "your tools" {
				t.Errorf("origin should say where it came from, got %q", r.Origin)
			}
		}
		if r.Origin == "framework" {
			framework++
			if !r.Governable {
				t.Errorf("%q cannot be governed, so the tools most worth stopping on are the ones you cannot stop on", r.Name)
			}
		}
	}
	if mine == 0 {
		t.Error("the owner's own tool is missing from the agent's resolved catalog")
	}
	if framework == 0 {
		t.Error("no framework tools resolved, so the governable check proved nothing")
	}
}

// Governable rows sort first. They are what somebody opened this page to act
// on, and burying them under the framework catalog is how a control surface
// becomes a list nobody scrolls.
func TestTheRowsYouCanActOnComeFirst(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "zzz_last_alphabetically", CommandTemplate: "echo hi"}); err != nil {
		t.Fatal(err)
	}
	rec := AgentRecord{ID: "a3", Name: "Wren", Owner: "alice", OrchestratorPrompt: "you are a helper"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := app.resolvedAgentTools(context.Background(), udb, "alice", rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 || !rows[0].Governable {
		t.Fatalf("a tool the owner can act on is not first; got %+v", rows[0])
	}
	seenPlain := false
	for _, r := range rows {
		if !r.Governable {
			seenPlain = true
		} else if seenPlain {
			t.Errorf("%q is governable but sorts after a row that is not", r.Name)
		}
	}
}

// Both controls write through the SAME setters the Permissions page uses.
// Two surfaces storing one fact two ways is how they start disagreeing, and
// this pair has to agree: a tool set to ask here must read as asking there.
func TestTheAccessPageWritesThroughTheSameSetters(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "shared_row_tool", CommandTemplate: "curl x"}); err != nil {
		t.Fatal(err)
	}
	rec := AgentRecord{ID: "a9", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	post := func(body string) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPatch,
			"/api/agent-access/tool?agent=a9&name=shared_row_tool", strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleAgentAccessTool(w, asUser(r, "alice"))
		return w.Code
	}
	if code := post(`{"chat":"ask"}`); code != http.StatusNoContent {
		t.Fatalf("setting ask: %d", code)
	}
	// Scoped to THIS agent, so it holds here and does NOT become a fleet-wide
	// mark. A permission belongs to the principal: the same tool can want
	// supervision on one agent and not on another, and a per-agent decision
	// silently binding every agent is the narrowing nobody asked for.
	if !UserToolAsksInChat(AuthDB(), "alice", "a9", "shared_row_tool") {
		t.Error("the mark did not take on the agent it was set for")
	}
	if UserToolAsksInChat(AuthDB(), "alice", "some_other_agent", "shared_row_tool") {
		t.Error("a mark set on one agent bound another one too")
	}
	if code := post(`{"unattended":"block"}`); code != http.StatusNoContent {
		t.Fatalf("setting unattended: %d", code)
	}
	if row := rowByDetail(permRowsFor(t, app, "alice"), "Never unattended"); row == nil {
		t.Error("an unattended decision made here is not reviewable on the Permissions page")
	}
}

// A framework tool can be marked. It has no record, and it does not need one:
// the mark is about a NAME.
func TestAFrameworkToolCanBeSetToAsk(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a10", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPatch,
		"/api/agent-access/tool?agent=a10&name=web_search", strings.NewReader(`{"chat":"ask"}`))
	w := httptest.NewRecorder()
	app.handleAgentAccessTool(w, asUser(r, "alice"))
	if w.Code != http.StatusNoContent {
		t.Fatalf("marking a framework tool: %d %s", w.Code, w.Body.String())
	}
	if !UserToolAsksInChat(AuthDB(), "alice", "a10", "web_search") {
		t.Error("the mark did not stick, so the tool goes on running unprompted")
	}
}

// An unattended value the gate does not recognize is refused, not stored. A
// stored nonsense policy reads as SOME state on the page and behaves as none.
func TestAnUnknownUnattendedValueIsRefused(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a11", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPatch,
		"/api/agent-access/tool?agent=a11&name=web_search", strings.NewReader(`{"unattended":"sometimes"}`))
	w := httptest.NewRecorder()
	app.handleAgentAccessTool(w, asUser(r, "alice"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d %s", w.Code, w.Body.String())
	}
}

// A RowAction's PostTo placeholders are FIELD NAMES resolved against the row.
// One naming no field resolves to EMPTY rather than erroring, so the control
// renders, takes the click, and posts to a URL with no tool in it. The
// RowAction doc said "{row_key} substituted", which is not what the
// substituter does, and that is exactly how this shipped dead once.
func TestTheToolControlsPostToAFieldThatExists(t *testing.T) {
	src, err := os.ReadFile("page_agent_access.go")
	if err != nil {
		t.Fatal(err)
	}
	// The ASSIGNMENT, not the file: the comment above it names the wrong
	// placeholder on purpose, to say why it is wrong.
	var assign string
	for _, line := range strings.Split(string(src), "\n") {
		if strings.Contains(line, "toolWrite :=") {
			assign = line
		}
	}
	if assign == "" {
		t.Fatal("the tool write URL is gone; the controls have nowhere to post")
	}
	if strings.Contains(assign, "{row_key}") {
		t.Error("the write URL uses {row_key}, which names no field on the row and resolves to empty")
	}
	if !strings.Contains(assign, "name={name}") {
		t.Errorf("the write URL does not carry the tool name, so every row would post the same request: %s", strings.TrimSpace(assign))
	}
	// The placeholder has to match a field the rows actually carry.
	if !strings.Contains(mustRead(t, "agent_access_tools.go"), "`json:\"name\"`") {
		t.Error("rows do not expose a name field for the placeholder to resolve")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A tool the owner has and this agent does NOT is listed, reading off. Without
// it the page answers "what is on" and silently refuses "what is off", which
// is the half somebody tightening an agent is usually looking for.
func TestAToolThisAgentDoesNotLoadIsStillListedAsOff(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	for _, n := range []string{"loaded_tool", "not_loaded_tool"} {
		if err := AdminPersistTempTool(udb, "alice", TempTool{
			Name: n, CommandTemplate: "echo hi"}); err != nil {
			t.Fatal(err)
		}
	}
	// An allowlist naming one of them: the other exists but this agent has no
	// access to it.
	rec := AgentRecord{ID: "a20", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p",
		DisabledPersistentTools: []string{"not_loaded_tool"}}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := app.resolvedAgentTools(context.Background(), udb, "alice", rec)
	if err != nil {
		t.Fatal(err)
	}
	var off *accessToolRow
	for i := range rows {
		if rows[i].Name == "not_loaded_tool" {
			off = &rows[i]
		}
	}
	if off == nil {
		t.Fatal("a tool this agent does not load is missing, so the page cannot say what is off")
	}
	if off.Enabled {
		t.Error("a tool this agent does not load reads as enabled")
	}
	// On rows sort before off ones: what the agent HAS is the subject.
	seenOff := false
	for _, r := range rows {
		if !r.Enabled {
			seenOff = true
		} else if seenOff {
			t.Errorf("%q is loaded but sorts after one that is not", r.Name)
		}
	}
}

// A sub-agent runs with its parent's authority, so tightening the parent is
// worth nothing if something it owns is looser. The band says which.
func TestSubAgentsReportHowRestrictionsReachThem(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	parent := AgentRecord{ID: "p1", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}
	if _, err := saveAgent(udb, parent); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []AgentRecord{
		{ID: "s1", Name: "triage", Owner: "alice", OwnedBy: "p1", OrchestratorPrompt: "p"},
		{ID: "s2", Name: "digest", Owner: "alice", OwnedBy: "p1", OrchestratorPrompt: "p",
			WorkspaceNoNetwork: true},
		{ID: "x1", Name: "unrelated", Owner: "alice", OrchestratorPrompt: "p"},
	} {
		if _, err := saveAgent(udb, sub); err != nil {
			t.Fatal(err)
		}
	}
	rows := agentSubAgents(udb, "alice", parent)
	if len(rows) != 2 {
		t.Fatalf("want this agent's 2 sub-agents and nothing else, got %d: %+v", len(rows), rows)
	}
	byName := map[string]accessSubAgentRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if byName["triage"].Inherits != "lockstep" {
		t.Errorf("a plain sub-agent should read lockstep: %+v", byName["triage"])
	}
	// Tighter, not looser: lockstep means the parent's limits REACH it, not
	// that the two are identical. A child carrying its own cut is allowed and
	// is worth saying out loud.
	if byName["digest"].Inherits != "tighter" {
		t.Errorf("a sub-agent with a limit of its own should read tighter: %+v", byName["digest"])
	}
	if _, ok := byName["unrelated"]; ok {
		t.Error("an agent this one does not own was listed as its sub-agent")
	}
}

// The console is reached from the Permissions control and nowhere else. It
// used to hang off a button at the bottom of the EDITOR, which is the wrong
// place for a question you ask when you are not editing, and is what the
// console's own header complains about. Moving the page and leaving its
// entrance behind is how a surface ends up unfindable.
func TestTheConsoleIsReachedFromThePermissionsControl(t *testing.T) {
	chat, err := os.ReadFile("page_chat.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(chat), `URL: "orchestrate_secure_agent"`) {
		t.Error("the Permissions control has no way into the console")
	}
	// And the handler it names is actually registered, or the button logs to
	// the console and does nothing.
	assets, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(assets), "uiRegisterClientAction('orchestrate_secure_agent'") {
		t.Error("the client action the button names is not registered")
	}
	editor, err := os.ReadFile("page_agent.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(editor), "/access\"") {
		t.Error("the editor still links to the console, so there are two doors that will drift")
	}
}

// Behaviour and enforcement are two different acts and no longer share a
// screen. Rules are guidance the agent follows and can edit; a guardrail is a
// check that holds whether or not the model agrees. One Save over both made
// installing a check look like editing a preference.
func TestGuardrailsAreOnSecurityAndRulesStayInConfigure(t *testing.T) {
	page := mustRead(t, "page_agent_access.go")
	if !strings.Contains(page, `"only": "guardrails"`) {
		t.Error("the Guardrails tab does not ask for the enforced band, so it would render rules too")
	}
	if !strings.Contains(page, `Group: "Guardrails"`) {
		t.Error("guardrails have no tab of their own")
	}
	assets := mustRead(t, "assets/web_assets.html")
	// Configure -> Rules keeps the soft rules, which is the whole point of the
	// split: the agent's own guidance stays where the agent's behaviour is set.
	if !strings.Contains(assets, `uiRegisterClientAction('orchestrate_rules_modal'`) {
		t.Error("the Rules surface is gone, not split")
	}
	// The bands are gated at their ATTACHMENT, so every reference across them
	// keeps working and the band you cannot see keeps the state it loaded.
	for _, want := range []string{
		"var showRules = only !== 'guardrails';",
		"var showGuards = only !== 'rules';",
		"if (showGuards) m.body.appendChild(gWrap);",
	} {
		if !strings.Contains(assets, want) {
			t.Errorf("the band gating is missing %q", want)
		}
	}
	// A page section is not a dialog: close() must not delete the host.
	if !strings.Contains(assets, "close: function() {}") {
		t.Error("the hosted shell's close removes the section, leaving a hole where the surface was")
	}
}

// Elevating a rule is the moment somebody stops trusting the agent to comply
// and installs a check that does not care. In the rules-only view the enforced
// band is not on screen, so without a word the rule just vanishes, which reads
// as a delete.
func TestElevatingARuleSaysWhereItWent(t *testing.T) {
	assets := mustRead(t, "assets/web_assets.html")
	i := strings.Index(assets, "up.title = 'Elevate to an enforced guardrail';")
	if i < 0 {
		t.Fatal("the elevate control is gone")
	}
	body := assets[i : i+2600]
	if !strings.Contains(body, "if (showGuards) {") {
		t.Error("elevate behaves the same whether or not the band is visible")
	}
	if !strings.Contains(body, "Moved to enforced guardrails") {
		t.Error("elevating from the rules-only view says nothing, so the rule appears to be deleted")
	}
	if !strings.Contains(body, "Security") {
		t.Error("the message does not name where the rule landed")
	}
}

// One vocabulary. There were three sets of words for one decision, and a
// reader had to learn it three times and work out they were the same question
// about different things.
func TestEveryLadderSpeaksTheSameThreeWords(t *testing.T) {
	want := []string{"Always allow", "Ask first", "Never"}
	got := permissionLadder()
	if len(got) != 3 {
		t.Fatalf("the ladder is %d long", len(got))
	}
	for i, o := range got {
		if o.Label != want[i] {
			t.Errorf("segment %d is %q, want %q", i, o.Label, want[i])
		}
	}
	// The short form is the SAME words, one segment short - not a different
	// control. A row that cannot hold "Never" used to get a bare switch, which
	// asked the reader to recognise the decision in a second shape.
	short := permissionLadderNoNever()
	if len(short) != 2 || short[0].Label != want[0] || short[1].Label != want[1] {
		t.Errorf("the short ladder is not the same words: %+v", short)
	}
	// And the old vocabularies are gone from the surfaces that used them.
	for _, f := range []string{"page_agent_access.go", "page_chat.go"} {
		src := mustRead(t, f)
		for _, dead := range []string{`Label: "Needs approval"`, `Label: "Queues"`, `Label: "Runs"}`, `"Blocked", Value:`} {
			if strings.Contains(src, dead) {
				t.Errorf("%s still offers %s", f, dead)
			}
		}
	}
}

// In chat a tool either asks first or always runs. "Never" is not one of its
// states: a tool the agent must never call is not loaded at all, which is a
// different question and a different surface.
func TestTheInChatLadderRefusesNever(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{
		ID: "a30", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPatch,
		"/api/agent-access/tool?agent=a30&name=web_search", strings.NewReader(`{"chat":"block"}`))
	w := httptest.NewRecorder()
	app.handleAgentAccessTool(w, asUser(r, "alice"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for a state this ladder cannot hold, got %d %s", w.Code, w.Body.String())
	}
}

// Every switch on the Security page reads "on = allowed", including the ones
// whose stored field is negative. Mixed polarity is not a style question: it
// is the arrangement in which somebody turns a restriction ON by reaching for
// the switch that looks like every other switch.
func TestEveryToggleOnSecurityMeansAllowed(t *testing.T) {
	src := mustRead(t, "page_agent_access.go")
	// Each negative field is inverted where it is offered.
	for _, f := range []string{"workspace_no_network", "share_hold_cortex", "share_hold_reference", "share_no_uploads", "hidden"} {
		i := strings.Index(src, `Field: "`+f+`", Type: "toggle"`)
		if i < 0 {
			t.Errorf("%s is not offered as a toggle", f)
			continue
		}
		if !strings.Contains(src[i:i+120], "Invert: true") {
			t.Errorf("%s is stored negatively and shown as-is, so its switch means the opposite of the ones around it", f)
		}
	}
	// And the labels say the positive thing, or the inversion just moves the
	// confusion from the switch to the words beside it.
	for _, dead := range []string{"may not reach the network", "Keep its standing activity to yourself", "Hide from agent fleet"} {
		if strings.Contains(src, dead) {
			t.Errorf("a label still states the restriction: %q", dead)
		}
	}
	// share_memory_explicit is stored POSITIVELY and must not be inverted.
	i := strings.Index(src, `Field: "share_memory_explicit", Type: "toggle"`)
	if i < 0 {
		t.Fatal("share_memory_explicit is not offered")
	}
	if strings.Contains(src[i:i+120], "Invert: true") {
		t.Error("a positively-stored field was inverted, which flips it for every existing agent")
	}
}

// Which tools may run unwatched is the Unattended ladder, and nowhere else.
//
// The editor carried two checklists over exactly the fields that ladder
// writes. Two controls over one fact drift, and these could disagree in a way
// neither showed: a tool could sit on BOTH lists from the editor, which the
// ladder cannot produce because setting one policy clears the other.
func TestUnattendedPolicyHasOneControl(t *testing.T) {
	editor := mustRead(t, "page_agent.go")
	for _, f := range []string{"auto_approve_tools", "no_unattended_tools"} {
		if strings.Contains(editor, `Field: "`+f+`"`) {
			t.Errorf("the editor still offers %s beside the ladder that writes it", f)
		}
	}
	// And the ladder still writes both lists, or removing them lost the
	// setting rather than moving it.
	// Across the package: removeAutoApproveTool lives beside the permissions
	// handler rather than in tool_policy.go.
	pol := packageSource(t)
	for _, fn := range []string{"addAutoApproveTool", "addNoUnattendedTool", "removeAutoApproveTool", "removeNoUnattendedTool"} {
		if !strings.Contains(pol, "func "+fn) {
			t.Errorf("%s is gone, so the ladder cannot write what the checklists used to", fn)
		}
	}
	if !strings.Contains(mustRead(t, "tool_policy.go"), "case PolicyAllow:") {
		t.Error("the ladder no longer maps its three answers onto the two lists")
	}
}

// Limits are ceilings the FRAMEWORK keeps, which is the whole reason they are
// settings rather than prompt text: written into a prompt they become rules
// the model has to count for itself, and it miscounts.
func TestLimitsAreOnSecurityAndNotTheEditor(t *testing.T) {
	sec := mustRead(t, "page_agent_access.go")
	editor := mustRead(t, "page_agent.go")
	for _, f := range []string{"action_quotas", "daily_spend_usd", "allow_explorer", "explorer_hard_cap"} {
		if !strings.Contains(sec, `Field: "`+f+`"`) {
			t.Errorf("%s is offered nowhere", f)
		}
		if strings.Contains(editor, `Field: "`+f+`"`) {
			t.Errorf("the editor still offers %s, so there are two controls over one limit", f)
		}
	}
	// Their own tab: they are ceilings on HOW MUCH, where the ladders answer
	// WHETHER, so putting them on Tools would mean one tab asking two
	// questions of every row.
	if !strings.Contains(sec, `Group:    "Limits"`) {
		t.Error("the limits have no tab of their own")
	}
	// And they must survive a PATCH, or the panel saves nothing.
	for _, f := range []string{"action_quotas", "daily_spend_usd", "allow_explorer", "explorer_hard_cap"} {
		if !patchAgentFields[f] {
			t.Errorf("%s is on a panel but the PATCH allowlist drops it", f)
		}
	}
}
