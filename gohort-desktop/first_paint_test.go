package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// Every document the DESKTOP serves itself has to be themed before its first
// frame. The window's own colour (WINDOW_BG_*) sits BEHIND the webview, so it
// covers the gaps where nothing is on screen — it cannot reach the first frame
// of a document that is on screen but hasn't painted its background yet. There
// the browser uses its default canvas, which is white.
//
// The bounce bodies are the ones that matter: they exist for a single frame in
// the middle of a navigation, which is exactly when someone is looking.
func TestDesktopServedPagesArePaintedBeforeFirstFrame(t *testing.T) {
	head := core.FirstPaintHead()
	for name, doc := range map[string]string{
		"apikey_page_html":        apikey_page_html,
		"redirect_bounce_html":    redirect_bounce_html,
		"GOHORT_NOT_RUNNING_HTML": GOHORT_NOT_RUNNING_HTML,
	} {
		if !strings.Contains(doc, head) {
			t.Errorf("%s does not carry core.FirstPaintHead(); it will flash white mid-navigation", name)
		}
		// Before any other style, or the browser has already picked a canvas.
		if style := strings.Index(doc, "<style>"); style >= 0 && strings.Index(doc, head) > style {
			t.Errorf("%s puts its own <style> ahead of the first-paint head", name)
		}
	}
}

// configure.html states the same two properties by hand (it's an asset, not a
// Go string). It only has to declare the scheme — the check is that nobody
// removes it while tidying the file.
func TestConfigurePageDeclaresColorScheme(t *testing.T) {
	b, err := assets.ReadFile("frontend/configure.html")
	if err != nil {
		t.Fatalf("read configure.html: %v", err)
	}
	if !strings.Contains(string(b), `<meta name="color-scheme"`) {
		t.Error("configure.html has no color-scheme; its form controls render light on a dark page and it flashes white on the way in")
	}
}

// The shim is 35 KB of inline script. Injected at the TOP of the head — where
// it used to go — it sits ahead of the page's <meta charset> and ahead of the
// first-paint theme block the server emits, so the parser runs all of it
// before the browser knows what colour the canvas is. That is a white frame on
// every navigation inside the webview, and it is invisible in a plain browser
// because nothing is injected there.
func TestPopupShimGoesInAfterTheHeadIsThemed(t *testing.T) {
	page := `<!doctype html><html><head><meta charset="utf-8">` +
		`<meta name="color-scheme" content="dark">` +
		`<style>html,body{background:#0f1117}</style>` +
		`<title>x</title></head><body>hi</body></html>`

	got := run_shim_injection(t, page, "text/html", 200)

	shim := strings.Index(got, "<script>")
	if shim < 0 {
		t.Fatal("shim was not injected at all")
	}
	for _, must_precede := range []string{`<meta charset="utf-8">`, `name="color-scheme"`, "<style>"} {
		at := strings.Index(got, must_precede)
		if at < 0 {
			t.Fatalf("%s vanished from the page", must_precede)
		}
		if at > shim {
			t.Errorf("shim is injected ahead of %s; the page paints white until the script finishes", must_precede)
		}
	}
	if strings.Index(got, "</head>") < shim {
		t.Error("shim landed outside the head")
	}
	// The charset has to stay inside the window the browser actually scans.
	if at := strings.Index(got, `<meta charset="utf-8">`); at > 1024 {
		t.Errorf("charset moved to byte %d; browsers stop looking after 1024", at)
	}
}

// A fragment (no head) is served untouched — injecting into an AJAX response
// would corrupt it.
func TestPopupShimLeavesFragmentsAlone(t *testing.T) {
	frag := `<div class="row">just a fragment</div>`
	if got := run_shim_injection(t, frag, "text/html", 200); got != frag {
		t.Errorf("fragment was modified: %q", got)
	}
}

// run_shim_injection pushes one body through inject_popup_shim and returns the
// body the webview would receive.
func run_shim_injection(t *testing.T, body, content_type string, status int) string {
	t.Helper()
	resp := &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{content_type}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	if err := inject_popup_shim(resp); err != nil {
		t.Fatalf("inject_popup_shim: %v", err)
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read injected body: %v", err)
	}
	return string(out)
}
