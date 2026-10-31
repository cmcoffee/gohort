package admin

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

// gohort's own providers are listed with the other connectors, read from
// their settings: each says what it wraps, whether it is in use (and if not,
// what is), and where it is set up. One in use is tested by its settings' own
// test; one that is not says why instead. None can be approved, exported or
// deleted.
func TestGohortsOwnProvidersAreConnectors(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	a := &AdminApp{}
	a.db = &DBase{Store: kvlite.MemStore()}

	got := map[string]builtinConnector{}
	for _, b := range a.builtinConnectors() {
		got[b.name] = b
	}
	// Embeddings, transcription, speech and images always come from a
	// connector or a peer: there is no built-in embedder, transcriber,
	// speaker or image code to list.
	for _, gone := range []string{"built-in:embeddings", "built-in:transcription", "built-in:speech", "built-in:gemini-images", "built-in:openai-images"} {
		if b, ok := got[gone]; ok {
			t.Errorf("%s listed: %+v", gone, b)
		}
	}
	if b := got["built-in:web-search"]; b.wrapper != "Web search ⇐ duckduckgo ⇐ lite.duckduckgo.com" || !strings.HasPrefix(b.status, "In use for web search") || b.test == nil {
		t.Errorf("web search, never set up: %+v", b)
	}
	a.db.Set(SearchTable, "provider", "service:finder")
	a.db.Set(SearchTable, "api_key", "k")
	for _, b := range a.builtinConnectors() {
		if b.name == "built-in:web-search" && (b.status != "Not in use: web search is done by the connector finder. Set up under Admin > Capabilities > Web Search." || b.test != nil) {
			t.Errorf("web search through a connector: %+v", b)
		}
	}
	a.db.Set(SearchTable, "provider", "")
	if b := got["built-in:worker-llm"]; b.wrapper != "Worker LLM ⇐ not set" || !strings.HasPrefix(b.status, "Off:") || b.test != nil {
		t.Errorf("worker, never set: %+v", b)
	}
	if b := got["built-in:lead-llm"]; !strings.HasPrefix(b.status, "Not set: the worker LLM answers in its place.") {
		t.Errorf("lead, never set: %+v", b)
	}
	a.db.Set(LLMTable, "provider", "anthropic")
	a.db.Set(LLMTable, "model", "claude-x")
	a.db.Set(LLMTable, "api_key", "k")
	if b := a.builtinLLM(LLMTable, "worker-llm", "Worker LLM"); b.wrapper != "Worker LLM ⇐ anthropic (model claude-x) ⇐ api.anthropic.com (key set)" ||
		b.status != "In use as the worker LLM. Set up under Admin > LLMs > Worker LLM." || b.test == nil {
		t.Errorf("worker on a hosted provider: %+v", b)
	}
	a.db.Set(LeadLLMTable, "provider", "vllm")
	a.db.Set(LeadLLMTable, "model", "big-model")
	a.db.Set(LeadLLMTable, "endpoint", "http://gpu.example.test:8000/v1")
	if b := a.builtinLLM(LeadLLMTable, "lead-llm", "Lead LLM"); b.wrapper != "Lead LLM ⇐ vllm (model big-model) ⇐ gpu.example.test:8000 (no key)" {
		t.Errorf("lead on a model server: %+v", b)
	}
	a.db.Set(LeadLLMTable, "provider", "service:hosted")
	if b := a.builtinLLM(LeadLLMTable, "lead-llm", "Lead LLM"); b.wrapper != "Lead LLM ⇐ the connector hosted (its row shows its calls and cost)" || b.test == nil {
		t.Errorf("lead on a model connector: %+v", b)
	}
	if b := got["built-in:mail"]; b.wrapper != "Mail ⇐ SMTP ⇐ localhost:25 (no sign-in)" || !strings.HasPrefix(b.status, "Off:") || b.test != nil {
		t.Errorf("mail, never set up: %+v", b)
	}
	a.db.Set(MailTable, "server", "smtp.example.test:587")
	a.db.Set(MailTable, "username", "u")
	a.db.Set(MailTable, "password", "p")
	if b := a.builtinMail(); b.wrapper != "Mail ⇐ SMTP ⇐ smtp.example.test:587 (sign-in set)" || !strings.HasPrefix(b.status, "In use for mail") || b.test == nil {
		t.Errorf("mail by SMTP: %+v", b)
	}
	a.db.Set(MailTable, "provider", "service:postbox")
	if b := a.builtinMail(); !strings.HasPrefix(b.status, "Not in use: mail is sent by the connector postbox.") || b.test != nil {
		t.Errorf("mail through a connector: %+v", b)
	}
	if b := got["built-in:page-rendering"]; !strings.HasPrefix(b.status, "In use for page rendering") || b.test == nil {
		t.Errorf("page rendering, never set: %+v", b)
	}
	SetBrowseConfig(BrowseConfig{Source: "service:reader"})
	t.Cleanup(func() { SetBrowseConfig(BrowseConfig{}) })
	if b := builtinBrowse(); b.status != "Not in use: pages are rendered by the connector reader. Set up under Admin > Capabilities > Page Rendering (Browser)." || b.test != nil {
		t.Errorf("page rendering through a connector: %+v", b)
	}

	c := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/connectors/test", nil)}
	res, _ := a.builtinTest(c, "built-in:page-rendering")
	if body, _ := res.(map[string]any); !strings.HasPrefix(body["message"].(string), "Not in use") || body["warnings"] == nil {
		t.Errorf("testing a built-in not in use: %v", res)
	}
	if _, err := a.builtinTest(c, "built-in:nothing"); err == nil {
		t.Error("an unknown built-in was tested")
	}

	rowsAny, _ := a.handleConnectors(c)
	b, _ := json.Marshal(rowsAny)
	var rows []map[string]any
	json.Unmarshal(b, &rows)
	builtins := 0
	for _, r := range rows {
		if strings.HasPrefix(r["name"].(string), builtinPrefix) {
			builtins++
			if r["stored"] != false || r["state"] != "built-in" || r["approved"] != false {
				t.Errorf("a built-in row could be approved, edited or deleted: %v", r)
			}
		}
	}
	if builtins != 5 {
		t.Errorf("%d built-in rows, want 5", builtins)
	}
}
