// The gap between what a rule forbids and what the catalog offers.
//
// An agent's rules reach it as prose — "## Enforced limits" in the system
// prompt (renderGuardrailsPromptSection). Its capabilities reach it as tool
// schemas. When the two disagree, the schema wins: it is concrete, it is
// adjacent to the decision, and it describes an action in the terms the model
// is choosing between. So an agent told "never delegate" and handed an
// `agents` tool that says "delegate work and get the result back" reaches for
// the tool, gets blocked, rephrases, gets blocked again — the prose never had
// a chance against the schema.
//
// Closing it means the rule has to reach the catalog, not just the prompt.
// This file does that from the one source of truth that cannot be guessed at:
// the warden's own verdicts. A pre_action block is the framework stating, on
// the record, that THIS rule refuses THIS tool. The only thing left unknown is
// whether it refuses every use of it or only some — one narrow question, asked
// once per (rule, tool) pair, and answered off the turn's critical path.
//
// A rule that refuses every use makes the tool unusable, so the tool stops
// being offered. A rule that refuses some uses leaves it, because removing it
// would take away the uses the owner still wants — the failure mode that made
// a trimmed catalog convince Builder it could not write games.
//
// Why not resolve rules against the catalog when the owner writes them: it
// would be a guess across every tool × every rule, made before the agent has
// run, and wrong again the next time either side changes. This asks only about
// pairs that have actually collided, and asks after the collision proved they
// do.

package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/textutil"
)

const (
	guardrailToolScopeTable = "guardrail_tool_scope"
	// Bounded like every other per-agent ledger. A pair is written once, so
	// this is a ceiling on distinct (rule, tool) collisions, not on traffic.
	guardrailToolScopeKept = 100
)

// Scope values — what a rule does to a tool.
const (
	// guardrailScopeAll: every use of the tool violates the rule, so the tool
	// is unusable and stops being offered.
	guardrailScopeAll = "all"
	// guardrailScopeSome: only certain uses violate it. The tool stays; the
	// warden keeps judging the individual calls, which is what it is for.
	guardrailScopeSome = "some"
)

// GuardrailToolScope is what the warden established about one (rule, tool)
// pair. Exported for the owner-facing guardrails endpoint, which lists these
// so a withheld tool is something the owner can see and undo rather than a
// capability that quietly went missing.
type GuardrailToolScope struct {
	Rule  string    `json:"rule"`
	Tool  string    `json:"tool"`
	Scope string    `json:"scope"`
	Why   string    `json:"why"`
	At    time.Time `json:"at"`
}

// guardrailToolsNeverWithheld are tools the framework's own mechanics depend
// on, which therefore cannot be dropped however a rule reads.
//
// The test is "does the loop still work without it", NOT "is it useful". A
// rule that genuinely forbids every use of calculate should take calculate
// away; a rule that reads as forbidding background_work must not take away the
// agent's only means of STOPPING work it already started, because then
// "actually, forget the rest" becomes a sentence nothing can act on.
var guardrailToolsNeverWithheld = map[string]bool{
	"background_work": true,
}

// listGuardrailToolScopes returns an agent's recorded pairs, newest first.
func listGuardrailToolScopes(db Database, agentID string) []GuardrailToolScope {
	if db == nil || strings.TrimSpace(agentID) == "" {
		return nil
	}
	var list []GuardrailToolScope
	db.Get(guardrailToolScopeTable, agentID, &list)
	out := make([]GuardrailToolScope, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	return out
}

// clearGuardrailToolScopes forgets every pair for an agent, so the next block
// re-establishes them. The owner's undo: a tool withheld on a reading they
// disagree with comes back, and if the reading was right it will be withheld
// again the next time the agent reaches for it.
func clearGuardrailToolScopes(db Database, agentID string) {
	if db == nil || strings.TrimSpace(agentID) == "" {
		return
	}
	db.Unset(guardrailToolScopeTable, agentID)
}

// findGuardrailToolScope reports the recorded scope for one pair.
func findGuardrailToolScope(db Database, agentID, rule, tool string) (GuardrailToolScope, bool) {
	for _, s := range listGuardrailToolScopes(db, agentID) {
		if strings.EqualFold(s.Rule, rule) && strings.EqualFold(s.Tool, tool) {
			return s, true
		}
	}
	return GuardrailToolScope{}, false
}

// saveGuardrailToolScope records one pair, replacing any earlier reading of it.
func saveGuardrailToolScope(db Database, agentID string, s GuardrailToolScope) {
	if db == nil || strings.TrimSpace(agentID) == "" {
		return
	}
	var list []GuardrailToolScope
	db.Get(guardrailToolScopeTable, agentID, &list)
	kept := list[:0]
	for _, old := range list {
		if strings.EqualFold(old.Rule, s.Rule) && strings.EqualFold(old.Tool, s.Tool) {
			continue
		}
		kept = append(kept, old)
	}
	kept = append(kept, s)
	if n := len(kept); n > guardrailToolScopeKept {
		kept = kept[n-guardrailToolScopeKept:]
	}
	db.Set(guardrailToolScopeTable, agentID, kept)
}

// guardrailWithheldTools is the tool → rule map for THIS agent right now:
// every tool a still-enforced rule makes entirely unusable.
//
// Resolved against the rules currently in force, never against the ledger
// alone. That is what makes the ledger self-expiring: edit a rule and its
// text changes, so its entries stop matching; delete it and they stop
// matching; suspend the agent's guardrails and enforcedGuardrailRules drops
// its own rules, so its withholdings lift with them. Nothing has to be
// cleaned up, and no stale entry can keep taking a tool away.
func (t *chatTurn) guardrailWithheldTools() map[string]string {
	if t == nil {
		return nil
	}
	db := t.ownerDB
	if db == nil {
		db = t.udb
	}
	if db == nil {
		return nil
	}
	scopes := listGuardrailToolScopes(db, t.agent.ID)
	if len(scopes) == 0 {
		return nil
	}
	inForce := map[string]bool{}
	for _, r := range enforcedGuardrailRules(t.agent) {
		inForce[strings.ToLower(strings.TrimSpace(r.Text))] = true
	}
	var out map[string]string
	for _, s := range scopes {
		if s.Scope != guardrailScopeAll {
			continue
		}
		if !inForce[strings.ToLower(strings.TrimSpace(s.Rule))] {
			continue
		}
		if guardrailToolsNeverWithheld[s.Tool] {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[s.Tool] = s.Rule
	}
	return out
}

// liveGuardrailToolScopes is the owner-facing view: the recorded readings that
// are still in force, newest first.
//
// Filtered against the rules currently authored, for the same reason the
// runtime is: an entry whose rule has been edited or deleted changes nothing
// and listing it would describe a capability as missing when it is not. The
// filter is the rule's TEXT, which is also the ledger's key — so "I reworded
// that rule" and "that entry is gone" are one action, not two.
func liveGuardrailToolScopes(db Database, agent AgentRecord) []GuardrailToolScope {
	all := listGuardrailToolScopes(db, agent.ID)
	if len(all) == 0 {
		return nil
	}
	inForce := map[string]bool{}
	for _, r := range enforcedGuardrailRules(agent) {
		inForce[strings.ToLower(strings.TrimSpace(r.Text))] = true
	}
	out := make([]GuardrailToolScope, 0, len(all))
	for _, s := range all {
		if inForce[strings.ToLower(strings.TrimSpace(s.Rule))] {
			out = append(out, s)
		}
	}
	return out
}

// applyGuardrailToolWithholding drops the tools this agent's rules make
// unusable. Returns the list unchanged when there are none, which is every
// turn for every agent that has never had a rule refuse a tool outright.
//
// Late, over the assembled AgentToolDefs rather than over a name list, for the
// same reason Private mode needs a backstop there: the per-turn tools (the
// `agents` grouped tool, temp tools, attached pipelines) are built in code and
// never pass through the registry, so a name filter upstream would miss
// exactly the ones most likely to collide with a rule.
func (t *chatTurn) applyGuardrailToolWithholding(tools []AgentToolDef) []AgentToolDef {
	withheld := t.guardrailWithheldTools()
	if len(withheld) == 0 {
		return tools
	}
	kept := make([]AgentToolDef, 0, len(tools))
	var dropped []string
	for _, td := range tools {
		if rule, ok := withheld[td.Tool.Name]; ok {
			dropped = append(dropped, fmt.Sprintf("%s (rule %q)", td.Tool.Name, rule))
			continue
		}
		kept = append(kept, td)
	}
	if len(dropped) > 0 {
		// Log, not a session breadcrumb. The breadcrumb is written ONCE, when
		// the reading is established (see learnGuardrailToolScope) — a card or
		// a trail entry on every turn thereafter would report a steady state
		// as news, which is how a diagnostics trail stops being read.
		Log("[orchestrate.guardrail] agent=%s withheld %d tool(s) its rules forbid outright: %v", t.agent.ID, len(dropped), dropped)
	}
	return kept
}

// learnGuardrailToolScope is called when a rule blocks a tool call. It asks
// the one question the block leaves open — does this rule refuse EVERY use of
// this tool, or only some — and records the answer.
//
// Off the critical path, deliberately. The turn already has its verdict and is
// mid-round; the answer changes nothing until the NEXT catalog is assembled,
// so making the user wait for it would buy nothing. It runs on a context
// detached from the turn's for the same reason: a reply that lands and a turn
// that ends would otherwise cancel the call that stops this from happening
// again.
//
// Asked once per pair, ever. The second block of the same tool by the same
// rule finds the reading already recorded and asks nothing.
func (t *chatTurn) learnGuardrailToolScope(rule, hookPoint, candidate string) {
	if t == nil || t.app == nil || hookPoint != guardHookPreAction {
		return // only a pre_action candidate names a tool
	}
	tool := guardrailCandidateTool(candidate)
	if tool == "" || guardrailToolsNeverWithheld[tool] {
		return
	}
	rule = strings.TrimSpace(rule)
	if rule == "" || rule == taintedActionRule {
		return // not an authored rule; there is no rule text to reason about
	}
	db := t.ownerDB
	if db == nil {
		db = t.udb
	}
	if db == nil {
		return
	}
	if _, ok := findGuardrailToolScope(db, t.agent.ID, rule, tool); ok {
		return
	}
	// Everything the goroutine needs, captured before the turn can move on.
	app, agentID := t.app, t.agent.ID
	diagAgent, diagSession := t.agent.ID, t.diagSessionID
	if t.session != nil {
		diagSession = t.session.ID
	}
	if t.diagAgentID != "" && t.session == nil {
		diagAgent = t.diagAgentID
	}
	ctx := context.WithoutCancel(t.ctx)
	go scopeAndRecordGuardrailTool(ctx, app, db, agentID, diagAgent, diagSession, rule, tool, candidate)
}

// scopeAndRecordGuardrailTool is the body of learnGuardrailToolScope with
// nothing of the turn left in it: one classification, one ledger write, one
// breadcrumb. Separated so the work is a plain function that can be run and
// waited on, rather than something only observable by racing a goroutine.
func scopeAndRecordGuardrailTool(ctx context.Context, app *OrchestrateApp, db Database, agentID, diagAgent, diagSession, rule, tool, candidate string) {
	cctx, cancel := context.WithTimeout(ctx, guardrailToolScopeTimeout)
	defer cancel()
	scope, why, err := app.classifyGuardrailToolScope(cctx, rule, tool, candidate)
	if err != nil {
		// No reading is the safe outcome: the tool stays, the warden keeps
		// judging each call, and the next block asks again.
		Log("[orchestrate.guardrail] agent=%s could not scope rule %q against tool %q (%v): the tool stays offered", agentID, rule, tool, err)
		return
	}
	saveGuardrailToolScope(db, agentID, GuardrailToolScope{
		Rule: rule, Tool: tool, Scope: scope, Why: why, At: time.Now(),
	})
	if scope != guardrailScopeAll {
		Log("[orchestrate.guardrail] agent=%s rule %q refuses only SOME uses of %q: the tool stays offered", agentID, rule, tool)
		return
	}
	Log("[orchestrate.guardrail] agent=%s rule %q refuses EVERY use of %q, withholding it from the catalog: %s", agentID, rule, tool, why)
	// The breadcrumb is here, once, because this is the moment something
	// changed. It says what stopped being available and why, so a capability
	// that goes missing next turn is answerable from the trail the owner can
	// already open.
	appendSessionDiag(db, diagAgent, diagSession, "guardrail-tool-scoped", fmt.Sprintf(
		"The tool %q will no longer be offered to this agent: the rule %q refuses every use of it (%s). It comes back if the rule is edited or removed, or from Clear in the guardrails panel.",
		tool, rule, why))
}

// guardrailCandidateTool pulls the tool name out of a pre_action candidate,
// which core builds as "<tool> <args…>".
func guardrailCandidateTool(candidate string) string {
	s := strings.TrimSpace(candidate)
	if i := strings.IndexAny(s, " \t\n"); i > 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSpace(s))
}

const guardrailToolScopeTimeout = 30 * time.Second

// classifyGuardrailToolScope asks whether a rule refuses a tool entirely.
//
// A deliberately narrow question, and narrow is what makes it trustworthy. It
// is not asked to decide whether the rule and the tool are related — a warden
// verdict already settled that by blocking the call. It is asked only to tell
// a rule ABOUT a tool ("never delegate to other agents") from a rule about
// some of its uses ("never email the CEO"), which is a distinction the rule's
// own wording carries.
//
// Both inputs are TRUSTED text: the rule is the owner's, the tool name and
// description are the framework's. The blocked call rides along as an example
// and is the only part that came from the model, so it is fenced.
func (T *OrchestrateApp) classifyGuardrailToolScope(ctx context.Context, rule, tool, candidate string) (scope, why string, err error) {
	if T == nil || T.LLM == nil {
		return "", "", fmt.Errorf("guardrail tool scope: LLM not initialized")
	}
	desc := ""
	if ct, ok := FindChatTool(tool); ok {
		desc = strings.TrimSpace(ct.Desc())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "RULE (the owner's words, trusted):\n%s\n\n", rule)
	fmt.Fprintf(&b, "TOOL: %s\n", tool)
	if desc != "" {
		fmt.Fprintf(&b, "WHAT IT DOES: %s\n", desc)
	}
	b.WriteString("\nA check has already established that this rule refuses at least one call to this tool. Decide which kind of rule it is.\n\n")
	// The call that was refused, as the example. It matters most for the tools
	// with no registered description — the framework builds several of them per
	// turn (the `agents` grouped tool among them), so FindChatTool above comes
	// up empty for exactly the ones a rule is most likely to collide with, and
	// the name alone is thin evidence. Fenced: everything else here is the
	// owner's words or the framework's, and this one line is the model's.
	b.WriteString(textutil.UntrustedData("the call that was refused", candidate))
	msgs := []Message{
		{Role: "system", Content: guardrailToolScopeSystemPrompt},
		{Role: "user", Content: b.String()},
	}
	resp, err := T.WorkerChat(ctx, msgs,
		WithRouteKey("app.orchestrate.warden"),
		WithThink(false),
		WithTemperature(0.1),
		WithMaxTokens(200),
	)
	if err != nil {
		return "", "", err
	}
	if resp == nil {
		return "", "", fmt.Errorf("guardrail tool scope: empty response")
	}
	raw := extractJSONObject(resp.Content)
	if raw == "" {
		return "", "", fmt.Errorf("guardrail tool scope: reply was not parseable")
	}
	var parsed struct {
		Scope string `json:"scope"`
		Why   string `json:"why"`
	}
	if jerr := json.Unmarshal([]byte(raw), &parsed); jerr != nil {
		return "", "", fmt.Errorf("guardrail tool scope: %w", jerr)
	}
	// Anything but an explicit "all" is "some". An unreadable or hedged answer
	// must never take a capability away — the cost of leaving a tool offered is
	// one more block, and the cost of removing one wrongly is a thing the agent
	// can no longer do and cannot explain.
	if strings.EqualFold(strings.TrimSpace(parsed.Scope), guardrailScopeAll) {
		return guardrailScopeAll, strings.TrimSpace(parsed.Why), nil
	}
	return guardrailScopeSome, strings.TrimSpace(parsed.Why), nil
}

const guardrailToolScopeSystemPrompt = `You classify how a rule relates to a tool.

Answer with JSON only:
{"scope": "all" | "some", "why": "<one short sentence>"}

"all": the rule forbids what the tool DOES. No call to it could comply, whatever
         the arguments. Examples: rule "never delegate to other agents" against a
         tool whose only function is dispatching to another agent; rule "never send
         email" against a tool that sends email.

"some": the rule forbids certain uses and permits others. The tool has legitimate
         calls under this rule. Examples: rule "never email the CEO" against a tool
         that sends email to anyone; rule "don't post before 9am" against a posting
         tool; any rule naming a recipient, a subject, a time, an amount, or a
         condition rather than the action itself.

Answering "all" removes the tool from the agent entirely, so answer "all" only when
you would defend it for EVERY possible call. If the rule turns on who, what, when or
how much, it is "some". If you are unsure, answer "some".`

// guardrailToolChoices is the vocabulary a rule may be bound to: the tools
// this agent can actually call.
//
// It is the picker's list AND the save-time validator's allowlist, which is
// the point of having one function. A name the picker cannot offer is a name
// the owner must not be able to store, because a rule bound to a tool that
// does not exist applies to nothing and enforces nothing — and unlike a dead
// carve-out link, which makes a rule STRONGER by not excepting anything, a
// dead tool binding makes it vanish.
//
// Three sources, because an agent's reach has three:
//
//	the registry     — the ordinary curated pool, filtered by AllowedTools
//	framework tools  — built per turn and in no registry (`agents` among them,
//	                   which is the binding people reach for first)
//	the allowlist    — names the owner added by hand that the registry does
//	                   not know: a custom tool, an MCP name
func guardrailToolChoices(agent AgentRecord) []string {
	allowAll := len(agent.AllowedTools) == 0 || nameListed(agent.AllowedTools, "*")
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || n == "*" || n == noToolsSentinel || seen[n] {
			return
		}
		// Only a name the MARKER can carry. A binding is stored as "#name" in
		// the rule text, so a name the grammar cannot round-trip would be
		// offered in the picker, saved as something shorter, and come back
		// unmatched - which reads as the setting not having saved. A control
		// must not offer a state its value cannot hold.
		if leadingToolName(n) != n {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	if !isNoToolsSentinel(agent.AllowedTools) {
		for _, ct := range RegisteredChatTools() {
			name := ct.Name()
			if !allowAll && !nameListed(agent.AllowedTools, name) {
				continue
			}
			add(name)
		}
		for n := range frameworkInfrastructureTools {
			add(n)
		}
		for _, n := range frameworkUtilityTools {
			add(n)
		}
	}
	for _, n := range agent.AllowedTools {
		add(n)
	}
	sort.Strings(out)
	return out
}

// validateGuardrailToolBindings rejects a rule bound to a tool this agent
// cannot call, naming it.
//
// Refused at the boundary rather than stored and ignored. A binding only ever
// NARROWS where a rule is judged, so a name that matches nothing narrows it to
// nowhere — the rule would sit in the list looking enforced and be enforced
// nowhere. That is the one failure direction this must not have, and it is why
// the check is here and not a warning in the UI that a hand-edit can skip.
func validateGuardrailToolBindings(agent AgentRecord, rules string) error {
	known := map[string]bool{}
	for _, n := range guardrailToolChoices(agent) {
		known[n] = true
	}
	for _, line := range strings.Split(rules, "\n") {
		r := parseGuardrailRule(strings.TrimSpace(line))
		if r.Tool == "" || known[r.Tool] {
			continue
		}
		return fmt.Errorf("the rule %q is bound to %q, which is not a tool this agent can call: pick one from the list, or drop the #%s to make the rule apply everywhere", r.Text, r.Tool, r.Tool)
	}
	return nil
}

// scopeBoundGuardrailRules reads every newly-bound rule against its tool, so
// the outcome is settled when the rule is SAVED rather than the first time the
// agent walks into it.
//
// This is what a binding buys beyond narrowing the warden. Without one the
// framework can only learn that a rule forbids a tool outright by watching it
// refuse a call — a block, a wasted turn and a classification before anything
// improves. With one the pair is known on the day the rule is written, so the
// question can be asked immediately and the tool is gone from the catalog
// before the agent ever reaches for it.
//
// Still the same question, answered the same conservative way: only an
// unambiguous "every use of this tool violates the rule" withholds anything. A
// rule that turns on who, what or when stays a warden rule — now a cheaper one,
// judged only when its tool is called.
//
// Off the request. The owner pressed Save; they should not wait on an LLM call
// per new binding to find out whether it worked, and nothing they can see is
// wrong until the next turn assembles a catalog.
func (T *OrchestrateApp) scopeBoundGuardrailRules(ctx context.Context, db Database, agent AgentRecord) {
	if T == nil || db == nil {
		return
	}
	var pending []guardrailRule
	for _, r := range enforcedGuardrailRules(agent) {
		if r.Tool == "" || guardrailToolsNeverWithheld[r.Tool] {
			continue
		}
		if _, ok := findGuardrailToolScope(db, agent.ID, r.Text, r.Tool); ok {
			continue // already read
		}
		pending = append(pending, r)
	}
	if len(pending) == 0 {
		return
	}
	agentID := agent.ID
	detached := context.WithoutCancel(ctx)
	go func() {
		for _, r := range pending {
			// The binding names the pair, so there is no blocked call to show
			// the classifier — the rule and the tool are the whole question.
			scopeAndRecordGuardrailTool(detached, T, db, agentID, agentID, "",
				r.Text, r.Tool, r.Tool)
		}
	}()
}
