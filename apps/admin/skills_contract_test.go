package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the Skills, Pipelines and Collections sections get back, through the
// real routes and the gate in front of them: every user's pipelines and
// deleting one; the administrator's own skills listed, read (whole and as the
// Edit form loads it), created, edited onto the stored record, renamed onto a
// name already taken, switched off and on, and deleted; and the collections
// the Skills editor offers. Each as an administrator, as a user who is not
// one, by the wrong method and with a broken or missing body. Compared with
// testdata/skills_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestSkillsRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	orch := adb.Bucket("orchestrate")
	SavePipelineDef(UserDB(orch, "alice"), PipelineDef{ID: "pl-alice", Name: "Alice's flow", Description: "two steps",
		Stages: []PipelineStage{{Name: "one"}, {Name: "two"}}})
	SavePipelineDef(UserDB(orch, "root"), PipelineDef{ID: "pl-root", Name: "Root's flow", Stages: []PipelineStage{{Name: "only"}}})
	SaveCollection(UserDB(orch, "root"), Collection{ID: "col-root", Owner: "root", Name: "Runbooks", Description: "how-tos"})
	SaveCollection(UserDB(orch, "alice"), Collection{ID: "col-alice", Owner: "alice", Name: "Alice's notes"})

	ct := &contract{dates: true}
	var masks []string
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	// The pipeline list follows the order the accounts are listed in, which
	// is not one: compared sorted, as a top-level list is.
	sortPipelines := func(out string) string {
		var m struct{ Pipelines []json.RawMessage }
		if json.Unmarshal([]byte(out), &m) != nil || m.Pipelines == nil {
			return out
		}
		rows := make([]string, len(m.Pipelines))
		for i, p := range m.Pipelines {
			rows[i] = string(p)
		}
		sort.Strings(rows)
		return `{"pipelines":[` + strings.Join(rows, ",") + `]}`
	}
	do := func(user, method, path, body string) string {
		if len(masks) > 0 {
			ct.rewrite = strings.NewReplacer(masks...)
		}
		ct.shape = nil
		if strings.HasPrefix(path, "/api/pipelines") {
			ct.shape = sortPipelines
		}
		return ct.run(mux, adb, user, method, path, body)
	}
	// save creates a skill, and names its id in the record from then on.
	save := func(as, body string) string {
		var got struct{ ID string }
		reply := do("root", "POST", "/api/skills", body)
		json.Unmarshal([]byte(reply), &got)
		if got.ID == "" {
			t.Fatalf("save %s: no id in %s", body, reply)
		}
		masks = append(masks, got.ID, as)
		return got.ID
	}

	for _, p := range []string{"/api/pipelines", "/api/skills", "/api/collections"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}
	do("alice", "DELETE", "/api/pipelines?id=pl-alice", "")
	do("root", "DELETE", "/api/pipelines", "")
	do("root", "DELETE", "/api/pipelines?id=nope", "")
	do("root", "DELETE", "/api/pipelines?id=pl-alice", "")
	do("root", "GET", "/api/pipelines", "")

	do("alice", "POST", "/api/skills", `{"name":"mine","description":"d"}`)
	s1 := save("<skill-1>", `{"name":"Release notes","description":"Write release notes","triggers":["release"],"instructions":"Be brief.","allowed_tools":["git"],"attached_collections":["col-root"]}`)
	s2 := save("<skill-2>", `{"name":"Triage","description":"Sort tickets","playbook_text":"[{\"fact\":\"severity\",\"how\":\"ask the reporter\",\"then\":\"label it urgent\"}]"}`)
	do("root", "POST", "/api/skills", `{"name":"release NOTES","description":"a second one"}`)
	do("root", "POST", "/api/skills", `{"description":"no name"}`)
	do("root", "POST", "/api/skills", `{"name":"No description"}`)
	do("root", "POST", "/api/skills", `{"name":"Bad playbook","description":"d","playbook_text":"not json"}`)
	do("root", "POST", "/api/skills", `{"name":"Odd playbook","description":"d","playbook_text":"[{\"when\":\"bug\"}]"}`)
	do("root", "POST", "/api/skills", `{"name":"Empty rule","description":"d","playbook_text":"[{}]"}`)
	do("root", "POST", "/api/skills", `{"name":`)
	do("root", "POST", "/api/skills", ``)
	do("root", "PATCH", "/api/skills?id="+s1, `{"instructions":"Be very brief."}`)
	do("root", "POST", "/api/skills", `{"id":"`+s2+`","name":"Release Notes","description":"rename onto a taken name"}`)
	do("root", "POST", "/api/skills", `{"id":"`+s2+`","playbook_text":""}`)
	do("root", "GET", "/api/skills", "")

	for _, id := range []string{s1, s2, "nope", ""} {
		do("alice", "GET", "/api/skills/"+id, "")
		do("root", "GET", "/api/skills/"+id, "")
	}
	do("root", "GET", "/api/skills/"+s1+"?view=form", "")
	do("root", "GET", "/api/skills/"+s1+"/extra", "")
	do("root", "POST", "/api/skills/"+s1, "{}")

	do("alice", "POST", "/api/skills?action=disable&id="+s1, "")
	do("root", "POST", "/api/skills?action=disable&id="+s1, "")
	do("root", "POST", "/api/skills?action=disable", "")
	do("root", "POST", "/api/skills?action=disable&id=nope", "")
	do("root", "GET", "/api/skills/"+s1, "")
	do("root", "POST", "/api/skills", `{"id":"`+s1+`","name":"Release notes","description":"Write release notes","disabled":false}`)
	do("root", "GET", "/api/skills/"+s1, "")
	do("root", "PATCH", "/api/skills?action=enable&id="+s1, "")
	do("root", "GET", "/api/skills/"+s1, "")

	do("alice", "DELETE", "/api/skills?id="+s2, "")
	do("root", "DELETE", "/api/skills", "")
	do("root", "DELETE", "/api/skills?id=nope", "")
	do("root", "DELETE", "/api/skills?id="+s2, "")
	do("root", "GET", "/api/skills", "")

	ct.check(t, "testdata/skills_contract.golden")
}
