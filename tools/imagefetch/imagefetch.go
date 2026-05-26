// Package imagefetch provides tools for fetching, finding, and generating images
// and collecting them for delivery (e.g. as iMessage attachments).
package imagefetch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterChatTool(new(FetchImageTool))
	RegisterChatTool(new(FindImageTool))
	RegisterChatTool(new(GenerateImageTool))
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

	type candidate struct {
		raw  []byte
		b64  string
		meta SerperImageResult
	}
	var candidates []candidate
	for _, r := range results {
		if len(candidates) >= 5 {
			break
		}
		data, err := FetchImageBytes(r.ImageURL, r.Link, 20)
		if err != nil {
			continue
		}
		// Validate the bytes are ACTUALLY an image — Serper sometimes
		// returns URLs that 404, hot-link-protect, or redirect to
		// HTML error pages. Without this check we'd save the HTML
		// (or whatever non-image blob came back) as a candidate,
		// the vision-LLM picker would fail to interpret it, and the
		// chosen file lands in the workspace as garbage.
		mime := http.DetectContentType(data)
		if !strings.HasPrefix(mime, "image/") {
			Log("[imagefetch/find_image] candidate skipped — URL %q returned %s (not an image)", r.ImageURL, mime)
			continue
		}
		candidates = append(candidates, candidate{
			raw:  data,
			b64:  base64.StdEncoding.EncodeToString(data),
			meta: r,
		})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("could not download any image results for %q", query)
	}

	chosen := 0
	if len(candidates) > 1 && sess.LLM != nil {
		var rawImages [][]byte
		var meta strings.Builder
		for i, c := range candidates {
			rawImages = append(rawImages, c.raw)
			fmt.Fprintf(&meta, "Image %d — title: %q, source: %s\n", i+1, c.meta.Title, c.meta.Source)
		}
		prompt := fmt.Sprintf(
			"I searched for %q and found %d candidate images. "+
				"Their metadata (title and source domain) is listed below — use this alongside what you see to identify the correct subject.\n\n"+
				"%s\n"+
				"Choose the single best match: most relevant to the query, correct subject identity, and appropriate to send. "+
				"Reply with ONLY the number (1 through %d), nothing else.",
			query, len(candidates), meta.String(), len(candidates),
		)
		resp, err := sess.LLM.Chat(context.Background(),
			[]Message{{Role: "user", Content: prompt, Images: rawImages}},
			WithCaller("imagefetch/find_image"),
			WithMaxRetries(0),
			WithThink(true),
		)
		if err == nil && resp != nil {
			for _, tok := range strings.Fields(resp.Content) {
				tok = strings.Trim(tok, ".,;:\"'")
				if n, err := strconv.Atoi(tok); err == nil && n >= 1 && n <= len(candidates) {
					chosen = n - 1
					break
				}
			}
		}
	}

	// Save to session workspace, return the path. No auto-attach;
	// the LLM uses workspace(action="attach", path=..., cleanup=true)
	// to deliver. The cleanup hint matches the one-shot lifecycle of
	// find results — search, send, done.
	wsDir, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", fmt.Errorf("session workspace unavailable: %w", err)
	}
	ct := http.DetectContentType(candidates[chosen].raw)
	ext := extForMime(ct)
	name := "find-" + shortID() + ext
	target := filepath.Join(wsDir, name)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}
	if err := os.WriteFile(target, candidates[chosen].raw, 0600); err != nil {
		return "", fmt.Errorf("save image: %w", err)
	}
	Log("[imagefetch/find_image] query=%q chose candidate %d/%d (title: %q, source: %s) → %s",
		query, chosen+1, len(candidates), candidates[chosen].meta.Title, candidates[chosen].meta.Source, name)
	return fmt.Sprintf(
		"Stored at %q (title: %q, source: %s). If the user asked you to SEND / SHARE the image, call workspace(action=\"attach\", path=%q, cleanup=true) to deliver. If they just want info about it (describe, identify, summarize), skip the attach — answer from context. When you do attach, cleanup=true keeps the workspace tidy (find results are typically one-shot).",
		name, candidates[chosen].meta.Title, candidates[chosen].meta.Source, name,
	), nil
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
