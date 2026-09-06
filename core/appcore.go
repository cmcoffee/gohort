package core

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

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
	// NoLead marks this agent as handling material that must not reach a
	// third-party model. When it BINDS, LeadChat() redirects to the worker and
	// RunAgentLoop ignores the LEAD tier.
	//
	// Read it through LeadDenied(), never directly: the pin exists because the
	// lead is normally remote, and a deployment where every model is local has
	// no reason to keep the most sensitive agent on the weaker model. See
	// llm_privacy.go.
	NoLead bool

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

// promptToolsMode is the effective prompt-tools setting for this agent: the
// process-wide published value when there is one, else the agent's own field.
// The published value wins so a live LLM reload actually changes the mode —
// see SetPromptToolsMode.
func (T *AppCore) promptToolsMode() bool {
	if mode, ok := PromptToolsMode(); ok {
		return mode
	}
	return T.PromptTools
}

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

// Private marks this AppCore as handling material that must not reach a
// third-party model. Call once in Init() or Main().
//
// It means "private-appropriate", not "worker forever" — see LeadDenied.
func (T *AppCore) Private() { T.NoLead = true }

// LeadDenied reports whether this agent's private pin currently BINDS.
//
// Two questions, deliberately separate. NoLead is what the app declared about
// its own material and never changes at runtime. LeadDenied is whether that
// declaration currently costs the agent the lead tier, and it does not when the
// operator has declared every model private — at which point escalating keeps
// the data on hardware they control and the pin is pure loss on exactly the
// work that most needs a stronger reasoner.
//
// Every read of NoLead outside this method is a bug: it would enforce the pin
// in a deployment that has explicitly lifted it, and the symptom is servitor
// silently staying on the worker with nothing on screen to explain why.
func (T *AppCore) LeadDenied() bool { return T.NoLead && !AllLLMsPrivate() }

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
	if T.LeadDenied() || T.LeadLLM == nil {
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
	if T.LeadDenied() {
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
	return !T.LeadDenied() && T.LeadLLM != nil && LeadIsDistinct()
}

// leadUnavailableReason says WHY an escalation could not happen, in the words
// somebody would need to fix it. Three different settings produce the same
// silent worker call, and "it went to the worker" is not a diagnosis.
func leadUnavailableReason(T *AppCore) string {
	switch {
	case T == nil:
		return "no LLM stack is wired"
	case T.LeadDenied():
		return "this deployment is pinned to private models (no-lead), which no per-run setting can override"
	// Asked BEFORE "none is configured", because a lead that failed to start
	// leaves exactly the same nil and is a completely different problem.
	case LeadInitError() != "":
		return LeadInitError()
	case T.LeadLLM == nil:
		return "no lead model is configured for this app"
	case !LeadIsDistinct():
		return "no separate lead model is configured — the lead and the worker would be the same model, so there is nothing to escalate to (set one in the LLM settings)"
	}
	return "the lead was unavailable"
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

// loopContextSize is the window an agent loop has to stay inside: the SMALLER
// of the tiers the turn might run on.
//
// A loop is not pinned to one tier — it starts on the route's model and can
// escalate mid-turn — so a single budget has to be safe for whichever it lands
// on, and the smaller window is the only one that is. The cost of being wrong
// in this direction is that a large-window turn sheds some old tool BODIES
// slightly early, and the model can re-run any tool whose body went; the cost
// of being wrong in the other direction is the server quietly dropping the
// system prompt mid-turn.
//
// Zero when no tier reports a window (a model that isn't a ContextSizer), which
// leaves compaction off exactly as before — no invented number.
func (T *AppCore) loopContextSize() int {
	worker, lead := T.WorkerContextSize(), T.LeadContextSize()
	switch {
	case worker <= 0:
		return lead
	case lead <= 0:
		return worker
	case lead < worker:
		return lead
	}
	return worker
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
