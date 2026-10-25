package bridges

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// Connecting a conversation to an agent's channel — the "hook a source" control
// lifted from phantom. A Channel (core) is the interface to an agent; connecting
// a chat stamps the channel's source (service + the chat's address) so inbound
// over a bridge routes to that channel's bound agent.

// handleConversations lists the caller's chats, with which channel (if any)
// each is currently connected to.
func (T *Bridges) handleConversations(c *jsonapi.Call) (any, error) {
	user := c.User
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
	for _, c := range T.convosFor(user, RequestIsAdmin(c.R)) {
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
	return out, nil
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
func (T *Bridges) handleIncomingConvos(c *jsonapi.Call) (any, error) {
	user := c.User
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
	for _, c := range T.convosFor(user, RequestIsAdmin(c.R)) {
		// Curated or already-connected chats are in the managed list, not here.
		if c.Added || connected(c) {
			continue
		}
		// Show the last message so a contact is recognizable at a glance (the
		// "dropdown of contacts with their last message").
		desc := ServiceDisplayName(c.Service)
		if msgs := T.recentMessages(c.Owner, c.ChatID, 1); len(msgs) > 0 {
			if t := strings.TrimSpace(msgs[len(msgs)-1].Text); t != "" {
				desc = truncateText(t, 80)
			}
		}
		out = append(out, row{ID: c.ChatID, Label: firstNonEmpty(c.DisplayName, c.Handle, c.ChatID), Desc: desc})
	}
	return out, nil
}

// handleAddConvo curates a conversation into the managed list — either an
// existing incoming chat (by chat_id) or a brand-new one entered by number
// (handle + service), so an operator can pre-bind a contact before any message
// has arrived.
//
//	POST /bridges/api/add-convo?chat_id=<id>   (pick an incoming chat)
//	POST /bridges/api/add-convo  {handle, service}   (manual number)
func (T *Bridges) handleAddConvo(c *jsonapi.Call, req addConvoReq) (any, error) {
	user, admin := c.User, RequestIsAdmin(c.R)
	if req.ChatID == "" {
		req.ChatID = c.R.URL.Query().Get("chat_id")
	}

	// Manual entry: no chat_id, just a number/handle — synthesize a Convo keyed
	// by the handle so it can be connected before any inbound arrives.
	if req.ChatID == "" {
		handle := strings.TrimSpace(req.Handle)
		if handle == "" {
			return nil, jsonapi.BadRequest("chat_id or handle is required")
		}
		svc := firstNonEmpty(strings.TrimSpace(req.Service), "imessage")
		// A new entry is the caller's. One already on record must be theirs
		// too; another user's reads as not found, same as every chat_id path.
		cv, exists := T.getConvo(user, handle)
		if !exists {
			cv.Owner = user
		}
		cv.ChatID = handle
		cv.Service = svc
		cv.Handle = handle
		cv.Added = true
		T.saveConvo(cv)
		return map[string]any{"added": true, "chat_id": handle}, nil
	}

	cv, found := T.convoFor(req.ChatID, user, admin)
	if !found {
		return nil, jsonapi.NotFound("conversation not found")
	}
	cv.Added = true
	T.saveConvo(cv)
	return map[string]any{"added": true, "chat_id": cv.ChatID}, nil
}

// handleAddConvoForm is the manual-entry form's prefill: a blank record.
func (T *Bridges) handleAddConvoForm(c *jsonapi.Call) (any, error) {
	return map[string]any{"handle": "", "service": "imessage"}, nil
}

// addConvoReq is what handleAddConvo is posted. The picker posts no body and
// names the chat in the query string instead.
type addConvoReq struct {
	ChatID  string `json:"chat_id"`
	Handle  string `json:"handle"`
	Service string `json:"service"`
}

// handleAgentChannels lists the owner's CHANNELS available to connect — a
// channel is the interface to an agent, and only an unbound (free) one is
// offered so connecting can't steal a channel already serving another chat.
func (T *Bridges) handleAgentChannels(c *jsonapi.Call) (any, error) {
	user := c.User
	agentName := map[string]string{}
	if host := agenthost.Get(); host != nil {
		agentName = host.AgentNames(user)
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
	return out, nil
}

// handleSetAutoReply flips a channel's per-channel auto-reply switch (like
// phantom's per-conversation toggle). Off = inbound recorded, agent not run.
//
//	PATCH /bridges/api/set-autoreply?id=<channel_id>  {auto_reply}
func (T *Bridges) handleSetAutoReply(c *jsonapi.Call, req autoReplyReq) (any, error) {
	ch, found := GetChannel(RootDB, c.User, c.R.URL.Query().Get("id"))
	if !found {
		return nil, jsonapi.NotFound("channel not found")
	}
	ch.AutoReply = req.AutoReply
	SaveChannel(RootDB, ch)
	return map[string]any{"id": ch.ID, "auto_reply": ch.AutoReply}, nil
}

// autoReplyReq is what handleSetAutoReply is sent. No body turns auto-reply
// off; a cross-site request cannot do that, because the route answers only a
// state-changing method and those are held to the same origin.
type autoReplyReq struct {
	AutoReply bool `json:"auto_reply"`
}

// handleConnectChannel binds a conversation to an AGENT. The picker passes an
// agent id; we reuse a free channel for that agent or create one, then stamp
// this chat's source onto it. agent_id="" (or legacy channel_id="") disconnects.
// Accepts query params (the ActionList posts via PostTo substitution) or a body.
func (T *Bridges) handleConnectChannel(c *jsonapi.Call, req connectChannelReq) (any, error) {
	user := c.User
	q := c.R.URL.Query()
	if req.ChatID == "" {
		req.ChatID = q.Get("chat_id")
	}
	if req.ChannelID == "" {
		req.ChannelID = q.Get("channel_id")
	}
	if req.ChatID == "" {
		return nil, jsonapi.BadRequest("chat_id is required")
	}
	// Only the caller's own conversation connects: binding another user's chat
	// to your channel would route their contact's messages to your agent.
	cv, found := T.convoFor(req.ChatID, user, RequestIsAdmin(c.R))
	if !found {
		return nil, jsonapi.NotFound("conversation not found")
	}
	svc := firstNonEmpty(cv.Service, "imessage")
	// A group binds to its stable chat id (no single member owns the room); a
	// 1:1 binds to the contact's handle so it routes even if the chat id varies.
	addr := firstNonEmpty(cv.Handle, req.ChatID)
	if isGroupChat(req.ChatID) {
		addr = req.ChatID
	}
	// Connecting curates the chat into the managed list (in case it was added
	// straight from the incoming picker's Connect path).
	if !cv.Added {
		cv.Added = true
		T.saveConvo(cv)
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
		return map[string]any{"connected": false}, nil
	}
	ch, found := GetChannel(RootDB, user, req.ChannelID)
	if !found {
		return nil, jsonapi.NotFound("channel not found")
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
	return map[string]any{"connected": true, "channel_id": ch.ID, "address": addr}, nil
}

// connectChannelReq is what handleConnectChannel is posted. The table's row
// actions post no body and name the chat and channel in the query string.
type connectChannelReq struct {
	ChatID    string `json:"chat_id"`
	ChannelID string `json:"channel_id"`
}
