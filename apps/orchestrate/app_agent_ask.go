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
	"sync"

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

// AppAgentRun runs one of an app's agents WITH its tools, for an app script's
// gohort.run_agent, and returns its reply and what it cost.
//
// ask is one answer from the agent's prompt and no tools: right for a summary,
// wrong for a game master that should roll its dice with calculate rather than
// make the numbers up. This is the same agent as a full turn.
//
// Which agent: the app's own (agentID, the app's agent_id) unless agentKey
// names another, and then only one made for this app (owning_app = slug). An
// app's backend reaching any agent its owner has is a wider promise than an
// app having a brain.
//
// Whose run: the definition is the owner's and the run is the person's
// (user), as the app's chat section runs it, so a shared app's tools act with
// that person's own accounts. Nobody is there to approve a tool that stops to
// ask, so such a call is refused, never assumed.
func (T *OrchestrateApp) AppAgentRun(ctx context.Context, owner, user, slug, agentID, agentKey, prompt string) (string, float64, error) {
	if T == nil || T.DB == nil || owner == "" {
		return "", 0, fmt.Errorf("the app's agents are not available")
	}
	if user == "" {
		user = owner
	}
	agent, err := T.appOwnAgent(owner, slug, agentID, agentKey)
	if err != nil {
		return "", 0, err
	}
	res, err := T.runAgentSyncConfirm(ctx, owner, user, agent.ID, prompt, func(string, string) bool { return false })
	cost := GetCostRates().Estimate(res.Usage)
	if err != nil {
		return "", cost, err
	}
	text := res.Text
	if res.HitRoundCap {
		text += "\n\n(The agent stopped at its round limit before it finished.)"
	}
	return text, cost, nil
}

// appOwnAgent resolves an agent an app may run: its agent_id, or one whose
// owning_app is the app.
func (T *OrchestrateApp) appOwnAgent(owner, slug, agentID, agentKey string) (AgentRecord, error) {
	udb := UserDB(T.DB, owner)
	if udb == nil {
		return AgentRecord{}, fmt.Errorf("the app's agents are not available")
	}
	var own AgentRecord
	haveOwn := false
	if strings.TrimSpace(agentID) != "" {
		own, haveOwn = findAgentByNameOrID(udb, owner, agentID)
	}
	key := strings.TrimSpace(agentKey)
	if key == "" {
		if !haveOwn {
			return AgentRecord{}, fmt.Errorf("this app has no agent to run: set its agent_id, or name one of its agents (created with owning_app=%q)", slug)
		}
		return own, nil
	}
	agent, ok := findAgentByNameOrID(udb, owner, key)
	if !ok {
		return AgentRecord{}, fmt.Errorf("no agent %q", key)
	}
	if (haveOwn && agent.ID == own.ID) || (slug != "" && agent.OwningApp == slug) {
		return agent, nil
	}
	return AgentRecord{}, fmt.Errorf("agent %q is not one of this app's agents: run_agent reaches the app's agent_id or an agent made for it (owning_app=%q)", agent.Name, slug)
}

// AppPipelineRun runs an app's pipeline to the end, for an app script's
// gohort.run_pipeline, and returns its final output and what it cost. The
// app's own (pipelineID, its pipeline_id) unless pipelineKey names one made
// for it (owning_app = slug). Its agent stages run as AppAgentRun runs an
// agent: the owner's definitions, the person's run, nothing approved unasked.
//
// The whole run happens inside the script's, so it suits a short pipeline; a
// long one belongs in a pipeline section, where it runs on its own.
func (T *OrchestrateApp) AppPipelineRun(ctx context.Context, owner, user, slug, pipelineID, pipelineKey, input string) (string, float64, error) {
	if T == nil || T.DB == nil || owner == "" {
		return "", 0, fmt.Errorf("the app's pipelines are not available")
	}
	if user == "" {
		user = owner
	}
	def, err := T.appOwnPipeline(owner, slug, pipelineID, pipelineKey)
	if err != nil {
		return "", 0, err
	}
	// Worker stages bill to this tracker; agent stages to their own, added in.
	ctx, mine := WithRequestUsage(ctx)
	var mu sync.Mutex
	var stageCost float64
	ownerDB := UserDB(T.DB, owner)
	out, _, err := T.RunPipelineDefHooks(ctx, def, input, PipelineHooks{
		Dispatch: func(ctx context.Context, agentKey, stageInput string) (string, error) {
			// A panel voice that is a plain role, not an agent, answers as a
			// role on the worker, as it does on a pipeline's own page.
			if _, found := findAgentByNameOrID(ownerDB, owner, agentKey); !found {
				return "", ErrNoSuchAgent
			}
			res, err := T.runAgentSyncConfirm(ctx, owner, user, agentKey, stageInput, func(string, string) bool { return false })
			mu.Lock()
			stageCost += GetCostRates().Estimate(res.Usage)
			mu.Unlock()
			return res.Text, err
		},
		Machine: T.pipelineMachineRunner(user),
		Tools:   T.pipelineStandaloneTools(ctx, user, def),
	})
	mu.Lock()
	cost := GetCostRates().Estimate(mine.Diff(UsageSnapshot{})) + stageCost
	mu.Unlock()
	if err != nil {
		return "", cost, err
	}
	return out, cost, nil
}

// appOwnPipeline resolves a pipeline an app may run: its pipeline_id, or one
// whose owning_app is the app.
func (T *OrchestrateApp) appOwnPipeline(owner, slug, pipelineID, pipelineKey string) (PipelineDef, error) {
	key := strings.TrimSpace(pipelineKey)
	if key == "" {
		def, ok := T.LookupAppPipeline(owner, pipelineID)
		if !ok {
			return PipelineDef{}, fmt.Errorf("this app has no pipeline to run: set its pipeline_id, or name one of its pipelines (created with owning_app=%q)", slug)
		}
		return def, nil
	}
	def, ok := T.LookupAppPipeline(owner, key)
	if !ok {
		return PipelineDef{}, fmt.Errorf("no pipeline %q", key)
	}
	if own, ok := T.LookupAppPipeline(owner, pipelineID); (ok && own.ID == def.ID) || (slug != "" && def.OwningApp == slug) {
		return def, nil
	}
	return PipelineDef{}, fmt.Errorf("pipeline %q is not one of this app's pipelines: run_pipeline reaches the app's pipeline_id or a pipeline made for it (owning_app=%q)", def.Name, slug)
}
