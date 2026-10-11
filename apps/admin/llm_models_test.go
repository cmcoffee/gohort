package admin

// The Model field's "List models" button posts the form as it stands and
// gets back what the provider offers. These pin the two things a person sees:
// the list, with the tier's own stored key sent when the box is blank, and a
// refusal read back as the provider's reason rather than a dead button.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	a.db.Set(LeadLLMTable, "api_key", "stored-lead-key")
	a.db.Set(LLMTable, "api_key", "worker-key")

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
