package orchestrate

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// (turnHasFreshExternalContent retired alongside the synthesis
// auto-ingest path. Reference Memory is now explicit-save only —
// the LLM calls memory_save when it consciously wants to record
// something, no framework-driven capture. The loop-break gate
// this function fed isn't needed anymore.)

// renderTriggeredSkills injects the FULL instructions of any allowed skill
// the LLM DREW ON this turn (read_skill / skill_knowledge_search →
// t.deliveredSkills), so a consulted skill's lens governs the whole turn —
// the synthesis reply especially, which builds a fresh system prompt. A
// mere trigger MATCH no longer injects here; it surfaces as a soft nudge
// via renderSkillTriggerHints. Empty when DisableSkills, no allowlist, or
// nothing was consulted.
func (t *chatTurn) renderTriggeredSkills() string {
	if t == nil || t.agent.DisableSkills || len(t.agent.AllowedSkills) == 0 {
		return ""
	}
	allowed := make(map[string]bool, len(t.agent.AllowedSkills))
	for _, id := range t.agent.AllowedSkills {
		allowed[id] = true
	}
	var b strings.Builder
	for _, s := range LoadSkills(t.udb, t.user) {
		if s.Disabled || !allowed[s.ID] {
			continue
		}
		if !t.deliveredSkills[s.ID] {
			continue
		}
		b.WriteString(SkillPromptSection(s))
	}
	return b.String()
}

// renderSkillTriggerHints surfaces a soft HINT for allowed skills whose
// triggers match this turn (last user message + attached doc filenames for
// glob triggers like "*.pdf") but that the LLM hasn't consulted yet — a
// nudge to reach for them, not an injection. Skills already delivered this
// turn are skipped (no point hinting what's already loaded). The match is a
// relevance signal; the LLM still decides.
func (t *chatTurn) renderSkillTriggerHints(userMsg string) string {
	if t == nil || t.agent.DisableSkills || len(t.agent.AllowedSkills) == 0 {
		return ""
	}
	allowed := make(map[string]bool, len(t.agent.AllowedSkills))
	for _, id := range t.agent.AllowedSkills {
		allowed[id] = true
	}
	var names []string
	for _, s := range LoadSkills(t.udb, t.user) {
		if s.Disabled || !allowed[s.ID] || t.deliveredSkills[s.ID] {
			continue
		}
		if SkillTriggersMatch(s, userMsg, t.docNames) {
			names = append(names, s.Name)
		}
	}
	return SkillTriggerHintBlock(names)
}

// availableSkillsBlock is the chatTurn-free form so the shared capability
// assembler (used by BOTH the web and channel/dispatch paths) can render it
// without a chatTurn. See appendAgentCapabilityBlocks.
func availableSkillsBlock(agent AgentRecord, udb Database, user string) string {
	if agent.DisableSkills || len(agent.AllowedSkills) == 0 {
		return ""
	}
	allowed := make(map[string]bool, len(agent.AllowedSkills))
	for _, id := range agent.AllowedSkills {
		allowed[id] = true
	}
	var avail []SkillRecord
	for _, s := range LoadSkills(udb, user) {
		if s.Disabled || !allowed[s.ID] {
			continue
		}
		avail = append(avail, s)
	}
	// Rendering lives in core (shared with phantom); this function only
	// computes the per-agent available set.
	return RenderAvailableSkills(avail)
}

// fleetView returns the (db, user) pair to use for agent-record
// lookups (Available agents block + agents(action="run") dispatch).
// On phantom and other foreign-user runs ownerDB / ownerUser are set
// so the fleet read hits the original owner's per-user DB even
// though session/memory/facts stay scoped to the runtime user. On
// interactive owner-runs where the fields are unset, falls back to
// udb / user — same behavior as before this split.
func (t *chatTurn) fleetView() (Database, string) { return t.ownerView() }

// ownerView is the store that owns the AGENT — its record, its fleet peers, and
// the custom-tool pool its AllowedTools names. Falls back to the runtime
// identity, which is the same thing on an ordinary owner-driven turn.
//
// The distinction only bites when the two differ: a phantom/channel run under a
// synthetic per-chat user, or a PUBLISHED agent being chatted by someone who is
// not its author. In both cases sessions, memory and knowledge belong to the
// runtime user and everything describing the agent belongs to the owner.
func (t *chatTurn) ownerView() (Database, string) {
	if t == nil {
		return nil, ""
	}
	if t.ownerDB != nil && t.ownerUser != "" {
		return t.ownerDB, t.ownerUser
	}
	return t.udb, t.user
}

// dispatchableFleet returns the agents this turn's agent may dispatch
// to — the shared source of truth for BOTH the "Available agents"
// prompt block AND the per-agent consult_<name> tools. Empty when the
// current agent can't dispatch at all.
//
// Excludes: the current agent (don't dispatch to yourself), Builder
// (separate routing concern — needs the user directly), and (in default
// mode) Hidden agents. Returns nil for a sub-agent leaf (OwnedBy set —
// no agents tool) or a restricted catalog without the `agents` tool, so
// neither the block nor the consult tools tell an agent to do something
// its catalog physically prevents.
// dispatchableFleet returns this turn's dispatch catalog, computed once and
// memoized on the chatTurn. Its three consumers (available-agents block,
// trigger hints, active-dispatch-threads block) share the result instead of
// each re-listing agents from the DB and re-logging the catalog.
func (t *chatTurn) dispatchableFleet() []AgentRecord {
	if t == nil {
		return nil
	}
	if t.fleetDone {
		return t.fleetCatalog
	}
	t.fleetCatalog = t.computeDispatchableFleet()
	t.fleetDone = true
	return t.fleetCatalog
}

func (t *chatTurn) computeDispatchableFleet() []AgentRecord {
	if t == nil {
		return nil
	}
	// Audit trail (Debug): the available-agents catalog silently not
	// rendering was a costly bug, so every exit point says what happened.
	// Grep "available-agents" to confirm it shows N agents each turn, or
	// catch a suppression (and its reason) if it ever regresses.
	if t.agent.OwnedBy != "" {
		Debug("[orchestrate] available-agents: suppressed for agent=%q — sub-agent leaf (no dispatch surface)", t.agent.ID)
		return nil
	}
	fleetDB, fleetUser := t.fleetView()
	if fleetDB == nil || fleetUser == "" {
		Debug("[orchestrate] available-agents: suppressed for agent=%q — no fleet view (db/user unresolved)", t.agent.ID)
		return nil
	}
	// The `agents` grouped tool is force-added to EVERY non-leaf agent's
	// catalog (see the unconditional knowTools append in the catalog
	// builder — it is NOT gated on AllowedTools). So an explicit
	// AllowedTools list that doesn't happen to name "agents" still HAS
	// dispatch. The old gate here checked for a literal "agents" in
	// AllowedTools and bailed when absent — which silently suppressed the
	// whole catalog for any agent with a materialized tool list (seed-chat
	// after the first tool approval, every custom agent). The tool was
	// present but the model never saw WHAT it could dispatch to, so it
	// fell back to agents(action="list") or just didn't delegate. Gate
	// only on the no-tools sentinel: an agent an admin set to zero tools
	// genuinely shouldn't be told to delegate.
	if isNoToolsSentinel(t.agent.AllowedTools) {
		Debug("[orchestrate] available-agents: suppressed for agent=%q — no-tools sentinel (admin set zero tools)", t.agent.ID)
		return nil
	}
	// Dispatch policy (see AgentRecord.DispatchMode / effectiveDispatchMode):
	// all = any non-hidden; only = allowlist (explicit pick wins over Hidden);
	// except = any non-hidden minus the list; none = nothing. Only count targets
	// that STILL EXIST — a stale (deleted) id must not keep an allowlist in
	// restrict-mode, which would silently hide every other agent. Self-heals a
	// legacy "only" list whose members were all deleted by falling back to all.
	all := listAgents(fleetDB, fleetUser)
	exists := make(map[string]bool, len(all))
	for _, a := range all {
		exists[a.ID] = true
	}
	mode := effectiveDispatchMode(t.agent)
	if mode == dispatchNone {
		Debug("[orchestrate] available-agents: suppressed for agent=%q — dispatch policy is Allow none", t.agent.ID)
		return nil
	}
	listed := map[string]bool{}
	for _, id := range t.agent.AllowedDispatchTargets {
		if exists[id] {
			listed[id] = true
		}
	}
	// Self-heal (every allowlisted target deleted) now lives in
	// dispatchModeAfterSelfHeal, shared with the dispatch gate — see there for
	// what went wrong while only this surface applied it.
	mode = t.dispatchModeAfterSelfHeal(fleetDB, fleetUser)
	var available []AgentRecord
	for _, a := range all {
		if a.ID == t.agent.ID || isFleetRetiredSeed(a.ID) || isRetiringArchetypeSeed(a.ID) {
			continue
		}
		// Builder rides the same carve-out as the dispatch gate: invisible
		// to everyone who can't call it, listed for everyone who can — and
		// listed regardless of dispatch mode, since the grant is explicit
		// and shouldn't also have to be repeated in an allowlist. (Allow
		// none already returned above, so this can't resurrect dispatch for
		// an agent the user switched off.)
		if isBuilderAgent(a.ID) {
			if t.canDispatchBuilder() {
				available = append(available, a)
			}
			continue
		}
		// A target BLOCKED in the Permissions pane must not be advertised.
		// The dispatch gate refuses it, but the fleet catalog feeds the
		// available-agents block, the per-turn trigger hints ("dispatch to
		// X FIRST"), and the open-dispatch-threads cue — leaving a blocked
		// agent in those keeps nudging the model into calls the gate then
		// refuses, a wedge of pure refusal noise. Drop it here so every
		// steering surface goes quiet together.
		if IsDelegationBlocked(RootDB, fleetUser, a.Name) || IsDelegationBlocked(RootDB, fleetUser, a.ID) {
			continue
		}
		// A sub-agent owned by ANOTHER agent is private to its owner — never surface
		// it in this agent's fleet view (mirrors the dispatch gate, which refuses to
		// dispatch to it). The owner still sees its own sub-agents per the Hidden /
		// mode rules below.
		if a.OwnedBy != "" && a.OwnedBy != t.agent.ID {
			continue
		}
		switch mode {
		case dispatchOnly:
			if !listed[a.ID] {
				continue
			}
		case dispatchExcept:
			if listed[a.ID] || a.Hidden {
				continue
			}
		default: // dispatchAll
			if a.Hidden {
				continue
			}
		}
		available = append(available, a)
	}
	if len(available) == 0 {
		Debug("[orchestrate] available-agents: 0 in catalog for agent=%q — no OTHER dispatchable agents in the fleet (not a bug if the user has none)", t.agent.ID)
	} else {
		names := make([]string, 0, len(available))
		for _, a := range available {
			names = append(names, a.Name)
		}
		Debug("[orchestrate] available-agents: %d in catalog for agent=%q this turn: %s", len(available), t.agent.ID, strings.Join(names, ", "))
	}
	return available
}

// renderAvailableAgentsBlock surfaces the OTHER agents in the user's
// fleet so the host LLM knows what it can dispatch to via
// agents(action="run", agent=..., message=...). Without this block
// the LLM has to call agents(action="list") first to discover them,
// which it almost never does speculatively — the result is that
// authored specialist agents (Pickleball Expert, Code Reviewer,
// etc.) are effectively invisible. Empty when the user has no other
// agents or the current agent is the only one.
func (t *chatTurn) renderAvailableAgentsBlock() string {
	if t == nil {
		return ""
	}
	available := t.dispatchableFleet()
	if len(available) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Available agents\n\n")
	b.WriteString("Specialists the user has authored. **If a question lands in one of these agents' domains, DELEGATE FIRST.** Rely on the agent for the work it's built for — when the use case fits it gives the best result: its own persona, tools, and grounded sources beat your general knowledge. This holds EVEN WHEN you could handle it with your own tools — for a question in a listed agent's domain, delegate rather than web_searching it yourself; a tool call is not a substitute for the specialist. Dispatch as your FIRST move on such a question — don't run several of your own searches and fall back to the agent only when they come up short; the specialist IS the move, not the backup. Answer yourself only when no agent's domain fits — NOT because you feel you already know it or could look it up. And don't narrate that you'll consult an agent and then answer anyway: either dispatch, or answer plainly as you.\n\nDelegate via `agents(action=\"run\", agent=\"<name>\", message=\"<the brief>\")`. **The agent remembers within this session.** It re-threads your prior dispatches to it this session (ephemeral continuity) on top of its own persona, saved facts, and knowledge base, so a follow-up to the same agent can be brief without repeating earlier context. A RELATED FOLLOW-UP goes back to the SAME agent; don't interpret or answer it yourself from the earlier result. This dispatch memory is ephemeral, scoped to this session. Re-dispatch, including the prior context in the brief: \"Earlier you summarized Acme Corp as <X>. Now tell me more about their B2B presence.\" You own the context; the sub-agent answers the question in front of it.\n\nIntegrate the answers into your reply as if they were your own — don't say \"I asked X\" or \"the X agent said\"; the user doesn't know the fleet structure. Just answer with the substance.\n\nFormat: **name** — when to delegate.\n\n")
	for _, a := range available {
		// Full description — it's the routing cue (descriptions are
		// model-facing, per the Builder guidance), shown un-truncated so
		// no part of the cue is hidden from the dispatch decision.
		desc := strings.TrimSpace(a.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString("- **")
		b.WriteString(a.Name)
		b.WriteString("** — ")
		b.WriteString(desc)
		// Deterministic dispatch contract: a sub-agent gets its structured
		// input from the parent's brief, not a form (intake_form isn't applied
		// on dispatch), so an agent that declares an intake_form is telling us
		// exactly what its brief needs. Surface those field labels so the
		// orchestrator packs them up front instead of dispatching a vague brief
		// and forcing a clarifying round-trip. Derived at render time from the
		// agent's CURRENT spec — nothing stored, never stale.
		b.WriteString(dispatchBriefHint(a))
		b.WriteString("\n")
	}
	return b.String()
}

// dispatchBriefHint returns a one-line "put this in the brief" cue built from an
// agent's intake_form field labels, or "" when it declares none. Required
// fields are marked; a file field asks for the document's text (a text brief
// can't carry an upload); button fields are skipped (self-submitting actions,
// not inputs to supply).
func dispatchBriefHint(a AgentRecord) string {
	if len(a.IntakeForm) == 0 {
		return ""
	}
	var parts []string
	for _, f := range a.IntakeForm {
		label := strings.TrimSpace(f.Label)
		if label == "" {
			label = strings.TrimSpace(f.Name)
		}
		if label == "" {
			continue
		}
		if f.Type == "button" {
			// A button is a router rather than something to type, so the
			// human form self-submits on click. A CALLER composing a brief
			// still has to state the choice, though, and the options ARE
			// that choice — so render them instead of dropping the field.
			// Skipping it meant a button-only form (the natural shape for
			// "pick a starting point") published no brief guidance at all.
			if len(f.Options) == 0 {
				continue
			}
			parts = append(parts, label+" ("+strings.Join(f.Options, " | ")+")")
			continue
		}
		if f.Type == "file" {
			label += " (as document text)"
		}
		if f.Required {
			label += " (required)"
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		return ""
	}
	return " When you dispatch, include in the brief: " + strings.Join(parts, ", ") + "."
}

// renderAgentTriggerHints surfaces a SOFT per-turn nudge for dispatchable
// agents whose Triggers match this turn — "this turn is <agent>'s domain,
// dispatch FIRST" — placed right after the catalog (near the user
// message, the highest-salience spot). The static Available-agents block
// alone doesn't bind a model with strong priors in the domain: it reads
// the block, agrees, and web_searches anyway (observed on PC-372 legal
// Qs). A trigger match is a relevance signal, not a command — a wrong
// hint is one line the model ignores. Mirrors the skill trigger-hint
// mechanism. Empty when nothing matches or the agent can't dispatch.
func (t *chatTurn) renderAgentTriggerHints(userMsg string) string {
	if t == nil {
		return ""
	}
	fleet := t.dispatchableFleet()
	if len(fleet) == 0 {
		return ""
	}
	var names []string
	for _, a := range fleet {
		if TriggersMatch(a.Triggers, userMsg, t.docNames) {
			names = append(names, a.Name)
		}
	}
	return agentTriggerHintBlock(names)
}

// agentTriggerHintBlock formats the per-turn dispatch nudge for the given
// agent names. Empty names → "". Mirrors SkillTriggerHintBlock's shape.
func agentTriggerHintBlock(names []string) string {
	if len(names) == 0 {
		return ""
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "**" + n + "**"
	}
	return "\n\n[Likely this turn (agent triggers matched): the question lands in " + strings.Join(quoted, ", ") + "'s domain — dispatch to it via agents(action=\"run\", agent=\"<name>\", message=\"<brief>\") as your FIRST move, before web_search or answering from memory. A trigger match is a strong nudge, not a command: skip it only if it plainly doesn't fit.]\n\n"
}

// renderActiveDispatchThreads surfaces the agents this session has ALREADY
// dispatched to, so the host LLM knows it has live, re-threadable
// conversations open. The dispatch continuity (the prior exchange) lives
// only on the sub-agent side (dispatch:<sess>:<agentID>); the host's own
// history hides delegation entirely ("integrate as your own, don't say 'I
// asked X'"), so without this cue a follow-up like "tell me more" reads to
// the host as a question it can answer from its own memory — and it does,
// instead of re-dispatching to the agent that actually has the context. The
// trigger-hint nudge doesn't cover this: a generic follow-up rarely matches
// the original agent's keyword triggers. This block is the missing signal:
// name the open threads + the directive to send follow-ups back to the same
// agent. Empty when this session has no open dispatch threads.
func (t *chatTurn) renderActiveDispatchThreads() string {
	if t == nil || t.udb == nil || t.session == nil || t.session.ID == "" {
		return ""
	}
	fleet := t.dispatchableFleet()
	if len(fleet) == 0 {
		return ""
	}
	var parts []string
	for _, a := range fleet {
		subSessID := "dispatch:" + t.session.ID + ":" + a.ID
		sess, ok := loadChatSession(t.udb, a.ID, subSessID)
		if !ok || len(sess.Messages) == 0 {
			continue
		}
		part := "**" + a.Name + "**"
		if topic := lastDispatchTopic(sess.Messages); topic != "" {
			part += " (last: " + topic + ")"
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n[Active dispatch threads (this session): you've already delegated to " +
		strings.Join(parts, "; ") +
		". If the user's message is a FOLLOW-UP to one of these — \"tell me more\", \"what about X\", drilling into the same topic — re-dispatch it to that SAME agent with the prior context via agents(action=\"run\", agent=\"<name>\", message=\"<brief>\"); do NOT answer it yourself from the earlier result. You delegated it before because it's that agent's domain — that hasn't changed, and the agent re-threads its own prior turns so a brief follow-up is enough.]\n\n"
}

// lastDispatchTopic returns a short label for a dispatch thread — the most
// recent user brief sent to that agent, with the delegation marker stripped
// and truncated. Used only for the active-threads hint so the host can tell
// which open thread a follow-up belongs to. Empty when no user message.
func lastDispatchTopic(msgs []ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		topic := strings.TrimSpace(msgs[i].Content)
		// Briefs are wrapped by markAsDelegated as "[DELEGATED INVOCATION]
		// …\n\nBrief: <text>"; show just the brief.
		if idx := strings.LastIndex(topic, "Brief: "); idx >= 0 {
			topic = strings.TrimSpace(topic[idx+len("Brief: "):])
		}
		topic = strings.ReplaceAll(topic, "\n", " ")
		if len(topic) > 80 {
			topic = strings.TrimSpace(topic[:80]) + "…"
		}
		return topic
	}
	return ""
}

// renderBuilderExistingToolsBlock lists the user's persistent (admin-
// approved) custom tools as READ-ONLY awareness for Builder. Builder's
// executable catalog hides these on purpose — see the persistent-tool
// load skip in newToolSession — so the LLM can't accidentally "use" a
// pre-existing tool when it should be authoring a new one. But Builder
// still needs to KNOW what exists so it can:
//
//   - Spot name collisions ("user wants a news_summary tool — does one
//     already exist?")
//   - Pick the iteration path when authoring with an existing name
//     (re-author with same name overwrites the active entry in place,
//     no admin re-approval needed — handled by UpdatePersistentTempTool
//     from the queueForReview path)
//   - Reference an existing tool by name in pipeline_tools (pipeline
//     mode resolves by name at dispatch, doesn't need the tool in the
//     executable catalog)
//
// Returns empty when there are no persistent custom tools or when the
// current agent isn't Builder. Format mirrors renderAvailableAgentsBlock
// — one bullet per tool, name + one-line description.
func (t *chatTurn) renderBuilderExistingToolsBlock() string {
	if t == nil || t.udb == nil || t.user == "" {
		return ""
	}
	if !isBuilderAgent(t.agent.ID) {
		return ""
	}
	persistent := LoadPersistentTempTools(t.udb, t.user)
	if len(persistent) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Existing custom tools in this user's environment (READ-ONLY for awareness)\n\n")
	b.WriteString("Tools the user already authored + an admin approved. **These are NOT in your executable catalog — you cannot dispatch them.** They're listed here so you can: (a) check for name collisions before authoring (re-authoring with an existing name OVERWRITES the active entry — no admin re-approval needed, that's the canonical iteration path); (b) reference one in pipeline_tools when composing (pipeline mode resolves by name at dispatch, doesn't need the tool in your callable catalog).\n\nFormat: **name** (mode) — one-line description.\n\n")
	for _, p := range persistent {
		desc := strings.TrimSpace(p.Tool.Description)
		if len(desc) > 140 {
			desc = desc[:140] + "…"
		}
		if desc == "" {
			desc = "(no description)"
		}
		mode := strings.TrimSpace(p.Tool.Mode)
		if mode == "" {
			mode = "shell"
		}
		b.WriteString("- **")
		b.WriteString(p.Tool.Name)
		b.WriteString("** (")
		b.WriteString(mode)
		b.WriteString(") — ")
		b.WriteString(desc)
		b.WriteString("\n")
	}
	return b.String()
}

// renderKnownTopicsBlock lists the snake_case topic slugs this
// (user, agent) has already used for memory_save. Surfaced so the
// LLM reuses an existing slug instead of minting near-duplicates
// when it calls memory_save / memory_search. Replaces the per-turn
// classifier — the LLM picks the slug now, not a side worker call.
// Empty when there's no accumulator yet.
func (t *chatTurn) renderKnownTopicsBlock() string {
	if t == nil || t.app == nil || t.app.DB == nil {
		return ""
	}
	topics := listAgentTopics(t.app.DB, t.user, t.agent.ID)
	if len(topics) == 0 {
		return ""
	}
	// Cap to the most-recently-used slugs (the tail — recordAgentTopic keeps the
	// list recency-ordered). Dropped slugs still work; the agent just mints or
	// reuses them without the hint. 0 = show all.
	if maxN := TuneInt(tuneKnownTopicsMax); maxN > 0 && len(topics) > maxN {
		topics = topics[len(topics)-maxN:]
	}
	var b strings.Builder
	b.WriteString("\n\n## Known topics\n\n")
	saveVerb := "memory(save)"
	if unifiedMemoryEnabled() {
		saveVerb = "remember (findings)"
	}
	fmt.Fprintf(&b, "Snake_case slugs already used for %s in this agent's bucket. Reuse one when the current finding fits — picking a fresh slug for material that belongs alongside existing entries makes retrieval split across buckets that should be one. Mint a new slug only when the subject genuinely doesn't fit.\n\n", saveVerb)
	for _, name := range topics {
		b.WriteString("- ")
		b.WriteString(name)
		b.WriteString("\n")
	}
	return b.String()
}

// skillToolDefs builds the per-turn skill tools (read_skill,
// skill_knowledge_search, skill_knowledge_fetch_doc) gated on the agent's
// skill allowlist. All three are stateless one-shot calls in the agent's
// OWN context — no sub-agent, no activation. The search/fetch callbacks
// reuse the agent's own scoped knowledge tooling so a skill's collections
// search the same way the agent's corpus does; the per-turn deliveredSkills
// set dedupes instruction delivery across read_skill / search / triggers.
// Returns nil when skills are disabled or none are allowed.
func (t *chatTurn) skillToolDefs() []AgentToolDef {
	if t == nil || t.agent.DisableSkills || len(t.agent.AllowedSkills) == 0 {
		return nil
	}
	allowed := t.agent.AllowedSkills
	return []AgentToolDef{
		BuildReadSkillTool(t.udb, t.user, allowed, t.deliveredSkills),
		BuildSkillKnowledgeSearchTool(t.udb, t.user, allowed, t.deliveredSkills,
			func(skill SkillRecord, query string) string {
				res, _ := t.knowledgeToolDefScoped([]SkillRecord{skill}).Handler(map[string]any{"query": query})
				return res
			}),
		BuildSkillKnowledgeFetchDocTool(t.udb, t.user, allowed, t.deliveredSkills,
			func(skill SkillRecord, docID string) (string, error) {
				return t.fetchKnowledgeDocScoped([]SkillRecord{skill}).Handler(map[string]any{"doc_id": docID})
			}),
	}
}

// roundShapePreamble returns the universal "How this round works"
// framework block that sits ABOVE the agent persona for agents
// without their own detailed phased rhythm. Builder skips it
// entirely — its phases govern the rhythm.
//
// Position above-persona is deliberate: LLMs weight recent prompt
// content heavier, so the persona (more recent) reads as the
// authoritative voice. The preamble is reference material for when
// the persona is silent on a question.
func roundShapePreamble(maxSteps int) string {
	stepBudget := fmt.Sprintf("up to %d step%s", maxSteps, plural(maxSteps))
	return "## How this round works\n\n" +
		"Call tools inline (call → see result → call again → reply; multi-round is fine) or end the round with **ask_user / ask_user_form** (pause for input) or **plan_set** (hand off to fresh-context workers, " + stepBudget + ", min 2, research-style \"investigate A and B in parallel\" — not a wrapper for sequential tool calls). To reply, just write your answer as text; that ends the turn. There is no separate reply tool. The persona below wins on anything it addresses; this is the default otherwise.\n\n" +
		"**Before a tool call, write ONE short sentence in your own voice saying what you're about to do** — \"Let me grab that video.\" / \"Checking your calendar…\" / \"Pulling the latest numbers.\" The user sees it right away, so they're never left watching dead air while the tool runs. Keep it to a sentence. Do NOT write your actual ANSWER before a tool call — that's not the place for it, and you'd just repeat yourself once the result is back. Save the real answer for your final, tool-free reply AFTER you have the results.\n\n" +
		"**Delivering files.** Producer tools (image, video, screenshot_page, custom tools that save a file) write to your workspace and return the path — they do NOT auto-attach. To deliver, follow up with `workspace(action=\"attach\", path=\"<returned-path>\", cleanup=true)`. cleanup=true for one-shot deliveries, cleanup=false when the file is also work product. Multiple files in one turn is fine — chain one workspace(attach) per file.\n\n" +
		"Pure conversation (greetings, opinions, follow-ups already answered): just reply as text.\n\n"
}

// drainNotes pulls all queued interjections and persists them as
// user messages on the session (so reload sees them too). Returns
// the drained slice for the caller to fold into the next prompt.
// Empty when there's nothing queued or no queue at all.
func (t *chatTurn) drainNotes() []injectionNote {
	if t == nil || t.queue == nil {
		return nil
	}
	taken := t.queue.Drain()
	if len(taken) == 0 {
		return nil
	}
	if t.session != nil {
		now := time.Now()
		for _, n := range taken {
			t.session.Messages = append(t.session.Messages, ChatMessage{
				Role: "user", Content: n.Text, Created: now,
			})
		}
		if saved, err := saveChatSession(t.udb, *t.session); err == nil {
			*t.session = saved
		}
	}
	ids := make([]string, len(taken))
	for i, n := range taken {
		ids[i] = n.ID
	}
	// Tell the client to mark these interjection bubbles as consumed
	// (servitor's pattern — the framework runtime already tags the
	// bubbles with data-note-id on submit).
	t.sse.Send(map[string]any{
		"kind": "block",
		"type": "orchestrate_notes_consumed",
		"ids":  ids,
	})
	return taken
}

// notesContextBlock formats a drained slice for prepending to a
// round's user content. Returns empty string when nothing was drained
// so callers can append unconditionally.
func notesContextBlock(notes []injectionNote) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## User notes added mid-flight\n")
	b.WriteString("The user added the following notes after the turn started. Treat as additional context / direction:\n\n")
	for _, n := range notes {
		b.WriteString("- ")
		b.WriteString(strings.ReplaceAll(strings.TrimSpace(n.Text), "\n", " "))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}
