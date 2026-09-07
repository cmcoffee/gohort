package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// resolveApproval approves a queued delegation: optionally pre-authorizes the
// agent (always-allow), drops the pending entry, and runs the delegation async
// (the result lands in Activity / the run-ledger).
func (T *OrchestrateApp) resolveApproval(w http.ResponseWriter, r *http.Request, always bool) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	a, found := GetAuthorization(RootDB, user, r.URL.Query().Get("id"))
	if !found {
		http.Error(w, "no such authorization", http.StatusNotFound)
		return
	}
	DeleteAuthorization(RootDB, user, a.ID)
	// activate_sub_agent: a dispatched Builder drafted this sub-agent and held it
	// for approval. Approval just clears the PendingApproval hold so it goes
	// live — no delegation runs. ("Always allow" has no meaning here.)
	if a.Action == "activate_sub_agent" {
		if rec, ok := loadAgent(udb, a.Agent); ok {
			rec.PendingApproval = false
			if _, err := saveAgent(udb, rec); err != nil {
				Log("[operator.approval] activate_sub_agent %s save failed: %v", a.Agent, err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// build_agent: a non-Fleet agent asked (via request_build) to have a sub-agent
	// built. The user approving THIS is the human-in-the-loop the Fleet gate
	// protects — so now run Builder on the requester's behalf. a.FromAgent rides
	// as the dispatch origin, which runAgentSyncConfirm turns into the sub-session
	// DispatchParentAgentID, so Builder's create_agent stamps OwnedBy=<requester>
	// exactly as a live Fleet dispatch would. Builder's own creation then queues
	// its usual activate_sub_agent approval, so the built agent still lands
	// held-for-activation — this approval authorizes the BUILD, the next the
	// switch-on. Async, like the delegate approval below.
	if a.Action == buildAgentAction {
		go RunDelegation(context.Background(), RootDB, a.Owner, "builder", a.Brief, a.FromAgent)
		Log("[operator.approval] build_agent approved — dispatching Builder for owner=%s requester=%s", a.Owner, a.FromAgent)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// autonomous_tool: an unattended (standing/scheduled) fire wanted a
	// NeedsConfirm tool the agent isn't pre-authorized for. Approving adds the tool
	// to the PARENT's AutoApproveTools when the requester is a sub-agent — the grant
	// is inherited down the ownership chain, so the owner authorizes ONCE at the
	// parent and every sub-agent inherits it (autonomousApprovedSet), rather than
	// re-approving the same tool per sub-agent. Top-level agents grant on themselves.
	if a.Action == "autonomous_tool" {
		grantTo := a.Agent
		if rec, ok := loadAgent(udb, a.Agent); ok && rec.OwnedBy != "" {
			grantTo = rec.OwnedBy
		}
		if rec, ok := loadAgent(udb, grantTo); ok {
			has := false
			for _, t := range rec.AutoApproveTools {
				if t == a.Brief {
					has = true
					break
				}
			}
			if !has && a.Brief != "" {
				rec.AutoApproveTools = append(rec.AutoApproveTools, a.Brief)
				if _, err := saveAgent(udb, rec); err != nil {
					Log("[operator.approval] autonomous_tool %s/%s save failed: %v", grantTo, a.Brief, err)
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// orphan_memory_ref: an agent's memory names a tool whose last carrier was
	// deleted. Approving RE-HOMES the orphan onto this agent, which makes the
	// remembered capability real again — the repair the memory implies. The
	// other repair (editing the memory) stays manual in the Memory pane,
	// because the audit never deletes on the owner's behalf.
	if a.Action == orphanMemoryRefAction {
		if a.Brief != "" {
			if err := rehomeOrphanTool(RootDB, user, a.Brief, a.Agent); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			Log("[orchestrate.memaudit] approved: re-homed orphaned %q onto agent=%s", a.Brief, a.Agent)
		}
		DeleteAuthorization(RootDB, user, a.ID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// scope_tool: the Tier-2 elevation suggestion — this agent load_tool'd the
	// same shared tool across enough sessions that it's evidently kit.
	// Approving ADDS the agent to the tool's ScopeAgents: first-class in its
	// catalog from then on, and (when the tool was shared) withdrawn from the
	// general pool — the approval text says so.
	if a.Action == "scope_tool" {
		if a.Brief != "" {
			p, ok := UserToolByName(udb, user, a.Brief)
			if !ok {
				http.Error(w, "tool no longer exists", http.StatusNotFound)
				return
			}
			scope := p.ScopeAgents
			has := false
			for _, id := range scope {
				if id == a.Agent {
					has = true
					break
				}
			}
			if !has {
				scope = append(scope, a.Agent)
				if !SetUserToolScopeAgents(udb, user, a.Brief, scope) {
					http.Error(w, "scope update failed", http.StatusInternalServerError)
					return
				}
				Log("[orchestrate.elevate] approved: scoped %q to agent=%s", a.Brief, a.Agent)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// send_message: deliver the queued text to the contact via the phantom
	// bridge. ("Always allow" doesn't pre-authorize a contact — each outbound
	// to a real person stays a deliberate approval.)
	if a.Action == "send_message" {
		recip := operatorRecipientKey(a.ChatID, a.Handle)
		// "Always allow" pre-authorizes THIS recipient, so future texts (and
		// autonomous conversations) to the same chat/handle send without
		// re-queuing.
		if always {
			SetContactPreAuthorized(RootDB, a.Owner, recip, true)
		}
		if _, err := operatorDeliverMessage(a.Owner, a.Agent, a.ChatID, a.Handle, a.Text, a.Images); err != nil {
			Log("[operator.approval] send_message to %s failed: %v", recip, err)
		}
		// Approved post: if the target is a bound channel, make its agent see it
		// (channel session + cortex) so it can field follow-ups.
		recordChannelPost(UserDB(T.DB, a.Owner), a.Owner, a.ChatID, a.Handle, a.Text)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// bind_thread: the agent asked to bind a person's 1:1 thread so it can read
	// their replies. Approval creates a persistent, agent-bound channel scoped to
	// that handle, with a gatekeeper rule that keeps it to replies-to-its-own
	// messages. Idempotent; "Always allow" has no separate meaning here.
	if a.Action == "bind_thread" {
		addr := a.ChatID
		if addr == "" {
			addr = a.Handle
		}
		if addr != "" {
			exists := false
			for _, ch := range ListChannelsForAgent(RootDB, a.Owner, a.Agent) {
				if ch.Service == "imessage" && (ch.Address == addr || ch.Address == a.Handle || ch.Address == a.ChatID) {
					exists = true
					break
				}
			}
			if !exists {
				name := a.Brief
				if name == "" {
					name = addr
				}
				wake := a.Text != "nowake"
				gk := threadBindingGatekeeperRule
				if !wake {
					gk = "" // no-wake = record-only; the gatekeeper is skipped anyway
				}
				SaveChannel(RootDB, Channel{
					ID:         UUIDv4(),
					Owner:      a.Owner,
					AgentID:    a.Agent,
					Name:       "DM: " + name,
					Service:    "imessage",
					Address:    addr,
					AutoReply:  wake,
					Gatekeeper: gk,
					AgentBound: true,
				})
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// converse_contact: hand the goal to phantom, which runs the autonomous
	// multi-turn conversation and wakes the Operator back here when done.
	// (Like send_message, "Always allow" does NOT pre-authorize a contact —
	// each conversation stays a deliberate approval.)
	if a.Action == "converse_contact" {
		// Goal-conversations are retired; an approval for a stale queued one just
		// clears (nothing to start). Guard kept so it doesn't fall through to the
		// delegation path below.
		Log("[operator.approval] converse_contact retired; clearing stale approval")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if always {
		SetDelegationPreAuthorized(RootDB, user, a.Agent, true)
	}
	// a.FromAgent (captured when the delegation was queued) keeps an approved
	// delegation's channel reach identical to a pre-authorized one's. Empty on
	// legacy records — that run just keeps its own scope.
	go RunDelegation(context.Background(), RootDB, a.Owner, a.Agent, a.Brief, a.FromAgent)
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleApprovalApprove(w http.ResponseWriter, r *http.Request) {
	T.resolveApproval(w, r, false)
}

func (T *OrchestrateApp) handleApprovalAlways(w http.ResponseWriter, r *http.Request) {
	T.resolveApproval(w, r, true)
}

// handleCredentialUpdateApply applies a user-APPROVED config change to the
// caller's OWN api credential (the credential_update card's Approve button).
// Config-only: base_url / param_name / description. It loads the existing
// record, overlays only the provided fields, and saves with an EMPTY secret —
// which Secure().Save treats as "keep the existing secret" and also preserves
// the Disabled/Secured state. So approving an update can never wipe the secret
// or silently enable/disable a working credential. Scoped to the user's
// namespace (LoadUser + Owner=user), so it can't touch a global/admin cred.
func (T *OrchestrateApp) handleCredentialUpdateApply(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var body struct {
		Name        string `json:"name"`
		BaseURL     string `json:"base_url"`
		ParamName   string `json:"param_name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	c, found := Secure().LoadUser(user, name)
	if !found {
		http.Error(w, "no credential named "+name+" in your API credentials", http.StatusNotFound)
		return
	}
	// Overlay only the provided config fields; leave the rest as-is.
	if v := strings.TrimSpace(body.BaseURL); v != "" {
		c.BaseURL = v
	}
	if v := strings.TrimSpace(body.ParamName); v != "" {
		c.ParamName = v
	}
	if v := strings.TrimSpace(body.Description); v != "" {
		c.Description = v
	}
	c.Owner = user
	// Empty secret => preserve the existing secret + enabled/secured state.
	if err := Secure().Save(c, ""); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleApprovalDeny(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	// Denying a sub-agent activation rejects the draft outright — delete the
	// held agent so it doesn't linger dormant (PendingApproval) forever.
	if a, found := GetAuthorization(RootDB, user, id); found && a.Action == "activate_sub_agent" {
		deleteAgent(udb, user, a.Agent)
	}
	DeleteAuthorization(RootDB, user, id)
	w.WriteHeader(http.StatusNoContent)
}
