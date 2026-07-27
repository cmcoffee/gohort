package orchestrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// turnTelemetry accumulates per-round signals during a single
// orchestrator turn OR worker step and produces a one-line summary
// at exit. The goal is forensic: when a turn ends with "tool result
// then nothing" or a 90-rounds-used burn, the log line at exit tells
// you WHAT got called and HOW MANY times, so you can distinguish
// genuine multi-tool work from drift/fixation patterns.
//
// Cheap to populate (a few map writes per round); cheap to emit (one
// log line per loop exit). Not surfaced to the user, only to the log.
type turnTelemetry struct {
	rounds         int               // last observed round number
	contentChars   int               // total streamed content chars
	toolCallCounts map[string]int    // tool name → number of calls
	dupFingerprint map[string]int    // tool name + canonical args hash → count
	dupExemplar    map[string]string // fingerprint → human-readable example name (so dup row reads as "tool×N")
	lastTool       string            // last tool the LLM called
	lastToolErr    bool              // did the most recent tool round include any errors
	toolErrorCount int               // total tool errors across the turn
}

func newTurnTelemetry() *turnTelemetry {
	return &turnTelemetry{
		toolCallCounts: map[string]int{},
		dupFingerprint: map[string]int{},
		dupExemplar:    map[string]string{},
	}
}

// record absorbs one StepInfo from the agent loop. Idempotent on
// rounds — the per-round dispatch fires once per round, so rounds is
// simply tracked as the highest observed Round number.
func (tt *turnTelemetry) record(info StepInfo) {
	if info.Round > tt.rounds {
		tt.rounds = info.Round
	}
	tt.contentChars += len(info.Content)
	tt.toolErrorCount += info.ToolErrors
	tt.lastToolErr = info.ToolErrors > 0
	for _, tc := range info.ToolCalls {
		tt.toolCallCounts[tc.Name]++
		fp := tc.Name + "|" + canonicalArgsHash(tc.Args)
		tt.dupFingerprint[fp]++
		tt.dupExemplar[fp] = tc.Name
		tt.lastTool = tc.Name
	}
}

// summary returns a single human-readable line for the log. softCap
// and hardCap let the summary report the budget context (rounds_used
// reads against the soft cap when explorer didn't fire, hard cap
// when it did). exit is the caller's classification of WHY the loop
// ended (budget_exhausted / respond_directly / plan_set / ask_user /
// ctx_cancelled / stream_error / unknown).
func (tt *turnTelemetry) summary(label string, softCap, hardCap int, exit string) string {
	cap := softCap
	if hardCap > softCap {
		cap = hardCap
	}
	pct := 0
	if cap > 0 {
		pct = (tt.rounds * 100) / cap
	}
	parts := []string{
		fmt.Sprintf("[%s] turn complete:", label),
		fmt.Sprintf("rounds_used=%d/%d (%d%%)", tt.rounds, cap, pct),
		fmt.Sprintf("exit=%s", exit),
	}
	if tt.lastTool != "" {
		parts = append(parts, fmt.Sprintf("last_tool=%s", tt.lastTool))
	}
	if tt.contentChars > 0 {
		parts = append(parts, fmt.Sprintf("content_chars=%d", tt.contentChars))
	}
	if tt.toolErrorCount > 0 {
		parts = append(parts, fmt.Sprintf("tool_errors=%d", tt.toolErrorCount))
	}
	return strings.Join(parts, " ")
}

// toolCallSummary returns a second log line (or empty when no tools
// fired) listing the tool-call breakdown + duplicate-args count. The
// dup row is the productivity tell: 3 identical knowledge_search
// calls in one turn means the LLM is re-asking the same question
// instead of moving forward.
func (tt *turnTelemetry) toolCallSummary(label string) string {
	if len(tt.toolCallCounts) == 0 {
		return ""
	}
	// Tool-name × count, sorted by count desc then name asc.
	type row struct {
		name  string
		count int
	}
	rows := make([]row, 0, len(tt.toolCallCounts))
	for name, c := range tt.toolCallCounts {
		rows = append(rows, row{name, c})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].name < rows[j].name
	})
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.count > 1 {
			parts = append(parts, fmt.Sprintf("%s×%d", r.name, r.count))
		} else {
			parts = append(parts, r.name)
		}
	}
	// Dup-args entries — only those with count > 1 (so the same args
	// got passed more than once for the same tool). Names dedup
	// against the dupExemplar so a tool with 3 calls all-distinct
	// shows up in toolCallCounts but NOT here.
	dups := []string{}
	for fp, count := range tt.dupFingerprint {
		if count > 1 {
			dups = append(dups, fmt.Sprintf("%s×%d", tt.dupExemplar[fp], count))
		}
	}
	sort.Strings(dups)
	out := fmt.Sprintf("[%s] tool calls this turn: %s", label, strings.Join(parts, " "))
	if len(dups) > 0 {
		out += fmt.Sprintf(" (%d dup-args: %s)", len(dups), strings.Join(dups, ", "))
	}
	return out
}

// Churn thresholds — see churnDiag.
const (
	// A tool called with IDENTICAL args this many times has definitively
	// made no progress. Sits above the loop-guard's own repeat limits so
	// this only fires on churn the guards didn't already stop.
	churnDupLimit = 3
	// A single tool dominating a turn is only churn WITH errors: eight
	// distinct web_search calls is a research turn, eight failing
	// tool_def calls is a fight.
	churnCallLimit = 8
)

// churnDiag reports a turn that spent its tool calls without moving, as
// a (kind, detail) breadcrumb for the session diagnostics trail. Returns
// ok=false for a healthy turn.
//
// This is the detector for the shape that destroyed a live toolbox:
// seventeen tool_def calls in one turn, five of them with duplicate
// args, each reporting success, ending in a delete. The telemetry
// already computed every number needed to see it — the counts went to a
// log line the operator never reads, while the ⚠ trail that exists for
// exactly this said nothing. No new bookkeeping, just a second consumer
// of numbers already in hand.
//
// Deliberately tool-agnostic: it knows nothing about tool_def, so the
// same shape gets caught whichever tool a turn starts fighting with.
func (tt *turnTelemetry) churnDiag() (kind, detail string, ok bool) {
	if tt == nil {
		return "", "", false
	}
	// Same tool, same args, repeatedly — the strongest signal, so it wins
	// when both fire. Report the worst offender.
	worstDup, worstDupCount := "", 0
	for fp, count := range tt.dupFingerprint {
		if count > worstDupCount {
			worstDup, worstDupCount = tt.dupExemplar[fp], count
		}
	}
	if worstDupCount >= churnDupLimit {
		return "tool_churn", fmt.Sprintf(
			"%s was called %d times with identical arguments in one turn — the same call repeated without making progress. If it kept reporting success, the change may not be landing.",
			worstDup, worstDupCount), true
	}
	// High volume on one tool, with failures.
	if tt.toolErrorCount > 0 {
		worstTool, worstCount := "", 0
		for name, c := range tt.toolCallCounts {
			if c > worstCount {
				worstTool, worstCount = name, c
			}
		}
		if worstCount >= churnCallLimit {
			return "tool_churn", fmt.Sprintf(
				"%s was called %d times in one turn with %d tool error(s) — a sustained fight with one tool rather than progress.",
				worstTool, worstCount, tt.toolErrorCount), true
		}
	}
	return "", "", false
}

// canonicalArgsHash returns a short stable fingerprint for a tool
// call's args map. Used to detect "same tool, same args" repetitions
// across a turn (the plan_set fixation / re-search drift pattern).
// JSON-marshal collapses to a sorted-key form because Go's
// encoding/json sorts map keys; the SHA-256 prefix keeps the
// fingerprint short while remaining collision-resistant for
// per-turn use (no security implication).
func canonicalArgsHash(args map[string]any) string {
	if len(args) == 0 {
		return "empty"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "err"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// classifyOrchestratorExit derives a human-readable exit reason from
// the orchestrator's loop outcome. softCap is the round budget; the
// other args come from the captured-state machinery in runPlan.
func classifyOrchestratorExit(loopErr error, rounds, softCap int, hadDirectReply, hadQuestion, hadSteps bool, ctxCancelled bool) string {
	switch {
	case loopErr != nil && IsStreamIdleTimeoutError(loopErr):
		return "stream_idle_timeout"
	case loopErr != nil:
		return "loop_error"
	case hadSteps:
		return "plan_set"
	case hadDirectReply:
		return "respond_directly"
	case hadQuestion:
		return "ask_user"
	case ctxCancelled:
		return "ctx_cancelled"
	case rounds >= softCap:
		return "budget_exhausted"
	default:
		return "unknown"
	}
}

// classifyWorkerExit derives the exit reason for a worker-step loop.
// Simpler than the orchestrator since worker steps don't have a
// plan_set / respond_directly outcome — they either complete with a
// reply, error, or burn the budget.
func classifyWorkerExit(loopErr error, rounds, softCap int, outLen int) string {
	switch {
	case loopErr != nil && IsStreamIdleTimeoutError(loopErr):
		return "stream_idle_timeout"
	case loopErr != nil:
		return "loop_error"
	case rounds >= softCap:
		return "budget_exhausted"
	case outLen > 0:
		return "completed"
	default:
		return "empty_completion"
	}
}
