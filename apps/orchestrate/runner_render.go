package orchestrate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// activityCheapID returns a monotonic short id for an activity row.
// Each activity event needs a unique id so future updates (truncation
// hints, status flips) can target it; we don't have one from the
// agent loop, so we generate here. Atomic counter to keep concurrent
// wrapped-handler calls from racing.
var activityIDCounter uint64

func activityCheapID() string {
	n := atomic.AddUint64(&activityIDCounter, 1)
	return fmt.Sprintf("a-%d-%d", time.Now().UnixNano(), n)
}

// formatToolCall renders a tool invocation as a single-line label
// for the activity pane. Keys sorted for stable output across runs.
// Values are stringified and clipped to keep the row scannable —
// long args (page bodies, base64 blobs) ruin the at-a-glance read.
func formatToolCall(name string, args map[string]any) string {
	if len(args) == 0 {
		return "🔧 " + name + "()"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var s string
		switch vv := args[k].(type) {
		case string:
			s = vv
		case []any:
			elems := make([]string, 0, len(vv))
			for _, e := range vv {
				elems = append(elems, compactArgElem(e))
			}
			s = "[" + strings.Join(elems, ", ") + "]"
		case map[string]any:
			s = "{…}"
		case nil:
			s = "null"
		default:
			s = fmt.Sprintf("%v", vv)
		}
		s = strings.ReplaceAll(s, "\n", " ")
		if len(s) > 80 {
			s = s[:80] + "…"
		}
		parts = append(parts, fmt.Sprintf("%s=%q", k, s))
	}
	return "🔧 " + name + "(" + strings.Join(parts, ", ") + ")"
}

// deriveFindings extracts a short summary from worker output for the
// plan card's always-visible "findings" preview. Takes the first
// non-empty paragraph (split on blank line), strips markdown noise
// that reads badly without rendering, and clips to a length that
// fits a few lines in the plan card. The full output still lives in
// the collapsible — this is just the headline.
func deriveFindings(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "(no output)" {
		return ""
	}
	// First paragraph = everything up to the first blank line.
	if i := strings.Index(s, "\n\n"); i > 0 {
		s = s[:i]
	}
	// Collapse internal newlines so the snippet renders as one
	// flowing line; the user can expand the full output if they
	// want the structure.
	s = strings.ReplaceAll(s, "\n", " ")
	// Strip a leading markdown heading marker — headings in the
	// findings slot land as bare text and look broken.
	s = strings.TrimLeft(s, "# ")
	const cap = 280
	if len(s) > cap {
		// Cut at the last space before cap to avoid mid-word slices.
		cut := cap
		if sp := strings.LastIndexByte(s[:cap], ' '); sp > cap/2 {
			cut = sp
		}
		s = s[:cut] + "…"
	}
	return strings.TrimSpace(s)
}

// truncate clips long tool-output text for activity rendering. The
// worker's full output still rides through the agent loop into the
// step's persisted output, so nothing is lost — only the live
// activity row gets cut down.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n…[truncated]"
}

// planBlockID is the stable id for one plan's in-chat block. Each
// chat round (i.e. each user turn that produces a new plan) gets its
// own block via the round-index suffix so prior plans in the same
// session stay visible — re-emitting the same id triggers onUpdate on
// the existing block instead of creating a new one.
func planBlockID(sessionID string, roundIdx int) string {
	return fmt.Sprintf("plan-%s-%d", sessionID, roundIdx)
}

// emitPlanBlock sends the plan as a single `block` SSE event into the
// conversation pane. Called once when the plan is first set and again
// on every step status / output transition; the framework's block
// dispatcher routes the repeat into the renderer's onUpdate so the
// same DOM node refreshes in place.
//
// Shape matches servitor_plan's payload: title + what_to_find +
// findings + blocked_reason. Intent maps to what_to_find (same
// semantic — "what this step is looking for"). Full per-step output
// is NOT shipped to the renderer — findings is the visible result;
// drill-down lives in the activity pane.
func emitPlanBlock(sse *sseWriter, blockID string, steps []PlanStep) {
	// An empty block id means this round has no plan worth showing — the
	// degenerate "Respond directly" pseudo-step (see the synthesis below).
	// Gating here rather than at each of the nine call sites, which re-emit
	// the same block on every step transition: one of them would eventually
	// be missed, and a plan that appears halfway through a turn is worse than
	// one that never appears.
	if blockID == "" {
		return
	}
	items := make([]map[string]any, 0, len(steps))
	for _, s := range steps {
		item := map[string]any{
			"id":     s.ID,
			"title":  s.Title,
			"status": string(s.Status),
		}
		if strings.TrimSpace(s.Intent) != "" {
			item["what_to_find"] = s.Intent
		}
		if strings.TrimSpace(s.Findings) != "" {
			item["findings"] = s.Findings
		}
		if strings.TrimSpace(s.BlockedReason) != "" {
			item["blocked_reason"] = s.BlockedReason
		}
		items = append(items, item)
	}
	sse.Send(map[string]any{
		"kind": "block",
		"type": "orchestrate_plan",
		"id":   blockID,
		"plan": items,
	})
}

// emitIntentBlock emits a servitor_intent-style block before each
// worker step starts. The conversation pane renders an accent-
// bordered card with "▸ Investigating" + the step's title + intent
// as italic reason, so the user sees what the agent is about to do
// in real time. One block per step; unique id so they accumulate
// chronologically rather than overwriting.
func emitIntentBlock(sse *sseWriter, blockID string, step PlanStep) {
	payload := map[string]any{
		"kind": "block",
		"type": "orchestrate_intent",
		"id":   blockID,
		"text": fmt.Sprintf("Step %d: %s", step.ID, step.Title),
	}
	if r := strings.TrimSpace(step.Intent); r != "" {
		payload["reason"] = r
	}
	sse.Send(payload)
}

// logPromptComposition reports the estimated token split of one turn's prompt.
//
// Rough by design: EstimateTokens is chars/4, and the point is proportion, not
// precision — which of three things is big enough to be the reason a turn is
// slow. The tool catalog is measured from its serialized form because that is
// what the provider is sent; name and description alone would understate a
// catalog whose weight is in its parameter schemas.
func logPromptComposition(kind, sessID, sys string, tools []AgentToolDef, msgs []Message) {
	toolTokens := 0
	for _, td := range tools {
		if b, err := json.Marshal(td.Tool); err == nil {
			toolTokens += len(b) / 4
		}
	}
	sysTokens := EstimateTokens(sys)
	// EstimateMessagesTokens counts tool-result bodies, which is where a
	// standing thread's weight actually lives — this line read "history 4635"
	// against a conversation the loop measured at 151k while it counted only
	// what was said.
	histTokens := EstimateMessagesTokens(msgs)
	Log("[orchestrate.orch] session=%s %s prompt~%d tokens = system %d + tools %d (%d) + history %d (%d msgs)",
		sessID, kind, sysTokens+toolTokens+histTokens, sysTokens, toolTokens, len(tools), histTokens, len(msgs))
}
