package extensions

// What the skill, publish-request, catalog and category routes do,
// read back from the store: a request carries its note and is refused for a
// tool that is not the caller's, a lent tool is added and removed by query
// and by body, a category reads back what was filed under it, a skill's
// conditions are set, refused half-written, kept by a form that does not
// carry them and cleared by an empty list, and a skill is the owner's alone
// to change or delete.

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

	// A skill's conditions: set as rows, read back on the form and in the
	// list, refused half-written, kept by a save that does not carry them,
	// cleared by an empty list.
	skill, err := SaveSkill(adb, "alice", SkillRecord{Name: "cr_skill", Instructions: "i"})
	if err != nil {
		t.Fatal(err)
	}
	edit := "/api/skills?id=" + skill.ID
	chain := `{"name":"cr_skill","instructions":"i","conditions":[` +
		`{"kind":"","check":"","question":"Is it v4?","how":"Read the version.","mentions":"","instructions":"left over"},` +
		`{"kind":"","check":"mentions","question":"","how":"","mentions":"refund, *.pdf","instructions":"Billing steps."},` +
		`{"kind":"else","check":"lookup","question":"stray","how":"","mentions":"","instructions":"Upgrade first."}]}`
	if code, body := call("", "POST", edit, chain); code/100 != 2 {
		t.Fatalf("set the conditions: %d %s", code, body)
	}
	own, _ := findOwnSkill("alice", skill.ID)
	if c := own.Conditions; len(c) != 3 || c[0].Kind != "if" || c[0].Instructions != "" || c[1].Kind != "else_if" ||
		strings.Join(c[1].Mentions, "|") != "refund|*.pdf" || c[2].Question != "" || c[2].Instructions != "Upgrade first." {
		t.Fatalf("conditions after setting them: %+v", own.Conditions)
	}
	if _, body := call("", "GET", edit, ""); !strings.Contains(body, `"mentions":"refund, *.pdf"`) || !strings.Contains(body, `"question":"Is it v4?"`) {
		t.Errorf("the form does not read the conditions back: %s", body)
	}
	if _, body := call("", "GET", "/api/skills", ""); !strings.Contains(body, `"conditions":"If Is it v4? (+2 cases)"`) {
		t.Errorf("the list does not show the conditions: %s", body)
	}
	if code, body := call("", "POST", edit, `{"name":"cr_skill","instructions":"i","conditions":[{"kind":"if","question":"Is it v5?"}]}`); code != 400 || !strings.Contains(body, "how to check") {
		t.Errorf("a condition with no way to check it: %d %s", code, body)
	}
	if code, body := call("", "POST", edit, `{"name":"cr_skill","instructions":"i","conditions":[{"kind":"if","question":"q","how":"h"},{"kind":"else","instructions":"x"},{"kind":"else_if","question":"q","how":"h","instructions":"y"}]}`); code != 400 || !strings.Contains(body, "Else is the last") {
		t.Errorf("a row after the Else: %d %s", code, body)
	}
	if code, _ := call("", "POST", edit, `{"name":"cr_skill","instructions":"changed"}`); code/100 != 2 {
		t.Fatalf("a save without the conditions: %d", code)
	}
	if own, _ := findOwnSkill("alice", skill.ID); len(own.Conditions) != 3 {
		t.Errorf("a save that did not carry the conditions changed them: %+v", own.Conditions)
	}
	if code, _ := call("", "POST", edit, `{"name":"cr_skill","instructions":"i","conditions":[]}`); code/100 != 2 {
		t.Fatalf("clear the conditions: %d", code)
	}
	if own, _ := findOwnSkill("alice", skill.ID); len(own.Conditions) != 0 {
		t.Errorf("an empty list should clear the conditions: %+v", own.Conditions)
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
