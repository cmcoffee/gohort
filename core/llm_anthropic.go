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
			ConnectTimeout: llmConnectTimeout,
			RequestTimeout: llmRequestTimeout,
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

type anthRequest struct {
	Model       string        `json:"model"`
	Messages    []anthMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	System      string        `json:"system,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	Tools       []anthTool    `json:"tools,omitempty"`
}

type anthTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthMessage struct {
	Role    string            `json:"role"`
	Content json.RawMessage   `json:"content"`
}

type anthContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   string      `json:"content,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`
}

type anthResponse struct {
	Content []anthContentBlock `json:"content"`
	Model   string             `json:"model"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
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

// buildAnthTools converts generic Tool definitions to Anthropic format.
func buildAnthTools(tools []Tool) []anthTool {
	var out []anthTool
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
		out = append(out, anthTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: json.RawMessage(raw),
		})
	}
	return out
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
	}
}

// Chat sends a non-streaming request.
func (c *anthropicClient) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.model, 4096, opts)

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

	payload := anthRequest{
		Model:       cfg.Model,
		Messages:    msgs,
		MaxTokens:   cfg.MaxTokens,
		System:      systemPrompt,
		Temperature: cfg.Temperature,
		Tools:       buildAnthTools(cfg.Tools),
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
	Debug("[anthropic]: Response: model=%s input_tokens=%d output_tokens=%d tool_calls=%d", r.Model, r.InputTokens, r.OutputTokens, len(r.ToolCalls))
	return r, nil
}

// ChatStream sends a streaming request.
func (c *anthropicClient) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.model, 4096, opts)

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

	payload := anthRequest{
		Model:       cfg.Model,
		Messages:    msgs,
		MaxTokens:   cfg.MaxTokens,
		System:      systemPrompt,
		Temperature: cfg.Temperature,
		Stream:      true,
		Tools:       buildAnthTools(cfg.Tools),
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

	return &Response{
		Content:      textContent.String(),
		ToolCalls:    toolCalls,
		Model:        model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}, nil
}
