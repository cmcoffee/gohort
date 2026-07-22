package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
)

// --- pluggable image backends ------------------------------------------------
//
// The built-in providers (gemini, openai) are compiled in. A rest_image
// connector registers itself here so the SAME native pipeline — the default
// generate_image tool, writer-app illustrations (GenerateImageLandscape /
// GenerateImageWithProfile), and the admin image-provider setting — can target
// it by name, with no per-service code. This is the generic seam that makes a
// ComfyUI / Automatic1111 / any spec-declared backend a first-class image
// source everywhere the native providers are.

// ImageBackendFunc produces an image for a named backend. landscape mirrors the
// native providers' aspect switch (wide vs square).
type ImageBackendFunc func(ctx context.Context, prompt string, landscape bool) (*ImageGenResult, error)

var (
	imageBackendMu sync.RWMutex
	imageBackends  = map[string]ImageBackendFunc{}
)

// RegisterImageBackend installs (or replaces) a named image backend. Idempotent:
// re-registering the same name (e.g. a connector re-materialize on restart) just
// refreshes the closure.
func RegisterImageBackend(name string, fn ImageBackendFunc) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		return
	}
	imageBackendMu.Lock()
	imageBackends[name] = fn
	imageBackendMu.Unlock()
}

// ImageBackendRegistered reports whether a named backend exists — lets the admin
// UI show approved connector backends as image-provider choices.
func ImageBackendRegistered(name string) bool {
	imageBackendMu.RLock()
	defer imageBackendMu.RUnlock()
	_, ok := imageBackends[strings.TrimSpace(name)]
	return ok
}

func lookupImageBackend(name string) (ImageBackendFunc, bool) {
	imageBackendMu.RLock()
	defer imageBackendMu.RUnlock()
	fn, ok := imageBackends[strings.TrimSpace(name)]
	return fn, ok
}

// ImageDir returns the directory where generated images are stored.
// Defaults to os.TempDir()/gohort-images if not set via SetImageDir.
func ImageDir() string {
	if imageDir != "" {
		return imageDir
	}
	return filepath.Join(os.TempDir(), "gohort-images")
}

// SetImageDir configures the persistent image storage directory.
func SetImageDir(dir string) {
	imageDir = dir
	os.MkdirAll(dir, 0755)
}

var imageDir string

// BrowserDir returns the directory where go-rod stores its Chromium binary and
// user data. Defaults to os.TempDir()/gohort-browser if not set via SetBrowserDir.
func BrowserDir() string {
	if browserDir != "" {
		return browserDir
	}
	return filepath.Join(os.TempDir(), "gohort-browser")
}

// SetBrowserDir configures the directory used for Chromium binary caching.
func SetBrowserDir(dir string) {
	browserDir = dir
	os.MkdirAll(dir, 0755)
}

var browserDir string

// GeminiKeyFunc is set by the main package to provide the Gemini API key.
var GeminiKeyFunc func() string

// OpenAIKeyFunc is set by the main package to provide the OpenAI API key.
var OpenAIKeyFunc func() string

// ImageProviderFunc returns the configured image provider ("gemini", "openai", "none").
var ImageProviderFunc func() string

// ImageGenProfile holds provider and key for a named image generation slot.
type ImageGenProfile struct {
	Provider string // "gemini", "openai", or "none"
	APIKey   string
}

// ImageGenProfileFunc looks up a named image generation profile by name.
// Returns a zero-value ImageGenProfile when the profile is not configured.
// Set by the main package at startup.
var ImageGenProfileFunc func(name string) ImageGenProfile

// ImageGenerationAvailable reports whether the default image generation provider
// is configured. Returns false when no provider is set or it is "none".
func ImageGenerationAvailable() bool {
	if ImageProviderFunc == nil {
		return false
	}
	p := ImageProviderFunc()
	return p != "" && p != "none"
}

// generateImageChatTool is no longer registered — the canonical
// generate_image tool lives in tools/imagefetch/. The struct +
// methods remain in case private code references them directly,
// but there's no global registration to compete with the
// imagefetch version.
func init() {
	// Tier-2 auto-retry knobs. The soft budget drives how hard the tool nudges
	// the model to regenerate a checkable miss; the hard cap is a runaway guard
	// set well above it so genuine multi-image turns ("make me 4 logos") aren't
	// blocked. Both are per-turn (the attempt counter is session-scoped).
	RegisterTunable(TunableSpec{Key: "tune_image_max_regens", Category: "Limits", Label: "Image auto-regeneration budget", Help: "How many times the model may regenerate an image to fix a CHECKABLE miss (a specific count, named object, or required text) before it must deliver the best result. 0 disables auto-retry — the model still SEES the image (Tier 1) but is told to caveat rather than retry.", Kind: KindInt, Default: 2, Min: 0, Max: 5})
	RegisterTunable(TunableSpec{Key: "tune_image_gen_hard_cap", Category: "Limits", Label: "Image generations per turn (hard cap)", Help: "Absolute ceiling on generate_image calls in one turn — a runaway guard, not the retry budget. Set well above the auto-regeneration budget so legitimate multi-image requests still work.", Kind: KindInt, Default: 10, Min: 1, Max: 50})
}

// imageMaxRegens / imageGenHardCap read the Tier-2 knobs with safe fallbacks.
func imageMaxRegens() int {
	n := TuneInt("tune_image_max_regens")
	if n < 0 {
		return 0
	}
	return n
}

func imageGenHardCap() int {
	if n := TuneInt("tune_image_gen_hard_cap"); n >= 1 {
		return n
	}
	return 10
}

// imageVerifyText is the attempt-aware tool result: it puts the checkable-criteria
// GATE in front of the model (which alone can judge whether the request has
// objective criteria and whether they were met) and tracks the remaining
// regeneration budget so the retry chain is bounded.
func imageVerifyText(attempt, maxRegens int) string {
	const base = "IMAGE:generated. The image is shown to you now — verify it against what the user EXPLICITLY asked for: a specific count, a named object, or required/visible text. "
	const tail = " Do not describe or try to open a file path."
	if maxRegens <= 0 {
		return base + "If it misses an explicit requirement, tell the user plainly; otherwise deliver it." + tail
	}
	if regensLeft := maxRegens - (attempt - 1); regensLeft > 0 {
		return base + fmt.Sprintf("If it FAILS one of those checkable requirements, call generate_image again with a CORRECTED prompt that fixes the specific miss (do not repeat the same prompt) and refine_of_previous=true — %d regeneration(s) left. Regenerate ONLY for such explicit requirements, never for aesthetic or stylistic preference. Otherwise deliver it.", regensLeft) + tail
	}
	return base + "The auto-regeneration budget for this image is spent — do NOT call generate_image again to retry it. Deliver this result; if it still falls short of an explicit requirement, tell the user plainly rather than retrying." + tail
}

type generateImageChatTool struct{}

func (t *generateImageChatTool) Name() string { return "generate_image" }
func (t *generateImageChatTool) Desc() string {
	return "Generate a NEW image from a text description via the configured image-generation backend and return the image URL. USE ONLY when the user explicitly asks to CREATE, DRAW, MAKE, or GENERATE a fresh image — e.g. \"draw me a dragon\", \"create a logo\", \"generate an illustration\". DO NOT use this for finding existing images (use find_image), downloading from a known URL (use fetch_image), or capturing a webpage (use screenshot_page). For real-world reference images, generation is the wrong tool — it produces invented content, not real photos."
}
func (t *generateImageChatTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"prompt":             {Type: "string", Description: "A detailed description of the image to generate."},
		"refine_of_previous": {Type: "boolean", Description: "Set true ONLY when this call regenerates the PREVIOUS image to fix a specific checkable miss (a failed count, named object, or required text). Leave false or omit for a new, distinct image — that starts a fresh regeneration budget."},
	}
}
func (t *generateImageChatTool) Run(args map[string]any) (string, error) {
	if !ImageGenerationAvailable() {
		return "", fmt.Errorf("image generation is not configured")
	}
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	result, err := GenerateImage(context.Background(), "", prompt)
	if err != nil {
		return "", err
	}
	// Return a short reference instead of embedding the full base64 payload
	// — that would bloat every future LLM request with megabytes of image data.
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		return "IMAGE:" + result.URL, nil
	}
	os.Remove(result.URL)
	return "IMAGE:generated (local file: " + result.URL + ")", nil
}

// RunWithSession generates an image, delivers it to the user, and shows it to the
// LLM to self-verify (Tier 1). Tier 2 bounds the retry chain: a per-turn attempt
// counter drives an escalating, checkable-criteria-gated instruction and a hard
// runaway cap. Returns a short reference to avoid bloating LLM context with base64.
func (t *generateImageChatTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if !ImageGenerationAvailable() {
		return "", fmt.Errorf("image generation is not configured")
	}
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	// Tier-2 accounting. Count this call BEFORE generating so the runaway guard
	// can refuse without burning a diffusion run. `refine` continues the retry
	// chain (bounded budget); a new subject resets the chain but the absolute
	// `total` still climbs, so the hard cap can't be evaded by mislabeling.
	refine, _ := args["refine_of_previous"].(bool)
	attempt, total := sess.NextImageAttempt(refine)
	if hardCap := imageGenHardCap(); total > hardCap {
		return "", fmt.Errorf("image generation limit reached for this turn (%d calls) — deliver the best result so far or tell the user what fell short instead of regenerating again", hardCap)
	}
	// On a refine (chain length > 1), tell the user the wait is deliberate
	// (nil-safe: apps without live status just ignore it).
	if attempt > 1 && sess.StatusCallback != nil {
		sess.StatusCallback("Refining the image to better match your request…")
	}

	result, err := GenerateImage(context.Background(), "", prompt)
	if err != nil {
		return "", err
	}
	// For HTTP URLs (DALL-E), return the URL — no local bytes to show or verify.
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		return "IMAGE:" + result.URL, nil
	}
	// For local files (self-hosted ComfyUI / Gemini), read the bytes once and use
	// them for two independent channels: AppendImage delivers the image to the
	// USER (outbox), and AppendViewImage shows it to the LLM on its next round so
	// it can VERIFY the result matches the request before presenting it. The bytes
	// never bloat persisted history — the vision message is injected for that one
	// round and drained. We do NOT echo the file path: it's removed immediately
	// below, and a stale path only invites the model to hunt for a file that no
	// longer exists (see connector_restimage note).
	data, err := os.ReadFile(result.URL)
	if err == nil {
		sess.AppendImage(base64.StdEncoding.EncodeToString(data)) // → user (outbox)
		sess.AppendViewImage(data)                                // → LLM (self-verify)
	}
	os.Remove(result.URL)
	if err != nil {
		return "IMAGE:generated", nil
	}
	return imageVerifyText(attempt, imageMaxRegens()), nil
}

func (t *generateImageChatTool) IsInternetTool() bool { return true }

// ImageProfileAvailable reports whether a named image generation profile is
// configured and usable. Falls back to ImageGenerationAvailable when the name
// is "default" or the profile func is not set.
func ImageProfileAvailable(name string) bool {
	if name == "default" || name == "" {
		return ImageGenerationAvailable()
	}
	if ImageGenProfileFunc == nil {
		return false
	}
	p := ImageGenProfileFunc(name)
	return p.Provider != "" && p.Provider != "none"
}

// ImageGenResult holds the output of an image generation call.
type ImageGenResult struct {
	URL    string // URL or local path to the generated image
	Prompt string // the prompt that was used
}

// generateWithProvider dispatches to the appropriate backend.
// Both provider and apiKey must already be resolved by the caller.
// landscape=true produces a wide 16:9 image; false produces 1024x1024 square.
func generateWithProvider(ctx context.Context, provider, apiKey, prompt string, landscape bool) (result *ImageGenResult, err error) {
	defer func() {
		if err == nil && result != nil {
			ProcessUsage().AddImageCall()
		}
	}()

	switch provider {
	case "none":
		return nil, fmt.Errorf("image generation disabled")
	case "gemini":
		if apiKey == "" {
			return nil, fmt.Errorf("Gemini API key not configured for image generation")
		}
		return generateGeminiImage(ctx, apiKey, prompt, landscape)
	case "openai":
		if apiKey == "" {
			return nil, fmt.Errorf("OpenAI API key not configured for image generation")
		}
		result, err = doOpenAIRequest(ctx, apiKey, prompt, landscape)
		if err != nil && isServerError(err) {
			Debug("[image_gen] DALL-E first attempt failed, retrying: %s", err)
			time.Sleep(2 * time.Second)
			result, err = doOpenAIRequest(ctx, apiKey, prompt, landscape)
		}
		return result, err
	default:
		// A spec-declared backend (rest_image connector) registered by name.
		if fn, ok := lookupImageBackend(provider); ok {
			return fn(ctx, prompt, landscape)
		}
		return nil, fmt.Errorf("unknown image provider: %s", provider)
	}
}

func resolveDefaultProviderAndKey() (provider, apiKey string) {
	provider = "gemini"
	if ImageProviderFunc != nil {
		if p := ImageProviderFunc(); p != "" {
			provider = p
		}
	}
	switch provider {
	case "gemini":
		if GeminiKeyFunc != nil {
			apiKey = GeminiKeyFunc()
		}
	case "openai":
		if OpenAIKeyFunc != nil {
			apiKey = OpenAIKeyFunc()
		}
	}
	return
}

// GenerateImage generates a landscape 16:9 image.
func GenerateImage(ctx context.Context, apiKey, prompt string) (*ImageGenResult, error) {
	provider, defaultKey := resolveDefaultProviderAndKey()
	if apiKey == "" {
		apiKey = defaultKey
	}
	return generateWithProvider(ctx, provider, apiKey, prompt, true)
}

// GenerateImageLandscape generates a wide 16:9 image (blog/article use).
func GenerateImageLandscape(ctx context.Context, apiKey, prompt string) (*ImageGenResult, error) {
	provider, defaultKey := resolveDefaultProviderAndKey()
	if apiKey == "" {
		apiKey = defaultKey
	}
	return generateWithProvider(ctx, provider, apiKey, prompt, true)
}

// GenerateImageWithProfile generates an image using a named profile.
// Falls back to the default provider when the named profile is not configured.
func GenerateImageWithProfile(ctx context.Context, profile, prompt string) (*ImageGenResult, error) {
	if profile != "" && profile != "default" && ImageGenProfileFunc != nil {
		p := ImageGenProfileFunc(profile)
		if p.Provider != "" && p.Provider != "none" {
			key := p.APIKey
			if key == "" {
				switch p.Provider {
				case "gemini":
					if GeminiKeyFunc != nil {
						key = GeminiKeyFunc()
					}
				case "openai":
					if OpenAIKeyFunc != nil {
						key = OpenAIKeyFunc()
					}
				}
			}
			return generateWithProvider(ctx, p.Provider, key, prompt, false)
		}
	}
	return GenerateImage(ctx, "", prompt)
}

// generateGeminiImage uses Gemini's Imagen model to generate an image.
// Returns a local file path since Imagen returns base64 data, not a URL.
func generateGeminiImage(ctx context.Context, apiKey, prompt string, landscape bool) (*ImageGenResult, error) {
	aspectRatio := "1:1"
	if landscape {
		aspectRatio = "16:9"
	}
	body := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"responseModalities": []string{"TEXT", "IMAGE"},
			"imageConfig": map[string]interface{}{
				"aspectRatio": aspectRatio,
			},
		},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	client := &apiclient.APIClient{
		Server:         "generativelanguage.googleapis.com",
		RequestTimeout: 120 * time.Second,
		VerifySSL:      true,
		AuthFunc: func(req *http.Request) {
			q := req.URL.Query()
			q.Set("key", apiKey)
			req.URL.RawQuery = q.Encode()
		},
	}

	req, err := client.NewRequestWithContext(ctx, "POST", "/v1beta/models/gemini-2.5-flash-image:generateContent")
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(bytes.NewReader(payload))
	req.ContentLength = int64(len(payload))

	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return nil, fmt.Errorf("Gemini image request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Gemini response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Gemini image generation failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData *struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData,omitempty"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini response: %w", err)
	}

	// Find the first image part in the response.
	var imgBase64 string
	var mimeType string
	for _, c := range result.Candidates {
		for _, p := range c.Content.Parts {
			if p.InlineData != nil && strings.HasPrefix(p.InlineData.MimeType, "image/") {
				imgBase64 = p.InlineData.Data
				mimeType = p.InlineData.MimeType
				break
			}
		}
		if imgBase64 != "" {
			break
		}
	}
	if imgBase64 == "" {
		return nil, fmt.Errorf("Gemini returned no image data")
	}

	// Decode base64 and save to a temp file.
	imgData, err := base64.StdEncoding.DecodeString(imgBase64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	ext := ".png"
	if strings.Contains(mimeType, "jpeg") || strings.Contains(mimeType, "jpg") {
		ext = ".jpg"
	}

	imgDir := ImageDir()
	os.MkdirAll(imgDir, 0755)
	tmpFile := filepath.Join(imgDir, UUIDv4()+ext)
	if err := os.WriteFile(tmpFile, imgData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write image: %w", err)
	}

	Debug("[image_gen] Gemini image saved: %s (%d bytes)", tmpFile, len(imgData))

	return &ImageGenResult{
		URL:    tmpFile,
		Prompt: prompt,
	}, nil
}

func isServerError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 500") || strings.Contains(msg, "HTTP 502") || strings.Contains(msg, "HTTP 503")
}

func doOpenAIRequest(ctx context.Context, apiKey, prompt string, landscape bool) (*ImageGenResult, error) {
	size := "1024x1024"
	if landscape {
		size = "1792x1024"
	}
	body := map[string]interface{}{
		"model":   "dall-e-3",
		"prompt":  prompt,
		"n":       1,
		"size":    size,
		"quality": "standard",
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	client := &apiclient.APIClient{
		Server:         "api.openai.com",
		RequestTimeout: 120 * time.Second,
		VerifySSL:      true,
		AuthFunc: func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		},
	}

	req, err := client.NewRequestWithContext(ctx, "POST", "/v1/images/generations")
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(bytes.NewReader(payload))
	req.ContentLength = int64(len(payload))

	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return nil, fmt.Errorf("image generation request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read image response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("image generation failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data []struct {
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse image response: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("image generation returned no results")
	}

	return &ImageGenResult{
		URL:    result.Data[0].URL,
		Prompt: result.Data[0].RevisedPrompt,
	}, nil
}
