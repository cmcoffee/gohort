package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// request_build: a non-Fleet agent's path to a sub-agent.
//
// Authoring is Builder's job, and Builder is dispatch-callable only from a Fleet
// agent (see agents_grouped_tool.go). So a plain agent asked to "create a
// sub-agent" has no path and improvises badly (self-dispatch loop, malformed
// search, telling the user to open an admin panel — the friction we saw live).
//
// This gives it a path that keeps the human in the loop, which is exactly the
// concern the hard Fleet gate was protecting: instead of authoring anything, it
// QUEUES an approval the user sees. On approve, Builder runs on the agent's
// behalf and stamps its creation OwnedBy the requesting agent (the async twin of
// a live Fleet dispatch — see runAgentSyncConfirm's DispatchParentAgentID
// handling and the build_agent case in resolveApproval).
//
// So: admin/Fleet decides an agent CAN author (turn Fleet on); this is the
// no-Fleet fallback where the user approves each build one at a time.

const buildAgentAction = "build_agent"

// requestBuildTool builds the request_build AgentToolDef for one agent. user is
// the owner; agentID/agentName identify the requester stamped onto the approval.
func requestBuildTool(user, agentID, agentName string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "request_build",
			Description: "Ask to have a SUB-AGENT built for you — the way to answer \"create an agent that…\" when you cannot author one yourself. You do NOT build it: this queues a request the user approves, and on approval Builder authors it as YOUR sub-agent. Give a complete spec in `brief` (what it does, its persona, the tools/sources it needs, any schedule). Use this instead of trying to dispatch, search, or write files. After calling it, tell the user you've queued the build for their approval.",
			Parameters: map[string]ToolParam{
				"brief": {
					Type:        "string",
					Description: "The full build spec for Builder: the sub-agent's job, persona, the tools/credentials/sources it needs, and any schedule or trigger. Write it as a complete brief — Builder acts on this after the user approves, without you in the loop.",
				},
				"name": {
					Type:        "string",
					Description: "Optional suggested name for the sub-agent (Builder may refine it).",
				},
			},
			Required: []string{"brief"},
			Caps:     []Capability{CapExecute},
		},
		NeedsConfirm: false, // the approval IS the gate; no per-call confirm on top
		Handler: func(args map[string]any) (string, error) {
			brief := strings.TrimSpace(StringArg(args, "brief"))
			if brief == "" {
				return "", fmt.Errorf("brief is required — describe the sub-agent to build (its job, persona, tools, schedule)")
			}
			if name := strings.TrimSpace(StringArg(args, "name")); name != "" {
				brief = "Suggested name: " + name + "\n\n" + brief
			}
			a := SaveAuthorization(RootDB, Authorization{
				Owner:     user,
				Action:    buildAgentAction,
				Agent:     "builder", // approval dispatches Builder
				FromAgent: agentID,   // stamps OwnedBy on the creation
				Brief:     brief,
			})
			who := agentName
			if who == "" {
				who = "this agent"
			}
			Log("[orchestrate/request_build] %s queued a sub-agent build (auth %s, requester=%s)", user, a.ID, agentID)
			return fmt.Sprintf("Queued a sub-agent build for the user's approval (id %s). It appears in their Authorizations pane; on approval Builder drafts it as a sub-agent of %s. Tell the user it's waiting for their approval and briefly what it will do.", a.ID, who), nil
		},
	}
}
