package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cmcoffee/snugforge/apiclient"
)

const (
	anthropicEndpoint   = "https://api.anthropic.com/v1"
	anthropicAPIVersion = "2023-06-01"

	// Default output budgets. The agent loop never sets WithMaxTokens, so
	// these are what a lead final answer is capped at. 4096 (the old value)
	// silently truncated long answers mid-thought because stop_reason was
	// dropped; give real headroom. Streaming avoids the HTTP-timeout concern
	// so it gets a larger default.
	anthDefaultMaxTokens       = 8192
	anthDefaultStreamMaxTokens = 16384
)

// anthropicClient implements the LLM interface for the Anthropic Messages API.
type anthropicClient struct {
	apiKey string
	model  string
	api    *apiclient.APIClient
}

// NewAnthropicLLM creates an LLM client for Anthropic (Claude) using the default HTTP client.
func NewAnthropicLLM(apiKey string, model string) LLM {
	return newAnthropicLLM(apiKey, model, nil)
}

// newAnthropicLLM creates an LLM client with optional APIClient.
func newAnthropicLLM(apiKey string, model string, api *apiclient.APIClient) LLM {
	if api == nil {
		api = &apiclient.APIClient{
			VerifySSL:      true,
			ConnectTimeout: llmConnectTimeout(),
			RequestTimeout: llmRequestTimeout(),
		}
	}
	api.Server = "api.anthropic.com"
	api.AuthFunc = func(req *http.Request) {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", anthropicAPIVersion)
	}
	return &anthropicClient{
		apiKey: apiKey,
		model:  model,
		api:    api,
	}
}

// Anthropic request/response types

// cacheControl marks a prompt-cache breakpoint. Anthropic prompt caching is
// opt-in per content block; without it every request is billed as a full
// uncached prefill regardless of how stable the prefix is.
type cacheControl struct {
	Type string `json:"type"` // always "ephemeral"
}

func ephemeralCache() *cacheControl { return &cacheControl{Type: "ephemeral"} }

// anthRequest carries no sampling params: temperature/top_p/top_k are rejected
// with a 400 on current Claude models (Opus 4.7/4.8, Sonnet 5).
type anthRequest struct {
	Model     string            `json:"model"`
	Messages  []anthMessage     `json:"messages"`
	MaxTokens int               `json:"max_tokens"`
	System    []anthSystemBlock `json:"system,omitempty"`
	Stream    bool              `json:"stream,omitempty"`
	Tools     []anthTool        `json:"tools,omitempty"`
}

// anthSystemBlock is the block form of the system prompt so a cache_control
// breakpoint can be attached (the plain-string form cannot be cached).
type anthSystemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type anthTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type anthMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthContentBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
	StopReason   string          `json:"stop_reason,omitempty"` // carried on the message_delta stream event
}

type anthResponse struct {
	Content []anthContentBlock `json:"content"`
	Model   string             `json:"model"`
	Usage   struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	StopReason string `json:"stop_reason"`
}

type anthStreamEvent struct {
	Type         string            `json:"type"`
	Index        int               `json:"index,omitempty"`
	ContentBlock *anthContentBlock `json:"content_block,omitempty"`
	Delta        *anthContentBlock `json:"delta,omitempty"`
	Message      *anthResponse     `json:"message,omitempty"`
	Usage        *struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

type anthError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// snoopRequest logs the outbound HTTP request details via Trace.
func (c *anthropicClient) snoopRequest(body []byte, stream bool) {
	Trace("[anthropic]: %s", c.api.Server)
	if stream {
		Trace("--> METHOD: \"POST\" PATH: \"/messages\" (streaming)")
	} else {
		Trace("--> METHOD: \"POST\" PATH: \"/messages\"")
	}
	Trace("--> HEADER: x-api-key: [HIDDEN]")
	Trace("--> HEADER: anthropic-version: %s", anthropicAPIVersion)
	Trace("--> HEADER: Content-Type: application/json")

	// Pretty-print the request body with API key redacted.
	var pretty map[string]interface{}
	if json.Unmarshal(body, &pretty) == nil {
		formatted, _ := json.MarshalIndent(pretty, "", "  ")
		Trace("--> REQUEST BODY:\n%s", string(formatted))
	}
}

// snoopResponse logs the HTTP response details via Trace.
func snoopAnthResponse(statusCode int, body []byte) {
	Trace("<-- RESPONSE STATUS: %d", statusCode)
	var generic map[string]interface{}
	if json.Unmarshal(body, &generic) == nil {
		formatted, _ := json.MarshalIndent(generic, "", "  ")
		Trace("<-- RESPONSE BODY:\n%s", string(formatted))
	} else {
		Trace("<-- RESPONSE BODY:\n%s", string(body))
	}
}

func (c *anthropicClient) doRequest(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	c.snoopRequest(body, stream)
	Debug("[anthropic]: Sending request to %s/v1/messages (stream=%v)", c.api.Server, stream)

	req, err := c.api.NewRequestWithContext(ctx, "POST", "/v1/messages")
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	resp, err := c.api.SendRawRequest("", req)
	if err != nil {
		Debug("[anthropic]: Request failed: %v", err)
	} else {
		Debug("[anthropic]: Response status: %d", resp.StatusCode)
	}
	return resp, err
}

// buildAnthMessages converts generic Messages into Anthropic-formatted messages.
func buildAnthMessages(messages []Message) ([]anthMessage, error) {
	var msgs []anthMessage
	for _, m := range messages {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			// Assistant message with tool calls: build content blocks.
			var blocks []anthContentBlock
			if m.Content != "" {
				blocks = append(blocks, anthContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				inputJSON, err := json.Marshal(tc.Args)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, anthContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: json.RawMessage(inputJSON),
				})
			}
			raw, err := json.Marshal(blocks)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, anthMessage{Role: "assistant", Content: raw})

		case len(m.ToolResults) > 0:
			// Tool result message.
			var blocks []anthContentBlock
			for _, tr := range m.ToolResults {
				blocks = append(blocks, anthContentBlock{
					Type:      "tool_result",
					ToolUseID: tr.ID,
					Content:   tr.Content,
					IsError:   tr.IsError,
				})
			}
			raw, err := json.Marshal(blocks)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, anthMessage{Role: "user", Content: raw})

		default:
			// Simple text message.
			raw, err := json.Marshal(m.Content)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, anthMessage{Role: m.Role, Content: raw})
		}
	}
	return msgs, nil
}

// buildAnthTools converts generic Tool definitions to Anthropic format and
// caches the tools block. Tools render first in the prefix, so a breakpoint on
// the last tool caches the entire (large, stable) tool schema block for reuse.
func buildAnthTools(tools []Tool) []anthTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, anthTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: buildToolParamsSchema(t),
		})
	}
	out[len(out)-1].CacheControl = ephemeralCache()
	return out
}

// buildSystemBlocks renders the system prompt as a single cached text block.
// System renders after tools, so this breakpoint caches tools+system together.
func buildSystemBlocks(system string) []anthSystemBlock {
	if system == "" {
		return nil
	}
	return []anthSystemBlock{{Type: "text", Text: system, CacheControl: ephemeralCache()}}
}

// addCacheBreakpoint stamps an ephemeral breakpoint on the last content block
// of the newest message so the whole prior-conversation prefix is written once
// and read on subsequent turns. Best-effort: on any decode issue it leaves the
// message untouched. A plain-string message is promoted to a single text block
// so the marker has somewhere to live.
func addCacheBreakpoint(msgs []anthMessage) {
	if len(msgs) == 0 {
		return
	}
	last := &msgs[len(msgs)-1]
	var blocks []anthContentBlock
	if err := json.Unmarshal(last.Content, &blocks); err == nil && len(blocks) > 0 {
		blocks[len(blocks)-1].CacheControl = ephemeralCache()
		if raw, err := json.Marshal(blocks); err == nil {
			last.Content = raw
		}
		return
	}
	var text string
	if err := json.Unmarshal(last.Content, &text); err == nil {
		wrapped := []anthContentBlock{{Type: "text", Text: text, CacheControl: ephemeralCache()}}
		if raw, err := json.Marshal(wrapped); err == nil {
			last.Content = raw
		}
	}
}

// parseAnthResponse extracts text content and tool calls from an Anthropic response.
func parseAnthResponse(result anthResponse) *Response {
	var text strings.Builder
	var toolCalls []ToolCall
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			args := make(map[string]any)
			var raw map[string]interface{}
			if json.Unmarshal(block.Input, &raw) == nil {
				args = parseToolArgs(raw)
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   block.ID,
				Name: block.Name,
				Args: args,
			})
		}
	}
	return &Response{
		Content:      text.String(),
		ToolCalls:    toolCalls,
		Model:        result.Model,
		InputTokens:  result.Usage.InputTokens,
		OutputTokens: result.Usage.OutputTokens,
		StopReason:   result.StopReason,
	}
}

// warnStopReason surfaces terminal stop reasons that would otherwise be
// invisible. A refusal or a max_tokens truncation arrives as an assistant
// message with no tool calls, so the agent loop finalizes it as a normal
// answer; log it so the truncation/refusal isn't silent.
func warnStopReason(stopReason string) {
	switch stopReason {
	case "max_tokens":
		Warn("[anthropic]: response truncated (stop_reason=max_tokens) — raise max_tokens or the answer is cut off mid-thought")
	case "refusal":
		Warn("[anthropic]: model declined the request (stop_reason=refusal)")
	}
}

// Chat sends a non-streaming request.
func (c *anthropicClient) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.model, anthDefaultMaxTokens, opts)

	systemPrompt := cfg.SystemPrompt
	if cfg.JSONMode {
		jsonInstr := "You must respond with valid JSON only. No markdown, no explanation, just JSON."
		if systemPrompt != "" {
			systemPrompt = systemPrompt + "\n\n" + jsonInstr
		} else {
			systemPrompt = jsonInstr
		}
	}

	msgs, err := buildAnthMessages(messages)
	if err != nil {
		return nil, err
	}
	addCacheBreakpoint(msgs)

	payload := anthRequest{
		Model:     cfg.Model,
		Messages:  msgs,
		MaxTokens: cfg.MaxTokens,
		System:    buildSystemBlocks(systemPrompt),
		Tools:     buildAnthTools(cfg.Tools),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := c.doRequest(ctx, body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	snoopAnthResponse(resp.StatusCode, respBody)

	if resp.StatusCode != http.StatusOK {
		msg := string(respBody)
		var apiErr anthError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			msg = apiErr.Error.Message
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: "anthropic"}
	}

	var result anthResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	r := parseAnthResponse(result)
	warnStopReason(r.StopReason)
	Debug("[anthropic]: Response: model=%s input_tokens=%d output_tokens=%d cache_read=%d cache_write=%d tool_calls=%d stop=%s", r.Model, r.InputTokens, r.OutputTokens, result.Usage.CacheReadInputTokens, result.Usage.CacheCreationInputTokens, len(r.ToolCalls), r.StopReason)
	return r, nil
}

// ChatStream sends a streaming request.
func (c *anthropicClient) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.model, anthDefaultStreamMaxTokens, opts)

	systemPrompt := cfg.SystemPrompt
	if cfg.JSONMode {
		jsonInstr := "You must respond with valid JSON only. No markdown, no explanation, just JSON."
		if systemPrompt != "" {
			systemPrompt = systemPrompt + "\n\n" + jsonInstr
		} else {
			systemPrompt = jsonInstr
		}
	}

	msgs, err := buildAnthMessages(messages)
	if err != nil {
		return nil, err
	}
	addCacheBreakpoint(msgs)

	payload := anthRequest{
		Model:     cfg.Model,
		Messages:  msgs,
		MaxTokens: cfg.MaxTokens,
		System:    buildSystemBlocks(systemPrompt),
		Stream:    true,
		Tools:     buildAnthTools(cfg.Tools),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := c.doRequest(ctx, body, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		msg := string(respBody)
		var apiErr anthError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			msg = apiErr.Error.Message
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: "anthropic"}
	}

	var textContent strings.Builder
	var model string
	var inputTokens, outputTokens int
	var stopReason string
	var toolCalls []ToolCall

	// Track current content block for tool_use assembly.
	type blockState struct {
		blockType string
		id        string
		name      string
		inputBuf  strings.Builder
	}
	var currentBlocks []blockState

	scanner := bufio.NewScanner(resp.Body)
	// Default Scanner caps a line at 64KB; a single large SSE `data:` line
	// (a big content block) would trip bufio.ErrTooLong and abort the stream.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event anthStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				model = event.Message.Model
				inputTokens = event.Message.Usage.InputTokens
			}
		case "content_block_start":
			if event.ContentBlock != nil {
				bs := blockState{blockType: event.ContentBlock.Type}
				if event.ContentBlock.Type == "tool_use" {
					bs.id = event.ContentBlock.ID
					bs.name = event.ContentBlock.Name
				}
				// Grow slice to accommodate index.
				for len(currentBlocks) <= event.Index {
					currentBlocks = append(currentBlocks, blockState{})
				}
				currentBlocks[event.Index] = bs
			}
		case "content_block_delta":
			if event.Delta != nil {
				if event.Index < len(currentBlocks) {
					bs := &currentBlocks[event.Index]
					switch bs.blockType {
					case "text":
						if event.Delta.Text != "" {
							textContent.WriteString(event.Delta.Text)
							if handler != nil {
								handler(event.Delta.Text)
							}
						}
					case "tool_use":
						// input_json_delta: delta contains partial JSON.
						if event.Delta.Type == "input_json_delta" && event.Delta.Text != "" {
							bs.inputBuf.WriteString(event.Delta.Text)
						}
					}
				} else if event.Delta.Text != "" {
					// Fallback for simple text deltas without content_block_start.
					textContent.WriteString(event.Delta.Text)
					if handler != nil {
						handler(event.Delta.Text)
					}
				}
			}
		case "content_block_stop":
			if event.Index < len(currentBlocks) {
				bs := &currentBlocks[event.Index]
				if bs.blockType == "tool_use" {
					args := make(map[string]any)
					var raw map[string]interface{}
					if json.Unmarshal([]byte(bs.inputBuf.String()), &raw) == nil {
						args = parseToolArgs(raw)
					}
					toolCalls = append(toolCalls, ToolCall{
						ID:   bs.id,
						Name: bs.name,
						Args: args,
					})
				}
			}
		case "message_delta":
			if event.Usage != nil {
				outputTokens = event.Usage.OutputTokens
			}
			// Terminal stop reason is delivered here on the stream. Without
			// capturing it, a refusal or a max_tokens truncation is invisible
			// (it arrives as an assistant turn with no tool calls).
			if event.Delta != nil && event.Delta.StopReason != "" {
				stopReason = event.Delta.StopReason
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read error: %w", err)
	}

	Debug("[anthropic]: Stream complete: model=%s input_tokens=%d output_tokens=%d tool_calls=%d", model, inputTokens, outputTokens, len(toolCalls))
	Trace("<-- STREAM COMPLETE: model=%s input_tokens=%d output_tokens=%d", model, inputTokens, outputTokens)
	if textContent.Len() > 0 {
		Trace("<-- RESPONSE TEXT:\n%s", textContent.String())
	}
	for _, tc := range toolCalls {
		argsJSON, _ := json.Marshal(tc.Args)
		Trace("<-- TOOL CALL: id=%s name=%s args=%s", tc.ID, tc.Name, string(argsJSON))
	}

	warnStopReason(stopReason)
	return &Response{
		Content:      textContent.String(),
		ToolCalls:    toolCalls,
		Model:        model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		StopReason:   stopReason,
	}, nil
}
