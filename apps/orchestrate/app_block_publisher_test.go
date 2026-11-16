package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/appagents"
)

// The publisher's contract is "safe to call, always" — an app builds its tools
// over one before there is any turn to bind, and the same tools run on
// headless dispatches that have no stream at all. Every one of those must be
// a quiet no-op rather than a panic in a tool handler goroutine.
func TestPublisherIsInertUntilBound(t *testing.T) {
	var nilPub *AppBlockPublisher
	nilPub.Publish(agenthost.Block{Type: "demo_card", ID: "1"}) // nil receiver

	unbound := &AppBlockPublisher{}
	unbound.Publish(agenthost.Block{Type: "demo_card", ID: "1"}) // never bound

	bound := &AppBlockPublisher{}
	bindPublisher(bound, &chatTurn{}) // bound to a turn with no sse and no session
	bound.Publish(agenthost.Block{Type: "demo_card", ID: "1"})

	// Reaching here without a panic IS the assertion.
}

// A block with no type has no renderer to dispatch to, so publishing one would
// put an event on the wire that the panel can only log a warning about.
func TestPublisherDropsTypelessBlocks(t *testing.T) {
	buf := &syncBuf{}
	pub := &AppBlockPublisher{}
	bindPublisher(pub, &chatTurn{sse: &sseWriter{live: buf}})

	pub.Publish(agenthost.Block{ID: "1", Title: "no type"})
	if buf.String() != "" {
		t.Fatalf("a typeless block was emitted: %s", buf.String())
	}
}

// The live half: what an app publishes has to arrive as a {kind:"block"}
// frame carrying the renderer's name and payload, because that is the shape
// the panel's block dispatcher matches on.
func TestPublishEmitsABlockFrame(t *testing.T) {
	buf := &syncBuf{}
	pub := &AppBlockPublisher{}
	bindPublisher(pub, &chatTurn{sse: &sseWriter{live: buf}})

	pub.Publish(agenthost.Block{
		Type:  "anvil_edit",
		ID:    "edit-1",
		Title: "main.go",
		Text:  "Edited - +3 −1",
		Data:  map[string]string{"diff": "@@ -1 +1 @@"},
	})

	out := buf.String()
	for _, want := range []string{
		`"kind":"block"`, `"type":"anvil_edit"`, `"id":"edit-1"`,
		"main.go", "Edited", "@@ -1 +1 @@",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("frame missing %q:\n%s", want, out)
		}
	}
}

// The durable half: a published block lands on the session so a reload
// replays it, and re-publishing the SAME id UPDATES that card rather than
// stacking a second one. Stacking is what makes a tool that revises its own
// card (a progress card, a diff that gets amended) unusable.
func TestPublishPersistsAndUpsertsByID(t *testing.T) {
	sess := &ChatSession{}
	pub := &AppBlockPublisher{}
	bindPublisher(pub, &chatTurn{session: sess})

	pub.Publish(agenthost.Block{Type: "demo_card", ID: "a", Title: "first"})
	pub.Publish(agenthost.Block{Type: "demo_card", ID: "b", Title: "other"})
	pub.Publish(agenthost.Block{Type: "demo_card", ID: "a", Title: "revised"})

	if len(sess.UIBlocks) != 2 {
		t.Fatalf("re-publishing an id stacked a card: %d blocks, want 2", len(sess.UIBlocks))
	}
	var found bool
	for _, b := range sess.UIBlocks {
		if b.ID == "a" {
			found = true
			if b.Title != "revised" {
				t.Fatalf("the upsert kept the stale title %q", b.Title)
			}
		}
	}
	if !found {
		t.Fatal("the re-published block is not on the session")
	}
}

// An app's tools can set a condition on the turn ending: the reply it holds
// back is sent back to the model with the app's note, and the turn ends on
// the reply after it.
func TestAnAppsFinishCheckSendsTheReplyBack(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "All done."}, {Content: "The tests still fail in parse.go."}}}
	T.LLM = fake
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-finish-check", OwningApp: "Test", Name: "Coder", Prompt: "You change code.",
		AllowedTools: []string{"ask_user"}, Hidden: true, ExactTools: true,
	})
	agent, ok := loadAgent(udb, "app-test-finish-check")
	if !ok {
		t.Fatal("the app agent did not resolve")
	}
	const note = "The project's check fails: parse.go:12: undefined: x"
	held := 0
	pub := &AppBlockPublisher{FinishCheck: func(reply string) string {
		if strings.Contains(reply, "All done") {
			held++
			return note
		}
		return ""
	}}
	body, _ := json.Marshal(map[string]any{"message": "Fix the parser."})
	r := asUser(httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(string(body))), user)
	T.handleSendWithAppToolsPublishing(httptest.NewRecorder(), r, udb, user, agent, []AgentToolDef{{
		Tool:    Tool{Name: "noop", Description: "Does nothing."},
		Handler: func(context.Context, map[string]any) (string, error) { return "", nil },
	}}, pub)
	if held != 1 {
		t.Fatalf("the first reply was held %d time(s), want once", held)
	}
	told := false
	for i := 1; i < fake.Calls(); i++ {
		for _, m := range fake.Sent(i) {
			if m.Role == "user" && strings.Contains(m.Content, note) {
				told = true
			}
		}
	}
	if !told {
		t.Error("the model was not asked again with what the check found")
	}
}

// The turn's condition on ending puts an authoring agent's build check first
// and the app's after it; with neither there is none.
func TestTheTurnsFinishCheckCombinesBuildAndApp(t *testing.T) {
	if (&chatTurn{}).finishCheck() != nil {
		t.Error("a turn with no checks has a finish check")
	}
	turn := &chatTurn{}
	bindPublisher(&AppBlockPublisher{FinishCheck: func(string) string { return "app says no" }}, turn)
	if notice, strike := turn.finishCheck()("done"); notice != "app says no" || strike != "" {
		t.Errorf("got %q %q", notice, strike)
	}
}
