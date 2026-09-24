package extensions

// A tool of the user's own and a same-named tool they took from somebody else
// are two tools under one name, and their own wins. Neither list said so: the
// Tools list only compared the user's own buckets, and the catalog HID the
// other offer, so the user could not see the tool their own copy had overtaken
// or remove an adoption that no longer ran.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAnOwnToolShadowingATakenOneIsShownOnBothLists(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "lender", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo lent"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(RootDB, "lender", "wiki_read", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(RootDB, "alice", "wiki_read", "lender", true); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo own"}); err != nil {
		t.Fatal(err)
	}

	// A tool named after a built-in never loads (hydration drops it), which
	// the list is the only place to learn.
	RegisterReservedToolName("zz_builtin_for_shadow_test")
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "zz_builtin_for_shadow_test", Description: "d", CommandTemplate: "echo x"}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	app.handleUserTools(w, req("GET", "/api/tools"))
	var tools []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &tools); err != nil {
		t.Fatalf("tools list: %v %s", err, w.Body.String())
	}
	found := false
	for _, r := range tools {
		if r["name"] == "zz_builtin_for_shadow_test" {
			if s, _ := r["shadows"].(string); r["conflict"] != true || !strings.Contains(s, "built-in") {
				t.Errorf("a tool named after a built-in is not flagged: %+v", r)
			}
		}
		if r["name"] != "wiki_read" {
			continue
		}
		found = true
		if r["conflict"] != true || r["shadows"] == nil {
			t.Errorf("the own tool hides lender's and the row does not say so: %+v", r)
		}
	}
	if !found {
		t.Fatal("precondition: the own tool is listed")
	}

	w = httptest.NewRecorder()
	app.handleGlobalTools(w, req("GET", "/api/global-tools"))
	var offers []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &offers); err != nil {
		t.Fatalf("catalog: %v %s", err, w.Body.String())
	}
	if len(offers) != 1 || offers[0]["owner"] != "lender" || offers[0]["shadowed"] != true {
		t.Errorf("lender's offer should be listed, marked as shadowed by the own tool: %+v", offers)
	}
}
