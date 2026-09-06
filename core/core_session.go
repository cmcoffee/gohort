package core

import (
	"context"
	"sync"
)

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

// String names the tier, so a diagnostic prints "lead" rather than "2".
//
// Added because a routing breadcrumb whose entire job is being read by a person
// reported pin=2 and made them go looking up an iota. A number is the right
// thing to store and the wrong thing to report, and %v is what every log line
// reaches for.
func (t LLMTier) String() string {
	switch t {
	case WORKER:
		return "worker"
	case LEAD:
		return "lead"
	}
	return "unset"
}

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
	if s.Tier == LEAD && !s.agent.LeadDenied() {
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
	// HasDistinctLead, the same test LeadChat uses — NOT a bare nil check on the
	// handle. ReloadableLeadLLM() returns a non-nil handle even when no lead is
	// configured, so `s.agent.LeadLLM != nil` was true on every deployment and
	// any LEAD-tier session stream billed itself to LEAD while the process
	// tracker (which records inside the handle) billed the same tokens to
	// WORKER. Two tiers, one call, two prices. Latent today because every
	// in-tree Session.ChatStream caller is worker-tier; the fix is cheaper than
	// the day one is not.
	servedByLead := s.Tier == LEAD && s.agent.HasDistinctLead()
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
		s.agent.trackLeadTokens(ctx, resp)
	} else {
		s.agent.trackTokens(ctx, resp)
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
	// The cached share counts as consumption: a turn whose whole prompt was a
	// cache hit reports InputTokens=2, and the old guard dropped it as an empty
	// response — so the most expensive kind of conversation recorded nothing.
	if resp == nil || (resp.InputTokens == 0 && resp.OutputTokens == 0 &&
		resp.CacheReadTokens == 0 && resp.CacheWriteTokens == 0) {
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
		s.counters.LeadCacheRead += int64(resp.CacheReadTokens)
		s.counters.LeadCacheWrite += int64(resp.CacheWriteTokens)
	} else {
		s.counters.WorkerInput += int64(resp.InputTokens)
		s.counters.WorkerOutput += int64(resp.OutputTokens)
		s.counters.WorkerCacheRead += int64(resp.CacheReadTokens)
		s.counters.WorkerCacheWrite += int64(resp.CacheWriteTokens)
	}
}

// Report returns a flat {Input, Output} summary of tokens consumed
// through this session so far — worker and lead rolled up. Use AsDiff
// for the tier-split view needed by cost estimation.
func (s *Session) Report() SessionUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionUsage{
		Input:  s.counters.WorkerPrompt() + s.counters.LeadPrompt(),
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
