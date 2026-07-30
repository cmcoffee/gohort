package servitor

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// planToolSet is the investigation-plan tool group: the checklist the
// investigator commits to, plus its step lifecycle. Extracted from the Map
// branch so CHAT can build the same group — a follow-up question that needs
// real discovery gets to run a tracked investigation instead of firing one-off
// probes until it gives up. See buildPlanTools.
type planToolSet struct {
	Plan     *Plan
	Set      AgentToolDef
	Start    AgentToolDef
	Findings AgentToolDef
	Blocked  AgentToolDef
	Revise   AgentToolDef
	Gaps     AgentToolDef
}

// All returns the group in catalog order.
func (p planToolSet) All() []AgentToolDef {
	return []AgentToolDef{p.Set, p.Start, p.Findings, p.Blocked, p.Revise, p.Gaps}
}

// Pending counts steps still to do — the signal for "grant another round
// budget" (a capped pass with work left is unfinished, not stuck).
func (p planToolSet) Pending() int {
	if p.Plan == nil || !p.Plan.IsSet() {
		return 0
	}
	n := 0
	for _, s := range p.Plan.Snapshot() {
		if s.Status == PlanStepPending || s.Status == PlanStepInProgress {
			n++
		}
	}
	return n
}

// buildPlanTools constructs the plan group for one session.
//
// required=true keeps the Map behavior: set_plan is the mandatory first call,
// because mapping a system IS the plan. In CHAT it is optional — most questions
// are one probe, and forcing a 5-step plan onto "what port is nginx on" would
// be a tax on every follow-up. There the plan is what the investigator reaches
// for when a question turns out to be bigger than it looked.
func buildPlanTools(id string, required bool) planToolSet {
	// plan: session-scoped investigation plan. The investigator's
	// system prompt requires set_plan as the first tool call;
	// subsequent step lifecycle tools (mark_step_in_progress,
	// record_step_findings, mark_step_blocked) operate on this
	// state. Plan events stream to the UI for the left-pane
	// checklist render.
	plan := &Plan{}
	set_plan_tool := AgentToolDef{
		Tool: Tool{
			Name: "set_plan",
			Description: "REQUIRED FIRST CALL — emit a structured investigation plan before any other tool. " +
				"Each step has a short title (5–10 words) and a what_to_find description (1–3 sentences) explaining what success looks like for that step. " +
				"Order steps by dependency: foundation/discovery first, then deeper investigation that builds on it. " +
				"Typically 5–12 steps for a generic system; scale up to 15+ for complex appliances (Kubernetes hosts, multi-tenant DB servers, hosts running many distinct services). " +
				"Err toward more steps with narrower scopes rather than fewer steps with sprawling scopes — narrow steps produce sharper findings and clearer gap reports. " +
				"You can revise step status as you go, but the initial plan is your contract — use revise_plan only if findings reveal a step you couldn't have known to include.",
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
				return "[PLAN ALREADY SET] You may revise specific steps via mark_step_in_progress / record_step_findings / mark_step_blocked. To replace the entire plan, that's not currently supported in this phase.", nil
			}
			raw, ok := args["steps"].([]any)
			if !ok || len(raw) == 0 {
				return "", fmt.Errorf("steps must be a non-empty array of {title, what_to_find}")
			}
			var titles, finds []string
			for i, s := range raw {
				m, ok := s.(map[string]any)
				if !ok {
					return "", fmt.Errorf("step %d: must be an object with 'title' and 'what_to_find'", i+1)
				}
				title, _ := m["title"].(string)
				find, _ := m["what_to_find"].(string)
				if strings.TrimSpace(title) == "" {
					return "", fmt.Errorf("step %d: 'title' is required", i+1)
				}
				if strings.TrimSpace(find) == "" {
					return "", fmt.Errorf("step %d: 'what_to_find' is required", i+1)
				}
				titles = append(titles, strings.TrimSpace(title))
				finds = append(finds, strings.TrimSpace(find))
			}
			if err := plan.SetSteps(titles, finds); err != nil {
				return "", err
			}
			emit(id, probeEvent{Kind: "plan_set", Plan: plan.Snapshot()})
			return fmt.Sprintf("Plan set with %d steps. Begin step 1: mark_step_in_progress with id=1, then probe to investigate, then record_step_findings or mark_step_blocked.", len(titles)), nil
		},
	}
	mark_step_in_progress_tool := AgentToolDef{
		Tool: Tool{
			Name:        "mark_step_in_progress",
			Description: "Mark a plan step as the one you're currently working on. Surfaces in the user-visible plan as the active step. Call before delegating probe(s) for that step.",
			Parameters: map[string]ToolParam{
				"step_id": {Type: "integer", Description: "The step ID to mark in_progress (from set_plan)."},
			},
			Required: []string{"step_id"},
		},
		Handler: func(args map[string]any) (string, error) {
			if !plan.IsSet() {
				return "[NO PLAN] Call set_plan first.", nil
			}
			stepID, _ := toInt(args["step_id"])
			if err := plan.SetStatus(stepID, PlanStepInProgress); err != nil {
				return "", err
			}
			emit(id, probeEvent{Kind: "plan_step", Plan: plan.Snapshot()})
			return fmt.Sprintf("Step %d marked in_progress. Delegate probe(s) to investigate.", stepID), nil
		},
	}
	record_step_findings_tool := AgentToolDef{
		Tool: Tool{
			Name:        "record_step_findings",
			Description: "Attach findings to a plan step and mark it done. Findings should be a 1–3 sentence summary of what was learned for this step (NOT the raw worker output — your synthesis of it). Call after the probe(s) for this step return useful results.",
			Parameters: map[string]ToolParam{
				"step_id":  {Type: "integer", Description: "The step ID to record findings for."},
				"findings": {Type: "string", Description: "1–3 sentence summary of what was learned for this step."},
			},
			Required: []string{"step_id", "findings"},
		},
		Handler: func(args map[string]any) (string, error) {
			if !plan.IsSet() {
				return "[NO PLAN] Call set_plan first.", nil
			}
			stepID, _ := toInt(args["step_id"])
			findings, _ := args["findings"].(string)
			if strings.TrimSpace(findings) == "" {
				return "", fmt.Errorf("findings is required")
			}
			if err := plan.RecordFindings(stepID, strings.TrimSpace(findings)); err != nil {
				return "", err
			}
			emit(id, probeEvent{Kind: "plan_step", Plan: plan.Snapshot()})
			return fmt.Sprintf("Step %d marked done. Move to the next pending step, OR if all pending steps are done, revisit any blocked steps now (call mark_step_in_progress on them — what you learned from the other steps often unblocks them).", stepID), nil
		},
	}
	mark_step_blocked_tool := AgentToolDef{
		Tool: Tool{
			Name:        "mark_step_blocked",
			Description: "Mark a plan step as a GENUINE dead-end — the step cannot be completed no matter how many more rounds you have: no access, a required tool is missing, the target service is unreachable, or every reasonable angle has been exhausted. Do NOT use this to hide difficulty on the first attempt (try a couple of angles first), and NEVER use it because you are low on rounds or 'out of time' — that is not a blocker. Unfinished steps should be left pending/in-progress, not blocked; the investigation automatically receives more rounds to finish pending work, and a slow step is faster revisited after you've worked the rest of the plan. The reason appears in the final report's gap section, so it must describe a real obstacle, never a time/round limit.",
			Parameters: map[string]ToolParam{
				"step_id": {Type: "integer", Description: "The step ID to mark blocked."},
				"reason":  {Type: "string", Description: "Why the step couldn't be completed (e.g. 'sudo not available', 'mysql user lacks SHOW GRANTS permission', 'logfile rotated, no archive)."},
			},
			Required: []string{"step_id", "reason"},
		},
		Handler: func(args map[string]any) (string, error) {
			if !plan.IsSet() {
				return "[NO PLAN] Call set_plan first.", nil
			}
			stepID, _ := toInt(args["step_id"])
			reason, _ := args["reason"].(string)
			if strings.TrimSpace(reason) == "" {
				return "", fmt.Errorf("reason is required")
			}
			if err := plan.MarkBlocked(stepID, strings.TrimSpace(reason)); err != nil {
				return "", err
			}
			emit(id, probeEvent{Kind: "plan_step", Plan: plan.Snapshot()})
			return fmt.Sprintf("Step %d marked blocked: %s. Move to the next pending step.", stepID, reason), nil
		},
	}
	revise_plan_tool := AgentToolDef{
		Tool: Tool{
			Name: "revise_plan",
			Description: fmt.Sprintf(
				"Revise the plan when findings reveal something you couldn't have known to plan for. Three operations, all optional and combinable: add (new steps appended with fresh IDs), remove (drop pending steps that are no longer relevant — done/blocked/in_progress steps are durable history and refused), reorder (full new ordering of remaining step IDs). Capped at %d revisions per session — use deliberately, not reflexively. The plan is your contract; revise only when reality contradicts the contract, not because you'd write it differently in hindsight.",
				PlanRevisionLimit,
			),
			Parameters: map[string]ToolParam{
				"add": {
					Type:        "array",
					Description: "Optional. New steps to append. Each item is an object with 'title' and 'what_to_find', same shape as set_plan.",
				},
				"remove": {
					Type:        "array",
					Description: "Optional. Step IDs (integers) to drop. Only pending steps can be removed.",
				},
				"reorder": {
					Type:        "array",
					Description: "Optional. Full new ordering of step IDs (integers). Must be a permutation of all remaining step IDs after add+remove are applied.",
				},
				"reason": {
					Type:        "string",
					Description: "Brief one-sentence explanation of why this revision is needed. Surfaced in the UI alongside the plan changes.",
				},
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
			count, atCap := plan.IncrRevision()
			if atCap && count > PlanRevisionLimit {
				return fmt.Sprintf("[REVISION LIMIT REACHED] You have already revised the plan %d times this session, the maximum allowed. Work the existing plan to completion; if a step you wanted is missing, mark related blocked steps and call out the gap in your final report.", PlanRevisionLimit), nil
			}
			var feedback strings.Builder
			fmt.Fprintf(&feedback, "Revision %d/%d: %s\n", count, PlanRevisionLimit, reason)
			// Process remove first so reorder operates on the
			// final set, then add (appends), then reorder.
			if rawRem, ok := args["remove"].([]any); ok && len(rawRem) > 0 {
				var ids []int
				for _, v := range rawRem {
					if n, ok := toInt(v); ok {
						ids = append(ids, n)
					}
				}
				if len(ids) > 0 {
					removed, refused, _ := plan.RemoveSteps(ids)
					if len(removed) > 0 {
						fmt.Fprintf(&feedback, "Removed steps: %v\n", removed)
					}
					if len(refused) > 0 {
						fmt.Fprintf(&feedback, "Refused to remove (status not pending): %v\n", refused)
					}
				}
			}
			if rawAdd, ok := args["add"].([]any); ok && len(rawAdd) > 0 {
				var titles, finds []string
				for i, s := range rawAdd {
					m, ok := s.(map[string]any)
					if !ok {
						return "", fmt.Errorf("add[%d]: must be an object with 'title' and 'what_to_find'", i)
					}
					title, _ := m["title"].(string)
					find, _ := m["what_to_find"].(string)
					if strings.TrimSpace(title) == "" || strings.TrimSpace(find) == "" {
						return "", fmt.Errorf("add[%d]: title and what_to_find are both required", i)
					}
					titles = append(titles, strings.TrimSpace(title))
					finds = append(finds, strings.TrimSpace(find))
				}
				added, err := plan.AddSteps(titles, finds)
				if err != nil {
					return "", err
				}
				fmt.Fprintf(&feedback, "Added steps: %v\n", added)
			}
			if rawOrd, ok := args["reorder"].([]any); ok && len(rawOrd) > 0 {
				var order []int
				for _, v := range rawOrd {
					if n, ok := toInt(v); ok {
						order = append(order, n)
					}
				}
				if err := plan.ReorderSteps(order); err != nil {
					return "", err
				}
				feedback.WriteString("Reordered.\n")
			}
			emit(id, probeEvent{Kind: "plan_step", Plan: plan.Snapshot(), Reason: reason})
			return strings.TrimSpace(feedback.String()), nil
		},
	}
	report_gaps_tool := AgentToolDef{
		Tool: Tool{
			Name:        "report_gaps",
			Description: "REQUIRED before you write your final answer. Returns a structured summary of every plan step that's blocked or never completed, plus the reasons. You MUST incorporate this into the final report under a clearly-labeled 'What I Couldn't Determine' section so the user sees the gaps explicitly rather than getting an overconfident report that quietly omits unverified things. The user trusts the report only if you're honest about what you couldn't see.",
			Parameters:  map[string]ToolParam{},
		},
		Handler: func(args map[string]any) (string, error) {
			if !plan.IsSet() {
				return "[NO PLAN] Call set_plan first.", nil
			}
			gaps := plan.MarkGapsReported()
			emit(id, probeEvent{Kind: "plan_step", Plan: plan.Snapshot()})
			if len(gaps.Blocked) == 0 && len(gaps.Skipped) == 0 {
				return "No gaps. Every plan step was completed with findings. Write your final answer now — no 'What I Couldn't Determine' section needed.", nil
			}
			var b strings.Builder
			b.WriteString("Gap report — incorporate the following into a 'What I Couldn't Determine' section in your final answer:\n\n")
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
	set := set_plan_tool
	if !required {
		set.Tool.Description = "Commit to a multi-step investigation plan, and track it. Use when answering needs SEVERAL distinct findings that build on each other — not for a question one probe can settle. " +
			"Each step has a short title (5-10 words) and a what_to_find description (1-3 sentences) saying what success looks like. Order by dependency: discovery first, then what builds on it. " +
			"Typically 3-8 steps for a focused question. Once set, work the steps with mark_step_in_progress / record_step_findings / mark_step_blocked, and call report_gaps before your final answer so unresolved steps are stated rather than quietly dropped. " +
			"A plan is a commitment the user can see in the checklist — set one when the work deserves it, skip it when a single probe answers the question."
	}
	return planToolSet{
		Plan: plan, Set: set, Start: mark_step_in_progress_tool,
		Findings: record_step_findings_tool, Blocked: mark_step_blocked_tool,
		Revise: revise_plan_tool, Gaps: report_gaps_tool,
	}
}
