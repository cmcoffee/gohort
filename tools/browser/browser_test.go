package browser

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/sourcehooks"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// testBrowser starts a Chromium of the test's own: GOHORT_TEST_CHROME names
// the binary, else one rod can find. Skipped when there is none, as on CI.
func testBrowser(t *testing.T) *rod.Browser {
	t.Helper()
	bin := os.Getenv("GOHORT_TEST_CHROME")
	if bin == "" {
		found, ok := launcher.LookPath()
		if !ok {
			t.Skip("no Chromium to drive: set GOHORT_TEST_CHROME to run this")
		}
		bin = found
	}
	// The launcher's own profile folder, removed by Cleanup once Chromium
	// has exited (a t.TempDir would be removed while it still writes there).
	l := launcher.New().Bin(bin).Headless(true).Set("disable-gpu").Set("disable-dev-shm-usage")
	u, err := l.Launch()
	if err != nil {
		t.Skipf("Chromium would not start: %v", err)
	}
	t.Cleanup(func() {
		l.Kill()
		l.Cleanup()
	})
	b := rod.New().ControlURL(u)
	if err := b.Connect(); err != nil {
		t.Skipf("Chromium would not connect: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

// A page whose scripts never let go of the renderer loads, and then answers
// nothing, so reading it waited until the outer budget gave up (45s), with
// the tab left spinning a core. Now the read is bounded and says why it
// failed, and the scripts-off render reads what the server sent. A page whose
// scripts finish is read with them on, as before.
func TestAPageWhoseScriptsNeverFinishIsReadWithScriptsOff(t *testing.T) {
	b := testBrowser(t)
	prev := sourcehooks.HTTPRequestTimeout
	// read 2s, idle 3s, budget 18s: room for a slow runner, whose first page
	// in a new Chromium has taken 7s, to make both renders in one budget.
	sourcehooks.HTTPRequestTimeout = 6 * time.Second
	t.Cleanup(func() { sourcehooks.HTTPRequestTimeout = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/busy":
			w.Write([]byte(`<html><body><article><h1>Held page</h1><p>The article text the server sent.</p></article><script>for(;;){}</script></body></html>`))
		default:
			w.Write([]byte(`<html><body><p id="x">server</p><script>document.getElementById('x').textContent='script ran'</script></body></html>`))
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), browsePageTotalBudget())
	defer cancel()

	// errScriptsHeld comes only from the bounded read, while the budget still
	// runs: an unbounded read would end with the budget's own deadline.
	if _, _, err := renderPage(ctx, b, srv.URL+"/busy", true); !errors.Is(err, errScriptsHeld) {
		t.Fatalf("a page whose scripts never finish: %v", err)
	}
	html, _, err := renderPage(ctx, b, srv.URL+"/busy", false)
	if err != nil || !strings.Contains(html, "The article text the server sent.") {
		t.Fatalf("scripts off: %v\n%s", err, html)
	}
	html, _, err = renderPage(ctx, b, srv.URL+"/ok", true)
	if err != nil || !strings.Contains(html, "script ran") {
		t.Fatalf("a page whose scripts finish is read with them on: %v\n%s", err, html)
	}
}
