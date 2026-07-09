package bridges

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
)

// Connecting a conversation to an agent's channel — the "hook a source" control
// lifted from phantom. A Channel (core) is the interface to an agent; connecting
// a chat stamps the channel's source (service + the chat's address) so inbound
// over a bridge routes to that channel's bound agent.

var cachedOrch *orchestrate.OrchestrateApp

func findOrchestrate() *orchestrate.OrchestrateApp {
	if cachedOrch != nil {
		return cachedOrch
	}
	a, ok := FindAgent("orchestrate")
	if !ok {
		return nil
	}
	o, ok := a.(*orchestrate.OrchestrateApp)
	if !ok {
		return nil
	}
	cachedOrch = o
	return o
}

// handleConversations lists chats Bridges has seen, with which channel (if any)
// each is currently connected to.
func (T *Bridges) handleConversations(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	chans := ListChannels(RootDB, user)
	type row struct {
		ChatID       string `json:"chat_id"`
		Name         string `json:"name"`
		Service      string `json:"service"`
		ServiceLabel string `json:"service_label"`
		Connected    string `json:"connected"`
		ChannelID    string `json:"channel_id,omitempty"` // the connected channel, for the auto-reply toggle
		AutoReply    bool   `json:"auto_reply"`           // that channel's per-channel switch
		LastAt       string `json:"last_at,omitempty"`
	}
	out := []row{}
	for _, c := range T.listConvos() {
		connected, channelID, autoReply := "", "", false
		for _, ch := range chans {
			if ch.Service == c.Service && ch.Address != "" && (ch.Address == c.Handle || ch.Address == c.ChatID) {
				connected, channelID, autoReply = ch.Name, ch.ID, ch.AutoReply
				break
			}
		}
		// Show curated chats, plus any that are currently connected to a channel
		// (a live binding is implicitly managed — and must stay visible so it can
		// still be cleared, even if it predates the Added flag). Raw, unconnected
		// inbound lives in the Add picker until curated.
		if !c.Added && connected == "" {
			continue
		}
		out = append(out, row{
			ChatID: c.ChatID, Name: firstNonEmpty(c.DisplayName, c.Handle, c.ChatID),
			Service: c.Service, ServiceLabel: ServiceDisplayName(c.Service),
			Connected: connected, ChannelID: channelID, AutoReply: autoReply, LastAt: c.LastAt,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// truncateText shortens a string to n runes, appending an ellipsis when cut.
func truncateText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// handleIncomingConvos lists chats that have arrived over a bridge but the
// operator hasn't curated yet — the pick-list behind the "Add a conversation"
// affordance. Already-added chats drop off (they're in the managed list).
//
//	GET /bridges/api/incoming-convos → [{id(chat_id), label, desc}]
func (T *Bridges) handleIncomingConvos(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	chans := ListChannels(RootDB, user)
	connected := func(c Convo) bool {
		for _, ch := range chans {
			if ch.Service == c.Service && ch.Address != "" && (ch.Address == c.Handle || ch.Address == c.ChatID) {
				return true
			}
		}
		return false
	}
	type row struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Desc  string `json:"desc"`
	}
	out := []row{}
	for _, c := range T.listConvos() {
		// Curated or already-connected chats are in the managed list, not here.
		if c.Added || connected(c) {
			continue
		}
		// Show the last message so a contact is recognizable at a glance (the
		// "dropdown of contacts with their last message").
		desc := ServiceDisplayName(c.Service)
		if msgs := T.recentMessages(c.ChatID, 1); len(msgs) > 0 {
			if t := strings.TrimSpace(msgs[len(msgs)-1].Text); t != "" {
				desc = truncateText(t, 80)
			}
		}
		out = append(out, row{ID: c.ChatID, Label: firstNonEmpty(c.DisplayName, c.Handle, c.ChatID), Desc: desc})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleAddConvo curates a conversation into the managed list — either an
// existing incoming chat (by chat_id) or a brand-new one entered by number
// (handle + service), so an operator can pre-bind a contact before any message
// has arrived.
//
//	POST /bridges/api/add-convo?chat_id=<id>   (pick an incoming chat)
//	POST /bridges/api/add-convo  {handle, service}   (manual number)
func (T *Bridges) handleAddConvo(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	// The manual-entry form GETs this for prefill — hand back a blank record.
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"handle": "", "service": "imessage"})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ChatID  string `json:"chat_id"`
		Handle  string `json:"handle"`
		Service string `json:"service"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ChatID == "" {
		req.ChatID = r.URL.Query().Get("chat_id")
	}

	// Manual entry: no chat_id, just a number/handle — synthesize a Convo keyed
	// by the handle so it can be connected before any inbound arrives.
	if req.ChatID == "" {
		handle := strings.TrimSpace(req.Handle)
		if handle == "" {
			http.Error(w, "chat_id or handle is required", http.StatusBadRequest)
			return
		}
		svc := firstNonEmpty(strings.TrimSpace(req.Service), "imessage")
		c, _ := T.getConvo(handle)
		c.ChatID = handle
		c.Service = svc
		c.Handle = handle
		c.Added = true
		T.saveConvo(c)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"added": true, "chat_id": handle})
		return
	}

	c, ok := T.getConvo(req.ChatID)
	if !ok {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	c.Added = true
	T.saveConvo(c)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"added": true, "chat_id": c.ChatID})
}

// handleAgentChannels lists the owner's CHANNELS available to connect — a
// channel is the interface to an agent, and only an unbound (free) one is
// offered so connecting can't steal a channel already serving another chat.
func (T *Bridges) handleAgentChannels(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentName := map[string]string{}
	if orch := findOrchestrate(); orch != nil {
		for _, a := range orch.AgentsForUser(user) {
			agentName[a.ID] = a.Name
		}
	}
	type row struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Desc  string `json:"desc"`
	}
	out := []row{}
	for _, ch := range ListChannels(RootDB, user) {
		if ch.Service != "" {
			continue // occupied — already bound to a source
		}
		label := firstNonEmpty(ch.Name, ch.ID)
		desc := "Agent: " + agentName[ch.AgentID]
		out = append(out, row{ID: ch.ID, Label: label, Desc: desc})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleSetAutoReply flips a channel's per-channel auto-reply switch (like
// phantom's per-conversation toggle). Off = inbound recorded, agent not run.
//
//	PATCH /bridges/api/set-autoreply?id=<channel_id>  {auto_reply}
func (T *Bridges) handleSetAutoReply(w http.ResponseWriter, r *http.Request) {
	// Must not be GET-reachable: the body decode is best-effort, so a bodyless
	// cross-site GET would flip auto_reply to false. The UI calls this via POST.
	if !IsStateChangingMethod(r.Method) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	ch, found := GetChannel(RootDB, user, id)
	if !found {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	var req struct {
		AutoReply bool `json:"auto_reply"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	ch.AutoReply = req.AutoReply
	SaveChannel(RootDB, ch)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": ch.ID, "auto_reply": ch.AutoReply})
}

// handleConnectChannel binds a conversation to an AGENT. The picker passes an
// agent id; we reuse a free channel for that agent or create one, then stamp
// this chat's source onto it. agent_id="" (or legacy channel_id="") disconnects.
// Accepts query params (the ActionList posts via PostTo substitution) or a body.
func (T *Bridges) handleConnectChannel(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ChatID    string `json:"chat_id"`
		ChannelID string `json:"channel_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	q := r.URL.Query()
	if req.ChatID == "" {
		req.ChatID = q.Get("chat_id")
	}
	if req.ChannelID == "" {
		req.ChannelID = q.Get("channel_id")
	}
	if req.ChatID == "" {
		http.Error(w, "chat_id is required", http.StatusBadRequest)
		return
	}
	var c Convo
	T.DB.Get(convosTable, req.ChatID, &c)
	svc := firstNonEmpty(c.Service, "imessage")
	// A group binds to its stable chat id (no single member owns the room); a
	// 1:1 binds to the contact's handle so it routes even if the chat id varies.
	addr := firstNonEmpty(c.Handle, req.ChatID)
	if isGroupChat(req.ChatID) {
		addr = req.ChatID
	}
	// Connecting curates the chat into the managed list (in case it was added
	// straight from the incoming picker's Connect path).
	if !c.Added {
		c.ChatID, c.Added = req.ChatID, true
		T.saveConvo(c)
	}

	// Free any channel currently bound to this conversation (exclusive routing);
	// keepID is the one we're about to (re)bind.
	clearSiblings := func(keepID string) {
		for _, other := range ListChannels(RootDB, user) {
			if other.ID == keepID || other.Service != svc {
				continue
			}
			if other.Address != "" && (other.Address == addr || other.Address == req.ChatID) {
				other.Service, other.Address = "", ""
				SaveChannel(RootDB, other)
			}
		}
	}

	if req.ChannelID == "" {
		clearSiblings("")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"connected": false})
		return
	}
	ch, found := GetChannel(RootDB, user, req.ChannelID)
	if !found {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	ch.Service, ch.Address = svc, addr
	ch.AutoReply = true // connect = it answers; the per-channel toggle pauses it
	// Seed the default DM wake rules the first time this channel goes live as an
	// inbound surface: an inbound-capable channel with no per-channel gate would
	// otherwise fail open (answer everyone). Only fill when empty so an operator's
	// explicit gate (or an intentional blank on an outbound-only channel) is kept.
	if strings.TrimSpace(ch.Gatekeeper) == "" && ChannelDirection(ch) != DirectionOutbound {
		ch.Gatekeeper = DefaultDMGatekeeperRule
	}
	SaveChannel(RootDB, ch)
	clearSiblings(ch.ID)
	Log("[bridges] connected chat %s (addr=%s) to channel %q (agent=%s)", req.ChatID, addr, ch.Name, ch.AgentID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"connected": true, "channel_id": ch.ID, "address": addr})
}
