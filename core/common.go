package core

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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
	Flags FlagSet
	DB    Database
	Cache Database
	// VectorDB + EmbedCfg let an SDK consumer inject the retrieval backend
	// (semantic collections / RAG) instead of relying on the process globals.
	// Unset falls back to the globals, so the running server is unaffected.
	// SDK Phase 1 — see docs/sdk-decoupling-scope.md.
	VectorDB     Database
	EmbedCfg     EmbeddingConfig
	Report       *TaskReport
	Limiter      LimitGroup
	LLM          LLM  // Primary (worker) LLM — used for most calls.
	LeadLLM      LLM  // Lead (judge) LLM — used for high-precision calls. Falls back to LLM if nil.
	LeadFallback bool // Set to true if any lead LLM call fell back to the primary during this session.
	NoLead       bool // HARD GUARD: when true, LeadChat() redirects to worker and RunAgentLoop ignores LEAD tier.

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

	// webMux + webPrefix are set by the framework before calling
	// SimpleWebApp.Routes(). They give the app a pre-wired sub-mux
	// to register handlers on via T.HandleFunc / T.Handle, without
	// having to plumb (mux, prefix) through itself. Apps using the
	// older WebApp.RegisterRoutes(mux, prefix) shape don't need
	// these — they get nil values and ignore them.
	webMux    *http.ServeMux
	webPrefix string
}

// HandleFunc registers an HTTP handler against the app's pre-wired
// sub-mux. Call inside SimpleWebApp.Routes(). The pattern is
// relative to the app's prefix — "/" mounts at e.g. /myapp/, and
// "/api/foo" at /myapp/api/foo.
//
// Panics if called before the framework has wired the mux (i.e.
// before Routes() fires). Apps using WebApp.RegisterRoutes can
// ignore this method and keep registering against the supplied
// mux argument.
func (T *AppCore) HandleFunc(pattern string, handler http.HandlerFunc) {
	if T.webMux == nil {
		panic("AppCore.HandleFunc called before framework wired the mux (only valid inside SimpleWebApp.Routes())")
	}
	T.webMux.HandleFunc(pattern, handler)
}

// Handle is the http.Handler-flavored counterpart to HandleFunc.
func (T *AppCore) Handle(pattern string, handler http.Handler) {
	if T.webMux == nil {
		panic("AppCore.Handle called before framework wired the mux (only valid inside SimpleWebApp.Routes())")
	}
	T.webMux.Handle(pattern, handler)
}

// WebPrefix returns the app's URL prefix — useful when an app
// needs to build absolute URLs to its own routes (e.g. redirects).
// Returns "" before the framework has wired it.
func (T *AppCore) WebPrefix() string { return T.webPrefix }

// SetWebMux is called by the framework before SimpleWebApp.Routes()
// to wire the sub-mux. Apps don't call this directly.
func (T *AppCore) SetWebMux(mux *http.ServeMux, prefix string) {
	T.webMux = mux
	T.webPrefix = prefix
}

// RegisterRoutes is a no-op default so AppCore alone satisfies the
// WebApp interface. SimpleWebApp implementations don't need to
// write their own RegisterRoutes — the framework dispatches them
// through Routes() instead and never calls this method.
//
// Apps that need the legacy WebApp shape (custom access wrappers,
// fine-grained mux control) override this method explicitly.
func (T *AppCore) RegisterRoutes(mux *http.ServeMux, prefix string) {}

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

// HasDistinctLead reports whether a separate lead (precision) LLM is wired —
// i.e. escalating to lead would actually reach a different, stronger model
// rather than falling straight back to the worker. False when NoLead is set
// or no distinct lead tier is configured. UI uses this to gate the per-agent
// "use lead model" option (no point offering an escalation that's a no-op).
func (T *AppCore) HasDistinctLead() bool {
	return !T.NoLead && T.LeadLLM != nil && LeadIsDistinct()
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
	// servedByLead — true only when a SEPARATE lead LLM is wired
	// AND this session asked for LEAD. GetLeadLLM() silently falls
	// back to the worker LLM when no lead is configured; without
	// this check we'd attribute worker-served tokens as lead calls
	// (the cost dashboard would show phantom lead activity that
	// never appears in the [llm] debug log). LeadChat has its
	// own fellBackToWorker flag for the non-streaming case; this
	// is the streaming equivalent.
	servedByLead := s.Tier == LEAD && !s.agent.NoLead && s.agent.LeadLLM != nil
	var llm LLM
	if servedByLead {
		llm = s.agent.LeadLLM
	} else {
		llm = s.agent.LLM
	}
	resp, err := llm.ChatStream(ctx, messages, handler, opts...)
	if resp != nil {
		if servedByLead {
			resp.Tier = LEAD
		} else {
			resp.Tier = WORKER
		}
	}
	s.recordTokens(resp)
	if servedByLead {
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
	if err != nil && T.LeadLLM != nil && T.LLM != nil && LeadIsDistinct() {
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
	} else if resp.OutputTokens == 0 && resp.Content == "" && T.LeadLLM != nil && T.LLM != nil && LeadIsDistinct() {
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
	Server    string `json:"server"`    // SMTP server host:port (e.g. "smtp.gmail.com:587")
	From      string `json:"from"`      // Sender email address
	Username  string `json:"username"`  // SMTP auth username
	Password  string `json:"password"`  // SMTP auth password
	Recipient string `json:"recipient"` // Default report recipient email address
}

// WebSearchConfig holds web search provider settings.
type WebSearchConfig struct {
	Provider string `json:"provider"` // Search provider: "duckduckgo", "brave", "google", "searxng"
	APIKey   string `json:"api_key"`  // API key (not required for duckduckgo/searxng)
	Endpoint string `json:"endpoint"` // Custom endpoint (for searxng instances)
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
// "worker" or "worker (thinking)". Stages registered with Private=true
// are locked to the worker tier and never escalate, regardless of DB
// value — used by apps (e.g. servitor) that handle sensitive data and
// must not send it to a remote lead model.
func RouteToLead(key string) bool {
	if IsPrivateStage(key) {
		return false
	}
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
		probe.Think = func() *bool { v := true; return &v }()
	}
	// Dynamic think budget is now handled at the LLM client layer
	// (core/llm_openai.go applyDynamicThinkBudget) so every call
	// path — Chat, ChatStream, Session.ChatStream, WorkerChat —
	// gets the same correct sizing without duplicating logic here.
	// Callers can still override per-call via WithThinkBudget.
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

// CLIApp is an optional marker interface — opt-in for apps that
// have a meaningful command-line workflow beyond "use the dashboard."
// The menu hides every Agent that does NOT implement this from
// --help, and refuses CLI dispatch with a friendly "use serve"
// hint. Default is dashboard-only because that's where almost
// every app lives now; CLI is the exception.
//
// Implement as a no-op method on the agent struct:
//
//	func (T *MyApp) CLI() {}
type CLIApp interface {
	CLI()
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

// FrameworkTool is an optional interface tools implement to declare
// "I'm framework infrastructure — never offer me as a user-toggleable
// option in any picker." Tools tagged this way are still REGISTERED
// (they appear in RegisteredChatTools), still EXECUTABLE, and still
// wired into agent / phantom catalogs when the framework decides
// they're needed (workspace is always wired; stay_silent / keep_going
// ride along on every turn; skills appear when the owner
// has workers). They just don't show up in the chip pickers /
// allowed-tools lists that users see.
//
// IsFrameworkTool below is the canonical accessor — pickers consult
// it rather than maintaining their own skip lists.
type FrameworkTool interface {
	IsFrameworkTool() bool
}

// IsFrameworkTool reports whether the given ChatTool is framework
// infrastructure that should be hidden from user-facing pickers.
// Returns false for tools that don't implement FrameworkTool or
// implement it as false — back-compat default is "user-selectable".
func IsFrameworkTool(t ChatTool) bool {
	if ft, ok := t.(FrameworkTool); ok {
		return ft.IsFrameworkTool()
	}
	return false
}

// SingleFireTool is an optional interface ChatTool implementations
// can satisfy to declare "only one call to me per batch." When the LLM
// emits multiple parallel calls to a single-fire tool in one response,
// only the FIRST runs; the rest get a SKIPPED notice. The round itself
// CONTINUES — this isn't a round-abort; it's a per-tool batch dedup.
//
// Use for tools where multi-fire-per-batch is structurally wrong
// regardless of other tools: authoring actions (create_agent /
// add_tool), outbound communication (send_email / vapi_call), long-
// lived resource creation (watcher_create), or anything where
// bundling indicates the LLM is parallel-planning rather than
// reasoning step-by-step.
//
// For cross-tool single-fire (e.g. find_image + fetch_image attaching
// images — only one across the group fires), use AgentLoopConfig's
// SingleFireGroups field instead. Tool-level single-fire is implicit
// (auto-grouped as a one-element group); the explicit config handles
// multi-tool groups.
type SingleFireTool interface {
	SingleFirePerBatch() bool
}

// ToolSession carries mutable per-session state shared between the caller
// and session-aware tools. Pass a *ToolSession when building agent tools via
// GetAgentToolsWithSession; read results back from it after the loop completes.
type ToolSession struct {
	Images            []string           // base64-encoded images accumulated by image tools (delivered as outbound attachments / displayed inline)
	Videos            []string           // base64-encoded video data accumulated by video tools; consumers (phantom outbox) deliver as attachments
	PendingViewImages [][]byte           // raw image bytes a tool wants the LLM to see on its NEXT round; the agent loop's caller injects these as a synthetic user message before the next LLM call, then clears. NOT delivered to the user — different channel from Images.
	InboundMedia      []InboundMediaItem // turn-scoped registry of media that arrived on THIS turn (a contact's photo/clip), each addressable by a stable id (media#1, …) so the model can post a specific inbound item back BY ID. Populated at dispatch, listed via the media manifest, resolved by the outbound attachment collector. See RegisterInboundMedia.
	Silenced          bool               // set true by the stay_silent tool — caller suppresses the LLM's text reply but still flushes attachments
	LLM               LLM                // optional LLM made available to tools that need sub-calls
	LeadLLM           LLM                // optional lead/judge LLM for tools that want a higher-tier reasoner (delegate orchestrator); falls back to LLM when nil
	DB                Database           // optional DB handle for tools that need persistence (e.g. create_temp_tool with persist=true)
	WorkspaceDir      string             // absolute path to the sandbox dir for local-exec / file-I/O tools; empty disables sandboxed tools entirely
	WorkspaceID       string             // ID of the managed workspace currently active; "" when WorkspaceDir is the app's default user workspace, set by workspace(action=create|use)
	// ReplyAuthorizedKey, when set, is the recipient key (chat id or handle) of
	// the conversation this run is REPLYING to — a channel inbound the agent is
	// answering. Sending back to that same conversation is a reply, not a
	// proactive reach-out, so the messaging tools (send_message / message_contact)
	// deliver to it WITHOUT the approval queue. Empty on web / dispatch runs, so
	// the gate is unchanged everywhere except a live channel reply.
	ReplyAuthorizedKey string
	// StatusCallback, if set, is invoked by the send_status tool to deliver
	// an in-progress status message to the user mid-turn ("Working on it…").
	// Each app wires it differently: chat emits an SSE status event, phantom
	// enqueues an outbox item that becomes its own iMessage. If the callback
	// is nil the tool no-ops gracefully (apps that don't support live status
	// just ignore the call). Must be safe to call from a tool handler
	// goroutine — set it once at session creation and don't mutate after.
	StatusCallback func(text string)

	// ConnectPrompt, if set, is invoked by a tool that needs the user to
	// authorize an external integration (today: a per-user OAuth MCP server)
	// before it can run. The app wires it to surface an inline Connect
	// affordance — chat emits a connect_required block carrying the consent URL
	// so the user authorizes in-place instead of navigating to their Account
	// page. server is the integration id; the app builds the per-user connect
	// URL from it. Nil ⇒ the tool falls back to an actionable error only (a
	// standing-agent / wake turn with no live watcher). Must be safe to call
	// from a tool goroutine — set once at session creation, don't mutate after.
	ConnectPrompt func(server string)

	// SubAgentRunner spawns a one-shot sub-agent loop for tools that
	// need to dispatch their OWN LLM round (today: pipeline-mode
	// temp tools). Apps wire this on session creation; nil means
	// pipeline-mode tools degrade gracefully to an error. Signature:
	// (ctx, sysPrompt, userMessage, allowedToolNames, maxRounds) →
	// (final_text, error). The runner is responsible for capping
	// recursion depth so pipeline-calls-pipeline can't infinite-loop.
	SubAgentRunner func(ctx context.Context, sysPrompt, userMsg string, allowedToolNames []string, maxRounds int) (string, error)

	// TempTools holds tools the LLM defined at runtime (via the
	// create_temp_tool tool). They live for the session only — never
	// persisted, never shared across users. Apps that want to expose
	// runtime-defined tools wire AgentLoopConfig.DynamicTools to a
	// closure that converts these to AgentToolDef. Empty by default;
	// nil-safe.
	TempTools []*TempTool

	// Network is the framework-managed network-access gate for this
	// session and every descendant spawned from it. Top-level turns
	// build it from privacy mode; sub-agent dispatches
	// inherit the parent's connector (descendants can't be more
	// permissive than their parent). Tools that issue HTTP read it
	// via sess.NetworkAllowed() OR derive a context with the connector
	// via WithNetworkConnector(parentCtx, sess.Network) before calling
	// helpers that read from context. Nil = no constraint (back-compat
	// default; treated as allowed).
	Network *NetworkConnector

	// Ctx is the turn's cancelable context — the SAME context the agent
	// loop runs under, so it's canceled when the user stops the turn
	// (/api/cancel) or the run registry replaces it. Tools that spawn
	// their OWN synchronous sub-run (e.g. the delegate tool calling
	// RunDelegation) must derive their run context from this — via
	// sess.Context() — instead of context.Background(), so a cancel of
	// the parent turn also cancels the outgoing agent call. Without it
	// the sub-run is detached and keeps running after Stop. Nil-safe:
	// sess.Context() falls back to context.Background() when unset (apps
	// that don't wire it, or detached background runs).
	Ctx context.Context

	// Username scopes session-aware features that need a stable identity:
	// loading a user's persistent temp tools, scoping the workspace,
	// approval-queue association. Empty for unauthenticated sessions
	// (chat without auth) or apps that don't support per-user features
	// (phantom, since persistence isn't honored there).
	Username string

	// ChatSessionID identifies the chat session this tool call is running
	// within (chat app only). Used by tools that need to schedule recurring
	// callbacks back into the same conversation (schedule_chat_update,
	// stock-tracker style features). Empty for non-chat apps.
	ChatSessionID string

	// DispatchParentAgentID is set when this session runs a sub-agent dispatched
	// by a parent (e.g. Chat dispatching Builder). It carries the PARENT agent's
	// id so authoring tools can stamp creations as owned by the parent and route
	// them to the parent owner's approval queue. Empty for top-level turns.
	DispatchParentAgentID string

	// RoutingTarget is a generic "where does this session belong?" identifier
	// the host app stamps on at construction time. Format is "<prefix>:<ref>"
	// — e.g. "phantom:iMessage;-;+14155551234", "chat:<sessionID>". It scopes
	// session-bound state for tools that act on behalf of an originating
	// context; today it keys the managed workspace (see workspace_managed.go)
	// so files a tool writes for a phantom conversation land in a stable spot.
	RoutingTarget string

	// Files holds generic file attachments the LLM produced via
	// attach_file. Distinct from Images/Videos — those have inline
	// rendering paths in the chat UI and bridge protocol; Files is
	// for arbitrary types (PDFs, archives, CSVs, audio, etc.) and
	// renders as a download link in chat. Phantom currently ignores
	// this channel (delivery via the bridge's iMessage attachment
	// path is a future addition).
	Files []FileAttachment

	// Per-session "flushed up to here" markers for the attachment
	// channels. Used by the app's flush-new-attachments hook to
	// claim an exclusive range atomically — without this, parallel
	// tool-call goroutines that all snapshot len(sess.Images)
	// before any append capture the same starting index and each
	// goroutine ends up flushing the FULL accumulated slice. Each
	// image then gets delivered once per concurrent attach call:
	// 2 distinct attaches → 4 deliveries, 3 → 9, etc.
	//
	// The markers are read/written under mu. ClaimUnflushedImages
	// and friends return the previously-unflushed slice + advance
	// the marker so the same range can't be claimed twice.
	flushedImages int
	flushedVideos int
	flushedFiles  int

	mu sync.Mutex
}

// ClaimUnflushedImages returns the slice of images that haven't
// been claimed for delivery yet, and advances the per-session
// NetworkAllowed reports whether the session's network connector
// permits network calls. Nil session or nil connector = allowed
// (back-compat default; matches NetworkConnector.Allowed). Tools
// that issue HTTP should check this before making the call.
func (s *ToolSession) NetworkAllowed() bool {
	if s == nil {
		return true
	}
	return s.Network.Allowed()
}

// Context returns the session's turn context (s.Ctx), or
// context.Background() when unset or the session is nil. Tools that
// spawn a synchronous sub-run should root it here so a parent-turn
// cancel propagates to the child.
func (s *ToolSession) Context() context.Context {
	if s == nil || s.Ctx == nil {
		return context.Background()
	}
	return s.Ctx
}

// ContextWithNetworkConnector wraps ctx with the session's connector
// (when set) so context-based helpers like RunSandboxedShellWithEnv
// see the same gate. Returns ctx unchanged when no connector is set.
func (s *ToolSession) ContextWithNetworkConnector(ctx context.Context) context.Context {
	if s == nil || s.Network == nil {
		return ctx
	}
	return WithNetworkConnector(ctx, s.Network)
}

// flushed marker. Call once per tool-call dispatch (typically
// from the app's flushNewAttachments hook). Returns an empty
// slice when there's nothing new.
func (s *ToolSession) ClaimUnflushedImages() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flushedImages >= len(s.Images) {
		return nil
	}
	out := append([]string(nil), s.Images[s.flushedImages:]...)
	s.flushedImages = len(s.Images)
	return out
}

// ClaimUnflushedVideos — see ClaimUnflushedImages.
func (s *ToolSession) ClaimUnflushedVideos() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flushedVideos >= len(s.Videos) {
		return nil
	}
	out := append([]string(nil), s.Videos[s.flushedVideos:]...)
	s.flushedVideos = len(s.Videos)
	return out
}

// ClaimUnflushedFiles — see ClaimUnflushedImages.
func (s *ToolSession) ClaimUnflushedFiles() []FileAttachment {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flushedFiles >= len(s.Files) {
		return nil
	}
	out := append([]FileAttachment(nil), s.Files[s.flushedFiles:]...)
	s.flushedFiles = len(s.Files)
	return out
}

// FileAttachment is one entry in ToolSession.Files. Data is base64-
// encoded bytes; MimeType is sniffed from the content (not trusted
// from any user-supplied extension); Name is the workspace-relative
// path the LLM referenced — useful as the suggested filename when
// the user downloads.
type FileAttachment struct {
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // base64
	Size     int    `json:"size"` // raw byte count, pre-base64
}

// AppendFile records a file attachment on the session under the lock.
// Content-dedup by data payload: duplicate content with the same Name
// gets skipped to defend against LLMs that emit redundant attach
// calls in one batch.
func (s *ToolSession) AppendFile(f FileAttachment) {
	if s == nil || f.Data == "" {
		return
	}
	s.mu.Lock()
	for _, existing := range s.Files {
		if existing.Data == f.Data && existing.Name == f.Name {
			s.mu.Unlock()
			return
		}
	}
	s.Files = append(s.Files, f)
	s.mu.Unlock()
}

// TempToolMode determines how a temp tool's body is interpreted at
// dispatch time. Two modes today: shell command (the original) and
// secure-API call (wraps a registered credential).
const (
	TempToolModeShell      = "shell"
	TempToolModeAPI        = "api"
	TempToolModePipeline   = "pipeline"
	TempToolModePersistent = "persistent" // long-lived shell process; action-dispatched (open/send/read/interrupt/close)
	TempToolModeToolbox    = "toolbox"    // multi-action wrapper bundling several api-mode endpoints under one tool name; surfaces in the catalog as a GroupedTool with action="<sub>" dispatch
)

// TempToolAction is one sub-endpoint of a toolbox-mode TempTool.
// Each action is a single api-mode HTTP endpoint with its own params,
// URL template, method, optional body template, and optional response
// pipe. The parent TempTool's Credential is shared across actions —
// real-world API wrappers almost always have one credential per API,
// and putting it at the parent level avoids the LLM having to repeat
// it per action. To call: <toolbox_name>(action="<sub-action name>",
// <action-specific args>).
type TempToolAction struct {
	Name         string               `json:"name"`
	Description  string               `json:"description"`
	Params       map[string]ToolParam `json:"params,omitempty"`
	Required     []string             `json:"required,omitempty"`
	URLTemplate  string               `json:"url_template"`
	Method       string               `json:"method,omitempty"`
	BodyTemplate string               `json:"body_template,omitempty"`
	ResponsePipe string               `json:"response_pipe,omitempty"`
}

// TempTool is a runtime-defined tool created via create_temp_tool or
// create_api_tool. The LLM sees it in its tool catalog like any other
// tool, but it lives only for the current session unless persisted.
//
// Two execution modes:
//
//   - shell (default): CommandTemplate is a shell command run through
//     RunSandboxedShell. Placeholders are POSIX-shell-quoted. Requires
//     CapExecute.
//
//   - api: CommandTemplate is reinterpreted as the URL template of an
//     HTTP call against the named Credential. Placeholders in the URL
//     are URL-path-encoded; placeholders in BodyTemplate are JSON-encoded.
//     The auth header is injected server-side from the credential's
//     encrypted secret. Requires CapNetwork.
type TempTool struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Params      map[string]ToolParam `json:"params,omitempty"`
	Required    []string             `json:"required,omitempty"`
	// CommandTemplate is the body. Interpreted as a shell command in
	// shell mode and as a URL template in api mode. `{arg_name}`
	// placeholders are substituted with the args at dispatch time
	// (quoting/encoding rules depend on Mode).
	CommandTemplate string `json:"command_template"`
	// Mode picks the execution backend. Empty defaults to shell for
	// backward-compatibility with TempTool records written before this
	// field existed.
	Mode string `json:"mode,omitempty"`
	// Credential is the registered secure-API credential name this
	// tool dispatches through. Used in api mode only.
	Credential string `json:"credential,omitempty"`
	// Method is the HTTP method for api mode (default GET).
	Method string `json:"method,omitempty"`
	// BodyTemplate is an optional request body template for api mode.
	// `{arg_name}` placeholders are JSON-encoded.
	BodyTemplate string `json:"body_template,omitempty"`
	// ResponsePipe is an optional shell command for api mode that
	// receives the raw API response on stdin and emits the LLM-visible
	// result on stdout. Runs in a tight sandbox (no network, no
	// writable filesystem, /tmp tmpfs only) so it can use jq, awk,
	// grep, sed, etc. to filter / reshape responses before they reach
	// the LLM's context. Empty = LLM sees the raw response unchanged.
	// Adding a pipe upgrades the wrapper tool's required caps to
	// include CapExecute.
	ResponsePipe string `json:"response_pipe,omitempty"`
	// Recipe is a declarative manifest of files that get deployed
	// into a fresh sandbox dir on every dispatch. Replaces the older
	// tar.gz-snapshot model: the recipe is human-readable, diffable,
	// editable, and rebuilds identically every time. Empty for self-
	// contained shell tools (e.g. "uname -a") whose CommandTemplate
	// references no files.
	Recipe []RecipeFile `json:"recipe,omitempty"`

	// ScriptBody is the source of a script shipped alongside the
	// tool — Python, Bash, jq, awk, whatever. Stored IN the tool
	// record (DB-side) so the script survives workspace wipes:
	// at every dispatch the framework idempotently ensures
	// {workspace_dir}/<ScriptName> matches ScriptBody, writing it
	// if missing or stale. Without this, deleting the workspace
	// dir silently breaks tools whose command_template references
	// a script_body file that was only ever on disk.
	//
	// Distinct from Recipe: Recipe deploys files into an EPHEMERAL
	// per-dispatch tmpdir; ScriptBody persists into sess.WorkspaceDir
	// so it shares the workspace with other tools (find_image's
	// outputs, etc.). Use ScriptBody for "this tool needs my
	// script in the active workspace"; use Recipe for "this tool
	// needs an isolated tmpdir staged from scratch."
	ScriptBody string `json:"script_body,omitempty"`

	// ScriptName is the LLM-facing filename — what the LLM
	// originally chose (or defaulted to) at tool authoring time.
	// Appears verbatim in CommandTemplate; the LLM sees this when
	// reading back its own tool record. Defaults to "script.py".
	ScriptName string `json:"script_name,omitempty"`

	// CanonicalScriptName is the framework-assigned on-disk
	// filename: "<tool_name>_<content_hash>.<ext>". Always distinct
	// per (tool, content) so collisions are impossible. Hidden from
	// the LLM's view of the tool record; the dispatcher translates
	// every CommandTemplate reference to ScriptName into a reference
	// to CanonicalScriptName at dispatch time.
	CanonicalScriptName string `json:"canonical_script_name,omitempty"`

	// WorkspaceFiles carries HELPER files a shell tool's primary script
	// depends on — a Python module the script imports, a bash file it
	// sources, a lookup table it reads. Like ScriptBody they persist IN
	// the tool record (so the tool is self-contained: it exports whole
	// and survives a workspace wipe), but plural: ScriptBody is the one
	// entry script the command runs, WorkspaceFiles are everything it
	// pulls in. Each is redeployed to {workspace_dir}/<Path> at dispatch
	// under its LITERAL path (NOT canonicalized — the primary imports it
	// by that exact name). Empty for single-file tools. This is the
	// "a tool may bundle a few scripts underneath" case; distinct from
	// Recipe, which stages an ISOLATED tmpdir from scratch rather than
	// sharing the session workspace.
	WorkspaceFiles []RecipeFile `json:"workspace_files,omitempty"`

	// StatePath, when set, names a relative subdirectory inside the
	// deployed sandbox whose contents are preserved across dispatches.
	// Use for tools that legitimately need runtime state (counters,
	// accumulating logs, cached lookup DBs). Everything else outside
	// state_path is rebuilt fresh from the recipe each fire.
	StatePath string `json:"state_path,omitempty"`

	// HookCapabilities lists the SandboxHook methods this tool's
	// script is allowed to invoke. When non-empty, the dispatcher
	// starts a per-dispatch UDS hook server inside the workspace,
	// exposes its path via the GOHORT_HOOK_PATH env var, and the
	// shipped `gohort.py` helper module lets the script call back
	// into gohort for those narrow operations (fetch, log, secret,
	// fetch_via) WITHOUT opening the sandbox's network namespace.
	// Empty list means no hook is wired — zero surface area, same
	// posture as before the hook existed. Recognized methods:
	// "fetch", "log", "secret:<name>", "fetch_via:<name>".
	HookCapabilities []string `json:"hook_capabilities,omitempty"`

	// RawNetwork, when true, leaves the sandbox's network namespace
	// JOINED with the host's (no --unshare-net). The default is
	// false: shell-mode + persistent-mode tools run with the network
	// namespace cut, so a script that does urllib.request /
	// socket.connect / curl from inside the sandbox fails. Such
	// tools must declare hook_capabilities=["fetch"] and call
	// gohort.fetch(...) instead — gohort proxies HTTP on their
	// behalf with auditing.
	//
	// Reserve RawNetwork=true for the narrow cases where the tool
	// genuinely needs raw TCP from the sandbox (persistent-mode
	// psql / redis-cli / ssh-like REPLs that connect to a
	// non-HTTP protocol; legacy shell tools that haven't been
	// re-authored yet). Every new shell-mode tool that does HTTP
	// should use the hook, not RawNetwork.
	//
	// The session-level NetworkConnector still acts as a hard
	// upper bound — RawNetwork=true on a tool dispatched within a
	// private-mode session still gets no network. RawNetwork only
	// matters when the session would otherwise permit it.
	RawNetwork bool `json:"raw_network,omitempty"`

	// --- Pipeline-mode fields (Mode == "pipeline") ----------------------
	// Pipeline-mode tools are mini-agents exposed as a single tool.
	// On dispatch the framework spawns a sub-agent loop via the host
	// session's SubAgentRunner, builds the system prompt from
	// PipelinePrompt, gives the sub-agent PipelineTools as its tool
	// catalog, and returns the final text as the tool's result. Lets
	// admins compose multi-step flows ("research a company: search +
	// fetch + summarize") as a single LLM-callable tool without
	// writing code.

	// PipelinePrompt is the system prompt for the sub-agent loop.
	// `{arg_name}` placeholders are substituted with the dispatch args
	// (string-cast, no quoting needed since this lands in a prompt).
	PipelinePrompt string `json:"pipeline_prompt,omitempty"`

	// PipelineTools lists the tool names the sub-agent may call.
	// Subset of the parent session's catalog. Recursive pipeline calls
	// (a pipeline tool calling another pipeline tool) work but are
	// capped by the host runner to prevent infinite descent.
	PipelineTools []string `json:"pipeline_tools,omitempty"`

	// PipelineMaxRounds caps the sub-agent loop. Default 6 — enough
	// for a small multi-step flow without runaway cost.
	PipelineMaxRounds int `json:"pipeline_max_rounds,omitempty"`

	// Cache, when non-nil, enables persistent memoization of this
	// tool's result text keyed by the rendered Cache.Key (defaults to
	// the SHA-256 of all args). The canonical use is wrapping an
	// expensive remote fetch (download_video, transcribe_audio,
	// document conversion) so a re-call with the same args returns
	// the prior result instead of re-spending bandwidth or compute.
	// Cache entries are stored in RootDB; TTL bounds entry lifetime;
	// InvalidateWhen lets a side-effect-producing tool say "drop the
	// cache if the workspace artifact I previously wrote is gone."
	Cache *TempToolCache `json:"cache,omitempty"`

	// PipelineSteps is the structured alternative to PipelinePrompt.
	// When non-empty, the pipeline runs DETERMINISTICALLY: each step's
	// tool is called in order with its (template-substituted) args, no
	// sub-agent / no per-step LLM. Cheap, fast, predictable — right for
	// linear chains like "search → fetch → summarize" where no
	// reasoning is needed between steps. PipelinePrompt is ignored when
	// PipelineSteps is set. Mutually exclusive in practice: set one or
	// the other based on whether the chain needs adaptive logic.
	PipelineSteps []PipelineStep `json:"pipeline_steps,omitempty"`

	// --- Persistent-shell-mode fields (Mode == "persistent") ------------
	// Persistent shells host a long-lived process inside the sandbox
	// (bash, psql, ssh, etc.) and accept commands across multiple LLM
	// turns. The bwrap process AND the inner shell both stay alive
	// from the first send until close or session end. State (env vars,
	// working directory, mounted FS, login session) persists between
	// calls.

	// PersistentOpenCmd is the shell command that launches the long-
	// lived process inside the sandbox. Examples: "bash", "psql -h
	// dev-db -U app", "ssh user@host" (when keys are reachable). The
	// command runs through the same bwrap as one-shot shell mode but
	// the bwrap process itself is kept alive across tool calls.
	PersistentOpenCmd string `json:"persistent_open_cmd,omitempty"`

	// PersistentPromptPattern is a regex matched against the trailing
	// bytes of the shell's output to decide when "the shell is ready
	// for the next command." When the pattern matches, the send action
	// returns (complete=true). Default patterns are mode-dependent but
	// authors should set this explicitly for known shells:
	//   bash:  `[\$#] $`
	//   psql:  `\w+=> $`
	//   ssh:   depends on the remote shell's PS1
	PersistentPromptPattern string `json:"persistent_prompt_pattern,omitempty"`

	// PersistentSendTimeoutSec caps how long a send action waits for
	// the prompt to reappear before returning with complete=false (the
	// LLM should call read for more). Default 5s when unset.
	PersistentSendTimeoutSec int `json:"persistent_send_timeout_sec,omitempty"`

	// --- Toolbox-mode fields (Mode == TempToolModeToolbox) ----------------
	// Toolbox-mode tools bundle multiple api-mode endpoints under one
	// tool name. The catalog shows ONE entry; the LLM picks a sub-
	// endpoint via action="<name>". Same shape as the framework's
	// built-in grouped tools (tool_def / agents / workspace). Useful
	// when wrapping an API surface with several related endpoints
	// (GitHub: get_user / get_repo / list_issues; Stripe: list_charges /
	// create_invoice / etc.) — keeps the catalog clean and shares one
	// Credential across all endpoints.
	Actions []TempToolAction `json:"actions,omitempty"`
}

// TempToolCache is the declarative memoization spec attached to a
// TempTool. All fields optional; an empty struct caches everything
// forever under user scope, which is fine for tools where every
// argument combination genuinely produces the same result.
type TempToolCache struct {
	// Key is a {param}-templated string that produces the cache key.
	// Empty defaults to a stable hash of all args, so semantically-
	// identical calls land on the same entry regardless of arg order.
	Key string `json:"key,omitempty"`

	// TTL is a duration string (e.g. "30d", "12h", "10m"). Empty =
	// no expiry (entry lives until an InvalidateWhen check drops it
	// or the table is wiped).
	TTL string `json:"ttl,omitempty"`

	// Scope determines the cache partition. Choices:
	//   "user"    — keyed by sess.Username (default; cross-session
	//               dedup for one user).
	//   "session" — keyed by sess.ChatSessionID (per-conversation).
	//   "global"  — shared across all users / sessions (use only
	//               when the result is content-addressable and
	//               privacy-safe, e.g. public URL → video bytes).
	// When the required identifier is missing on the session
	// (sessionless run, anonymous CLI invocation, etc.) caching is
	// silently skipped rather than broadening to a less-restrictive
	// scope.
	Scope string `json:"scope,omitempty"`

	// InvalidateWhen is a list of post-hit verification checks. Each
	// string has the form "kind:expression". Today one kind is
	// supported:
	//   "file_exists:<path-template>"  — the rendered path must
	//   exist on disk (templated against args + {workspace_dir}).
	// Use to keep the cache honest when the tool's side effect (a
	// workspace file) might have been reaped between the original
	// run and the hit.
	InvalidateWhen []string `json:"invalidate_when,omitempty"`
}

// PipelineStep is one rung of a structured (deterministic) pipeline.
// Args values are strings or other JSON-encodable values; string args
// undergo template substitution before the tool fires:
//
//   - {param_name}    → value of the caller-supplied parameter
//   - $N              → full output of step N (1-indexed)
//   - $N.field.path   → JSON field path into step N's output
//     (returns empty string if step N's output isn't
//     JSON or the path doesn't resolve)
//
// Optional Name lets later steps reference outputs by name instead
// of by index: $name.field works the same as $N.field. Mostly a
// readability convenience for longer pipelines.
type PipelineStep struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
	Name string         `json:"name,omitempty"`
}

// RecipeFile is one file in a TempTool's deployment recipe. Path is
// relative to the deployed sandbox dir. Mode defaults to 0700 if
// unset (sufficient for scripts and data files).
type RecipeFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    uint32 `json:"mode,omitempty"`
}

// AppendTempTool registers a temp tool on the session. Returns an error
// if the name conflicts with an existing temp tool (caller is expected
// to have already validated against the static catalog).
func (s *ToolSession) AppendTempTool(t *TempTool) error {
	if s == nil || t == nil {
		return fmt.Errorf("nil session or tool")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.TempTools {
		if existing.Name == t.Name {
			return fmt.Errorf("temp tool %q already exists in this session", t.Name)
		}
	}
	s.TempTools = append(s.TempTools, t)
	return nil
}

// HasTempTool returns true when a temp tool with the given name is
// already registered on this session. Callers that load temp tools
// from multiple layered sources (e.g. user pool + agent-scoped +
// session drafts) use this to skip a redundant append silently
// instead of trying to append and treating the "already exists"
// error as a real failure.
func (s *ToolSession) HasTempTool(name string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.TempTools {
		if existing.Name == name {
			return true
		}
	}
	return false
}

// RemoveTempTool deletes a temp tool from the session by name. Returns
// true if a tool was actually removed.
func (s *ToolSession) RemoveTempTool(name string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.TempTools {
		if t.Name == name {
			s.TempTools = append(s.TempTools[:i], s.TempTools[i+1:]...)
			return true
		}
	}
	return false
}

// CopyTempTools returns a snapshot of the session's temp tools. Used by
// the agent loop's DynamicTools hook to convert them to AgentToolDef
// without holding the lock during the conversion.
func (s *ToolSession) CopyTempTools() []*TempTool {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.TempTools) == 0 {
		return nil
	}
	out := make([]*TempTool, len(s.TempTools))
	copy(out, s.TempTools)
	return out
}

// AppendImage appends a base64-encoded image to the session image list.
func (s *ToolSession) AppendImage(b64 string) {
	if s == nil || b64 == "" {
		return
	}
	s.mu.Lock()
	// Content-dedup: if the same b64 is already queued for this
	// turn's delivery, skip the duplicate. Catches "LLM emitted
	// three workspace(attach) calls with the same path in one
	// batch" — they all hash the same file content, only the
	// first gets queued, the rest no-op. Cheaper than maintaining
	// a separate fingerprint set since we already hold the lock
	// and the slice is bounded per-turn.
	for _, existing := range s.Images {
		if existing == b64 {
			s.mu.Unlock()
			Debug("[sess.AppendImage] skipped duplicate (content already attached this turn); caller=%s", callerSummary(2))
			return
		}
	}
	s.Images = append(s.Images, b64)
	n := len(s.Images)
	s.mu.Unlock()
	// Debug log — captures the call site + a content hash (md5
	// of full b64) so duplicate vs distinct appends are visible
	// at a glance. The earlier "first 16 chars" fingerprint was
	// misleading for JPEGs because they all share the JFIF
	// header prefix; md5 of full content disambiguates.
	sum := md5.Sum([]byte(b64))
	fp := hex.EncodeToString(sum[:4]) // 8 hex chars — plenty unique for visual scan
	Debug("[sess.AppendImage] now %d image(s); size=%dB; md5_8=%s; caller=%s", n, len(b64), fp, callerSummary(2))
}

// callerSummary returns a short "file:line func" string from N frames
// up the stack. Used by sess.AppendImage's debug log to point at
// what tool / handler actually appended an image, so duplicate
// appends can be traced to their source. Skip=2 names the function
// that called AppendImage (skip=0 is callerSummary itself, skip=1
// is AppendImage, skip=2 is the caller).
func callerSummary(skip int) string {
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "?"
	}
	fn := runtime.FuncForPC(pc)
	name := "?"
	if fn != nil {
		name = fn.Name()
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
	}
	// File path → basename for readability.
	if i := strings.LastIndexByte(file, '/'); i >= 0 {
		file = file[i+1:]
	}
	return fmt.Sprintf("%s:%d %s", file, line, name)
}

// AppendVideo appends a base64-encoded video to the session video list.
// Consumed by apps that deliver outbound attachments (phantom iMessage
// repost path); ignored by apps that don't. Same content-dedup as
// AppendImage — duplicate-content appends are skipped to defend
// against LLMs that emit redundant workspace(attach) calls in one
// batch with the same path.
func (s *ToolSession) AppendVideo(b64 string) {
	if s == nil || b64 == "" {
		return
	}
	s.mu.Lock()
	for _, existing := range s.Videos {
		if existing == b64 {
			s.mu.Unlock()
			Debug("[sess.AppendVideo] skipped duplicate (content already attached this turn); caller=%s", callerSummary(2))
			return
		}
	}
	s.Videos = append(s.Videos, b64)
	s.mu.Unlock()
}

// InboundMediaItem is one addressable piece of media that arrived on THIS turn
// (a contact's photo or clip on a channel). It carries the bytes inline because,
// unlike produced media, inbound media has no workspace file to read back at
// delivery time. The ID (media#1, media#2, …) is the handle exposed to the model
// via the media manifest so it can post a SPECIFIC inbound item back BY ID.
type InboundMediaItem struct {
	ID     string // "media#1" — the handle shown to the model
	Kind   string // "image" | "video"
	B64    string // base64-encoded bytes, ready to attach outbound
	Sender string // display name of who sent it, for the manifest
}

// RegisterInboundMedia records an inbound attachment and returns its assigned id
// (media#1, media#2, …). Order-stable so the id matches the position the media
// manifest shows the model. Raw bytes in, base64 stored. The gap this closes:
// produced media (image action=find/generate) is already postable by its
// workspace filename, but inbound media had NO handle, so the model could only
// describe a photo someone sent, never re-send it. Turn-scoped: dispatch mints a
// fresh session per turn, so ids never point at stale bytes.
func (s *ToolSession) RegisterInboundMedia(kind string, raw []byte, sender string) string {
	if s == nil || len(raw) == 0 {
		return ""
	}
	if strings.TrimSpace(kind) == "" {
		kind = "image"
	}
	s.mu.Lock()
	id := fmt.Sprintf("media#%d", len(s.InboundMedia)+1)
	s.InboundMedia = append(s.InboundMedia, InboundMediaItem{
		ID:     id,
		Kind:   kind,
		B64:    base64.StdEncoding.EncodeToString(raw),
		Sender: strings.TrimSpace(sender),
	})
	s.mu.Unlock()
	return id
}

// ResolveInboundMedia maps a media-id reference ("media#2") to its base64 bytes
// and kind. Match is space- and case-tolerant (see normalizeMediaID). ok is
// false when ref is not a known inbound id, so the outbound collector falls
// through to workspace-filename resolution (the produced-media path).
func (s *ToolSession) ResolveInboundMedia(ref string) (b64, kind string, ok bool) {
	if s == nil {
		return "", "", false
	}
	want := normalizeMediaID(ref)
	if want == "" {
		return "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.InboundMedia {
		if normalizeMediaID(m.ID) == want {
			return m.B64, m.Kind, true
		}
	}
	return "", "", false
}

// normalizeMediaID canonicalizes a media-id reference so "media#2", "media #2",
// and "MEDIA#2" all match the stored "media#2". Returns "" when ref is not a
// media id at all (e.g. a workspace filename), which is the signal for callers
// to fall through to file resolution. Strict on the tail: only digits follow the
// hash, so a filename that merely starts with "media" never false-matches.
func normalizeMediaID(ref string) string {
	r := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(ref)), " ", "")
	if !strings.HasPrefix(r, "media#") {
		return ""
	}
	num := strings.TrimPrefix(r, "media#")
	if num == "" {
		return ""
	}
	for _, c := range num {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return "media#" + num
}

// AppendViewImage queues a raw image byte slice (typically a sampled video
// frame) for the LLM to see on its next round. Tools that want the LLM to
// "look at" something call this; the agent loop's caller is responsible
// for draining and injecting them as a synthetic user message before the
// next LLM call. These bytes never reach the user — they're LLM-input only.
func (s *ToolSession) AppendViewImage(data []byte) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.PendingViewImages = append(s.PendingViewImages, data)
	s.mu.Unlock()
}

// DrainViewImages returns and clears any pending view images. Callers
// should drain right before the next LLM call, after tool results have
// been appended to history; the frames go into a synthetic user message
// with Images set so buildMessages handles them through the standard
// vision content path.
func (s *ToolSession) DrainViewImages() [][]byte {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.PendingViewImages) == 0 {
		return nil
	}
	out := s.PendingViewImages
	s.PendingViewImages = nil
	return out
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
	Critical        = nfo.Critical        // err-or-nil → Fatal helper
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
