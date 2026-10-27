package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// addToolTestSess builds the minimal ToolSession add_tool needs, plus a parent
// agent and a helper sub-agent, and leaves authoring focus pointing at the
// HELPER — reproducing the state after "author a parent, then create_agent two
// helpers for it", which is where the real-world mis-attach happened.
func addToolTestSess(t *testing.T) (*ToolSession, AgentRecord, AgentRecord) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	const sid = "sess-1"
	udb := agentUserDB(root, owner)
	if udb == nil {
		t.Fatal("agentUserDB nil")
	}
	parent, err := saveAgent(udb, AgentRecord{
		Name: "OSINT Parent", OrchestratorPrompt: "the intended target", Owner: owner,
	})
	if err != nil {
		t.Fatalf("saveAgent parent: %v", err)
	}
	helper, err := saveAgent(udb, AgentRecord{
		Name: "Helper Sub", OrchestratorPrompt: "a sub-agent built for the parent", Owner: owner,
	})
	if err != nil {
		t.Fatalf("saveAgent helper: %v", err)
	}
	// create_agent stamps focus onto whatever it just made — the helper.
	saveAuthoringInProgress(udb, sid, helper.ID)
	return &ToolSession{Username: owner, DB: udb, ChatSessionID: sid}, parent, helper
}

func shellToolArgs(extra map[string]any) map[string]any {
	// No script_body: these tests are about WHICH AGENT the tool lands on, and
	// shipping a script would drag in workspace minting for no added coverage.
	// The script_body path is exercised in the temptool package, which owns the
	// shared PrepareScriptBody seam.
	args := map[string]any{
		"name":             "sentiment_analyzer",
		"mode":             "shell",
		"description":      "analyze text",
		"command_template": "echo hi",
	}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

// TestAddToolExplicitAgentBeatsStolenFocus is the regression for the observed
// bug: with focus sitting on a helper sub-agent, naming the parent explicitly
// must attach there — not wherever focus happens to point.
func TestAddToolExplicitAgentBeatsStolenFocus(t *testing.T) {
	sess, parent, helper := addToolTestSess(t)

	out, err := addToolTool{}.RunWithSession(shellToolArgs(map[string]any{"agent": "OSINT Parent"}), sess)
	if err != nil {
		t.Fatalf("add_tool with explicit agent: %v (out=%s)", err, out)
	}

	gotParent, ok := loadAgent(sess.DB, parent.ID)
	if !ok {
		t.Fatal("loadAgent parent failed")
	}
	if !agentHasTool(sess, gotParent, "sentiment_analyzer") {
		t.Fatalf("tool did not land on the explicitly named agent %q; its tools=%v", parent.Name, toolNames(sess, gotParent))
	}
	gotHelper, ok := loadAgent(sess.DB, helper.ID)
	if !ok {
		t.Fatal("loadAgent helper failed")
	}
	if agentHasTool(sess, gotHelper, "sentiment_analyzer") {
		t.Fatal("tool ALSO landed on the focused helper — explicit agent must not fall through to focus")
	}
}

// TestAddToolResolvesAgentByID proves the explicit argument takes an id as well
// as a name (parity with agents run/run_tool, which document "Name or id").
func TestAddToolResolvesAgentByID(t *testing.T) {
	sess, parent, _ := addToolTestSess(t)

	if _, err := (addToolTool{}).RunWithSession(shellToolArgs(map[string]any{"agent": parent.ID}), sess); err != nil {
		t.Fatalf("add_tool by id: %v", err)
	}
	got, _ := loadAgent(sess.DB, parent.ID)
	if !agentHasTool(sess, got, "sentiment_analyzer") {
		t.Fatal("tool did not land on the agent named by id")
	}
}

// TestAddToolFallsBackToFocus keeps the ergonomic default honest: with no
// agent argument, focus still governs.
func TestAddToolFallsBackToFocus(t *testing.T) {
	sess, parent, helper := addToolTestSess(t)

	if _, err := (addToolTool{}).RunWithSession(shellToolArgs(nil), sess); err != nil {
		t.Fatalf("add_tool via focus: %v", err)
	}
	gotHelper, _ := loadAgent(sess.DB, helper.ID)
	if !agentHasTool(sess, gotHelper, "sentiment_analyzer") {
		t.Fatal("tool did not land on the focused agent when no agent argument was passed")
	}
	gotParent, _ := loadAgent(sess.DB, parent.ID)
	if agentHasTool(sess, gotParent, "sentiment_analyzer") {
		t.Fatal("tool leaked onto the unfocused parent")
	}
}

// TestAddToolUnknownAgentErrors — a typo'd name must fail loudly rather than
// silently falling back to focus and attaching somewhere unintended.
func TestAddToolUnknownAgentErrors(t *testing.T) {
	sess, _, helper := addToolTestSess(t)

	if _, err := (addToolTool{}).RunWithSession(shellToolArgs(map[string]any{"agent": "No Such Agent"}), sess); err == nil {
		t.Fatal("expected an error for an unknown agent name, got nil")
	}
	gotHelper, _ := loadAgent(sess.DB, helper.ID)
	if agentHasTool(sess, gotHelper, "sentiment_analyzer") {
		t.Fatal("unknown agent name fell back to focus and attached the tool — must error instead")
	}
}

// Flattened namespace: kit membership is store scope, not record copies.
func agentHasTool(sess *ToolSession, a AgentRecord, name string) bool {
	p, ok := UserToolByName(sess.DB, sess.Username, name)
	return ok && p.ScopedToAgent(a.ID)
}

func toolNames(sess *ToolSession, a AgentRecord) []string {
	var out []string
	for _, p := range AgentScopedTools(sess.DB, sess.Username, a.ID) {
		out = append(out, p.Tool.Name)
	}
	return out
}

// An allowlist is an INTERSECTION. A name in it that matches no tool
// removes nothing and adds nothing, so the agent saves clean and is
// simply missing a capability its author believes it has — and the next
// read strips the name and says so in a server log, which is the wrong
// audience. The author is right there in the reply, still able to fix it.
func TestAToolNameThatMatchesNothingIsReportedToItsAuthor(t *testing.T) {
	sess, _, _ := addToolTestSess(t)

	// The resolvable name is a prefix-rule one (from_client.*, call_*),
	// because the chat-tool registry is populated when the server starts
	// and not in a unit test — the same reason selfHealAllowedTools,
	// which uses this predicate to DELETE names, only ever runs behind a
	// live registry.
	note := unresolvedToolNote(sess.DB, AgentRecord{
		Owner:        sess.Username,
		AllowedTools: []string{"call_github", "search_the_moon", "also_not_real"},
	})
	if note == "" {
		t.Fatal("two names match nothing and the author was told nothing")
	}
	for _, want := range []string{`"search_the_moon"`, `"also_not_real"`, "intersection"} {
		if !strings.Contains(note, want) {
			t.Errorf("the warning should carry %s:\n%s", want, note)
		}
	}
	// The real one is not accused.
	if strings.Contains(note, `"call_github"`) {
		t.Errorf("a resolvable tool should not be named:\n%s", note)
	}
	// And it says what to do, since "this is wrong" without a next step
	// is how a model reports success anyway.
	if !strings.Contains(note, "author the tool first") {
		t.Errorf("the warning should name the fix:\n%s", note)
	}

	// The sentinels are not tool names and must not be accused.
	for _, sentinel := range [][]string{{"*"}, {noToolsSentinel}} {
		if got := unresolvedToolNote(sess.DB, AgentRecord{
			Owner: sess.Username, AllowedTools: sentinel}); got != "" {
			t.Errorf("%v is a marker, not a tool: %s", sentinel, got)
		}
	}
	// An empty allowlist is the default pool, not a mistake.
	if got := unresolvedToolNote(sess.DB, AgentRecord{Owner: sess.Username}); got != "" {
		t.Errorf("an empty allowlist means everything: %s", got)
	}
	// And an agent whose names all resolve gets no note at all — a
	// warning that appears every time is one nobody reads.
	if got := unresolvedToolNote(sess.DB, AgentRecord{
		Owner: sess.Username, AllowedTools: []string{"call_github", "from_client.scan"}}); got != "" {
		t.Errorf("nothing to warn about:\n%s", got)
	}
}

// TestAddToolRefusesDefaultAPIScript: add_tool goes through the same guard as
// tool_def. Builder shipped a script importing default_api through add_tool,
// which accepted it and failed only at the verification run, then rewrote
// the same import twice more against the traceback.
func TestAddToolRefusesDefaultAPIScript(t *testing.T) {
	sess, parent, _ := addToolTestSess(t)
	_, err := (addToolTool{}).RunWithSession(shellToolArgs(map[string]any{
		"agent":       parent.Name,
		"name":        "post_to_board",
		"script_body": "import os\nfrom default_api import bulletins\nprint(bulletins(action='create', name='x', content=os.environ['news']))\n",
		"script_name": "post.py",
	}), sess)
	if err == nil || !strings.Contains(err.Error(), "default_api") || !strings.Contains(err.Error(), "allow_poster") {
		t.Fatalf("expected the default_api refusal naming allow_poster, got: %v", err)
	}
}

// TestAgentEchoJSONHidesOwnerOnlyState — the "Saved record:" blob create/update/
// clone hand back must carry only what the CALL could have written.
//
// It used to marshal the whole AgentRecord. An authoring model then read a
// per-agent credential opt-out that predated the credential being secured,
// could not tell stale from current (nothing on the record says which), and
// reported it to the user as a live misconfiguration in the agent it had just
// edited.
func TestAgentEchoJSONHidesOwnerOnlyState(t *testing.T) {
	rec := AgentRecord{
		ID:                  "abc",
		Name:                "TechGuru",
		Description:         "expert",
		AllowedTools:        []string{"web_search"},
		Triggers:            []string{"acme"},
		DisabledCredentials: []string{"gitlab"},
		DisabledPipelines:   []string{"p1"},
		GuardrailFailClosed: true,
		Machine:             "m-1",
		Owner:               "alice",
		Exposed:             true,
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(agentEchoJSON(rec)), &got); err != nil {
		t.Fatalf("echo is not valid JSON: %v", err)
	}
	for _, k := range []string{"disabled_credentials", "disabled_pipelines", "guardrail_fail_closed", "machine", "owner", "exposed"} {
		if _, ok := got[k]; ok {
			t.Errorf("owner-only field %q must not be echoed to the authoring model", k)
		}
	}
	for _, k := range []string{"id", "name", "description", "allowed_tools", "triggers"} {
		if _, ok := got[k]; !ok {
			t.Errorf("writable field %q must be echoed so the model can confirm what stuck", k)
		}
	}
}

// The live failure this exists for: an agent could not reach knowledge_search,
// its owner added the name to allowed_tools, and the save answered "match no
// known tool and were dropped — the agent will NOT have them". Both halves
// misled. The name is framework-injected, so adding it was a no-op rather than
// a fix, and the real cause was elsewhere.
func TestAFrameworkInjectedNameIsNotReportedAsATypo(t *testing.T) {
	rec := &AgentRecord{AllowedTools: []string{"knowledge_search", "fetch_knowledge_doc"}}
	msg := unresolvedToolsWarning(nil, rec)

	if strings.Contains(msg, "were dropped") || strings.Contains(msg, "will NOT have them") {
		t.Errorf("a framework tool must not be reported as a dropped typo: %q", msg)
	}
	if !strings.Contains(msg, "provided by the framework") {
		t.Errorf("say where it actually comes from: %q", msg)
	}
	// The part that would have saved the session: point at the narrowing.
	if !strings.Contains(msg, "machine phase") {
		t.Errorf("an unreachable framework tool is usually a phase narrowing; say so: %q", msg)
	}
	if !strings.Contains(msg, "are provided") {
		t.Errorf("two names should agree the verb: %q", msg)
	}
}

// A genuine typo must still be reported as one, or the fix above has traded a
// misleading warning for a missing one.
func TestARealTypoIsStillReportedAsDropped(t *testing.T) {
	rec := &AgentRecord{AllowedTools: []string{"knowledge_search", "kw_logtrceee"}}
	msg := unresolvedToolsWarning(nil, rec)
	if !strings.Contains(msg, "kw_logtrceee") || !strings.Contains(msg, "were dropped") {
		t.Errorf("a name nothing answers to is still a typo: %q", msg)
	}
	if !strings.Contains(msg, "provided by the framework") {
		t.Errorf("and the framework note should still appear alongside it: %q", msg)
	}
}

func TestOneFrameworkNameAgreesTheVerb(t *testing.T) {
	rec := &AgentRecord{AllowedTools: []string{"knowledge_search"}}
	if msg := unresolvedToolsWarning(nil, rec); !strings.Contains(msg, "is provided") {
		t.Errorf("one name takes a singular verb: %q", msg)
	}
}

// Effort is the everyday reasoning control on an agent. The CRUD tools accept
// it, it takes precedence over the legacy think on/off/auto, and every run
// path reads the agent's think mode through it.

func TestCreateAgentAcceptsEffort(t *testing.T) {
	rec := agentRecordFromArgs(map[string]any{
		"name": "Fresh", "orchestrator_prompt": "p", "effort": "High", "think": "off",
	})
	if rec.Effort != "high" {
		t.Errorf("effort = %q, want high", rec.Effort)
	}
	// Effort wins over the think param passed beside it.
	if rec.Think != "on" || rec.thinkMode() != "on" {
		t.Errorf("think = %q (mode %q): a level above off means reasoning on", rec.Think, rec.thinkMode())
	}
	off := agentRecordFromArgs(map[string]any{"name": "Quiet", "orchestrator_prompt": "p", "effort": "off"})
	if off.Effort != "off" || off.Think != "off" {
		t.Errorf("effort off: effort=%q think=%q", off.Effort, off.Think)
	}
	// Not passed: nothing changes from the old behaviour.
	plain := agentRecordFromArgs(map[string]any{"name": "Plain", "orchestrator_prompt": "p"})
	if plain.Effort != "" || plain.Think != "on" {
		t.Errorf("no effort: effort=%q think=%q", plain.Effort, plain.Think)
	}
}

func TestUpdateAgentAcceptsEffort(t *testing.T) {
	rec := AgentRecord{ID: "a", Think: "off"}
	mergeAgentArgs(&rec, map[string]any{"effort": "medium"})
	if rec.Effort != "medium" || rec.Think != "on" {
		t.Errorf("effort=%q think=%q, want medium / on", rec.Effort, rec.Think)
	}
	// Omitted keeps the stored value; a typo does too.
	mergeAgentArgs(&rec, map[string]any{"name": "renamed"})
	mergeAgentArgs(&rec, map[string]any{"effort": "extreme"})
	if rec.Effort != "medium" {
		t.Errorf("an omitted or invalid effort changed the stored value to %q", rec.Effort)
	}
	// think passed beside effort: effort decides.
	mergeAgentArgs(&rec, map[string]any{"think": "off", "effort": "low"})
	if rec.Think != "on" || rec.Effort != "low" {
		t.Errorf("think=%q effort=%q, want effort to take precedence", rec.Think, rec.Effort)
	}
	// "default" clears it on purpose.
	mergeAgentArgs(&rec, map[string]any{"effort": "default"})
	if rec.Effort != "" {
		t.Errorf("default did not clear effort: %q", rec.Effort)
	}
}

// An agent edited in the form can hold Think "off" with an effort level; the
// run paths read thinkMode, so the level still decides.
func TestEffortDecidesTheThinkMode(t *testing.T) {
	for _, c := range []struct{ think, effort, want string }{
		{"off", "high", "on"},
		{"on", "off", "off"},
		{"", "low", "on"},
		{"off", "", "off"},
		{"", "", ""},
	} {
		if got := (AgentRecord{Think: c.think, Effort: c.effort}).thinkMode(); got != c.want {
			t.Errorf("think=%q effort=%q: mode %q, want %q", c.think, c.effort, got, c.want)
		}
	}
	if !resolveDispatchThink(AgentRecord{Think: "off", Effort: "medium"}) {
		t.Error("a dispatched agent at effort medium did not reason")
	}
}

func TestEffortIsOnEveryAgentSurface(t *testing.T) {
	if _, ok := agentMutationParams(false)["effort"]; !ok {
		t.Error("create_agent has no effort param")
	}
	if _, ok := agentMutationParams(true)["effort"]; !ok {
		t.Error("update_agent has no effort param")
	}
	if !patchAgentFields["effort"] {
		t.Error("the agent editor's partial save drops effort")
	}
}

// Regressions from an observed Builder session: update_agent called with no id
// was told agent "<nil>" did not exist, a call that re-sent only the agent's
// existing name reported AGENT_UPDATED ok and ordered the turn to end, and the
// model then told the user an edit had landed that it never sent.

func TestAMissingIdSaysSoInsteadOfLookingUpNil(t *testing.T) {
	sess, _, _ := addToolTestSess(t)
	for _, tool := range []interface {
		RunWithSession(map[string]any, *ToolSession) (string, error)
	}{updateAgentTool{}, cloneAgentTool{}, deleteAgentTool{}} {
		_, err := tool.RunWithSession(map[string]any{"description": "x"}, sess)
		if err == nil {
			t.Fatalf("%T with no id succeeded", tool)
		}
		if strings.Contains(err.Error(), "<nil>") || !strings.Contains(err.Error(), "needs id") {
			t.Errorf("%T: %v", tool, err)
		}
	}
}

// agents() and add_tool call the target "agent"; update_agent calls it "id".
// The model carries one into the other, so both are read.
func TestUpdateAgentTakesAgentAsWellAsId(t *testing.T) {
	sess, parent, _ := addToolTestSess(t)
	out, err := updateAgentTool{}.RunWithSession(map[string]any{"agent": parent.Name, "description": "now covers more"}, sess)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out, "Changed: description") {
		t.Errorf("the result should name what changed:\n%s", out)
	}
	if got, _ := loadAgent(sess.DB, parent.ID); got.Description != "now covers more" {
		t.Errorf("description = %q", got.Description)
	}
}

func TestAnUpdateThatChangesNothingSaysSo(t *testing.T) {
	sess, parent, _ := addToolTestSess(t)
	before, _ := loadAgent(sess.DB, parent.ID)
	out, err := updateAgentTool{}.RunWithSession(map[string]any{"id": parent.ID, "name": parent.Name}, sess)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out, "NOTHING CHANGED") || strings.Contains(out, "END THE TURN") {
		t.Errorf("a no-op update must say nothing changed and must not end the turn:\n%s", out)
	}
	if !strings.Contains(out, "name") {
		t.Errorf("it should say which fields the call set:\n%s", out)
	}
	if after, _ := loadAgent(sess.DB, parent.ID); !after.Updated.Equal(before.Updated) {
		t.Error("a no-op update was saved anyway")
	}
}

func TestACloneWithNoNameIsNotNamedNil(t *testing.T) {
	sess, parent, _ := addToolTestSess(t)
	out, err := cloneAgentTool{}.RunWithSession(map[string]any{"id": parent.ID}, sess)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if strings.Contains(out, "<nil>") {
		t.Errorf("the clone was named after a missing argument:\n%s", out)
	}
}

// The "*" sentinel means "every tool". Downstream readers spell that as
// an EMPTY allowlist, so "*" has to collapse to nil on every write path.
//
// Regression: update_agent stored a literal ["*"], which is a strict
// allowlist of length one naming a tool that cannot exist. The agent came
// up with ZERO tools — the exact inverse of the request — and
// unresolvedAllowedTools skips "*" when hunting for typos, so nothing
// warned. The call reported "AGENT_UPDATED ok".
func TestNormalizeAllowedToolsWildcard(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"bare wildcard", []string{"*"}, nil},
		{"padded wildcard", []string{" * "}, nil},
		{"wildcard among names", []string{"web_search", "*"}, nil},
		{"explicit list untouched", []string{"web_search", "fetch_url"}, []string{"web_search", "fetch_url"}},
		{"none sentinel untouched", []string{noToolsSentinel}, []string{noToolsSentinel}},
		{"empty stays empty", []string{}, []string{}},
		{"nil stays nil", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeAllowedTools(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("normalizeAllowedTools(%q) = %q, want %q", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("normalizeAllowedTools(%q) = %q, want %q", c.in, got, c.want)
				}
			}
		})
	}
}

// An "everything" surface must read as everything to the code that
// decides what an agent can reach. This pins the contract the wildcard
// collapse depends on: empty means the default pool, and a one-element
// allowlist does NOT.
func TestEverythingSurfaceIsEmptyAllowlist(t *testing.T) {
	rec := &AgentRecord{AllowedTools: normalizeAllowedTools([]string{"*"})}
	if len(rec.AllowedTools) != 0 {
		t.Fatalf("wildcard did not collapse: %q", rec.AllowedTools)
	}
	// A literal wildcard left in place would have been a strict allowlist
	// that resolves to nothing, and would not even be reported as a typo.
	if missing := unresolvedAllowedTools(nil, &AgentRecord{AllowedTools: []string{"*"}}); len(missing) != 0 {
		t.Fatalf("unresolvedAllowedTools flagged %q — it deliberately skips the sentinel, which is why an unnormalized wildcard failed silently", missing)
	}
}

// The transcript case: a standing agent was bound to an agent whose
// allowlist is the "none" sentinel, for a mission that needed to fetch
// and send. It scheduled fine and could never do the job.
func TestAgentHasNoTools(t *testing.T) {
	cases := []struct {
		name string
		rec  AgentRecord
		want bool
	}{
		{"none sentinel", AgentRecord{AllowedTools: []string{noToolsSentinel}}, true},
		{"padded sentinel", AgentRecord{AllowedTools: []string{" " + noToolsSentinel + " "}}, true},
		// Empty means the DEFAULT POOL, the opposite of none. Confusing
		// these would warn on every fully-capable agent.
		{"empty is everything", AgentRecord{AllowedTools: nil}, false},
		{"explicit list", AgentRecord{AllowedTools: []string{"web_search"}}, false},
		{"sentinel among names", AgentRecord{AllowedTools: []string{noToolsSentinel, "web_search"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := agentHasNoTools(c.rec); got != c.want {
				t.Errorf("agentHasNoTools(%q) = %v, want %v", c.rec.AllowedTools, got, c.want)
			}
		})
	}
}
