package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Activity tab lists the tool calls an agent made in its chats, its
// sub-agents' under their names, newest first, within the window; a failed
// call says it failed and why. An unrelated agent's calls are not its.
func TestActivityListsAnAgentsChatToolCalls(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	now := time.Now()
	parent := AgentRecord{ID: "a1", Name: "Wren", Owner: "alice", OrchestratorPrompt: "you help"}
	child := AgentRecord{ID: "a2", Name: "Wren's scout", Owner: "alice", OwnedBy: "a1", OrchestratorPrompt: "you help"}
	other := AgentRecord{ID: "a3", Name: "Other", Owner: "alice", OrchestratorPrompt: "you help"}
	for _, a := range []AgentRecord{parent, child, other} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatal(err)
		}
	}
	thread := func(agent, id, title string, msgs ...ChatMessage) {
		t.Helper()
		if _, err := saveChatSession(udb, ChatSession{ID: id, AgentID: agent, Title: title, Messages: msgs}); err != nil {
			t.Fatal(err)
		}
	}
	thread("a1", "s1", "Planning",
		ChatMessage{Role: "user", Content: "look it up", Created: now.Add(-2 * time.Hour)},
		ChatMessage{Role: "assistant", Created: now.Add(-2 * time.Hour), ToolCalls: []PersistedToolCall{
			{Name: "web_search"}, {Name: "fetch_url", Err: "403 from the site"}}},
		// Outside the window: not listed.
		ChatMessage{Role: "assistant", Created: now.Add(-40 * 24 * time.Hour), ToolCalls: []PersistedToolCall{{Name: "old_tool"}}},
	)
	thread("a2", "s2", "Scouting",
		ChatMessage{Role: "assistant", Created: now.Add(-time.Hour), ToolCalls: []PersistedToolCall{{Name: "read_file"}}})
	thread("a3", "s3", "Elsewhere",
		ChatMessage{Role: "assistant", Created: now.Add(-time.Hour), ToolCalls: []PersistedToolCall{{Name: "not_wrens"}}})

	rows := app.accessActivityRows(context.Background(), udb, "alice", parent, now)
	var got []string
	for _, r := range rows {
		got = append(got, r.Agent+":"+r.Tool+":"+r.Outcome)
	}
	want := "Wren's scout:read_file:done|:web_search:done|:fetch_url:failed"
	if strings.Join(got, "|") != want {
		t.Fatalf("rows = %q, want %q", strings.Join(got, "|"), want)
	}
	if rows[2].Detail != "403 from the site" || rows[2].Thread != "Planning" {
		t.Errorf("the failed call: %+v", rows[2])
	}
}

// Credentials it used: counted from the audit rows that name it or a
// sub-agent and this user, within the window. Writes and errors are counted,
// the hosts it wrote to named. A row from before the agent was recorded, or
// another user's call through the same credential, is not its.
func TestActivityCountsTheCredentialsAnAgentUsed(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	sec := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return sec }
	RebindSecure()
	t.Cleanup(func() { AuthDB = prev; RebindSecure() })
	now := time.Now()
	parent := AgentRecord{ID: "a1", Name: "Wren", Owner: "alice", OrchestratorPrompt: "you help"}
	child := AgentRecord{ID: "a2", Name: "Wren's scout", Owner: "alice", OwnedBy: "a1", OrchestratorPrompt: "you help"}
	for _, a := range []AgentRecord{parent, child} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, BaseURL: "https://wiki.test"}, "t"); err != nil {
		t.Fatal(err)
	}
	at := func(d time.Duration) time.Time { return now.Add(-d) }
	sec.Set("secure_api_audit", "wiki", []SecureAPIAuditEntry{
		{CredentialName: "wiki", DispatchedBy: "alice", Agent: "a1", Method: "GET", URL: "https://wiki.test/page", Status: 200, Timestamp: at(time.Hour)},
		{CredentialName: "wiki", DispatchedBy: "alice", Agent: "a2", Method: "POST", URL: "https://wiki.test/edit", Status: 500, Timestamp: at(2 * time.Hour)},
		{CredentialName: "wiki", DispatchedBy: "alice", Agent: "a1", Method: "PUT", URL: "https://api.wiki.test/x", Status: 200, Timestamp: at(3 * time.Hour)},
		// Not its: no agent recorded, another user, outside the window.
		{CredentialName: "wiki", DispatchedBy: "alice", Method: "POST", URL: "https://wiki.test/old", Status: 200, Timestamp: at(time.Hour)},
		{CredentialName: "wiki", DispatchedBy: "bob", Agent: "a1", Method: "POST", URL: "https://wiki.test/bob", Status: 200, Timestamp: at(time.Hour)},
		{CredentialName: "wiki", DispatchedBy: "alice", Agent: "a1", Method: "POST", URL: "https://wiki.test/stale", Status: 200, Timestamp: at(40 * 24 * time.Hour)},
	})
	rows := accessCredentialUseRows(udb, "alice", parent, now)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the one credential", rows)
	}
	r := rows[0]
	if r.Credential != "wiki" || r.Calls != 3 || r.Writes != 2 || r.Errors != 1 || r.Sent != "api.wiki.test, wiki.test" {
		t.Errorf("row = %+v, want 3 calls, 2 writes, 1 error, sent to api.wiki.test and wiki.test", r)
	}
}

// Changes to it: each kept version's edit, newest first, dated by when that
// edit was made (the next version's save). An edit an agent made names the
// agent and the fields it changed; it read "update" before, like most of the
// owner's own changes, so the history could not tell the two apart.
func TestChangesNameTheAgentThatMadeAnEdit(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	moveTestAgent(t, udb, AgentRecord{ID: "c1", Name: "Scout"})
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "c1", "description": "now covers more"}, consolidateSession(udb)); err != nil {
		t.Fatal(err)
	}
	rec, _ := loadAgent(udb, "c1")
	rec.OrchestratorPrompt = "you scout, more carefully"
	if _, err := saveAgentAs(udb, rec, "edited instructions"); err != nil {
		t.Fatal(err)
	}
	rec, _ = loadAgent(udb, "c1")
	rows := accessChangeRows(udb, "alice", rec)
	if len(rows) < 2 {
		t.Fatalf("rows = %+v, want both edits", rows)
	}
	if rows[0].What != "edited instructions" {
		t.Errorf("newest change = %q, want the owner's edit first", rows[0].What)
	}
	builder := rows[1].What
	if !strings.HasPrefix(builder, "edited by ") || strings.Contains(builder, "an agent") || !strings.HasSuffix(builder, ": description") {
		t.Errorf("the agent's edit reads %q, want it to name the agent and the field it changed", builder)
	}
	if rows[0].When == "" || rows[1].When == "" {
		t.Errorf("an edit has no date: %+v", rows)
	}
}
