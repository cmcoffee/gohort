package orchestrate

// One question from an app's page to the app's agent.
//
// An app's page could reach a model only through a chat or pipeline section,
// never from its own code: a game could not ask for a line of dialogue, a
// tool could not ask "summarize this". AppAgentAsk answers one prompt with the
// owner's app agent: its instructions and rules as the system prompt, its
// model tier and effort, a bounded reply, and NO tools. No tools on purpose:
// the prompt comes from any user of a shared app, and the owner's agent runs
// with the owner's credentials, so the page may use the agent's voice but
// never drive its hands. The owner pays; customapps caps the spend per app
// and per user before calling here.

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appAskMaxTokens bounds one reply.
const appAskMaxTokens = 2000

// AppAgentAsk answers prompt with owner's agent agentID, and says what the
// call cost at this deployment's rates.
func (T *OrchestrateApp) AppAgentAsk(ctx context.Context, owner, agentID, prompt string, jsonMode bool) (string, float64, error) {
	if T == nil || T.DB == nil || owner == "" {
		return "", 0, fmt.Errorf("the app's agent is not available")
	}
	agent, ok := findAgentByNameOrID(UserDB(T.DB, owner), owner, agentID)
	if !ok {
		return "", 0, fmt.Errorf("the app's agent %q is gone", agentID)
	}
	sys := strings.TrimSpace(agent.OrchestratorPrompt)
	if r := strings.TrimSpace(agent.Rules); r != "" {
		sys += "\n\nRules:\n" + r
	}
	if sys == "" {
		sys = "Answer the request directly."
	}
	opts := []ChatOption{WithMaxTokens(appAskMaxTokens)}
	if jsonMode {
		opts = append(opts, WithJSONMode())
	}
	if e := strings.TrimSpace(agent.Effort); e != "" {
		opts = append(opts, WithEffort(e))
	}
	call := T.WorkerChat
	if agent.LeadModel {
		call = T.LeadChat
	}
	resp, err := call(ctx, []Message{{Role: "system", Content: sys}, {Role: "user", Content: prompt}}, opts...)
	if err != nil {
		return "", 0, err
	}
	return resp.Content, GetCostRates().Estimate(appAskUsage(resp)), nil
}

// appAskUsage is one response's tokens, on the tier that served it.
func appAskUsage(resp *Response) UsageDiff {
	if resp == nil {
		return UsageDiff{}
	}
	in, out, cr, cw := int64(resp.InputTokens), int64(resp.OutputTokens), int64(resp.CacheReadTokens), int64(resp.CacheWriteTokens)
	if resp.Tier == LEAD {
		return UsageDiff{LeadInput: in, LeadOutput: out, LeadCacheRead: cr, LeadCacheWrite: cw}
	}
	return UsageDiff{WorkerInput: in, WorkerOutput: out, WorkerCacheRead: cr, WorkerCacheWrite: cw}
}
