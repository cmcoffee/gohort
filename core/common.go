package core

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cmcoffee/snugforge/cfg"
	"github.com/cmcoffee/snugforge/eflag"
	"github.com/cmcoffee/snugforge/nfo"
	"github.com/cmcoffee/snugforge/swapreader"
	"github.com/cmcoffee/snugforge/xsync"
)

// err_table stores error messages for reporting.
var err_table *Table

// SetErrTable sets the error table.
func SetErrTable(input Table) {
	err_table = &input
	err_table.Drop()
}

// FlagSet encapsulates command-line flags and provides parsing functionality.
type FlagSet struct {
	FlagArgs []string
	*eflag.EFlagSet
}

// FuzzAgent encapsulates the components required to execute an agent.
type FuzzAgent struct {
	Flags   FlagSet
	DB      Database
	Cache   Database
	Report  *TaskReport
	Limiter LimitGroup
	LLM          LLM     // Primary (worker) LLM — used for most calls.
	LeadLLM      LLM     // Lead (judge) LLM — used for high-precision calls. Falls back to LLM if nil.
	LeadFallback bool    // Set to true if any lead LLM call fell back to the primary during this session.

	// MaxRounds limits how many LLM call rounds Run() will perform.
	// Set this in Init() or Main(). Default is 10 if unset.
	MaxRounds int

	// systemPrompt is populated by the framework from the Agent's
	// SystemPrompt() method before Main() is called.
	systemPrompt string

	// PromptTools when true makes Run() describe tools in the system prompt
	// instead of using native function calling. See AgentLoopConfig.PromptTools.
	PromptTools bool

	// tools holds tool names set via SetTools(), resolved at Run() time.
	tools []string
}

// Get returns the FuzzAgent instance itself.
func (T *FuzzAgent) Get() *FuzzAgent {
	return T
}

// SystemPrompt returns the default system prompt (empty).
// Agents override this method to provide their own system prompt.
func (T *FuzzAgent) SystemPrompt() string {
	return ""
}

// SetSystemPrompt stores the system prompt resolved from the Agent interface.
// Called by the framework before Main().
func (T *FuzzAgent) SetSystemPrompt(prompt string) {
	T.systemPrompt = prompt
}

// SetTools sets the tool names to resolve from the registry when Run() is called.
func (T *FuzzAgent) SetTools(names ...string) {
	T.tools = names
}

// RequireLLM returns an error if no LLM is configured.
func (T *FuzzAgent) RequireLLM() error {
	if T.LLM == nil {
		return fmt.Errorf("LLM is required, run --setup")
	}
	return nil
}

// PingLLM performs a connectivity check against the worker LLM.
// Returns an error if the LLM is unreachable or the call fails.
// Use this at the start of long-running pipelines to fail fast instead of
// burning through every step with the same connection error.
//
// If the LLM implements Pinger (Ollama does, via GET /api/ps), a short
// 10-second probe is used — it bypasses the fair-queue scheduler and
// returns immediately regardless of in-flight generation. Otherwise we
// fall back to a real chat call with a generous 5-minute timeout, since
// that call may have to wait behind an in-flight long-running request
// and a short timeout would produce false negatives under load.
func (T *FuzzAgent) PingLLM(ctx context.Context) error {
	if T.LLM == nil {
		return fmt.Errorf("LLM not configured")
	}
	if p, ok := T.LLM.(Pinger); ok {
		ping_ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := p.Ping(ping_ctx); err != nil {
			return fmt.Errorf("worker LLM unavailable: %w", err)
		}
		return nil
	}
	ping_ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, err := T.LLM.Chat(ping_ctx, []Message{
		{Role: "user", Content: "ping"},
	}, WithMaxTokens(4), WithThink(false))
	if err != nil {
		return fmt.Errorf("worker LLM unavailable: %w", err)
	}
	return nil
}

// PingLeadLLM performs a quick connectivity check against the lead LLM.
// If no lead LLM is configured, returns nil (the primary handles fallback).
// Prefers the Pinger fast path when available (e.g. Ollama /api/ps) so
// the probe isn't queued behind an in-flight long-running call.
func (T *FuzzAgent) PingLeadLLM(ctx context.Context) error {
	if T.LeadLLM == nil {
		return nil
	}
	if p, ok := T.LeadLLM.(Pinger); ok {
		ping_ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := p.Ping(ping_ctx); err != nil {
			return fmt.Errorf("lead LLM unavailable: %w", err)
		}
		return nil
	}
	ping_ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := T.LeadLLM.Chat(ping_ctx, []Message{
		{Role: "user", Content: "ping"},
	}, WithMaxTokens(4), WithThink(false))
	if err != nil {
		return fmt.Errorf("lead LLM unavailable: %w", err)
	}
	return nil
}

// GetLeadLLM returns the lead LLM if configured, otherwise falls back to the primary LLM.
func (T *FuzzAgent) GetLeadLLM() LLM {
	if T.LeadLLM != nil {
		return T.LeadLLM
	}
	return T.LLM
}

// WorkerContextSize returns the worker LLM's context window size, or 0
// if the LLM doesn't implement ContextSizer.
func (T *FuzzAgent) WorkerContextSize() int {
	if cs, ok := T.LLM.(ContextSizer); ok {
		return cs.ContextSize()
	}
	return 0
}

// LLMTier selects which LLM tier a Session routes to. Worker is the
// primary/local tier; Lead is the precision/judge tier (which may
// fall back to Worker if not configured separately).
type LLMTier int

const (
	WORKER LLMTier = iota
	LEAD
)

// Session is a logical unit of LLM work tagged with a unique caller
// ID. Every call through the session carries the same UUID, so the
// Ollama fair-queueing scheduler treats them as one caller competing
// fairly against other concurrent sessions.
//
// Create a session per logical unit of work (pipeline run, user chat,
// batch job). Two users chatting at once → two sessions → round-robin
// fairness. A pipeline fanning out 10 worker calls → one session →
// all share a queue, other sessions still get turns between them.
//
// Pick the tier at creation via CreateSession(WORKER) or
// CreateSession(LEAD). If both tiers point at the same Ollama
// endpoint, create one session per tier so each gets its own queue
// identity (competing as separate callers for the same GPU).
type Session struct {
	CallerID string
	Tier     LLMTier
	agent    *FuzzAgent
}

// CreateSession returns a new LLM session for the given tier with a
// fresh UUID as the caller ID.
func (T *FuzzAgent) CreateSession(tier LLMTier) *Session {
	return &Session{CallerID: UUIDv4(), Tier: tier, agent: T}
}

// Chat dispatches to WorkerChat or LeadChat based on the session's
// tier, with the session's caller ID prepended. Any explicit
// WithCaller later in opts overrides it.
func (s *Session) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	opts = prependCaller(s.CallerID, opts)
	if s.Tier == LEAD {
		return s.agent.LeadChat(ctx, messages, opts...)
	}
	return s.agent.WorkerChat(ctx, messages, opts...)
}

// ChatStream dispatches to the tier's LLM ChatStream with the
// session's caller ID attached. For LEAD, falls back to the worker
// LLM if GetLeadLLM returns nil.
func (s *Session) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	opts = prependCaller(s.CallerID, opts)
	var llm LLM
	if s.Tier == LEAD {
		llm = s.agent.GetLeadLLM()
	} else {
		llm = s.agent.LLM
	}
	return llm.ChatStream(ctx, messages, handler, opts...)
}

// prependCaller inserts WithCaller(id) at the start of opts so later
// opts can still override via their own WithCaller.
func prependCaller(id string, opts []ChatOption) []ChatOption {
	out := make([]ChatOption, 0, len(opts)+1)
	out = append(out, WithCaller(id))
	out = append(out, opts...)
	return out
}

// LeadChat calls the lead LLM and tallies token usage.
// If the lead LLM fails and a separate primary LLM is available, it falls
// back to the primary so the session can continue rather than aborting.
func (T *FuzzAgent) LeadChat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	// Honor routing config: if a route key was supplied via WithRouteKey
	// and the stage is configured for "worker", delegate transparently.
	var probe ChatConfig
	for _, opt := range opts {
		opt(&probe)
	}
	if probe.RouteKey != "" && !RouteToLead(probe.RouteKey) {
		if think := RouteThink(probe.RouteKey); think != nil {
			opts = append(opts, WithThink(*think))
			Debug("[llm] %s routed to worker LLM with thinking (routing config)", probe.RouteKey)
		} else {
			opts = append(opts, WithThink(false))
			Debug("[llm] %s routed to worker LLM (routing config)", probe.RouteKey)
		}
		return T.WorkerChat(ctx, messages, opts...)
	}
	lead := T.GetLeadLLM()
	start := time.Now()
	resp, err := lead.Chat(ctx, messages, opts...)
	elapsed := time.Since(start)
	if err != nil && T.LeadLLM != nil && T.LLM != nil && T.LeadLLM != T.LLM {
		Debug("[llm] lead chat failed after %s: %s — falling back to primary", elapsed.Round(time.Millisecond), err)
		T.LeadFallback = true
		start = time.Now()
		resp, err = T.LLM.Chat(ctx, messages, opts...)
		elapsed = time.Since(start)
		if err != nil {
			Debug("[llm] primary fallback also failed after %s: %s", elapsed.Round(time.Millisecond), err)
			return nil, err
		}
		Debug("[llm] primary fallback completed in %s (input: %d, output: %d tokens)", elapsed.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	} else if err != nil {
		Debug("[llm] lead chat failed after %s: %s", elapsed.Round(time.Millisecond), err)
		return nil, err
	} else if resp.OutputTokens == 0 && resp.Content == "" && T.LeadLLM != nil && T.LLM != nil && T.LeadLLM != T.LLM {
		// Lead returned empty output (possible safety filter) — fall back to primary.
		Debug("[llm] lead chat returned empty after %s (input: %d, thinking: %d) — falling back to primary", elapsed.Round(time.Millisecond), resp.InputTokens, len(resp.Reasoning))
		T.LeadFallback = true
		start = time.Now()
		resp, err = T.LLM.Chat(ctx, messages, opts...)
		elapsed = time.Since(start)
		if err != nil {
			Debug("[llm] primary fallback also failed after %s: %s", elapsed.Round(time.Millisecond), err)
			return nil, err
		}
		Debug("[llm] primary fallback completed in %s (input: %d, output: %d tokens)", elapsed.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	} else {
		Debug("[llm] lead chat completed in %s (input: %d, output: %d tokens)", elapsed.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	}
	if T.Report != nil {
		T.Report.Tally("Input Tokens").Add(resp.InputTokens)
		T.Report.Tally("Output Tokens").Add(resp.OutputTokens)
	}
	return resp, nil
}

// MailConfig holds SMTP mail settings.
type MailConfig struct {
	Server    string // SMTP server host:port (e.g. "smtp.gmail.com:587")
	From      string // Sender email address
	Username  string // SMTP auth username
	Password  string // SMTP auth password
	Recipient string // Default report recipient email address
}

// WebSearchConfig holds web search provider settings.
type WebSearchConfig struct {
	Provider  string // Search provider: "duckduckgo", "brave", "google", "searxng"
	APIKey    string // API key (not required for duckduckgo/searxng)
	Endpoint  string // Custom endpoint (for searxng instances)
}

// LoadWebSearchConfigFunc is set by the application to load search settings from the database.
var LoadWebSearchConfigFunc func() WebSearchConfig

// LoadWebSearchConfig returns the stored web search configuration.
func LoadWebSearchConfig() WebSearchConfig {
	if LoadWebSearchConfigFunc != nil {
		return LoadWebSearchConfigFunc()
	}
	return WebSearchConfig{}
}

// LoadMailConfigFunc is set by the application to load mail settings from the database.
var LoadMailConfigFunc func() MailConfig

// LoadMailConfig returns the stored mail configuration.
func LoadMailConfig() MailConfig {
	if LoadMailConfigFunc != nil {
		return LoadMailConfigFunc()
	}
	return MailConfig{}
}

// GhostConfig holds CMS connection details.
type GhostConfig struct {
	URL    string
	APIKey string
}

// LoadGhostConfigFunc is set by the application to load CMS settings from the database.
var LoadGhostConfigFunc func() GhostConfig

// LoadGhostConfig returns the stored CMS configuration.
func LoadGhostConfig() GhostConfig {
	if LoadGhostConfigFunc != nil {
		return LoadGhostConfigFunc()
	}
	return GhostConfig{}
}

// RouteStage represents a lead-LLM call site that can be optionally
// downgraded to the worker LLM via the routing menu. Apps self-register
// their stages via RegisterRouteStage in init().
type RouteStage struct {
	Key   string // db key, e.g. "myapp.stage_name"
	Label string // menu label
}

var routeRegistry struct {
	mu     sync.RWMutex
	stages []RouteStage
	byKey  map[string]bool
}

// RegisterRouteStage registers a routable lead-LLM call site so it appears
// in the routing menu. Default routing is lead; users can opt into worker.
func RegisterRouteStage(s RouteStage) {
	routeRegistry.mu.Lock()
	defer routeRegistry.mu.Unlock()
	if routeRegistry.byKey == nil {
		routeRegistry.byKey = make(map[string]bool)
	}
	if routeRegistry.byKey[s.Key] {
		return
	}
	routeRegistry.byKey[s.Key] = true
	routeRegistry.stages = append(routeRegistry.stages, s)
}

// ListRouteStages returns all registered route stages in registration order.
func ListRouteStages() []RouteStage {
	routeRegistry.mu.RLock()
	defer routeRegistry.mu.RUnlock()
	out := make([]RouteStage, len(routeRegistry.stages))
	copy(out, routeRegistry.stages)
	return out
}

// LookupRouteFunc is set by the application to read a route stage's
// current setting from the database. Returns "worker" or "" (lead).
var LookupRouteFunc func(key string) string

// RouteToLead returns true if the named route stage should use the lead
// LLM. Registered stages default to lead; users can opt into worker via
// the routing menu. Returns true for unknown keys (safe default).
func RouteToLead(key string) bool {
	if LookupRouteFunc == nil {
		return true
	}
	val := LookupRouteFunc(key)
	return val != "worker" && val != "worker (thinking)"
}

// RouteThink returns a thinking override for the named route stage, or
// nil if the stage should use its hardcoded default. When a stage is
// configured as "worker (thinking)" in the routing menu, this forces
// thinking on regardless of what the code passes to WithThink.
func RouteThink(key string) *bool {
	if LookupRouteFunc == nil {
		return nil
	}
	val := LookupRouteFunc(key)
	if val == "worker (thinking)" {
		t := true
		return &t
	}
	return nil
}

// RunAgentFunc is set by the application to enable agent-to-agent delegation.
var RunAgentFunc func(name string, args []string) (string, error)

// DelegateAgent runs another agent by name and returns its captured output.
func (T *FuzzAgent) DelegateAgent(name string, args ...string) (string, error) {
	if RunAgentFunc == nil {
		return "", fmt.Errorf("agent delegation is not available")
	}
	return RunAgentFunc(name, args)
}

// WorkerChat calls T.LLM.Chat and tallies token usage on T.Report.
func (T *FuzzAgent) WorkerChat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	start := time.Now()
	resp, err := T.LLM.Chat(ctx, messages, opts...)
	elapsed := time.Since(start)
	if err != nil {
		Debug("[llm] chat failed after %s: %s", elapsed.Round(time.Millisecond), err)
		return resp, err
	}
	Debug("[llm] chat completed in %s (input: %d, output: %d tokens)", elapsed.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	T.trackTokens(resp)
	return resp, nil
}

// WorkerChatWithCalc is like WorkerChat but includes the calculator tool.
// If the LLM calls the calculator, executes it and sends the result back
// for a final answer. Handles at most 3 rounds of tool calls.
//
// Works with both native tool calling and prompt-based fallback:
// - Native: passes Tool definitions via WithTools, handles ToolCall responses
// - Prompt-based: injects tool description into system prompt, parses <tool_call> tags
func (T *FuzzAgent) WorkerChatWithCalc(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	calc, ok := FindChatTool("calculate")
	if !ok {
		return T.WorkerChat(ctx, messages, opts...)
	}

	calcTool := Tool{
		Name:        calc.Name(),
		Description: calc.Desc(),
		Parameters:  calc.Params(),
	}
	calcDef := AgentToolDef{Tool: calcTool, Handler: calc.Run}
	handlers := map[string]ToolHandlerFunc{calc.Name(): calc.Run}

	// Pass native tool definition -- the LLM layer will strip it if
	// native tools are disabled, and the prompt-based fallback below
	// will handle that case.
	nativeOpts := append(append([]ChatOption{}, opts...), WithTools([]Tool{calcTool}))

	// Also inject the tool into the system prompt for models without
	// native support. We append to whichever system prompt is already set.
	promptToolText := BuildToolPrompt([]AgentToolDef{calcDef})

	history := make([]Message, len(messages))
	copy(history, messages)

	for round := 0; round < 3; round++ {
		callOpts := nativeOpts
		// Inject prompt-based tool description into system prompt.
		// This is additive -- models with native support will use
		// the native path and ignore the text, models without will
		// see the prompt and use <tool_call> tags.
		callOpts = append(callOpts, appendSystemPrompt(promptToolText))

		resp, err := T.WorkerChat(ctx, history, callOpts...)
		if err != nil {
			return resp, err
		}

		// Check for native tool calls first.
		if len(resp.ToolCalls) > 0 {
			history = append(history, Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})
			var results []ToolResult
			for _, tc := range resp.ToolCalls {
				result, runErr := calc.Run(tc.Args)
				if runErr != nil {
					results = append(results, ToolResult{ID: tc.ID, Content: runErr.Error(), IsError: true})
				} else {
					results = append(results, ToolResult{ID: tc.ID, Content: result})
				}
				Debug("[calc] %s -> %s", tc.Args["expression"], result)
			}
			history = append(history, Message{Role: "tool", ToolResults: results})
			continue
		}

		// Check for prompt-based <tool_call> tags.
		tc, preamble := ParsePromptToolCall(resp.Content, handlers)
		if tc == nil {
			return resp, nil
		}
		result, runErr := calc.Run(tc.Args)
		var resultText string
		if runErr != nil {
			resultText = "Error: " + runErr.Error()
		} else {
			resultText = result
		}
		Debug("[calc] %s -> %s", tc.Args["expression"], resultText)

		if preamble != "" {
			history = append(history, Message{Role: "assistant", Content: preamble})
		}
		history = append(history, Message{Role: "user", Content: fmt.Sprintf("Tool result: %s\n\nContinue your response using this result.", resultText)})
	}

	// Max rounds hit, do a final call without tools to force a text response.
	return T.WorkerChat(ctx, history, opts...)
}

// appendSystemPrompt returns a ChatOption that appends text to the existing
// system prompt rather than replacing it.
func appendSystemPrompt(extra string) ChatOption {
	return func(c *ChatConfig) {
		c.SystemPrompt += extra
	}
}

// ChatStreamWithReport calls T.LLM.ChatStream and tallies token usage on T.Report.
func (T *FuzzAgent) ChatStreamWithReport(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	start := time.Now()
	resp, err := T.LLM.ChatStream(ctx, messages, handler, opts...)
	elapsed := time.Since(start)
	if err != nil {
		Debug("[llm] stream failed after %s: %s", elapsed.Round(time.Millisecond), err)
		return resp, err
	}
	Debug("[llm] stream completed in %s (input: %d, output: %d tokens)", elapsed.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	T.trackTokens(resp)
	return resp, nil
}

// trackTokens adds the response's token counts to the report tallies.
func (T *FuzzAgent) trackTokens(resp *Response) {
	if T.Report == nil || resp == nil {
		return
	}
	if resp.InputTokens > 0 {
		T.Report.Tally("Input Tokens").Add(resp.InputTokens)
	}
	if resp.OutputTokens > 0 {
		T.Report.Tally("Output Tokens").Add(resp.OutputTokens)
	}
}

// SetLimiter sets the limiter with the given limit.
func (T *FuzzAgent) SetLimiter(limit int) {
	T.Limiter = NewLimitGroup(limit)
}

// Wait blocks until a permit is available from the limiter.
func (T *FuzzAgent) Wait() {
	if T.Limiter == nil {
		return
	}
	T.Limiter.Wait()
}

// Try attempts to acquire a permit from the rate limiter.
func (T *FuzzAgent) Try() bool {
	if T.Limiter == nil {
		return false
	}
	return T.Limiter.Try()
}

// Done signals the completion of a task, decrementing the limiter if present.
func (T *FuzzAgent) Done() {
	if T.Limiter != nil {
		T.Limiter.Done()
	}
}

// Add increments the limiter by the given input value.
func (T *FuzzAgent) Add(input int) {
	if T.Limiter == nil {
		T.SetLimiter(50)
	}
	T.Limiter.Add(input)
}

// Parse parses the command-line arguments.
func (f *FlagSet) Parse() (err error) {
	if err = f.EFlagSet.Parse(f.FlagArgs[0:]); err != nil {
		return err
	}
	return nil
}

// Text returns the underlying command-line arguments.
func (f *FlagSet) Text() (output []string) {
	return f.FlagArgs
}

// Agent defines the interface for fuzz agents.
type Agent interface {
	Get() *FuzzAgent
	Name() string
	Desc() string
	SystemPrompt() string
	Init() error
	Main() error
}

// ChatTool defines the interface for chat tools.
// Tools are lightweight functions available in chat mode and the agent loop.
type ChatTool interface {
	Name() string
	Desc() string
	Params() map[string]ToolParam
	Run(args map[string]any) (string, error)
}

// ConfirmableTool is an optional interface that ChatTool implementations
// can implement to indicate the tool requires user confirmation before execution.
type ConfirmableTool interface {
	NeedsConfirm() bool
}

// NeedInteract pauses for user input.
func NeedInteract() {
	nfo.PressEnter("\n(press enter to continue)")
}

// GetBodyBytes returns a function that returns an io.ReadCloser for the given byte slice.
func GetBodyBytes(input []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(input)), nil
	}
}

// NONE is an empty string.
// SLASH is the operating system's path separator.
const (
	NONE  = ""
	SLASH = string(os.PathSeparator)
)

type Options = nfo.Options

var (
	NewOptions      = nfo.NewOptions
	Log             = nfo.Log             // Standard Log Output
	Fatal           = nfo.Fatal           // Fatal Log Output & Exit.
	Notice          = nfo.Notice          // Notice Log Output
	Flash           = nfo.Flash           // Flash to Stderr
	Stdout          = nfo.Stdout          // Send to Stdout
	Warn            = nfo.Warn            // Warn Log Output
	Defer           = nfo.Defer           // Global Application Defer
	Debug           = nfo.Debug           // Debug Log Output
	Trace           = nfo.Trace           // Trace Log Output
	Exit            = nfo.Exit            // End Application, Run Global Defer.
	PleaseWait      = nfo.PleaseWait      // Set Loading Prompt
	Stderr          = nfo.Stderr          // Send to Stderr
	ProgressBar     = nfo.NewProgressBar  // Set Progress Bar animation
	Path            = filepath.Clean      // Provide clean path
	TransferCounter = nfo.TransferCounter // Transfer Animation
	NewLimitGroup   = xsync.NewLimitGroup // Limiter Group
	FormatPath      = filepath.FromSlash  // Convert to standard path.
	GetPath         = filepath.ToSlash    // Convert to OS specific path.
	Info            = nfo.Aux             // Log as standard INFO
	HumanSize       = nfo.HumanSize       // Convert bytes int64 to B/KB/MB/GB/TB.
	GetInput        = nfo.GetInput        // Prompt user for text input.
)

var (
	transferMonitor = nfo.TransferMonitor
	leftToRight     = nfo.LeftToRight
	rightToLeft     = nfo.RightToLeft
	nopSeeker       = nfo.NopSeeker
	noRate          = nfo.NoRate
)

// NewFlagSet returns a new flag set.
var (
	NewFlagSet      = eflag.NewFlagSet
	ReturnErrorOnly = eflag.ReturnErrorOnly
)

type (
	BitFlag        = xsync.BitFlag
	LimitGroup     = xsync.LimitGroup
	ConfigStore    = cfg.Store
	ReadSeekCloser = nfo.ReadSeekCloser
	SwapReader     = swapreader.Reader
)

// error_counter tracks the number of errors encountered.
var error_counter uint32

// ErrCount returns amount of times Err has been triggered.
func ErrCount() uint32 {
	return atomic.LoadUint32(&error_counter)
}

// Err logs a standard error and adds counter to ErrCount().
func Err(input ...interface{}) {
	atomic.AddUint32(&error_counter, 1)
	msg := nfo.Stringer(input...)
	nfo.Err(msg)
	if err_table != nil {
		err_table.Set(fmt.Sprintf("%d", atomic.LoadUint32(&error_counter)), fmt.Sprintf("<%v> %s", time.Now().Round(time.Second), msg))
	}
}

// Critical performs a fatal error check.
func Critical(err error) {
	if err != nil {
		Fatal(err)
	}
}

// StringDate converts string to date.
func StringDate(input string) (output time.Time, err error) {
	if input == NONE {
		return
	}
	output, err = time.Parse(time.RFC3339, fmt.Sprintf("%sT00:00:00Z", input))
	if err != nil {
		if strings.Contains(err.Error(), "parse") {
			err = fmt.Errorf("Invalid date specified, should be in format: YYYY-MM-DD")
		} else {
			err_split := strings.Split(err.Error(), ":")
			err = fmt.Errorf("Invalid date specified:%s", err_split[len(err_split)-1])
		}
	}
	return
}

// MyRoot returns the absolute path to the root directory of the executable.
func MyRoot() string {
	exec, err := os.Executable()
	Critical(err)

	root, err := filepath.Abs(filepath.Dir(exec))
	Critical(err)

	return GetPath(root)
}

// SplitPath splits a path into components.
func SplitPath(path string) (folder_path []string) {
	if strings.Contains(path, "/") {
		path = strings.TrimSuffix(path, "/")
		folder_path = strings.Split(path, "/")
	} else {
		path = strings.TrimSuffix(path, "\\")
		folder_path = strings.Split(path, "\\")
	}
	for i := 0; i < len(folder_path); i++ {
		if folder_path[i] == NONE {
			folder_path = append(folder_path[:i], folder_path[i+1:]...)
			i--
		}
	}
	return
}

// DefaultPleaseWait resets the please wait prompt to its default.
func DefaultPleaseWait() {
	PleaseWait.Set(func() string { return "Please wait ..." }, []string{"[>  ]", "[>> ]", "[>>>]", "[ >>]", "[  >]", "[  <]", "[ <<]", "[<<<]", "[<< ]", "[<  ]"})
}

// MD5Sum calculates the MD5 hash of a file.
func MD5Sum(filename string) (sum string, err error) {
	checkSum := md5.New()
	file, err := os.Open(filename)
	if err != nil {
		return
	}
	defer file.Close()

	var (
		o int64
		n int
		r int
	)

	for tmp := make([]byte, 16384); ; {
		r, err = file.ReadAt(tmp, o)

		if err != nil && err != io.EOF {
			return NONE, err
		}

		if r == 0 {
			break
		}

		tmp = tmp[0:r]
		n, err = checkSum.Write(tmp)
		if err != nil {
			return NONE, err
		}
		o = o + int64(n)
	}

	if err != nil && err != io.EOF {
		return NONE, err
	}

	md5sum := checkSum.Sum(nil)

	s := make([]byte, hex.EncodedLen(len(md5sum)))
	hex.Encode(s, md5sum)

	return string(s), nil
}

// UUIDv4 generates a UUID v4.
func UUIDv4() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// RandBytes generates a random byte slice of length specified.
func RandBytes(sz int) []byte {
	if sz <= 0 {
		sz = 16
	}

	ch := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+/"
	chlen := len(ch)

	rand_string := make([]byte, sz)
	rand.Read(rand_string)

	for i, v := range rand_string {
		rand_string[i] = ch[v%byte(chlen)]
	}
	return rand_string
}

// Error handler for const errors.
type Error string

func (e Error) Error() string { return string(e) }

// MkDir creates folders.
func MkDir(name ...string) (err error) {
	for _, path := range name {
		err = os.MkdirAll(path, 0766)
		if err != nil {
			subs := strings.Split(path, string(os.PathSeparator))
			for i := 0; i < len(subs); i++ {
				p := strings.Join(subs[0:i+1], string(os.PathSeparator))
				if p == "" {
					p = "."
				}
				f, err := os.Stat(p)
				if err != nil {
					if os.IsNotExist(err) {
						err = os.Mkdir(p, 0766)
						if err != nil && !os.IsExist(err) {
							return err
						}
					} else {
						return err
					}
				}
				if f != nil && !f.IsDir() {
					return fmt.Errorf("mkdir: %s: file exists", f.Name())
				}
			}
		}
	}
	return nil
}

// PadZero pads a number with a leading zero if less than 10.
func PadZero(num int) string {
	if num < 10 {
		return fmt.Sprintf("0%d", num)
	} else {
		return fmt.Sprintf("%d", num)
	}
}

// DateString creates a standard date YY-MM-DD out of time.Time.
func DateString(input time.Time) string {
	pad := func(num int) string {
		if num < 10 {
			return fmt.Sprintf("0%d", num)
		}
		return fmt.Sprintf("%d", num)
	}
	return fmt.Sprintf("%s-%s-%s", pad(input.Year()), pad(int(input.Month())), pad(input.Day()))
}

// CombinePath combines several paths.
func CombinePath(name ...string) string {
	if name == nil {
		return NONE
	}
	if len(name) < 2 {
		return name[0]
	}
	return LocalPath(fmt.Sprintf("%s%s%s", name[0], SLASH, strings.Join(name[1:], SLASH)))
}

// LocalPath adapts path to whatever local filesystem uses.
func LocalPath(path string) string {
	path = strings.Replace(path, "/", SLASH, -1)
	subs := strings.Split(path, SLASH)
	for i, v := range subs {
		subs[i] = strings.TrimSpace(v)
	}
	return strings.Join(subs, SLASH)
}

// NormalizePath switches windows based slash to forward slash.
func NormalizePath(path string) string {
	path = strings.Replace(path, "\\", "/", -1)
	subs := strings.Split(path, "/")
	for i, v := range subs {
		subs[i] = strings.TrimSpace(v)
	}
	return strings.Join(subs, "/")
}

// Rename renames a path.
func Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

// IsBlank confirms all strings handed to it are empty.
func IsBlank(input ...string) bool {
	for _, v := range input {
		if len(v) == 0 {
			return true
		}
	}
	return false
}

// Dequote removes leading and trailing quotation marks on string.
func Dequote(input string) string {
	var output string
	output = input
	if len(output) > 0 && (output)[0] == '"' {
		output = output[1:]
	}
	if len(output) > 0 && (output)[len(output)-1] == '"' {
		output = output[:len(output)-1]
	}
	return output
}

// Delete removes a file at the given path.
func Delete(path string) error {
	return os.Remove(LocalPath(path))
}
