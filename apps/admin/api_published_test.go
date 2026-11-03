package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/shareledger"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Shared with everyone: the administrator sees each kind's deployment-wide
// grants, takes one back through its kind, and the owner is told. Nobody else
// reaches either door.
func TestTheAdminTakesBackWhatIsSharedWithEveryone(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB, RootDB = func() Database { return adb }, &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })

	shared := true
	shareledger.Register("test_kind", shareledger.Provider{Label: "Test kind",
		Published: func() []shareledger.Grant {
			if !shared {
				return nil
			}
			return []shareledger.Grant{{ID: "x1", Name: "Runbook", Owner: "bob"}}
		},
		Unpublish: func(owner, id string) error {
			if owner == "bob" && id == "x1" {
				shared = false
			}
			return nil
		},
	})
	t.Cleanup(func() { shareledger.Register("test_kind", shareledger.Provider{Label: "Test kind"}) })

	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := do("root", "GET", "/api/published"); !strings.Contains(w.Body.String(), `"name":"Runbook"`) || !strings.Contains(w.Body.String(), `"revocable":true`) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if w := do("alice", "POST", "/api/published?kind=test_kind&owner=bob&id=x1"); w.Code/100 == 2 || !shared {
		t.Fatalf("a non-admin took it back: %d", w.Code)
	}
	if w := do("root", "POST", "/api/published?kind=test_kind&owner=bob&id=x1"); w.Code/100 != 2 || shared {
		t.Fatalf("take back: %d %s", w.Code, w.Body.String())
	}
	got := notices.List(RootDB, "bob")
	if len(got) != 1 || !strings.Contains(got[0].Title, `test kind "Runbook" is no longer shared with everyone`) {
		t.Fatalf("the owner was not told: %+v", got)
	}
}
