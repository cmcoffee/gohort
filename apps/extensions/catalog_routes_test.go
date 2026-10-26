package extensions

// What the skill, publish-request, catalog, category and playbook routes do,
// read back from the store: a request carries its note and is refused for a
// tool that is not the caller's, a lent tool is added and removed by query
// and by body, a category reads back what was filed under it, a playbook rule is
// read empty, added and removed, and a skill is the owner's alone to change
// or delete.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestTheCatalogAndSkillRoutesChangeWhatTheySay(t *testing.T) {
	app, req := authedExtensions(t)
	h := serve(app)
	adb := AuthDB()
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	bob := AuthCreateSession(adb, "bob")
	call := func(token, method, path, body string) (int, string) {
		t.Helper()
		r := req(method, path)
		if token != "" {
			r = httptest.NewRequest(method, path, nil)
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		}
		r.Body = readCloser(body)
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code, w.Body.String()
	}
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "cr_lent", Description: "d", CommandTemplate: "echo x"}); err != nil {
		t.Fatal(err)
	}

	// A publish request names its note; bob cannot ask for alice's tool.
	if code, _ := call("", "POST", "/api/promotions?kind=tool&name=cr_lent", `{"note":"for the team"}`); code != 204 {
		t.Fatalf("request answered %d", code)
	}
	noted := false
	for _, p := range ListPromotionRequests(adb, true) {
		noted = noted || (p.Name == "cr_lent" && p.Note == "for the team")
	}
	if !noted {
		t.Error("the request's note was not kept")
	}
	if code, _ := call(bob, "POST", "/api/promotions?kind=tool&name=cr_lent", `{}`); code != 404 {
		t.Errorf("bob asked to publish alice's tool: %d, want 404", code)
	}

	// Lent to bob, added by query and taken off by body.
	if err := SetPersistentTempToolSharedWith(adb, "alice", "cr_lent", []string{"bob"}); err != nil {
		t.Fatal(err)
	}
	adopted := func() bool {
		for _, p := range AdoptedToolsFor(adb, "bob") {
			if p.Tool.Name == "cr_lent" && p.Owner == "alice" {
				return true
			}
		}
		return false
	}
	if code, _ := call(bob, "POST", "/api/global-tools?name=cr_lent&owner=alice&adopt=true", ""); code != 204 || !adopted() {
		t.Fatalf("add by query: %d, adopted=%v", code, adopted())
	}
	if code, _ := call(bob, "POST", "/api/global-tools", `{"name":"cr_lent","owner":"alice","adopt":false}`); code != 204 || adopted() {
		t.Fatalf("remove by body: %d, adopted=%v", code, adopted())
	}
	call(bob, "POST", "/api/global-tools?name=cr_lent&owner=alice&adopt=true", "")
	if code, _ := call(bob, "POST", "/api/global-tools?name=cr_lent&owner=alice&adopt=false", ""); code != 204 || adopted() {
		t.Fatalf("remove by query: %d, adopted=%v", code, adopted())
	}
	if code, _ := call(bob, "POST", "/api/global-tools", ``); code != 400 {
		t.Errorf("a toggle naming nothing answered %d", code)
	}

	// A category reads back its tools.
	if code, _ := call("", "POST", "/api/tool-categories?name=Lent", `{"tools":["cr_lent"]}`); code != 204 {
		t.Fatalf("file: %d", code)
	}
	if _, body := call("", "GET", "/api/tool-categories?name=Lent", ""); body != `{"name":"Lent","tools":["cr_lent"]}`+"\n" {
		t.Errorf("category record: %s", body)
	}

	// A playbook rule: the empty form, then one added and the first removed.
	skill, err := SaveSkill(adb, "alice", SkillRecord{Name: "cr_skill", Instructions: "i", Playbook: []PlaybookRule{{Fact: "first", How: "ask"}}})
	if err != nil {
		t.Fatal(err)
	}
	pb := "/api/skill-playbook?id=" + skill.ID + "&rule="
	if code, body := call("", "GET", pb+"new", ""); code != 200 || body != `{"else":"","fact":"","how":"","then":""}`+"\n" {
		t.Errorf("new rule form: %d %s", code, body)
	}
	if code, _ := call("", "POST", pb+"add", `{"fact":"second","how":"say"}`); code != 200 {
		t.Fatalf("add: %d", code)
	}
	if code, _ := call("", "POST", pb+"0", `{"remove":true}`); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	if own, _ := findOwnSkill("alice", skill.ID); len(own.Playbook) != 1 || own.Playbook[0].Fact != "second" {
		t.Errorf("playbook after add and remove: %+v", own.Playbook)
	}

	// Bob cannot share alice's skill; alice can delete it.
	if code, _ := call(bob, "PATCH", "/api/skills?id="+skill.ID, `{"allowed_users":["bob"]}`); code != 404 {
		t.Errorf("bob's grant on alice's skill answered %d, want 404", code)
	}
	if own, _ := findOwnSkill("alice", skill.ID); len(own.AllowedUsers) != 0 {
		t.Errorf("bob's grant reached alice's skill: %v", own.AllowedUsers)
	}
	if code, _ := call("", "DELETE", "/api/skills?id="+skill.ID, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if _, still := findOwnSkill("alice", skill.ID); still {
		t.Error("the skill is still there")
	}
	if code, _ := call("", "DELETE", "/api/skills?id="+skill.ID, ""); code != 404 {
		t.Errorf("deleting it again answered %d", code)
	}
}
