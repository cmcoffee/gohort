package anvil

// The agent's plan: for a change that touches several files, a short list of
// steps it sets before it starts and ticks as it goes, shown to the person as
// a checklist in the conversation (docs/design/anvil-development.md). A wrong
// approach is easier to catch before ten edits land than after.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
)

// planBlock is the card the plan publishes.
const planBlock = "anvil_plan"

// maxPlanSteps bounds a plan: it is a checklist to read at a glance.
const maxPlanSteps = 12

// planStep is one step of the plan.
type planStep struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// planTool sets the turn's plan and marks its steps done.
func (c *toolCtx) planTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "plan",
			Description: "Keep a short plan for a change that touches several files: set its steps before you start, then mark each one done as you finish it. " +
				"The person sees it as a checklist. Setting steps replaces the plan.",
			Parameters: map[string]ToolParam{
				"steps": {Type: "array", Description: "The steps, in order, each a short line. Setting them replaces the plan.", Items: &ToolParam{Type: "string"}},
				"done":  {Type: "array", Description: "The numbers (from 1) of steps you have finished.", Items: &ToolParam{Type: "integer"}},
			},
			Caps: []Capability{CapRead},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			return c.updatePlan(args)
		},
	}
}

// updatePlan applies a plan call and publishes the checklist.
func (c *toolCtx) updatePlan(args map[string]any) (string, error) {
	c.checkMu.Lock()
	defer c.checkMu.Unlock()
	if raw, ok := args["steps"].([]any); ok {
		var steps []planStep
		for _, v := range raw {
			if t := strings.TrimSpace(fmt.Sprint(v)); t != "" {
				steps = append(steps, planStep{Text: t})
			}
		}
		if len(steps) == 0 {
			return "", fmt.Errorf("a plan needs at least one step")
		}
		if len(steps) > maxPlanSteps {
			return "", fmt.Errorf("a plan has at most %d steps: keep it to what you will check off", maxPlanSteps)
		}
		c.plan = steps
		c.planSeq++
	}
	if len(c.plan) == 0 {
		return "", fmt.Errorf("there is no plan yet: set its steps first")
	}
	if raw, ok := args["done"].([]any); ok {
		for _, v := range raw {
			n, ok := v.(float64)
			if !ok || int(n) < 1 || int(n) > len(c.plan) {
				return "", fmt.Errorf("step %v is not in the plan (1 to %d)", v, len(c.plan))
			}
			c.plan[int(n)-1].Done = true
		}
	}
	done, next := 0, ""
	for _, s := range c.plan {
		if s.Done {
			done++
		} else if next == "" {
			next = s.Text
		}
	}
	if c.emitBlock != nil {
		b, _ := json.Marshal(c.plan)
		c.emitBlock(agenthost.Block{
			Type:  planBlock,
			ID:    fmt.Sprintf("anvil-plan-%s-%d", c.sessionID, c.planSeq),
			Title: "Plan",
			Text:  fmt.Sprintf("%d of %d done", done, len(c.plan)),
			Data:  map[string]string{"steps": string(b)},
		})
	}
	if next == "" {
		return fmt.Sprintf("Plan: all %d steps done.", len(c.plan)), nil
	}
	return fmt.Sprintf("Plan: %d of %d done. Next: %s", done, len(c.plan), next), nil
}
