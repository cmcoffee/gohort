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

// A slow action through window.app: the first answer says it is running, the
// helper follows that job until it ends, hands each progress line to
// onProgress, and resolves with the outcome (or rejects with its error).
func TestThePageHelperFollowsALongAction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	js := strings.TrimSuffix(strings.TrimPrefix(appPageHelper, "<script>"), "</script>")
	harness := `
var calls = [], window = {}, polls = 0;
var realTimeout = setTimeout; global.setTimeout = function(fn) { return realTimeout(fn, 1); };
function resp(body) { return {ok: true, status: 200, text: function() { return Promise.resolve(JSON.stringify(body)); }}; }
global.fetch = function(u, o) {
  calls.push((o && o.method || "GET") + " " + u);
  if (u === "action/extract") return Promise.resolve(resp({running: true, job: "j1", progress: "starting"}));
  if (u === "action/broken") return Promise.resolve(resp({running: true, job: "j2", progress: "starting"}));
  if (u.indexOf("action-progress/extract") === 0) {
    polls++;
    if (polls < 3) return Promise.resolve(resp({running: true, job: "j1", progress: "part " + polls + " of 8"}));
    return Promise.resolve(resp({done: true, job: "j1", message: "CASE-1 extracted", saved: 1, records: [{id: "a"}], outcome: "CASE-1 extracted"}));
  }
  if (u.indexOf("action-progress/broken") === 0) return Promise.resolve(resp({done: true, job: "j2", error: "the archive is corrupt"}));
  return Promise.resolve(resp({ok: true}));
};
` + js + `
var seen = [], out = {};
window.app.action("extract", {case: "CASE-1"}, {onProgress: function(p) { seen.push(p); }}).then(function(r) {
  out.result = r;
  return window.app.action("broken").catch(function(e) { out.err = e.message; });
}).then(function() {
  return window.app.cancelAction("extract");
}).then(function() {
  console.log(JSON.stringify({calls: calls, seen: seen, out: out}));
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
		`GET action-progress/extract?job=j1`,
		`"seen":["starting","part 1 of 8","part 2 of 8"]`,
		`"message":"CASE-1 extracted"`,
		`"saved":1`,
		`"err":"the archive is corrupt"`,
		`POST action-cancel/extract`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
}

// app.coauthor, run in node through the frame's whole prelude (window.app,
// core/editor's diff and coauthor, the kit) against a small DOM: highlighting
// shows the chip, ask sends the document and the passage to propose, the
// change opens in the document's place, and Apply writes it and lets the
// passage go. A change whose passage is gone is refused.
func TestAPagesDocumentIsCoauthoredThroughTheKit(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	var scripts []string
	for rest := appPagePrelude; ; {
		i := strings.Index(rest, "<script>")
		if i < 0 {
			break
		}
		j := strings.Index(rest[i:], "</script>")
		scripts = append(scripts, rest[i+len("<script>"):i+j])
		rest = rest[i+j:]
	}
	if len(scripts) != 2 {
		t.Fatalf("the prelude has %d scripts, want the helper and the kit", len(scripts))
	}
	harness := `
function node(tag) {
  var n = {tagName: tag.toUpperCase(), children: [], style: {}, on: {}, attrs: {}, parentNode: null, textContent: "", className: ""};
  n.setAttribute = function(k, v) { n.attrs[k] = v; };
  n.addEventListener = function(t, f) { (n.on[t] = n.on[t] || []).push(f); };
  n.removeEventListener = function(t, f) { n.on[t] = (n.on[t] || []).filter(function(g) { return g !== f; }); };
  n.fire = function(t, e) { (n.on[t] || []).slice().forEach(function(f) { f(e || {type: t}); }); };
  n.dispatchEvent = function(e) { n.fire(e.type, e); };
  n.appendChild = function(c) { c.parentNode = n; n.children.push(c); return c; };
  n.insertBefore = function(c, ref) { c.parentNode = n; var i = ref ? n.children.indexOf(ref) : -1; if (i < 0) n.children.push(c); else n.children.splice(i, 0, c); return c; };
  n.removeChild = function(c) { n.children.splice(n.children.indexOf(c), 1); c.parentNode = null; };
  n.querySelector = function() { return null; };
  Object.defineProperty(n, "innerHTML", {set: function(v) { n.children = []; n.html = v; }, get: function() { return n.html || ""; }});
  return n;
}
function text(n) { return (n.textContent || "") + (n.html || "") + n.children.map(text).join(""); }
function find(n, pred) { if (pred(n)) return n; for (var i = 0; i < n.children.length; i++) { var f = find(n.children[i], pred); if (f) return f; } return null; }
var docListeners = {};
var document = {activeElement: null,
  createElement: node,
  createTextNode: function(s) { var t = node("#text"); t.textContent = s; return t; },
  addEventListener: function(t, f) { (docListeners[t] = docListeners[t] || []).push(f); },
  removeEventListener: function() {}};
global.window = global;
global.document = document;
global.Event = function(type) { this.type = type; };
var calls = [], answer = {};
function resp(body) { return {ok: true, status: 200, text: function() { return Promise.resolve(JSON.stringify(body)); }}; }
global.fetch = function(u, o) { calls.push(u + " " + (o && o.body || "")); return Promise.resolve(resp(answer)); };
// As a browser runs a <script>: its declarations are globals.
var vm = require("vm"), fs = require("fs");
vm.runInThisContext(fs.readFileSync(__dirname + "/helper.js", "utf8"));
vm.runInThisContext(fs.readFileSync(__dirname + "/kit.js", "utf8"));
var out = {};
var page = node("div"), ask = node("div"), ta = node("textarea");
page.appendChild(ta);
ta.value = "Verse one\nNeon on the water\nCity never sleeps\n";
ta.offsetHeight = 300;
ta.focus = function() {};
ta.setSelectionRange = function(s, e) { ta.selectionStart = s; ta.selectionEnd = e; };
var saw = [];
ta.addEventListener("input", function() { saw.push("input"); });
try { window.app.coauthor(node("div")); } catch (e) { out.notTextarea = e.message; }
var doc = window.app.coauthor(ta, {chip: ask, onSelect: function(s) { saw.push(s ? "sel:" + s.text : "sel:none"); }});
ta.selectionStart = 10; ta.selectionEnd = 27;
ta.fire("mouseup");
setTimeout(function() {
  out.chip = text(ask);
  out.selection = doc.selection();
  answer = {reply: "Darker.", edit: {find: "Neon on the water", replace: "Ash on the water", summary: "darker image"}};
  doc.ask("make it darker", {history: [{role: "user", text: "a night song"}]}).then(function(r) {
    out.ask = r;
    out.sent = calls[0];
    out.hidden = ta.style.display;
    var review = page.children[0];
    out.review = review.className + " | " + text(review).slice(0, 40);
    out.diff = text(review).indexOf("editor-diff-remove") >= 0 ? "editor-diff-remove" : "no diff";
    find(review, function(n) { return n.tagName === "BUTTON" && text(n) === "Apply"; }).fire("click");
    out.value = ta.value;
    out.shown = ta.style.display;
    out.chipAfter = text(ask);
    out.undo = text(page);
    answer = {reply: "Done.", edit: {find: "Stars on the river", replace: "x"}};
    return doc.ask("again");
  }).then(function(r) {
    out.gone = r;
    out.saw = saw;
    console.log(JSON.stringify(out));
  }).catch(function(e) { console.log("ERR " + e.stack); });
}, 5);
`
	dir := t.TempDir()
	for name, src := range map[string]string{"helper.js": scripts[0], "kit.js": scripts[1], "run.js": harness} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := filepath.Join(dir, "run.js")
	b, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, b)
	}
	got := string(b)
	for _, want := range []string{
		`"notTextarea":"app.coauthor takes a <textarea>"`,
		`"chip":"×Highlighted: “Neon on the water”"`,
		`"selection":{"text":"Neon on the water","start":10,"end":27}`,
		`propose {\"document\":\"Verse one\\nNeon on the water\\nCity never sleeps\\n\",\"prompt\":\"make it darker\",\"selection\":{\"text\":\"Neon on the water\",\"start\":10,\"end\":27},\"history\":[{\"role\":\"user\",\"text\":\"a night song\"}]}`,
		`"ask":{"reply":"Darker.","proposed":true,"reason":""}`,
		`"hidden":"none"`,
		`"review":"ui-coauthor-review ui-coauthor-review-inplace | darker image+1 −1ApplyReject`,
		`editor-diff-remove`,
		`"value":"Verse one\nAsh on the water\nCity never sleeps\n"`,
		`"shown":""`,
		`"chipAfter":""`,
		`Applied.Undo`,
		`"gone":{"reply":"Done.","proposed":false,"reason":"That passage is no longer in the document`,
		`"saw":["sel:Neon on the water","input","sel:none"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
}
