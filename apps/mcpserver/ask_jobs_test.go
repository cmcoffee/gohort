package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// shortWaits makes the job waits test-sized.
func shortWaits(t *testing.T) {
	t.Helper()
	pa, pr, pk := askWait, resultWait, jobKeep
	askWait, resultWait = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { askWait, resultWait, jobKeep = pa, pr, pk })
}

// A turn that ends quickly is answered as the call's reply, as before; one
// still running is answered with a job id, keeps running, and is fetched by
// that id once it ends. The agent's progress note rides on the check-in.
func TestALongTurnBecomesAJobToCheckBackOn(t *testing.T) {
	shortWaits(t)
	ctx := context.Background()
	quick := startAskJob(ctx, "u", "k1", "a", "", func(context.Context, func(string)) (string, []string, error) {
		return "done", nil, nil
	})
	if text, _, err := quick.await(ctx, askWait); err != nil || text != "done" {
		t.Fatalf("a quick turn: %q %v", text, err)
	}

	finish := make(chan struct{})
	slow := startAskJob(ctx, "u", "k1", "a", "", func(_ context.Context, status func(string)) (string, []string, error) {
		status("searching the web")
		<-finish
		return "the answer", []string{"img"}, nil
	})
	text, _, err := slow.await(ctx, askWait)
	if err != nil || !strings.Contains(text, "Still working") || !strings.Contains(text, slow.id) ||
		!strings.Contains(text, "now: searching the web") || !strings.Contains(text, "agent_result") {
		t.Fatalf("a slow turn's first answer: %q %v", text, err)
	}
	app := &MCPServer{}
	tok := &AccountToken{ID: "k1"}
	if text, _, _ := app.agentResult(ctx, "u", tok, map[string]any{"job": slow.id}); !strings.Contains(text, "Still working") {
		t.Errorf("a check-in while it runs: %q", text)
	}
	close(finish)
	text, images, err := app.agentResult(ctx, "u", tok, map[string]any{"job": slow.id})
	if err != nil || text != "the answer" || len(images) != 1 {
		t.Errorf("the reply once it ended: %q %v %v", text, images, err)
	}
	// Kept, so a client that lost the reply can fetch it again.
	if text, _, _ := app.agentResult(ctx, "u", tok, map[string]any{"job": slow.id}); text != "the answer" {
		t.Errorf("fetched again: %q", text)
	}
}

// A client that gives up does not stop the turn: the job ends on its own
// and its reply is there for the next check-in.
func TestATurnOutlivesTheRequestThatStartedIt(t *testing.T) {
	shortWaits(t)
	ctx, cancel := context.WithCancel(context.Background())
	finish := make(chan struct{})
	var ran context.Context
	j := startAskJob(ctx, "u", "k1", "a", "", func(c context.Context, _ func(string)) (string, []string, error) {
		ran = c
		<-finish
		return "finished anyway", nil, c.Err()
	})
	cancel()
	if _, _, err := j.await(ctx, time.Second); err == nil {
		t.Error("a gone client was answered as if it waited")
	}
	close(finish)
	<-j.done
	if ran.Err() != nil || j.text != "finished anyway" {
		t.Errorf("the turn was stopped with its request: %v %q", ran.Err(), j.text)
	}
}

// A job is its key's: another key or another owner is told there is no such
// job, not shown its reply. One that ended long enough ago is gone.
func TestAJobIsOnlyItsKeys(t *testing.T) {
	shortWaits(t)
	ctx := context.Background()
	j := startAskJob(ctx, "u", "k1", "a", "", func(context.Context, func(string)) (string, []string, error) {
		return "private", nil, nil
	})
	<-j.done
	app := &MCPServer{}
	for _, c := range []struct {
		owner string
		tok   *AccountToken
	}{{"u", &AccountToken{ID: "k2"}}, {"other", &AccountToken{ID: "k1"}}, {"u", nil}} {
		if text, _, err := app.agentResult(ctx, c.owner, c.tok, map[string]any{"job": j.id}); err == nil || strings.Contains(text, "private") {
			t.Errorf("owner %q key %+v read the job: %q %v", c.owner, c.tok, text, err)
		}
	}
	jobKeep = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	if _, ok := askJobFor("u", "k1", j.id); ok {
		t.Error("a job that ended past jobKeep is still kept")
	}
}

// A named conversation is a thread of its own under the key, the same name
// the same thread; with none, every call from the key shares one, as before.
// Its reply says which conversation it went to.
func TestANamedConversationIsItsOwnThread(t *testing.T) {
	tok := &AccountToken{ID: "k1", Name: "Claude Code"}
	if got := mcpThread(tok, ""); got != mcpSession+":k1" {
		t.Errorf("no name: %q, the key's one thread as before", got)
	}
	a, b := mcpThread(tok, "Check test review"), mcpThread(tok, "check-test   review!")
	if a != b || a != mcpSession+":k1:check-test-review" {
		t.Errorf("the same name, two spellings: %q, %q", a, b)
	}
	if mcpThread(tok, "dialogs") == a {
		t.Error("two names share a thread")
	}
	if got := threadSlug(strings.Repeat("abc ", 40)); len(got) > 48 || strings.HasSuffix(got, "-") {
		t.Errorf("a long name: %q", got)
	}
	if mcpSender(tok) != "Claude Code" || mcpSender(nil) != "MCP client" {
		t.Errorf("sender: %q, %q", mcpSender(tok), mcpSender(nil))
	}
	shortWaits(t)
	j := startAskJob(context.Background(), "u", "k1", "a", "Check test review", func(context.Context, func(string)) (string, []string, error) {
		return "reply", nil, nil
	})
	if text, _, _ := j.await(context.Background(), time.Second); text != "[conversation: Check test review]\n\nreply" {
		t.Errorf("a named conversation's reply: %q", text)
	}
}

// agent_result is part of ask_agent: a key that may ask may fetch, listed or
// not, and one that may not ask may not fetch either.
func TestAgentResultFollowsAskAgent(t *testing.T) {
	if toolScope("list_conversations") != "ask_agent" {
		t.Error("list_conversations does not follow ask_agent")
	}
	asks, other := []string{"ask_agent"}, []string{"list_agents"}
	may := &AccountToken{ID: "k", Scope: &TokenScope{Tools: &asks}}
	mayNot := &AccountToken{ID: "k", Scope: &TokenScope{Tools: &other}}
	if !may.AllowsTool(toolScope("agent_result")) || mayNot.AllowsTool(toolScope("agent_result")) {
		t.Error("agent_result does not follow ask_agent on a narrowed key")
	}
	names := map[string]bool{}
	for _, d := range allowedToolDefs(toolDefs(), may) {
		names[d["name"].(string)] = true
	}
	if !names["ask_agent"] || !names["agent_result"] || names["list_agents"] {
		t.Errorf("a key narrowed to ask_agent lists: %v", names)
	}
}

// list_conversations names each of the key's threads by what ask_agent's
// conversation argument takes to continue it, with its title, agent, size
// and last activity, asking the host for this key's prefix only.
func TestListConversationsNamesEachThreadToContinueIt(t *testing.T) {
	var asked string
	RegisterMCPAgentHost(MCPAgentHost{
		Run: func(context.Context, MCPTurn) (ChannelReply, error) { return ChannelReply{}, nil },
		List: func(owner, prefix string) []MCPThread {
			asked = owner + " " + prefix
			at := time.Date(2026, 11, 12, 15, 4, 0, 0, time.UTC)
			return []MCPThread{
				{AgentID: "a1", AgentName: "Clerk", Thread: prefix + ":check-test-review", Title: "Check test review", LastAt: at, Messages: 4},
				{AgentID: "a1", AgentName: "Clerk", Thread: prefix, Title: "Claude Code", LastAt: at, Messages: 2},
			}
		},
	})
	t.Cleanup(func() { RegisterMCPAgentHost(MCPAgentHost{}) })
	got := (&MCPServer{}).listConversations("u", &AccountToken{ID: "k1"})
	if asked != "u "+mcpSession+":k1" {
		t.Errorf("asked the host for %q", asked)
	}
	for _, want := range []string{
		"Check test review (conversation: check-test-review), with Clerk (a1): 4 message(s), last active 2026-11-12 15:04",
		"(the default conversation: leave conversation out), with Clerk (a1): 2 message(s)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the list lacks %q:\n%s", want, got)
		}
	}
	RegisterMCPAgentHost(MCPAgentHost{})
	if got := (&MCPServer{}).listConversations("u", &AccountToken{ID: "k1"}); got != "No conversations yet for this key." {
		t.Errorf("with none: %q", got)
	}
}
