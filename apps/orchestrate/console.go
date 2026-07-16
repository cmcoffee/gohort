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
	"net/url"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// cortexSessionID is the session id of a channel agent's persistent home
// thread. Per-agent so every channel agent has its own ongoing conversation.
// The client gets this value (per agent) from the host page, so it never
// hardcodes the scheme. (The retired Operator's pre-split "operator-thread"
// bridge was removed when the Operator seed was dropped — see
// dropLegacyOperator.)
func cortexSessionID(agentID string) string {
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
	gw := T.adminGatedWrite // admin + rejects safe methods (mutating actions)
	T.HandleFunc("/api/console/agents", g(T.handleConsoleAgents))
	T.HandleFunc("/api/console/agents/delete", gw(T.handleConsoleAgentDelete))
	T.HandleFunc("/api/console/agents/pause", gw(T.handleConsoleAgentPause))
	T.HandleFunc("/api/console/agents/resume", gw(T.handleConsoleAgentResume))
	T.HandleFunc("/api/console/monitors", g(T.handleConsoleMonitors))
	T.HandleFunc("/api/console/monitors/delete", gw(T.handleConsoleMonitorDelete))
	T.HandleFunc("/api/console/monitors/pause", gw(T.handleConsoleMonitorPause))
	T.HandleFunc("/api/console/monitors/resume", gw(T.handleConsoleMonitorResume))
	T.HandleFunc("/api/console/runs", g(T.handleConsoleRuns))
	T.HandleFunc("/api/console/run-detail", g(T.handleConsoleRunDetail))
	T.HandleFunc("/api/console/approvals", g(T.handleConsoleApprovals))
	T.HandleFunc("/api/console/permissions", g(T.handleConsolePermissions))
	T.HandleFunc("/api/console/permissions/policy", g(T.handleConsolePermissionPolicy))
	T.HandleFunc("/api/console/permissions/remove", g(T.handleConsolePermissionRemove))
	T.HandleFunc("/api/console/approvals/approve", gw(T.handleApprovalApprove))
	T.HandleFunc("/api/console/approvals/always", gw(T.handleApprovalAlways))
	T.HandleFunc("/api/console/approvals/deny", gw(T.handleApprovalDeny))
	T.HandleFunc("/api/console/channel/clear", g(T.handleChannelClear))
	T.HandleFunc("/api/console/channel/decommission", g(T.handleChannelDecommission))
	T.HandleFunc("/api/console/grants", g(T.handleConsoleGrants))
	T.HandleFunc("/api/console/grants/revoke", g(T.handleGrantRevoke))
	// Bridges — deployment-wide admin management of the credential-
	// polling bridges agents have created, regardless of owner. This
	// is the admin's enable/disable switch for a bridge: a paused
	// bridge stops polling AND agents can't resume it themselves
	// (bridge tools have no resume action; only these routes and the
	// owner's console do).
	T.HandleFunc("/api/console/bridges", g(T.handleConsoleBridges))
	T.HandleFunc("/api/console/bridges/pause", gw(T.handleConsoleBridgePause))
	T.HandleFunc("/api/console/bridges/resume", gw(T.handleConsoleBridgeResume))
	T.HandleFunc("/api/console/bridges/delete", gw(T.handleConsoleBridgeDelete))
	// Hook/unhook a bridge to a channel (Stage C: the UI equivalent of the
	// bridge tool's update action).
	T.HandleFunc("/api/console/bridges/set-channel", gw(T.handleConsoleBridgeSetChannel))
	// Channel picker for the Connect action — an owner's channels as
	// {id, label, desc}. owner is passed per-row (a bridge's own owner).
	T.HandleFunc("/api/console/bridge-channels", g(T.handleConsoleBridgeChannels))
	// Recent activity in a poll bridge's connected channel — the HistoryPanel
	// expand on the /bridges/ poll table.
	T.HandleFunc("/api/console/bridge-thread", g(T.handleConsoleBridgeThread))
	// Per-agent schedules rail — the agent's own event monitors + scheduled
	// runs, so a schedule is visible within the agent it fires (any agent, not
	// just controllers).
	T.HandleFunc("/api/schedules", g(T.handleSchedules))
}

// handleSchedules returns an agent's scheduled runs + event monitors for the
// per-agent Schedules rail. Scoped "where it fires": monitors by WakeAgent,
// standing runs by AgentID (the agent that runs on the schedule) — matching
// introspect(section="schedules"). Each row embeds its own pause/resume/delete
// URLs so the rail JS stays generic across the two record types.
func (T *OrchestrateApp) handleSchedules(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	rows := []map[string]any{}
	for _, m := range ListEventMonitors(RootDB, user) {
		wake := m.WakeAgent
		if wake == "" {
			wake = "seed-chat"
		}
		if agentID != "" && wake != agentID {
			continue
		}
		kind := string(m.Kind)
		if isBridgeMonitor(m) {
			kind = "bridge"
		}
		id := url.QueryEscape(m.Name)
		rows = append(rows, map[string]any{
			"name":       m.Name,
			"detail":     fmt.Sprintf("%s · every %ds", kind, m.IntervalSeconds),
			"paused":     m.Paused,
			"pause_url":  "api/console/monitors/pause?id=" + id,
			"resume_url": "api/console/monitors/resume?id=" + id,
			"delete_url": "api/console/monitors/delete?id=" + id,
		})
	}
	for _, sa := range ListStandingAgents(RootDB, user) {
		if agentID != "" && sa.AgentID != agentID {
			continue
		}
		id := url.QueryEscape(sa.Name)
		rows = append(rows, map[string]any{
			"name":       sa.Name,
			"detail":     "scheduled run · " + StandingScheduleLabel(sa),
			"paused":     sa.Paused,
			"pause_url":  "api/console/agents/pause?id=" + id,
			"resume_url": "api/console/agents/resume?id=" + id,
			"delete_url": "api/console/agents/delete?id=" + id,
		})
	}
	writeJSON(w, rows)
}

// channelLabelForRow returns the friendly name of a bridge's target channel
// for the console row, or "" when it has none (wakes the agent's own thread).
func channelLabelForRow(m EventMonitor) string {
	if ch := strings.TrimSpace(m.WakeChannel); ch != "" {
		if c, ok := GetChannel(RootDB, m.Owner, ch); ok && strings.TrimSpace(c.Name) != "" {
			return c.Name
		}
		return ch
	}
	return ""
}

// handleConsoleBridgeSetChannel hooks the bridge (owner+name) to a channel, or
// detaches it when channel_id is empty. Reuses the same shared logic the bridge
// tool's update action calls, so UI and tool behave identically.
func (T *OrchestrateApp) handleConsoleBridgeSetChannel(w http.ResponseWriter, r *http.Request) {
	m, ok := T.consoleBridgeMonitor(w, r)
	if !ok {
		return
	}
	channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
	if channelID == "" {
		// Also accept it in the JSON body (ActionList posts {id} → channel_id).
		var body struct {
			ChannelID string `json:"channel_id"`
			ID        string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		channelID = strings.TrimSpace(body.ChannelID)
		if channelID == "" {
			channelID = strings.TrimSpace(body.ID)
		}
	}
	if _, err := setBridgeChannel(m.Owner, m.Name, channelID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleBridgeChannels lists a given owner's channels as {id,label,desc}
// for the Connect picker. A bridge can deliver into ANY of the owner's channels
// (it's a delivery target, not an exclusive transport binding), so — unlike the
// messaging Connect picker — occupied channels are included.
func (T *OrchestrateApp) handleConsoleBridgeChannels(w http.ResponseWriter, r *http.Request) {
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	if owner == "" {
		if u, _, ok := RequireUser(w, r, T.DB); ok {
			owner = u
		} else {
			return
		}
	}
	udb := UserDB(T.DB, owner)
	rows := []map[string]any{}
	for _, ch := range ListChannels(RootDB, owner) {
		label := ch.Name
		if label == "" {
			label = ch.ID
		}
		agentName := ch.AgentID
		if a, ok := loadAgent(udb, ch.AgentID); ok && strings.TrimSpace(a.Name) != "" {
			agentName = a.Name
		}
		rows = append(rows, map[string]any{
			"id":    ch.ID,
			"label": label,
			"desc":  "Agent: " + agentName,
		})
	}
	writeJSON(w, rows)
}

// handleConsoleBridges lists every owner's bridges (watch-kind event
// monitors created by the bridge tool) for the Admin > Bridges table.
// Admin-gated at the route (adminGated), so cross-owner listing is
// deliberate — the admin governs all bridges, not just their own.
func (T *OrchestrateApp) handleConsoleBridges(w http.ResponseWriter, r *http.Request) {
	rows := []map[string]any{}
	for _, m := range ListAllEventMonitors(RootDB) {
		if !isBridgeMonitor(m) {
			continue
		}
		state := "active"
		if m.Paused {
			state = "paused"
		}
		lastFired := ""
		if !m.LastFired.IsZero() {
			lastFired = m.LastFired.Local().Format("Jan 2 3:04 PM")
		}
		// Destination: a channel target (Stage B) delivers into that channel's
		// conversation; otherwise the alert wakes the agent's own thread.
		dest := fmt.Sprintf("wake %q", m.WakeAgent)
		if ch := strings.TrimSpace(m.WakeChannel); ch != "" {
			label := ch
			if c, ok := GetChannel(RootDB, m.Owner, ch); ok && strings.TrimSpace(c.Name) != "" {
				label = c.Name
			}
			dest = fmt.Sprintf("→ channel %q", label)
		}
		rows = append(rows, map[string]any{
			"name":         m.Name,
			"owner":        m.Owner,
			"credential":   strings.TrimPrefix(m.ToolName, bridgeCredToolPrefix),
			"detail":       fmt.Sprintf("every %ds: %v %s", m.IntervalSeconds, m.ToolArgs["url"], dest),
			"state":        state,
			"last_fired":   lastFired,
			"_paused":      m.Paused,
			"_connected":   strings.TrimSpace(m.WakeChannel) != "",
			"channel_name": channelLabelForRow(m),
		})
	}
	writeJSON(w, rows)
}

// handleConsoleBridgeThread returns the recent messages of the channel a poll
// bridge delivers into, so the /bridges/ poll table can SHOW what's landed in
// that conversation (the HistoryPanel expand). Resolves the bridge → its target
// channel → the session that channel's inbound actually runs in (the cortex
// thread for a dedicated cortex agent, else the per-source channel session, via
// effectiveChannelSession — the SAME resolver delivery uses, so the view can't
// diverge from where the messages actually land). Emits [] (not an error) when
// the bridge targets no channel or nothing has landed yet.
func (T *OrchestrateApp) handleConsoleBridgeThread(w http.ResponseWriter, r *http.Request) {
	m, ok := T.consoleBridgeMonitor(w, r)
	if !ok {
		return
	}
	out := []ChannelLine{}
	if wc := strings.TrimSpace(m.WakeChannel); wc != "" {
		if ch, found := GetChannel(RootDB, m.Owner, wc); found && ch.AgentID != "" {
			sid := T.effectiveChannelSession(m.Owner, ch.AgentID, ChannelSessionKey(ch, "bridge:"+m.Name))
			if sess, ok := loadChatSession(UserDB(T.DB, m.Owner), ch.AgentID, sid); ok {
				out = channelLinesFromMessages(sess.Messages, 50)
			}
		}
	}
	writeJSON(w, out)
}

// channelLinesFromMessages projects a session's stored turns into the
// HistoryPanel row shape (role / sender / text / timestamp), skipping empty and
// hidden messages, keeping the last `limit` (0 = all). Sender comes from
// ReportFrom, which channel inbound cards carry (e.g. "bridge:<name>").
func channelLinesFromMessages(msgs []ChatMessage, limit int) []ChannelLine {
	out := []ChannelLine{}
	for _, msg := range msgs {
		text := strings.TrimSpace(msg.Content)
		if text == "" || msg.Hidden {
			continue
		}
		ts := ""
		if !msg.Created.IsZero() {
			ts = msg.Created.Local().Format("Jan 2 3:04 PM")
		}
		out = append(out, ChannelLine{Role: msg.Role, Sender: strings.TrimSpace(msg.ReportFrom), Text: text, Timestamp: ts})
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// consoleBridgeMonitor resolves the bridge a row action targets from
// ?owner=&name=. Returns ok=false after writing the error response.
func (T *OrchestrateApp) consoleBridgeMonitor(w http.ResponseWriter, r *http.Request) (EventMonitor, bool) {
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	m, found := GetEventMonitor(RootDB, owner, name)
	if !found || !isBridgeMonitor(m) {
		http.Error(w, "no such bridge", http.StatusNotFound)
		return EventMonitor{}, false
	}
	return m, true
}

func (T *OrchestrateApp) setConsoleBridgePaused(w http.ResponseWriter, r *http.Request, paused bool) {
	m, ok := T.consoleBridgeMonitor(w, r)
	if !ok {
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

func (T *OrchestrateApp) handleConsoleBridgePause(w http.ResponseWriter, r *http.Request) {
	T.setConsoleBridgePaused(w, r, true)
}

func (T *OrchestrateApp) handleConsoleBridgeResume(w http.ResponseWriter, r *http.Request) {
	T.setConsoleBridgePaused(w, r, false)
}

func (T *OrchestrateApp) handleConsoleBridgeDelete(w http.ResponseWriter, r *http.Request) {
	m, ok := T.consoleBridgeMonitor(w, r)
	if !ok {
		return
	}
	if m.SchedulerID != "" {
		UnscheduleTask(m.SchedulerID)
	}
	DeleteEventMonitor(RootDB, m.Owner, m.Name)
	w.WriteHeader(http.StatusNoContent)
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
	sid := cortexSessionID(agentID)
	deleteChatSession(udb, agentID, sid) // includes the compact state
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

type consoleAgentRow struct {
	Name string `json:"name"`
	// Mission is the standing brief handed to the agent each run — "what it's
	// told to do." Surfaced so the Enabled-agents view shows each agent's
	// instructions, not just its schedule/status. Renders as a detail line
	// under the name in the cards layout.
	Mission  string `json:"mission,omitempty"`
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
	// Scope to the agent this pane is for — a standing agent runs (and was
	// created from) AgentID, so without this every agent's pane shows every
	// agent's scheduled agents with no way to tell which belongs where.
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	rows := []consoleAgentRow{}
	for _, sa := range ListStandingAgents(RootDB, user) {
		// A standing agent belongs to the CONTROLLER that created and manages it
		// (ReportAgentID) — that's whose console this pane is. AgentID is the
		// target agent it RUNS (e.g. a "Market Research" job created from the
		// Operator), which is a different agent, so scoping by it hid the job
		// from its own controller. Empty ReportAgentID (legacy) stays visible
		// everywhere rather than vanishing.
		controller := sa.ReportAgentID
		if agentID != "" && controller != "" && controller != agentID {
			continue
		}
		state := "active"
		if sa.Paused {
			state = "paused"
		}
		row := consoleAgentRow{Name: sa.Name, Mission: sa.Mission, State: state, Schedule: StandingScheduleLabel(sa), ID: sa.Name, Paused: sa.Paused}
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
	// Scope to the agent this pane is for — a monitor belongs to the agent it
	// wakes (WakeAgent, set on create), so without this every agent's pane shows
	// every agent's monitors.
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	rows := []consoleMonitorRow{}
	for _, m := range ListEventMonitors(RootDB, user) {
		// A monitor belongs to the agent it wakes. An empty WakeAgent means the
		// default Chat agent (seed-chat) — map it so those monitors show on
		// Chat's pane instead of vanishing from every pane.
		wake := m.WakeAgent
		if wake == "" {
			wake = "seed-chat"
		}
		if agentID != "" && wake != agentID {
			continue
		}
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
	case "bind_thread":
		who = operatorApprovalRecipient(user, a)
		detail = "bind 1:1 thread so the agent can read replies"
		if a.Brief != "" {
			detail = "bind 1:1 thread (" + a.Brief + ")"
		}
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
		Managed   bool   `json:"_managed,omitempty"` // a standing policy row (segmented control + Remove)
		Policy    string `json:"_policy,omitempty"`  // allow | ask | block (the segmented state)
	}
	out := []permRow{}
	// Zone 1 — live pending requests (a decision is blocked on the user).
	for _, a := range ListAuthorizations(RootDB, user) {
		who, detail := approvalDisplay(udb, user, a)
		out = append(out, permRow{
			Who: who, Detail: detail, Status: "Pending",
			Requested: a.Requested.Local().Format("Jan 2 3:04 PM"),
			ID:        a.ID, Pending: true,
		})
	}
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
	writeJSON(w, out)
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
		if _, err := operatorDeliverMessage(a.Owner, a.ChatID, a.Handle, a.Text, a.Images); err != nil {
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
