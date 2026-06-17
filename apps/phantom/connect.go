package phantom

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Connecting a phantom chat to an agent's channel — the manual "hook a source"
// action. A channel is an inert interface on an agent until a source is hooked
// in; here phantom (the iMessage source) points a conversation at one, which
// stamps the channel's source (service=imessage + address=this chat's handle)
// so inbound from the chat routes to the channel's bound agent. Migration does
// this automatically; this is the manual path for an existing agent channel.

// handleAgentChannels lists the owner's agent channels so phantom can offer a
// "connect this chat to a channel" picker.
//
//	GET /phantom/api/agent-channels → [{id, name, agent, service, service_label, address, connected}]
func (T *Phantom) handleAgentChannels(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	owner := phantomToolOwner(T.DB)
	if owner == "" || owner != user {
		http.Error(w, "only the phantom owner can manage channels", http.StatusForbidden)
		return
	}
	agentName := map[string]string{}
	if orch := findOrchestrate(); orch != nil {
		for _, a := range orch.AgentsForUser(owner) {
			agentName[a.ID] = a.Name
		}
	}
	type channelOpt struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Label        string `json:"label"` // render-ready for the ActionList picker
		Desc         string `json:"desc"`
		Agent        string `json:"agent"`
		Service      string `json:"service"`
		ServiceLabel string `json:"service_label"`
		Address      string `json:"address"`
		Connected    bool   `json:"connected"`
	}
	out := []channelOpt{}
	for _, c := range ListChannels(RootDB, owner) {
		label := c.Name
		if label == "" {
			label = c.ID
		}
		desc := "Agent: " + agentName[c.AgentID]
		if c.Service != "" {
			desc += " · connected (" + ServiceDisplayName(c.Service) + " → " + c.Address + ")"
		} else {
			desc += " · not connected"
		}
		out = append(out, channelOpt{
			ID:           c.ID,
			Name:         c.Name,
			Label:        label,
			Desc:         desc,
			Agent:        agentName[c.AgentID],
			Service:      c.Service,
			ServiceLabel: ServiceDisplayName(c.Service),
			Address:      c.Address,
			Connected:    c.Service != "",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleConnectChannel points a conversation at an agent's channel by stamping
// the channel's source. channel_id="" disconnects (clears the source, leaving
// the channel inert).
//
//	POST /phantom/api/connect-channel  {chat_id, channel_id}
func (T *Phantom) handleConnectChannel(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	owner := phantomToolOwner(T.DB)
	if owner == "" || owner != user {
		http.Error(w, "only the phantom owner can connect channels", http.StatusForbidden)
		return
	}
	var req struct {
		ChatID    string `json:"chat_id"`
		ChannelID string `json:"channel_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	// The ActionList picker posts via query params (PostTo substitution); accept
	// those when the JSON body didn't carry them.
	if q := r.URL.Query(); true {
		if req.ChatID == "" {
			req.ChatID = q.Get("chat_id")
		}
		if req.ChannelID == "" {
			req.ChannelID = q.Get("channel_id")
		}
	}
	if req.ChatID == "" {
		http.Error(w, "chat_id is required", http.StatusBadRequest)
		return
	}
	// Disconnect: clear the source on whatever channel currently points at this
	// chat's handle, leaving it inert.
	var conv Conversation
	T.DB.Get(conversationTable, req.ChatID, &conv)
	addr := firstNonEmpty(conv.Handle, req.ChatID)
	if req.ChannelID == "" {
		for _, c := range ListChannels(RootDB, owner) {
			if c.Service == phantomDefaultService && c.Address == addr {
				c.Service = ""
				c.Address = ""
				SaveChannel(RootDB, c)
				Log("[phantom] disconnected chat %s from channel %q", req.ChatID, c.Name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"connected": false})
		return
	}
	ch, found := GetChannel(RootDB, owner, req.ChannelID)
	if !found {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	ch.Service = phantomDefaultService
	ch.Address = addr
	SaveChannel(RootDB, ch)
	// Exclusive connect: clear the source from any OTHER channel already bound to
	// a sibling id of this conversation, so the chat routes to exactly the one
	// just picked (no duplicate channels fighting over the same inbound).
	convIDs := map[string]bool{addr: true, req.ChatID: true}
	for _, id := range append([]string{conv.Handle, conv.ChatID}, conv.AliasHandles...) {
		if id = strings.TrimSpace(id); id != "" {
			convIDs[id] = true
		}
	}
	for _, other := range ListChannels(RootDB, owner) {
		if other.ID == ch.ID || other.Service != phantomDefaultService || !convIDs[other.Address] {
			continue
		}
		other.Service = ""
		other.Address = ""
		SaveChannel(RootDB, other)
		Log("[phantom] connect: cleared duplicate source on channel %q (was addr=%q)", other.Name, other.Address)
	}
	Log("[phantom] connected chat %s (addr=%s) to channel %q (agent=%s)", req.ChatID, addr, ch.Name, ch.AgentID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"connected": true, "channel_id": ch.ID, "address": addr})
}
