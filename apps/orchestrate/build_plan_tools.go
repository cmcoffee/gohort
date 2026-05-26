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
			Description: "Show the user a visible checklist of your build plan as a card. Call this at the END of Phase 2 alongside ask_user — the card renders with all steps in \"pending\" status; subsequent mark_step_done calls update individual rows to \"done\" as you execute. Each step is {title, detail?}: title is the one-line summary (\"Create Reddit Researcher shell\"), detail is the brief tool-call info (\"create_agent\"). Re-call to update the plan (e.g. user requested edits) — same id, replaces the visible card in place.",
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
			return fmt.Sprintf("Build plan presented (%d step%s). The user sees the checklist; each step will flip to ✓ as you call mark_step_done during execution.",
				len(steps), plural(len(steps))), nil
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
		title := strings.TrimSpace(s.Title)
		if title == "" {
			continue
		}
		out = append(out, BuildPlanStep{
			Number:     i + 1,
			Title:      title,
			WhatToFind: strings.TrimSpace(s.Detail),
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
