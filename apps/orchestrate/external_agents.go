// External-agent exposure — the shared gate for every machine-facing surface
// that lets something outside gohort address an agent by name.
//
// The MCP server (ask_agent) already had this rule embedded in its own wiring.
// Once a second surface needed it (an OpenAI-compatible endpoint for clients
// that only speak that protocol), the rule had to become a seam rather than be
// copied — two exposure checks that can disagree is exactly how an agent ends
// up reachable from a surface its owner never opened.
//
// Both surfaces read the SAME per-agent switch: "Reachable over MCP"
// (AgentRecord.MCPExposed), which is off by default.

package orchestrate

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// ExternalAgent is the minimal public view of an externally-reachable agent:
// enough to populate a caller's model/agent picker, and nothing more.
type ExternalAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ExternalAgents lists the owner's agents that are reachable from outside
// gohort. db is the app's base store; the per-user store is resolved here so
// callers don't have to know the layering.
// externallyReachable answers "may external key-authenticated surfaces (/v1,
// MCP) see and resolve this agent at all". For USER agents that consent is the
// per-agent MCPExposed toggle (agent editor → Access & visibility). APP agents
// (Servitor Investigator, Guide Author, …) have NO editor page — they're
// Hidden, registry-owned — so their consent is the app FEATURE grant: the
// admin enabled the app for this user under Feature Access. The per-KEY tier
// is enforced downstream (KeyAllowsAppAgent at both surfaces); this is only
// the visibility/resolution gate. Without this, enabling Servitor at admin AND
// key level still dead-ended on an MCPExposed flag nobody could flip.
func externallyReachable(a AgentRecord, owner string) bool {
	if a.MCPExposed {
		return true
	}
	if k := AppFeatureKeyForAgent(a.ID); k != "" {
		return FeatureAllowedForUser(RootDB, k, owner)
	}
	return false
}

func ExternalAgents(db Database, owner string) []ExternalAgent {
	if db == nil || strings.TrimSpace(owner) == "" {
		return nil
	}
	udb := UserDB(db, owner)
	var out []ExternalAgent
	for _, a := range listAgents(udb, owner) {
		if externallyReachable(a, owner) {
			out = append(out, ExternalAgent{ID: a.ID, Name: a.Name})
		}
	}
	return out
}

// ResolveExternalAgent maps a caller-supplied key (agent id OR name) to a
// concrete agent id, but only when that agent is exposed. Returns false for an
// unknown agent AND for a known-but-unexposed one — deliberately the same
// answer, so probing this endpoint can't enumerate an account's private agents.
func ResolveExternalAgent(db Database, owner, key string) (string, bool) {
	if db == nil || strings.TrimSpace(owner) == "" || strings.TrimSpace(key) == "" {
		return "", false
	}
	udb := UserDB(db, owner)
	a, ok := findAgentByNameOrID(udb, owner, key)
	if !ok || !externallyReachable(a, owner) {
		return "", false
	}
	return a.ID, true
}

// ExternalChannelTarget is a live conversation an external caller can join: the
// agent that serves it and the session that IS its thread.
type ExternalChannelTarget struct {
	AgentID   string
	AgentName string
	SessionID string
	ChatID    string
	Name      string // conversation/room display name, when the transport knows one
}

// ResolveExternalChannel maps a chat identifier — chat id, handle, or the room's
// display name — to the agent and THREAD that already serve it.
//
// This is what lets a call from outside gohort land IN a group chat rather than
// beside it. The session key comes from ChannelSessionKey + effectiveChannelSession,
// the same pair the inbound-message path uses, so the external turn appends to
// the very thread the room has been accumulating: same history, same agent, same
// memories. Deriving a "similar" key here instead would produce a parallel
// conversation that looks right and shares nothing.
//
// Exposure follows the bound agent's "Reachable over MCP" switch — one gate for
// every external surface. Unknown and not-exposed both return false so probing
// can't enumerate an account's rooms.
func (T *OrchestrateApp) ResolveExternalChannel(owner, key string) (ExternalChannelTarget, bool) {
	key = strings.TrimSpace(key)
	if T == nil || owner == "" || key == "" {
		return ExternalChannelTarget{}, false
	}
	// Resolve the key to a concrete chat id when the caller named a room by
	// handle or display name: only the transport knows those aliases.
	chatID := key
	name := ""
	matched := false
	if ct, ok := ActiveChannelThreads(); ok {
		for _, t := range ct.Threads(owner) {
			if key == t.ChatID || (t.Handle != "" && key == t.Handle) ||
				(t.DisplayName != "" && strings.EqualFold(key, t.DisplayName)) {
				chatID, name, matched = t.ChatID, t.DisplayName, true
				break
			}
		}
	}
	if !matched {
		// Not fatal — a channel bound directly by address still resolves below
		// — but it is the signature of a mistyped or stale id, so say so.
		Log("[external] %s: %q matched no live channel thread; trying it as a raw address", owner, key)
	}
	// The HTTP answer stays deliberately opaque (unknown and not-exposed look
	// identical, so probing can't enumerate an account's rooms) — but the
	// SERVER log says exactly which check failed. A caller staring at a 404
	// with three indistinguishable causes has no way forward, and "the guard
	// left no trail" is its own bug.
	ch, ok := channelForChat(owner, chatID, key)
	if !ok {
		Log("[external] %s: no channel bound to chat %q (and no whole-service channel covers it) — bind one, or check the id against list_chats", owner, chatID)
		return ExternalChannelTarget{}, false
	}
	udb := UserDB(T.DB, owner)
	ag, ok := loadAgent(udb, ch.AgentID)
	if !ok {
		Log("[external] %s: channel %q is bound to agent %q, which does not load — the agent was probably deleted; rebind the channel", owner, ch.Name, ch.AgentID)
		return ExternalChannelTarget{}, false
	}
	if !ag.MCPExposed {
		Log("[external] %s: chat %q resolves to agent %q, but it is NOT exposed — turn on \"Reachable over MCP\" on that agent to allow external surfaces to reach it", owner, chatID, ag.Name)
		return ExternalChannelTarget{}, false
	}
	return ExternalChannelTarget{
		AgentID:   ag.ID,
		AgentName: ag.Name,
		SessionID: T.effectiveChannelSession(owner, ag.ID, ChannelSessionKey(ch, chatID)),
		ChatID:    chatID,
		Name:      name,
	}, true
}

// ExternalChannels lists the conversations an external caller may join — the
// owner's live channel threads whose bound agent is exposed.
func (T *OrchestrateApp) ExternalChannels(owner string) []ExternalChannelTarget {
	if T == nil || strings.TrimSpace(owner) == "" {
		return nil
	}
	ct, ok := ActiveChannelThreads()
	if !ok {
		return nil
	}
	var out []ExternalChannelTarget
	seen := map[string]bool{}
	for _, t := range ct.Threads(owner) {
		if t.ChatID == "" || seen[t.ChatID] {
			continue
		}
		seen[t.ChatID] = true
		if tgt, ok := T.ResolveExternalChannel(owner, t.ChatID); ok {
			if tgt.Name == "" {
				tgt.Name = chFirst(t.DisplayName, t.Handle, t.ChatID)
			}
			out = append(out, tgt)
		}
	}
	return out
}

func init() {
	// Populate the account page's grantable-targets picker without account
	// importing orchestrate. Sources a user's OWN exposed agents plus agents
	// SHARED to them (AllowedUsers) that are exposed, plus their channels. Tiers
	// (worker/lead) are added by core.ListExternalTargets around this.
	ListExternalTargetsFn = func(db Database, user string) []ExternalTarget {
		if db == nil || strings.TrimSpace(user) == "" {
			return nil
		}
		var out []ExternalTarget
		seen := map[string]bool{}
		addAgent := func(id, name, note string) {
			if id == "" || seen["agent:"+id] {
				return
			}
			seen["agent:"+id] = true
			label := name
			if label == "" {
				label = id
			}
			if note != "" {
				label += " — " + note
			}
			out = append(out, ExternalTarget{Value: "agent:" + id, Label: label, Group: "Agents"})
		}
		// Own exposed agents.
		for _, a := range ExternalAgents(db, user) {
			addAgent(a.ID, a.Name, "")
		}
		// Agents shared TO this user (by another owner) that are exposed. Walk
		// every user's store — the same cross-user walk the admin governance view
		// uses — and keep only agents whose AllowedUsers include this user and
		// which are MCPExposed. An unexposed shared agent isn't externally
		// reachable, so offering it would grant a key nothing.
		for _, au := range AuthListUsers(db) {
			if au.Username == user {
				continue // own agents already added above
			}
			udb := UserDB(db, au.Username)
			for _, a := range listAgents(udb, au.Username) {
				if a.MCPExposed && userCanRunSharedAgent(a, user) {
					addAgent(a.ID, a.Name, "shared by "+au.Username)
				}
			}
		}
		// Channels the user's agents serve. orchRef is the running app singleton
		// (set at startup) — the in-package handle scheduler callbacks use.
		orchRefMu.Lock()
		orch := orchRef
		orchRefMu.Unlock()
		if orch != nil {
			for _, c := range orch.ExternalChannels(user) {
				if c.ChatID == "" || seen["channel:"+c.ChatID] {
					continue
				}
				seen["channel:"+c.ChatID] = true
				label := c.Name
				if label == "" {
					label = c.ChatID
				}
				if c.AgentName != "" {
					label += " — " + c.AgentName
				}
				out = append(out, ExternalTarget{Value: "channel:" + c.ChatID, Label: label, Group: "Channels"})
			}
		}
		return out
	}
}
