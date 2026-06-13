// Orchestrator data endpoints — the fleet (standing agents), the activity
// feed (run-ledger), and pending authorizations. These back the orchestrator's
// "Enabled agents" / "Authorizations" controls.
//
// Decision (model A): a channel agent is NOT a bespoke console. It's a normal
// Agency agent (Chat is the primary one) and reuses Agency's full agent surface
// — picker, toolbar, chat — wholesale (see page_chat.go). Its channel-specific
// controls are added as sidebar nav backed by these endpoints, so it has every
// feature a normal agent has plus the fleet/authorization views.
//
// Owner-scoped, reusing the shared core spine (run-ledger + standing-agent
// store). Approvals + delegation (model A) wiring lands in a later stage.

package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// legacyOperatorThread is the pre-split single session id the Operator used.
// Retained only as the migration SOURCE — the one-time Operator→Chat fold
// copies this thread into seed-chat's channel session. New code derives the
// per-agent channel session via channelSessionID.
const legacyOperatorThread = "operator-thread"

// channelSessionID is the session id of a channel agent's persistent home
// thread. Per-agent so every channel agent has its own ongoing conversation.
// The client gets this value (per agent) from the host page, so it never
// hardcodes the scheme. seed-operator is bridged to its pre-split thread id
// so the existing Operator conversation isn't orphaned by the id-scheme
// change; this special case is removed when the Operator→Chat fold migrates
// that thread.
func channelSessionID(agentID string) string {
	if agentID == "seed-operator" {
		return legacyOperatorThread
	}
	return "channel:" + agentID
}

// defaultConsoleAgent is the channel agent the console endpoints — and the
// event-monitor wake fallback — default to when no agent is specified. Chat is
// the primary channel agent now (the Operator folded into it), so legacy
// monitors with an empty WakeAgent wake Chat's channel automatically.
const defaultConsoleAgent = "seed-chat"

func consoleAgentID(r *http.Request) string {
	if a := strings.TrimSpace(r.URL.Query().Get("agent")); a != "" {
		return a
	}
	return defaultConsoleAgent
}

func (T *OrchestrateApp) registerConsoleRoutes() {
	g := T.adminGated
	T.HandleFunc("/api/console/agents", g(T.handleConsoleAgents))
	T.HandleFunc("/api/console/agents/delete", g(T.handleConsoleAgentDelete))
	T.HandleFunc("/api/console/agents/pause", g(T.handleConsoleAgentPause))
	T.HandleFunc("/api/console/agents/resume", g(T.handleConsoleAgentResume))
	T.HandleFunc("/api/console/monitors", g(T.handleConsoleMonitors))
	T.HandleFunc("/api/console/monitors/delete", g(T.handleConsoleMonitorDelete))
	T.HandleFunc("/api/console/monitors/pause", g(T.handleConsoleMonitorPause))
	T.HandleFunc("/api/console/monitors/resume", g(T.handleConsoleMonitorResume))
	T.HandleFunc("/api/console/runs", g(T.handleConsoleRuns))
	T.HandleFunc("/api/console/run-detail", g(T.handleConsoleRunDetail))
	T.HandleFunc("/api/console/approvals", g(T.handleConsoleApprovals))
	T.HandleFunc("/api/console/approvals/approve", g(T.handleApprovalApprove))
	T.HandleFunc("/api/console/approvals/always", g(T.handleApprovalAlways))
	T.HandleFunc("/api/console/approvals/deny", g(T.handleApprovalDeny))
	T.HandleFunc("/api/console/history", g(T.handleConsoleHistory))
	T.HandleFunc("/api/console/channel/clear", g(T.handleChannelClear))
	T.HandleFunc("/api/console/channel/decommission", g(T.handleChannelDecommission))
	T.HandleFunc("/api/console/grants", g(T.handleConsoleGrants))
	T.HandleFunc("/api/console/grants/revoke", g(T.handleGrantRevoke))
}

// handleConsoleGrants lists the owner's STANDING authorizations — the "Always
// allow" grants that let future delegations/messages skip the approval queue.
// Distinct from the pending queue (/api/console/approvals): those are one-time
// and vanish once acted on; these persist until revoked. Each row carries a
// "_id" of "<kind>:<target>" the Revoke action round-trips. GET only.
func (T *OrchestrateApp) handleConsoleGrants(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	out := []map[string]any{}
	for _, agent := range ListDelegationPreAuthorizations(RootDB, user) {
		out = append(out, map[string]any{"_id": "agent:" + agent, "Kind": "Agent", "Target": agent})
	}
	for _, handle := range ListContactPreAuthorizations(RootDB, user) {
		out = append(out, map[string]any{"_id": "contact:" + handle, "Kind": "Contact", "Target": handle})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleGrantRevoke clears one standing grant. id = "<kind>:<target>" from the
// grants list; future actions to that agent/contact queue for approval again.
func (T *OrchestrateApp) handleGrantRevoke(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	kind, target, found := strings.Cut(id, ":")
	if !found || target == "" {
		http.Error(w, "bad grant id", http.StatusBadRequest)
		return
	}
	switch kind {
	case "agent":
		SetDelegationPreAuthorized(RootDB, user, target, false)
	case "contact":
		SetContactPreAuthorized(RootDB, user, target, false)
	default:
		http.Error(w, "unknown grant kind", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleChannelClear wipes a channel agent's home-thread conversation and its
// rolling summary / fold cursor — the cheap fix for a crystallized thread.
// Operational state (monitors, standing agents, approvals) is left untouched;
// that's Decommission's job. POST ?agent=<id>.
func (T *OrchestrateApp) handleChannelClear(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agentID := consoleAgentID(r)
	sid := channelSessionID(agentID)
	deleteChatSession(udb, agentID, sid)
	deleteCompactState(udb, agentID, sid)
	w.WriteHeader(http.StatusNoContent)
}

// handleChannelDecommission tears down the owner's standing fleet — every event
// monitor, standing agent, and pending authorization. Destructive and explicit
// (confirm-gated client-side); the channel conversation itself is left intact
// (use Clear channel for that). POST ?agent=<id>.
func (T *OrchestrateApp) handleChannelDecommission(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	for _, m := range ListEventMonitors(RootDB, user) {
		DeleteEventMonitor(RootDB, user, m.Name)
	}
	for _, s := range ListStandingAgents(RootDB, user) {
		DeleteStandingAgent(RootDB, user, s.Name)
	}
	for _, a := range ListAuthorizations(RootDB, user) {
		DeleteAuthorization(RootDB, user, a.ID)
	}
	// Standing grants too — a clean slate means future actions all re-queue.
	for _, agent := range ListDelegationPreAuthorizations(RootDB, user) {
		SetDelegationPreAuthorized(RootDB, user, agent, false)
	}
	for _, handle := range ListContactPreAuthorizations(RootDB, user) {
		SetContactPreAuthorized(RootDB, user, handle, false)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleHistory powers the orchestrator's History nav: GET returns the
// pinned thread's turns (role + preview + hidden index); DELETE (?index=N)
// removes a single turn so the user can scrub a bad instruction. Storage holds
// the full thread; deleting from it drops the turn from future context (and
// keeps the compaction marker aligned). A turn already folded into the summary
// stays in the summary — scrubbing is most reliable on recent (un-summarized)
// turns.
func (T *OrchestrateApp) handleConsoleHistory(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := consoleAgentID(r)
	sess, _ := loadChatSession(udb, agentID, channelSessionID(agentID))

	if r.Method == http.MethodDelete {
		idx, _ := strconv.Atoi(r.URL.Query().Get("id"))
		if idx >= 0 && idx < len(sess.Messages) {
			sess.Messages = append(sess.Messages[:idx], sess.Messages[idx+1:]...)
			_, _ = saveChatSession(udb, sess)
			// Keep the compaction marker aligned when deleting a turn that
			// was already folded into the summary. When the marker reaches
			// zero, every summarized turn has been scrubbed, so the summary no
			// longer reflects anything in the thread — clear its text too,
			// otherwise a stale digest keeps re-priming the conversation.
			if st := loadCompactState(udb, agentID, channelSessionID(agentID)); idx < st.SummarizedThrough {
				st.SummarizedThrough--
				if st.SummarizedThrough <= 0 {
					st.SummarizedThrough = 0
					st.Summary = ""
				}
				saveCompactState(udb, agentID, channelSessionID(agentID), st)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	type histRow struct {
		Role    string `json:"role"`
		Message string `json:"message"`
		ID      string `json:"_id"`
	}
	out := []histRow{}
	for i, m := range sess.Messages {
		msg := m.Content
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		out = append(out, histRow{Role: m.Role, Message: msg, ID: strconv.Itoa(i)})
	}
	writeJSON(w, out)
}

type consoleAgentRow struct {
	Name     string `json:"name"`
	State    string `json:"state"` // active | paused
	Schedule string `json:"schedule"`
	Status   string `json:"status"`
	NextRun  string `json:"next_run"`
	ID       string `json:"_id"`     // hidden; row-action target (the agent name)
	Paused   bool   `json:"_paused"` // hidden; gates Pause vs Resume per row
}

// handleConsoleAgents lists the owner's standing (scheduled) agents, each
// joined with the status of its most recent run.
func (T *OrchestrateApp) handleConsoleAgents(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rows := []consoleAgentRow{}
	for _, sa := range ListStandingAgents(RootDB, user) {
		state := "active"
		if sa.Paused {
			state = "paused"
		}
		row := consoleAgentRow{Name: sa.Name, State: state, Schedule: StandingScheduleLabel(sa), ID: sa.Name, Paused: sa.Paused}
		if !sa.NextRun.IsZero() {
			row.NextRun = sa.NextRun.UTC().Format(time.RFC3339)
		}
		if latest := ListRuns(RootDB, user, RunFilter{Agent: sa.Name, Limit: 1}); len(latest) > 0 {
			row.Status = string(latest[0].Status)
		}
		rows = append(rows, row)
	}
	writeJSON(w, rows)
}

// handleConsoleAgentDelete removes a standing agent (and cancels its schedule)
// from the Enabled-agents pane's row Delete button. id = the agent's name.
func (T *OrchestrateApp) handleConsoleAgentDelete(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	if name == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	DeleteStandingAgent(RootDB, user, name)
	w.WriteHeader(http.StatusNoContent)
}

// setConsoleAgentPaused pauses or resumes a standing agent — the same logic as
// the set_standing_paused tool, behind the Enabled-agents pane's Pause/Resume
// row buttons. Idempotent, so a Pause click on an already-paused agent (or vice
// versa) is harmless. id = the agent's name.
func (T *OrchestrateApp) setConsoleAgentPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	sa, found := GetStandingAgent(RootDB, user, name)
	if !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	sa.Paused = paused
	if paused {
		if sa.SchedulerID != "" {
			UnscheduleTask(sa.SchedulerID)
			sa.SchedulerID = ""
			sa.NextRun = time.Time{}
		}
		SaveStandingAgent(RootDB, sa)
	} else {
		SaveStandingAgent(RootDB, sa)
		_ = ScheduleStandingAgent(RootDB, sa)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleConsoleAgentPause(w http.ResponseWriter, r *http.Request) {
	T.setConsoleAgentPaused(w, r, true)
}

func (T *OrchestrateApp) handleConsoleAgentResume(w http.ResponseWriter, r *http.Request) {
	T.setConsoleAgentPaused(w, r, false)
}

type consoleMonitorRow struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Detail  string `json:"detail"`
	Script  string `json:"format_script"` // the watch format_script, if any (so you can SEE it)
	Checked string `json:"last_checked"`  // when the poll last ran (liveness)
	Seen    string `json:"last_seen"`     // last response/value it hashed/observed
	Last    string `json:"last_fired"`
	ID      string `json:"_id"`     // hidden; row-action target (the monitor name)
	Paused  bool   `json:"_paused"` // hidden; gates Pause vs Resume per row
}

// handleConsoleMonitors lists the owner's event monitors (webhook / poll /
// http_poll) for the Event-monitors nav pane.
func (T *OrchestrateApp) handleConsoleMonitors(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rows := []consoleMonitorRow{}
	for _, m := range ListEventMonitors(RootDB, user) {
		state := "active"
		if m.Paused {
			state = "paused"
		}
		detail := ""
		switch m.Kind {
		case EventKindPoll:
			detail = fmt.Sprintf("every %ds via %s", m.IntervalSeconds, m.CheckAgent)
		case EventKindHTTP:
			detail = fmt.Sprintf("every %ds: %s %s %s", m.IntervalSeconds, m.URL, m.CompareOp, m.Threshold)
		case EventKindWatch:
			detail = fmt.Sprintf("every %ds: watch %s", m.IntervalSeconds, m.ToolName)
			if len(m.ToolArgs) > 0 {
				if b, err := json.Marshal(m.ToolArgs); err == nil {
					detail += " " + string(b)
				}
			}
			detail += " for changes"
			if strings.TrimSpace(m.FormatScript) != "" {
				// A format_script that emits nothing silently suppresses every
				// alert (advances baseline, never fires). Flag it so a
				// "updates-but-never-fires" watcher is obvious.
				detail += " [format_script]"
			}
		case EventKindWebhook:
			detail = "webhook (POST .../event/" + m.Token + ")"
		}
		if detail != "" {
			var dests []string
			hasWake := false
			for _, mode := range strings.Split(m.Notify, ",") {
				switch strings.TrimSpace(mode) {
				case EventNotifyText:
					dests = append(dests, "texts you")
				case EventNotifyDirect:
					if m.DeliverChatID != "" {
						dests = append(dests, "posts to its chat")
					} else {
						dests = append(dests, "posts here")
					}
				case EventNotifyChannel:
					dests = append(dests, "wakes here")
					hasWake = true
				}
			}
			if len(dests) == 0 {
				dests = append(dests, "wakes here")
				hasWake = true
			}
			detail += " → " + strings.Join(dests, " + ")
			if !hasWake {
				detail += " (no LLM)"
			}
		}
		last := ""
		if !m.LastFired.IsZero() {
			last = m.LastFired.Local().Format("Jan 2 3:04 PM")
		}
		checked := ""
		if !m.LastChecked.IsZero() {
			checked = m.LastChecked.Local().Format("Jan 2 3:04 PM")
		}
		seen := strings.ReplaceAll(m.LastResult, "\n", " ")
		if r := []rune(seen); len(r) > 80 {
			seen = string(r[:80]) + "…"
		}
		// Full script — the UI table renders long/multi-line cells with a
		// click-to-expand toggle, so send it whole rather than truncating here.
		script := strings.TrimSpace(m.FormatScript)
		rows = append(rows, consoleMonitorRow{Name: m.Name, Kind: m.Kind, State: state, Detail: detail, Script: script, Checked: checked, Seen: seen, Last: last, ID: m.Name, Paused: m.Paused})
	}
	writeJSON(w, rows)
}

func (T *OrchestrateApp) handleConsoleMonitorDelete(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	if name == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	DeleteEventMonitor(RootDB, user, name)
	w.WriteHeader(http.StatusNoContent)
}

// setConsoleMonitorPaused pauses/resumes an event monitor. Pausing a scheduled
// monitor (poll/http_poll) cancels its next check; resuming reschedules it. For
// a webhook monitor the Paused flag alone gates the public endpoint.
func (T *OrchestrateApp) setConsoleMonitorPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	m, found := GetEventMonitor(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	m.Paused = paused
	if paused {
		if m.SchedulerID != "" {
			UnscheduleTask(m.SchedulerID)
			m.SchedulerID = ""
			m.NextCheck = time.Time{}
		}
		SaveEventMonitor(RootDB, m)
	} else {
		SaveEventMonitor(RootDB, m)
		_ = ScheduleEventMonitor(RootDB, m)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleConsoleMonitorPause(w http.ResponseWriter, r *http.Request) {
	T.setConsoleMonitorPaused(w, r, true)
}

func (T *OrchestrateApp) handleConsoleMonitorResume(w http.ResponseWriter, r *http.Request) {
	T.setConsoleMonitorPaused(w, r, false)
}

// handleConsoleRuns returns the run-ledger feed (owner-scoped, status-level).
func (T *OrchestrateApp) handleConsoleRuns(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	runs := ListRuns(RootDB, user, RunFilter{Limit: 100})
	if runs == nil {
		runs = []RunRecord{}
	}
	writeJSON(w, runs)
}

// handleConsoleRunDetail returns one run's full record (encrypted raw fetched
// on demand).
func (T *OrchestrateApp) handleConsoleRunDetail(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rec, found := GetRun(RootDB, user, r.URL.Query().Get("id"))
	if !found {
		writeJSON(w, map[string]any{})
		return
	}
	writeJSON(w, rec)
}

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
		agent, action := a.Agent, a.Brief
		if a.Action == "send_message" {
			agent = operatorApprovalRecipient(user, a)
			action = "text: " + a.Text
		}
		if a.Action == "converse_contact" {
			agent = operatorApprovalRecipient(user, a)
			action = "converse toward: " + a.Brief
		}
		if a.Action == "activate_sub_agent" {
			if rec, found := loadAgent(udb, a.Agent); found {
				agent = rec.Name
			}
			action = "activate sub-agent: " + a.Brief
		}
		out = append(out, apprRow{
			Agent:     agent,
			Action:    action,
			Requested: a.Requested.Local().Format("Jan 2 3:04 PM"),
			ID:        a.ID,
		})
	}
	writeJSON(w, out)
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
		if link, ok := ActivePhantomLink(); ok {
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
		if link, ok := ActivePhantomLink(); ok {
			if _, err := operatorDeliverMessage(link, a.Owner, a.ChatID, a.Handle, a.Text, a.Images); err != nil {
				Log("[operator.approval] send_message to %s failed: %v", recip, err)
			}
		} else {
			Log("[operator.approval] phantom bridge unavailable; dropped message to %s", recip)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// converse_contact: hand the goal to phantom, which runs the autonomous
	// multi-turn conversation and wakes the Operator back here when done.
	// (Like send_message, "Always allow" does NOT pre-authorize a contact —
	// each conversation stays a deliberate approval.)
	if a.Action == "converse_contact" {
		recip := operatorRecipientKey(a.ChatID, a.Handle)
		// "Always allow" pre-authorizes THIS recipient (shared with one-shot
		// texts), so future conversations/texts start without re-queuing.
		if always {
			SetContactPreAuthorized(RootDB, a.Owner, recip, true)
		}
		if link, ok := ActivePhantomLink(); ok {
			if _, err := link.StartGoalConversation(a.Owner, a.ChatID, a.Handle, a.Brief, defaultConsoleAgent, channelSessionID(defaultConsoleAgent)); err != nil {
				Log("[operator.approval] converse_contact with %s failed: %v", recip, err)
			}
		} else {
			Log("[operator.approval] phantom bridge unavailable; dropped conversation with %s", recip)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if always {
		SetDelegationPreAuthorized(RootDB, user, a.Agent, true)
	}
	go RunDelegation(context.Background(), RootDB, a.Owner, a.Agent, a.Brief)
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleApprovalApprove(w http.ResponseWriter, r *http.Request) {
	T.resolveApproval(w, r, false)
}

func (T *OrchestrateApp) handleApprovalAlways(w http.ResponseWriter, r *http.Request) {
	T.resolveApproval(w, r, true)
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
