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
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
	"github.com/cmcoffee/snugforge/iotimeout"
)

const (
	geminiEndpoint = "https://generativelanguage.googleapis.com/v1beta"
)

// GeminiModels queries the Gemini API and returns available model names.
// Only returns models that support generateContent (chat-capable).
func GeminiModels(apiKey string) ([]string, error) {
	client := &apiclient.APIClient{
		Server:         "generativelanguage.googleapis.com",
		VerifySSL:      true,
		ConnectTimeout: llmConnectTimeout,
		RequestTimeout: 15 * time.Second,
		AuthFunc: func(req *http.Request) {
			q := req.URL.Query()
			q.Set("key", apiKey)
			req.URL.RawQuery = q.Encode()
		},
	}
	req, err := client.NewRequest("GET", "/v1beta/models")
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
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	var names []string
	for _, m := range result.Models {
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				name := strings.TrimPrefix(m.Name, "models/")
				names = append(names, name)
				break
			}
		}
	}
	return names, nil
}

// geminiClient implements the LLM interface for Google's Gemini API.
type geminiClient struct {
	apiKey          string
	model           string
	api             *apiclient.APIClient
	disableThinking bool
	thinkingBudget  int // 0 = default (16384); positive = cap at that many tokens
}

// NewGeminiLLM creates an LLM client for Google Gemini using the default HTTP client.
func NewGeminiLLM(apiKey string, model string) LLM {
	return newGeminiLLM(apiKey, model, false, 0, nil)
}

// newGeminiLLM creates a Gemini LLM client with optional APIClient.
func newGeminiLLM(apiKey string, model string, disableThinking bool, thinkingBudget int, api *apiclient.APIClient) LLM {
	if api == nil {
		api = &apiclient.APIClient{
			VerifySSL:      true,
			ConnectTimeout: llmConnectTimeout,
			RequestTimeout: llmRequestTimeout,
		}
	}
	api.Server = "generativelanguage.googleapis.com"
	// Gemini uses API key in query params, not headers.
	api.AuthFunc = func(req *http.Request) {
		q := req.URL.Query()
		q.Set("key", apiKey)
		req.URL.RawQuery = q.Encode()
	}
	return &geminiClient{
		apiKey:          apiKey,
		model:           model,
		api:             api,
		disableThinking: disableThinking,
		thinkingBudget:  thinkingBudget,
	}
}

// Gemini API types

type gemContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []gemPart `json:"parts"`
}

type gemPart struct {
	Text             string              `json:"text,omitempty"`
	Thought          bool                `json:"thought,omitempty"`
	FunctionCall     *gemFunctionCall    `json:"functionCall,omitempty"`
	FunctionResponse *gemFunctionResponse `json:"functionResponse,omitempty"`
}

type gemFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args,omitempty"`
}

type gemFunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

type gemTool struct {
	FunctionDeclarations []gemFunctionDecl `json:"functionDeclarations"`
}

type gemFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type gemThinkingConfig struct {
	ThinkingBudget *int `json:"thinkingBudget,omitempty"` // Max thinking tokens; nil = model default, -1 = disabled.
}

type gemGenerationConfig struct {
	MaxOutputTokens int               `json:"maxOutputTokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	ResponseMimeType string           `json:"responseMimeType,omitempty"`
	ThinkingConfig  *gemThinkingConfig `json:"thinkingConfig,omitempty"`
}

type gemSafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

type gemRequest struct {
	Contents          []gemContent         `json:"contents"`
	SystemInstruction *gemContent          `json:"systemInstruction,omitempty"`
	Tools             []gemTool            `json:"tools,omitempty"`
	GenerationConfig  *gemGenerationConfig `json:"generationConfig,omitempty"`
	SafetySettings    []gemSafetySetting   `json:"safetySettings,omitempty"`
}

type gemResponse struct {
	Candidates []struct {
		Content      gemContent `json:"content"`
		FinishReason string     `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
	Error        *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// buildMessages converts generic Messages to Gemini contents.
func (c *geminiClient) buildMessages(messages []Message) []gemContent {
	var contents []gemContent
	for _, m := range messages {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			var parts []gemPart
			if m.Content != "" {
				parts = append(parts, gemPart{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := make(map[string]interface{})
				for k, v := range tc.Args {
					args[k] = v
				}
				parts = append(parts, gemPart{
					FunctionCall: &gemFunctionCall{
						Name: tc.Name,
						Args: args,
					},
				})
			}
			contents = append(contents, gemContent{Role: "model", Parts: parts})

		case len(m.ToolResults) > 0:
			var parts []gemPart
			for _, tr := range m.ToolResults {
				resp := map[string]interface{}{"content": tr.Content}
				if tr.IsError {
					resp["error"] = tr.Content
				}
				// Extract tool name from ID format "gem:<name>:<uuid>".
				name := tr.ID
				if parts := strings.SplitN(name, ":", 3); len(parts) == 3 && parts[0] == "gem" {
					name = parts[1]
				}
				parts = append(parts, gemPart{
					FunctionResponse: &gemFunctionResponse{
						Name:     name,
						Response: resp,
					},
				})
			}
			contents = append(contents, gemContent{Role: "user", Parts: parts})

		default:
			role := m.Role
			if role == "assistant" {
				role = "model"
			}
			contents = append(contents, gemContent{
				Role:  role,
				Parts: []gemPart{{Text: m.Content}},
			})
		}
	}
	return contents
}

// buildGeminiTools converts generic Tool definitions to Gemini format.
func buildGeminiTools(tools []Tool) []gemTool {
	if len(tools) == 0 {
		return nil
	}
	var decls []gemFunctionDecl
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
		decls = append(decls, gemFunctionDecl{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  json.RawMessage(raw),
		})
	}
	return []gemTool{{FunctionDeclarations: decls}}
}

// parseGeminiResponse extracts text content, thinking, and tool calls from a Gemini response.
// Returns (content, reasoning, toolCalls) — thinking model parts with thought=true
// go to reasoning; regular text parts go to content.
func parseGeminiResponse(resp gemResponse) (string, string, []ToolCall) {
	if len(resp.Candidates) == 0 {
		return "", "", nil
	}
	var textParts []string
	var thinkParts []string
	var toolCalls []ToolCall
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			if part.Thought {
				thinkParts = append(thinkParts, part.Text)
			} else {
				textParts = append(textParts, part.Text)
			}
		}
		if part.FunctionCall != nil {
			args := parseToolArgs(part.FunctionCall.Args)
			toolCalls = append(toolCalls, ToolCall{
				ID:   fmt.Sprintf("gem:%s:%s", part.FunctionCall.Name, UUIDv4()),
				Name: part.FunctionCall.Name,
				Args: args,
			})
		}
	}
	return strings.Join(textParts, ""), strings.Join(thinkParts, ""), toolCalls
}

// geminiSupportsThinking reports whether the model accepts a ThinkingConfig.
// Covers gemini-2.5-* and gemini-3.* families; older models ignore the field
// but some return an error, so we gate it explicitly.
func geminiSupportsThinking(model string) bool {
	return strings.Contains(model, "2.5") || strings.Contains(model, "gemini-3")
}

func (c *geminiClient) doRequest(ctx context.Context, urlPath string, body []byte) (*http.Response, error) {
	path := "/v1beta/" + urlPath

	Trace("--> GEMINI REQUEST: POST %s", path)
	Trace("--> BODY:\n%s", string(body))

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
	if err == nil {
		resp.Body = iotimeout.NewReadCloser(resp.Body, c.api.RequestTimeout)
	}
	return resp, err
}

// Chat sends a non-streaming request to Gemini.
func (c *geminiClient) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.model, 4096, opts)

	payload := gemRequest{
		Contents: c.buildMessages(messages),
		Tools:    buildGeminiTools(cfg.Tools),
		SafetySettings: []gemSafetySetting{
			{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_HATE_SPEECH", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_SEXUALLY_EXPLICIT", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_DANGEROUS_CONTENT", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_CIVIC_INTEGRITY", Threshold: "BLOCK_NONE"},
		},
	}
	if cfg.SystemPrompt != "" {
		payload.SystemInstruction = &gemContent{
			Parts: []gemPart{{Text: cfg.SystemPrompt}},
		}
	}
	genCfg := &gemGenerationConfig{}
	if cfg.MaxTokens > 0 {
		genCfg.MaxOutputTokens = cfg.MaxTokens
	}
	if cfg.Temperature != nil {
		genCfg.Temperature = cfg.Temperature
	}
	if cfg.JSONMode {
		genCfg.ResponseMimeType = "application/json"
	}
	// Enable thinking for models that support it (gemini-2.5-* and gemini-3.*).
	// Flash supports thinkingBudget:0 to fully disable thinking. Pro models
	// require a minimum positive budget and reject 0 — for those, omitting
	// ThinkingConfig lets the model use its default (Pro always thinks anyway).
	// When thinking is on, bump maxOutputTokens so visible output isn't
	// starved by thinking tokens.
	if c.disableThinking {
		f := false
		cfg.Think = &f
	}
	if geminiSupportsThinking(cfg.Model) {
		if cfg.Think != nil && !*cfg.Think {
			if strings.Contains(cfg.Model, "flash") {
				zero := 0
				genCfg.ThinkingConfig = &gemThinkingConfig{ThinkingBudget: &zero}
			}
			// Pro: leave ThinkingConfig nil — can't disable, model uses default.
			Debug("[gemini]: thinking disabled: model=%s", cfg.Model)
		} else {
			budget := c.thinkingBudget
			if cfg.ThinkBudget != nil && *cfg.ThinkBudget > 0 {
				budget = *cfg.ThinkBudget
			}
			if budget <= 0 {
				budget = 16384
			}
			genCfg.ThinkingConfig = &gemThinkingConfig{ThinkingBudget: &budget}
			if genCfg.MaxOutputTokens > 0 {
				genCfg.MaxOutputTokens += budget
			}
			Debug("[gemini]: thinking enabled: model=%s budget=%d maxOutputTokens=%d", cfg.Model, budget, genCfg.MaxOutputTokens)
		}
	}
	payload.GenerationConfig = genCfg

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	Debug("[gemini]: Sending request: model=%s body=%d bytes think=%s maxOutputTokens=%d",
		cfg.Model, len(body), fmtThink(cfg.Think), genCfg.MaxOutputTokens)

	urlPath := fmt.Sprintf("models/%s:generateContent", cfg.Model)
	resp, err := c.doRequest(ctx, urlPath, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	Trace("<-- GEMINI RESPONSE (%d):\n%s", resp.StatusCode, string(respBody))

	if resp.StatusCode != http.StatusOK {
		msg := string(respBody)
		var gemResp gemResponse
		if json.Unmarshal(respBody, &gemResp) == nil && gemResp.Error != nil {
			msg = gemResp.Error.Message
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: "gemini"}
	}

	var result gemResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("gemini: failed to parse response: %w", err)
	}

	content, reasoning, toolCalls := parseGeminiResponse(result)

	finishReason := ""
	if len(result.Candidates) > 0 {
		finishReason = result.Candidates[0].FinishReason
	}
	Debug("[gemini]: Chat complete: model=%s input_tokens=%d output_tokens=%d thinking_tokens=%d tool_calls=%d finish=%s",
		result.ModelVersion, result.UsageMetadata.PromptTokenCount, result.UsageMetadata.CandidatesTokenCount, result.UsageMetadata.ThoughtsTokenCount, len(toolCalls), finishReason)
	if finishReason == "SAFETY" || finishReason == "RECITATION" || finishReason == "BLOCKLIST" {
		Debug("[gemini]: response blocked by safety filter: %s", finishReason)
	}
	if reasoning != "" {
		Trace("[gemini]: <-- THINKING:\n%s", reasoning)
	}

	return &Response{
		Content:      content,
		Reasoning:    reasoning,
		ToolCalls:    toolCalls,
		Model:        result.ModelVersion,
		InputTokens:  result.UsageMetadata.PromptTokenCount,
		OutputTokens: result.UsageMetadata.CandidatesTokenCount,
	}, nil
}

// ChatStream sends a streaming request to Gemini.
func (c *geminiClient) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	cfg := applyOpts(c.model, 4096, opts)

	payload := gemRequest{
		Contents: c.buildMessages(messages),
		Tools:    buildGeminiTools(cfg.Tools),
		SafetySettings: []gemSafetySetting{
			{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_HATE_SPEECH", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_SEXUALLY_EXPLICIT", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_DANGEROUS_CONTENT", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_CIVIC_INTEGRITY", Threshold: "BLOCK_NONE"},
		},
	}
	if cfg.SystemPrompt != "" {
		payload.SystemInstruction = &gemContent{
			Parts: []gemPart{{Text: cfg.SystemPrompt}},
		}
	}
	genCfg := &gemGenerationConfig{}
	if cfg.MaxTokens > 0 {
		genCfg.MaxOutputTokens = cfg.MaxTokens
	}
	if cfg.Temperature != nil {
		genCfg.Temperature = cfg.Temperature
	}
	if cfg.JSONMode {
		genCfg.ResponseMimeType = "application/json"
	}
	if geminiSupportsThinking(cfg.Model) {
		if cfg.Think != nil && !*cfg.Think {
			if strings.Contains(cfg.Model, "flash") {
				zero := 0
				genCfg.ThinkingConfig = &gemThinkingConfig{ThinkingBudget: &zero}
			}
		} else {
			budget := c.thinkingBudget
			if cfg.ThinkBudget != nil && *cfg.ThinkBudget > 0 {
				budget = *cfg.ThinkBudget
			}
			if budget <= 0 {
				budget = 16384
			}
			genCfg.ThinkingConfig = &gemThinkingConfig{ThinkingBudget: &budget}
			if genCfg.MaxOutputTokens > 0 {
				genCfg.MaxOutputTokens += budget
			}
		}
	}
	payload.GenerationConfig = genCfg

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	Debug("[gemini]: Sending stream request: model=%s body=%d bytes think=%s maxOutputTokens=%d",
		cfg.Model, len(body), fmtThink(cfg.Think), genCfg.MaxOutputTokens)

	urlPath := fmt.Sprintf("models/%s:streamGenerateContent?alt=sse", cfg.Model)
	resp, err := c.doRequest(ctx, urlPath, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		Trace("<-- GEMINI STREAM ERROR (%d):\n%s", resp.StatusCode, string(respBody))
		msg := string(respBody)
		var gemResp gemResponse
		if json.Unmarshal(respBody, &gemResp) == nil && gemResp.Error != nil {
			msg = gemResp.Error.Message
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: "gemini"}
	}

	var full strings.Builder
	var thinking strings.Builder
	var toolCalls []ToolCall
	var modelVersion string
	var inputTokens, outputTokens int

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var chunk gemResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.ModelVersion != "" {
			modelVersion = chunk.ModelVersion
		}
		if chunk.UsageMetadata.PromptTokenCount > 0 {
			inputTokens = chunk.UsageMetadata.PromptTokenCount
		}
		if chunk.UsageMetadata.CandidatesTokenCount > 0 {
			outputTokens = chunk.UsageMetadata.CandidatesTokenCount
		}

		if len(chunk.Candidates) > 0 {
			for _, part := range chunk.Candidates[0].Content.Parts {
				if part.Text != "" {
					if part.Thought {
						thinking.WriteString(part.Text)
					} else {
						full.WriteString(part.Text)
						if handler != nil {
							handler(part.Text)
						}
					}
				}
				if part.FunctionCall != nil {
					args := parseToolArgs(part.FunctionCall.Args)
					toolCalls = append(toolCalls, ToolCall{
						ID:   fmt.Sprintf("gem:%s:%s", part.FunctionCall.Name, UUIDv4()),
						Name: part.FunctionCall.Name,
						Args: args,
					})
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("gemini: stream read error: %w", err)
	}

	Debug("[gemini]: Stream complete: model=%s input_tokens=%d output_tokens=%d tool_calls=%d thinking=%d",
		modelVersion, inputTokens, outputTokens, len(toolCalls), thinking.Len())
	if thinking.Len() > 0 {
		Trace("[gemini]: <-- THINKING:\n%s", thinking.String())
	}
	if full.Len() > 0 {
		Trace("<-- GEMINI RESPONSE TEXT:\n%s", full.String())
	}
	for _, tc := range toolCalls {
		argsJSON, _ := json.Marshal(tc.Args)
		Trace("<-- GEMINI TOOL CALL: id=%s name=%s args=%s", tc.ID, tc.Name, string(argsJSON))
	}

	return &Response{
		Content:      full.String(),
		Reasoning:    thinking.String(),
		ToolCalls:    toolCalls,
		Model:        modelVersion,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}, nil
}
