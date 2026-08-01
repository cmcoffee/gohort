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
	"sort"
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

func (createAgentTool) Name() string             { return "create_agent" }
func (createAgentTool) SingleFirePerBatch() bool { return true }
func (createAgentTool) Desc() string {
	return "Create a new agent owned by the user. Returns the saved agent JSON with its assigned id. REQUIRED: name, description, orchestrator_prompt, allowed_tools. Pick the allowlist deliberately — a tight 4-10 tool set sharpens the catalog and prevents off-task tool use; pass [\"*\"] only if the user genuinely wants everything. Call after gathering requirements AND running a failure-mode pass: for each mode (ambiguous input, multi-result tools, empty results, conflicting evidence) the orchestrator_prompt should say what the agent does — \"pick the top result\" is right for \"what's the weather\" and wrong for \"find this person\". Refine later via update_agent."
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
	rec.AllowedTools = normalizeAllowedTools(rec.AllowedTools)
	// LLM-supplied inline tools commit to the user's unified store AFTER the
	// record is saved (scoping a store row needs the assigned agent ID) — the
	// record no longer carries embedded tool copies.
	inlineTools := agentScopedToolsFromArgs(args)
	rec.ID = "" // force fresh assignment
	rec.Owner = sess.Username
	// Dispatched-Builder authoring: a Fleet parent (e.g. Chat) dispatched Builder
	// to mint this agent. Stamp it as an owned, parent-inheriting sub-agent held
	// for approval — saved, but kept OUT of service (excluded from dispatch / run
	// / listing) until the parent owner approves it from the Authorizations pane.
	dispatchedBuild := strings.TrimSpace(sess.DispatchParentAgentID) != ""
	if dispatchedBuild {
		rec.OwnedBy = sess.DispatchParentAgentID
		rec.InheritParentTools = true
		rec.PendingApproval = true
	}
	saved, err := saveAgent(sess.DB, rec)
	if err != nil {
		return "", err
	}
	// Commit the inline tools as store rows scoped to the new agent.
	committedTools := make([]TempTool, 0, len(inlineTools))
	for _, t := range inlineTools {
		if err := bundleAgentToolByID(sess.DB, sess.Username, saved.ID, t); err != nil {
			Log("[orchestrate.agent_crud] could not scope inline tool %q to agent %q: %v", t.Name, saved.Name, err)
			continue
		}
		committedTools = append(committedTools, t)
	}
	// Auto-copy session drafts: any name in allowed_tools that matches a
	// session-pool draft is committed to the store scoped to this agent, so
	// the agent owns its dependencies independent of the originating session.
	// Without this, the LLM's "make a tool then make an agent that uses it"
	// flow saves an agent whose allowlist references a name that vanishes at
	// session end.
	copiedTools := autoCopySessionToolsForAgent(sess, &saved)
	copied := len(copiedTools)
	committedTools = append(committedTools, copiedTools...)
	if dispatchedBuild {
		// Queue the parent owner's approval. Agent holds the created id; the
		// console's activate handler flips PendingApproval off on approve.
		auth := SaveAuthorization(RootDB, Authorization{
			Owner:  sess.Username,
			Action: "activate_sub_agent",
			Agent:  saved.ID,
			Brief:  fmt.Sprintf("Activate %q — sub-agent Builder drafted for %s. Inherits the parent's read-only tools; nothing consequential.", saved.Name, sess.DispatchParentAgentID),
		})
		// Surface it as an inline Approve/Deny card in the conversation so the
		// owner decides right here — not only in the Permissions pane. No-op on a
		// non-interactive run (nil callback).
		if sess.ApprovalPrompt != nil {
			sess.ApprovalPrompt(auth.ID, saved.Name, auth.Brief)
		}
	}
	// Stamp the session's authoring-in-progress slot so subsequent
	// create_*_tool calls can auto-default for_agent to this agent
	// without the LLM having to re-state it.
	if sess.ChatSessionID != "" {
		saveAuthoringInProgress(sess.DB, sess.ChatSessionID, saved.ID)
	}
	// Register each inline-bundled tool IN MEMORY on this session so the LLM can
	// dispatch it BY NAME to verify before declaring success. The canonical copy
	// is the store row scoped to the new agent; this is purely a verification
	// handle for the authoring turn.
	//
	// In-memory, not a persisted session draft: the authoring agent is not the
	// agent that owns the tool, so it has no kit to load it from — but it only
	// needs it while it is verifying, and a persisted copy outlived the turn as
	// a ghost in a parallel scope that then had to be shadowed and pruned.
	installedDrafts := 0
	for i := range committedTools {
		t := committedTools[i]
		sess.RemoveTempTool(t.Name)
		if err := sess.AppendTempTool(&t); err != nil {
			Log("[orchestrate.agent_crud] in-session registration failed for bundled tool %q: %v", t.Name, err)
			continue
		}
		installedDrafts++
	}
	b, _ := json.Marshal(saved)
	toolWarn := unresolvedToolsWarning(sess, &saved)
	if dispatchedBuild {
		// Held-for-approval path: the sub-agent is saved but not live, so there
		// is nothing for Builder to verify by dispatch — it stays gated until
		// the parent owner approves. Report and end the turn.
		return fmt.Sprintf(
			"AGENT_DRAFTED ok. id=%s name=%q — saved but HELD FOR APPROVAL. It will not run until the owner approves it in the Authorizations pane; on approval it goes live as a sub-agent of %s and inherits that parent's read-only tools.%s DONE — reply with a one-line summary of what you drafted and END THE TURN. Do NOT call ask_user or create_agent again.\n\nSaved record: %s",
			saved.ID, saved.Name, sess.DispatchParentAgentID, toolWarn, string(b),
		), nil
	}
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
	verifyHint += toolWarn
	// Surface what this agent may now DO as an inline card, so the powers it
	// just gained are reviewed in the conversation that granted them instead of
	// on a later trip to the Permissions pane.
	emitPrivilegeCard(sess, saved, committedTools)
	// Announce the focus move. Creating an agent silently re-points the
	// authoring-focus slot (above), which is the implicit target of a later
	// add_tool. When the new agent is a HELPER for something else — the
	// create-two-sub-agents-then-tool-up-the-parent flow — that silent move
	// sends the parent's tool onto the last sub-agent instead, with no error.
	// Say where focus landed and how to override it, so the next add_tool is a
	// deliberate choice rather than a guess about hidden state.
	focusNote := fmt.Sprintf(
		" Authoring focus is now %q — a subsequent add_tool with no `agent` argument attaches THERE. To tool up a different agent (e.g. the parent this was built for), pass agent=\"<name or id>\" explicitly.",
		saved.Name,
	)
	return fmt.Sprintf(
		"AGENT_CREATED ok. id=%s name=%q.%s%s DONE — reply with a short summary of what was saved and END THE TURN. Do NOT call ask_user, create_agent, or any other tool after this.\n\nSaved record: %s",
		saved.ID, saved.Name, verifyHint, focusNote, string(b),
	), nil
}

// autoCopySessionToolsForAgent scans rec.AllowedTools for names that match
// this session's session_temp_tools drafts and commits each to the user's
// unified tool store scoped to the agent (rec.ID must already be assigned —
// call it after saveAgent). Returns the tools it committed.
//
// Session drafts die at session end, so they MUST be committed. Pool-sourced
// names need no copy anymore: shared rows in the unified store are already
// visible to every agent, including this one — the old snapshot-onto-the-
// record contract is obsolete along with the embedded copies it protected.
//
// Names already in the store are left alone (a shared row already serves the
// agent; a scoped row was just authored inline and pre-existing inline tools
// win, no overwrite).
func autoCopySessionToolsForAgent(sess *ToolSession, rec *AgentRecord) []TempTool {
	if sess == nil || rec == nil || strings.TrimSpace(rec.ID) == "" {
		return nil
	}
	// An "everything" surface (empty / nil AllowedTools — including the "*"
	// sentinel that create_agent already collapsed to nil) STILL needs its
	// freshly-authored tools committed. Otherwise the LLM's documented "make a
	// tool, then make an agent that uses it" flow silently loses the tool when
	// the agent is given the full pool: the session draft dies at session end
	// and was never kept anywhere durable.
	everything := len(rec.AllowedTools) == 0

	byName := make(map[string]*TempTool)
	draftNames := []string{}
	if sess.ChatSessionID != "" {
		for _, draft := range LoadSessionTempTools(sess.DB, sess.ChatSessionID) {
			t := draft
			byName[t.Name] = &t
			draftNames = append(draftNames, t.Name)
		}
	}
	if len(byName) == 0 {
		return nil
	}
	// Which names to commit: a specific allowlist commits its named drafts; an
	// everything surface commits every draft authored this session.
	names := rec.AllowedTools
	if everything {
		sort.Strings(draftNames) // deterministic commit order
		names = draftNames
	}
	var copied []TempTool
	for _, name := range names {
		t, ok := byName[name]
		if !ok {
			continue
		}
		if _, exists := UserToolByName(sess.DB, sess.Username, name); exists {
			continue // already in the store (shared or just-committed inline)
		}
		if err := bundleAgentToolByID(sess.DB, sess.Username, rec.ID, *t); err != nil {
			Log("[orchestrate.agent_crud] could not scope session tool %q to agent %q: %v", name, rec.Name, err)
			continue
		}
		copied = append(copied, *t)
		Log("[orchestrate.agent_crud] committed session tool %q scoped to agent %q", name, rec.Name)
		// Dequeue from admin pending-review pool — this tool is now
		// committed to an agent's kit and doesn't need separate
		// promotion. No-op when the name isn't in the queue.
		if sess.Username != "" {
			DequeuePendingTempTool(sess.DB, sess.Username, name)
		}
		// Drop the session draft too — the tool is now in the unified
		// store, so the session-scoped copy is just stale duplication
		// that confuses the Session-tools UI and the runtime loader's
		// "already loaded" tracking.
		if sess.ChatSessionID != "" {
			RemoveSessionTempTool(sess.DB, sess.ChatSessionID, name)
		}
	}
	return copied
}

// agentHasNoTools reports whether an agent's allowlist is the explicit
// "none" sentinel — it can produce text and nothing else. Distinct from
// an EMPTY allowlist, which means the opposite (the default pool).
func agentHasNoTools(rec AgentRecord) bool {
	if len(rec.AllowedTools) != 1 {
		return false
	}
	return strings.TrimSpace(rec.AllowedTools[0]) == noToolsSentinel
}

// normalizeAllowedTools collapses the "*" everything-sentinel to nil,
// which is how every downstream reader spells "the default pool"
// (GetAgentToolsWithSession, autoCopySessionToolsForAgent, and
// unresolvedAllowedTools all key off len(AllowedTools) == 0).
//
// This has to run on EVERY write path, not just create. A literal
// ["*"] surviving into the record is the worst possible outcome: it is
// a strict allowlist of length one naming a tool that cannot exist, so
// the agent comes up with ZERO tools — the exact inverse of what "*"
// asks for — and unresolvedAllowedTools skips "*" when it hunts for
// typos, so nothing warns. update_agent used to do exactly that while
// reporting success.
func normalizeAllowedTools(list []string) []string {
	for _, n := range list {
		if strings.TrimSpace(n) == "*" {
			return nil
		}
	}
	return list
}

// unresolvedAllowedTools returns the allowed_tools names that won't resolve
// to any tool the agent can actually reach at run time: a registered tool,
// the user's unified tool store (shared rows plus agent-scoped rows — which
// already includes anything committed by autoCopySessionToolsForAgent), or
// this session's credential tools (fetch_url_<cred>, plus the legacy
// call_<cred> alias the runtime accepts). Names that resolve to nothing are
// silently dropped at dispatch (see GetAgentToolsWithSession's per-name
// fallback), so the agent comes up with a smaller catalog than the author
// asked for and nobody is told. Surfacing them lets the author catch a typo
// or a tool they THOUGHT they created but didn't — the exact trap that turned
// one fat-fingered tool_def into a 15-minute debugging session. An empty/nil
// allowlist (the "*" everything surface) has nothing to validate.
func unresolvedAllowedTools(sess *ToolSession, rec *AgentRecord) []string {
	if len(rec.AllowedTools) == 0 {
		return nil
	}
	known := make(map[string]bool)
	if sess != nil && sess.Username != "" {
		// Flattened namespace: the agent's authored tools are store rows, not
		// embedded record copies — any row counts (shared rows are visible to
		// every agent; scoped rows were committed for this agent above).
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			known[p.Tool.Name] = true
		}
	}
	if sess != nil {
		for _, td := range Secure().BuildTools(sess) {
			known[td.Tool.Name] = true
			// Runtime also accepts the legacy call_<cred> alias for fetch_url_<cred>.
			if strings.HasPrefix(td.Tool.Name, "fetch_url_") {
				known["call_"+strings.TrimPrefix(td.Tool.Name, "fetch_url_")] = true
			}
		}
	}
	var missing []string
	for _, n := range rec.AllowedTools {
		name := strings.TrimSpace(n)
		switch {
		case name == "" || name == "*":
			continue
		case strings.HasPrefix(name, "from_client."): // per-user desktop tools, resolved at run time
			continue
		case known[name]:
			continue
		}
		if _, ok := FindChatTool(name); ok {
			continue
		}
		missing = append(missing, name)
	}
	return missing
}

// unresolvedToolsWarning renders the trailing warning clause for a create/
// update success message, or "" when every allowed_tools name resolved.
func unresolvedToolsWarning(sess *ToolSession, rec *AgentRecord) string {
	missing := unresolvedAllowedTools(sess, rec)
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf(
		" ⚠ WARNING: these allowed_tools entries match no known tool and were dropped (typo, or a tool you haven't actually created yet): %s. The agent will NOT have them. Create the tool first (tool_def), then update_agent to add it.",
		strings.Join(missing, ", "),
	)
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
	// LOCK — no editing another agent's sub-agent (see agentMutationLock).
	if msg := agentMutationLock(existing, sess); msg != "" {
		return "", errors.New(msg)
	}
	mergeAgentArgs(&existing, args)
	// LLM-supplied inline tools commit via the unified store (scoped to this
	// agent) after the record saves — mergeAgentArgs no longer writes them
	// onto the record.
	var inlineTools []TempTool
	if v, ok := args["tools"]; ok && v != nil {
		inlineTools = agentScopedToolsFromArgs(args)
	}
	// Auto-copy session tools into the agent when allowed_tools picks up
	// a name that exists in this session's draft pool — same rule as
	// create_agent. Lets the LLM extend an agent's tool set across the
	// "make a session tool, then add it to my agent" flow without the
	// reference going stale at session end.
	copiedTools := autoCopySessionToolsForAgent(sess, &existing)
	copied := len(copiedTools)
	saved, err := saveAgent(sess.DB, existing)
	if err != nil {
		return "", err
	}
	for _, t := range inlineTools {
		if err := bundleAgentToolByID(sess.DB, sess.Username, saved.ID, t); err != nil {
			Log("[orchestrate.agent_crud] could not scope inline tool %q to agent %q: %v", t.Name, saved.Name, err)
		}
	}
	// If tools[] was in the update, register each in memory so the LLM can
	// dispatch them by name to verify before ending the turn. Same testability
	// principle as create_agent, and the same reason it isn't persisted: the
	// canonical copy is the store row scoped to the target agent, and this
	// handle is only needed for the authoring turn.
	installedDrafts := 0
	for i := range inlineTools {
		t := inlineTools[i]
		sess.RemoveTempTool(t.Name)
		if err := sess.AppendTempTool(&t); err != nil {
			Log("[orchestrate.agent_crud] in-session registration failed for updated tool %q: %v", t.Name, err)
			continue
		}
		installedDrafts++
	}
	verifyHint := ""
	if installedDrafts > 0 {
		verifyHint = fmt.Sprintf(" %d tool(s) on this agent are also installed as drafts in THIS session so you can dispatch them by name to verify.", installedDrafts)
	}
	if copied > 0 {
		verifyHint += fmt.Sprintf(" Auto-copied %d session tool(s) into the agent so it owns its tool dependencies.", copied)
	}
	verifyHint += unresolvedToolsWarning(sess, &saved)
	// An update can widen what an agent may do as easily as a create can —
	// same card, same reason.
	emitPrivilegeCard(sess, saved, append(append([]TempTool{}, inlineTools...), copiedTools...))
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
	// A clone copies the source's grants verbatim, which is exactly the case
	// where they go unexamined — show them.
	emitPrivilegeCard(sess, saved, nil)
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
	// LOCK — an agent may only delete a sub-agent IT owns (target.OwnedBy ==
	// caller). Another agent's sub-agent is off-limits to tools; only its owner
	// or the user (via the dashboard) can remove it. Prevents one agent from
	// deleting another's. The human dashboard path (deleteAgent direct) is
	// unrestricted.
	if target, ok := loadAgent(sess.DB, id); ok {
		if msg := agentMutationLock(target, sess); msg != "" {
			return "", errors.New(msg)
		}
	}
	if err := deleteAgent(sess.DB, id, sess.Username); err != nil {
		return "", err
	}
	return fmt.Sprintf(`{"deleted":%q}`, id), nil
}

// agentMutationLock gates tool-initiated delete/update of an agent: an agent may
// only mutate a sub-agent it owns (OwnedBy == its dispatch-parent caller) or an
// unowned/top-level agent it is authoring in this same flow. Touching ANOTHER
// agent's sub-agent is rejected — only its owner or the human can. Returns "" to
// allow, or a refusal message. The human dashboard never calls this (it deletes
// via deleteAgent directly), so it stays unrestricted.
func agentMutationLock(target AgentRecord, sess *ToolSession) string {
	// Explicit per-agent lock — the user marked this agent protected, so NO agent
	// may edit or delete it; only the human (dashboard/editor) can.
	if target.Locked {
		return fmt.Sprintf("can't modify %q — it's locked; only the user can change it (from the agent editor)", target.ID)
	}
	caller := strings.TrimSpace(sess.DispatchParentAgentID)
	if target.OwnedBy != "" && target.OwnedBy != caller {
		return fmt.Sprintf("can't modify %q — it belongs to another agent; only its owner or the user (from the dashboard) can change it", target.ID)
	}
	return ""
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
			Description: "Explicit allowlist of worker tool names. REQUIRED on create — a deliberate 4-10 tool set, or [\"*\"] for everything. Omit on update to leave it unchanged. An empty stored list runs the default pool (read + network).",
			Items:       &ToolParam{Type: "string"},
		},
		"max_plan_steps":           {Type: "integer", Description: fmt.Sprintf("Optional 1-12. Default %d.", defaultMaxPlanSteps)},
		"max_worker_rounds":        {Type: "integer", Description: fmt.Sprintf("Optional 1-20. Default %d.", defaultMaxWorkerRounds)},
		"think_budget":             {Type: "integer", Description: "Max thinking tokens per LLM call; applies only when thinking is on. 0 (default) = deployment default (4096). The admin global budget is a hard ceiling, so this can only LOWER it."},
		"lead_model":               {Type: "boolean", Description: "When true, MAIN reasoning (plan + synthesis) escalates to the lead/precision LLM; per-step workers stay on the worker model. Ignored when no distinct lead is configured, or when force_private or the Private toggle is on. Default false."},
		"gap_check":                {Type: "boolean", Description: "When true, the runner runs a structural-gap review pass after the plan finishes (research-style quality bar). Default false."},
		"disable_explicit":         {Type: "boolean", Description: rewriteMemoryToolNames("Turns off Explicit Memory (store_fact / list_facts / forget_fact + the always-in-prompt facts block). For agents that should hold no standing state. Orthogonal to disable_inferred. Default false.")},
		"disable_inferred":         {Type: "boolean", Description: rewriteMemoryToolNames("Turns off Reference Memory: memory_save / memory_search / memory_forget stripped from the catalog, derived chunks excluded from recall. For agents that must answer from authoritative sources only. The per-turn Clean toggle is this switch scoped to one turn. Default false.")},
		"memory_mode":              {Type: "string", Description: rewriteMemoryToolNames("Explicit Memory framing: \"agent\" (default) or \"chatbot\". agent = store_fact holds generalized lessons only; specifics go to Reference Memory via memory_save. chatbot = those PLUS user personalization and conversation-coherence notes. chatbot for conversational agents, agent for task-focused ones. No-op when disable_explicit is true.")},
		"enable_notes":             {Type: "boolean", Description: "Turns on the Working-notes layer: one bounded, agent-REWRITABLE block of running state, always in prompt, plus the update_notes tool. Unlike store_fact (append-only durable rules), notes are rewritten wholesale as task state changes. For long-running conversational/project agents. Default false."},
		"seed_notes":               {Type: "string", Description: "Initial Working-notes text (requires enable_notes). Rewritable from the first turn; stays the durable fallback until the first update_notes. Under 1500 characters."},
		"allow_private_mode":       {Type: "boolean", Description: "Exposes a per-turn Private toggle that drops internet-capability tools. Default false."},
		"force_private":            {Type: "boolean", Description: "Locks the agent into Private mode: every turn drops network-capability tools (web_search, fetch_url, dispatch, …) regardless of the user toggle, and the toggle is hidden. Overrides allow_private_mode. Default false."},
		"disable_skills":           {Type: "boolean", Description: "Fully suppresses skills: no activation, no prompt addendum, no skill_knowledge chunks, no skill-attached tools. For agents that must faithfully report one source. The per-turn Clean toggle also suppresses skills. Default false."},
		"allowed_skills":           {Type: "array", Description: "Strict allowlist of skill IDs the classifier may consider. Skills are opt-in per agent; empty (default) = none active. IDs from skill_def(action=list).", Items: &ToolParam{Type: "string"}},
		"hidden":                   {Type: "boolean", Description: "When true, hidden from other agents' \"Available agents\" block and refused by agents(run) — unless a caller lists it in allowed_dispatch_targets. Default false."},
		"allowed_dispatch_targets": {Type: "array", Description: "Dispatch allowlist of agent IDs. Empty (default) = may call any non-hidden agent. Non-empty = ONLY these, hidden or not — the explicit pick wins, so it reaches hidden specialists too.", Items: &ToolParam{Type: "string"}},
		"attached_collections":     {Type: "array", Description: "Document Collection IDs merged into this agent's RAG recall — a curated reference corpus without authoring a skill. Bound at the agent layer, no activation needed. IDs from the Collections surface. Default empty.", Items: &ToolParam{Type: "string"}},
		"attached_pipelines":       {Type: "array", Description: "Pipeline IDs (pipeline action=list). Each becomes its own callable tool here (run_<pipeline>), so a saved multi-stage workflow is on hand without the generic pipeline tool. Author the pipeline first. Default empty.", Items: &ToolParam{Type: "string"}},
		"recall_hints":             {Type: "boolean", Description: "Each turn surfaces a short scored list of the agent's OWN knowledge relevant to the message — pointers (title + doc_id for fetch_knowledge_doc), not content. Needs a real corpus. Default false."},
		"triggers":                 {Type: "array", Description: "Substring/glob patterns matched against each user message. On a match the host agent gets a per-turn nudge to dispatch HERE first. Author SPECIFIC patterns the domain's questions actually contain (criminal law: \"penal code\", \"felony\", \"sentencing\") — loose ones over-fire and train the host to ignore the hint. Empty = in the catalog, no nudge.", Items: &ToolParam{Type: "string"}},
		"owned_by":                 {Type: "string", Description: "Parent agent ID, making this a sub-agent: deleting the parent cascade-deletes this agent (sessions/memory/knowledge included), and the parent may dispatch to it without an allowed_dispatch_targets entry — ownership IS the dispatch link. Pair with hidden=true to keep it out of the global fleet menu."},
		"ingest_attachments":       {Type: "boolean", Description: "Extracted text from uploaded documents (PDF/DOCX/text) is ALSO ingested into the agent's knowledge store under topic=\"attachments\", searchable in later sessions. For document-Q&A agents whose uploads are referenced repeatedly. Default false."},
		"think":                    {Type: "string", Description: "Reasoning override: \"on\", \"off\", or \"auto\" (the route decides). Create defaults: top-level \"on\", sub-agents (owned_by set) \"off\"; update keeps the stored value when omitted. \"on\" for planners/synthesizers, \"off\" for lookups, transformers, routers."},
		"intake_form": {
			Type:        "array",
			Description: "Intake form shown on the first turn of every new session (chat input hidden until submitted). Values pack into a markdown user message; file fields upload as attachments (PDF/DOCX text-extracted, images to vision). Each entry: {name, label, type, placeholder, help, required, options, allow_other}. type: \"text\" (default), \"textarea\", \"select\" (one), \"checklist\" (many, comma-joined), \"number\", \"file\", \"button\" (submits immediately with the label as value). options feeds select/checklist/button; allow_other (checklist only) adds an \"Other:\" free-text row. Omit for chat-first agents.",
			Items:       &ToolParam{Type: "object"},
		},
		"tools": {
			Type:        "array",
			Description: "Agent-scoped tools that auto-load whenever this agent runs — bespoke shell/api tools for THIS agent's job, kept out of the user-wide pool (two agents can carry same-named tools with different configs). Each entry a TempTool: {name, description, params, mode (\"shell\"|\"api\"), command_template, body_template, credential, method}. Do NOT also list these in allowed_tools; they attach automatically. For a multi-stage workflow use attached_pipelines instead.",
			Items:       &ToolParam{Type: "object"},
		},
		"evals": {
			Type:        "array",
			Description: "Saved test cases for the eval harness. Each EvalCase: {name, prompt, must_include[], must_not_include[], must_call_tools[], must_not_call_tools[], stub_results{} (tool→canned result), judge_prompt, notes}. Each runs as a fresh session: case-insensitive substring checks on the reply; tool checks against the ACTUAL call trace (catches narrated-but-never-emitted calls); judge_prompt an optional LLM-judged criterion. STUB is the default — nothing real fires. Run via POST .../api/agents/{id}/eval?runs=30 (?live=1 non-consequential for real, ?live=all everything).",
			Items:       &ToolParam{Type: "object"},
		},
		// exposed / public_name are intentionally OMITTED here — they're
		// admin-only overrides set via the agent editor. Keeping them out
		// of the LLM-facing CRUD surface stops a self-managing agent from
		// accidentally publishing or rebranding itself.
	}
	if includeID {
		params["id"] = ToolParam{Type: "string", Description: "Agent id (from agents(action=\"list\"))."}
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
		// Tools deliberately NOT set: LLM-supplied inline tools commit to the
		// unified store scoped to the agent (see create_agent, post-save) —
		// the record no longer embeds tool copies.
		Evals: evalsFromArgs(args),
		// The hook set a new agent starts on. Inert until a guardrail is
		// authored (resolveGuardrailHooks returns nil with no rules), so this
		// costs a brand-new agent nothing — it just decides what happens the
		// first time someone writes a rule.
		GuardrailHooks: defaultNewAgentGuardrailHooks(),
		// Inert until a rule is authored, like the hooks above — this only
		// decides what happens the first time a check cannot reach a verdict.
		GuardrailFailClosed: defaultNewAgentFailClosed,
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
	if v, ok := args["enable_notes"].(bool); ok {
		rec.EnableNotes = v
	}
	if _, ok := args["seed_notes"]; ok {
		rec.SeedNotes = strings.TrimSpace(stringArg(args, "seed_notes"))
	}
	if v, ok := args["allow_private_mode"].(bool); ok {
		rec.AllowPrivateMode = v
	}
	if v, ok := args["force_private"].(bool); ok {
		rec.ForcePrivate = v
	}
	if v, ok := args["lead_model"].(bool); ok {
		rec.LeadModel = v
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
	if v, ok := args["recall_hints"].(bool); ok {
		rec.RecallHints = v
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
// Used by update_agent so callers can patch one field at a time.
//
// Presence semantics (uniform across scalar, slice, and object fields):
// an OMITTED key or an explicit `null` value is a no-op — the stored
// value is left untouched. This honors update_agent's contract ("only
// fields you supply are changed") even when a model re-emits the schema
// and fills unchanged fields with null. An explicit EMPTY value ([] for
// slices, "" for strings) is an intentional clear and IS applied. The
// `v != nil` guard on every block is what draws that line — without it a
// stray `"allowed_tools": null` from the LLM silently wiped an agent's
// tool grant, collapsing it to the default pool.
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
	if v, ok := args["allowed_tools"]; ok && v != nil {
		rec.AllowedTools = normalizeAllowedTools(stringSliceFromArgs(args, "allowed_tools"))
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
	if v, ok := args["enable_notes"].(bool); ok {
		rec.EnableNotes = v
	}
	if _, ok := args["seed_notes"]; ok {
		rec.SeedNotes = strings.TrimSpace(stringArg(args, "seed_notes"))
	}
	if v, ok := args["allow_private_mode"].(bool); ok {
		rec.AllowPrivateMode = v
	}
	if v, ok := args["force_private"].(bool); ok {
		rec.ForcePrivate = v
	}
	if v, ok := args["lead_model"].(bool); ok {
		rec.LeadModel = v
	}
	if v, ok := args["disable_skills"].(bool); ok {
		rec.DisableSkills = v
	}
	if v, ok := args["allowed_skills"]; ok && v != nil {
		rec.AllowedSkills = stringSliceFromArgs(args, "allowed_skills")
	}
	if v, ok := args["hidden"].(bool); ok {
		rec.Hidden = v
	}
	if v, ok := args["recall_hints"].(bool); ok {
		rec.RecallHints = v
	}
	if v, ok := args["allowed_dispatch_targets"]; ok && v != nil {
		rec.AllowedDispatchTargets = stringSliceFromArgs(args, "allowed_dispatch_targets")
	}
	if v, ok := args["attached_collections"]; ok && v != nil {
		rec.AttachedCollections = stringSliceFromArgs(args, "attached_collections")
	}
	if v, ok := args["attached_pipelines"]; ok && v != nil {
		rec.AttachedPipelines = stringSliceFromArgs(args, "attached_pipelines")
	}
	if v, ok := args["triggers"]; ok && v != nil {
		rec.Triggers = stringSliceFromArgs(args, "triggers")
	}
	if v, ok := args["owned_by"]; ok && v != nil {
		rec.OwnedBy = strings.TrimSpace(fmt.Sprint(v))
	}
	if v, ok := args["ingest_attachments"].(bool); ok {
		rec.IngestAttachments = v
	}
	// Think on UPDATE: only touch when the caller passed think
	// explicitly and non-null. Omitted OR null = preserve whatever's
	// stored (the author's last decision). "auto" still flips to nil —
	// that's the explicit "go back to route default" intent.
	if v, ok := args["think"]; ok && v != nil {
		rec.Think = parseThinkArg(args, rec.OwnedBy != "")
	}
	if v, ok := args["intake_form"]; ok && v != nil {
		rec.IntakeForm = intakeFormFromArgs(args)
	}
	// "tools" deliberately NOT merged onto the record: inline tools commit to
	// the unified store scoped to the agent (see update_agent, post-save).
	if v, ok := args["evals"]; ok && v != nil {
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
			switch e := x.(type) {
			case string:
				if t := strings.TrimSpace(e); t != "" {
					out = append(out, t)
				}
			case map[string]any:
				// Object-shaped elements — smaller models emit options as
				// {label}/{value}/{name} objects (mirroring SelectOption /
				// IntakeField shapes they see elsewhere). Silently dropping
				// them rendered "the multi-select never populates": the step
				// showed checkboxes' worth of nothing. Take the first
				// conventional key that yields text.
				for _, k := range []string{"label", "value", "name", "option", "text"} {
					if t := strings.TrimSpace(stringArg(e, k)); t != "" {
						out = append(out, t)
						break
					}
				}
			default:
				// Numbers etc. — render as text rather than vanish.
				if t := strings.TrimSpace(fmt.Sprintf("%v", x)); t != "" && t != "<nil>" {
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
