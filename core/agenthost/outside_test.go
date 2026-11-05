package agenthost

import (
	"strings"
	"testing"
)

// An outside person's store is named by the key and the person, and the name
// can never be an account's: it holds ":", which no account name may.
func TestAnOutsidePersonsStoreIsNoAccountsStore(t *testing.T) {
	got := OutsideScope("key1", "cust-7")
	if got != "outside:key1:cust-7" {
		t.Fatalf("OutsideScope = %q", got)
	}
	if !IsOutsideScope(got) {
		t.Error("an outside person's store does not read as one")
	}
	for _, u := range []string{"alice", "phantom:991", "app:servitor:x", ""} {
		if IsOutsideScope(u) {
			t.Errorf("%q read as an outside person", u)
		}
	}
	if OutsideScope("", "cust-7") != "" || OutsideScope("key1", " ") != "" {
		t.Error("a store was named with no key or no person: an unnamed caller would share it with every other")
	}
}

// The id is refused, not cleaned, when it cannot name a store safely: cleaning
// could fold two different people into one store.
func TestAnUnsafePersonIdIsRefused(t *testing.T) {
	for _, ok := range []string{"cust-7", "Ann.Lee@example.test", "8f3e2a1c-77b0-4c1e-9d55-1a2b3c4d5e6f", "a|b+c_d"} {
		if err := CheckOutsidePerson(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " cust", "cust:7", "a/b", "a b", "ünï", strings.Repeat("x", MaxOutsidePersonLen+1)} {
		if CheckOutsidePerson(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
