package core

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// deliveryMarkerRe matches the framework's own "send this file" marker. Shared
// by the phantom-delivery check and the stripper below so the two can never
// disagree about what a delivery claim looks like.
var deliveryMarkerRe = regexp.MustCompile(`\[ATTACH:\s*[^\]]*\]`)

// Correction kinds. One allowance each — a phantom delivery and an orphaned
// tool-call tag are unrelated failures, and spending one must not disarm the
// other. Named constants so a typo can't silently mint a fresh allowance.
const (
	correctionOrphanedXML     = "orphaned-xml"
	correctionPhantomDelivery = "phantom-delivery"
	correctionFakeToolCode    = "fake-tool-code"
	correctionActionPromise   = "action-promise"
	correctionAnnouncedCall   = "announced-call"
	correctionToolMention     = "tool-mention"
	correctionCollapse        = "reasoning-collapse"
	correctionGiveUp          = "giveup-with-errors"
	correctionUnkeptClaim     = "unkept-claim"
	correctionUngrounded      = "ungrounded-claim"
	correctionMachinery       = "machinery-leak"
	correctionTruncated       = "output-truncated"
)

const (
	// maxCorrectionsPerKind is how many times ONE problem gets re-prompted.
	// Two is enough to nudge a stuck turn; a third attempt on the same fault is
	// a model that isn't going to move.
	maxCorrectionsPerKind = 2
	// maxCorrectionsPerTurn is the ceiling across every kind together, so a
	// turn going wrong in many ways at once still terminates. Set above
	// 2×kinds deliberately: the point is to stop a spiral, not to ration
	// guards against each other.
	maxCorrectionsPerTurn = 6
)

// correctionBudget rations the loop's silent re-prompts.
//
// It used to be a single counter shared by every guard, and that had two
// distinct failures. Two corrections of UNRELATED kinds disarmed every other
// guard for the rest of the turn — an orphaned tool tag early on meant a false
// delivery claim later went uncorrected, though nothing about the first says
// anything about the second. And a guard that spent the budget on the SAME
// problem twice simply stopped firing, so the content it had judged wrong
// shipped, with the exhausted guard being the only thing that ever noticed.
//
// Observed: the phantom-delivery guard fired 1/2 and then 2/2 on one invented
// filename, the model rewrote the same claim both times, and the third one went
// out to the user — a claim the framework had already ruled false, twice.
type correctionBudget struct {
	spentByKind map[string]int
	spentTotal  int
	noted       map[string]bool // kinds that have already breadcrumbed their exhaustion
}

func newCorrectionBudget() *correctionBudget {
	return &correctionBudget{spentByKind: map[string]int{}, noted: map[string]bool{}}
}

// available reports whether one more correction of this kind may be spent.
func (b *correctionBudget) available(kind string) bool {
	return b.spentByKind[kind] < maxCorrectionsPerKind && b.spentTotal < maxCorrectionsPerTurn
}

// spend takes one and returns which attempt it was, for the log line.
func (b *correctionBudget) spend(kind string) int {
	b.spentByKind[kind]++
	b.spentTotal++
	return b.spentByKind[kind]
}

// exhausted reports whether this kind is out AND has not said so yet, so the
// caller breadcrumbs once rather than on every round after. A guard that goes
// quiet without a word is the silent drop this codebase keeps paying for.
func (b *correctionBudget) exhausted(kind string) bool {
	if b.available(kind) || b.noted[kind] {
		return false
	}
	b.noted[kind] = true
	return true
}

// phantomDeliveryRefs asks the app whether a reply promises files that do not
// exist. Nil hook = no check, which is the behaviour every host had before it.
func phantomDeliveryRefs(cfg AgentLoopConfig, content string) []string {
	if cfg.PhantomDeliveryRefs == nil || strings.TrimSpace(content) == "" {
		return nil
	}
	return cfg.PhantomDeliveryRefs(content)
}

// UnfulfilledDeliveryReply is what goes out when a reply insists on handing over
// something that does not exist and will not stop after being corrected.
//
// Written in the agent's own voice, short, and with no machinery in it: the
// person on the other end asked for a picture and did not get one, which is the
// whole of what they need to know. It deliberately does NOT apologize for their
// request or ask them to rephrase — the request was fine, and blaming it is the
// failure this whole line of work started from.
//
// Exported because a host may want to recognize or replace it; the default is
// the framework's, and any reply is better than a false one.
func UnfulfilledDeliveryReply(refs []string) string {
	what := "it"
	if named := strings.Join(refs, ", "); named != "" {
		what = named
	}
	return fmt.Sprintf("I said I was sending %s, and I was wrong — it was never made, so there's nothing to send. Say the word and I'll have another go at it.", what)
}

// StripToolCallMarkup removes fake tool-call markup from streamed
// content so it doesn't leak to the user-visible bubble. Used after
// the agent loop promotes a synthesized tool call (or re-prompts the
// LLM for a corrected call) — the original markup stays out of the
// chat surface, only the corrected behavior is visible.
//
// Handles four shapes:
//   - <tool_call>...</tool_call> (Qwen / Hermes — JSON or function form inside)
//   - <function=...>...</function> (bare Hermes/Qwen)
//   - <tool_code>...</tool_code> (Gemini training-data artifact)
//   - ```tool_code ... ``` (markdown code fence variant of the same)
//
// Unclosed tags drop everything from the open onward — safer than
// leaving partial markup that the bubble renders raw.
func StripToolCallMarkup(s string) string {
	// Drop <tool_call>...</tool_call> wrappers first (they may contain
	// JSON-shape calls or function-tag calls inside).
	for {
		start := strings.Index(s, "<tool_call>")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</tool_call>")
		if end < 0 || end < start {
			// Unclosed tag — drop everything from <tool_call> onward
			// to be safe.
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</tool_call>"):]
	}
	// Drop bare <function=...>...</function> blocks (Hermes/Qwen form
	// emitted without the tool_call wrapper).
	for {
		start := strings.Index(s, "<function=")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</function>")
		if end < 0 || end < start {
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</function>"):]
	}
	// Drop <tool_code>...</tool_code> blocks. This is Gemini's
	// training-data artifact format — Qwen sometimes copies it under
	// confusion. The promise-detector elsewhere in the loop catches
	// the pattern and re-prompts; stripping here ensures the bubble
	// doesn't show the raw markup if corrections were exhausted or
	// the strip is being called after the loop gave up.
	for {
		start := strings.Index(s, "<tool_code>")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</tool_code>")
		if end < 0 || end < start {
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</tool_code>"):]
	}
	// Drop ```tool_code ... ``` markdown code-fence variants. Same
	// failure pattern as bare <tool_code> blocks but emitted with
	// markdown wrapping. Fenced blocks may have trailing newlines
	// inside the fence, so match through to the closing ```.
	for {
		start := strings.Index(s, "```tool_code")
		if start < 0 {
			break
		}
		// Find the closing ``` after the open fence.
		searchFrom := start + len("```tool_code")
		end := strings.Index(s[searchFrom:], "```")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + s[searchFrom+end+len("```"):]
	}
	// Note: we do NOT strip "let me try" / "one moment" narration here
	// even though it's noise the user shouldn't see. The promise-detector
	// elsewhere in the loop catches that pattern and re-prompts the LLM
	// to produce clean output, which is more useful than silent removal
	// (the LLM learns the pattern is wrong; doesn't just keep doing it).
	return strings.TrimSpace(s)
}

// containsFakeToolCodeBlock detects training-data-artifact tool-call
// formats the LLM writes as plain text instead of structured calls:
//
//   - <tool_code>...</tool_code> blocks (Gemini's text tool format)
//   - ```tool_code\n...\n``` markdown code fences tagged tool_code
//   - ```json\n[{"tool_def": {...}}]\n``` JSON-shaped tool-call lists
//     in markdown fences (Qwen variant where the LLM writes what a
//     tool_calls field WOULD look like as JSON content)
//   - ::tool_name(arg=val, ...):: cascade-style invocations (a
//     gohort-shaped fake that Qwen has invented in training data;
//     looks like Smalltalk/Ruby cascade with gohort tool names)
//
// Used by the agent loop to detect "model wrote a tool call as
// narrative text" and inject a corrective re-prompt instead of
// silently terminating with the call un-executed.
// bracketedCallDirectiveRe matches a tool call written as a BRACKETED
// DIRECTIVE — "[CALL_FUNCTION] fetch_image: find a photo of …" and its
// siblings. A shape worth naming separately because of how it got out: the
// prose scan is gated off for a long body that finished cleanly (a real answer
// must not be re-read as a tool call), and this arrived inside 700 characters
// of narration. Nothing extracted it, nothing corrected it, and the whole
// monologue — invented progress, second thoughts, "OK found a good one" — went
// to a contact verbatim.
//
// Treated as MARKUP rather than prose, because that is what it is: no sentence
// a person writes contains "[CALL_FUNCTION]". Length can't make it an answer.
var bracketedCallDirectiveRe = regexp.MustCompile(`(?i)\[(?:CALL_FUNCTION|FUNCTION_CALL|TOOL_CALL|CALL_TOOL|INVOKE)\]\s*:?\s*([a-zA-Z_][a-zA-Z0-9_]*)`)

func containsFakeToolCodeBlock(s string) bool {
	if strings.Contains(s, "<tool_code>") {
		return true
	}
	if bracketedCallDirectiveRe.MatchString(s) {
		return true
	}
	if strings.Contains(s, "```tool_code") {
		return true
	}
	if containsFakeJSONToolBlock(s) {
		return true
	}
	// ::name(  — Smalltalk-cascade-shaped fake. Require alphanumeric
	// + underscore for the name, an opening paren, and a closing
	// :: somewhere downstream so we don't false-positive on the
	// common "::" markdown-headline separator or C++-style scope
	// resolution that might appear in legitimate prose.
	if idx := strings.Index(s, "::"); idx >= 0 {
		rest := s[idx+2:]
		nameEnd := 0
		for nameEnd < len(rest) {
			c := rest[nameEnd]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
				nameEnd++
				continue
			}
			break
		}
		if nameEnd > 0 && nameEnd < len(rest) && rest[nameEnd] == '(' && strings.Contains(rest[nameEnd:], "::") {
			return true
		}
	}
	return false
}

// fakeJSONFenceToolNames is the set of tool names whose presence
// as a JSON key inside a ```json fence flags the fence as a fake
// tool-call attempt. Restricted to authoring-side names that would
// only legitimately appear via native tool_calls, never as content
// describing what a worker found. Adding ordinary read tools here
// (knowledge_search, fetch_url) would false-positive on workers
// that legitimately return JSON examples mentioning them.
var fakeJSONFenceToolNames = []string{
	"tool_def", "create_agent", "update_agent", "clone_agent",
	"delete_agent", "add_tool", "skill_def", "pipeline",
}

// containsFakeJSONToolBlock detects when the LLM emits a tool call
// as JSON inside a ```json markdown fence — the Qwen variant where
// the model writes what a native tool_calls payload would look like
// as content. The distinguishing signal is an authoring tool name
// appearing as a JSON key inside the fence body. A regular ```json
// fence with no authoring-tool-name key reads as legitimate JSON
// content (a worker showing an API response shape) and is left
// alone.
func containsFakeJSONToolBlock(s string) bool {
	lower := strings.ToLower(s)
	pos := 0
	for {
		idx := strings.Index(lower[pos:], "```json")
		if idx < 0 {
			return false
		}
		fenceStart := pos + idx
		bodyStart := fenceStart + len("```json")
		bodyEnd := strings.Index(s[bodyStart:], "```")
		if bodyEnd < 0 {
			// Unclosed fence — treat as fake if any authoring name
			// appears anywhere from the open onward.
			tail := s[bodyStart:]
			for _, name := range fakeJSONFenceToolNames {
				if strings.Contains(tail, `"`+name+`"`) {
					return true
				}
			}
			return false
		}
		body := s[bodyStart : bodyStart+bodyEnd]
		for _, name := range fakeJSONFenceToolNames {
			if strings.Contains(body, `"`+name+`"`) {
				return true
			}
		}
		// This fence is legit JSON content — advance past it and
		// keep looking for another one.
		pos = bodyStart + bodyEnd + len("```")
	}
}

// extractFakeToolCodeName pulls the first plausible tool-name out
// of a fake <tool_code> or ::name(...):: block so the corrective
// message can reference it ("You appeared to invoke 'tool_def'").
// Returns "" when no name can be extracted.
func extractFakeToolCodeName(s string) string {
	// [CALL_FUNCTION] name: … form
	if m := bracketedCallDirectiveRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	// ::name( form
	if idx := strings.Index(s, "::"); idx >= 0 {
		rest := s[idx+2:]
		nameEnd := 0
		for nameEnd < len(rest) {
			c := rest[nameEnd]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
				nameEnd++
				continue
			}
			break
		}
		if nameEnd > 0 && nameEnd < len(rest) && rest[nameEnd] == '(' {
			return rest[:nameEnd]
		}
	}
	// <tool_code>\n[whitespace]name( form
	if start := strings.Index(s, "<tool_code>"); start >= 0 {
		body := s[start+len("<tool_code>"):]
		// Skip leading whitespace and any ::
		body = strings.TrimSpace(body)
		body = strings.TrimPrefix(body, "::")
		nameEnd := 0
		for nameEnd < len(body) {
			c := body[nameEnd]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
				nameEnd++
				continue
			}
			break
		}
		if nameEnd > 0 && nameEnd < len(body) && body[nameEnd] == '(' {
			return body[:nameEnd]
		}
	}
	return ""
}

// stripFakeToolCodeBlocks removes <tool_code>...</tool_code> blocks,
// ```tool_code fenced blocks, and ::name(...):: cascades from text
// content so the user-visible message doesn't show the fake
// invocation alongside the narrative that introduced it.
func stripFakeToolCodeBlocks(s string) string {
	// [CALL_FUNCTION] name: … — drop from the directive to the end of its line.
	// The narration around it is left alone: the correction re-prompt replaces
	// the whole reply anyway, and cutting more than the directive would be
	// guessing at where the model's real sentence began.
	for {
		loc := bracketedCallDirectiveRe.FindStringIndex(s)
		if loc == nil {
			break
		}
		end := strings.IndexByte(s[loc[0]:], '\n')
		if end < 0 {
			s = s[:loc[0]]
			break
		}
		s = s[:loc[0]] + s[loc[0]+end+1:]
	}
	// <tool_code>...</tool_code>
	for {
		start := strings.Index(s, "<tool_code>")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</tool_code>")
		if end < 0 || end < start {
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</tool_code>"):]
	}
	// ```tool_code\n...\n```
	for {
		start := strings.Index(s, "```tool_code")
		if start < 0 {
			break
		}
		end := strings.Index(s[start+len("```tool_code"):], "```")
		if end < 0 {
			s = s[:start]
			break
		}
		closeAt := start + len("```tool_code") + end + len("```")
		s = s[:start] + s[closeAt:]
	}
	// ::name(...)::  — best-effort: drop from "::" through the matching "::".
	for {
		start := strings.Index(s, "::")
		if start < 0 {
			break
		}
		// Confirm this is the cascade-call shape (name follows, then "(").
		rest := s[start+2:]
		nameEnd := 0
		for nameEnd < len(rest) {
			c := rest[nameEnd]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
				nameEnd++
				continue
			}
			break
		}
		if nameEnd == 0 || nameEnd >= len(rest) || rest[nameEnd] != '(' {
			break // not a fake call — leave the rest alone
		}
		end := strings.Index(rest, "::")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + rest[end+2:]
	}
	return strings.TrimSpace(s)
}

// containsActionPromise reports whether content includes an explicit
// promise of action — phrases the LLM emits when it intends to call a
// tool but doesn't actually emit the call. Detection is conservative:
// only matches forms that almost always indicate "I'm about to do
// something" and never natural conversational closes ("let me know if
// you have other questions" wouldn't trigger because of the "know if").
//
// Scope: matches the trailing portion of content (last ~200 chars)
// since the action-promise is usually the closing sentence, and a
// promise-shaped phrase mid-text followed by a real conclusion is
// usually fine. Case-insensitive.
func containsActionPromise(content string) bool {
	c := strings.ToLower(strings.TrimSpace(content))
	if c == "" {
		return false
	}
	// Look at trailing 200 chars; longer content with a closing
	// promise is the typical failure shape.
	if len(c) > 200 {
		c = c[len(c)-200:]
	}
	// Phrase set chosen to match "stated intent to act" and avoid
	// natural conversational closes. Each must be followed by some
	// hint of an upcoming action ("try", "pull", "check", etc.) or
	// a temporal hold ("moment", "second", "sec").
	phrases := []string{
		"let me try",
		"let me figure",
		"let me pull",
		"let me look up",
		"let me check",
		"let me see if",
		"let me get",
		"let me find",
		"let me grab",
		"let me look",
		"let me actually",
		"let me first",
		"i'll figure",
		"i'll pull",
		"i'll check",
		"i'll look",
		"i'll try",
		"i'll grab",
		"i'll fetch",
		"one moment",
		"one sec",
		"give me a moment",
		"give me a sec",
		"hold on",
		"stand by",
		"hang on",
		"hold tight",
		"bear with me",
		"working on it",
		"on it",
	}
	for _, p := range phrases {
		if strings.Contains(c, p) {
			return true
		}
	}
	return false
}

// frameworkNoticeTag prefixes every loop-injected corrective/pacing message.
// These ride the user ROLE (the only reliable mid-conversation carrier), so
// small models kept attributing them to the human and answering THEM
// ("You're right, I dispatched Comedian 120 times. My bad.") instead of the
// actual user. The tag makes the origin explicit and forbids replying to it.
const frameworkNoticeTag = "[AUTOMATED FRAMEWORK NOTICE — not written by the user, who cannot see it. Do not reply to it, apologize, or address anyone about it; silently adjust and continue.] "

// isGuardStopResult reports whether a nominally-successful tool result is
// actually a framework guard verdict ("STOP — you have already…") rather than
// real output. Inner guards return STOP as a plain string; the loop's
// progress accounting must not mistake that for a working tool call. The scan
// tolerates the untrusted-content fence prefix on dispatch results.
func isGuardStopResult(content string) bool {
	head := content
	if len(head) > 600 {
		head = head[:600]
	}
	return strings.Contains(head, "STOP — you")
}

// endsWithCallAnnouncement detects the announce-then-stop failure: the reply's
// LAST line ends with a colon introducing a call that never followed —
// "Here's the `update_agent` call to implement these changes:" and then the
// turn ends. Far narrower than the disabled containsActionPromise (which
// false-positived on conversational "I'll try next time" closes): a complete
// reply essentially never terminates on a colon, and the colon alone still
// isn't enough — the line must also read like a call announcement, either by
// containing a snake_case token (tool-ish names like update_agent don't occur
// in ordinary prose; the announced name may be INVENTED, so matching against
// the real catalog would miss exactly the worst case) or the words
// "call"/"tool". A legit turn-ending colon ("Paste the error here:") carries
// neither signal.
func endsWithCallAnnouncement(content string) bool {
	trimmed := strings.TrimSpace(content)
	if !strings.HasSuffix(trimmed, ":") {
		return false
	}
	line := trimmed
	if i := strings.LastIndexByte(trimmed, '\n'); i >= 0 {
		line = strings.TrimSpace(trimmed[i+1:])
	}
	lower := strings.ToLower(strings.ReplaceAll(line, "\u2019", "'"))
	if callWordRe.MatchString(lower) {
		return true
	}
	if snakeCaseTokenRe.MatchString(lower) {
		return true
	}
	// First-person intent is the general form of this failure, and requiring a
	// call word missed most of it: "Let me dig up that benchmark article with
	// actual token/s numbers:" announces work, ends on a colon, and stops —
	// but names no tool and contains no snake_case, so the guard passed it
	// through and the user watched the turn end on a promise.
	//
	// What separates it from a legitimate turn-ending colon is WHO the colon
	// commits. "Paste the error message here:" hands the next move to the
	// user and is complete. "Let me look that up:" / "Here's the plan:" commit
	// the AGENT to something that then never arrives.
	//
	// Checked in this order because a line can carry both — "Send me the link
	// and I'll take a look:" states first-person intent AND hands over the
	// next move, and it is the handover that makes it complete. Asking is a
	// finished turn; promising is not.
	if userDirectiveRe.MatchString(lower) {
		return false
	}
	return firstPersonIntentRe.MatchString(lower)
}

// replyStalledOnAPromiseMaxLen is the lead-in cutoff: past it, a reply is an
// ANSWER that happens to contain "I'll", not a turn that stalled on a promise.
// Same 600 the no-arg-mention guard and the runner's narration cutoff use.
const replyStalledOnAPromiseMaxLen = 600

// replyStalledOnAPromise reports whether a reply commits the agent to work it
// then never does — "let me create this", "I'll blend Alex onto the picture" —
// and stops.
//
// This is endsWithCallAnnouncement's shape with the colon requirement dropped,
// and dropping it is the whole point: the colon is a typographic accident, not
// the failure. "Here's the update_agent call to implement these changes:" and
// "Got it, let me create this. I'll blend Alex onto the picture. 🏚️👔" are the
// same turn ending the same way, and only the first one was catchable.
//
// Without the colon this is far too loose to act on alone — that looseness is
// what got the standalone actionPromiseCorrection disabled, and it stays
// disabled. Its ONLY caller conjoins it with unaddressed tool errors, no tool
// call this round, rounds to spare, and a correction budget. Under those, a
// sentence about what happens next is never a finished turn.
//
// Two carve-outs, both inherited from endsWithCallAnnouncement:
//   - a directive to the USER ends a turn legitimately, however it is
//     punctuated — asking is finished, promising is not.
//   - length. A long reply containing "I'll" is an answer; this is for the
//     lead-in that was supposed to be followed by a tool call.
func replyStalledOnAPromise(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || len(trimmed) > replyStalledOnAPromiseMaxLen {
		return false
	}
	lower := strings.ToLower(strings.ReplaceAll(trimmed, "’", "'"))
	if userDirectiveRe.MatchString(lower) {
		return false
	}
	// A promise about future CONDUCT is not work left undone, and there is no
	// tool that performs it.
	if behavioralCommitmentRe.MatchString(lower) {
		return false
	}
	return futureCommitmentRe.MatchString(lower)
}

// ReplyPromisesWork reports whether a reply commits the agent to work it has
// not done — the exported form of the loop's own stall predicate, for a host
// that wants to decide something about the turn AFTER it ends.
//
// The loop can only correct a promise while the turn is still running. What it
// cannot do is remember: the next turn arrives as a fresh call, so a user
// holding the agent to something it said a minute ago ("are you really?") gets
// answered by a model with no idea it promised anything. See the commitment
// ledger in the orchestrate app.
func ReplyPromisesWork(reply string) bool { return replyStalledOnAPromise(reply) }

// futureCommitmentRe matches the agent committing itself to work that has NOT
// happened yet.
//
// Split out from firstPersonIntentRe, which also matches "here's" / "here is".
// Presenting something is a finished turn; promising something is not, and the
// difference only stopped mattering while this was conjoined with pending tool
// errors. Standing alone it decides whether an ordinary reply gets re-prompted,
// and "Here's the answer: 42." is an answer.
var futureCommitmentRe = regexp.MustCompile(`\b(?:let me|i'll|i will|i'm going to|i am going to|going to|now i|next i|on it)\b`)

// behavioralCommitmentRe matches the agent promising to BEHAVE differently
// rather than to do something.
//
// futureCommitmentRe matches "i'll", which is the right hook for "I'll look
// that up" and the wrong one for "I'll keep it straight going forward". Both
// are promises; only the first has work behind it. Told apart by what follows,
// because nothing in the sentence's grammar distinguishes them.
//
// Observed 2026-08-29: a user asked why the agent used em-dashes, it answered
// "I'll keep it straight going forward", and the guard re-prompted it to "do it
// NOW with a real tool call". There is no tool for not using a punctuation
// mark. It fired three times in one casual conversation, concatenated its
// retries into one bubble, and ended with the agent inventing work nobody
// asked for so it would have a tool call to make. A guard that demands an
// action for a promise no action can keep does not correct the turn, it
// derails it.
var behavioralCommitmentRe = regexp.MustCompile(`\b(?:going forward|from now on|next time|in future|in the future|this time|won't happen again|will not happen again|keep (?:that|this|it) in mind|keep (?:that|this|it) straight|watch (?:out )?for (?:that|this|it)|be (?:more )?careful|my (?:mistake|bad)|noted)\b`)

// callWordRe word-bounds the announcement keywords so "basically:" /
// "technically:" (which CONTAIN "call") can't false-fire the guard.
var callWordRe = regexp.MustCompile(`\b(?:call|calls|calling|tool|tools|toolbox)\b`)

// snakeCaseTokenRe matches a multi-word snake_case identifier — the shape of
// tool/action names ("update_agent", "reply_to_comment") and essentially
// nothing in natural prose.
var snakeCaseTokenRe = regexp.MustCompile(`\b[a-z0-9]+(?:_[a-z0-9]+)+\b`)

// firstPersonIntentRe matches the agent committing ITSELF to what follows the
// colon. Deliberately first-person: an imperative aimed at the user ("paste
// the error message here:") ends a turn legitimately and must not re-prompt.
var firstPersonIntentRe = regexp.MustCompile(`\b(?:let me|i'll|i will|i'm going to|i am going to|here's|here is|now i|next i|going to)\b`)

// userDirectiveRe matches a line handing the next move to the USER. A turn that
// ends by asking for something is finished, however it is punctuated — it is
// waiting, not stalled. Takes precedence over first-person intent, which the
// same sentence often also contains ("send me the link and I'll look:").
// "let me know" appears here rather than as intent for exactly that reason.
var userDirectiveRe = regexp.MustCompile(`\b(?:paste|send me|send it|reply with|tell me|let me know|share (?:the|it|that)|upload|attach|type|enter|choose|pick)\b`)

// responseWasTruncated reports whether the provider cut the response off at the
// output ceiling rather than the model finishing.
//
// Both spellings: Anthropic says "max_tokens", the OpenAI-compatible clients
// (and Gemini, through geminiStopReason) say "length". Every client populates
// StopReason now, so this is a fact rather than a guess.
//
// "interrupted" is this package's own value (stopInterrupted): a stream
// that closed before the provider sent its stop reason. Same situation from
// the loop's side — content in hand, model not done — so the same continuation.
//
// Deliberately does NOT require empty content. A truncated turn usually HAS
// content — a preamble the model wrote before it ran out — and the guard that
// only fires on emptiness (llm_openai.go's finish_reason==length check) misses
// exactly the case that matters: 133 characters of "Doing it now" with the
// tool call it was about to make lost past the ceiling.
// joinContinuation puts a continued reply back together: the text that was
// cut off, then what the model wrote when asked to carry on. The seam gets a
// space only when both sides end and begin in a word character — a cut lands
// mid-token, and "config" + "is stored" is far more common than "config" +
// "uration"; punctuation or whitespace on either side needs nothing.
func joinContinuation(lead, tail string) string {
	lead = strings.TrimRight(lead, " \t")
	tail = strings.TrimLeft(tail, " \t")
	if lead == "" {
		return tail
	}
	if tail == "" {
		return lead
	}
	last, _ := utf8.DecodeLastRuneInString(lead)
	first, _ := utf8.DecodeRuneInString(tail)
	wordy := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	if wordy(last) && wordy(first) {
		return lead + " " + tail
	}
	return lead + tail
}

// truncationDiag names the cut for the turn's diagnostics: the output limit
// and a dropped stream are fixed in different places.
func truncationDiag(resp *Response) string {
	if strings.EqualFold(strings.TrimSpace(resp.StopReason), stopInterrupted) {
		return "The provider's stream ended before the model finished its reply; it was asked to continue."
	}
	return "The model's reply hit the output limit before it finished; it was asked to continue."
}

func responseWasTruncated(resp *Response) bool {
	if resp == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(resp.StopReason)) {
	case "max_tokens", "length", stopInterrupted:
		return true
	}
	return false
}

// providerRefused reports whether a response is the provider declining on its
// OWN content policy rather than the model answering.
//
// This is not the local deployment's policy — a remote provider refusing an
// agent-design turn (or a game with a rude name) says nothing about what this
// deployment permits, and the local worker will simply do the work. Detected
// from the stop reason, which every client now populates.
//
// Content or tool calls means the model answered; a filter that fires after a
// complete answer is not a refusal to act on.
func providerRefused(resp *Response) bool {
	if resp == nil {
		return false
	}
	if strings.TrimSpace(resp.Content) != "" || len(resp.ToolCalls) > 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(resp.StopReason)) {
	case "safety", "recitation", "blocklist", "content_filter", "prohibited_content", "refusal":
		return true
	}
	return false
}

// providerCutReply reports a reply the provider stopped PARTWAY on its content
// policy: content arrived, then stop_reason=refusal. The empty case is
// providerRefused's (it re-runs the round on the worker); this one has a
// fragment the user has already watched stream, which is delivered as-is with
// a diagnostic saying why it stops where it does.
func providerCutReply(resp *Response) bool {
	if resp == nil || strings.TrimSpace(resp.Content) == "" || len(resp.ToolCalls) > 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(resp.StopReason), "refusal")
}
