package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A system message past the first one goes to a local server as a user turn,
// in the same place: Qwen's chat template refuses a system message anywhere
// but first, and the guardrail's steer sits just before the request. OpenAI's
// own API keeps it as written.
func TestALaterSystemMessageReachesALocalServerAsAUserTurn(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "earlier"}, {Role: "assistant", Content: "ok"},
		{Role: "system", Content: "Constraint on this reply"}, {Role: "user", Content: "now"}}
	roles := func(c *openAIClient) string {
		var r []string
		for _, m := range c.buildMessages(ChatConfig{SystemPrompt: "persona"}, msgs) {
			r = append(r, m.Role)
		}
		return strings.Join(r, ",")
	}
	for name, c := range map[string]*openAIClient{"vllm": {vllm: true}, "llama.cpp": {llamacpp: true}} {
		if got := roles(c); got != "system,user,assistant,user,user" {
			t.Errorf("%s: %s", name, got)
		}
	}
	if got := roles(&openAIClient{}); got != "system,user,assistant,system,user" {
		t.Errorf("openai: %s", got)
	}
}

// A tool that arrived mid-turn (RenderLate) leaves the tools block on vLLM,
// whose template ignores lazy_tool_names and would put it at the top, where
// adding it invalidates the whole cached prompt. It is described in a note at
// the end instead, so the tools block, and the prompt up to the conversation's
// tail, is the same as the round before it was loaded. llama.cpp's split
// template keeps it in the block, named in lazy_tool_names.
func TestALateToolLeavesTheToolsBlockOnVLLM(t *testing.T) {
	early := Tool{Name: "web_search", Description: "Search.", Parameters: map[string]ToolParam{"q": {Type: "string"}}}
	late := Tool{Name: "recall_about", Description: "Recall.", Parameters: map[string]ToolParam{"name": {Type: "string", Description: "Who"}}, RenderLate: true}
	msgs := []Message{{Role: "user", Content: "hi"}}
	for _, c := range []struct {
		provider  string
		stream    bool
		wantBlock []string
	}{
		{"vllm", false, []string{"web_search"}},
		{"vllm", true, []string{"web_search"}},
		{"llama.cpp", false, []string{"web_search", "recall_about"}},
	} {
		url, bodies := recordingChatServer(t)
		llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: c.provider, Model: "qwen", Endpoint: url})
		if err != nil {
			t.Fatal(err)
		}
		if c.stream {
			llm.ChatStream(t.Context(), msgs, func(string) {}, WithTools([]Tool{early, late}))
		} else {
			llm.Chat(t.Context(), msgs, WithTools([]Tool{early, late}))
		}
		got := bodies()
		if len(got) == 0 {
			t.Fatalf("%s: no request", c.provider)
		}
		body := got[len(got)-1]
		var block []string
		tools, _ := body["tools"].([]any)
		for _, tl := range tools {
			fn, _ := tl.(map[string]any)["function"].(map[string]any)
			block = append(block, fmt.Sprint(fn["name"]))
		}
		if strings.Join(block, ",") != strings.Join(c.wantBlock, ",") {
			t.Errorf("%s stream=%v: tools block %v, want %v", c.provider, c.stream, block, c.wantBlock)
		}
		sent, _ := body["messages"].([]any)
		last, _ := sent[len(sent)-1].(map[string]any)
		lastText := fmt.Sprint(last["content"])
		if c.provider == "vllm" {
			if !strings.Contains(lastText, `"name":"recall_about"`) || !strings.Contains(lastText, "Who") {
				t.Errorf("%s: the late tool is not described at the end: %d messages, last %q", c.provider, len(sent), lastText)
			}
		} else if strings.Contains(lastText, "recall_about") {
			t.Errorf("%s: a note was added where the template already places late tools", c.provider)
		}
	}
}
