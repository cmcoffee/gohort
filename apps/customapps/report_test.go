package customapps

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/tools/appscript"
)

func reportSpec() AppSpec {
	return AppSpec{Slug: "wx", Name: "Weather", Owner: "alice", RecordKey: "id",
		Page:        json.RawMessage(`{"title":"Weather","sections":[]}`),
		DataSources: []AppDataSource{{Name: "forecast", Script: "x"}}}
}

func postReport(t *testing.T, T *CustomApps, user, msg string, diag bool) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(reportIn{Message: msg, Diagnostics: diag})
	return appAs(t, T, reportSpec(), user, httptest.NewRequest(http.MethodPost, "/apps/wx/_report", strings.NewReader(string(b))))
}

func listReports(t *testing.T, T *CustomApps, user string) (int, []map[string]any) {
	t.Helper()
	w := appAs(t, T, reportSpec(), user, httptest.NewRequest(http.MethodGet, "/apps/wx/_reports/list", nil))
	var rows []map[string]any
	json.Unmarshal(w.Body.Bytes(), &rows)
	return w.Code, rows
}

// Someone the app is shared with reports a problem: it is kept for the
// owner and lands on their bell naming who sent it. The owner has Builder
// instead, an empty report says nothing, and a day's reports are capped.
func TestSomeoneItIsSharedWithReportsAProblem(t *testing.T) {
	T := sharingTestApp(t)
	prev := agenthost.Get()
	t.Cleanup(func() { agenthost.Register(prev) })
	agenthost.Register(&orchestrate.OrchestrateApp{})
	w := postReport(t, T, "bob", "The forecast is blank", false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Sent to the app's owner") {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	reports := T.loadReports("alice", "wx")
	if len(reports) != 1 || reports[0].Reporter != "bob" || reports[0].Message != "The forecast is blank" || reports[0].Diag.State != "" {
		t.Fatalf("kept for the owner: %+v", reports)
	}
	ns := notices.List(RootDB, "alice")
	if len(ns) != 1 || ns[0].Title != "bob reported a problem with Weather" || !strings.Contains(ns[0].Body, "The forecast is blank") ||
		!strings.Contains(ns[0].Body, "/wx/_reports") {
		t.Errorf("the owner's notice names the reporter and links the report: %+v", ns)
	}
	if len(ns) == 1 && (!strings.Contains(ns[0].Body, "Fix with Builder: ") || !strings.Contains(ns[0].Body, "intake.action=Fix") ||
		!strings.Contains(ns[0].Body, "intake.problem=bob+reported%3A+The+forecast+is+blank")) {
		t.Errorf("the owner's notice links straight to Builder with what was written: %s", ns[0].Body)
	}
	if w := postReport(t, T, "alice", "mine", false); w.Code != http.StatusForbidden {
		t.Errorf("the owner changes it with Builder, not a report: %d", w.Code)
	}
	if w := postReport(t, T, "bob", "   ", false); w.Code != http.StatusBadRequest {
		t.Errorf("an empty report: %d", w.Code)
	}
	postReport(t, T, "bob", "second", false)
	postReport(t, T, "bob", "third", false)
	if w := postReport(t, T, "bob", "fourth", false); w.Code != http.StatusTooManyRequests {
		t.Errorf("a fourth report in a day: %d %s", w.Code, w.Body.String())
	}
	if w := postReport(t, T, "carol", "mine too", false); w.Code != 200 {
		t.Errorf("another person's cap is their own: %d", w.Code)
	}
	// A person with a display name is named by it, with their account.
	AuthDB().Set(AuthTable, "user:dave", AuthUser{Username: "dave", DisplayName: "Dave Reader", Apps: []string{"/apps"}})
	postReport(t, T, "dave", "it is slow", false)
	found := false
	for _, n := range notices.List(RootDB, "alice") {
		found = found || n.Title == "Dave Reader (dave) reported a problem with Weather"
	}
	if !found {
		t.Errorf("the notice names the reporter by their display name: %+v", notices.List(RootDB, "alice"))
	}
}

// The header offers "Report a problem" to the people the app is shared
// with, and only them: the owner and maintainers get Builder, and the owner
// sees in the header that problems wait.
func TestTheReportLinkIsForThoseWhoCannotChangeIt(t *testing.T) {
	T := sharingTestApp(t)
	page := func(user string) string {
		w := appAs(t, T, reportSpec(), user, httptest.NewRequest(http.MethodGet, "/apps/wx/", nil))
		if w.Code != 200 {
			t.Fatalf("%s's page: %d", user, w.Code)
		}
		return w.Body.String()
	}
	bob := page("bob")
	if !strings.Contains(bob, `"action":"`+reportNavAction+`"`) || !strings.Contains(bob, "uiRegisterClientAction('"+reportNavAction+"'") {
		t.Error("someone it is shared with gets the link and its dialog")
	}
	if a := page("alice"); strings.Contains(a, reportNavAction) || strings.Contains(a, "problem was reported") {
		t.Error("the owner gets no report link, and nothing waits yet")
	}
	postReport(t, T, "bob", "The forecast is blank", false)
	if !strings.Contains(page("alice"), "A problem was reported: see it") {
		t.Error("the owner's header says a report waits")
	}
	if canReport(AppSpec{Maintainers: []string{"dave"}}, "alice", "dave") {
		t.Error("a maintainer changes it with Builder")
	}
}

// The owner reads the reports, closes and deletes them; nobody else reaches
// the list.
func TestTheOwnerReadsClosesAndDeletesReports(t *testing.T) {
	T := sharingTestApp(t)
	postReport(t, T, "bob", "The forecast is blank", false)
	if code, _ := listReports(t, T, "bob"); code != http.StatusNotFound {
		t.Errorf("the reporter reaches the owner's list: %d", code)
	}
	code, rows := listReports(t, T, "alice")
	if code != 200 || len(rows) != 1 || rows[0]["reporter"] != "bob" || rows[0]["status"] != "open" || rows[0]["diag"] != "none" {
		t.Fatalf("the owner's list: %d %v", code, rows)
	}
	id := rows[0]["id"].(string)
	w := appAs(t, T, reportSpec(), "alice", httptest.NewRequest(http.MethodPost, "/apps/wx/_reports/"+id+"/close", strings.NewReader(`{"closed":true}`)))
	if w.Code != 200 {
		t.Fatalf("close: %d %s", w.Code, w.Body.String())
	}
	if _, rows = listReports(t, T, "alice"); rows[0]["status"] != "closed" {
		t.Errorf("closed: %v", rows[0])
	}
	if w := appAs(t, T, reportSpec(), "bob", httptest.NewRequest(http.MethodDelete, "/apps/wx/_reports/"+id, nil)); w.Code != http.StatusNotFound {
		t.Errorf("the reporter deletes it: %d", w.Code)
	}
	if w := appAs(t, T, reportSpec(), "alice", httptest.NewRequest(http.MethodDelete, "/apps/wx/_reports/"+id, nil)); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, rows = listReports(t, T, "alice"); len(rows) != 0 {
		t.Errorf("deleted: %v", rows)
	}
}

// With diagnostics, the app is loaded again as the reporter, in a scratch
// store holding a copy of their records and saved view, and the assistant
// reads what that found, their failed runs and the screenshot. The owner
// gets all of it, and the screenshot is theirs alone.
func TestADiagnosisLoadsTheAppAsTheReporter(t *testing.T) {
	T := sharingTestApp(t)
	prevStart, prevBrowser, prevAddr := startReportDiagnosis, BrowserCheckPage, WebListenAddr
	t.Cleanup(func() { startReportDiagnosis, BrowserCheckPage, WebListenAddr = prevStart, prevBrowser, prevAddr })
	startReportDiagnosis = func(fn func()) { fn() }
	WebListenAddr = "127.0.0.1:18999"

	spec := reportSpec()
	SaveAppSpec(spec)
	bobDB := T.recordBase(spec, "bob")
	bobDB.Set(appscript.RecordsTable("wx"), "r1", map[string]any{"id": "r1", "city": "Oslo"})
	bobDB.Set(viewStateTable, "wx", map[string]string{"tab": "week"})
	recordRun("alice", "wx", "data", "forecast", "bob", true, errors.New("KeyError: 'temp'"))

	var target string
	var seeded []map[string]any
	var view map[string]string
	BrowserCheckPage = func(tg string, cookies []PageCheckCookie, _ string, _ bool) (*PageCheckReport, error) {
		target = tg
		for _, c := range cookies {
			if c.Name == appscript.CheckScratchCookie {
				if sc, ok := appscript.CheckScratchFor(c.Value); ok {
					seeded = appscript.ReadRecords(sc.DB, "wx")
					view = loadViewState(sc.DB, "wx")
				}
			}
		}
		return &PageCheckReport{
			PageErrors:      []string{"TypeError: rows is undefined"},
			FailedRequests:  []string{"/apps/wx/data/forecast → HTTP 500"},
			PendingRequests: []string{"http://127.0.0.1:18999/api/changes?wait=1", "http://127.0.0.1:18999/apps/wx/data/slow"},
			BodyText:        "Weather",
			FrameTexts:      []string{"No forecast"},
			Screenshot:      []byte{0xff, 0xd8, 0x01},
		}, nil
	}
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "The forecast data source fails with KeyError 'temp'."}}}
	T.LLM = fake

	if w := postReport(t, T, "bob", "The forecast is blank", true); w.Code != 200 || !strings.Contains(w.Body.String(), "looked over") {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	if !strings.HasSuffix(target, "/wx/") {
		t.Errorf("loaded %q, want the app's page", target)
	}
	if len(seeded) != 1 || seeded[0]["city"] != "Oslo" || view["tab"] != "week" {
		t.Errorf("the check sees a copy of the reporter's records and view: %v %v", seeded, view)
	}
	if appscript.ReadRecords(bobDB, "wx")[0]["city"] != "Oslo" {
		t.Error("the reporter's own records are untouched")
	}

	sent := fake.LastSent()
	if len(sent) != 1 || len(sent[0].Images) != 1 {
		t.Fatalf("the assistant gets the evidence and the screenshot: %+v", sent)
	}
	p := sent[0].Content
	for _, want := range []string{"UNTRUSTED: report", "The forecast is blank", "TypeError: rows is undefined", "HTTP 500", "No forecast", "KeyError: 'temp'", `"tab":"week"`} {
		if !strings.Contains(p, want) {
			t.Errorf("the assistant's evidence lacks %q", want)
		}
	}

	_, rows := listReports(t, T, "alice")
	if len(rows) != 1 {
		t.Fatalf("rows: %v", rows)
	}
	r := rows[0]
	if r["diag"] != "done" || r["analysis"] != "The forecast data source fails with KeyError 'temp'." || r["has_shot"] != "1" ||
		!strings.Contains(r["page_errors"].(string), "TypeError") || !strings.Contains(r["run_failures"].(string), "KeyError") {
		t.Errorf("the owner's row: %v", r)
	}
	if fr := r["failed_requests"].(string); !strings.Contains(fr, "/apps/wx/data/slow") || strings.Contains(fr, "api/changes") {
		t.Errorf("only the app's own requests count as still waiting: %q", fr)
	}
	shot := r["shot_url"].(string) // the mounted path, /apps/wx/...
	if !strings.HasPrefix(shot, "/apps/wx/_reports/") {
		t.Fatalf("shot url %q", shot)
	}
	if w := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, shot, nil)); w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || w.Body.Len() != 3 {
		t.Errorf("the owner's screenshot: %d %q %d", w.Code, w.Header().Get("Content-Type"), w.Body.Len())
	}
	if w := appAs(t, T, spec, "bob", httptest.NewRequest(http.MethodGet, shot, nil)); w.Code != http.StatusNotFound {
		t.Errorf("the screenshot is the owner's alone: %d", w.Code)
	}
}

// A diagnosis that cannot load the app still reaches the owner, saying why.
func TestADiagnosisWithNoBrowserSaysSo(t *testing.T) {
	T := sharingTestApp(t)
	prevStart, prevBrowser := startReportDiagnosis, BrowserCheckPage
	t.Cleanup(func() { startReportDiagnosis, BrowserCheckPage = prevStart, prevBrowser })
	startReportDiagnosis = func(fn func()) { fn() }
	BrowserCheckPage = nil
	postReport(t, T, "bob", "The forecast is blank", true)
	_, rows := listReports(t, T, "alice")
	if len(rows) != 1 || rows[0]["diag"] != "failed" || !strings.Contains(rows[0]["diag_note"].(string), "no headless browser") ||
		!strings.Contains(rows[0]["analysis"].(string), "No diagnosis: no model") {
		t.Errorf("row: %v", rows)
	}
}

// Fix with Builder opens Builder's Fix form on this app with the report as
// the problem.
func TestAReportGoesToBuilderAsAFix(t *testing.T) {
	prev := agenthost.Get()
	t.Cleanup(func() { agenthost.Register(prev) })
	agenthost.Register(&orchestrate.OrchestrateApp{})
	u := reportBuilderURL("wx", appReport{Reporter: "bob", Message: "The forecast is blank",
		Diag: reportDiag{Analysis: "The forecast source fails.", PageErrors: []string{"TypeError"}}})
	for _, want := range []string{"agent=seed-builder", "intake.action=Fix", "intake.kind=App", "intake.target=wx", "intake.problem=bob+reported%3A+The+forecast+is+blank", "The+forecast+source+fails", "TypeError"} {
		if !strings.Contains(u, want) {
			t.Errorf("Builder link lacks %q: %s", want, u)
		}
	}
}

// The check a diagnosis runs loads the app as the reporter; nothing that
// check does may file a report of its own.
func TestAPageCheckCannotReportAProblem(t *testing.T) {
	T := sharingTestApp(t)
	token, end := appscript.BeginCheckScratch("alice", "wx", "id", nil, nil)
	defer end()
	r := httptest.NewRequest(http.MethodPost, "/apps/wx/_report", strings.NewReader(`{"message":"from a check"}`))
	r.AddCookie(&http.Cookie{Name: appscript.CheckScratchCookie, Value: token})
	if w := appAs(t, T, reportSpec(), "bob", r); w.Code == 200 {
		t.Fatalf("a page check filed a report: %s", w.Body.String())
	}
	if n := len(T.loadReports("alice", "wx")); n != 0 {
		t.Errorf("%d reports kept from a check", n)
	}
}

// The dialog, run in node against a stand-in page: it waits for the runtime,
// refuses an empty message, sends the message and the box's answer, says what
// the server said, and on a refusal keeps the message and says why.
func TestTheReportDialogSendsWhatWasWritten(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	js := strings.TrimSuffix(strings.TrimPrefix(reportDialogJS, "<script>"), "</script>")
	harness := `
var calls = [], ready = [], opened = null, reply = null;
function node(tag) {
  var n = {tag: tag, kids: [], style: {}, attrs: {}, on: {}, _t: '', className: '', value: '', checked: false,
    appendChild: function(k) { this.kids.push(k); k.parent = this; return k; },
    setAttribute: function(k, v) { this.attrs[k] = v; },
    addEventListener: function(e, f) { this.on[e] = f; },
    remove: function() { if (this.parent) this.parent.kids.splice(this.parent.kids.indexOf(this), 1); this.removed = true; },
    focus: function() {}};
  // Setting textContent drops the children, as it does in a page.
  Object.defineProperty(n, 'textContent', {get: function() { return n._t; }, set: function(v) { n._t = v; n.kids = []; }});
  Object.defineProperty(n, 'text', {get: function() { return (n._t + ' ' + n.kids.map(function(k) { return k.text; }).join(' ')).trim(); }});
  return n;
}
global.document = {readyState: 'loading', createElement: node, addEventListener: function(e, f) { ready.push(f); }};
global.window = {UIClientActions: {}};
global.fetch = function(u, o) { calls.push(o.method + ' ' + u + ' ' + o.body); return Promise.resolve(reply); };
` + js + `
var out = {};
out.beforeRuntime = Object.keys(window.UIClientActions).length;
window.uiRegisterClientAction = function(n, f) { window.UIClientActions[n] = f; };
window.uiOpenModal = function(o) { opened = o; };
ready.forEach(function(f) { f(); });
window.UIClientActions['` + reportNavAction + `']({});
var body = node('div'), cancel = node('button'), footer = [];
cancel.textContent = 'Cancel'; cancel.className = 'ui-row-btn primary';
opened.mount(body, {primaryButton: cancel, addAction: function(b) { footer.push(b); cancel.className = 'ui-row-btn'; return b; }});
var box = body.kids[0], label = body.kids[1], err = body.kids[2], tick = label.kids[0], send = footer[0];
out.title = opened.title; out.cancel = cancel.textContent; out.send = send && send.textContent; out.ticked = tick.checked;
send.on.click();
out.empty = err.textContent; out.emptyCalls = calls.length;
box.value = 'The list is empty'; tick.checked = false;
reply = {ok: false, status: 429, text: function() { return Promise.resolve('you have reported 3 problems with this app today'); }};
send.on.click();
setTimeout(function() {
  out.refused = err.textContent; out.sendBack = send.textContent + '/' + send.disabled;
  reply = {ok: true, status: 200, json: function() { return Promise.resolve({said: 'Sent to the app owner.'}); }};
  send.on.click();
  setTimeout(function() {
    out.calls = calls; out.done = body.text; out.sendGone = !!send.removed; out.closeNow = cancel.textContent + '/' + cancel.className;
    console.log(JSON.stringify(out));
  }, 5);
}, 5);
`
	f := filepath.Join(t.TempDir(), "dialog.js")
	if err := os.WriteFile(f, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, b)
	}
	var out struct {
		BeforeRuntime              int
		Title, Cancel, Send, Empty string
		Ticked                     bool
		EmptyCalls                 int
		Refused, SendBack          string
		Calls                      []string
		Done, CloseNow             string
		SendGone                   bool
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if out.BeforeRuntime != 0 || out.Title != "Report a problem" || out.Cancel != "Cancel" || out.Send != "Send" || !out.Ticked {
		t.Errorf("the dialog as it opens: %+v", out)
	}
	if out.EmptyCalls != 0 || out.Empty == "" {
		t.Errorf("an empty message is not sent: %+v", out)
	}
	if !strings.Contains(out.Refused, "Not sent: you have reported 3 problems") || out.SendBack != "Send/false" {
		t.Errorf("a refusal says why and lets them try again: %+v", out)
	}
	if len(out.Calls) != 2 || out.Calls[1] != `POST _report {"message":"The list is empty","diagnostics":false}` {
		t.Errorf("what it sent: %q", out.Calls)
	}
	if out.Done != "Sent to the app owner." || !out.SendGone || out.CloseNow != "Close/ui-row-btn primary" {
		t.Errorf("after sending: %+v", out)
	}
}
