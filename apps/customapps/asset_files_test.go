package customapps

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// An app's page loads its own scripts and stylesheets as files in its
// version's folder: every other attribute as written (a module stays a
// module, a deferred script deferred, a stylesheet's media kept), and
// anything not one of the app's own assets, or missing, left as written.
func TestAnAppsOwnScriptsAndStylesheetsLoadAsFiles(t *testing.T) {
	have := map[string]bool{"app.js": true, "late.js": true, "mod.mjs": true, "site.css": true}
	html := `<html><head><link rel="stylesheet" href="assets/site.css" media="screen"><link href="https://cdn.example/x.css" rel="stylesheet"></head><body>
<script src="assets/app.js"></script>
<script defer src="./assets/late.js"></script>
<script type="module" src="assets/mod.mjs"></script>
<script src="https://cdn.example/react.js"></script>
<script src="assets/missing.js"></script>
<p>end</p></body></html>`
	page, _ := json.Marshal(map[string]any{"sections": []any{map[string]any{"body": map[string]any{"type": "frame", "html": html}}}})
	var out struct {
		Sections []struct {
			Body struct {
				HTML string `json:"html"`
			} `json:"body"`
		} `json:"sections"`
	}
	base := "/apps/_files/ID/v1/"
	json.Unmarshal(linkAssetsWith(page, base, func(n string) bool { return have[n] }), &out)
	got := out.Sections[0].Body.HTML
	for _, want := range []string{
		`<link rel="stylesheet" href="/apps/_files/ID/v1/site.css" media="screen">`,
		`href="https://cdn.example/x.css"`,
		`<script src="/apps/_files/ID/v1/app.js"></script>`,
		`<script defer src="/apps/_files/ID/v1/late.js"></script>`,
		`<script type="module" src="/apps/_files/ID/v1/mod.mjs"></script>`,
		`<script src="https://cdn.example/react.js"></script>`,
		`<script src="assets/missing.js"></script>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}

	// A page with no asset reference is served as it was, byte for byte.
	plain := json.RawMessage(`{"sections":[{"body":{"type":"frame","html":"<p>hi</p>"}}]}`)
	if string(linkAssetsWith(plain, base, func(string) bool { return true })) != string(plain) {
		t.Error("a page with no assets was rewritten")
	}
}

// The files are served with no session, kept by the browser, readable by a
// module in the frame's null origin, and only under the app's current
// version: a change to any script or stylesheet is a new folder, and the old
// one is gone. Nothing but scripts and stylesheets is served there, and
// nothing of an app that is switched off.
func TestAnAppsFilesAreServedUnderItsCurrentVersionOnly(t *testing.T) {
	prev := AppAssetsDir()
	SetAppAssetsDir(t.TempDir())
	t.Cleanup(func() { SetAppAssetsDir(prev) })
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "game", Name: "Game", Owner: "alice", RecordKey: "id"}
	SaveAppSpec(spec)
	for name, body := range map[string]string{"main.js": "import './chunk.js';", "chunk.js": "window.x = 1;", "site.css": "body{}", "hero.png": "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"} {
		if _, err := SaveAppAsset("alice", "game", name, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	page := json.RawMessage(`{"sections":[{"body":{"type":"frame","html":"<script type=\"module\" src=\"assets/main.js\"></script>"}}]}`)
	linked := string(T.linkAppFiles(page, "alice", "game"))
	i := strings.Index(linked, appFilesPath)
	if i < 0 {
		t.Fatalf("the page does not load its script as a file: %s", linked)
	}
	folder := linked[i : i+strings.Index(linked[i:], "main.js")]
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		T.handleAppFiles(w, httptest.NewRequest(http.MethodGet, p, nil))
		return w
	}

	w := get(folder + "chunk.js")
	if w.Code != 200 || w.Body.String() != "window.x = 1;" {
		t.Fatalf("a module's chunk in the same folder: %d %s", w.Code, w.Body.String())
	}
	h := w.Header()
	if h.Get("Content-Type") != "text/javascript; charset=utf-8" || !strings.Contains(h.Get("Cache-Control"), "immutable") ||
		h.Get("Access-Control-Allow-Origin") != "*" || !strings.HasPrefix(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("headers %v", h)
	}
	if w := get(folder + "site.css"); w.Code != 200 || w.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Errorf("the stylesheet: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get(folder + "hero.png"); w.Code != 404 {
		t.Errorf("an image was served without a session: %d", w.Code)
	}
	if w := get(folder + "missing.js"); w.Code != 404 {
		t.Errorf("a missing script: %d", w.Code)
	}
	if w := get(strings.Replace(folder, "/"+appFilesID("alice", "game")+"/", "/"+appFilesID("bob", "game")+"/", 1) + "chunk.js"); w.Code != 404 {
		t.Errorf("another person's app under this version: %d", w.Code)
	}

	// A change is a new version; the old folder serves nothing.
	SaveAppAsset("alice", "game", "chunk.js", []byte("window.x = 2;"))
	if w := get(folder + "chunk.js"); w.Code != 404 {
		t.Errorf("the old version still served after a change: %d %s", w.Code, w.Body.String())
	}
	relinked := string(T.linkAppFiles(page, "alice", "game"))
	if relinked == linked {
		t.Fatal("the page still points at the old version")
	}
	j := strings.Index(relinked, appFilesPath)
	if w := get(relinked[j:j+strings.Index(relinked[j:], "main.js")] + "chunk.js"); w.Code != 200 || w.Body.String() != "window.x = 2;" {
		t.Errorf("the new version: %d %s", w.Code, w.Body.String())
	}

	// Switched off, nothing of it is served.
	spec.Disabled = true
	SaveAppSpec(spec)
	if w := get(relinked[j:j+strings.Index(relinked[j:], "main.js")] + "chunk.js"); w.Code != 404 {
		t.Errorf("a switched-off app's script: %d", w.Code)
	}
}

// A real browser, inside a frame sandboxed the way an app's page is (scripts,
// no origin, no session), loads the app's module, the chunk the module
// imports, a classic script and a stylesheet from its files. This is what
// failed before (ORB on a classic script and a stylesheet, CORS on a module),
// so it is checked in Chromium, not only by the headers. Skipped where there
// is no Chromium, as on CI: GOHORT_TEST_CHROME names one.
func TestABrowserLoadsAnAppsFilesInItsSandboxedFrame(t *testing.T) {
	bin := os.Getenv("GOHORT_TEST_CHROME")
	if bin == "" {
		found, ok := launcher.LookPath()
		if !ok {
			t.Skip("no Chromium to drive: set GOHORT_TEST_CHROME to run this")
		}
		bin = found
	}
	prev := AppAssetsDir()
	SetAppAssetsDir(t.TempDir())
	t.Cleanup(func() { SetAppAssetsDir(prev) })
	T := sharingTestApp(t)
	SaveAppSpec(AppSpec{Slug: "game", Name: "Game", Owner: "alice", RecordKey: "id"})
	for name, body := range map[string]string{
		"main.js":    "import { n } from './chunk.js'; parent.postMessage('module:' + n + ':' + window.classic + ':' + getComputedStyle(document.body).color, '*');",
		"chunk.js":   "export const n = 7;",
		"classic.js": "window.classic = 'yes';",
		"site.css":   "body { color: rgb(1, 2, 3); }",
	} {
		if _, err := SaveAppAsset("alice", "game", name, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	inner := `<html><head><link rel="stylesheet" href="assets/site.css"></head><body><script src="assets/classic.js"></script><script type="module" src="assets/main.js"></script></body></html>`
	page, _ := json.Marshal(map[string]any{"sections": []any{map[string]any{"body": map[string]any{"type": "frame", "html": inner}}}})
	var linked struct {
		Sections []struct {
			Body struct {
				HTML string `json:"html"`
			} `json:"body"`
		} `json:"sections"`
	}
	json.Unmarshal(T.linkAppFiles(page, "alice", "game"), &linked)
	frame := linked.Sections[0].Body.HTML
	if !strings.Contains(frame, appFilesPath) {
		t.Fatalf("not linked: %s", frame)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(T.WebPath()+appFilesPath, T.handleAppFiles)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><body><script>addEventListener('message', function(e) { document.title = e.data; });</script>` +
			`<iframe sandbox="allow-scripts" srcdoc="` + html.EscapeString(frame) + `"></iframe></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	l := launcher.New().Bin(bin).Headless(true).Set("disable-gpu").Set("disable-dev-shm-usage")
	u, err := l.Launch()
	if err != nil {
		t.Skipf("Chromium would not start: %v", err)
	}
	t.Cleanup(func() { l.Kill(); l.Cleanup() })
	b := rod.New().ControlURL(u)
	if err := b.Connect(); err != nil {
		t.Skipf("Chromium would not connect: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	pg, err := b.Page(proto.TargetCreateTarget{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	title := ""
	for time.Now().Before(deadline) {
		if info, err := pg.Info(); err == nil {
			title = info.Title
		}
		if strings.HasPrefix(title, "module:") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if title != "module:7:yes:rgb(1, 2, 3)" {
		t.Errorf("the frame reported %q, want the module, its chunk, the classic script and the stylesheet all loaded", title)
	}
}

// The app's page, as served to the person opening it, loads its scripts from
// the files folder rather than carrying their text.
func TestTheServedPageLoadsItsScriptsAsFiles(t *testing.T) {
	prev := AppAssetsDir()
	SetAppAssetsDir(t.TempDir())
	t.Cleanup(func() { SetAppAssetsDir(prev) })
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "game", Name: "Game", Owner: "alice", RecordKey: "id",
		Page: json.RawMessage(`{"title":"Game","sections":[{"body":{"type":"frame","html":"<script src=\"assets/main.js\"></script>"}}]}`)}
	if _, err := SaveAppAsset("alice", "game", "main.js", []byte("window.secretish = 'text of the script';")); err != nil {
		t.Fatal(err)
	}
	body := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/game/", nil)).Body.String()
	if !strings.Contains(body, appFilesPath+appFilesID("alice", "game")+"/") {
		t.Errorf("the page does not load its script from the files folder")
	}
	if strings.Contains(body, "text of the script") {
		t.Error("the page still carries the script's text")
	}
}
