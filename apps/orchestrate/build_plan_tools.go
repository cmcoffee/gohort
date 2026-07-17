// Build-plan UI surface for Builder. Two LLM-facing control tools
// emit + update the orchestrate_plan SSE block so the user sees a
// live checklist of Builder's multi-step authoring flow:
//
//   present_build_plan(steps[])
//     → Called at the end of Phase 2 alongside ask_user.
//     → Emits orchestrate_plan with all steps in "pending" status.
//     → Stores BuildPlanState on the session so subsequent updates
//       can target the same block id.
//
//   mark_step_done(step, summary)
//     → Called after each Phase 4 tool call completes successfully.
//     → Flips that step's status to "done" and writes the summary
//       as findings. Re-emits orchestrate_plan with the same id;
//       the renderer's onUpdate hook re-renders the card in place.
//
// Both tools are closure-bound to chatTurn so they can read t.sse
// + t.session without arg plumbing. State persists on ChatSession
// so the plan survives turn boundaries across the (typical 5-step)
// Phase 4 execution.

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// presentBuildPlanToolDef — Builder's "show the user the plan" tool.
// Pairs with ask_user in the same Phase 2 response: ask_user pauses
// the round for confirmation; present_build_plan paints the visual
// checklist so the user can read what they're approving.
func (t *chatTurn) presentBuildPlanToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "present_build_plan",
			Description: "Show the user a visible checklist of your build plan as a card. The PREFERRED Phase-2 shape is passing `plan` on ask_user (one call paints the checklist AND asks for approval) — reach for present_build_plan only when there's no question to ask (plan already approved) or to UPDATE the plan mid-build (user requested edits; re-call with the full step list — same id, replaces the visible card in place). The card renders with all steps \"pending\"; subsequent mark_step_done calls flip individual rows to \"done\" as you execute. Each step is {title, detail?}: title is the one-line summary (\"Create Reddit Researcher shell\"), detail is the brief tool-call info (\"create_agent\").",
			Parameters: map[string]ToolParam{
				"steps": {
					Type:        "array",
					Description: "Ordered list of step objects. Each step: {title: \"Create agent shell\", detail: \"create_agent\"} — keep titles short (1 line) and details to the tool/arg summary.",
					Items: &ToolParam{
						Type: "object",
						Properties: map[string]ToolParam{
							"title":  {Type: "string", Description: "One-line step title shown in the card. Example: \"Add search_reddit tool\"."},
							"detail": {Type: "string", Description: "Optional one-line detail under the title — typically the tool name + key args. Example: \"add_tool(api, no credential)\"."},
						},
						Required: []string{"title"},
					},
				},
			},
			Required: []string{"steps"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			if t.session == nil {
				return "", errors.New("present_build_plan requires an active session")
			}
			raw, ok := args["steps"]
			if !ok || raw == nil {
				return "", errors.New("steps is required")
			}
			data, err := json.Marshal(raw)
			if err != nil {
				return "", fmt.Errorf("steps marshal: %v", err)
			}
			var input []struct {
				Title  string `json:"title"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(data, &input); err != nil {
				return "", fmt.Errorf("steps shape: %v", err)
			}
			if len(input) == 0 {
				return "", errors.New("steps must include at least one entry")
			}
			// Build / overwrite the plan state. Stable id derived from
			// the session — same id across emissions so the renderer's
			// onUpdate replaces the card in place.
			id := "build-plan-" + t.session.ID
			steps := make([]BuildPlanStep, 0, len(input))
			for i, s := range input {
				title := strings.TrimSpace(s.Title)
				if title == "" {
					continue
				}
				steps = append(steps, BuildPlanStep{
					Number:     i + 1,
					Title:      title,
					WhatToFind: strings.TrimSpace(s.Detail),
					Status:     "pending",
				})
			}
			if len(steps) == 0 {
				return "", errors.New("steps had no usable titles")
			}
			t.session.BuildPlan = &BuildPlanState{ID: id, Steps: steps}
			emitBuildPlanBlock(t.sse, t.session.BuildPlan)
			// Grant the execution budget: lift the round cap to where we
			// are now + buildPlanRoundsPerStep per step, so building +
			// verifying the plan doesn't compete with whatever exploration
			// already cost. max() — never lowers an existing grant.
			if grant := t.currentRound + buildPlanRoundsPerStep*len(steps); grant > t.planBudgetCap {
				t.planBudgetCap = grant
			}
			Log("[orchestrate.build_plan] plan presented: %d step(s), round budget lifted to %d (at round %d)",
				len(steps), t.planBudgetCap, t.currentRound)
			return fmt.Sprintf("Build plan presented (%d step%s) — round budget extended to %d for execution. The user sees the checklist; each step will flip to ✓ as you call mark_step_done during execution.",
				len(steps), plural(len(steps)), t.planBudgetCap), nil
		},
	}
}

// markStepDoneToolDef — Builder's "step finished" tool. Called after
// each Phase 4 tool call completes so the UI checklist updates in
// real time. Same block id as present_build_plan → the card updates
// in place via the renderer's onUpdate hook.
func (t *chatTurn) markStepDoneToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "mark_step_done",
			Description: "Mark a step of the current build plan as done. Call IMMEDIATELY after each Phase 4 tool call's success result, in the same round, so the user sees the checklist update live. Pass step=N (1-indexed, matching present_build_plan's step numbering) and summary=\"one-line result\". Last step: pass summary like \"All N steps complete\".",
			Parameters: map[string]ToolParam{
				"step": {
					Type:        "integer",
					Description: "1-indexed step number from the plan you presented. Step 1 is the first call (typically create_agent).",
				},
				"summary": {
					Type:        "string",
					Description: "One-line result of this step. Example: \"AGENT_CREATED ok\" / \"Tool search_reddit attached\" / \"Verified via dispatch\".",
				},
			},
			Required: []string{"step", "summary"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			if t.session == nil || t.session.BuildPlan == nil {
				return "", errors.New("mark_step_done: no active build plan — call present_build_plan first")
			}
			step := intFromArgs(args, "step")
			if step < 1 {
				return "", errors.New("step must be >= 1 (1-indexed)")
			}
			summary := strings.TrimSpace(stringArg(args, "summary"))
			plan := t.session.BuildPlan
			idx := step - 1
			if idx >= len(plan.Steps) {
				return "", fmt.Errorf("step %d exceeds plan length %d", step, len(plan.Steps))
			}
			plan.Steps[idx].Status = "done"
			plan.Steps[idx].Findings = summary
			emitBuildPlanBlock(t.sse, plan)
			remaining := 0
			for _, s := range plan.Steps {
				if s.Status != "done" {
					remaining++
				}
			}
			if remaining == 0 {
				return fmt.Sprintf("Step %d marked done. All %d steps complete — end the turn with a one-line summary; no more tool calls.",
					step, len(plan.Steps)), nil
			}
			return fmt.Sprintf("Step %d marked done (%d remaining). Continue executing the next step.", step, remaining), nil
		},
	}
}

// buildPlanStepsFromArg normalizes an LLM-supplied plan array into
// []BuildPlanStep. Tolerates the loose shapes models produce
// (untyped maps, missing detail field, extra fields). Used by the
// extended ask_user tool when the `plan` parameter is provided.
func buildPlanStepsFromArg(raw any) []BuildPlanStep {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var input []struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		return nil
	}
	out := make([]BuildPlanStep, 0, len(input))
	for i, s := range input {
		title := sanitizePlanText(s.Title)
		if title == "" {
			continue
		}
		out = append(out, BuildPlanStep{
			Number:     i + 1,
			Title:      title,
			WhatToFind: sanitizePlanText(s.Detail),
			Status:     "pending",
		})
	}
	return out
}

// autoAdvanceBuildPlan flips the next pending step to "done" with
// the supplied summary, then re-emits the orchestrate_plan SSE block.
// Called from wrapped authoring-tool handlers so the user's visible
// checklist updates even when the LLM forgets to call mark_step_done
// in its response. No-op when there's no active plan, no pending
// steps, or the session lacks an SSE writer (background paths).
//
// Returns the step number that was advanced (1-indexed) or 0 when
// no advance happened. Callers can use the return value to log /
// nudge but otherwise treat as best-effort.
func (t *chatTurn) autoAdvanceBuildPlan(summary string) int {
	if t == nil || t.session == nil || t.session.BuildPlan == nil || t.sse == nil {
		return 0
	}
	plan := t.session.BuildPlan
	for i := range plan.Steps {
		if plan.Steps[i].Status == "done" {
			continue
		}
		plan.Steps[i].Status = "done"
		if summary != "" {
			plan.Steps[i].Findings = summary
		}
		emitBuildPlanBlock(t.sse, plan)
		return plan.Steps[i].Number
	}
	return 0
}

// markStepInProgressToolDef — Builder's "starting step N" tool.
// Servitor's investigator calls mark_step_in_progress before every
// probe so the UI shows which step the worker is currently driving;
// Builder mirrors the shape so the plan card surfaces the live focus
// during Phase 4 execution. Refuses if another step is already
// in_progress — keeping the lifecycle clean (one step at a time
// reflects the actual rhythm of plan_set's sequential worker pipeline).
func (t *chatTurn) markStepInProgressToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "mark_step_in_progress",
			Description: "Mark a step of the current build plan as in_progress before starting its worker. Pass step=N (1-indexed). Refuses when another step is already in_progress — call mark_step_done or mark_step_blocked on it first. Updates the visible plan card so the user sees which step is actively running.",
			Parameters: map[string]ToolParam{
				"step": {Type: "integer", Description: "1-indexed step number, matching present_build_plan's numbering."},
			},
			Required: []string{"step"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			if t.session == nil || t.session.BuildPlan == nil {
				return "", errors.New("mark_step_in_progress: no active build plan — call present_build_plan first")
			}
			step := intFromArgs(args, "step")
			if step < 1 {
				return "", errors.New("step must be >= 1 (1-indexed)")
			}
			plan := t.session.BuildPlan
			idx := step - 1
			if idx >= len(plan.Steps) {
				return "", fmt.Errorf("step %d exceeds plan length %d", step, len(plan.Steps))
			}
			// Refuse if another step is already in_progress —
			// lifecycle hygiene. The LLM must close the prior step
			// (done or blocked) before opening a new one.
			for i, s := range plan.Steps {
				if i == idx {
					continue
				}
				if s.Status == "in_progress" {
					return "", fmt.Errorf("step %d (%q) is already in_progress — call mark_step_done or mark_step_blocked on it before starting step %d", s.Number, s.Title, step)
				}
			}
			plan.Steps[idx].Status = "in_progress"
			emitBuildPlanBlock(t.sse, plan)
			return fmt.Sprintf("Step %d (%q) marked in_progress.", step, plan.Steps[idx].Title), nil
		},
	}
}

// markStepBlockedToolDef — Builder's "this step couldn't complete"
// tool. Records a one-line reason and flips status to blocked. The
// reason becomes part of the gap report at synthesis time, so the
// final reply can be explicit about what didn't work. Mirrors
// servitor's mark_step_blocked exactly.
func (t *chatTurn) markStepBlockedToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "mark_step_blocked",
			Description: "Mark a step of the current build plan as blocked when its worker couldn't complete it. Pass step=N (1-indexed) and reason=\"one-line description of what blocked it\" (e.g. \"credential not registered\", \"endpoint requires OAuth flow\", \"user declined\"). The reason surfaces in report_build_gaps and your final reply must address it. Use when proceeding wastes worker rounds; for re-tryable failures (wrong arg, model error) just have the next worker retry instead of blocking the step.",
			Parameters: map[string]ToolParam{
				"step":   {Type: "integer", Description: "1-indexed step number."},
				"reason": {Type: "string", Description: "One-line explanation of what blocked the step. Becomes part of the gap report."},
			},
			Required: []string{"step", "reason"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			if t.session == nil || t.session.BuildPlan == nil {
				return "", errors.New("mark_step_blocked: no active build plan — call present_build_plan first")
			}
			step := intFromArgs(args, "step")
			if step < 1 {
				return "", errors.New("step must be >= 1 (1-indexed)")
			}
			reason := strings.TrimSpace(stringArg(args, "reason"))
			if reason == "" {
				return "", errors.New("reason is required — describe what blocked the step in one line")
			}
			plan := t.session.BuildPlan
			idx := step - 1
			if idx >= len(plan.Steps) {
				return "", fmt.Errorf("step %d exceeds plan length %d", step, len(plan.Steps))
			}
			plan.Steps[idx].Status = "blocked"
			plan.Steps[idx].BlockedReason = reason
			emitBuildPlanBlock(t.sse, plan)
			return fmt.Sprintf("Step %d (%q) marked blocked. Reason recorded for the gap report.", step, plan.Steps[idx].Title), nil
		},
	}
}

// reviseBuildPlanToolDef — Builder's plan-revision tool. Single
// action verb to add / remove / reorder steps when execution reveals
// something the original plan didn't anticipate. Capped at
// BuildPlanRevisionLimit (3) calls per session — mirrors servitor's
// guard against the "reshuffle instead of execute" failure mode.
func (t *chatTurn) reviseBuildPlanToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "revise_build_plan",
			Description: fmt.Sprintf("Revise the current build plan when findings reveal something the original plan missed. action=\"add\" appends new pending steps, action=\"remove\" drops pending steps by step number (done / blocked steps refuse removal — they're durable history), action=\"reorder\" rearranges the full step list. Capped at %d revisions per session — use deliberately, not reflexively. Re-emits the plan card so the user sees the updated checklist.", BuildPlanRevisionLimit),
			Parameters: map[string]ToolParam{
				"action": {Type: "string", Description: "One of \"add\" | \"remove\" | \"reorder\"."},
				"steps": {
					Type:        "array",
					Description: "(add) New {title, detail?} step objects to append. Same shape as present_build_plan's steps.",
					Items: &ToolParam{
						Type: "object",
						Properties: map[string]ToolParam{
							"title":  {Type: "string", Description: "One-line step title."},
							"detail": {Type: "string", Description: "Optional one-line detail."},
						},
						Required: []string{"title"},
					},
				},
				"step_numbers": {
					Type:        "array",
					Description: "(remove) Step numbers to drop. Only pending steps can be removed; done / blocked refused.",
					Items:       &ToolParam{Type: "integer"},
				},
				"order": {
					Type:        "array",
					Description: "(reorder) New ordering of step numbers. Must be a permutation of all current step numbers — no missing, no extra.",
					Items:       &ToolParam{Type: "integer"},
				},
			},
			Required: []string{"action"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			if t.session == nil || t.session.BuildPlan == nil {
				return "", errors.New("revise_build_plan: no active build plan — call present_build_plan first")
			}
			plan := t.session.BuildPlan
			if plan.RevisionCount >= BuildPlanRevisionLimit {
				return "", fmt.Errorf("revise_build_plan: revision cap reached (%d) — execute the plan you have rather than re-shuffling", BuildPlanRevisionLimit)
			}
			action := strings.TrimSpace(stringArg(args, "action"))
			switch action {
			case "add":
				added := buildPlanStepsFromArg(args["steps"])
				if len(added) == 0 {
					return "", errors.New("revise_build_plan(add): steps is required and must contain at least one valid {title, detail?} object")
				}
				// Re-number the appended steps to follow the existing
				// max — keeps the user-facing numbering monotonic
				// across revisions.
				next := 0
				for _, s := range plan.Steps {
					if s.Number > next {
						next = s.Number
					}
				}
				for i := range added {
					next++
					added[i].Number = next
				}
				plan.Steps = append(plan.Steps, added...)
			case "remove":
				raw, _ := args["step_numbers"].([]any)
				if len(raw) == 0 {
					return "", errors.New("revise_build_plan(remove): step_numbers is required")
				}
				want := make(map[int]bool, len(raw))
				for _, v := range raw {
					if n, ok := toInt(v); ok {
						want[n] = true
					}
				}
				var refused []int
				out := plan.Steps[:0]
				for _, s := range plan.Steps {
					if !want[s.Number] {
						out = append(out, s)
						continue
					}
					if s.Status != "pending" {
						out = append(out, s)
						refused = append(refused, s.Number)
						continue
					}
					// dropped
				}
				plan.Steps = out
				if len(refused) > 0 {
					return "", fmt.Errorf("revise_build_plan(remove): refused step(s) %v — only pending steps can be removed (done / blocked steps stay as durable history)", refused)
				}
			case "reorder":
				raw, _ := args["order"].([]any)
				if len(raw) != len(plan.Steps) {
					return "", fmt.Errorf("revise_build_plan(reorder): order must list all %d step numbers; got %d", len(plan.Steps), len(raw))
				}
				want := make([]int, 0, len(raw))
				for _, v := range raw {
					if n, ok := toInt(v); ok {
						want = append(want, n)
					}
				}
				byNum := make(map[int]BuildPlanStep, len(plan.Steps))
				for _, s := range plan.Steps {
					byNum[s.Number] = s
				}
				seen := make(map[int]bool, len(want))
				newSteps := make([]BuildPlanStep, 0, len(want))
				for _, n := range want {
					s, ok := byNum[n]
					if !ok {
						return "", fmt.Errorf("revise_build_plan(reorder): unknown step number %d", n)
					}
					if seen[n] {
						return "", fmt.Errorf("revise_build_plan(reorder): step number %d listed more than once", n)
					}
					seen[n] = true
					newSteps = append(newSteps, s)
				}
				plan.Steps = newSteps
			default:
				return "", fmt.Errorf("revise_build_plan: action must be \"add\" | \"remove\" | \"reorder\" (got %q)", action)
			}
			plan.RevisionCount++
			emitBuildPlanBlock(t.sse, plan)
			return fmt.Sprintf("Plan revised (%s). %d of %d revisions used (%d remaining).",
				action, plan.RevisionCount, BuildPlanRevisionLimit, BuildPlanRevisionLimit-plan.RevisionCount), nil
		},
	}
}

// reportBuildGapsToolDef — Builder's "what didn't get done" tool.
// Required call before the final user-facing reply when any step is
// blocked or still pending. Returns a structured summary that the
// model must address in its Phase 5 synthesis (either explicitly
// surfacing the gap to the user, or revising the plan to fill it).
// Sets BuildPlanState.GapsReported, which injectSkippedGapReportWarning
// reads at turn end: a reply that closes out the plan without this call
// gets a corrective note + a visible warning. Enforcement is post-hoc
// rather than a mid-turn block because the reply has already streamed by
// then, and discarding it to force another round re-renders the content.
//
// Grades TWO independent things, deliberately. Step status is the model's OWN
// claim — it calls mark_step_done itself — so it cannot stand in for evidence:
// a step read "done" over a tool whose verification had FAILED, this tool
// answered "All steps completed successfully", and the model told the user
// everything was implemented. The verification ledger supplies the second,
// independent signal (see tool_verify_ledger.go).
func (t *chatTurn) reportBuildGapsToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "report_build_gaps",
			Description: "BEFORE your final reply, call this to surface every gap in the build: blocked or still-pending steps, AND any tool you authored that does not currently stand verified (never tested, failed its test, or edited since it last passed). Returns a structured summary you MUST address in the reply — either explain the gap to the user, or fix it (verify the tool, or call revise_build_plan within the revision cap) and re-call this. Marking a step done is your OWN claim and does not make its tool verified; only a passing test does. When every step is done and every authored tool is verified, this reports no gaps and you may write the reply. Takes no arguments.",
			Parameters:  map[string]ToolParam{},
			Caps:        []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			if t.session == nil || t.session.BuildPlan == nil {
				return "", errors.New("report_build_gaps: no active build plan — call present_build_plan first")
			}
			plan := t.session.BuildPlan
			plan.GapsReported = true
			type gapEntry struct {
				Step   int    `json:"step"`
				Title  string `json:"title"`
				Reason string `json:"reason"`
			}
			type unverifiedEntry struct {
				Tool   string `json:"tool"`
				Reason string `json:"reason"`
			}
			type gapReport struct {
				Blocked    []gapEntry        `json:"blocked,omitempty"`
				Skipped    []gapEntry        `json:"skipped,omitempty"`
				Unverified []unverifiedEntry `json:"unverified,omitempty"`
			}
			rep := gapReport{}
			for _, s := range plan.Steps {
				switch s.Status {
				case "blocked":
					rep.Blocked = append(rep.Blocked, gapEntry{Step: s.Number, Title: s.Title, Reason: s.BlockedReason})
				case "pending", "in_progress":
					rep.Skipped = append(rep.Skipped, gapEntry{Step: s.Number, Title: s.Title, Reason: "step never completed"})
				}
			}
			// Step status is SELF-REPORTED: the model calls mark_step_done itself,
			// so a step can read "done" over a tool whose verification failed —
			// which is precisely what happened, and this gate said "All steps
			// completed successfully" on top of it. Grade the tools too, from the
			// verification ledger, which records the outcome where it was actually
			// known instead of leaving it as prose in a scrolled-past result.
			if t.session != nil {
				for _, u := range unverifiedTools(t.udb, t.session.ID) {
					rep.Unverified = append(rep.Unverified, unverifiedEntry{Tool: u.Tool, Reason: u.Reason})
				}
			}
			emitBuildPlanBlock(t.sse, plan)
			if len(rep.Blocked) == 0 && len(rep.Skipped) == 0 && len(rep.Unverified) == 0 {
				return "All steps completed and every authored tool verified — no gaps to report. You may write the final reply.", nil
			}
			data, err := json.Marshal(rep)
			if err != nil {
				return "", fmt.Errorf("report_build_gaps: marshal: %v", err)
			}
			msg := string(data) + "\n\nIncorporate each gap into your final reply: explain what couldn't be built and why, OR call revise_build_plan to address it (within the revision cap)."
			if len(rep.Unverified) > 0 {
				msg += "\n\nThe `unverified` tools above were authored but do NOT currently stand verified. Do NOT tell the user they are working. Either verify each one now (add_tool with test_args, or tool_def(action=\"test\")) and re-call this, or state plainly in your reply which tools are unverified and why."
			}
			return msg, nil
		},
	}
}

// toInt is a small JSON-tolerant int coercion for revise_build_plan's
// array args. The LLM may emit step numbers as float64 (JSON number),
// int, or string-of-digits; we accept all three.
func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case string:
		var n int
		_, err := fmt.Sscanf(x, "%d", &n)
		return n, err == nil
	}
	return 0, false
}

// emitBuildPlanBlock sends the orchestrate_plan SSE block. Reused for
// the initial paint AND for every update — the renderer's onUpdate
// hook handles re-rendering when the block id matches a card that's
// already visible. Shape mirrors emitPlanBlock's payload so the same
// renderer in web_assets.go handles both.
func emitBuildPlanBlock(sse *sseWriter, plan *BuildPlanState) {
	if sse == nil || plan == nil {
		return
	}
	items := make([]map[string]any, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		item := map[string]any{
			"id":     s.Number,
			"title":  s.Title,
			"status": s.Status,
		}
		if s.WhatToFind != "" {
			item["what_to_find"] = s.WhatToFind
		}
		if s.Findings != "" {
			item["findings"] = s.Findings
		}
		if s.BlockedReason != "" {
			item["blocked_reason"] = s.BlockedReason
		}
		items = append(items, item)
	}
	sse.Send(map[string]any{
		"kind": "block",
		"type": "orchestrate_plan",
		"id":   plan.ID,
		"plan": items,
		// Stamp to help the activity log show update ordering when
		// the same block id receives multiple events.
		"emitted_at": time.Now().UTC().Format(time.RFC3339),
	})
}
