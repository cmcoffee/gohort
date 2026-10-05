package orchestrate

// Orchestrate's guardrails on an agent loop an app runs itself.
//
// Most app agents run through orchestrate's runner (AppChat, RunAgentSync),
// which puts the guardrails on every turn. Servitor's investigator does not:
// it runs its own loops (the investigator, its probe workers, synthesis,
// mapping, the workspace session) with RunAgentLoop directly, so the
// deployment's Always rules, the agent's own rules, the tool-result fence and
// scan, and the tainted-action check never saw them.
//
// AppLoopGuard is those checks, built once for one run of one app agent and
// applied to each loop config the run makes. Once per RUN, not per loop: a
// worker that reads injected text taints the run, and the investigator acting
// on what the worker reported is the action the tainted-action check exists
// for; the escalation counter is the run's for the same reason.
//
// Every model call these checks make (the warden, the scanner, the rejection
// writer) goes to the worker, so a private agent's run stays local.

import (
	"context"

	. "github.com/cmcoffee/gohort/core"
)

// AppLoopGuard is orchestrate's guardrails for one run of an app agent.
// A nil guard applies nothing.
type AppLoopGuard struct {
	t *chatTurn
}

// AppLoopGuard builds the guard for one run of agentID for user: their copy of
// the agent, with its rules, on ctx. Nil when the agent cannot be resolved;
// Apply on nil is a no-op, so a caller need not check.
func (T *OrchestrateApp) AppLoopGuard(ctx context.Context, user, agentID string) *AppLoopGuard {
	if T == nil || T.DB == nil || user == "" || agentID == "" {
		return nil
	}
	udb := UserDB(T.DB, user)
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		Log("[orchestrate.app-guard] %s has no agent %q: its loops run without orchestrate's guardrails", user, agentID)
		return nil
	}
	return &AppLoopGuard{t: &chatTurn{
		app: T, agent: rec, user: user, ownerUser: user, udb: udb, ctx: ctx,
		privateMode: agentForcesPrivate(rec) && !AllLLMsPrivate(),
	}}
}

// Apply returns cfg with the guardrail hooks set and every tool wrapped in the
// agent's tool-result policy (the untrusted-content fence and the injection
// scan, by what each tool declares). cfg.Tools is copied, not mutated.
func (g *AppLoopGuard) Apply(cfg AgentLoopConfig) AgentLoopConfig {
	if g == nil || g.t == nil {
		return cfg
	}
	e := g.t.guardrailEnforcer()
	cfg.GuardrailCheck = e.Check
	cfg.GuardrailActionGate = e.ActionGate
	cfg.GuardrailHalted = e.Halted
	cfg.GuardrailReject = e.Reject
	cfg.GuardrailDeclines = g.t.agent.GuardrailDeclines
	tools := make([]AgentToolDef, len(cfg.Tools))
	copy(tools, cfg.Tools)
	for i := range tools {
		if tools[i].Handler != nil {
			tools[i].Handler = g.t.withToolResultPolicy(g.t.agent, tools[i].Tool, tools[i].Handler)
		}
	}
	cfg.Tools = tools
	return cfg
}
