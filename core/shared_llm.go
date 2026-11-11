package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/ollama"
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
	leadInitErr = "the configured lead model (" + ProviderTitle(provider) + "/" + model + ") could not be initialized: " + err.Error()
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
//
// A different lead also starts its call counts over. They describe the model
// that made the calls, and kept across a change they went on reporting the
// failures of model ids already replaced (a typo saved on the way to the
// right one) for the two hours the dashboard looks back.
func SetLiveLLMs(worker, lead string) {
	leadInitMu.Lock()
	changed := liveLeadLLM != lead
	liveWorkerLLM, liveLeadLLM = worker, lead
	leadInitMu.Unlock()
	if changed {
		leadStatsMu.Lock()
		leadHours = [24]leadHour{}
		leadLastFailure = time.Time{}
		leadOKStreak, leadLastSuccess, leadAckAt = 0, time.Time{}, time.Time{}
		leadStatsMu.Unlock()
	}
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
	if judge, key := r.judgeFor(messages, opts); judge != nil {
		jctx, cancel := judgeShare(ctx)
		resp, err := judge.Chat(jctx, messages, judgeOpts(r.withTierText(opts))...)
		cancel()
		if err == nil || ctx.Err() != nil {
			r.record(ctx, resp)
			return resp, err
		}
		judgeFailed(key, err)
	}
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
	r.noteLeadHealth(ctx, resp, err)
	return resp, err
}

func (r reloadableLLM) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	if judge, key := r.judgeFor(messages, opts); judge != nil {
		jctx, cancel := judgeShare(ctx)
		resp, err := judge.ChatStream(jctx, messages, handler, judgeOpts(r.withTierText(opts))...)
		cancel()
		if err == nil || ctx.Err() != nil {
			r.record(ctx, resp)
			return resp, err
		}
		judgeFailed(key, err)
	}
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
	r.noteLeadHealth(ctx, resp, err)
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
	// The person's calls among them, for the lanes (run_lane.go).
	inter     int
	interLast time.Time
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
	// No hold here: a run gives way at its own calls, in the agent loop
	// (callModel), so the short calls a round makes beside them never wait.
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
	return countModelUse(tier, RunLaneOf(ctx) == LaneInteractive), nil
}

// countModelUse counts one call to tier as out until done is called, and as a
// person's when interactive, which is what the lanes wait on. Split out so a
// call that reaches the model without this handle, a peer's borrowed call,
// is counted the same way.
func countModelUse(tier LLMTier, interactive bool) (done func()) {
	modelUseMu.Lock()
	u := modelUses[tier]
	if u == nil {
		u = &modelUse{}
		modelUses[tier] = u
	}
	u.now++
	if interactive {
		u.inter++
	}
	modelUseMu.Unlock()
	return func() {
		modelUseMu.Lock()
		u.now--
		u.last = time.Now()
		if interactive {
			u.inter--
			u.interLast = u.last
		}
		modelUseMu.Unlock()
	}
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
//
// The count (noteLeadCall) does take an empty answer as a failure: the
// callers fall back to the worker on one, so it is a lead call that did not
// do the work, whatever the health line says.
func (r reloadableLLM) noteLeadHealth(ctx context.Context, resp *Response, err error) {
	if !r.lead || SharedLeadLLM() == nil {
		return
	}
	if err == nil {
		if resp != nil && resp.Content == "" && resp.OutputTokens == 0 && len(resp.ToolCalls) == 0 {
			noteLeadCall(errors.New("returned an empty answer"))
		} else {
			noteLeadCall(nil)
		}
		noteLeadCallSucceeded()
		return
	}
	if ctx.Err() != nil || IsContextExceededError(err) {
		return
	}
	noteLeadCall(err)
	noteLeadCallFailed(err)
}

// LeadCallStats is how the lead's calls have gone lately: how many, how many
// failed (each of those ran on the worker, or failed outright when pinned),
// and the failure seen most.
//
// The health line above is one error, cleared by any call that works, and a
// failure that hits only SOME calls defeats it. Gemini 3 answered the first
// round of every turn and refused every follow-up, so the line flipped
// between "stopped answering" and "answering again" all day while nearly all
// the lead's work ran on the worker. A count shows that shape.
type LeadCallStats struct {
	Calls, Failed             int // the last 24 hours
	RecentCalls, RecentFailed int // this clock hour and the one before it
	TopError                  string
	TopErrorCount             int
	LastFailure               time.Time
	// OKStreak is how many calls in a row have worked since the last
	// failure, LastSuccess when one last did, and Acknowledged when an admin
	// last dismissed the failures.
	OKStreak     int
	LastSuccess  time.Time
	Acknowledged time.Time
}

// leadHour is one clock hour of lead calls, in a ring of 24.
type leadHour struct {
	hour          int64
	calls, failed int
	errs          map[string]int
}

var (
	leadStatsMu     sync.Mutex
	leadHours       [24]leadHour
	leadLastFailure time.Time
	leadStatsNow    = time.Now
	// leadOKStreak counts lead calls that worked since the last that failed,
	// and leadLastSuccess is when one last did: a lead that has recovered
	// says so, rather than its old failures reading as current for two hours.
	leadOKStreak    int
	leadLastSuccess time.Time
	// leadAckAt is when an admin dismissed the failures: they stay counted,
	// and only a failure after it brings the notice back.
	leadAckAt time.Time
)

// AcknowledgeLeadFailures dismisses the lead's failures so far. The counts
// are kept; a failure after this brings the notice back.
func AcknowledgeLeadFailures() {
	leadStatsMu.Lock()
	leadAckAt = leadStatsNow()
	leadStatsMu.Unlock()
}

// noteLeadCall counts one lead call, a failure when err is set.
func noteLeadCall(err error) {
	now := leadStatsNow()
	h := now.Unix() / 3600
	leadStatsMu.Lock()
	defer leadStatsMu.Unlock()
	b := &leadHours[h%24]
	if b.hour != h {
		*b = leadHour{hour: h}
	}
	b.calls++
	if err == nil {
		leadOKStreak++
		leadLastSuccess = now
		return
	}
	leadOKStreak = 0
	b.failed++
	if b.errs == nil {
		b.errs = map[string]int{}
	}
	b.errs[leadErrorKey(err)]++
	leadLastFailure = now
}

// leadErrorKey is a failure as one short line, so the same refusal counts as
// one error however many calls it hit.
func leadErrorKey(err error) string {
	msg := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	return msg
}

// LeadCalls reports the lead's calls over the last 24 hours.
func LeadCalls() LeadCallStats {
	h := leadStatsNow().Unix() / 3600
	leadStatsMu.Lock()
	defer leadStatsMu.Unlock()
	var out LeadCallStats
	errs := map[string]int{}
	for _, b := range leadHours {
		if b.calls == 0 || b.hour <= h-24 || b.hour > h {
			continue
		}
		out.Calls += b.calls
		out.Failed += b.failed
		if b.hour >= h-1 {
			out.RecentCalls += b.calls
			out.RecentFailed += b.failed
		}
		for k, n := range b.errs {
			errs[k] += n
		}
	}
	for k, n := range errs {
		if n > out.TopErrorCount || n == out.TopErrorCount && k < out.TopError {
			out.TopError, out.TopErrorCount = k, n
		}
	}
	out.OKStreak, out.LastSuccess, out.Acknowledged = leadOKStreak, leadLastSuccess, leadAckAt
	if out.Failed > 0 {
		out.LastFailure = leadLastFailure
	}
	return out
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

// Lanes on the model.
//
// Someone asking an agent a question should not wait behind a schedule's
// run, a monitor's poll or a maintenance pass that happened to start first.
// So every call carries a lane on its context, read where the call goes out
// (beginModelUse): a person's call goes at once, a background call waits
// while people are using the tier it reaches, and a call an agent's owner has
// marked lowest waits while anything else at all is using it. Nothing is
// dropped: a call already out runs to its end, and a wait has a cap so a
// busy afternoon delays a schedule rather than cancelling it.
//
// The lane is set where a run starts, by what started it: a chat turn, a
// channel message and an API call are a person; a scheduled fire, a monitor
// wake and a maintenance pass are not, and so is every run nothing marked.
// Sub-runs inherit through the context, so a Builder dispatched from a chat
// turn runs at the chat turn's lane. An agent's run_priority overrides the
// origin for its own runs (agentRunLane in orchestrate). This generalises
// Optimize's giving way (SetYieldGate), which stays as it is: that gate sees
// every call after this hold, and ModelInUse still counts everyone's calls.

// RunLane says who is waiting on a call.
type RunLane string

const (
	// LaneInteractive is a person waiting: a chat turn, a channel message, an
	// API call, and every run those start. Never held.
	LaneInteractive RunLane = "interactive"
	// LaneBackground is nobody waiting: schedules, monitors, maintenance, and
	// any run nothing marked. Held while people use the model.
	LaneBackground RunLane = "background"
	// LaneLowest gives way to anything at all, people or not: for an agent
	// whose runs are the least urgent thing on the box.
	LaneLowest RunLane = "lowest"
)

type runLaneKey struct{}

// WithRunLane marks the lane every call under ctx goes out on.
func WithRunLane(ctx context.Context, lane RunLane) context.Context {
	return context.WithValue(ctx, runLaneKey{}, lane)
}

// RunLaneOf is the lane ctx carries; background when nothing set one.
func RunLaneOf(ctx context.Context) RunLane {
	if ctx == nil {
		return LaneBackground
	}
	if l, ok := ctx.Value(runLaneKey{}).(RunLane); ok && l != "" {
		return l
	}
	return LaneBackground
}

// The waits. A turn makes its calls with tool work between them, so a lane
// also waits a little after the last call it gives way to (the quiet), or it
// would be in the way of each next call; and it waits at most the cap, so a
// busy day delays background work rather than stopping it. Variables so a
// test can shorten them.
var (
	laneQuiet         = 10 * time.Second
	laneLowestQuiet   = 20 * time.Second
	laneMaxWait       = 2 * time.Minute
	laneLowestMaxWait = 10 * time.Minute
	laneTick          = 250 * time.Millisecond
	// laneDeadlineFloor is what a call keeps for itself: a call whose context
	// has less than this left is never held, and one with more is held only
	// until this much remains. The guardrail warden's check (30s, or 90s
	// when it reasons first), a title, a recall hint all run on a deadline of
	// their own inside a run; held for the run's lane they expired waiting,
	// and a warden that could not run blocked a lowest agent's turn. A run's
	// own calls carry no deadline this short, so the lane still holds them.
	laneDeadlineFloor = time.Minute
)

// Whether background work yields to people at all. On a model running on the
// deployment's own hardware it must: parallel slots on one GPU share its
// compute and its cache, so a background run beside a person's turn slows the
// turn and can push its cached prompt out. On a hosted API there is no shared
// hardware to protect, and waiting is pure delay. Auto decides by that; Always
// and Never are for what the code cannot see from outside, like a vLLM spread
// over spare GPUs or a tight API rate limit. Lowest still gives way to
// anything: that is an agent's own mark, not the deployment's.
const (
	BackgroundYieldAuto   = ""
	BackgroundYieldAlways = "always"
	BackgroundYieldNever  = "never"
)

var (
	yieldMu   sync.Mutex
	yieldMode = BackgroundYieldAuto
	// yieldLocal says, per model, whether it runs on hardware the deployment
	// shares, which is what Auto decides by. Per model because a deployment
	// can mix them: a worker on the local GPU and a lead on a hosted API.
	// Decided once from the worker, a background call to that hosted lead
	// waited for people using it, up to laneMaxWait, guarding nothing.
	yieldLocal = map[LLMTier]bool{WORKER: true, LEAD: true} // until a config says otherwise, the old behavior
)

// SetBackgroundYield records the deployment's setting and each model's
// provider, which Auto reads. An empty lead provider is a lead that falls back
// to the worker. Called at boot and on every LLM reload.
func SetBackgroundYield(mode, workerProvider, leadProvider string) {
	yieldMu.Lock()
	defer yieldMu.Unlock()
	switch mode {
	case BackgroundYieldAlways, BackgroundYieldNever:
		yieldMode = mode
	default:
		yieldMode = BackgroundYieldAuto
	}
	if strings.TrimSpace(leadProvider) == "" {
		leadProvider = workerProvider
	}
	yieldLocal = map[LLMTier]bool{WORKER: ProviderIsLocal(workerProvider), LEAD: ProviderIsLocal(leadProvider)}
}

// ProviderIsLocal says whether a provider runs on hardware the deployment
// shares: a local server, or a peer's. An OpenAI-compatible server on the same
// box reads as hosted, which is what Always is for.
func ProviderIsLocal(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch {
	case p == "llama.cpp" || p == "vllm" || p == "ollama":
		return true
	case strings.HasPrefix(p, peerProviderPrefix):
		return true
	}
	return false
}

// backgroundYields says whether a background call to tier waits for people.
// A tier with no model of its own recorded is the worker's.
func backgroundYields(tier LLMTier) bool {
	yieldMu.Lock()
	defer yieldMu.Unlock()
	switch yieldMode {
	case BackgroundYieldAlways:
		return true
	case BackgroundYieldNever:
		return false
	}
	if local, ok := yieldLocal[tier]; ok {
		return local
	}
	return yieldLocal[WORKER]
}

// interactiveInUse says whether a person's call to tier is out now, or ended
// within quiet.
func interactiveInUse(tier LLMTier, quiet time.Duration) bool {
	modelUseMu.Lock()
	defer modelUseMu.Unlock()
	u := modelUses[tier]
	return u != nil && (u.inter > 0 || (!u.interLast.IsZero() && time.Since(u.interLast) < quiet))
}

// holdForLane holds a call back as its lane requires and says how long it
// waited. A cancelled context ends the wait with its error.
func holdForLane(ctx context.Context, tier LLMTier) (time.Duration, error) {
	return holdForLaneWith(ctx, tier, nil)
}

// laneProgressEvery is how often a wait reports itself while it goes on.
var laneProgressEvery = 2 * time.Second

// holdForLaneWith is holdForLane reporting the wait so far to progress
// every laneProgressEvery while it lasts, so a held run can look held.
func holdForLaneWith(ctx context.Context, tier LLMTier, progress func(time.Duration)) (time.Duration, error) {
	var busy func() bool
	var cap time.Duration
	switch RunLaneOf(ctx) {
	case LaneInteractive:
		return 0, nil
	case LaneLowest:
		busy = func() bool { return ModelInUse(tier, laneLowestQuiet) }
		cap = laneLowestMaxWait
	default:
		if !backgroundYields(tier) {
			return 0, nil
		}
		busy = func() bool { return interactiveInUse(tier, laneQuiet) }
		cap = laneMaxWait
	}
	if !busy() {
		return 0, nil
	}
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		if left <= laneDeadlineFloor {
			return 0, nil
		}
		if room := left - laneDeadlineFloor; room < cap {
			cap = room
		}
	}
	start := time.Now()
	reported := time.Duration(0)
	for busy() {
		if progress != nil && time.Since(start)-reported >= laneProgressEvery {
			reported = time.Since(start)
			progress(reported)
		}
		if time.Since(start) >= cap {
			Log("[llm.lane] a %s call to %s waited the full %s for the model and goes now", RunLaneOf(ctx), tier, cap)
			break
		}
		select {
		case <-ctx.Done():
			return time.Since(start), ctx.Err()
		case <-time.After(laneTick):
		}
	}
	held := time.Since(start)
	if held >= time.Second {
		Debug("[llm.lane] a %s call to %s waited %s for people", RunLaneOf(ctx), tier, held.Round(100*time.Millisecond))
	}
	return held, nil
}

// The Judge model: a third model, beside the worker and the lead, for the
// quick checks gohort makes around every turn (the guardrail warden, the
// tool-result scan, the turn and grounding judges, the image source check,
// the memory gate). They were all worker calls, so each one waited its turn
// on the same model as the agents' own rounds, and an agent's next round
// waited behind them. A judge on a model of its own (a second GPU) takes them
// off that queue.
//
// A check is a worker call with a name (its route key, or its caller when
// it has none) that registers as a judge call. The worker handle sends a
// registered, switched-on check to the judge when one is set, and to the
// worker as before when it is not, or when the judge has just failed: one
// failure sets it aside for judgeRestFor, so a judge that is switched off
// costs one timeout, not one per check. A check with pictures goes to the
// judge only when the judge reads images, and a check that asks to think
// goes to the worker (see judgeFor).
//
// Local or peer only: a check reads what the worker reads (the person's
// messages, tool results, replies), so the judge is held to a model on
// hardware this deployment controls (JudgePrivate), checked when it is saved
// and again when it is built.

// JudgeCall is one check that may run on the Judge model: Key is its route
// key, or its caller when it has none; Label and Help say what it is on the
// Judge model's list.
type JudgeCall struct {
	Key   string
	Label string
	Help  string
}

var judgeState struct {
	mu          sync.RWMutex
	llm         LLM
	readsImages bool
	live        string // what is running, for the admin page ("" when none)
	initErr     string // why the configured judge did not build
	restUntil   time.Time
	calls       []JudgeCall
	byKey       map[string]bool
}

// judgeRestFor is how long a judge that failed a call is passed over.
const judgeRestFor = 3 * time.Minute

// RegisterJudgeCall lists a check as one the Judge model may take. Called
// from init(); a key registered twice keeps its first entry.
func RegisterJudgeCall(c JudgeCall) {
	judgeState.mu.Lock()
	defer judgeState.mu.Unlock()
	if judgeState.byKey == nil {
		judgeState.byKey = map[string]bool{}
	}
	if c.Key == "" || judgeState.byKey[c.Key] {
		return
	}
	judgeState.byKey[c.Key] = true
	judgeState.calls = append(judgeState.calls, c)
}

// JudgeCalls is every registered check, in registration order.
func JudgeCalls() []JudgeCall {
	judgeState.mu.RLock()
	defer judgeState.mu.RUnlock()
	return append([]JudgeCall(nil), judgeState.calls...)
}

// JudgeCallOn reports whether check key is registered and not switched off
// on the Judge model's list (JudgeLLMTable "off"). Every check is on until
// switched off.
func JudgeCallOn(key string) bool {
	judgeState.mu.RLock()
	registered := judgeState.byKey[key]
	judgeState.mu.RUnlock()
	return registered && OffloadAllowed(key)
}

// SetSharedJudgeLLM installs the Judge model (nil for none), whether it
// reads images, and what it is as the admin page says it ("" for none).
// initErr is why a configured judge did not build ("" when it did).
func SetSharedJudgeLLM(llm LLM, readsImages bool, live, initErr string) {
	judgeState.mu.Lock()
	defer judgeState.mu.Unlock()
	judgeState.llm, judgeState.readsImages = llm, readsImages
	judgeState.live, judgeState.initErr = live, initErr
	judgeState.restUntil = time.Time{}
}

// JudgeLLM is the Judge model itself, nil when none is set: for the check
// tests (core/checktest), which run a check's cases on the judge directly
// rather than through the worker handle's routing.
func JudgeLLM() LLM {
	judgeState.mu.RLock()
	defer judgeState.mu.RUnlock()
	return judgeState.llm
}

// LiveJudgeLLM is what the Judge model is running as, why a configured one
// did not build, and until when it is passed over after a failure.
func LiveJudgeLLM() (live, initErr string, restingUntil time.Time) {
	judgeState.mu.RLock()
	defer judgeState.mu.RUnlock()
	return judgeState.live, judgeState.initErr, judgeState.restUntil
}

// JudgePrivate reports whether a provider and endpoint may serve as the
// Judge model: a peer, or a model gohort reads as running on hardware this
// deployment controls.
func JudgePrivate(provider, endpoint string) (bool, string) {
	if _, ok := PeerFromProvider(provider); ok {
		return true, "a peer instance, which lends only its own local models"
	}
	return ProviderLooksPrivate(provider, endpoint)
}

// judgeFor is the Judge model, and the check's key, when this call is a
// switched-on check the judge should take; nil to send it to the worker.
func (r reloadableLLM) judgeFor(messages []Message, opts []ChatOption) (LLM, string) {
	if r.lead {
		return nil, ""
	}
	judgeState.mu.RLock()
	judge, images, rest := judgeState.llm, judgeState.readsImages, judgeState.restUntil
	judgeState.mu.RUnlock()
	if judge == nil || time.Now().Before(rest) {
		return nil, ""
	}
	var probe ChatConfig
	for _, opt := range opts {
		opt(&probe)
	}
	// A check goes to the judge only when it is on AND the judge map gives
	// it to the judge: its cases were all right on this judge (core/checktest).
	takes := func(k string) bool { return JudgeCallOn(k) && MappedTo(k, MapJudge) }
	key := strings.TrimSpace(probe.RouteKey)
	if !takes(key) {
		key = strings.TrimSpace(probe.Caller)
		if !takes(key) {
			return nil, ""
		}
	}
	if !images {
		for _, m := range messages {
			if len(m.Images) > 0 || len(m.Videos) > 0 {
				return nil, ""
			}
		}
	}
	// A check that asks to think goes to the worker. The Judge model is a
	// small model on a small card, quick at a short verdict and slow at a
	// long reasoning: the turn judge's confirming reading thought for 2,582
	// and 4,774 tokens there (43s and 78s), held up the checks behind it,
	// and a reasoning budget did not bound it (vLLM ignores one). The
	// readings that think are the rare, careful ones (a confirmation, a
	// thorough guardrail), which is where the worker's larger model earns
	// its place.
	if probe.Think != nil && *probe.Think {
		return nil, ""
	}
	// A big question goes to the worker. The map is earned on short cases,
	// and a small judge reading a long tool output or a long list of notes
	// is the call they never asked it.
	if n, limit := EstimateMessagesTokens(messages)+EstimateTokens(probe.SystemPrompt), judgePromptLimit(judge); n > limit {
		Debug("[judge] %s: a %d-token question is over the Offload model's %d, sent to the worker", key, n, limit)
		return nil, ""
	}
	return judge, key
}

// tunableJudgePromptMax is the largest question, in tokens, a check may put
// to the Judge model.
const tunableJudgePromptMax = "tune_judge_prompt_max"

func init() {
	RegisterTunable(TunableSpec{Key: tunableJudgePromptMax, Category: "Model calls", Label: "Offload model: largest question (tokens)",
		Help: "A check whose prompt is longer than this goes to the worker, not the Offload model.",
		Detail: "The judge map is earned on short test cases, and a smaller model reading a long tool output or a long list of notes is the question those cases never asked it. " +
			"Half the Offload model's context window is the limit whenever that is smaller.",
		Kind: KindInt, Default: 6000, Min: 500, Max: 200000})
}

// judgePromptLimit is the largest question the Judge model is asked: the
// tunable, or half its context window when that is smaller.
func judgePromptLimit(judge LLM) int {
	limit := TuneInt(tunableJudgePromptMax)
	if limit <= 0 {
		limit = 6000
	}
	if cs, ok := judge.(ContextSizer); ok {
		if half := cs.ContextSize() / 2; half > 0 && half < limit {
			limit = half
		}
	}
	return limit
}

// The tiers a check can be mapped to, beside the worker every check falls
// back to: the Judge model, and System One (core/decide).
const (
	MapJudge     = "judge"
	MapSystemOne = "system one"
)

// JudgeMapGate reports whether check key is mapped to tier: its test cases
// all came out right there, on the model that tier is now, for the check as
// it is now. Set by core/checktest, which keeps the map; nil, nothing is
// mapped and every check stays on the worker.
var JudgeMapGate func(key, tier string) bool

// MappedTo reports whether check key is mapped to tier (JudgeMapGate), and
// the Decision Offloading mode lets that tier be used.
func MappedTo(key, tier string) bool {
	gate := JudgeMapGate
	return gate != nil && key != "" && OffloadUses(tier) && OffloadAllowed(key) && gate(key, tier)
}

// OffloadAllowed reports whether check key's switch on the judge map lets it
// go to the Offload model or System One at all: on unless switched off,
// except the warden pre-guard, off until switched on (PreguardRecord.On).
func OffloadAllowed(key string) bool {
	if key == PreguardRoute {
		return LoadPreguard().On
	}
	var off []string
	if RootDB != nil {
		RootDB.Get(JudgeLLMTable, "off", &off)
	}
	for _, k := range off {
		if k == key {
			return false
		}
	}
	return true
}

// The Decision Offloading modes: which tiers the map may give checks to.
// A mode only ever takes a tier out; checks are still tested and mapped for
// every tier that is set, so turning one back on uses its mapping at once.
const (
	OffloadBoth      = "both"
	OffloadSystemOne = "system_one"
	OffloadModelOnly = "offload"
	OffloadOff       = "off"
)

// offloadModeKey is where the mode is kept, in JudgeLLMTable.
const offloadModeKey = "offload_mode"

// OffloadMode is the Decision Offloading mode; one never set, or not one of
// the modes, is OffloadBoth, as before there was a mode.
func OffloadMode() string {
	var m string
	if RootDB != nil {
		RootDB.Get(JudgeLLMTable, offloadModeKey, &m)
	}
	switch m {
	case OffloadSystemOne, OffloadModelOnly, OffloadOff:
		return m
	}
	return OffloadBoth
}

// SetOffloadMode keeps the Decision Offloading mode.
func SetOffloadMode(m string) {
	if RootDB != nil {
		RootDB.Set(JudgeLLMTable, offloadModeKey, m)
	}
}

// OffloadUses reports whether the mode lets checks go to tier.
func OffloadUses(tier string) bool {
	switch OffloadMode() {
	case OffloadOff:
		return false
	case OffloadSystemOne:
		return tier == MapSystemOne
	case OffloadModelOnly:
		return tier == MapJudge
	}
	return true
}

// judgeShareMax is the most time the Judge model is given for one check.
const judgeShareMax = 20 * time.Second

// judgeShare is the part of a check's time the Judge model may use: a third
// of what is left of the check's deadline, at most judgeShareMax. The rest is
// the worker's, should the judge not answer in time.
//
// The judge used to have the check's whole deadline, so a judge that was slow
// (busy with a long reasoning on its small card) ran the check out of time
// with nothing left to fall back on: a guardrail check timed out there and,
// failing closed, blocked the action it was checking.
func judgeShare(ctx context.Context) (context.Context, context.CancelFunc) {
	share := judgeShareMax
	if d, ok := ctx.Deadline(); ok {
		if third := time.Until(d) / 3; third < share {
			share = third
		}
	}
	return context.WithTimeout(ctx, share)
}

// judgeOpts is a check's options as the Offload model gets them: tried once.
// Any failure there already goes to the worker, so a retry only spends the
// check's time. A server restarting in a loop refused every attempt and the
// backoff (1s, then 4s) ran the check's share out before it fell back, about
// 8s on each check, every time the rest below ran out.
func judgeOpts(opts []ChatOption) []ChatOption {
	return append(opts, WithMaxRetries(0))
}

// judgeFailed sets the judge aside for judgeRestFor after a failed call;
// the call goes to the worker instead.
func judgeFailed(key string, err error) {
	judgeState.mu.Lock()
	judgeState.restUntil = time.Now().Add(judgeRestFor)
	judgeState.mu.Unlock()
	Warn("[llm] the Offload model failed %s (%v): checks go to the worker for %s", key, err, judgeRestFor)
}

// checkPriority marks ctx so a check goes first in the local model queue
// (ollama.WithCheckPriority): a registered check, from a turn a person is
// waiting on. On the worker each one used to wait its turn behind whatever
// else was queued (another agent's round, a background task), and the
// checks before an action or a reply hold the turn while they wait. Checks
// are short, so letting them through first costs the others little.
// Background work's checks keep their place: nobody is waiting on them.
func checkPriority(ctx context.Context, cfg ChatConfig) context.Context {
	if RunLaneOf(ctx) != LaneInteractive || !(registeredCheck(cfg.RouteKey) || registeredCheck(cfg.Caller)) {
		return ctx
	}
	return ollama.WithCheckPriority(ctx)
}

// registeredCheck reports whether key names a registered check, switched
// on for the Judge model or not.
func registeredCheck(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	judgeState.mu.RLock()
	defer judgeState.mu.RUnlock()
	return judgeState.byKey[key]
}

// checkKeepsItsCap reports whether a call to a local model keeps the output
// cap it asked for: a registered check's does. Local calls otherwise send
// none, since a cap clips a long structured answer mid-string; a check's
// answer is short by design, and without its cap a small model at
// temperature 0 that fell into a loop wrote thousands of tokens of one
// gatekeeper answer, holding the judge's card for minutes.
func checkKeepsItsCap(cfg ChatConfig) bool {
	return cfg.MaxTokens > 0 && (registeredCheck(cfg.RouteKey) || registeredCheck(cfg.Caller))
}

// QuickChoice is a check's question as a fixed choice, declared once: what
// System One is told (System), what it decides about (State), the question
// (Ask), and the answers. Each answer says whether it needs the full
// answer's written reason. One that does (a flag, a block, a contradiction)
// never stands on System One; one that does not (clean, on task, nothing to
// keep) stands when System One is sure of it and the judge map gives the
// check to System One.
//
// The same declaration is the check's System One form in its check tests
// (core/checktest builds it), so what is tested is what runs.
type QuickChoice struct {
	Key     string
	System  string
	State   string
	Ask     string
	Answers []QuickAnswer
	// Sure is the confidence an answer needs to stand; 0 is QuickSure. A
	// question whose standing answer skips a safety check sets it higher.
	Sure float64
}

// QuickAnswer is one answer: its key (the check's own word for it), what it
// means (the option System One reads), and whether it needs a written
// reason.
type QuickAnswer struct {
	Key    string
	Means  string
	Reason bool
}

// QuickSure is how firmly System One must choose an answer that needs no
// reason for it to stand (decide's Answer.Firm: both asks chose it, the
// weaker at least this probability). The map is the gate (only a check
// whose cases System One got right is asked at all); this keeps an answer
// it leans to, or one that follows the order the options were shown in,
// from standing in for the full one.
const QuickSure = 0.6

// PreguardRecord is the warden pre-guard's switch and its record: rules it
// skipped, how many of those the full warden re-checked after the turn, and
// any it then found violated. A miss turns it off, with why.
type PreguardRecord struct {
	On       bool      `json:"on"`
	Skipped  int       `json:"skipped"`
	Verified int       `json:"verified"`
	Missed   int       `json:"missed"`
	OffWhy   string    `json:"off_why,omitempty"`
	At       time.Time `json:"at"`
}

// PreguardRoute is the warden pre-guard's check key on the judge map.
const PreguardRoute = "app.orchestrate.warden.preguard"

// preguardKey is where the record is kept, in JudgeLLMTable.
const preguardKey = "warden_preguard"

var preguardMu sync.Mutex

// LoadPreguard is the pre-guard's record; off when never set.
func LoadPreguard() PreguardRecord {
	var r PreguardRecord
	if RootDB != nil {
		RootDB.Get(JudgeLLMTable, preguardKey, &r)
	}
	return r
}

// UpdatePreguard changes the record under one lock, so counts from checks
// running at once are not lost.
func UpdatePreguard(change func(*PreguardRecord)) PreguardRecord {
	preguardMu.Lock()
	defer preguardMu.Unlock()
	r := LoadPreguard()
	change(&r)
	r.At = time.Now()
	if RootDB != nil {
		RootDB.Set(JudgeLLMTable, preguardKey, r)
	}
	return r
}

// QuickDecide asks System One q, set by core/decide. ok is true only when it
// gave, sure enough, an answer that needs no reason, on a check mapped to
// it; otherwise the check asks its full question. Nil: never.
var QuickDecide func(ctx context.Context, q QuickChoice) (answer string, ok bool)

// AskQuick is QuickDecide when it is set: the answer key, and whether it
// stands in for the full question.
func AskQuick(ctx context.Context, q QuickChoice) (string, bool) {
	ask := QuickDecide
	if ask == nil || strings.TrimSpace(q.Key) == "" {
		return "", false
	}
	return ask(ctx, q)
}
