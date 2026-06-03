// Package browser provides a headless-browser fetch tool backed by go-rod.
// On first use, go-rod auto-downloads a compatible Chromium build; subsequent
// calls reuse the running browser process. Each request gets an isolated page.
package browser

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	rodutils "github.com/go-rod/rod/lib/utils"
	readability "github.com/go-shiori/go-readability"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	// Silence go-rod's stack-dumping panic helper. go-rod uses Try +
	// MustX helpers that panic on error; the default Panic logs a
	// full stack trace before re-panicking. Try catches the re-panic
	// and we surface the actual error normally, but the noisy stack
	// dump still hits gohort.log on every navigation failure (e.g.
	// HTTP/2 protocol errors from picky sites). Override to a clean
	// panic-only — the recovered error message is enough.
	rodutils.Panic = func(v interface{}) { panic(v) }
}

// clearStaleSingleton removes Chromium's SingletonLock / SingletonCookie /
// SingletonSocket files left behind when a previous Chromium instance
// crashed or was killed. Without this, a fresh launch fails with
// "Failed to create .../SingletonLock: File exists (17)". Safe because
// only one gohort process owns this profile dir.
func clearStaleSingleton(profileDir string) {
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		path := filepath.Join(profileDir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			Debug("[browser] clearStaleSingleton: could not remove %s: %v", path, err)
		}
	}
}

func init() {
	RegisterChatTool(&BrowsePageTool{})
	// Wire the sandbox-hook's raw-text shim so gohort.fetch_url's
	// JS-heavy auto-route and gohort.browse_page can call Fetch
	// directly — getting just the page text without the LLM-shaped
	// "Fetched X via browser (N chars):" preamble that
	// BrowsePageTool.Run wraps on for the LLM consumer.
	BrowserFetchFunc = Fetch
}

// shared is the singleton used by both the LLM tool and the package-level Fetch function.
var shared = &BrowsePageTool{}

// Fetch loads target with the headless browser and returns extracted text up to
// maxChars. Used by other packages as a programmatic fallback (e.g. websearch
// for known JS-heavy domains). Returns an error if Chromium fails to launch or
// navigate; callers should fall back to HTTP on error.
func Fetch(target string, maxChars int) (string, error) {
	return shared.fetch(target, maxChars)
}

// BrowsePageTool fetches a URL using a real headless browser (JavaScript
// executed, cookies handled). A single Chromium process is shared across
// all calls; each call gets an isolated page that is closed when done.
type BrowsePageTool struct {
	once    sync.Once
	browser *rod.Browser
	initErr error
	mu      sync.Mutex
}

func (t *BrowsePageTool) Name() string { return "browse_page" }
func (t *BrowsePageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} } // headless browser fetch
func (t *BrowsePageTool) Desc() string {
	return "Load a URL in a real browser (JavaScript executed, cookies handled) and return " +
		"the rendered page's readable text. Reach for it as fetch_url's recovery path when " +
		"the simpler tool comes back BLOCKED or USELESS: 403 / captcha challenge page / " +
		"Cloudflare interstitial / JS-required skeleton / empty body — the headless browser " +
		"clears most soft blocks because it executes JS and ships normal browser headers. " +
		"Also right as the FIRST call for sites known to require JS (Reddit, Twitter/X, " +
		"single-page-app news, aggregators, infinite-scroll feeds). " +
		"Slower than fetch_url (5–20s) and burns more tokens, so try fetch_url first when " +
		"the page might be static. Returns up to 10000 characters of extracted text."
}
func (t *BrowsePageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"url": {Type: "string", Description: "The URL to load. Must be http:// or https://."},
	}
}

// launch starts Chromium the first time and stores the result.
func (t *BrowsePageTool) launch() {
	t.once.Do(func() {
		dir := BrowserDir()
		Log("[browser] launching Chromium (first use may download ~150MB to %s)...", dir)

		// Resolve (and optionally download) Chromium into our managed dir.
		b := launcher.NewBrowser()
		b.RootDir = dir
		binPath, err := b.Get()
		if err != nil {
			t.initErr = fmt.Errorf("Chromium download failed: %w", err)
			Log("[browser] download error: %v", t.initErr)
			return
		}

		profileDir := dir + "/profile"
		// Clean up stale SingletonLock from a previous Chromium that
		// crashed or was killed without clean shutdown. The lock is a
		// symlink whose target encodes the previous PID; if the PID is
		// dead the lock is stale and a fresh launch fails with
		// "File exists (17)" until it's removed. Verifying the symlink
		// target is non-trivial across distros — simplest and safest
		// is to remove it unconditionally at our own startup, since
		// only one gohort process owns this profile.
		clearStaleSingleton(profileDir)

		u, err := launcher.New().
			Bin(binPath).
			Headless(true).
			NoSandbox(true).
			Set("disable-gpu").
			Set("disable-dev-shm-usage").
			UserDataDir(profileDir).
			Launch()
		if err != nil {
			t.initErr = fmt.Errorf("Chromium launch failed: %w", err)
			Log("[browser] launch error: %v", t.initErr)
			return
		}
		t.browser = rod.New().ControlURL(u).MustConnect()
		Log("[browser] Chromium ready")
	})
}

// Timeout scaling for browse_page: every sub-timeout derives from the
// operator-configured HTTPRequestTimeout (the same knob fetch_url and
// other source-hook clients honor, set via --setup → Network Timeouts).
// browse_page does strictly more than fetch_url (launch tab, run JS,
// wait for idle, extract HTML, parse readability), so its budget scales
// up from that base:
//
//	navTimeout       = 2 × HTTPRequestTimeout  (default 30s)
//	idleWait         = HTTPRequestTimeout / 2  (default ~7.5s)
//	totalOuterBudget = 3 × HTTPRequestTimeout  (default 45s)
//
// Operator raising HTTPRequestTimeout (slow networks, complex SPAs)
// stretches browse_page's budgets proportionally without a separate
// knob — and the previous hardcoded 30s/8s/45s line up exactly with the
// 15s default so existing deployments don't shift behavior.
func browsePageNavTimeout() time.Duration {
	return 2 * HTTPRequestTimeout
}
func browsePageIdleWait() time.Duration {
	return HTTPRequestTimeout / 2
}
func browsePageTotalBudget() time.Duration {
	return 3 * HTTPRequestTimeout
}

// fetch wraps fetchImpl in an outer-budget guard. On timeout, the
// goroutine is allowed to leak (it'll complete eventually when rod
// errors out or the process exits); the caller gets a clear error
// immediately rather than waiting alongside a hung browser.
func (t *BrowsePageTool) fetch(target string, maxChars int) (string, error) {
	budget := browsePageTotalBudget()
	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		text, err := t.fetchImpl(target, maxChars)
		ch <- result{text: text, err: err}
	}()
	select {
	case r := <-ch:
		return r.text, r.err
	case <-time.After(budget):
		Log("[browse_page] outer budget %v exceeded for %s — Chromium likely wedged (page creation / CDP / idle wait); goroutine leaks until rod errors out", budget, target)
		return "", fmt.Errorf("browse_page timed out after %v on %s — Chromium appears wedged. Try fetch_url for static content, or wait and retry if this is transient. Persistent wedge → restart the gohort process.", budget, target)
	}
}

// fetchImpl is the real fetch — the outer fetch() bounds its overall
// runtime via browsePageTotalBudget so rod hangs outside the inner
// page.Timeout calls (page creation, CDP wedge, etc.) don't outlast
// the call.
func (t *BrowsePageTool) fetchImpl(target string, maxChars int) (string, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("parsing url: %w", err)
	}

	t.launch()
	if t.initErr != nil {
		return "", t.initErr
	}

	t.mu.Lock()
	b := t.browser
	t.mu.Unlock()

	page, err := b.Page(proto.TargetCreateTarget{})
	if err != nil {
		return "", fmt.Errorf("creating browser page: %w", err)
	}
	defer page.MustClose()

	page.MustSetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	})

	navErr := rod.Try(func() {
		page.Timeout(browsePageNavTimeout()).MustNavigate(target)
		// Wait for network to settle; ignore timeout — get whatever rendered so far.
		_ = page.Timeout(browsePageIdleWait()).WaitIdle(500 * time.Millisecond)
	})
	if navErr != nil {
		// rod.Try returns *TryError, whose .Error() formats with
		// the full debug.Stack baked in — noisy in logs and useless
		// to the LLM. Unwrap to the underlying panic value (a
		// rod.NavigationError or similar) and report that cleanly.
		msg := navErr.Error()
		if te, ok := navErr.(*rod.TryError); ok {
			if inner, isErr := te.Value.(error); isErr {
				msg = inner.Error()
			} else {
				msg = fmt.Sprintf("%v", te.Value)
			}
		}
		return "", fmt.Errorf("navigation failed: %s", msg)
	}

	html, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("getting rendered HTML: %w", err)
	}

	var text string
	article, rerr := readability.FromReader(strings.NewReader(html), parsed)
	if rerr == nil && strings.TrimSpace(article.TextContent) != "" {
		title := strings.TrimSpace(article.Title)
		body := strings.TrimSpace(article.TextContent)
		if title != "" {
			text = title + "\n\n" + body
		} else {
			text = body
		}
	} else {
		text = stripTags(html)
	}

	if maxChars > 0 && len(text) > maxChars {
		text = text[:maxChars]
		if idx := strings.LastIndexByte(text, ' '); idx > maxChars/2 {
			text = text[:idx]
		}
		text += "..."
	}
	return strings.TrimSpace(text), nil
}

// FetchPageImage renders pageURL in the headless browser and returns the
// bytes of the page's representative image (og:image / twitter:image, else
// the first <img>), fetched THROUGH the browser so referer-based hotlink
// protection — which blocks a plain downloader — is bypassed. find_image
// uses it as an escalation: when a result whose page mentions the subject has
// a cheap/blocked image the vision pass rejected, render the page to pull the
// real image and re-score. Returns an error (the caller falls through) on any
// failure — never fatal.
func FetchPageImage(pageURL string) ([]byte, error) { return shared.fetchImage(pageURL) }

func (t *BrowsePageTool) fetchImage(pageURL string) ([]byte, error) {
	t.launch()
	if t.initErr != nil {
		return nil, t.initErr
	}
	t.mu.Lock()
	b := t.browser
	t.mu.Unlock()

	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var data []byte
		var noImg bool
		err := rod.Try(func() {
			page := b.MustPage()
			defer page.MustClose()
			page.MustSetUserAgent(&proto.NetworkSetUserAgentOverride{
				UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
			})
			page.Timeout(browsePageNavTimeout()).MustNavigate(pageURL)
			_ = page.Timeout(browsePageIdleWait()).WaitIdle(500 * time.Millisecond)
			// Prefer the social-share meta image (the page's canonical
			// representative image); otherwise take the LARGEST rendered <img>
			// (skipping icons/sprites), honoring lazy-load attrs + srcset.
			obj, eerr := page.Eval(`() => {
				var m = document.querySelector('meta[property="og:image"], meta[property="og:image:url"], meta[name="twitter:image"], meta[name="twitter:image:src"], link[rel="image_src"]');
				if (m) { var c = m.getAttribute('content') || m.getAttribute('href'); if (c) return c; }
				var best = '', bestArea = 0, imgs = document.querySelectorAll('img');
				for (var i = 0; i < imgs.length; i++) {
					var im = imgs[i];
					var w = im.naturalWidth || im.width || 0, h = im.naturalHeight || im.height || 0;
					if (w < 200 || h < 150) continue;
					var src = im.currentSrc || im.src || im.getAttribute('data-src') || im.getAttribute('data-original') || '';
					if (!src) continue;
					var area = w * h;
					if (area > bestArea) { bestArea = area; best = src; }
				}
				return best;
			}`)
			if eerr != nil {
				panic(eerr)
			}
			imgURL := obj.Value.Str()
			if imgURL == "" {
				noImg = true // normal outcome (no extractable image) — not a panic
				return
			}
			d, gerr := page.GetResource(imgURL)
			if gerr != nil {
				panic(gerr)
			}
			data = d
		})
		if err == nil && noImg {
			err = fmt.Errorf("no extractable image on page")
		}
		ch <- result{data: data, err: err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			// rod.Try bakes a debug.Stack into *TryError.Error(); unwrap to
			// the underlying value so the log line is a clean one-liner, not
			// a goroutine dump.
			msg := r.err.Error()
			if te, ok := r.err.(*rod.TryError); ok {
				if inner, isErr := te.Value.(error); isErr {
					msg = inner.Error()
				} else {
					msg = fmt.Sprintf("%v", te.Value)
				}
			}
			return nil, fmt.Errorf("browser image fetch: %s", msg)
		}
		if len(r.data) == 0 {
			return nil, fmt.Errorf("browser image fetch returned no data")
		}
		return r.data, nil
	case <-time.After(browsePageTotalBudget()):
		return nil, fmt.Errorf("browser image fetch timed out after %v on %s", browsePageTotalBudget(), pageURL)
	}
}

func (t *BrowsePageTool) IsInternetTool() bool { return true }

func (t *BrowsePageTool) Run(args map[string]any) (string, error) {
	target := StringArg(args, "url")
	if target == "" {
		return "", fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("url must be http:// or https://")
	}
	// SSRF guard — same rules as fetch_url.
	if host := parsed.Hostname(); host != "" {
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				return "", fmt.Errorf("refusing to fetch non-public host: %s", host)
			}
		}
		lower := strings.ToLower(host)
		if lower == "localhost" || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
			return "", fmt.Errorf("refusing to fetch non-public host: %s", host)
		}
	}

	text, err := t.fetch(target, 10000)
	if err != nil {
		return "", err
	}
	if text == "" {
		return "Page loaded but no readable text could be extracted.", nil
	}
	Debug("[browse_page] %s → %d chars", target, len(text))
	return fmt.Sprintf("Fetched %s via browser (%d chars):\n\n%s", target, len(text), text), nil
}

// stripTags removes all HTML tags and collapses whitespace — minimal fallback
// when Readability cannot identify an article structure.
func stripTags(html string) string {
	var b strings.Builder
	inTag := false
	for _, r := range html {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			b.WriteRune(' ')
		case !inTag:
			b.WriteRune(r)
		}
	}
	parts := strings.Fields(b.String())
	return strings.Join(parts, " ")
}
