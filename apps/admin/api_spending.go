package admin

// Overview > Spending by person: what each person spent this month, from the
// per-person ledger (core/costledger), and their monthly limit. The default
// limit applies to everyone; an override on a person's row replaces it for
// them. A person may set a lower one of their own (Account, Spending), which
// the Limit column shows when it is what holds. Unset means no limit.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/costledger"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

func (a *AdminApp) registerSpendingRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/spending/default", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleSpendingDefault, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleSpendingDefaultSave, a.gate()),
	}.Serve)
	sub.HandleFunc("/api/spending/people", jsonapi.Get(a.db, a.handleSpendingPeople, a.gate()))
	sub.HandleFunc("/api/spending/override", jsonapi.Post(a.db, a.handleSpendingOverride, a.gate()))
}

// spendingSection is the Cost tab's per-person spending and limits.
func spendingSection() ui.Section {
	return ui.Section{
		Title:    "Spending by person",
		Subtitle: "What each person spent this month, and the most they may spend in a month.",
		Detail: "Spending is what is priced: model use at the rates under Prices below, and each call through a priced credential or connector, charged to the person it was done for (the signed-in person, or the owner of the key that asked). A local model priced at 0 costs nothing.\n\n" +
			"Once a person reaches their limit, work that costs money stops until the month turns over or the limit is raised: a new turn is refused, a turn under way finishes on the worker model, and a priced call is not sent. At 80% they are told once. A person can set a lower limit of their own, and a limit per API key, on their Account page. Blank or 0 is no limit.",
		Body: ui.Stack{Children: []ui.Component{
			ui.FormPanel{
				Source: "api/spending/default",
				Fields: []ui.FormField{{Field: "default_cap", Label: "Default monthly limit ($)", Type: "number", Decimals: 2, Min: 0,
					Help: "For everyone without a limit of their own below. Blank or 0: no limit."}},
			},
			ui.Table{
				Source:    "api/spending/people",
				RowKey:    "person",
				EmptyText: "Nobody has an account yet.",
				Columns: []ui.Col{
					{Field: "person", Label: "Person", Flex: 2},
					{Field: "spent", Label: "This month"},
					{Field: "limit", Label: "Limit", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{{Type: "number", Label: "Limit ($)", Field: "override", Decimals: 2,
					PlaceholderField: "default", PostTo: "api/spending/override"}},
			},
		}},
	}
}

func (a *AdminApp) handleSpendingDefault(c *jsonapi.Call) (any, error) {
	return map[string]float64{"default_cap": costledger.DefaultCap()}, nil
}

func (a *AdminApp) handleSpendingDefaultSave(c *jsonapi.Call, in struct {
	DefaultCap float64 `json:"default_cap"`
}) (any, error) {
	if in.DefaultCap < 0 {
		return nil, jsonapi.BadRequest("a limit cannot be below 0")
	}
	costledger.SetDefaultCap(in.DefaultCap)
	RecordActivity(c.R, activity.Settings, c.User, "set the default monthly spending limit to "+spendDollars(in.DefaultCap))
	return map[string]float64{"default_cap": in.DefaultCap}, nil
}

// spendingRow is one person on Spending by person.
type spendingRow struct {
	Person   string   `json:"person"`
	Spent    string   `json:"spent"`
	Limit    string   `json:"limit"`
	Override *float64 `json:"override"`
	Default  string   `json:"default"`
}

// handleSpendingPeople is every account, with this month's spending, the
// limit that holds for them and who set it, and the administrator's override.
func (a *AdminApp) handleSpendingPeople(c *jsonapi.Call) (any, error) {
	month := costledger.ThisMonth()
	spent := map[string]float64{}
	for _, s := range costledger.MonthSpends(month) {
		spent[s.Person] = s.Total
	}
	people := map[string]bool{}
	if AuthDB != nil && AuthDB() != nil {
		for _, u := range AuthListUsers(AuthDB()) {
			people[u.Username] = true
		}
	}
	for p := range spent {
		people[p] = true
	}
	def := costledger.DefaultCap()
	defText := ""
	if def > 0 {
		defText = strings.TrimPrefix(spendDollars(def), "$")
	}
	rows := []spendingRow{}
	for p := range people {
		row := spendingRow{Person: p, Spent: spendDollars(spent[p]), Default: defText, Limit: spendLimitText(p)}
		if o := costledger.PersonOverride(p); o > 0 {
			row.Override = &o
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if spent[rows[i].Person] != spent[rows[j].Person] {
			return spent[rows[i].Person] > spent[rows[j].Person]
		}
		return rows[i].Person < rows[j].Person
	})
	return rows, nil
}

func (a *AdminApp) handleSpendingOverride(c *jsonapi.Call, in struct {
	Person   string   `json:"person"`
	Override *float64 `json:"override"`
}) (any, error) {
	p := strings.TrimSpace(in.Person)
	if p == "" {
		return nil, jsonapi.BadRequest("person required")
	}
	v := 0.0
	if in.Override != nil {
		v = *in.Override
	}
	if v < 0 {
		return nil, jsonapi.BadRequest("a limit cannot be below 0")
	}
	costledger.SetPersonOverride(p, v)
	what := "the default"
	if v > 0 {
		what = spendDollars(v)
	}
	RecordActivity(c.R, activity.Settings, c.User, "set the monthly spending limit of "+p+" to "+what)
	return map[string]any{"person": p, "limit": spendLimitText(p)}, nil
}

// limitText is the limit that holds for person and who set it.
func spendLimitText(person string) string {
	held, own, admin := costledger.PersonCap(person), costledger.OwnCap(person), costledger.AdminCap(person)
	switch {
	case held <= 0:
		return "none"
	case own > 0 && held == own && (admin <= 0 || own < admin):
		return spendDollars(held) + " (their own)"
	case costledger.PersonOverride(person) > 0:
		return spendDollars(held) + " (set for them)"
	}
	return spendDollars(held) + " (default)"
}

// spendDollars is usd as dollars and cents.
func spendDollars(usd float64) string { return fmt.Sprintf("$%.2f", usd) }
