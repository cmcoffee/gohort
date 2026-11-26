package appreach

import (
	"context"
	"strings"
	"testing"
)

// The first rules put on a context stay: work a run starts keeps the app's
// rules at the top. A choice nobody made is reads, and a sign-in is reached
// only for reads in a run for someone else, never once the app keeps it out;
// a reach is heard, a refusal is not.
func TestAnAppRunsRulesRideOnItsContext(t *testing.T) {
	if _, ok := From(context.Background()); ok {
		t.Fatal("rules on a bare context")
	}
	if _, ok := From(nil); ok {
		t.Fatal("rules on a nil context")
	}
	var used []string
	lend := map[string]string{}
	off := map[string]bool{}
	r := Rules{Person: "bob",
		Lend: func(n string) string { return lend[n] },
		Off:  func(n string) bool { return off[n] },
		Used: func(n string) { used = append(used, n) }}
	ctx := With(context.Background(), r)
	ctx = With(ctx, Rules{Person: "carol"})
	got, ok := From(ctx)
	if !ok || got.Person != "bob" {
		t.Fatalf("the first rules did not stay: %+v %v", got, ok)
	}
	if MCPKey("atl") != "mcp:atl" || got.Choice("wiki") != "read" || (Rules{}).Choice("wiki") != "read" || (Rules{}).IsOff("wiki") {
		t.Error("the defaults")
	}
	if err := got.MCPRefusal("atl"); err != nil || len(used) != 1 || used[0] != "mcp:atl" {
		t.Errorf("a sign-in lent for reads: %v %v", err, used)
	}
	for _, c := range []string{"withhold", "own", "write"} {
		lend["mcp:atl"] = c
		if err := got.MCPRefusal("atl"); err == nil || !strings.Contains(err.Error(), "someone else uses this app") {
			t.Errorf("a sign-in chosen %s: %v", c, err)
		}
	}
	owner := got
	owner.Person = ""
	if err := owner.MCPRefusal("atl"); err != nil {
		t.Errorf("the owner's own run was held to the lend: %v", err)
	}
	off["mcp:atl"] = true
	if err := owner.MCPRefusal("atl"); err == nil || !strings.Contains(err.Error(), "turned it off") {
		t.Errorf("a sign-in kept out of the app: %v", err)
	}
	if len(used) != 2 {
		t.Errorf("a refusal was heard as a reach: %v", used)
	}
}
