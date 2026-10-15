package orchestrate

// "Lead plans, worker does the work" on a run with no chat page: a dispatch,
// a scheduled fire, a channel message, an agent run by another agent.
//
// The chat turn plans with plan_set and a plan card the person watches; none
// of these runs has a page, and their loops (agent_dispatch.go,
// agents_grouped_tool.go, scheduled_updates.go) do not run plans. So the lead
// gets work_step instead: one call is one step, run on the worker with the
// tools the lead names, and its answer comes back as the call's result. The
// lead takes another step when one comes back short (up to the agent's plan
// step limit) and writes the reply itself. Its own catalog keeps the
// conversation's tools; the work tools are listed for it by name, and a work
// tool named anyway does not resolve.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// workStepTool is the name of the dispatch lead's delegation tool.
const workStepTool = "work_step"

// applyDispatchLeadPlans turns a dispatched run into a planning one when its
// agent plans on the lead and this run is on the lead: it takes the work
// tools out of *tools, adds work_step, and tells the lead how this works in
// *sysPrompt. It reports whether it did, so the caller turns off the lazy
// tool fallback and the mid-run tool feed, which would hand the lead back what
// was taken.
func (t *chatTurn) applyDispatchLeadPlans(sess *ToolSession, pin LLMTier, request string, sysPrompt *string, tools *[]AgentToolDef, confirm func(string, string) bool) bool {
	if !t.agent.LeadPlans || !t.agent.LeadModel || pin != LEAD || t.machine.on ||
		isBuilderAgent(t.agent.ID) || appAgentExactTools(t.agent.ID) {
		return false
	}
	keep := map[string]bool{"ask_user": true, "ask_user_form": true}
	for _, td := range t.frameworkConversationalTools(sess) {
		if !leadDropsInPlanMode[td.Tool.Name] {
			keep[td.Tool.Name] = true
		}
	}
	for _, td := range t.appTools {
		keep[td.Tool.Name] = true
	}
	var lead, delegable []AgentToolDef
	for _, td := range *tools {
		switch {
		case keep[td.Tool.Name]:
			lead = append(lead, td)
		case leadDropsInPlanMode[td.Tool.Name]:
		default:
			delegable = append(delegable, td)
		}
	}
	lead = append(lead, t.workStepToolDef(request, delegable, confirm))
	*tools = lead
	*sysPrompt += dispatchLeadPlansBlock(delegable, resolveMaxPlanSteps(t.agent))
	Log("[orchestrate.routing] agent=%s dispatched run: the lead plans; %d tool(s) listed for work_step", t.agent.ID, len(delegable))
	return true
}

// dispatchLeadPlansBlock is leadPlansBlock for a lead that delegates with
// work_step rather than plan_set.
func dispatchLeadPlansBlock(delegable []AgentToolDef, maxSteps int) string {
	b := leadPlansBlock(delegable)
	b = strings.Replace(b, "commit it with plan_set: each step runs on a worker with the tools you name in its tools list, and one step is enough for a small job.",
		fmt.Sprintf("hand it to work_step: each call runs ONE step on a worker with the tools you name, and returns what the worker found. Call it again for the next step, or with a corrected brief when a step comes back short; at most %d steps this run.", maxSteps), 1)
	b = strings.Replace(b, "When the steps finish you write the reply from what they found.", "Then write the reply from what the steps found.", 1)
	return b
}

// workStepToolDef runs one step on the worker for the planning lead.
func (t *chatTurn) workStepToolDef(request string, delegable []AgentToolDef, confirm func(string, string) bool) AgentToolDef {
	byName := map[string]AgentToolDef{}
	var names []string
	for _, td := range delegable {
		if _, dup := byName[td.Tool.Name]; !dup {
			byName[td.Tool.Name] = td
			names = append(names, td.Tool.Name)
		}
	}
	maxSteps := resolveMaxPlanSteps(t.agent)
	var mu sync.Mutex
	steps := 0
	return AgentToolDef{
		Tool: Tool{
			Name:        workStepTool,
			Description: "Run one step of this run's work on a worker. The worker sees the request, your brief and the tools you name here, and nothing else; it returns what it found or did. Call it once per step.",
			Parameters: map[string]ToolParam{
				"title": {Type: "string", Description: "The step, in a few words."},
				"brief": {Type: "string", Description: "The worker's instructions: what to find or do, what to return and in what shape, and anything from earlier it needs. 2 to 5 sentences."},
				"tools": {Type: "array", Items: &ToolParam{Type: "string"}, Description: "The tools the worker gets, by name, from \"Tools a step can be given\". Name every one the brief needs."},
			},
			Required: []string{"title", "brief", "tools"},
			Caps:     []Capability{CapExecute},
		},
		SerialFirePerBatch: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			title := strings.TrimSpace(stringArg(args, "title"))
			brief := strings.TrimSpace(stringArg(args, "brief"))
			if brief == "" {
				return "", fmt.Errorf("brief is required: what the worker should find or do")
			}
			var pick []AgentToolDef
			var unknown []string
			for _, nm := range stringSliceArg(args, "tools") {
				nm = strings.TrimSpace(nm)
				if td, ok := byName[nm]; ok {
					pick = append(pick, td)
				} else if nm != "" {
					unknown = append(unknown, nm)
				}
			}
			if len(unknown) > 0 {
				return "", fmt.Errorf("no tool %s for a step; it can be given: %s", strings.Join(unknown, ", "), strings.Join(names, ", "))
			}
			// Counted once the call is one that runs: a refused call is not a step.
			mu.Lock()
			if steps >= maxSteps {
				mu.Unlock()
				return "", fmt.Errorf("this run has used its %d steps: write the reply from what the steps found", maxSteps)
			}
			steps++
			n := steps
			mu.Unlock()
			return t.runDispatchedStep(ctx, n, title, brief, request, pick, confirm)
		},
	}
}

// runDispatchedStep is one work_step: a worker loop with the picked tools,
// under the agent's rules, budgets and guardrails, on the worker tier.
func (t *chatTurn) runDispatchedStep(ctx context.Context, n int, title, brief, request string, tools []AgentToolDef, confirm func(string, string) bool) (string, error) {
	sys := prependAgentContext(t.gatedPersona(brief), t.agent, t.facts(), t.operatingNotes())
	if len(tools) > 0 {
		sys += "\n\n" + buildToolUseDirective(tools)
	}
	if frag := RenderToolPromptFragments(tools); frag != "" {
		sys += "\n\n" + frag
	}
	user := fmt.Sprintf("## Original request\n%s\n\n## Your task (step %d)\n%s", request, n, chFirst(title, brief))
	Log("[orchestrate.worker] agent=%s work_step %d (%q): %d tool(s)", t.agent.ID, n, title, len(tools))
	resp, _, err := t.app.RunAgentLoop(ctx, []Message{{Role: "user", Content: user}}, AgentLoopConfig{
		SystemPrompt:         sys,
		Tools:                tools,
		TierOverride:         WORKER,
		MaxRounds:            resolveMaxWorkerRounds(t.agent),
		ThinkBudget:          t.agent.ThinkBudget,
		Effort:               t.agent.Effort,
		ActionQuotas:         t.agent.ActionQuotas,
		BudgetKey:            t.agent.ID,
		DailySpendUSD:        t.agent.DailySpendUSD,
		SharedBudgets:        familyBudgets(t.agent),
		Confirm:              confirm,
		AskFirst:             t.asksFirst,
		GuardrailCheck:       t.guardrailEnforcer().Check,
		GuardrailActionGate:  t.guardrailEnforcer().ActionGate,
		GuardrailHalted:      t.guardrailEnforcer().Halted,
		GuardrailReject:      t.guardrailEnforcer().Reject,
		GuardrailDeclines:    t.agent.GuardrailDeclines,
		RoundAbortTools:      workerAbortTools,
		InterimContentHidden: true,
		ChatOptions:          []ChatOption{WithRouteKey("app.orchestrate.worker"), WithThink(false)},
	})
	if err != nil {
		return "", fmt.Errorf("step %d failed: %w", n, err)
	}
	out := ""
	if resp != nil {
		out = strings.TrimSpace(StripToolCallMarkup(resp.Content))
	}
	if out == "" {
		out = "(the worker returned nothing)"
	}
	return out, nil
}
