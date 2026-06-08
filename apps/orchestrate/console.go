// Orchestrator data endpoints — the fleet (standing agents), the activity
// feed (run-ledger), and pending authorizations. These back the orchestrator's
// "Enabled agents" / "Authorizations" controls.
//
// Decision (model A): the Operator is NOT a bespoke console. It's a normal
// Agency agent (seed-operator) and reuses Agency's full agent surface — picker,
// toolbar, chat — wholesale (see page_chat.go). Its orchestrator-specific
// controls are added as toolbar actions backed by these endpoints, so it has
// every feature a normal agent has plus the fleet/authorization views.
//
// Owner-scoped, reusing the shared core spine (run-ledger + standing-agent
// store). Approvals + delegation (model A) wiring lands in a later stage.

package orchestrate

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// operatorPinnedSession is the single session id the Operator's ongoing thread
// uses. MUST match the literal in core/ui/runtime.go's applyOrchMode.
const operatorPinnedSession = "operator-thread"

// defaultConsoleAgent is the orchestrator agent the console endpoints default
// to when no ?agent= is supplied.
const defaultConsoleAgent = "seed-operator"

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
	sess, _ := loadChatSession(udb, agentID, operatorPinnedSession)

	if r.Method == http.MethodDelete {
		idx, _ := strconv.Atoi(r.URL.Query().Get("id"))
		if idx >= 0 && idx < len(sess.Messages) {
			sess.Messages = append(sess.Messages[:idx], sess.Messages[idx+1:]...)
			_, _ = saveChatSession(udb, sess)
			// Keep the compaction marker aligned when deleting a turn that
			// was already folded into the summary.
			if st := loadCompactState(udb, agentID, operatorPinnedSession); idx < st.SummarizedThrough {
				st.SummarizedThrough--
				saveCompactState(udb, agentID, operatorPinnedSession, st)
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
	ID       string `json:"_id"` // hidden; row-action target (the agent name)
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
		row := consoleAgentRow{Name: sa.Name, State: state, Schedule: StandingScheduleLabel(sa), ID: sa.Name}
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
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Last   string `json:"last_fired"`
	ID     string `json:"_id"` // hidden; row-action target (the monitor name)
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
		case EventKindWebhook:
			detail = "webhook (POST .../event/" + m.Token + ")"
		}
		last := ""
		if !m.LastFired.IsZero() {
			last = m.LastFired.Local().Format("Jan 2 3:04 PM")
		}
		rows = append(rows, consoleMonitorRow{Name: m.Name, Kind: m.Kind, State: state, Detail: detail, Last: last, ID: m.Name})
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
	user, _, ok := RequireUser(w, r, T.DB)
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
			agent = a.Handle
			action = "text: " + a.Text
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

// resolveApproval approves a queued delegation: optionally pre-authorizes the
// agent (always-allow), drops the pending entry, and runs the delegation async
// (the result lands in Activity / the run-ledger).
func (T *OrchestrateApp) resolveApproval(w http.ResponseWriter, r *http.Request, always bool) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	a, found := GetAuthorization(RootDB, user, r.URL.Query().Get("id"))
	if !found {
		http.Error(w, "no such authorization", http.StatusNotFound)
		return
	}
	DeleteAuthorization(RootDB, user, a.ID)
	// send_message: deliver the queued text to the contact via the phantom
	// bridge. ("Always allow" doesn't pre-authorize a contact — each outbound
	// to a real person stays a deliberate approval.)
	if a.Action == "send_message" {
		if link, ok := ActivePhantomLink(); ok {
			if err := link.SendToHandle(a.Owner, a.Handle, a.Text); err != nil {
				Log("[operator.approval] send_message to %q failed: %v", a.Handle, err)
			}
		} else {
			Log("[operator.approval] phantom bridge unavailable; dropped message to %q", a.Handle)
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
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	DeleteAuthorization(RootDB, user, r.URL.Query().Get("id"))
	w.WriteHeader(http.StatusNoContent)
}
