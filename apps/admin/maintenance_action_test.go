package admin

// The Run button has to post the maintenance function's KEY. It posted {Label}
// instead, so every action 404'd with "unknown maintenance function" — the
// label is prose ("Sweep expired caches"), the registry is keyed by
// sweep_expired_caches, and nothing checked that the placeholder named a field
// the row actually carries.
//
// Source-level, in the style of the built-in tool sweeps: the page is a Go
// literal that needs a live DB and an authenticated user to build, and the
// property worth pinning is one string in that literal.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestMaintenanceRunPostsTheKeyNotTheLabel(t *testing.T) {
	src, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatal(err)
	}
	page := string(src)
	if strings.Contains(page, "api/maintenance?key={Label}") {
		t.Error("the Run button posts the label; the registry is keyed by Key, so every action 404s")
	}
	if !strings.Contains(page, "api/maintenance?key={Key}") {
		t.Error("the maintenance PostTo no longer substitutes {Key}")
	}
}

// The runtime substitutes raw JSON field names off each row, so the
// placeholder has to match what the endpoint actually serializes.
func TestMaintenanceRowsCarryAKeyField(t *testing.T) {
	raw, err := json.Marshal(ListMaintenanceFuncs())
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Skip("no maintenance functions registered in this binary")
	}
	if _, ok := rows[0]["Key"]; !ok {
		t.Fatalf("rows carry no \"Key\" field, so {Key} cannot substitute: %v", rows[0])
	}
}
