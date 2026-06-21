package orchestrate

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Agent capability governance — the OUTWARD / SPENDING tier (slice 1).
//
// This answers "what is this agent's blast radius?" at the altitude the owner
// actually cares about: not per-call agent-to-agent approvals, but the standing
// capabilities that reach REAL PEOPLE or COST MONEY. Read-only and derived from
// the agent's own config (bound channels + messaging tools + the paid
// credentials its api-mode tools dispatch through), so it reflects what the
// agent CAN do right now without re-running anything.
//
// Deliberately scoped to the scary tier first. Autonomous (schedules/monitors/
// delegation) and Network (web) tiers come later; so does folding in paid tools
// the agent reaches through the user's shared persistent pool (here we report
// the agent's DELIBERATELY-attached kit — a.Tools — which is the accurate
// per-agent spending surface).

// CapChannel is one messaging surface the agent can reach real people on.
type CapChannel struct {
	Name    string `json:"name"`
	Service string `json:"service"` // display name of the transport (iMessage, Telegram, …)
	Scope   string `json:"scope"`   // "whole service" (every contact) or a specific address/room
}

// CapPaidAPI is one paid credential the agent can spend through, via an attached
// api-mode tool.
type CapPaidAPI struct {
	Credential  string  `json:"credential"`
	Tool        string  `json:"tool"`          // the attached tool that dispatches through it
	CostPerCall float64 `json:"cost_per_call"` // priced per dispatched call
	Enabled     bool    `json:"enabled"`       // false = credential drafted/disabled (can't actually spend yet)
}

// OutwardCapability is one agent's outward + spending footprint.
type OutwardCapability struct {
	Agent    string       `json:"agent"`
	AgentID  string       `json:"agent_id"`
	Channels []CapChannel `json:"channels,omitempty"`  // can message: bound, live channels
	MsgTools []string     `json:"msg_tools,omitempty"` // broad messaging tools held (texts/emails arbitrary people)
	PaidAPIs []CapPaidAPI `json:"paid_apis,omitempty"` // can spend: paid credentials reachable

	// Table-friendly one-line summaries (derived from the structured fields
	// above), so an admin Table can render scalar columns. "—" when empty.
	MessageSummary string `json:"message_summary"`
	SpendSummary   string `json:"spend_summary"`
}

// HasReach reports whether the agent has ANY outward/spending capability — used
// to keep the overview to agents that actually carry blast radius.
func (c OutwardCapability) HasReach() bool {
	return len(c.Channels) > 0 || len(c.MsgTools) > 0 || len(c.PaidAPIs) > 0
}

// broadMessagingTools are the tools that let an agent message ARBITRARY people
// (not just reply within a bound channel) — the outward-reach surface.
var broadMessagingTools = map[string]string{
	"message_contact":  "text any contact",
	"converse_contact": "hold a conversation with a contact",
	"send_email":       "send email",
}

// outwardCapabilities derives one agent's outward + spending footprint from its
// stored config. owner is the agent's owner (channels + credentials resolve in
// that scope).
func outwardCapabilities(owner string, a AgentRecord) OutwardCapability {
	cap := OutwardCapability{Agent: chFirst(a.Name, a.ID), AgentID: a.ID}

	// Channels — bound messaging surfaces. Only ones with a transport hooked
	// (Service set) are LIVE; an inert channel (no source) can't reach anyone.
	for _, ch := range ListChannelsForAgent(RootDB, owner, a.ID) {
		if strings.TrimSpace(ch.Service) == "" {
			continue
		}
		scope := "whole service"
		if addr := strings.TrimSpace(ch.Address); addr != "" {
			scope = addr
		}
		cap.Channels = append(cap.Channels, CapChannel{
			Name:    chFirst(ch.Name, ch.Address, ch.ID),
			Service: ServiceDisplayName(ch.Service),
			Scope:   scope,
		})
	}

	// Broad messaging tools the agent holds explicitly.
	seen := map[string]bool{}
	for _, n := range a.AllowedTools {
		if _, ok := broadMessagingTools[n]; ok && !seen[n] {
			cap.MsgTools = append(cap.MsgTools, n)
			seen[n] = true
		}
	}
	// Fleet agents get message_contact through the delegation/management toolset
	// even without an explicit allowlist entry — so the reach is real.
	if a.Fleet && !seen["message_contact"] {
		cap.MsgTools = append(cap.MsgTools, "message_contact (via Delegation tools)")
	}

	// Paid APIs — the agent's deliberately-attached api-mode tools that dispatch
	// through a credential priced > 0. Dedup by tool+credential.
	credSeen := map[string]bool{}
	for i := range a.Tools {
		t := a.Tools[i]
		if t.Mode != TempToolModeAPI || strings.TrimSpace(t.Credential) == "" {
			continue
		}
		key := t.Credential + "\x00" + t.Name
		if credSeen[key] {
			continue
		}
		c, ok := Secure().Load(t.Credential)
		if !ok || c.CostPerCall <= 0 {
			continue
		}
		_, enabled, _ := Secure().CredentialStatus(t.Credential)
		cap.PaidAPIs = append(cap.PaidAPIs, CapPaidAPI{
			Credential:  t.Credential,
			Tool:        t.Name,
			CostPerCall: c.CostPerCall,
			Enabled:     enabled,
		})
		credSeen[key] = true
	}

	// Table-friendly summaries.
	var msg []string
	for _, ch := range cap.Channels {
		msg = append(msg, ch.Service+": "+ch.Name+" ("+ch.Scope+")")
	}
	msg = append(msg, cap.MsgTools...)
	cap.MessageSummary = strings.Join(msg, " · ")
	if cap.MessageSummary == "" {
		cap.MessageSummary = "—"
	}
	var spend []string
	for _, p := range cap.PaidAPIs {
		s := fmt.Sprintf("%s $%.2f/call (%s)", p.Credential, p.CostPerCall, p.Tool)
		if !p.Enabled {
			s += " — disabled"
		}
		spend = append(spend, s)
	}
	cap.SpendSummary = strings.Join(spend, " · ")
	if cap.SpendSummary == "" {
		cap.SpendSummary = "—"
	}
	return cap
}

// handleAgentCapabilities returns the outward/spending footprint for every one
// of the owner's agents that carries any — the "blast radius across your agents"
// overview. Admin-gated like the rest of Agency.
func (T *OrchestrateApp) handleAgentCapabilities(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	out := []OutwardCapability{}
	for _, a := range listAgents(udb, user) {
		if c := outwardCapabilities(user, a); c.HasReach() {
			out = append(out, c)
		}
	}
	writeJSON(w, out)
}
