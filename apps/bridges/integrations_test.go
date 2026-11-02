package bridges

// The Teams bots and linked services a person adds are listed and added
// under Extensions > Integrations, through this app's own endpoints, for
// the people who may use this app.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/governance"
	"github.com/cmcoffee/gohort/core/sections"
	"github.com/cmcoffee/gohort/core/webhooks"
)

func TestBridgesKindsUnderIntegrations(t *testing.T) {
	f := newOwnershipFixture(t)
	f.adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice", Apps: []string{"/bridges"}})
	f.adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob", Apps: []string{"/extensions"}})
	f.adb.Set(WebTable, "external_url", "https://gohort.example.test")
	T := f.T
	T.wireWebhooks()
	t.Cleanup(func() { webhooks.LinkedServices, webhooks.ToChannel = nil, nil })
	T.registerIntegrations("/bridges")
	kinds := map[string]sections.IntegrationKind{}
	for _, k := range sections.IntegrationKinds() {
		kinds[k.Name] = k
	}
	bot, link := kinds["teams_bot"], kinds["link"]
	if bot.Add == nil || link.Add == nil || bot.Fields == nil || link.Fields == nil {
		t.Fatalf("kinds: %v", kinds)
	}
	as := func(user, method, body string) *http.Request {
		r := httptest.NewRequest(method, "/extensions/api/integrations", bytes.NewReader([]byte(body)))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(f.adb, user)})
		return r
	}
	if !bot.Allowed(as("alice", "GET", ""), "alice") || bot.Allowed(as("bob", "GET", ""), "bob") || link.Allowed(as("bob", "GET", ""), "bob") {
		t.Error("only people who may use Bridges see its kinds")
	}
	w := httptest.NewRecorder()
	bot.Add.ServeHTTP(w, as("alice", "POST", `{"name":"Wren","app_id":"`+botApp+`","secret":"sekret"}`))
	if w.Code >= 300 || !strings.Contains(w.Body.String(), "approval") {
		t.Fatalf("add a bot: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	bot.List.ServeHTTP(w, as("alice", "GET", ""))
	var recs []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &recs); err != nil || len(recs) != 1 {
		t.Fatalf("list: %v %s", err, w.Body.String())
	}
	row := bot.Row(recs[0])
	if row.Key != "teams" || row.Title != "Wren" || row.Status != sections.StatusWaiting || !strings.Contains(row.Detail, "https://gohort.example.test/hook/") {
		t.Errorf("the bot's row: %+v", row)
	}
	for status, want := range map[string]string{"switched off by an administrator": sections.StatusOff, "answering": ""} {
		if got := bot.Row(map[string]any{"status": status}).Status; got != want {
			t.Errorf("%q reads %q, want %q", status, got, want)
		}
	}
	if bot.Row(map[string]any{"status": "answering", "trouble": true}).Status != sections.StatusTrouble ||
		link.Row(map[string]any{"service": "slack", "trouble": true}).Status != sections.StatusTrouble ||
		link.Row(map[string]any{"service": "slack"}).Status != "" {
		t.Error("trouble")
	}
	if ok, note := bot.CanAdd("alice"); !ok || note == "" {
		t.Errorf("under Needs approval: %v %q", ok, note)
	}
	governance.Set(governance.TeamsBots, string(governance.AdminOnly))
	if ok, _ := bot.CanAdd("alice"); ok {
		t.Error("offered a bot the policy keeps to administrators")
	}
	for _, a := range bot.Actions {
		if a.Type == "button" && !strings.Contains(a.PostTo, "?service={service}") {
			t.Errorf("an action that does not name its bot: %+v", a)
		}
	}
}
