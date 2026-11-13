package orchestrate

// Threads: the conversations an agent has that nobody here started, kept
// apart from its sessions (docs/design/threads.md). The rail shows them
// behind a switch (AgentLoopPanel.ThreadsURL), read-only; a send into one is
// refused here, so the hidden composer is not the only gate.
//
// What a thread is, is read from its session id, so nothing stored changes:
//
//	mcp-desktop:<key>[:<conversation>]  an ask over MCP (core.MCPThreadPrefix)
//	dispatch:<conversation>:<agent>     another agent handing this one work
//	machine:<thread>:<step>             a pipeline step delegating to it
//
// Channel conversations (chan:) are not here: they have their own section in
// the rail, read-only already. Cortex is its own view.

import (
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// Thread groups, in the order the rail lists them.
const (
	threadGroupMCP     = "Over MCP"
	threadGroupHandOff = "Hand-offs"
	threadGroupStep    = "Pipeline steps"
)

var threadGroupOrder = map[string]int{threadGroupMCP: 0, threadGroupHandOff: 1, threadGroupStep: 2}

// threadGroup is the group a session id belongs to as a thread, or "" for an
// ordinary session.
func threadGroup(sid string) string {
	switch {
	case sid == MCPThreadPrefix || strings.HasPrefix(sid, MCPThreadPrefix+":"):
		return threadGroupMCP
	case strings.HasPrefix(sid, "dispatch:"):
		return threadGroupHandOff
	case strings.HasPrefix(sid, "machine:"):
		return threadGroupStep
	}
	return ""
}

// isThread reports whether sid is a thread: the agent's own conversation,
// read here and never written into.
func isThread(sid string) bool { return threadGroup(sid) != "" }

// threadRow is one thread as the rail lists it.
type threadRow struct {
	ID     string    `json:"id"`
	Title  string    `json:"title"`
	Group  string    `json:"group"`
	With   string    `json:"with,omitempty"`
	LastAt time.Time `json:"last_at"`
}

// threadRowFor is a stored thread as the rail lists it: its title, or one
// that says what it is, and who it is with.
func threadRowFor(s ChatSession, group string) threadRow {
	// No unread mark: a thread is for checking in on, not something waiting
	// on the person, so the rail draws no new-activity dot for one.
	row := threadRow{ID: s.ID, Title: strings.TrimSpace(s.Title), Group: group, LastAt: s.LastAt}
	// Who wrote to it: the last author named on an incoming message (an MCP
	// key's name, a delegating agent's).
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if m := s.Messages[i]; m.Role == "user" && strings.TrimSpace(m.Sender) != "" {
			row.With = strings.TrimSpace(m.Sender)
			break
		}
	}
	switch group {
	case threadGroupHandOff:
		if row.With == "" {
			row.With = "handed over by another agent"
		}
		if row.Title == "" {
			row.Title = "Hand-off"
		}
	case threadGroupStep:
		if parts := strings.Split(s.ID, ":"); len(parts) >= 3 && row.Title == "" {
			row.Title = "Step " + parts[len(parts)-1]
		}
	case threadGroupMCP:
		if row.Title == "" {
			row.Title = "Over MCP"
		}
	}
	if row.Title == "" {
		row.Title = s.ID
	}
	return row
}

// listThreads is an agent's threads, grouped in rail order and newest first
// in each group.
func listThreads(db Database, agentID string) []threadRow {
	if db == nil || agentID == "" {
		return nil
	}
	out := []threadRow{}
	for _, k := range db.Keys(sessionTable(agentID)) {
		group := threadGroup(k)
		if group == "" {
			continue
		}
		s, ok := loadChatSession(db, agentID, k)
		if !ok {
			continue
		}
		if s.ID == "" {
			s.ID = k
		}
		out = append(out, threadRowFor(s, group))
	}
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := threadGroupOrder[out[i].Group], threadGroupOrder[out[j].Group]
		if gi != gj {
			return gi < gj
		}
		return out[i].LastAt.After(out[j].LastAt)
	})
	return out
}

// handleThreadList is GET api/threads?agent_id=: the active agent's threads,
// for the rail's Threads view. Empty for an agent the caller does not own.
func (T *OrchestrateApp) handleThreadList(c *jsonapi.Call) (any, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("agent_id"))
	if id == "" {
		return []threadRow{}, nil
	}
	agent, ok := loadAgent(c.UDB, id)
	if !ok || (agent.Owner != c.User && agent.Owner != seedOwner) {
		return []threadRow{}, nil
	}
	return listThreads(c.UDB, agent.ID), nil
}

// threadWriteRefusal is what a send into a thread from the web is told.
const threadWriteRefusal = "this thread is the agent's own conversation, kept for reading: start a new session to talk"
