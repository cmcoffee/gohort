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
		detail = "Suggestion: \"" + a.Brief + "\" looks like part of this agent's kit: scope it so it's always in the catalog instead of loaded on demand. It keeps working either way."
	case orphanMemoryRefAction:
		if rec, found := loadAgent(udb, a.Agent); found {
			who = rec.Name
		}
		// Says what is actually wrong — the agent's memory is now false — and
		// that nothing is blocked. It runs fine; it just runs believing it can
		// call something it cannot.
		detail = "Suggestion: this agent's memory still refers to \"" + a.Brief + "\", whose last agent was deleted, so nothing can call it. Approving re-homes the tool here so the memory is true again. Or edit the memory from the agent's Memory pane: nothing is blocked either way."
	}
	return who, detail
}

// handleConsolePermissions is the combined Security page: pending approval
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
		Managed   bool   `json:"_managed,omitempty"` // a standing policy row (Remove; segmented control when it has a Policy)
		// Promotable marks a row scoped to ONE agent, which can be widened to
		// every agent. The all-agents row does not carry it: a control offering
		// to do what is already done is the same fault as one offering a state
		// its row cannot hold.
		Promotable bool   `json:"_promotable,omitempty"`
		Policy     string `json:"_policy,omitempty"`     // allow | ask | block (the segmented state)
		OneShot    bool   `json:"_oneshot,omitempty"`    // one-time decision (Approve/Deny only) — no "Always" grant makes sense (e.g. activating a drafted sub-agent, which the approval consumes)
		Suggested  bool   `json:"_suggestion,omitempty"` // an OFFER, not a request: nothing is blocked on it (see approvalIsSuggestion)
		// NoAsk hides the "Needs approval" segment on a row that cannot hold
		// it. A sandbox does not queue: its network namespace is cut at spawn
		// or it is not, and a switched-off sub-action is gone from the schema
		// rather than waiting on anybody.
		NoAsk bool `json:"_noask,omitempty"`
		// NoBlock is its mirror, for a row whose only two states are ask and
		// allow. "Blocked" is the never-unattended mark and answers a
		// different question; offered here it would read as switching the tool
		// off, which is not what clearing an in-chat prompt does.
		NoBlock bool `json:"_noblock,omitempty"`
		// Kind sorts the row under one of the tabs along the top of the
		// window. Derived from the row ID rather than set at each construction
		// site, for the same reason the agent is: that grammar already exists
		// and two encodings of one fact drift.
		Kind string `json:"_kind,omitempty"`
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
	// Agent delegation, scoped to the agent doing the delegating. A row with no
	// scope is the one the dispatch surfaces have always read: off limits (or
	// open) to everyone, whatever route is taken to reach the target.
	for _, e := range ListDelegationPolicies(RootDB, user) {
		agentLabel := func(id string) string {
			if rec, found := loadAgent(udb, id); found && rec.Name != "" {
				return rec.Name
			}
			return id
		}
		row := permRow{
			Who: agentLabel(e.Target), Detail: "Agent delegation: from every agent",
			ID: "agent:" + e.Target, Managed: true, Policy: e.Policy,
		}
		if e.Scope != "" {
			row.Detail = "Agent delegation: from " + agentLabel(e.Scope)
			row.ID = "agentfor:" + e.Scope + ":" + e.Target
			row.Promotable = true
		}
		out = append(out, row)
	}
	// Contact messaging, per agent. A grant says WHICH agent may reach this
	// person, because an agent carries a persona and its own rules about what
	// it may say to them: a permission the whole fleet shares is one any other
	// agent can spend, and then those rules hold nothing. An owner who does not
	// want the distinction promotes the row to every agent and stops thinking
	// about it.
	for _, e := range ListContactPolicies(RootDB, user) {
		row := permRow{
			Who: e.Target, Detail: "Contact messaging: every agent",
			ID: "contact:" + e.Target, Managed: true, Policy: e.Policy,
		}
		if e.Scope != "" {
			who := e.Scope
			if rec, found := loadAgent(udb, e.Scope); found && rec.Name != "" {
				who = rec.Name
			}
			row.Detail = "Contact messaging: " + who
			row.ID = "contactfor:" + e.Scope + ":" + e.Target
			// Only a scoped row can be promoted. Offering it on the row that
			// already covers everything is a button that does what has been
			// done, which is the same fault as a control offering a state its
			// row cannot hold.
			row.Promotable = true
		}
		out = append(out, row)
	}
	// Zone 3 — autonomous-run tool grants: the tools you "Always allowed" a
	// scheduled/standing agent to run unattended (AutoApproveTools). Surfaced so
	// the grant isn't invisible after approval; Remove revokes it, and the tool
	// re-queues on its next unattended fire.
	//
	// NO Policy, deliberately, so these get no segmented control.
	//
	// They used to carry Policy "allow" and so rendered the same three-way
	// Always allow / Needs approval / Blocked control the agent and contact rows
	// get. On those the middle option is a real stored state and the row stays,
	// showing it. Here there is no such state to store: the grant is the tool's
	// presence in AutoApproveTools and nothing else, so choosing "Needs
	// approval" revoked it and the row vanished — the control offering a state
	// its own row cannot hold, and a click reading as the setting being lost
	// rather than as the deliberate revoke it was.
	//
	// Remove already says what it does and asks first. That is the honest
	// control for a binary grant.
	//
	// Built from the UNION of the grants and the recorded decisions. A grant
	// with no record predates this table (or was made by approving a request,
	// which writes the list directly), and must still appear; a record with no
	// grant is the "needs approval" state, which exists only here and is the
	// whole reason the table does. Name resolution is per agent, so the grants
	// are walked first and the records fill in what they did not cover.
	seenTool := map[string]bool{}
	agentName := map[string]string{}
	for _, ag := range listAgents(udb, user) {
		agentName[ag.ID] = firstNonEmptyStr(ag.Name, ag.ID)
		for _, tool := range ag.AutoApproveTools {
			seenTool[ag.ID+"\x00"+tool] = true
			// A grant that grants nothing is not a permission, and listing it as
			// one is how this page came to show rows for tools that were never
			// gated: names approved under the old rule, which refused every
			// NeedsConfirm tool, and left behind when the rule became "a tool
			// attached to an agent is a tool it may use". The gate allows those
			// with or without the entry. So the row appears only while the grant
			// is load-bearing, which is while the tool would otherwise ask.
			if !toolAlwaysConfirms(udb, user, nil, tool) {
				continue
			}
			out = append(out, permRow{
				Who:     agentName[ag.ID],
				Detail:  "Autonomous tool: " + tool,
				ID:      "autotool:" + ag.ID + ":" + tool,
				Managed: true, Policy: PolicyAllow,
			})
		}
		// The other half of the same decision, and the one with nothing
		// conditional about it: a mark the owner made, which the runner refuses
		// on without asking. Listed whatever the tool's credential says, because
		// unlike a grant it is never inert.
		for _, tool := range ag.NoUnattendedTools {
			seenTool[ag.ID+"\x00"+tool] = true
			out = append(out, permRow{
				Who:     agentName[ag.ID],
				Detail:  "Never unattended: " + tool,
				ID:      "autotool:" + ag.ID + ":" + tool,
				Managed: true, Policy: PolicyBlock,
			})
		}
		// The WORKSPACE decisions, which are about the sandbox rather than a
		// tool and so had nowhere on this page to be seen or taken back. They
		// were set in the editor and reviewable nowhere.
		//
		// EVERY agent, not only the narrowed ones. This used to appear only
		// while the restriction was set, on the rule that a row shows up while
		// its decision is load-bearing, and the reasoning was that a page
		// listing every agent should not grow inert rows for the agents that
		// narrowed nothing.
		//
		// That was wrong twice. The state is a plain bool, so "allowed" and
		// "never decided" are the same value: setting an agent back to allowed
		// DELETED the row you had just used, which reads as the click having
		// failed. And an agent whose workspace can reach the network is not an
		// inert row, it is the answer to the question somebody opens this page
		// to ask. Who can reach the network is only legible next to who
		// cannot.
		//
		// The page is scoped to ONE agent, so this is a single row rather
		// than one per agent. The sub-action rows below deliberately do NOT
		// list exhaustively: those are (grouped tool x action) and listing
		// every action of thirteen grouped tools would bury the page.
		//
		// NoAsk, because "Needs approval" is not a state either can hold.
		// Nothing queues a sandbox: the namespace is cut at spawn or it is
		// not. Offering the segment would be offering something that cannot be
		// stored, which is what its own doc warns against.
		wsPolicy, wsDetail := PolicyAllow, "Workspace may reach the network"
		if ag.WorkspaceNoNetwork {
			wsPolicy, wsDetail = PolicyBlock, "Workspace may not reach the network"
		}
		out = append(out, permRow{
			Who:     agentName[ag.ID],
			Detail:  wsDetail,
			ID:      "workspace:" + ag.ID + ":network",
			Managed: true, Policy: wsPolicy, NoAsk: true,
		})
		for _, pair := range ag.DisabledToolActions {
			if pair = strings.TrimSpace(pair); pair == "" {
				continue
			}
			out = append(out, permRow{
				Who:     agentName[ag.ID],
				Detail:  "Switched off: " + strings.Replace(pair, "/", " - ", 1),
				ID:      "subaction:" + ag.ID + ":" + pair,
				Managed: true, Policy: PolicyBlock, NoAsk: true,
			})
		}
	}
	// Tools set to ask before every call, in CHAT. One row per tool and not
	// per agent, because the flag lives on the tool record: a tool's riskiness
	// is a property of the tool, and two agents holding it must not disagree
	// about whether it asks.
	//
	// Its state is "ask" and its only other state is "allow". Blocked is the
	// unattended mark and belongs to a different question, so the segment is
	// hidden rather than offered to mean something it does not.
	//
	// EVERY tool of the user's, not only the ones currently asking. The flag
	// is a plain bool on the tool record, so "allow" and "never decided" are
	// the same value, and listing only the true ones made the control delete
	// itself: you clicked Always allow and the row vanished, which reads as
	// the click having failed rather than having worked.
	//
	// The autotool rows beside these never had the problem because they keep
	// their own decision record. This is the same fix without a second store:
	// show the whole set and let each row carry its state. It is also the
	// honest shape for a security page - "supervised" is only meaningful next
	// to the things that are not.
	for _, pt := range LoadPersistentTempTools(AuthDB(), user) {
		policy, detail := PolicyAllow, "Runs without asking, in chat"
		if pt.Tool.ConfirmInChat {
			policy, detail = PolicyAsk, "Asks before every call, in chat, on every agent"
		}
		out = append(out, permRow{
			Who:     pt.Tool.Name,
			Detail:  detail,
			ID:      "confirmtool:" + pt.Tool.Name,
			Managed: true, Policy: policy, NoBlock: true,
		})
	}
	for _, p := range listAutoToolPolicies(RootDB, user) {
		if seenTool[p.AgentID+"\x00"+p.Tool] {
			continue // already listed from the grant, which is the same fact
		}
		who, ok := agentName[p.AgentID]
		if !ok {
			continue // a decision about an agent that is gone is not actionable
		}
		detail := "Autonomous tool: " + p.Tool
		if p.Policy == PolicyBlock {
			detail = "Never unattended: " + p.Tool
		}
		out = append(out, permRow{
			Who:     who,
			Detail:  detail,
			ID:      "autotool:" + p.AgentID + ":" + p.Tool,
			Managed: true, Policy: p.Policy,
		})
	}
	// Narrow to the agent this page was opened from. These are the permissions
	// OF AN AGENT, not of the fleet: a page mixing every agent's decisions
	// answers "what have I decided somewhere" when the question is "what can
	// THIS one do", and leaves the reader filtering in their head on a security
	// surface, which is where that goes wrong most expensively.
	//
	// A row that binds EVERY agent is kept. A contact policy with no scope, or
	// a tool set to ask wherever it appears, governs this agent too, and
	// dropping it would let the page lie by omission, which is the dangerous
	// direction here.
	want := strings.TrimSpace(r.URL.Query().Get("agent"))
	// kind narrows to one tab's worth. Server-side, because each tab on the
	// Security page asks for its own rows: a client-side filter would have
	// every tab fetch every row and hide most of them, and a count in a tab
	// heading would then be counting things the tab is not showing.
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	kept := make([]permRow, 0, len(out))
	for _, row := range out {
		if want != "" {
			if a := permRowAgent(row.ID); a != "" && a != want {
				continue
			}
		}
		row.Kind = permRowKind(row.ID)
		if kind != "" && row.Kind != kind {
			continue
		}
		kept = append(kept, row)
	}
	writeJSON(w, kept)
}

// permRowKind sorts a row under one of the tabs along the top of the window.
//
// Four subjects, because they are four different questions somebody arrives
// with: which tools does it have and do they need watching, what may its
// sandbox reach, who may it talk to, and what may it hand work to. A single
// undifferentiated list made the reader do that sorting in their head.
//
// A pending request carries no prefix, so it lands under "requests" and shows
// on the All tab, which is the one the window opens on. That is deliberate:
// something blocking a run must not be filed behind a tab nobody clicked.
func permRowKind(id string) string {
	kind, _, found := strings.Cut(id, ":")
	if !found {
		return "requests"
	}
	switch kind {
	case "confirmtool", "autotool", "subaction":
		return "tools"
	case "workspace":
		return "workspace"
	case "contact", "contactfor":
		return "access"
	case "agent", "agentfor":
		return "delegation"
	}
	return "requests"
}

// permRowAgent returns the agent a permissions row is about, or "" when it
// binds every agent.
//
// Read back out of the row ID rather than carried as a second field, because
// that grammar already exists and is already what handleConsolePermissionPolicy
// parses from the other end. Two encodings of one fact drift; one does not.
func permRowAgent(id string) string {
	kind, rest, found := strings.Cut(id, ":")
	if !found {
		return "" // a pending request, which is always the user's to see
	}
	switch kind {
	case "agentfor", "contactfor", "autotool", "workspace", "subaction":
		// The agent leads and its subject follows, which is why it leads: the
		// subject carries its own colon or slash and neither has to be escaped.
		if a, _, ok := strings.Cut(rest, ":"); ok {
			return a
		}
	}
	// agent: / contact: / confirmtool: bind every agent by construction.
	return ""
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
	// Flags whose ON was filed as a request rather than applied. Reported back
	// so the UI can leave the box clear and say who it is waiting on, instead
	// of showing a capability that is not actually live.
	var requested []string
	for field, on := range body.Flags {
		switch strings.TrimSpace(field) {
		case "fleet":
			rec.Fleet = on
		case "author":
			rec.Author = on
		// REACH. Turning this on lets every signed-in user of the deployment
		// use the agent, which is the same reach a shared app has and has
		// needed an administrator since v0.6.710; an agent is the larger
		// grant, since it carries its owner's tools, documents and skills.
		// Turning OFF stays direct: nobody needs permission to stop sharing.
		// See agent_promotion.go.
		case "exposed":
			if T.agentPublishNeedsApproval(r, user, rec.ID, "exposed", on, rec.Everyone) {
				requested = append(requested, "exposed")
				continue
			}
			rec.Everyone = on
		case "mcp_exposed":
			if T.agentPublishNeedsApproval(r, user, rec.ID, "mcp_exposed", on, rec.MCPExposed) {
				requested = append(requested, "mcp_exposed")
				continue
			}
			rec.MCPExposed = on
		// PRESENTATION. A card on the dashboard and a page at /agents/<slug>,
		// for the people who can already use the agent. It grants nothing, so
		// it asks nobody: a shortcut to something you already have is not a
		// decision anybody else has a stake in. This used to be the same flag
		// as the reach above, which meant an owner could not add a shortcut
		// without widening who could use it.
		case "show_on_dashboard":
			rec.ShowOnDashboard = on
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
	if len(requested) > 0 {
		// A body rather than 204, because "requested" and "done" are different
		// outcomes and a caller that cannot tell them apart will show the
		// capability as live when an administrator has not looked at it yet.
		writeJSON(w, map[string]any{"ok": true, "requested": requested})
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
		SetDelegationPolicy(RootDB, user, "", target, value)
	case "agentfor":
		if from, tgt, ok := strings.Cut(target, ":"); ok && tgt != "" {
			SetDelegationPolicy(RootDB, user, from, tgt, value)
		}
	case "contact":
		SetContactPolicy(RootDB, user, "", target, value)
	case "contactfor":
		// Scoped to one agent, in the shape autotool already uses: the agent
		// leads and the subject follows, so neither has to be escaped.
		if aid, handle, ok := strings.Cut(target, ":"); ok && handle != "" {
			SetContactPolicy(RootDB, user, aid, handle, value)
		}
	case "confirmtool":
		// Keyed by TOOL, not agent: the flag is on the tool record. "ask" sets
		// it, anything else clears it, and there is no third state because
		// Blocked is the unattended mark and is not this control's question.
		if !SetUserToolConfirmInChat(AuthDB(), user, target, value == PolicyAsk) {
			http.Error(w, "no such tool", http.StatusNotFound)
			return
		}
	case "workspace":
		// The sandbox's own reach. Two states only, and "allow" CLEARS the
		// mark rather than storing one: the absence is the permission.
		if aid, what, ok := strings.Cut(target, ":"); ok && what == "network" {
			udb := UserDB(T.DB, user)
			if rec, found := loadAgent(udb, aid); found && rec.Owner == user {
				rec.WorkspaceNoNetwork = value == PolicyBlock
				if _, err := saveAgent(udb, rec); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
		}
	case "subaction":
		// tool/action carries its own slash, so the agent leads and the pair
		// follows and neither has to be escaped.
		if aid, pair, ok := strings.Cut(target, ":"); ok && pair != "" {
			udb := UserDB(T.DB, user)
			if rec, found := loadAgent(udb, aid); found && rec.Owner == user {
				kept := []string{}
				for _, p := range rec.DisabledToolActions {
					if strings.TrimSpace(p) != pair {
						kept = append(kept, p)
					}
				}
				if value == PolicyBlock {
					kept = append(kept, pair)
				}
				rec.DisabledToolActions = kept
				if _, err := saveAgent(udb, rec); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
		}
	case "autotool":
		// Records the decision and makes the grant match it. "ask" keeps a
		// record where the list cannot hold one, so the row stays showing the
		// state you chose instead of disappearing as though the click failed.
		if aid, tool, ok := strings.Cut(target, ":"); ok && tool != "" {
			setAutoToolPolicy(RootDB, UserDB(T.DB, user), user, aid, tool, value)
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
		RemoveDelegationPolicy(RootDB, user, "", target)
	case "agentfor":
		if from, tgt, ok := strings.Cut(target, ":"); ok && tgt != "" {
			RemoveDelegationPolicy(RootDB, user, from, tgt)
		}
	case "contact":
		RemoveContactPolicy(RootDB, user, "", target)
	case "contactfor":
		if aid, handle, ok := strings.Cut(target, ":"); ok && handle != "" {
			RemoveContactPolicy(RootDB, user, aid, handle)
		}
	case "autotool":
		if aid, tool, ok := strings.Cut(target, ":"); ok && tool != "" {
			removeAutoToolPolicy(RootDB, UserDB(T.DB, user), user, aid, tool)
		}
	default:
		http.Error(w, "unknown subject", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleConsolePermissionPromote widens one agent's grant, contact or
// delegation, to every agent the owner has.
//
// The scoped row is the default because a permission and a guardrail have to
// share a scope, and this is the escape hatch for when that distinction is not
// worth the owner's attention. It writes the all-agents row and DROPS the
// scoped one, because leaving both would leave a row claiming to decide
// something that the row above it already decided.
//
// It does not touch any OTHER agent's row. Promoting is "everyone else may too",
// not "everyone now agrees with this one": an agent the owner has deliberately
// blocked from this contact stays blocked, which is the precedence rule the
// resolution follows everywhere else.
func (T *OrchestrateApp) handleConsolePermissionPromote(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	kind, target, found := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), ":")
	if !found || (kind != "contactfor" && kind != "agentfor") {
		http.Error(w, "only a grant scoped to one agent can be promoted", http.StatusBadRequest)
		return
	}
	aid, subject, ok := strings.Cut(target, ":")
	if !ok || subject == "" {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if kind == "agentfor" {
		policy := DelegationPolicy(RootDB, user, aid, subject)
		SetDelegationPolicy(RootDB, user, "", subject, policy)
		RemoveDelegationPolicy(RootDB, user, aid, subject)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	policy := ContactPolicy(RootDB, user, aid, subject)
	SetContactPolicy(RootDB, user, "", subject, policy)
	RemoveContactPolicy(RootDB, user, aid, subject)
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
