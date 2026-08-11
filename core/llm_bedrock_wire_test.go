package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/apiclient"
)

// Proves what actually goes on the wire for the InvokeModel path: URL, method,
// headers, signature, and body. Everything AWS validates before it looks at
// permissions.
func TestBedrockRuntimeWireFormat(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRETTEST")
	t.Setenv("AWS_SESSION_TOKEN", "TOKENTEST")

	var gotPath, gotAuth, gotToken, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotAuth = r.Header.Get("Authorization")
		gotToken = r.Header.Get("X-Amz-Security-Token")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","model":"m","stop_reason":"end_turn",
			"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	llm, err := newBedrockRuntimeLLM("", "anthropic.claude-3-5-sonnet-20241022-v2:0",
		"us-west-2", "", host, api)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}

	resp, err := llm.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}

	wantPath := "/model/anthropic.claude-3-5-sonnet-20241022-v2%3A0/invoke"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if gotToken != "TOKENTEST" {
		t.Errorf("session token header = %q", gotToken)
	}
	for _, want := range []string{
		"AWS4-HMAC-SHA256 ",
		"/us-west-2/bedrock/aws4_request",
		"x-amz-security-token",
	} {
		if !strings.Contains(gotAuth, want) {
			t.Errorf("Authorization missing %q\n  got %s", want, gotAuth)
		}
	}
	if gotBody["anthropic_version"] != bedrockAnthropicVersion {
		t.Errorf("body anthropic_version = %v", gotBody["anthropic_version"])
	}
	if _, bad := gotBody["model"]; bad {
		t.Error("body carried a model field")
	}
	if gotBody["messages"] == nil {
		t.Error("body carried no messages")
	}
}

// Regression against a REAL, confirmed-working Bedrock call: the exact model
// id, region, envelope, and response body from a successful
// `aws bedrock-runtime invoke-model` on an account whose permission set grants
// bedrock:InvokeModel. If this passes, anything still failing in the field is
// configuration or AWS-side, not the client.
func TestBedrockRuntimeAgainstRealInvokeModelExchange(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRETTEST")

	const realResponse = `{"model":"claude-opus-4-8","id":"msg_bdrk_se7tkefzq42dbw2cjwzdbwc34ia4e6ivays5jxa7h424yc7uy65q","type":"message","role":"assistant","content":[{"type":"text","text":"Hi there! How can I help you today?"}],"stop_reason":"end_turn","stop_sequence":null,"stop_details":null,"usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":14,"output_tokens_details":{"thinking_tokens":0},"service_tier":"standard"}}`

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, realResponse)
	}))
	defer srv.Close()

	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	llm, err := newBedrockRuntimeLLM("", "us.anthropic.claude-opus-4-8", "us-west-2", "",
		strings.TrimPrefix(srv.URL, "http://"), api)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	resp, err := llm.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}

	// The id has no colon, so it must NOT be mangled by the escaping added for
	// versioned ids.
	if want := "/model/us.anthropic.claude-opus-4-8/invoke"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if resp.Content != "Hi there! How can I help you today?" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q", resp.StopReason)
	}
	if resp.InputTokens != 8 || resp.OutputTokens != 14 {
		t.Errorf("tokens = %d/%d, want 8/14", resp.InputTokens, resp.OutputTokens)
	}
	if resp.Model != "claude-opus-4-8" {
		t.Errorf("model = %q", resp.Model)
	}
}

// Tool arguments must survive the InvokeModel path. Every grouped tool in the
// fleet takes a required "action", so an input that arrives empty turns every
// call into a bare probe that answers with help text — which is precisely the
// symptom of a turn spent re-calling tools and learning nothing.
func TestBedrockRuntimeCarriesToolArguments(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRETTEST")

	const withToolUse = `{"model":"claude-opus-5","id":"msg_1","type":"message","role":"assistant",
		"stop_reason":"tool_use","content":[
			{"type":"text","text":"Looking that up."},
			{"type":"tool_use","id":"toolu_1","name":"archetype","input":{"action":"read","slug":"knowledge_base"}}
		],"usage":{"input_tokens":8,"output_tokens":14}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, withToolUse)
	}))
	defer srv.Close()

	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	llm, err := newBedrockRuntimeLLM("", "us.anthropic.claude-opus-5", "us-west-2", "",
		strings.TrimPrefix(srv.URL, "http://"), api)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Name != "archetype" {
		t.Errorf("name = %q", tc.Name)
	}
	if tc.Args["action"] != "read" || tc.Args["slug"] != "knowledge_base" {
		t.Errorf("arguments were lost in transit: %+v", tc.Args)
	}
}

// A tool input the decoder cannot read must not silently become "no arguments".
// That is how a grouped tool ends up answering with its usage spec: the model
// constructed a call, the arguments were dropped in transit, and nothing said so.
func TestToolInputShapesAndFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		input   string
		want    string
		wantErr bool
	}{
		"object":         {`{"action":"read","slug":"kb"}`, "read", false},
		"string-encoded": {`"{\"action\":\"read\",\"slug\":\"kb\"}"`, "read", false},
		"empty":          {``, "", false},
		"empty string":   {`""`, "", false},
		"garbage":        {`"not json at all"`, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			args, err := decodeToolInput([]byte(tc.input))
			if tc.wantErr && err == nil {
				t.Fatal("an undecodable input must report an error, not degrade to a bare call")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want != "" && args["action"] != tc.want {
				t.Errorf("action = %v, want %q", args["action"], tc.want)
			}
			if args == nil {
				t.Error("args must never be nil — callers index it directly")
			}
		})
	}
}
