package admin

// Each tier says whether it reads images, saved and read back like the other
// tier settings, under the key the loader reads, and offered on both forms as
// "Reads images" (stored inverted, so an unset key reads images as before).

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestEachTierSaysWhetherItReadsImages(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	for _, c := range []struct {
		table  string
		worker bool
		body   string
		want   bool
	}{
		{LLMTable, true, `{"provider":"llama.cpp","no_images":false}`, false},
		{LeadLLMTable, false, `{"provider":"bedrock","no_images":true}`, true},
	} {
		w := httptest.NewRecorder()
		a.handleLLMConfig(w, httptest.NewRequest("POST", "/api/llm", strings.NewReader(c.body)), c.table, c.worker)
		if w.Code != 204 {
			t.Fatalf("%s save: %d %s", c.table, w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		a.handleLLMConfig(w, httptest.NewRequest("GET", "/api/llm", nil), c.table, c.worker)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		if out["no_images"] != c.want {
			t.Errorf("%s read back no_images=%v", c.table, out["no_images"])
		}
		var stored bool
		a.db.Get(c.table, "no_images", &stored)
		if stored != c.want {
			t.Errorf("%s stored under a different key", c.table)
		}
	}
	for _, s := range (&AdminApp{}).llmSections()[:2] {
		panel, ok := s.Body.(ui.FormPanel)
		if !ok {
			t.Fatalf("%s: body is %T", s.Title, s.Body)
		}
		found := false
		for _, f := range panel.Fields {
			if f.Field == "no_images" {
				found = f.Label == "Reads images" && f.Type == "toggle" && f.Invert
			}
		}
		if !found {
			t.Errorf("%s does not offer Reads images", s.Title)
		}
	}
}
