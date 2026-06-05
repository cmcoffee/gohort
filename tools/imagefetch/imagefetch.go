// Package imagefetch provides tools for fetching, finding, and generating images
// and collecting them for delivery (e.g. as iMessage attachments).
package imagefetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/browser"
	_ "golang.org/x/image/webp" // register the WebP decoder for image.Decode
)

func init() {
	RegisterChatTool(new(ImageTool))
	RegisterChatTool(new(FetchImageTool))
	RegisterChatTool(new(FindImageTool))
	RegisterChatTool(new(GenerateImageTool))
}

// --- ImageTool (grouped) ---
//
// Single entry point for image work — find | fetch | generate — picked by
// `action`, mirroring the `video` grouped tool. Collapses three
// near-identical schemas into one. The standalone find_image / fetch_image
// / generate_image stay registered (phantom + explicit allowlists use
// them); orchestrate drops them from its default pool in favor of this
// (see supersededWorkerTools). The handler just delegates to the existing
// per-action tools, so behavior is identical.

type ImageTool struct{}

func (t *ImageTool) Name() string { return "image" }
func (t *ImageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} }
func (t *ImageTool) IsInternetTool() bool { return true }
func (t *ImageTool) Desc() string {
	return "Work with images — single entry point; pick the action matching intent. " +
		"actions: find (search the web for a picture/meme/GIF/photo by description and save the best match — use whenever the user wants a picture of something and has no URL), " +
		"fetch (download a specific image URL you already have), " +
		"generate (create a NEW image from a text prompt — DALL·E / Stable Diffusion / whatever's wired; generation makes things up, so NOT for real-world reference), " +
		"help. " +
		"Each saves into your session workspace and returns the path — it does NOT deliver; follow up with workspace(action=\"attach\", path=..., cleanup=true) to ship the file. " +
		"Decision: wants a picture of something, no URL → find. Gave an image URL → fetch. Wants something drawn / created / imagined → generate."
}
func (t *ImageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"action": {Type: "string", Enum: []string{"find", "fetch", "generate"}, Description: "find | fetch | generate."},
		"query":  {Type: "string", Description: "(find) Description of the image to find (e.g. 'funny cat meme', 'golden gate bridge sunset', 'surprised pikachu')."},
		"url":    {Type: "string", Description: "(fetch) Direct URL of the image to download (must resolve to an image file: jpg, png, gif, webp, etc.)."},
		"prompt": {Type: "string", Description: "(generate) Detailed description of the image to create."},
	}
}

func (t *ImageTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("image requires a session context — use GetAgentToolsWithSession")
}
func (t *ImageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	switch strings.ToLower(strings.TrimSpace(StringArg(args, "action"))) {
	case "find":
		return (&FindImageTool{}).RunWithSession(args, sess)
	case "fetch":
		return (&FetchImageTool{}).RunWithSession(args, sess)
	case "generate":
		return (&GenerateImageTool{}).RunWithSession(args, sess)
	case "", "help":
		return "image actions: find (query) | fetch (url) | generate (prompt). Each saves to your workspace and returns the path; deliver with workspace(action=\"attach\", path=...).", nil
	default:
		return "", fmt.Errorf("unknown action %q for image — use find | fetch | generate", StringArg(args, "action"))
	}
}

// --- FetchImageTool ---

type FetchImageTool struct{}

func (t *FetchImageTool) Name() string { return "fetch_image" }
func (t *FetchImageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} } // HTTP GET image
func (t *FetchImageTool) Desc() string {
	return "Download an image from a URL into your session workspace. Returns the saved path. Does NOT deliver — call workspace(action=\"attach\", path=..., cleanup=true) to ship the file. Use this after finding an image URL via web_search, or whenever you already have a specific image URL the user wants."
}
func (t *FetchImageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"url": {Type: "string", Description: "Direct URL of the image to download (must resolve to an image file: jpg, png, gif, webp, etc.)."},
	}
}
func (t *FetchImageTool) IsInternetTool() bool { return true }

func (t *FetchImageTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("fetch_image requires a session context — use GetAgentToolsWithSession")
}
func (t *FetchImageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	rawURL := StringArg(args, "url")
	if rawURL == "" {
		return "", fmt.Errorf("url is required")
	}
	return downloadImageTo(rawURL, sess)
}

// --- FindImageTool ---

type FindImageTool struct{}

func (t *FindImageTool) Name() string { return "find_image" }
func (t *FindImageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} } // search + download
func (t *FindImageTool) Desc() string {
	return "Search for an image by description and save the SINGLE BEST MATCH into your session workspace. The framework's internal vision-LLM picks the best candidate from multiple search results. Returns the saved path. Does NOT deliver to the user — call workspace(action=\"attach\", path=..., cleanup=true) to ship the file. Use this whenever the user asks for a picture, meme, GIF, or photo."
}
func (t *FindImageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"query": {Type: "string", Description: "Description of the image to find (e.g. 'funny cat meme', 'golden gate bridge sunset', 'surprised pikachu')."},
	}
}
func (t *FindImageTool) IsInternetTool() bool { return true }

func (t *FindImageTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("find_image requires a session context — use GetAgentToolsWithSession")
}
func (t *FindImageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	query := StringArg(args, "query")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	cfg := LoadWebSearchConfig()
	if cfg.Provider != "serper" || cfg.APIKey == "" {
		return "", fmt.Errorf("find_image requires the serper search provider with an API key configured")
	}
	results, err := SerperImageSearch(query, cfg.APIKey)
	if err != nil {
		return "", fmt.Errorf("image search failed: %w", err)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("no image results found for %q", query)
	}

	// Save the chosen image to the session workspace and return its path with
	// the standard delivery hint. No auto-attach; the LLM ships it via
	// workspace(action="attach", path=..., cleanup=true).
	saveAndReturn := func(data []byte, meta SerperImageResult) (string, error) {
		wsDir, err := EnsureSessionWorkspace(sess)
		if err != nil {
			return "", fmt.Errorf("session workspace unavailable: %w", err)
		}
		name := "find-" + shortID() + extForMime(http.DetectContentType(data))
		target := filepath.Join(wsDir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return "", fmt.Errorf("create parent dir: %w", err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			return "", fmt.Errorf("save image: %w", err)
		}
		Log("[imagefetch/find_image] query=%q delivered %q (title: %q, source: %s)", query, name, meta.Title, meta.Source)
		return fmt.Sprintf(
			"Stored at %q (title: %q, source: %s). If the user asked you to SEND / SHARE the image, call workspace(action=\"attach\", path=%q, cleanup=true) to deliver. If they just want info about it (describe, identify, summarize), skip the attach — answer from context. When you do attach, cleanup=true keeps the workspace tidy (find results are typically one-shot).",
			name, meta.Title, meta.Source, name,
		), nil
	}

	// LAZY short-circuit: evaluate results ONE AT A TIME and stop at the first
	// that matches — no need to fetch + vision-score all five every time (the
	// vision call is the expensive part). The first candidate that BOTH
	// text-matches (page/title mentions the subject) AND visually depicts the
	// query is the answer; we only look deeper on a miss. A best-vision-match
	// fallback covers results whose title was too sparse to text-match but
	// whose image is right. Per candidate: one page fetch, one image, one
	// vision call — and usually just the first.
	const maxFindCandidates = 6
	var bestData []byte
	var bestMeta SerperImageResult
	bestScore := -1
	usable := 0
	for _, r := range results {
		if usable >= maxFindCandidates {
			break
		}
		ogImage, pageMentions := inspectPage(r.Link, query)
		textMatch := pageMentions || pageMentionsSubject(strings.ToLower(r.Title), query)
		// Prefer the page's real image; fall back to the (accessible) result
		// image URL when the source blocks the direct fetch.
		var data []byte
		var ok bool
		if ogImage != "" && ogImage != r.ImageURL {
			data, _, _, ok = fetchValidImage(ogImage, r.Link)
		}
		if !ok {
			data, _, _, ok = fetchValidImage(r.ImageURL, r.Link)
		}
		if !ok {
			Log("[imagefetch/find_image] candidate skipped — no usable image for %q (source blocked?)", r.Link)
			continue
		}
		usable++
		// No vision configured → can't screen the pixels; take the first
		// text-matching result (or the first usable one at all).
		if sess.LLM == nil {
			if textMatch || bestScore < 0 {
				return saveAndReturn(data, r)
			}
			continue
		}
		score := scoreImageMatch(sess, data, query)
		Log("[imagefetch/find_image] query=%q candidate %d (title %q) text=%v vision=%d/100", query, usable, r.Title, textMatch, score)
		if textMatch && score >= imageMatchThreshold {
			return saveAndReturn(data, r) // confident match — stop here
		}
		if score > bestScore {
			bestData, bestMeta, bestScore = data, r, score
		}
		// Escalation: the page IS about the subject (text-matched) but the
		// cheap image — a blocked source that fell back to Google's thumbnail,
		// or a low-res cache — didn't pass vision. Render the page in the
		// headless browser to pull its REAL image (bypasses hotlink
		// protection) and re-score before abandoning this candidate. Getting
		// the right image HERE is cheaper than paying a fresh page-fetch +
		// vision call on the next candidate, so it's faster overall when it
		// converts a multi-candidate search into a one-candidate hit.
		if textMatch && score < imageMatchThreshold {
			if raw, rerr := browser.FetchPageImage(r.Link); rerr == nil {
				if rdata, _, _, rok := normalizeToJPEG(raw); rok {
					rscore := scoreImageMatch(sess, rdata, query)
					Log("[imagefetch/find_image] query=%q candidate %d browser-rendered image vision=%d/100 (cheap image was %d)", query, usable, rscore, score)
					if rscore >= imageMatchThreshold {
						return saveAndReturn(rdata, r)
					}
					if rscore > bestScore {
						bestData, bestMeta, bestScore = rdata, r, rscore
					}
				}
			} else {
				Log("[imagefetch/find_image] query=%q browser render failed for %q: %v", query, r.Link, rerr)
			}
		}
	}
	// Nothing both text- and vision-matched. Use the best vision match if it's
	// a confident depiction; otherwise reject rather than return a wrong image.
	if bestScore >= imageMatchThreshold {
		Log("[imagefetch/find_image] query=%q no text+vision match; using best vision match %d/100", query, bestScore)
		return saveAndReturn(bestData, bestMeta)
	}
	if bestScore < 0 {
		return "", fmt.Errorf("could not download any usable image for %q (sources may be blocking the fetch)", query)
	}
	return "", fmt.Errorf("found image(s) for %q but none clearly depict it (best visual match %d/100) — the search may have surfaced lookalikes or unrelated results; refine the query, or use fetch_image with a specific image URL", query, bestScore)
}

// --- GenerateImageTool ---

type GenerateImageTool struct{}

func (t *GenerateImageTool) Name() string { return "generate_image" }
func (t *GenerateImageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} } // image-gen API call
func (t *GenerateImageTool) Desc() string {
	return "Generate a NEW image from a text description (DALL·E / Stable Diffusion / whichever image-gen backend is wired up) and save it into your session workspace. Returns the saved path. Does NOT deliver — call workspace(action=\"attach\", path=..., cleanup=true) to ship the file. USE ONLY when the user explicitly asks to CREATE / DRAW / MAKE / GENERATE a fresh image. NOT for finding existing images (use find_image), downloading a known URL (use fetch_image), or page screenshots (use screenshot_page). Generation makes things up — wrong tool for real-world reference."
}
func (t *GenerateImageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"prompt": {Type: "string", Description: "Detailed description of the image to generate."},
	}
}
func (t *GenerateImageTool) IsInternetTool() bool { return true }

func (t *GenerateImageTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("generate_image requires a session context — use GetAgentToolsWithSession")
}
func (t *GenerateImageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	prompt := StringArg(args, "prompt")
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	result, err := GenerateImage(context.Background(), "", prompt)
	if err != nil {
		return "", fmt.Errorf("image generation failed: %w", err)
	}
	var data []byte
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		data, err = downloadImageBytes(result.URL)
	} else {
		data, err = os.ReadFile(result.URL)
		os.Remove(result.URL)
	}
	if err != nil {
		return "", fmt.Errorf("failed to retrieve generated image: %w", err)
	}
	// Save to session workspace, return the path. No auto-attach.
	wsDir, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", fmt.Errorf("session workspace unavailable: %w", err)
	}
	name := "gen-" + shortID() + ".png"
	target := filepath.Join(wsDir, name)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		return "", fmt.Errorf("save image: %w", err)
	}
	Log("[imagefetch/generate_image] generated image for prompt: %s → %s", truncate(prompt, 80), name)
	return fmt.Sprintf("Stored at %q (%d bytes). Generated images are normally meant for delivery — call workspace(action=\"attach\", path=%q, cleanup=true) and then write a short text describing what you made. (Skip the attach only if the user explicitly asked you to generate WITHOUT sending — rare; default is attach.)",
		name, len(data), name), nil
}

// --- Serper image search ---

type SerperImageResult struct {
	ImageURL string
	Title    string
	Source   string
	Link     string
}

type serperImageResponse struct {
	Images []struct {
		ImageURL string `json:"imageUrl"`
		Title    string `json:"title"`
		Source   string `json:"source"`
		Link     string `json:"link"`
	} `json:"images"`
}

func SerperImageSearch(query, apiKey string) ([]SerperImageResult, error) {
	payload, _ := json.Marshal(map[string]any{"q": query, "num": 10})
	req, err := http.NewRequest("POST", "https://google.serper.dev/images", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-KEY", apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("serper images API error (%d): %s", resp.StatusCode, string(body))
	}

	var result serperImageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing serper images response: %w", err)
	}

	var out []SerperImageResult
	for _, img := range result.Images {
		if img.ImageURL != "" {
			out = append(out, SerperImageResult{
				ImageURL: img.ImageURL,
				Title:    img.Title,
				Source:   img.Source,
				Link:     img.Link,
			})
		}
	}
	// Count every provider call — Serper bills per request regardless
	// of how many images come back. Symmetric with web_search's
	// "count on call, not on result-content" semantics.
	ProcessUsage().AddSearchCall()
	return out, nil
}

// --- HTTP helpers ---

const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

func FetchImageBytes(rawURL, referer string, timeoutSecs int) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	client := &http.Client{Timeout: time.Duration(timeoutSecs) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	const maxBytes = 10 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read failed: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	return data, nil
}

// pageImageRes matches a page's representative image in its <head> meta
// tags — og:image (either attribute order) and twitter:image.
var pageImageRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<meta[^>]+property=["']og:image(?::url)?["'][^>]+content=["']([^"']+)["']`),
	regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+property=["']og:image(?::url)?["']`),
	regexp.MustCompile(`(?i)<meta[^>]+name=["']twitter:image(?::src)?["'][^>]+content=["']([^"']+)["']`),
}

// inspectPage fetches a source page ONCE and reports (a) its representative
// image URL (og:image / twitter:image, resolved absolute) and (b) whether
// the page actually MENTIONS the search subject. find_image uses both: grab
// the page's real image instead of the cached/thumbnail result image, and
// trust it only when the page is genuinely about what we searched for — the
// drill-in-and-verify step that discards mis-indexed / wrong results.
// Returns ("", false) if the page can't be fetched.
func inspectPage(pageURL, query string) (ogImage string, mentions bool) {
	if pageURL == "" {
		return "", false
	}
	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	const maxHTML = 1 << 20 // 1 MB reaches the <head> metas + visible text on any sane page
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTML))
	if err != nil {
		return "", false
	}
	page := string(body)
	mentions = pageMentionsSubject(strings.ToLower(page), query)
	for _, re := range pageImageRes {
		if m := re.FindStringSubmatch(page); len(m) > 1 && strings.TrimSpace(m[1]) != "" {
			if abs := resolvePageURL(pageURL, html.UnescapeString(strings.TrimSpace(m[1]))); abs != "" {
				ogImage = abs
				break
			}
		}
	}
	return ogImage, mentions
}

// imageQueryFiller is generic query noise that shouldn't be required to
// appear on a source page (a page about a red Ferrari needn't say "photo").
var imageQueryFiller = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"photo": true, "photos": true, "image": true, "images": true,
	"picture": true, "pictures": true, "pic": true, "pics": true,
	"png": true, "jpg": true, "jpeg": true, "gif": true,
}

// significantQueryWords reduces a query to the tokens worth matching on a
// page: alphanumeric, length >= 3, minus generic filler.
func significantQueryWords(query string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(w) >= 3 && !imageQueryFiller[w] {
			out = append(out, w)
		}
	}
	return out
}

// pageMentionsSubject reports whether a page (already lowercased) references
// the search subject — every significant query word for short queries, a
// ~60% majority for longer ones. An uncheckable query (no significant words)
// passes so it never blocks the result.
func pageMentionsSubject(pageLower, query string) bool {
	toks := significantQueryWords(query)
	if len(toks) == 0 {
		return true
	}
	need := len(toks)
	if need > 3 {
		need = (len(toks)*3 + 4) / 5 // ~60%, rounded up
	}
	hit := 0
	for _, t := range toks {
		if strings.Contains(pageLower, t) {
			hit++
		}
	}
	return hit >= need
}

// resolvePageURL resolves a possibly-relative image ref against the page it
// came from, keeping only http(s) results.
func resolvePageURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	abs := b.ResolveReference(r)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	return abs.String()
}

// imageMatchThreshold is the minimum 0-100 vision score for find_image to
// accept a candidate. Below it, the tool reports no confident match rather
// than returning a wrong image. Tunable.
const imageMatchThreshold = 50

// scoreImageMatch asks the vision LLM to actually LOOK at ONE image and rate
// 0-100 how well it depicts the query. Forcing a one-line description first
// makes the model examine the pixels instead of guessing from metadata or
// from a confusing multi-image prompt. Returns -1 if no usable score came back.
func scoreImageMatch(sess *ToolSession, img []byte, query string) int {
	prompt := fmt.Sprintf(
		"Look closely at this image. In one sentence, describe what it ACTUALLY shows. "+
			"Then rate from 0 to 100 how well it depicts: %q "+
			"(0 = unrelated or the wrong subject, 100 = exactly that subject). "+
			"Put the rating as a plain number on its own FINAL line.", query)
	resp, err := sess.LLM.Chat(context.Background(),
		[]Message{{Role: "user", Content: prompt, Images: [][]byte{img}}},
		WithCaller("imagefetch/find_image"),
		WithMaxRetries(0),
		WithThink(true),
	)
	if err != nil || resp == nil {
		return -1
	}
	return parseTrailingScore(resp.Content)
}

// parseTrailingScore extracts the last integer in [0,100] from the content
// (the model is asked to end with the rating on its own line).
func parseTrailingScore(s string) int {
	fields := strings.Fields(s)
	for i := len(fields) - 1; i >= 0; i-- {
		tok := strings.Trim(fields[i], ".,;:%\"'()[]")
		if n, err := strconv.Atoi(tok); err == nil && n >= 0 && n <= 100 {
			return n
		}
	}
	return -1
}

// fetchValidImage downloads a URL and returns the image RE-ENCODED AS JPEG
// plus its pixel dimensions, only if it's a genuinely usable image. ok=false
// for a download error (403 etc.), a non-image body (an HTML "access denied"
// block page from a hotlink-protected source), or an undecodable image — the
// caller then falls back to a more accessible URL.
//
// The JPEG re-encode is the fix for "the LLM isn't seeing what's attached":
// llama.cpp's vision (stb_image) can't read WebP/AVIF — the dominant formats
// on the web — so without normalizing it'd be handed bytes it can't decode
// and would hallucinate a description for a blank/wrong image while the saved
// file (the original webp) renders fine everywhere else. Decoding (webp via
// golang.org/x/image/webp) and re-encoding JPEG guarantees the model scores
// exactly what we save and attach.
func fetchValidImage(rawURL, referer string) (data []byte, w, h int, ok bool) {
	if rawURL == "" {
		return nil, 0, 0, false
	}
	d, err := FetchImageBytes(rawURL, referer, 20)
	if err != nil {
		return nil, 0, 0, false
	}
	return normalizeToJPEG(d)
}

// normalizeToJPEG decodes image bytes (jpeg/png/gif/webp) and re-encodes them
// as JPEG — the format the vision model can actually read — returning the
// JPEG plus pixel dimensions. ok=false for a non-image body or an undecodable
// image. Shared by the plain download path and the go-rod render escalation.
func normalizeToJPEG(d []byte) (data []byte, w, h int, ok bool) {
	if !strings.HasPrefix(http.DetectContentType(d), "image/") {
		return nil, 0, 0, false
	}
	img, _, derr := image.Decode(bytes.NewReader(d))
	if derr != nil {
		return nil, 0, 0, false
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return nil, 0, 0, false
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		return nil, 0, 0, false
	}
	return buf.Bytes(), b.Dx(), b.Dy(), true
}

func downloadImageBytes(rawURL string) ([]byte, error) {
	return FetchImageBytes(rawURL, "", 30)
}

func downloadImageTo(rawURL string, sess *ToolSession) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid image URL: %s", rawURL)
	}
	data, err := FetchImageBytes(rawURL, "", 20)
	if err != nil {
		return "", err
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("URL does not appear to be an image (detected: %s)", ct)
	}
	// Save to the session workspace and return the path. Does NOT
	// auto-attach — the LLM uses workspace(action="attach", path=...)
	// to deliver when it's ready.
	wsDir, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", fmt.Errorf("session workspace unavailable: %w", err)
	}
	ext := extForMime(ct)
	name := "fetch-" + shortID() + ext
	target := filepath.Join(wsDir, name)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		return "", fmt.Errorf("save image: %w", err)
	}
	Log("[imagefetch/fetch_image] fetched %d bytes from %s → %s", len(data), rawURL, name)
	return fmt.Sprintf("Stored at %q (%s, %d bytes). If the user asked you to SEND / SHARE the image, call workspace(action=\"attach\", path=%q, cleanup=true) to deliver. If they just want info about what's in it, skip the attach — answer from context. cleanup=true keeps the workspace tidy when you do attach.",
		name, ct, len(data), name), nil
}

// extForMime returns a file extension matching a mime type. Used to
// give saved files plausible suffixes so workspace tools downstream
// can mime-detect them by name when convenient. Covers the formats
// http.DetectContentType emits for common image / video / audio
// types. Unknown mimes fall back to ".bin" rather than empty — the
// filename should always have an extension so the user-facing
// attachment delivery (especially iMessage / SMS) has a meaningful
// suffix.
func extForMime(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/png"):
		return ".png"
	case strings.HasPrefix(mime, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(mime, "image/gif"):
		return ".gif"
	case strings.HasPrefix(mime, "image/webp"):
		return ".webp"
	case strings.HasPrefix(mime, "image/avif"):
		return ".avif"
	case strings.HasPrefix(mime, "image/svg"):
		return ".svg"
	case strings.HasPrefix(mime, "image/bmp"):
		return ".bmp"
	case strings.HasPrefix(mime, "image/heic"), strings.HasPrefix(mime, "image/heif"):
		return ".heic"
	case strings.HasPrefix(mime, "image/tiff"):
		return ".tiff"
	case strings.HasPrefix(mime, "image/"):
		// Unknown image subtype — generic .img fallback. Better
		// than no extension; bridge / browser will still mime-detect
		// from content. Log for diagnosis so we can extend this
		// switch when a new format surfaces in the wild.
		Log("[imagefetch] unknown image mime %q — falling back to .img", mime)
		return ".img"
	case strings.HasPrefix(mime, "video/mp4"):
		return ".mp4"
	case strings.HasPrefix(mime, "video/webm"):
		return ".webm"
	case strings.HasPrefix(mime, "video/"):
		Log("[imagefetch] unknown video mime %q — falling back to .mp4", mime)
		return ".mp4"
	case strings.HasPrefix(mime, "audio/mpeg"):
		return ".mp3"
	case strings.HasPrefix(mime, "audio/wav"), strings.HasPrefix(mime, "audio/x-wav"):
		return ".wav"
	case strings.HasPrefix(mime, "audio/"):
		Log("[imagefetch] unknown audio mime %q — falling back to .m4a", mime)
		return ".m4a"
	default:
		// Non-media content (HTML error page, plaintext error, etc.) —
		// shouldn't happen on a successful image fetch but log so the
		// path is observable when it does.
		Log("[imagefetch] non-media mime %q saved as .bin (likely a fetch error masquerading as success)", mime)
		return ".bin"
	}
}

// shortID returns a brief unique-ish identifier for filenames.
func shortID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
