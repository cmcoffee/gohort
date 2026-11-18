package apptools

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An app's tool space is its own: its get_weather and the pool's are two
// tools, only Builder and Anvil (or an import) change it, a change is told
// to the app's hooks, and deleting it takes the whole space.
func TestAnAppsToolSpaceIsItsOwn(t *testing.T) {
	db := &core.DBase{Store: kvlite.MemStore()}
	saved := core.RootDB
	core.RootDB = db
	t.Cleanup(func() { core.RootDB = saved })
	var told []string
	core.RegisterAppOwnedChangeHook(func(owner, slug, kind, name, appEdit string) {
		told = append(told, owner+"/"+slug+" "+kind+" "+name+" by "+appEdit)
	})

	if err := core.AdminPersistTempTool(db, "u", core.TempTool{Name: "get_weather", Description: "the pool's", CommandTemplate: "echo pool"}); err != nil {
		t.Fatal(err)
	}
	app := core.TempTool{Name: "get_weather", Description: "the app's", CommandTemplate: "echo app"}
	if err := SaveBy("u", "wx", app, ""); err == nil || !strings.Contains(err.Error(), "Builder or Anvil") {
		t.Fatalf("a save by anything else: %v", err)
	}
	if err := SaveBy("u", "wx", app, "Builder"); err != nil {
		t.Fatal(err)
	}
	if got, ok := ByName("u", "wx", "get_weather"); !ok || got.Tool.Description != "the app's" || got.SavedBy != "Builder" {
		t.Errorf("the app's tool: %+v %v", got, ok)
	}
	if pool := core.LoadPersistentTempTools(db, "u"); len(pool) != 1 || pool[0].Tool.Description != "the pool's" {
		t.Errorf("the pool after: %+v", pool)
	}
	if len(Load("u", "other")) != 0 || len(Load("v", "wx")) != 0 {
		t.Error("another app's space, or another person's, holds the tool")
	}
	if err := SaveBy("u", "wx", app, "Anvil"); err != nil {
		t.Fatal(err)
	}
	if len(told) != 1 || told[0] != "u/wx tool get_weather by Builder" {
		t.Errorf("a save that changes nothing was told as a change, or the change was not: %q", told)
	}
	if err := DeleteBy("u", "wx", "get_weather", "someone"); err == nil {
		t.Error("a delete by anything but Builder or Anvil went through")
	}
	SaveBy("u", "wx", core.TempTool{Name: "second", CommandTemplate: "echo 2"}, "Anvil")
	if gone := DeleteAll("u", "wx"); strings.Join(gone, ",") != "get_weather,second" || len(Load("u", "wx")) != 0 {
		t.Errorf("deleting the space: %v", gone)
	}
}

// A tool of an app's space that reaches further than gohort's own tools (a
// credential, raw network) loads only once an administrator approved it as
// it is: one that changed since waits again, a flag that narrows it does not
// count, a tool changed while the review was open is not approved by it, and
// an administrator's stop holds any tool.
func TestAnAppsToolReachingFurtherWaitsForAnAdministrator(t *testing.T) {
	db := &core.DBase{Store: kvlite.MemStore()}
	saved := core.RootDB
	core.RootDB = db
	t.Cleanup(func() { core.RootDB = saved })

	plain := core.TempTool{Name: "lookup", CommandTemplate: "echo x", HookCapabilities: []string{"fetch"}}
	crm := core.TempTool{Name: "crm", Mode: core.TempToolModeAPI, Credential: "crm", Method: "GET"}
	raw := core.TempTool{Name: "psql", CommandTemplate: "psql", RawNetwork: true, HookCapabilities: []string{"secret:db"}}
	if got := Reach(plain); len(got) != 0 {
		t.Errorf("a sandboxed tool fetching through gohort reaches %v", got)
	}
	if got := strings.Join(Reach(raw), "; "); got != `the credential "db"; raw network` {
		t.Errorf("reach of a raw-network tool with a secret: %q", got)
	}
	for _, tool := range []core.TempTool{plain, crm, raw} {
		if err := SaveBy("u", "wx", tool, "Builder"); err != nil {
			t.Fatal(err)
		}
	}
	held := func(name string) string {
		at, _ := ByName("u", "wx", name)
		return Held("u", "wx", at.Tool, LoadReview("u", "wx"))
	}
	if held("lookup") != "" || !strings.Contains(held("crm"), "waiting for an administrator") {
		t.Fatalf("before a review: lookup %q, crm %q", held("lookup"), held("crm"))
	}
	waiting := AwaitingReview("u", "wx")
	if len(waiting) != 2 {
		t.Fatalf("awaiting review: %+v", waiting)
	}
	seen := map[string]string{}
	for _, at := range waiting {
		seen[at.Tool.Name] = Fingerprint(at.Tool)
	}
	// psql changes while the review is open.
	raw.CommandTemplate = "psql -h elsewhere"
	SaveBy("u", "wx", raw, "Anvil")
	if n := Approve("u", "wx", "admin", seen); n != 1 {
		t.Errorf("approved %d, want only the one unchanged since the review", n)
	}
	if held("crm") != "" || held("psql") == "" {
		t.Errorf("after the review: crm %q, psql %q", held("crm"), held("psql"))
	}
	crm.ConfirmInChat = true
	SaveBy("u", "wx", crm, "Builder")
	if held("crm") != "" {
		t.Errorf("a flag that narrows the tool undid its approval: %q", held("crm"))
	}
	crm.Credential = "other"
	SaveBy("u", "wx", crm, "Builder")
	if held("crm") == "" {
		t.Error("a tool reaching a different credential kept its approval")
	}
	Stop("u", "wx", "lookup", "admin", true)
	if !strings.Contains(held("lookup"), "switched off by an administrator") {
		t.Errorf("a stopped tool: %q", held("lookup"))
	}
	Stop("u", "wx", "lookup", "admin", false)
	if held("lookup") != "" {
		t.Errorf("a tool switched back on: %q", held("lookup"))
	}
}

// A tool that waits puts its app's review in the approvals queue, one request
// for the whole space naming each tool and what it reaches; approving it
// approves what it named, and a space with nothing waiting asks for nothing.
func TestAnAppsToolsWaitInTheApprovalsQueue(t *testing.T) {
	db := &core.DBase{Store: kvlite.MemStore()}
	saved, savedAuth := core.RootDB, core.AuthDB
	core.RootDB, core.AuthDB = db, func() core.Database { return db }
	t.Cleanup(func() { core.RootDB, core.AuthDB = saved, savedAuth })

	SaveBy("u", "wx", core.TempTool{Name: "plain", CommandTemplate: "echo"}, "Builder")
	if core.PendingPromotion(db, "u", RequestKind, "wx") {
		t.Fatal("a tool reaching nothing further asked for a review")
	}
	SaveBy("u", "wx", core.TempTool{Name: "crm", Mode: core.TempToolModeAPI, Credential: "crm"}, "Builder")
	SaveBy("u", "wx", core.TempTool{Name: "psql", CommandTemplate: "psql", RawNetwork: true}, "Anvil")
	req, ok := core.GetPromotionRequest(db, core.PromotionRequestKey(RequestKind, "u", "wx"))
	if !ok || req.State != core.PromotionPendingState || !strings.Contains(req.Note, `crm reaches the credential "crm"`) || !strings.Contains(req.Note, "psql reaches raw network") {
		t.Fatalf("the request: %+v %v", req, ok)
	}
	if err := promotion.Approve(db, req.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if len(AwaitingReview("u", "wx")) != 0 {
		t.Errorf("after approving, still waiting: %+v", AwaitingReview("u", "wx"))
	}
	SaveBy("u", "wx", core.TempTool{Name: "psql", CommandTemplate: "psql -q", RawNetwork: true}, "Anvil")
	if !core.PendingPromotion(db, "u", RequestKind, "wx") {
		t.Error("a tool changed after its approval did not ask again")
	}
	DeleteBy("u", "wx", "psql", "Builder")
	if core.PendingPromotion(db, "u", RequestKind, "wx") {
		t.Error("the request stayed after the tool it asked for was removed")
	}
}
