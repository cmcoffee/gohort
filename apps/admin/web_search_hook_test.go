package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sourcehooks"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A Source Hook picked as the search engine is saved and read back; one that
// cannot be (gone, off, not an API hook) is refused at the save, and the
// picker lists only those that can.
func TestASourceHookCanBeTheSearchEngine(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	sub := http.NewServeMux()
	a.registerNetConfigRoutes(sub)
	hooks := &DBase{Store: kvlite.MemStore()}
	sourcehooks.SaveSourceHook(hooks, sourcehooks.SourceHook{Name: "mysearch", Type: sourcehooks.HookTypeAPI, Endpoint: "https://search.example.org/api"})
	sourcehooks.SaveSourceHook(hooks, sourcehooks.SourceHook{Name: "walls", Type: sourcehooks.HookTypePaywall})
	defer sourcehooks.DeleteSourceHook(hooks, "mysearch")
	defer sourcehooks.DeleteSourceHook(hooks, "walls")

	do := func(method, path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(string(b))))
		return w
	}
	if w := do("POST", "/api/web-search", map[string]any{"provider": "hook", "hook": "walls"}); w.Code != http.StatusBadRequest {
		t.Errorf("a paywall hook should be refused as the engine: %d", w.Code)
	}
	if w := do("POST", "/api/web-search", map[string]any{"provider": "hook", "hook": "mysearch"}); w.Code >= 300 {
		t.Fatalf("saving the hook as the engine: %d %s", w.Code, w.Body.String())
	}
	if got := a.storedString(SearchTable, "hook"); got != "mysearch" {
		t.Errorf("the hook was not stored: %q", got)
	}
	if w := do("GET", "/api/web-search", nil); !strings.Contains(w.Body.String(), `"hook":"mysearch"`) {
		t.Errorf("the form should read the hook back: %s", w.Body.String())
	}
	// Another provider clears it.
	do("POST", "/api/web-search", map[string]any{"provider": "duckduckgo", "hook": "mysearch"})
	if got := a.storedString(SearchTable, "hook"); got != "" {
		t.Errorf("a provider that is not a hook kept %q", got)
	}
	w := do("GET", "/api/web-search/hooks", nil)
	if !strings.Contains(w.Body.String(), `"value":"mysearch"`) || !strings.Contains(w.Body.String(), "search.example.org") || strings.Contains(w.Body.String(), "walls") {
		t.Errorf("the picker should list the API hooks only: %s", w.Body.String())
	}
}
