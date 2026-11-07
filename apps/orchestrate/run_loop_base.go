package orchestrate

import (
	. "github.com/cmcoffee/gohort/core"
)

// withLoopBase fills the part of an agent run's loop config that comes from
// the agent and its turn alone, and hands the config back.
//
// Every way an agent runs (the web turn, a sync or continuing dispatch, the
// agents tool, a scheduled fire, a lead plan's worker step and its dispatched
// step) builds its own AgentLoopConfig, because each streams, reports and
// confirms differently. What must NOT differ is what the agent is held to: its
// spend cap and action quotas, its family's budgets, its guardrails and its
// ask-first marks, and the think settings it was given. Each path used to set
// those by hand, and the agents tool's run left the caps out entirely, so an
// agent run that way spent past its own cap. Built here once, a path cannot
// drop one by leaving a line out.
//
// t is the turn the agent runs in (a dispatch's sub-turn, whose agent is the
// target). The fields below are set unconditionally, so a path that needs a
// different value (a continuing run's own Effort) sets it after this call.
// TestEveryAgentRunStartsFromTheLoopBase keeps every run path on it.
func (t *chatTurn) withLoopBase(cfg AgentLoopConfig) AgentLoopConfig {
	g := t.guardrailEnforcer()
	cfg.ActionQuotas = t.agent.ActionQuotas // per-agent 24h caps; empty = uncapped
	cfg.BudgetKey = t.agent.ID
	cfg.DailySpendUSD = t.agent.DailySpendUSD
	cfg.SharedBudgets = familyBudgets(t.agent) // a sub-agent counts against its parents' caps
	cfg.ThinkBudget = t.agent.ThinkBudget      // per-agent override; 0 = inherit route/global
	cfg.Effort = t.agent.Effort                // per-agent level; a budget above wins
	cfg.GuardrailCheck = g.Check
	cfg.GuardrailObserve = g.Observe
	cfg.GuardrailActionGate = g.ActionGate
	cfg.GuardrailHalted = g.Halted
	cfg.GuardrailReject = g.Reject
	cfg.GuardrailDeclines = t.agent.GuardrailDeclines
	cfg.AskFirst = t.asksFirst // its marks and, through delegation, its caller's (ask_first.go)
	return cfg
}
