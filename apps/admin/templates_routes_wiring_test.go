package admin

// Each template API path reaches its own handler. Several answer alike on a
// bad query (two say "no such template", two "template has no detect"), so
// each is asked something only its own handler answers that way: the
// connector fetch ignores target, the extension fetch honours it, and each
// detect knows only its own target's templates.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestEachTemplatePathReachesItsOwnHandler(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerTemplatesRoutes(mux)
	for _, tc := range []struct {
		method, path string
		code         int
		prefix       string
	}{
		{"GET", "/api/connector-templates", 200, `[{"name":"a1111","label":"Automatic1111","category"`},
		{"GET", "/api/connector-template?name=a1111&target=tool", 200, `{"category":"Image generation"`},
		{"POST", "/api/connector-detect?template=comfyui", 400, "empty workflow"},
		{"GET", "/api/connector-config", 404, "no such connector\n"},
		{"GET", "/api/extensions", 200, `[{"name":"a1111","label":"Automatic1111","target":"connector"`},
		{"GET", "/api/extension-template?name=github_issue&target=tool", 200, `{"category":"Tools"`},
		// With no target it reads connectors, which the tool fetch does not.
		{"GET", "/api/extension-template?name=a1111", 200, `{"category":"Image generation"`},
		{"GET", "/api/tool-templates", 200, `[{"name":"github_issue"`},
		{"GET", "/api/tool-template?name=github_issue", 200, `{"category":"Tools"`},
		{"POST", "/api/tool-detect?template=openapi_tool", 400, "spec is required"},
		{"GET", "/api/tool-config", 404, "no such tool\n"},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"values":{}}`)))
		if w.Code != tc.code || !strings.HasPrefix(w.Body.String(), tc.prefix) {
			t.Errorf("%s %s answered %d %q, want %d starting %q", tc.method, tc.path, w.Code, w.Body.String(), tc.code, tc.prefix)
		}
	}
}
