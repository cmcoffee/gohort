package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// errWardenLLM returns an error from Chat, to exercise the fail-open path.
type errWardenLLM struct{}

func (errWardenLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	return nil, errors.New("warden LLM down")
}
func (errWardenLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return nil, errors.New("warden LLM down")
}

func guardTurn(t *testing.T, llm LLM, agent AgentRecord) *chatTurn {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.LLM = llm
	app.DB = root
	return &chatTurn{app: app, agent: agent, user: "u", udb: UserDB(root, "u"), ctx: context.Background()}
}

// TestGuardrailHookInertWithoutRules pins zero-overhead: no rules → nil hook.
func TestGuardrailHookInertWithoutRules(t *testing.T) {
	turn := guardTurn(t, &wardenStubLLM{}, AgentRecord{Name: "X"})
	if turn.guardrailCheckHook() != nil {
		t.Fatal("an agent with no guardrails must yield a nil hook (no overhead)")
	}
}

// TestGuardrailBlocksAndForbidsReroute pins the block message: a violation at
// an active hook returns blocked=true with a message naming the rule and
// forbidding a re-route.
func TestGuardrailBlocksAndForbidsReroute(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never spend money","status":"violate","reason":"it makes a purchase"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "Buyer", Guardrails: "never spend money", GuardrailHooks: []string{"pre_action"},
	})
	hook := turn.guardrailCheckHook()
	if hook == nil {
		t.Fatal("active guardrails must yield a hook")
	}
	blocked, msg := hook(guardHookPreAction, "purchase item=widget qty=1")
	if !blocked {
		t.Fatal("a violation must block")
	}
	for _, want := range []string{"never spend money", "NOT performed", "different tool"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("block message must mention %q; got: %s", want, msg)
		}
	}
	// An INACTIVE hook point (pre_output not selected) does not fire — even a
	// violating candidate passes because that point wasn't enabled.
	if b, _ := hook(guardHookPreOutput, "anything"); b {
		t.Fatal("an unselected hook point must not fire")
	}
}

// TestGuardrailEscalatesOnRepeats pins the retry cap: after guardBlockEscalateAt
// blocks in one turn, the hook halts with a STOP message instead of another
// informative block (a compromised context can't probe indefinitely).
func TestGuardrailEscalatesOnRepeats(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"r","status":"violate","reason":"x"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "r", GuardrailHooks: []string{"pre_action"},
	})
	hook := turn.guardrailCheckHook()
	var lastMsg string
	for i := 0; i < guardBlockEscalateAt; i++ {
		_, lastMsg = hook(guardHookPreAction, "do the thing")
	}
	if !strings.HasPrefix(lastMsg, "STOP") {
		t.Fatalf("the %dth block should escalate to a STOP; got: %s", guardBlockEscalateAt, lastMsg)
	}
}

// TestGuardrailFailsOpenOnWardenError pins the fail-open: if the warden LLM
// errors, the action proceeds (not blocked) rather than bricking the turn.
func TestGuardrailFailsOpenOnWardenError(t *testing.T) {
	turn := guardTurn(t, errWardenLLM{}, AgentRecord{
		Name: "X", Guardrails: "r", GuardrailHooks: []string{"pre_action"},
	})
	blocked, _ := turn.guardrailCheckHook()(guardHookPreAction, "do the thing")
	if blocked {
		t.Fatal("a warden infra error must fail OPEN (allow), not block every action")
	}
}

// TestGuardrailHookConstantsMatchCore pins that the app hook labels are the
// canonical core ones — the loop calls GuardrailCheck with the core strings.
func TestGuardrailHookConstantsMatchCore(t *testing.T) {
	if guardHookPreInput != GuardHookPreInput || guardHookPreAction != GuardHookPreAction || guardHookPreOutput != GuardHookPreOutput || guardHookPeriodic != GuardHookPeriodic {
		t.Fatal("app guardrail hook constants must alias the core constants (or the loop won't match)")
	}
}

// TestPreInputInjectsSteerAwayDirective pins the pre_input pre-pass: when the
// incoming request violates a guardrail whose pre_input hook is on, a leading
// system directive is prepended so the model is steered off BEFORE round 1 —
// the fix for a salary range leaking in an interim narration turn that
// pre_output (terminal only) never sees.
func TestPreInputInjectsSteerAwayDirective(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never mention salary or wages","status":"violate","reason":"the request asks for pay"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "WiWee", Guardrails: "never mention salary or wages", GuardrailHooks: []string{"pre_input"},
	})
	in := []Message{{Role: "user", Content: "How much does Rory make?"}}
	out := turn.applyInputGuardrail(in)
	if len(out) != len(in)+1 {
		t.Fatalf("a flagged request must prepend one directive; got %d msgs", len(out))
	}
	if out[0].Role != "system" {
		t.Fatalf("the directive must lead as a system message; got role %q", out[0].Role)
	}
	for _, want := range []string{"never mention salary or wages", "interim narration", "deflect"} {
		if !strings.Contains(out[0].Content, want) {
			t.Fatalf("directive must mention %q; got: %s", want, out[0].Content)
		}
	}
	// The directive must NOT coach the model to cite a rule — that reveals the
	// constraint (the "per your rules" leak we're closing).
	if strings.Contains(out[0].Content, "off-limits because") {
		t.Fatal("directive must not model rule-citing decline language")
	}
	// The original request is preserved after the directive.
	if out[len(out)-1].Content != "How much does Rory make?" {
		t.Fatal("the user's request must survive intact after the directive")
	}
}

// TestPreInputJudgesFollowUpWithContext pins the bypass fix: a bare "Why?"
// after a declined salary question must be judged WITH the prior turns, so the
// warden's candidate carries the earlier "How much does Rory make?" — otherwise
// the one-word follow-up slips the guard and the model answers what it just
// declined.
func TestPreInputJudgesFollowUpWithContext(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never mention salary","status":"violate","reason":"the follow-up presses for the withheld pay"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "WiWee", Guardrails: "never mention salary or wages", GuardrailHooks: []string{"pre_input"},
	})
	convo := []Message{
		{Role: "user", Content: "How much does Rory make?"},
		{Role: "assistant", Content: "I'll pass on that one."},
		{Role: "user", Content: "Why?"},
	}
	out := turn.applyInputGuardrail(convo)
	if len(out) != len(convo)+1 || out[0].Role != "system" {
		t.Fatal("a context-implicated follow-up must still get a directive")
	}
	// The warden must have SEEN the prior salary question, not just "Why?".
	if !strings.Contains(stub.lastMsg, "How much does Rory make?") {
		t.Fatalf("pre_input candidate must carry the conversation window; warden saw: %s", stub.lastMsg)
	}
	if !strings.Contains(stub.lastMsg, "Why?") {
		t.Fatalf("pre_input candidate must include the current request; warden saw: %s", stub.lastMsg)
	}
}

// TestPreInputInertWhenHookOff pins that pre_input does nothing (no warden call,
// no injection) when the agent has guardrails but pre_input isn't the chosen
// hook — a "never spend money" rule enforced only at pre_action must not gate
// every incoming question.
func TestPreInputInertWhenHookOff(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"r","status":"violate","reason":"x"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "never spend money", GuardrailHooks: []string{"pre_action"},
	})
	in := []Message{{Role: "user", Content: "anything"}}
	out := turn.applyInputGuardrail(in)
	if len(out) != len(in) {
		t.Fatal("pre_input off → the message slice must pass through untouched")
	}
}

// TestPreInputComplyPassesThrough pins that a benign request (warden says
// comply) is not gated even when pre_input is on.
func TestPreInputComplyPassesThrough(t *testing.T) {
	stub := &wardenStubLLM{reply: `{"verdicts":[{"rule":"never mention salary","status":"comply","reason":"unrelated"}]}`}
	turn := guardTurn(t, stub, AgentRecord{
		Name: "X", Guardrails: "never mention salary", GuardrailHooks: []string{"pre_input"},
	})
	in := []Message{{Role: "user", Content: "what's the weather?"}}
	if out := turn.applyInputGuardrail(in); len(out) != len(in) {
		t.Fatal("a complying request must pass through with no directive")
	}
}

// TestPreInputFailsOpen pins fail-open: a warden infra error must let the
// request through (unchecked, loudly) rather than gagging the agent.
func TestPreInputFailsOpen(t *testing.T) {
	turn := guardTurn(t, errWardenLLM{}, AgentRecord{
		Name: "X", Guardrails: "never mention salary", GuardrailHooks: []string{"pre_input"},
	})
	in := []Message{{Role: "user", Content: "How much does Rory make?"}}
	if out := turn.applyInputGuardrail(in); len(out) != len(in) {
		t.Fatal("a warden error at pre_input must fail OPEN (pass through), not block")
	}
}

// TestAgentHasOutputGuardrail pins which hooks force the runner to buffer the
// stream (so a blocked reply never flashes on screen): pre_output and periodic
// judge output prose; pre_input/pre_action do not; no rules = inert.
func TestAgentHasOutputGuardrail(t *testing.T) {
	cases := []struct {
		name  string
		agent AgentRecord
		want  bool
	}{
		{"pre_output", AgentRecord{Guardrails: "r", GuardrailHooks: []string{"pre_output"}}, true},
		{"periodic", AgentRecord{Guardrails: "r", GuardrailHooks: []string{"periodic"}}, true},
		{"pre_input only", AgentRecord{Guardrails: "r", GuardrailHooks: []string{"pre_input"}}, false},
		{"pre_action only", AgentRecord{Guardrails: "r", GuardrailHooks: []string{"pre_action"}}, false},
		{"no rules", AgentRecord{}, false},
	}
	for _, c := range cases {
		if got := agentHasOutputGuardrail(c.agent); got != c.want {
			t.Errorf("%s: agentHasOutputGuardrail = %v, want %v", c.name, got, c.want)
		}
	}
}
