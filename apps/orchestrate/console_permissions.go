package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleConsoleApprovals lists pending delegations awaiting the user's
// approval (the authorizations queue).
func (T *OrchestrateApp) handleConsoleApprovals(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type apprRow struct {
		Agent     string `json:"agent"`
		Action    string `json:"action"`
		Requested string `json:"requested"`
		ID        string `json:"_id"`
	}
	out := []apprRow{}
	for _, a := range ListAuthorizations(RootDB, user) {
		agent, action := approvalDisplay(udb, user, a)
		out = append(out, apprRow{
			Agent:     agent,
			Action:    action,
			Requested: a.Requested.Local().Format("Jan 2 3:04 PM"),
			ID:        a.ID,
		})
	}
	writeJSON(w, out)
}

// approvalDisplay maps a pending Authorization to its (who, detail) display
// pair, applying the same per-action relabeling the approvals queue shows.
// Shared by the approvals list and the combined Permissions view.
func approvalDisplay(udb Database, user string, a Authorization) (who, detail string) {
	who, detail = a.Agent, a.Brief
	switch a.Action {
	case "send_message":
		who = operatorApprovalRecipient(user, a)
		detail = "text: " + a.Text
	case "converse_contact":
		who = operatorApprovalRecipient(user, a)
		detail = "converse toward: " + a.Brief
	case "activate_sub_agent":
		if rec, found := loadAgent(udb, a.Agent); found {
			who = rec.Name
		}
		detail = "activate sub-agent: " + a.Brief
	case buildAgentAction:
		// The REQUESTER is who's asking; the build itself is the detail.
		if a.FromAgent != "" {
			if rec, found := loadAgent(udb, a.FromAgent); found {
				who = rec.Name
			} else {
				who = a.FromAgent
			}
		}
		detail = "build a sub-agent: " + a.Brief
	case "bind_thread":
		who = operatorApprovalRecipient(user, a)
		detail = "bind 1:1 thread so the agent can read replies"
		if a.Brief != "" {
			detail = "bind 1:1 thread (" + a.Brief + ")"
		}
	case "autonomous_tool":
		if rec, found := loadAgent(udb, a.Agent); found {
			who = rec.Name
		}
		detail = "run \"" + a.Brief + "\" unattended (refused on a scheduled fire; approving pre-authorizes it)"
	case "scope_tool":
		if rec, found := loadAgent(udb, a.Agent); found {
			who = rec.Name
		}
		// Says plainly that nothing is waiting on this. The old copy ("keep X in
		// this agent's own kit") read as a permission being withheld, when the
		// agent has been calling the tool all along — that IS the evidence.
		detail = "Suggestion: \"" + a.Brief + "\" looks like part of this agent's kit — scope it so it's always in the catalog instead of loaded on demand. It keeps working either way."
	case orphanMemoryRefAction:
		if rec, found := loadAgent(udb, a.Agent); found {
			who = rec.Name
		}
		// Says what is actually wrong — the agent's memory is now false — and
		// that nothing is blocked. It runs fine; it just runs believing it can
		// call something it cannot.
		detail = "Suggestion: this agent's memory still refers to \"" + a.Brief + "\", whose last agent was deleted, so nothing can call it. Approving re-homes the tool here so the memory is true again. Or edit the memory from the agent's Memory pane — nothing is blocked either way."
	}
	return who, detail
}

// handleConsolePermissions is the combined "Permissions" page: pending approval
// requests AND the standing grants you've already given, on one page. Pending
// rows come first (they need action) and carry _pending; granted rows carry
// _granted, so the table's conditional row actions show Approve/Always/Deny on
// the former and Revoke on the latter. The pinned rail badge counts _pending.
func (T *OrchestrateApp) handleConsolePermissions(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Field order matters: the card layout uses the FIRST non-underscore field
	// as the title (Who), then Detail (mono box), Status (pill), Requested.
	type permRow struct {
		Who       string `json:"Who"`
		Detail    string `json:"Detail"`
		Status    string `json:"Status,omitempty"`
		Requested string `json:"Requested,omitempty"`
		ID        string `json:"_id"`
		Pending   bool   `json:"_pending,omitempty"`
		Managed   bool   `json:"_managed,omitempty"`    // a standing policy row (segmented control + Remove)
		Policy    string `json:"_policy,omitempty"`     // allow | ask | block (the segmented state)
		OneShot   bool   `json:"_oneshot,omitempty"`    // one-time decision (Approve/Deny only) — no "Always" grant makes sense (e.g. activating a drafted sub-agent, which the approval consumes)
		Suggested bool   `json:"_suggestion,omitempty"` // an OFFER, not a request: nothing is blocked on it (see approvalIsSuggestion)
	}
	out := []permRow{}
	// Zone 1 — live pending requests (a decision is blocked on the user), then
	// Zone 1b — suggestions. Both come out of the Authorizations store, but only
	// the first kind is holding anything up, so they are collected separately:
	// requests stay on top and own the rail badge, offers sit below them and are
	// counted nowhere. Mixing them made routine curation look like a permission.
	var suggested []permRow
	for _, a := range ListAuthorizations(RootDB, user) {
		who, detail := approvalDisplay(udb, user, a)
		if approvalIsSuggestion(a.Action) {
			suggested = append(suggested, permRow{
				Who: who, Detail: detail, Status: "Suggested",
				Requested: a.Requested.Local().Format("Jan 2 3:04 PM"),
				ID:        a.ID, Suggested: true,
			})
			continue
		}
		out = append(out, permRow{
			Who: who, Detail: detail, Status: "Pending",
			Requested: a.Requested.Local().Format("Jan 2 3:04 PM"),
			ID:        a.ID, Pending: true,
			// Activating a drafted sub-agent is a ONE-TIME decision — approving it
			// makes the agent live and the authorization disappears, so "Always"
			// has no meaning. Present just Approve / Deny for it.
			OneShot: a.Action == "activate_sub_agent",
		})
	}
	out = append(out, suggested...)
	// Zone 2 — standing permission policy per subject (Always allow / Needs
	// approval / Blocked, + Remove).
	for _, e := range ListDelegationPolicies(RootDB, user) {
		name := e.Target
		if rec, found := loadAgent(udb, e.Target); found && rec.Name != "" {
			name = rec.Name
		}
		out = append(out, permRow{Who: name, Detail: "Agent delegation", ID: "agent:" + e.Target, Managed: true, Policy: e.Policy})
	}
	for _, e := range ListContactPolicies(RootDB, user) {
		out = append(out, permRow{Who: e.Target, Detail: "Contact messaging", ID: "contact:" + e.Target, Managed: true, Policy: e.Policy})
	}
	// Zone 3 — autonomous-run tool grants: the tools you "Always allowed" a
	// scheduled/standing agent to run unattended (AutoApproveTools). Surfaced so
	// the grant isn't invisible after approval — Remove (or "Needs approval")
	// revokes it, and the tool re-queues on its next unattended fire.
	for _, ag := range listAgents(udb, user) {
		for _, tool := range ag.AutoApproveTools {
			out = append(out, permRow{
				Who:     firstNonEmptyStr(ag.Name, ag.ID),
				Detail:  "Autonomous tool: " + tool,
				ID:      "autotool:" + ag.ID + ":" + tool,
				Managed: true, Policy: "allow",
			})
		}
	}
	writeJSON(w, out)
}

// removeAutoApproveTool revokes a standing autonomous-tool grant from an agent.
func removeAutoApproveTool(udb Database, agentID, tool string) {
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		return
	}
	var kept []string
	changed := false
	for _, t := range rec.AutoApproveTools {
		if t == tool {
			changed = true
			continue
		}
		kept = append(kept, t)
	}
	if changed {
		rec.AutoApproveTools = kept
		if _, err := saveAgent(udb, rec); err != nil {
			Log("[console.perm] revoke autonomous tool %s/%s failed: %v", agentID, tool, err)
		}
	}
}

// handleConsolePrivileges is the write side of the inline privileges card: it
// applies the same edits the Permissions pane and the agent editor make, to the
// same AgentRecord, for an agent the CALLER owns.
//
// The card is a shortcut, never an authority. The model can emit a card; only a
// request carrying the owner's session can change anything, and the record is
// loaded from that user's own store — an id belonging to someone else simply
// isn't there. POST body:
//
//	{"agent_id":"…","tools":{"send_message":"allow","call_x":"ask"},
//	 "flags":{"fleet":true,"exposed":false}}
//
// Tool policy is binary: "allow" pre-authorizes the tool for unattended runs
// (AutoApproveTools), anything else revokes so it queues again on its next
// fire. Flags map to the capability toggles by their form field names.
func (T *OrchestrateApp) handleConsolePrivileges(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		AgentID string            `json:"agent_id"`
		Tools   map[string]string `json:"tools"`
		Flags   map[string]bool   `json:"flags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	udb := UserDB(T.DB, user)
	rec, found := loadAgent(udb, strings.TrimSpace(body.AgentID))
	if !found {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	// A sub-agent's posture is the framework's to decide (it carries its
	// parent's authority and is never published on its own), so the card shows
	// those rows locked and the server refuses them too — a locked control that
	// only the UI enforces is not a control.
	if strings.TrimSpace(rec.OwnedBy) != "" && len(body.Flags) > 0 {
		http.Error(w, "a sub-agent's capabilities follow its parent", http.StatusConflict)
		return
	}
	for tool, policy := range body.Tools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(policy), "allow") {
			rec.AutoApproveTools = appendUnique(rec.AutoApproveTools, tool)
			continue
		}
		var kept []string
		for _, t := range rec.AutoApproveTools {
			if t != tool {
				kept = append(kept, t)
			}
		}
		rec.AutoApproveTools = kept
	}
	for field, on := range body.Flags {
		switch strings.TrimSpace(field) {
		case "fleet":
			rec.Fleet = on
		case "author":
			rec.Author = on
		case "exposed":
			rec.Exposed = on
		case "mcp_exposed":
			rec.MCPExposed = on
		case "allow_builder_dispatch":
			rec.AllowBuilderDispatch = on
		default:
			http.Error(w, "unknown capability "+field, http.StatusBadRequest)
			return
		}
	}
	if _, err := saveAgent(udb, rec); err != nil {
		Log("[console.privileges] save %s failed: %v", rec.ID, err)
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// appendUnique adds s to list unless it's already there.
func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

// handleConsolePermissionPolicy sets a subject's standing policy.
// POST ?id=<agent:…|contact:…>&value=<allow|ask|block>.
func (T *OrchestrateApp) handleConsolePermissionPolicy(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	kind, target, found := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), ":")
	value := strings.TrimSpace(r.URL.Query().Get("value"))
	if !found || target == "" {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	switch kind {
	case "agent":
		SetDelegationPolicy(RootDB, user, target, value)
	case "contact":
		SetContactPolicy(RootDB, user, target, value)
	case "autotool":
		// A tool grant is binary (granted or not) — any state other than "allow"
		// revokes it; the tool re-queues for approval on its next unattended fire.
		if aid, tool, ok := strings.Cut(target, ":"); ok && tool != "" && value != "allow" {
			removeAutoApproveTool(UserDB(T.DB, user), aid, tool)
		}
	default:
		http.Error(w, "unknown subject", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleConsolePermissionRemove forgets a subject's policy entirely (back to
// the ask default, dropped from the list). POST ?id=<agent:…|contact:…>.
func (T *OrchestrateApp) handleConsolePermissionRemove(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	kind, target, found := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), ":")
	if !found || target == "" {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	switch kind {
	case "agent":
		RemoveDelegationPolicy(RootDB, user, target)
	case "contact":
		RemoveContactPolicy(RootDB, user, target)
	case "autotool":
		if aid, tool, ok := strings.Cut(target, ":"); ok && tool != "" {
			removeAutoApproveTool(UserDB(T.DB, user), aid, tool)
		}
	default:
		http.Error(w, "unknown subject", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// operatorApprovalRecipient renders a human-readable recipient for a phantom
// messaging approval. When the message was addressed by chat_id (e.g. a group,
// which has no single handle), it resolves the chat to its display name/handle
// via the bridge so the approval row names WHO it's going to — otherwise the
// row would show a bare chat id (or nothing) and the user couldn't tell.
func operatorApprovalRecipient(owner string, a Authorization) string {
	if strings.TrimSpace(a.Handle) != "" {
		return a.Handle
	}
	if strings.TrimSpace(a.ChatID) != "" {
		if link, ok := ActiveMessagingLink(); ok {
			if s, ok := link.DescribeChat(owner, a.ChatID); ok {
				switch {
				case s.DisplayName != "" && s.Handle != "":
					return s.DisplayName + " (" + s.Handle + ")"
				case s.DisplayName != "":
					return s.DisplayName
				case s.Handle != "":
					return s.Handle
				}
			}
		}
		return a.ChatID
	}
	return "(unknown recipient)"
}
