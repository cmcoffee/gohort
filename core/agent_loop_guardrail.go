package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/cmcoffee/gohort/core/textutil"
	"math/rand/v2"
)

// AgentLoopConfig configures a RunAgentLoop invocation.
// Guardrail interception points — the canonical hook labels shared between
// core (which calls GuardrailCheck at these moments) and the app (which
// resolves which points an agent enabled). One source of truth so the two
// can't drift.
const (
	GuardHookPreInput  = "pre_input"  // judges the incoming request before round 1 (app-layer pre-pass, not called by the loop)
	GuardHookPreAction = "pre_action" // before a consequential tool call
	GuardHookPreOutput = "pre_output" // before the final reply is returned
	GuardHookPeriodic  = "periodic"   // sampling narration mid-turn

	// GuardHookToolResult labels the injection scan of what a TOOL RETURNED
	// (core/toolscan.go). It is NOT one of the warden's hook points and is
	// deliberately absent from the app's validGuardHooks set: the warden judges
	// a candidate against rules the owner authored, and this judges content
	// against a question the framework asks the same way for every agent.
	//
	// The constant exists so the audit log, the breadcrumb trail, and the
	// console name the interception point in the same vocabulary as the other
	// four. It is where the other four cannot reach: pre_input reads the
	// request and pre_output reads the reply, so an agent doing agentic work
	// that is steered by a tool result at round 3 passes both while acting on
	// it for rounds 4 through 10.
	GuardHookToolResult = "tool_result"
)

// The periodic guardrail check used to sample every 4th round with fresh
// narration, and the constant that set the interval is deliberately gone rather
// than raised. Interim prose is appended to history and delivered mid-turn, so
// any round the sampler skipped reached the transcript and the user unjudged —
// an interval cannot be tuned into containment, only out of it. The check now
// runs on every round that produces narration, deduped by prose so a repeated
// lead-in isn't paid for twice. See the gate in the round loop.

// maxGuardrailOutputCorrections bounds how many times one turn may be sent
// back to revise a guardrail-blocked reply, so a reply the model can't make
// compliant can't loop forever. The app's own per-turn block counter
// escalates independently; this is the core-side backstop.
//
// A revise pass only earns its cost when a COMPLIANT answer to the same
// question exists — a format or tone rule ("answer in Spanish", "no bullet
// lists") is corrected into a genuinely better reply. It is close to worthless
// when the rule forbids the very content the question asks for: the model still
// holds that content, still wants to say it, and each attempt manufactures
// another leaking draft to retract and scrub. Guardrails block by default for
// exactly that reason and skip this budget entirely (GuardrailDecision.Correctable
// is false); only a rule the owner marked correctable reaches it.
//
// ONE retry, not two. The second was never observed to rescue a reply the first
// couldn't: a model that missed a shaping rule twice with the correction in front
// of it is not converging, and each pass costs a full generation the user waits
// through plus another draft to retract. If one honest attempt at "you broke this
// rule, try again" doesn't land, the decline is the better answer.
const maxGuardrailOutputCorrections = 1

// GuardrailDecision is what a guardrail check reports about one candidate.
type GuardrailDecision struct {
	// Blocked stops the candidate: the action is not performed, or the reply is
	// not released. The zero value passes, so a check with nothing to say costs
	// the caller no ceremony.
	Blocked bool

	// Message is the trusted, unfenced framework text handed back on a block —
	// what the model is told about why its candidate did not go through.
	Message string

	// Correctable marks a block as worth one more attempt: the rule shapes the
	// answer rather than forbidding it, so sending the reply back can produce a
	// genuinely compliant version. When false the output gates skip the correction
	// budget entirely and hand the reply straight to the rejection writer, because
	// a revise pass against a rule that forbids what was asked would only
	// regenerate the violation from the same context that just produced it.
	//
	// FALSE is the default, deliberately. GuardrailDecision{Blocked: true} with no
	// severity set means block-and-refuse, which is the strict reading; a field
	// named for the blocking case would have made the zero value the lax one.
	//
	// Not named Block, though that is what the UI calls it: this struct already has
	// Blocked for "was the candidate stopped", and Block beside Blocked is a
	// maintenance trap. One word per concept — the app parses rules into
	// guardrailRule.Correctable, ruleIsCorrectable decides, and
	// maxGuardrailOutputCorrections bounds it.
	//
	// It carries no weight at pre_action. A blocked tool call still has a
	// compliant path available — pick a different tool, drop the offending
	// argument, finish the task another way — and that is a change of course, not
	// a refusal. Ending the turn there would convert every recoverable detour into
	// a dead end.
	Correctable bool
}

// guardrailRedactedDraft replaces a blocked assistant draft in history so the
// withheld content is never persisted or delivered. Kept generic (no rule text)
// — it stands in for the scrubbed turn in the transcript.
const guardrailRedactedDraft = "[a draft reply was withheld here: it violated an enforced guardrail and was never shown to the user]"

// guardrailSafeFallbacks are the neutral declines substituted for the reply
// when the model cannot produce a guardrail-compliant one within the
// correction budget — a determined push. This is the hard floor that makes
// pre_output a real guarantee: an input check can always be talked around, but
// the output check cannot release the protected content.
//
// A SET rather than one fixed string, for two reasons. A verbatim-identical
// reply is a fingerprint: someone probing learns exactly which attempts tripped
// the guardrail and can bisect toward the rule without ever seeing it. And the
// same sentence returned every time reads like a broken machine rather than a
// refusal.
//
// Every line must be interchangeable in INFORMATION, only in wording. None
// names a rule, hints whether the limit is capability or policy, or suggests a
// rephrase would land differently — a set that varied on any of those would
// leak more than the single string it replaced. No em-dashes (house style, and
// the display boundary rewrites them anyway).
var guardrailSafeFallbacks = []string{
	"I can't help with that one.",
	"That's not something I can do.",
	"I'm not able to help with that.",
	"I can't take that one on.",
	"That's outside what I can do here.",
	"No, I can't do that one.",
	"I won't be able to help with that.",
	"That one isn't something I can take on.",
}

// guardrailSafeFallbackReply picks a decline at random, never the one it
// returned last.
//
// The original was a uniform draw with no memory, on the argument that
// "don't repeat the last one" makes consecutive refusals distinguishable from
// independent ones. That is true and it is worth almost nothing: an observer
// who probes twice sees two different lines with probability 7/8 under a
// uniform draw and 1 under this one, so the whole signal is a fraction of a
// bit, and it only exists for someone already able to trigger two blocks in a
// row. Against that, a repeat is the single most obvious tell that a machine
// answered — a user hitting the same wall twice reads the identical sentence
// as a canned response, which is exactly what it is. Suppressing the immediate
// repeat costs a rounding error of entropy and removes the tell.
//
// Only the IMMEDIATE repeat. Longer memory would start shaping the sequence
// into something an observer really could read.
func guardrailSafeFallbackReply(custom []string) string {
	// The agent's own set wins when it has one. Those are authored ahead of
	// time (the owner may have had the model write them, then reviewed them),
	// so by the time this runs they are static trusted text.
	//
	// Nothing is generated HERE, at block time. The model available at this
	// point is the one that just failed the correction budget, with the
	// withheld content still in its context, so asking it for user-facing text
	// would make the decline a channel that content can escape through. It
	// would also need guardrail-checking itself, which recurses, and would
	// still need a hardcoded floor when that check failed. A floor that calls
	// the thing it is a floor for is not a floor.
	pool := guardrailSafeFallbacks
	if clean := nonEmptyLines(custom); len(clean) > 0 {
		// An owner who authored exactly one line chose to always say that. Not
		// topped up from the built-ins: their voice wins outright, and mixing
		// stock lines into it would be the framework overruling an explicit
		// choice to fix a problem the owner may not have.
		pool = clean
	}
	guardrailDeclineMu.Lock()
	defer guardrailDeclineMu.Unlock()
	if len(pool) > 1 {
		var fresh []string
		for _, line := range pool {
			if line != guardrailLastDecline {
				fresh = append(fresh, line)
			}
		}
		if len(fresh) > 0 {
			pool = fresh
		}
	}
	pick := pool[rand.IntN(len(pool))]
	guardrailLastDecline = pick
	return pick
}

var (
	guardrailDeclineMu   sync.Mutex
	guardrailLastDecline string
)

// deliverPreEmptedReply hands back a reply for a turn that never ran, shaped like
// a turn that did.
//
// It streams the text and fires one Done step, because that is how every host
// already renders an answer: the web path builds its transcript from streamed
// content, so returning the text without streaming it persists a reply the
// browser never paints — a blank bubble. Mimicking the normal terminal round is
// what lets seven different callers deliver this without seven short-circuits.
// Returns a well-formed history too — the input turns plus the reply as the
// assistant's. A caller that persists the transcript (a scheduled fire does)
// would otherwise record nothing for the turn, and a nil history reads as "the
// loop produced no conversation" rather than "the conversation was one refusal".
func (T *AppCore) deliverPreEmptedReply(messages []Message, reply string, cfg AgentLoopConfig) (*Response, []Message) {
	if cfg.Stream != nil {
		cfg.Stream(reply)
	}
	if cfg.OnStep != nil {
		cfg.OnStep(StepInfo{Round: 0, Content: reply, Done: true})
	}
	Debug("[agent_loop] pre-empted before round 1: delivering an app-supplied reply (%d chars), no model call", len(reply))
	history := make([]Message, 0, len(messages)+1)
	history = append(history, messages...)
	history = append(history, Message{Role: "assistant", Content: reply})
	return &Response{Content: reply}, history
}

// GuardrailDecline returns a neutral decline for a turn that must not run at all
// — the app-layer counterpart to the loop's own floor, for a request a guardrail
// refuses before any model sees it.
//
// Exported so the pre_input hard-block can reuse the curated set rather than
// inventing its own line. That matters: the set is deliberately interchangeable
// in INFORMATION (see guardrailSafeFallbacks), so a second, differently-worded
// pool would leak more than it replaced by letting a prober tell which check
// fired from the shape of the answer.
func GuardrailDecline(custom []string) string { return guardrailSafeFallbackReply(custom) }

// guardrailClosedNote is appended to a substituted decline, for the MODEL only.
//
// Without it the transcript reads as a question that was dodged rather than one
// that was refused, and the next turn quietly completes it — the user asks the
// date and gets the answer to the blocked question. The decline is deliberately
// terse and in the agent's own voice, which makes it read as deferral; nothing
// in it says the matter is settled.
//
// Stripped at every delivery boundary by textutil.StripMetaTags, kept in the
// persisted history the next turn loads. It states the outcome and the two
// behaviours that follow from it, and deliberately does NOT restate the subject
// — repeating it here would re-seed the very topic the block removed.
//
// It also does not name a CHECK, a rule, or a guardrail. Naming the mechanism
// is what guardrailInputMessage dropped for the same reason: it invites the
// model to reason about the system it is inside, which is the deliberation the
// one-shot thinking-off exists to stop. "Declined and closed" is the whole of
// what the next turn needs — the outcome, not the machinery behind it.
const guardrailClosedNote = "\n<gohort-meta>That request was declined and is closed. Do not answer it later, do not return to it unprompted, and do not treat it as unfinished business. Answer only what is asked from here.</gohort-meta>"

// guardrailRejectionReply produces the user-facing text for a halted turn: the
// app's fresh-context rejection model when one is wired, else the canned
// decline. Any empty or whitespace answer falls through to the canned line —
// a rejection call that failed must never leave the turn with nothing to say,
// because the alternative is releasing the draft it was replacing.
func guardrailRejectionReply(cfg AgentLoopConfig, reason string, history []Message) string {
	if cfg.GuardrailReject != nil {
		if reply := strings.TrimSpace(cfg.GuardrailReject(reason, lastUserRequest(history))); reply != "" {
			return reply
		}
		Debug("[agent_loop] guardrail rejection model returned nothing — using the canned decline")
	}
	return guardrailSafeFallbackReply(cfg.GuardrailDeclines)
}

// lastUserRequest returns the most recent genuine user turn — what the person
// actually asked — so a refusal can be about something instead of generic.
//
// Framework notices are injected as user-role messages (corrections, guardrail
// directives, tool results), and by the time a turn halts the tail is usually
// several of those. Handing one to the rejection model would have it refuse the
// framework's own correction text rather than the request. Skips them, and
// strips the date stamp the loop prepends to the live turn.
//
// The skip is a BLOCKLIST — it recognizes frameworkNoticeTag and nothing else —
// so the tag is load-bearing, not decoration. Four injections here shipped
// without it (the wrap-up budget note, the hard-stop directive on its last
// round, the give-up-with-errors re-prompt, and the failure-shape correction),
// and each was a plausible tail at halt time: the refusal would then have been
// written about the framework's own pacing text. Any new user-role message the
// LOOP authors must carry the tag. The one exception that cannot is a
// prompt-tools tool result (it is real data the model must act on, and the tag
// tells it not to) — those are skipped by the empty-Content test in native
// mode, where results ride in ToolResults instead.
func lastUserRequest(history []Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != "user" {
			continue
		}
		c := history[i].Content
		if strings.Contains(c, frameworkNoticeTag) {
			continue
		}
		// Payload turns the LOOP built, recognized by what they CARRY rather
		// than by a tag. The queued-images turn ("Here are N image(s)…") is
		// framework-authored but must never wear the notice tag: the tag tells
		// the model not to act on the message, and that one exists precisely to
		// be acted on. Its Images field says what it is without putting a word
		// in the prompt, so the structure is the discriminator. Same for a
		// tool-results turn, whose Content is empty in native mode but need not
		// be relied on to stay that way.
		if len(history[i].Images) > 0 || len(history[i].ToolResults) > 0 {
			continue
		}
		if strings.HasPrefix(c, "[Current date & time:") {
			if nl := strings.Index(c, "\n\n"); nl >= 0 {
				c = c[nl+2:]
			}
		}
		if c = strings.TrimSpace(c); c != "" {
			return c
		}
	}
	return ""
}

// nonEmptyLines trims and drops blanks — a set that is all whitespace must
// fall back to the built-ins rather than sending an empty reply.
func nonEmptyLines(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// isGuardrailSafeFallback reports whether s is one of the built-in declines.
// Exists so tests can assert "the floor fired" without pinning which line.
func isGuardrailSafeFallback(s string) bool {
	// Compared as DELIVERED. A substituted decline carries guardrailClosedNote
	// for the model, which every delivery boundary strips — so the reader's
	// copy is the decline alone, and that is what this answers about.
	s = strings.TrimSpace(textutil.StripMetaTags(s))
	for _, f := range guardrailSafeFallbacks {
		if s == f {
			return true
		}
	}
	return false
}

// guardrailArgCharsDefault is how much of each argument value the pre_action
// warden gets to read.
//
// It used to read formatArgs, which caps values at 200 characters — a number
// chosen for the confirm dialog and the loop-guard signature, where 200 is
// plenty, and inherited by the guardrail check without anyone deciding it
// should judge a prefix. A rule about what an agent SENDS someone was being
// applied to the first 200 characters of the message; anything withheld
// further in was invisible to it.
//
// Uncapped is not the answer either. Tool arguments carry base64 images and
// whole documents, and an unbounded warden prompt on one of those is a slow,
// expensive check that may not fit the context at all — so the cap stays and
// only its size changes. 4000 covers essentially any message body, post, or
// commit text a rule would be written about.
const guardrailArgCharsDefault = 4000

// guardrailArgTotalFactor bounds the WHOLE candidate relative to the per-value
// cap, because a call with twenty large arguments would otherwise multiply past
// any per-value limit.
const guardrailArgTotalFactor = 4

func init() {
	RegisterTunable(TunableSpec{
		Key:      "tune_guardrail_action_arg_chars",
		Category: "Limits",
		Label:    "Guardrail: argument text read per tool call",
		Help:     "How much of each argument the pre-action guardrail check reads when judging a consequential tool call. A rule about the CONTENT of what an agent sends is applied to this much of it — raise it if your rules need to see long message bodies or documents, at the cost of a bigger check on every consequential call. The whole candidate is additionally capped at four times this. Does not affect the reply check, which always sees the complete reply.",
		Kind:     KindInt,
		Default:  guardrailArgCharsDefault,
		Min:      200,
		Max:      50000,
	})
}

func guardrailArgChars() int {
	if n := TuneInt("tune_guardrail_action_arg_chars"); n > 0 {
		return n
	}
	return guardrailArgCharsDefault
}

// formatArgsForGuardrail renders a tool call's arguments for the pre_action
// warden. Same deterministic key order as formatArgs — a candidate that varies
// with map iteration order would judge the same call differently on different
// runs — but with a cap sized for JUDGING rather than for display.
//
// Truncation is ANNOUNCED. A warden handed a silent prefix reports that it
// found nothing objectionable, which is true of the prefix and says nothing
// about the rest; told it is looking at part of a value, it can weigh that.
func formatArgsForGuardrail(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	perValue := guardrailArgChars()
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s: %s", k, truncateRunes(stringify(args[k]), perValue)))
	}
	out := strings.Join(lines, "\n")
	return truncateRunes(out, perValue*guardrailArgTotalFactor)
}

// truncateRunes cuts to a rune count, never mid-character, and says so when it
// cuts. Bytes would split a multi-byte rune and hand the warden a replacement
// glyph, which reads as corruption rather than as a trim.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…[truncated for length; more follows that is NOT shown here]"
}
