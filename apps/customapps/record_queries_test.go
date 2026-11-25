package customapps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/recquery"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// The page asks for part of its records: a query answers {records, next},
// a count answers {count}, a bad query says what to fix, and a list with
// no query answers as it always has. The same goes for a shared collection.
func TestThePageAsksForPartOfItsRecords(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "log", Name: "Log", Owner: "alice", RecordKey: "id", SharedCollections: []string{"board"}}
	for i, day := range []string{"2026-11-09", "2026-11-10", "2026-11-11", "2026-11-12"} {
		body := `{"id":"r` + string(rune('1'+i)) + `","kind":"entry","day":"` + day + `"}`
		if w := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodPost, "/apps/log/records", strings.NewReader(body))); w.Code != 200 {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
	}
	get := func(path string) (int, string) {
		w := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, path, nil))
		return w.Code, w.Body.String()
	}
	q := func(s string) string { return url.QueryEscape(s) }

	if code, body := get("/apps/log/records"); code != 200 || !strings.HasPrefix(strings.TrimSpace(body), "[") || strings.Count(body, `"kind"`) != 4 {
		t.Errorf("a list with no query: %d %s", code, body)
	}
	code, body := get("/apps/log/records?q=" + q(`{"where":{"day":{">=":"2026-11-10"}},"order":"-day","limit":2}`))
	var page struct {
		Records []map[string]any `json:"records"`
		Next    string           `json:"next"`
	}
	json.Unmarshal([]byte(body), &page)
	if code != 200 || len(page.Records) != 2 || page.Records[0]["id"] != "r4" || page.Next != "r3" {
		t.Errorf("a query: %d %s", code, body)
	}
	if code, body := get("/apps/log/records?q=" + q(`{"order":"-day","limit":2,"after":"r3"}`)); code != 200 || !strings.Contains(body, `"r2"`) || !strings.Contains(body, `"r1"`) {
		t.Errorf("the next page: %d %s", code, body)
	}
	if code, body := get("/apps/log/records?count=" + q(`{"day":{"<":"2026-11-11"}}`)); code != 200 || !strings.Contains(body, `"count":2`) {
		t.Errorf("a count: %d %s", code, body)
	}
	if code, body := get("/apps/log/records?q=" + q(`{"limit":9999}`)); code != 400 || !strings.Contains(body, "limit is 1 to 500") {
		t.Errorf("a bad query: %d %s", code, body)
	}
	if code, body := get("/apps/log/shared/board?q=" + q(`{"where":{"x":1}}`)); code != 200 || !strings.Contains(body, `"records":[]`) {
		t.Errorf("a shared collection's query: %d %s", code, body)
	}
}

// window.app sends the query and hands back the records with where the next
// page starts; count and shared go the same way; no query is the old call.
func TestThePageHelperSendsAQuery(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	js := strings.TrimSuffix(strings.TrimPrefix(appPageHelper, "<script>"), "</script>")
	harness := `
var calls = [], window = {};
function resp(body) { return {ok: true, status: 200, text: function() { return Promise.resolve(JSON.stringify(body)); }}; }
global.fetch = function(u) {
  calls.push(u);
  if (u.indexOf("records?q=") === 0) return Promise.resolve(resp({records: [{id: "r4"}, {id: "r3"}], next: "r3"}));
  if (u.indexOf("records?count=") === 0) return Promise.resolve(resp({count: 7}));
  if (u.indexOf("shared/board?q=") === 0) return Promise.resolve(resp({records: [{id: "s1"}], next: ""}));
  return Promise.resolve(resp([{id: "all"}]));
};
` + js + `
var app = window.app, out = {};
app.records.list({order: "-day", limit: 2}).then(function(rs) {
  out.list = rs.length + ":" + rs[0].id + ":" + rs.next;
  return app.records.count({kind: "entry"});
}).then(function(n) {
  out.count = n;
  return app.shared("board", {where: {x: 1}});
}).then(function(rs) {
  out.shared = rs.length;
  return app.records.list();
}).then(function(rs) {
  out.plain = rs[0].id;
  console.log(JSON.stringify({out: out, calls: calls}));
});
`
	f := filepath.Join(t.TempDir(), "q.js")
	if err := os.WriteFile(f, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, b)
	}
	got := string(b)
	for _, want := range []string{`"list":"2:r4:r3"`, `"count":7`, `"shared":1`, `"plain":"all"`,
		`records?q=%7B%22order%22%3A%22-day%22%2C%22limit%22%3A2%7D`, `records?count=%7B%22kind%22%3A%22entry%22%7D`, `"records"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
}

// A data source or action that declares its reads is run with them, keyed by
// the app's record key, whether the page or a press runs it.
func TestTheHostRunsAScriptWithItsReads(t *testing.T) {
	T := sharingTestApp(t)
	reads := &recquery.Query{Where: map[string]any{"kind": "entry"}}
	spec := AppSpec{Slug: "log", Name: "Log", Owner: "alice", RecordKey: "ref",
		DataSources: []AppDataSource{{Name: "entries", Script: "x", Reads: reads}},
		Actions:     []AppAction{{Name: "tidy", Script: "x", Reads: reads}}}
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	var got []string
	runAppScript = func(j appscript.Job) (string, error) {
		if j.Reads != nil {
			got = append(got, j.Kind+":"+j.Reads.Where["kind"].(string)+":"+j.KeyField)
		}
		if j.Kind == "action" {
			return `{"message":"ok"}`, nil
		}
		return `[]`, nil
	}
	appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/log/data/entries", nil))
	appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodPost, "/apps/log/action/tidy", nil))
	if strings.Join(got, ",") != "data:entry:ref,action:entry:ref" {
		t.Errorf("runs carried %v", got)
	}
}
