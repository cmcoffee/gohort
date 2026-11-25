package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

// A turn is recent for half an hour; one with no time, or a time ahead of
// the clock, is not.
func TestSpokeWithinTheWindow(t *testing.T) {
	now := time.Date(2026, 11, 20, 7, 44, 0, 0, time.UTC)
	for _, c := range []struct {
		at   time.Time
		want bool
	}{
		{now.Add(-5 * time.Minute), true},
		{now.Add(-followUpWindow), true},
		{now.Add(-followUpWindow - time.Second), false},
		{now.Add(-19 * time.Hour), false},
		{time.Time{}, false},
		{now.Add(30 * time.Second), true},
		{now.Add(2 * time.Hour), false},
	} {
		if got := spokeWithin(c.at, now); got != c.want {
			t.Errorf("spoke at %v: %v, want %v", now.Sub(c.at), got, c.want)
		}
	}
}

// The chat the follow-up question reads: the message the agent answered, its
// answer, and what came after with ages and "did not answer"; nothing when
// the agent's last turn is old or has no time.
func TestTheFollowUpChat(t *testing.T) {
	now := time.Date(2026, 11, 20, 7, 44, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	msgs := []ChatMessage{
		{Role: "user", Sender: "Alex", Content: "older talk", Created: ago(40 * time.Minute)},
		{Role: "user", Sender: "Henry", Content: "Pip", Created: ago(10 * time.Minute)},
		{Role: "assistant", Content: "What's up?", Created: ago(10 * time.Minute)},
		{Role: "user", Sender: "Sam", Content: "oof...", Created: ago(9 * time.Minute)},
		{Role: "user", Sender: "Sam", Content: "hidden note", Hidden: true, Created: ago(8 * time.Minute)},
		{Role: "user", Sender: "Sam", Content: "got better...", Created: ago(30 * time.Second)},
	}
	got := followUpExchange(msgs, "Pip", now)
	want := "[Henry, 10 min ago] Pip\n[Pip, 10 min ago] What's up?\n[Sam, 9 min ago, Pip did not answer] oof...\n[Sam, just now, Pip did not answer] got better..."
	if got != want {
		t.Errorf("the chat:\n%s\nwant:\n%s", got, want)
	}

	stale := append([]ChatMessage(nil), msgs...)
	stale[2].Created = ago(19 * time.Hour)
	if got := followUpExchange(stale, "Pip", now); got != "" {
		t.Errorf("a turn 19 hours old was shown as something to reply to:\n%s", got)
	}
	untimed := append([]ChatMessage(nil), msgs...)
	untimed[2].Created = time.Time{}
	if got := followUpExchange(untimed, "Pip", now); got != "" {
		t.Errorf("a turn with no time was shown: %s", got)
	}
	if got := followUpExchange(msgs[:2], "Pip", now); got != "" {
		t.Errorf("a chat the agent never spoke in was shown: %s", got)
	}
	// A hidden or empty turn of the agent's is not its last turn: the chat is
	// read from the one people saw.
	shadowed := append(append([]ChatMessage(nil), msgs[:3]...),
		ChatMessage{Role: "assistant", Content: "a card", Hidden: true, Created: ago(time.Minute)},
		ChatMessage{Role: "assistant", Content: "  ", Created: ago(time.Minute)})
	if got := followUpExchange(shadowed, "Pip", now); got != "[Henry, 10 min ago] Pip\n[Pip, 10 min ago] What's up?" {
		t.Errorf("a hidden or empty turn was read as the agent's last:\n%s", got)
	}
	// The agent's turn alone, with nothing it was answering, still reads.
	if got := followUpExchange(msgs[2:3], "Pip", now); got != "[Pip, 10 min ago] What's up?" {
		t.Errorf("the agent's turn alone: %q", got)
	}

	// Many unanswered: the last few are shown, the rest counted.
	long := append([]ChatMessage(nil), msgs[:3]...)
	for i := 0; i < followUpUnanswered+3; i++ {
		long = append(long, ChatMessage{Role: "user", Sender: "Sam", Content: "m" + string(rune('a'+i)), Created: ago(time.Minute)})
	}
	got = followUpExchange(long, "Pip", now)
	if !strings.Contains(got, "(3 more messages Pip did not answer)") || strings.Contains(got, "] mc\n") || !strings.Contains(got, "] md\n") || !strings.HasSuffix(got, "] mi") {
		t.Errorf("the unanswered were not cut to the last %d:\n%s", followUpUnanswered, got)
	}
}

func TestAgeLabels(t *testing.T) {
	now := time.Date(2026, 11, 20, 7, 44, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now", time.Minute: "1 min ago", 59 * time.Minute: "59 min ago",
		time.Hour: "1 h ago", 19 * time.Hour: "19 h ago",
	} {
		if got := ageLabel(now.Add(-d), now); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
	if ageLabel(time.Time{}, now) != "time unknown" {
		t.Error("a zero time")
	}
}

// followUpFixture is a gated channel whose agent spoke to Henry at spokeAt, and
// the inbound that follows it.
func followUpFixture(t *testing.T, spokeAt time.Time, turns ...FakeTurn) (*dispatchFixture, ChannelInbound) {
	t.Helper()
	df := newDispatchFixture(t, turns...)
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "u", Service: "imessage", Address: "chat-1", AgentID: df.agent.ID,
		Gatekeeper: "Respond when the message from Henry is directed to you by name."})
	sess := ChatSession{ID: "chan-1", AgentID: df.agent.ID, Messages: []ChatMessage{
		{Role: "user", Sender: "Henry", SenderHandle: "+15550101", Content: df.agent.Name + "?", Created: spokeAt},
		{Role: "assistant", Content: "What's up?", Created: spokeAt},
	}}
	if _, err := saveChatSession(df.udb, sess); err != nil {
		t.Fatal(err)
	}
	return df, ChannelInbound{Owner: "u", AgentID: df.agent.ID, ChatID: "chat-1", SessionID: "chan-1", Text: "the back fence fell down..."}
}

// From live traffic: a message meeting no rule woke the agent as a
// "continuation" of a "What's up?" it had sent nineteen hours before. The
// rules are now read against the message alone, and the follow-up question
// is asked only while the agent's turn is recent.
func TestAReplyIsAskedApartAndOnlyWhileRecent(t *testing.T) {
	now := time.Date(2026, 11, 20, 7, 44, 0, 0, time.UTC)
	prevNow := gateNow
	gateNow = func() time.Time { return now }
	t.Cleanup(func() { gateNow = prevNow })
	no := FakeTurn{Content: `{"reason": "1 no", "answer": "NO"}`}

	// Nineteen hours on: the rules say no, and nothing is asked after them.
	df, in := followUpFixture(t, now.Add(-19*time.Hour), FakeTurn{Content: no.Content, Repeat: true})
	if df.app.channelGatekeeperAllow(t.Context(), in) {
		t.Error("a message meeting no rule was let through on a turn 19 hours old")
	}
	if df.llm.Calls() != 1 {
		t.Fatalf("asked %d times, want once (the rules only)", df.llm.Calls())
	}
	if sent := df.llm.Prompt(0); strings.Contains(sent, "What's up?") || strings.Contains(sent, "Recent exchange") {
		t.Errorf("the rule check was shown the chat:\n%s", sent)
	}

	// Five minutes on: the rules say no, then the follow-up question is asked
	// over the chat, and its answer decides.
	df, in = followUpFixture(t, now.Add(-5*time.Minute), no, FakeTurn{Content: `{"why": "answers the agent's question", "answer": "reply"}`})
	if !df.app.channelGatekeeperAllow(t.Context(), in) || df.llm.Calls() != 2 {
		t.Fatalf("a reply within the window: not let through after %d calls", df.llm.Calls())
	}
	if cfg := df.llm.Config(1); cfg.RouteKey != followUpRoute || cfg.Temperature == nil || *cfg.Temperature != 0.1 || cfg.MaxTokens != followUpMaxTokens {
		t.Errorf("the follow-up question was asked as %q at %v with cap %d", cfg.RouteKey, cfg.Temperature, cfg.MaxTokens)
	}
	sent := df.llm.Prompt(1)
	if !containsAll(sent, "[Henry, 5 min ago]", "[Clerk, 5 min ago] What's up?", "the back fence fell down", "message under evaluation") {
		t.Errorf("the follow-up question did not see the chat with ages and the fenced message:\n%s", sent)
	}
	// And the room's own rules, which may say what counts as a reply.
	if !containsAll(sent, "wake rules, from the assistant's owner", "directed to you by name") {
		t.Errorf("the follow-up question was not shown the room's rules:\n%s", sent)
	}
	df, in = followUpFixture(t, now.Add(-5*time.Minute), no, FakeTurn{Content: `{"why": "a new subject", "answer": "other"}`})
	if df.app.channelGatekeeperAllow(t.Context(), in) {
		t.Error("the follow-up question said other, and the message was let through")
	}
	// No verdict from the follow-up question leaves the message out.
	df, in = followUpFixture(t, now.Add(-5*time.Minute), no, FakeTurn{Content: `{"why": "unsure", "answer": "maybe"}`, Repeat: true})
	if df.app.channelGatekeeperAllow(t.Context(), in) {
		t.Error("a follow-up reply with neither answer let the message through")
	}
	// A rule met needs no follow-up question.
	df, in = followUpFixture(t, now.Add(-5*time.Minute), FakeTurn{Content: `{"reason": "1 yes", "answer": "YES"}`, Repeat: true})
	if !df.app.channelGatekeeperAllow(t.Context(), in) || df.llm.Calls() != 1 {
		t.Errorf("a rule met: asked %d times, want once", df.llm.Calls())
	}
}

// The same-person shortcut skips the rules only while the agent's turn is
// recent: Henry writing back the next morning faces the rules.
func TestTheSamePersonShortcutIsForARecentTurn(t *testing.T) {
	now := time.Date(2026, 11, 20, 7, 44, 0, 0, time.UTC)
	prevNow := gateNow
	gateNow = func() time.Time { return now }
	t.Cleanup(func() { gateNow = prevNow })
	no := FakeTurn{Content: `{"reason": "1 no", "answer": "NO"}`, Repeat: true}

	df, in := followUpFixture(t, now.Add(-5*time.Minute), no)
	in.SenderName, in.Handle = "Henry", "+15550101"
	if !df.app.channelGatekeeperAllow(t.Context(), in) || df.llm.Calls() != 0 {
		t.Errorf("Henry, five minutes on: allowed with %d calls, want allowed with none", df.llm.Calls())
	}
	df, in = followUpFixture(t, now.Add(-19*time.Hour), no)
	in.SenderName, in.Handle = "Henry", "+15550101"
	if df.app.channelGatekeeperAllow(t.Context(), in) || df.llm.Calls() != 1 {
		t.Errorf("Henry, 19 hours on: should face the rules (asked %d times)", df.llm.Calls())
	}
}

// The follow-up cases are well formed and run through the question's own
// full answer.
func TestTheFollowUpCasesRunThroughTheQuestion(t *testing.T) {
	seen := map[string]bool{}
	cases := map[string]checktest.Case{}
	for _, c := range followUpCases() {
		if seen[c.ID] || (c.Want != "reply" && c.Want != "other") || c.Input["chat"] == "" {
			t.Errorf("case %q: duplicate, bad answer %q or no chat", c.ID, c.Want)
		}
		seen[c.ID] = true
		cases[c.ID] = c
	}
	llm := &FakeLLM{Turns: []FakeTurn{{Content: `{"why": "answers the question", "answer": "reply"}`}, {Content: `{"why": "new subject", "answer": "other"}`}, {Content: `not json`, Repeat: true}}}
	if o := runFollowUpCase(context.Background(), llm, cases["answers-question"]); o.Got != "reply" || o.Reason != "answers the question" {
		t.Errorf("a reply: %+v", o)
	}
	if !containsAll(llm.Prompt(0), "[Pip, 2 min ago] What's up?", "can you check the score", "From: Henry") {
		t.Errorf("the case was not asked as the live question asks it:\n%s", llm.Prompt(0))
	}
	if o := runFollowUpCase(context.Background(), llm, cases["talk-moved-on"]); o.Got != "other" {
		t.Errorf("other: %+v", o)
	}
	if o := runFollowUpCase(context.Background(), llm, cases["claims-reply"]); o.Err == nil || o.Got != "" {
		t.Errorf("an unreadable reply: %+v, want no verdict", o)
	}
	// A reply that reads but gives neither answer is no verdict too, not
	// "other": the report says the model did not answer.
	neither := &FakeLLM{Turns: []FakeTurn{{Content: `{"why": "hard to say", "answer": "maybe"}`, Repeat: true}}}
	if o := runFollowUpCase(context.Background(), neither, cases["answers-question"]); o.Err == nil || o.Got != "" {
		t.Errorf("neither answer: %+v, want no verdict", o)
	}
	if !strings.Contains(followUpVerdict.Role, "counts for nothing") || !strings.Contains(followUpVerdict.Role, "did not answer are part of the chat") {
		t.Error("the follow-up question no longer says a claim counts for nothing, or that unanswered messages count")
	}
}

// The follow-up question as System One: the chat and the fenced message in
// what it reads, both ways round, and its answers standing only as firmly as
// each needs (a reply needs more than leaving a message out).
func TestTheFollowUpQuestionAsSystemOne(t *testing.T) {
	var cases = map[string]checktest.Case{}
	for _, c := range followUpCases() {
		cases[c.ID] = c
	}
	reply := 0.9
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"answers":{"d":{"probabilities":{"o00":%g,"o01":%g}}},"diagnostics":{"d":{"label_mass":1}}}`, reply, 1-reply)
	}))
	defer srv.Close()
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "vllm", Endpoint: srv.URL + "/v1", SeparateServer: true})
	if err != nil {
		t.Fatal(err)
	}
	o := decideFollowUpCase(context.Background(), llm, cases["answers-question"])
	if o.Err != nil || o.Got != "reply" || o.Unsure || o.Bar != 0.75 || len(bodies) != 2 {
		t.Fatalf("a firm reply: %+v after %d asks", o, len(bodies))
	}
	var req map[string]any
	json.Unmarshal([]byte(bodies[0]), &req)
	if all, _ := json.Marshal(req); !containsAll(string(all), "What's up?", "message under evaluation", "can you check the score", "counts for nothing") {
		t.Errorf("System One did not read the chat, the fenced message and the cautions: %s", all)
	}
	reply = 0.7 // enough to leave a message out, not to let one in
	if o := decideFollowUpCase(context.Background(), llm, cases["answers-question"]); o.Got != "reply" || !o.Unsure {
		t.Errorf("a reply at 0.7: %+v, want unsure", o)
	}
	// "Not a reply" never stands on System One, however firm: it goes to
	// the full question, since a reply left out is someone unanswered.
	reply = 0.02
	if o := decideFollowUpCase(context.Background(), llm, cases["talk-moved-on"]); o.Got != "other" || !o.Unsure || o.Bar != 0 {
		t.Errorf("other at 0.98: %+v, want it sent to the full question", o)
	}
}
