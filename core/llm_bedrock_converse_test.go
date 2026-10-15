package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cmcoffee/snugforge/apiclient"
)

// converseServer is a fake bedrock-runtime that records each request body and
// answers with reply(n, body), n counting from 0.
func converseServer(t *testing.T, model string, reply func(n int, w http.ResponseWriter, body map[string]any)) (LLM, *[]string, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		mu.Lock()
		n := len(bodies)
		paths = append(paths, r.URL.EscapedPath())
		bodies = append(bodies, body)
		mu.Unlock()
		reply(n, w, body)
	}))
	t.Cleanup(srv.Close)
	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	llm, err := newBedrockConverseLLM("tok", model, "us-west-2", "", strings.TrimPrefix(srv.URL, "http://"), api)
	if err != nil {
		t.Fatal(err)
	}
	return llm, &paths, &bodies
}

func converseFrame(kind, payload string) []byte {
	return encodeEventStreamFrame(map[string]string{
		":message-type": "event", ":event-type": kind, ":content-type": "application/json",
	}, []byte(payload))
}

func TestConverseModelID(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5":                   "anthropic.claude-opus-5",
		" us.anthropic.claude-opus-5 ":    "us.anthropic.claude-opus-5",
		"meta.llama3-70b-instruct-v1:0":   "meta.llama3-70b-instruct-v1:0",
		"us.amazon.nova-pro-v1:0":         "us.amazon.nova-pro-v1:0",
		"arn:aws:bedrock:us-west-2:1:x/y": "arn:aws:bedrock:us-west-2:1:x/y",
	} {
		if got := converseModelID(in); got != want {
			t.Errorf("converseModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

// A non-streaming call: the request shape Converse wants, and the reply read
// back into text, tool calls and usage.
func TestConverseChatRequestAndReply(t *testing.T) {
	llm, paths, bodies := converseServer(t, "us.anthropic.claude-opus-5", func(n int, w http.ResponseWriter, _ map[string]any) {
		w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[
			{"reasoningContent":{"reasoningText":{"text":"thinking it over","signature":"s"}}},
			{"text":"Checking."},
			{"toolUse":{"toolUseId":"t9","name":"get_time","input":{"tz":"utc"}}}]}},
			"stopReason":"tool_use",
			"usage":{"inputTokens":11,"outputTokens":7,"cacheReadInputTokens":300,"cacheWriteInputTokens":5}}`))
	})
	msgs := []Message{
		{Role: "user", Content: "what time is it"},
		{Role: "assistant", Content: "Let me look.", ToolCalls: []ToolCall{{ID: "t1", Name: "get_time", Args: map[string]any{"tz": "pst"}}}},
		{Role: "user", ToolResults: []ToolResult{{ID: "t1", Content: ""}}},
		{Role: "user", Content: "and in utc?"},
	}
	tools := []Tool{{Name: "get_time", Description: "", Parameters: map[string]ToolParam{"tz": {Type: "string"}}}}
	resp, err := llm.Chat(t.Context(), msgs, WithTools(tools), WithSystemPrompt("Be brief."), WithThink(true))
	if err != nil {
		t.Fatal(err)
	}
	if (*paths)[0] != "/model/us.anthropic.claude-opus-5/converse" {
		t.Errorf("path = %q", (*paths)[0])
	}
	body := (*bodies)[0]
	raw, _ := json.Marshal(body)
	s := string(raw)

	// Roles alternate: the tool result and the next user line are one turn.
	m := body["messages"].([]any)
	if len(m) != 3 {
		t.Fatalf("messages = %d, want 3 (user, assistant, merged user): %s", len(m), s)
	}
	last := m[2].(map[string]any)["content"].([]any)
	if _, ok := last[0].(map[string]any)["toolResult"]; !ok || last[1].(map[string]any)["text"] != "and in utc?" {
		t.Errorf("merged user turn = %v", last)
	}
	for _, want := range []string{
		`"toolUseId":"t1"`, `"(no output)"`, `"status":"success"`,
		`"toolSpec":{"description":"get_time"`, // a blank description is refused; the name stands in
		`"cachePoint":{"type":"default"}`,
		`"thinking":{"budget_tokens"`,
		`"maxTokens":`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("request lacks %s:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "Be brief.") {
		t.Errorf("system prompt missing: %s", s)
	}

	if resp.Content != "Checking." || resp.StopReason != "tool_use" {
		t.Errorf("content=%q stop=%q", resp.Content, resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "t9" || resp.ToolCalls[0].Args["tz"] != "utc" {
		t.Errorf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.InputTokens != 11 || resp.OutputTokens != 7 || resp.CacheReadTokens != 300 || resp.CacheWriteTokens != 5 {
		t.Errorf("usage = %+v", resp)
	}
}

// A model other than Claude: no Claude thinking fields, and no output
// allowance it might refuse unless the caller asked for one.
func TestConverseOtherModelGetsNoClaudeFields(t *testing.T) {
	llm, _, bodies := converseServer(t, "meta.llama3-70b-instruct-v1:0", func(n int, w http.ResponseWriter, _ map[string]any) {
		w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"hi"}]}},"stopReason":"end_turn","usage":{}}`))
	})
	if _, err := llm.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, WithThink(true)); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal((*bodies)[0])
	if s := string(raw); strings.Contains(s, "thinking") || strings.Contains(s, "maxTokens") {
		t.Errorf("a Llama request carried Claude's fields: %s", s)
	}
	if got := llm.(*bedrockConverseClient).ContextSize(); got != converseDefaultContextSize {
		t.Errorf("context size = %d", got)
	}
}

// Without tools, history's tool calls and results go as text: Converse
// refuses tool blocks in a request that defines no tools.
func TestConverseToolHistoryWithoutToolsIsText(t *testing.T) {
	out := buildConverseMessages([]Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a", Name: "look", Args: map[string]any{"x": 1}}}},
		{Role: "user", ToolResults: []ToolResult{{ID: "a", Content: "found it"}}},
	}, false)
	raw, _ := json.Marshal(out)
	s := string(raw)
	if strings.Contains(s, "toolUse") || strings.Contains(s, "toolResult") {
		t.Errorf("tool blocks sent with no tools defined: %s", s)
	}
	if !strings.Contains(s, "look") || !strings.Contains(s, "found it") {
		t.Errorf("the tool history was lost: %s", s)
	}
}

// A model that refuses prompt caching is believed: the call is sent again
// without cache points, and later calls leave them off.
func TestConverseCacheRefusalRetriesWithout(t *testing.T) {
	model := "us.mistral.test-cache-refusal-v1:0"
	llm, _, bodies := converseServer(t, model, func(n int, w http.ResponseWriter, body map[string]any) {
		raw, _ := json.Marshal(body)
		if strings.Contains(string(raw), "cachePoint") {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"You invoked an unsupported model or your request did not allow prompt caching."}`))
			return
		}
		w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{}}`))
	})
	resp, err := llm.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, WithSystemPrompt("s"))
	if err != nil || resp.Content != "ok" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if _, err := llm.Chat(t.Context(), []Message{{Role: "user", Content: "again"}}, WithSystemPrompt("s")); err != nil {
		t.Fatal(err)
	}
	if len(*bodies) != 3 {
		t.Errorf("requests = %d, want 3 (refused, retried, then straight through)", len(*bodies))
	}
}

// Streaming: Converse's events become text as it arrives, an assembled tool
// call, the stop reason and the usage that follows it.
func TestConverseStream(t *testing.T) {
	frames := [][]byte{
		converseFrame("messageStart", `{"role":"assistant"}`),
		converseFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"hmm"}}}`),
		converseFrame("contentBlockStop", `{"contentBlockIndex":0}`),
		converseFrame("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"text":"Hi "}}`),
		converseFrame("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"text":"there"}}`),
		converseFrame("contentBlockStop", `{"contentBlockIndex":1}`),
		converseFrame("contentBlockStart", `{"contentBlockIndex":2,"start":{"toolUse":{"toolUseId":"tu1","name":"get_time"}}}`),
		converseFrame("contentBlockDelta", `{"contentBlockIndex":2,"delta":{"toolUse":{"input":"{\"tz\":"}}}`),
		converseFrame("contentBlockDelta", `{"contentBlockIndex":2,"delta":{"toolUse":{"input":"\"utc\"}"}}}`),
		converseFrame("contentBlockStop", `{"contentBlockIndex":2}`),
		converseFrame("messageStop", `{"stopReason":"tool_use"}`),
		converseFrame("metadata", `{"usage":{"inputTokens":8,"outputTokens":14,"cacheReadInputTokens":100}}`),
	}
	llm, paths, _ := converseServer(t, "us.anthropic.claude-opus-5", func(n int, w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		for _, f := range frames {
			w.Write(f)
		}
	})
	var chunks, thoughts []string
	resp, err := llm.ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}},
		func(c string) { chunks = append(chunks, c) },
		WithReasoningStream(func(c string) { thoughts = append(thoughts, c) }))
	if err != nil {
		t.Fatal(err)
	}
	if (*paths)[0] != "/model/us.anthropic.claude-opus-5/converse-stream" {
		t.Errorf("path = %q", (*paths)[0])
	}
	if len(chunks) != 2 || resp.Content != "Hi there" {
		t.Errorf("chunks=%q content=%q", chunks, resp.Content)
	}
	if len(thoughts) != 1 || thoughts[0] != "hmm" {
		t.Errorf("thinking = %q", thoughts)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "tu1" || resp.ToolCalls[0].Args["tz"] != "utc" {
		t.Errorf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.StopReason != "tool_use" || resp.InputTokens != 8 || resp.OutputTokens != 14 || resp.CacheReadTokens != 100 {
		t.Errorf("stop=%q usage=%d/%d/%d", resp.StopReason, resp.InputTokens, resp.OutputTokens, resp.CacheReadTokens)
	}
}

// An exception mid-stream is reported, and partial text is kept as an
// interrupted reply rather than passed off as the answer.
func TestConverseStreamException(t *testing.T) {
	llm, _, _ := converseServer(t, "us.anthropic.claude-opus-5", func(n int, w http.ResponseWriter, _ map[string]any) {
		w.Write(converseFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"part"}}`))
		w.Write(encodeEventStreamFrame(map[string]string{
			":message-type": "exception", ":exception-type": "throttlingException",
		}, []byte(`{"message":"slow down"}`)))
	})
	resp, err := llm.ChatStream(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "part" || resp.StopReason != stopInterrupted {
		t.Errorf("content=%q stop=%q", resp.Content, resp.StopReason)
	}
}

// NewLLMFromConfig builds the Converse client for "converse", and carries the
// tier's context cap onto it.
func TestNewLLMFromConfigBuildsConverse(t *testing.T) {
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "bedrock", BedrockAPI: "converse", APIKey: "tok",
		Model: "meta.llama3-70b-instruct-v1:0", Region: "us-west-2", ContextSize: 64000})
	if err != nil {
		t.Fatal(err)
	}
	inner := llm.(*retryLLM).inner
	c, ok := inner.(*bedrockConverseClient)
	if !ok {
		t.Fatalf("built %T", inner)
	}
	if c.ContextSize() != 64000 {
		t.Errorf("context size = %d", c.ContextSize())
	}
}
