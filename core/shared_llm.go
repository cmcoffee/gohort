package core

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/prompts"
)

// Process-wide default worker and lead LLM references. Set at startup by the
// application, and re-set live when the admin UI changes LLM config (so a model
// / provider / key swap takes effect without a restart). Apps hold a stable
// reloadable handle (ReloadableWorkerLLM / ReloadableLeadLLM) rather than the
// concrete LLM, so a swap reaches every reference without re-threading.
var (
	sharedMu        sync.RWMutex
	sharedWorkerLLM LLM
	sharedLeadLLM   LLM
	llmReloader     func() error
)

// SetSharedLLMs registers the process-wide default worker and lead LLMs. Called
// at startup and again on a live reload. Safe to call concurrently with chat
// traffic — the swap is under a write lock and reads take the read lock.
func SetSharedLLMs(worker, lead LLM) {
	sharedMu.Lock()
	sharedWorkerLLM = worker
	sharedLeadLLM = lead
	sharedMu.Unlock()
}

// SharedWorkerLLM returns the default worker LLM, or nil if none has been
// configured. Tools should guard against nil and return a descriptive error
// rather than panic when the app hasn't wired an LLM (e.g., `--help`).
func SharedWorkerLLM() LLM {
	sharedMu.RLock()
	defer sharedMu.RUnlock()
	return sharedWorkerLLM
}

// SharedLeadLLM returns the default lead LLM, or nil if not configured.
func SharedLeadLLM() LLM {
	sharedMu.RLock()
	defer sharedMu.RUnlock()
	return sharedLeadLLM
}

// LeadIsDistinct reports whether a separate lead (precision) LLM provider is
// configured — i.e. escalating to lead reaches a different model than the
// worker. This is the ONLY safe way to ask that question: do NOT compare the
// LLM interface values directly (T.LeadLLM != T.LLM). Apps hold reloadableLLM
// forwarding wrappers, which are structs with a func field — uncomparable, so
// == / != panics at runtime ("comparing uncomparable type"). The shared lead
// reference is nil exactly when no distinct lead is wired (ReloadableLeadLLM
// then falls back to the worker), so a nil check answers it without comparing.
func LeadIsDistinct() bool { return SharedLeadLLM() != nil }

// leadInitErr records a lead model that IS configured and failed to start.
//
// The distinction matters more than it looks. "No lead is configured" and "the
// lead you configured could not be built" produce the identical symptom — every
// escalation runs on the worker — and lead to opposite actions: configure one,
// versus go find out why the one you have won't start. Reporting the first when
// the second is true sends somebody to re-enter settings that were already
// right, which is exactly what happened with a Bedrock lead whose AWS
// credentials no longer resolved at boot.
//
// leadRuntimeErr is the OTHER way a lead goes away: it built fine and later
// stopped answering. An SSO session reaching the end of its life is the one
// that cost eleven hours: every lead call fell back to the worker, nothing
// recorded it, so the background retry never ran and no surface said a word.
// The only symptom was answers being worse than they should be, which is the
// same symptom as no lead being configured at all.
//
// Kept SEPARATE from leadInitErr rather than folded into it, because the two
// want opposite remedies. An init failure is fixed by REBUILDING, which is
// what the background loop does every two minutes. A runtime failure already
// has a live client that re-resolves its own credentials on the next call, so
// the thing that proves it is over is a call that WORKS. Folded together, the
// loop's rebuild would clear a runtime failure with no successful call behind
// it, which is how a diagnosis starts lying.
var (
	leadInitMu     sync.RWMutex
	leadInitErr    string
	leadRuntimeErr string
	// What is actually installed, per tier. Guarded by the same mutex as the
	// lead's error state because the two are read together: "why is the lead
	// broken" and "what is answering instead" are one question.
	liveWorkerLLM string
	liveLeadLLM   string
)

// SetLeadInitError records (or with a nil err, clears) the reason a configured
// lead model is unavailable. Called by whoever builds the LLMs — startup and the
// admin's live reload alike, so a fixed config clears a stale complaint.
func SetLeadInitError(provider, model string, err error) {
	leadInitMu.Lock()
	defer leadInitMu.Unlock()
	// Either way this is a NEW client, so the old one's call history no longer
	// describes anything. A runtime complaint that outlives the client it was
	// about is the same stale-diagnosis problem leadInitErr was added to fix.
	leadRuntimeErr = ""
	if err == nil {
		leadInitErr = ""
		return
	}
	leadInitErr = "the configured lead model (" + provider + "/" + model + ") could not be initialized: " + err.Error()
	// The lead that IS running is whatever was installed before this attempt,
	// and saying so is the whole point: "saved but not applied" is invisible
	// otherwise.
	if liveLeadLLM != "" {
		leadInitErr += ". Until this is fixed, lead calls keep going to the previously loaded " + liveLeadLLM
	}
}

// SetLiveLLMs records WHAT IS RUNNING, as a short description per tier, and is
// called only where an LLM is actually installed.
//
// The admin form shows what is STORED. Those are not the same thing and the
// gap has a specific cause: a save writes the config and then rebuilds, and a
// rebuild that fails leaves the previous client live (see reloadSharedLLMs,
// which returns before SetSharedLLMs). The form then describes a system nobody
// is running, the only evidence is one log line, and an operator can spend an
// evening changing a setting that is already correct.
//
// A description rather than the config: the thing worth showing is the model
// and the host a call actually goes to, which for Bedrock is not derivable
// from the model field alone.
func SetLiveLLMs(worker, lead string) {
	leadInitMu.Lock()
	liveWorkerLLM, liveLeadLLM = worker, lead
	leadInitMu.Unlock()
}

// LiveLLMs returns those descriptions, empty before anything is installed.
func LiveLLMs() (worker, lead string) {
	leadInitMu.RLock()
	defer leadInitMu.RUnlock()
	return liveWorkerLLM, liveLeadLLM
}

// LeadInitError returns that reason, or "" when the lead is fine or absent.
func LeadInitError() string {
	leadInitMu.RLock()
	defer leadInitMu.RUnlock()
	return leadInitErr
}

// leadRuntimeError returns why the lead's last call failed, or "" when it
// worked or none has been made.
//
// Unexported, with its two recorders: every producer and every reader is in
// this package, and the retry loop in the main package must keep reading
// LeadInitError alone, because a rebuild is not evidence about this one.
func leadRuntimeError() string {
	leadInitMu.RLock()
	defer leadInitMu.RUnlock()
	return leadRuntimeErr
}

// noteLeadCallFailed records a lead call that failed, and says so ONCE.
//
// Once, because the failure repeats on every call and the fact worth having
// is the moment escalation stopped working. Repeating it per call would bury
// that moment in its own echo, and teach somebody to filter the line.
func noteLeadCallFailed(err error) {
	if err == nil {
		return
	}
	leadInitMu.Lock()
	first := leadRuntimeErr == ""
	leadRuntimeErr = "the lead model stopped answering: " + err.Error()
	leadInitMu.Unlock()
	if first {
		// No period of its own: a provider message may or may not end in one,
		// and a hardcoded "." produced "us-east-1.." on every Bedrock refusal
		// that carries a hint. endSentence adds one only where one is missing.
		Warn("[llm] the lead model stopped answering: %s Every escalation runs on the WORKER until a lead call succeeds again.", endSentence(err.Error()))
	}
}

// noteLeadCallSucceeded clears it. A call that worked is the only honest
// evidence a runtime failure is over: rebuilding the client proves the
// constructor runs, which for several providers does no work at all.
func noteLeadCallSucceeded() {
	// The overwhelmingly common path is "nothing was wrong", so it takes the
	// read lock and stops. The write lock is only for the transition.
	leadInitMu.RLock()
	set := leadRuntimeErr != ""
	leadInitMu.RUnlock()
	if !set {
		return
	}
	leadInitMu.Lock()
	cleared := leadRuntimeErr != ""
	leadRuntimeErr = ""
	leadInitMu.Unlock()
	if cleared {
		Log("[llm] the lead model is answering again, escalations have resumed.")
	}
}

// RegisterLLMReloader installs the function that rebuilds the shared worker +
// lead LLMs from current config and swaps them in via SetSharedLLMs. Called once
// at startup by the main package (which owns config access). Lets the admin UI
// apply LLM config changes live.
func RegisterLLMReloader(fn func() error) { llmReloader = fn }

// ReloadLLMs rebuilds the shared LLMs from current config. No-op when no reloader
// is registered (e.g. CLI invocations). Returns the rebuild error so the caller
// can surface a bad config; on error the previous LLMs stay active.
func ReloadLLMs() error {
	if llmReloader == nil {
		return nil
	}
	return llmReloader()
}

// reloadableLLM forwards every call to whatever the getter currently returns, so
// a handle captured once (by an app at startup) always uses the live shared LLM
// after a reload. The LLM interface is just Chat + ChatStream.
//
// It is also where token usage is recorded, because it is the ONE place every
// call in the shipped framework passes through. The AppCore chat wrappers
// (WorkerChat / LeadChat / ChatStreamWithReport) record too, but only calls
// that go through them: roughly twenty call sites — the turn judge, the
// grounding judge, gap check, channel gatekeeping, operator compaction, every
// suggest/draft helper — reach LLM.Chat directly, and their spend was invisible
// to the cost history entirely. Instrumenting the handle catches them without
// asking every future call site to remember, and without the wrappers' side
// effects (WorkerChat turns thinking on by default, which is not something a
// judge parsing strict JSON should inherit just to get counted).
type reloadableLLM struct {
	get func() LLM
	// lead marks this as the LEAD-tier handle. Attribution follows the HANDLE
	// rather than the caller's intent, which is what makes it correct: a lead
	// request that routed to the worker went through the worker handle and is
	// worker spend, priced at worker rates.
	lead bool
}

func (r reloadableLLM) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	llm := r.get()
	if llm == nil {
		return nil, errors.New("no LLM configured")
	}
	done, err := r.beginModelUse(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	resp, err := llm.Chat(ctx, messages, r.withTierText(opts)...)
	r.record(ctx, resp)
	r.noteLeadHealth(ctx, err)
	return resp, err
}

func (r reloadableLLM) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	llm := r.get()
	if llm == nil {
		return nil, errors.New("no LLM configured")
	}
	done, err := r.beginModelUse(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	resp, err := llm.ChatStream(ctx, messages, handler, r.withTierText(opts)...)
	r.record(ctx, resp)
	r.noteLeadHealth(ctx, err)
	return resp, err
}

// People first.
//
// Some work can wait: an Optimize run spends hours of a model on builds of its
// own, and someone asking a question meanwhile should not queue behind it. The
// gate (SetYieldGate) sees every call through these handles before it goes
// out, holds back the ones made for such work while people need the model, and
// says which they are. Every other call is someone's, and is counted against
// the tier it reaches, so the waiting work can see that its model is in use
// (ModelInUse). A call already out runs to its end.
type modelUse struct {
	now  int
	last time.Time
}

var (
	modelUseMu sync.Mutex
	modelUses  = map[LLMTier]*modelUse{}
	yieldGate  func(ctx context.Context, tier LLMTier) (yielding bool, err error)
)

// SetYieldGate sets what every call through the shared handles passes before
// it goes out, told the tier it reaches. For work that gives way it blocks
// while people need the model and answers yielding, which keeps the call out
// of the count; an error stops the call. nil removes it.
func SetYieldGate(fn func(ctx context.Context, tier LLMTier) (yielding bool, err error)) {
	modelUseMu.Lock()
	yieldGate = fn
	modelUseMu.Unlock()
}

// ModelInUse says whether someone's call to tier is out now, or ended within
// quiet: a turn makes its calls with tool work between them, and work that
// resumed in every gap would be in the way of each next call.
func ModelInUse(tier LLMTier, quiet time.Duration) bool {
	modelUseMu.Lock()
	defer modelUseMu.Unlock()
	u := modelUses[tier]
	return u != nil && (u.now > 0 || (!u.last.IsZero() && time.Since(u.last) < quiet))
}

// beginModelUse passes a call through the gate and counts it when it is
// someone's. done ends it.
func (r reloadableLLM) beginModelUse(ctx context.Context) (done func(), err error) {
	tier := r.serving()
	modelUseMu.Lock()
	gate := yieldGate
	modelUseMu.Unlock()
	yielding := false
	if gate != nil {
		if yielding, err = gate(ctx, tier); err != nil {
			return nil, err
		}
	}
	if yielding {
		return func() {}, nil
	}
	modelUseMu.Lock()
	u := modelUses[tier]
	if u == nil {
		u = &modelUse{}
		modelUses[tier] = u
	}
	u.now++
	modelUseMu.Unlock()
	return func() {
		modelUseMu.Lock()
		u.now--
		u.last = time.Now()
		modelUseMu.Unlock()
	}, nil
}

// record credits the call to whichever tier this handle serves. Runs on the
// error path as well — a call that failed after its prompt went out is still
// billed for the prompt.
//
// The LEAD handle falls back to the worker when no distinct lead is configured
// ("use primary"), so it asks whether one exists RIGHT NOW rather than assuming
// its own name. Without that check, a worker-only deployment — a free local
// model with the cloud lead rates still filled in — would price every escalated
// call at lead rates and invent a bill.
func (r reloadableLLM) record(ctx context.Context, resp *Response) {
	RecordUsage(ctx, r.serving(), resp)
}

// serving is the tier this handle reaches right now.
func (r reloadableLLM) serving() LLMTier {
	if r.lead && SharedLeadLLM() != nil {
		return LEAD
	}
	return WORKER
}

// withTierText adds, last, the swap of each prompt block's shared text for
// the serving tier's own (prompts.ApplyTierText). Here and not where the
// prompt is assembled, because this is the first point the tier is final:
// every reroute from lead to worker (denied, routed, failed, empty,
// de-escalated, forced final) arrives through the worker handle and so gets
// the worker's words, whatever the caller built the prompt for.
func (r reloadableLLM) withTierText(opts []ChatOption) []ChatOption {
	tier := prompts.TierWorker
	if r.serving() == LEAD {
		tier = prompts.TierLead
	}
	return append(append([]ChatOption{}, opts...), func(c *ChatConfig) {
		c.SystemPrompt = prompts.ApplyTierText(tier, c.SystemPrompt)
		c.Tools = tunedToolDescriptions(tier, c.Tools)
	})
}

// tunedToolDescriptions applies the edits to shipped tools' descriptions
// and their parameters' (prompts.ToolDescriptionFor and
// ToolParamDescriptionFor), recording each shipped description as its block
// on the way. A copy, never the caller's slice or parameter maps: the same
// tool list goes out on the next call, and to the other tier.
func tunedToolDescriptions(tier string, tools []Tool) []Tool {
	var out []Tool
	for i, t := range tools {
		if prompts.TunableToolGroup(t.Name) == "" {
			continue
		}
		prompts.ObserveToolDescription(t.Name, t.Description)
		desc := prompts.ToolDescriptionFor(tier, t.Name, t.Description)
		var params map[string]ToolParam
		for name, p := range t.Parameters {
			prompts.ObserveToolParamDescription(t.Name, name, p.Description)
			pd := prompts.ToolParamDescriptionFor(tier, t.Name, name, p.Description)
			if pd == p.Description {
				continue
			}
			if params == nil {
				params = make(map[string]ToolParam, len(t.Parameters))
				for k, v := range t.Parameters {
					params[k] = v
				}
			}
			p.Description = pd
			params[name] = p
		}
		if desc == t.Description && params == nil {
			continue
		}
		if out == nil {
			out = append([]Tool(nil), tools...)
		}
		out[i].Description = desc
		if params != nil {
			out[i].Parameters = params
		}
	}
	if out == nil {
		return tools
	}
	return out
}

// noteLeadHealth records whether the LEAD tier is answering, from the calls
// themselves. This handle is the one place every lead call passes through,
// which is the same reason usage recording lives here rather than at twenty
// call sites that would each have to remember.
//
// Same guard as record, for the same reason: this handle serves the WORKER
// when no distinct lead is configured, and a worker outage is not a lead
// outage.
//
// Two failures are deliberately not counted. A cancelled context is the
// caller leaving, not the model refusing. A context-exceeded error is a
// prompt too big for the window, which says nothing about availability and
// would mark the lead down over the single turn that overflowed.
//
// A provider REFUSING on content policy is not counted either, because it
// never reaches here as an error: it comes back as a successful response the
// agent loop inspects separately. That is the right split. A refusal is the
// lead working.
func (r reloadableLLM) noteLeadHealth(ctx context.Context, err error) {
	if !r.lead || SharedLeadLLM() == nil {
		return
	}
	if err == nil {
		noteLeadCallSucceeded()
		return
	}
	if ctx.Err() != nil || IsContextExceededError(err) {
		return
	}
	noteLeadCallFailed(err)
}

// ContextSize forwards the underlying LLM's ContextSizer, mirroring retryLLM.
// Without it, T.LLM.(ContextSizer) fails through this wrapper and returns 0,
// silently disabling every context-size-dependent feature (history compaction,
// context math). Returns 0 when the underlying exposes no window. (Pinger is
// deliberately NOT implemented — retryLLM doesn't either, so the existing
// assertion behavior is preserved.)
func (r reloadableLLM) ContextSize() int {
	if cs, ok := r.get().(ContextSizer); ok {
		return cs.ContextSize()
	}
	return 0
}

// ReloadableWorkerLLM returns a stable handle that always forwards to the current
// shared worker LLM — apps hold this so a live reload reaches them.
func ReloadableWorkerLLM() LLM { return reloadableLLM{get: SharedWorkerLLM} }

// ReloadableLeadLLM returns a stable handle for the lead LLM, falling back to the
// worker when no lead is configured ("use primary"). Always forwards (never a nil
// interface) so reconfiguring the lead provider later also takes effect live.
func ReloadableLeadLLM() LLM {
	return reloadableLLM{lead: true, get: func() LLM {
		if l := SharedLeadLLM(); l != nil {
			return l
		}
		return SharedWorkerLLM()
	}}
}

// Prompt-tools mode, published process-wide.
//
// It used to be computed once per agent at startup (from the worker provider's
// native_tools setting) and copied to every later agent. Nothing recomputed it,
// so flipping "Native tool calling" in the admin UI rebuilt the live LLM —
// which that form promises applies immediately — while the running process
// stayed in whichever mode it booted with. An operator who turned native tools
// ON kept getting prompt-parsed calls until a restart, with no way to tell:
// the setting read correct everywhere they could look.
//
// Published centrally because the value was always uniform across agents
// anyway; a per-agent snapshot bought nothing and could not be refreshed.
var (
	promptToolsMu   sync.RWMutex
	promptToolsSet  bool
	promptToolsMode bool
)

// SetPromptToolsMode publishes whether tools are described in the system
// prompt (true) or sent natively (false). Called at startup and on every
// LLM reload.
func SetPromptToolsMode(v bool) {
	promptToolsMu.Lock()
	promptToolsMode, promptToolsSet = v, true
	promptToolsMu.Unlock()
}

// PromptToolsMode returns the published mode. ok is false when nothing has
// published one — an embedding caller (the SDK) that never went through the
// framework's setup — in which case the caller's own field decides.
func PromptToolsMode() (mode bool, ok bool) {
	promptToolsMu.RLock()
	defer promptToolsMu.RUnlock()
	return promptToolsMode, promptToolsSet
}

func init() {
	prompts.SetUnstableToolLogger(func(name string) {
		Log("[prompts] the %q tool's description changes from call to call (built per caller), so edits to it are not applied and it is left as the code builds it", name)
	})
}
