package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

// providedWorld is a store holding the connections gohort provides, as
// startup installs them.
func providedWorld(t *testing.T) {
	t.Helper()
	saved, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = saved, savedAuth })
	ProvideConnectors(RootDB)
}

// tierSave posts a tier's form as the page does.
func tierSave(a *AdminApp, table, body string) (int, string) {
	w := httptest.NewRecorder()
	a.llmConfigRoutes(table, table == LLMTable)(w, httptest.NewRequest("POST", "/api/llm", strings.NewReader(body)))
	return w.Code, w.Body.String()
}

// A tier saves its provider, address and key: the key kept for that address
// and shown as saved, never sent back; an address changed with no new key
// leaves the key unsent until it is given again. A tier naming a connection
// is refused, and one still stored that way reads as not set, with a notice.
func TestATierSavesItsProviderAddressAndKey(t *testing.T) {
	providedWorld(t)
	a := &AdminApp{db: RootDB}
	read := func() map[string]any {
		out, err := a.handleLLMConfig(LeadLLMTable, false)(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/lead-llm", nil)})
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}
	if code, body := tierSave(a, LeadLLMTable, `{"provider":"openai","endpoint":"https://api.groq.example/openai/v1","model":"llama-x","api_key":"gsk-0123456789abcdWXYZ"}`); code >= 300 {
		t.Fatalf("saving: %d %s", code, body)
	}
	got := read()
	if got["provider"] != "openai" || got["endpoint"] != "https://api.groq.example/openai/v1" || got["api_key"] != "" ||
		!strings.HasPrefix(got["_kept_key"].(string), "ends in …WXYZ, given ") {
		t.Errorf("read back: %v", got)
	}
	cfg := LLMProviderConfig{Provider: "openai", Endpoint: "https://api.groq.example/openai/v1"}
	if err := cfg.LoadKey(RootDB, LeadLLMTable); err != nil || cfg.APIKey != "gsk-0123456789abcdWXYZ" {
		t.Errorf("the key kept: %q %v", cfg.APIKey, err)
	}

	if code, body := tierSave(a, LeadLLMTable, `{"provider":"openai","endpoint":"https://openrouter.example/api/v1","model":"llama-x"}`); code >= 300 {
		t.Fatalf("moving: %d %s", code, body)
	}
	if kept := read()["_kept_key"].(string); !strings.Contains(kept, "not sent while the tier goes elsewhere") {
		t.Errorf("moved with no new key: %q", kept)
	}

	if code, body := tierSave(a, LeadLLMTable, `{"provider":"llm:anthropic","model":"claude-x"}`); code != http.StatusBadRequest || !strings.Contains(body, "no longer runs on a connection") {
		t.Errorf("naming a connection: %d %s", code, body)
	}
	RootDB.Set(LeadLLMTable, "provider", "llm:anthropic")
	if got := read(); got["provider"] != "" || !strings.Contains(fmt.Sprint(got["_stale"]), "anthropic") {
		t.Errorf("still naming a connection: provider %q, notice %q", got["provider"], got["_stale"])
	}
}
