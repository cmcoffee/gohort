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

// AppVersion is set at startup from the build-time VERSION variable.
var AppVersion = "dev"

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

// AppCore encapsulates the components required to execute an agent.
type AppCore struct {
	Flags   FlagSet
	DB      Database
	Cache   Database
	Report  *TaskReport
	Limiter LimitGroup
	LLM          LLM     // Primary (worker) LLM — used for most calls.
	LeadLLM      LLM     // Lead (judge) LLM — used for high-precision calls. Falls back to LLM if nil.
	LeadFallback bool    // Set to true if any lead LLM call fell back to the primary during this session.
	NoLead       bool    // HARD GUARD: when true, LeadChat() redirects to worker and RunAgentLoop ignores LEAD tier.

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

// Get returns the AppCore instance itself.
func (T *AppCore) Get() *AppCore {
	return T
}

// SystemPrompt returns the default system prompt (empty).
// Agents override this method to provide their own system prompt.
func (T *AppCore) SystemPrompt() string {
	return ""
}

// SetSystemPrompt stores the system prompt resolved from the Agent interface.
// Called by the framework before Main().
func (T *AppCore) SetSystemPrompt(prompt string) {
	T.systemPrompt = prompt
}

// SetTools sets the tool names to resolve from the registry when Run() is called.
func (T *AppCore) SetTools(names ...string) {
	T.tools = names
}

// RequireLLM returns an error if no LLM is configured.
func (T *AppCore) RequireLLM() error {
	if T.LLM == nil {
		return fmt.Errorf("LLM is required, run --setup")
	}
	return nil
}

// Private marks this AppCore as a private/worker-only agent.
// Sets NoLead=true, which redirects LeadChat() to worker and blocks
// RunAgentLoop from escalating to LEAD tier. Call once in Init() or Main().
func (T *AppCore) Private() { T.NoLead = true }

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
func (T *AppCore) PingLLM(ctx context.Context) error {
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
// Returns nil immediately when NoLead is set — no probe is sent.
func (T *AppCore) PingLeadLLM(ctx context.Context) error {
	if T.NoLead || T.LeadLLM == nil {
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
// Returns nil when NoLead is set — the caller should never attempt lead escalation.
func (T *AppCore) GetLeadLLM() LLM {
	if T.NoLead {
		return nil
	}
	if T.LeadLLM != nil {
		return T.LeadLLM
	}
	return T.LLM
}

// WorkerContextSize returns the worker LLM's context window size, or 0
// if the LLM doesn't implement ContextSizer.
func (T *AppCore) WorkerContextSize() int {
	if cs, ok := T.LLM.(ContextSizer); ok {
		return cs.ContextSize()
	}
	return 0
}

// LeadContextSize returns the lead LLM's context window size, or 0 if the
// lead LLM doesn't implement ContextSizer. Falls back to the worker LLM's
// size when the lead tier is not separately configured.
func (T *AppCore) LeadContextSize() int {
	if T.LeadLLM != nil {
		if cs, ok := T.LeadLLM.(ContextSizer); ok {
			return cs.ContextSize()
		}
	}
	return T.WorkerContextSize()
}

// LLMTier selects which LLM tier a Session routes to. Worker is the
// primary/local tier; Lead is the precision/judge tier (which may
// fall back to Worker if not configured separately).
type LLMTier int

// TierUnset is the zero-value sentinel used on Response.Tier when no
// explicit tier has been recorded (older call paths, custom transports).
// Keeping it at 0 lets plain Response{} literals mean "not set" so
// downstream code can fall back to a contextual tier (e.g., a Session's
// Tier). WORKER/LEAD start at 1 so they never alias the zero-value.
const (
	TierUnset LLMTier = iota
	WORKER
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
	agent    *AppCore

	// Per-session usage counters. Bumped after each Chat/ChatStream
	// response using Response.Tier so counters reflect which tier
	// *actually served* each call — not the session's nominal tier.
	// This matters because a LEAD session can execute on the worker
	// via (1) routing config delegating the call, (2) lead-LLM
	// fallback-to-primary on error, (3) fallback on empty output.
	// In all three cases, cost should price at worker rates.
	//
	// Search and image call counts stay on the global ProcessUsage()
	// tracker (they don't flow through Session.Chat).
	mu       sync.Mutex
	counters UsageDiff
}

// SessionUsage is the flat {Input, Output} summary view returned by
// Session.Report(). Collapses worker + lead counts into single numbers
// for readers that don't care about tier breakdown. Use AsDiff() for
// the tier-split UsageDiff suitable for CostRates.Estimate.
type SessionUsage struct {
	Input  int64
	Output int64
}

// CreateSession returns a new LLM session for the given tier with a
// fresh UUID as the caller ID. Each session carries its own token
// counters; call Report() at the end of the logical operation to read
// back what this session consumed.
func (T *AppCore) CreateSession(tier LLMTier) *Session {
	return &Session{CallerID: UUIDv4(), Tier: tier, agent: T}
}

// Chat dispatches to WorkerChat or LeadChat based on the session's
// tier, with the session's caller ID prepended. Any explicit
// WithCaller later in opts overrides it.
func (s *Session) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	opts = prependCaller(s.CallerID, opts)
	var resp *Response
	var err error
	if s.Tier == LEAD && !s.agent.NoLead {
		resp, err = s.agent.LeadChat(ctx, messages, opts...)
	} else {
		resp, err = s.agent.WorkerChat(ctx, messages, opts...)
	}
	s.recordTokens(resp)
	return resp, err
}

// ChatStream dispatches to the tier's LLM ChatStream with the
// session's caller ID attached. For LEAD, falls back to the worker
// LLM if GetLeadLLM returns nil. Bumps both the session's own
// counters and the process-wide tracker so per-op scopes and
// middleware reports stay in sync (the direct llm.ChatStream path
// skips the trackTokens call that WorkerChat/LeadChat do for us).
func (s *Session) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	opts = prependCaller(s.CallerID, opts)
	var llm LLM
	if s.Tier == LEAD && !s.agent.NoLead {
		llm = s.agent.GetLeadLLM()
	} else {
		llm = s.agent.LLM
	}
	resp, err := llm.ChatStream(ctx, messages, handler, opts...)
	// Streaming has no fallback path — whatever tier we dispatched to
	// is what served. Tag the response so downstream attribution
	// (recordTokens, callers inspecting resp.Tier) doesn't need to
	// re-derive it from s.Tier.
	if resp != nil {
		resp.Tier = s.Tier
	}
	s.recordTokens(resp)
	if s.Tier == LEAD {
		s.agent.trackLeadTokens(resp)
	} else {
		s.agent.trackTokens(resp)
	}
	return resp, err
}

// recordTokens attributes token counts from a completed Chat/ChatStream
// response into the session's own counters. Uses resp.Tier (populated
// by WorkerChat/LeadChat including fallback attribution) to split the
// count between worker and lead. Falls back to s.Tier when resp.Tier
// is unset — older/custom code paths that don't populate it.
// Safe to call with nil.
func (s *Session) recordTokens(resp *Response) {
	if resp == nil || (resp.InputTokens == 0 && resp.OutputTokens == 0) {
		return
	}
	tier := resp.Tier
	if tier == TierUnset {
		tier = s.Tier
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tier == LEAD {
		s.counters.LeadInput += int64(resp.InputTokens)
		s.counters.LeadOutput += int64(resp.OutputTokens)
	} else {
		s.counters.WorkerInput += int64(resp.InputTokens)
		s.counters.WorkerOutput += int64(resp.OutputTokens)
	}
}

// Report returns a flat {Input, Output} summary of tokens consumed
// through this session so far — worker and lead rolled up. Use AsDiff
// for the tier-split view needed by cost estimation.
func (s *Session) Report() SessionUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionUsage{
		Input:  s.counters.WorkerInput + s.counters.LeadInput,
		Output: s.counters.WorkerOutput + s.counters.LeadOutput,
	}
}

// SnapshotDiff captures the session's current full UsageDiff counters.
// Used by UsageScope to baseline a sub-operation against a session
// shared with a parent — Diff(snapshot) later returns only the delta.
func (s *Session) SnapshotDiff() UsageDiff {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counters
}

// Snapshot is an alias for Report, provided for symmetry with
// ProcessUsage().Snapshot() / Diff(). Returns the flat summary; use
// SnapshotDiff when the caller needs the tier-split view.
func (s *Session) Snapshot() SessionUsage { return s.Report() }

// Diff returns tokens consumed between the given flat-summary start
// snapshot and current counters. Returns the simple {Input, Output}
// delta — most callers should prefer UsageScope for sub-op attribution
// since it tracks tier-split counters correctly.
func (s *Session) Diff(start SessionUsage) SessionUsage {
	now := s.Report()
	return SessionUsage{
		Input:  now.Input - start.Input,
		Output: now.Output - start.Output,
	}
}

// AsDiff returns the session's tier-split counters as a UsageDiff
// suitable for CostRates.Estimate / FormatUsage. Tier comes from what
// actually served each call (via Response.Tier), so a LEAD session
// whose calls were routed/fell-back to worker prices at worker rates.
func (s *Session) AsDiff() UsageDiff {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counters
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
func (T *AppCore) LeadChat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	if T.NoLead {
		// NoLead is set — redirect to worker instead of escalating.
		return T.WorkerChat(ctx, messages, opts...)
	}
	// Honor routing config: if a route key was supplied via WithRouteKey
	// and the stage is configured for "worker", delegate transparently.
	var probe ChatConfig
	for _, opt := range opts {
		opt(&probe)
	}
	if probe.RouteKey != "" && !RouteToLead(probe.RouteKey) {
		if probe.Think == nil {
			if think := RouteThink(probe.RouteKey); think != nil {
				opts = append(opts, WithThink(*think))
				if *think && probe.ThinkBudget == nil {
					if budget := RouteThinkBudget(probe.RouteKey); budget != nil {
						opts = append(opts, WithThinkBudget(*budget))
					}
				}
				Debug("[llm] %s routed to worker LLM (thinking=%v, routing config)", probe.RouteKey, *think)
			} else {
				// RouteThink returns nil only when LookupRouteFunc is nil; guard
				// against that edge by defaulting thinking off on any worker route.
				opts = append(opts, WithThink(false))
				Debug("[llm] %s routed to worker LLM (routing config)", probe.RouteKey)
			}
		} else {
			Debug("[llm] %s routed to worker LLM (thinking=%v, call-site override)", probe.RouteKey, *probe.Think)
		}
		return T.WorkerChat(ctx, messages, opts...)
	}
	lead := T.GetLeadLLM()
	start := time.Now()
	resp, err := lead.Chat(ctx, messages, opts...)
	elapsed := time.Since(start)
	// fellBackToWorker tracks whether the tokens on resp actually
	// came from the worker LLM (fallback path). Matters for the
	// UsageTracker tier attribution — worker pricing, not lead.
	fellBackToWorker := false
	if err != nil && T.LeadLLM != nil && T.LLM != nil && T.LeadLLM != T.LLM {
		Debug("[llm] lead chat failed after %s: %s — falling back to primary", elapsed.Round(time.Millisecond), err)
		T.LeadFallback = true
		fellBackToWorker = true
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
		fellBackToWorker = true
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
	// Track the response against the tier that actually served it.
	// Fallback path → worker tokens. Non-fallback → lead tokens.
	if fellBackToWorker {
		if resp != nil {
			resp.Tier = WORKER
		}
		T.trackTokens(resp)
	} else {
		if resp != nil {
			resp.Tier = LEAD
		}
		T.trackLeadTokens(resp)
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

// OllamaBackendFunc returns the Ollama backend base URL, configured model name,
// and context window size (num_ctx). Set by the main application at startup.
// Returns ("", "", 0) when Ollama is not the active provider or is not configured.
var OllamaBackendFunc func() (backend, model string, numCtx int)

// LlamaCppBackendFunc returns the llama.cpp server base URL (including /v1 path)
// and configured model name. Returns ("", "") when llama.cpp is not the active provider.
var LlamaCppBackendFunc func() (endpoint, model string)

// OllamaProxyEnabledFunc reports whether the Ollama proxy is enabled.
// Set by the main application. Returns false when unset.
var OllamaProxyEnabledFunc func() bool

// OllamaProxyPortFunc returns the port the standalone Ollama proxy server
// should listen on. Returns 0 when unset or not configured.
var OllamaProxyPortFunc func() int

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
	Key           string // db key, e.g. "myapp.stage_name"
	Label         string // menu label
	Default       string // effective value when not set in DB: "lead" (default), "worker", or "worker (thinking)"
	DefaultBudget int    // default thinking budget tokens for this stage; 0 means fall back to global
	Group         string // display group in the admin routing UI; derived from key prefix if empty
	Private       bool   // when true, the stage is locked to worker tier (private-only app)
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

// IsPrivateStage reports whether the given stage key is registered as
// private (locked to worker tier).
func IsPrivateStage(key string) bool {
	routeRegistry.mu.RLock()
	defer routeRegistry.mu.RUnlock()
	for _, s := range routeRegistry.stages {
		if s.Key == key && s.Private {
			return true
		}
	}
	return false
}

// LookupRouteFunc is set by the application to read a route stage's
// current setting from the database. Returns "worker" or "" (lead).
var LookupRouteFunc func(key string) string

// LookupRouteThinkBudgetFunc is set by the application to read per-route
// thinking budget overrides. Returns &N or nil (use global default).
var LookupRouteThinkBudgetFunc func(key string) *int

// routeEffectiveVal returns the effective routing value for key,
// falling back to the stage's Default when the DB has no stored value.
func routeEffectiveVal(key string) string {
	val := ""
	if LookupRouteFunc != nil {
		val = LookupRouteFunc(key)
	}
	if val == "" {
		routeRegistry.mu.RLock()
		for _, s := range routeRegistry.stages {
			if s.Key == key {
				val = s.Default
				break
			}
		}
		routeRegistry.mu.RUnlock()
	}
	return val
}

// RouteToLead returns true if the named route stage should use the lead
// LLM. Stages default to lead unless their Default field or DB value says
// "worker" or "worker (thinking)".
func RouteToLead(key string) bool {
	val := routeEffectiveVal(key)
	return val != "worker" && val != "worker (thinking)"
}

// RouteThink returns the thinking override for the named route stage.
//   - "worker (thinking)" → &true
//   - "worker"            → &false
//   - "lead" / ""         → nil (not routed to worker)
func RouteThink(key string) *bool {
	val := routeEffectiveVal(key)
	switch val {
	case "worker (thinking)":
		t := true
		return &t
	case "worker":
		f := false
		return &f
	}
	return nil
}

// RouteThinkBudget returns the per-route thinking token budget, checking
// (in order): DB override → stage DefaultBudget → nil (use global default).
func RouteThinkBudget(key string) *int {
	if LookupRouteThinkBudgetFunc != nil {
		if n := LookupRouteThinkBudgetFunc(key); n != nil {
			return n
		}
	}
	routeRegistry.mu.RLock()
	defer routeRegistry.mu.RUnlock()
	for _, s := range routeRegistry.stages {
		if s.Key == key && s.DefaultBudget > 0 {
			n := s.DefaultBudget
			return &n
		}
	}
	return nil
}

// SaveSnippetFunc is set by the CodeWriter app so other apps can save code snippets
// to the user's CodeWriter library without importing the codewriter package.
var SaveSnippetFunc func(userID, name, lang, code string) (id string, err error)

// SaveArticleFunc is set by the TechWriter app so other apps can save documents
// to the user's TechWriter library without importing the techwriter package.
var SaveArticleFunc func(userID, subject, body string) (id string, err error)

// RunAgentFunc is set by the application to enable agent-to-agent delegation.
var RunAgentFunc func(name string, args []string) (string, error)

// DelegateAgent runs another agent by name and returns its captured output.
func (T *AppCore) DelegateAgent(name string, args ...string) (string, error) {
	if RunAgentFunc == nil {
		return "", fmt.Errorf("agent delegation is not available")
	}
	return RunAgentFunc(name, args)
}

// WorkerChat calls T.LLM.Chat and tallies token usage on T.Report.
// If the call includes a WithRouteKey option and the routing menu has a
// thinking override configured for that stage, it is applied here so
// direct worker-session calls respect the same setting as lead-routed calls.
func (T *AppCore) WorkerChat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	var probe ChatConfig
	for _, opt := range opts {
		opt(&probe)
	}
	if probe.RouteKey != "" && probe.Think == nil {
		if think := RouteThink(probe.RouteKey); think != nil {
			opts = append(opts, WithThink(*think))
			probe.Think = think
			if *think && probe.ThinkBudget == nil {
				if budget := RouteThinkBudget(probe.RouteKey); budget != nil {
					opts = append(opts, WithThinkBudget(*budget))
				}
			}
			Debug("[llm] %s worker thinking override: %v (routing config)", probe.RouteKey, *think)
		}
	}
	// Worker tier: thinking on by default. Callers that don't need thinking
	// (e.g. title generation) pass WithThink(false) explicitly.
	if probe.Think == nil {
		opts = append(opts, WithThink(true))
	}
	start := time.Now()
	resp, err := T.LLM.Chat(ctx, messages, opts...)
	elapsed := time.Since(start)
	if err != nil {
		Debug("[llm] chat failed after %s: %s", elapsed.Round(time.Millisecond), err)
		return resp, err
	}
	if resp != nil {
		resp.Tier = WORKER
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
func (T *AppCore) WorkerChatWithCalc(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
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

	// Accumulate token counts across all inner WorkerChat calls so the
	// returned Response reflects total consumption — callers (and
	// Session.ChatWithCalc) that attribute tokens to a session see the
	// full tool-loop cost, not just the final turn.
	var cumInput, cumOutput int
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
		cumInput += resp.InputTokens
		cumOutput += resp.OutputTokens

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
			resp.InputTokens = cumInput
			resp.OutputTokens = cumOutput
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
	finalResp, finalErr := T.WorkerChat(ctx, history, opts...)
	if finalResp != nil {
		finalResp.InputTokens += cumInput
		finalResp.OutputTokens += cumOutput
	}
	return finalResp, finalErr
}

// LeadChatWithCalc is like WorkerChatWithCalc but uses the lead LLM for every
// round. Route config (WithRouteKey) is respected — if the stage is set to
// "worker" or "worker (thinking)", LeadChat will redirect to the worker.
func (T *AppCore) LeadChatWithCalc(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	calc, ok := FindChatTool("calculate")
	if !ok {
		return T.LeadChat(ctx, messages, opts...)
	}

	calcTool := Tool{
		Name:        calc.Name(),
		Description: calc.Desc(),
		Parameters:  calc.Params(),
	}
	calcDef := AgentToolDef{Tool: calcTool, Handler: calc.Run}
	handlers := map[string]ToolHandlerFunc{calc.Name(): calc.Run}

	nativeOpts := append(append([]ChatOption{}, opts...), WithTools([]Tool{calcTool}))
	promptToolText := BuildToolPrompt([]AgentToolDef{calcDef})

	history := make([]Message, len(messages))
	copy(history, messages)

	var cumInput, cumOutput int
	for round := 0; round < 3; round++ {
		callOpts := append(nativeOpts, appendSystemPrompt(promptToolText))

		resp, err := T.LeadChat(ctx, history, callOpts...)
		if err != nil {
			return resp, err
		}
		cumInput += resp.InputTokens
		cumOutput += resp.OutputTokens

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

		tc, preamble := ParsePromptToolCall(resp.Content, handlers)
		if tc == nil {
			resp.InputTokens = cumInput
			resp.OutputTokens = cumOutput
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

	finalResp, finalErr := T.LeadChat(ctx, history, opts...)
	if finalResp != nil {
		finalResp.InputTokens += cumInput
		finalResp.OutputTokens += cumOutput
	}
	return finalResp, finalErr
}

// ChatWithCalc dispatches to LeadChatWithCalc or WorkerChatWithCalc based on
// the session's tier. The tool-calling loop runs on the same LLM tier as the
// session so lead sessions (e.g. gemma4) use lead for arithmetic too.
func (s *Session) ChatWithCalc(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	opts = prependCaller(s.CallerID, opts)
	var resp *Response
	var err error
	if s.Tier == LEAD {
		resp, err = s.agent.LeadChatWithCalc(ctx, messages, opts...)
	} else {
		resp, err = s.agent.WorkerChatWithCalc(ctx, messages, opts...)
	}
	s.recordTokens(resp)
	return resp, err
}

// appendSystemPrompt returns a ChatOption that appends text to the existing
// system prompt rather than replacing it.
func appendSystemPrompt(extra string) ChatOption {
	return func(c *ChatConfig) {
		c.SystemPrompt += extra
	}
}

// ChatStreamWithReport calls T.LLM.ChatStream and tallies token usage on T.Report.
func (T *AppCore) ChatStreamWithReport(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	start := time.Now()
	resp, err := T.LLM.ChatStream(ctx, messages, handler, opts...)
	elapsed := time.Since(start)
	if err != nil {
		Debug("[llm] stream failed after %s: %s", elapsed.Round(time.Millisecond), err)
		return resp, err
	}
	if resp != nil {
		resp.Tier = WORKER
	}
	Debug("[llm] stream completed in %s (input: %d, output: %d tokens)", elapsed.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	T.trackTokens(resp)
	return resp, nil
}

// trackTokens adds the response's token counts to the report tallies.
// This is the WORKER-tier tracker — WorkerChat and similar primary-LLM
// paths call it. For lead-tier calls, use trackLeadTokens. The split
// matters for cost estimation since worker and lead models typically
// price very differently.
func (T *AppCore) trackTokens(resp *Response) {
	if resp == nil {
		return
	}
	if T.Report != nil {
		if resp.InputTokens > 0 {
			T.Report.Tally("Input Tokens").Add(resp.InputTokens)
		}
		if resp.OutputTokens > 0 {
			T.Report.Tally("Output Tokens").Add(resp.OutputTokens)
		}
	}
	// Also feed the process-wide UsageTracker so per-run Scope()
	// callers see the worker portion of consumption.
	ProcessUsage().AddWorker(resp.InputTokens, resp.OutputTokens)
}

// trackLeadTokens is the lead-tier counterpart to trackTokens. Called
// from LeadChat when the response came from the lead LLM. When
// LeadChat falls back to the primary LLM (LeadFallback path), callers
// should use trackTokens instead so the tokens get attributed to the
// worker tier that actually served them.
func (T *AppCore) trackLeadTokens(resp *Response) {
	if resp == nil {
		return
	}
	if T.Report != nil {
		if resp.InputTokens > 0 {
			T.Report.Tally("Input Tokens").Add(resp.InputTokens)
		}
		if resp.OutputTokens > 0 {
			T.Report.Tally("Output Tokens").Add(resp.OutputTokens)
		}
	}
	ProcessUsage().AddLead(resp.InputTokens, resp.OutputTokens)
}

// SetLimiter sets the limiter with the given limit.
func (T *AppCore) SetLimiter(limit int) {
	T.Limiter = NewLimitGroup(limit)
}

// Wait blocks until a permit is available from the limiter.
func (T *AppCore) Wait() {
	if T.Limiter == nil {
		return
	}
	T.Limiter.Wait()
}

// Try attempts to acquire a permit from the rate limiter.
func (T *AppCore) Try() bool {
	if T.Limiter == nil {
		return false
	}
	return T.Limiter.Try()
}

// Done signals the completion of a task, decrementing the limiter if present.
func (T *AppCore) Done() {
	if T.Limiter != nil {
		T.Limiter.Done()
	}
}

// Add increments the limiter by the given input value.
func (T *AppCore) Add(input int) {
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
	Get() *AppCore
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

// InternetTool is an optional interface that ChatTool implementations
// can implement to indicate the tool contacts the internet. Tools that
// implement this are excluded from private-mode chat sessions.
type InternetTool interface {
	IsInternetTool() bool
}

// CapabilityTool is an optional interface ChatTool implementations can
// satisfy to declare what side effects they have (CapRead / CapNetwork /
// CapWrite / CapExecute). The agent loop reads these via Tool.Caps when
// AllowedCaps gating is active. A tool that doesn't implement this is
// treated as "unannotated" and passes the cap filter unconditionally —
// migration-safe; tighten gradually as tools opt in.
type CapabilityTool interface {
	Caps() []Capability
}

// ToolSession carries mutable per-session state shared between the caller
// and session-aware tools. Pass a *ToolSession when building agent tools via
// GetAgentToolsWithSession; read results back from it after the loop completes.
type ToolSession struct {
	Images       []string // base64-encoded images accumulated by image tools (delivered as outbound attachments / displayed inline)
	Videos       []string // base64-encoded video data accumulated by video tools; consumers (phantom outbox) deliver as attachments
	Silenced     bool     // set true by the stay_silent tool
	LLM          LLM      // optional LLM made available to tools that need sub-calls
	WorkspaceDir string   // absolute path to the sandbox dir for local-exec / file-I/O tools; empty disables sandboxed tools entirely
	mu           sync.Mutex
}

// AppendImage appends a base64-encoded image to the session image list.
func (s *ToolSession) AppendImage(b64 string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.Images = append(s.Images, b64)
	s.mu.Unlock()
}

// AppendVideo appends a base64-encoded video to the session video list.
// Consumed by apps that deliver outbound attachments (phantom iMessage
// repost path); ignored by apps that don't.
func (s *ToolSession) AppendVideo(b64 string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.Videos = append(s.Videos, b64)
	s.mu.Unlock()
}

// SessionChatTool extends ChatTool for tools that need per-session state.
// When a *ToolSession is provided via GetAgentToolsWithSession, RunWithSession
// is called in preference to Run.
type SessionChatTool interface {
	ChatTool
	RunWithSession(args map[string]any, sess *ToolSession) (string, error)
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
