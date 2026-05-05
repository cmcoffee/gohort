package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
	"github.com/cmcoffee/snugforge/iotimeout"
)

const (
	visionMaxDim  = 1024 // longest side for images sent to LLM
	visionJQQual  = 82   // JPEG quality for resized images
)

// LLM API clients apply iotimeout.NewReadCloser to every response body using
// c.api.RequestTimeout as the per-read idle timeout. These defaults give:
//   - llmConnectTimeout:  dial + TLS handshake cap
//   - llmRequestTimeout:  response header deadline AND per-read idle timeout
//                         on body reads. Must be long enough to tolerate
//                         Ollama cold-load silence and extended thinking.
const (
	llmConnectTimeout = 10 * time.Second
	llmRequestTimeout = 5 * time.Minute
)

const (
	openAIEndpoint = "https://api.openai.com/v1"
	ollamaEndpoint = "http://localhost:11434/v1"
)

// fmtThink stringifies a *bool Think flag as "true", "false", or "default"
// (when nil). Plain %v on a *bool prints a pointer address, which is noise.
func fmtThink(p *bool) string {
	if p == nil {
		return "default"
	}
	if *p {
		return "true"
	}
	return "false"
}

// thinkBlockRE matches <think>...</think> blocks that thinking models
// embed in content when using the OpenAI-compatible endpoint.
var thinkBlockRE = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)

// thinkUnclosedRE matches truncated <think> blocks where the model hit
// the token limit before emitting </think>.
var thinkUnclosedRE = regexp.MustCompile(`(?s)^<think>.*`)

// openAIClient implements the LLM interface for OpenAI-compatible APIs.
type openAIClient struct {
	apiKey          string
	model           string
	endpoint        string
	api             *apiclient.APIClient
	ollama          bool // true when created via NewOllamaLLM
	llamacpp        bool // true when provider is llama.cpp server
	llamacppBudget  int  // llama.cpp: default thinking_budget_tokens (0 = server default, >0 = limit)
	contextSize     int  // Ollama num_ctx; 0 uses ollamaDefaultCtx
	disableThinking  bool // master override forcing think=false / thinking_budget_tokens=0 on every call.
	nativeTools      bool // When true, send native tool specs. When false, strip them (tools handled via text prompts at agent loop level).
	thinkingBudget   int  // reserved (unused after Ollama thinking_budget removal).
}

// isOllama reports whether this client is talking to an Ollama instance.
func (c *openAIClient) isOllama() bool {
	return c.ollama
}

// provider returns the log tag for this client.
func (c *openAIClient) provider() string {
	if c.llamacpp {
		return "llama.cpp"
	}
	return "openai"
}

// llamacppThinkBudget returns the thinking_budget_tokens value for a llama.cpp
// request, or nil to omit. The primary disable path is chat_template_kwargs
// (reliable on Qwen 3.6+); we still send budget=0 alongside as a fallback for
// models whose chat templates don't read enable_thinking (e.g. DeepSeek-R1),
// where a sampler-level cap is the only way to suppress thinking.
func (c *openAIClient) llamacppThinkBudget(cfg ChatConfig) *int {
	// Explicit per-call disable: also cap budget to 0 as a fallback for
	// models that don't honor chat_template_kwargs.enable_thinking.
	if cfg.Think != nil && !*cfg.Think {
		zero := 0
		return &zero
	}
	// Per-call budget override takes priority over global config.
	if cfg.ThinkBudget != nil && *cfg.ThinkBudget > 0 {
		return cfg.ThinkBudget
	}
	// Global configured budget.
	if c.llamacppBudget > 0 {
		return &c.llamacppBudget
	}
	// No client-side cap — defer to the llama-server's launch-time
	// --reasoning-budget config. Apps that need a tighter cap should
	// pass WithThinkBudget(n) per-call, or operators can set the
	// global llamacppBudget via config / admin UI.
	return nil
}

// llamacppChatTemplateKwargs returns the chat_template_kwargs map for a
// llama.cpp request, or nil to omit the field. This is the reliable
// per-request thinking switch on Qwen 3.6+: setting enable_thinking
// overrides the launch-time --reasoning flag for this single request.
func (c *openAIClient) llamacppChatTemplateKwargs(cfg ChatConfig) map[string]any {
	if cfg.Think == nil {
		return nil // let the server's launch default apply
	}
	return map[string]any{"enable_thinking": *cfg.Think}
}

// Ping implements the Pinger interface. For Ollama it issues a bounded
// GET /api/ps which returns immediately regardless of whether a
// generation is currently in flight — so the caller can distinguish
// "server down" from "server busy" without waiting on the chat queue.
// For non-Ollama backends it currently returns nil (no queue-wait
// problem to work around); real reachability is still validated by the
// subsequent chat call.
func (c *openAIClient) Ping(ctx context.Context) error {
	if !c.ollama {
		return nil
	}
	req, err := c.api.NewRequestWithContext(ctx, "GET", "/api/ps")
	if err != nil {
		return err
	}
	resp, err := c.api.SendRawRequest("", req)
	if err != nil {
		return fmt.Errorf("ollama unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("ollama /api/ps returned %s", resp.Status)
	}
	return nil
}

// NewOpenAILLM creates an LLM client for OpenAI (ChatGPT) using the default HTTP client.
func NewOpenAILLM(apiKey string, model string) LLM {
	return newOpenAILLM(apiKey, model, openAIEndpoint, nil)
}

// OpenAIModels queries the OpenAI API and returns available model IDs.
func OpenAIModels(apiKey string) ([]string, error) {
	client := &apiclient.APIClient{
		Server:         "api.openai.com",
		VerifySSL:      true,
		ConnectTimeout: llmConnectTimeout,
		RequestTimeout: 15 * time.Second, // small, fast models listing
		AuthFunc: func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		},
	}
	req, err := client.NewRequest("GET", "/v1/models")
	if err != nil {
		return nil, err
	}
	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	resp.Body = iotimeout.NewReadCloser(resp.Body, client.RequestTimeout)
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	var names []string
	for _, m := range result.Data {
		names = append(names, m.ID)
	}
	return names, nil
}

// LlamaCppModels queries the llama.cpp HTTP server at the given base URL
// (e.g. "http://localhost:8080" or "http://localhost:8080/v1") and returns
// the model IDs it advertises via its OpenAI-compatible /v1/models endpoint.
func LlamaCppModels(baseURL string) ([]string, error) {
	u, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil {
		return nil, err
	}
	// llama.cpp's models endpoint is /v1/models. Honor an existing /v1 path
	// if the user already included it; otherwise add it.
	path := strings.TrimSuffix(u.Path, "/")
	if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	path += "/models"
	client := &apiclient.APIClient{
		Server:         u.Host,
		URLScheme:      u.Scheme,
		ConnectTimeout: llmConnectTimeout,
		RequestTimeout: 15 * time.Second,
		AuthFunc:       func(req *http.Request) {},
	}
	req, err := client.NewRequest("GET", path)
	if err != nil {
		return nil, err
	}
	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	resp.Body = iotimeout.NewReadCloser(resp.Body, client.RequestTimeout)
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	var names []string
	for _, m := range result.Data {
		names = append(names, m.ID)
	}
	return names, nil
}

// OllamaModels queries the ollama instance at the given base URL
// (e.g. "http://localhost:11434") and returns the names of installed models.
func OllamaModels(baseURL string) ([]string, error) {
	u, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil {
		return nil, err
	}
	client := &apiclient.APIClient{
		Server:         u.Host,
		URLScheme:      u.Scheme,
		ConnectTimeout: llmConnectTimeout,
		RequestTimeout: 15 * time.Second,
		AuthFunc:       func(req *http.Request) {},
	}
	req, err := client.NewRequest("GET", "/api/tags")
	if err != nil {
		return nil, err
	}
	resp, err := client.SendRawRequest("", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	resp.Body = iotimeout.NewReadCloser(resp.Body, client.RequestTimeout)
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	var names []string
	for _, m := range result.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// NewOllamaLLM creates an LLM client for a local Ollama instance.
// Use endpoint to override the default (http://localhost:11434/v1).
func NewOllamaLLM(model string, endpoint ...string) LLM {
	ep := ollamaEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		ep = strings.TrimSuffix(endpoint[0], "/")
	}
	client := newOpenAILLM("", model, ep, nil)
	client.(*openAIClient).ollama = true
	return client
}

// newOpenAILLM creates an openAIClient with optional APIClient.
func newOpenAILLM(apiKey string, model string, endpoint string, api *apiclient.APIClient) LLM {
	endpoint = strings.TrimSuffix(endpoint, "/")
	if api == nil {
		api = &apiclient.APIClient{
			VerifySSL:      true,
			ConnectTimeout: llmConnectTimeout,
			RequestTimeout: llmRequestTimeout,
		}
	}
	// Parse the endpoint to set the server and scheme on the APIClient.
	if u, err := url.Parse(endpoint); err == nil {
		api.Server = u.Host
		api.URLScheme = u.Scheme
	}
	// Set auth: Bearer token for OpenAI, no-op for Ollama.
	if apiKey != "" {
		api.StaticToken = apiKey
	} else {
		api.AuthFunc = func(req *http.Request) {}
	}
	return &openAIClient{
		apiKey:   apiKey,
		model:    model,
		endpoint: endpoint,
		api:      api,
	}
}

// openAI request/response types

type oaiResponseFormat struct {
	Type string `json:"type"`
}

type oaiRequest struct {
	Model                string             `json:"model"`
	Messages             []oaiMessage       `json:"messages"`
	MaxTokens            int                `json:"max_tokens,omitempty"`
	Temperature          *float64           `json:"temperature,omitempty"`
	Stream               bool               `json:"stream,omitempty"`
	StreamOptions        *oaiStreamOptions  `json:"stream_options,omitempty"` // OpenAI-compat: include_usage=true is required for streaming responses to carry a final usage block (input/output/reasoning_tokens). Without it, streamed responses arrive with zero token counts.
	Tools                []oaiTool          `json:"tools,omitempty"`
	ResponseFormat       *oaiResponseFormat `json:"response_format,omitempty"`
	Think                *bool              `json:"think,omitempty"`
	ThinkingBudgetTokens *int               `json:"thinking_budget_tokens,omitempty"` // llama.cpp: budget when thinking is on; do NOT use 0 to disable (unreliable on Qwen 3.6)
	ChatTemplateKwargs   map[string]any     `json:"chat_template_kwargs,omitempty"`   // llama.cpp: per-request {"enable_thinking": bool} — works reliably even when launch is --reasoning on
	Options              map[string]any     `json:"options,omitempty"`                // Ollama-specific options (num_ctx, etc.)
}

type oaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ollamaDefaultCtx is the default context window size for Ollama when not configured.
// Set higher than Ollama's built-in 32K default to handle large prompts.
const ollamaDefaultCtx = 65536

// ollamaNumCtx returns the context size to use, preferring the configured value.
func (c *openAIClient) ollamaNumCtx() int {
	if c.contextSize > 0 {
		return c.contextSize
	}
	return ollamaDefaultCtx
}

// ContextSize implements the ContextSizer interface.
func (c *openAIClient) ContextSize() int {
	return c.ollamaNumCtx()
}

// warmupContext sends a minimal request to Ollama's native /api/chat endpoint
// to force the model to load with the desired num_ctx. The OpenAI-compatible
// /v1/chat/completions endpoint ignores the options field, so this is needed
// to set the context window size.
func (c *openAIClient) warmupContext() {
	numCtx := c.ollamaNumCtx()

	payload := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"options":{"num_ctx":%d},"stream":false}`, c.model, numCtx)
	body := []byte(payload)

	req, err := c.api.NewRequest("POST", "/api/chat")
	if err != nil {
		Debug("[ollama] context warmup failed: %s", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))

	resp, err := c.api.SendRawRequest("", req)
	if err != nil {
		Debug("[ollama] context warmup failed: %s", err)
		return
	}
	resp.Body.Close()
	Debug("[ollama] context warmup: num_ctx=%d", numCtx)
}

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaiToolCallMsg struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiMessage struct {
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content"`
	ToolCalls  []oaiToolCallMsg `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// oaiTextContent creates a Content field with a plain text string.
func oaiTextContent(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// resizeImage resizes a single image to visionMaxDim on the longest side
// and encodes it as JPEG at visionJQQual quality. Returns the encoded bytes;
// on any decode/encode failure returns the original src unchanged.
func resizeImage(src []byte) []byte {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return src // fallback to original if decode fails
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= visionMaxDim && h <= visionMaxDim {
		return src // already small enough
	}
	// Compute new dimensions preserving aspect ratio.
	var nw, nh int
	if w >= h {
		nw = visionMaxDim
		nh = h * visionMaxDim / w
	} else {
		nh = visionMaxDim
		nw = w * visionMaxDim / h
	}
	resized := resizePNG(img, nw, nh)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: visionJQQual}); err != nil {
		return src
	}
	return buf.Bytes()
}

// resizePNG scales img to nw×nh using a simple box filter (fast, good enough for vision).
func resizePNG(img image.Image, nw, nh int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	dx, dy := float64(img.Bounds().Dx()), float64(img.Bounds().Dy())
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			sx := float64(x) * dx / float64(nw)
			sy := float64(y) * dy / float64(nh)
			if sx >= dx {
				sx = dx - 1
			}
			if sy >= dy {
				sy = dy - 1
			}
			dst.Set(x, y, img.At(int(sx), int(sy)))
		}
	}
	return dst
}

// hasImages reports whether any message in the slice carries images or
// videos. Videos count because they're sampled into still frames at send
// time, so the same vision-mode defaults (no thinking, deterministic temp,
// authoritative-location directive) should apply.
func hasImages(messages []Message) bool {
	for _, m := range messages {
		if len(m.Images) > 0 || len(m.Videos) > 0 {
			return true
		}
	}
	return false
}

// applyVisionDefaults disables thinking and forces temperature=0 when images
// are present. Most backends don't support extended reasoning alongside image
// inputs, and image analysis is factual — deterministic output is the default.
//
// It also appends a directive to the system prompt telling the model to trust
// the resolved `location:` field in [image_context] over its own coordinate-
// based recall. Without this, the model has been observed to confabulate a
// landmark in a wrong country even when an authoritative place name was right
// there in the prompt — pure training-data prior overpowering the context.
func applyVisionDefaults(cfg *ChatConfig, messages []Message) {
	if !hasImages(messages) {
		return
	}
	if cfg.Think == nil {
		f := false
		cfg.Think = &f
	} else if *cfg.Think {
		*cfg.Think = false
	}
	if cfg.Temperature == nil {
		t := 0.0
		cfg.Temperature = &t
	}
	cfg.SystemPrompt += "\n\nWhen an image's [image_context] block contains a `location:` field, that field is the authoritative source of where the photo was taken. Do not infer a different location from the `gps:` coordinates, the `taken:` timestamp, or your training data — the resolved name comes from a geocoding service and supersedes coordinate-based recall."
}

// oaiVisionContent creates a Content field with text + base64 images
// in the OpenAI vision format that Ollama also supports.
func oaiVisionContent(text string, images [][]byte) json.RawMessage {
	type imgURL struct {
		URL string `json:"url"`
	}
	type part struct {
		Type     string  `json:"type"`
		Text     string  `json:"text,omitempty"`
		ImageURL *imgURL `json:"image_url,omitempty"`
	}
	parts := []part{{Type: "text", Text: text}}
	for _, img := range images {
		img = resizeImage(img)
		mime := detectImageMIME(img)
		b64 := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img)
		parts = append(parts, part{Type: "image_url", ImageURL: &imgURL{URL: b64}})
	}
	b, _ := json.Marshal(parts)
	return b
}

// detectImageMIME returns the MIME type of image bytes.
// Falls back to image/jpeg for unrecognised formats (HEIC, AVIF, etc.).
func detectImageMIME(data []byte) string {
	ct := http.DetectContentType(data)
	switch ct {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return ct
	}
	// ISO Base Media File Format (HEIC, AVIF): bytes 4-7 are "ftyp".
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		return "image/jpeg" // treat as JPEG; caller should convert if possible
	}
	return "image/jpeg"
}

type oaiResponse struct {
	Choices []struct {
		Message struct {
			Content          string           `json:"content"`
			Reasoning        string           `json:"reasoning,omitempty"`         // some forks / older llama.cpp builds
			ReasoningContent string           `json:"reasoning_content,omitempty"` // current llama.cpp + Qwen 3.6
			ToolCalls        []oaiToolCallMsg `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens            int `json:"prompt_tokens"`
		CompletionTokens        int `json:"completion_tokens"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens,omitempty"`
		} `json:"completion_tokens_details,omitempty"`
	} `json:"usage"`
	// Timings is llama.cpp-specific — pure-decode and pure-prefill
	// throughput numbers, without the wall-clock-elapsed muddling that
	// comes from including network round-trip + prefill in the same
	// metric. Mirrors what llama.cpp's built-in web UI displays.
	Timings *oaiTimings `json:"timings,omitempty"`
}

type oaiTimings struct {
	PromptN            int     `json:"prompt_n,omitempty"`             // prompt tokens
	PromptMS           float64 `json:"prompt_ms,omitempty"`            // prefill wall-time
	PromptPerSecond    float64 `json:"prompt_per_second,omitempty"`    // prefill throughput
	PredictedN         int     `json:"predicted_n,omitempty"`          // generated tokens
	PredictedMS        float64 `json:"predicted_ms,omitempty"`         // decode wall-time
	PredictedPerSecond float64 `json:"predicted_per_second,omitempty"` // decode throughput — what llama.cpp's UI displays
}

type oaiStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string           `json:"content"`
			Reasoning        string           `json:"reasoning,omitempty"`
			ReasoningContent string           `json:"reasoning_content,omitempty"`
			ToolCalls        []oaiToolCallMsg `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Model string `json:"model"`
	Usage *struct {
		PromptTokens            int `json:"prompt_tokens"`
		CompletionTokens        int `json:"completion_tokens"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens,omitempty"`
		} `json:"completion_tokens_details,omitempty"`
	} `json:"usage,omitempty"`
	Timings *oaiTimings `json:"timings,omitempty"`
}

type oaiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *openAIClient) buildMessages(cfg ChatConfig, messages []Message) []oaiMessage {
	var msgs []oaiMessage
	if cfg.SystemPrompt != "" {
		msgs = append(msgs, oaiMessage{Role: "system", Content: oaiTextContent(cfg.SystemPrompt)})
	}
	lastIdx := len(messages) - 1
	for i, m := range messages {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			var calls []oaiToolCallMsg
			for _, tc := range m.ToolCalls {
				argsJSON, _ := json.Marshal(tc.Args)
				calls = append(calls, oaiToolCallMsg{
					ID:   tc.ID,
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: tc.Name, Arguments: string(argsJSON)},
				})
			}
			msgs = append(msgs, oaiMessage{Role: "assistant", Content: oaiTextContent(m.Content), ToolCalls: calls})
		case len(m.ToolResults) > 0:
			for _, tr := range m.ToolResults {
				msgs = append(msgs, oaiMessage{Role: "tool", Content: oaiTextContent(tr.Content), ToolCallID: tr.ID})
			}
		default:
			// Only include images/videos from the last message — older
			// attachments in history would be re-sent on every turn,
			// ballooning the context window.
			if (len(m.Images) > 0 || len(m.Videos) > 0) && i == lastIdx {
				// Prepend [image_context] / [video_context] blocks (mime/
				// size/dimensions + EXIF or container metadata, GPS →
				// place name) so the vision model has ambient grounding
				// alongside the pixels.
				text := m.Content
				var contextBlocks []string
				if meta := extractImagesMetadata(m.Images); meta != "" {
					Debug("[vision] extracted metadata for %d image(s):\n%s", len(m.Images), meta)
					contextBlocks = append(contextBlocks, meta)
				} else if len(m.Images) > 0 {
					Debug("[vision] no metadata extracted for %d image(s) (no EXIF, undecodable, or empty)", len(m.Images))
				}

				// Videos: extract container metadata and sample N frames.
				// The frames are concatenated onto Images so the LLM sees
				// them as a temporal sequence of stills. Failures (ffmpeg
				// missing, bad container, no decodable streams) silently
				// degrade — we send what we have, including raw text.
				images := m.Images
				if len(m.Videos) > 0 {
					if meta := extractVideosMetadata(m.Videos); meta != "" {
						Debug("[vision] extracted metadata for %d video(s):\n%s", len(m.Videos), meta)
						contextBlocks = append(contextBlocks, meta)
					}
					if frames := extractVideosFrames(m.Videos, videoFrameSampleCount); len(frames) > 0 {
						Debug("[vision] sampled %d frame(s) across %d video(s)", len(frames), len(m.Videos))
						images = append(images, frames...)
					} else {
						Debug("[vision] no frames extracted from %d video(s) — ffmpeg missing or unreadable", len(m.Videos))
					}
				}

				if joined := strings.Join(contextBlocks, "\n\n"); joined != "" {
					if text != "" {
						text = joined + "\n\n" + text
					} else {
						text = joined
					}
				}
				msgs = append(msgs, oaiMessage{Role: m.Role, Content: oaiVisionContent(text, images)})
			} else {
				msgs = append(msgs, oaiMessage{Role: m.Role, Content: oaiTextContent(m.Content)})
			}
		}
	}
	return msgs
}

func buildOAITools(tools []Tool) []oaiTool {
	var out []oaiTool
	for _, t := range tools {
		schema := map[string]interface{}{
			"type": "object",
		}
		if len(t.Parameters) > 0 {
			props := make(map[string]interface{})
			for name, p := range t.Parameters {
				props[name] = buildParamSchema(p)
			}
			schema["properties"] = props
		}
		if len(t.Required) > 0 {
			schema["required"] = t.Required
		}
		raw, _ := json.Marshal(schema)
		out = append(out, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  json.RawMessage(raw),
			},
		})
	}
	return out
}

func parseOAIToolCalls(calls []oaiToolCallMsg) []ToolCall {
	var out []ToolCall
	for _, tc := range calls {
		args := make(map[string]any)
		var raw map[string]interface{}
		if json.Unmarshal([]byte(tc.Function.Arguments), &raw) == nil {
			args = parseToolArgs(raw)
		}
		out = append(out, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
	}
	return out
}

// snoopOAIRequest logs the outbound HTTP request details via Trace.
func (c *openAIClient) snoopRequest(body []byte, stream bool) {
	provider := "openai"
	if c.isOllama() {
		provider = "ollama"
	}
	Trace("[%s]: %s", provider, c.endpoint)
	if stream {
		Trace("--> METHOD: \"POST\" PATH: \"/chat/completions\" (streaming)")
	} else {
		Trace("--> METHOD: \"POST\" PATH: \"/chat/completions\"")
	}
	Trace("--> HEADER: Content-Type: application/json")
	if c.apiKey != "" {
		Trace("--> HEADER: Authorization: Bearer [HIDDEN]")
	}

	var pretty map[string]interface{}
	if json.Unmarshal(body, &pretty) == nil {
		formatted, _ := json.MarshalIndent(pretty, "", "  ")
		Trace("--> REQUEST BODY:\n%s", string(formatted))
	}
}

// snoopOAIResponse logs the HTTP response details via Trace.
func snoopOAIResponse(statusCode int, body []byte) {
	Trace("<-- RESPONSE STATUS: %d", statusCode)
	var generic map[string]interface{}
	if json.Unmarshal(body, &generic) == nil {
		formatted, _ := json.MarshalIndent(generic, "", "  ")
		Trace("<-- RESPONSE BODY:\n%s", string(formatted))
	} else {
		Trace("<-- RESPONSE BODY:\n%s", string(body))
	}
}

func (c *openAIClient) doRequest(ctx context.Context, body []byte) (*http.Response, error) {
	Debug("[%s]: Sending request to %s/chat/completions", c.provider(), c.endpoint)

	// Extract the path portion from the endpoint for APIClient.
	path := "/v1/chat/completions"
	if u, err := url.Parse(c.endpoint); err == nil && u.Path != "" {
		path = u.Path + "/chat/completions"
	}

	req, err := c.api.NewRequestWithContext(ctx, "POST", path)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	resp, err := c.api.SendRawRequest("", req)
	if err != nil {
		Debug("[%s]: Request failed: %v", c.provider(), err)
	} else {
		Debug("[%s]: Response status: %d", c.provider(), resp.StatusCode)
		resp.Body = iotimeout.NewReadCloser(resp.Body, c.api.RequestTimeout)
	}
	return resp, err
}

// nativeOllamaChatRequest is the request format for ollama's native /api/chat.
// Note: this uses nativeOllamaMessage (not oaiMessage) so that tool_calls
// arguments are sent as structured objects instead of JSON strings.
type nativeOllamaChatRequest struct {
	Model            string                `json:"model"`
	Messages         []nativeOllamaMessage `json:"messages"`
	Stream           bool                  `json:"stream"`
	Think            *bool                 `json:"think,omitempty"`
	PreserveThinking *bool                 `json:"preserve_thinking,omitempty"`
	Options          map[string]any        `json:"options,omitempty"`
	Format           any                   `json:"format,omitempty"`
	Tools            []oaiTool             `json:"tools,omitempty"`
}

// nativeOllamaMessage is the message format ollama's /api/chat expects.
// Differs from oaiMessage in that ToolCalls.Function.Arguments is an
// object/map, not a JSON string.
type nativeOllamaMessage struct {
	Role      string                 `json:"role"`
	Content   string                 `json:"content"`
	Thinking  string                 `json:"thinking,omitempty"`  // preserve_thinking: prior turn's reasoning content
	Images    []string               `json:"images,omitempty"`    // base64-encoded images for vision
	ToolCalls []nativeOllamaToolCall `json:"tool_calls,omitempty"`
}

// nativeOllamaToolCall mirrors ollama's response format for tool calls.
// Ollama returns arguments as a structured object (map), not a JSON string,
// which differs from the OpenAI-compatible /v1 endpoint.
type nativeOllamaToolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

// nativeOllamaChatResponse is the response format from ollama's native /api/chat.
type nativeOllamaChatResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role      string                 `json:"role"`
		Content   string                 `json:"content"`
		Thinking  string                 `json:"thinking,omitempty"`
		ToolCalls []nativeOllamaToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done            bool `json:"done"`
	PromptEvalCount int  `json:"prompt_eval_count"`
	EvalCount       int  `json:"eval_count"`
}

// convertToNativeOllamaMessages translates OpenAI-shaped messages into
// ollama's native /api/chat shape. The key difference is that tool_calls
// arguments are sent as parsed objects, not as JSON-encoded strings.
func convertToNativeOllamaMessages(msgs []oaiMessage) []nativeOllamaMessage {
	out := make([]nativeOllamaMessage, len(msgs))
	for i, m := range msgs {
		// Extract text content from the raw JSON. It's either a plain
		// string or an array of content parts (vision format).
		var contentText string
		var images []string
		var plainStr string
		if json.Unmarshal(m.Content, &plainStr) == nil {
			contentText = plainStr
		} else {
			// Try vision array format.
			var parts []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url"`
			}
			if json.Unmarshal(m.Content, &parts) == nil {
				for _, p := range parts {
					if p.Type == "text" {
						contentText = p.Text
					} else if p.Type == "image_url" && p.ImageURL != nil {
						// Strip data URI prefix — Ollama expects raw base64.
						url := p.ImageURL.URL
						if idx := strings.Index(url, ","); idx >= 0 {
							url = url[idx+1:]
						}
						images = append(images, url)
					}
				}
			}
		}
		out[i] = nativeOllamaMessage{
			Role:    m.Role,
			Content: contentText,
			Images:  images,
		}
		for _, tc := range m.ToolCalls {
			args := make(map[string]any)
			if tc.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					Debug("[ollama] failed to parse tool call args for round-trip: %v", err)
				}
			}
			var native nativeOllamaToolCall
			native.Function.Name = tc.Function.Name
			native.Function.Arguments = args
			out[i].ToolCalls = append(out[i].ToolCalls, native)
		}
	}
	return out
}

// modelPreservesThinking reports whether the given Ollama model should run
// with preserve_thinking=true. Auto-enabled for Qwen 3.6+, which supports
// replaying prior thinking blocks in assistant history.
func modelPreservesThinking(model string) bool {
	return strings.Contains(strings.ToLower(model), "qwen3.6")
}

// enrichThinking copies reasoning from the original Message slice into the
// corresponding assistant entries in the native ollama message slice.
// Called when preserve_thinking is enabled so Ollama can replay prior
// thinking blocks in subsequent turns.
func enrichThinking(msgs []nativeOllamaMessage, origMessages []Message) {
	var assistants []string
	for _, m := range origMessages {
		if m.Role == "assistant" {
			assistants = append(assistants, m.Reasoning)
		}
	}
	idx := 0
	for i, m := range msgs {
		if m.Role == "assistant" && idx < len(assistants) {
			msgs[i].Thinking = assistants[idx]
			idx++
		}
	}
}

// truncateForDebug shortens a string for safe debug logging.
func truncateForDebug(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... [truncated, %d total bytes]", len(s))
}

// chatViaOllamaNative sends a chat request to ollama's native /api/chat
// endpoint instead of the OpenAI-compatible /v1/chat/completions. This is
// needed for gemma4 because the v1 endpoint silently drops the `think` and
// `options` fields, leaving thinking enabled by default.
func (c *openAIClient) chatViaOllamaNative(ctx context.Context, cfg ChatConfig, messages []Message) (*Response, error) {
	// Acquire a slot from the global Ollama fair-queueing scheduler.
	// The scheduler enforces a global parallelism cap plus per-caller
	// round-robin so one app can't monopolize the local model.
	// Unconfigured → passes through immediately.
	caller := cfg.Caller
	if err := AcquireOllamaSlot(ctx, caller); err != nil {
		return nil, err
	}
	defer ReleaseOllamaSlot(caller)

	// Master override: if DisableThinking is set in the provider config,
	// force think=false regardless of what the caller passed. This is the
	// escape hatch for thinking hangs on local models.
	if c.disableThinking {
		f := false
		cfg.Think = &f
	}
	applyVisionDefaults(&cfg, messages)
	// Strip native tool specs when the model doesn't support function calling.
	// Tools are handled via text prompts at the agent loop level instead.
	if !c.nativeTools {
		cfg.Tools = nil
	}

	// Build messages using the same logic as the OpenAI path, then convert
	// to ollama's native shape (tool_call arguments as object, not string).
	oaiMsgs := c.buildMessages(cfg, messages)
	msgs := convertToNativeOllamaMessages(oaiMsgs)

	options := map[string]any{"num_ctx": c.ollamaNumCtx()}
	if cfg.Temperature != nil {
		options["temperature"] = *cfg.Temperature
	}
	if cfg.MaxTokens > 0 {
		options["num_predict"] = cfg.MaxTokens
	}
	payload := nativeOllamaChatRequest{
		Model:    cfg.Model,
		Messages: msgs,
		Stream:   false,
		Think:    cfg.Think,
		Options:  options,
		Tools:    buildOAITools(cfg.Tools),
	}
	preserve := modelPreservesThinking(cfg.Model)
	if preserve {
		t := true
		payload.PreserveThinking = &t
		enrichThinking(msgs, messages)
	}
	if cfg.JSONMode {
		payload.Format = "json"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	c.snoopRequest(body, false)

	Debug("[ollama]: Sending request to %s/api/chat (tools=%d, body=%d bytes, think=%s, preserve_thinking=%v, json=%v)", c.api.Server, len(payload.Tools), len(body), fmtThink(cfg.Think), preserve, cfg.JSONMode)
	req, err := c.api.NewRequestWithContext(ctx, "POST", "/api/chat")
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	// Force the transport to hard-close the connection after this request
	// rather than returning it to the idle pool. When ctx is cancelled
	// mid-flight, this ensures ollama sees a TCP disconnect on its next
	// write and aborts generation instead of silently finishing the
	// zombie call while blocking the NUM_PARALLEL slot.
	req.Close = true

	resp, err := c.api.SendRawRequest("", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	resp.Body = iotimeout.NewReadCloser(resp.Body, c.api.RequestTimeout)

	// Abort promptly if the caller cancelled while we were waiting for
	// the response to start.
	if ctx.Err() != nil {
		resp.Body.Close()
		return nil, ctx.Err()
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	snoopOAIResponse(resp.StatusCode, respBody)

	if resp.StatusCode != http.StatusOK {
		Debug("[ollama] error response: status=%d body=%s", resp.StatusCode, string(respBody))
		if !cfg.MaskDebug {
			Debug("[ollama] outgoing payload (truncated to 4KB):\n%s", truncateForDebug(string(body), 4096))
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: string(respBody), Provider: "ollama"}
	}

	var result nativeOllamaChatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to decode native ollama response: %w", err)
	}

	Debug("[ollama]: Response: model=%s input_tokens=%d output_tokens=%d tool_calls=%d", result.Model, result.PromptEvalCount, result.EvalCount, len(result.Message.ToolCalls))

	// Convert ollama's structured tool calls into the framework's ToolCall type.
	var toolCalls []ToolCall
	for i, tc := range result.Message.ToolCalls {
		toolCalls = append(toolCalls, ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}

	return &Response{
		Content:      result.Message.Content,
		Reasoning:    result.Message.Thinking,
		Model:        result.Model,
		InputTokens:  result.PromptEvalCount,
		OutputTokens: result.EvalCount,
		ToolCalls:    toolCalls,
	}, nil
}

// chatStreamViaOllamaNative streams a chat completion through ollama's native
// /api/chat endpoint. The native endpoint emits newline-delimited JSON objects
// rather than SSE, with each chunk containing a `message.content` delta.
func (c *openAIClient) chatStreamViaOllamaNative(ctx context.Context, cfg ChatConfig, messages []Message, handler StreamHandler) (*Response, error) {
	// Acquire a slot from the global Ollama fair-queueing scheduler.
	// See chatViaOllamaNative for details.
	caller := cfg.Caller
	if err := AcquireOllamaSlot(ctx, caller); err != nil {
		return nil, err
	}
	defer ReleaseOllamaSlot(caller)

	// Master overrides: see chatViaOllamaNative for rationale.
	if c.disableThinking {
		f := false
		cfg.Think = &f
	}
	applyVisionDefaults(&cfg, messages)
	if !c.nativeTools {
		cfg.Tools = nil
	}

	oaiMsgs := c.buildMessages(cfg, messages)
	msgs := convertToNativeOllamaMessages(oaiMsgs)

	options := map[string]any{"num_ctx": c.ollamaNumCtx()}
	if cfg.Temperature != nil {
		options["temperature"] = *cfg.Temperature
	}
	if cfg.MaxTokens > 0 {
		options["num_predict"] = cfg.MaxTokens
	}
	payload := nativeOllamaChatRequest{
		Model:    cfg.Model,
		Messages: msgs,
		Stream:   true,
		Think:    cfg.Think,
		Options:  options,
		Tools:    buildOAITools(cfg.Tools),
	}
	preserve := modelPreservesThinking(cfg.Model)
	if preserve {
		t := true
		payload.PreserveThinking = &t
		enrichThinking(msgs, messages)
	}
	if cfg.JSONMode {
		payload.Format = "json"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	c.snoopRequest(body, true)

	Debug("[ollama]: Sending stream request to %s/api/chat (tools=%d, body=%d bytes, think=%s, preserve_thinking=%v, json=%v)", c.api.Server, len(payload.Tools), len(body), fmtThink(cfg.Think), preserve, cfg.JSONMode)
	req, err := c.api.NewRequestWithContext(ctx, "POST", "/api/chat")
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	// Force the transport to hard-close the connection on completion or
	// cancellation rather than pooling it.
	req.Close = true

	resp, err := c.api.SendRawRequest("", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	resp.Body = iotimeout.NewReadCloser(resp.Body, c.api.RequestTimeout)

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, &APIError{StatusCode: resp.StatusCode, Message: string(errBody), Provider: "ollama"}
	}

	var content, reasoning strings.Builder
	var model string
	var inputTokens, outputTokens int
	var streamedToolCalls []nativeOllamaToolCall

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		// Check for cancellation on every chunk. When ctx is cancelled,
		// break out of the scan loop, explicitly close the body to force
		// an immediate TCP teardown, and return the ctx error. Without
		// this check the scanner would keep processing any chunks that
		// were already buffered from the server before the cancel fired.
		if ctx.Err() != nil {
			resp.Body.Close()
			return nil, ctx.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var chunk struct {
			Model   string `json:"model"`
			Message struct {
				Role      string                 `json:"role"`
				Content   string                 `json:"content"`
				Thinking  string                 `json:"thinking,omitempty"`
				ToolCalls []nativeOllamaToolCall `json:"tool_calls,omitempty"`
			} `json:"message"`
			Done            bool `json:"done"`
			PromptEvalCount int  `json:"prompt_eval_count"`
			EvalCount       int  `json:"eval_count"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Message.Content != "" {
			content.WriteString(chunk.Message.Content)
			if handler != nil {
				handler(chunk.Message.Content)
			}
		}
		if chunk.Message.Thinking != "" {
			reasoning.WriteString(chunk.Message.Thinking)
		}
		if len(chunk.Message.ToolCalls) > 0 {
			streamedToolCalls = append(streamedToolCalls, chunk.Message.ToolCalls...)
		}
		if chunk.Done {
			inputTokens = chunk.PromptEvalCount
			outputTokens = chunk.EvalCount
		}
	}
	// If ctx was cancelled while the scanner was mid-read, surface that
	// as the primary error rather than whatever scanner.Err() returns
	// from the aborted connection.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("native stream read error: %w", err)
	}

	// Convert ollama's structured tool calls into the framework's ToolCall type.
	var toolCalls []ToolCall
	for i, tc := range streamedToolCalls {
		toolCalls = append(toolCalls, ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}

	Debug("[ollama]: Stream complete: model=%s input_tokens=%d output_tokens=%d tool_calls=%d", model, inputTokens, outputTokens, len(toolCalls))

	return &Response{
		Content:      content.String(),
		Reasoning:    reasoning.String(),
		Model:        model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		ToolCalls:    toolCalls,
	}, nil
}

// Chat sends a non-streaming request.
func (c *openAIClient) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.model, 0, opts)

	// Ollama runs locally with no per-token cost — always remove the
	// token limit so the model generates until its natural stop token.
	if c.isOllama() {
		cfg.MaxTokens = 0
	}

	// Route ALL ollama traffic through the native /api/chat endpoint.
	// The /v1/chat/completions endpoint silently drops `think` and `options`
	// fields, which breaks thinking control and context window sizing.
	if c.isOllama() {
		return c.chatViaOllamaNative(ctx, cfg, messages)
	}

	// llama.cpp is single-threaded. Serialize requests so concurrent
	// callers (e.g. background consolidation + a new session) queue up
	// here instead of racing to the server and getting 503s.
	if c.llamacpp {
		if err := AcquireLlamacppSlot(ctx, cfg.Caller); err != nil {
			return nil, err
		}
		defer ReleaseLlamacppSlot(cfg.Caller)
	}

	// At this point we know it's NOT ollama (ollama is routed through native).
	if c.disableThinking {
		f := false
		cfg.Think = &f
	}
	applyVisionDefaults(&cfg, messages)
	payload := oaiRequest{
		Model:       cfg.Model,
		Messages:    c.buildMessages(cfg, messages),
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
		Tools:       buildOAITools(cfg.Tools),
	}
	if c.llamacpp {
		payload.ChatTemplateKwargs = c.llamacppChatTemplateKwargs(cfg)
		payload.ThinkingBudgetTokens = c.llamacppThinkBudget(cfg)
		// llama.cpp counts thinking tokens against max_tokens, so inflate
		// the limit by the budget to ensure response tokens remain after thinking.
		if payload.ThinkingBudgetTokens != nil && *payload.ThinkingBudgetTokens > 0 && payload.MaxTokens > 0 {
			payload.MaxTokens += *payload.ThinkingBudgetTokens
		}
	} else {
		payload.Think = cfg.Think
	}
	if cfg.JSONMode {
		payload.ResponseFormat = &oaiResponseFormat{Type: "json_object"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	c.snoopRequest(body, false)

	Debug("[%s]: Sending request (body=%d bytes, think=%s, json=%v)", c.provider(), len(body), fmtThink(cfg.Think), cfg.JSONMode)
	resp, err := c.doRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	snoopOAIResponse(resp.StatusCode, respBody)

	if resp.StatusCode != http.StatusOK {
		msg := string(respBody)
		var apiErr oaiError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			msg = apiErr.Error.Message
		}
		ep := "openai"
		if c.llamacpp {
			ep = "llama.cpp"
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: ep}
	}

	var result oaiResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	content := ""
	reasoning := ""
	finishReason := ""
	var toolCalls []ToolCall
	if len(result.Choices) > 0 {
		content = result.Choices[0].Message.Content
		// Prefer reasoning_content (current llama.cpp + Qwen 3.6); fall back
		// to reasoning for older builds and other backends. Don't silently
		// drop one if the other is empty.
		reasoning = result.Choices[0].Message.ReasoningContent
		if reasoning == "" {
			reasoning = result.Choices[0].Message.Reasoning
		}
		finishReason = result.Choices[0].FinishReason
		toolCalls = parseOAIToolCalls(result.Choices[0].Message.ToolCalls)

		Trace("[%s]: <-- RAW content (%d chars): %s", c.provider(), len(content), content)
		Trace("[%s]: <-- RAW reasoning (%d chars): %s", c.provider(), len(reasoning), reasoning)

		// Ollama's OpenAI-compatible endpoint embeds <think> blocks
		// directly in content rather than separating them into reasoning.
		if reasoning == "" && strings.Contains(content, "<think>") {
			if thinkBlockRE.MatchString(content) {
				if m := thinkBlockRE.FindString(content); m != "" {
					reasoning = strings.TrimSpace(m[len("<think>") : len(m)-len("</think>")-1])
				}
				content = strings.TrimSpace(thinkBlockRE.ReplaceAllString(content, ""))
			} else if thinkUnclosedRE.MatchString(content) {
				// Token limit hit before </think> — entire content is reasoning.
				reasoning = strings.TrimSpace(content[len("<think>"):])
				content = ""
			}
		}

		// Always trace reasoning when present.
		if reasoning != "" {
			Trace("[%s]: <-- REASONING:\n%s", c.provider(), reasoning)
		}
	}

	reasoningTokens := result.Usage.CompletionTokensDetails.ReasoningTokens
	if reasoningTokens == 0 && reasoning != "" && (content != "" || len(toolCalls) > 0) {
		// Backend didn't report a reasoning-token breakdown but we
		// have visible reasoning + content; estimate by char ratio so
		// debug output still surfaces "where did the budget go."
		reasoningTokens = estimateReasoningTokens(result.Usage.CompletionTokens, reasoning, content, toolCalls)
	}
	Debug("[%s]: Response: model=%s input_tokens=%d output_tokens=%d (reasoning=%d) tool_calls=%d finish=%s",
		c.provider(), result.Model, result.Usage.PromptTokens, result.Usage.CompletionTokens, reasoningTokens, len(toolCalls), finishReason)

	// finish_reason == "length" with empty content means thinking burned the
	// entire output budget. Surface this so the agent loop can retry instead
	// of accepting an empty response as terminal.
	if finishReason == "length" && content == "" && len(toolCalls) == 0 {
		return nil, &APIError{Message: fmt.Sprintf("empty LLM response: finish_reason=length after %d output tokens (raise max_tokens or disable thinking for this call)", result.Usage.CompletionTokens), Provider: c.provider()}
	}

	out := &Response{
		Content:         content,
		Reasoning:       reasoning,
		ToolCalls:       toolCalls,
		Model:           result.Model,
		InputTokens:     result.Usage.PromptTokens,
		OutputTokens:    result.Usage.CompletionTokens,
		ReasoningTokens: reasoningTokens,
	}
	if result.Timings != nil {
		out.PredictedPerSecond = result.Timings.PredictedPerSecond
		out.PromptPerSecond = result.Timings.PromptPerSecond
	}
	return out, nil
}

// estimateReasoningTokens approximates the reasoning-channel share of
// total output tokens via char ratio when the backend doesn't report
// the breakdown directly. Tokenization isn't perfectly proportional
// to char count, but for surfacing "thinking ate 90% of the budget"
// patterns this is plenty accurate.
func estimateReasoningTokens(total int, reasoning, content string, toolCalls []ToolCall) int {
	if total <= 0 || reasoning == "" {
		return 0
	}
	rChars := len(reasoning)
	cChars := len(content)
	tChars := 0
	for _, tc := range toolCalls {
		tChars += len(tc.Name) + 4
		for k, v := range tc.Args {
			tChars += len(k) + len(fmt.Sprint(v)) + 6
		}
	}
	denom := rChars + cChars + tChars
	if denom == 0 {
		return 0
	}
	return total * rChars / denom
}

// ChatStream sends a streaming request.
func (c *openAIClient) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.model, 0, opts)

	// See Chat() — Ollama runs locally, no token limit needed.
	if c.isOllama() {
		cfg.MaxTokens = 0
	}

	// Route ALL ollama traffic through the native /api/chat endpoint.
	if c.isOllama() {
		return c.chatStreamViaOllamaNative(ctx, cfg, messages, handler)
	}

	// Serialize llama.cpp requests — see Chat() for rationale.
	if c.llamacpp {
		if err := AcquireLlamacppSlot(ctx, cfg.Caller); err != nil {
			return nil, err
		}
		defer ReleaseLlamacppSlot(cfg.Caller)
	}

	if c.disableThinking {
		f := false
		cfg.Think = &f
	}
	applyVisionDefaults(&cfg, messages)
	payload := oaiRequest{
		Model:         cfg.Model,
		Messages:      c.buildMessages(cfg, messages),
		MaxTokens:     cfg.MaxTokens,
		Temperature:   cfg.Temperature,
		Stream:        true,
		StreamOptions: &oaiStreamOptions{IncludeUsage: true},
		Tools:         buildOAITools(cfg.Tools),
	}
	if c.llamacpp {
		payload.ChatTemplateKwargs = c.llamacppChatTemplateKwargs(cfg)
		payload.ThinkingBudgetTokens = c.llamacppThinkBudget(cfg)
		if payload.ThinkingBudgetTokens != nil && *payload.ThinkingBudgetTokens > 0 && payload.MaxTokens > 0 {
			payload.MaxTokens += *payload.ThinkingBudgetTokens
		}
	} else {
		payload.Think = cfg.Think
	}
	if cfg.JSONMode {
		payload.ResponseFormat = &oaiResponseFormat{Type: "json_object"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	c.snoopRequest(body, true)

	Debug("[%s]: Sending stream request (body=%d bytes, think=%s, json=%v)", c.provider(), len(body), fmtThink(cfg.Think), cfg.JSONMode)
	resp, err := c.doRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		snoopOAIResponse(resp.StatusCode, respBody)
		msg := string(respBody)
		var apiErr oaiError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			msg = apiErr.Error.Message
		}
		ep := "openai"
		if c.llamacpp {
			ep = "llama.cpp"
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: ep}
	}

	var full strings.Builder
	var reasoning strings.Builder
	var model string
	var inputTokens, outputTokens, reasoningTokens int
	var predictedPerSecond, promptPerSecond float64

	// Accumulate tool call fragments by index.
	toolCallBuilders := make(map[int]*struct {
		id   string
		name strings.Builder
		args strings.Builder
	})

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for thinking models
	totalLines := 0
	skipCount := 0
	contentCount := 0
	finishReason := ""
	for scanner.Scan() {
		totalLines++
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk oaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			skipCount++
			continue
		}

		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			contentCount++
		}

		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			inputTokens = chunk.Usage.PromptTokens
			outputTokens = chunk.Usage.CompletionTokens
			reasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
		}
		if chunk.Timings != nil {
			predictedPerSecond = chunk.Timings.PredictedPerSecond
			promptPerSecond = chunk.Timings.PromptPerSecond
		}

		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if chunk.Choices[0].FinishReason != "" {
				finishReason = chunk.Choices[0].FinishReason
			}

			// Thinking models (qwen3, deepseek-r1, etc.) emit reasoning in
			// a separate field. llama.cpp + Qwen 3.6 use reasoning_content;
			// some other backends use reasoning. Accept both. When the
			// caller installed a reasoning handler (via WithReasoningStream),
			// fan out reasoning chunks to it for live-thinking UI.
			if delta.ReasoningContent != "" {
				reasoning.WriteString(delta.ReasoningContent)
				if cfg.ReasoningHandler != nil {
					cfg.ReasoningHandler(delta.ReasoningContent)
				}
			}
			if delta.Reasoning != "" {
				reasoning.WriteString(delta.Reasoning)
				if cfg.ReasoningHandler != nil {
					cfg.ReasoningHandler(delta.Reasoning)
				}
			}

			if delta.Content != "" {
				full.WriteString(delta.Content)
				if handler != nil {
					handler(delta.Content)
				}
			}
			for _, tc := range delta.ToolCalls {
				idx := tc.Index
				if _, ok := toolCallBuilders[idx]; !ok {
					toolCallBuilders[idx] = &struct {
						id   string
						name strings.Builder
						args strings.Builder
					}{}
				}
				b := toolCallBuilders[idx]
				if tc.ID != "" {
					b.id = tc.ID
				}
				if tc.Function.Name != "" {
					b.name.WriteString(tc.Function.Name)
				}
				if tc.Function.Arguments != "" {
					b.args.WriteString(tc.Function.Arguments)
				}
			}
		}
	}

	// Ollama's OpenAI-compatible endpoint embeds <think> blocks
	// directly in streamed content rather than using the reasoning field.
	if reasoning.Len() == 0 && strings.Contains(full.String(), "<think>") {
		raw := full.String()
		if thinkBlockRE.MatchString(raw) {
			if m := thinkBlockRE.FindString(raw); m != "" {
				reasoning.Reset()
				reasoning.WriteString(strings.TrimSpace(m[len("<think>") : len(m)-len("</think>")-1]))
			}
			full.Reset()
			full.WriteString(strings.TrimSpace(thinkBlockRE.ReplaceAllString(raw, "")))
		} else if thinkUnclosedRE.MatchString(raw) {
			reasoning.Reset()
			reasoning.WriteString(strings.TrimSpace(raw[len("<think>"):]))
			full.Reset()
		}
	}

	// Always trace reasoning when present.
	if reasoning.Len() > 0 {
		Trace("[%s]: <-- REASONING:\n%s", c.provider(), reasoning.String())
	}

	if reasoning.Len() > 0 {
		Debug("[%s]: reasoning present (%d chars), content=%d chars", c.provider(), reasoning.Len(), full.Len())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read error: %w", err)
	}

	var toolCalls []ToolCall
	for _, b := range toolCallBuilders {
		args := make(map[string]any)
		var raw map[string]interface{}
		if json.Unmarshal([]byte(b.args.String()), &raw) == nil {
			args = parseToolArgs(raw)
		}
		toolCalls = append(toolCalls, ToolCall{
			ID:   b.id,
			Name: b.name.String(),
			Args: args,
		})
	}

	if reasoningTokens == 0 && reasoning.Len() > 0 && (full.Len() > 0 || len(toolCalls) > 0) {
		reasoningTokens = estimateReasoningTokens(outputTokens, reasoning.String(), full.String(), toolCalls)
	}
	Debug("[%s]: Stream complete: model=%s input_tokens=%d output_tokens=%d (reasoning=%d) tool_calls=%d finish=%s (lines=%d content=%d skipped=%d, resp=%d chars)", c.provider(), model, inputTokens, outputTokens, reasoningTokens, len(toolCalls), finishReason, totalLines, contentCount, skipCount, full.Len())
	Trace("<-- STREAM COMPLETE: model=%s input_tokens=%d output_tokens=%d", model, inputTokens, outputTokens)
	if full.Len() > 0 {
		Trace("<-- RESPONSE TEXT:\n%s", full.String())
	}
	for _, tc := range toolCalls {
		argsJSON, _ := json.Marshal(tc.Args)
		Trace("<-- TOOL CALL: id=%s name=%s args=%s", tc.ID, tc.Name, string(argsJSON))
	}

	// Detect empty response with consumed output tokens — the model hit its
	// output ceiling mid-generation and the stream completed with no content.
	// Return an error so the agent loop can retry (thinking disabled or lower
	// max_tokens gives the model room for actual output).
	if outputTokens > 0 && full.Len() == 0 && reasoning.Len() == 0 && len(toolCalls) == 0 {
		return nil, &APIError{Message: fmt.Sprintf("empty LLM response after consuming %d output tokens (model hit output ceiling mid-generation)", outputTokens), Provider: c.provider()}
	}
	// finish_reason == "length" with empty content is the same problem — thinking
	// burned the entire budget and no answer was emitted. Surface explicitly so
	// the agent loop's retry kicks in.
	if finishReason == "length" && full.Len() == 0 && len(toolCalls) == 0 {
		return nil, &APIError{Message: fmt.Sprintf("empty LLM response: finish_reason=length after %d output tokens (raise max_tokens or disable thinking for this call)", outputTokens), Provider: c.provider()}
	}

	return &Response{
		Content:            full.String(),
		Reasoning:          reasoning.String(),
		ToolCalls:          toolCalls,
		Model:              model,
		InputTokens:        inputTokens,
		OutputTokens:       outputTokens,
		ReasoningTokens:    reasoningTokens,
		PredictedPerSecond: predictedPerSecond,
		PromptPerSecond:    promptPerSecond,
	}, nil
}
