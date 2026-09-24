package admin

// Each tier has its own effort settings, saved and read back like the
// thinking budget beside them. A key the form writes that the loader never
// reads is a setting that does nothing.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestLLMEffortSettingsSaveAndLoadPerTier(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	post := func(table string, worker bool, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		a.handleLLMConfig(w, httptest.NewRequest("POST", "/api/llm", strings.NewReader(body)), table, worker)
		if w.Code != 204 {
			t.Fatalf("%s save: %d %s", table, w.Code, w.Body.String())
		}
	}
	get := func(table string, worker bool) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		a.handleLLMConfig(w, httptest.NewRequest("GET", "/api/llm", nil), table, worker)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	post(LLMTable, true, `{"provider":"llama.cpp","default_effort":"medium","max_effort":"High"}`)
	post(LeadLLMTable, false, `{"provider":"bedrock","default_effort":"off","max_effort":"bogus"}`)

	w := get(LLMTable, true)
	if w["default_effort"] != "medium" || w["max_effort"] != "high" {
		t.Errorf("worker read back default=%v max=%v", w["default_effort"], w["max_effort"])
	}
	l := get(LeadLLMTable, false)
	if l["default_effort"] != "off" {
		t.Errorf("lead default = %v, want off", l["default_effort"])
	}
	// Not a level: stored as "not set" rather than passed to a provider.
	if l["max_effort"] != "" {
		t.Errorf("lead max = %v, want an invalid value stored as unset", l["max_effort"])
	}
	// The keys the loader reads (config.go) are the keys saved here.
	var def string
	a.db.Get(LLMTable, "default_effort", &def)
	if def != "medium" {
		t.Errorf("stored under a different key: %q", def)
	}
}

func TestLLMFormsOfferEffortOnBothTiers(t *testing.T) {
	for _, s := range (&AdminApp{}).llmSections()[:2] {
		panel, ok := s.Body.(ui.FormPanel)
		if !ok {
			t.Fatalf("%s: body is %T", s.Title, s.Body)
		}
		seen := map[string]bool{}
		for _, f := range panel.Fields {
			seen[f.Field] = true
		}
		if !seen["default_effort"] || !seen["max_effort"] {
			t.Errorf("%s form is missing an effort setting", s.Title)
		}
	}
}
