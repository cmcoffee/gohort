package customapps

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// The owner's app page carries a way to change it: Builder, its starting form
// already pointed at this app.
func TestAnAppPageLinksToBuilderForItsOwner(t *testing.T) {
	prev := findOrchestrate
	t.Cleanup(func() { findOrchestrate = prev })
	page := json.RawMessage(`{"title":"Swing","nav":[{"label":"Settings","url":"_settings"}],"sections":[]}`)

	orch := &orchestrate.OrchestrateApp{}
	findOrchestrate = func() *orchestrate.OrchestrateApp { return orch }
	var m struct {
		Nav []struct{ Label, URL string } `json:"nav"`
	}
	if err := json.Unmarshal(withBuilderLink(page, "swing"), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Nav) != 2 || m.Nav[0].Label != "Settings" {
		t.Fatalf("nav = %+v, want Settings kept and one link added", m.Nav)
	}
	if l := m.Nav[1]; l.Label != "Change with Builder" || !strings.Contains(l.URL, "agent=seed-builder") ||
		!strings.Contains(l.URL, "intake.kind=App") || !strings.HasSuffix(l.URL, "intake.target=swing") {
		t.Errorf("link = %+v", l)
	}
	if u := builderChangeURL("{slug}"); !strings.HasSuffix(u, "intake.target={slug}") {
		t.Errorf("the row action's URL must keep its placeholder: %s", u)
	}
}
