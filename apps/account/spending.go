package account

// Account > Spending: what this person spent this month (core/costledger),
// split by agent, by API key and by where it came from; the limit that holds
// for them; a lower one of their own; and a limit per API key, which keeps a
// key's outside client from spending the whole month's allowance.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/costledger"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/core/userprofile"
)

func (T *Account) spendingRoutes() {
	T.HandleFunc("/api/spending/summary", jsonapi.Get(T.DB, T.handleSpendingSummary))
	T.HandleFunc("/api/spending/limit", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handleSpendingLimit),
		"POST": jsonapi.Post(T.DB, T.handleSpendingLimitSave),
	}.Serve)
	T.HandleFunc("/api/spending/breakdown", jsonapi.Get(T.DB, T.handleSpendingBreakdown))
	T.HandleFunc("/api/spending/keys", unlessTaken(userprofile.RemoveAPIKeys, jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handleSpendingKeys),
		"POST": jsonapi.Post(T.DB, T.handleSpendingKeySave),
	}.Serve))
}

// spendingSection is the Account page's Spending: with what each API key
// spent, unless the person's user profile takes their keys away.
func spendingSection(keys bool) ui.Section {
	parts := []ui.Component{
		ui.DisplayPanel{
			Source: "api/spending/summary",
			Pairs: []ui.DisplayPair{
				{Label: "Month", Field: "month"},
				{Label: "Spent", Field: "spent"},
				{Label: "Limit", Field: "limit"},
			},
		},
		ui.FormPanel{
			Source:     "api/spending/limit",
			Invalidate: []string{"api/spending/summary"},
			Fields: []ui.FormField{{Field: "own_cap", Label: "My monthly limit ($)", Type: "number", Decimals: 2, Min: 0,
				Help: "A limit of your own, to stop before your administrator's would. Above theirs, theirs holds. Blank or 0: none of your own."}},
		},
		ui.Table{
			Source:    "api/spending/breakdown",
			RowKey:    "id",
			GroupBy:   "kind",
			EmptyText: "Nothing priced has run for you this month.",
			Columns: []ui.Col{
				{Field: "name", Label: "Spent on", Flex: 3},
				{Field: "spent", Label: "This month"},
			},
		},
	}
	if keys {
		parts = append(parts, ui.Table{
			Source:    "api/spending/keys",
			RowKey:    "id",
			EmptyText: "You have no API keys.",
			Columns: []ui.Col{
				{Field: "name", Label: "API key", Flex: 3},
				{Field: "spent", Label: "This month"},
			},
			RowActions: []ui.RowAction{{Type: "number", Label: "Limit ($)", Field: "cap", Decimals: 2, PostTo: "api/spending/keys"}},
		})
	}
	return ui.Section{
		Title:    "Spending",
		Subtitle: "What your work cost this month, and the most it may cost.",
		Detail: "Spending is what is priced: model use at the rates your administrator set, and calls through priced credentials and connectors, whether you asked, a schedule did, or a client with one of your API keys did. A local model priced at 0 costs nothing.\n\n" +
			"Once your limit is reached, work that costs money stops until the month turns over: a new turn is refused, a turn under way finishes on the worker model, and a priced call is not sent. At 80% you are told once. A key's own limit stops just that key.",
		Body: ui.Stack{Children: parts},
	}
}

// dollars is usd as dollars and cents.
func dollars(usd float64) string { return fmt.Sprintf("$%.2f", usd) }

func (T *Account) handleSpendingSummary(c *jsonapi.Call) (any, error) {
	s := costledger.Spent(costledger.ThisMonth(), c.User)
	held, own, admin := costledger.PersonCap(c.User), costledger.OwnCap(c.User), costledger.AdminCap(c.User)
	limit := "none"
	switch {
	case held > 0 && own > 0 && held == own && (admin <= 0 || own < admin):
		limit = dollars(held) + " (your own)"
	case held > 0:
		limit = dollars(held) + " (set by your administrator)"
	}
	if held > 0 {
		left := held - s.Total
		if left < 0 {
			left = 0
		}
		limit += ", " + dollars(left) + " left"
	}
	return map[string]string{
		"month": time.Now().Format("January 2006"),
		"spent": dollars(s.Total),
		"limit": limit,
	}, nil
}

func (T *Account) handleSpendingLimit(c *jsonapi.Call) (any, error) {
	return map[string]float64{"own_cap": costledger.OwnCap(c.User)}, nil
}

func (T *Account) handleSpendingLimitSave(c *jsonapi.Call, in struct {
	OwnCap float64 `json:"own_cap"`
}) (any, error) {
	if in.OwnCap < 0 {
		return nil, jsonapi.BadRequest("a limit cannot be below 0")
	}
	costledger.SetOwnCap(c.User, in.OwnCap)
	return map[string]float64{"own_cap": in.OwnCap}, nil
}

// spendingRow is one line of the breakdown.
type spendingRow struct {
	ID    string  `json:"id"`
	Kind  string  `json:"kind"`
	Name  string  `json:"name"`
	Spent string  `json:"spent"`
	usd   float64 // for the order
}

// handleSpendingBreakdown is this month's spending split three ways: by
// agent, by API key, and by where it came from. Each split sums to what was
// charged with one known; work run by no agent or asked by no key is not in
// those.
func (T *Account) handleSpendingBreakdown(c *jsonapi.Call) (any, error) {
	s := costledger.Spent(costledger.ThisMonth(), c.User)
	agents := map[string]string{}
	for _, o := range AgentNameOptions(c.User) {
		agents[o.Value] = o.Label
	}
	keys := map[string]string{}
	for _, t := range ListAccountTokens(c.User) {
		keys[t.ID] = t.Name
	}
	var rows []spendingRow
	add := func(kind, prefix string, m map[string]float64, name func(string) string) {
		var part []spendingRow
		for k, v := range m {
			part = append(part, spendingRow{ID: prefix + k, Kind: kind, Name: name(k), Spent: dollars(v), usd: v})
		}
		sort.Slice(part, func(i, j int) bool { return part[i].usd > part[j].usd })
		rows = append(rows, part...)
	}
	add("By agent", "agent:", s.Agents, func(id string) string {
		if n := agents[id]; n != "" {
			return n
		}
		return id
	})
	add("By API key", "key:", s.Keys, func(id string) string {
		if n := keys[id]; n != "" {
			return n
		}
		return "a key since revoked"
	})
	add("By source", "source:", s.Sources, sourceName)
	if rows == nil {
		rows = []spendingRow{}
	}
	return rows, nil
}

// sourceName says where a cost came from, as the ledger names it.
func sourceName(source string) string {
	kind, name, _ := strings.Cut(source, ":")
	switch kind {
	case "model":
		return "Model use (" + name + ")"
	case "cred":
		return "Credential " + name
	case "service":
		return "Connector " + name
	case "hook":
		return "Source hook " + name
	}
	return source
}

// spendingKeyRow is one of the person's API keys with what it spent this
// month and its limit.
type spendingKeyRow struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Spent string   `json:"spent"`
	Cap   *float64 `json:"cap"`
}

func (T *Account) handleSpendingKeys(c *jsonapi.Call) (any, error) {
	s := costledger.Spent(costledger.ThisMonth(), c.User)
	rows := []spendingKeyRow{}
	for _, t := range ListAccountTokens(c.User) {
		row := spendingKeyRow{ID: t.ID, Name: t.Name, Spent: dollars(s.Keys[t.ID])}
		if v := costledger.KeyCap(t.ID); v > 0 {
			row.Cap = &v
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// handleSpendingKeySave sets the monthly limit of one of the person's own
// keys; blank or 0 is none.
func (T *Account) handleSpendingKeySave(c *jsonapi.Call, in struct {
	ID  string   `json:"id"`
	Cap *float64 `json:"cap"`
}) (any, error) {
	v := 0.0
	if in.Cap != nil {
		v = *in.Cap
	}
	if v < 0 {
		return nil, jsonapi.BadRequest("a limit cannot be below 0")
	}
	for _, t := range ListAccountTokens(c.User) {
		if t.ID == in.ID {
			costledger.SetKeyCap(t.ID, v)
			return map[string]any{"id": t.ID, "cap": v}, nil
		}
	}
	return nil, jsonapi.NotFound("no key of yours by that id")
}
