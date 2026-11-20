package costledger

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is a Store in memory, values kept as JSON as a database would.
type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Get(table, key string, out interface{}) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[table+"\x00"+key]
	if !ok {
		return false
	}
	return json.Unmarshal(b, out) == nil
}

func (s *memStore) Set(table, key string, v interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(v)
	s.m[table+"\x00"+key] = b
}

func (s *memStore) Keys(table string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.m {
		if t, key, ok := strings.Cut(k, "\x00"); ok && t == table {
			out = append(out, key)
		}
	}
	return out
}

func withLedger(t *testing.T) {
	t.Helper()
	prev := ledgerDB()
	SetCostLedgerDB(&memStore{m: map[string][]byte{}})
	t.Cleanup(func() { SetCostLedgerDB(prev) })
}

// Who pays: the first person and key set stay (work someone caused stays
// theirs), the last agent set is the one doing the work.
func TestWhoPaysIsTheFirstPersonAndTheLastAgent(t *testing.T) {
	ctx := WithSpender(context.Background(), Spender{Person: "alice", Key: "k1"})
	ctx = WithSpender(ctx, Spender{Person: "bob", Agent: "writer", Key: "k2"})
	ctx = WithSpender(ctx, Spender{Agent: "helper"})
	if got := SpenderFrom(ctx); got != (Spender{Person: "alice", Agent: "helper", Key: "k1"}) {
		t.Errorf("who pays: %+v", got)
	}
	if got := SpenderFrom(context.Background()); got != (Spender{}) {
		t.Errorf("nobody set: %+v", got)
	}
	if got := SpenderFrom(WithSpender(context.Background(), Spender{Person: "  carol "})); got.Person != "carol" {
		t.Errorf("spaces kept: %q", got.Person)
	}
}

// A charge goes to the person paying, split by agent, key and source, in this
// month; work no person stands behind, and a zero cost, charge nothing.
func TestAChargeIsThePersonsSplitThreeWays(t *testing.T) {
	withLedger(t)
	ctx := WithSpender(context.Background(), Spender{Person: "alice", Agent: "writer", Key: "k1"})
	Charge(ctx, "model:lead", 0.5)
	Charge(ctx, "cred:maps", 0.25)
	Charge(WithSpender(context.Background(), Spender{Person: "alice"}), "model:lead", 1)
	Charge(context.Background(), "model:lead", 9)
	Charge(ctx, "model:worker", 0)

	s := Spent(ThisMonth(), "alice")
	if s.Total != 1.75 || s.Agents["writer"] != 0.75 || s.Keys["k1"] != 0.75 || s.Sources["model:lead"] != 1.5 || s.Sources["cred:maps"] != 0.25 {
		t.Errorf("alice's month: %+v", s)
	}
	if _, ok := s.Sources["model:worker"]; ok {
		t.Error("a zero cost was charged")
	}
	if all := MonthSpends(ThisMonth()); len(all) != 1 || all[0].Person != "alice" {
		t.Errorf("the month's spenders: %+v", all)
	}
	if got := Spent("1999-01", "alice"); got.Total != 0 {
		t.Errorf("another month: %+v", got)
	}
	if Month(time.Date(2026, 3, 31, 23, 0, 0, 0, time.Local)) != "2026-03" {
		t.Error("the month is read in the deployment's zone")
	}
}

// The limit that holds is the administrator's (an override, else the default),
// lowered by a person's own and never raised by it; none is set by default.
func TestTheLimitThatHolds(t *testing.T) {
	withLedger(t)
	if PersonCap("alice") != 0 {
		t.Error("a limit with nothing set")
	}
	SetOwnCap("alice", 30)
	if PersonCap("alice") != 30 {
		t.Error("a limit of her own alone")
	}
	SetDefaultCap(50)
	if PersonCap("alice") != 30 || PersonCap("bob") != 50 || AdminCap("bob") != 50 {
		t.Errorf("the default: alice %v bob %v", PersonCap("alice"), PersonCap("bob"))
	}
	SetOwnCap("alice", 80)
	if PersonCap("alice") != 50 {
		t.Error("her own limit raised the administrator's")
	}
	SetPersonOverride("alice", 100)
	if PersonCap("alice") != 80 || AdminCap("alice") != 100 || PersonOverride("alice") != 100 {
		t.Errorf("an override: %v", PersonCap("alice"))
	}
	SetPersonOverride("alice", 0)
	if AdminCap("alice") != 50 {
		t.Error("clearing the override does not go back to the default")
	}
	SetKeyCap("k1", -3)
	if KeyCap("k1") != 0 {
		t.Error("a negative limit")
	}
}

// Over the limit: the person's, or the key's, reached, or one that a known
// cost would cross. Work nobody pays for is never over.
func TestOverTheLimit(t *testing.T) {
	withLedger(t)
	ctx := WithSpender(context.Background(), Spender{Person: "alice", Key: "k1"})
	SetDefaultCap(10)
	Charge(ctx, "model:lead", 9)
	if why := OverCap(ctx, 0); why != "" {
		t.Errorf("under the limit: %s", why)
	}
	if why := OverCap(ctx, 2); !strings.Contains(why, "alice") || !strings.Contains(why, "$9.00 of $10.00") {
		t.Errorf("a cost that would cross it: %q", why)
	}
	if OverCap(ctx, 1) != "" {
		t.Error("a cost that reaches it exactly is refused")
	}
	Charge(ctx, "model:lead", 1)
	if OverCap(ctx, 0) == "" {
		t.Error("the limit reached")
	}
	if OverCap(context.Background(), 100) != "" {
		t.Error("work nobody pays for is over a limit")
	}

	SetDefaultCap(0)
	SetKeyCap("k1", 5)
	if why := OverCap(ctx, 0); !strings.Contains(why, "this key") {
		t.Errorf("the key's limit: %q", why)
	}
	if OverCap(WithSpender(context.Background(), Spender{Person: "alice", Key: "k2"}), 0) != "" {
		t.Error("another key of hers is held to k1's limit")
	}
}

// Crossing 80% of a person's limit tells them, once a month.
func TestEightyPercentIsToldOnce(t *testing.T) {
	withLedger(t)
	var mu sync.Mutex
	var told []string
	done := make(chan struct{}, 4)
	prev := OnWarn
	OnWarn = func(person string, spent, cap float64) {
		mu.Lock()
		told = append(told, person)
		mu.Unlock()
		done <- struct{}{}
	}
	t.Cleanup(func() { OnWarn = prev })
	SetDefaultCap(10)
	ctx := WithSpender(context.Background(), Spender{Person: "alice"})
	Charge(ctx, "model:lead", 7)
	Charge(ctx, "model:lead", 1.5)
	Charge(ctx, "model:lead", 1)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("not told at 80%")
	}
	// The limit raised after: crossing 80% of the new one tells nobody again
	// this month.
	SetDefaultCap(20)
	Charge(ctx, "model:lead", 7)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(told) != 1 || told[0] != "alice" {
		t.Errorf("told: %v", told)
	}
	if !Spent(ThisMonth(), "alice").Warned {
		t.Error("the month does not remember she was told")
	}
}
