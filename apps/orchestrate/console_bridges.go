package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

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
func (T *OrchestrateApp) handleConsoleBridgeSetChannel(c *jsonapi.Call, raw json.RawMessage) (any, error) {
	r := c.R
	m, mErr := T.consoleBridgeMonitor(c)
	if mErr != nil {
		return nil, mErr
	}
	channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
	if channelID == "" {
		// Also accept it in the JSON body (ActionList posts {id} → channel_id).
		var body struct {
			ChannelID string `json:"channel_id"`
			ID        string `json:"id"`
		}
		_ = json.Unmarshal(raw, &body)
		channelID = strings.TrimSpace(body.ChannelID)
		if channelID == "" {
			channelID = strings.TrimSpace(body.ID)
		}
	}
	if _, err := setBridgeChannel(m.Owner, m.Name, channelID); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleConsoleBridgeChannels lists a given owner's channels as {id,label,desc}
// for the Connect picker. A bridge can deliver into ANY of the owner's channels
// (it's a delivery target, not an exclusive transport binding), so — unlike the
// messaging Connect picker — occupied channels are included.
func (T *OrchestrateApp) handleConsoleBridgeChannels(c *jsonapi.Call) (any, error) {
	r := c.R
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	if owner == "" {
		// Behind the admin gate, which admits a request without asking who
		// it is: with no owner named, the reader's own channels.
		if c.User == "" {
			return nil, jsonapi.Status(http.StatusUnauthorized, "unauthorized")
		}
		owner = c.User
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
	return rows, nil
}

// handleConsoleBridges lists every owner's bridges (watch-kind event
// monitors created by the bridge tool) for the Admin > Bridges table.
// Admin-gated at the route (adminGated), so cross-owner listing is
// deliberate — the admin governs all bridges, not just their own.
func (T *OrchestrateApp) handleConsoleBridges(c *jsonapi.Call) (any, error) {
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
	return rows, nil
}

// handleConsoleBridgeThread returns the recent messages of the channel a poll
// bridge delivers into, so the /bridges/ poll table can SHOW what's landed in
// that conversation (the HistoryPanel expand). Resolves the bridge → its target
// channel → the session that channel's inbound actually runs in (the cortex
// thread for a dedicated cortex agent, else the per-source channel session, via
// effectiveChannelSession — the SAME resolver delivery uses, so the view can't
// diverge from where the messages actually land). Emits [] (not an error) when
// the bridge targets no channel or nothing has landed yet.
func (T *OrchestrateApp) handleConsoleBridgeThread(c *jsonapi.Call) (any, error) {
	m, mErr := T.consoleBridgeMonitor(c)
	if mErr != nil {
		return nil, mErr
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
	return out, nil
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
func (T *OrchestrateApp) consoleBridgeMonitor(c *jsonapi.Call) (EventMonitor, error) {
	owner := strings.TrimSpace(c.R.URL.Query().Get("owner"))
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	m, found := GetEventMonitor(RootDB, owner, name)
	if !found || !isBridgeMonitor(m) {
		return EventMonitor{}, jsonapi.NotFound("no such bridge")
	}
	return m, nil
}

func (T *OrchestrateApp) setConsoleBridgePaused(c *jsonapi.Call, paused bool) (any, error) {
	m, mErr := T.consoleBridgeMonitor(c)
	if mErr != nil {
		return nil, mErr
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
	return jsonapi.Empty(http.StatusNoContent), nil
}

func (T *OrchestrateApp) handleConsoleBridgePause(c *jsonapi.Call, _ struct{}) (any, error) {
	return T.setConsoleBridgePaused(c, true)
}

func (T *OrchestrateApp) handleConsoleBridgeResume(c *jsonapi.Call, _ struct{}) (any, error) {
	return T.setConsoleBridgePaused(c, false)
}

func (T *OrchestrateApp) handleConsoleBridgeDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	m, mErr := T.consoleBridgeMonitor(c)
	if mErr != nil {
		return nil, mErr
	}
	if m.SchedulerID != "" {
		UnscheduleTask(m.SchedulerID)
	}
	DeleteEventMonitor(RootDB, m.Owner, m.Name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleConsoleGrants lists the owner's STANDING authorizations — the "Always
// allow" grants that let future delegations/messages skip the approval queue.
// Distinct from the pending queue (/api/console/approvals): those are one-time
// and vanish once acted on; these persist until revoked. Each row carries a
// "_id" of "<kind>:<target>" the Revoke action round-trips. GET only.
func (T *OrchestrateApp) handleConsoleGrants(c *jsonapi.Call) (any, error) {
	user := c.User
	out := []map[string]any{}
	for _, agent := range ListDelegationPreAuthorizations(RootDB, user) {
		out = append(out, map[string]any{"_id": "agent:" + agent, "Kind": "Agent", "Target": agent})
	}
	for _, handle := range ListContactPreAuthorizations(RootDB, user) {
		out = append(out, map[string]any{"_id": "contact:" + handle, "Kind": "Contact", "Target": handle})
	}
	return out, nil
}

// handleGrantRevoke clears one standing grant. id = "<kind>:<target>" from the
// grants list; future actions to that agent/contact queue for approval again.
func (T *OrchestrateApp) handleGrantRevoke(c *jsonapi.Call, _ struct{}) (any, error) {
	r := c.R
	user := c.User
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	kind, target, found := strings.Cut(id, ":")
	if !found || target == "" {
		return nil, jsonapi.BadRequest("bad grant id")
	}
	switch kind {
	case "agent":
		SetDelegationPreAuthorized(RootDB, user, "", target, false)
	case "contact":
		SetContactPreAuthorized(RootDB, user, "", target, false)
	default:
		return nil, jsonapi.BadRequest("unknown grant kind")
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleChannelClear wipes an agent's Cortex home-thread conversation and its
// rolling summary / fold cursor — the cheap fix for a crystallized thread.
// Operational state (monitors, standing agents, approvals) is left untouched;
// that is not this button's job. POST ?agent=<id>.
func (T *OrchestrateApp) handleChannelClear(c *jsonapi.Call, _ struct{}) (any, error) {
	r := c.R
	udb := c.UDB
	agentID := consoleAgentID(r)
	sid := cortexSessionID(agentID)
	deleteChatSession(udb, agentID, sid) // includes the compact state
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleChannelCompact force-folds an agent's Cortex home thread now: messages
// older than the verbatim recent tail collapse into the rolling summary (and are
// archived to searchable history), rather than being wiped. The gentle middle
// ground between doing nothing and Clear Cortex. The fold runs in the background
// (single-flight per thread), so this returns immediately. POST ?agent=<id>.
func (T *OrchestrateApp) handleChannelCompact(c *jsonapi.Call, _ struct{}) (any, error) {
	r := c.R
	user, udb := c.User, c.UDB
	agentID := consoleAgentID(r)
	agent, ok := loadAgent(udb, agentID)
	if !ok {
		return nil, jsonapi.NotFound("agent not found")
	}
	// Nothing to fold into when the rolling summary is turned off — the thread
	// bounds by forgetting old messages instead, so there's no summary to build.
	if agent.DisableCompaction {
		return nil, jsonapi.BadRequest("compaction is disabled for this agent: nothing to compact")
	}
	keepRecent := agent.ContextDepth
	if keepRecent <= 0 {
		keepRecent = 12
	}
	// Trigger=1 forces a fold regardless of how short the unsummarized tail is:
	// everything older than the KeepRecent verbatim tail folds now.
	T.maybeFoldOperatorHistory(udb, user, agent, cortexSessionID(agentID), CompactionConfig{KeepRecent: keepRecent, Trigger: 1})
	return jsonapi.Empty(http.StatusNoContent), nil
}
