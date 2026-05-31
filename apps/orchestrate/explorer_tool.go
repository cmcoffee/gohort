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
			Description: "Lift the round budget from the agent's normal limit up to the exploration hard cap (50 rounds) for the rest of this step. Call when you've STARTED an investigation and can already see it needs more than your normal budget of chained tool rounds — the trigger is concrete evidence in your current results, not speculation. Good fits include: (a) mapping an unfamiliar API where each endpoint reveals more sub-resources (you've fetched one, seen 12 child links, need to walk each); (b) figuring out HOW to do something multi-step that wasn't obvious up front — e.g. \"scrape this site for the video\" (find the video container, identify the streaming format, locate the manifest, resolve segment URLs, fetch + concatenate), \"reverse-engineer this app's auth flow\" (intercept the login, identify token shape, find the refresh path), \"figure out where this data lives in the system\" (grep the codebase, follow imports, identify the persistence layer); (c) iterative build/verify loops where the verify step keeps revealing more work; (d) discovery work where you don't know the shape of the answer until you've looked around; (e) troubleshooting a misbehaving tool — a tool returned a confusing error or wrong-shape output and you need to probe it (try variant args, inspect related state, narrow down the failure mode) before you can either work around it or report cleanly. Do NOT call speculatively at the start of a turn (\"just in case\") or for tasks that are routine multi-step — the elevated budget burns tokens, and burning the explorer budget when the work fits a normal round count is the failure mode admins audit for. Once activated, the elevated budget lasts for the rest of this step.",
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
