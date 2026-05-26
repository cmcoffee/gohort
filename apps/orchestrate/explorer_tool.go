// enter_explorer_mode — lifts the current worker step's round budget
// from the agent's MaxWorkerRounds (soft cap) up to an absolute hard
// cap (explorerHardCap, 50). Only available when the agent has
// AllowExplorer=true. The LLM should call this when it's mapping an
// unfamiliar API surface and needs more than 5-8 chained tool
// rounds to map the calls. 50 covers a multi-endpoint API walkthrough
// (e.g. enumerating vapi's ~30+ resources with intermediate fetches)
// without immediately bumping the ceiling again.
//
// Implementation: the runner passes MaxRounds = explorerHardCap to
// RunAgentLoop and wires StopRound to enforce the soft cap unless
// chatTurn.explorerMode is true. Calling enter_explorer_mode just
// flips that flag for the rest of the worker step.

package orchestrate

import (
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// explorerHardCap is the absolute round ceiling once explorer mode
// is active. Generous enough for a multi-step API exploration but
// still bounded so a runaway loop self-terminates.
const explorerHardCap = 50

func (t *chatTurn) enterExplorerModeToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "enter_explorer_mode",
			Description: "Lift the worker step's round budget from the agent's normal limit (5-8 rounds) up to an exploration hard cap (50). ONLY call when you've started exploring an unfamiliar API / system / file surface and you can already see the work needs >5 chained tool rounds to map the calls — e.g. you've hit a multi-step API where every call reveals new endpoints. Do NOT call speculatively at the start of a turn (\"just in case\"); the higher budget burns tokens. Once activated, the elevated budget lasts for the rest of this step. The framework logs the reason so admins can audit overuse.",
			Parameters: map[string]ToolParam{
				"reason": {
					Type:        "string",
					Description: "Short explanation of what you've found that warrants more rounds. Example: \"The /api/v1 endpoint returned a list of 12 sub-resources; need to enumerate each before I can answer the user's question.\"",
				},
			},
			Required: []string{"reason"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			if !t.agent.AllowExplorer {
				return "", errors.New("enter_explorer_mode: this agent doesn't have AllowExplorer set; ask the admin to enable it on the agent config")
			}
			reason := strings.TrimSpace(stringArg(args, "reason"))
			if reason == "" {
				return "", errors.New("reason is required")
			}
			if t.explorerMode {
				return "EXPLORER_OK already active. Continue your exploration.", nil
			}
			t.explorerMode = true
			t.explorerReason = reason
			Log("[orchestrate.explorer] activated agent=%s user=%s reason=%q",
				t.agent.ID, t.user, reason)
			return fmt.Sprintf("EXPLORER_OK round budget raised to %d for the rest of this step. Continue.", explorerHardCap), nil
		},
	}
}
