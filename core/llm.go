package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/keystore"
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

// refused logs a provider refusing a request and returns the error unchanged.
//
// The provider's own message (the parameter it rejected, the quota it hit)
// otherwise reached only whoever got the error back: a Test button's result,
// a turn that failed. When gemini-3.5-flash-lite refused the Lead's test,
// the log said nothing at all, and the reason had to be asked for.
func refused(e *APIError, model string) *APIError {
	msg := strings.TrimSpace(e.Message)
	if r := []rune(msg); len(r) > 600 {
		msg = string(r[:600]) + "..."
	}
	Log("[%s] %s refused the request (HTTP %d): %s", e.Provider, chooseStr(model, "the model"), e.StatusCode, msg)
	return e
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
// StandInModel is the Model of a reply no model wrote. The stand-in a
// check's prompts are fingerprinted with (core/checktest) answers every call
// with an empty object under this name, so what reads a reply can leave it
// out of the log: it is not a model failing.
const StandInModel = "stand-in"

type Response struct {
	// TopLogprobs are the most likely first tokens of the reply and their
	// log-probabilities, for a decision (ChatConfig.DecidePrefix).
	TopLogprobs []struct {
		Token   string
		Logprob float64
	}
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
	// LaneWait is how long the run that produced this waited for the model
	// to be free of people (the lanes, shared_llm.go): time that was neither
	// the model's nor gohort's. The loop fills it on the reply it returns.
	LaneWait   time.Duration `json:"-"`
	StopReason string
	// ReasoningTokens is the portion of OutputTokens spent on the
	// thinking channel, as reported by the model when supported (e.g.
	// llama.cpp's usage.completion_tokens_details.reasoning_tokens
	// for Qwen3 thinking, or OpenAI's o1-style breakdown). Zero when
	// the backend doesn't report it; callers can fall back to a
	// char-ratio estimate from len(Reasoning) / len(Reasoning+Content).
	ReasoningTokens int
	// Pure-throughput numbers. PredictedPerSecond is decode-only
	// tokens/sec (excludes prefill), matching what llama.cpp's own web
	// UI displays: llama.cpp's figure when it reports one, otherwise
	// measured off the stream by decodeClock. PromptPerSecond is prefill
	// throughput, llama.cpp only. Zero when neither is known (a call
	// that was not streamed), never a guess.
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

	// ActionCaps gives the capabilities of each sub-action, for a grouped
	// tool whose actions differ. Empty for an ordinary tool, where Caps
	// already answers for every call. See ActionCapabilityTool: Caps is the
	// union and decides whether the tool is offered; this decides what one
	// CALL through it actually does.
	ActionCaps map[string][]Capability `json:"-"`

	// OwnModelReach names the calls whose only outside reach is the
	// deployment's own inference endpoint — keyed by action, or by the
	// tool's own name for a tool with no actions. See OwnModelReachTool.
	OwnModelReach map[string]bool `json:"-"`

	// FollowUps is, per action, the other actions a reply to it may ask the
	// agent to call (a toolbox action's follow_ups), and ActionPaths the
	// endpoint path each action calls, so a reply naming the path rather than
	// the action is recognised too. The injection scan reads a flagged reply
	// asking for one of these as the service's protocol (TempToolAction.FollowUps).
	FollowUps   map[string][]string `json:"-"`
	ActionPaths map[string]string   `json:"-"`

	// Prompt is an optional system-prompt fragment that gets appended
	// when this tool is loaded into an agent. Most tools (web_search,
	// fetch_url, calculate, ...) don't need one — name + description in
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

	// FetchesExternal marks a tool with no network capability of its own
	// whose output still carries content from outside: a script that fetches
	// through gohort's hooks (fetch, fetch_via, browse_page). The untrusted-
	// content fence reads it as it reads CapNetwork; nothing else does, so
	// the tool's reach and where it may run are unchanged. Not serialized.
	FetchesExternal bool `json:"-"`
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
	// registered root, as "kind:name" (e.g. "files:build_logs").
	// Checked when the tool RUNS, and the value substituted is the
	// absolute path it resolved to.
	//
	// The late-binding sibling of Enum. An enum is frozen when a tool is
	// authored, which suits "--env production|staging" and cannot
	// express a set that changes — the folders under a drop directory,
	// where new ones appearing without ceremony is the whole point. See
	// core/path_scope.go.
	PathScope string `json:"path_scope,omitempty"`
	// Default is the value an omitted optional param takes when the tool
	// runs. Without it an optional query param the model leaves out drops
	// out of the URL entirely, and an API that needs it (a page size, say)
	// answers 400 to every call that took the schema at its word. A string,
	// number or boolean only: the record is gob-encoded, and those are the
	// types gob can carry in an interface without registration.
	Default any `json:"default,omitempty"`
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
	// Shown so the model can leave the param out knowingly instead of
	// guessing a value, or sending one only because it cannot tell what an
	// omission does.
	if p.Default != nil {
		schema["default"] = p.Default
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
	// ArgsError is set when the model sent arguments that could not be read
	// (not valid JSON: an unescaped quote, a reply cut off mid-value). Args is
	// then empty, and the call must NOT run: run with no arguments, a tool
	// answers about a call the model never meant ("agent not found"), and the
	// model goes looking for the cause everywhere except its own JSON. The
	// agent loop refuses the call and says why.
	ArgsError string `json:"args_error,omitempty"`
	// Signature is a provider's opaque token for the reasoning behind this
	// call (Gemini 3's thoughtSignature). It must go back with the call when
	// the history is replayed, or the provider refuses the next request.
	Signature string `json:"signature,omitempty"`
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
	Model     string
	MaxTokens int
	// DecidePrefix makes the call a decision (core/decide): the reply is
	// pre-started with this text, the server continues it by ONE token, and
	// TopLogprobs of that token's alternatives come back with their
	// probabilities. vLLM only, for now.
	DecidePrefix string
	TopLogprobs  int
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
	Effort       string // Provider-neutral reasoning level: "off" | "low" | "medium" | "high"; "" = the tier's DefaultEffort. An explicit ThinkBudget or Think=false outranks it (see resolveEffort).
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
	// SuppressAutoDate skips the "Today's date is ..." system-prompt prepend
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

// WithReasoningStream installs a per-chunk handler for the reasoning
// channel. When set on a streaming Chat call, the handler receives
// reasoning text as the model emits it — useful for "live thinking"
// UI panels. Non-stream callers and backends without a reasoning
// channel ignore it.
func WithReasoningStream(h StreamHandler) ChatOption {
	return func(c *ChatConfig) { c.ReasoningHandler = h }
}

// WithMaxTokens sets the maximum number of tokens to generate.
// stopInterrupted is the stop reason stamped on a response whose stream ended
// before the provider said why. It is not a wire value. Every streaming
// transport delivers its terminal stop reason on a final event — Anthropic on
// message_delta, the OpenAI-compatible backends as finish_reason followed by
// [DONE] — and a stream that closes without one, whether from a dropped
// connection, a proxy that gave up on silence, or a frame cut in half,
// delivered a fragment, not a finish. The agent loop reads it exactly as it
// reads max_tokens: settle what showed, then ask the model to continue.
const stopInterrupted = "interrupted"

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
	// One definition per name, the first. Some providers refuse a whole
	// request that names a tool twice, and a caller that assembled its kit
	// from two sources (a writer's references and the turn's) sent exactly
	// that. First wins, which is also the agent loop's rule, so the schema
	// sent and the handler that runs are the same tool.
	out := make([]Tool, 0, len(tools))
	seen := make(map[string]bool, len(tools))
	for _, t := range tools {
		if seen[t.Name] {
			Debug("[llm] tool %q was offered twice in one request; the first definition was kept", t.Name)
			continue
		}
		seen[t.Name] = true
		t.Description = textutil.StripEmDashes(t.Description)
		t.Parameters = houseStyleParams(t.Parameters)
		out = append(out, t)
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

// WithEffort sets the provider-neutral reasoning level for this call: "off",
// "low", "medium" or "high". An explicit WithThinkBudget or WithThink(false) on
// the same call wins over it. See resolveEffort for the whole precedence.
func WithEffort(level string) ChatOption {
	return func(c *ChatConfig) { c.Effort = level }
}

// Effort levels. Plain strings rather than an exported type: they cross the
// admin form, the agent record and the tool schema as text, and every reader
// normalizes through normEffort anyway.
const (
	effortOff    = "off"
	effortLow    = "low"
	effortMedium = "medium"
	effortHigh   = "high"
)

// effortRank orders the levels so a tier's MaxEffort can cap them. 0 means
// "not a level".
func effortRank(level string) int {
	switch level {
	case effortOff:
		return 1
	case effortLow:
		return 2
	case effortMedium:
		return 3
	case effortHigh:
		return 4
	}
	return 0
}

// normEffort trims and lowercases a level and returns "" for anything that is
// not one, so a stray value in a stored record reads as "not set" rather than
// reaching a provider as a parameter it rejects.
func normEffort(level string) string {
	l := strings.ToLower(strings.TrimSpace(level))
	if effortRank(l) == 0 {
		return ""
	}
	return l
}

// effortTier is one tier's admin effort settings (LLMProviderConfig
// DefaultEffort / MaxEffort), carried by every client so each resolves a call
// the same way.
type effortTier struct {
	def string
	max string
}

func (e effortTier) resolve(cfg *ChatConfig) { resolveEffort(cfg, e.def, e.max) }

// resolveEffort settles the effort for one call, in place, before a client
// builds its request. Afterwards cfg.Effort is "" (no effort in play: the
// client behaves exactly as it did before effort existed) or a normalized
// level, and cfg.Think agrees with it.
//
// Precedence, highest first:
//  1. an explicit ThinkBudget: the raw token budget is the advanced override,
//     so effort steps aside entirely;
//  2. Think=false: a caller that turned thinking off meant it;
//  3. the per-call level (WithEffort, the agent's Effort);
//  4. the tier default - except that a default of "off" does not veto a
//     caller that explicitly asked for thinking (a "(thinking)" route stage);
//
// and whatever wins is then capped at the tier's max. Never scaled by prompt
// size: prompt size is not task difficulty (see the think-budget model).
func resolveEffort(cfg *ChatConfig, def, max string) {
	level := normEffort(cfg.Effort)
	cfg.Effort = ""
	if cfg.ThinkBudget != nil && *cfg.ThinkBudget > 0 {
		return
	}
	if cfg.Think != nil && !*cfg.Think {
		return
	}
	if level == "" {
		level = normEffort(def)
		if level == effortOff && cfg.Think != nil && *cfg.Think {
			level = ""
		}
	}
	if level == "" {
		return
	}
	if m := normEffort(max); m != "" && effortRank(level) > effortRank(m) {
		level = m
	}
	cfg.Effort = level
	on := level != effortOff
	if !on || cfg.Think == nil {
		cfg.Think = &on
	}
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
	BedrockAPI          string        // Which Bedrock API to speak: "converse" = the Converse API, any Bedrock model (needs bedrock:InvokeModel; the default for a new setup); "" / "messages" = the Messages-API endpoint, Claude only (needs bedrock-mantle:CreateInference); "invoke" = bedrock-runtime InvokeModel, Claude only (needs bedrock:InvokeModel).
	ContextSize         int           // Working context window (tokens). Ollama/llama.cpp: sent as num_ctx. Anthropic/Bedrock: the cap agent-loop history compaction keys on (0 = 200K default — deliberately under the 1M API window; see anthropicDefaultContextSize).
	ConnectTimeout      time.Duration // Dial timeout; defaults to 10s if zero.
	RequestTimeout      time.Duration // Per-Read idle deadline applied via iotimeout; defaults to 5min if zero. Long because non-streaming Gemini / model-listing calls need to ride out slow handshakes; streaming paths layer a shorter StreamIdleTimeout on top.
	StreamIdleTimeout   time.Duration // Idle-read deadline applied ONLY to streaming chat calls — if no bytes arrive for this long the body is closed and the error reads as transient so the retry layer can take another shot. Defaults to DefaultStreamIdleTimeout (60s) if zero. Tune up if the model legitimately stalls between tokens (heavy thinking budgets, cold prefills on a busy server).
	DisableThinking     bool          // Master override: forces think=false on every call regardless of per-call WithThink(true). Read by Gemini, Ollama and llama.cpp (and peers, which run through llama.cpp).
	ThinkingBudget      int           // Max thinking tokens per call, read by Gemini and llama.cpp (and peers); Ollama ignores it. 0 = model default. Ignored when DisableThinking is set.
	NativeTools         bool          // When true, use native function calling. When false, tools are described in the system prompt and parsed from <tool_call> tags. Default false for ollama models without tool support.
	OllamaMaxParallel   int           // Ollama only: global concurrency cap. 0 or negative = scheduler disabled; 1 = strict serial (default). Requests are fair-queued across sessions.
	LlamacppMaxParallel int           // llama.cpp and vLLM: global concurrency cap. Default 1 for llama.cpp (single-threaded), vllmDefaultMaxParallel for vLLM (it batches). Raise only when the server supports concurrent requests.
	// SeparateServer is a local model on a server of its own (the Judge
	// model): it neither starts nor waits in the process-wide Ollama or
	// llama.cpp/vLLM queue, which is the worker's server's. Waiting there
	// put every check behind the agents' rounds, the queue the Judge model
	// exists to leave; and starting it set the worker's cap to the judge's.
	SeparateServer bool
	// NoThink* fields control individual signals sent to llama.cpp on
	// WithThink(false) calls. Defaults are what's empirically proven
	// to work on Qwen 3 unified — kwarg + budget alone is sufficient,
	// the /no_think prepends are opt-in for models where kwarg fails.
	NoThinkUseKwarg      bool // llama.cpp: send chat_template_kwargs.enable_thinking=false. Default true (proven reliable disable on Qwen 3 unified + Gemma 4).
	NoThinkSendBudget    bool // llama.cpp: send thinking_budget_tokens cap. Default true (hard ceiling caught when kwarg slips).
	NoThinkPrependSystem bool // llama.cpp: prepend "/no_think " to system prompt. Default false (off because kwarg+budget alone works); enable as belt-and-suspenders for models where kwarg unreliable.
	NoThinkPrependUser   bool // llama.cpp: prepend "/no_think " to last user message. Default false (same reasoning as PrependSystem).
	NoThinkBudget        int  // llama.cpp: thinking_budget_tokens value when NoThinkSendBudget is true. 0 = llamacppNoThinkDefaultBudget (512).
	// DefaultEffort is the effort a call on this tier gets when it names none
	// ("" = model default, else off/low/medium/high). MaxEffort caps any level
	// a call or agent asks for ("" = no cap). Neither touches an explicit
	// token budget: ThinkingBudget above stays the llama.cpp ceiling.
	DefaultEffort string
	MaxEffort     string
	// NoImages keeps images from this model: each is described by a
	// configured model that reads them, or replaced with a note when there
	// is none (the image gate in llm.go). For a model that cannot see, and for one
	// that should not be shown pictures. Stored as the negative so every
	// existing configuration reads images as before.
	NoImages bool
	// awsKeys is the key pair a Bedrock tier signs with, when its key is
	// one (ACCESS_KEY_ID:SECRET_ACCESS_KEY) rather than a bearer token:
	// filled when the key is loaded, never stored.
	awsKeys *awsCreds
}

// bedrockAuth is how this config's Bedrock client signs.
func (cfg LLMProviderConfig) bedrockAuth() bedrockAuth {
	return bedrockAuth{bearer: cfg.APIKey, profile: cfg.Profile, keys: cfg.awsKeys}
}

// --- a tier's key ---------------------------------------------------------
//
// A tier keeps its key in its own table, encrypted (api_key), and beside it
// the address it was given for and when (api_key_for), as a connection keeps
// its own: it is sent only while the tier still goes to that address, so
// pointing a tier at another server does not carry the key there. A key kept
// before keys were bound to an address has no api_key_for, and is not sent.

// tierKeyAddress is where a provider's key goes when the tier gives no
// address of its own.
var tierKeyAddress = map[string]string{"anthropic": anthropicEndpoint, "openai": openAIEndpoint,
	"gemini": geminiEndpoint, "bedrock": "bedrock"}

// KeyAddress is the address the tier's key goes to: its endpoint, or its
// provider's own.
func (cfg LLMProviderConfig) KeyAddress() string {
	if e := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/"); e != "" {
		return e
	}
	return tierKeyAddress[strings.ToLower(strings.TrimSpace(cfg.Provider))]
}

// SaveKey keeps key as the tier's in table, for the address it now goes to.
// A key of its own replaces a Keystore key it named.
func (cfg LLMProviderConfig) SaveKey(db Database, table, key string) {
	db.CryptSet(table, "api_key", strings.TrimSpace(key))
	db.Set(table, "api_key_for", ConnectionSecret{Addresses: []string{cfg.KeyAddress()}, Set: time.Now()})
	db.Unset(table, tierKeyRef)
	db.Unset(table, tierKeyNone)
}

// tierKeyRef is the tier table's key naming a Keystore entry in place of a
// key of its own.
const tierKeyRef = "api_key_ref"

// TierKeystoreRef is the Keystore entry the tier in table takes its key
// from, or "".
func TierKeystoreRef(db Database, table string) string {
	var id string
	if db != nil {
		db.Get(table, tierKeyRef, &id)
	}
	return strings.TrimSpace(id)
}

// KeystoreFits refuses Keystore entry e as the tier's key: a signing set for
// a provider that takes a key, or an entry not given for the address the
// tier goes to. Bedrock takes either kind: a key pair or profile to sign
// with, or a Bedrock API key.
func (cfg LLMProviderConfig) KeystoreFits(e keystore.Entry) error {
	bedrock := strings.EqualFold(strings.TrimSpace(cfg.Provider), "bedrock")
	if e.Kind == keystore.KindAWS && !bedrock {
		return fmt.Errorf("%s is an AWS signing entry, and this provider takes a key", e.Name)
	}
	if !e.Covers(cfg.KeyAddress()) {
		return fmt.Errorf("the Keystore key %s is not given for %s: add that address to it under Services > Keystore, or pick another", e.Name, cfg.KeyAddress())
	}
	return nil
}

// SaveKeyRef makes the tier in table take its key from Keystore entry id,
// keeping no copy of its own; "" goes back to none.
func (cfg LLMProviderConfig) SaveKeyRef(db Database, table, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		db.Unset(table, tierKeyRef)
		return nil
	}
	e, ok := keystore.Get(RootDB, id)
	if !ok {
		return fmt.Errorf("no Keystore key %s", id)
	}
	if err := cfg.KeystoreFits(e); err != nil {
		return err
	}
	db.Unset(table, "api_key")
	db.Unset(table, "api_key_for")
	db.Unset(table, tierKeyNone)
	db.Set(table, tierKeyRef, id)
	return nil
}

// loadKeystoreKey puts Keystore entry id's key into cfg, checked as for a
// connection: the entry is there, of a kind the provider takes, and given
// for the address the tier goes to.
func (cfg *LLMProviderConfig) loadKeystoreKey(id string) error {
	e, ok := keystore.Get(RootDB, id)
	if !ok {
		return fmt.Errorf("it names a Keystore key that is gone: pick another")
	}
	if err := cfg.KeystoreFits(e); err != nil {
		return err
	}
	v, _ := keystore.Secret(RootDB, id)
	if e.Kind == keystore.KindAWS && v == "" {
		cfg.APIKey, cfg.awsKeys = "", nil
		if e.Profile != "" {
			cfg.Profile = e.Profile
		}
		return nil
	}
	cfg.UseKey(v)
	return nil
}

// KeyOptional reports whether the tier may go without a key: a local server
// (Ollama, llama.cpp, vLLM), Bedrock signing as this machine, or a provider
// at an address of its own. The official Anthropic, OpenAI and Gemini APIs
// always need one.
func (cfg LLMProviderConfig) KeyOptional() bool {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "anthropic", "openai":
		return strings.TrimSpace(cfg.Endpoint) != ""
	case "gemini":
		return false
	}
	return true
}

// tierKeyNone marks a tier set to go without a key, chosen: its form then
// does not offer a Keystore key in its place, and a save does not attach one.
const tierKeyNone = "api_key_none"

// ClearKey forgets the tier's key, its own and a Keystore key it named, and
// records that it goes without.
func (cfg LLMProviderConfig) ClearKey(db Database, table string) {
	db.Unset(table, "api_key")
	db.Unset(table, "api_key_for")
	db.Unset(table, tierKeyRef)
	db.Set(table, tierKeyNone, true)
}

// HasKey reports whether the tier holds a key of its own or names one.
func HasKey(db Database, table string) bool {
	if TierKeystoreRef(db, table) != "" {
		return true
	}
	key, _ := keptKey(db, table)
	return key != ""
}

// keystoreKind is the kind of Keystore entry key is for the tier: an AWS
// signing set for a Bedrock key pair, a key otherwise.
func (cfg LLMProviderConfig) keystoreKind(key string) string {
	if strings.EqualFold(strings.TrimSpace(cfg.Provider), "bedrock") {
		if _, err := awsKeyPair(key); err == nil {
			return keystore.KindAWS
		}
	}
	return keystore.KindKey
}

// tierLabel is what a tier is called on the Keystore's rows.
func tierLabel(table string) string {
	if table == LeadLLMTable {
		return "Lead model"
	}
	return "Worker model"
}

// KeepTypedKey keeps a key typed on the tier's form: in the Keystore, which
// the tier then names, when no entry there fits the tier's address yet, so
// the first key for a provider is kept once; otherwise as the tier's own,
// since with one already there a key typed here is a deliberate other one.
// It reports whether the key went into the Keystore.
func (cfg LLMProviderConfig) KeepTypedKey(db Database, table, key string) (bool, error) {
	addr := cfg.KeyAddress()
	kind := cfg.keystoreKind(key)
	if _, ok := keystore.NormalizeAddress(addr); !ok || RootDB == nil {
		cfg.SaveKey(db, table, key)
		return false, nil
	}
	if _, fits := keystore.FirstFit(RootDB, kind, []string{addr}); fits {
		cfg.SaveKey(db, table, key)
		return false, nil
	}
	e, _, err := keystore.Keep(RootDB, kind, tierLabel(table)+" key", []string{addr}, key)
	if err != nil {
		cfg.SaveKey(db, table, key)
		return false, nil
	}
	return true, cfg.SaveKeyRef(db, table, e.ID)
}

// DefaultKeystoreFor is the Keystore entry the tier in table is offered by
// default when it keeps no key yet: the first that fits its address.
func (cfg LLMProviderConfig) DefaultKeystoreFor(db Database, table string) string {
	if TierKeystoreRef(db, table) != "" {
		return ""
	}
	var none bool
	if db.Get(table, tierKeyNone, &none) && none {
		return "" // set to go without, by choice
	}
	if key, _ := keptKey(db, table); key != "" {
		return ""
	}
	if RootDB == nil {
		return ""
	}
	for _, e := range keystore.List(RootDB) {
		if cfg.KeystoreFits(e) == nil {
			return e.ID
		}
	}
	return ""
}

// MoveKeyToKeystore moves the tier's own key into the Keystore and points
// the tier at it: an entry already holding that key for the tier's address
// is reused, else one is made, named for the provider. It answers the
// entry's name.
func (cfg LLMProviderConfig) MoveKeyToKeystore(db Database, table string) (string, error) {
	key, given := keptKey(db, table)
	if key == "" {
		return "", fmt.Errorf("%s keeps no key of its own", tierLabel(table))
	}
	addrs := given.Addresses
	if len(addrs) == 0 {
		return "", fmt.Errorf("%s's key was kept before keys were bound to an address: give it again", tierLabel(table))
	}
	e, _, err := keystore.Keep(RootDB, cfg.keystoreKind(key), tierLabel(table)+" key", addrs, key)
	if err != nil {
		return "", err
	}
	if err := cfg.SaveKeyRef(db, table, e.ID); err != nil {
		return "", err
	}
	return e.Name, nil
}

// TierKeyHeld is the hint for a key the tier in table keeps of its own, and
// the address it was given for; "" when it keeps none (or names the
// Keystore).
func TierKeyHeld(db Database, table string) (hint string, addrs []string) {
	if TierKeystoreRef(db, table) != "" {
		return "", nil
	}
	key, given := keptKey(db, table)
	if key == "" {
		return "", nil
	}
	hint, v := "set", []rune(key)
	if len(v) >= 16 {
		hint = "ends in ..." + string(v[len(v)-4:])
	}
	if !given.Set.IsZero() {
		hint += ", given " + given.Set.Format("2 Jan 2006")
	}
	return hint, given.Addresses
}

// tiersUsing is which model tiers name Keystore entry id, as the Keystore's
// row says.
func tiersUsing(id string) []string {
	if RootDB == nil {
		return nil
	}
	var out []string
	for _, t := range []struct{ table, name string }{{LLMTable, "Worker model"}, {LeadLLMTable, "Lead model"}} {
		if TierKeystoreRef(RootDB, t.table) == id {
			out = append(out, t.name)
		}
	}
	return out
}

func init() {
	keystore.RegisterUse(tiersUsing)
	// A model client copies its key when it is built: a tier naming a
	// changed entry is rebuilt, or it would send the old key until the next
	// save or restart.
	keystore.OnChange(func(id string) {
		if len(tiersUsing(id)) == 0 {
			return
		}
		if err := ReloadLLMs(); err != nil {
			Warn("[keystore] reloading the models after a key change: %v", err)
		}
	})
}

// keptKey is the tier's key in table and where it was given for; given is
// zero for a key kept before keys were bound to an address.
func keptKey(db Database, table string) (key string, given ConnectionSecret) {
	if db == nil || !db.Get(table, "api_key", &key) || strings.TrimSpace(key) == "" {
		return "", given
	}
	db.Get(table, "api_key_for", &given)
	return key, given
}

// LoadKey puts the tier's key from table into cfg while it was given for the
// address the tier now goes to. A key given for another address, or kept
// before keys were bound to one, is not sent, and the error says to give it
// again.
func (cfg *LLMProviderConfig) LoadKey(db Database, table string) error {
	if id := TierKeystoreRef(db, table); id != "" {
		return cfg.loadKeystoreKey(id)
	}
	key, given := keptKey(db, table)
	switch {
	case key == "":
		return nil
	case len(given.Addresses) == 0:
		return fmt.Errorf("its key was kept before keys were bound to an address: give the key again")
	case !slices.Contains(given.Addresses, cfg.KeyAddress()):
		return fmt.Errorf("its key was given for %s, and it now goes to %s: give the key again", strings.Join(given.Addresses, ", "), cfg.KeyAddress())
	}
	cfg.UseKey(key)
	return nil
}

// KeptKey is what the tier's form says of its saved key, never the key: the
// last four characters of a long one, the day it was given, and whether it
// goes where the tier does. "" when none is saved.
func (cfg LLMProviderConfig) KeptKey(db Database, table string) string {
	if id := TierKeystoreRef(db, table); id != "" {
		e, ok := keystore.Get(RootDB, id)
		if !ok {
			return "names a Keystore key that is gone: pick another"
		}
		hint := "from the Keystore: " + e.Name + " (" + keystore.Hint(RootDB, e) + ")"
		if err := cfg.KeystoreFits(e); err != nil {
			hint += ": not sent, " + err.Error()
		}
		return hint
	}
	key, given := keptKey(db, table)
	if key == "" {
		return ""
	}
	hint, v := "set", []rune(key)
	if len(v) >= 16 {
		hint = "ends in ..." + string(v[len(v)-4:])
	}
	switch {
	case len(given.Addresses) == 0:
		return hint + ", kept before keys were bound to an address: not sent until given again"
	case !given.Set.IsZero():
		hint += ", given " + given.Set.Format("2 Jan 2006")
	}
	if !slices.Contains(given.Addresses, cfg.KeyAddress()) {
		hint += ", for " + strings.Join(given.Addresses, ", ") + ": not sent while the tier goes elsewhere"
	}
	return hint
}

// UseKey makes key the tier's: its API key, or for Bedrock a key pair
// (ACCESS_KEY_ID:SECRET_ACCESS_KEY) it signs with.
func (cfg *LLMProviderConfig) UseKey(key string) {
	cfg.APIKey, cfg.awsKeys = strings.TrimSpace(key), nil
	if strings.EqualFold(strings.TrimSpace(cfg.Provider), "bedrock") {
		if pair, err := awsKeyPair(key); err == nil {
			cfg.APIKey, cfg.awsKeys = "", &pair
		}
	}
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
	// noImages: this model is not shown images; they are described in their
	// place (LLMProviderConfig.NoImages, the image gate in llm.go).
	noImages bool
	// model names the model this client calls, provider/model: the subject of
	// what it learns per model (imagesSubject).
	model string
	// What it learns of the model's pictures are request fields
	// (request_adapt.go) with the model as their subject (imagesSubject):
	// that it takes none (learnNoImages), and how many it takes in one
	// request (learnImageLimit, applied by withinImageLimit).
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
	Log("[peer] %q refused our credential on a model call: dropped it so the next request re-exchanges", r.peer)
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
// string matching. Wrap with fmt.Errorf("%w: ...", ErrContextExceeded,
// ...) on the provider side; downstream code checks via errors.Is.
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
	var apiErr *APIError
	if errors.As(err, &apiErr) {
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
	if netErr, ok := err.(net.Error); ok && (netErr.Timeout() || netErr.Temporary()) { //nolint:staticcheck
		return true
	}
	// Unwrap and retry any net.OpError (e.g. "connection reset by peer",
	// "broken pipe"). The net.Error check above used to return its answer
	// outright, and the *url.Error every failed request comes back as is a
	// net.Error whose Timeout and Temporary are false for a reset or a broken
	// pipe, so this line was never reached for one. Observed: vLLM closed a
	// keep-alive connection idle for its five seconds, the next request wrote
	// into it, and the turn ended with no reply instead of retrying.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return false
}

// SystemOne passes a structured decision through to the model inside (core's
// vLLM client, see its SystemOne); a status of 0 is a model without one.
// Not retried: a decision that fails falls back to the first-token reading,
// and a check to its full question.
func (r *retryLLM) SystemOne(ctx context.Context, req map[string]any) (int, []byte, error) {
	if so, ok := r.inner.(interface {
		SystemOne(context.Context, map[string]any) (int, []byte, error)
	}); ok {
		return so.SystemOne(ctx, req)
	}
	return 0, nil, nil
}

func (r *retryLLM) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	return r.chat(ctx, messages, opts...)
}

func (r *retryLLM) chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	messages = r.withinImageLimit(ctx, r.withoutImages(ctx, messages))
	resp, err := doWithRetry(ctx, LLMMaxRetries(), opts, func() (*Response, error) {
		// First attempt: original opts, original messages.
		resp, err := r.inner.Chat(ctx, messages, opts...)
		if r.peerRefused(err) {
			// The credential is gone; this attempt resolves a new one.
			resp, err = r.inner.Chat(ctx, messages, opts...)
		}
		if r.learnImageLimit(err, messages) {
			// Too many pictures for this model: the same request, within its
			// limit.
			messages = r.withinImageLimit(ctx, messages)
			resp, err = r.inner.Chat(ctx, messages, opts...)
		}
		if r.learnNoImages(err, messages) {
			// No pictures at all for this model: the same request, each one
			// described by a model that reads them.
			messages = r.withoutImages(ctx, messages)
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
		Debug("[retry] unusable response (err=%v, %s): retrying with hint, thinking still enabled", err, respShape(resp))
		hinted := append([]Message{}, messages...)
		hinted = append(hinted, Message{
			Role:    "user",
			Content: "Your previous turn produced no output. Reason briefly, then either call a tool or send a text reply: do not end your turn empty.",
		})
		resp2, err2 := r.inner.Chat(ctx, hinted, opts...)
		if err2 == nil && responseIsUseable(resp2) {
			return resp2, nil
		}
		// Last-ditch retry: drop thinking entirely. Worse for tool
		// decisions but sometimes the only thing that produces output
		// when the model is wedged.
		Debug("[retry] still empty after hint: falling back to thinking disabled")
		f := false
		retryOpts := append(append([]ChatOption{}, opts...), WithThink(f))
		resp3, err3 := r.inner.Chat(ctx, hinted, retryOpts...)
		if err3 == nil && responseIsUseable(resp3) {
			return resp3, nil
		}
		// All paths empty — return the original.
		return resp, err
	})
	r.noteUnmappedRefusal(err)
	return resp, err
}

func (r *retryLLM) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	return r.chatStream(ctx, messages, handler, opts...)
}

func (r *retryLLM) chatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	messages = r.withinImageLimit(ctx, r.withoutImages(ctx, messages))
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
	resp, err := doWithRetry(ctx, LLMMaxRetries(), opts, func() (*Response, error) {
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
		// The same for a request with more pictures than the model takes: a
		// refusal streams nothing, so the retry within the limit is safe.
		if !handlerCalled && r.learnImageLimit(err, messages) {
			messages = r.withinImageLimit(ctx, messages)
			resp, err = r.inner.ChatStream(ctx, messages, wrappedHandler, opts...)
		}
		if !handlerCalled && r.learnNoImages(err, messages) {
			messages = r.withoutImages(ctx, messages)
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
			Debug("[retry] stream failed after %d partial chunk(s); caller discarded them, retrying: %v", chunks, err)
			return resp, err
		}
		if handlerCalled || !shouldRetryEmpty(resp, err) {
			return resp, err
		}
		// First retry: keep thinking ON, append a hint message.
		Debug("[retry] unusable stream response (err=%v, %s): retrying with hint, thinking still enabled", err, respShape(resp))
		hinted := append([]Message{}, messages...)
		hinted = append(hinted, Message{
			Role:    "user",
			Content: "Your previous turn produced no output. Reason briefly, then either call a tool or send a text reply: do not end your turn empty.",
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
		Debug("[retry] still empty after hint: falling back to thinking disabled")
		f := false
		retryOpts := append(append([]ChatOption{}, opts...), WithThink(f))
		handlerCalled = false
		resp3, err3 := r.inner.ChatStream(ctx, hinted, wrappedHandler, retryOpts...)
		if err3 == nil && responseIsUseable(resp3) {
			return resp3, nil
		}
		return resp, err
	})
	r.noteUnmappedRefusal(err)
	return resp, err
}

// ContextSize forwards the inner LLM's ContextSizer through the retry
// wrapper. Without it, T.LLM (a *retryLLM) fails the T.LLM.(ContextSizer)
// type assertion in WorkerContextSize()/LeadContextSize(), which then
// return 0 — silently disabling every context-size-dependent feature
// (history compaction, debate context math, ...). Returns 0 only when the
// inner LLM genuinely exposes no window.
func (r *retryLLM) ContextSize() int {
	if cs, ok := r.inner.(ContextSizer); ok {
		return cs.ContextSize()
	}
	return 0
}

// decodeClock times a stream's generation, from the first output it carries
// (thinking, text or tool-call arguments) to the last, for backends that do
// not report their own decode rate. The span starts at the first token rather
// than at the request, so prefill and queueing stay out of it; timed from the
// request, a long prompt reads as a slow model.
type decodeClock struct{ first, last time.Time }

// mark records that output arrived now.
func (d *decodeClock) mark() {
	now := time.Now()
	if d.first.IsZero() {
		d.first = now
	}
	d.last = now
}

// minDecodeSpan is the shortest span a rate is read from. A provider that
// delivers a short reply in one or two network bursts would otherwise report
// thousands of tokens a second.
const minDecodeSpan = 250 * time.Millisecond

// rate is tokens per second over the span. The first token arrives at the
// span's start, so tokens-1 were generated across it. Zero when the span is
// too short to mean anything.
func (d *decodeClock) rate(tokens int) float64 {
	span := d.last.Sub(d.first)
	if tokens < 2 || span < minDecodeSpan {
		return 0
	}
	return float64(tokens-1) / span.Seconds()
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
	// A refusal the client moved a request field on (request_adapt.go) is
	// retried with the field's next step, each a different request: down the
	// thinking-off ladder, an output maximum named, sampling left out. Up to
	// four of them in one call.
	offSteps := 0
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
		// The same holds for a reasoning_effort the model refused: the client
		// recorded the model (noteIfReasoningEffortRefused) and the rebuilt
		// request leaves the field out.
		if _, moved := adaptedField(err); moved && offSteps < 4 {
			offSteps++
			continue
		}
		if (isUnsupportedThinkingTypeErr(err) || isUnsupportedReasoningEffortErr(err)) && !thinkingRetried {
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
			Log("[retry] attempt %d/%d failed: %v, retrying in %v", attempt+1, maxRetries, err, backoff)
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
	// A peer model is served through the llama.cpp client (ResolveModelProvider
	// rewrites the provider before the client is built), so it calls tools
	// natively too. Judged on the raw "peer:<name>" string it fell through to
	// the fallback, and a peer-backed worker got prompt-parsed tool calls.
	if strings.HasPrefix(strings.TrimSpace(provider), peerProviderPrefix) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "bedrock", "openai", "gemini", "llama.cpp", "vllm":
		return true
	}
	// ollama (and anything unrecognized): depends on the model, so honor the
	// operator's toggle.
	return false
}

// vllmDefaultMaxParallel is the local scheduler's cap for a vLLM backend when
// the operator has not set one. vLLM batches concurrent requests, so the
// llama.cpp default of 1 would queue work the server could run together.
const vllmDefaultMaxParallel = 8

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
		// The official API needs a key; a compatible server at an address of
		// its own may take none.
		if cfg.APIKey == "" && strings.TrimSpace(cfg.Endpoint) == "" {
			return nil, Error("anthropic API key is not configured: set it in the web UI under Admin → Models")
		}
		model := cfg.Model
		if model == "" {
			model = "claude-sonnet-5"
		}
		ac := newAnthropicLLM(cfg.APIKey, model, api).(*anthropicClient)
		// An address of its own is an Anthropic-compatible gateway's.
		if strings.TrimSpace(cfg.Endpoint) != "" {
			if err := ac.at(cfg.Endpoint); err != nil {
				return nil, err
			}
		}
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
		switch cfg.BedrockAPI {
		case "converse":
			inner, err = newBedrockConverseLLM(cfg.bedrockAuth(), cfg.Model, cfg.Region, cfg.Endpoint, api)
		case "invoke":
			inner, err = newBedrockRuntimeLLM(cfg.bedrockAuth(), cfg.Model, cfg.Region, cfg.Endpoint, api)
		default:
			inner, err = newBedrockLLM(cfg.bedrockAuth(), cfg.Model, cfg.Region, cfg.Endpoint, api)
		}
		if err != nil {
			return nil, err
		}
		// Carry the working context cap so ContextSize() reflects the
		// operator's setting.
		switch c := inner.(type) {
		case *anthropicClient:
			c.contextSize = cfg.ContextSize
		case *bedrockRuntimeClient:
			c.contextSize = cfg.ContextSize
		case *bedrockConverseClient:
			c.rt.contextSize = cfg.ContextSize
		}
	case "openai":
		// The official API needs a key; a compatible server at an address of
		// its own may take none.
		if cfg.APIKey == "" && strings.TrimSpace(cfg.Endpoint) == "" {
			return nil, Error("openai API key is not configured: set it in the web UI under Admin → Models")
		}
		model := cfg.Model
		if model == "" {
			model = "gpt-4o"
		}
		// An address of its own is an OpenAI-compatible API's (Groq,
		// OpenRouter, a local server speaking OpenAI's API).
		inner = newOpenAILLM(cfg.APIKey, model, chooseStr(cfg.Endpoint, openAIEndpoint), api)
		inner.(*openAIClient).streamIdleTimeout = cfg.StreamIdleTimeout
	case "gemini":
		if cfg.APIKey == "" {
			return nil, Error("gemini API key is not configured: set it in the web UI under Admin → Models → Worker LLM")
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
		oc.ownServer = cfg.SeparateServer
		inner = client
		// Start (or adjust) the global Ollama scheduler so concurrent
		// sessions get fair-queued. Safe to call multiple times; the
		// second call adjusts MaxParallel on the running dispatcher.
		maxParallel := cfg.OllamaMaxParallel
		if maxParallel < 1 {
			maxParallel = 1
		}
		if !cfg.SeparateServer {
			StartOllamaScheduler(maxParallel)
		}
	case "llama.cpp", "vllm":
		// vLLM speaks the same OpenAI-compatible dialect and reads
		// chat_template_kwargs the same way, so it is this client with the
		// one llama.cpp-only field left off (see openAIClient.vllm).
		vllm := cfg.Provider == "vllm"
		ep := "http://localhost:8080/v1"
		if vllm {
			ep = "http://localhost:8000/v1"
		}
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
		oc.vllm = vllm
		oc.llamacppBudget = cfg.ThinkingBudget
		oc.disableThinking = cfg.DisableThinking
		oc.noThinkUseKwarg = cfg.NoThinkUseKwarg
		oc.noThinkSendBudget = cfg.NoThinkSendBudget
		oc.noThinkPrependSystem = cfg.NoThinkPrependSystem
		oc.noThinkPrependUser = cfg.NoThinkPrependUser
		oc.noThinkBudget = cfg.NoThinkBudget
		oc.contextSize = cfg.ContextSize
		oc.streamIdleTimeout = cfg.StreamIdleTimeout
		oc.ownServer = cfg.SeparateServer
		inner = client
		// Start the serializer so concurrent callers queue here instead
		// of racing to llama.cpp and getting 503s. Default 1 matches
		// llama.cpp's single-threaded design; configurable via admin UI.
		// One local server, one queue: vLLM shares the scheduler, which keeps
		// its calls on the Monitor page and caps a lent-out model. Unset, it
		// starts wider, because vLLM batches concurrent requests where stock
		// llama.cpp runs one at a time.
		mp := cfg.LlamacppMaxParallel
		if mp < 1 {
			mp = 1
			if vllm {
				mp = vllmDefaultMaxParallel
			}
		}
		if !cfg.SeparateServer {
			StartLlamacppScheduler(mp)
		}
	default:
		return nil, Error("no LLM provider is configured: set one in the web UI under Admin → Models → Worker LLM")
	}

	// Handed to every client rather than applied here, because only the
	// client knows its own dial (an effort string, a budget, a flag) and
	// resolution has to see the call's final options.
	tier := effortTier{def: normEffort(cfg.DefaultEffort), max: normEffort(cfg.MaxEffort)}
	switch c := inner.(type) {
	case *anthropicClient:
		c.effort = tier
	case *bedrockRuntimeClient:
		c.effort = tier
	case *bedrockConverseClient:
		c.rt.effort = tier
	case *openAIClient:
		c.effort = tier
	case *geminiClient:
		c.effort = tier
	}

	return &retryLLM{inner: inner, maxRetries: 5, peer: peerName, noImages: cfg.NoImages,
		model: strings.ToLower(strings.TrimSpace(cfg.Provider)) + "/" + chFirstNonEmpty(strings.TrimSpace(cfg.Model), "(provider default)")}, nil
}

// ListModels asks a provider which models it offers, from the same settings a
// client is built from, so the admin form can offer them instead of asking
// for an id typed from memory. The answer is ids exactly as the Model field
// takes them, sorted, and an error carries the provider's own reason: a key
// it refused, a permission the account lacks, an endpoint that is not there.
//
// A provider that serves models other than the chat ones (OpenAI lists its
// speech and image models beside them; Bedrock lists every vendor's) is
// narrowed to what a client of that provider here can actually talk to.
func ListModels(ctx context.Context, cfg LLMProviderConfig) ([]string, error) {
	resolved, err := ResolveModelProvider(cfg, cfg.Provider)
	if err != nil {
		return nil, err
	}
	cfg = resolved
	var ids []string
	switch cfg.Provider {
	case "anthropic":
		ids, err = anthropicModelList(ctx, cfg.APIKey, cfg.Endpoint)
	case "bedrock":
		ids, err = bedrockModelList(ctx, cfg)
	case "gemini":
		ids, err = geminiModelList(ctx, cfg.APIKey)
	case "openai":
		ids, err = openAIModelList(ctx, "openai", chooseStr(cfg.Endpoint, openAIEndpoint), cfg.APIKey, cfg.Endpoint == "")
	case "ollama":
		ids, err = ollamaModelList(ctx, chooseStr(cfg.Endpoint, ollamaEndpoint))
	case "llama.cpp", "vllm":
		ep := cfg.Endpoint
		if ep == "" {
			ep = "http://localhost:8080/v1"
			if cfg.Provider == "vllm" {
				ep = "http://localhost:8000/v1"
			}
		}
		ids, err = openAIModelList(ctx, cfg.Provider, ep, cfg.APIKey, false)
	case "":
		return nil, Error("pick a provider first")
	default:
		return nil, Error("listing models is not supported for provider " + cfg.Provider)
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := ids[:0]
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// getModelList GETs a provider's model listing into out. A refusal comes back
// as the provider's own message, which is the whole of what an operator needs
// to see under the field ("API key not valid", "not authorized to perform
// bedrock:ListFoundationModels").
func getModelList(ctx context.Context, provider, rawURL string, auth func(*http.Request), out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if auth != nil {
		auth(req)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return refused(&APIError{Provider: provider, StatusCode: resp.StatusCode, Message: modelListReason(body)}, "model listing")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s answered something other than a model list: %w", req.URL.Host, err)
	}
	return nil
}

// modelListReason pulls the message out of a provider's error body, which
// every provider here nests differently ({error:{message}}, {message},
// {Message}), falling back to the body itself.
func modelListReason(body []byte) string {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Upper   string          `json:"Message"`
	}
	if json.Unmarshal(body, &e) == nil {
		var inner struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &inner) == nil && inner.Message != "" {
			return inner.Message
		}
		var s string
		if json.Unmarshal(e.Error, &s) == nil && s != "" {
			return s
		}
		if e.Message != "" {
			return e.Message
		}
		if e.Upper != "" {
			return e.Upper
		}
	}
	return strings.TrimSpace(string(body))
}

// --- a scripted LLM, for tests -------------------------------------------------
//
// Every behaviour in the agent loop is a function of what the model said, so
// testing any of it means being able to say it on demand. Nineteen test files
// had each written their own stub to do that, all implementing the same two
// methods, each covering exactly what its own test needed and nothing more.
// The cost of that is not duplication — it is what does NOT get tested, because
// a scenario that needs one more capability than the local stub has means
// writing a twentieth stub, and the easier path is to test something else.
//
// It lives in package core, not a subpackage, because Go refuses an internal
// test file that imports a package which imports the package under test, and
// most of those nineteen are internal tests of core. It ships in the binary, in
// the same way httptest ships in the standard library: the alternative is
// carrying the same type in every package that needs it.
//
// Two things it does that the hand-rolled stubs mostly did not. It STREAMS —
// almost every stub made ChatStream call Chat, so the chunked path was never
// exercised by any of them, and chunk boundaries are where a class of real bugs
// lives. And it RECORDS what it was asked, so a test can assert on the prompt
// that was actually built rather than only on what came back.

// FakeTurn is one scripted reply. The zero value is a model that said nothing,
// which is itself a case worth testing.
type FakeTurn struct {
	// Content is the reply text.
	Content string
	// Chunks is how Content arrives when streamed. Empty means it arrives in
	// one piece. Set it to place a boundary exactly where a test needs one —
	// mid-sentence, inside a marker, between a word and its punctuation — which
	// is where streaming bugs live and what a stub that forwards ChatStream to
	// Chat can never reach. When set, Content is ignored for streaming and the
	// joined chunks are what the response carries.
	Chunks []string
	// ToolCalls make this a tool round rather than an answer.
	ToolCalls []ToolCall
	// Reasoning is the thinking channel, populated but not promoted.
	Reasoning string
	// StopReason is the provider's terminal signal ("end_turn", "max_tokens",
	// "refusal"). Empty is the common case; set it to test the guards that read
	// a truncation or a refusal apart from a clean finish.
	StopReason string
	// Err makes this call fail. The loop's error paths are as much of its
	// behaviour as its happy ones.
	Err error
	// Repeat makes this turn answer every remaining call, for a test that
	// cares what the model says but not how many times it is asked.
	Repeat bool
	// Wait, when set, blocks the call until the channel is closed. For the
	// tests that are about what happens WHILE a model is still thinking — a
	// second request arriving mid-call, a fold running behind a live turn —
	// which cannot be written at all against a model that answers instantly.
	Wait <-chan struct{}
	// Token counts, for the accounting paths. Zero unless a test is about them.
	InputTokens, OutputTokens int
}

// fakeCall is one thing the fake was asked.
type fakeCall struct {
	messages []Message
	opts     []ChatOption
	streamed bool
}

// FakeLLM is a scripted LLM: it answers with the turns it was given, in order,
// and remembers what it was asked.
//
// Running past the end of the script is an ERROR rather than a default reply.
// A loop that asks more times than a test scripted is either a runaway or a
// test that has drifted from what it is checking, and both are worth failing
// on. Set Repeat on the last turn where the count genuinely does not matter.
type FakeLLM struct {
	// Turns are consumed in order.
	Turns []FakeTurn

	mu    sync.Mutex
	calls []fakeCall
}

// Chat implements LLM.
func (f *FakeLLM) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	return f.answer(ctx, messages, nil, opts)
}

// ChatStream implements LLM, delivering the turn's chunks through handler
// before returning.
func (f *FakeLLM) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	return f.answer(ctx, messages, handler, opts)
}

func (f *FakeLLM) answer(ctx context.Context, messages []Message, handler StreamHandler, opts []ChatOption) (*Response, error) {
	f.mu.Lock()
	n := len(f.calls)
	// The messages are COPIED. The loop reuses and appends to its history
	// slice between rounds, so keeping the caller's slice would leave every
	// recorded call pointing at whatever the last one ended up being.
	f.calls = append(f.calls, fakeCall{
		messages: append([]Message(nil), messages...),
		opts:     opts,
		streamed: handler != nil,
	})
	turn, ok := f.turnFor(n)
	f.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("FakeLLM: call %d has no scripted turn (%d scripted), the loop asked more times than this test expects", n+1, len(f.Turns))
	}
	if turn.Wait != nil {
		// Before the error and the context check, so a test can hold a call
		// open and then decide how it ends.
		select {
		case <-turn.Wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Cancellation is checked after the call is recorded, so a test can still
	// see that the call was made before the context went.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	content := turn.Content
	if len(turn.Chunks) > 0 {
		content = strings.Join(turn.Chunks, "")
	}
	// Chunks are delivered BEFORE a failure, because that is the order a real
	// stream fails in: it emits, then dies on a deadline partway through. A
	// fake that failed first could not produce the case retry logic actually
	// turns on — whether anything already reached the caller — and that is the
	// difference between a safe retry and a duplicated reply.
	if handler != nil {
		for _, c := range turn.Chunks {
			handler(c)
		}
		if len(turn.Chunks) == 0 && content != "" && turn.Err == nil {
			handler(content)
		}
	}
	if turn.Err != nil {
		// The response goes back WITH the error, because a real client's does:
		// the prompt went out and the provider will bill it, so a failed call
		// still carries the tokens it sent. Returning nil here would make the
		// accounting paths untestable, which is how a deployment comes to
		// under-report its worst days.
		return &Response{InputTokens: turn.InputTokens, OutputTokens: turn.OutputTokens}, turn.Err
	}
	return &Response{
		Content:      content,
		Reasoning:    turn.Reasoning,
		ToolCalls:    turn.ToolCalls,
		StopReason:   turn.StopReason,
		InputTokens:  turn.InputTokens,
		OutputTokens: turn.OutputTokens,
	}, nil
}

// turnFor picks the turn for call n. Caller holds the lock.
func (f *FakeLLM) turnFor(n int) (FakeTurn, bool) {
	if n < len(f.Turns) {
		return f.Turns[n], true
	}
	if len(f.Turns) > 0 {
		if last := f.Turns[len(f.Turns)-1]; last.Repeat {
			return last, true
		}
	}
	return FakeTurn{}, false
}

// Calls reports how many times the model was asked.
func (f *FakeLLM) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// Sent returns the messages the i'th call received, or nil when there was no
// such call. This is the half that makes a prompt testable: what the loop
// BUILT, rather than only what it did with the reply.
func (f *FakeLLM) Sent(i int) []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.calls) {
		return nil
	}
	return f.calls[i].messages
}

// LastSent returns the messages of the most recent call.
func (f *FakeLLM) LastSent() []Message {
	f.mu.Lock()
	n := len(f.calls)
	f.mu.Unlock()
	return f.Sent(n - 1)
}

// Prompt returns the i'th call's messages flattened to text, for asserting
// that something reached the model without caring which message carried it.
func (f *FakeLLM) Prompt(i int) string {
	var b strings.Builder
	for _, m := range f.Sent(i) {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// Config returns the i'th call's options, resolved. The loop delivers a good
// deal as options rather than as messages — the system prompt among them — so a
// test asserting on what the model was told has to be able to look here as well
// as at the messages.
func (f *FakeLLM) Config(i int) ChatConfig {
	f.mu.Lock()
	var opts []ChatOption
	if i >= 0 && i < len(f.calls) {
		opts = f.calls[i].opts
	}
	f.mu.Unlock()
	var cfg ChatConfig
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// Streamed reports whether the i'th call came through ChatStream. Which path a
// surface takes is itself behaviour: a reply that streams and one that does not
// reach the user through different code.
func (f *FakeLLM) Streamed(i int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return i >= 0 && i < len(f.calls) && f.calls[i].streamed
}

// ---------------------------------------------------------------- image gate
//
// A model that does not read images (LLMProviderConfig.NoImages) never gets
// one. Every call to it passes through retryLLM, so the gate sits there: an
// image or a video in the messages is taken out before the call, and in its
// place goes a description written by a configured model that does read
// images, or, when there is none, a note that an image was there.
//
// Two reasons to turn it off, and the gate serves both. A model that cannot
// see answers better from a description than from an attachment it silently
// drops. And a remote model an owner would rather not show their pictures to
// gets only the words a local model wrote about them: with the worker on and
// the lead off, a photo never leaves the machine.
//
// The original stays in the conversation; only the copy sent to this model
// changes, so a later call to a model that can see still gets the picture.

// imageDescribePrompt is what the describing model is asked.
const imageDescribePrompt = `Describe this image for an assistant that cannot see it and has to answer questions about it. Say what it shows; copy any text in it exactly; give the layout, the people and their expressions, numbers, colours and anything unusual. Plain prose, no preamble, no opinion of the image.`

// imageFollowUpNote follows the descriptions a model that does not read images
// is handed: how to ask about a detail they leave out.
const imageFollowUpNote = `[For a detail a description does not give, call workspace(action="view_image") with path set to that picture's reference (its media#N or image#N as given with it, or its workspace path) and prompt set to your question: a model that reads images looks at the picture and answers.]`

// imageNote is what goes in an image's place when nothing could describe it.
const imageNote = "[An image was attached here. This model does not read images, and no model that does was available to describe it, so its contents are unknown.]"

// describeCacheMax bounds the descriptions kept: one turn re-sends its whole
// history every round, so a picture would otherwise be described per round.
const describeCacheMax = 128

var (
	describeMu    sync.Mutex
	describeCache = map[[32]byte]string{}
	describeOrder [][32]byte
)

// withoutImages returns messages with every image and video replaced by a
// description, for a model that does not read them. Messages carrying none
// are returned as they are, and the caller's slice is never changed.
func (r *retryLLM) withoutImages(ctx context.Context, messages []Message) []Message {
	if !r.blind() {
		return messages
	}
	carries := false
	for _, m := range messages {
		if len(m.Images) > 0 || len(m.Videos) > 0 {
			carries = true
			break
		}
	}
	if !carries {
		return messages
	}
	describer := imageDescriber(r)
	out := make([]Message, len(messages))
	copy(out, messages)
	for i, m := range out {
		if len(m.Images) == 0 && len(m.Videos) == 0 {
			continue
		}
		images := m.Images
		var notes []string
		if len(m.Videos) > 0 {
			if meta := ExtractVideosMetadata(m.Videos); meta != "" {
				notes = append(notes, meta)
			}
			images = append(append([][]byte(nil), images...), ExtractVideosFrames(m.Videos, VideoFrameSampleCount)...)
		}
		for n, img := range images {
			notes = append(notes, fmt.Sprintf("[Image %d, described by another model because this one does not read images: %s]", n+1, describeImage(ctx, describer, img)))
		}
		if len(images) == 0 && len(m.Videos) > 0 {
			notes = append(notes, "[A video was attached here, and no still could be taken from it.]")
		}
		// A picture handed to this model, which did not ask for it: the
		// description is all it has, so it is told how to look again. Only
		// when something read the picture; a note that nothing could has
		// nothing to ask about.
		if describer != nil && len(images) > 0 {
			notes = append(notes, imageFollowUpNote)
		}
		text := strings.TrimSpace(m.Content)
		if text != "" {
			notes = append([]string{text}, notes...)
		}
		m.Content = strings.Join(notes, "\n\n")
		m.Images, m.Videos = nil, nil
		out[i] = m
	}
	Debug("[llm] images held back from a model that does not read them (describer available: %v)", describer != nil)
	return out
}

// A model can read pictures and still take only so many in one request: vLLM
// refuses a prompt over its --limit-mm-per-prompt, and counts every picture
// in the history, not only the new ones. Observed: four parallel image finds
// queued four pictures for the next round, the worker refused the request
// with "At most 2 image(s) may be provided in one prompt", and the turn
// failed outright.
//
// So the limit is learned from that refusal and kept: the request is retried
// with the newest pictures that fit, and every older one is described by the
// same model, one picture at a time, so it stays on the machine it was
// already going to. Every call after is trimmed before it is sent.

// imageLimitRE reads a provider refusing a request for carrying too many
// pictures, in vLLM's words.
var imageLimitRE = regexp.MustCompile(`(?i)at most (\d+) image(?:\(s\)|s)? may be provided`)

// imageLimitFrom is the per-request picture limit err reports, or 0.
func imageLimitFrom(err error) int {
	if err == nil {
		return 0
	}
	m := imageLimitRE.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// countImages is how many pictures messages carry.
func countImages(messages []Message) (n int) {
	for _, m := range messages {
		n += len(m.Images)
	}
	return n
}

// blind reports whether this model is not shown images: set so ("Reads
// images" off), or learned from its own refusal.
func (r *retryLLM) blind() bool {
	return r.noImages || adaptedStep(r.imagesSubject(), fieldImages) == "describe"
}

// fieldImages is whether a model is sent pictures, a request field
// (request_adapt.go): sent until it refuses a request for carrying them, then
// described by a model that reads them.
const fieldImages = "images"

func init() {
	registerRequestField(requestField{
		key: fieldImages,
		top: "send",
		next: func(current string, err error) (string, bool) {
			if current == "send" && imagesRefusedRE.MatchString(err.Error()) {
				return "describe", true
			}
			return "", false
		},
		said: func(model, _, _ string, err error) {
			Log("[llm] %s refused a request for carrying pictures (%v): from now on its pictures are described by a model that reads them", model, err)
		},
		label: "Pictures",
		sends: func(string) string { return "a description of each picture, by a model that reads them" },
	})
}

// imagesSubject is the images field's subject: the model this client calls,
// so what one tier learns about a model holds for every tier on it and
// survives a reload; the client itself when it names no model (a test's).
func (r *retryLLM) imagesSubject() string {
	if r.model != "" {
		return r.model
	}
	return fmt.Sprintf("client:%p", r)
}

// imagesRefusedRE reads a provider refusing a request because the model takes
// no pictures at all, in the words llama.cpp, vLLM, OpenAI and routers use. A
// picture in a format the model cannot read ("unsupported image type") is not
// this: one odd file must not mark a model that sees as blind.
var imagesRefusedRE = regexp.MustCompile(`(?i)(image inputs? (is|are) not supported|do(es)?( no|n')t support (image|images|vision|multi-?modal)|not a multi-?modal model|image_url is only supported|at most 0 image|vision is not supported|support image input|multi-?modal inputs? (is|are) not supported)`)

// learnNoImages marks this model as not reading images when err is it
// refusing messages for carrying pictures, and says whether messages can be
// retried without them. Learned for the life of the process, and said once:
// the setting that keeps it is "Reads images" in Admin > Models. Without it,
// an owner who left the switch on for a model that cannot see had every
// picture fail the request, or worse, ignored.
func (r *retryLLM) learnNoImages(err error, messages []Message) bool {
	if err == nil || r.blind() || !carriesImages(messages) {
		return false
	}
	return adaptFrom(r.imagesSubject(), fieldImages, err)
}

// carriesImages reports whether messages hold any picture or video.
func carriesImages(messages []Message) bool {
	for _, m := range messages {
		if len(m.Images) > 0 || len(m.Videos) > 0 {
			return true
		}
	}
	return false
}

// learnImageLimit keeps the limit err reports and says whether messages can
// be retried within it. A limit of none is not learned: a model that reads no
// pictures is the NoImages setting.
func (r *retryLLM) learnImageLimit(err error, messages []Message) bool {
	n, have := imageLimitFrom(err), countImages(messages)
	if n < 1 || have <= n {
		return false
	}
	if r.imageLimitNow() != n {
		setAdaptedStep(r.imagesSubject(), fieldImageLimit, strconv.Itoa(n), err)
	}
	return true
}

// fieldImageLimit is how many pictures a model takes in one request, a
// request field (request_adapt.go): any number until a refusal names a
// limit, then that limit; a lower one named later lowers it again.
const fieldImageLimit = "images.limit"

func init() {
	registerRequestField(requestField{
		key: fieldImageLimit,
		top: "any",
		next: func(current string, err error) (string, bool) {
			n := imageLimitFrom(err)
			if was, _ := strconv.Atoi(current); n < 1 || (current != "any" && was <= n) {
				return "", false
			}
			return strconv.Itoa(n), true
		},
		said: func(model, _, to string, _ error) {
			Log("[llm] %s takes at most %s pictures in one request: requests now carry the newest %s, and older ones as descriptions", model, to, to)
		},
		label: "Pictures in one request",
		sends: func(step string) string { return "the newest " + step + ", and older ones as descriptions" },
	})
}

// imageLimitNow is how many pictures the model takes in one request, 0 for
// not known.
func (r *retryLLM) imageLimitNow() int {
	n, _ := strconv.Atoi(adaptedStep(r.imagesSubject(), fieldImageLimit))
	return n
}

// withinImageLimit returns messages carrying at most the model's limit of
// pictures, the newest kept and each older one replaced by its description.
// The caller's slice is never changed.
func (r *retryLLM) withinImageLimit(ctx context.Context, messages []Message) []Message {
	n := r.imageLimitNow()
	if n < 1 || countImages(messages) <= n {
		return messages
	}
	out := make([]Message, len(messages))
	copy(out, messages)
	keep := n
	for i := len(out) - 1; i >= 0; i-- {
		m := out[i]
		if len(m.Images) == 0 {
			continue
		}
		if keep >= len(m.Images) {
			keep -= len(m.Images)
			continue
		}
		held := m.Images[:len(m.Images)-keep]
		m.Images = append([][]byte(nil), m.Images[len(m.Images)-keep:]...)
		if len(m.Images) == 0 {
			m.Images = nil
		}
		keep = 0
		var notes []string
		if text := strings.TrimSpace(m.Content); text != "" {
			notes = append(notes, text)
		}
		for _, img := range held {
			notes = append(notes, fmt.Sprintf("[A picture here is not shown, because this model reads at most %d in one request. What it shows: %s]", n, describeImage(ctx, r, img)))
		}
		m.Content = strings.Join(notes, "\n\n")
		out[i] = m
	}
	Debug("[llm] %d picture(s) in the request, %d kept for a model that takes %d", countImages(messages), n, n)
	return out
}

// imageDescriber is a configured model that reads images, worker first: on
// the usual setup that is the local one, so a picture is described without
// leaving the machine. Never the model asking.
func imageDescriber(asking *retryLLM) LLM {
	for _, m := range []LLM{SharedWorkerLLM(), SharedLeadLLM()} {
		r, ok := m.(*retryLLM)
		if !ok || r == asking || r.blind() {
			continue
		}
		return r
	}
	return nil
}

// ImagesReadable reports whether any configured model reads images. False
// only when every one has "Reads images" off (LLMProviderConfig.NoImages):
// then a picture can be neither shown to a model nor described by one, and a
// tool about to look at one says so instead of asking a model about a note
// that stands where the picture was. No model configured is not that case.
func ImagesReadable() bool {
	configured := false
	for _, m := range []LLM{SharedWorkerLLM(), SharedLeadLLM()} {
		if m == nil {
			continue
		}
		configured = true
		if r, ok := m.(*retryLLM); !ok || !r.blind() {
			return true
		}
	}
	return !configured
}

// describeImage is one picture's description, from the cache when it has
// been described before.
func describeImage(ctx context.Context, describer LLM, img []byte) string {
	if describer == nil {
		return imageNote
	}
	key := sha256.Sum256(img)
	describeMu.Lock()
	if d, ok := describeCache[key]; ok {
		describeMu.Unlock()
		return d
	}
	describeMu.Unlock()
	resp, err := describer.Chat(ctx, []Message{{Role: "user", Content: imageDescribePrompt, Images: [][]byte{img}}}, WithThink(false))
	if err != nil || resp == nil || strings.TrimSpace(resp.Content) == "" {
		Debug("[llm] describing an image for a model that does not read them failed: %v", err)
		return "it could not be described (the describing model failed), so its contents are unknown"
	}
	d := strings.TrimSpace(resp.Content)
	describeMu.Lock()
	if _, ok := describeCache[key]; !ok {
		describeCache[key] = d
		describeOrder = append(describeOrder, key)
		if len(describeOrder) > describeCacheMax {
			delete(describeCache, describeOrder[0])
			describeOrder = describeOrder[1:]
		}
	}
	describeMu.Unlock()
	return d
}
