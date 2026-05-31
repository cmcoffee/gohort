// fetch_json — the JSON-only mode the LLM kept reaching for when it
// used fetch_url to test a JSON endpoint and then crashed in a script
// trying to parse the wrapped result. Same behavior shape as the
// script-side gohort.fetch_json: validates 2xx, parses the body,
// raises a clean error on either failure. Output skips the "Fetched
// X (N chars):" preamble so the LLM gets pure JSON it can act on
// directly.
//
// Symmetric with gohort.fetch_json (the script-side primitive) by
// design — the LLM uses fetch_json to test; if it works there, the
// same URL in a script via gohort.fetch_json works identically.
// Tested in [[feedback_shell_tool_symmetry]].

package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

type FetchJSONTool struct{}

func (t *FetchJSONTool) Name() string       { return "fetch_json" }
func (t *FetchJSONTool) Caps() []Capability { return []Capability{CapNetwork} }
func (t *FetchJSONTool) Desc() string {
	return "Fetch a URL that returns JSON and parse the response in one step. " +
		"Returns the parsed JSON (pretty-printed) on success. On HTTP error " +
		"(non-2xx) returns a clean error message with the status code and a " +
		"body excerpt — caller sees the upstream's actual response instead of " +
		"a generic 'fetch failed'. On parse failure (200 but body isn't JSON) " +
		"same: clean error with the body excerpt. NEVER routes through a " +
		"headless browser (which would return a pretty-printed text view of " +
		"the JSON instead of parseable data) — always plain HTTP. " +
		"Use over fetch_url whenever the endpoint returns JSON; you skip the " +
		"'Fetched X (N chars):' preamble that would otherwise need to be " +
		"stripped before parsing. Script-side equivalent: gohort.fetch_json."
}

// IsInternetTool — drops under the privacy-mode internet filter the
// same way fetch_url does. fetch_json hits the public internet.
func (t *FetchJSONTool) IsInternetTool() bool { return true }

func (t *FetchJSONTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"url": {Type: "string", Description: "Absolute http:// or https:// URL of the JSON endpoint."},
	}
}

func (t *FetchJSONTool) Run(args map[string]any) (string, error) {
	return t.runImpl(args, nil)
}

func (t *FetchJSONTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	return t.runImpl(args, sess)
}

func (t *FetchJSONTool) runImpl(args map[string]any, sess *ToolSession) (string, error) {
	if sess != nil && !sess.NetworkAllowed() {
		return "", fmt.Errorf("fetch_json refused: network is blocked for this turn (private mode is on)")
	}
	target := strings.TrimSpace(StringArg(args, "url"))
	if target == "" {
		return "", fmt.Errorf("'url' is required")
	}
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("'url' must be an http:// or https:// URL")
	}
	// SSRF guard — same rules as fetch_url so the JSON tool can't be
	// used to bypass that protection.
	if host := parsed.Hostname(); host != "" {
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				return "", fmt.Errorf("refusing to fetch non-public host: %s", host)
			}
		}
		if lower := strings.ToLower(host); lower == "localhost" || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
			return "", fmt.Errorf("refusing to fetch non-public host: %s", host)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	// Explicit "I want JSON" Accept header — content negotiation
	// servers serve JSON over HTML when both are options.
	req.Header.Set("Accept", "application/json,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Browser-shaped UA — same one the article path uses — to slip
	// past anti-bot WAFs that block tooly UAs. JSON endpoints behind
	// Cloudflare honor this.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	resp, err := NewBoundedHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch_json: %w", err)
	}
	defer resp.Body.Close()
	// Cap body read at 10 MiB — same as the sandbox-hook fetch.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("fetch_json: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch_json %s returned HTTP %d: %s", target, resp.StatusCode, excerpt(string(body), 300))
	}
	var parsedJSON any
	if err := json.Unmarshal(body, &parsedJSON); err != nil {
		return "", fmt.Errorf("fetch_json %s: status was %d but body is not valid JSON: %v. Body excerpt: %s", target, resp.StatusCode, err, excerpt(string(body), 300))
	}
	// Re-marshal pretty so the LLM sees structured output. Sorts keys
	// implicitly via encoding/json default (insertion order; for the
	// LLM's purposes that's fine — keys are stable enough).
	pretty, err := json.MarshalIndent(parsedJSON, "", "  ")
	if err != nil {
		return "", fmt.Errorf("fetch_json: re-marshal failed: %w", err)
	}
	Debug("[fetch_json] %s → %d bytes (status=%d)", target, len(pretty), resp.StatusCode)
	return string(pretty), nil
}

// excerpt returns the first n bytes of s with an ellipsis if
// truncated. Used to embed a peek of an upstream error body in our
// error message without dumping the whole thing.
func excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
