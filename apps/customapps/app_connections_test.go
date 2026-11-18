package customapps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/applocal"
	"github.com/cmcoffee/gohort/core/ui"
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

// Under the form, a way to make each kind that can be made: a credential
// under Integrations, a tool by Builder as the app's own, its request said.
func TestConnectionsOfferToMakeWhatIsMissing(t *testing.T) {
	sharingTestApp(t)
	prev := agenthost.Get()
	t.Cleanup(func() { agenthost.Register(prev) })
	agenthost.Register(&orchestrate.OrchestrateApp{})
	spec := AppSpec{Slug: "desk", Name: "Desk", Owner: "alice"}
	SaveAppSpec(spec)
	applocal.SaveAppNeeds("alice", "desk", []applocal.AppNeed{{Kind: "credential", Name: "crm"}, {Kind: "tool", Name: "jira_search"}, {Kind: "voice", Name: "zz"}})
	body, unanswered := connectionsBody(context.Background(), "/apps/desk", spec, "alice")
	st, ok := body.(ui.Stack)
	if !ok || unanswered != 3 || len(st.Children) != 2 {
		t.Fatalf("the body: %T %d", body, unanswered)
	}
	bar := st.Children[1].(ui.Toolbar)
	if len(bar.Actions) != 2 {
		t.Fatalf("the ways to make something new: %+v", bar.Actions)
	}
	if a := bar.Actions[0]; a.Label != "New credential for crm" || a.URL != "/extensions" || a.Method != "GET" {
		t.Errorf("the credential's: %+v", a)
	}
	if a := bar.Actions[1]; !strings.Contains(a.URL, "intake.target=desk") || !strings.Contains(a.URL, "intake.change=") || !strings.Contains(a.URL, "jira_search") {
		t.Errorf("the tool's: %+v", a)
	}
}
