package admin

// Overview > Cost by source is every metered source's spend and where its
// price is set: each connector's job (its Price, blank using its template's)
// and each API credential (its cost per call), editable on the row, and any
// other source the ledger recorded, read-only. The price itself is kept by
// the source (service.SetPrice, SecureAPI.SetCostPerCall), not here.

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
)

func (a *AdminApp) registerCostSourceRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/cost-by-source", jsonapi.Get(a.db, a.handleCostBySource, a.gate()))
	sub.HandleFunc("/api/cost-by-source/price", jsonapi.Post(a.db, a.handleCostSourcePrice, a.gate()))
}

// costSourceRow is one source on Cost by source: what it spent, and its
// price where the row can change it.
type costSourceRow struct {
	SourceID string  `json:"source_id"`
	Label    string  `json:"label"`
	Calls    int64   `json:"calls"`
	Cost     float64 `json:"cost"`
	// Price is the price set on the source, absent while it uses its
	// template's (Default, shown greyed in the box). Per is its unit.
	Price    *float64 `json:"price,omitempty"`
	Default  string   `json:"default,omitempty"`
	Per      string   `json:"per,omitempty"`
	From     string   `json:"from,omitempty"`
	Editable bool     `json:"editable,omitempty"`
}

// handleCostBySource is every connector job and API credential, priced or
// not, with its spend over the ?days= window, then whatever else the ledger
// recorded (a peer's share, a search engine gohort runs itself); the
// costliest first.
func (a *AdminApp) handleCostBySource(c *jsonapi.Call) (any, error) {
	spent := map[string]CostSourceSummary{}
	for _, s := range CostBySource(costDays(c)) {
		spent[s.SourceID] = s
	}
	rows := []costSourceRow{}
	add := func(r costSourceRow) {
		if s, ok := spent[r.SourceID]; ok {
			r.Calls, r.Cost = s.Calls, s.Cost
			delete(spent, r.SourceID)
		}
		rows = append(rows, r)
	}
	for _, conn := range ListConnectors(RootDB) {
		for _, p := range service.Prices(conn) {
			r := costSourceRow{SourceID: "service:" + conn.Name + "/" + p.Capability, Label: conn.Title() + " (" + p.Capability + ")",
				Per: "per " + p.Unit, Editable: true}
			if p.Set {
				v := p.Price
				r.Price, r.From = &v, "set here"
			} else if p.Declared > 0 {
				r.From = "its template's"
			}
			if p.Declared > 0 {
				r.Default = strconv.FormatFloat(p.Declared, 'f', -1, 64)
			}
			if p.FromReply {
				r.From = "the provider's own figure, when its reply gives one"
			}
			add(r)
		}
	}
	for _, cred := range Secure().List() {
		r := costSourceRow{SourceID: "cred:" + cred.Name, Label: cred.Name + " (API credential)", Per: "per call", Editable: true}
		if cred.CostPerCall > 0 {
			v := cred.CostPerCall
			r.Price, r.From = &v, "set on the credential"
		}
		add(r)
	}
	for _, s := range spent {
		rows = append(rows, costSourceRow{SourceID: s.SourceID, Label: s.Label, Calls: s.Calls, Cost: s.Cost, From: "priced where it is made"})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Cost != rows[j].Cost {
			return rows[i].Cost > rows[j].Cost
		}
		return rows[i].Label < rows[j].Label
	})
	return map[string]any{"items": rows}, nil
}

// costPriceForm is what a row's price box sends: its source, and a price, or
// null to go back to its template's.
type costPriceForm struct {
	SourceID string   `json:"source_id"`
	Price    *float64 `json:"price"`
}

// handleCostSourcePrice sets a row's price where its source keeps it: a
// connector job's Price (null clears it), or a credential's cost per call
// (null is 0, untracked).
func (a *AdminApp) handleCostSourcePrice(c *jsonapi.Call, req costPriceForm) (any, error) {
	id := strings.TrimSpace(req.SourceID)
	if req.Price != nil && *req.Price < 0 {
		return nil, jsonapi.BadRequest("a price is not negative")
	}
	switch {
	case strings.HasPrefix(id, "service:"):
		name, capability, ok := strings.Cut(strings.TrimPrefix(id, "service:"), "/")
		conn, found := GetConnector(RootDB, name)
		if !ok || !found {
			return nil, jsonapi.BadRequest("no connector job " + id)
		}
		if err := service.SetPrice(conn, capability, req.Price); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	case strings.HasPrefix(id, "cred:"):
		v := 0.0
		if req.Price != nil {
			v = *req.Price
		}
		if err := Secure().SetCostPerCall(strings.TrimPrefix(id, "cred:"), v); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	default:
		return nil, jsonapi.BadRequest(id + " is priced where it is made, not here")
	}
	price := "its template's"
	if req.Price != nil {
		price = "$" + strconv.FormatFloat(*req.Price, 'f', -1, 64)
	}
	adminChange(c.R, activity.Settings, "priced %s at %s", id, price)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// costBySourceTable is Cost by source: spend per source, and a box for the
// price of each one priced here.
func costBySourceTable() ui.Table {
	return ui.Table{
		Source:    costBySourceSource,
		RowKey:    "source_id",
		EmptyText: "No connector or API credential yet, and no metered call recorded.",
		Columns: []ui.Col{
			{Field: "label", Label: "Source", Flex: 2},
			{Field: "calls", Label: "Calls", Format: "thousands", Flex: 1},
			{Field: "cost", Label: "Cost ($)", Flex: 1},
			{Field: "per", Label: "", Mute: true},
			{Field: "from", Label: "", Mute: true, Flex: 2},
		},
		RowActions: []ui.RowAction{
			{Type: "number", Label: "Price ($)", Field: "price", Decimals: 6, PlaceholderField: "default",
				PostTo: "api/cost-by-source/price", OnlyIf: "editable", Width: "7rem"},
		},
	}
}
