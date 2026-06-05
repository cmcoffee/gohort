// Agent-management tools — registered as ChatTools so any agent that
// includes them in AllowedTools (today: the seeded Chat agent) can
// create / read / update / clone / delete the calling user's agents.
// Ownership is enforced per tool: users can only mutate their own
// agents. Seed agents are visible to everyone but never mutable.
//
// Each tool implements SessionChatTool to read Username + DB off the
// ToolSession; Run (the no-session path) returns an error, since
// these tools don't make sense without authentication context.

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	// Legacy list_agents + get_agent registrations dropped. The
	// `agents` grouped tool (list/get/run actions) is the single
	// entry point for agent operations and is always-mounted in
	// chatTurn — old agent records that named "list_agents" or
	// "get_agent" in AllowedTools simply intersect to nothing for
	// those names; functionally they lose nothing because `agents`
	// covers the same capability.
	// (The structs + methods stay below for type identity; nothing
	// else references them.)
	// Authoring tools (create_agent / update_agent / clone_agent /
	// delete_agent) are NOT globally registered. Their structs exist
	// as Go types but they're invisible to RegisteredChatTools(),
	// FindChatTool, default-pool enumeration, and every other
	// registry-traversing path. The Builder seed agent's catalog
	// assembly imports them by symbol via builderAuthoringTools() —
	// no other agent can reach them.
}

// (list_agents and get_agent removed — collapsed into the `agents`
// grouped tool's list/get actions, which is chatTurn-bound and
// always-mounted. The standalone registrations and struct definitions
// had been kept for back-compat with old agent records that named
// them in AllowedTools; that intersection just drops the name now and
// the same capability remains via the grouped tool.)

// --- create_agent ---------------------------------------------------------

type createAgentTool struct{}

func (createAgentTool) Name() string                 { return "create_agent" }
func (createAgentTool) SingleFirePerBatch() bool     { return true }
func (createAgentTool) Desc() string {
	return "Create a new agent owned by the user. Returns the saved agent JSON with its assigned id. REQUIRED args: name, description, orchestrator_prompt, AND allowed_tools (an explicit tool allowlist). Pick the allowlist deliberately — a tight 4-10 tool set sharpens the agent's catalog and prevents off-task tool use. If the user genuinely wants every tool, pass [\"*\"] as the single element. Use after you've gathered requirements AND run a failure-mode pass on the design — for each named failure mode (ambiguous input, multi-result tools, empty results, conflicting evidence), the orchestrator_prompt should specify what the agent does. \"Pick the top result\" is a real choice for some agents (\"what's the weather\") and wrong for others (\"find this person\") — state it explicitly rather than leaving the default to the worker. The user can refine via update_agent later."
}
func (createAgentTool) Params() map[string]ToolParam {
	return agentMutationParams(false)
}
func (createAgentTool) Run(map[string]any) (string, error) {
	return "", errors.New("create_agent requires a session context")
}
func (createAgentTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.Username == "" || sess.DB == nil {
		return "", errors.New("create_agent requires authenticated session")
	}
	rec := agentRecordFromArgs(args)
	if strings.TrimSpace(rec.Name) == "" {
		return "", errors.New("name is required")
	}
	if strings.TrimSpace(rec.OrchestratorPrompt) == "" {
		return "", errors.New("orchestrator_prompt is required")
	}
	// allowed_tools is now required — picking a deliberate tool surface
	// is the difference between a focused agent and a fat one. The LLM
	// must explicitly state which tools the agent gets. If the user
	// genuinely wants every tool, pass ["*"] as the single element.
	if len(rec.AllowedTools) == 0 {
		return "", errors.New("allowed_tools is required — pick a tight allowlist (4-10 tool names) for the agent's actual job. If the user genuinely wants every tool, pass [\"*\"]")
	}
	if len(rec.AllowedTools) == 1 && rec.AllowedTools[0] == "*" {
		// Sentinel "*" = "everything" — the runner already treats
		// nil AllowedTools as the default pool, so map back.
		rec.AllowedTools = nil
	}
	// Auto-copy session tools into the new agent's Tools[]: any name in
	// allowed_tools that matches a session-pool draft is copied into
	// the agent's bundled tools so the agent owns its dependencies
	// (per-agent scope, independent of the originating session). Without
	// this, the LLM's "make a tool then make an agent that uses it"
	// flow saves an agent whose allowlist references a name that
	// vanishes at session end.
	copied := autoCopySessionToolsForAgent(sess, &rec)
	rec.ID = "" // force fresh assignment
	rec.Owner = sess.Username
	saved, err := saveAgent(sess.DB, rec)
	if err != nil {
		return "", err
	}
	// Stamp the session's authoring-in-progress slot so subsequent
	// create_*_tool calls can auto-default for_agent to this agent
	// without the LLM having to re-state it.
	if sess.ChatSessionID != "" {
		saveAuthoringInProgress(sess.DB, sess.ChatSessionID, saved.ID)
	}
	// Install each inline-bundled tool as a session draft so the LLM
	// can dispatch it BY NAME in the current session to verify before
	// declaring success. The canonical copy lives in saved.Tools; the
	// session draft is purely a verification handle and shadows out
	// when the user switches to the new agent.
	installedDrafts := 0
	if sess.ChatSessionID != "" && len(saved.Tools) > 0 {
		for i := range saved.Tools {
			if err := SaveSessionTempTool(sess.DB, sess.ChatSessionID, saved.Tools[i]); err != nil {
				Log("[orchestrate.agent_crud] session draft save failed for bundled tool %q: %v", saved.Tools[i].Name, err)
				continue
			}
			installedDrafts++
		}
	}
	b, _ := json.Marshal(saved)
	// Lead with a directive line so the LLM doesn't keep iterating
	// (e.g. asking the user a follow-up question after the agent's
	// already been created). The JSON after is for reference if the
	// model needs to cite specific fields in its summary.
	verifyHint := ""
	if installedDrafts > 0 {
		verifyHint = fmt.Sprintf(" Bundled %d tool(s) are also installed as drafts in THIS session so you can dispatch them by name to verify before ending the turn.", installedDrafts)
	}
	if copied > 0 {
		verifyHint += fmt.Sprintf(" Auto-copied %d session tool(s) into the agent so it owns its tool dependencies (the agent will keep working past this session).", copied)
	}
	return fmt.Sprintf(
		"AGENT_CREATED ok. id=%s name=%q.%s DONE — reply with a short summary of what was saved and END THE TURN. Do NOT call ask_user, create_agent, or any other tool after this.\n\nSaved record: %s",
		saved.ID, saved.Name, verifyHint, string(b),
	), nil
}

// autoCopySessionToolsForAgent scans rec.AllowedTools for names that
// match either (a) this session's session_temp_tools entries or
// (b) the user's persistent_temp_tools pool, and appends each as a
// copy into rec.Tools (deduped by name — pre-existing inline tools
// win, no overwrite). Returns the number of tools copied.
//
// Copy-always (not reference-by-name) is the contract for both
// source pools:
//   - Session tools die at session end, so they MUST be copied.
//   - Persistent tools could be referenced by name at runtime, but
//     that creates fragility: admin cleanup of the persistent pool
//     silently breaks downstream agents. Snapshotting at create/
//     update time makes agents self-contained — admin pool
//     management can't break them. Tradeoff: persistent-tool
//     updates don't auto-propagate; admin can run a "sync from
//     pool" action to pick up changes explicitly.
//
// Session takes precedence over persistent when both pools have a
// tool by the same name (the session version is what the LLM just
// authored / iterated on; persistent is the older approved copy).
func autoCopySessionToolsForAgent(sess *ToolSession, rec *AgentRecord) int {
	if sess == nil || len(rec.AllowedTools) == 0 {
		return 0
	}
	byName := make(map[string]*TempTool)
	// Persistent pool first; session overrides.
	if sess.Username != "" {
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			t := p.Tool
			byName[t.Name] = &t
		}
	}
	if sess.ChatSessionID != "" {
		for _, draft := range LoadSessionTempTools(sess.DB, sess.ChatSessionID) {
			t := draft
			byName[t.Name] = &t
		}
	}
	if len(byName) == 0 {
		return 0
	}
	already := make(map[string]bool, len(rec.Tools))
	for _, t := range rec.Tools {
		already[t.Name] = true
	}
	copied := 0
	for _, name := range rec.AllowedTools {
		if already[name] {
			continue
		}
		t, ok := byName[name]
		if !ok {
			continue
		}
		rec.Tools = append(rec.Tools, *t)
		already[name] = true
		copied++
		Log("[orchestrate.agent_crud] snapshotted tool %q into agent %q", name, rec.Name)
		// Dequeue from admin pending-review pool — this tool is now
		// owned by an agent record and doesn't need separate
		// promotion. No-op when the name isn't in the queue (e.g.
		// the tool came from the persistent pool which was already
		// admin-approved).
		if sess.Username != "" {
			DequeuePendingTempTool(sess.DB, sess.Username, name)
		}
		// Drop the session draft too — the tool is now owned by the
		// agent record, so the session-scoped copy is just stale
		// duplication that confuses the Session-tools UI and the
		// runtime loader's "already loaded" tracking. The persistent
		// pool already has its own dedup path (line 244-249); only
		// session drafts need explicit removal.
		if sess.ChatSessionID != "" {
			RemoveSessionTempTool(sess.DB, sess.ChatSessionID, name)
		}
	}
	return copied
}

// --- update_agent ---------------------------------------------------------

type updateAgentTool struct{}

func (updateAgentTool) Name() string             { return "update_agent" }
func (updateAgentTool) SingleFirePerBatch() bool { return true }
func (updateAgentTool) Desc() string {
	return "Update fields on an existing agent the user owns. Only fields you supply are changed; omitted fields stay as-is. Returns the saved agent JSON. Cannot mutate seed agents — use clone_agent first if the user wants to customize a starter."
}
func (updateAgentTool) Params() map[string]ToolParam {
	return agentMutationParams(true)
}
func (updateAgentTool) Run(map[string]any) (string, error) {
	return "", errors.New("update_agent requires a session context")
}
func (updateAgentTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.Username == "" || sess.DB == nil {
		return "", errors.New("update_agent requires authenticated session")
	}
	id := strings.TrimSpace(fmt.Sprint(args["id"]))
	if id == "" {
		return "", errors.New("id is required")
	}
	existing, ok := loadAgent(sess.DB, id)
	if !ok {
		return "", fmt.Errorf("agent %q not found", id)
	}
	if existing.Owner != sess.Username {
		return "", fmt.Errorf("agent %q is not yours — clone it first to customize", id)
	}
	mergeAgentArgs(&existing, args)
	// Auto-copy session tools into the agent when allowed_tools picks up
	// a name that exists in this session's draft pool — same rule as
	// create_agent. Lets the LLM extend an agent's tool set across the
	// "make a session tool, then add it to my agent" flow without the
	// reference going stale at session end.
	copied := autoCopySessionToolsForAgent(sess, &existing)
	saved, err := saveAgent(sess.DB, existing)
	if err != nil {
		return "", err
	}
	// If tools[] was in the update, install each as a session draft so
	// the LLM can dispatch them by name to verify before ending the turn.
	// Same testability principle as create_agent.
	installedDrafts := 0
	if _, supplied := args["tools"]; supplied && sess.ChatSessionID != "" {
		for i := range saved.Tools {
			if err := SaveSessionTempTool(sess.DB, sess.ChatSessionID, saved.Tools[i]); err != nil {
				Log("[orchestrate.agent_crud] session draft save failed for updated tool %q: %v", saved.Tools[i].Name, err)
				continue
			}
			installedDrafts++
		}
	}
	verifyHint := ""
	if installedDrafts > 0 {
		verifyHint = fmt.Sprintf(" %d tool(s) on this agent are also installed as drafts in THIS session so you can dispatch them by name to verify.", installedDrafts)
	}
	if copied > 0 {
		verifyHint += fmt.Sprintf(" Auto-copied %d session tool(s) into the agent so it owns its tool dependencies.", copied)
	}
	b, _ := json.Marshal(saved)
	return fmt.Sprintf(
		"AGENT_UPDATED ok. id=%s name=%q.%s DONE — reply with a short summary of what changed and END THE TURN. Do NOT call ask_user, update_agent, or any other tool after this.\n\nSaved record: %s",
		saved.ID, saved.Name, verifyHint, string(b),
	), nil
}

// --- clone_agent ----------------------------------------------------------

type cloneAgentTool struct{}

func (cloneAgentTool) Name() string             { return "clone_agent" }
func (cloneAgentTool) SingleFirePerBatch() bool { return true }
func (cloneAgentTool) Desc() string {
	return "Clone an agent the user can see (their own or a seed) into a fresh owned copy. Returns the new agent JSON. Use when the user wants to customize a starter without affecting the original."
}
func (cloneAgentTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"id":   {Type: "string", Description: "Source agent id."},
		"name": {Type: "string", Description: "Optional new name. Defaults to source name + \" (copy)\"."},
	}
}
func (cloneAgentTool) Run(map[string]any) (string, error) {
	return "", errors.New("clone_agent requires a session context")
}
func (cloneAgentTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.Username == "" || sess.DB == nil {
		return "", errors.New("clone_agent requires authenticated session")
	}
	id := strings.TrimSpace(fmt.Sprint(args["id"]))
	if id == "" {
		return "", errors.New("id is required")
	}
	newName := strings.TrimSpace(fmt.Sprint(args["name"]))
	// LLM-initiated clone preserves the source's OwnedBy (no promotion).
	// Promotion (sub-agent → top-level) is a deliberate user choice
	// available only via the chat UI's Clone button prompt.
	saved, err := cloneAgent(sess.DB, id, sess.Username, newName, false)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(saved)
	return fmt.Sprintf(
		"AGENT_CLONED ok. id=%s name=%q. DONE — reply with a short summary of what was cloned and END THE TURN. Do NOT call ask_user, clone_agent, or any other tool after this.\n\nSaved record: %s",
		saved.ID, saved.Name, string(b),
	), nil
}

// --- delete_agent ---------------------------------------------------------

type deleteAgentTool struct{}

func (deleteAgentTool) Name() string             { return "delete_agent" }
func (deleteAgentTool) SingleFirePerBatch() bool { return true }
func (deleteAgentTool) Desc() string {
	return "Delete an owned agent and all of its sessions. CONFIRM with the user before calling — this is irreversible. Seed agents cannot be deleted."
}
func (deleteAgentTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"id": {Type: "string", Description: "Agent id to delete (must be owned by the user)."},
	}
}
func (deleteAgentTool) NeedsConfirm() bool { return true }
func (deleteAgentTool) Run(map[string]any) (string, error) {
	return "", errors.New("delete_agent requires a session context")
}
func (deleteAgentTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.Username == "" || sess.DB == nil {
		return "", errors.New("delete_agent requires authenticated session")
	}
	id := strings.TrimSpace(fmt.Sprint(args["id"]))
	if id == "" {
		return "", errors.New("id is required")
	}
	if err := deleteAgent(sess.DB, id, sess.Username); err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"deleted":%q}`, id), nil
}

// --- shared param + merge helpers -----------------------------------------

// agentMutationParams returns the ToolParam schema shared by
// create_agent and update_agent. The includeID flag adds the id
// parameter (required for update, omitted for create).
func agentMutationParams(includeID bool) map[string]ToolParam {
	params := map[string]ToolParam{
		"name":                {Type: "string", Description: "Human-readable agent name."},
		"description":         {Type: "string", Description: "One-sentence summary."},
		"orchestrator_prompt": {Type: "string", Description: "System prompt for the thinking LLM (talks to user, decomposes work, briefs the worker per step, synthesizes)."},
		"plan_guidance":       {Type: "string", Description: "Optional decomposition-style nudge appended to orchestrator prompt."},
		"rules":               {Type: "string", Description: "Optional standing rules, one per line, applied to every turn."},
		"allowed_tools": {
			Type:        "array",
			Description: "Optional explicit allowlist of worker tool names. Empty = default pool (read + network).",
			Items:       &ToolParam{Type: "string"},
		},
		"max_plan_steps":    {Type: "integer", Description: "Optional 1-12. Default 5."},
		"max_worker_rounds": {Type: "integer", Description: "Optional 1-20. Default 5."},
		"think_budget":      {Type: "integer", Description: "Optional. Max thinking tokens (thinking_budget_tokens) per LLM call for this agent. 0 (default) = inherit the deployment default (4096). The admin global budget is a hard ceiling, so this can only LOWER the budget (for snappier agents) — setting it above the admin ceiling has no effect. Only applies when thinking is on."},
		"gap_check":         {Type: "boolean", Description: "Optional. When true, the runner runs a structural-gap review pass after the plan finishes (research-style quality bar). Default false."},
		"disable_explicit":  {Type: "boolean", Description: "Optional. When true, turn off the Explicit Memory layer — the always-in-prompt structured facts (store_fact / list_facts / forget_fact + the prompt block). Set for impersonal agents that shouldn't accumulate any always-in-prompt state (KB readers, one-shot transformers, stateless tools). Composes orthogonally with disable_inferred. Default false."},
		"disable_inferred":  {Type: "boolean", Description: "Optional. When true, turn off the Reference Memory layer — the vector-grown store the LLM writes to via memory_save. memory_save / memory_search / memory_forget stripped from catalog; derived chunks excluded from recall. Use for agents that should answer from authoritative sources only and never grow their own fuzzy recall (KB readers, compliance bots). The per-turn Clean toggle on the chat surface is the same switch scoped to a single turn. Default false."},
		"memory_mode":       {Type: "string", Description: "Optional. Selects the Explicit Memory framing: \"agent\" (default, narrow) or \"chatbot\" (broader). agent mode = store_fact for generalized lessons only (design principles, recurring gotchas, \"X fails do Y\" rules); specific API details + working approaches go in Reference Memory via memory_save. chatbot mode = same lessons PLUS user personalization (name, preferences, recurring details) PLUS conversation-coherence notes. Use chatbot mode for general-purpose conversational agents; agent mode for task-focused agents (Builder, KB readers, research bots). No-op when disable_explicit is true."},
		"allow_private_mode": {Type: "boolean", Description: "Optional. When true, the public /agents/<slug>/ surface exposes a Private toggle that drops internet-capability tools per turn. Off by default — only opt in for agents where local-only operation is meaningful."},
		"force_private":      {Type: "boolean", Description: "Optional. When true, the agent is LOCKED into Private mode permanently: every turn drops network-capability tools (web_search, fetch_url, browse_page, agents-dispatch, etc.) regardless of the user toggle, and the public Private toggle is hidden from the UI. Use for compliance bots, confidential-doc assistants, family-facing agents — anywhere the agent should NEVER reach the network. Overrides allow_private_mode when both are true."},
		"disable_skills":     {Type: "boolean", Description: "Optional. When true, the skills classifier is fully suppressed for this agent — no skill ever activates, no skill addendum is appended to the prompt, no skill_knowledge corpus chunks are injected, no skill-attached tools enter the catalog. Set for agents whose job is to faithfully report a specific source (KB readers, doc-Q&A, compliance look-ups). The per-turn Clean toggle also suppresses skills regardless of this flag. Default false (skills auto-activate)."},
		"allowed_skills":     {Type: "array", Description: "Strict allowlist of skill IDs the classifier may consider for this agent. Every skill is opt-in per agent — nothing fires unless its ID is here. Empty (default) = no skills active. Pass IDs from skill_def(action=list). Use the Knowledge surface in chat to curate this interactively instead.", Items: &ToolParam{Type: "string"}},
		"hidden": {Type: "boolean", Description: "Optional. When true, this agent is hidden from OTHER agents' \"Available agents\" prompt block and the agents(run) dispatch handler refuses to call it — UNLESS a specific caller has it on their allowed_dispatch_targets list. Use for personal agents the user wants to chat with directly but doesn't want the fleet routing to. Default false (public to all callers)."},
		"allowed_dispatch_targets": {Type: "array", Description: "Optional dispatch allowlist of agent IDs. Empty (default) = this agent can call ANY non-hidden agent in the fleet. Non-empty = restricted: this agent can ONLY call the listed targets (Hidden status of targets is ignored — explicit pick wins, so this also wires through to hidden specialists). Use to scope a specialist agent to only its relevant collaborators.", Items: &ToolParam{Type: "string"}},
		"attached_collections": {Type: "array", Description: "Optional list of Document Collection IDs to merge into this agent's RAG search. Each attached collection's chunks surface alongside the agent's own knowledge during recall — same many-to-many shape as skills, but bound at the agent layer (no skill activation required). Use to give a topic-narrow agent a curated reference corpus without authoring a skill: one collection of K8s docs, another of internal runbooks, etc. Pass collection IDs from the Collections surface. Default empty.", Items: &ToolParam{Type: "string"}},
		"attached_pipelines": {Type: "array", Description: "Optional list of pipeline IDs (from pipeline action=list) to bolt onto this agent. Each attached pipeline becomes its OWN callable tool on the agent (named run_<pipeline>), so the agent can run that saved multi-stage workflow on demand without going through the generic pipeline tool. Use to give an agent a repeatable staged capability — e.g. a research agent with a \"decompose → investigate → synthesize\" pipeline always at hand. Pass pipeline IDs; author the pipeline first with the pipeline tool if it doesn't exist yet. Default empty.", Items: &ToolParam{Type: "string"}},
		"triggers": {Type: "array", Description: "Optional substring/glob patterns matched against the user message each turn. On a match the host agent gets a salient per-turn nudge to dispatch to THIS agent FIRST (more effective than the static catalog for domains the host has priors in, e.g. law/medicine). Author SPECIFIC patterns the domain's questions actually contain — e.g. a criminal-law agent: \"penal code\", \"PC \", \"felony\", \"misdemeanor\", \"charged with\", \"sentencing\". Loose patterns over-fire and train the host to ignore the hint. Empty = agent still in the catalog, just no per-turn nudge.", Items: &ToolParam{Type: "string"}},
		"owned_by":           {Type: "string", Description: "Optional ID of a parent agent that owns this one as a sub-agent. Two effects: (1) when the parent is deleted, this agent is cascade-deleted along with its sessions/memory/knowledge — no orphan sub-agents; (2) the parent can dispatch to this sub-agent via agents(action=\"run\") without needing it in allowed_dispatch_targets — ownership IS the dispatch link. Combine with hidden=true to keep the sub-agent out of the global fleet menu while still reachable from its parent. Use for the sub-agent / specialist pattern: a parent orchestrator agent owns several focused capability sub-agents (e.g. OSINT parent → BusinessResearcher / CourtResearcher sub-agents). Pass the parent agent's ID."},
		"ingest_attachments": {Type: "boolean", Description: "Optional. When true, the extracted text from any paperclip- or intake-form-uploaded document (PDF, DOCX, text) is ALSO ingested into the agent's vector knowledge store under topic=\"attachments\". Future sessions retrieve it via knowledge_search. Set for document-Q&A, resume-reviewer, contract-analyzer style agents where the upload is meant to be referenced repeatedly. Default false."},
		"think":              {Type: "string", Description: "Optional reasoning mode override: \"on\", \"off\", or \"auto\". CREATE defaults: top-level agents default \"on\" (reasoning helps planners / synthesizers / conversational agents), sub-agents (owned_by set) default \"off\" (fast focused specialists where reasoning just adds latency). UPDATE preserves the stored value when omitted. Use \"on\" for sub-agents that decompose / plan / synthesize; \"off\" for lookup-shaped specialists, transformers, routers; \"auto\" to let the framework route default decide."},
		"intake_form": {
			Type:        "array",
			Description: "Optional intake form. When set, the chat shows this form on the first turn of every new session, hides the text input while the form is visible, packs the values into a markdown user message, AND uploads any file fields as attachments (PDFs/DOCX get text-extracted server-side, images go to vision). Use for agents that always need structured input up front (\"resume reviewer\" needs the resume PDF, \"marketing copy\" needs company + audience + deadline, \"pick a topic\" needs a button choice). Each entry is an object: {name, label, type, placeholder, help, required, options, allow_other}. type: \"text\" (default), \"textarea\", \"select\", \"checklist\", \"number\", \"file\", \"button\". options: array of strings — for select these are single-choice dropdown choices; for checklist these are multi-pick checkboxes (selected values get joined comma-separated in the packed markdown, e.g. \"**Topics:** ai, healthcare\"); for button these are the buttons rendered (clicking any one submits the form immediately with that label as the value, no separate submit click needed). Use \"select\" when the user picks exactly one from a list; \"checklist\" when the user can pick any combination (\"which topics interest you?\", \"which formats do you want?\"); \"button\" when the user picks from a small set of mutually-exclusive starting points. allow_other (checklist only): when true, render an extra \"Other:\" row with a free-text input — selected + non-empty text gets joined into the same comma list as any picked options (\"**Topics:** ai, healthcare, my own thing\"). Set when the curated list can't cover every reasonable answer and you want the user to be able to add their own. Leave intake_form empty/omit for normal chat-first agents.",
			Items:       &ToolParam{Type: "object"},
		},
		"tools": {
			Type:        "array",
			Description: "Optional agent-scoped tools that auto-load whenever this agent runs. Use for bespoke shell/api tools tied to THIS agent's job — e.g. a research agent can ship its own \"lookup_company\" api tool without polluting the user-wide tool pool. Each entry is a TempTool object: {name, description, params, mode, command_template, body_template, credential, method}. mode is \"shell\" or \"api\". Two agents can each carry a \"lookup_company\" with different configs — agent scope keeps them independent. Don't ALSO add these names to allowed_tools; agent-scoped tools attach automatically. For a multi-stage workflow, don't author a tool — create a declarative pipeline (the pipeline tool) and attach it via attached_pipelines.",
			Items:       &ToolParam{Type: "object"},
		},
		"evals": {
			Type:        "array",
			Description: "Optional saved test cases for the eval harness. Each entry is a EvalCase: {name, prompt, must_include[], must_not_include[], judge_prompt, notes}. The harness runs each case as an independent fresh session, captures the agent's reply, and grades: must_include / must_not_include are case-insensitive substring checks; judge_prompt (optional) is a criterion an LLM judge grades the reply against (PASS/FAIL + reason). Use to lock in expected behaviors before a prompt edit so you can catch regressions: a chat-style agent's \"asks_clarifying\" case might have must_include=[\"?\"] and judge_prompt=\"the reply asks a clarifying question rather than guessing\". Run via POST .../api/agents/{id}/eval.",
			Items:       &ToolParam{Type: "object"},
		},
		// exposed / public_name are intentionally OMITTED here — they're
		// admin-only overrides set via the agent editor. Keeping them out
		// of the LLM-facing CRUD surface stops a self-managing agent from
		// accidentally publishing or rebranding itself.
	}
	if includeID {
		params["id"] = ToolParam{Type: "string", Description: "Agent id (from list_agents)."}
	}
	return params
}

// agentRecordFromArgs builds an AgentRecord from tool args. Used by
// create_agent (fresh record). update_agent uses mergeAgentArgs
// instead so omitted fields stay as-is.
func agentRecordFromArgs(args map[string]any) AgentRecord {
	rec := AgentRecord{
		Name:               strings.TrimSpace(stringArg(args, "name")),
		Description:        strings.TrimSpace(stringArg(args, "description")),
		OrchestratorPrompt: stringArg(args, "orchestrator_prompt"),
		PlanGuidance:       stringArg(args, "plan_guidance"),
		Rules:              strings.TrimSpace(stringArg(args, "rules")),
		AllowedTools:       stringSliceFromArgs(args, "allowed_tools"),
		MaxPlanSteps:       intFromArgs(args, "max_plan_steps"),
		MaxWorkerRounds:    intFromArgs(args, "max_worker_rounds"),
		ThinkBudget:        intFromArgs(args, "think_budget"),
		IntakeForm:         intakeFormFromArgs(args),
		Tools:              agentScopedToolsFromArgs(args),
		Evals:              evalsFromArgs(args),
	}
	if v, ok := args["gap_check"].(bool); ok {
		rec.GapCheck = v
	}
	if v, ok := args["disable_explicit"].(bool); ok {
		rec.DisableExplicit = v
	}
	if v, ok := args["disable_inferred"].(bool); ok {
		rec.DisableInferred = v
	}
	if v := strings.TrimSpace(stringArg(args, "memory_mode")); v != "" {
		rec.MemoryMode = v
	}
	if v, ok := args["allow_private_mode"].(bool); ok {
		rec.AllowPrivateMode = v
	}
	if v, ok := args["force_private"].(bool); ok {
		rec.ForcePrivate = v
	}
	if v, ok := args["disable_skills"].(bool); ok {
		rec.DisableSkills = v
	}
	if _, ok := args["allowed_skills"]; ok {
		rec.AllowedSkills = stringSliceFromArgs(args, "allowed_skills")
	}
	if v, ok := args["hidden"].(bool); ok {
		rec.Hidden = v
	}
	if _, ok := args["allowed_dispatch_targets"]; ok {
		rec.AllowedDispatchTargets = stringSliceFromArgs(args, "allowed_dispatch_targets")
	}
	if _, ok := args["attached_collections"]; ok {
		rec.AttachedCollections = stringSliceFromArgs(args, "attached_collections")
	}
	if _, ok := args["attached_pipelines"]; ok {
		rec.AttachedPipelines = stringSliceFromArgs(args, "attached_pipelines")
	}
	if _, ok := args["triggers"]; ok {
		rec.Triggers = stringSliceFromArgs(args, "triggers")
	}
	if v, ok := args["owned_by"]; ok && v != nil {
		rec.OwnedBy = strings.TrimSpace(fmt.Sprint(v))
	}
	if v, ok := args["ingest_attachments"].(bool); ok {
		rec.IngestAttachments = v
	}
	// Think tri-state on CREATE: explicit value wins; otherwise pick
	// the right default based on the agent's role. Top-level agents are
	// usually conversational / planning surfaces that benefit from
	// reasoning; sub-agents are usually fast focused specialists where
	// reasoning adds latency without improving the answer. Author can
	// override either default by passing think explicitly.
	rec.Think = parseThinkArg(args, rec.OwnedBy != "")
	return rec
}

// parseThinkArg reads the "think" arg as a tri-state ("on" / "off" /
// "auto") and returns the canonical string to store on the record.
// When the arg is missing, returns the default for the agent's role:
// sub-agents (isSubAgent=true) default to "off"; top-level agents
// default to "on". Returns "" only for explicit "auto" — the empty
// string is the "let the route decide" signal at the call site.
func parseThinkArg(args map[string]any, isSubAgent bool) string {
	v, ok := args["think"]
	if !ok || v == nil {
		if isSubAgent {
			return "off"
		}
		return "on"
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(v))) {
	case "on", "true", "yes", "1":
		return "on"
	case "off", "false", "no", "0":
		return "off"
	case "auto", "":
		return ""
	}
	if isSubAgent {
		return "off"
	}
	return "on"
}

// mergeAgentArgs overlays only the fields present in args onto rec.
// Missing or zero-value-only fields are left untouched. Used by
// update_agent so callers can patch one field at a time.
func mergeAgentArgs(rec *AgentRecord, args map[string]any) {
	if v, ok := args["name"]; ok && v != nil {
		rec.Name = strings.TrimSpace(fmt.Sprint(v))
	}
	if v, ok := args["description"]; ok && v != nil {
		rec.Description = strings.TrimSpace(fmt.Sprint(v))
	}
	if v, ok := args["orchestrator_prompt"]; ok && v != nil {
		rec.OrchestratorPrompt = fmt.Sprint(v)
	}
	if v, ok := args["plan_guidance"]; ok && v != nil {
		rec.PlanGuidance = fmt.Sprint(v)
	}
	if v, ok := args["rules"]; ok && v != nil {
		rec.Rules = strings.TrimSpace(fmt.Sprint(v))
	}
	if _, ok := args["allowed_tools"]; ok {
		rec.AllowedTools = stringSliceFromArgs(args, "allowed_tools")
	}
	if v, ok := args["max_plan_steps"]; ok && v != nil {
		rec.MaxPlanSteps = coerceInt(v)
	}
	if v, ok := args["max_worker_rounds"]; ok && v != nil {
		rec.MaxWorkerRounds = coerceInt(v)
	}
	if v, ok := args["think_budget"]; ok && v != nil {
		rec.ThinkBudget = coerceInt(v)
	}
	if v, ok := args["gap_check"].(bool); ok {
		rec.GapCheck = v
	}
	if v, ok := args["disable_explicit"].(bool); ok {
		rec.DisableExplicit = v
	}
	if v, ok := args["disable_inferred"].(bool); ok {
		rec.DisableInferred = v
	}
	if v := strings.TrimSpace(stringArg(args, "memory_mode")); v != "" {
		rec.MemoryMode = v
	}
	if v, ok := args["allow_private_mode"].(bool); ok {
		rec.AllowPrivateMode = v
	}
	if v, ok := args["force_private"].(bool); ok {
		rec.ForcePrivate = v
	}
	if v, ok := args["disable_skills"].(bool); ok {
		rec.DisableSkills = v
	}
	if _, ok := args["allowed_skills"]; ok {
		rec.AllowedSkills = stringSliceFromArgs(args, "allowed_skills")
	}
	if v, ok := args["hidden"].(bool); ok {
		rec.Hidden = v
	}
	if _, ok := args["allowed_dispatch_targets"]; ok {
		rec.AllowedDispatchTargets = stringSliceFromArgs(args, "allowed_dispatch_targets")
	}
	if _, ok := args["attached_collections"]; ok {
		rec.AttachedCollections = stringSliceFromArgs(args, "attached_collections")
	}
	if _, ok := args["attached_pipelines"]; ok {
		rec.AttachedPipelines = stringSliceFromArgs(args, "attached_pipelines")
	}
	if _, ok := args["triggers"]; ok {
		rec.Triggers = stringSliceFromArgs(args, "triggers")
	}
	if v, ok := args["owned_by"]; ok && v != nil {
		rec.OwnedBy = strings.TrimSpace(fmt.Sprint(v))
	}
	if v, ok := args["ingest_attachments"].(bool); ok {
		rec.IngestAttachments = v
	}
	// Think on UPDATE: only touch when the caller passed think
	// explicitly. Omitted = preserve whatever's stored (the author's
	// last decision). "auto" still flips to nil — that's the explicit
	// "go back to route default" intent.
	if _, ok := args["think"]; ok {
		rec.Think = parseThinkArg(args, rec.OwnedBy != "")
	}
	if _, ok := args["intake_form"]; ok {
		rec.IntakeForm = intakeFormFromArgs(args)
	}
	if _, ok := args["tools"]; ok {
		rec.Tools = agentScopedToolsFromArgs(args)
	}
	if _, ok := args["evals"]; ok {
		rec.Evals = evalsFromArgs(args)
	}
}

// evalsFromArgs coerces the LLM-supplied `evals` array into
// []EvalCase. JSON-roundtrip handles type normalization; bad
// entries (missing name or prompt) get logged and skipped.
func evalsFromArgs(args map[string]any) []EvalCase {
	raw, ok := args["evals"]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		Log("[orchestrate.agent_crud] evals marshal failed: %v", err)
		return nil
	}
	var cases []EvalCase
	if err := json.Unmarshal(data, &cases); err != nil {
		Log("[orchestrate.agent_crud] evals unmarshal failed: %v", err)
		return nil
	}
	out := make([]EvalCase, 0, len(cases))
	for _, c := range cases {
		c.Name = strings.TrimSpace(c.Name)
		c.Prompt = strings.TrimSpace(c.Prompt)
		if c.Name == "" || c.Prompt == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// agentScopedToolsFromArgs coerces the LLM-supplied `tools` array
// into []TempTool. Round-trips through JSON so loose typing on the
// LLM side gets normalized to the strict struct shape. Bad entries
// (missing name, etc.) get logged and skipped; the rest still save.
func agentScopedToolsFromArgs(args map[string]any) []TempTool {
	raw, ok := args["tools"]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		Log("[orchestrate.agent_crud] tools marshal failed: %v", err)
		return nil
	}
	var tools []TempTool
	if err := json.Unmarshal(data, &tools); err != nil {
		Log("[orchestrate.agent_crud] tools unmarshal failed: %v", err)
		return nil
	}
	// Drop entries without a name — they'd fail AppendTempTool at
	// load time anyway and leaving them in pollutes the record.
	out := make([]TempTool, 0, len(tools))
	for _, t := range tools {
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}

// intakeFormFromArgs coerces the LLM-supplied intake_form payload
// into an IntakeFormSpec. Accepts three shapes (mirrors
// IntakeFormSpec.UnmarshalJSON):
//
//   - []any (the natural shape: an array of {name, label, type, ...}
//     objects). Each object is JSON-roundtripped through IntakeField
//     so the LLM's loose typing gets normalized.
//   - string (JSON text containing the array). Some models pass the
//     whole spec as a string when they're unsure of nested schema.
//   - nil / missing → empty form.
//
// Any conversion failure is logged and treated as "no intake form"
// so a malformed payload doesn't break the create/update call.
func intakeFormFromArgs(args map[string]any) IntakeFormSpec {
	raw, ok := args["intake_form"]
	if !ok || raw == nil {
		return nil
	}
	// Roundtrip through JSON so IntakeFormSpec.UnmarshalJSON handles
	// the shape variants uniformly. Cheap; the form is small.
	data, err := json.Marshal(raw)
	if err != nil {
		Log("[orchestrate.agent_crud] intake_form marshal failed: %v", err)
		return nil
	}
	var spec IntakeFormSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		preview := string(data)
		if len(preview) > 200 {
			preview = preview[:200] + "…"
		}
		Log("[orchestrate.agent_crud] intake_form unmarshal failed: %v (raw=%s)", err, preview)
		return nil
	}
	return spec
}

// stringArg is a defensive fmt.Sprint that tolerates non-string
// values (numbers, bools) the LLM occasionally emits even when the
// schema asks for string.
func stringArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func stringSliceFromArgs(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	switch s := v.(type) {
	case []string:
		out := make([]string, 0, len(s))
		for _, x := range s {
			if t := strings.TrimSpace(x); t != "" {
				out = append(out, t)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(s))
		for _, x := range s {
			if str, ok := x.(string); ok {
				if t := strings.TrimSpace(str); t != "" {
					out = append(out, t)
				}
			}
		}
		return out
	case string:
		// LLM fallback shapes: smaller models occasionally emit the
		// array as a JSON-encoded string ('["a","b"]') or as a
		// comma-separated string ("a, b, c"). Both render as a plain
		// textarea when we return nil; the user perceives this as
		// "the multi-select didn't show." Coerce here so the array
		// reaches the renderer regardless of the wrapping shape.
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return nil
		}
		// Try JSON array first.
		if strings.HasPrefix(trimmed, "[") {
			var arr []any
			if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
				out := make([]string, 0, len(arr))
				for _, x := range arr {
					if str, ok := x.(string); ok {
						if t := strings.TrimSpace(str); t != "" {
							out = append(out, t)
						}
					}
				}
				if len(out) > 0 {
					return out
				}
			}
		}
		// Fall back to comma-separated.
		if strings.Contains(trimmed, ",") {
			parts := strings.Split(trimmed, ",")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				if t := strings.TrimSpace(p); t != "" {
					out = append(out, t)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
		// Single value — wrap in a one-element slice.
		return []string{trimmed}
	}
	return nil
}

func intFromArgs(args map[string]any, key string) int {
	v, ok := args[key]
	if !ok || v == nil {
		return 0
	}
	return coerceInt(v)
}

func coerceInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		out := 0
		started := false
		for _, c := range n {
			if c < '0' || c > '9' {
				if started {
					return out
				}
				continue
			}
			out = out*10 + int(c-'0')
			started = true
		}
		if started {
			return out
		}
	}
	return 0
}
