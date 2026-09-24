package admin

// Each routing-table control posts only its own field. A budget change must
// save, and a tier change must not reset the budget.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestRoutingRowControlsChangeOnlyTheirField(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerLLMRoutes(mux)
	post := func(body string) int {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/routing", strings.NewReader(body)))
		return w.Code
	}
	const key = "test.routing.partial"
	if c := post(`{"key":"` + key + `","value":"worker"}`); c/100 != 2 {
		t.Fatalf("tier: %d", c)
	}
	if c := post(`{"key":"` + key + `","think_budget":2048}`); c/100 != 2 {
		t.Fatalf("a budget-only save was refused: %d", c)
	}
	if c := post(`{"key":"` + key + `","value":"worker (thinking)"}`); c/100 != 2 {
		t.Fatalf("tier change: %d", c)
	}
	var val string
	var budget int
	a.db.Get(RoutingTable, key, &val)
	a.db.Get(RoutingTable, key+".think_budget", &budget)
	if val != "worker (thinking)" || budget != 2048 {
		t.Fatalf("got tier %q budget %d: a tier change reset the budget, or the budget never saved", val, budget)
	}
	if c := post(`{"key":"` + key + `","value":"bogus"}`); c != http.StatusBadRequest {
		t.Errorf("an unknown tier: %d", c)
	}
	if c := post(`{"key":"` + key + `"}`); c != http.StatusBadRequest {
		t.Errorf("nothing to change: %d", c)
	}
}
