package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer srv.Close()
	a := &AdminApp{}
	a.db = &DBase{Store: kvlite.MemStore()}
	a.db.Set(EmbeddingTable, "current", EmbeddingConfig{Enabled: true, Provider: "local", Endpoint: srv.URL + "/v1", Model: "nomic-embed-text"})
	a.db.Set(media.SpeechTable, "current", media.SpeechConfig{Enabled: true, Provider: "service:voice", Endpoint: "http://localhost:8880/v1"})
	a.db.Set(ImageTable, "provider", "openai")

	got := map[string]builtinConnector{}
	for _, b := range a.builtinConnectors() {
		got[b.name] = b
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	if b := got["built-in:embeddings"]; b.wrapper != "Embeddings ⇐ embeddings (model nomic-embed-text) ⇐ "+host+" (no key)" ||
		!strings.HasPrefix(b.status, "In use for Embeddings") || b.test == nil {
		t.Errorf("embeddings: %+v", b)
	}
	if b := got["built-in:speech"]; b.status != "Not in use: speech is made by the connector voice. Set up under Admin > Capabilities > Speech (TTS)." || b.test != nil {
		t.Errorf("speech while a service speaks: %+v", b)
	}
	if b := got["built-in:transcription"]; !strings.HasPrefix(b.status, "Off.") || b.test != nil {
		t.Errorf("transcription, never set up: %+v", b)
	}
	if b := got["built-in:gemini-images"]; !strings.Contains(b.status, "set to the built-in OpenAI") {
		t.Errorf("gemini while openai is chosen: %+v", b)
	}
	if b := got["built-in:openai-images"]; !strings.HasPrefix(b.status, "In use for Image Generation") || !strings.Contains(b.wrapper, "api.openai.com") {
		t.Errorf("openai: %+v", b)
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
	res, err := a.builtinTest(c, "built-in:embeddings")
	if body, _ := res.(map[string]any); err != nil || !strings.Contains(body["message"].(string), "3-dim embedding") || body["warnings"] != nil {
		t.Errorf("testing the embedder in use: %v %v", res, err)
	}
	res, _ = a.builtinTest(c, "built-in:speech")
	if body, _ := res.(map[string]any); !strings.HasPrefix(body["message"].(string), "Not in use") || body["warnings"] == nil {
		t.Errorf("testing a built-in not in use: %v", res)
	}
	res, _ = a.builtinTest(c, "built-in:openai-images")
	if body, _ := res.(map[string]any); !strings.Contains(body["message"].(string), "set one under Admin > Capabilities > Image Generation") {
		t.Errorf("an image key test from the Connectors page: %v", res)
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
	if builtins != 10 {
		t.Errorf("%d built-in rows, want 10", builtins)
	}
}
