package orchestrate

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// A thread is read from its session id: an ask over MCP, a hand-off from
// another agent, a pipeline step's delegate. A session the owner started, a
// channel conversation (its own rail section) and Cortex are not threads.
func TestAThreadIsKnownByItsID(t *testing.T) {
	for sid, want := range map[string]string{
		MCPThreadPrefix:                   threadGroupMCP,
		MCPThreadPrefix + ":k1":           threadGroupMCP,
		MCPThreadPrefix + ":k1:review":    threadGroupMCP,
		"dispatch:sess-1:agent-2":         threadGroupHandOff,
		"machine:thread-1:sort":           threadGroupStep,
		"3f2a9c1e-session":                "",
		"chan:+15550100":                  "",
		cortexSessionID("agent-1"):        "",
		MCPThreadPrefix + "-lookalike:k1": "",
	} {
		if got := threadGroup(sid); got != want {
			t.Errorf("%q: group %q, want %q", sid, got, want)
		}
	}
}

// The threads list is one flat list of an agent's threads, newest first
// whatever their kind, each tagged with its kind and with a title that says what it is, who it is with (the
// last named author of an incoming message) and whether it has something
// new. The owner's own sessions are not in it, and the session list leaves
// the threads out.
func TestTheThreadsListAndTheSessionListAreApart(t *testing.T) {
	df := newDispatchFixture(t)
	now := time.Now()
	put := func(s ChatSession) {
		s.AgentID = df.agent.ID
		df.udb.Set(sessionTable(df.agent.ID), s.ID, s)
	}
	put(ChatSession{ID: "mine", Title: "My work", LastAt: now, LastSeen: now})
	put(ChatSession{ID: MCPThreadPrefix + ":k1:review", Title: "Check test review", LastAt: now.Add(-time.Minute), LastSeen: now.Add(-time.Hour),
		Messages: []ChatMessage{{Role: "user", Content: "how did it go", Sender: "Claude Code"}, {Role: "assistant", Content: "fine"}}})
	// The key's default thread, titled with the key's name when it began and
	// written to since under the key's new one.
	put(ChatSession{ID: MCPThreadPrefix + ":k1", Title: "Claude Desktop", LastAt: now.Add(-2 * time.Minute), LastSeen: now,
		Messages: []ChatMessage{{Role: "user", Content: "hi", Sender: "Claude Code"}}})
	put(ChatSession{ID: "dispatch:sess-1:" + df.agent.ID, LastAt: now.Add(-3 * time.Minute), LastSeen: now})
	put(ChatSession{ID: "machine:thread-1:sort", LastAt: now, LastSeen: now})

	rows := listThreads(df.udb, df.agent.ID)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	want := []string{"machine:thread-1:sort", MCPThreadPrefix + ":k1:review", MCPThreadPrefix + ":k1", "dispatch:sess-1:" + df.agent.ID}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("threads in order:\n got %v\nwant %v", ids, want)
	}
	if r := rows[0]; r.Tag != "Step" || r.Title != "Step sort" {
		t.Errorf("a pipeline step: %+v", r)
	}
	if r := rows[1]; r.Tag != "MCP" || r.Title != "Check test review" || r.With != "Claude Code" {
		t.Errorf("a named MCP conversation: %+v", r)
	}
	if r := rows[2]; r.Tag != "MCP" || r.Title != "" || r.With != "Claude Code" {
		t.Errorf("the key's default thread is who and when, whatever title it kept, with whoever wrote last: %+v", r)
	}
	if r := threadRowFor(ChatSession{ID: MCPThreadPrefix + ":k2"}, threadGroupMCP); r.With != "MCP client" || r.Title != "" {
		t.Errorf("a client that never named itself: %+v", r)
	}
	if r := rows[3]; r.Tag != "Agent" || r.Title != "" || r.With == "" || !r.Created.Equal(r.LastAt) {
		t.Errorf("a hand-off is who and when, no stand-in title, created falling back to its last activity: %+v", r)
	}

	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	call := &jsonapi.Call{R: httptest.NewRequest("GET", "/api/sessions?agent_id="+df.agent.ID, nil), User: "u", UDB: df.udb}
	out, err := df.app.handleSessionList(call)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range out.([]sessionListItem) {
		if isThread(item.ID) {
			t.Errorf("the session list carries thread %q", item.ID)
		}
	}
	if len(out.([]sessionListItem)) != 1 {
		t.Errorf("the session list: %d rows, want the one session", len(out.([]sessionListItem)))
	}
	call.R = httptest.NewRequest("GET", "/api/threads?agent_id="+df.agent.ID, nil)
	if got, _ := df.app.handleThreadList(call); len(got.([]threadRow)) != 4 {
		t.Errorf("the threads endpoint: %v", got)
	}
	call.User = "someone-else"
	if got, _ := df.app.handleThreadList(call); len(got.([]threadRow)) != 0 {
		t.Errorf("another user listed this agent's threads: %v", got)
	}
}

// A thread is read, never written into from the web: a note into a thread's
// run is refused as a message into it is.
func TestAThreadIsNotWrittenInto(t *testing.T) {
	q := &injectionQueue{AgentID: "a1"}
	if err := refuseCortexNote(q, MCPThreadPrefix+":k1"); err == nil || !strings.Contains(err.Error(), "kept for reading") {
		t.Errorf("a note into a thread: %v", err)
	}
	if err := refuseCortexNote(q, "ordinary"); err != nil {
		t.Errorf("a note into a session: %v", err)
	}
}
