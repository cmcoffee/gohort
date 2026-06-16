package core

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Channel binds a messaging surface — a Service transport (imessage /
// telegram / slack) plus an address scope within it — to an orchestrate
// Agent. A message arriving on the channel runs the bound agent's loop;
// the reply goes back out the same surface. Phantom is the transport (it
// POSTs inbound and drains the outbox per Service); the binding here says
// WHICH agent handles a given channel. See docs/channels-and-agents.md.
//
// The binding is authoritative on the Channel (AgentID), so routing an
// inbound is a single lookup and "one channel, one agent" is an
// invariant rather than a convention. An agent's "attached channels" is
// the inverse view (ListChannelsForAgent), which is what the agent
// editor surfaces.
//
// Phase 1 (this) is data model + attach only — nothing routes inbound to
// the bound agent yet. Phase 2 wires ChannelForInbound into the inbound
// path. See [[project_channels_to_agents]].
type Channel struct {
	ID      string `json:"id"`
	Owner   string `json:"owner"`          // gohort user who owns this channel
	Name    string `json:"name,omitempty"` // friendly label
	Service string `json:"service"`        // transport id: imessage / telegram / slack
	// Address scopes the binding within the service: "" means the WHOLE
	// service (every contact/room routes to AgentID); a specific value
	// (a handle / room id) binds just that conversation, overriding the
	// whole-service default.
	Address   string `json:"address,omitempty"`
	AgentID   string `json:"agent_id"`             // the orchestrate agent bound to this channel
	AutoReply bool   `json:"auto_reply,omitempty"` // channel-layer policy: answer inbound automatically (vs record-only)
	Created   string `json:"created,omitempty"`
}

const channelsTable = "channels"

func channelKey(owner, id string) string { return owner + ":" + id }

// NewChannelID returns a random, unguessable channel id.
func NewChannelID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SaveChannel upserts a channel binding (requires Owner + ID).
func SaveChannel(db Database, ch Channel) {
	if db == nil || ch.Owner == "" || ch.ID == "" {
		return
	}
	db.Set(channelsTable, channelKey(ch.Owner, ch.ID), ch)
}

// GetChannel fetches one channel, owner-scoped.
func GetChannel(db Database, owner, id string) (Channel, bool) {
	if db == nil || owner == "" || id == "" {
		return Channel{}, false
	}
	var ch Channel
	if !db.Get(channelsTable, channelKey(owner, id), &ch) {
		return Channel{}, false
	}
	return ch, true
}

// ListChannels returns all of the owner's channels.
func ListChannels(db Database, owner string) []Channel {
	if db == nil || owner == "" {
		return nil
	}
	prefix := owner + ":"
	var out []Channel
	for _, k := range db.Keys(channelsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var ch Channel
		if db.Get(channelsTable, k, &ch) {
			out = append(out, ch)
		}
	}
	return out
}

// ListChannelsForAgent returns the owner's channels bound to a given agent —
// the inverse view that backs the agent editor's "attached channels".
func ListChannelsForAgent(db Database, owner, agentID string) []Channel {
	var out []Channel
	for _, ch := range ListChannels(db, owner) {
		if ch.AgentID == agentID {
			out = append(out, ch)
		}
	}
	return out
}

// DeleteChannel removes a channel binding.
func DeleteChannel(db Database, owner, id string) {
	if db == nil {
		return
	}
	db.Unset(channelsTable, channelKey(owner, id))
}

// ChannelForInbound resolves which channel — and therefore which agent —
// handles an inbound message on a service for an owner. An exact Address
// binding wins over a whole-service binding (Address==""), so a per-contact
// channel overrides the service-wide default. Returns false when nothing is
// bound. Phase 2 routing uses this; Phase 1 just stores the bindings.
func ChannelForInbound(db Database, owner, service, address string) (Channel, bool) {
	service = strings.TrimSpace(service)
	var wholeService *Channel
	for _, ch := range ListChannels(db, owner) {
		if ch.Service != service {
			continue
		}
		if ch.Address != "" && ch.Address == address {
			return ch, true // exact-address binding wins
		}
		if ch.Address == "" {
			c := ch
			wholeService = &c
		}
	}
	if wholeService != nil {
		return *wholeService, true
	}
	return Channel{}, false
}
