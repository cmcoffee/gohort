package core

import (
	"fmt"
	"strings"
	"time"
)

// FirstToolOrderViolation returns the index of the first message carrying tool
// results that does NOT directly follow an assistant tool-call message (or
// another tool-result message), or -1 when the history is well-formed.
//
// Providers enforce this adjacency in their chat templates, and they enforce it
// HARD: llama.cpp answers with a 400 ("A tool message must follow an assistant
// or tool message") and the turn dies with loop_error, ~40s in, having produced
// nothing. It is an easy invariant to break by accident, because the natural
// place to inject a mid-round correction — right where the results are
// inspected — is inside the very gap the rule forbids. That is exactly how the
// failure-shape guard broke it: the guard fired only on turns that had already
// hit three identical failures, so it killed precisely the builds that were
// struggling while easy turns sailed through.
//
// Kept as an exported pure function so callers can assert cheaply before a
// dispatch and leave a breadcrumb naming the offending index, instead of
// discovering the problem as an opaque provider error much later.
func FirstToolOrderViolation(history []Message) int {
	for i, m := range history {
		if len(m.ToolResults) == 0 {
			continue
		}
		if i == 0 {
			return i
		}
		prev := history[i-1]
		if len(prev.ToolCalls) == 0 && len(prev.ToolResults) == 0 {
			return i
		}
	}
	return -1
}

// failureShapeCorrection builds the message injected when a turn has hit the
// same failure shape repeatedly. Two forms:
//
//   - With a working consult: the wall's raw text goes to a model that can
//     ANSWER, and its reply rides back as explicitly-labelled advice. Telling a
//     stuck model "stop retrying variations" only names the problem; advice can
//     supply the fix.
//   - Without one (nil, error, or an empty reply): the generic directive, which
//     is still correct on its own. A consult that fails must never cost the
//     turn its correction.
//
// Advice is labelled advice in both the prose and the ADVICE prefix. Both tiers
// have been confidently wrong about the same API inside one session, so the
// model is told to verify before reporting anything as working.
//
// Returns the message and whether a consult actually supplied it.
func failureShapeCorrection(n int, shape, evidence string, consult func(question, evidence string) (string, error)) (string, bool) {
	if consult != nil {
		q := fmt.Sprintf("An agent has hit this same failure %d times from different calls and different arguments, so its arguments are not what is wrong. Diagnose the failure itself and give the concrete fix — exact field names and nesting if this is a request-shape problem. If the evidence does not settle it, say so and name what would.", n)
		if advice, err := consult(q, evidence); err == nil && strings.TrimSpace(advice) != "" {
			return fmt.Sprintf(
				"You have hit this SAME failure %d times this turn: %q. The arguments are not what's wrong, so a stronger model was consulted with the failure text. Its ADVICE follows — it is advice, not fact: apply it and VERIFY with a real call before reporting anything as working.\n\n%s",
				n, shape, strings.TrimSpace(advice)), true
		} else if err != nil {
			Debug("[agent_loop] failure-shape guard: consult failed, falling back to directive: %v", err)
		}
	}
	return fmt.Sprintf(
		"You have now hit this SAME failure %d times this turn, from different calls and different arguments: %q. The arguments are not what's wrong. Stop retrying variations of it — diagnose the failure itself, take a different approach, or tell the user plainly what is blocked and what you tried.",
		n, shape), false
}

// normalizeFailureShape reduces a failed tool result to a comparable
// fingerprint of WHAT went wrong, so the same wall is recognized across
// different calls and different arguments. Case and whitespace are flattened,
// a leading "error:" is dropped, and long digit runs (ids, timestamps, ports)
// collapse to "#" so one failure with a rotating id is still one shape. Only
// the head of the message is kept — the first line or two carries the failure;
// the tail is usually a stack or a body echo that varies harmlessly.
//
// Returns "" for a result too short to fingerprint, which the caller skips.
func normalizeFailureShape(content string) string {
	s := strings.ToLower(strings.TrimSpace(content))
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "error: ")
	s = strings.TrimPrefix(s, "error:")
	var b strings.Builder
	var digits []rune
	// Long runs collapse to "#" (a rotating id shouldn't split one wall into
	// many); short ones are written back VERBATIM — "exit status 1" and "exit
	// status 2" are different failures, and the shape is quoted back to the
	// model in the nudge, so it must not misreport what it saw.
	flushDigits := func() {
		if len(digits) >= 4 {
			b.WriteByte('#')
		} else {
			for _, d := range digits {
				b.WriteRune(d)
			}
		}
		digits = digits[:0]
	}
	lastSpace := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits = append(digits, r)
			lastSpace = false
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flushDigits()
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			flushDigits()
			b.WriteRune(r)
			lastSpace = false
		}
	}
	flushDigits()
	out := strings.TrimSpace(b.String())
	if len(out) < 12 { // too generic to be a meaningful fingerprint
		return ""
	}
	if len(out) > 160 {
		out = out[:160]
	}
	return out
}

// Failure-streak collapse — the in-context repetition damper.
//
// One failure fact carries signal; thirty-eight verbatim copies carry a
// BEHAVIOR: by mid-turn, "call the tool, get the error, note it, continue" is
// the dominant pattern in the context and the model imitates the pattern it
// sees (observed live: a standing agent re-firing one broken tool 38× per
// cycle, cycle after cycle — the storms were self-teaching). The damper keeps
// the model's knowledge of a failure while deleting the repetition: once one
// normalized failure shape has recurred errShapeCollapseAt times, the MIDDLE
// occurrences in the accumulated history are rewritten to a one-line marker —
// the FIRST occurrence keeps its full text (the informative copy) and the
// newest occurrence stays full (the current state). Rewrite-in-place, never
// remove: tool-result messages must keep their ids and ordering for the
// provider's chat template.
//
// This also applies to INCOMING history at loop start (prior turns' tool
// results ride back in via toLLMMessages), so a storm that already happened
// stops re-teaching every later turn. Stored sessions are only affected for
// results produced after the streak tripped — the first full occurrence is
// always preserved for exports/debugging.
const errShapeCollapseAt = 3

// collapsedFailureMarker names the collapsed shape so the marker is
// self-describing — in the live context AND in a session export, a collapsed
// row should say WHICH error it was without scrolling for the first
// occurrence. Markers of one shape are byte-identical (cache-friendly), and
// the marker's own shape never matches the original (the prefix text differs),
// so sweeps stay idempotent.
func collapsedFailureMarker(shape string) string {
	return "(repeated failure collapsed — same as the earlier full result: \"" + oneLineShape(shape) + "\")"
}

// resolvedFailureMarker replaces a failure result whose tool LATER succeeded
// this turn — the failure is stale the moment the 200 lands, and a context
// carrying both teaches the model to arbitrate; models side with whatever
// appears more often, which is the failure.
func resolvedFailureMarker(toolName string) string {
	return "(earlier " + toolName + " failure collapsed — a later " + toolName + " call SUCCEEDED this turn; treat the failure as resolved)"
}

// collapseRepeatedFailureResults rewrites duplicate occurrences of one
// failure shape across history's tool results. The first occurrence is always
// kept in full; keepLast additionally preserves the newest one (used for
// incoming history, where the latest copy IS the current state — during a
// live turn the current round's full result is appended after the sweep, so
// keepLast is false there). Copy-on-write per message: history is a shallow
// copy of the caller's slice, so the ToolResults backing arrays are shared —
// clone before mutating so the rewrite never reaches the caller's messages.
func collapseRepeatedFailureResults(history []Message, shape string, keepLast bool) int {
	type pos struct{ mi, ri int }
	var found []pos
	for mi := range history {
		for ri := range history[mi].ToolResults {
			r := &history[mi].ToolResults[ri]
			if r.IsError && normalizeFailureShape(r.Content) == shape {
				found = append(found, pos{mi, ri})
			}
		}
	}
	end := len(found)
	if keepLast {
		end--
	}
	if end <= 1 {
		return 0
	}
	n := 0
	cloned := map[int]bool{}
	for _, p := range found[1:end] {
		if !cloned[p.mi] {
			history[p.mi].ToolResults = append([]ToolResult(nil), history[p.mi].ToolResults...)
			cloned[p.mi] = true
		}
		history[p.mi].ToolResults[p.ri].Content = collapsedFailureMarker(shape)
		n++
	}
	return n
}

// collapseIncomingFailureStreaks applies the damper to the history a turn
// STARTS with: prior turns' failure storms ride back in through the rebuilt
// tool rounds, and without this each new turn re-reads the whole wall.
func collapseIncomingFailureStreaks(history []Message) int {
	counts := map[string]int{}
	for mi := range history {
		for _, r := range history[mi].ToolResults {
			if r.IsError {
				if s := normalizeFailureShape(r.Content); s != "" {
					counts[s]++
				}
			}
		}
	}
	total := 0
	for shape, c := range counts {
		if c >= errShapeCollapseAt {
			total += collapseRepeatedFailureResults(history, shape, true)
		}
	}
	return total
}

// retireResolvedFailureResults is the success half of the damper: when a tool
// that failed earlier this turn SUCCEEDS, every prior failure result matching
// one of that tool's recorded failure shapes is rewritten to a resolved
// marker — including the first occurrence, because a resolved failure's full
// text is no longer information, it's a contradiction of the current state.
func retireResolvedFailureResults(history []Message, shapes map[string]bool, toolName string) int {
	if len(shapes) == 0 {
		return 0
	}
	n := 0
	cloned := map[int]bool{}
	for mi := range history {
		for ri := range history[mi].ToolResults {
			r := &history[mi].ToolResults[ri]
			if !r.IsError || !shapes[normalizeFailureShape(r.Content)] {
				continue
			}
			if !cloned[mi] {
				history[mi].ToolResults = append([]ToolResult(nil), history[mi].ToolResults...)
				cloned[mi] = true
			}
			history[mi].ToolResults[ri].Content = resolvedFailureMarker(toolName)
			n++
		}
	}
	return n
}

// oneLineShape renders a failure shape for a log line or a directive.
func oneLineShape(shape string) string {
	if len(shape) > 120 {
		return shape[:120] + "…"
	}
	return shape
}

// repeatFailHistoryWindow bounds how far back seedRepeatFailFromHistory
// replays: only the recent tail counts, so an ancient failure doesn't
// ban a call forever (a fixation worth stopping shows up within a handful
// of recent turns; a success anywhere in the window clears it).
const repeatFailHistoryWindow = 40

// shakeoutTemperature is the one-shot sampling temperature applied to the
// round right after a repeat guard fires. High enough to break a greedy
// fixed point (Qwen 3.x route defaults are ~0.6-0.7), low enough to stay
// out of degeneration territory. Per-call, so it never touches the server
// config and costs nothing when no guard has tripped.
const shakeoutTemperature = 0.9

// seedRepeatFailFromHistory pre-arms the loop-guard from the conversation
// tail so a fixation spanning separate turns is caught. It walks the
// recent messages in order, mapping each tool call's ID to its signature,
// then applies each tool result under the SAME rule the live loop uses:
// bump the signature on an errored result, clear it on a successful one.
// The result is repeatFail reflecting the current per-signature failure
// streak as of the end of history — so a call that already failed
// identically repeatFailLimit times is blocked on its next attempt.
func seedRepeatFailFromHistory(messages []Message, repeatFail map[string]int) {
	start := 0
	if len(messages) > repeatFailHistoryWindow {
		start = len(messages) - repeatFailHistoryWindow
	}
	idToSig := map[string]string{}
	for _, m := range messages[start:] {
		for _, tc := range m.ToolCalls {
			idToSig[tc.ID] = tc.Name + "\x00" + formatArgs(tc.Args)
		}
		for _, tr := range m.ToolResults {
			sig, ok := idToSig[tr.ID]
			if !ok {
				continue
			}
			if tr.IsError {
				repeatFail[sig]++
			} else {
				delete(repeatFail, sig)
			}
		}
	}
}

// --- failure memory across turns ---------------------------------------------
//
// The repeat guard counts identical failures within one turn, and a
// conversation re-arms it from the tool results in its history. Standing work
// has neither: a scheduled fire is a fresh loop whose history is stored
// messages, role and content only. So a call that failed the same way every
// hour for a week was, every hour, a brand-new failure — retried, re-diagnosed
// in the report, and forgotten again.
//
// This is the smallest thing that fixes it: the per-signature counts, kept
// under a key the caller chooses, aged out so a fault that gets fixed stops
// being remembered.

const failureMemoryTable = "agent_failure_memory"

// failureMemoryTTL is how long a remembered failure stays remembered. Long
// enough to span a daily task's fires, short enough that a repaired endpoint
// is not held against a call for a week.
const failureMemoryTTL = 48 * time.Hour

type failureMemory struct {
	Counts  map[string]int       `json:"counts"`
	Seen    map[string]time.Time `json:"seen"`
	Updated time.Time            `json:"updated"`
}

// loadFailureMemory seeds counts with what this key has failed at recently.
// Entries past the TTL are ignored, so a fixed fault fades on its own.
//
// carryMax caps what is carried in, and is the guard's limit MINUS ONE. That
// one is the whole design: a call that has failed for days gets exactly one
// attempt per turn rather than being refused outright. An endpoint fixed
// overnight comes back by itself and its count clears on the success, while
// a genuinely dead one costs one call a cycle instead of three — and nothing
// the framework refuses forever on evidence it gathered yesterday.
func loadFailureMemory(key string, counts map[string]int, carryMax int) {
	if strings.TrimSpace(key) == "" || RootDB == nil {
		return
	}
	var mem failureMemory
	if !RootDB.Get(failureMemoryTable, key, &mem) {
		return
	}
	cutoff := time.Now().Add(-failureMemoryTTL)
	carried := 0
	for sig, n := range mem.Counts {
		if at, ok := mem.Seen[sig]; ok && at.Before(cutoff) {
			continue
		}
		if n > 0 {
			if carryMax > 0 && n > carryMax {
				n = carryMax
			}
			counts[sig] = n
			carried++
		}
	}
	if carried > 0 {
		Debug("[agent_loop] failure memory %q: carried %d failing call signature(s) from earlier work", key, carried)
	}
}

// saveFailureMemory persists the counts that are still failing. A signature
// the turn CLEARED is dropped rather than written as zero: the guard resets a
// count on success, and that success is exactly what should stop this being
// remembered at all.
func saveFailureMemory(key string, counts map[string]int) {
	if strings.TrimSpace(key) == "" || RootDB == nil {
		return
	}
	now := time.Now()
	mem := failureMemory{Counts: map[string]int{}, Seen: map[string]time.Time{}, Updated: now}
	var prior failureMemory
	RootDB.Get(failureMemoryTable, key, &prior)
	cutoff := now.Add(-failureMemoryTTL)
	for sig, n := range counts {
		if n <= 0 {
			continue
		}
		mem.Counts[sig] = n
		// Keep the ORIGINAL first-seen stamp when the count only carried
		// through, so a failure cannot renew its own lease by being retried.
		if at, ok := prior.Seen[sig]; ok && at.After(cutoff) {
			mem.Seen[sig] = at
		} else {
			mem.Seen[sig] = now
		}
	}
	if len(mem.Counts) == 0 {
		RootDB.Unset(failureMemoryTable, key)
		return
	}
	RootDB.Set(failureMemoryTable, key, &mem)
}
