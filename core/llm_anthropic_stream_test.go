package core

import (
	"encoding/json"
	"testing"
)

// A streamed tool call carries its arguments in input_json_delta events, whose
// fragment field is partial_json — NOT text. Reading the wrong field yields a
// correctly-named call with empty arguments, which surfaces as every tool
// answering "required parameter missing" and reads like the model failing to
// send arguments rather than the client failing to read them.
func TestStreamedToolArgumentsComeFromPartialJSON(t *testing.T) {
	st := &anthStreamState{}
	for _, e := range []string{
		`{"type":"message_start","message":{"model":"claude-opus-5","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"web_search"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"world news\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
	} {
		st.feed([]byte(e))
	}
	resp := st.response("test")
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].Args["query"]; got != "world news" {
		t.Errorf("args = %+v — the fragments were dropped", resp.ToolCalls[0].Args)
	}
}

// Fragments must concatenate in order: each delta is a slice of one JSON
// document, so a single dropped or reordered piece makes the whole thing
// unparseable and the call arrives empty.
func TestStreamedToolArgumentsConcatenateAcrossManyFragments(t *testing.T) {
	st := &anthStreamState{}
	st.feed([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"fetch_url"}}`))
	for _, frag := range []string{`{"url"`, `:"https://`, `example.com`, `/news"}`} {
		st.feed([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` +
			mustJSONString(frag) + `}}`))
	}
	st.feed([]byte(`{"type":"content_block_stop","index":0}`))

	resp := st.response("test")
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Args["url"] != "https://example.com/news" {
		t.Errorf("reassembled args = %+v", resp.ToolCalls)
	}
}

func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
