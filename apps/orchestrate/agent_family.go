package orchestrate

// A sub-agent belongs to its parent: it has no front door of its own (a
// channel bound to it is its top-level parent's, see channelAgentFor), no
// schedule surface of its own (the Scheduler lists a whole family on any
// member's page, and a schedule it runs reports to that parent), and no
// Cortex of its own (what would reach it is filed in the parent's, see
// cortexHolder; its working thread, the one its recurring tasks and monitor
// wakes carry their history in, is kept but listed nowhere).

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// topAgentOf is the top-level agent of id's family: id itself for an agent
// that no other owns. Stops on a cycle or a parent that no longer exists.
func topAgentOf(udb Database, id string) string {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		a, ok := loadAgent(udb, id)
		if !ok || strings.TrimSpace(a.OwnedBy) == "" {
			return id
		}
		if _, parentOK := loadAgent(udb, a.OwnedBy); !parentOK {
			return id
		}
		id = a.OwnedBy
	}
	return id
}

// schedulerFamily is the agents whose schedules an agent's Scheduler shows:
// its top-level parent and every agent under it. [""] (everything) when no
// agent is named.
func schedulerFamily(udb Database, user, agentID string) []string {
	if strings.TrimSpace(agentID) == "" || udb == nil {
		return []string{agentID}
	}
	ids := agentFamilyIDs(udb, user, topAgentOf(udb, agentID))
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// uniqueSchedulerRows drops a row already listed: a link group two family
// members share comes back once per member.
func uniqueSchedulerRows(rows []map[string]any) []map[string]any {
	seen := map[string]bool{}
	out := rows[:0:0]
	for _, m := range rows {
		k := fmt.Sprint(m["_kind"], "\x00", m["_id"], "\x00", m["_uid"], "\x00", m["_group"], "\x00", m["_group_head"], "\x00", m["name"])
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, m)
	}
	return out
}

// channelAgentFor is messaging.ChannelAgentFor: a channel bound to a
// sub-agent is bound to its top-level parent, which answers it and can hand
// the work to the sub-agent.
func channelAgentFor(owner, agentID string) string {
	if orchestrateBaseDB == nil {
		return agentID
	}
	return topAgentOf(UserDB(orchestrateBaseDB, owner), agentID)
}

// moveSubAgentChannels moves every channel bound to a sub-agent onto its
// top-level parent, once at start-up, for bindings made before a sub-agent
// could not hold one.
func moveSubAgentChannels() {
	moved := 0
	for _, u := range deploymentUsers() {
		for _, ch := range ListChannels(RootDB, u) {
			if to := channelAgentFor(u, ch.AgentID); ch.AgentID != "" && to != ch.AgentID {
				from := ch.AgentID
				SaveChannel(RootDB, ch) // SaveChannel applies channelAgentFor
				moved++
				Log("[orchestrate.channels] %s: channel %q moved from sub-agent %s to its parent %s", u, chFirst(ch.Name, ch.ID), from, to)
			}
		}
	}
	if moved > 0 {
		Log("[orchestrate.channels] moved %d channel(s) from sub-agents to their parents", moved)
	}
}

// subAgentChannelRefusal is what binding a channel to a sub-agent says: the
// channel belongs on its parent, named, which can hand the work to it. ""
// for an agent that is not a sub-agent.
func subAgentChannelRefusal(udb Database, agentID string) string {
	top := topAgentOf(udb, agentID)
	if top == agentID {
		return ""
	}
	name := top
	if a, ok := loadAgent(udb, top); ok && strings.TrimSpace(a.Name) != "" {
		name = a.Name
	}
	return "A sub-agent has no channel of its own: bind it to " + name + ", the agent this one belongs to, which can hand the work to it."
}

// udbFor is the owner's agent store.
func udbFor(T *OrchestrateApp, user string) Database {
	if T == nil || T.DB == nil {
		return nil
	}
	return UserDB(T.DB, user)
}

// reportToParent posts a scheduled run's report, made by a sub-agent, to its
// top-level parent's home thread as well: the sub-agent's own thread is its
// working context, and not a thread anybody reads. The card says which
// sub-agent ran it. A no-op for an agent that is not a sub-agent, or for an
// empty report.
func reportToParent(udb Database, agentID string, card ChatMessage) bool {
	if udb == nil || strings.TrimSpace(card.Content) == "" {
		return false
	}
	top := topAgentOf(udb, agentID)
	if top == agentID {
		return false
	}
	if a, ok := loadAgent(udb, agentID); ok && strings.TrimSpace(a.Name) != "" {
		card.ReportFrom = strings.TrimSpace(card.ReportFrom + " (by " + a.Name + ")")
	}
	if card.Created.IsZero() {
		card.Created = time.Now()
	}
	sid := cortexSessionID(top)
	if err := appendToStoredSession(udb, top, sid, ChatSession{ID: sid, AgentID: top}, card); err != nil {
		Log("[orchestrate] report from sub-agent %s to parent %s failed: %v", agentID, top, err)
		return false
	}
	return true
}

// cortexHolder is the agent whose Cortex a note about agentID is filed in,
// and the name the note is filed under. A sub-agent has none of its own
// (enforceSubAgentPosture), so its notes go to its top-level parent's,
// saying which sub-agent they are about: "(to Critic)" for a request made
// of it, "(by Critic)" for anything else.
func cortexHolder(db Database, agentID, from, kind string) (holder, label string) {
	top := topAgentOf(db, agentID)
	if top == agentID {
		return agentID, from
	}
	name := agentID
	if a, ok := loadAgent(db, agentID); ok && strings.TrimSpace(a.Name) != "" {
		name = strings.TrimSpace(a.Name)
	}
	how := "by"
	if kind == cortexKindRequest {
		how = "to"
	}
	return top, strings.TrimSpace(from + " (" + how + " " + name + ")")
}

// familyRequest reports whether a request from callerID to targetID stays
// inside one family, made of a sub-agent: its Cortex note would land in the
// parent's Cortex, where the parent's own conversation already shows it.
func familyRequest(db Database, callerID, targetID string) bool {
	top := topAgentOf(db, targetID)
	return top != targetID && top == topAgentOf(db, callerID)
}

// isSubAgent reports whether id has a top-level parent other than itself.
func isSubAgent(db Database, id string) bool {
	return db != nil && topAgentOf(db, id) != id
}
