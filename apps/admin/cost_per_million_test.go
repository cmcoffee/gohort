package admin

// Prices are typed per million tokens, the way providers publish them, and
// stored per thousand. The form saves the WHOLE record it loaded, stale
// per-thousand fields included, so a new per-million price has to win.

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestCostRatesPerMillion(t *testing.T) {
	prev := GetCostRates()
	t.Cleanup(func() { SetCostRates(prev) })
	SetCostRates(CostRates{LeadInputPer1K: 0.0015, LeadOutputPer1K: 0.009})

	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerCostRoutes(mux)
	call := func(method, body string) map[string]any {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/cost-rates", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", method, w.Code, w.Body.String())
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	got := call(http.MethodGet, "")
	if got["lead_input_per_1m"] != 1.5 || got["lead_output_per_1m"] != 9.0 {
		t.Fatalf("GET per million = %v / %v, want 1.5 / 9", got["lead_input_per_1m"], got["lead_output_per_1m"])
	}

	// What the form does: the loaded record back, with one price changed.
	got["lead_output_per_1m"] = 10.0
	body, _ := json.Marshal(got)
	call(http.MethodPut, string(body))

	r := GetCostRates()
	if math.Abs(r.LeadOutputPer1K-0.010) > 1e-12 {
		t.Errorf("stored lead output = %g per 1K, want 0.010 (the stale per-1K field must not win)", r.LeadOutputPer1K)
	}
	if math.Abs(r.LeadInputPer1K-0.0015) > 1e-12 {
		t.Errorf("stored lead input = %g per 1K, want 0.0015 unchanged", r.LeadInputPer1K)
	}
	if got = call(http.MethodGet, ""); got["lead_output_per_1m"] != 10.0 {
		t.Errorf("GET after save = %v, want 10", got["lead_output_per_1m"])
	}
}

func TestCostPageTypesPerMillion(t *testing.T) {
	src, _ := json.Marshal((&AdminApp{}).costSections())
	for _, f := range []string{"worker_input_per_1m", "worker_output_per_1m", "lead_input_per_1m", "lead_output_per_1m"} {
		if !strings.Contains(string(src), `"`+f+`"`) {
			t.Errorf("cost page has no %s field", f)
		}
	}
	if strings.Contains(string(src), "_per_1k") || strings.Contains(string(src), "/1K tokens") {
		t.Error("cost page still shows per-thousand prices")
	}
}
