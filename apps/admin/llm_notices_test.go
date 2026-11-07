package admin

// Whatever is wrong with the lead is ONE dashboard card. A model that cannot
// start goes on to fail its calls, and two cards with the same button read
// like two things to fix.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestLeadProblemsAreOneCard(t *testing.T) {
	t.Cleanup(func() { SetLeadInitError("", "", nil) })
	SetLeadInitError("provider", "lead-model", errors.New("no credentials"))

	a := &AdminApp{}
	got := a.llmNotices()
	if len(got) != 1 || !strings.Contains(got[0].Text, "could not be initialized") {
		t.Fatalf("got %+v, want one card for the lead", got)
	}
	if !strings.HasPrefix(got[0].Text, "The configured lead model") {
		t.Errorf("the card should open with a capital: %s", got[0].Text)
	}

	SetLeadInitError("", "", nil)
	if got = a.llmNotices(); len(got) != 0 {
		t.Errorf("nothing wrong should be no card: %+v", got)
	}
}

// A choice that no longer reads (a connection named the old way, or one not
// here) reads as not set on its page, which says why; an entry offered to
// agents that no longer reads is dropped and named. One dashboard card lists
// every such setting and opens the first, and goes once they are set.
func TestAStaleChoiceReadsAsNotSetAndIsOneCard(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	finder := `{"provider":{"transport":"http","base_url":"https://find.example.test","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"t","url":"u"}}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "finder", Kind: "service", Spec: json.RawMessage(finder)}); err != nil {
		t.Fatal(err)
	}
	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("GET", "/", nil)}
	RootDB.Set(SearchTable, "provider", "search:finder")
	RootDB.Set(ImageTable, "provider", "none")
	RootDB.Set(ImageTable, "agents", []string{"gone"})

	out, _ := a.handleWebSearch(call)
	got := out.(map[string]any)
	if got["provider"] != "" || !strings.Contains(fmt.Sprint(got["_stale"]), "names the connection finder the old way") {
		t.Errorf("web search: provider %q, %q", got["provider"], got["_stale"])
	}
	out, _ = a.handleImageGen(call)
	got = out.(map[string]any)
	if fmt.Sprint(got["agents"]) != "[]" || got["provider"] != "none" || !strings.Contains(fmt.Sprint(got["_stale"]), "gone") {
		t.Errorf("image generation: %+v", got)
	}

	var n DashboardNotice
	ok := false
	for _, c := range a.notices(true) {
		if strings.Contains(c.Text, "need setting again") {
			n, ok = c, true
		}
	}
	if !ok || !strings.HasPrefix(n.Text, "2 settings need setting again: Media Generation, Web Search.") ||
		n.Action != "Open Media Generation" || !strings.HasSuffix(n.URL, ui.SectionOnTab("Services", "Media Generation")) {
		t.Errorf("the card: %+v %v", n, ok)
	}
	RootDB.Set(SearchTable, "provider", "finder")
	RootDB.Set(ImageTable, "agents", []string{})
	out, _ = a.handleWebSearch(call)
	if got := out.(map[string]any); got["provider"] != "finder" || got["_stale"] != nil {
		t.Errorf("set again: %+v", got)
	}
	if n, ok := a.staleNotice(); ok {
		t.Errorf("set again, still a card: %+v", n)
	}
}
