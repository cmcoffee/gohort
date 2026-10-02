package admin

// An app's pane on the Apps tab shows the routing it has claimed, as a filter
// over the LLM Routing table: same rows, same keys, same POST, and each view
// tells the other when a row changes.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestRoutingFiltersToAnAppsClaimedStages(t *testing.T) {
	RegisterRouteStage(RouteStage{Key: "app.panetest.a", Label: "A", App: "/panetest"})
	RegisterRouteStage(RouteStage{Key: "panetest.unclaimed", Label: "U"})

	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	sub := http.NewServeMux()
	a.registerLLMRoutes(sub)

	get := func(url string) []map[string]any {
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		var rows []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("%s: %v (%s)", url, err, w.Body.String())
		}
		return rows
	}
	rows := get("/api/routing?app=/panetest")
	if len(rows) != 1 || rows[0]["key"] != "app.panetest.a" {
		t.Errorf("app view = %v, want only the claimed stage", rows)
	}
	// The unfiltered table still lists everything, the claimed stage included:
	// the app view is a second presentation, not a move.
	var sawClaimed, sawUnclaimed bool
	for _, r := range get("/api/routing") {
		switch r["key"] {
		case "app.panetest.a":
			sawClaimed = true
		case "panetest.unclaimed":
			sawUnclaimed = true
		}
	}
	if !sawClaimed || !sawUnclaimed {
		t.Errorf("full table lost a row: claimed=%v unclaimed=%v", sawClaimed, sawUnclaimed)
	}
}

func TestAppPaneCarriesItsRoutingTable(t *testing.T) {
	RegisterRouteStage(RouteStage{Key: "app.panebody.a", Label: "A", App: "/panebody"})

	st, ok := appPaneBody("/panebody").(ui.Stack)
	if !ok || len(st.Children) != 2 {
		t.Fatalf("an app with claimed routing should get summary + table, got %#v", appPaneBody("/panebody"))
	}
	tbl, ok := st.Children[1].(ui.Table)
	if !ok {
		t.Fatalf("second child = %T, want ui.Table", st.Children[1])
	}
	if tbl.Source != routingSourceForApp("/panebody") {
		t.Errorf("source = %q", tbl.Source)
	}
	for _, act := range tbl.RowActions {
		// Writes go where the LLMs tab writes, or the two views hold two values.
		if act.PostTo != "api/routing" {
			t.Errorf("%s posts to %q, want api/routing", act.Type, act.PostTo)
		}
		if len(act.Invalidate) != 1 || act.Invalidate[0] != "api/routing" {
			t.Errorf("%s invalidates %v, want the LLMs tab's table", act.Type, act.Invalidate)
		}
	}

	// And an app that claims nothing gets the summary alone: no empty table
	// implying there is something to set.
	if _, ok := appPaneBody("/claims-nothing").(ui.DisplayPanel); !ok {
		t.Errorf("unclaimed pane = %T, want the bare summary", appPaneBody("/claims-nothing"))
	}
}

// The LLMs tab's table must tell every app pane that renders one of its rows,
// or a tier changed there is contradicted on the Apps tab until a reload.
func TestFullRoutingTableTellsTheAppPanes(t *testing.T) {
	RegisterApp(fakeWebApp{path: "/paneinvalidate"})
	RegisterRouteStage(RouteStage{Key: "app.paneinvalidate", Label: "P", App: "/paneinvalidate"})

	want := routingSourceForApp("/paneinvalidate")
	for _, act := range routingTable("", appRoutingSources()).RowActions {
		found := false
		for _, s := range act.Invalidate {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s on the full table does not invalidate %q: %v", act.Type, want, act.Invalidate)
		}
	}
}
