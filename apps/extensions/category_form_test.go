package extensions

// "Add category" types the name into a field, so it arrives in the body; the
// old form posted an unsubstituted "{name}" and filed tools under a category
// literally called that. And the pre-rename /gateways path redirects here.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAddCategoryTakesItsNameFromTheForm(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(AuthDB(), "alice", TempTool{Name: "cal_add", Description: "d"}); err != nil {
		t.Fatal(err)
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		r := req(http.MethodPost, path)
		r.Body = io.NopCloser(strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleUserToolCategories(w, r)
		return w
	}
	if w := post("/api/tool-categories?name=%7Bname%7D", `{"name":"Calendar","tools":["cal_add"]}`); w.Code/100 != 2 {
		t.Fatalf("form post: %d %s", w.Code, w.Body.String())
	}
	cats := userToolCategories("alice")
	if _, bad := cats["{name}"]; bad {
		t.Fatal("tools were filed under a literal {name}")
	}
	if len(cats["Calendar"]) != 1 {
		t.Fatalf("the typed name should be the category: %v", cats)
	}
	if w := post("/api/tool-categories", `{"tools":["cal_add"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("no name at all should be refused: %d", w.Code)
	}
}

func TestTheOldGatewaysPathIsNotThisAppsOwn(t *testing.T) {
	if legacyGatewaysPath == (&Extensions{}).WebPath() || legacyGatewaysPath != "/gateways" {
		t.Fatalf("the legacy mount must be the pre-rename path, got %q", legacyGatewaysPath)
	}
}
