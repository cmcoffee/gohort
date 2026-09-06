package core

import (
	"strings"
	"time"
)

// AgentToolDef combines a tool definition with its handler.
type AgentToolDef struct {
	Tool    Tool
	Handler ToolHandlerFunc

	// NeedsConfirm marks a call worth stopping on before it executes.
	// What that COSTS depends on who supplied AgentLoopConfig.Confirm,
	// and the two live callers differ:
	//
	//   - CLI (defaultConfirm): a real terminal prompt — allow or deny.
	//   - Web (orchestrate's confirmFuncFor): escalates ONLY when the
	//     call resolves to a credential marked RequiresConfirm, and
	//     otherwise returns true. So on the dashboard this flag does NOT
	//     by itself put a prompt in front of the user.
	//
	// It has a second, always-live effect: it selects which tools get the
	// pre-action guardrail check (GuardHookPreAction). That is a real
	// function, so the flag is not decoration — but do not read it as
	// "the user will be asked" on the web path, because today they won't.
	// Wiring THIS flag to a web prompt is what ConfirmPrompt below is for;
	// it cannot be done by honoring NeedsConfirm itself, because grouped
	// tools OR the flag across their actions (GroupedTool.NeedsConfirm),
	// so a group with one dangerous action would prompt on its reads too.
	NeedsConfirm bool

	// Confirmation, when non-nil, turns a call into a question the user
	// actually answers. Setting it implies NeedsConfirm, so a tool needs
	// only this field. See ToolConfirmation.
	Confirmation *ToolConfirmation

	// SingleFirePerBatch indicates that only ONE call to this tool may run
	// per batch. When the LLM emits multiple parallel calls in one
	// response, only the first runs; the rest get a SKIPPED notice. The
	// round CONTINUES — this isn't a round-abort. Use for tools where
	// multi-fire-per-batch is structurally wrong (authoring actions,
	// outbound communication, resource creation).
	SingleFirePerBatch bool

	// SerialFirePerBatch indicates that batched calls to this tool are a
	// SEQUENCE: they all run, but sequentially in submission order (each
	// observing the prior's mutations), instead of the single-fire skip.
	// Other tools in the same batch still run in parallel. Use for stateful
	// authoring tools where [delete X, create Y] is a legit two-step edit
	// (tool_def). Takes precedence over SingleFirePerBatch if both are set.
	SerialFirePerBatch bool

	// BatchLane generalizes SerialFirePerBatch from one sequence to N. Calls
	// this round whose lane keys MATCH run sequentially in submission order;
	// calls in different lanes run in parallel with each other and with the
	// rest of the batch. Returning a constant is exactly SerialFirePerBatch;
	// returning "" puts the call in the shared serial lane alongside the
	// plain serial-fire tools.
	//
	// The point is a tool whose batched calls are only conditionally a
	// sequence: safe to fan out across distinct subjects, unsafe against the
	// same one (two dispatches to one sub-agent share a session id, so they
	// would race on its record and tear down each other's temp tools). The
	// tool knows which subject a call names; the loop cannot. Called once per
	// call during the serial partition pass, before anything executes, so it
	// may read stores — keep it cheap and side-effect free.
	//
	// Set INSTEAD of SerialFirePerBatch / SingleFirePerBatch, not alongside:
	// a lane function supersedes both.
	BatchLane func(args map[string]any) string
}

// ConfirmFunc is called to ask the user whether a tool call should proceed.
// It receives the tool name and a human-readable summary of the arguments.
// Return true to allow, false to deny.
type ConfirmFunc func(toolName string, argsSummary string) bool

// StepInfo provides observability into each round of the agent loop.
type StepInfo struct {
	Round      int        // Current round number (1-based).
	Content    string     // Text content from the LLM this round.
	ToolCalls  []ToolCall // Tool calls the LLM requested this round.
	ToolErrors int        // Number of tool calls that returned errors.
	Done       bool       // True if this is the final round (no more tool calls).
}

// StepCallback is called after each round of the agent loop for observability.
type StepCallback func(step StepInfo)

// wantsLead reports whether this loop's configuration asks for the lead tier,
// IGNORING whether the deployment currently permits one.
//
// Split out because three sites computed it independently — the round's cost
// attribution and two chat paths — and a fourth reading would have drifted from
// the others the first time the rules changed. It answers only "what was
// configured"; every caller still gates on LeadDenied(), which is where the
// privacy pin lives.
//
// Precedence, narrowest first: an explicit per-run TierOverride beats the route
// stage, which beats the config's own Tier. That order is what lets one
// resource opt out of a global routing decision without the stage having to
// know the resource exists.
func (cfg AgentLoopConfig) wantsLead() bool {
	if cfg.TierOverride != TierUnset {
		return cfg.TierOverride == LEAD
	}
	if cfg.RouteKey != "" {
		return RouteToLead(cfg.RouteKey)
	}
	return cfg.Tier == LEAD
}

type AgentLoopConfig struct {
	// TierOverride pins THIS run to a tier regardless of what its route stage
	// says — the per-resource escape hatch from a deployment-wide routing
	// decision. TierUnset (the zero value) follows RouteKey as before, so every
	// existing caller is unchanged.
	//
	// It cannot escalate past the privacy pin: callers gate on LeadDenied(), so
	// with "All LLMs are private" off a LEAD override still runs on the worker.
	// That is deliberate — the pin exists because the lead may be a third party,
	// and a per-resource flag is exactly the kind of thing that would otherwise
	// quietly become the exception to it.
	TierOverride LLMTier

	// GuardrailDeclines optionally replaces the built-in neutral declines used
	// when a reply cannot be made guardrail-compliant. Empty uses the built-in
	// set. Authored ahead of time, never generated at block time — see
	// guardrailSafeFallbackReply.
	GuardrailDeclines []string

	// SystemPrompt sets the system prompt for the LLM.
	SystemPrompt string

	// Tools defines the tools available to the LLM and their handlers.
	Tools []AgentToolDef

	// MaxRounds limits how many LLM call rounds before stopping. Default 10.
	MaxRounds int

	// OnStep is called after each LLM round for logging/observability. Optional.
	OnStep StepCallback

	// OnPromptDigest is called ONCE per turn with what the prompt was made of,
	// as soon as the first response makes the provider's own token count
	// available. Wire it to whatever outlives the turn — orchestrate puts it on
	// the run record, which is the only surface a scheduled fire has: a
	// recurring fire tails no SSE ring and has no HTTP client watching, so a
	// digest delivered over the live channel would reach every path EXCEPT the
	// unattended ones that need it most. Optional.
	OnPromptDigest func(PromptDigest)

	// OnDiag is called when the loop makes a silent framework decision on the
	// user's behalf mid-turn — chiefly the correction guards below that inject
	// a hidden corrective message and re-prompt (no-arg tool named in prose,
	// tool-call written as text markup, empty reasoning-collapse round, giving
	// up with tool errors pending). These otherwise vanish into Debug logs; a
	// silent guard that alters the turn but leaves no trace is exactly what the
	// per-session ⚠ diagnostics trail exists to prevent. Wire it to the app's
	// session-diag sink (orchestrate: chatTurn.turnDiag). kind is a short
	// stable slug; detail is one human-readable sentence. Optional; nil means
	// the loop still corrects, just without a breadcrumb.
	OnDiag func(kind, detail string)

	// Consult asks a stronger model ONE self-contained question on the loop's
	// behalf and returns its answer, which the caller injects into history as
	// advice. Wired by the app (orchestrate routes it through
	// app.orchestrate.consult); core stays ignorant of tiers and routes, the
	// same way OnDiag keeps it ignorant of session trails.
	//
	// Used by the failure-shape guard below: three hits on one wall means the
	// arguments aren't what's wrong, and a model that can ANSWER — given just
	// the failure text, with no tool catalog or history to lose track of — is
	// worth more there than another variation from the model that's stuck.
	// Note the symmetry: the same fingerprint DE-escalates a lead-driven turn
	// to the worker at six hits, and ESCALATES a worker-driven turn to a
	// consult at three. One signal, direction depending on who is driving.
	//
	// Optional; nil means the guard falls back to its generic directive. An
	// error or empty answer falls back the same way — a consult that fails
	// must never cost the turn its correction.
	Consult func(question, evidence string) (string, error)

	// SendGuardKey identifies a SIDE-EFFECTING send to a recipient — a message
	// delivered to a chat/contact — and returns a stable key for it (e.g.
	// "message_contact\x00Group Message"). Empty ⇒ not a guarded send. The loop
	// blocks the SECOND+ send to the same key in one turn: a model that drafts
	// several variations and fires them all (the "sent all 4 jokes to the group
	// chat" failure) delivers only the first; the rest come back as held drafts
	// with a note to pick one and send next turn. Keyed on recipient, NOT the
	// message text, so intentionally-varied duplicates are still caught — unlike
	// the identical-args loop guard, which they slip past. Nil ⇒ no send guard.
	// The app supplies it because recipient extraction is tool-specific and core
	// stays domain-agnostic.
	SendGuardKey func(toolName string, args map[string]any) string

	// SettleRound is called by a correction guard right before it re-prompts
	// and continues, so the app can FINALIZE whatever the just-rejected round
	// already streamed into its own bubble and open a fresh one for the retry.
	// Without it, a correction that `continue`s before the normal end-of-round
	// finalize orphans the streamed text: the retry round's text concatenates
	// into the still-open bubble ("…What API?Fair point…") and the post-loop
	// path can re-emit it as a second bubble. The app MUST settle by finalizing
	// (never length-clearing) — a correction retry is not guaranteed to repeat
	// the earlier text, so clearing it would lose real content; finalizing is
	// lossless and lets the post-loop near-duplicate check suppress any echo.
	// Wire it to the orchestrate streamHandler's finalize path. Optional; nil
	// means the pre-fix behavior (orphaned bubble). Idempotent / no-op when
	// nothing streamed this round.
	SettleRound func()

	// RetractRound is like SettleRound but DISCARDS the current round's streamed
	// bubble instead of finalizing it — the app must NOT persist or deliver it.
	// Called when a guardrail BLOCKS a round: the streamed text is the very
	// content the rule protects, so settling it (which both flushes it to the
	// client and appends it to the session, see the orchestrate captureMidTurn
	// path) would leak it even though the loop then re-prompts. The app should
	// clear the open bubble in the client and drop it without capturing. Falls
	// back to SettleRound when nil (concatenation-safe but NOT leak-safe — only
	// wire the retract on paths that persist per-round bubbles). Must reset the
	// stream buffer like SettleRound so the retry doesn't concatenate.
	RetractRound func()

	// DrainViewImages, when set, is called after each tool-execution round to
	// pull any frames a tool queued for the model to look at (e.g. view_video's
	// sampled video frames, held on sess.PendingViewImages). Returned images are
	// injected as a vision user-message before the next LLM call, so the model
	// actually SEES them. Wire it to sess.DrainViewImages. Optional; nil means
	// the loop never injects view images (the pre-fix behavior, where queued
	// frames were silently dropped and the model "described" a video it never saw).
	DrainViewImages func() []ViewImage

	// BeforeToolRound, when set, is called once per round after the LLM has
	// chosen its tool calls and before any of them run. It is the moment the
	// round's arguments are fixed but nothing has acted on them yet, which is
	// where per-round state that tool ARGUMENTS are interpreted against belongs
	// — wire it to sess.SnapshotImageRefs so a positional image#N means the
	// picture the model was looking at rather than whatever a sibling call in
	// the same batch has since made newest. Optional; nil means such refs
	// resolve live, the behavior before the hook existed.
	BeforeToolRound func()

	// Stream enables streaming mode. When set, LLM responses are streamed
	// through this handler as they arrive. Optional.
	Stream StreamHandler

	// ReasoningStream, when set, receives reasoning_content chunks (the
	// model's <think>...</think> stream) as they arrive. Fires only when
	// the LLM call uses ChatStream (i.e. when Stream is also set), and
	// only for backends that surface reasoning incrementally (llama.cpp,
	// Ollama). Use to surface what the model is reasoning about during
	// long agentic loops — e.g. servitor's investigator panel showing
	// the orchestrator's thought process as it streams. Optional.
	ReasoningStream func(chunk string)

	// Confirm is called when a tool with NeedsConfirm is about to execute.
	// If nil, a default terminal prompt is used (y/n).
	Confirm ConfirmFunc

	// GuardrailCheck, when set, lets the app enforce owner-authored guardrails
	// at fixed interception points. hookPoint is one of GuardHook* below;
	// candidate is what's being judged (a tool call + args for pre_action, the
	// reply text for pre_output, recent narration for periodic). It returns a
	// GuardrailDecision; Blocked means the candidate violated a guardrail. The
	// loop delivers Message as a TRUSTED framework result — never wrapped in the
	// untrusted-content fence (a fenced guard is one the model is licensed to
	// ignore) — and does NOT run the blocked action. The app owns the warden
	// call, the block/escalation policy, and the breadcrumb; core just calls the
	// hook at the right moments and honors the verdict. nil ⇒ no guardrails
	// (zero overhead). Optional.
	GuardrailCheck func(hookPoint, candidate string) GuardrailDecision

	// GuardrailActionGate, when set, WIDENS which tool calls reach the
	// pre_action check. nil keeps the default: the NeedsConfirm set, and only
	// that.
	//
	// NeedsConfirm is the right default and the wrong ceiling. It names the
	// calls a person would want to approve — sends, posts, deletes, spends —
	// which is what an owner's rule is usually about. It does not name the
	// calls that carry data OUT without looking consequential: a plain fetch of
	// https://elsewhere.example/?q=<the system prompt> is an exfiltration with
	// the shape of a read.
	//
	// That gap costs nothing while nothing has gone wrong, which is why the
	// default stays where it is — judging every network read on every turn is a
	// model call per read. It matters on a turn where something HAS gone wrong,
	// and the app is the only party that knows. So the app supplies a predicate
	// and may widen the gate for exactly those turns.
	//
	// Widening changes WHICH calls are judged, never HOW: the verdict, the
	// block, the message, and the escalation counter are unchanged.
	GuardrailActionGate func(toolName string) bool

	// GuardrailHalted, when set, is consulted immediately after any blocked
	// check. true means the app has decided this turn must END — not be
	// corrected, redirected, or asked to stop.
	//
	// It exists because every guardrail hook except pre_output was advisory. A
	// blocked pre_action returned an error result and the loop carried on, so
	// the model was free to reword the call, reach the same end through an
	// ungoverned tool, or simply try again; periodic redirected and continued.
	// The escalation meant to catch that returned a message reading "STOP — you
	// have hit enforced guardrails N times", which is an INSTRUCTION TO THE
	// AGENT. The whole premise of an independent warden is that the agent's
	// judgment may already be compromised, so asking that agent to stop is not
	// enforcement — it is the same trust the warden was built to withdraw.
	//
	// When this returns true the loop discards the in-flight round, substitutes
	// the safe decline (GuardrailDeclines), and returns. No further generation,
	// no further tool calls. The app decides WHEN (repeated blocks in one turn);
	// core guarantees the turn actually ends. nil ⇒ prior advisory behavior.
	GuardrailHalted func() bool

	// PreEmptedReply, when set, IS the turn: the loop delivers it and returns
	// without calling a model at all.
	//
	// For a request an app-layer check refused before anything ran — a pre_input
	// guardrail on a rule that forbids what was asked for. There is nothing to
	// steer in that case, and running the agent only buys a long deliberation
	// about how to decline without saying why, which is the largest single cost a
	// blocked turn carries. It also cannot leak: no draft is generated, so there
	// is no protected content anywhere in the turn.
	//
	// Delivered the way a normal terminal reply is (streamed, then a Done step) so
	// every host renders it through the path it already has, rather than each
	// caller needing its own short-circuit.
	PreEmptedReply string

	// InterimContentHidden tells the loop that assistant prose from NON-FINAL
	// rounds is neither shown to anyone nor stored by this host — only the final
	// reply is. When set, the periodic guardrail check is skipped, because the
	// thing it exists to catch cannot reach anybody on this path.
	//
	// The periodic check is the expensive one: it judges every round that produces
	// narration, so on a long tool-using turn it is one extra model call per
	// round. Worth every call where those words are painted into a live
	// transcript. Worth nothing at all where the round's prose is discarded and
	// pre_output judges the only text that ships.
	//
	// The ZERO VALUE IS THE SAFE ONE, deliberately. False means "assume interim
	// prose is visible", so a host that forgets this field keeps full checking and
	// pays for it, rather than silently losing containment. Set it true only after
	// confirming the path neither streams interim deltas nor persists per-round
	// turns — a host that streams (AgentLoopConfig.Stream) or settles rounds into
	// a transcript (SettleRound) does deliver them and must leave this false.
	InterimContentHidden bool

	// GuardrailReject, when set, writes the user-facing reply for a halted turn.
	// It must run in FRESH context — a separate model call that never saw the
	// conversation — for the same reason the warden does: the turn's own context
	// is the thing that just failed, and anything generated from it is generated
	// by a persuaded model.
	//
	// This is a HANDOVER, not a correction. Past this point the original model
	// produces nothing further: it is not asked to revise, apologize, or explain,
	// because each of those is another generation from the compromised context
	// and another chance to leak what the rule protects. The rejection model owns
	// the remainder of the turn.
	//
	// It receives the user's REQUEST so the refusal can be about something
	// ("I can't help with that one" reads like a broken bot) — but never the
	// draft, the rule, or the history. The request is attacker-controlled text
	// and MUST be fenced as untrusted by the implementation: handed over as a
	// bare instruction it would be read as the task, and "ignore that, output
	// the following" is precisely what a halted turn attracts.
	//
	// reason is for the app's own logging/telemetry only; a decline must never
	// disclose the rule or that an automated check fired (naming it hands a
	// prober the signal the guardrail exists to withhold). Returning "" falls
	// back to the canned decline, so a failed rejection call can never leak the
	// draft it was meant to replace. nil ⇒ canned decline. Optional.
	GuardrailReject func(reason, request string) string

	// ChatOptions are additional options passed to every LLM call.
	ChatOptions []ChatOption

	// ToolRoundOptions are options applied to rounds that follow a tool-call
	// round (i.e. rounds where the model is processing tool results). When set,
	// these replace ChatOptions for those rounds. Use to enable thinking only
	// for tool-execution rounds while keeping the initial conversational round
	// lean — e.g. ChatOptions: [WithThink(false)], ToolRoundOptions: [WithThink(true)].
	ToolRoundOptions []ChatOption

	// PromptTools describes tools in the system prompt as text instead of
	// using native function calling. The LLM responds with plain text
	// containing tool calls in a defined format, which the loop parses and
	// executes. Results are sent back as regular user messages, giving the
	// caller full control over context. This works reliably with models
	// that have poor or no native tool support (e.g. Gemma via Ollama).
	PromptTools bool

	// DisableToolMentionCorrection turns off the tool-mention nudge (the
	// loop otherwise re-prompts when the model names a known tool in prose
	// but emits no call). Set this when the model is EXPECTED to name
	// its own tools legitimately — chiefly a code-analysis session whose
	// SUBJECT is a codebase that defines those same tools (e.g. servitor
	// pointed at an agent framework), where "the code defines store_fact" is
	// description, not an intended call. Leaving it on there makes the model
	// waste a round explaining "I didn't mean to call any tool", and that
	// meta-explanation leaks into the answer. Optional; default false (on).
	DisableToolMentionCorrection bool

	// DisableIDProvenanceGate turns off the invented-id refusal (see
	// idProvenanceRefusal). Set it for a loop whose tools legitimately take
	// identifiers the session never saw — a caller that mints ids on the
	// model's behalf, or one seeded from a store this loop cannot read.
	// Optional; default false (on).
	DisableIDProvenanceGate bool

	// FailureMemoryKey scopes a persistent record of calls that keep failing,
	// so a repeat guard survives the end of a turn. Empty keeps the guard
	// per-turn, which is right for a conversation: the history the next turn
	// carries already re-arms it (seedRepeatFailFromHistory).
	//
	// A SCHEDULED fire is the case this exists for. It rebuilds its history
	// from stored messages, which carry role and content but no tool results,
	// so nothing about last cycle's failures reaches the guard: two fires an
	// hour apart each re-ran the same broken call and each wrote a fresh
	// diagnosis of it. Set it to something stable for the standing work —
	// agent plus session — and the count carries across fires.
	FailureMemoryKey string

	// ActionQuotas caps how often one ACTION may run in a rolling 24 hours,
	// keyed by the name the quota is written against: a tool name
	// ("create_post"), or a grouped tool's action ("moltbook/create_post").
	// Counted and enforced here because a cap the MODEL is asked to keep is
	// not a cap: told "6 posts a day", an agent counted its own posts out of
	// a listing, got the UTC day boundary wrong, and posted nine.
	//
	// BudgetKey scopes everything this agent is charged for — its action
	// counts and its spend — and is the agent's own id at every call site.
	// Work with no key is UNCAPPED rather than pooled: a budget nobody owns
	// is somebody else's, and silently sharing one would be worse than not
	// enforcing it.
	ActionQuotas map[string]int
	BudgetKey    string

	// DailySpendUSD caps what this agent may cost in a rolling 24 hours.
	// 0 = uncapped.
	//
	// A turn already under way is never stranded: crossing the line
	// DE-ESCALATES the rest of it to the worker tier (the same move the
	// per-turn lead budget makes), and it is the NEXT turn that is refused
	// outright. A scheduled fire on a frontier model is what this is for —
	// one turn, unattended, cost over a dollar in prompt-cache writes alone,
	// and nothing between it and doing that every hour.
	DailySpendUSD float64

	// Tier selects which LLM tier runs the loop. Defaults to WORKER.
	// Set to LEAD to route all rounds through the lead LLM.
	// Ignored when RouteKey is set.
	Tier LLMTier

	// RouteKey is a registered route stage key (see RegisterRouteStage).
	// When set, the tier is resolved from the admin routing config via
	// RouteToLead(key) instead of the Tier field. This lets admins
	// configure per-agent LLM routing from the admin panel.
	RouteKey string

	// MaskDebugOutput suppresses tool argument and result content from debug
	// logs. Use this for sessions that handle sensitive data (SSH credentials,
	// system facts, private files) to prevent data leaking into log files.
	// Tool names are still logged; content is replaced with byte counts.
	MaskDebugOutput bool

	// ThinkBudget sets the thinking_budget_tokens for every round of this
	// loop (e.g. a per-agent configured budget). 0 = inherit the
	// operator-configured global default (admin "Thinking Budget" →
	// llamacppBudget, default 4096). We no longer scale the budget by
	// prior input-token count: prompt size is a poor proxy for task
	// difficulty (a trivial tool call in a long history doesn't need more
	// thinking), and Qwen's own best-practice guidance is a flat budget,
	// not an input-scaled one. Callers needing a specific size set this;
	// the resolution order is per-call WithThinkBudget > this > global.
	ThinkBudget int

	// SerialTools limits execution to one tool call per round. When the LLM
	// returns multiple tool calls in a single response, only the first is
	// executed; the rest receive a SKIPPED notice so the LLM is forced to
	// proceed one step at a time and see each result before deciding what to
	// do next. Recommended for investigative agents where failure feedback
	// must be seen before the next attempt.
	SerialTools bool

	// RoundAbortTools names tools that, when called, must close the round
	// immediately — any other tool calls in the same LLM response are
	// dropped with a SKIPPED notice, and the loop breaks after this round.
	// Use for control tools like ask_user / respond_directly / plan_set
	// that route the turn to a different flow: bundling them with a real
	// tool (e.g. ask_user + create_agent) lets the LLM "ask then act
	// anyway" which defeats the pause. With this set, only the abort tool
	// fires and the LLM has no chance to chain through.
	RoundAbortTools []string

	// SingleFireGroups names sets of tools where AT MOST ONE call from
	// each set may run per batch. When the LLM emits multiple calls from
	// the same group in one response, only the FIRST runs; the rest get
	// a SKIPPED notice. Unlike RoundAbortTools, the round itself
	// CONTINUES — the LLM can still produce its text reply. Designed
	// for attachment-emitting tool families (find_image + fetch_image
	// + generate_image) where parallel-dispatch in one batch produces
	// multiple attachments when the user wanted one.
	//
	// Each inner slice is one group; calls within a group cross-block
	// each other, calls across groups don't.
	SingleFireGroups [][]string

	// OnRoundStart, when set, is called at the top of each round AFTER the
	// ctx-cancellation check and BEFORE the LLM call. Any messages it returns
	// are appended to history before the call. Use for per-round content the
	// model should see every round — budget/pacing notes, status reminders.
	// MAY return content on every call (e.g. orchestrate's round-counter
	// pacer always returns a note); do NOT use it for the pre-finalize
	// injection drain — that's InjectionDrain's job.
	OnRoundStart func() []Message

	// InjectionDrain, when set, returns any pending mid-flight user
	// notes (from an injection queue) and REMOVES them from the queue.
	// Distinct from OnRoundStart in one critical way: it must return
	// EMPTY when there's nothing pending. The loop calls it both at
	// round start AND once more right before finalizing — so a note
	// that lands during the final round still gets picked up and the
	// agent does another round instead of finishing with the note
	// unread. Because it empties its queue, the pre-finalize re-call
	// terminates (returns empty once drained) rather than looping.
	//
	// Wire an injection queue's Drain here, NOT OnRoundStart — a hook
	// that always returns content (like a budget pacer) would make the
	// pre-finalize check loop forever.
	InjectionDrain func() []Message

	// StopRound, when set, is called at the top of each round AFTER the
	// ctx-cancellation check. Returning true breaks the loop cleanly,
	// same effect as hitting MaxRounds. Use for soft-cap policies where
	// the cap depends on runtime state (e.g. orchestrate's explorer-mode
	// flag — the per-agent budget is enforced via StopRound; explorer
	// mode keeps it lifted to the absolute MaxRounds).
	StopRound func() bool

	// GraceRounds is the wrap-up runway. When the cap is reached (MaxRounds
	// or StopRound), instead of hard-stopping — which strips tools and makes
	// some models emit their intended call as TEXT to compensate — the loop
	// keeps tools available and gives the model this many extra rounds to
	// land the turn, escalating a "wrap up now" directive each round before
	// a hard stop (the forced no-tools rescue is the final backstop). 0 lets
	// the loop default it: 5 for real agent turns (MaxRounds >= 10), 0 for
	// short fixed loops (classifiers, judges) which must stop exactly on cap.
	// Set explicitly to override either default.
	GraceRounds int

	// OnRoundReset, when set, is called once per round. Returning true
	// rebases the soft-pacing thresholds (midpoint nudge, wrap-up
	// warning, failure-streak counter) as if the loop just started —
	// "remaining budget" is recomputed from the current round onward.
	// Hard MaxRounds cap stays in place; this only resets the LLM-
	// facing pacing signals.
	//
	// Use when the app's notion of "logical phase" changes mid-loop and
	// the LLM should get a fresh pacing window for the new phase (e.g.
	// servitor advancing to a new plan step — burning rounds on step 1
	// shouldn't trigger the wrap-up warning on step 4). One-shot per
	// transition: app's closure tracks "have I reset since the last
	// phase change?" and returns true exactly once per phase boundary.
	OnRoundReset func() bool

	// PendingWorkFn, when set, reports how many authorized work items
	// (e.g. unfinished plan steps) still remain. The agent-loop's
	// wrap-up warning uses this to distinguish "stop exploring" (the
	// default) from "you still have N authorized items to finish — wind
	// down this item cleanly and continue the list." Without this hook,
	// the wrap-up nudge tells the model not to start new investigations,
	// which a plan-driven worker can read as "abort the remaining plan
	// steps and write a summary" — leading to clean wrap-ups that
	// silently skip pending steps.
	//
	// Return the count of remaining items (pending + in-progress is
	// usually right). 0 means "no more authorized work, exploration is
	// up to you" and the default wrap-up text fires.
	PendingWorkFn func() int

	// DynamicTools, when set, is called at the top of each round to fetch
	// runtime-defined tools to merge into the catalog. Used by apps that
	// support session-scoped tools the LLM creates mid-conversation
	// (e.g. via create_temp_tool). The returned tools go through the
	// same AllowedCaps filter as static tools — runtime registration
	// can't escape capability gating. Returning nil/empty is fine and
	// just means "no extras this round."
	DynamicTools func() []AgentToolDef

	// ToolFallbackResolver, when set, is consulted when the model calls a
	// tool name that ISN'T in the round's catalog. It lets an app route a
	// call to a tool whose SCHEMA is intentionally lazy (kept out of the
	// LLM tool array to save tokens) but whose HANDLER is still valid —
	// e.g. a custom tool the model already learned via load_tool on a
	// prior turn and now calls directly from context. Return (handler,
	// true) to run it; (nil, false) to fall through to the normal
	// "unknown tool" error. The resolver may also mark the tool loaded so
	// its schema rejoins the catalog next round.
	ToolFallbackResolver func(name string) (ToolHandlerFunc, bool)

	// DeliveredCount reports how many attachments actually go out with this
	// reply. Evidence for the turn judge: "here's your picture" is true or false
	// depending entirely on this number. Nil reads as zero, which is honest —
	// a host that doesn't track deliveries has none to report.
	DeliveredCount func() int

	// Backgrounded reports whether this turn started a detached job. It makes
	// "I'll let you know when it's done" TRUE, and that is the reply
	// detachedNotice explicitly asks for — so the judge must never see such a
	// turn. Nil reads as false.
	Backgrounded func() bool
	// BackgroundEstimate reports the wait the framework offered for that job,
	// humanized ("13 seconds"), or empty. Feeds the turn judge so a quoted
	// estimate the framework supplied is not convicted as an invented one.
	BackgroundEstimate func() string

	// TurnClaimJudge reads the finished turn and reports whether the reply is
	// honest about what the turn actually did — the backstop for the shapes the
	// phrase-list guards above do not know about. See turn_judge.go.
	TurnClaimJudge TurnClaimJudge

	// PriorWork reports work already done FOR this turn before the loop
	// started, which the loop therefore cannot observe: a machine step that
	// searched, a delegated step, a pipeline phase. One entry per piece of
	// work, naming what ran.
	//
	// The host supplies it because only the host knows what it ran on the
	// turn's behalf; the loop's own accounting begins when the loop does.
	// Nil, and the empty result, both read as "nothing ran before this".
	PriorWork func() []string
	// PriorReports supplies TurnClaimEvidence.PriorReports: the automated
	// reports this agent's own scheduled runs already filed into the thread,
	// which a reply may be recapping. Nil = none.
	PriorReports func() []string

	// Unattended marks a turn nobody is reading as it happens — a scheduled
	// fire, a task wake, an autonomous run. Set by the host, which is the only
	// thing that knows how the turn was started.
	//
	// It is the one arm of the claim judge's pre-filter that is about the
	// SITUATION rather than the evidence. Every other arm exists because the
	// framework has reason to doubt this particular turn; this one exists
	// because on an unattended turn a false report is never contradicted. The
	// interactive paths get away with a narrow filter precisely because a
	// person is there to say "it didn't attach anything".
	Unattended bool

	// TurnGroundingJudge reads the finished turn and reports whether the reply
	// states an unchecked claim as established fact. Separate from
	// TurnClaimJudge because the questions differ: that one asks whether the
	// turn DID what the reply describes, this one whether the reply KNOWS what
	// it asserts. Nil disables it.
	TurnGroundingJudge TurnGroundingJudge

	// UncheckedClaims are the stored notes in this turn's prompt that carry an
	// unchecked marker. Supplied by the host, which is what renders the memory
	// block and therefore the only thing that knows which notes were marked.
	// Empty means the grounding judge never runs.
	UncheckedClaims []string

	// LiveClaimSpeaker names the person whose message this turn is answering,
	// when they are NOT the principal — a participant in a room, a contact on a
	// channel. Empty for an owner turn and for every non-channel surface.
	//
	// It exists because the stored-claim machinery cannot reach the claim that
	// matters most in a group: the one asserted in THIS message, which nothing
	// has classified or marked because it is not in memory yet. Set, it puts
	// the inbound itself in the judge's scope.
	LiveClaimSpeaker string

	// LiveClaimTrusted marks the speaker as someone the host recognizes — on a
	// roster the owner maintains, matched on something they cannot simply
	// claim. It relaxes the premise HOLD and nothing else: their claims are
	// still attributed and still judged, because being trusted is not the same
	// as having been checked.
	LiveClaimTrusted bool

	// PhantomDeliveryRefs names what a reply CLAIMS to be sending that does not
	// exist — a delivery promised for something never produced. The loop cannot
	// answer this itself: what a reference resolves against (a workspace, an
	// inbound-media registry, a recent-image ring) is the app's to know, and so
	// is whether this turn ran anything that MAKES a deliverable.
	//
	// Each entry is quoted straight into the correction, so return whatever
	// names the missing thing best: a filename when the reply named one, a plain
	// noun phrase ("the image") when it didn't.
	//
	// Return only GENUINE phantoms. A reference to a file that was delivered and
	// then cleaned up is not one — the delivery happened — and neither is one
	// the app can still recover from a staged file. Anything returned here costs
	// a correction round, so a false positive spends a round telling a model it
	// was wrong when it wasn't.
	//
	// The failure it exists for, observed in full: a reply consisting of exactly
	// "[ATTACH: find-dkfindcraig.jpg]", a filename with the right shape and the
	// subject's name stuffed into it, for a picture the turn never made — it
	// called no tools at all. The marker resolved to nothing, stripping left an
	// empty reply, and the contact was asked to rephrase a request that was
	// never the problem.
	//
	// Its quieter twin, which no marker rule can reach: the turn DID call the
	// image tool, the generation failed, and the reply was a caption — "Here's
	// you, wasting away in the garage like Craig ordered" — delivered whole, with
	// no picture and nothing anywhere in the words to suggest one was missing.
	PhantomDeliveryRefs func(reply string) []string

	// RoundToolFilter, when set, is called at the top of each round for
	// every candidate tool name; returning false drops that tool from the
	// round's catalog. Use to SUPPRESS a tool mid-turn — e.g. after it has
	// looped/errored repeatedly — so the model is forced off it. A fixated
	// model ignores error feedback but physically can't call a tool that
	// isn't in the catalog. Nil = no filtering (all tools offered).
	RoundToolFilter func(name string) bool

	// RoundChatOptions, when set, is called at the top of each round; its
	// options are appended LAST (after route/budget defaults and
	// ChatOptions/ToolRoundOptions) so they OVERRIDE for that round.
	// Use for per-round dynamic overrides the static option slices can't
	// express — e.g. forcing WithThink(false) on the round right after a
	// control-tool rejection so the model doesn't deliberate itself back
	// into the same dead end. Nil = no per-round override.
	RoundChatOptions func() []ChatOption

	// ContextSize is the model's context window (tokens). The loop compacts
	// history before each round once it crosses ~70% of the window — eliding
	// the bodies of OLD tool results (keeping recent ones + all conversational
	// text) so a long multi-round session can't grow past the window and
	// trigger server-side context-shift, which drops the system prompt and
	// degrades the model.
	//
	// LEAVE IT ZERO. RunAgentLoop fills it from the running tiers, and that is
	// the right answer for every caller in the tree. It used to read "0 = no
	// compaction, set it from the caller's WorkerContextSize()/LeadContextSize()"
	// — an instruction one of twenty loop configs followed, so nineteen kinds of
	// turn had no compaction at all and grew until the server silently dropped
	// their system prompt. See RunAgentLoop.
	//
	// Negative disables compaction outright, for a caller that genuinely wants
	// an unbounded history and has a reason.
	ContextSize int

	// RoundCompactNow, when set, is checked at the top of each round; a
	// true return forces an AGGRESSIVE compaction this round (shed all but
	// the newest tool-result body, regardless of budget). This is the
	// LLM-driven path: a compact_context tool sets it so the model can
	// proactively drop a long tool output (e.g. a smoke-test report) the
	// moment it's done with it, instead of waiting for the budget floor.
	// Works even when ContextSize is 0. Nil = budget-only compaction.
	RoundCompactNow func() bool

	// TurnNotes supplies volatile, turn-scoped context to append to the newest
	// user message. Called once with that message's text, before the first LLM
	// call; return "" to add nothing.
	//
	// The newest user turn is the cache-safe home for anything that changes
	// between turns — it is the volatile tail that never hits cache anyway, so
	// appending there costs nothing, which is the same reasoning that puts the
	// date stamp below here rather than in the system prompt. Facts that a tool
	// SCHEMA cannot carry (schemas sit at the front of the prompt, so a changing
	// one re-pays cold prefill every turn) can be carried here instead.
	//
	// The loop has no idea what a turn is about, so what is worth saying is
	// entirely the app's call. It sees the user's message and decides.
	//
	// Turn-scoped means turn-scoped: the note is appended to the loop's working
	// copy of history. Hosts persist their own user message, so it does not ride
	// into the stored conversation — which matters when the note contains
	// anything positional, since next turn it would be wrong.
	TurnNotes func(userMessage string) string

	// StampLocation sets the timezone of the "[Current date & time: …]"
	// marker prefixed onto the newest user turn. Nil = the deployment/host
	// zone (time.Local). Set it to the acting user's location (UserLocation)
	// so the model sees the wall-clock in the user's own zone rather than the
	// server's. Only the stamp is affected; nothing else in the loop reads it.
	StampLocation *time.Location

	// AllowedCaps gates which tools the LLM is offered, by capability tier
	// (CapRead, CapNetwork, CapWrite, CapExecute). Tools whose declared Caps
	// aren't all in this set are filtered out before the LLM ever sees the
	// catalog. Empty/nil means "no restriction" (legacy behavior — every
	// tool the caller passed is offered). Use to enforce least-privilege:
	// e.g. a chat agent permits read+network but not write+execute, so even
	// if a write/execute tool ends up in the registry it can't be invoked
	// from chat. Tools with empty Caps (unannotated) pass through unfiltered
	// during the migration period.
	AllowedCaps []Capability
}

// applyTurnNotes appends the app's turn-scoped context to the newest user
// message, in place. See AgentLoopConfig.TurnNotes.
//
// Only ever the TRAILING message, and only when it is the human turn: mid-loop
// the tail is a tool result, and reference material buried inside one reads as
// output from something the model just ran. Earlier user turns are settled
// context that the prompt cache already covers — writing into one moves the
// cache boundary backwards for nothing.
func applyTurnNotes(cfg AgentLoopConfig, history []Message) {
	n := len(history)
	if cfg.TurnNotes == nil || n == 0 || history[n-1].Role != "user" {
		return
	}
	note := strings.TrimSpace(cfg.TurnNotes(history[n-1].Content))
	if note == "" {
		return
	}
	Debug("[agent_loop] turn note appended to the user turn (%d chars)", len(note))
	history[n-1].Content += "\n\n" + note
}

// deliveredCount / backgrounded are the nil-safe reads of the turn-evidence
// hooks. Absent means "nothing delivered, nothing started", which is the only
// safe reading: it makes a delivery claim MORE suspect, never less.
func (c AgentLoopConfig) deliveredCount() int {
	if c.DeliveredCount == nil {
		return 0
	}
	return c.DeliveredCount()
}

// priorWork is the host's account of what ran for this turn before the loop
// began. Nil-safe, because most hosts have nothing to report.
func (c AgentLoopConfig) priorWork() []string {
	if c.PriorWork == nil {
		return nil
	}
	return c.PriorWork()
}

// priorReports is the host's account of what this agent's own scheduled runs
// already filed into the thread. Nil-safe for the same reason.
func (c AgentLoopConfig) priorReports() []string {
	if c.PriorReports == nil {
		return nil
	}
	return c.PriorReports()
}

func (c AgentLoopConfig) backgrounded() bool {
	return c.Backgrounded != nil && c.Backgrounded()
}

func (c AgentLoopConfig) backgroundEstimate() string {
	if c.BackgroundEstimate == nil {
		return ""
	}
	return strings.TrimSpace(c.BackgroundEstimate())
}
