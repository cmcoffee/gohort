// Builder's build plan: the checklist it presents at the end of Phase 2 and
// works through in Phase 4, drawn as a live orchestrate_plan card.
//
// The plan itself is core's WorkPlan (core/plan.go), the same state a tracked
// plan keeps (work_plan.go): step ids, the lifecycle, removing only pending
// steps, the revision cap, the gaps-reported mark. What stays here is Builder's
// own surface over it, and it differs from core's WorkPlanTools on purpose:
//
//   - The names and words are the ones Builder's prompts teach
//     (present_build_plan, mark_step_done, revise_build_plan,
//     report_build_gaps), and ask_user can carry the plan in the same call.
//   - The step tools are TOLERANT. The checklist is cosmetic, so a stale step
//     number is a soft note rather than an error, and starting a step finishes
//     the one left open; hard errors here drove the model into apology loops.
//   - report_build_gaps grades the verification ledger as well as the steps,
//     because a step marked done is the model's own claim.
//
// The plan lives on the session (ChatSession.BuildWorkPlan), so it survives
// the turn boundaries a five-step build crosses.

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
			Description: "Show the user a visible checklist of your build plan as a card. The PREFERRED Phase-2 shape is passing `plan` on ask_user (one call paints the checklist AND asks for approval), reach for present_build_plan only when there's no question to ask (plan already approved) or to UPDATE the plan mid-build (user requested edits; re-call with the full step list, same id, replaces the visible card in place). The card renders with all steps \"pending\"; subsequent mark_step_done calls flip individual rows to \"done\" as you execute. Each step is {title, detail?}: title is the one-line summary (\"Create Reddit Researcher shell\"), detail is the brief tool-call info (\"create_agent\").",
			Parameters: map[string]ToolParam{
				"steps": {
					Type:        "array",
					Description: "Ordered list of step objects. Each step: {title: \"Create agent shell\", detail: \"create_agent\"}, keep titles short (1 line) and details to the tool/arg summary.",
					Items: &ToolParam{
						Type: "object",
						Properties: map[string]ToolParam{
							"title":  {Type: "string", Description: "One-line step title shown in the card. Example: \"Add search_reddit tool\"."},
							"detail": {Type: "string", Description: "Optional one-line detail under the title: typically the tool name + key args. Example: \"add_tool(api, no credential)\"."},
						},
						Required: []string{"title"},
					},
				},
			},
			Required: []string{"steps"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if t.session == nil {
				return "", errors.New("present_build_plan requires an active session")
			}
			raw, ok := args["steps"]
			if !ok || raw == nil {
				return "", errors.New("steps is required")
			}
			titles, details, err := buildPlanArg(raw)
			if err != nil {
				return "", err
			}
			if len(titles) == 0 {
				return "", errors.New("steps had no usable titles")
			}
			if err := t.startBuildPlan(titles, details, ""); err != nil {
				return "", err
			}
			return fmt.Sprintf("Build plan presented (%d step%s): round budget extended to %d for execution. The user sees the checklist; each step will flip to ✓ as you call mark_step_done during execution.",
				len(titles), plural(len(titles)), t.planBudgetCap), nil
		},
	}
}

// startBuildPlan commits a new build plan, wholesale: fresh steps, a fresh
// revision count, no gap report yet. It is drawn on the current card while
// that plan still has steps open, since a plan replaced midway is the same
// piece of work, and on a card of its own after a finished one (plan_card.go).
//
// It also grants the execution budget: the round cap is lifted to where the
// turn is now plus buildPlanRoundsPerStep per step, so building and verifying
// the plan does not compete with whatever exploration already cost, and it is
// never lowered. present_build_plan and ask_user's plan both start one here.
func (t *chatTurn) startBuildPlan(titles, details []string, via string) error {
	plan := &WorkPlan{}
	if err := plan.SetSteps(titles, details); err != nil {
		return err
	}
	t.session.BuildPlanCard = t.buildPlanCard()
	t.session.BuildWorkPlan = plan
	t.paintBuildPlan()
	if grant := t.currentRound + buildPlanRoundsPerStep*len(titles); grant > t.planBudgetCap {
		t.planBudgetCap = grant
	}
	Log("[orchestrate.build_plan] plan presented%s: %d step(s), round budget lifted to %d (at round %d)",
		via, len(titles), t.planBudgetCap, t.currentRound)
	return nil
}

// buildPlanArg reads the {title, detail?} steps that present_build_plan,
// ask_user's plan and revise_build_plan(add) take, tolerating the loose shapes
// models produce (untyped maps, a missing detail, extra fields). A step with
// no title is dropped.
func buildPlanArg(raw any) (titles, details []string, err error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("steps marshal: %v", err)
	}
	var input []struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, nil, fmt.Errorf("steps shape: %v", err)
	}
	for _, s := range input {
		title := sanitizePlanText(s.Title)
		if title == "" {
			continue
		}
		titles = append(titles, title)
		details = append(details, sanitizePlanText(s.Detail))
	}
	return titles, details, nil
}

// buildPlanStep is one step of the session's build plan by id, and whether
// the plan has it.
func buildPlanStep(plan *WorkPlan, id int) (WorkPlanStep, bool) {
	for _, s := range plan.Snapshot() {
		if s.ID == id {
			return s, true
		}
	}
	return WorkPlanStep{}, false
}

// noBuildPlanNote is what a step tool says with no plan to mark.
const noBuildPlanNote = "No active build plan, so there's nothing to mark: the checklist is optional. Just keep doing the actual work."

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
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			// This is a COSMETIC checklist update — never hard-error on a
			// stale/out-of-range step number, or the model burns the turn in an
			// apology loop instead of doing the actual work. Soft-note and move on.
			if t.session == nil || t.session.BuildWorkPlan == nil {
				return noBuildPlanNote, nil
			}
			plan := t.session.BuildWorkPlan
			step := intFromArgs(args, "step")
			summary := strings.TrimSpace(stringArg(args, "summary"))
			if _, ok := buildPlanStep(plan, step); !ok {
				return fmt.Sprintf("There's no step %d (the plan has %d step(s)): nothing to update. The checklist is cosmetic; don't re-plan to fix numbering, just continue the real work.", step, len(plan.Snapshot())), nil
			}
			plan.RecordFindings(step, summary)
			t.paintBuildPlan()
			steps := plan.Snapshot()
			remaining := 0
			for _, s := range steps {
				if s.Status != WorkStepDone {
					remaining++
				}
			}
			if remaining == 0 {
				return fmt.Sprintf("Step %d marked done. All %d steps complete: end the turn with a one-line summary; no more tool calls.",
					step, len(steps)), nil
			}
			return fmt.Sprintf("Step %d marked done (%d remaining). Continue executing the next step.", step, remaining), nil
		},
	}
}

// autoAdvanceBuildPlan flips the next step not yet done to "done" with
// the supplied summary, then re-emits the orchestrate_plan SSE block.
// Called from wrapped authoring-tool handlers so the user's visible
// checklist updates even when the LLM forgets to call mark_step_done
// in its response. No-op when there's no active plan, no step left
// to do, or the session lacks an SSE writer (background paths).
//
// Returns the step id that was advanced, or 0 when no advance
// happened. Callers can use the return value to log / nudge but
// otherwise treat as best-effort.
func (t *chatTurn) autoAdvanceBuildPlan(summary string) int {
	if t == nil || t.session == nil || t.session.BuildWorkPlan == nil || t.sse == nil {
		return 0
	}
	plan := t.session.BuildWorkPlan
	for _, s := range plan.Snapshot() {
		if s.Status == WorkStepDone {
			continue
		}
		if summary != "" {
			plan.RecordFindings(s.ID, summary)
		} else {
			plan.SetStatus(s.ID, WorkStepDone)
		}
		t.paintBuildPlan()
		return s.ID
	}
	return 0
}

// markStepInProgressToolDef — Builder's "starting step N" tool.
// Servitor's investigator calls mark_step_in_progress before every
// probe so the UI shows which step the worker is currently driving;
// Builder mirrors the shape so the plan card surfaces the live focus
// during Phase 4 execution. A step left in_progress is completed rather
// than refused, keeping the lifecycle clean (one step at a time reflects
// the actual rhythm of plan_set's sequential worker pipeline).
func (t *chatTurn) markStepInProgressToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "mark_step_in_progress",
			Description: "Mark a step of the current build plan as in_progress before starting its worker. Pass step=N (1-indexed). If another step was still in_progress it's auto-completed (forward progress): you don't have to close it first. Updates the visible plan card so the user sees which step is actively running.",
			Parameters: map[string]ToolParam{
				"step": {Type: "integer", Description: "1-indexed step number, matching present_build_plan's numbering."},
			},
			Required: []string{"step"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			// Cosmetic checklist update — tolerant by design (see mark_step_done).
			if t.session == nil || t.session.BuildWorkPlan == nil {
				return noBuildPlanNote, nil
			}
			plan := t.session.BuildWorkPlan
			step := intFromArgs(args, "step")
			target, ok := buildPlanStep(plan, step)
			if !ok {
				return fmt.Sprintf("There's no step %d (the plan has %d step(s)): nothing to update. The checklist is cosmetic; don't re-plan to fix numbering, just continue the real work.", step, len(plan.Snapshot())), nil
			}
			// Auto-advance a stale in_progress step instead of refusing: the model
			// has clearly moved on, and the old "close it first" error was the
			// single biggest source of turn-wasting apology loops. Forward
			// progress → mark any other in_progress step done.
			advanced := false
			for _, s := range plan.Snapshot() {
				if s.ID != step && s.Status == WorkStepInProgress {
					plan.SetStatus(s.ID, WorkStepDone)
					advanced = true
				}
			}
			plan.SetStatus(step, WorkStepInProgress)
			t.paintBuildPlan()
			if advanced {
				return fmt.Sprintf("Step %d (%q) marked in_progress (auto-completed the previously open step). Continue.", step, target.Title), nil
			}
			return fmt.Sprintf("Step %d (%q) marked in_progress.", step, target.Title), nil
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
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if t.session == nil || t.session.BuildWorkPlan == nil {
				return noBuildPlanNote, nil
			}
			plan := t.session.BuildWorkPlan
			step := intFromArgs(args, "step")
			reason := strings.TrimSpace(stringArg(args, "reason"))
			if reason == "" {
				return "", errors.New("reason is required: describe what blocked the step in one line")
			}
			target, ok := buildPlanStep(plan, step)
			if !ok {
				return fmt.Sprintf("There's no step %d (the plan has %d step(s)): nothing to update. The checklist is cosmetic; note the blocker in your reply instead of re-planning.", step, len(plan.Snapshot())), nil
			}
			plan.MarkBlocked(step, reason)
			t.paintBuildPlan()
			return fmt.Sprintf("Step %d (%q) marked blocked. Reason recorded for the gap report.", step, target.Title), nil
		},
	}
}

// reviseBuildPlanToolDef — Builder's plan-revision tool. Single
// action verb to add / remove / reorder steps when execution reveals
// something the original plan didn't anticipate. Capped at
// WorkPlanRevisionLimit calls per plan — the guard against the
// "reshuffle instead of execute" failure mode. A refused revision
// does not count against the cap.
func (t *chatTurn) reviseBuildPlanToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "revise_build_plan",
			Description: fmt.Sprintf("Revise the current build plan when findings reveal something the original plan missed. action=\"add\" appends new pending steps, action=\"remove\" drops pending steps by step number (done / blocked steps refuse removal: they're durable history), action=\"reorder\" rearranges the full step list. Capped at %d revisions per session: use deliberately, not reflexively. Re-emits the plan card so the user sees the updated checklist.", WorkPlanRevisionLimit),
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
					Description: "(reorder) New ordering of step numbers. Must be a permutation of all current step numbers: no missing, no extra.",
					Items:       &ToolParam{Type: "integer"},
				},
			},
			Required: []string{"action"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if t.session == nil || t.session.BuildWorkPlan == nil {
				return "", errors.New("revise_build_plan: no active build plan, call present_build_plan first")
			}
			plan := t.session.BuildWorkPlan
			if plan.RevisionCount() >= WorkPlanRevisionLimit {
				return "", fmt.Errorf("revise_build_plan: revision cap reached (%d), execute the plan you have rather than re-shuffling", WorkPlanRevisionLimit)
			}
			action := strings.TrimSpace(stringArg(args, "action"))
			switch action {
			case "add":
				titles, details, _ := buildPlanArg(args["steps"])
				if len(titles) == 0 {
					return "", errors.New("revise_build_plan(add): steps is required and must contain at least one valid {title, detail?} object")
				}
				if _, err := plan.AddSteps(titles, details); err != nil {
					return "", fmt.Errorf("revise_build_plan(add): %v", err)
				}
			case "remove":
				ids := stepNumbersArg(args["step_numbers"])
				if len(ids) == 0 {
					return "", errors.New("revise_build_plan(remove): step_numbers is required")
				}
				if _, refused := plan.RemoveSteps(ids); len(refused) > 0 {
					return "", fmt.Errorf("revise_build_plan(remove): refused step(s) %v, only pending steps can be removed (done / blocked steps stay as durable history)", refused)
				}
			case "reorder":
				if err := plan.ReorderSteps(stepNumbersArg(args["order"])); err != nil {
					return "", fmt.Errorf("revise_build_plan(reorder): %v", err)
				}
			default:
				return "", fmt.Errorf("revise_build_plan: action must be \"add\" | \"remove\" | \"reorder\" (got %q)", action)
			}
			count, _ := plan.IncrRevision()
			t.paintBuildPlan()
			return fmt.Sprintf("Plan revised (%s). %d of %d revisions used (%d remaining).",
				action, count, WorkPlanRevisionLimit, WorkPlanRevisionLimit-count), nil
		},
	}
}

// stepNumbersArg reads revise_build_plan's array of step numbers, skipping an
// entry it cannot read as one.
func stepNumbersArg(v any) []int {
	raw, _ := v.([]any)
	out := make([]int, 0, len(raw))
	for _, item := range raw {
		if n, ok := toInt(item); ok {
			out = append(out, n)
		}
	}
	return out
}

// reportBuildGapsToolDef — Builder's "what didn't get done" tool.
// Required call before the final user-facing reply when any step is
// blocked or still pending. Returns a structured summary that the
// model must address in its Phase 5 synthesis (either explicitly
// surfacing the gap to the user, or revising the plan to fill it).
// Marks the plan's gaps reported, which injectSkippedGapReportWarning
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
			Description: "BEFORE your final reply, call this to surface every gap in the build: blocked or still-pending steps, AND any tool you authored that does not currently stand verified (never tested, failed its test, or edited since it last passed). Returns a structured summary you MUST address in the reply: either explain the gap to the user, or fix it (verify the tool, or call revise_build_plan within the revision cap) and re-call this. Marking a step done is your OWN claim and does not make its tool verified; only a passing test does. When every step is done and every authored tool is verified, this reports no gaps and you may write the reply. Works with NO build plan too, on a repair it grades just the tools you touched, so call it before claiming a fix worked. Takes no arguments.",
			Parameters:  map[string]ToolParam{},
			Caps:        []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if t.session == nil {
				return "", errors.New("report_build_gaps: no session")
			}
			// A REPAIR has no build plan — nobody presents a plan to fix one
			// broken tool — and refusing here removed the check exactly where it
			// mattered most: an agent that edited a tool twice, watched its test
			// fail twice, then wrote a confident "this can't be fixed" summary.
			// The plan half simply has nothing to say without a plan; the
			// verification-ledger half is about tools this session touched and
			// stands on its own.
			plan := t.session.BuildWorkPlan
			if plan != nil {
				plan.MarkGapsReported()
			}
			rep := currentBuildGaps(plan, t.udb, t.session.ID)
			t.gapsShown = rep.key()
			if plan != nil {
				t.paintBuildPlan()
			}
			if rep.empty() {
				if plan == nil {
					return "No build plan is active, and every tool you touched this session stands verified: no gaps to report. You may write the final reply.", nil
				}
				return "All steps completed and every authored tool verified: no gaps to report. You may write the final reply.", nil
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

// buildGaps is what report_build_gaps grades, and what the finish check below
// grades on its behalf.
type buildGaps struct {
	Blocked    []buildGapStep `json:"blocked,omitempty"`
	Skipped    []buildGapStep `json:"skipped,omitempty"`
	Unverified []buildGapTool `json:"unverified,omitempty"`
}

type buildGapStep struct {
	Step   int    `json:"step"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

type buildGapTool struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
}

// currentBuildGaps grades the session's plan steps and the verification ledger.
//
// Step status is SELF-REPORTED: the model calls mark_step_done itself, so a
// step can read "done" over a tool whose verification failed, which is
// precisely what happened, and report_build_gaps said "All steps completed
// successfully" on top of it. The tools are graded from the ledger, which
// records the outcome where it was actually known.
func currentBuildGaps(plan *WorkPlan, udb Database, sessionID string) buildGaps {
	var rep buildGaps
	if plan != nil {
		for _, st := range plan.Snapshot() {
			switch st.Status {
			case WorkStepBlocked:
				rep.Blocked = append(rep.Blocked, buildGapStep{Step: st.ID, Title: st.Title, Reason: st.BlockedReason})
			case WorkStepPending, WorkStepInProgress:
				rep.Skipped = append(rep.Skipped, buildGapStep{Step: st.ID, Title: st.Title, Reason: "step never completed"})
			}
		}
	}
	for _, u := range unverifiedTools(udb, sessionID) {
		rep.Unverified = append(rep.Unverified, buildGapTool{Tool: u.Tool, Reason: u.Reason})
	}
	return rep
}

func (g buildGaps) empty() bool {
	return len(g.Blocked) == 0 && len(g.Skipped) == 0 && len(g.Unverified) == 0
}

// key identifies this set of gaps, so a set the model has already been shown
// is not shown to it again.
func (g buildGaps) key() string {
	if g.empty() {
		return ""
	}
	data, _ := json.Marshal(g)
	return string(data)
}

// buildGapsFinishCheck is an authoring run's AgentLoopConfig.FinishCheck: the
// report_build_gaps call Builder is told to make before its final reply, made
// for it when it did not. Observed: a delegated Builder edited a failing tool,
// never ran it, and answered "has been fixed"; the next call failed exactly as
// before.
//
// Holds a reply back only for gaps the model has not been shown, whether by
// its own report_build_gaps call or by this check, so a reply that already
// accounts for them goes out on the next try. A plan with steps still pending
// is left alone: that is the plan-approval turn or a question mid-build, and
// the reply there is legitimately not a finish.
//
// plan reads the live plan (nil when there is none, as on a delegated run);
// sessionID is the key the verification ledger records this run's tools under.
func buildGapsFinishCheck(plan func() *WorkPlan, udb Database, sessionID string, shown *string) func(string) (string, string) {
	return func(string) (string, string) {
		var p *WorkPlan
		if plan != nil {
			p = plan()
		}
		rep := currentBuildGaps(p, udb, sessionID)
		if len(rep.Skipped) > 0 {
			return "", ""
		}
		if rep.empty() {
			// Checked on its behalf and clean: the after-turn warning about a
			// skipped report has nothing left to say.
			if p != nil {
				p.MarkGapsReported()
			}
			return "", ""
		}
		key := rep.key()
		if shown != nil && *shown == key {
			return "", ""
		}
		if shown != nil {
			*shown = key
		}
		if p != nil {
			p.MarkGapsReported()
		}
		return rep.finishNotice(), rep.strikeReason()
	}
}

// finishNotice tells the model what the check found, in the terms it needs to
// act on: which tools are not verified and why, and which steps are blocked.
func (g buildGaps) finishNotice() string {
	var b strings.Builder
	b.WriteString("Your reply was held back: before a reply that finishes the work, the build check (report_build_gaps) runs, and you had not run it. It found:")
	apps := false
	for _, u := range g.Unverified {
		if slug, isApp := ledgerApp(u.Tool); isApp {
			apps = true
			fmt.Fprintf(&b, "\n  - app %s is NOT ready: %s", slug, u.Reason)
			continue
		}
		fmt.Fprintf(&b, "\n  - tool %s is NOT verified: %s", u.Tool, u.Reason)
	}
	for _, st := range g.Blocked {
		fmt.Fprintf(&b, "\n  - step %d (%s) is blocked: %s", st.Step, st.Title, st.Reason)
	}
	b.WriteString("\n\nAn edit is not a fix and a save is not a test: until a tool has run and worked, you do not know that it works. Verify each tool now (tool_def(action=\"test\") with cases, or add_tool with test_args) and fix what fails. If you cannot verify one, say plainly in your reply which one and why. Do NOT tell the user an unverified tool works or has been fixed.")
	if apps {
		b.WriteString(" For an app: fix what its last check reported and run app_def(action=\"verify\") until it passes. If you cannot get it to pass, say plainly in your reply that the app does not work yet, what fails, and what you tried; do NOT tell the user where to open it as if it works.")
	}
	return b.String()
}

// buildGapsFinishCheck wires the check for an interactive turn. Nil for an
// agent that does not author.
func (t *chatTurn) buildGapsFinishCheck() func(string) (string, string) {
	if !agentCanAuthor(t.agent) || t.session == nil {
		return nil
	}
	return buildGapsFinishCheck(func() *WorkPlan { return t.session.BuildWorkPlan }, t.udb, t.session.ID, &t.gapsShown)
}

// buildGapsNote is what an authoring turn adds when it ends with the check's
// corrections spent and gaps still standing: a labelled line naming them, so a
// reply calling them working does not stand alone. "" when nothing is unmet,
// or when steps are still pending, which is not a finish.
func buildGapsNote(plan *WorkPlan, udb Database, sessionID string) string {
	rep := currentBuildGaps(plan, udb, sessionID)
	if len(rep.Skipped) > 0 || rep.empty() {
		return ""
	}
	var parts []string
	for _, u := range rep.Unverified {
		if slug, isApp := ledgerApp(u.Tool); isApp {
			parts = append(parts, fmt.Sprintf("app %s (%s)", slug, u.Reason))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", u.Tool, u.Reason))
	}
	for _, st := range rep.Blocked {
		parts = append(parts, fmt.Sprintf("step %d, %s (blocked: %s)", st.Step, st.Title, st.Reason))
	}
	return "[Build check] Still not verified when this reply was written: " + strings.Join(parts, "; ") + ". Anything above that says these work is unconfirmed."
}

// buildGapsUnmet is an interactive turn's FinishUnmet: the reply has already
// streamed, so what is still unmet goes on the turn's diagnostics trail rather
// than into the reply, where the host would render the whole reply twice.
func (t *chatTurn) buildGapsUnmet() func() {
	if !agentCanAuthor(t.agent) || t.session == nil {
		return nil
	}
	return func() {
		if note := buildGapsNote(t.session.BuildWorkPlan, t.udb, t.session.ID); note != "" {
			t.turnDiag("build-check-unmet", note)
		}
	}
}

// dispatchBuildCheck is the build check for a delegated run of an authoring
// agent, which has no plan and records its tools under its own session key.
// A delegated reply is relayed rather than streamed, so what is still unmet
// is added to the reply itself. Nil for an agent that does not author, and
// every method is safe on nil.
type dispatchBuildCheck struct {
	ts    *ToolSession
	shown string
	note  string
}

func newDispatchBuildCheck(target AgentRecord, ts *ToolSession) *dispatchBuildCheck {
	if !agentCanAuthor(target) || ts == nil || ts.ChatSessionID == "" {
		return nil
	}
	return &dispatchBuildCheck{ts: ts}
}

func (d *dispatchBuildCheck) finishCheck() func(string) (string, string) {
	if d == nil {
		return nil
	}
	return buildGapsFinishCheck(nil, d.ts.DB, d.ts.ChatSessionID, &d.shown)
}

func (d *dispatchBuildCheck) finishUnmet() func() {
	if d == nil {
		return nil
	}
	return func() { d.note = buildGapsNote(nil, d.ts.DB, d.ts.ChatSessionID) }
}

// questionNote is what is still unmet when the run ends on a QUESTION rather
// than a reply. A question is a control tool that closes the turn, so the
// finish check never runs and annotate has nothing; and the question text
// replaces the reply in the relay anyway. A delegated Builder whose posting
// tool had failed every verification asked the user how to schedule it,
// described the tool as running "on its first live run", and nothing said
// otherwise. The note rides on the question, so the user reads it with it.
func (d *dispatchBuildCheck) questionNote() string {
	if d == nil {
		return ""
	}
	return buildGapsNote(nil, d.ts.DB, d.ts.ChatSessionID)
}

// annotate adds what is still unmet to the reply the run hands back.
func (d *dispatchBuildCheck) annotate(resp *Response) {
	if d == nil || d.note == "" || resp == nil {
		return
	}
	resp.Content = strings.TrimRight(resp.Content, " \n") + "\n\n" + d.note
}

// strikeReason is the line shown beside the reply the check took back.
func (g buildGaps) strikeReason() string {
	switch n := len(g.Unverified); {
	case n == 1:
		if slug, isApp := ledgerApp(g.Unverified[0].Tool); isApp {
			return fmt.Sprintf("Held back: sent while app %s's last check had not passed.", slug)
		}
		return fmt.Sprintf("Held back: sent before the build check, which found %s not verified.", g.Unverified[0].Tool)
	case n > 1:
		return fmt.Sprintf("Held back: sent before the build check, which found %d tools not verified.", n)
	}
	return "Held back: sent before the build check, which found blocked steps."
}

// toInt is a small JSON-tolerant int coercion for revise_build_plan's
// array args (stepNumbersArg). The LLM may emit step numbers as float64 (JSON number),
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

// paintBuildPlan sends the build plan's card and keeps it with the thread.
// Reused for the initial paint AND for every update: the renderer's onUpdate
// hook re-renders the card already showing under the same id.
func (t *chatTurn) paintBuildPlan() {
	if t.session == nil || t.session.BuildWorkPlan == nil {
		return
	}
	t.paintPlanCard(t.session.BuildPlanCard, planCardSteps(t.session.BuildWorkPlan.Snapshot()), "")
}

// buildPlanCard is the card a newly presented build plan is drawn on.
func (t *chatTurn) buildPlanCard() string {
	if p := t.session.BuildWorkPlan; p != nil {
		return t.nextPlanCard(t.session.BuildPlanCard, p.Pending() > 0, "build-plan-")
	}
	return t.nextPlanCard("", false, "build-plan-")
}
