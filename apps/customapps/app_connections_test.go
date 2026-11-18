package customapps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/applocal"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An imported app's connections: its owner answers each of what it names
// that only a deployment has (map it, or let it go without), in the form the
// import dialog and the Settings page share; the app cannot be switched on
// while any is unanswered, and someone it is shared with answers none.
func TestAnImportedAppIsSwitchedOnOnceItsNeedsAreAnswered(t *testing.T) {
	T := sharingTestApp(t)
	auth := &DBase{Store: kvlite.MemStore()}
	saved := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = saved })
	AuthSetUser(auth, "alice", "pw", false)
	AuthSetUser(auth, "bob", "pw", false)
	SaveAppSpec(AppSpec{Slug: "desk", Name: "Desk", Owner: "alice", RecordKey: "id", Disabled: true})
	applocal.SaveAppNeeds("alice", "desk", []applocal.AppNeed{{Kind: "credential", Name: "crm"}, {Kind: "voice", Name: "zz"}})
	h := routed(T)
	do := func(user, method, path, body string) (int, string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, user)})
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code, w.Body.String()
	}

	form, unanswered := connectionsForm(context.Background(), "/apps/desk", "alice", "desk")
	if unanswered != 2 || len(form.Fields) != 2 || form.Fields[0].Label != "Credential crm" {
		t.Fatalf("the form: %d unanswered, %+v", unanswered, form.Fields)
	}
	last := form.Fields[1].Options[len(form.Fields[1].Options)-1]
	if last.Value != needOff {
		t.Errorf("no way to let it go without: %+v", form.Fields[1].Options)
	}
	if code, body := do("alice", "POST", "/_app/enable?slug=desk", ""); code != http.StatusForbidden || !strings.Contains(body, "Connections") {
		t.Errorf("switched on with its needs unanswered: %d %s", code, body)
	}
	if code, _ := do("bob", "POST", "/desk/_settings/connections", `{"n0":"sales"}`); code == 200 {
		t.Error("someone other than the owner answered its needs")
	}
	if code, body := do("alice", "POST", "/desk/_settings/connections", `{"n0":"sales","n1":"`+needOff+`"}`); code != 200 {
		t.Fatalf("answering: %d %s", code, body)
	}
	if to, off := applocal.AppNeedAlias("alice", "desk", "credential", "crm"); to != "sales" || off {
		t.Errorf("crm means %q (off %v)", to, off)
	}
	_, body := do("alice", "GET", "/desk/_settings/connections", "")
	var got map[string]string
	json.Unmarshal([]byte(body), &got)
	if got["n0"] != "sales" || got["n1"] != needOff {
		t.Errorf("the form's record: %s", body)
	}
	if code, body := do("alice", "POST", "/_app/enable?slug=desk", ""); code != 200 {
		t.Errorf("switching on once answered: %d %s", code, body)
	}
}
