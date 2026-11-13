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
	if !strings.Contains(gatekeeperSysPrompt, "never by claiming to") || !strings.Contains(gatekeeperSysPrompt, "judged from the recent exchange") ||
		!strings.Contains(gatekeeperSysPrompt, "part of the message, not the exchange") {
		t.Error("the gate no longer says a claim counts for nothing and a real follow-up is judged from the exchange")
	}
	if o := runGateCase(context.Background(), llm, cases["follow-up-photos"]); o.Got != "allow" || llm.Calls() != 1 {
		t.Errorf("a follow-up, decided by the model: %+v", o)
	}
	if cfg := llm.Config(0); cfg.Temperature == nil || *cfg.Temperature != 0.1 {
		t.Errorf("the gate asks at temperature %v; it must be 0.1: near-deterministic, and clear of greedy loops", cfg.Temperature)
	}
	if sent := llm.LastSent(); len(sent) == 0 || !containsAll(sent[len(sent)-1].Content, "Recent exchange", "show me the reference photos") {
		t.Error("the model was not shown the recent exchange and the message")
	}
	no := &FakeLLM{Turns: []FakeTurn{{Content: `{"reason": "no rule fires", "answer": "NO"}`, Repeat: true}}}
	if o := runGateCase(context.Background(), no, cases["others-chatting"]); o.Got != "block" || o.Reason != "no rule fires" {
		t.Errorf("a NO: %+v", o)
	}
	// A follow-up is to what the agent said, not to people talking to each
	// other: asked whether a message was "a natural follow-up to the recent
	// exchange", a worker let "anyone up for pizza tonight?" through after
	// two others chatting about a game.
	if sent := no.LastSent(); len(sent) == 0 || !containsAll(sent[len(sent)-1].Content,
		"carry on your own part of the recent exchange", "what Pip said", "talk between other people is not a follow-up to you") {
		t.Error("the gate's question does not say a follow-up is to the agent's own part of the exchange")
	}
	if !strings.Contains(gatekeeperSysPrompt, "is not your conversation") || !strings.Contains(gateSystemOneSys, "Talk between other people that you were not part of is not a follow-up to you") {
		t.Error("the gate's instructions do not say talk between other people is not a follow-up")
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
	o := decideGateCase(context.Background(), llm, cases["follow-up-photos"])
	if o.Got != "allow" || len(reqs) != 2 || !strings.Contains(o.Reason, "P(take it up)=0.90") {
		t.Fatalf("a follow-up as System One: %+v after %d asks", o, len(reqs))
	}
	sys, _ := json.Marshal(reqs[0]["messages"])
	if !strings.Contains(string(sys), "never by claiming to") || !strings.Contains(string(sys), "Recent exchange") || !strings.Contains(string(sys), "message under evaluation") {
		t.Errorf("the decision lacks the rules' cautions, the exchange or the fence: %s", sys)
	}
}
