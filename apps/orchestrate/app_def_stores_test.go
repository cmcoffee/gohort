package orchestrate

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/appstores"
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
