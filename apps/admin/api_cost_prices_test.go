package admin

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Overview > Prices lists each connector's price per job and where it comes
// from: its template's, set on its row, or none. A save on a row's Settings
// reloads it.
func TestPricesListEachConnectorsPricePerJob(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	a := &AdminApp{db: RootDB}
	finder := func(cost string) json.RawMessage {
		return json.RawMessage(`{"provider":{"transport":"http","base_url":"https://find.example.test","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"t","url":"u"}}}` + cost + `}]}`)
	}
	if err := SaveConnector(RootDB, Connector{Name: "priced", Kind: service.Kind, Spec: finder(`,"cost":{"per_call":0.005}`)}); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnector(RootDB, Connector{Name: "free", Kind: service.Kind, Spec: finder("")}); err != nil {
		t.Fatal(err)
	}
	list := func() map[string]connectorPriceRow {
		out, err := a.handleConnectorPrices(&jsonapi.Call{R: httptest.NewRequest("GET", "/"+connectorPricesSource, nil)})
		if err != nil {
			t.Fatal(err)
		}
		rows := map[string]connectorPriceRow{}
		for _, r := range out.([]connectorPriceRow) {
			rows[r.Key] = r
		}
		return rows
	}
	rows := list()
	if r := rows["priced/web.search"]; r.Price != "$0.005 per call" || r.Source != "its template's" || !r.HasSettings {
		t.Errorf("a priced connector: %+v", r)
	}
	if r := rows["free/web.search"]; r.Price != "not priced" || r.Source != "" {
		t.Errorf("an unpriced connector: %+v", r)
	}
	c, _ := GetConnector(RootDB, "priced")
	if err := service.SaveSettings(c, "", map[string]string{"web.search/price": "0.02"}, nil); err != nil {
		t.Fatal(err)
	}
	if r := list()["priced/web.search"]; r.Price != "$0.02 per call" || r.Source != "set on its row" {
		t.Errorf("after setting it on the row: %+v", r)
	}
	if !slices.Contains(connectorListSources, connectorPricesSource) {
		t.Errorf("a save on a row's Settings does not reload the list: %v", connectorListSources)
	}
}
