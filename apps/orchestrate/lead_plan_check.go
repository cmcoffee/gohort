package orchestrate

// The lead reads every plan the worker makes, on the consultant and hybrid
// levels (leadAsks): the worker runs the turn, and when it plans the work the
// lead reads the plan before it runs, and approves it or rewrites it.
//
// The worker deciding the work needs a plan is the signal, so no
// model has to judge its own task as hard, which a small one does badly: it
// either never asks or always does. Simple turns never reach the lead. The
// lead does what it is reliably good at, judging a plan from the request and
// the tools, and sees nothing else: no history, no tool schemas, so a check
// costs a few thousand tokens, as a consult does.
//
// A check that cannot be had (an error, a timeout, an answer that cannot be
// read, a rewrite that does not hold up) runs the worker's own plan. Checking
// must never cost the turn its work.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// leadPlanCheckRouteKey routes the check, its own stage so an
	// administrator sees it on the routing page and can move it.
	leadPlanCheckRouteKey = "app.orchestrate.lead_plan_check"
	// leadPlanCheckTimeout bounds the wait before the worker's plan runs anyway.
	leadPlanCheckTimeout = 90 * time.Second
)

func init() {
	RegisterRouteStage(RouteStage{
		Key:   leadPlanCheckRouteKey,
		Label: "Agents: Lead plan check (reads a big plan before it runs)",
		// One call per big plan, carrying the request, the plan and the tools
		// a step may use, and nothing else. NOT Private, so it can reach the
		// lead; it falls back to the worker when no lead is configured.
		Default: "lead",
		Group:   "Agents",
	})
}

// leadPlanCheckSysPrompt is the checker's whole brief.
const leadPlanCheckSysPrompt = `You check another assistant's plan before it runs. You see what the person asked, the plan (each step's title, what it aims to produce, the brief its worker gets, and the tools it may use), and the tools a step can be given. You do not see the conversation before this message, and you cannot run anything.

Approve the plan unless something matters:
- the steps do not cover the whole job: a part of the request no step does, or too few steps for what was asked;
- a file the person asked to receive that no step sends to them: making a file does not send it, attaching it does;
- a step that does not serve the request, or one step doing two jobs that should be two;
- steps in an order that cannot work, such as a step that needs a result a later step makes;
- a step given tools that cannot do its job, or missing one it needs;
- a brief too vague for a worker who sees nothing but the brief.

If the plan is fine, answer {"verdict":"go"}.
If it needs changing, answer {"verdict":"revise","reason":"one sentence on what was wrong","steps":[{"title":"...","intent":"...","worker_brief":"...","tools":["..."]}]} with the whole plan as it should run, using only the tools listed.
Reply with the JSON only.`

// leadChecksPlan reports whether this turn's plan of n steps goes to the lead:
// the agent works with the lead beside the worker (consultant or hybrid),
// there is a plan, the worker is driving (the lead already owns a turn on
// Lead orchestrates or Lead only), and the turn may reach the lead at all.
// Every plan, whatever its size: whether the steps are enough for the job is
// the question the lead is there to answer.
func (pr *planRun) leadChecksPlan(n int) bool {
	t := pr.t
	return t != nil && t.app != nil && n > 0 && leadAsks(RootDB, t.agent) &&
		!t.agent.LeadModel && !pr.leadPlansOn && !pr.mach.on && !isBuilderAgent(t.agent.ID) && t.leadPermitted()
}

// checkPlanWithLead is the plan that runs: the lead's rewrite when it sends
// one that holds up, the worker's own otherwise.
func (pr *planRun) checkPlanWithLead(steps []PlanStep) []PlanStep {
	if !pr.leadChecksPlan(len(steps)) {
		return steps
	}
	t := pr.t
	t.emitStatus("The lead is checking the plan.")
	started := time.Now()
	ctx, cancel := context.WithTimeout(t.ctx, leadPlanCheckTimeout)
	defer cancel()
	resp, err := t.app.LeadChat(ctx, []Message{{Role: "user", Content: pr.leadPlanCheckEvidence(steps)}},
		WithSystemPrompt(leadPlanCheckSysPrompt), WithJSONMode(), WithRouteKey(leadPlanCheckRouteKey))
	took := time.Since(started).Round(100 * time.Millisecond)
	if err != nil || resp == nil {
		Log("[lead-plan-check] agent=%s: no check after %s (%v); the worker's %d-step plan runs", t.agent.ID, took, err, len(steps))
		t.emitStatus("The lead could not check the plan; running it as written.")
		return steps
	}
	revised, reason, ok := pr.readPlanCheck(resp.Content)
	switch {
	case !ok:
		Log("[lead-plan-check] agent=%s: unreadable answer %q after %s; the worker's plan runs", t.agent.ID, truncateObs(resp.Content, 120), took)
		t.emitStatus("The lead's check could not be read; running the plan as written.")
		return steps
	case revised == nil:
		Log("[lead-plan-check] agent=%s: approved the worker's %d-step plan in %s", t.agent.ID, len(steps), took)
		t.emitStatus("Plan checked by the lead.")
		return steps
	}
	Log("[lead-plan-check] agent=%s: revised the plan in %s, %d step(s) to %d: %s", t.agent.ID, took, len(steps), len(revised), reason)
	t.emitStatus("Plan revised by the lead: " + reason)
	return revised
}

// readPlanCheck reads the lead's answer: (nil, "", true) to run the plan as it
// is, (steps, reason, true) for a rewrite that holds up, ok false for an
// answer that cannot be acted on, including a rewrite that does not hold up.
func (pr *planRun) readPlanCheck(raw string) ([]PlanStep, string, bool) {
	var out struct {
		Verdict string            `json:"verdict"`
		Reason  string            `json:"reason"`
		Steps   []json.RawMessage `json:"steps"`
	}
	if DecodeJSON(raw, &out) != nil {
		return nil, "", false
	}
	switch strings.ToLower(strings.TrimSpace(out.Verdict)) {
	case "go":
		return nil, "", true
	case "revise":
	default:
		return nil, "", false
	}
	var items []any
	for _, s := range out.Steps {
		var m map[string]any
		if json.Unmarshal(s, &m) == nil {
			items = append(items, m)
		}
	}
	steps := parsePlanSteps(items, pr.maxSteps)
	if len(steps) < 2 || looksLikeVacuousPlan(steps) != "" {
		return nil, "", false
	}
	// Only the tools a step could be given: a name the lead made up would
	// leave its step with nothing to run.
	known := map[string]bool{}
	for _, td := range pr.allTools {
		known[td.Tool.Name] = true
	}
	for i := range steps {
		var kept []string
		for _, name := range steps[i].Tools {
			if known[name] {
				kept = append(kept, name)
			}
		}
		steps[i].Tools = kept
	}
	reason := strings.TrimSpace(out.Reason)
	if reason == "" {
		reason = "it changed the steps"
	}
	return steps, reason, true
}

// leadPlanCheckEvidence is what the lead reads: the request, the plan, and the
// tools a step may be given, by name and first sentence.
func (pr *planRun) leadPlanCheckEvidence(steps []PlanStep) string {
	var b strings.Builder
	b.WriteString("WHAT THEY ASKED\n")
	b.WriteString(strings.TrimSpace(lastUserText(pr.msgs)))
	b.WriteString("\n\nTHE PLAN\n")
	for i, s := range steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, s.Title)
		if s.Intent != "" {
			fmt.Fprintf(&b, "   aims to: %s\n", s.Intent)
		}
		if s.WorkerBrief != "" {
			fmt.Fprintf(&b, "   brief: %s\n", s.WorkerBrief)
		}
		if len(s.Tools) > 0 {
			fmt.Fprintf(&b, "   tools: %s\n", strings.Join(s.Tools, ", "))
		}
	}
	fmt.Fprintf(&b, "\nTOOLS A STEP CAN BE GIVEN (at most %d steps)\n", pr.maxSteps)
	names := make([]string, 0, len(pr.allTools))
	desc := map[string]string{}
	for _, td := range pr.allTools {
		names = append(names, td.Tool.Name)
		desc[td.Tool.Name] = firstSentence(td.Tool.Description)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "- %s: %s\n", n, desc[n])
	}
	return b.String()
}
