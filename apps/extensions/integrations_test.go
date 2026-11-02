package extensions

// Integrations: every kind in one list, one Add handed to the chosen kind's
// own endpoint, and each kind's row actions kept to its own rows.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/governance"
	"github.com/cmcoffee/gohort/core/mcppersonal"
	"github.com/cmcoffee/gohort/core/sections"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/core/webhooks"
)

// The list holds each kind's own rows, read from its own endpoint: what the
// shared columns show, and the kind's own fields beside them and under its
// name, which its actions read.
func TestIntegrationsListEveryKind(t *testing.T) {
	app, req := authedExtensions(t)
	do := func(method, path, body string) (int, string) {
		r := req(method, path)
		if body != "" {
			r.Body = readCloser(body)
		}
		w := httptest.NewRecorder()
		serve(app)(w, r)
		return w.Code, w.Body.String()
	}
	if code, body := do("POST", "/api/credentials", `{"name":"github_api","type":"bearer","base_url":"https://api.github.example.test","secret":"s"}`); code >= 300 {
		t.Fatalf("credential: %d %s", code, body)
	}
	if code, body := do("POST", "/api/mcp-servers", `{"name":"linear","url":"https://mcp.invalid/mcp"}`); code >= 300 {
		t.Fatalf("mcp: %d %s", code, body)
	}
	SaveEventMonitor(RootDB, EventMonitor{Name: "w", Owner: "alice", Kind: EventKindWebhook, Token: NewEventToken()})
	if _, err := webhooks.Create(webhooks.Webhook{Label: "Pushes", Owner: "alice", Verify: webhooks.VerifyNone,
		Feeds: webhooks.FeedWatch + "w", Reach: webhooks.ReachDirect}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, h := range webhooks.List("alice") {
			webhooks.Remove("alice", h.Name)
		}
	})
	code, body := do("GET", "/api/integrations", "")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(body), &rows); code != 200 || err != nil {
		t.Fatalf("list: %d %v %s", code, err, body)
	}
	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r["_key"].(string)] = r
		if r["_kind"] == "API lent to you" {
			t.Errorf("nobody lent alice anything: %v", r)
		}
	}
	var hook map[string]any
	for _, h := range webhooks.List("alice") {
		hook = byKey["webhook:"+h.Name]
	}
	api, mcp := byKey["api:github_api"], byKey["mcp:linear"]
	if api == nil || mcp == nil || hook == nil || api["_kind"] != "API" || mcp["_kind"] != "MCP server" || hook["_kind"] != "Webhook" {
		t.Fatalf("rows: %v", rows)
	}
	if api["_key"] != "api:github_api" || api["_title"] != "github_api" || api["_status"] != "" ||
		!strings.Contains(api["_detail"].(string), "https://api.github.example.test") || strings.Contains(api["_detail"].(string), "Just you") ||
		api["name"] != "github_api" {
		t.Errorf("an API: %v", api)
	}
	if nested, _ := api["api"].(map[string]any); nested["has_secret"] != true {
		t.Errorf("the kind's own fields under its name: %v", api["api"])
	}
	if mcp["_status"] != sections.StatusWaiting || mcp["_title"] != "linear" {
		t.Errorf("an MCP server waiting for approval: %v", mcp)
	}
	if hook["_title"] != "Pushes" || strings.Count(hook["_detail"].(string), "checks nothing") != 1 || hook["_status"] != "" {
		t.Errorf("a webhook: %v", hook)
	}
	if _, ok := mcp["api"]; ok {
		t.Error("an MCP server's row carries an API's fields, so an API's actions show on it")
	}
}

// The Add form is handed to the chosen kind's own endpoint with only that
// kind's fields, under their own names; its answer is the kind's.
func TestTheAddFormGoesToTheChosenKind(t *testing.T) {
	app, req := authedExtensions(t)
	do := func(body string) (int, string) {
		r := req("POST", "/api/integrations")
		r.Body = readCloser(body)
		w := httptest.NewRecorder()
		serve(app)(w, r)
		return w.Code, w.Body.String()
	}
	code, body := do(`{"integration":"mcp","mcp__name":"linear","mcp__url":"https://mcp.invalid/mcp","api__name":"stray","webhook__label":"stray"}`)
	if code >= 300 || !strings.Contains(body, "approval") {
		t.Fatalf("add an MCP server: %d %s", code, body)
	}
	if _, ok := mcppersonal.Load("alice", "linear"); !ok {
		t.Error("the MCP server was not added")
	}
	if len(webhooks.List("alice")) != 0 {
		t.Error("another kind's fields made something")
	}
	// The kind's own refusal comes back as it gave it.
	if code, body := do(`{"integration":"mcp","mcp__name":"Bad Name","mcp__url":"https://mcp.invalid/mcp"}`); code != http.StatusBadRequest || body == "" {
		t.Errorf("the kind's refusal: %d %s", code, body)
	}
	for _, bad := range []string{`{"integration":""}`, `{"integration":"nothing"}`, `not json`} {
		if code, _ := do(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
	if code, _ := do(`{"integration":"api_lent","api_lent__name":"x"}`); code != http.StatusForbidden {
		t.Errorf("a kind that is not added here: %d", code)
	}
	if err := governance.Set(governance.MCPServers, string(governance.AdminOnly)); err != nil {
		t.Fatal(err)
	}
	if code, _ := do(`{"integration":"mcp","mcp__name":"other","mcp__url":"https://mcp.invalid/mcp"}`); code != http.StatusForbidden {
		t.Errorf("a kind the policy keeps to administrators: %d", code)
	}
	// Its starting values are the kinds' own, under their names.
	r := req("GET", "/api/integrations?options=new")
	w := httptest.NewRecorder()
	serve(app)(w, r)
	var vals map[string]any
	json.Unmarshal(w.Body.Bytes(), &vals)
	if vals["webhook__verify"] != webhooks.VerifyHMAC || vals["webhook__secret"] == "" || vals["verify"] != nil {
		t.Errorf("starting values: %v", vals)
	}
}

// The Add form: a choice of kind, each kind's fields named under it and
// shown only while it is chosen, their own conditions reading the kind's
// fields; a kind the policy keeps from the person is not offered.
func TestTheAddFormOffersWhatThePersonMayAdd(t *testing.T) {
	app, req := authedExtensions(t)
	serve(app)
	form := func() map[string]ui.FormField {
		sec := app.integrationsSection(req("GET", "/extensions"), "alice")
		b, _ := json.Marshal(sec)
		var page struct {
			Body struct {
				Items []struct {
					Body struct {
						Fields []ui.FormField `json:"fields"`
					} `json:"body"`
				} `json:"items"`
			} `json:"body"`
		}
		json.Unmarshal(b, &page)
		out := map[string]ui.FormField{}
		for _, c := range page.Body.Items {
			for _, f := range c.Body.Fields {
				out[f.Field] = f
			}
		}
		return out
	}
	fields := form()
	pick, ok := fields[pickField]
	if !ok {
		t.Fatalf("no choice of kind: %v", fields)
	}
	offered := map[string]string{}
	for _, o := range pick.Options {
		offered[o.Value] = o.Label
	}
	if _, ok := offered["api_lent"]; ok || offered["mcp"] == "" || offered["api"] == "" || offered["webhook"] == "" {
		t.Errorf("offered: %v", offered)
	}
	if f := fields["mcp__oauth_client_secret"]; f.ShowWhen != "integration:mcp;mcp__auth:oauth;mcp__oauth_client_id" {
		t.Errorf("a field's own condition: %q", f.ShowWhen)
	}
	if f := fields["api__name"]; f.ShowWhen != "integration:api" {
		t.Errorf("a field with no condition of its own: %q", f.ShowWhen)
	}
	if f := fields["mcp___about"]; f.Type != "header" || !strings.Contains(f.Help, "approval") || f.ShowWhen != "integration:mcp" {
		t.Errorf("the kind's heading says a new one waits: %+v", f)
	}
	for name, f := range fields {
		if f.Type == "header" && name == "" {
			t.Error("a heading with no name shows whatever is chosen")
		}
	}
	governance.Set(governance.MCPServers, string(governance.AdminOnly))
	if _, ok := form()["mcp__name"]; ok {
		t.Error("offered a kind the policy keeps to administrators")
	}
}

// Conditions read the kind's own fields, in every clause and group.
func TestConditionsReadTheKindsFields(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "integration:k",
		"auth:oauth":             "integration:k;k__auth:oauth",
		"!token; mode:a|b":       "integration:k;!k__token;k__mode:a|b",
		"a:x||b":                 "integration:k;k__a:x||integration:k;k__b",
		"verify:!" + "none;kind": "integration:k;k__verify:!none;k__kind",
	} {
		if got := chosenWhen("k", in); got != want {
			t.Errorf("chosenWhen(%q) = %q, want %q", in, got, want)
		}
	}
	if kindWhen("k", "") != "" || kindWhen("k", "a;!b") != "k__a;!k__b" {
		t.Error("kindWhen")
	}
}

// Each kind's actions show only on its own rows, and their own conditions
// read its own fields.
func TestActionsKeepToTheirKind(t *testing.T) {
	got := gateActions("mcp", []ui.RowAction{
		{Label: "Check"},
		{Label: "Enable", OnlyIf: "can_enable"},
		{Label: "Disable", HideIf: "disabled"},
		{Label: "Both", OnlyIf: "a", HideIf: "b", DisableIf: "c"},
	})
	want := [][3]string{{"mcp", "", ""}, {"mcp.can_enable", "", ""}, {"mcp", "mcp.disabled", ""}, {"mcp.a", "mcp.b", "mcp.c"}}
	for i, a := range got {
		if a.OnlyIf != want[i][0] || a.HideIf != want[i][1] || a.DisableIf != want[i][2] {
			t.Errorf("%s: only %q hide %q disable %q", a.Label, a.OnlyIf, a.HideIf, a.DisableIf)
		}
	}
	app, req := authedExtensions(t)
	serve(app)
	b, _ := json.Marshal(app.integrationsSection(req("GET", "/extensions"), "alice"))
	for _, k := range []string{`"only_if":"api"`, `"only_if":"mcp.can_enable"`, `"only_if":"webhook.signed"`, `"hide_if":"webhook.managed"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("the list's actions lack %s", k)
		}
	}
}

// A kind the person may not use is neither listed nor offered, and an add
// of one never reaches its endpoint.
func TestAKindThePersonMayNotUseStaysAway(t *testing.T) {
	app, req := authedExtensions(t)
	serve(app)
	reached := false
	reach := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Write([]byte(`[{"name":"x"}]`))
	})
	// Left registered: no one may use it, so it is never seen.
	sections.RegisterIntegrationKind(sections.IntegrationKind{Name: "zz_nobodys", Label: "Nobody's", Blurb: "b",
		Allowed: func(*http.Request, string) bool { return false },
		Fields:  func(string) []ui.FormField { return []ui.FormField{{Field: "name"}} },
		Add:     reach, List: reach, Defaults: reach,
		Row: func(map[string]any) sections.IntegrationRow { return sections.IntegrationRow{Key: "x", Title: "x"} }})
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/integrations", ""},
		{"GET", "/api/integrations?options=new", ""},
		{"POST", "/api/integrations", `{"integration":"zz_nobodys","zz_nobodys__name":"x"}`},
	} {
		r := req(c.method, c.path)
		if c.body != "" {
			r.Body = readCloser(c.body)
		}
		w := httptest.NewRecorder()
		serve(app)(w, r)
		if strings.Contains(w.Body.String(), "Nobody's") {
			t.Errorf("%s %s: %s", c.method, c.path, w.Body.String())
		}
	}
	if reached {
		t.Error("a kind the person may not use was asked")
	}
	if b, _ := json.Marshal(app.integrationsSection(req("GET", "/extensions"), "alice")); strings.Contains(string(b), "zz_nobodys") {
		t.Error("offered")
	}
}

// A kind with nothing to add it with is not offered, a kind the person may
// not add gives the form no starting values, a list that fails lists
// nothing, and an item another kind's row stands for is left out.
func TestAKindOnlyGivesWhatItCan(t *testing.T) {
	app, req := authedExtensions(t)
	serve(app)
	failing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`[{"name":"broken"}]`))
	})
	starts := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"x":"start"}`)) })
	row := func(map[string]any) sections.IntegrationRow {
		return sections.IntegrationRow{Key: "broken", Title: "broken"}
	}
	// Left registered: neither lists, offers nor starts anything, for anyone.
	sections.RegisterIntegrationKind(sections.IntegrationKind{Name: "zz_formless", Label: "Formless",
		Add: starts, List: failing, Row: row})
	sections.RegisterIntegrationKind(sections.IntegrationKind{Name: "zz_closed", Label: "Closed",
		CanAdd:   func(string) (bool, string) { return false, "" },
		Fields:   func(string) []ui.FormField { return []ui.FormField{{Field: "x"}} },
		Defaults: starts, Add: starts, List: failing, Row: row})
	stoodFor := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[{"name":"stood_for"}]`)) })
	sections.RegisterIntegrationKind(sections.IntegrationKind{Name: "zz_stood_for", Label: "Stood for", List: stoodFor,
		Row: func(map[string]any) sections.IntegrationRow { return sections.IntegrationRow{Title: "stood_for"} }})
	for _, k := range sections.IntegrationKinds() {
		if k.Name == "webhook" && k.Row(map[string]any{"name": "h", "label": "Wren (Teams bot)", "managed": true}).Key != "" {
			t.Error("a Teams bot's own webhook is listed beside the bot, to be deleted from under it")
		}
	}
	b, _ := json.Marshal(app.integrationsSection(req("GET", "/extensions"), "alice"))
	if strings.Contains(string(b), "zz_formless") || strings.Contains(string(b), "zz_closed") {
		t.Error("offered a kind with no form, or one the person may not add")
	}
	for path, nots := range map[string][]string{"/api/integrations?options=new": {"zz_closed__x"}, "/api/integrations": {"broken", "stood_for"}} {
		w := httptest.NewRecorder()
		serve(app)(w, req("GET", path))
		for _, not := range nots {
			if strings.Contains(w.Body.String(), not) {
				t.Errorf("%s: %s", path, w.Body.String())
			}
		}
	}
}
