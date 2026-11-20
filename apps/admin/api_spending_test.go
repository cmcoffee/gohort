package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/costledger"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Spending by person lists every account with this month's spending and the
// limit that holds for them and who set it; the default is the
// administrator's for everyone, an override replaces it for one person, and
// a person's own lower limit is shown when it is what holds.
func TestSpendingByPersonListsEveryoneAndTheirLimit(t *testing.T) {
	prevAuth, prevRoot := AuthDB, RootDB
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "alice", "pw", false)
	AuthSetUser(db, "bob", "pw", false)
	AuthDB = func() Database { return db }
	RootDB = db
	costledger.SetCostLedgerDB(db)
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot; costledger.SetCostLedgerDB(nil) })
	a := &AdminApp{db: db}
	c := &jsonapi.Call{User: "root", R: httptest.NewRequest("POST", "/admin/api/spending/default", nil)}
	costledger.Charge(costledger.WithSpender(context.Background(), costledger.Spender{Person: "alice"}), "model:lead", 4)

	people := func() string { v, _ := a.handleSpendingPeople(c); b, _ := json.Marshal(v); return string(b) }
	if s := people(); !strings.Contains(s, `{"person":"alice","spent":"$4.00","limit":"none","override":null,"default":""}`) || !strings.Contains(s, `"person":"bob","spent":"$0.00"`) || strings.Index(s, "alice") > strings.Index(s, "bob") {
		t.Errorf("no limits, the most spent first: %s", s)
	}
	if _, err := a.handleSpendingDefaultSave(c, struct {
		DefaultCap float64 `json:"default_cap"`
	}{25}); err != nil {
		t.Fatal(err)
	}
	if v, _ := a.handleSpendingDefault(c); v.(map[string]float64)["default_cap"] != 25 {
		t.Errorf("the default reads back as %v", v)
	}
	if s := people(); !strings.Contains(s, `"limit":"$25.00 (default)"`) || !strings.Contains(s, `"default":"25.00"`) {
		t.Errorf("the default for everyone: %s", s)
	}
	forty := 40.0
	if _, err := a.handleSpendingOverride(c, struct {
		Person   string   `json:"person"`
		Override *float64 `json:"override"`
	}{"bob", &forty}); err != nil {
		t.Fatal(err)
	}
	costledger.SetOwnCap("alice", 10)
	s := people()
	if !strings.Contains(s, `"person":"bob","spent":"$0.00","limit":"$40.00 (set for them)","override":40`) || !strings.Contains(s, `"limit":"$10.00 (their own)"`) {
		t.Errorf("an override and a person's own limit: %s", s)
	}
	if _, err := a.handleSpendingOverride(c, struct {
		Person   string   `json:"person"`
		Override *float64 `json:"override"`
	}{"bob", nil}); err != nil || costledger.PersonOverride("bob") != 0 {
		t.Errorf("clearing an override: %v", err)
	}
	neg := -1.0
	if _, err := a.handleSpendingOverride(c, struct {
		Person   string   `json:"person"`
		Override *float64 `json:"override"`
	}{"bob", &neg}); err == nil {
		t.Error("a limit below 0 was saved")
	}
}
