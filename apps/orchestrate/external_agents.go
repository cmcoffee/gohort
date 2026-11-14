// External-agent exposure — the shared gate for every machine-facing surface
// that lets something outside gohort address an agent by name.
//
// The MCP server (ask_agent) already had this rule embedded in its own wiring.
// Once a second surface needed it (an OpenAI-compatible endpoint for clients
// that only speak that protocol), the rule had to become a seam rather than be
// copied — two exposure checks that can disagree is exactly how an agent ends
// up reachable from a surface its owner never opened.
//
// Both surfaces follow the same rule: a key reaches the agents it lists. The
// per-agent "Reachable over MCP" switch that once stood beside it is retired
// (docs/design/retire-mcp-switch.md); only a key from before scoping still
// reads its stored value.

package orchestrate

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/appagents"
)

// reachableThrough answers "may this key (/v1, MCP) see and resolve this agent
// at all". For a USER agent the consent is the key's own list (granted, from
// AccountToken.AgentGrant): the owner ticking the agent on the key. It covers
// the owner's own agents and the ones shared with them while the share
// stands, so unsharing cuts a key off at its next call. APP agents (Servitor
// Investigator, Guide Author, ...) have no editor page, so their consent is
// the app FEATURE grant: the admin enabled the app for this user under
// Feature Access; the per-KEY tier for them is KeyAllowsAppAgent, downstream.
func reachableThrough(a AgentRecord, owner string, granted func(canonical string) bool) bool {
	if k := AppFeatureKeyForAgent(a.ID); k != "" {
		return FeatureAllowedForUser(RootDB, k, owner)
	}
	if granted == nil || !granted("agent:"+a.ID) {
		return false
	}
	return ownAgentFor(a, owner) || userCanRunSharedAgent(a, owner)
}

// resolveAppByLabel maps an APP label ("Servitor", "Guides") to that app's
// agent. The app is the CALLABLE UNIT for external callers — which agent
// implements it is deliberately invisible — so "agent":"Servitor" resolves
// without the caller ever learning the investigator's internal name. First
// registered agent wins when an app registers several (today each has one).
func resolveAppByLabel(key string) (AgentRecord, bool) {
	k := strings.TrimSpace(key)
	for _, sp := range appagents.AppAgents() {
		if strings.EqualFold(strings.TrimSpace(sp.OwningApp), k) {
			if a, ok := appagents.AppAgentByID(sp.ID); ok {
				return appAgentSpecToRecord(a), true
			}
		}
	}
	return AgentRecord{}, false
}

// ResolveExternalAgent maps a caller-supplied key (agent id OR name) to a
// concrete agent id, but only when no key is needed to reach it: an app's
// agent under its feature grant. Returns false for an unknown agent AND for a
// known-but-unreachable one — deliberately the same
// answer, so probing this endpoint can't enumerate an account's private agents.
func ResolveExternalAgent(db Database, owner, key string) (string, bool) {
	if db == nil || strings.TrimSpace(owner) == "" || strings.TrimSpace(key) == "" {
		return "", false
	}
	return ResolveExternalAgentGranted(db, owner, key, nil)
}

// ResolveExternalAgentGranted is ResolveExternalAgent with the calling key's
// grant (AccountToken.AgentGrant): an agent the key lists is reachable
// through it, and no other user agent is (reachableThrough). Nil granted
// reaches app agents only.
func ResolveExternalAgentGranted(db Database, owner, key string, granted func(canonical string) bool) (string, bool) {
	if db == nil || strings.TrimSpace(owner) == "" || strings.TrimSpace(key) == "" {
		return "", false
	}
	udb := UserDB(db, owner)
	a, ok := findAgentByNameOrID(udb, owner, key)
	if !ok {
		// App-label fallback: the app is the callable unit.
		a, ok = resolveAppByLabel(key)
	}
	if !ok {
		return "", false
	}
	if reachableThrough(a, owner, granted) {
		return a.ID, true
	}
	return "", false
}

// ownAgentFor reports whether a is owner's own agent (or a framework agent,
// which every user has).
func ownAgentFor(a AgentRecord, owner string) bool {
	o := strings.TrimSpace(a.Owner)
	return o == "" || o == owner || o == seedOwner
}

// ExternalChannelTarget is agenthost.ChannelTarget, which apps outside orchestrate use to reach it.
type ExternalChannelTarget = agenthost.ChannelTarget

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
// Which rooms a caller may join is the calling key's list ("channel:<chat id>",
// checked by the surface after this resolves). Unknown returns false so
// probing can't enumerate an account's rooms. Exposed carries the retired
// "Reachable over MCP" switch's stored value, for a key from before scoping.
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
		Log("[external] %s: no channel bound to chat %q (and no whole-service channel covers it), bind one, or check the id against list_chats", owner, chatID)
		return ExternalChannelTarget{}, false
	}
	udb := UserDB(T.DB, owner)
	ag, ok := loadAgent(udb, ch.AgentID)
	if !ok {
		Log("[external] %s: channel %q is bound to agent %q, which does not load, the agent was probably deleted; rebind the channel", owner, ch.Name, ch.AgentID)
		return ExternalChannelTarget{}, false
	}
	return ExternalChannelTarget{
		AgentID:   ag.ID,
		AgentName: ag.Name,
		SessionID: T.effectiveChannelSession(owner, ag.ID, ChannelSessionKey(ch, chatID)),
		ChatID:    chatID,
		Name:      name,
		Exposed:   ag.MCPExposed,
	}, true
}

// ExternalChannels lists the conversations an external caller may join — the
// owner's live channel threads whose bound agent loads. Which of them a key
// may join is that key's list.
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
	// What an external client may DISCOVER, kept identical to what it may
	// dispatch. Mirrors ResolveExternalAgentGranted's rule exactly
	// (reachableThrough): what the key lists, of the owner's own agents and
	// the ones shared with them, and an app's agent under its feature grant.
	// A list wider than the resolver would disclose agents the owner kept off
	// the surface; a narrower one would show a set the caller cannot act on.
	ListExternalReachableAgentsFn = func(db Database, owner string, granted func(string) bool) []ExternalAgentInfo {
		if db == nil || strings.TrimSpace(owner) == "" {
			return nil
		}
		udb := UserDB(db, owner)
		var out []ExternalAgentInfo
		var skipped []string
		all := listAgents(udb, owner)
		// Agents shared with the owner reach a key that lists them, as the
		// resolver accepts them (resolveAgentRef), so they are listed too.
		have := map[string]bool{}
		for _, a := range all {
			have[a.ID] = true
		}
		for _, a := range SharedAgentsFor(db, owner) {
			if !have[a.ID] {
				have[a.ID] = true
				all = append(all, a)
			}
		}
		for _, a := range all {
			// A hidden app agent is reached by its APP's label — which agent
			// implements the app is deliberately invisible (resolveAppByLabel).
			// Listing its internal id here disclosed the very name that design
			// withholds, the moment the app's feature grant was on.
			if hiddenAppAgent(a.ID) {
				skipped = append(skipped, fmt.Sprintf("%s(%s: app-internal, reached by app name)", a.Name, a.ID))
				continue
			}
			if !reachableThrough(a, owner, granted) {
				// Name what was passed over and why. "My agent is not in the
				// list" is otherwise unanswerable without a code read: the
				// agent may be absent from the pool entirely, or present and
				// not ticked on the key, and those need different fixes.
				skipped = append(skipped, fmt.Sprintf("%s(%s: not on this key)", a.Name, a.ID))
				continue
			}
			// Sub-agents are MARKED, not hidden. The first version filtered
			// them out on the reasoning that a child is reached through its
			// parent — but the resolver accepts any agent the key lists, so
			// filtering here made the list narrower than what ask_agent would
			// dispatch to, and an agent its owner had deliberately ticked
			// simply never appeared. The tick IS the consent; a rule that
			// overrides it silently is the wrong rule. Naming the parent
			// tells a caller what it is without deciding for them.
			info := ExternalAgentInfo{ID: a.ID, Name: a.Name, Description: a.Description}
			if parent := strings.TrimSpace(a.OwnedBy); parent != "" {
				info.ParentID = parent
			}
			out = append(out, info)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		Log("[list_agents] owner=%s pool=%d listed=%d skipped=[%s]", owner, len(all), len(out), strings.Join(skipped, ", "))
		return out
	}

	// Populate the account page's grantable-targets picker without account
	// importing orchestrate. Sources a user's OWN agents plus agents SHARED to
	// them (AllowedUsers), plus their channels. Tiers
	// (worker/lead) are added by core.ListExternalTargets around this.
	ListExternalTargetsFn = externalTargetsFor
}

// externalTargetsFor is the account page's grantable-targets list for user
// (ListExternalTargetsFn): their agents, agents shared to them, and their
// channels. db is the app's base store.
func externalTargetsFor(db Database, user string) []ExternalTarget {
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
			label += " - " + note
		}
		out = append(out, ExternalTarget{Value: "agent:" + id, Label: label, Group: "Agents"})
	}
	// Own agents: ticking an agent here is the consent that makes it
	// reachable through THIS key (reachableThrough), mirroring the app
	// feature grant. App agents are deliberately NOT listed — the app's own
	// feature checkbox covers them, the implementing agent stays invisible.
	// Hidden framework agents (Builder, Research) are skipped like the
	// tool-access pills. The main agent is where an ask with no agent named
	// goes, so a key can always be given it; it is listed first.
	var main []ExternalTarget
	for _, a := range listAgents(UserDB(db, user), user) {
		if isAppAgent(a.ID) {
			continue
		}
		if a.ID == defaultConsoleAgent {
			seen["agent:"+a.ID] = true
			main = append(main, ExternalTarget{Value: "agent:" + a.ID, Label: chFirst(a.Name, a.ID) + " - your main agent", Group: "Agents"})
			continue
		}
		if a.Owner == seedOwner && a.Hidden {
			continue
		}
		addAgent(a.ID, a.Name, "")
	}
	out = append(main, out...)
	// Agents shared TO this user by another owner: sharing is the owner's
	// consent, and a key that lists one reaches it while the share stands.
	for _, a := range SharedAgentsFor(db, user) {
		if a.Owner != user {
			addAgent(a.ID, a.Name, "shared by "+a.Owner)
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
				label += " - " + c.AgentName
			}
			out = append(out, ExternalTarget{Value: "channel:" + c.ChatID, Label: label, Group: "Channels"})
		}
	}
	return out
}
