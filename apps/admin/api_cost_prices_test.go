package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/costledger"
	"github.com/cmcoffee/gohort/core/service"
)

// Cost by source lists every connector job and API credential, priced or not,
// with its spend and its price: a connector's blank box stands for its
// template's price, shown greyed, and a price typed in its box is kept as the
// connector's Price; a credential's is its cost per call. A source priced
// where it is made (a peer's share, a search engine gohort runs itself) is
// listed read-only, and a save on a connector's Settings reloads the list.
func TestCostBySourceListsAndPricesEachSource(t *testing.T) {
	f := newCredsFixture(t)
	ledger := &DBase{Store: RootDB.(*DBase).Store}
	costledger.SetCostLedgerDB(ledger)
	t.Cleanup(func() { costledger.SetCostLedgerDB(nil) })
	a := &AdminApp{db: RootDB}
	sub := http.NewServeMux()
	a.registerCostSourceRoutes(sub)
	finder := func(cost string) json.RawMessage {
		return json.RawMessage(`{"provider":{"transport":"http","base_url":"https://find.example.test","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"t","url":"u"}}}` + cost + `}]}`)
	}
	if err := SaveConnector(RootDB, Connector{Name: "priced", Kind: service.Kind, Spec: finder(`,"cost":{"per_call":0.005}`)}); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnector(RootDB, Connector{Name: "free", Kind: service.Kind, Spec: finder("")}); err != nil {
		t.Fatal(err)
	}
	f.ok(t, "POST", "/api/secure-api", `{"name":"zz_metered","type":"bearer","base_url":"https://api.example.test","secret":"s-1"}`)
	RecordExternalCost("search:peer", "Web search (a peer)", 0.01)

	list := func() map[string]costSourceRow {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/"+costBySourceSource, nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: f.token})
		sub.ServeHTTP(w, r)
		var out struct {
			Items []costSourceRow `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		rows := map[string]costSourceRow{}
		for _, r := range out.Items {
			rows[r.SourceID] = r
		}
		return rows
	}
	price := func(id, v string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/cost-by-source/price", strings.NewReader(`{"source_id":"`+id+`","price":`+v+`}`))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: f.token})
		sub.ServeHTTP(w, r)
		return w.Code
	}

	rows := list()
	if r := rows["service:priced/web.search"]; !r.Editable || r.Price != nil || r.Default != "0.005" || r.Per != "per call" || r.From != "its template's" {
		t.Errorf("a priced connector: %+v", r)
	}
	if r := rows["service:free/web.search"]; !r.Editable || r.Price != nil || r.Default != "" {
		t.Errorf("an unpriced connector: %+v", r)
	}
	if r := rows["cred:zz_metered"]; !r.Editable || r.Price != nil {
		t.Errorf("an API credential: %+v", r)
	}
	if r := rows["search:peer"]; r.Editable || r.Calls != 1 || r.Cost != 0.01 {
		t.Errorf("a source priced where it is made: %+v", r)
	}

	if code := price("service:priced/web.search", "0.02"); code >= 300 {
		t.Fatalf("pricing a connector job: %d", code)
	}
	c, _ := GetConnector(RootDB, "priced")
	if v := ConnectionConfig(RootDB, c)["web.search/price"]; v != "0.02" {
		t.Errorf("kept as the connector's Price: %q", v)
	}
	if r := list()["service:priced/web.search"]; r.Price == nil || *r.Price != 0.02 || r.From != "set here" {
		t.Errorf("after pricing it: %+v", r)
	}
	if code := price("service:priced/web.search", "null"); code >= 300 {
		t.Fatalf("emptying the box: %d", code)
	}
	if r := list()["service:priced/web.search"]; r.Price != nil || r.From != "its template's" {
		t.Errorf("emptied, it goes back to its template's: %+v", r)
	}
	if code := price("cred:zz_metered", "0.0035"); code >= 300 {
		t.Fatalf("pricing a credential: %d", code)
	}
	if cred, _ := Secure().Load("zz_metered"); cred.CostPerCall != 0.0035 {
		t.Errorf("the credential's cost per call: %v", cred.CostPerCall)
	}
	for id, v := range map[string]string{"search:peer": "0.1", "service:priced/web.search": "-1", "service:nope/web.search": "1"} {
		if code := price(id, v); code != http.StatusBadRequest {
			t.Errorf("pricing %s at %s: %d", id, v, code)
		}
	}
	if !slices.Contains(connectorListSources, costBySourceSource) {
		t.Errorf("a save on a connector's Settings does not reload Cost by source: %v", connectorListSources)
	}
}
