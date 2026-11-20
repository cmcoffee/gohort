package account

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/costledger"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

func withSpending(t *testing.T) *Account {
	t.Helper()
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	costledger.SetCostLedgerDB(RootDB)
	t.Cleanup(func() { RootDB = prevRoot; costledger.SetCostLedgerDB(nil) })
	T := &Account{}
	T.DB = RootDB.(*DBase).Bucket("account")
	return T
}

func asJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// Spending says what this month cost, the limit that holds and who set it,
// split by agent, by key and by source; a limit of one's own lowers the
// administrator's and never raises it.
func TestSpendingSaysWhatTheMonthCostAndTheLimit(t *testing.T) {
	T := withSpending(t)
	c := &jsonapi.Call{User: "alice"}
	tok := MintAccountTokenScoped("alice", "Laptop", &TokenScope{Features: []string{"mcp"}})
	ctx := costledger.WithSpender(context.Background(), costledger.Spender{Person: "alice", Agent: "ag-1", Key: tok.ID})
	costledger.Charge(ctx, "model:lead", 3)
	costledger.Charge(costledger.WithSpender(context.Background(), costledger.Spender{Person: "alice"}), "cred:maps", 1)

	sum, _ := T.handleSpendingSummary(c)
	if s := asJSON(sum); !strings.Contains(s, `"spent":"$4.00"`) || !strings.Contains(s, `"limit":"none"`) {
		t.Errorf("no limit: %s", s)
	}
	costledger.SetDefaultCap(10)
	if s := asJSON(must(T.handleSpendingSummary(c))); !strings.Contains(s, "$10.00 (set by your administrator), $6.00 left") {
		t.Errorf("the administrator's limit: %s", s)
	}
	if _, err := T.handleSpendingLimitSave(c, struct {
		OwnCap float64 `json:"own_cap"`
	}{5}); err != nil {
		t.Fatal(err)
	}
	if s := asJSON(must(T.handleSpendingSummary(c))); !strings.Contains(s, "$5.00 (your own), $1.00 left") {
		t.Errorf("her own lower limit: %s", s)
	}
	T.handleSpendingLimitSave(c, struct {
		OwnCap float64 `json:"own_cap"`
	}{50})
	if s := asJSON(must(T.handleSpendingSummary(c))); !strings.Contains(s, "$10.00 (set by your administrator)") {
		t.Errorf("her own higher limit did not leave the administrator's: %s", s)
	}
	if _, err := T.handleSpendingLimitSave(c, struct {
		OwnCap float64 `json:"own_cap"`
	}{-1}); err == nil {
		t.Error("a limit below 0 was saved")
	}

	b := asJSON(must(T.handleSpendingBreakdown(c)))
	for _, want := range []string{`"kind":"By agent","name":"ag-1","spent":"$3.00"`, `"kind":"By API key","name":"Laptop","spent":"$3.00"`, `"name":"Model use (lead)"`, `"name":"Credential maps","spent":"$1.00"`} {
		if !strings.Contains(b, want) {
			t.Errorf("the breakdown lacks %s: %s", want, b)
		}
	}
	if s := asJSON(must(T.handleSpendingBreakdown(&jsonapi.Call{User: "bob"}))); s != "[]" {
		t.Errorf("bob spent nothing: %s", s)
	}
}

// A key of one's own takes a limit of its own; someone else's key does not.
func TestAKeyTakesALimitOfItsOwn(t *testing.T) {
	T := withSpending(t)
	tok := MintAccountTokenScoped("alice", "Laptop", &TokenScope{Features: []string{"mcp"}})
	other := MintAccountTokenScoped("bob", "Bob's", &TokenScope{Features: []string{"mcp"}})
	c := &jsonapi.Call{User: "alice"}
	two := 2.0
	if _, err := T.handleSpendingKeySave(c, struct {
		ID  string   `json:"id"`
		Cap *float64 `json:"cap"`
	}{tok.ID, &two}); err != nil {
		t.Fatal(err)
	}
	if costledger.KeyCap(tok.ID) != 2 {
		t.Error("the key's limit was not kept")
	}
	if s := asJSON(must(T.handleSpendingKeys(c))); !strings.Contains(s, `"name":"Laptop","spent":"$0.00","cap":2`) || strings.Contains(s, "Bob's") {
		t.Errorf("alice's keys: %s", s)
	}
	if _, err := T.handleSpendingKeySave(c, struct {
		ID  string   `json:"id"`
		Cap *float64 `json:"cap"`
	}{other.ID, &two}); err == nil || costledger.KeyCap(other.ID) != 0 {
		t.Error("alice set a limit on bob's key")
	}
	if _, err := T.handleSpendingKeySave(c, struct {
		ID  string   `json:"id"`
		Cap *float64 `json:"cap"`
	}{tok.ID, nil}); err != nil || costledger.KeyCap(tok.ID) != 0 {
		t.Errorf("clearing the key's limit: %v", err)
	}
}

func must(v any, err error) any {
	if err != nil {
		panic(err)
	}
	return v
}
