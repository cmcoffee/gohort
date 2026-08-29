package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/textutil"
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
	Reasoning   string       `json:"reasoning,omitempty"` // Thinking content from the prior turn; forwarded to Ollama when preserve_thinking is on.
	Images      [][]byte     `json:"-"`                   // Decoded image data for vision models.
	Videos      [][]byte     `json:"-"`                   // Raw video bytes; buildMessages auto-extracts metadata + N frames into Images at send time.
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
	// CacheReadTokens / CacheWriteTokens are the rest of the prompt when prompt
	// caching is in play, which for Anthropic-family models it is — the client
	// marks the system block and the history tail with cache_control.
	//
	// InputTokens then counts ONLY the uncached remainder, which is the API's
	// definition and is easy to read as the whole prompt. It is not: on a
	// conversation whose system prompt and history are a cache hit, the
	// uncached remainder is a handful of tokens. An "input_tokens=2" against a
	// prompt of thousands is not a bug, it is a near-total cache hit reported
	// faithfully — and reporting only that number makes an expensive turn look
	// free.
	//
	// The prompt's real size is InputTokens + CacheReadTokens +
	// CacheWriteTokens. They are kept apart rather than summed because they
	// bill at different rates (a read is far cheaper than an ordinary input
	// token, a write slightly dearer), so cost and size are different sums over
	// the same three numbers.
	CacheReadTokens  int
	CacheWriteTokens int
	// StopReason is the provider's terminal signal for the turn (Anthropic:
	// end_turn / tool_use / max_tokens / refusal / pause_turn). Populated by
	// the Anthropic client; other backends may leave it empty. Lets callers
	// distinguish a clean finish from a truncation or a refusal instead of
	// inferring "done" purely from an empty ToolCalls slice.
	StopReason string
	// ReasoningTokens is the portion of OutputTokens spent on the
	// thinking channel, as reported by the model when supported (e.g.
	// llama.cpp's usage.completion_tokens_details.reasoning_tokens
	// for Qwen3 thinking, or OpenAI's o1-style breakdown). Zero when
	// the backend doesn't report it; callers can fall back to a
	// char-ratio estimate from len(Reasoning) / len(Reasoning+Content).
	ReasoningTokens int
	// Server-reported pure-throughput numbers. Populated by llama.cpp;
	// other backends leave these zero. PredictedPerSecond is decode-
	// only tokens/sec (excludes prefill), matching what llama.cpp's
	// own web UI displays. PromptPerSecond is prefill throughput.
	PredictedPerSecond float64
	PromptPerSecond    float64
	// PromptTokensPrefilled is how many of the prompt's tokens the server
	// actually had to process this call (llama.cpp's timings.prompt_n);
	// PrefillMS is the wall-time it spent doing it. InputTokens minus
	// PromptTokensPrefilled is what the KV cache supplied for free, which
	// is the only direct read on whether the prefix is being REUSED. It is
	// the number that settles "is the prompt stable across turns" — a
	// question that has twice cost days of guessing because the answer was
	// parsed off the wire and then discarded. Zero on backends that do not
	// report it (everything but llama.cpp today).
	PromptTokensPrefilled int
	PrefillMS             float64
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

	// HitRoundCap is set by RunAgentLoop when the loop terminated because
	// it exhausted its round budget (MaxRounds + grace) rather than the
	// model producing a natural final answer. Lets a caller distinguish
	// "I'm done" from "I ran out of room" and react — e.g. grant another
	// budget to continue a still-unfinished investigation. Zero value on
	// every non-agent-loop Response and on natural completions.
	HitRoundCap bool

	// usageCounted marks that this response's tokens have already reached the
	// usage trackers, so the two layers that record them can both run without
	// counting the call twice.
	//
	// There are two, deliberately. The reloadable LLM handle records every call
	// that passes through it, which is every call in the shipped framework
	// INCLUDING the ones that go straight to LLM.Chat without a WorkerChat /
	// LeadChat / ChatStreamWithReport wrapper around them — the judges, the
	// suggesters, the compactor. The AppCore wrappers still record too, because
	// an AppCore built around a raw LLM (the SDK's NewAgent, a test's fake)
	// never touches a reloadable handle and would otherwise report nothing.
	// Whichever sees the response first claims it; the other skips.
	//
	// Unexported because it is bookkeeping about the response rather than part
	// of it — no caller should be able to set it, and no serialization of a
	// Response should carry it.
	usageCounted bool
}

// Capability describes the kind of side effect a tool can have. Apps use
// these tiers to gate which tools the LLM is even allowed to see — e.g. a
// chat agent might be permitted to read and reach the network but not
// execute shell commands or write files. Tools self-declare their caps;
// AgentLoopConfig.AllowedCaps gates the set the LLM is offered.
type Capability string

const (
	CapRead    Capability = "read"    // pure read: queries, lookups, in-memory transforms — no side effects
	CapNetwork Capability = "network" // outbound network: web search, API fetches, external calls
	CapWrite   Capability = "write"   // local writes: create/modify files, persist DB records
	CapExecute Capability = "execute" // shell commands, code execution, system control
)

// Tool describes a function the LLM can call.
type Tool struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Parameters  map[string]ToolParam `json:"parameters,omitempty"`
	Required    []string             `json:"required,omitempty"`

	// Caps lists which capability tiers this tool exercises. Empty means
	// "unannotated" — treated as legacy/unrestricted by AllowedCaps filtering
	// for backward compatibility. Tools should self-declare honestly: the
	// system trusts the declaration, it doesn't introspect the handler.
	Caps []Capability `json:"-"`

	// Prompt is an optional system-prompt fragment that gets appended
	// when this tool is loaded into an agent. Most tools (web_search,
	// fetch_url, calculate, …) don't need one — name + description in
	// the catalog is enough. Use this for tools with non-obvious call
	// cadence, multi-step pipelines exposed as a single tool, or
	// admin/LLM-authored temp-tools that need usage rules baked in.
	// Empty = no fragment appended (zero overhead).
	Prompt string `json:"-"`

	// RenderLate marks a lazy tool to be rendered at the BOTTOM of the
	// prompt (via chat_template_kwargs.lazy_tool_names + the split chat
	// template) instead of the top-of-prompt tools block. Used for lazy
	// custom tools loaded mid-session: keeps them callable without
	// invalidating the top-of-prompt KV cache (the load_tool cold-prefill).
	// Not serialized — it drives the kwargs name list, and llama.cpp strips
	// unknown fields off the tool object anyway.
	RenderLate bool `json:"-"`

	// Category is a human-chosen grouping label the tool CLAIMS for itself —
	// the organizing unit for the tool-picker's section headers (and the user's
	// "Tools" list). It rides on the tool like Caps do: self-declared, and
	// because it lives ON the tool it inherits the tool's ownership for free
	// (a user categorizing their own tool touches only their record — no
	// separate per-user group store). Empty falls back to the legacy
	// ToolGroup.Members mapping, then to the capability label. The matching
	// ToolGroup (by Name) supplies the LLM-facing description. Not serialized
	// to the model — purely a presentation/organization concern.
	Category string `json:"-"`

	// TrustedOutput marks a tool whose result is framework-generated control
	// or authoring text (e.g. tool_def / add_tool confirmations), NOT raw
	// content fetched from outside. Such a tool may still declare CapNetwork
	// for one sub-capability — tool_def's "test" action makes real calls, so
	// the grouped tool's union Caps() include network — without its everyday
	// output being untrusted. When set, the untrusted-content fence is
	// suppressed. Tools whose PURPOSE is fetching external content (fetch_url,
	// browse_page, api/toolbox temp tools) must NOT set this. Not serialized.
	TrustedOutput bool `json:"-"`
}

// RenderToolPromptFragments concatenates the Prompt fields of every
// tool that supplies one, formatted as "## Using <name>\n<prompt>"
// sections. Empty string when no tool ships a fragment so callers can
// append unconditionally. Used by app prompt assembly between the
// gated persona and the framework-emitted tool catalog.
func RenderToolPromptFragments(tools []AgentToolDef) string {
	var parts []string
	for _, td := range tools {
		p := strings.TrimSpace(td.Tool.Prompt)
		if p == "" {
			continue
		}
		parts = append(parts, "## Using "+td.Tool.Name+"\n\n"+p)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n\n"
}

// capsAllowed reports whether every capability a tool declares is in the
// allowed set. An unannotated tool (empty Caps) is allowed unconditionally
// during the migration period; once every tool annotates, callers can
// flip the default to deny-by-empty.
func capsAllowed(toolCaps []Capability, allowed map[Capability]bool) bool {
	if len(toolCaps) == 0 {
		return true // legacy / unannotated — pass through
	}
	for _, c := range toolCaps {
		if !allowed[c] {
			return false
		}
	}
	return true
}

// FilterToolsByCaps returns a new slice containing only the tools whose
// declared Caps fit inside the allowed set. Tools with empty Caps
// (unannotated) pass through unchanged for backward compatibility. When
// allowed is empty/nil the input is returned as-is — same "no restriction"
// semantics as AgentLoopConfig.AllowedCaps.
//
// Callers that build tool lists outside RunAgentLoop (e.g. chat handlers
// that drive ChatStream directly) use this to enforce capability gating
// at the same layer the agent loop does internally.
func FilterToolsByCaps(tools []AgentToolDef, allowed []Capability) []AgentToolDef {
	if len(allowed) == 0 {
		return tools
	}
	allowedSet := make(map[Capability]bool, len(allowed))
	for _, c := range allowed {
		allowedSet[c] = true
	}
	out := make([]AgentToolDef, 0, len(tools))
	for _, td := range tools {
		if capsAllowed(td.Tool.Caps, allowedSet) {
			out = append(out, td)
		}
	}
	return out
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
	// PathScope constrains a string parameter to a path inside a
	// registered root, as "kind:name" (e.g. "files:support_bundles").
	// Checked when the tool RUNS, and the value substituted is the
	// absolute path it resolved to.
	//
	// The late-binding sibling of Enum. An enum is frozen when a tool is
	// authored, which suits "--env production|staging" and cannot
	// express a set that changes — the folders under a drop directory,
	// where new ones appearing without ceremony is the whole point. See
	// core/path_scope.go.
	PathScope string `json:"path_scope,omitempty"`
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
		schema["required"] = sortedCopy(p.Required)
	}
	return schema
}

// sortedCopy returns an alphabetically-sorted copy of s without mutating
// the original. Used wherever a slice goes into an LLM-facing JSON schema:
// json.Marshal preserves slice order (unlike map keys, which it sorts), so
// an unsorted / map-derived "required" list would serialize differently
// across turns and break the worker's prompt-cache prefix (the tools block
// diverges → full re-prefill every turn).
func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// buildToolParamsSchema renders a tool's parameters as a JSON-schema object
// for the wire. This is the SINGLE serialization chokepoint every tool
// passes through (built-in, grouped, temp, toolbox, and anything added
// later), so it is the right place to GUARANTEE a deterministic, byte-stable
// schema: json.Marshal sorts the properties map's keys, and sortedCopy sorts
// required. Doing it here means no upstream tool builder has to remember to
// sort — non-deterministic ordering anywhere upstream is normalized on the
// way out, which is what keeps the prompt cache reusable across turns.
func buildToolParamsSchema(t Tool) json.RawMessage {
	schema := map[string]interface{}{"type": "object"}
	if len(t.Parameters) > 0 {
		props := make(map[string]interface{}, len(t.Parameters))
		for name, p := range t.Parameters {
			props[name] = buildParamSchema(p)
		}
		schema["properties"] = props
	}
	if len(t.Required) > 0 {
		schema["required"] = sortedCopy(t.Required)
	}
	raw, _ := json.Marshal(schema)
	return raw
}

// ToolCall represents the LLM's request to invoke a tool.
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
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
	salvageSwallowedParams(args)
	return args
}

// salvageSwallowedParams repairs a provider-side XML tool-call parsing
// failure observed with llama.cpp + Qwen: when the model glues a
// parameter's closing tag to the value ("...Sound good?</parameter>"
// instead of putting it on its own line), llama.cpp can terminate the
// value at a LATER close tag, so one string argument arrives carrying
// raw markup plus every parameter that followed it — and those
// parameters are missing from the args map entirely:
//
//	question = "...Sound good?</parameter>\n<parameter=options>\n[\"yes\",\"edit\",\"no\"]"
//
// For each string value containing "</parameter>", if everything after
// some occurrence parses as pure tool-call markup (parameter chunks and
// wrapper closers only — ordinary prose never does), the value is cut
// at that occurrence and the swallowed parameters are restored into the
// args map. Existing keys are never overwritten: the provider's parse
// wins wherever it succeeded. A value that merely mentions the tag
// mid-prose is left alone (the tail fails the pure-markup parse).
func salvageSwallowedParams(args map[string]any) {
	const pClose = "</parameter>"
	type patch struct {
		key       string
		val       string
		recovered map[string]any
	}
	var patches []patch
	for k, v := range args {
		s, ok := v.(string)
		if !ok || !strings.Contains(s, pClose) {
			continue
		}
		// Try each occurrence: the first one whose tail is pure markup
		// is the real boundary. Later occurrences matter when the value
		// legitimately mentions the tag before the swallow point.
		for from := 0; ; {
			ci := strings.Index(s[from:], pClose)
			if ci < 0 {
				break
			}
			ci += from
			if recovered, ok := parseSwallowedTail(s[ci+len(pClose):]); ok {
				patches = append(patches, patch{key: k, val: strings.TrimSpace(s[:ci]), recovered: recovered})
				break
			}
			from = ci + len(pClose)
		}
	}
	for _, p := range patches {
		args[p.key] = p.val
		names := make([]string, 0, len(p.recovered))
		for rk, rv := range p.recovered {
			names = append(names, rk)
			if _, exists := args[rk]; !exists {
				args[rk] = rv
			}
		}
		Debug("[llm] salvaged swallowed tool-call markup from arg %q (recovered params: %s)", p.key, strings.Join(names, ", "))
	}
}

// parseSwallowedTail reports whether tail consists solely of tool-call
// markup — zero or more <parameter=KEY>VALUE</parameter> chunks (the
// last may be unclosed if the emission was truncated) followed by
// optional </function> / </tool_call> wrapper closers — and returns the
// parameters it carries. Any prose outside the markup fails the parse:
// that's the guard that keeps salvage away from values which merely
// talk about the format.
func parseSwallowedTail(tail string) (map[string]any, bool) {
	const (
		pPrefix = "<parameter="
		pClose  = "</parameter>"
	)
	body := strings.TrimSpace(tail)
	// Wrapper closers come after the last parameter chunk; peel them
	// off the end (outermost last) so the walk below only sees chunks.
	for _, closer := range []string{"</tool_call>", "</function>"} {
		body = strings.TrimSpace(strings.TrimSuffix(body, closer))
	}
	out := map[string]any{}
	for body != "" {
		if !strings.HasPrefix(body, pPrefix) {
			return nil, false
		}
		body = body[len(pPrefix):]
		gt := strings.IndexByte(body, '>')
		if gt < 0 {
			return nil, false
		}
		name := strings.TrimSpace(body[:gt])
		if name == "" || strings.ContainsAny(name, " \t\n<") {
			return nil, false
		}
		body = body[gt+1:]
		var val string
		if end := strings.Index(body, pClose); end >= 0 {
			val = body[:end]
			body = strings.TrimSpace(body[end+len(pClose):])
		} else {
			// Truncated emission — the stream ended before the close
			// tag. The remainder is this parameter's value.
			val = body
			body = ""
		}
		out[name] = coerceSalvagedValue(strings.TrimSpace(val))
	}
	return out, true
}

// coerceSalvagedValue converts a salvaged parameter's raw text into the
// type the JSON form would have carried: arrays, objects, and quoted
// strings parse as JSON; bare booleans map to bool; everything else
// stays a string (matching what the prompt-tools XML parser stores).
func coerceSalvagedValue(s string) any {
	if s == "" {
		return s
	}
	if c := s[0]; c == '[' || c == '{' || c == '"' {
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			return v
		}
	}
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	return s
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
	inner := unwrapSchemaEchoAny(val)
	// If the result is still a map, JSON-serialize it directly to break the
	// stringify → unwrapSchemaEcho → stringify recursion that occurs when
	// unwrapSchemaEchoAny returns the original map unchanged.
	if _, isMap := inner.(map[string]interface{}); isMap {
		j, _ := json.Marshal(inner)
		return string(j)
	}
	return stringify(inner)
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
	Temperature  *float64 // Per-call temperature; nil = the mode's global default (tune_sampling_temperature_{think,nothink}), then server default.
	TopK         *int     // Per-call top-k cutoff; nil = the mode's global default (tune_sampling_top_k_{think,nothink}), then server default.
	TopP         *float64 // Per-call nucleus-sampling top-p; nil = the mode's global default (tune_sampling_top_p_{think,nothink}), then server default.
	MinP         *float64 // Per-call min-p cutoff; nil = server default (per-call only, no global tunable yet).
	SystemPrompt string
	Tools        []Tool
	JSONMode     bool
	MaxRetries   *int
	Think        *bool  // Enable/disable thinking for thinking models (nil = model default)
	ThinkBudget  *int   // Per-call thinking token budget; overrides global ThinkingBudget when set. 0 = ignored.
	RouteKey     string // Routing stage key; LeadChat may downgrade to worker based on config.
	// TierOverride pins this call's tier regardless of what RouteKey's stage
	// says — the per-call form of AgentLoopConfig.TierOverride, and the only
	// way that pin can reach the STREAMING path, which resolves the tier from
	// the options alone and never sees the loop's config.
	//
	// It cannot escalate past the privacy pin: every reader still requires
	// HasDistinctLead(), which folds in LeadDenied().
	TierOverride LLMTier
	// OnStreamRestart, when set, declares that this stream's partial output
	// can be thrown away — and IS the thing that throws it away.
	//
	// Streaming retries stop dead the moment one chunk has been delivered,
	// because for user-visible prose a retry would duplicate or contradict
	// half a paragraph somebody has already read. That is right for a chat
	// reply and wrong for a phase nobody is reading: a planner that streams
	// into a buffer and times out mid-generation loses the whole turn, with
	// max_retries set to whatever you like.
	//
	// The obvious spelling of the opt-in is a bool, and it is a trap. A caller
	// accumulating chunks would keep the partial from the failed attempt and
	// append the full text of the retry to it, producing output that is
	// corrupt rather than merely late — and nothing would say so. So the
	// permission and the cleanup are ONE thing: you cannot declare a stream
	// retryable without handing over the function that resets it. Called
	// immediately before each re-attempt, on the goroutine that owns the
	// stream.
	OnStreamRestart func()

	// NoTierFallback refuses the quiet degrade to the worker when a lead call
	// fails.
	//
	// The fallback is right for a routing PREFERENCE: the session continues on
	// the worker rather than aborting, and a transient lead outage costs
	// quality instead of the turn. It is wrong for an explicit pin. Somebody who
	// set one system to the lead model said which model they wanted; answering
	// from the worker anyway, behind a debug line, is the same silent
	// substitution the pin exists to prevent — and it hid a malformed thinking
	// request behind "it works, just slower" for every call.
	NoTierFallback bool
	// TierResolved says the CALLER has already decided this call belongs on the
	// lead, so LeadChat must not re-derive the tier from RouteKey and delegate
	// back to the worker.
	//
	// It exists because the agent loop and LeadChat each consulted routing
	// independently. The loop escalated on a per-resource override; LeadChat saw
	// the same RouteKey, asked RouteToLead, got "worker" from the stage, and
	// transparently un-escalated — so the override worked all the way down to
	// the call and was then undone one frame later. RouteKey is still passed,
	// because it carries the stage's THINKING preference, which the caller does
	// want; only the tier decision is already made.
	TierResolved bool
	Caller       string // Identifier of the app/pipeline making the call; used by the Ollama fair-queueing scheduler. Empty → "unknown".
	MaskDebug    bool   // Suppress request/response content from debug logs (use for sessions with sensitive data).
	// SuppressAutoDate skips the "Today's date is …" system-prompt prepend
	// (see applyOpts). Set by callers that stamp the date onto the latest
	// user turn instead — the cache-safe placement, since a system-prompt
	// date sits before the conversation and re-prefills it on every rollover.
	// The multi-turn agent loop uses this; one-shot callers leave it off and
	// keep the (cache-irrelevant, single-turn) system-prompt date.
	SuppressAutoDate bool
	// ReasoningHandler, when non-nil, receives reasoning-channel
	// chunks as the model emits them — separate from the main
	// content StreamHandler. UI surfaces (chat web) use this to
	// render a live "thinking" pane during reasoning so the user
	// has something to watch during long thinks. Called only on
	// streaming paths; non-stream Chat() puts the full reasoning
	// on Response.Reasoning as before.
	ReasoningHandler StreamHandler
}

// ChatOption is a functional option for configuring an LLM call.
type ChatOption func(*ChatConfig)

// WithModel overrides the default model for this call.
func WithModel(model string) ChatOption {
	return func(c *ChatConfig) { c.Model = model }
}

// WithReasoningStream installs a per-chunk handler for the reasoning
// channel. When set on a streaming Chat call, the handler receives
// reasoning text as the model emits it — useful for "live thinking"
// UI panels. Non-stream callers and backends without a reasoning
// channel ignore it.
func WithReasoningStream(h StreamHandler) ChatOption {
	return func(c *ChatConfig) { c.ReasoningHandler = h }
}

// WithMaxTokens sets the maximum number of tokens to generate.
func WithMaxTokens(n int) ChatOption {
	return func(c *ChatConfig) { c.MaxTokens = n }
}

// WithTemperature sets the sampling temperature.
func WithTemperature(t float64) ChatOption {
	return func(c *ChatConfig) { c.Temperature = &t }
}

// WithTopK sets the top-k sampling cutoff for this call.
func WithTopK(k int) ChatOption {
	return func(c *ChatConfig) { c.TopK = &k }
}

// WithTopP sets the nucleus-sampling top-p for this call.
func WithTopP(p float64) ChatOption {
	return func(c *ChatConfig) { c.TopP = &p }
}

// WithMinP sets the min-p sampling cutoff for this call.
func WithMinP(p float64) ChatOption {
	return func(c *ChatConfig) { c.MinP = &p }
}

// WithSystemPrompt sets the system prompt for this call.
func WithSystemPrompt(prompt string) ChatOption {
	return func(c *ChatConfig) { c.SystemPrompt = prompt }
}

// WithoutAutoDate suppresses applyOpts's system-prompt date prepend, for
// callers that stamp the date onto the latest user turn instead (the cache-safe
// spot). See ChatConfig.SuppressAutoDate.
func WithoutAutoDate() ChatOption {
	return func(c *ChatConfig) { c.SuppressAutoDate = true }
}

// CurrentContextStamp is the bracketed date+time marker prefixed onto the latest
// user turn so the model always has the current wall-clock. Placed on the newest
// user message — the volatile tail that is never a cache hit anyway — it costs no
// cache invalidation, unlike a system-prompt date which sits before the whole
// conversation and re-prefills it on the daily (or per-request, with time)
// rollover. Local time, no em-dash (an AI tell we scrub from product output).
func CurrentContextStamp() string {
	return CurrentContextStampIn(time.Local)
}

// CurrentContextStampIn is CurrentContextStamp in a specific location — used
// to stamp the turn in the acting user's own timezone (Phase 2 per-user zone).
// A nil location falls back to the deployment/host zone (time.Local).
func CurrentContextStampIn(loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	return "[Current date & time: " + time.Now().In(loc).Format("Mon, January 2, 2006 at 3:04 PM MST") + "]"
}

// WithTools provides tool definitions for the LLM to use.
func WithTools(tools []Tool) ChatOption {
	return func(c *ChatConfig) { c.Tools = houseStyleTools(tools) }
}

// houseStyleTools strips em-dashes out of tool and parameter DESCRIPTIONS on
// the way to the model.
//
// Every prompt with tools carries their descriptions, both as text through
// BuildToolPrompt and structurally in each provider's tool schema. Around 310
// of them contained an em-dash, so an agent read several hundred examples of
// the character directly below a rule telling it never to produce one. Qwen
// class models pattern match hard on what is in context (the phantom
// "[just now]" incident is the same effect), and a rule losing to its own
// prompt is not the model being stubborn.
//
// Done HERE because this is the one funnel: every provider reads ChatConfig
// .Tools, so one pass covers the structured schemas and tools added later,
// instead of 310 hand edits that the next contributor undoes.
//
// Descriptions only. Names, enum values, and path scopes are identifiers the
// model must reproduce exactly, and rewriting one would break the call it is
// meant to make.
func houseStyleTools(tools []Tool) []Tool {
	if len(tools) == 0 {
		return tools
	}
	out := make([]Tool, len(tools))
	copy(out, tools)
	for i := range out {
		out[i].Description = textutil.StripEmDashes(out[i].Description)
		out[i].Parameters = houseStyleParams(out[i].Parameters)
	}
	return out
}

func houseStyleParams(in map[string]ToolParam) map[string]ToolParam {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]ToolParam, len(in))
	for k, p := range in {
		p.Description = textutil.StripEmDashes(p.Description)
		p.Properties = houseStyleParams(p.Properties) // object params nest
		if p.Items != nil {
			item := *p.Items
			item.Description = textutil.StripEmDashes(item.Description)
			item.Properties = houseStyleParams(item.Properties)
			p.Items = &item
		}
		out[k] = p
	}
	return out
}

// WithJSONMode requests JSON output from the LLM.
func WithJSONMode() ChatOption {
	return func(c *ChatConfig) { c.JSONMode = true }
}

// WithMaxRetries overrides the default retry count for this call. 0 disables retries.
func WithMaxRetries(n int) ChatOption {
	return func(c *ChatConfig) { c.MaxRetries = &n }
}

// WithStreamRestart makes a streaming call retryable after partial output by
// supplying the reset that makes it safe. See ChatConfig.OnStreamRestart.
//
// Use it for a stream whose chunks are NOT shown to a person as they arrive —
// a plan phase, a judge, anything accumulating into a buffer the caller owns.
// Do not use it for a chat reply being rendered live: the reset cannot unsee
// what somebody already read.
func WithStreamRestart(reset func()) ChatOption {
	return func(c *ChatConfig) { c.OnStreamRestart = reset }
}

// WithThink enables or disables thinking mode for thinking models (qwen3, etc.).
// When set to false, the model skips reasoning and responds directly.
func WithThink(enabled bool) ChatOption {
	return func(c *ChatConfig) { c.Think = &enabled }
}

// WithThinkBudget caps the thinking token budget for this call, overriding the
// global ThinkingBudget setting. Has no effect when thinking is disabled.
func WithThinkBudget(n int) ChatOption {
	return func(c *ChatConfig) { c.ThinkBudget = &n }
}

// workerJudgeThinkBudget is the small thinking allowance for one-shot
// worker-tier judge/suggest calls. Qwen3-class models degenerate in no-think
// mode (repetition, format drift), so WithThink(false) is the wrong lever for
// "keep it fast" — a small budget keeps the call quick AND coherent.
const workerJudgeThinkBudget = 256

// WorkerJudgeThink is the standard thinking shape for one-shot worker-tier
// judge/suggest calls: thinking ON with a small budget. Callers that also cap
// output with WithMaxTokens must leave headroom on top of this budget — the
// completion cap covers the thinking span too.
func WorkerJudgeThink() ChatOption { return WithThinkBudget(workerJudgeThinkBudget) }

// WithRouteKey tags a LeadChat call with a routing stage key. If the stage
// is configured for "worker" in the routing menu, LeadChat transparently
// delegates to WorkerChat with the same options. Unknown/unset keys default
// to lead, so it's safe to add WithRouteKey before registering the stage.
func WithRouteKey(key string) ChatOption {
	return func(c *ChatConfig) { c.RouteKey = key }
}

// WithTierOverride pins this call to a tier, ahead of whatever the route
// stage says — see ChatConfig.TierOverride. TierUnset is a no-op, so a caller
// can pass an unresolved pin without branching.
func WithTierOverride(tier LLMTier) ChatOption {
	return func(c *ChatConfig) { c.TierOverride = tier }
}

// WithNoTierFallback refuses the silent degrade to the worker on a lead
// failure — see ChatConfig.NoTierFallback. The error surfaces instead.
func WithNoTierFallback() ChatOption {
	return func(c *ChatConfig) { c.NoTierFallback = true }
}

// WithTierResolved marks a LeadChat call whose tier the caller has already
// settled — see ChatConfig.TierResolved. The routing stage still supplies the
// thinking preference; it just no longer overrules the tier.
func WithTierResolved() ChatOption {
	return func(c *ChatConfig) { c.TierResolved = true }
}

// WithMaskDebug suppresses request/response content from debug logs for this
// call. Use for sessions that handle sensitive data (credentials, private docs).
func WithMaskDebug() ChatOption {
	return func(c *ChatConfig) { c.MaskDebug = true }
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
	// Prepend today's date so a caller that sets a system prompt gets date
	// awareness for free. SUPPRESSED for the multi-turn agent loop, which stamps
	// the date+time onto the latest user turn instead (cache-safe): a date at the
	// front of the system prompt sits before the whole conversation and re-prefills
	// it on the daily rollover. Single-turn callers leave it on — placement is
	// cache-irrelevant when there's no conversation history to preserve.
	if cfg.SystemPrompt != "" && !cfg.SuppressAutoDate {
		cfg.SystemPrompt = "Today's date is " + time.Now().Format("January 2, 2006") + ". " + cfg.SystemPrompt
	}
	cfg.Tools = dropInvalidlyNamedTools(cfg.Tools, cfg.Caller)
	return cfg
}

// validLLMToolName reports whether a tool name satisfies ^[a-zA-Z0-9_-]{1,128}$,
// the class every provider validates against. Anthropic states it directly and
// Bedrock relays the same rejection; OpenAI and Gemini are no more permissive.
// Hand-rolled rather than a regexp because this runs over the whole catalog on
// every call.
func validLLMToolName(name string) bool {
	if len(name) == 0 || len(name) > maxLLMToolNameBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// badToolNamesLogged remembers which names have already been reported (sync.Map
// keyed by name), so a
// permanently misnamed tool costs one log line rather than one per LLM call.
var badToolNamesLogged sync.Map

// dropInvalidlyNamedTools removes tools the provider would reject on name, and
// says so.
//
// The guard exists because the blast radius is the whole request, not the one
// tool. A provider handed a single malformed name rejects the ENTIRE call, so
// one bad tool takes every other tool in the catalog down with it and the agent
// silently loses all tool use — which is what a remote MCP server's
// "atlassian.search" did (see mcpExposedName). Every tool source that composes
// a name should validate it at the point it is minted; this is the backstop for
// the next source that forgets, converting a total outage into one missing tool
// plus a log line naming it.
//
// Deliberately drop rather than rename: the model calls a tool by the name it
// was given, so a rename here would need a reverse mapping on the response path
// and would turn a loud failure into a confusing one.
func dropInvalidlyNamedTools(tools []Tool, caller string) []Tool {
	bad := 0
	for i := range tools {
		if !validLLMToolName(tools[i].Name) {
			bad++
		}
	}
	if bad == 0 {
		return tools // the overwhelmingly common path allocates nothing
	}
	out := make([]Tool, 0, len(tools)-bad)
	for _, t := range tools {
		if validLLMToolName(t.Name) {
			out = append(out, t)
			continue
		}
		if _, seen := badToolNamesLogged.LoadOrStore(t.Name, true); !seen {
			Log("[llm] tool %q has a name the model APIs reject (must match ^[a-zA-Z0-9_-]{1,128}$); dropping it from the catalog so the rest of the tools still work. Caller: %s", t.Name, caller)
		}
	}
	return out
}

// LLMProviderConfig holds stored configuration for an LLM provider.
type LLMProviderConfig struct {
	Provider            string
	Model               string
	APIKey              string
	Endpoint            string
	Region              string        // AWS region (bedrock only); blank falls back to AWS_REGION, then us-east-1. Part of both the hostname and the SigV4 signature.
	Profile             string        // AWS profile (bedrock only); blank falls back to AWS_PROFILE. Selects which credentials/SSO session to use — the credentials themselves are never stored here.
	BedrockAPI          string        // Which Bedrock API to speak: "" / "messages" = the Messages-API endpoint (needs bedrock-mantle:CreateInference); "invoke" = legacy bedrock-runtime InvokeModel (needs bedrock:InvokeModel). Whichever one the account's IAM policy actually grants.
	ContextSize         int           // Working context window (tokens). Ollama/llama.cpp: sent as num_ctx. Anthropic/Bedrock: the cap agent-loop history compaction keys on (0 = 200K default — deliberately under the 1M API window; see anthropicDefaultContextSize).
	ConnectTimeout      time.Duration // Dial timeout; defaults to 10s if zero.
	RequestTimeout      time.Duration // Per-Read idle deadline applied via iotimeout; defaults to 5min if zero. Long because non-streaming Gemini / model-listing calls need to ride out slow handshakes; streaming paths layer a shorter StreamIdleTimeout on top.
	StreamIdleTimeout   time.Duration // Idle-read deadline applied ONLY to streaming chat calls — if no bytes arrive for this long the body is closed and the error reads as transient so the retry layer can take another shot. Defaults to DefaultStreamIdleTimeout (60s) if zero. Tune up if the model legitimately stalls between tokens (heavy thinking budgets, cold prefills on a busy server).
	DisableThinking     bool          // Master override: forces think=false on every call regardless of per-call WithThink(true). Supported for Ollama and Gemini (Flash) providers.
	ThinkingBudget      int           // Max thinking tokens per call (Gemini and Ollama). 0 = model default. Ignored when DisableThinking is set.
	NativeTools         bool          // When true, use native function calling. When false, tools are described in the system prompt and parsed from <tool_call> tags. Default false for ollama models without tool support.
	OllamaMaxParallel   int           // Ollama only: global concurrency cap. 0 or negative = scheduler disabled; 1 = strict serial (default). Requests are fair-queued across sessions.
	LlamacppMaxParallel int           // llama.cpp only: global concurrency cap. Default 1 (llama.cpp is single-threaded). Raise only when the server supports concurrent requests.
	// NoThink* fields control individual signals sent to llama.cpp on
	// WithThink(false) calls. Defaults are what's empirically proven
	// to work on Qwen 3 unified — kwarg + budget alone is sufficient,
	// the /no_think prepends are opt-in for models where kwarg fails.
	NoThinkUseKwarg      bool // llama.cpp: send chat_template_kwargs.enable_thinking=false. Default true (proven reliable disable on Qwen 3 unified + Gemma 4).
	NoThinkSendBudget    bool // llama.cpp: send thinking_budget_tokens cap. Default true (hard ceiling caught when kwarg slips).
	NoThinkPrependSystem bool // llama.cpp: prepend "/no_think " to system prompt. Default false (off because kwarg+budget alone works); enable as belt-and-suspenders for models where kwarg unreliable.
	NoThinkPrependUser   bool // llama.cpp: prepend "/no_think " to last user message. Default false (same reasoning as PrependSystem).
	NoThinkBudget        int  // llama.cpp: thinking_budget_tokens value when NoThinkSendBudget is true. 0 = llamacppNoThinkDefaultBudget (512).
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
		// Fallback when operator hasn't set request_timeout_seconds via
		// --setup or admin UI. 5 minutes is plenty for typical agentic
		// rounds on Qwen 27B dense with tightened thinking budgets and
		// /no_think directives — fail-fast is more diagnostic than
		// waiting 12 minutes for a hung request. Operators running
		// deep-thinking workloads (research/debate hitting full
		// dynamic-budget ceilings) should bump this in --setup to 10+
		// minutes; the override path is unchanged.
		requestTimeout = 5 * time.Minute
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
	// peer names the peer a peer-backed tier borrows this model from, so a
	// credential that peer REFUSES can be dropped and re-exchanged. Empty for
	// every other provider. See peerRefused.
	peer string
}

// peerRefused reports a peer answering 401 to a model call, and drops the
// access token it refused on the way through.
//
// The far side is the only thing that knows a token has died — reuse detection
// deleting the family, the peer restarting, a revoked grant — and this side's
// clock says the token is fine, so nothing else will ever renew it. Left alone,
// one refusal means every model call to that peer fails until an operator
// re-pairs by hand.
//
// Dropping it is what makes recovery automatic: the client's AuthFunc resolves
// a credential per request (see peerModelAuth), so the very next attempt
// exchanges a fresh one. The immediate retry below turns "one dead turn" into
// no dead turn; the drop alone would already have fixed the turn after.
//
// The LLM tiers cannot use the peer transport that every other capability
// authenticates through — they go out on snugforge's APIClient, which has an
// AuthFunc hook but no transport hook to install a RoundTripper into — so the
// same recovery is expressed here instead.
func (r *retryLLM) peerRefused(err error) bool {
	if r.peer == "" || err == nil {
		return false
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		return false
	}
	InvalidatePeerAccessToken(r.peer)
	Log("[peer] %q refused our credential on a model call — dropped it so the next request re-exchanges", r.peer)
	return true
}

// ErrContextExceeded is the sentinel returned (via fmt.Errorf "%w") when
// the LLM provider reports the request exceeded the model's context
// window. Distinct from generic transient errors because naive retries
// don't help — the same prompt will fail the same way. Callers that
// know how to free space (the agent loop's aggressive compactHistory,
// pipeline interpreters that can drop oldest stage outputs) check for
// this sentinel and retry-after-trim once; everyone else surfaces it
// as a clean caller-visible error.
var ErrContextExceeded = errors.New("llm: context window exceeded")

// IsContextExceededError reports whether err is (or wraps) a context-
// window-exceeded signal from the LLM provider. Recognizes the major
// provider patterns so callers don't have to do provider-specific
// string matching. Wrap with fmt.Errorf("%w: …", ErrContextExceeded,
// …) on the provider side; downstream code checks via errors.Is.
//
// Pattern coverage:
//   - OpenAI / OpenAI-compatible: "context_length_exceeded" code OR
//     message containing "maximum context length" / "context length"
//   - Anthropic: 400 with "input is too long" / "prompt is too long" /
//     "max_tokens_to_sample" in the body
//   - llama.cpp: "context size exceeded" / "the prompt is too long" /
//     "n_ctx" appearing in error string
//   - Generic substring fallback for any provider not covered above
//     ("context window" / "context exceeded" / "too many tokens")
//
// Conservative on false positives — the substrings chosen rarely
// appear in unrelated errors. False negatives just mean a recoverable
// context error gets treated as terminal, which is the safer fail.
func IsContextExceededError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrContextExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	// OpenAI explicit code
	if strings.Contains(msg, "context_length_exceeded") {
		return true
	}
	// Common phrasings across providers
	needles := []string{
		"context window",
		"context length",
		"context size",
		"context exceeded",
		"maximum context",
		"input is too long",
		"prompt is too long",
		"the prompt is too long",
		"too many tokens",
		"input tokens exceed",
		"exceeds the maximum",
		"n_ctx",
	}
	for _, n := range needles {
		if strings.Contains(msg, n) {
			return true
		}
	}
	return false
}

// isTransientError returns true if the error is worth retrying.
func isTransientError(err error) bool {
	// Context-exceeded is deterministic — the same prompt will fail the
	// same way on every retry. Caller (agent loop / pipeline) is
	// responsible for shrinking the prompt and retrying via the
	// ErrContextExceeded sentinel path; blind retries here just burn
	// the budget.
	if IsContextExceededError(err) {
		return false
	}
	if apiErr, ok := err.(*APIError); ok {
		switch apiErr.StatusCode {
		case 429, 500, 502, 503, 529:
			return true
		}
		return false
	}
	// Context deadline exceeded — historically not retried under the
	// assumption "retrying with the same budget will just timeout
	// again." That misses the common case: the LLM got stuck in a
	// generation loop and ate the whole budget, but a fresh request
	// (different KV state, different sampling) breaks out and
	// completes. Treating as transient lets the retry layer take that
	// shot. Caller's max-attempts cap bounds the worst case (e.g. 3
	// attempts × 5min = 15min ceiling), and a genuinely-slow workload
	// signals "raise RequestTimeout" via that ceiling being hit
	// repeatedly. Operators who want the strict no-retry behavior can
	// set max_retries=1.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Transport-level "context canceled" (reverse proxy idle-cap,
	// browser fetch abort that closed our HTTP request, etc.) shows up
	// as a wrapped *url.Error containing context.Canceled. The string
	// match is the only reliable signal once Go has serialized it into
	// the `Post "...": context canceled` form. Treat as transient so
	// the retry layer gets a chance — caller's own ctx cancellation is
	// already handled in doWithRetry's <-ctx.Done() guard before each
	// attempt, so a genuinely-canceled call still bails out immediately.
	if errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled") {
		return true
	}
	// Stream idle deadline tripped — server went silent mid-response.
	// Almost always recoverable on retry (load shifted, fresh slot,
	// transient KV pressure cleared). Matching via the marker substring
	// so wrapped forms (fmt.Errorf("%w from ...", errStreamIdleTimeout))
	// are still recognized.
	if IsStreamIdleTimeoutError(err) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if netErr, ok := err.(net.Error); ok {
		// Timeout or temporary (connection reset, refused, etc.)
		return netErr.Timeout() || netErr.Temporary() //nolint:staticcheck
	}
	// Unwrap and retry any net.OpError (e.g. "connection reset by peer").
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return false
}

func (r *retryLLM) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	return doWithRetry(ctx, LLMMaxRetries(), opts, func() (*Response, error) {
		// First attempt: original opts, original messages.
		resp, err := r.inner.Chat(ctx, messages, opts...)
		if r.peerRefused(err) {
			// The credential is gone; this attempt resolves a new one.
			resp, err = r.inner.Chat(ctx, messages, opts...)
		}
		if !shouldRetryEmpty(resp, err) {
			return resp, err
		}
		// First retry: keep thinking ON but append a hint message
		// nudging the model to actually produce output. Qwen's failure
		// shape here is "thought briefly, then stopped with finish=stop
		// and nothing in content/tool_calls" — disabling thinking on
		// retry strips its ability to decide on a tool, so we keep it
		// and just nudge.
		Debug("[retry] unusable response (err=%v, %s) — retrying with hint, thinking still enabled", err, respShape(resp))
		hinted := append([]Message{}, messages...)
		hinted = append(hinted, Message{
			Role:    "user",
			Content: "Your previous turn produced no output. Reason briefly, then either call a tool or send a text reply — do not end your turn empty.",
		})
		resp2, err2 := r.inner.Chat(ctx, hinted, opts...)
		if err2 == nil && responseIsUseable(resp2) {
			return resp2, nil
		}
		// Last-ditch retry: drop thinking entirely. Worse for tool
		// decisions but sometimes the only thing that produces output
		// when the model is wedged.
		Debug("[retry] still empty after hint — falling back to thinking disabled")
		f := false
		retryOpts := append(append([]ChatOption{}, opts...), WithThink(f))
		resp3, err3 := r.inner.Chat(ctx, hinted, retryOpts...)
		if err3 == nil && responseIsUseable(resp3) {
			return resp3, nil
		}
		// All paths empty — return the original.
		return resp, err
	})
}

func (r *retryLLM) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	var handlerCalled bool
	var chunks int
	wrappedHandler := func(chunk string) {
		handlerCalled = true
		chunks++
		handler(chunk)
	}
	// Resolved once: whether this caller has told us its partial output can be
	// discarded, and given us the means to discard it.
	streamCfg := ChatConfig{}
	for _, opt := range opts {
		opt(&streamCfg)
	}
	// restart discards whatever the failed attempt delivered and reports
	// whether a re-attempt is safe. Without an OnStreamRestart the answer is
	// always no, which keeps every existing caller exactly as it was.
	restart := func() bool {
		if streamCfg.OnStreamRestart == nil {
			return false
		}
		streamCfg.OnStreamRestart()
		return true
	}
	return doWithRetry(ctx, LLMMaxRetries(), opts, func() (*Response, error) {
		// Stream-aware empty retry: only safe to retry if the handler
		// hasn't already received chunks (otherwise the user has seen
		// partial output and a retry would duplicate or contradict it) —
		// unless the caller supplied the reset that makes it safe.
		handlerCalled = false
		chunks = 0
		resp, err := r.inner.ChatStream(ctx, messages, wrappedHandler, opts...)
		// A peer refusing the credential is caught before the transience rules
		// see it, because a 401 is not transient and would otherwise end the
		// turn. Re-attempted ONLY when nothing has reached the caller yet: a
		// refused request produces no output, so that is the normal case, and
		// the alternative — replaying a stream a reader has already seen — is
		// worse than the failure. The token is dropped either way, so a turn
		// that cannot be retried here still leaves the peer healthy for the
		// next one.
		if r.peerRefused(err) && !handlerCalled {
			resp, err = r.inner.ChatStream(ctx, messages, wrappedHandler, opts...)
		}
		if err != nil && handlerCalled {
			if !restart() {
				// Chunks were already delivered to the caller; do not retry.
				return resp, &nonRetryableError{err}
			}
			// The caller has thrown its partial away. Hand the error back
			// unwrapped so the normal transience rules decide — a stream that
			// died on a deadline retries, one that died on a 400 still does not.
			Debug("[retry] stream failed after %d partial chunk(s); caller discarded them — retrying: %v", chunks, err)
			return resp, err
		}
		if handlerCalled || !shouldRetryEmpty(resp, err) {
			return resp, err
		}
		// First retry: keep thinking ON, append a hint message.
		Debug("[retry] unusable stream response (err=%v, %s) — retrying with hint, thinking still enabled", err, respShape(resp))
		hinted := append([]Message{}, messages...)
		hinted = append(hinted, Message{
			Role:    "user",
			Content: "Your previous turn produced no output. Reason briefly, then either call a tool or send a text reply — do not end your turn empty.",
		})
		handlerCalled = false
		resp2, err2 := r.inner.ChatStream(ctx, hinted, wrappedHandler, opts...)
		if err2 == nil && responseIsUseable(resp2) {
			return resp2, nil
		}
		if handlerCalled && !restart() {
			// Second attempt produced visible chunks; can't safely retry again.
			return resp2, err2
		}
		// Last-ditch retry: drop thinking.
		Debug("[retry] still empty after hint — falling back to thinking disabled")
		f := false
		retryOpts := append(append([]ChatOption{}, opts...), WithThink(f))
		handlerCalled = false
		resp3, err3 := r.inner.ChatStream(ctx, hinted, wrappedHandler, retryOpts...)
		if err3 == nil && responseIsUseable(resp3) {
			return resp3, nil
		}
		return resp, err
	})
}

// ContextSize forwards the inner LLM's ContextSizer through the retry
// wrapper. Without it, T.LLM (a *retryLLM) fails the T.LLM.(ContextSizer)
// type assertion in WorkerContextSize()/LeadContextSize(), which then
// return 0 — silently disabling every context-size-dependent feature
// (history compaction, debate context math, …). Returns 0 only when the
// inner LLM genuinely exposes no window.
func (r *retryLLM) ContextSize() int {
	if cs, ok := r.inner.(ContextSizer); ok {
		return cs.ContextSize()
	}
	return 0
}

// respShape summarizes a response's actionable output for diagnostics. It
// distinguishes a TRULY empty turn from a REASONING-ONLY one: Qwen sometimes
// spends the whole turn inside a <think> block, which the streaming parser
// routes to Reasoning, leaving Content empty. Such a turn has consumed output
// tokens (so it "doesn't look empty" in the llama.cpp usage line) yet carries
// nothing actionable, which is exactly when the retry fires.
func respShape(resp *Response) string {
	if resp == nil {
		return "nil"
	}
	return fmt.Sprintf("content=%d reasoning=%d tools=%d",
		len(strings.TrimSpace(resp.Content)), len(resp.Reasoning), len(resp.ToolCalls))
}

// responseIsUseable reports whether resp has actionable output for the caller.
// Reasoning alone doesn't count — every downstream consumer (agent loops, chat
// handlers, tool-call dispatchers) acts on Content or ToolCalls.
func responseIsUseable(resp *Response) bool {
	if resp == nil {
		return false
	}
	if strings.TrimSpace(resp.Content) != "" {
		return true
	}
	return len(resp.ToolCalls) > 0
}

// shouldRetryEmpty reports whether an LLM result should trigger a one-shot
// retry with thinking disabled. Triggers on:
//   - timeout errors (thinking is the slow part)
//   - "empty LLM response" errors (model exhausted budget producing nothing)
//   - successful but empty responses (model produced only reasoning)
//
// shouldRetryEmpty reports whether the inner closure in retryLLM
// should attempt the "hint then drop thinking" recovery path. That
// path is meant for a specific model-side failure: a 200 response
// where finish_reason=stop but content/tool_calls are empty (Qwen
// occasionally does this when thinking ate the budget). Transport
// failures (timeout, EOF, connection refused) MUST NOT take this
// path — appending a hint and disabling thinking can't help when
// the server is unreachable, and each inner attempt re-pays the
// HTTP timeout. Those errors fall through to doWithRetry, which
// handles transient transport with exponential backoff.
func shouldRetryEmpty(resp *Response, err error) bool {
	if err != nil {
		return isEmptyResponseErr(err)
	}
	return !responseIsUseable(resp)
}

// isEmptyResponseErr reports whether err is the "empty LLM response" surfaced
// by the OpenAI-compatible client when the model consumed output tokens
// without producing content (most often: thinking ate the budget, or
// finish_reason=length with no content).
func isEmptyResponseErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "empty LLM response")
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
	thinkingRetried := false
	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, err := fn()
		if err == nil {
			return resp, nil
		}
		if _, ok := err.(*nonRetryableError); ok {
			return resp, err.(*nonRetryableError).error
		}
		// A model refusing the budgeted thinking shape is not transient, but it
		// IS self-correcting: it told us which shape it takes, so record that
		// and try once more. Which shape a Claude model wants is not derivable
		// from its id in a way that keeps working — Bedrock ids move constantly
		// — so believing the model beats maintaining a list that fails closed on
		// everything released after it was written.
		//
		// Once per model per process: the next call is built correctly and never
		// reaches here.
		// The client has already recorded which shape its model wants (see
		// noteIfAdaptiveThinking), so the rebuilt request differs from the one
		// that just failed. Retried at most once: a second identical refusal
		// means something else is wrong and looping on it would turn one clear
		// error into a hang.
		if isUnsupportedThinkingTypeErr(err) && !thinkingRetried {
			thinkingRetried = true
			continue
		}
		if !isTransientError(err) {
			return resp, err
		}
		lastErr = err
		if attempt < maxRetries {
			secs := math.Pow(float64(attempt+1), 2)
			if secs > 30 {
				secs = 30
			}
			backoff := time.Duration(secs) * time.Second
			Log("[retry] attempt %d/%d failed: %v — retrying in %v", attempt+1, maxRetries, err, backoff)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return nil, lastErr
}

// ProviderHasNativeTools reports whether a provider's API always supports
// native function calling, so the prompt-based fallback must never be used
// for it.
//
// The fallback describes tools in the system prompt and parses <tool_call>
// tags out of the reply. It exists for local models that cannot do native
// calls — which in practice means some Ollama models, and is why that provider
// has an operator toggle. Every hosted API here supports native tools.
//
// This is an allowlist because the denylist it replaced named exactly one
// provider (llama.cpp), so `native_tools` being unset — its zero value, and
// the default in a freshly configured provider — silently routed Anthropic,
// OpenAI, Gemini and Bedrock through the fallback. The observed failure was
// total and looked like a model problem: every call to every tool arrived with
// EMPTY arguments, because the tag parser recovered the tool NAME from a
// natively-formatted call and nothing else. Four tools deep, including
// plan_set, with the agent itself reporting "I keep dropping the arguments on
// send".
func ProviderHasNativeTools(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "bedrock", "openai", "gemini", "llama.cpp":
		return true
	}
	// ollama (and anything unrecognized): depends on the model, so honor the
	// operator's toggle.
	return false
}

// NewLLMFromConfig creates an LLM client from a stored configuration.
func NewLLMFromConfig(cfg LLMProviderConfig) (LLM, error) {
	// A provider naming a peer is resolved HERE rather than at save time, so
	// the stored config keeps saying "peer:den" and the endpoint and key are
	// read fresh from the peer record on every build. Snapshotting them into
	// the config is the bug that already bit embeddings: rotating a peer key
	// left a config that looked correct and 401'd, with nothing on screen to
	// suggest which of the two records was stale.
	// Captured BEFORE resolution, which rewrites Provider to the concrete one.
	peerName := peerNameFromProvider(cfg.Provider)
	resolved, err := ResolveModelProvider(cfg, cfg.Provider)
	if err != nil {
		return nil, Error(err.Error())
	}
	cfg = resolved
	api := newLLMAPIClient(cfg)
	if peerName != "" {
		// Per REQUEST, not per build. Resolution above fills cfg.APIKey with the
		// credential of this moment, and the client outlives it: an access token
		// lasts fifteen minutes and a re-key kills one instantly. AuthFunc wins
		// over StaticToken inside APIClient, so the value the constructors below
		// stamp in is only ever a fallback for the build that has no peer.
		api.AuthFunc = peerModelAuth(peerName)
	}
	var inner LLM

	switch cfg.Provider {
	case "anthropic":
		if cfg.APIKey == "" {
			return nil, Error("anthropic API key is not configured — set it in the web UI under Admin → LLMs → Worker LLM")
		}
		model := cfg.Model
		if model == "" {
			model = "claude-sonnet-5"
		}
		ac := newAnthropicLLM(cfg.APIKey, model, api).(*anthropicClient)
		ac.contextSize = cfg.ContextSize
		inner = ac
	case "bedrock":
		// Claude via AWS Bedrock. No key check: an empty APIKey is the normal
		// case (it means "sign with AWS credentials"), and newBedrockLLM
		// reports a specific error when neither a bearer token nor resolvable
		// credentials are present.
		var err error
		// Two endpoints, picked by which IAM action the account grants — see
		// llm_bedrock_runtime.go. Defaulting to the Messages API keeps the
		// better integration (real SSE streaming) as the norm.
		if cfg.BedrockAPI == "invoke" {
			inner, err = newBedrockRuntimeLLM(cfg.APIKey, cfg.Model, cfg.Region, cfg.Profile, cfg.Endpoint, api)
		} else {
			inner, err = newBedrockLLM(cfg.APIKey, cfg.Model, cfg.Region, cfg.Profile, cfg.Endpoint, api)
		}
		if err != nil {
			return nil, err
		}
		// Both Bedrock paths serve the same Claude models; carry the working
		// context cap so ContextSize() reflects the operator's setting.
		switch c := inner.(type) {
		case *anthropicClient:
			c.contextSize = cfg.ContextSize
		case *bedrockRuntimeClient:
			c.contextSize = cfg.ContextSize
		}
	case "openai":
		if cfg.APIKey == "" {
			return nil, Error("openai API key is not configured — set it in the web UI under Admin → LLMs → Worker LLM")
		}
		model := cfg.Model
		if model == "" {
			model = "gpt-4o"
		}
		inner = newOpenAILLM(cfg.APIKey, model, openAIEndpoint, api)
		inner.(*openAIClient).streamIdleTimeout = cfg.StreamIdleTimeout
	case "gemini":
		if cfg.APIKey == "" {
			return nil, Error("gemini API key is not configured — set it in the web UI under Admin → LLMs → Worker LLM")
		}
		model := cfg.Model
		if model == "" {
			model = "gemini-2.5-flash"
		}
		inner = newGeminiLLM(cfg.APIKey, model, cfg.DisableThinking, cfg.ThinkingBudget, api)
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
		oc.streamIdleTimeout = cfg.StreamIdleTimeout
		inner = client
		// Start (or adjust) the global Ollama scheduler so concurrent
		// sessions get fair-queued. Safe to call multiple times; the
		// second call adjusts MaxParallel on the running dispatcher.
		maxParallel := cfg.OllamaMaxParallel
		if maxParallel < 1 {
			maxParallel = 1
		}
		StartOllamaScheduler(maxParallel)
	case "llama.cpp":
		ep := "http://localhost:8080/v1"
		if cfg.Endpoint != "" {
			ep = cfg.Endpoint
		}
		model := cfg.Model
		if model == "" {
			model = "local"
		}
		client := newOpenAILLM(cfg.APIKey, model, ep, api)
		oc := client.(*openAIClient)
		oc.llamacpp = true
		oc.llamacppBudget = cfg.ThinkingBudget
		oc.disableThinking = cfg.DisableThinking
		oc.noThinkUseKwarg = cfg.NoThinkUseKwarg
		oc.noThinkSendBudget = cfg.NoThinkSendBudget
		oc.noThinkPrependSystem = cfg.NoThinkPrependSystem
		oc.noThinkPrependUser = cfg.NoThinkPrependUser
		oc.noThinkBudget = cfg.NoThinkBudget
		oc.contextSize = cfg.ContextSize
		oc.streamIdleTimeout = cfg.StreamIdleTimeout
		inner = client
		// Start the serializer so concurrent callers queue here instead
		// of racing to llama.cpp and getting 503s. Default 1 matches
		// llama.cpp's single-threaded design; configurable via admin UI.
		mp := cfg.LlamacppMaxParallel
		if mp < 1 {
			mp = 1
		}
		StartLlamacppScheduler(mp)
	default:
		return nil, Error("no LLM provider is configured — set one in the web UI under Admin → LLMs → Worker LLM")
	}

	return &retryLLM{inner: inner, maxRetries: 5, peer: peerName}, nil
}
