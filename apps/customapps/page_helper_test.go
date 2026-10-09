package customapps

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// window.app, run in node against a stand-in fetch: the URLs it builds, the
// server's message on a failure, and onChange firing on a change and not on
// its first answer.
func TestThePageHelperCallsTheAppsOwnEndpoints(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	js := strings.TrimSuffix(strings.TrimPrefix(appPageHelper, "<script>"), "</script>")
	harness := `
var calls = [], window = {};
var version = 0;
function resp(status, body) { return {ok: status < 400, status: status, text: function() { return Promise.resolve(body); }}; }
global.fetch = function(u, o) {
  calls.push((o && o.method || "GET") + " " + u + (o && o.body ? " " + o.body : ""));
  if (u.indexOf("data/missing") === 0) return Promise.resolve(resp(404, "no data source named missing"));
  if (u.indexOf("changes") === 0) { var v = version; return new Promise(function(r) { setTimeout(function() { r(resp(200, JSON.stringify({shared: "e." + v, records: "e.0"}))); }, 5); }); }
  return Promise.resolve(resp(200, "{\"ok\":true,\"text\":\"hi\"}"));
};
` + js + `
var app = window.app, fired = 0, out = {};
app.data("weather", {city: "Santa Cruz", n: 2}).then(function() {
  return app.action("submit-score", {score: 3});
}).then(function() {
  return app.records.remove("ab 1");
}).then(function() {
  return app.ask("Say hi", {json: true});
}).then(function(text) {
  out.ask = text;
  return app.data("missing").catch(function(e) { out.err = e.message; });
}).then(function() {
  var stop = app.onChange(function() { fired++; });
  setTimeout(function() { version = 1; }, 30);
  setTimeout(function() { stop(); console.log(JSON.stringify({calls: calls, fired: fired, out: out, asset: app.asset("rain.svg")})); }, 120);
});
`
	f := filepath.Join(t.TempDir(), "h.js")
	if err := os.WriteFile(f, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, b)
	}
	got := string(b)
	for _, want := range []string{
		`GET data/weather?city=Santa%20Cruz&n=2`,
		`POST action/submit-score {\"score\":3}`,
		`DELETE record?id=ab%201`,
		`POST ask {\"prompt\":\"Say hi\",\"json\":true}`,
		`"err":"no data source named missing"`,
		`"ask":"hi"`,
		`"asset":"assets/rain.svg"`,
		`GET changes?shared=&records=`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if !strings.Contains(got, `"fired":1`) {
		t.Errorf("onChange should fire once, for the one change, not for its first answer:\n%s", got)
	}
}

// Every frame an app's page renders in carries the helper, set at serve time
// so an app built before it existed has it too.
func TestEveryAppFrameCarriesTheHelper(t *testing.T) {
	page := []byte(`{"sections":[{"body":{"type":"frame","html":"<p>x</p>"}},{"body":{"type":"card","html":"<b>y</b>"}}]}`)
	out := string(isolateAppHTML(page, "/apps/wx/"))
	if strings.Count(out, `"isolate_prelude"`) != 2 || !strings.Contains(out, "window.app=") {
		t.Fatalf("served page:\n%s", out)
	}
}

// The helper's dialogs are the page's: a question an app asks is shown by
// the page the app sits in, as gohort's own modal, not by the browser.
func TestThePageHelpersDialogsAreThePages(t *testing.T) {
	for _, want := range []string{
		`confirm:function(m){return window.uiConfirm(m);}`,
		`alert:function(m){return window.uiAlert(m);}`,
		`prompt:function(m,d){return window.uiPrompt(m,d);}`,
	} {
		if !strings.Contains(appPageHelper, want) {
			t.Errorf("window.app lacks %s", want)
		}
	}
}
