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

// explorerHardCap is the DEFAULT absolute round ceiling once explorer
// mode is active. Generous enough for a multi-step API exploration but
// still bounded so a runaway loop self-terminates. Per-agent override
// via AgentRecord.ExplorerHardCap (see resolveExplorerHardCap) — Builder
// runs higher because authoring an unfamiliar API is exploration-heavy.
const explorerHardCap = 50

// resolveExplorerHardCap returns the agent's explorer ceiling: its
// per-agent ExplorerHardCap when set, else the default explorerHardCap.
func resolveExplorerHardCap(a AgentRecord) int {
	if a.ExplorerHardCap > 0 {
		return a.ExplorerHardCap
	}
	return explorerHardCap
}

func (t *chatTurn) enterExplorerModeToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "enter_explorer_mode",
			Description: "Lift the round budget from the agent's normal limit up to its exploration hard cap for the rest of this step. Call only once an investigation is UNDER WAY and your results already show it needs more chained rounds than usual — evidence in hand, not a hunch. Fits: an unfamiliar API whose endpoints keep revealing sub-resources; working out a multi-step HOW that wasn't obvious up front; build/verify loops that keep uncovering work; probing a tool that returned a confusing error. Do NOT call it at the start of a turn \"just in case\", or for work that's merely multi-step — that's the misuse admins audit for.",
			Parameters: map[string]ToolParam{
				"reason": {
					Type:        "string",
					Description: "What you've already found that warrants more rounds, in one line. Example: \"/api/v1 returned 12 sub-resources; need to walk each to answer.\"",
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
			return fmt.Sprintf("EXPLORER_OK round budget raised to %d for the rest of this step. Continue.", resolveExplorerHardCap(t.agent)), nil
		},
	}
}
