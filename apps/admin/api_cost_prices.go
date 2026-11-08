package admin

// Overview > Prices lists every connector's price, one row per job it does,
// with where the price comes from and the row's Settings, where it is set
// (service.Prices). The price itself is kept on the connector, not here.

import (
	"net/http"
	"strconv"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
)

// connectorPricesSource is the list's address, named once because a save on
// a connector's Settings reloads it (connectorListSources).
const connectorPricesSource = "api/connector-prices"

func (a *AdminApp) registerConnectorPricesRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/"+connectorPricesSource, jsonapi.Get(a.db, a.handleConnectorPrices, a.gate()))
}

// connectorPriceRow is one job of one connector on the Prices list.
type connectorPriceRow struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Job         string `json:"job"`
	Price       string `json:"price"`
	Source      string `json:"source"`
	HasSettings bool   `json:"has_settings"`
}

func (a *AdminApp) handleConnectorPrices(c *jsonapi.Call) (any, error) {
	rows := []connectorPriceRow{}
	for _, conn := range ListConnectors(RootDB) {
		for _, p := range service.Prices(conn) {
			row := connectorPriceRow{Key: conn.Name + "/" + p.Capability, Name: conn.Name, Title: conn.Title(),
				Job: p.Capability, Price: "not priced", HasSettings: true}
			if p.Price > 0 {
				row.Price = "$" + strconv.FormatFloat(p.Price, 'f', -1, 64) + " per " + p.Unit
			}
			switch {
			case p.FromReply:
				row.Source = "reported by the provider in each reply; this price when it does not"
			case p.Set:
				row.Source = "set on its row"
			case p.Price > 0:
				row.Source = "its template's"
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// connectorPricesTable is the Prices list: each connector's price per job,
// changed under the row's Settings.
func connectorPricesTable() ui.Table {
	return ui.Table{
		Source: connectorPricesSource,
		RowKey: "key",
		Columns: []ui.Col{
			{Field: "title", Label: "Connector", Flex: 2},
			{Field: "job", Label: "Job", Mute: true},
			{Field: "price", Label: "Price"},
			{Field: "source", Label: "From", Mute: true, Flex: 2},
		},
		RowActions: []ui.RowAction{connectorSettingsAction()},
		EmptyText:  "No connector yet.",
	}
}
