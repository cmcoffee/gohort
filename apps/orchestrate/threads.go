package orchestrate

// Threads: the conversations an agent has that nobody here started, kept
// apart from its sessions (docs/design/threads.md). The rail shows them
// behind a switch (AgentLoopPanel.ThreadsURL), read-only; a send into one is
// refused here, so the hidden composer is not the only gate.
//
// What a thread is, is read from its session id, so nothing stored changes:
//
//	mcp-desktop:<key>[:<conversation>]  an ask over MCP (core.MCPThreadPrefix)
//	ext:<key>:<thread>                  a call to the Model API (agenthost.ModelAPIThreadPrefix)
//	dispatch:<conversation>:<agent>     another agent handing this one work
//	machine:<thread>:<step>             a pipeline step delegating to it
//
// Channel conversations (chan:) are not here: they have their own section in
// the rail, read-only already. Cortex is its own view.

import (
	"context"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// Thread kinds, as the tag the rail draws before each thread's title. Short
// words, not abbreviations: "PPL" read as people.
const (
	threadGroupMCP     = "MCP"
	threadGroupAPI     = "API"
	threadGroupHandOff = "Agent"
	threadGroupStep    = "Step"
)

// threadGroup is the kind of thread a session id is, as its tag, or "" for
// an ordinary session.
func threadGroup(sid string) string {
	switch {
	case sid == MCPThreadPrefix || strings.HasPrefix(sid, MCPThreadPrefix+":"):
		return threadGroupMCP
	case strings.HasPrefix(sid, agenthost.ModelAPIThreadPrefix+":"):
		return threadGroupAPI
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
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Tag     string    `json:"tag"`
	With    string    `json:"with,omitempty"`
	Created time.Time `json:"created"`
	LastAt  time.Time `json:"last_at"`
}

// threadRowFor is a stored thread as the rail lists it: its title, or one
// that says what it is, and who it is with.
func threadRowFor(s ChatSession, group string) threadRow {
	// No unread mark: a thread is for checking in on, not something waiting
	// on the person, so the rail draws no new-activity dot for one.
	row := threadRow{ID: s.ID, Title: strings.TrimSpace(s.Title), Tag: group, Created: s.Created, LastAt: s.LastAt}
	if row.Created.IsZero() {
		row.Created = s.LastAt
	}
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
		// No stand-in title: "Hand-off" on every row said what the tag
		// already says. The rail shows who handed it over and when.
		if row.With == "" {
			row.With = "another agent"
		}
		return row
	case threadGroupStep:
		if parts := strings.Split(s.ID, ":"); len(parts) >= 3 && row.Title == "" {
			row.Title = "Step " + parts[len(parts)-1]
		}
	case threadGroupAPI:
		// Who called: the caller a voice platform named, else the key's name
		// (apps/openaiapi callerName). Its title is the one the conversation
		// was given from what was said, before when it began.
		if row.With == "" {
			row.With = "API client"
		}
		return row
	case threadGroupMCP:
		// Like a hand-off: who and when. The key's own thread, the one an ask
		// without a conversation of its own lands in (mcp-desktop:<key>, one
		// segment after the prefix), shows no title. It stored the key's name
		// as one when it began, which said nothing the line above it does not
		// and went stale when the key was renamed: a thread from "Claude
		// Code" titled "Claude Desktop". A named conversation keeps its name.
		if row.With == "" {
			row.With = "MCP client"
		}
		if strings.Count(s.ID, ":") <= 1 {
			row.Title = ""
		}
		return row
	}
	if row.Title == "" {
		row.Title = s.ID
	}
	return row
}

// listThreads is an agent's threads, one flat list, newest first: the one
// most likely to be looked in on is on top, whatever kind it is, and each
// row's tag says the kind.
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
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
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

// --- Cleanup -----------------------------------------------------------------

// tuneThreadCleanupDays is how long a thread may sit idle before it goes.
const tuneThreadCleanupDays = "tune_thread_cleanup_days"

func init() {
	RegisterTunable(TunableSpec{App: "/orchestrate", Key: tuneThreadCleanupDays, Category: "Memory",
		Label:  "Remove idle threads after (days, 0 = never)",
		Help:   "A thread nobody has written to in this long is removed: an ask over MCP, a hand-off from another agent, a pipeline step.",
		Detail: "Threads are an agent's own conversations, kept for checking in on and troubleshooting. Idle this long they have nothing left to show, and a conversation that picks up again starts a fresh one. Sessions people work in are never touched.",
		Kind:   KindInt, Default: 30, Min: 0, Max: 3650})
}

// sweepIdleThreads removes every thread, in every one of the owners' agents'
// session tables, last written to before the cutoff, and says how many went.
// Every table, not only the owners' own agents: a seed or app agent keeps
// threads too. A thread with no time on it is left, since its age is not
// known. Sessions are never touched (isThread), and each thread goes the way
// a delete from the rail takes it (deleteChatSession), side tables and
// archived history with it.
func sweepIdleThreads(owners []string, now time.Time) int {
	days := TuneInt(tuneThreadCleanupDays)
	if days <= 0 {
		return 0
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	removed := 0
	for _, owner := range owners {
		db := agentStore(owner)
		if db == nil {
			continue
		}
		n := 0
		for _, tbl := range db.Tables() {
			agentID, ok := strings.CutPrefix(tbl, sessionTable(""))
			if !ok || agentID == "" {
				continue
			}
			for _, k := range db.Keys(tbl) {
				if !isThread(k) {
					continue
				}
				s, ok := loadChatSession(db, agentID, k)
				if !ok || s.LastAt.IsZero() || !s.LastAt.Before(cutoff) {
					continue
				}
				deleteChatSession(db, agentID, k)
				n++
			}
		}
		if n > 0 {
			Log("[orchestrate.threads] removed %d thread(s) of %s idle over %d day(s)", n, owner, days)
		}
		removed += n
	}
	return removed
}

// registerThreadSweep runs the sweep with the scheduler's reconcilers, every
// half hour.
func registerThreadSweep() {
	RegisterReconciler("idle_thread_cleanup", func(ctx context.Context) error {
		if AuthDB == nil || AuthDB() == nil {
			return nil
		}
		var owners []string
		for _, u := range AuthListUsers(AuthDB()) {
			owners = append(owners, u.Username)
		}
		sweepIdleThreads(owners, time.Now())
		return nil
	})
}
