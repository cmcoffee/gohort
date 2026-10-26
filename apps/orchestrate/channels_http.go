package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/messaging"
)

// handleChannels manages an agent's attached messaging Channels. Phase 1 is
// data model + attach only — a binding here records WHICH agent owns a
// messaging surface, but nothing routes inbound to the agent yet (that's
// Phase 2). See docs/design/channels-and-agents.md. Owner-scoped; stored in RootDB
// alongside the other fleet data.
//
//	GET    /api/channels?agent_id=<id>  → list this owner's channels (filtered
//	                                       to the agent when agent_id is set)
//	POST   /api/channels?agent_id=<id>  → create a channel bound to the agent
//	DELETE /api/channels?id=<id>        → detach (delete) a channel
func (T *OrchestrateApp) handleChannels(c *jsonapi.Call, raw json.RawMessage) (any, error) {
	r := c.R
	user := c.User
	switch r.Method {
	case http.MethodGet:
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		// Carry a brand-correct service label (imessage → iMessage) alongside
		// each channel so the UI shows it without hardcoding service names.
		type channelView struct {
			Channel
			ServiceLabel string `json:"service_label,omitempty"`
			// ManageOnly: this channel relays into its agent's cortex (a DEDICATED
			// cortex agent — Cortex on + exactly one channel), so it has no
			// per-channel thread of its own. The rail makes its row open the manage
			// dialog instead of an (empty) thread; the conversation is read in the
			// cortex hero. Multi-channel / non-cortex agents keep per-room threads.
			ManageOnly bool `json:"manage_only,omitempty"`
			// State: the mark the rail draws when something feeding this
			// channel has come to rest — shape + tone + the sentence saying
			// why. Absent while everything feeding it is running, which is
			// most rows most of the time.
			State map[string]any `json:"state,omitempty"`
		}
		udb := UserDB(T.DB, user)
		// Read the monitors once for the whole listing rather than per row.
		monitors := ListEventMonitors(RootDB, user)
		out := []channelView{}
		for _, ch := range ListChannels(RootDB, user) {
			if agentID != "" && ch.AgentID != agentID {
				continue
			}
			manage := false
			if ag, ok := loadAgent(udb, ch.AgentID); ok && ag.Cortex &&
				len(ListChannelsForAgent(RootDB, user, ch.AgentID)) == 1 {
				manage = true
			}
			out = append(out, channelView{
				Channel: ch, ServiceLabel: ServiceDisplayName(ch.Service), ManageOnly: manage,
				State: channelRowState(ch, monitors),
			})
		}
		return out, nil

	case http.MethodPost:
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		var req struct {
			ID          string `json:"id"` // set → update in place; empty → create
			Name        string `json:"name"`
			Description string `json:"description"`
			Service     string `json:"service"`
			Address     string `json:"address"`
			AgentID     string `json:"agent_id"` // optional re-point on update / create from body
			AutoReply   bool   `json:"auto_reply"`
			Direction   string `json:"direction"`
			Gatekeeper  string `json:"gatekeeper"`
			TagOverride string `json:"tag_override"` // per-channel outbound name-tag override ("" = inherit)
			TagDisabled bool   `json:"tag_disabled"` // disable the outbound name tag on this channel
			// Who the channel answers. Pointers: a save that does not carry
			// them keeps what the channel has, so a caller that predates them
			// cannot open a closed channel by leaving them out.
			Senders        *string   `json:"senders"`
			AllowedHandles *[]string `json:"allowed_handles"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, jsonapi.BadRequest("bad request")
		}
		// Update in place when an id is given: preserve service/address/created,
		// refresh the editable fields, allow re-pointing the bound agent.
		if id := strings.TrimSpace(req.ID); id != "" {
			ch, found := GetChannel(RootDB, user, id)
			if !found {
				return nil, jsonapi.NotFound("channel not found")
			}
			// Specifying a channel is just the interface fields; the connector
			// (service + address) is attached separately, so it's preserved here
			// rather than wiped by an absent value.
			ch.Name = strings.TrimSpace(req.Name)
			ch.Description = strings.TrimSpace(req.Description)
			ch.AutoReply = req.AutoReply
			ch.Direction = strings.TrimSpace(req.Direction)
			ch.Gatekeeper = strings.TrimSpace(req.Gatekeeper)
			ch.TagOverride = strings.TrimSpace(req.TagOverride)
			ch.TagDisabled = req.TagDisabled
			applyChannelSenders(&ch, req.Senders, req.AllowedHandles)
			if a := strings.TrimSpace(req.AgentID); a != "" {
				if msg := subAgentChannelRefusal(udbFor(T, user), a); msg != "" {
					return nil, jsonapi.BadRequest(msg)
				}
				ch.AgentID = a
			}
			SaveChannel(RootDB, ch)
			return ch, nil
		}
		// Create — agent from the query (rail is agent-scoped) or the body.
		if agentID == "" {
			agentID = strings.TrimSpace(req.AgentID)
		}
		if agentID == "" {
			return nil, jsonapi.BadRequest("agent_id is required")
		}
		if msg := subAgentChannelRefusal(udbFor(T, user), agentID); msg != "" {
			return nil, jsonapi.BadRequest(msg)
		}
		// Service may be blank: an inert channel — the interface is configured
		// but no source is hooked in yet, so nothing routes until one is.
		ch := Channel{
			ID:          NewChannelID(),
			Owner:       user,
			Name:        strings.TrimSpace(req.Name),
			Description: strings.TrimSpace(req.Description),
			Service:     strings.TrimSpace(req.Service),
			Address:     strings.TrimSpace(req.Address),
			AgentID:     agentID,
			AutoReply:   req.AutoReply,
			Direction:   strings.TrimSpace(req.Direction),
			Gatekeeper:  strings.TrimSpace(req.Gatekeeper),
			TagOverride: strings.TrimSpace(req.TagOverride),
			TagDisabled: req.TagDisabled,
			Created:     time.Now().UTC().Format(time.RFC3339),
		}
		applyChannelSenders(&ch, req.Senders, req.AllowedHandles)
		SaveChannel(RootDB, ch)
		// Attaching a channel leaves the agent's Cortex setting alone, as a
		// schedule does (console_surface.go). The setting is whether the agent
		// READS its cortex, and a channel works either way: with it on and one
		// channel the conversation lives in the cortex thread
		// (effectiveChannelSession), with it off each contact gets a channel
		// thread of its own. Switching it on here changed the agent's character
		// (its reports in every prompt, the conversational group in the picker)
		// as a side effect of wiring it up.
		Log("[orchestrate.channels] user=%q attached channel %q (service=%s scope=%q) to agent=%s",
			user, ch.Name, ch.Service, ch.Address, agentID)
		return ch, nil

	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			return nil, jsonapi.BadRequest("id is required")
		}
		DeleteChannel(RootDB, user, id)
		return jsonapi.Empty(http.StatusNoContent), nil

	default:
		return nil, jsonapi.Status(http.StatusMethodNotAllowed, "method not allowed")
	}
}

// applyChannelSenders sets who a channel answers from a save, where given.
// The handle list is kept trimmed and without blanks.
func applyChannelSenders(ch *Channel, senders *string, handles *[]string) {
	if senders != nil {
		ch.Senders = strings.ToLower(strings.TrimSpace(*senders))
		if ch.Senders == messaging.SendersAnyone {
			ch.Senders = ""
		}
	}
	if handles != nil {
		var kept []string
		for _, h := range *handles {
			if h = strings.TrimSpace(h); h != "" {
				kept = append(kept, h)
			}
		}
		ch.AllowedHandles = kept
	}
}
