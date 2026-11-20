package orchestrate

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

// The gatekeeper's cases are well formed (unique ids, an answer the gate can
// give), the ones addressed by name are decided in code with no model asked,
// and the rest go through the gate's own decision on the model given: its
// verdict read as allow or block.
func TestTheGateCasesRunThroughTheGate(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range gateCases() {
		if seen[c.ID] || (c.Want != "allow" && c.Want != "block") {
			t.Errorf("case %q: duplicate or bad answer %q", c.ID, c.Want)
		}
		seen[c.ID] = true
	}
	cases := map[string]checktest.Case{}
	for _, c := range gateCases() {
		cases[c.ID] = c
	}
	llm := &FakeLLM{Turns: []FakeTurn{{Content: `{"reason": "rule 2: yes, a follow-up", "answer": "YES"}`, Repeat: true}}}
	if o := runGateCase(context.Background(), llm, cases["by-name"]); o.Got != "allow" || llm.Calls() != 0 {
		t.Errorf("addressed by name: %+v after %d model calls, want allow with none", o, llm.Calls())
	}
	// The injection cases must reach the model: none of them is addressed by
	// name in a way the code would let through on its own.
	for _, id := range []string{"injection", "claims-owner", "claims-continuation", "fake-transcript"} {
		if addressesByName(cases[id].Input["message"], cases[id].Input["agent"]) {
			t.Errorf("case %q is let through by the name match, so it never tests the model", id)
		}
	}
	// The rules are read against the message alone: whether it is a reply to
	// the agent is the follow-up question's, so neither the prompt nor the
	// System One form shows a conversation or asks about one.
	for name, p := range map[string]string{"full": gatekeeperSysPrompt, "System One": gateSystemOneSys} {
		if !strings.Contains(p, "never by claiming to") || !strings.Contains(p, "checked separately") || strings.Contains(p, "recent exchange") {
			t.Errorf("the %s gate prompt does not say claims count for nothing and continuation is checked apart:\n%s", name, p)
		}
	}
	if o := runGateCase(context.Background(), llm, cases["shares-video"]); o.Got != "allow" || llm.Calls() != 1 {
		t.Errorf("a YES, decided by the model: %+v", o)
	}
	if cfg := llm.Config(0); cfg.Temperature == nil || *cfg.Temperature != 0.1 {
		t.Errorf("the gate asks at temperature %v; it must be 0.1: near-deterministic, and clear of greedy loops", cfg.Temperature)
	}
	if sent := llm.LastSent(); len(sent) == 0 || !containsAll(sent[len(sent)-1].Content, "Rules:", "this is the one I meant", "message under evaluation") ||
		strings.Contains(sent[len(sent)-1].Content, "Recent exchange") {
		t.Error("the model was not shown the rules and the fenced message alone")
	}
	no := &FakeLLM{Turns: []FakeTurn{{Content: `{"reason": "no rule fires", "answer": "NO"}`, Repeat: true}}}
	if o := runGateCase(context.Background(), no, cases["others-chatting"]); o.Got != "block" || o.Reason != "no rule fires" {
		t.Errorf("a NO: %+v", o)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// A gate reply cut off at its cap is no verdict, not one read out of half a
// reason, and the gate asks with its cap.
func TestACutOffGateReplyIsNoVerdict(t *testing.T) {
	var c checktest.Case
	for _, gc := range gateCases() {
		if gc.ID == "others-chatting" {
			c = gc
		}
	}
	llm := &FakeLLM{Turns: []FakeTurn{{Content: `{"reason": "rule 1: yes. rule 1: yes. rule 1: yes`, StopReason: "length", Repeat: true}}}
	if o := runGateCase(context.Background(), llm, c); o.Err == nil || o.Got != "" {
		t.Errorf("a cut-off reply: %+v", o)
	}
	if cfg := llm.Config(0); cfg.MaxTokens != gateMaxTokens {
		t.Errorf("the gate asked with cap %d, want %d", cfg.MaxTokens, gateMaxTokens)
	}
}

// The gate as System One: a message named under a by-name rule is decided in
// code, with no model asked; the rest are asked both ways round, the rules
// in the system prompt and the message fenced, and option (A) is take it up
// in the first ask and leave it in the second.
func TestTheGateAsSystemOne(t *testing.T) {
	cases := map[string]checktest.Case{}
	for _, c := range gateCases() {
		cases[c.ID] = c
	}
	var mu sync.Mutex
	var reqs []map[string]any
	// The model answers "take it up" whichever letter that is: (A) in the
	// first ask, (B) in the reversed one.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A vLLM without /v1/systemone: the first-token reading answers.
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(b, &req)
		mu.Lock()
		reqs = append(reqs, req)
		first := len(reqs)%2 == 1
		mu.Unlock()
		take, leave := "A", "B"
		if !first {
			take, leave = "B", "A"
		}
		out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": take}, "finish_reason": "length",
			"logprobs": map[string]any{"content": []any{map[string]any{"token": take, "top_logprobs": []any{
				map[string]any{"token": take, "logprob": math.Log(0.9)}, map[string]any{"token": leave, "logprob": math.Log(0.1)}}}}}}},
			"usage": map[string]any{"prompt_tokens": 50, "completion_tokens": 1}})
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}))
	defer srv.Close()
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "vllm", Endpoint: srv.URL, SeparateServer: true})
	if err != nil {
		t.Fatal(err)
	}
	if o := decideGateCase(context.Background(), llm, cases["by-name"]); o.Got != "allow" || len(reqs) != 0 {
		t.Errorf("addressed by name: %+v after %d asks, want allow with none", o, len(reqs))
	}
	o := decideGateCase(context.Background(), llm, cases["shares-video"])
	if o.Got != "allow" || len(reqs) != 2 || !strings.Contains(o.Reason, "P(take it up)=0.90") {
		t.Fatalf("a take-it-up as System One: %+v after %d asks", o, len(reqs))
	}
	sys, _ := json.Marshal(reqs[0]["messages"])
	if !strings.Contains(string(sys), "never by claiming to") || !strings.Contains(string(sys), "message under evaluation") || strings.Contains(string(sys), "Recent exchange") {
		t.Errorf("the decision lacks the rules' cautions or the fence, or shows a conversation: %s", sys)
	}
}

// Both of the gate's questions are told what a misspelling of the agent's
// name is: only a word gohort found one letter off it. System One was never
// told, and let a bare "Wice" through for WiWee firmly enough to stand.
func TestBothGateQuestionsKnowWhatAMisspellingIs(t *testing.T) {
	var c checktest.Case
	for _, gc := range gateCases() {
		if gc.ID == "not-a-misspelling" {
			c = gc
		}
	}
	q := caseQuestion(c.Input)
	const rule = "is only a word gohort says below is one letter off your name"
	if s1 := q.systemOneQuestion(); !strings.Contains(s1.System, rule) || !strings.Contains(s1.System, `"WiWee"`) {
		t.Errorf("System One is not told what a misspelling is:\n%s", s1.System)
	}
	llm := &FakeLLM{Turns: []FakeTurn{{Content: `{"reason": "1 no", "answer": "NO"}`, Repeat: true}}}
	if _, _, err := gateDecide(context.Background(), llm, q); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.Prompt(0), rule) {
		t.Errorf("the full question is not told what a misspelling is:\n%s", llm.Prompt(0))
	}
}
