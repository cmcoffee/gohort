package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/promotion"
)

type fakeStoreCatalog struct{}

func (fakeStoreCatalog) GrantsFor(owner, slug string) []appstores.Grant { return nil }
func (fakeStoreCatalog) RunCommand(ctx context.Context, owner, slug, store, folder, command, input string) (string, error) {
	return "", nil
}
func (fakeStoreCatalog) Pull(ctx context.Context, owner, slug, store, item string, replace bool) (string, error) {
	return "", nil
}
func (fakeStoreCatalog) Stores(owner, slug string) []appstores.StoreInfo {
	return []appstores.StoreInfo{
		{Store: "intake", Name: "Intake", About: "Raw logs per case.", Temporary: true, FolderCount: 3,
			Commands: []appstores.CommandInfo{{Name: "extract", Label: "Extract", TwoPhase: true, InputLabel: "Response", Guide: "Support answers the signature.\nA response works once."}}},
		{Store: "analysis", Name: "Analysis", PullsFrom: "intake", PullPart: "prepared"},
	}
}

// Builder can see the stores an app could be built on, with their commands
// and the admin's guide to each, before the app exists.
func TestBuilderSeesTheStoresAnAppCanUse(t *testing.T) {
	prev := appstores.Provider
	appstores.Provider = fakeStoreCatalog{}
	t.Cleanup(func() { appstores.Provider = prev })
	turn := &chatTurn{user: "alice"}
	out, err := turn.appDefStores(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Intake (store \"intake\")", "Raw logs per case.", "3 folder(s)", "Temporary",
		"- \"extract\" (Extract): two phases, the second takes Response", "Support answers the signature.",
		"Pulls from \"intake\" (a folder's prepared/ part)", "store_command", "A new app has no grants yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(builderAppModeShipped, `app_def(action="stores")`) {
		t.Error("Builder's app instructions do not send it to the stores")
	}
}

// Builder asks for a store with app_def request_store. With nobody to ask on
// the spot (not an admin, or nobody watching) the request waits in the
// approvals queue, and the reply says where.
func TestBuilderRequestsAStoreForAnApp(t *testing.T) {
	prev := appstores.Provider
	appstores.Provider = fakeStoreCatalog{}
	t.Cleanup(func() { appstores.Provider = prev })
	_, _, user := newTestOrchestrate(t)
	pinRootDB(t)
	SaveAppSpec(AppSpec{Owner: user, Slug: "prep", Name: "Case prep", RecordKey: "id"})
	turn := &chatTurn{user: user}
	out, err := turn.appDefRequestStore(map[string]any{"id": "prep", "store": "intake", "write": true, "commands": true, "why": "downloads and extracts"})
	if err != nil || !strings.Contains(out, "waiting for an admin under Admin, Governance, Pending promotions") {
		t.Fatalf("%q %v", out, err)
	}
	name := appstores.GrantRequestName("intake", "prep", true, true, false)
	if !PendingPromotion(AuthDB(), user, appstores.GrantRequestKind, name) {
		t.Error("no pending request was filed")
	}
	if _, err := turn.appDefRequestStore(map[string]any{"id": "prep", "store": "nowhere"}); err == nil || !strings.Contains(err.Error(), "intake") {
		t.Errorf("an unknown store should name the real ones: %v", err)
	}
	if _, err := turn.appDefRequestStore(map[string]any{"id": "missing", "store": "intake"}); err == nil {
		t.Error("a request for an app that does not exist was filed")
	}
}

// An admin building the app is asked on the spot, and Approve applies the
// grant through the same approver the queue uses; anything else leaves the
// request waiting.
func TestAnAdminApprovesAStoreRequestOnTheCard(t *testing.T) {
	prev := appstores.Provider
	appstores.Provider = fakeStoreCatalog{}
	t.Cleanup(func() { appstores.Provider = prev })
	_, _, user := newTestOrchestrate(t)
	pinRootDB(t)
	AuthDB().Set(AuthTable, "user:"+user, AuthUser{Username: user, Admin: true})
	SaveAppSpec(AppSpec{Owner: user, Slug: "prep", Name: "Case prep", RecordKey: "id"})
	var applied string
	promotion.RegisterApprover(appstores.GrantRequestKind, func(owner, name string) error {
		applied = owner + " " + name
		return nil
	})
	var asked string
	answer := "Approve"
	turn := &chatTurn{user: user, askVia: func(prompt, detail string, yes []string) string { asked = prompt; return answer }}
	out, err := turn.appDefRequestStore(map[string]any{"id": "prep", "store": "intake", "commands": true, "why": "extracts"})
	if err != nil || !strings.Contains(out, "Granted") {
		t.Fatalf("%q %v", out, err)
	}
	if !strings.Contains(asked, "Grant Case prep read, run commands on the file store Intake?") || applied != user+" intake/prep: read, run commands" {
		t.Errorf("asked %q, applied %q", asked, applied)
	}
	answer, applied = "", ""
	out, _ = turn.appDefRequestStore(map[string]any{"id": "prep", "store": "intake", "write": true})
	if applied != "" || !strings.Contains(out, "Not granted now") {
		t.Errorf("a card not approved: applied %q, %q", applied, out)
	}
}
