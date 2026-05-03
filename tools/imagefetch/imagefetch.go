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
	return "Download an image from a URL so it can be sent as an attachment. Use this after finding an image URL via web_search. The image will be delivered alongside your text reply."
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
	return "Search for an image by description and send it as an attachment. Handles the full search-and-download in one step — use this instead of web_search + fetch_image when the user asks you to find and send a picture, meme, GIF, or photo."
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
	results, err := serperImageSearch(query, cfg.APIKey)
	if err != nil {
		return "", fmt.Errorf("image search failed: %w", err)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("no image results found for %q", query)
	}

	type candidate struct {
		raw  []byte
		b64  string
		meta serperImageResult
	}
	var candidates []candidate
	for _, r := range results {
		if len(candidates) >= 5 {
			break
		}
		data, err := fetchImageRequest(r.ImageURL, r.Link, 20)
		if err != nil {
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

	sess.AppendImage(candidates[chosen].b64)
	Log("[imagefetch/find_image] query=%q chose candidate %d/%d (title: %q, source: %s)",
		query, chosen+1, len(candidates), candidates[chosen].meta.Title, candidates[chosen].meta.Source)
	return fmt.Sprintf(
		"Image found (title: %q, source: %s). It is attached and will be delivered with your reply. "+
			"Tell the user you found it — do NOT say you could not find an image.",
		candidates[chosen].meta.Title, candidates[chosen].meta.Source,
	), nil
}

// --- GenerateImageTool ---

type GenerateImageTool struct{}

func (t *GenerateImageTool) Name() string { return "generate_image" }
func (t *GenerateImageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} } // image-gen API call
func (t *GenerateImageTool) Desc() string {
	return "Generate an image from a text description and send it as an attachment. Use this when the user asks you to create, draw, generate, or make an image."
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
	sess.AppendImage(base64.StdEncoding.EncodeToString(data))
	Log("[imagefetch/generate_image] generated image for prompt: %s", truncate(prompt, 80))
	return "Image generated and will be sent to the user with your reply.", nil
}

// --- Serper image search ---

type serperImageResult struct {
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

func serperImageSearch(query, apiKey string) ([]serperImageResult, error) {
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

	var out []serperImageResult
	for _, img := range result.Images {
		if img.ImageURL != "" {
			out = append(out, serperImageResult{
				ImageURL: img.ImageURL,
				Title:    img.Title,
				Source:   img.Source,
				Link:     img.Link,
			})
		}
	}
	if len(out) > 0 {
		ProcessUsage().AddSearchCall()
	}
	return out, nil
}

// --- HTTP helpers ---

const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

func fetchImageRequest(rawURL, referer string, timeoutSecs int) ([]byte, error) {
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
	return fetchImageRequest(rawURL, "", 30)
}

func downloadImageTo(rawURL string, sess *ToolSession) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid image URL: %s", rawURL)
	}
	data, err := fetchImageRequest(rawURL, "", 20)
	if err != nil {
		return "", err
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("URL does not appear to be an image (detected: %s)", ct)
	}
	sess.AppendImage(base64.StdEncoding.EncodeToString(data))
	Log("[imagefetch/fetch_image] fetched %d bytes from %s", len(data), rawURL)
	return "Image fetched and will be sent to the user with your reply.", nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
