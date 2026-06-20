// Post-plan structural-gap review. Scans the worker-step outputs for
// failure modes that a re-read would catch but a single decompose
// pass would miss, then runs targeted fills before synthesis. Gated
// by AgentRecord.GapCheck; off by default. Best for research-flavored
// agents.
//
// Failure modes the detector hunts for:
//
//   - Abstract sections — claims framed in capability language ("can
//     disrupt", "may mitigate") with no named country, program,
//     institution, person, or historical case as evidence.
//   - Evidence asymmetry — one side of an argument is grounded in
//     named, quantitative facts; the opposing side is hand-wavy.
//   - Mechanism gaps — the report argues a conclusion but never
//     names the specific program/policy/court ruling/incident that
//     shows the mechanism in action.
//
// The detector LLM is asked to return 0-N gap questions, each in
// "## Gap N\n<question>\n" shape. Empty/no-gap replies short-circuit
// the pass. Detected questions become extra PlanStep entries appended
// to the same plan, executed by the same runWorkerStep machinery, and
// folded into synthesis alongside the originals.

package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterTunable(TunableSpec{Key: "tune_gap_check_timeout", Category: "Timeouts", Label: "Gap-check timeout", Help: "Caps the structural-gap detection LLM call.", Kind: KindSeconds, Default: 90, Min: 15, Max: 600})
	RegisterTunable(TunableSpec{Key: "tune_max_gaps_per_check", Category: "Limits", Label: "Max gaps per check", Help: "Max extra worker steps the gap pass may inject.", Kind: KindInt, Default: 3, Min: 1, Max: 15})
}

// gapCheckTimeout caps the gap-detection LLM call. Pays its way only
// when the worker output is genuinely hollow; tight cap keeps a slow
// detection round from blowing the user's perceived latency.
func gapCheckTimeout() time.Duration { return TuneDuration("tune_gap_check_timeout") }

// maxGapsPerCheck limits how many extra worker steps the gap pass may
// inject. Belt-and-suspenders on top of the prompt's "1-3" guidance —
// a hallucinating detector that returns 10 gaps would otherwise turn
// one turn into a marathon.
func maxGapsPerCheck() int { return TuneInt("tune_max_gaps_per_check") }

// runGapCheck inspects the completed plan steps for structural gaps
// and returns 0-N additional PlanStep entries to execute before
// synthesis. Steps are numbered starting at nextID so the IDs stay
// monotone with the existing plan. Failures inside the detector are
// logged + swallowed — gap-check is an optimization, not part of the
// reply contract.
func (t *chatTurn) runGapCheck(userMsg string, steps []PlanStep, nextID int) []PlanStep {
	if t == nil || t.app == nil || t.app.LLM == nil {
		return nil
	}
	if len(steps) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(t.ctx, gapCheckTimeout())
	defer cancel()

	prompt := buildGapCheckPrompt(userMsg, steps)
	sys := gapCheckSystemPrompt()

	resp, err := t.app.LLM.Chat(ctx,
		[]Message{{Role: "user", Content: prompt}},
		WithSystemPrompt(sys),
		WithRouteKey("app.orchestrate.gap_check"),
	)
	if err != nil {
		Log("[orchestrate.gap_check] LLM error for agent=%s: %v", t.agent.ID, err)
		return nil
	}
	if resp == nil {
		return nil
	}

	questions := parseGapQuestions(resp.Content)
	if len(questions) == 0 {
		return nil
	}
	if len(questions) > maxGapsPerCheck() {
		questions = questions[:maxGapsPerCheck()]
	}

	out := make([]PlanStep, 0, len(questions))
	for i, q := range questions {
		out = append(out, PlanStep{
			ID:     nextID + i,
			Title:  q,
			Intent: "Fill a gap detected after the initial plan finished.",
			WorkerBrief: fmt.Sprintf(
				"Focused gap-fill. Answer this question concretely, naming specific examples (named programs, dates, numbers, sources): %s\n\n"+
					"Prefer a single targeted web_search with a query that would surface a named case. If you can't find evidence, say so explicitly — do not generalize.",
				q,
			),
			Status: StepPending,
		})
	}
	return out
}

// buildGapCheckPrompt assembles the detector's user prompt: the
// user's original question plus a compact view of each completed
// step's findings (or output if findings is empty). Structured so
// the LLM has the right signal to spot abstract-vs-named claims,
// evidence asymmetries, and mechanism gaps.
func buildGapCheckPrompt(userMsg string, steps []PlanStep) string {
	var b strings.Builder
	b.WriteString("## Original user question\n")
	b.WriteString(strings.TrimSpace(userMsg))
	b.WriteString("\n\n## What the worker steps produced\n\n")
	for _, s := range steps {
		fmt.Fprintf(&b, "### Step %d: %s\n", s.ID, s.Title)
		out := strings.TrimSpace(s.Output)
		if out == "" {
			out = strings.TrimSpace(s.Findings)
		}
		if out == "" {
			out = "(no output)"
		}
		// Cap each step's contribution so a sprawling output doesn't
		// drown the detector's view of structure. 1200 chars is enough
		// to spot abstract phrasing and named-vs-unnamed asymmetries.
		if len(out) > 1200 {
			out = out[:1200] + "\n[truncated]"
		}
		b.WriteString(out)
		b.WriteString("\n\n")
	}
	b.WriteString("---\n\n")
	b.WriteString("## Your task\n\n")
	b.WriteString("Scan the worker output above for STRUCTURAL gaps the synthesis pass will inherit if you don't catch them now. Failure modes:\n\n")
	b.WriteString("1. **Abstract sections** — claims framed only in capability/tendency language (\"can disrupt\", \"may mitigate\", \"tends to create\") without a named country, program, institution, person, or historical case as evidence. A targeted question asking for a specific named example would make it concrete.\n")
	b.WriteString("2. **Evidence asymmetry** — one side of an argument has named, quantitative facts (e.g. \"Sweden 33% employment drop\") while the opposing side has only abstract claims. A question that would produce named evidence for the weaker side.\n")
	b.WriteString("3. **Mechanism gaps** — the output argues a conclusion but never names HOW it happens — no specific program, policy, court ruling, or historical incident. A question asking for the mechanism in action.\n\n")
	b.WriteString("Output format:\n")
	b.WriteString("- If everything looks solid, reply with the single word NONE.\n")
	b.WriteString(fmt.Sprintf("- Otherwise, reply with %d or fewer gap questions, one per line, each starting with '- '. Each question must be self-contained (no \"this section\" / \"the above\") and phrased so a worker can answer it with one focused web_search.\n", maxGapsPerCheck()))
	b.WriteString("\nBe stingy. A turn that's already concrete and well-cited gets NONE — don't manufacture gaps to look thorough.")
	return b.String()
}

func gapCheckSystemPrompt() string {
	return "You are a structural-gap detector for a research pipeline. Your only job is to identify weaknesses in the assembled findings that targeted follow-up questions could fix. You do NOT answer the user's question, you do NOT critique style, you do NOT suggest reorganizations. You produce 0-3 gap questions (or NONE) and stop. Be conservative — most outputs need NONE."
}

// parseGapQuestions extracts bullet-prefixed questions from the
// detector's reply. Tolerates the same bullet styles as the memory
// consolidator (-, *, •, "1." / "1)"). Skips the NONE sentinel.
// Lines without a question shape (no '?' and no clear interrogative
// stem) are dropped to filter detector noise.
func parseGapQuestions(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "NONE") {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		ln = stripBulletPrefix(ln)
		if ln == "" || strings.EqualFold(ln, "NONE") {
			continue
		}
		// Light shape filter: keep lines with a '?' or that start
		// with a common interrogative stem. Detector sometimes
		// editorializes; we want questions only.
		if !looksLikeQuestion(ln) {
			continue
		}
		out = append(out, ln)
	}
	return out
}

func looksLikeQuestion(s string) bool {
	if strings.Contains(s, "?") {
		return true
	}
	lower := strings.ToLower(s)
	for _, stem := range []string{"what ", "which ", "who ", "when ", "where ", "why ", "how ", "name ", "list "} {
		if strings.HasPrefix(lower, stem) {
			return true
		}
	}
	return false
}
