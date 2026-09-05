package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

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
	// anthDefaultThinkBudget is the reasoning allowance when a call asks for
	// thinking without naming a budget. Matches the framework's own default
	// (DEFAULT_THINKING_BUDGET) so a Claude call and a llama.cpp call asked for
	// the same thing get the same thing.
	anthDefaultThinkBudget = 4096
	// anthThinkAnswerHeadroom is what max_tokens is lifted above the budget by
	// when a caller's ceiling would not leave room to answer.
	anthThinkAnswerHeadroom = 4096

	anthDefaultMaxTokens = 8192
	// The STREAMING default is where a turn actually lives, and 16384 was the
	// documented ceiling for the NON-streaming path — applied here it left a
	// model with adaptive thinking (which shares this one allowance) able to
	// spend the entire budget reasoning and be cut off before it emitted the
	// tool call it had just announced. Anthropic's guidance is ~16k
	// non-streaming, ~64k streaming, precisely because streaming removes the
	// HTTP-timeout concern this constant's own comment cites; the models this
	// client serves accept up to 128k.
	anthDefaultStreamMaxTokens = 64000
)

// anthropicClient implements the LLM interface for the Anthropic Messages API.
//
// The same client serves AWS Bedrock, which fronts the identical Messages API
// under a path prefix with its own auth: see llm_bedrock.go, which sets
// pathPrefix and swaps api.AuthFunc and leaves everything else here alone.
type anthropicClient struct {
	apiKey string
	model  string
	api    *apiclient.APIClient
	// pathPrefix sits in front of /v1/messages. Empty for api.anthropic.com;
	// "/anthropic" for Bedrock.
	pathPrefix string
	// hoistSystem folds system-role turns out of the message list. Bedrock
	// rejects them on BOTH its endpoints ("use the top-level 'system'
	// parameter"), while the first-party API accepts them mid-conversation on
	// current models and gohort relies on that — so this is opt-in per client
	// rather than a change to the shared builder.
	hoistSystem bool
	// contextSize is the operator-configured working context cap (tokens);
	// 0 falls back to anthropicDefaultContextSize. See ContextSize.
	contextSize int
}

// anthropicDefaultContextSize is the working context cap reported when the
// operator hasn't set one. Current Claude models accept a 1M-token window,
// but this is deliberately NOT that number: it's the budget the agent loop's
// compactHistory keys on, and the reasons to stay well under the true window
// are cost (input tokens bill per turn — a history that crawls toward 1M is
// dollars of input on every round even with cache reads at 0.1x), prompt-cache
// economics (a growing prefix re-writes at 1.25-2x), and prefill latency.
// 200K leaves the compactor's 50% steady-state cap targeting ~100K of
// history — roomy for any real session, cheap enough to run all day.
// Operators who genuinely want long-context work raise context_size in the
// admin LLM form; the true API ceiling is the model's max_input_tokens (1M
// on current Opus/Sonnet/Fable, 200K on Haiku 4.5).
const anthropicDefaultContextSize = 200_000

// ContextSize implements ContextSizer. Before this existed, the Anthropic
// client silently failed the ContextSizer assertion: LeadContextSize() then
// fell back to the WORKER's window (so lead agent loops compacted against the
// local llama.cpp num_ctx — an arbitrary number belonging to a different
// model), and with no sized worker it returned 0, which disables compaction
// entirely and lets a runaway session grow until the API 400s — after
// billing its way there.
func (c *anthropicClient) ContextSize() int {
	if c.contextSize > 0 {
		return c.contextSize
	}
	return anthropicDefaultContextSize
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
	Type string `json:"type"`          // always "ephemeral"
	TTL  string `json:"ttl,omitempty"` // "1h" for the extended cache; absent = the 5-minute default
}

// extendedCacheTTL is the beta this deployment declares when the long
// cache is on. Sent as a header on the direct API and inside the body on
// Bedrock, which passes the Anthropic request through.
const extendedCacheTTL = "extended-cache-ttl-2025-04-11"

// ephemeralCache stamps a cache breakpoint, at whichever lifetime the
// deployment asked for.
//
// The two lifetimes are not a preference, they are an economic choice
// with a clear break-even. A 5-minute cache is written at 1.25x input
// and expires between a person reading a reply and typing the next one —
// so an interactive session re-writes its whole prefix nearly every
// turn. A 1-hour cache is written at 2x and survives the gap, so the
// prefix is written once and read at 0.1x thereafter. Over ten turns of
// a 200k prefix that is 2.5M billable-equivalent tokens against 0.58M.
//
// The 5-minute default stands because it is what a batch workload with
// no human pauses actually wants, and because changing what a deployment
// is billed is not a thing to do silently on upgrade.
func ephemeralCache() *cacheControl {
	if TuneBool("tune_prompt_cache_1h") {
		return &cacheControl{Type: "ephemeral", TTL: "1h"}
	}
	return &cacheControl{Type: "ephemeral"}
}

// PromptCache1hOn reports whether the extended cache is enabled, for the
// providers that must declare the beta and for the cost model, which
// prices a write differently under it.
func PromptCache1hOn() bool { return TuneBool("tune_prompt_cache_1h") }

// promptCacheBetas is the body-carried beta list for providers with no
// header to put it in. Nil when nothing needs declaring, so the field
// stays absent rather than empty.
func promptCacheBetas() []string {
	if PromptCache1hOn() {
		return []string{extendedCacheTTL}
	}
	return nil
}

// anthRequest carries no sampling params: temperature/top_p/top_k are rejected
// with a 400 on current Claude models (Opus 4.7/4.8, Sonnet 5).
type anthRequest struct {
	Model     string            `json:"model"`
	Messages  []anthMessage     `json:"messages"`
	MaxTokens int               `json:"max_tokens"`
	System    []anthSystemBlock `json:"system,omitempty"`
	Stream    bool              `json:"stream,omitempty"`
	Tools     []anthTool        `json:"tools,omitempty"`
	Thinking  *anthThinking     `json:"thinking,omitempty"`
	// OutputConfig carries the effort dial adaptive thinking uses. Absent for
	// the budgeted shape, which has no effort concept.
	OutputConfig *anthOutputConfig `json:"output_config,omitempty"`
}

// anthThinking is the extended-thinking block, shared by the direct Anthropic
// endpoint and both Bedrock modes because all three speak the Messages format.
//
// Until now none of them sent it. Thinking was honoured by llama.cpp, ollama and
// Gemini and silently dropped on every Claude path — so a route stage set to
// "lead (thinking)", a per-route budget and a per-agent budget all applied
// perfectly and produced a request with no thinking in it. The control said one
// thing and the model did another, on the tier where the reasoning was most
// likely to be wanted.
type anthThinking struct {
	Type string `json:"type"` // "enabled" (budgeted) or "adaptive" (effort-led)
	// BudgetTokens is the reasoning allowance for the BUDGETED shape. Omitted
	// for adaptive, where the model decides and effort is the dial.
	BudgetTokens int `json:"budget_tokens,omitempty"`
}

// anthOutputConfig carries the effort dial that goes with adaptive thinking.
type anthOutputConfig struct {
	Effort string `json:"effort,omitempty"` // "low" | "medium" | "high"
}

// Two thinking shapes, because Claude has two.
//
// The older models take a budget: thinking {type: enabled, budget_tokens: N}.
// Newer ones reject that outright — "thinking.type.enabled is not supported for
// this model. Use thinking.type.adaptive and output_config.effort" — and decide
// their own depth from an effort level instead.
//
// Which a given model wants is not derivable from its id in any way that will
// keep working: Bedrock ids move constantly and a hardcoded list is a
// maintenance trap that fails closed on every model released after it was
// written. So the budgeted shape is tried first and the model is BELIEVED when
// it says otherwise — see noteAdaptiveThinking. One 400, once per model per
// process, and every call after it is correct.
const (
	anthThinkBudgeted = "enabled"
	anthThinkAdaptive = "adaptive"
)

var (
	adaptiveThinkMu     sync.RWMutex
	adaptiveThinkModels = map[string]bool{}
)

// thinkingStyleFor reports which shape this model has told us it takes.
func thinkingStyleFor(model string) string {
	adaptiveThinkMu.RLock()
	defer adaptiveThinkMu.RUnlock()
	if adaptiveThinkModels[model] {
		return anthThinkAdaptive
	}
	return anthThinkBudgeted
}

// noteAdaptiveThinking records that a model rejected the budgeted shape, so the
// next call uses the one it asked for.
func noteAdaptiveThinking(model string) {
	if strings.TrimSpace(model) == "" {
		return
	}
	adaptiveThinkMu.Lock()
	already := adaptiveThinkModels[model]
	adaptiveThinkModels[model] = true
	adaptiveThinkMu.Unlock()
	if !already {
		Log("[llm] %s takes adaptive thinking rather than a token budget — switching for this model", model)
	}
}

// budgetAsEffort maps a token budget onto the effort dial adaptive thinking
// uses.
//
// The two are not convertible, and pretending otherwise would be worse than
// this: a budget says "spend up to N", an effort says "try this hard". What
// carries across is the operator's INTENT, so the thresholds sit either side of
// the framework default (4096) — below it they wanted less than standard, well
// above it they wanted noticeably more.
func budgetAsEffort(budget int) string {
	switch {
	case budget <= 0:
		return ""
	case budget < anthDefaultThinkBudget:
		return "low"
	case budget >= anthDefaultThinkBudget*3:
		return "high"
	}
	return "medium"
}

// isUnsupportedThinkingTypeErr reports whether a provider refused the budgeted
// shape and asked for the adaptive one.
//
// Matched on the message because that is what the API gives: the status is a
// plain 400 shared with every other malformed request, and reacting to all of
// those by changing the thinking shape would turn one clear error into two
// confusing ones.
func isUnsupportedThinkingTypeErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "thinking.type") &&
		(strings.Contains(msg, "adaptive") || strings.Contains(msg, "not supported"))
}

// anthThinkingFor builds the thinking block for a call, and returns the
// max_tokens that must accompany it.
//
// max_tokens has to EXCEED the budget: the two share one output allowance, and a
// request whose ceiling is at or below its thinking budget is rejected outright.
// Raising it here rather than asking every caller to remember is the difference
// between a feature that works and one that 400s the first time somebody sets a
// budget near the default ceiling.
//
// Returns nil when thinking is off or unasked-for, which keeps every existing
// call byte-identical — this changes nothing until a route or an agent asks for
// thinking, so no bill moves by surprise.
func anthThinkingFor(cfg ChatConfig, maxTokens int) (*anthThinking, *anthOutputConfig, int) {
	if cfg.Think == nil || !*cfg.Think {
		return nil, nil, maxTokens
	}
	budget := anthDefaultThinkBudget
	if cfg.ThinkBudget != nil && *cfg.ThinkBudget > 0 {
		budget = *cfg.ThinkBudget
	}
	if thinkingStyleFor(cfg.Model) == anthThinkAdaptive {
		// The model decides its own depth here, so max_tokens needs no headroom
		// carved out of it — there is no budget competing for the allowance.
		return &anthThinking{Type: anthThinkAdaptive},
			&anthOutputConfig{Effort: budgetAsEffort(budget)}, maxTokens
	}
	if maxTokens <= budget {
		// Headroom for an actual answer on top of the reasoning. Without it a
		// turn could spend its entire allowance thinking and return nothing,
		// which reads as the model having failed.
		maxTokens = budget + anthThinkAnswerHeadroom
	}
	return &anthThinking{Type: anthThinkBudgeted, BudgetTokens: budget}, nil, maxTokens
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
	PartialJSON  string          `json:"partial_json,omitempty"` // streamed tool args; NOT Text — see anthStreamState.feed
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
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage,omitempty"`
}

type anthError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// snoopRequest logs the outbound HTTP request details via Trace.
func (c *anthropicClient) snoopRequest(body []byte, stream bool) {
	if !TraceEnabled() {
		return // body reformatting below is pure waste when nothing reads it
	}
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
	if !TraceEnabled() {
		return
	}
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
	path := c.pathPrefix + "/v1/messages"
	Debug("[anthropic]: Sending request to %s%s (stream=%v)", c.api.Server, path, stream)

	req, err := c.api.NewRequestWithContext(ctx, "POST", path)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The extended cache TTL is behind a beta on the direct API. Declared
	// only when it is actually in use, so a deployment on the default
	// lifetime never sends a header naming a feature it is not asking for.
	if PromptCache1hOn() {
		req.Header.Set("anthropic-beta", extendedCacheTTL)
	}
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
			args, err := decodeToolInput(block.Input)
			if err != nil {
				// Loud, because the tool is about to be called with nothing and
				// will answer with its usage spec — which reads like success.
				Warn("[anthropic]: tool %q: %v", block.Name, err)
			}
			// The raw input, whenever the decode produced nothing. An empty
			// argument map is indistinguishable downstream from a model that
			// meant to send none, and it is the shape that turns a grouped
			// tool into a usage-spec loop — so record what actually arrived.
			if len(args) == 0 {
				Debug("[anthropic]: tool %q called with NO arguments; raw input was: %s",
					block.Name, truncateRunes(string(block.Input), 300))
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   block.ID,
				Name: block.Name,
				Args: args,
			})
		}
	}
	return &Response{
		Content:          text.String(),
		ToolCalls:        toolCalls,
		Model:            result.Model,
		InputTokens:      result.Usage.InputTokens,
		CacheReadTokens:  result.Usage.CacheReadInputTokens,
		CacheWriteTokens: result.Usage.CacheCreationInputTokens,
		OutputTokens:     result.Usage.OutputTokens,
		StopReason:       result.StopReason,
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

	if c.hoistSystem {
		var lead string
		if lead, messages = hoistSystemMessages(messages); lead != "" {
			systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + lead)
		}
	}
	msgs, err := buildAnthMessages(messages)
	if err != nil {
		return nil, err
	}
	addCacheBreakpoint(msgs)

	thinking, outCfg, maxTokens := anthThinkingFor(cfg, cfg.MaxTokens)
	payload := anthRequest{
		Model:        cfg.Model,
		Messages:     msgs,
		MaxTokens:    maxTokens,
		Thinking:     thinking,
		OutputConfig: outCfg,
		System:       buildSystemBlocks(systemPrompt),
		Tools:        buildAnthTools(cfg.Tools),
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
		return nil, noteIfAdaptiveThinking(c.model, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: "anthropic"})
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

// decodeToolInput turns a tool_use block's input into arguments.
//
// It exists because the failure it replaces was silent and expensive. The old
// code was `if json.Unmarshal(...) == nil { args = ... }` — an unparseable
// input left an EMPTY map and said nothing, so the model's carefully
// constructed call arrived at the tool with no arguments at all. For a grouped
// tool that means no "action", which answers with its usage spec; the model
// reads a long successful-looking result, learns nothing, and calls again.
// Observed as an entire turn of archetype({}) / collections({}) / agents({})
// returning help text on repeat.
//
// Two shapes are accepted. The object form is what the API documents. The
// string form — input carrying JSON as a string — turns up in provider
// variations, and unwrapping it costs one type switch versus losing every
// argument. Anything else is an ERROR the caller must surface rather than
// silently degrade into a bare call.
func decodeToolInput(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		return parseToolArgs(obj), nil
	}
	// String-encoded JSON: unwrap once and retry.
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if strings.TrimSpace(asString) == "" {
			return map[string]any{}, nil
		}
		if err := json.Unmarshal([]byte(asString), &obj); err == nil {
			return parseToolArgs(obj), nil
		}
	}
	return map[string]any{}, fmt.Errorf("tool arguments could not be decoded: %s", truncateRunes(string(raw), 200))
}

// anthStreamState accumulates a streamed Anthropic response event by event.
//
// Extracted from ChatStream so a second transport can reuse it: Bedrock's
// InvokeModel streaming endpoint delivers the SAME event JSON, just wrapped in
// AWS's binary event-stream framing instead of SSE (see
// llm_bedrock_eventstream.go). Duplicating this switch was the alternative,
// and the two copies would have drifted the first time a block type changed.
type anthStreamState struct {
	handler StreamHandler

	textContent  strings.Builder
	model        string
	inputTokens  int
	outputTokens int
	// The cached halves of the prompt. Without these, a turn whose system
	// prompt and history are a cache hit reports a two-token prompt.
	cacheRead  int
	cacheWrite int
	stopReason string
	toolCalls  []ToolCall

	// blocks tracks in-flight content blocks by index, for tool_use assembly
	// (a tool call's arguments arrive as partial JSON across many deltas).
	blocks []anthBlockState
}

type anthBlockState struct {
	blockType string
	id        string
	name      string
	inputBuf  strings.Builder
}

// feed applies one event's JSON. Unparseable events are skipped rather than
// failing the stream: a single malformed frame should not discard a response
// that is otherwise arriving fine.
func (a *anthStreamState) feed(data []byte) {
	var event anthStreamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return
	}

	switch event.Type {
	case "message_start":
		if event.Message != nil {
			a.model = event.Message.Model
			a.inputTokens = event.Message.Usage.InputTokens
			a.cacheRead = event.Message.Usage.CacheReadInputTokens
			a.cacheWrite = event.Message.Usage.CacheCreationInputTokens
		}
	case "content_block_start":
		if event.ContentBlock == nil {
			Debug("[anthropic]: content_block_start at index %d carried no content_block — any tool input for this block will be dropped", event.Index)
		}
		if event.ContentBlock != nil {
			if event.ContentBlock.Type != "text" && event.ContentBlock.Type != "tool_use" {
				Debug("[anthropic]: content_block_start index=%d type=%q (not text/tool_use) — deltas for it are ignored",
					event.Index, event.ContentBlock.Type)
			}
			bs := anthBlockState{blockType: event.ContentBlock.Type}
			if event.ContentBlock.Type == "tool_use" {
				bs.id = event.ContentBlock.ID
				bs.name = event.ContentBlock.Name
			}
			for len(a.blocks) <= event.Index {
				a.blocks = append(a.blocks, anthBlockState{})
			}
			a.blocks[event.Index] = bs
		}
	case "content_block_delta":
		if event.Delta != nil {
			if event.Index < len(a.blocks) {
				bs := &a.blocks[event.Index]
				switch bs.blockType {
				case "text":
					if event.Delta.Text != "" {
						a.textContent.WriteString(event.Delta.Text)
						if a.handler != nil {
							a.handler(event.Delta.Text)
						}
					}
				case "tool_use":
					if event.Delta.Type == "input_json_delta" {
						// A streamed tool call's arguments arrive as fragments
						// across many input_json_delta events, in partial_json
						// — a SEPARATE wire field from text. This read text,
						// which is always empty on those events, so every
						// streamed tool call arrived correctly named with NO
						// arguments and every tool answered "required
						// parameter missing". That reads as the model failing
						// to send arguments rather than the client failing to
						// read them, and it cost a long evening.
						//
						// Text is kept as a fallback only because a provider
						// that put the fragment there would otherwise fail
						// exactly as silently.
						if frag := event.Delta.PartialJSON; frag != "" {
							bs.inputBuf.WriteString(frag)
						} else if event.Delta.Text != "" {
							bs.inputBuf.WriteString(event.Delta.Text)
						}
					}
				}
			} else if event.Delta.Text != "" {
				// Fallback for simple text deltas without content_block_start.
				a.textContent.WriteString(event.Delta.Text)
				if a.handler != nil {
					a.handler(event.Delta.Text)
				}
			}
		}
	case "content_block_stop":
		if event.Index < len(a.blocks) {
			bs := &a.blocks[event.Index]
			if bs.blockType == "tool_use" {
				args, err := decodeToolInput(json.RawMessage(bs.inputBuf.String()))
				if err != nil {
					Warn("[anthropic]: tool %q: %v", bs.name, err)
				}
				if len(args) == 0 {
					// blockType matters: input_json_delta is only accumulated
					// for a block marked "tool_use", so a content_block_start
					// whose shape this misses drops every argument while text
					// keeps working perfectly — indistinguishable downstream
					// from a model that sent nothing.
					Debug("[anthropic]: tool %q called with NO arguments; blockType=%q accumulated=%q",
						bs.name, bs.blockType, truncateRunes(bs.inputBuf.String(), 300))
				}
				a.toolCalls = append(a.toolCalls, ToolCall{ID: bs.id, Name: bs.name, Args: args})
			}
		}
	case "message_delta":
		if event.Usage != nil {
			a.outputTokens = event.Usage.OutputTokens
			// Restated here by some providers; take the larger so a
			// message_start value is never lost to a zero.
			if n := event.Usage.CacheReadInputTokens; n > a.cacheRead {
				a.cacheRead = n
			}
			if n := event.Usage.CacheCreationInputTokens; n > a.cacheWrite {
				a.cacheWrite = n
			}
		}
		// Terminal stop reason is delivered here on the stream. Without
		// capturing it, a refusal or a max_tokens truncation is invisible
		// (it arrives as an assistant turn with no tool calls).
		if event.Delta != nil && event.Delta.StopReason != "" {
			a.stopReason = event.Delta.StopReason
		}
	}
}

// anthStopInterrupted is the stop reason stamped on a response whose stream
// ended before the provider said why. It is not a wire value: Anthropic's
// terminal stop_reason rides on message_delta, and a stream that closes
// without one — a clean EOF from a dropped connection, a frame cut in half,
// an exception event mid-answer — delivered a fragment, not a finish. The
// agent loop reads it exactly as it reads max_tokens: settle what showed,
// then ask the model to continue.
const anthStopInterrupted = "interrupted"

// finish closes out a stream. cause is what ended it: nil for EOF, else the
// read or decode error.
//
// Before this, both transports returned whatever had accumulated as a
// finished response the moment the body ended — no stop reason, no error —
// so the loop finalized the fragment as the answer and the user saw a reply
// that stopped mid-sentence with nothing to say why. A stream is complete
// only when the terminal stop_reason arrived; anything short of that with
// content in hand is returned marked interrupted, and with nothing in hand
// it is the error it always should have been.
func (a *anthStreamState) finish(tag string, cause error) (*Response, error) {
	if a.stopReason != "" {
		return a.response(tag), nil
	}
	if a.textContent.Len() == 0 && len(a.toolCalls) == 0 {
		if cause == nil {
			cause = errors.New("stream ended before any content arrived")
		}
		return nil, fmt.Errorf("stream read error: %w", cause)
	}
	why := "connection closed"
	if cause != nil {
		why = cause.Error()
	}
	Warn("[%s]: stream ended before the terminal event (%s) — returning %d chars as an interrupted reply",
		tag, why, a.textContent.Len())
	a.stopReason = anthStopInterrupted
	return a.response(tag), nil
}

// response materializes the accumulated state, emitting the same trace lines
// both transports relied on.
func (a *anthStreamState) response(tag string) *Response {
	// Prompt size is the SUM. Logging input_tokens alone is what made a
	// cache-hit turn read as a two-token prompt.
	Debug("[%s]: Stream complete: model=%s prompt=%d (uncached=%d cache_read=%d cache_write=%d) output_tokens=%d tool_calls=%d",
		tag, a.model, a.inputTokens+a.cacheRead+a.cacheWrite, a.inputTokens, a.cacheRead, a.cacheWrite,
		a.outputTokens, len(a.toolCalls))
	Trace("<-- STREAM COMPLETE: model=%s prompt=%d (uncached=%d cache_read=%d cache_write=%d) output_tokens=%d",
		a.model, a.inputTokens+a.cacheRead+a.cacheWrite, a.inputTokens, a.cacheRead, a.cacheWrite, a.outputTokens)
	if a.textContent.Len() > 0 {
		Trace("<-- RESPONSE TEXT:\n%s", a.textContent.String())
	}
	for _, tc := range a.toolCalls {
		argsJSON, _ := json.Marshal(tc.Args)
		Trace("<-- TOOL CALL: id=%s name=%s args=%s", tc.ID, tc.Name, string(argsJSON))
	}
	warnStopReason(a.stopReason)
	return &Response{
		Content:          a.textContent.String(),
		ToolCalls:        a.toolCalls,
		Model:            a.model,
		InputTokens:      a.inputTokens,
		CacheReadTokens:  a.cacheRead,
		CacheWriteTokens: a.cacheWrite,
		OutputTokens:     a.outputTokens,
		StopReason:       a.stopReason,
	}
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

	if c.hoistSystem {
		var lead string
		if lead, messages = hoistSystemMessages(messages); lead != "" {
			systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + lead)
		}
	}
	msgs, err := buildAnthMessages(messages)
	if err != nil {
		return nil, err
	}
	addCacheBreakpoint(msgs)

	thinking, outCfg, maxTokens := anthThinkingFor(cfg, cfg.MaxTokens)
	payload := anthRequest{
		Model:        cfg.Model,
		Messages:     msgs,
		MaxTokens:    maxTokens,
		Thinking:     thinking,
		OutputConfig: outCfg,
		System:       buildSystemBlocks(systemPrompt),
		Stream:       true,
		Tools:        buildAnthTools(cfg.Tools),
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
		return nil, noteIfAdaptiveThinking(c.model, &APIError{StatusCode: resp.StatusCode, Message: msg, Provider: "anthropic"})
	}

	st := &anthStreamState{handler: handler}

	scanner := bufio.NewScanner(resp.Body)
	// Default Scanner caps a line at 64KB; a single large SSE `data:` line
	// (a big content block) would trip bufio.ErrTooLong and abort the stream.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		st.feed([]byte(strings.TrimPrefix(line, "data: ")))
	}

	return st.finish("anthropic", scanner.Err())
}

// noteIfAdaptiveThinking records the model's own answer about which thinking
// shape it takes, then returns the error unchanged.
//
// Recorded HERE, by the client, because only the client knows the model id it
// actually sent. The configured string and the sent string are not always the
// same — bedrockModelID prefixes a bare name and substitutes a default for an
// empty one — so a cache keyed from the caller's side would miss on exactly the
// deployments that need it, and the retry would rebuild an identical request
// forever. That is the second time this fallback was defeated by keying on the
// wrong copy of the model name; doing it where the name is unambiguous is what
// stops there being a third.
func noteIfAdaptiveThinking(model string, err error) error {
	if isUnsupportedThinkingTypeErr(err) {
		noteAdaptiveThinking(model)
	}
	return err
}
