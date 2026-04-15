package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/cmcoffee/snugforge/apiclient"
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
	contextSize     int  // Ollama num_ctx; 0 uses ollamaDefaultCtx
	disableThinking bool // Ollama-only master override forcing think=false on every call (escape hatch for thinking hangs).
	nativeTools     bool // When true, send native tool specs. When false, strip them (tools handled via text prompts at agent loop level).
}

// isOllama reports whether this client is talking to an Ollama instance.
func (c *openAIClient) isOllama() bool {
	return c.ollama
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
		Server:    "api.openai.com",
		VerifySSL: true,
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
		Server:    u.Host,
		URLScheme: u.Scheme,
		AuthFunc:  func(req *http.Request) {},
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
		api = &apiclient.APIClient{VerifySSL: true}
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
	Model          string             `json:"model"`
	Messages       []oaiMessage       `json:"messages"`
	MaxTokens      int                `json:"max_tokens,omitempty"`
	Temperature    *float64           `json:"temperature,omitempty"`
	Stream         bool               `json:"stream,omitempty"`
	Tools          []oaiTool          `json:"tools,omitempty"`
	ResponseFormat *oaiResponseFormat `json:"response_format,omitempty"`
	Think          *bool              `json:"think,omitempty"`
	Options        map[string]any     `json:"options,omitempty"` // Ollama-specific options (num_ctx, etc.)
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
		b64 := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)
		parts = append(parts, part{Type: "image_url", ImageURL: &imgURL{URL: b64}})
	}
	b, _ := json.Marshal(parts)
	return b
}

type oaiResponse struct {
	Choices []struct {
		Message struct {
			Content          string           `json:"content"`
			Reasoning string           `json:"reasoning,omitempty"`
			ToolCalls        []oaiToolCallMsg `json:"tool_calls,omitempty"`
		} `json:"message"`
	} `json:"choices"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type oaiStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string           `json:"content"`
			Reasoning string           `json:"reasoning,omitempty"`
			ToolCalls        []oaiToolCallMsg `json:"tool_calls,omitempty"`
		} `json:"delta"`
	} `json:"choices"`
	Model string `json:"model"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
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
	for _, m := range messages {
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
			if len(m.Images) > 0 {
				msgs = append(msgs, oaiMessage{Role: m.Role, Content: oaiVisionContent(m.Content, m.Images)})
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
	Debug("[openai]: Sending request to %s/chat/completions", c.endpoint)

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
		Debug("[openai]: Request failed: %v", err)
	} else {
		Debug("[openai]: Response status: %d", resp.StatusCode)
	}
	return resp, err
}

// nativeOllamaChatRequest is the request format for ollama's native /api/chat.
// Note: this uses nativeOllamaMessage (not oaiMessage) so that tool_calls
// arguments are sent as structured objects instead of JSON strings.
type nativeOllamaChatRequest struct {
	Model    string                `json:"model"`
	Messages []nativeOllamaMessage `json:"messages"`
	Stream   bool                  `json:"stream"`
	Think    *bool                 `json:"think,omitempty"`
	Options  map[string]any        `json:"options,omitempty"`
	Format   any                   `json:"format,omitempty"`
	Tools    []oaiTool             `json:"tools,omitempty"`
}

// nativeOllamaMessage is the message format ollama's /api/chat expects.
// Differs from oaiMessage in that ToolCalls.Function.Arguments is an
// object/map, not a JSON string.
type nativeOllamaMessage struct {
	Role      string                 `json:"role"`
	Content   string                 `json:"content"`
	Images    []string               `json:"images,omitempty"` // base64-encoded images for vision
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
	if cfg.JSONMode {
		payload.Format = "json"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	c.snoopRequest(body, false)

	Debug("[ollama]: Sending request to %s/api/chat (tools=%d, body=%d bytes, think=%s, json=%v)", c.api.Server, len(payload.Tools), len(body), fmtThink(cfg.Think), cfg.JSONMode)
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
		// Log the outgoing payload on error so we can diagnose validation issues.
		Debug("[ollama] error response: status=%d body=%s", resp.StatusCode, string(respBody))
		Debug("[ollama] outgoing payload (truncated to 4KB):\n%s", truncateForDebug(string(body), 4096))
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
	if cfg.JSONMode {
		payload.Format = "json"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	c.snoopRequest(body, true)

	Debug("[ollama]: Sending stream request to %s/api/chat (tools=%d, body=%d bytes, think=%s, json=%v)", c.api.Server, len(payload.Tools), len(body), fmtThink(cfg.Think), cfg.JSONMode)
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

	// At this point we know it's NOT ollama (ollama is routed through native).
	payload := oaiRequest{
		Model:       cfg.Model,
		Messages:    c.buildMessages(cfg, messages),
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
		Tools:       buildOAITools(cfg.Tools),
		Think:       cfg.Think,
	}
	if cfg.JSONMode {
		payload.ResponseFormat = &oaiResponseFormat{Type: "json_object"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	c.snoopRequest(body, false)

	Debug("[openai]: Sending request (body=%d bytes, think=%s, json=%v)", len(body), fmtThink(cfg.Think), cfg.JSONMode)
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
		provider := "openai"
		if c.isOllama() {
			provider = "ollama"
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: provider}
	}

	var result oaiResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	content := ""
	reasoning := ""
	var toolCalls []ToolCall
	if len(result.Choices) > 0 {
		content = result.Choices[0].Message.Content
		reasoning = result.Choices[0].Message.Reasoning
		toolCalls = parseOAIToolCalls(result.Choices[0].Message.ToolCalls)

		Trace("[openai]: <-- RAW content (%d chars): %s", len(content), content)
		Trace("[openai]: <-- RAW reasoning (%d chars): %s", len(reasoning), reasoning)

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
			Trace("[openai]: <-- REASONING:\n%s", reasoning)
		}
	}

	Debug("[openai]: Response: model=%s input_tokens=%d output_tokens=%d tool_calls=%d", result.Model, result.Usage.PromptTokens, result.Usage.CompletionTokens, len(toolCalls))

	return &Response{
		Content:      content,
		Reasoning:    reasoning,
		ToolCalls:    toolCalls,
		Model:        result.Model,
		InputTokens:  result.Usage.PromptTokens,
		OutputTokens: result.Usage.CompletionTokens,
	}, nil
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

	payload := oaiRequest{
		Model:       cfg.Model,
		Messages:    c.buildMessages(cfg, messages),
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
		Stream:      true,
		Tools:       buildOAITools(cfg.Tools),
		Think:       cfg.Think,
	}
	if cfg.JSONMode {
		payload.ResponseFormat = &oaiResponseFormat{Type: "json_object"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	c.snoopRequest(body, true)

	Debug("[openai]: Sending stream request (body=%d bytes, think=%s, json=%v)", len(body), fmtThink(cfg.Think), cfg.JSONMode)
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
		provider := "openai"
		if c.isOllama() {
			provider = "ollama"
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: provider}
	}

	var full strings.Builder
	var reasoning strings.Builder
	var model string
	var inputTokens, outputTokens int

	// Accumulate tool call fragments by index.
	toolCallBuilders := make(map[int]*struct {
		id   string
		name strings.Builder
		args strings.Builder
	})

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for thinking models
	for scanner.Scan() {
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
			continue
		}

		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			inputTokens = chunk.Usage.PromptTokens
			outputTokens = chunk.Usage.CompletionTokens
		}

		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta

			// Thinking models (qwen3, deepseek-r1, etc.) emit reasoning
			// in a separate field before the final answer.
			if delta.Reasoning != "" {
				reasoning.WriteString(delta.Reasoning)
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
		Trace("[openai]: <-- REASONING:\n%s", reasoning.String())
	}

	if reasoning.Len() > 0 {
		Debug("[openai]: reasoning present (%d chars), content=%d chars", reasoning.Len(), full.Len())
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

	Debug("[openai]: Stream complete: model=%s input_tokens=%d output_tokens=%d tool_calls=%d", model, inputTokens, outputTokens, len(toolCalls))
	Trace("<-- STREAM COMPLETE: model=%s input_tokens=%d output_tokens=%d", model, inputTokens, outputTokens)
	if full.Len() > 0 {
		Trace("<-- RESPONSE TEXT:\n%s", full.String())
	}
	for _, tc := range toolCalls {
		argsJSON, _ := json.Marshal(tc.Args)
		Trace("<-- TOOL CALL: id=%s name=%s args=%s", tc.ID, tc.Name, string(argsJSON))
	}

	return &Response{
		Content:      full.String(),
		Reasoning:    reasoning.String(),
		ToolCalls:    toolCalls,
		Model:        model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}, nil
}
