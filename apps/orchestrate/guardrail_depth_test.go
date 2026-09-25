package orchestrate

// How diligent a guardrail check is. The deployment's Always rules are checked
// at the depth an administrator set for them, and blocked on a failed check if
// the administrator said so, whatever the agent's owner chose for their own.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
)

func TestTheCheckReadsAsCarefullyAsItsRulesAsk(t *testing.T) {
	restore := withGlobalRules(t, "Never discuss the lunar calendar.")
	defer restore()
	own := AgentRecord{Guardrails: "never mention salary"}
	globals := enforcedGuardrailRules(own)
	var ownOnly []guardrailRule
	for _, r := range globals {
		if !r.Global {
			ownOnly = append(ownOnly, r)
		}
	}

	if d := wardenDepth(own, ownOnly); d != prompts.RuleDepthQuick {
		t.Errorf("an agent's own rules default to quick, as they always ran: %s", d)
	}
	if d := wardenDepth(own, globals); d != prompts.RuleDepthQuick {
		t.Errorf("the deployment's rules default to quick too: %s", d)
	}
	prompts.SetGlobalRulesDepth(prompts.RuleDepthModerate)
	if d := wardenDepth(own, globals); d != prompts.RuleDepthModerate {
		t.Errorf("with the deployment's rules in the check, its depth applies: %s", d)
	}
	careful := own
	careful.GuardrailDepth = prompts.RuleDepthThorough
	if d := wardenDepth(careful, globals); d != prompts.RuleDepthThorough {
		t.Errorf("judging both never lowers an owner's more careful choice: %s", d)
	}
	prompts.SetGlobalRulesDepth(prompts.RuleDepthQuick)
	if d := wardenDepth(own, globals); d != prompts.RuleDepthQuick {
		t.Errorf("the administrator's quick applies: %s", d)
	}

	// The depth reaches the checker as a reasoning level.
	prompts.SetGlobalRulesDepth(prompts.RuleDepthModerate)
	llm := &FakeLLM{Turns: []FakeTurn{{Content: `{"verdicts":[{"rule":"Never discuss the lunar calendar.","status":"comply","reason":"unrelated"}]}`}}}
	turn := guardTurn(t, llm, AgentRecord{Name: "X"})
	if turn.guardrailCheckHook()(guardHookPreOutput, "The weather is fine.").Blocked {
		t.Fatal("a complying reply was blocked")
	}
	cfg := llm.Config(0)
	if cfg.Think == nil || !*cfg.Think || cfg.Effort != "low" {
		t.Errorf("a moderate check reasons briefly: think=%v effort=%q", cfg.Think, cfg.Effort)
	}
}

// An owner's agent that lets a failed check through does not get to let a
// failed check on the DEPLOYMENT's rules through.
func TestAFailedCheckOnGovernanceRulesBlocksWhateverTheOwnerChose(t *testing.T) {
	restore := withGlobalRules(t, "Never discuss the lunar calendar.")
	defer restore()
	turn := guardTurn(t, wardenDown(), AgentRecord{Name: "X"}) // fails open on its own rules
	if !turn.guardrailCheckHook()(guardHookPreOutput, "anything").Blocked {
		t.Fatal("a failed check on the deployment's rules should block by default")
	}
	prompts.SetGlobalRulesFailOpen(true)
	if turn.guardrailCheckHook()(guardHookPreOutput, "anything").Blocked {
		t.Error("an administrator who chose to let it through is obeyed")
	}
	// Own rules alone keep the owner's choice.
	prompts.SetGlobalRules(nil)
	own := guardTurn(t, wardenDown(), AgentRecord{Name: "X", Guardrails: "r", GuardrailHooks: []string{"pre_action"}})
	if own.guardrailCheckHook()(guardHookPreAction, "do the thing").Blocked {
		t.Error("with no deployment rule in the check, the owner's fail-open stands")
	}
}

func TestGuardrailDepthIsAnAgentSettingWithADeploymentLimit(t *testing.T) {
	s, ok := triSettings[defaultGuardrailDepth]
	if !ok || s.framework != prompts.RuleDepthQuick || strings.Join(s.strictness, ",") != "quick,moderate,thorough" {
		t.Fatalf("depth should be a registered setting, quick by default, ordered quick to thorough: %+v", s)
	}
	if patchAgentFields["guardrail_depth"] {
		t.Error("guardrail fields never go through the general PATCH")
	}
}

// The depth is saved through its own owner-only endpoint, and a whole-record
// save from an agent's edit paths cannot change it.
func TestGuardrailDepthHasItsOwnOwnerOnlyEndpoint(t *testing.T) {
	app, req, udb := authedApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-1", Owner: "alice", Name: "Helper", OrchestratorPrompt: "p", Guardrails: "never mention salary"}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodPost, "/api/agents/agent-1/guardrail-depth", map[string]any{"guardrail_depth": "thorough"}))
	if w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	rec, _ := loadAgent(udb, "agent-1")
	if rec.GuardrailDepth != prompts.RuleDepthThorough || rec.Guardrails != "never mention salary" {
		t.Fatalf("depth set, rules untouched: %+v", rec)
	}
	w = httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodPost, "/api/agents/agent-1/guardrail-depth", map[string]any{"guardrail_depth": "extreme"}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("an unknown depth is refused, got %d", w.Code)
	}
}

// Whether a reply is held until its check clears is decided per kind of rule:
// the agent's own rules by its owner's hooks, the deployment's by Governance's
// "when replies are checked". Any Governance rule used to stop every agent
// streaming.
func TestReplyHoldingFollowsWhoseRulesAndWhen(t *testing.T) {
	restore := withGlobalRules(t, "Never discuss the lunar calendar.")
	defer restore()
	fast := AgentRecord{Guardrails: "never mention salary", GuardrailHooks: []string{guardHookPreInput, guardHookPreAction}}
	bare := AgentRecord{}

	if !agentHasOutputGuardrail(bare) || !agentHasOutputGuardrail(fast) {
		t.Error("by default the deployment's rules hold replies until checked")
	}
	prompts.SetGlobalRulesStreaming(true)
	if agentHasOutputGuardrail(bare) || agentHasOutputGuardrail(fast) {
		t.Error("checked while streaming: agents whose own rules don't judge output stream again")
	}
	if hooks := resolveGuardrailHooks(bare); !hooks[guardHookPreOutput] {
		t.Error("streaming changes WHEN the reply is checked, not whether")
	}
	balanced := fast
	balanced.GuardrailHooks = nil // the default: request, action and reply
	if !agentHasOutputGuardrail(balanced) {
		t.Error("an agent whose own rules judge output still holds its replies")
	}
}
