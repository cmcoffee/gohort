package extensions

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The Security panel sets who may use an API and how; Edit no longer carries
// those fields, and saving Edit leaves them as they were.
func TestAPISecurityPanel(t *testing.T) {
	app, req := authedExtensions(t)
	post := func(path, body string) int {
		r := req("POST", path)
		r.Body = readCloser(body)
		w := httptest.NewRecorder()
		app.handleCredentials(w, r)
		if w.Code >= 300 {
			t.Logf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Code
	}
	if code := post("/api/credentials", `{"name":"weather","type":"bearer","base_url":"https://w.test","secret":"k"}`); code >= 300 {
		t.Fatalf("create: %d", code)
	}
	if code := post("/api/credentials?action=security&name=weather",
		`{"reach":"agents","only_agents":["a1","seed-builder"],"ask":"writes","lending":"read"}`); code >= 300 {
		t.Fatalf("security: %d", code)
	}
	w := httptest.NewRecorder()
	app.handleCredentials(w, req("GET", "/api/credentials?name=weather&view=security"))
	var got apiSecurity
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Reach != "agents" || len(got.OnlyAgents) != 2 || got.Ask != "writes" || got.Lending != LendRead {
		t.Fatalf("security read back: %+v", got)
	}

	// Edit, without the security fields, keeps them.
	if code := post("/api/credentials", `{"name":"weather","type":"bearer","base_url":"https://w.test","description":"forecasts"}`); code >= 300 {
		t.Fatalf("edit: %d", code)
	}
	c, _ := Secure().LoadUser("alice", "weather")
	if c.Description != "forecasts" || !c.ConfirmWrites || c.Lending != LendRead || len(c.OnlyAgents) != 2 {
		t.Fatalf("an Edit save reset the security: %+v", c)
	}

	// Tools only clears the agent list; agents with none picked is refused.
	if code := post("/api/credentials?action=security&name=weather", `{"reach":"tools","ask":"never"}`); code >= 300 {
		t.Fatalf("tools: %d", code)
	}
	c, _ = Secure().LoadUser("alice", "weather")
	if !c.Secured || len(c.OnlyAgents) != 0 || c.ConfirmWrites {
		t.Fatalf("tools only: %+v", c)
	}
	if code := post("/api/credentials?action=security&name=weather", `{"reach":"agents","only_agents":[]}`); code != 400 {
		t.Fatalf("agents with none picked was not refused: %d", code)
	}
}
