package bridges

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
	"github.com/cmcoffee/gohort/core/decide"
	"github.com/cmcoffee/snugforge/kvlite"
)

// blowBudget sends replies into one of the owner's own conversations until
// the budget blows, and returns why.
func blowBudget(t *testing.T, chat string) string {
	t.Helper()
	for i := 0; i < replyBudgetFor()+1; i++ {
		if cut := noteReply("", chat, "", false, true); cut != "" {
			return cut
		}
	}
	t.Fatal("the budget never blew")
	return ""
}

// A blown budget is a guess, so it does not cut by itself: the conversation
// is under review (a second trip meanwhile starts nothing), and the check
// settles it. A conversation the worker reads as moving goes on with its
// counts reset, twice a window at most; then the budget cuts unasked. One
// the worker reads as looping, or cannot read, is cut, and with no worker at
// all, as the budget always did.
func TestABlownBudgetIsCheckedBeforeItCuts(t *testing.T) {
	LoopGuardReset()
	progress := &FakeLLM{Turns: []FakeTurn{{Content: `{"reason": "each message asks for a new change", "answer": "PROGRESS"}`, Repeat: true}}}
	T := &Bridges{AppCore{DB: &DBase{Store: kvlite.MemStore()}, LLM: progress}}
	lc := loopCut{chatID: "busy", reply: "Moved it to the left."}
	for round := 1; round <= loopReprieves; round++ {
		lc.cut = blowBudget(t, "busy")
		if loopTripped("", "busy", "") {
			t.Fatal("a blown budget cut routing before it was checked")
		}
		if again := noteReply("", "busy", "", false, true); again != "" {
			t.Fatalf("a second trip under review started another check: %q", again)
		}
		T.settleLoopCut(lc)
		if loopTripped("", "busy", "") || progress.Calls() != round {
			t.Fatalf("round %d: a conversation read as moving was cut, or not asked (%d asks)", round, progress.Calls())
		}
		if cut := noteReply("", "busy", "", false, true); cut != "" {
			t.Fatalf("round %d: the counts were not reset: %q", round, cut)
		}
	}
	if cfg := progress.Config(0); cfg.RouteKey != loopCheckRoute || cfg.Think == nil || *cfg.Think || cfg.MaxTokens != loopWorkerMaxTokens {
		t.Errorf("the worker was asked as route %q, think %v, cap %d", cfg.RouteKey, cfg.Think, cfg.MaxTokens)
	}
	if sent := progress.LastSent(); len(sent) == 0 || !strings.Contains(sent[len(sent)-1].Content, "Moved it to the left.") {
		t.Error("the check did not read the reply about to go out")
	}
	lc.cut = blowBudget(t, "busy")
	T.settleLoopCut(lc)
	if !loopTripped("", "busy", "") || progress.Calls() != loopReprieves {
		t.Errorf("past %d resets the budget must cut unasked (tripped %v, %d asks)", loopReprieves, loopTripped("", "busy", ""), progress.Calls())
	}

	for name, llm := range map[string]LLM{
		"looping":    &FakeLLM{Turns: []FakeTurn{{Content: `{"reason": "the same thanks again", "answer": "LOOP"}`, Repeat: true}}},
		"unreadable": &FakeLLM{Turns: []FakeTurn{{Content: `it is probably fine`, Repeat: true}}},
		"cut off":    &FakeLLM{Turns: []FakeTurn{{Content: `{"reason": "the agent, the agent, the agent`, StopReason: "length", Repeat: true}}},
		"no worker":  nil,
	} {
		LoopGuardReset()
		T := &Bridges{AppCore{DB: &DBase{Store: kvlite.MemStore()}, LLM: llm}}
		T.settleLoopCut(loopCut{chatID: "c", cut: blowBudget(t, "c"), reply: "Anytime!"})
		if !loopTripped("", "c", "") {
			t.Errorf("%s: the conversation was not cut", name)
		}
	}
}

// With Services > System One on, it reads the conversation first: a sure
// answer stands with no worker asked, either way; an unsure one goes to the
// worker. Cutting is the safe way to be wrong, so a loop stands on less
// confidence than a reprieve.
func TestTheLoopCheckAsksSystemOneFirst(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot; SetSharedJudgeLLM(nil, false, "", "") })
	looping, asked := 0.0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"answers":{"d":{"probabilities":{"o00":%g,"o01":%g}}},"diagnostics":{"d":{"label_mass":1}}}`, looping, 1-looping)
	}))
	defer srv.Close()
	judge, err := NewLLMFromConfig(LLMProviderConfig{Provider: "vllm", Endpoint: srv.URL + "/v1", SeparateServer: true})
	if err != nil {
		t.Fatal(err)
	}
	SetSharedJudgeLLM(judge, false, "judge", "")
	RootDB.Set(decide.ConfigTable, "provider", decide.ProviderJudge)
	worker := &FakeLLM{Turns: []FakeTurn{{Content: `{"reason": "new requests", "answer": "PROGRESS"}`, Repeat: true}}}
	T := &Bridges{AppCore{DB: &DBase{Store: kvlite.MemStore()}, LLM: worker}}
	for _, tc := range []struct {
		looping   float64
		loop, ask bool
	}{
		{0.05, false, false}, // sure it is moving (confidence 0.9)
		{0.70, true, false},  // a lean to looping (0.4) is enough to cut
		{0.30, false, true},  // a lean to moving (0.4) is not enough to go on: the worker answers
	} {
		looping, asked = tc.looping, 0
		before := worker.Calls()
		loop, why := T.isLooping(context.Background(), loopCut{chatID: "c", reply: "ok"})
		if loop != tc.loop || asked != 2 || (worker.Calls() > before) != tc.ask {
			t.Errorf("P(looping)=%.2f: loop %v (%s) after %d System One asks and %d worker asks", tc.looping, loop, why, asked, worker.Calls()-before)
		}
	}
	RootDB.Set(decide.ConfigTable, "provider", "")
	asked = 0
	if T.isLooping(context.Background(), loopCut{chatID: "c", reply: "ok"}); asked != 0 {
		t.Errorf("System One was asked while off (%d asks)", asked)
	}
}

// The loop check's cases are well formed and go through the check's own
// question: the conversation fenced, the agent's lines told apart, the
// verdict read from its answer.
func TestTheLoopCasesRunThroughTheCheck(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range loopCases() {
		q := loopCaseQuestion(c)
		if seen[c.ID] || (c.Want != "loop" && c.Want != "progress") || len(q.Messages) < 4 {
			t.Errorf("case %q: duplicate, bad answer %q, or too short", c.ID, c.Want)
		}
		seen[c.ID] = true
		agent := 0
		for _, m := range q.Messages {
			if m.Role == "assistant" {
				agent++
			}
		}
		if agent == 0 || agent == len(q.Messages) {
			t.Errorf("case %q: the agent's lines and the others' were not told apart", c.ID)
		}
	}
	llm := &FakeLLM{Turns: []FakeTurn{{Content: `{"reason": "thanks traded back and forth", "answer": "LOOP"}`, Repeat: true}}}
	var c checktest.Case
	for _, lc := range loopCases() {
		if lc.ID == "bots-thanking" {
			c = lc
		}
	}
	if o := runLoopCase(context.Background(), llm, c); o.Got != "loop" || o.Reason != "thanks traded back and forth" {
		t.Errorf("a LOOP answer: %+v", o)
	}
	user := llm.LastSent()[len(llm.LastSent())-1].Content
	if !strings.Contains(user, "conversation under review") || !strings.Contains(user, "[agent] Anytime! Take care!") || !strings.Contains(user, "[Helper] Thank you for your help!") {
		t.Errorf("the conversation as the check read it:\n%s", user)
	}
}
