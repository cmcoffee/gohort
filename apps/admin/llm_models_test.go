package admin

// The Model field's "List models" button posts the form as it stands and
// gets back what the provider offers. These pin the two things a person sees:
// the list, with the tier's own stored key sent when the box is blank, and a
// refusal read back as the provider's reason rather than a dead button.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

func modelsAdmin(t *testing.T) (*AdminApp, func(body string) map[string]any) {
	t.Helper()
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerLLMRoutes(mux)
	call := func(body string) map[string]any {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/lead-llm/models", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("not JSON: %s", w.Body.String())
		}
		return out
	}
	return a, call
}

func TestLLMModelsListsWithStoredKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[{"id":"qwen-b"},{"id":"qwen-a"}]}`))
	}))
	defer srv.Close()
	a, call := modelsAdmin(t)
	at := LLMProviderConfig{Provider: "vllm", Endpoint: srv.URL + "/v1"}
	at.SaveKey(a.db, LeadLLMTable, "stored-lead-key")
	at.SaveKey(a.db, LLMTable, "worker-key")

	out := call(`{"provider":"vllm","endpoint":"` + srv.URL + `/v1","model":"something-else"}`)
	if out["error"] != nil {
		t.Fatalf("unexpected error: %v", out["error"])
	}
	list, _ := out["suggestions"].([]any)
	if len(list) != 2 || list[0] != "qwen-a" || list[1] != "qwen-b" {
		t.Errorf("suggestions = %v, want sorted [qwen-a qwen-b]", list)
	}
	if gotAuth != "Bearer stored-lead-key" {
		t.Errorf("auth = %q, want the lead tier's own stored key", gotAuth)
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "something-else") {
		t.Errorf("note should say the current value is not listed: %q", note)
	}
}

func TestLLMModelsRefusalReadsAsReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key given"}}`))
	}))
	defer srv.Close()
	_, call := modelsAdmin(t)
	out := call(`{"provider":"llama.cpp","endpoint":"` + srv.URL + `","api_key":"typed"}`)
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "invalid api key given") || !strings.Contains(msg, "401") {
		t.Errorf("error = %q, want the provider's reason and status", msg)
	}
	if out = call(`{"provider":""}`); out["error"] == nil {
		t.Error("no provider should answer with an error, not an empty list")
	}
}

func TestLLMModelFieldsHaveAList(t *testing.T) {
	a := &AdminApp{}
	want := map[string]string{"Worker LLM": "api/worker-llm/models", "Lead LLM": "api/lead-llm/models"}
	for _, sec := range a.llmSections() {
		src, ok := want[sec.Title]
		if !ok {
			continue
		}
		delete(want, sec.Title)
		panel := sec.Body.(ui.FormPanel)
		found := false
		for _, f := range panel.Fields {
			if f.Field == "model" {
				found = f.SuggestionsSource == src
			}
		}
		if !found {
			t.Errorf("%s model field does not list from %s", sec.Title, src)
		}
	}
	if len(want) != 0 {
		t.Errorf("sections not found: %v", want)
	}
	for _, st := range wizardPanel(t).Steps {
		for _, f := range st.Fields {
			if f.Field == "model" && f.SuggestionsSource != "api/worker-llm/models" {
				t.Errorf("wizard model field lists from %q", f.SuggestionsSource)
			}
		}
	}
}

// The connection fields rebuild the live model on every save, so they save
// when left, not on each pause in typing (which tried half-typed model ids).
func TestLLMConnectionFieldsSaveOnBlur(t *testing.T) {
	want := map[string]bool{"model": true, "api_key": true, "endpoint": true}
	for _, sec := range (&AdminApp{}).llmSections() {
		if sec.Title != "Worker LLM" && sec.Title != "Lead LLM" {
			continue
		}
		for _, f := range sec.Body.(ui.FormPanel).Fields {
			if want[f.Field] && !f.SaveOnBlur {
				t.Errorf("%s: %s saves while typing", sec.Title, f.Field)
			}
		}
	}
}

// A Bedrock tier signs with its own profile in its own region. The listing
// took only the provider, address and key, so it signed as the machine's
// default profile in us-east-1 and was refused with "no AWS credentials
// found" for a tier that chats fine. The form's Bedrock fields reach the
// config the listing is built from.
func TestLLMModelsQueryCarriesBedrockSettings(t *testing.T) {
	var q llmModelsQuery
	body := `{"provider":" bedrock ","model":"m","aws_region":"us-west-2","aws_profile":" work ","bedrock_api":"converse","api_key_from":"own"}`
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatal(err)
	}
	cfg := q.config()
	if cfg.Provider != "bedrock" || cfg.Region != "us-west-2" || cfg.Profile != "work" || cfg.BedrockAPI != "converse" {
		t.Errorf("config = provider %q region %q profile %q api %q, want bedrock, us-west-2, work, converse",
			cfg.Provider, cfg.Region, cfg.Profile, cfg.BedrockAPI)
	}
}

// The Models page lists what models have refused of a request, in words, and
// Forget puts one back to the request as configured.
func TestLearnedRefusalsAreListedAndForgotten(t *testing.T) {
	a, _ := modelsAdmin(t)
	// Stored before the store is read, as a restart finds it.
	a.db.Set("request_adaptations", "thinking.off\x00claude-test-page", RequestAdaptation{
		Field: "thinking.off", Subject: "claude-test-page", Step: "between_tools", Since: time.Now(), Refusal: "send between_tools instead"})
	prev := RootDB
	RootDB = a.db
	t.Cleanup(func() { RootDB = prev })
	mux := http.NewServeMux()
	a.registerLLMRoutes(mux)
	do := func(method, path string) (int, string) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader("{}")))
		return w.Code, w.Body.String()
	}
	code, body := do("GET", "/api/llm-adaptations")
	if code != 200 || !strings.Contains(body, `"model":"claude-test-page"`) || !strings.Contains(body, `"what":"Thinking off"`) || !strings.Contains(body, `"_id":"thinking.off|claude-test-page"`) {
		t.Fatalf("listed: %d %s", code, body)
	}
	if code, body := do("POST", "/api/llm-adaptations/forget?id="+url.QueryEscape("thinking.off|claude-test-page")); code != 200 {
		t.Fatalf("forget: %d %s", code, body)
	}
	if _, body := do("GET", "/api/llm-adaptations"); strings.TrimSpace(body) != "[]" {
		t.Errorf("after Forget: %s", body)
	}
	if code, _ := do("POST", "/api/llm-adaptations/forget?id=nonsense"); code != 400 {
		t.Errorf("a malformed id: %d, want 400", code)
	}
}
