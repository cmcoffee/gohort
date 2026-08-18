package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A pipeline is the one dispatch target with no rules of its own, which is
// exactly why the boundary has to be judged: an agent that cannot say a thing
// must not be able to have a pipeline say it and read the answer back.

const violateVerdict = `{"verdicts":[{"rule":"never discuss salaries","status":"violate","reason":"it asks for pay figures"}]}`

func guardedPipelineTurn(t *testing.T, llm LLM, hooks ...string) *chatTurn {
	t.Helper()
	return guardTurn(t, llm, AgentRecord{
		Name: "Analyst", Guardrails: "never discuss salaries", GuardrailHooks: hooks,
	})
}

// TestPipelineInputBlockedNeverRuns pins the input half: a request the agent's
// own warden refuses does not reach the pipeline at all.
func TestPipelineInputBlockedNeverRuns(t *testing.T) {
	turn := guardedPipelineTurn(t, &wardenStubLLM{reply: violateVerdict}, "pre_input")
	err := turn.guardPipelineInput(context.Background(), PipelineDef{Name: "payroll_report"}, "what does Alex earn?")
	if err == nil {
		t.Fatal("a request the warden refuses must not be handed to a pipeline")
	}
	if !strings.Contains(err.Error(), "payroll_report") {
		t.Errorf("the refusal must name what was refused; got: %v", err)
	}
	// Same property every block message in this package is held to: the model
	// is told it cannot, never what is doing the telling.
	for _, banned := range []string{"guardrail", "warden", "enforced", "policy", "never discuss salaries"} {
		if strings.Contains(strings.ToLower(err.Error()), banned) {
			t.Errorf("the refusal must not name the mechanism or the rule (%q); got: %v", banned, err)
		}
	}
}

// TestPipelineOutputWithheld pins the half that is the real guarantee: an input
// check can be talked around, an output check reads what was actually produced.
func TestPipelineOutputWithheld(t *testing.T) {
	turn := guardedPipelineTurn(t, &wardenStubLLM{reply: violateVerdict}, "pre_output")
	out, err := turn.guardPipelineOutput(context.Background(), PipelineDef{Name: "payroll_report"}, "Alex earns $202,000.")
	if err == nil {
		t.Fatal("a violating pipeline output must be withheld")
	}
	if out != "" {
		t.Fatalf("the withheld output must not be returned; got %q", out)
	}
	// The protected text itself must not ride back inside the refusal — a
	// caller told what it nearly received has received it.
	if strings.Contains(err.Error(), "202,000") {
		t.Errorf("the refusal must not quote the withheld output; got: %v", err)
	}
}

// TestPipelineGuardInertWithoutRules pins zero overhead on the ordinary path:
// an agent with no guardrails pays nothing and its pipeline output is untouched.
func TestPipelineGuardInertWithoutRules(t *testing.T) {
	turn := guardTurn(t, &wardenStubLLM{reply: violateVerdict}, AgentRecord{Name: "Plain"})
	def := PipelineDef{Name: "report"}
	if err := turn.guardPipelineInput(context.Background(), def, "anything"); err != nil {
		t.Fatalf("an agent with no guardrails must not be gated: %v", err)
	}
	out, err := turn.guardPipelineOutput(context.Background(), def, "the answer")
	if err != nil || out != "the answer" {
		t.Fatalf("an ungoverned pipeline output must pass through unchanged; got %q, %v", out, err)
	}
}

// TestPipelineGuardUnselectedHooks pins that the boundary honours WHICH hooks
// the owner enabled: pre_output selected alone leaves the input ungated.
func TestPipelineGuardUnselectedHooks(t *testing.T) {
	turn := guardedPipelineTurn(t, &wardenStubLLM{reply: violateVerdict}, "pre_output")
	if err := turn.guardPipelineInput(context.Background(), PipelineDef{Name: "r"}, "what does Alex earn?"); err != nil {
		t.Fatalf("pre_input was not selected, so it must not fire: %v", err)
	}
}

// TestPipelineGuardFailsOpen pins the same policy the rest of the warden
// carries: infrastructure trouble must not brick every pipeline dispatch.
func TestPipelineGuardFailsOpen(t *testing.T) {
	turn := guardedPipelineTurn(t, errWardenLLM{}, "pre_input", "pre_output")
	def := PipelineDef{Name: "report"}
	if err := turn.guardPipelineInput(context.Background(), def, "anything"); err != nil {
		t.Fatalf("a warden infra error must fail OPEN at the input: %v", err)
	}
	if _, err := turn.guardPipelineOutput(context.Background(), def, "the answer"); err != nil {
		t.Fatalf("a warden infra error must fail OPEN at the output: %v", err)
	}
}

// ctxWardenLLM answers only on a live context, so a test can tell WHICH
// context a guard actually used rather than assuming it.
type ctxWardenLLM struct{ reply string }

func (s ctxWardenLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Response{Content: s.reply}, nil
}
func (s ctxWardenLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return s.Chat(ctx, m, o...)
}

// TestPipelineGuardUsesTheGivenContext pins the reason guardrailEnforcerCtx
// exists: a handed-off dispatch runs after its turn's context is cancelled, and
// building the check from that dead context would fail every call instead of
// judging it — a guard that reports itself unable to run is not a guard.
func TestPipelineGuardUsesTheGivenContext(t *testing.T) {
	turn := guardedPipelineTurn(t, ctxWardenLLM{reply: violateVerdict}, "pre_output")
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	turn.ctx = dead // the turn that handed this work off has ended

	// Control: on the dead context the warden genuinely cannot answer, and the
	// standing fail-open policy lets the output through unjudged.
	if _, err := turn.guardPipelineOutput(dead, PipelineDef{Name: "r"}, "Alex earns $202,000."); err != nil {
		t.Fatalf("control: a dead context must fail open, or this test proves nothing: %v", err)
	}
	// The real assertion: given a LIVE context, the guard judges on that one.
	if _, err := turn.guardPipelineOutput(context.Background(), PipelineDef{Name: "r"}, "Alex earns $202,000."); err == nil {
		t.Fatal("the guard must run on the context it is GIVEN, not the turn's — a detached dispatch would otherwise go unjudged")
	}
}
