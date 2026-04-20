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

// GeminiKeyFunc is set by the main package to provide the Gemini API key.
var GeminiKeyFunc func() string

// OpenAIKeyFunc is set by the main package to provide the OpenAI API key.
var OpenAIKeyFunc func() string

// ImageProviderFunc returns the configured image provider ("gemini", "openai", "none").
var ImageProviderFunc func() string

// ImageGenResult holds the output of an image generation call.
type ImageGenResult struct {
	URL    string // URL or local path to the generated image
	Prompt string // the prompt that was used
}

// GenerateImage generates an image from a prompt using the configured provider.
// On success, bumps ProcessUsage's image counter so per-run telemetry and cost
// estimates reflect the call. Counter fires once per returned image regardless
// of provider — Gemini/Imagen and DALL-E are priced at the per-call ImagePerCall
// rate in CostRates (not per-resolution; see AddImageCall comment).
func GenerateImage(ctx context.Context, apiKey, prompt string) (result *ImageGenResult, err error) {
	defer func() {
		if err == nil && result != nil {
			ProcessUsage().AddImageCall()
		}
	}()

	provider := "gemini"
	if ImageProviderFunc != nil {
		if p := ImageProviderFunc(); p != "" {
			provider = p
		}
	}

	switch provider {
	case "none":
		return nil, fmt.Errorf("image generation disabled")
	case "gemini":
		geminiKey := apiKey
		if GeminiKeyFunc != nil {
			if k := GeminiKeyFunc(); k != "" {
				geminiKey = k
			}
		}
		if geminiKey == "" {
			return nil, fmt.Errorf("Gemini API key not configured for image generation")
		}
		return generateGeminiImage(ctx, geminiKey, prompt)
	case "openai":
		if apiKey == "" {
			return nil, fmt.Errorf("OpenAI API key not configured for image generation")
		}
		result, err = doOpenAIRequest(ctx, apiKey, prompt)
		if err != nil && isServerError(err) {
			Debug("[image_gen] DALL-E first attempt failed, retrying: %s", err)
			time.Sleep(2 * time.Second)
			result, err = doOpenAIRequest(ctx, apiKey, prompt)
		}
		return result, err
	default:
		return nil, fmt.Errorf("unknown image provider: %s", provider)
	}
}

// generateGeminiImage uses Gemini's Imagen model to generate an image.
// Returns a local file path since Imagen returns base64 data, not a URL.
func generateGeminiImage(ctx context.Context, apiKey, prompt string) (*ImageGenResult, error) {
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
				"aspectRatio": "16:9",
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

func doOpenAIRequest(ctx context.Context, apiKey, prompt string) (*ImageGenResult, error) {
	body := map[string]interface{}{
		"model":   "dall-e-3",
		"prompt":  prompt,
		"n":       1,
		"size":    "1792x1024",
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
