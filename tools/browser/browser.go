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
	readability "github.com/go-shiori/go-readability"

	. "github.com/cmcoffee/gohort/core"
)

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

func init() { RegisterChatTool(&BrowsePageTool{}) }

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
		"the rendered page's readable text. Use when fetch_url returns empty or skeleton " +
		"content — JS-rendered sites, Reddit, dynamic news pages, aggregators. " +
		"Slower than fetch_url (5–20s). Returns up to 10000 characters of extracted text."
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

// fetch is the shared implementation used by Run and the package-level Fetch.
func (t *BrowsePageTool) fetch(target string, maxChars int) (string, error) {
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
		page.Timeout(30 * time.Second).MustNavigate(target)
		// Wait for network to settle; ignore timeout — get whatever rendered so far.
		_ = page.Timeout(8 * time.Second).WaitIdle(500 * time.Millisecond)
	})
	if navErr != nil {
		return "", fmt.Errorf("navigation failed: %w", navErr)
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
