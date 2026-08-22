// The tool group an agent drives a work plan with.
//
// Six tools, and the shape of them is the argument: commit to a list, say which
// step you are on, record what that step found or why it could not be done,
// revise when reality contradicts the plan, and take the gap report before
// answering. The last one is why this exists rather than a checklist card: a
// step that was never finished has to reach the final answer as a stated gap,
// not evaporate.
//
// Host-agnostic on purpose. It does not know what a step's work IS (a probe, a
// dispatch, a search), where the checklist is drawn, or how the plan is stored.
// Two knobs carry a host's own words; everything else is the same wherever it
// mounts, so an agent that learns the group in one place knows it in the next.

package core

import (
	"fmt"
	"strings"
)

// WorkPlanChange is one movement of the plan, handed to the host to render and
// persist. Steps is a snapshot, safe to hold.
type WorkPlanChange struct {
	// PlanID identifies this plan INSTANCE, so a surface that draws a card per
	// plan updates the right one instead of rewriting the previous plan's.
	PlanID string
	// Kind is "set" for the initial commitment, "step" for everything after.
	Kind string
	// Reason is set on a revision — why the plan changed.
	Reason string
	Steps  []WorkPlanStep
}

// WorkPlanToolSpec is what a host supplies to mount the group.
type WorkPlanToolSpec struct {
	// Plan is the state the tools operate on. Required; the host owns it, which
	// is what lets the host persist it.
	Plan *WorkPlan
	// PlanID rides every change. A host that draws one card per plan should mint
	// a fresh one per plan instance rather than reusing a session id.
	PlanID string
	// OnChange is called after every mutation, with a snapshot. Optional: a run
	// with nothing watching still tracks its plan correctly.
	OnChange func(WorkPlanChange)
	// SetDescription replaces the description of the set tool. A host with tuned
	// copy (how many steps this kind of work usually needs, what a good step
	// looks like here) passes its own; empty takes the generic one.
	SetDescription string
	// WorkHint is the sentence telling the model what to actually DO once a step
	// is marked in progress — "Delegate probe(s) to investigate", "Run the tools
	// for that step". Empty takes a generic line.
	WorkHint string
}

// WorkPlanToolSet is the mounted group. Held as a struct rather than a slice so a
// host can mount a subset, or reach the plan the tools share.
type WorkPlanToolSet struct {
	ID       string
	Plan     *WorkPlan
	Set      AgentToolDef
	Start    AgentToolDef
	Findings AgentToolDef
	Blocked  AgentToolDef
	Revise   AgentToolDef
	Gaps     AgentToolDef
}

// All returns the group in catalog order.
func (p WorkPlanToolSet) All() []AgentToolDef {
	return []AgentToolDef{p.Set, p.Start, p.Findings, p.Blocked, p.Revise, p.Gaps}
}

// Pending counts steps still to do; 0 when there is no plan.
func (p WorkPlanToolSet) Pending() int {
	if p.Plan == nil || !p.Plan.IsSet() {
		return 0
	}
	return p.Plan.Pending()
}

const workPlanGenericSetDescription = "Commit to a multi-step plan, and track it. Use when the work needs SEVERAL distinct results that build on each other — not for something a single call settles. " +
	"Each step has a short title (5-10 words) and a what_to_find description (1-3 sentences) saying what success looks like for that step. Order by dependency: what has to be known first goes first. " +
	"Typically 3-8 steps for a focused piece of work. Once set, work the steps with mark_step_in_progress / record_step_findings / mark_step_blocked, and call report_gaps before your final answer so unresolved steps are stated rather than quietly dropped. " +
	"A plan is a commitment the user can see — set one when the work deserves it, skip it when one call answers the question."

const workPlanGenericWorkHint = "Do the work for that step now."

// WorkPlanTools mounts the group for one plan.
func WorkPlanTools(spec WorkPlanToolSpec) WorkPlanToolSet {
	plan := spec.Plan
	if plan == nil {
		plan = &WorkPlan{}
	}
	planID := spec.PlanID
	work := strings.TrimSpace(spec.WorkHint)
	if work == "" {
		work = workPlanGenericWorkHint
	}
	changed := func(kind, reason string) {
		if spec.OnChange == nil {
			return
		}
		spec.OnChange(WorkPlanChange{PlanID: planID, Kind: kind, Reason: reason, Steps: plan.Snapshot()})
	}
	setDesc := strings.TrimSpace(spec.SetDescription)
	if setDesc == "" {
		setDesc = workPlanGenericSetDescription
	}

	// None of the six declares capabilities, and that is load-bearing rather
	// than an omission: an annotated read-only tool is CACHEABLE within a run
	// (WrapToolsWithRunCache), and a cached plan call would report success
	// without having moved the plan. Unannotated is never cached.
	setTool := AgentToolDef{
		Tool: Tool{
			Name:        "set_plan",
			Description: setDesc,
			Parameters: map[string]ToolParam{
				"steps": {
					Type:        "array",
					Description: "Ordered list of plan steps. Each item must be an object with 'title' (string) and 'what_to_find' (string).",
				},
			},
			Required: []string{"steps"},
		},
		Handler: func(args map[string]any) (string, error) {
			if plan.IsSet() {
				return "[PLAN ALREADY SET] Work the steps you committed to: mark_step_in_progress / record_step_findings / mark_step_blocked, or revise_plan to change what is left.", nil
			}
			titles, finds, err := workPlanStepsArg(args["steps"], "steps")
			if err != nil {
				return "", err
			}
			if err := plan.SetSteps(titles, finds); err != nil {
				return "", err
			}
			changed("set", "")
			return fmt.Sprintf("WorkPlan set with %d steps. Begin step 1: mark_step_in_progress with id=1, do that step's work, then record_step_findings or mark_step_blocked.", len(titles)), nil
		},
	}

	startTool := AgentToolDef{
		Tool: Tool{
			Name:        "mark_step_in_progress",
			Description: "Mark a plan step as the one you are working on now. It shows as the active step in the user-visible checklist. Call it before you start that step's work.",
			Parameters: map[string]ToolParam{
				"step_id": {Type: "integer", Description: "The step ID to mark in_progress (from set_plan)."},
			},
			Required: []string{"step_id"},
		},
		Handler: func(args map[string]any) (string, error) {
			if !plan.IsSet() {
				return "[NO PLAN] Call set_plan first.", nil
			}
			stepID := IntArg(args, "step_id")
			if err := plan.SetStatus(stepID, WorkStepInProgress); err != nil {
				return "", err
			}
			changed("step", "")
			return fmt.Sprintf("Step %d marked in_progress. %s", stepID, work), nil
		},
	}

	findingsTool := AgentToolDef{
		Tool: Tool{
			Name:        "record_step_findings",
			Description: "Attach findings to a plan step and mark it done. Findings are a 1-3 sentence summary of what was learned for this step — your synthesis, NOT the raw output you got back. Call it once that step's work has produced something worth keeping.",
			Parameters: map[string]ToolParam{
				"step_id":  {Type: "integer", Description: "The step ID to record findings for."},
				"findings": {Type: "string", Description: "1-3 sentence summary of what was learned for this step."},
			},
			Required: []string{"step_id", "findings"},
		},
		Handler: func(args map[string]any) (string, error) {
			if !plan.IsSet() {
				return "[NO PLAN] Call set_plan first.", nil
			}
			stepID := IntArg(args, "step_id")
			findings, _ := args["findings"].(string)
			if strings.TrimSpace(findings) == "" {
				return "", fmt.Errorf("findings is required")
			}
			if err := plan.RecordFindings(stepID, strings.TrimSpace(findings)); err != nil {
				return "", err
			}
			changed("step", "")
			return fmt.Sprintf("Step %d marked done. Move to the next pending step, OR if all pending steps are done, revisit any blocked steps now — what you learned from the other steps often unblocks them.", stepID), nil
		},
	}

	blockedTool := AgentToolDef{
		Tool: Tool{
			Name: "mark_step_blocked",
			Description: "Mark a plan step as a GENUINE dead-end — it cannot be completed however many rounds you have: no access, a required tool is missing, the thing it needs is unreachable, or every reasonable angle is exhausted. " +
				"Do NOT use it to hide difficulty on a first attempt (try a couple of angles first), and NEVER because you are low on rounds or 'out of time' — that is not a blocker. An unfinished step should be left pending, not blocked; a slow step is faster revisited after the rest of the plan. " +
				"The reason appears in the final answer's gap section, so it has to describe a real obstacle.",
			Parameters: map[string]ToolParam{
				"step_id": {Type: "integer", Description: "The step ID to mark blocked."},
				"reason":  {Type: "string", Description: "Why the step could not be completed (a real obstacle, e.g. 'no read access to that host', 'the API returns 403 for this account')."},
			},
			Required: []string{"step_id", "reason"},
		},
		Handler: func(args map[string]any) (string, error) {
			if !plan.IsSet() {
				return "[NO PLAN] Call set_plan first.", nil
			}
			stepID := IntArg(args, "step_id")
			reason, _ := args["reason"].(string)
			if strings.TrimSpace(reason) == "" {
				return "", fmt.Errorf("reason is required")
			}
			if err := plan.MarkBlocked(stepID, strings.TrimSpace(reason)); err != nil {
				return "", err
			}
			changed("step", "")
			return fmt.Sprintf("Step %d marked blocked: %s. Move to the next pending step.", stepID, strings.TrimSpace(reason)), nil
		},
	}

	reviseTool := AgentToolDef{
		Tool: Tool{
			Name: "revise_plan",
			Description: fmt.Sprintf(
				"Revise the plan when what you found reveals something you could not have known to plan for. Three operations, all optional and combinable: add (new steps appended with fresh IDs), remove (drop PENDING steps that are no longer relevant — done/blocked/in_progress steps are durable history and are refused), reorder (a full new ordering of the remaining step IDs). Capped at %d revisions — use it deliberately, not reflexively. The plan is your contract; revise it when reality contradicts the contract, not because you would write it differently in hindsight.",
				WorkPlanRevisionLimit,
			),
			Parameters: map[string]ToolParam{
				"add":     {Type: "array", Description: "Optional. New steps to append. Each item is an object with 'title' and 'what_to_find', same shape as set_plan."},
				"remove":  {Type: "array", Description: "Optional. Step IDs (integers) to drop. Only pending steps can be removed."},
				"reorder": {Type: "array", Description: "Optional. Full new ordering of step IDs (integers). Must be a permutation of all remaining step IDs after add+remove are applied."},
				"reason":  {Type: "string", Description: "Brief one-sentence explanation of why this revision is needed. Shown beside the plan changes."},
			},
			Required: []string{"reason"},
		},
		Handler: func(args map[string]any) (string, error) {
			if !plan.IsSet() {
				return "[NO PLAN] Call set_plan first.", nil
			}
			reason, _ := args["reason"].(string)
			if strings.TrimSpace(reason) == "" {
				return "", fmt.Errorf("reason is required to document the revision")
			}
			reason = strings.TrimSpace(reason)
			count, atCap := plan.IncrRevision()
			if atCap && count > WorkPlanRevisionLimit {
				return fmt.Sprintf("[REVISION LIMIT REACHED] You have already revised the plan %d times, the maximum. Work the existing plan to completion; if a step you wanted is missing, mark the related steps blocked and say so in your final answer.", WorkPlanRevisionLimit), nil
			}
			var feedback strings.Builder
			fmt.Fprintf(&feedback, "Revision %d/%d: %s\n", count, WorkPlanRevisionLimit, reason)
			// Remove first so a reorder operates on the final set, then add
			// (which appends), then reorder.
			if ids := workPlanIntsArg(args["remove"]); len(ids) > 0 {
				removed, refused := plan.RemoveSteps(ids)
				if len(removed) > 0 {
					fmt.Fprintf(&feedback, "Removed steps: %v\n", removed)
				}
				if len(refused) > 0 {
					fmt.Fprintf(&feedback, "Refused to remove (status not pending): %v\n", refused)
				}
			}
			if raw, ok := args["add"].([]any); ok && len(raw) > 0 {
				titles, finds, err := workPlanStepsArg(raw, "add")
				if err != nil {
					return "", err
				}
				added, err := plan.AddSteps(titles, finds)
				if err != nil {
					return "", err
				}
				fmt.Fprintf(&feedback, "Added steps: %v\n", added)
			}
			if order := workPlanIntsArg(args["reorder"]); len(order) > 0 {
				if err := plan.ReorderSteps(order); err != nil {
					return "", err
				}
				feedback.WriteString("Reordered.\n")
			}
			changed("step", reason)
			return strings.TrimSpace(feedback.String()), nil
		},
	}

	gapsTool := AgentToolDef{
		Tool: Tool{
			Name: "report_gaps",
			Description: "REQUIRED before you write your final answer. Returns every plan step that is blocked or was never completed, with the reasons. You MUST fold that into the answer under a clearly labelled section saying what you could not determine, so the user sees the gaps rather than getting a confident report that quietly omits them. " +
				"The answer is trusted because you are honest about what you could not see.",
			Parameters: map[string]ToolParam{},
		},
		Handler: func(args map[string]any) (string, error) {
			if !plan.IsSet() {
				return "[NO PLAN] Call set_plan first.", nil
			}
			gaps := plan.MarkGapsReported()
			changed("step", "")
			if len(gaps.Blocked) == 0 && len(gaps.Skipped) == 0 {
				return "No gaps. Every plan step was completed with findings. Write your final answer now — no 'what I could not determine' section needed.", nil
			}
			var b strings.Builder
			b.WriteString("Gap report — fold the following into a clearly labelled 'what I could not determine' section of your final answer:\n\n")
			if len(gaps.Blocked) > 0 {
				b.WriteString("Blocked:\n")
				for _, g := range gaps.Blocked {
					fmt.Fprintf(&b, "  - %s (step %d): %s\n", g.Title, g.ID, g.Reason)
				}
			}
			if len(gaps.Skipped) > 0 {
				b.WriteString("\nNever completed:\n")
				for _, g := range gaps.Skipped {
					fmt.Fprintf(&b, "  - %s (step %d): %s\n", g.Title, g.ID, g.Reason)
				}
			}
			return strings.TrimSpace(b.String()), nil
		},
	}

	return WorkPlanToolSet{
		ID: planID, Plan: plan,
		Set: setTool, Start: startTool, Findings: findingsTool,
		Blocked: blockedTool, Revise: reviseTool, Gaps: gapsTool,
	}
}

// workPlanStepsArg decodes the {title, what_to_find} array both set_plan and
// revise_plan(add) take. Refuses a step missing either half rather than
// accepting a titled step nobody can tell was finished.
func workPlanStepsArg(v any, field string) (titles, finds []string, err error) {
	raw, ok := v.([]any)
	if !ok || len(raw) == 0 {
		return nil, nil, fmt.Errorf("%s must be a non-empty array of {title, what_to_find}", field)
	}
	for i, s := range raw {
		m, ok := s.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("%s[%d]: must be an object with 'title' and 'what_to_find'", field, i+1)
		}
		title, _ := m["title"].(string)
		find, _ := m["what_to_find"].(string)
		if strings.TrimSpace(title) == "" {
			return nil, nil, fmt.Errorf("%s[%d]: 'title' is required", field, i+1)
		}
		if strings.TrimSpace(find) == "" {
			return nil, nil, fmt.Errorf("%s[%d]: 'what_to_find' is required", field, i+1)
		}
		titles = append(titles, strings.TrimSpace(title))
		finds = append(finds, strings.TrimSpace(find))
	}
	return titles, finds, nil
}

// workPlanIntsArg reads an array of step IDs, tolerating the float/string forms an
// LLM's JSON can arrive in. Values it cannot read are skipped rather than
// failing the call: a reorder naming one unreadable id is caught by
// ReorderSteps, which can say what is wrong with the ORDER.
func workPlanIntsArg(v any) []int {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, item := range raw {
		switch n := item.(type) {
		case float64:
			out = append(out, int(n))
		case int:
			out = append(out, n)
		case string:
			var parsed int
			if _, err := fmt.Sscanf(n, "%d", &parsed); err == nil {
				out = append(out, parsed)
			}
		}
	}
	return out
}
