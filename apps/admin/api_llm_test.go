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

// providedWorld is an empty store, as a fresh install starts.
func providedWorld(t *testing.T) {
	t.Helper()
	saved, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = saved, savedAuth })
}

// tierSave posts a tier's form as the page does.
func tierSave(a *AdminApp, table, body string) (int, string) {
	w := httptest.NewRecorder()
	a.llmConfigRoutes(table, table == LLMTable)(w, httptest.NewRequest("POST", "/api/llm", strings.NewReader(body)))
	return w.Code, w.Body.String()
}

// A tier saves its provider, address and key: the key kept for that address
// (the first key for an address goes into the Keystore, which the tier then
// names) and shown as saved, never sent back; an address changed with no new
// key leaves the key unsent until it is given again. A tier naming a connection
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
		!strings.HasPrefix(got["_kept_key"].(string), "from the Keystore: Lead model key (ends in ...WXYZ, given ") {
		t.Errorf("read back: %v", got)
	}
	cfg := LLMProviderConfig{Provider: "openai", Endpoint: "https://api.groq.example/openai/v1"}
	if err := cfg.LoadKey(RootDB, LeadLLMTable); err != nil || cfg.APIKey != "gsk-0123456789abcdWXYZ" {
		t.Errorf("the key kept: %q %v", cfg.APIKey, err)
	}

	if code, body := tierSave(a, LeadLLMTable, `{"provider":"openai","endpoint":"https://openrouter.example/api/v1","model":"llama-x"}`); code >= 300 {
		t.Fatalf("moving: %d %s", code, body)
	}
	if kept := read()["_kept_key"].(string); !strings.Contains(kept, "not sent, the Keystore key Lead model key is not given for https://openrouter.example/api/v1") {
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

// The Judge model takes a local server or a peer, and refuses a hosted
// model: the checks read everything the worker reads. Its list holds every
// registered check, each on until switched off, and a switch stays put.
func TestTheJudgeModelIsLocalAndListsItsChecks(t *testing.T) {
	providedWorld(t)
	a := &AdminApp{db: RootDB}
	if code, body := tierSave(a, JudgeLLMTable, `{"provider":"anthropic","model":"claude-x","api_key":"k"}`); code != http.StatusBadRequest || !strings.Contains(body, "locally or on a peer") {
		t.Errorf("a hosted Judge model: %d %s", code, body)
	}
	var provider string
	RootDB.Get(JudgeLLMTable, "provider", &provider)
	if provider != "" {
		t.Errorf("a refused Judge model was stored: %q", provider)
	}
	if code, body := tierSave(a, JudgeLLMTable, `{"provider":"vllm","model":"Qwen/Qwen3.5-4B","endpoint":"http://192.168.0.40:8000"}`); code >= 300 {
		t.Errorf("a local Judge model: %d %s", code, body)
	}
	for _, p := range judgeProviderOptions() {
		if p.Value == "anthropic" || p.Value == "gemini" || p.Value == "bedrock" {
			t.Errorf("the Judge model offers the hosted %s", p.Value)
		}
	}

	RegisterJudgeCall(JudgeCall{Key: "test.admin_check", Label: "A check"})
	rows := func() map[string]judgeCallRow {
		out, _ := a.handleJudgeCalls(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/judge-llm/calls", nil)})
		m := map[string]judgeCallRow{}
		for _, r := range out.([]judgeCallRow) {
			m[r.Key] = r
		}
		return m
	}
	if r, ok := rows()["test.admin_check"]; !ok || !r.On {
		t.Fatalf("a registered check, on until switched off: %+v", r)
	}
	if r := rows()["test.admin_check"]; r.Tested != "untested" {
		t.Errorf("a check with no test cases is not marked: %+v", r)
	}
	toggle := func(key string, on bool) error {
		_, err := a.handleJudgeCallToggle(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/judge-llm/calls/toggle?key="+key, nil)}, struct {
			On bool `json:"on"`
		}{on})
		return err
	}
	if err := toggle("test.admin_check", false); err != nil || rows()["test.admin_check"].On || JudgeCallOn("test.admin_check") {
		t.Errorf("switched off: %v, listed %+v", err, rows()["test.admin_check"])
	}
	if err := toggle("test.admin_check", true); err != nil || !JudgeCallOn("test.admin_check") {
		t.Errorf("switched back on: %v", err)
	}
	if err := toggle("no.such.check", false); err == nil {
		t.Error("a check that is not registered was switched")
	}
}
