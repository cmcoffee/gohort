package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
)

// APIError represents an HTTP error from an LLM provider.
type APIError struct {
	StatusCode int
	Message    string
	Provider   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s api error (%d): %s", e.Provider, e.StatusCode, e.Message)
}

// LLM defines a generic interface for interacting with large language models.
type LLM interface {
	// Chat sends messages and returns a complete response.
	Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error)
	// ChatStream sends messages and streams the response via a handler callback.
	ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error)
}

// Pinger is an optional LLM-side liveness probe. Implementations should
// return promptly (a few seconds) whether or not the backend is currently
// handling a request, so a caller can distinguish "server unreachable"
// from "server alive but queued behind a long-running call" without
// having to wait out a full chat timeout.
type Pinger interface {
	Ping(ctx context.Context) error
}

// ContextSizer is an optional interface for LLMs that expose their
// configured context window size. Used by the debate pipeline to
// decide whether to share all evidence (large context) or use
// per-side research (small context).
type ContextSizer interface {
	ContextSize() int
}

// StreamHandler is called for each chunk of streamed content.
type StreamHandler func(chunk string)

// Message represents a single message in a conversation.
type Message struct {
	Role        string       `json:"role"`
	Content     string       `json:"content"`
	Images      [][]byte     `json:"-"` // Base64-decoded image data for vision models.
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
}

// Response represents the result of an LLM call.
type Response struct {
	Content      string
	Reasoning    string // Thinking model reasoning (populated but not promoted to Content)
	ToolCalls    []ToolCall
	Model        string
	InputTokens  int
	OutputTokens int
	// Tier reports which LLM tier actually served this response.
	// Populated by WorkerChat (always WORKER), LeadChat (LEAD on
	// native success, WORKER when the routing config or fallback
	// paths delegate to the primary). Session.recordTokens keys off
	// Tier so per-session cost attribution reflects what was
	// *served*, not what was *asked for* — important because routing-
	// to-worker or fallback-to-worker means the call is priced at
	// worker rates, not lead rates. Zero value (TierUnset) means an
	// older code path didn't set it; callers fall back to their own
	// tier context (e.g., Session.Tier).
	Tier LLMTier
}

// Tool describes a function the LLM can call.
type Tool struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Parameters  map[string]ToolParam  `json:"parameters,omitempty"`
	Required    []string              `json:"required,omitempty"`
}

// ToolParam describes a single parameter of a tool.
// For simple tools only Type and Description are needed; the additional
// fields are opt-in for richer schemas (enums, arrays, nested objects).
type ToolParam struct {
	Type        string               `json:"type"`
	Description string               `json:"description"`
	Enum        []string             `json:"enum,omitempty"`       // Allowed values (for string params).
	Items       *ToolParam           `json:"items,omitempty"`      // Element schema (when Type is "array").
	Properties  map[string]ToolParam `json:"properties,omitempty"` // Nested params (when Type is "object").
	Required    []string             `json:"required,omitempty"`   // Required nested params (when Type is "object").
}

// buildParamSchema converts a ToolParam into a JSON Schema map suitable for
// LLM provider APIs. Simple params produce {"type":"string","description":"..."},
// while richer params include enum, items, and nested properties.
func buildParamSchema(p ToolParam) map[string]interface{} {
	schema := map[string]interface{}{
		"type":        p.Type,
		"description": p.Description,
	}
	if len(p.Enum) > 0 {
		schema["enum"] = p.Enum
	}
	if p.Items != nil {
		schema["items"] = buildParamSchema(*p.Items)
	}
	if len(p.Properties) > 0 {
		props := make(map[string]interface{})
		for name, sub := range p.Properties {
			props[name] = buildParamSchema(sub)
		}
		schema["properties"] = props
	}
	if len(p.Required) > 0 {
		schema["required"] = p.Required
	}
	return schema
}

// ToolCall represents the LLM's request to invoke a tool.
type ToolCall struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Args  map[string]any `json:"args"`
}

// parseToolArgs converts a raw JSON map into a tool argument map.
// Values are preserved in their native types (string, float64, bool,
// []any, map[string]any). Schema echo patterns from the LLM are
// unwrapped to extract the intended value.
func parseToolArgs(raw map[string]interface{}) map[string]any {
	args := make(map[string]any)
	for k, v := range raw {
		args[k] = cleanArg(v)
	}
	return args
}

// cleanArg unwraps schema echoes but preserves native types.
func cleanArg(v interface{}) any {
	switch val := v.(type) {
	case map[string]interface{}:
		return unwrapSchemaEchoAny(val)
	default:
		return v
	}
}

// StringArg extracts a string argument by key, converting non-string
// types to their string representation. Returns "" if the key is missing.
func StringArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	return stringify(v)
}

// IntArg extracts an integer argument by key. Returns 0 if the key is
// missing or not a number.
func IntArg(args map[string]any, key string) int {
	v, ok := args[key]
	if !ok || v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case string:
		var n int
		fmt.Sscanf(val, "%d", &n)
		return n
	default:
		return 0
	}
}

// BoolArg extracts a boolean argument by key. Returns false if the key
// is missing. Accepts bool, "true"/"false" strings, and numeric 0/1.
func BoolArg(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return val == "true" || val == "1"
	case float64:
		return val != 0
	default:
		return false
	}
}

// SliceArg extracts a slice argument by key. Returns nil if the key is
// missing or not a slice.
func SliceArg(args map[string]any, key string) []any {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	if s, ok := v.([]any); ok {
		return s
	}
	if s, ok := v.([]interface{}); ok {
		return s
	}
	return nil
}

// schemaKeys are the keys that appear in a JSON Schema property definition.
// When an LLM echoes the schema back as the argument value, these keys will
// be present in the nested object.
var schemaKeys = map[string]bool{
	"type": true, "description": true, "enum": true,
	"default": true, "items": true, "properties": true,
}

// unwrapSchemaEchoAny extracts the actual value from a nested object that
// looks like the LLM echoed the parameter schema back. Returns the native
// value type when possible.
func unwrapSchemaEchoAny(val map[string]interface{}) any {
	// If there's an explicit "value" key, prefer that.
	if inner, ok := val["value"]; ok {
		return inner
	}

	// Check if this looks like a schema echo: has "type" plus only other schema keys.
	_, hasType := val["type"]
	if hasType && len(val) == 2 {
		for key, v := range val {
			if key != "type" {
				return v
			}
		}
	}

	// Not a recognized echo pattern; return as-is (map).
	return val
}

// unwrapSchemaEcho extracts the actual value as a string (legacy helper for stringify).
func unwrapSchemaEcho(val map[string]interface{}) string {
	return stringify(unwrapSchemaEchoAny(val))
}

// stringify converts an interface value to a clean string for use as a tool argument.
// It handles the various ways LLMs return values: plain strings, nested schema
// echoes, single-element arrays, and other JSON types.
func stringify(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case map[string]interface{}:
		return unwrapSchemaEcho(val)
	case []interface{}:
		j, _ := json.Marshal(val)
		return string(j)
	case float64:
		// JSON numbers are float64; render without trailing zeros.
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case nil:
		return ""
	default:
		j, _ := json.Marshal(v)
		return string(j)
	}
}

// ToolResult carries the output of a tool call back to the LLM.
type ToolResult struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// ChatConfig holds configuration for a single LLM call.
type ChatConfig struct {
	Model        string
	MaxTokens    int
	Temperature  *float64
	SystemPrompt string
	Tools        []Tool
	JSONMode     bool
	MaxRetries   *int
	Think        *bool // Enable/disable thinking for thinking models (nil = model default)
	RouteKey     string // Routing stage key; LeadChat may downgrade to worker based on config.
	Caller       string // Identifier of the app/pipeline making the call; used by the Ollama fair-queueing scheduler. Empty → "unknown".
}

// ChatOption is a functional option for configuring an LLM call.
type ChatOption func(*ChatConfig)

// WithModel overrides the default model for this call.
func WithModel(model string) ChatOption {
	return func(c *ChatConfig) { c.Model = model }
}

// WithMaxTokens sets the maximum number of tokens to generate.
func WithMaxTokens(n int) ChatOption {
	return func(c *ChatConfig) { c.MaxTokens = n }
}

// WithTemperature sets the sampling temperature.
func WithTemperature(t float64) ChatOption {
	return func(c *ChatConfig) { c.Temperature = &t }
}

// WithSystemPrompt sets the system prompt for this call.
func WithSystemPrompt(prompt string) ChatOption {
	return func(c *ChatConfig) { c.SystemPrompt = prompt }
}

// WithTools provides tool definitions for the LLM to use.
func WithTools(tools []Tool) ChatOption {
	return func(c *ChatConfig) { c.Tools = tools }
}

// WithJSONMode requests JSON output from the LLM.
func WithJSONMode() ChatOption {
	return func(c *ChatConfig) { c.JSONMode = true }
}

// WithMaxRetries overrides the default retry count for this call. 0 disables retries.
func WithMaxRetries(n int) ChatOption {
	return func(c *ChatConfig) { c.MaxRetries = &n }
}

// WithThink enables or disables thinking mode for thinking models (qwen3, etc.).
// When set to false, the model skips reasoning and responds directly.
func WithThink(enabled bool) ChatOption {
	return func(c *ChatConfig) { c.Think = &enabled }
}

// WithRouteKey tags a LeadChat call with a routing stage key. If the stage
// is configured for "worker" in the routing menu, LeadChat transparently
// delegates to WorkerChat with the same options. Unknown/unset keys default
// to lead, so it's safe to add WithRouteKey before registering the stage.
func WithRouteKey(key string) ChatOption {
	return func(c *ChatConfig) { c.RouteKey = key }
}

// WithCaller identifies the app or pipeline stage making this LLM call.
// Used by the Ollama fair-queueing scheduler to enforce per-caller
// round-robin dispatch when multiple apps compete for a single local
// model. If unset, the caller defaults to the agent's Name() at the
// WorkerChat/LeadChat layer, falling back to "unknown".
func WithCaller(id string) ChatOption {
	return func(c *ChatConfig) { c.Caller = id }
}

// applyOpts applies functional options to a ChatConfig with defaults.
// Automatically prepends today's date to any system prompt so the LLM
// always knows the current date without each caller having to include it.
func applyOpts(defaultModel string, defaultMaxTokens int, opts []ChatOption) ChatConfig {
	cfg := ChatConfig{
		Model:     defaultModel,
		MaxTokens: defaultMaxTokens,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.SystemPrompt != "" {
		cfg.SystemPrompt = "Today's date is " + time.Now().Format("January 2, 2006") + ". " + cfg.SystemPrompt
	}
	return cfg
}

// LLMProviderConfig holds stored configuration for an LLM provider.
type LLMProviderConfig struct {
	Provider        string
	Model           string
	APIKey          string
	Endpoint        string
	ContextSize     int           // Context window size (Ollama only); 0 uses default.
	ConnectTimeout  time.Duration // Dial timeout; defaults to 10s if zero.
	RequestTimeout  time.Duration // Response header timeout; defaults to 120s if zero.
	DisableThinking bool          // Ollama only: master override forcing think=false on every call regardless of per-call WithThink(true). Escape hatch for thinking hangs on local models.
	NativeTools     bool          // When true, use native function calling. When false, tools are described in the system prompt and parsed from <tool_call> tags. Default false for ollama models without tool support.
	OllamaMaxParallel int         // Ollama only: global concurrency cap. 0 or negative = scheduler disabled; 1 = strict serial (default). Requests are fair-queued across sessions.
}

// newLLMAPIClient builds an apiclient.APIClient configured for LLM provider
// communication. It sets connection-level timeouts and leaves the client-level
// Timeout at 0 so that context handles overall deadlines, avoiding killing
// long-running streams.
func newLLMAPIClient(cfg LLMProviderConfig) *apiclient.APIClient {
	connectTimeout := cfg.ConnectTimeout
	if connectTimeout == 0 {
		connectTimeout = 10 * time.Second
	}
	requestTimeout := cfg.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = 2 * time.Minute
	}
	return &apiclient.APIClient{
		ConnectTimeout: connectTimeout,
		RequestTimeout: requestTimeout,
		VerifySSL:      true,
	}
}

// retryLLM wraps an LLM with retry-on-transient-error logic.
type retryLLM struct {
	inner      LLM
	maxRetries int
}

// isTransientError returns true if the error is worth retrying.
func isTransientError(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		switch apiErr.StatusCode {
		case 429, 500, 502, 503, 529:
			return true
		}
		return false
	}
	// Context deadline exceeded means the request hit its timeout.
	// Retrying with the same timeout will just timeout again (e.g.
	// Ollama cold-loading a model). Don't retry.
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return true
	}
	return false
}

func (r *retryLLM) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	return doWithRetry(ctx, r.maxRetries, opts, func() (*Response, error) {
		return r.inner.Chat(ctx, messages, opts...)
	})
}

func (r *retryLLM) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	var handlerCalled bool
	wrappedHandler := func(chunk string) {
		handlerCalled = true
		handler(chunk)
	}
	return doWithRetry(ctx, r.maxRetries, opts, func() (*Response, error) {
		handlerCalled = false
		resp, err := r.inner.ChatStream(ctx, messages, wrappedHandler, opts...)
		if err != nil && handlerCalled {
			// Chunks were already delivered to the caller; do not retry.
			return resp, &nonRetryableError{err}
		}
		return resp, err
	})
}

// nonRetryableError wraps an error to signal that retry should not be attempted.
type nonRetryableError struct{ error }

func doWithRetry(ctx context.Context, maxRetries int, opts []ChatOption, fn func() (*Response, error)) (*Response, error) {
	// Allow per-call override via WithMaxRetries.
	cfg := ChatConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.MaxRetries != nil {
		maxRetries = *cfg.MaxRetries
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, err := fn()
		if err == nil {
			return resp, nil
		}
		if _, ok := err.(*nonRetryableError); ok {
			return resp, err.(*nonRetryableError).error
		}
		if !isTransientError(err) {
			return resp, err
		}
		lastErr = err
		if attempt < maxRetries {
			backoff := time.Duration(math.Pow(float64(attempt+1), 2)) * time.Second
			Debug("[retry]: attempt %d/%d failed: %v, backing off %v", attempt+1, maxRetries, err, backoff)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return nil, lastErr
}

// NewLLMFromConfig creates an LLM client from a stored configuration.
func NewLLMFromConfig(cfg LLMProviderConfig) (LLM, error) {
	api := newLLMAPIClient(cfg)
	var inner LLM

	switch cfg.Provider {
	case "anthropic":
		if cfg.APIKey == "" {
			return nil, Error("anthropic API key is not configured, run --setup")
		}
		model := cfg.Model
		if model == "" {
			model = "claude-sonnet-4-6-20250514"
		}
		inner = newAnthropicLLM(cfg.APIKey, model, api)
	case "openai":
		if cfg.APIKey == "" {
			return nil, Error("openai API key is not configured, run --setup")
		}
		model := cfg.Model
		if model == "" {
			model = "gpt-4o"
		}
		inner = newOpenAILLM(cfg.APIKey, model, openAIEndpoint, api)
	case "gemini":
		if cfg.APIKey == "" {
			return nil, Error("gemini API key is not configured, run --setup")
		}
		model := cfg.Model
		if model == "" {
			model = "gemini-2.5-flash"
		}
		inner = newGeminiLLM(cfg.APIKey, model, api)
	case "ollama":
		model := cfg.Model
		if model == "" {
			model = "llama3"
		}
		ep := ollamaEndpoint
		if cfg.Endpoint != "" {
			ep = cfg.Endpoint
		}
		client := newOpenAILLM("", model, ep, api)
		oc := client.(*openAIClient)
		oc.ollama = true
		oc.contextSize = cfg.ContextSize
		oc.disableThinking = cfg.DisableThinking
		oc.nativeTools = cfg.NativeTools
		inner = client
		// Start (or adjust) the global Ollama scheduler so concurrent
		// sessions get fair-queued. Safe to call multiple times; the
		// second call adjusts MaxParallel on the running dispatcher.
		maxParallel := cfg.OllamaMaxParallel
		if maxParallel < 1 {
			maxParallel = 1
		}
		StartOllamaScheduler(maxParallel)
	default:
		return nil, Error("unknown LLM provider, run --setup to configure")
	}

	return &retryLLM{inner: inner, maxRetries: 3}, nil
}
