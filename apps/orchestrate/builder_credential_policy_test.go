package orchestrate

// Builder's draft_api_credential answers to the administrator's policy for
// personal credentials (core/governance), as the Extensions page does.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/governance"
)

func TestBuilderDraftsFollowThePolicy(t *testing.T) {
	depStores(t, "dana")
	draft := func(name string) string {
		t.Helper()
		out, err := draftAPICredentialToolDef(&chatTurn{user: "dana"}).Handler(context.Background(), map[string]any{
			"name": name, "type": "bearer", "base_url": "https://" + name + ".test"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if err := governance.Set(governance.Credentials, string(governance.AdminOnly)); err != nil {
		t.Fatal(err)
	}
	if out := draft("refused"); !strings.HasPrefix(out, "NOT drafted") {
		t.Errorf("admin only should refuse the draft: %s", out)
	}
	if _, ok := Secure().LoadUser("dana", "refused"); ok {
		t.Error("a refused draft was saved anyway")
	}
	if err := governance.Set(governance.Credentials, string(governance.NeedsApproval)); err != nil {
		t.Fatal(err)
	}
	out := draft("held")
	c, ok := Secure().LoadUser("dana", "held")
	if !ok || !c.AwaitingApproval || !strings.Contains(out, "approval") {
		t.Errorf("needs approval should draft it held and say so: %+v / %s", c, out)
	}
}
