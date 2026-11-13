package decide

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
)

// stubVLLM answers a decision the way vLLM does: the first token's top
// alternatives with their log-probabilities. It records each request.
func stubVLLM(t *testing.T, top map[string]float64) (LLM, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A vLLM from before /v1/systemone: decide reads the first token.
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(b, &req)
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		var alts []map[string]any
		for tok, p := range top {
			alts = append(alts, map[string]any{"token": tok, "logprob": math.Log(p)})
		}
		out, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "B"},
				"finish_reason": "length",
				"logprobs":      map[string]any{"content": []any{map[string]any{"token": "B", "top_logprobs": alts}}},
			}},
			"usage": map[string]any{"prompt_tokens": 40, "completion_tokens": 1},
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}))
	t.Cleanup(srv.Close)
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "vllm", Endpoint: srv.URL, SeparateServer: true})
	if err != nil {
		t.Fatal(err)
	}
	return llm, func() []map[string]any { mu.Lock(); defer mu.Unlock(); return append([]map[string]any(nil), seen...) }
}

// A decision is one token read off a pre-started reply: the request
// continues "Answer: (" by a single token, with thinking off and the first
// token's alternatives asked for; the answer is the options' probabilities
// renormalised among themselves, with how confident the top one is.
func TestADecisionIsReadFromTheFirstToken(t *testing.T) {
	llm, seen := stubVLLM(t, map[string]float64{"B": 0.72, "A": 0.18, " the": 0.05})
	a, err := Ask(context.Background(), llm, Question{System: "You filter messages.", State: "a message",
		Ask: "Should the agent take it up?", Options: []string{"Take it up", "Leave it"}})
	if err != nil || !a.OK || a.Choice != 1 {
		t.Fatalf("the decision: %+v %v", a, err)
	}
	if math.Abs(a.Probs[1]-0.8) > 1e-9 || math.Abs(a.Probs[0]-0.2) > 1e-9 {
		t.Errorf("probabilities among the options: %v, want [0.2 0.8]", a.Probs)
	}
	if math.Abs(a.Confidence-0.6) > 1e-9 {
		t.Errorf("confidence %v, want 0.6 (0.8 against an even 0.5)", a.Confidence)
	}
	req := seen()[0]
	msgs := req["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "assistant" || !strings.Contains(string(mustJSON(last["content"])), "Answer: (") {
		t.Errorf("the reply is not pre-started: %v", last)
	}
	if req["continue_final_message"] != true || req["add_generation_prompt"] != false || req["max_tokens"] != float64(1) ||
		req["logprobs"] != true || req["top_logprobs"] != float64(topTokens) {
		t.Errorf("the decision's request: %v", req)
	}
	user := string(mustJSON(msgs[len(msgs)-2].(map[string]any)["content"]))
	if !strings.Contains(user, "(A) Take it up") || !strings.Contains(user, "(B) Leave it") {
		t.Errorf("the options as asked: %s", user)
	}
}

// Asked both ways, the second time with the options reversed, a model that
// answers "B" whatever the options are has no lean left: the two readings
// cancel to an even split, with no confidence.
func TestBothWaysCancelsAPositionBias(t *testing.T) {
	llm, seen := stubVLLM(t, map[string]float64{"B": 0.9, "A": 0.1})
	a, err := Ask(context.Background(), llm, Question{Ask: "?", Options: []string{"yes", "no"}, Both: true})
	if err != nil || len(seen()) != 2 {
		t.Fatalf("both ways: %v after %d asks", err, len(seen()))
	}
	if math.Abs(a.Probs[0]-0.5) > 1e-9 || a.Confidence > 1e-9 {
		t.Errorf("a pure position bias read as %v, confidence %v", a.Probs, a.Confidence)
	}
	// And each order is kept, so a low confidence can be told apart: a
	// lean to the label shown second reads 0.10 in order, 0.90 reversed.
	if got := a.OrderLine(0); got != " (0.10 in order, 0.90 reversed)" {
		t.Errorf("order line %q", got)
	}
	if (Answer{}).OrderLine(0) != "" {
		t.Error("an answer asked once has an order line")
	}
}

// No option's label among the likely first tokens is no decision; labels
// are read with the spaces and brackets a tokenizer puts round them.
func TestNoLabelIsNoDecision(t *testing.T) {
	llm, _ := stubVLLM(t, map[string]float64{"The": 0.6, "I": 0.4})
	if a, err := Ask(context.Background(), llm, Question{Ask: "?", Options: []string{"yes", "no"}}); err != nil || a.OK {
		t.Errorf("no label: %+v %v", a, err)
	}
	for tok, want := range map[string]int{"A": 0, " B": 1, "(A": 0, "b": 1, "C": -1, "AB": -1} {
		k, ok := labelIndex(tok, 2)
		if (want < 0 && ok) || (want >= 0 && (!ok || k != want)) {
			t.Errorf("token %q read as %d %v, want %d", tok, k, ok, want)
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// A server with its own System One (vLLM 0.32+) is asked there: the options
// listed in the order shown (the server labels them in that order), the
// second ask reversed, thinking off; its probabilities are the answer, and
// its label mass weighs the confidence. Asked through the server, not the
// first-token reading.
func TestTheServersOwnSystemOneIsAskedFirst(t *testing.T) {
	var mu sync.Mutex
	var native []string
	chats := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/systemone") {
			chats++
			http.Error(w, "no", 500)
			return
		}
		native = append(native, string(b))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"answers":{"d":{"type":"choice","choice":"o00","probabilities":{"o00":0.9,"o01":0.1},"confidence":0.72}},` +
			`"diagnostics":{"d":{"label_mass":0.9,"argmax_is_label":true}}}`))
	}))
	defer srv.Close()
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "vllm", Model: "judge", Endpoint: srv.URL + "/v1", SeparateServer: true})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Ask(context.Background(), llm, Question{System: "You filter.", State: "a message", Ask: "Take it up?",
		Options: []string{"Take it up", "Leave it"}, Both: true})
	if err != nil || !a.OK || !a.Native || a.Choice != 0 || chats != 0 {
		t.Fatalf("the native decision: %+v %v, %d chat calls", a, err, chats)
	}
	if len(native) != 2 {
		t.Fatalf("asked natively %d times, want both ways", len(native))
	}
	first, second := native[0], native[1]
	if !strings.Contains(first, `"model":"judge"`) || !strings.Contains(first, `"enable_thinking":false`) || !strings.Contains(first, `"instructions":"You filter."`) {
		t.Errorf("the native request: %s", first)
	}
	if strings.Index(first, `"o00"`) > strings.Index(first, `"o01"`) || strings.Index(second, `"o01"`) > strings.Index(second, `"o00"`) {
		t.Errorf("the options are not listed in order, then reversed:\n%s\n%s", first, second)
	}
	// Both asks favour o00 (0.9): averaged 0.9; confidence (0.9-0.5)/0.5=0.8,
	// weighed by the label mass 0.9.
	if math.Abs(a.Probs[0]-0.9) > 1e-9 || math.Abs(a.Confidence-0.72) > 1e-9 || math.Abs(a.LabelMass-0.9) > 1e-9 {
		t.Errorf("probs %v, confidence %v, label mass %v", a.Probs, a.Confidence, a.LabelMass)
	}
}

// An answer stands on how firmly it was chosen: the weaker of the two asks'
// probabilities when both chose it, nothing when they chose differently.
// An answer right both ways but not by much is firm; one that followed the
// order the options were shown in is not, however its average reads.
func TestAnAnswerIsAsFirmAsItsWeakerAsk(t *testing.T) {
	both := Answer{OK: true, Choice: 0, Orders: [][]float64{{0.73, 0.27}, {0.53, 0.47}}}
	if f := both.Firm(); math.Abs(f-0.53) > 1e-9 {
		t.Errorf("both asks chose it: firm %v, want 0.53", f)
	}
	lean := Answer{OK: true, Choice: 1, Orders: [][]float64{{0.90, 0.10}, {0.07, 0.93}}}
	if f := lean.Firm(); f != 0 {
		t.Errorf("the asks chose differently: firm %v, want 0", f)
	}
	once := Answer{OK: true, Choice: 1, Orders: [][]float64{{0.2, 0.8}}}
	if f := once.Firm(); math.Abs(f-0.8) > 1e-9 {
		t.Errorf("asked once: firm %v", f)
	}
	weighed := Answer{OK: true, Choice: 0, Native: true, LabelMass: 0.5, Orders: [][]float64{{0.9, 0.1}, {0.9, 0.1}}}
	if f := weighed.Firm(); math.Abs(f-0.45) > 1e-9 {
		t.Errorf("weighed by label mass: firm %v, want 0.45", f)
	}
	if (Answer{}).Firm() != 0 {
		t.Error("no answer is firm")
	}
}

// A server whose System One fails on an ask (vLLM's has no logprobs while it
// batches the ask with a request that is generating) is read from the first
// token for that ask, through a chat, which has them. An answer that was
// native one way and not the other is not native: a first-token reading has
// no label mass to average in.
func TestAFailedSystemOneAskIsReadFromTheFirstToken(t *testing.T) {
	var mu sync.Mutex
	natives, chats := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			natives++
			if natives == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":{"message":"No logprobs available for item 0.","code":500}}`))
				return
			}
			w.Write([]byte(`{"answers":{"d":{"probabilities":{"o00":0.9,"o01":0.1}}},"diagnostics":{"d":{"label_mass":0.9}}}`))
			return
		}
		chats++
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"A"},"finish_reason":"length",` +
			`"logprobs":{"content":[{"token":"A","top_logprobs":[{"token":"A","logprob":-0.105},{"token":"B","logprob":-2.303}]}]}}],` +
			`"usage":{"prompt_tokens":40,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "vllm", Model: "judge", Endpoint: srv.URL + "/v1", SeparateServer: true})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Ask(context.Background(), llm, Question{System: "You filter.", State: "a message", Ask: "Take it up?",
		Options: []string{"Take it up", "Leave it"}, Both: true})
	if err != nil || !a.OK || a.Choice != 0 {
		t.Fatalf("the decision: %+v %v", a, err)
	}
	if chats != 1 || natives != 2 {
		t.Errorf("%d chat and %d native asks: want the failed ask read from the first token, the other native", chats, natives)
	}
	if a.Native || a.LabelMass != 0 {
		t.Errorf("half native, half first-token: native %v, label mass %v", a.Native, a.LabelMass)
	}
	if a.Confidence < 0.7 {
		t.Errorf("both asks chose it at about 0.9: confidence %v", a.Confidence)
	}
}
