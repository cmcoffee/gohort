package browser

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"math"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(&ScreenshotPageTool{}) }

// ScreenshotPageTool captures a PNG of a rendered web page using the same
// headless Chromium that powers BrowsePageTool. The screenshot is appended
// to the calling session's image queue so the chat / phantom delivery
// layer can attach it to the user-visible response. Returns a status string
// describing what was captured.
type ScreenshotPageTool struct{}

func (t *ScreenshotPageTool) Name() string { return "screenshot_page" }

func (t *ScreenshotPageTool) Caps() []Capability {
	return []Capability{CapNetwork, CapRead}
}

func (t *ScreenshotPageTool) Desc() string {
	return "Capture a PNG screenshot of a web page using a real headless browser. " +
		"Use when the user asks to see, view, show, or screenshot a page — visual " +
		"questions where text extraction (browse_page / fetch_url) loses the answer. " +
		"Set full_page=true to capture the entire scrollable page; otherwise just " +
		"the visible viewport. The screenshot is delivered as an attachment alongside " +
		"any text response. Slower than text fetches (5–25s). Larger pages may produce " +
		"multi-MB images — capped at 4 MB; oversized captures fail with a clear error."
}

func (t *ScreenshotPageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"url":       {Type: "string", Description: "The URL to screenshot. Must be http:// or https://."},
		"full_page": {Type: "boolean", Description: "If true, capture the full scrollable page; if false (default), capture just the viewport at the configured size."},
	}
}

// IsInternetTool ensures private-mode chat hides this tool — it makes
// outbound HTTP requests just like browse_page.
func (t *ScreenshotPageTool) IsInternetTool() bool { return true }

// Run is the no-session fallback. Without a session there's nowhere to
// hand the screenshot to, so the tool refuses rather than silently
// dropping the bytes.
func (t *ScreenshotPageTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("screenshot_page requires a session capable of carrying image attachments")
}

const screenshotMaxBytes = 4 * 1024 * 1024 // 4 MB cap; multi-MB pages are rare but possible

// RunWithSession captures the screenshot and appends it to sess.Images
// (base64-encoded) so the calling app's delivery layer can attach it.
func (t *ScreenshotPageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("screenshot_page requires a session capable of carrying image attachments")
	}
	target := StringArg(args, "url")
	if target == "" {
		return "", fmt.Errorf("url is required")
	}
	fullPage := false
	if v, ok := args["full_page"].(bool); ok {
		fullPage = v
	}
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("url must be http:// or https://")
	}
	// SSRF guard — same rules as fetch_url and browse_page.
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

	// Reuse the BrowsePageTool singleton's launched browser — no second
	// Chromium process, no second download.
	shared.launch()
	if shared.initErr != nil {
		return "", shared.initErr
	}
	shared.mu.Lock()
	b := shared.browser
	shared.mu.Unlock()
	if b == nil {
		return "", fmt.Errorf("browser unavailable")
	}

	page, err := b.Page(proto.TargetCreateTarget{})
	if err != nil {
		return "", fmt.Errorf("creating browser page: %w", err)
	}
	defer page.MustClose()

	page.MustSetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	})
	// Default viewport reasonable for vision models — wide enough to look
	// like a desktop page without producing 4K screenshots that swamp
	// token budgets.
	_ = page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:             1280,
		Height:            800,
		DeviceScaleFactor: 1,
	})

	navErr := rod.Try(func() {
		page.Timeout(30 * time.Second).MustNavigate(target)
		_ = page.Timeout(8 * time.Second).WaitIdle(500 * time.Millisecond)
	})
	if navErr != nil {
		return "", fmt.Errorf("navigation failed: %w", navErr)
	}

	var data []byte
	captureErr := rod.Try(func() {
		if fullPage {
			data = page.MustScreenshotFullPage()
		} else {
			data = page.MustScreenshot()
		}
	})
	if captureErr != nil {
		return "", fmt.Errorf("screenshot failed: %w", captureErr)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("screenshot returned 0 bytes")
	}
	if len(data) > screenshotMaxBytes {
		return "", fmt.Errorf("screenshot too large: %d bytes (cap %d). Try full_page=false or a different URL", len(data), screenshotMaxBytes)
	}

	// Reject obviously-blank captures rather than attaching them. A solid
	// black or solid white image typically means: DRM blocked the video
	// element (canvas comes back black), the page hadn't finished
	// rendering when we shot, an extension or cookie banner overlay
	// blanked the viewport, or the navigation never landed. Returning an
	// error gives the LLM clear feedback so it can tell the user instead
	// of confidently sending a broken image.
	if blank, reason := screenshotLooksBlank(data); blank {
		return "", fmt.Errorf("screenshot came back blank (%s) — page may be DRM-protected, login-walled, or still loading. Not attaching", reason)
	}

	sess.AppendImage(base64.StdEncoding.EncodeToString(data))

	mode := "viewport"
	if fullPage {
		mode = "full page"
	}
	Debug("[screenshot_page] %s (%s) → %d bytes", target, mode, len(data))
	return fmt.Sprintf("Captured %s screenshot of %s (%d bytes). Image attached to this reply.", mode, target, len(data)), nil
}

// screenshotLooksBlank samples pixels from the captured PNG and reports
// whether the image is essentially a solid color — the common shape for
// failed captures (DRM-blocked canvas → black, JS skeleton → white,
// loading spinner over a uniform background → near-uniform). Sample is a
// 12×12 grid across the image; a blank verdict requires *both* extreme
// mean brightness AND very low variance, so genuine dark/light photos
// (a starfield, a snowfield) don't false-positive.
//
// Returns (true, reason) when the capture should be rejected. Returns
// (false, "") when the image looks plausibly populated.
func screenshotLooksBlank(data []byte) (bool, string) {
	const minBytes = 4096 // anything smaller is almost certainly solid color
	if len(data) < minBytes {
		return true, fmt.Sprintf("only %d bytes", len(data))
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		// Couldn't decode — leave it for the caller; the byte count
		// already passed the floor check.
		return false, ""
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w < 16 || h < 16 {
		return true, fmt.Sprintf("image too small: %dx%d", w, h)
	}

	const grid = 12
	var sum, sumSq float64
	n := 0
	for gy := 0; gy < grid; gy++ {
		for gx := 0; gx < grid; gx++ {
			x := bounds.Min.X + gx*w/grid
			y := bounds.Min.Y + gy*h/grid
			r, g, b, _ := img.At(x, y).RGBA()
			// 16-bit channels → 0–255 grayscale (luma-ish; avg good enough).
			gray := float64(r+g+b) / 3 / 256
			sum += gray
			sumSq += gray * gray
			n++
		}
	}
	mean := sum / float64(n)
	variance := sumSq/float64(n) - mean*mean
	if variance < 0 {
		variance = 0
	}
	stddev := math.Sqrt(variance)

	// Reject only when the image is BOTH near-uniform AND extreme.
	// Threshold tuning: a real photo of a black cat in a coal mine still
	// has stddev > 5; a DRM-blanked canvas has stddev ≈ 0.
	const stddevFloor = 5.0
	if mean < 10 && stddev < stddevFloor {
		return true, fmt.Sprintf("near-uniform black (mean=%.1f stddev=%.1f)", mean, stddev)
	}
	if mean > 245 && stddev < stddevFloor {
		return true, fmt.Sprintf("near-uniform white (mean=%.1f stddev=%.1f)", mean, stddev)
	}
	if stddev < 1.5 {
		return true, fmt.Sprintf("flat color (mean=%.1f stddev=%.1f)", mean, stddev)
	}
	return false, ""
}
