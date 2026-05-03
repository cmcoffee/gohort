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
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
)

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

func init() {
	RegisterChatTool(&generateImageChatTool{})
}

type generateImageChatTool struct{}

func (t *generateImageChatTool) Name() string { return "generate_image" }
func (t *generateImageChatTool) Desc() string {
	return "Generate an AI image from a text description and return the image URL."
}
func (t *generateImageChatTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"prompt": {Type: "string", Description: "A detailed description of the image to generate."},
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

// RunWithSession generates an image and appends it to the session's image list
// so it gets delivered as an outbox attachment. Returns a short reference to
// avoid bloating LLM context with base64 image data.
func (t *generateImageChatTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
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
	// For HTTP URLs (DALL-E), return the URL — no session append needed.
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		return "IMAGE:" + result.URL, nil
	}
	// For local files (Gemini), read and encode the image for session delivery,
	// then return a short reference to avoid bloating LLM context.
	data, err := os.ReadFile(result.URL)
	if err == nil {
		sess.AppendImage(base64.StdEncoding.EncodeToString(data))
	}
	os.Remove(result.URL)
	if err != nil {
		return "IMAGE:generated", nil
	}
	return "IMAGE:generated (local file: " + result.URL + ")", nil
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
