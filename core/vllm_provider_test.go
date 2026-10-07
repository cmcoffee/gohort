package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// recordingChatServer answers every chat call with a short reply and keeps
// each request body it was sent.
func recordingChatServer(t *testing.T) (url string, bodies func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		mu.Lock()
		got = append(got, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"qwen","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), got...)
	}
}

// vLLM is the llama.cpp client minus thinking_budget_tokens. vLLM ignores that
// field (a 128-token budget still thought for thousands), so sending it only
// padded max_tokens for a cap nothing enforced. Effort still reaches the model,
// through the chat template, and the no-think switch still goes out.
func TestVLLMSendsEffortButNoBudget(t *testing.T) {
	for _, c := range []struct {
		provider   string
		wantBudget bool
	}{
		{"vllm", false},
		{"llama.cpp", true},
	} {
		url, bodies := recordingChatServer(t)
		llm, err := NewLLMFromConfig(LLMProviderConfig{
			Provider: c.provider, Model: "qwen", Endpoint: url,
			ThinkingBudget: 4096, NoThinkUseKwarg: true, NoThinkSendBudget: true,
		})
		if err != nil {
			t.Fatalf("%s: build: %v", c.provider, err)
		}
		msgs := []Message{{Role: "user", Content: "hi"}}
		if _, err := llm.Chat(t.Context(), msgs, WithEffort("low")); err != nil {
			t.Fatalf("%s: effort call: %v", c.provider, err)
		}
		if _, err := llm.Chat(t.Context(), msgs, WithThink(false)); err != nil {
			t.Fatalf("%s: no-think call: %v", c.provider, err)
		}
		got := bodies()
		if len(got) != 2 {
			t.Fatalf("%s: %d calls reached the server, want 2", c.provider, len(got))
		}
		for i, b := range got {
			if _, has := b["thinking_budget_tokens"]; has != c.wantBudget {
				t.Errorf("%s call %d: thinking_budget_tokens present=%v, want %v (body %v)", c.provider, i, has, c.wantBudget, b)
			}
		}
		kw, _ := got[0]["chat_template_kwargs"].(map[string]any)
		if kw["reasoning_effort"] != "low" || kw["enable_thinking"] != true {
			t.Errorf("%s: effort call kwargs %v, want reasoning_effort=low with thinking on", c.provider, kw)
		}
		kw, _ = got[1]["chat_template_kwargs"].(map[string]any)
		if kw["enable_thinking"] != false {
			t.Errorf("%s: no-think call kwargs %v, want enable_thinking=false", c.provider, kw)
		}
	}
}

// The rest of the framework treats vLLM as the local server it is: native
// tool calls, a model this instance may lend to a peer, and its own name in
// logs and errors.
func TestVLLMIsALocalServer(t *testing.T) {
	if !ProviderHasNativeTools("vllm") {
		t.Error("vLLM calls tools natively; without this its tools would be parsed out of prose")
	}
	if !peerLendableProviders["vllm"] {
		t.Error("a local vLLM model should be lendable to peers, as llama.cpp is")
	}
	if ok, why := ProviderLooksPrivate("vllm", "http://localhost:8000/v1"); !ok {
		t.Errorf("a loopback vLLM endpoint read as not private: %s", why)
	}
	if ok, _ := ProviderLooksPrivate("vllm", "https://api.example.com/v1"); ok {
		t.Error("a public vLLM endpoint read as private: the endpoint, not the name, decides")
	}
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "vllm", Model: "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	oc, ok := unwrapOpenAIClient(llm)
	if !ok {
		t.Fatal("vllm did not build the OpenAI-compatible client")
	}
	if oc.endpoint != "http://localhost:8000/v1" || oc.provider() != "vllm" {
		t.Errorf("endpoint %q tag %q, want vLLM's default port and its own name", oc.endpoint, oc.provider())
	}
}

// unwrapOpenAIClient digs the OpenAI-compatible client out of the wrappers
// NewLLMFromConfig puts around it.
func unwrapOpenAIClient(l LLM) (*openAIClient, bool) {
	for i := 0; i < 8 && l != nil; i++ {
		switch v := l.(type) {
		case *openAIClient:
			return v, true
		case *retryLLM:
			l = v.inner
		default:
			return nil, false
		}
	}
	return nil, false
}
