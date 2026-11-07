package orchestrate

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The base carries what the agent is held to, from the agent the turn runs.
func TestTheLoopBaseCarriesTheAgentsLimits(t *testing.T) {
	turn := &chatTurn{agent: AgentRecord{ID: "a1", DailySpendUSD: 2, ActionQuotas: ActionQuotaMap{"send": 4},
		ThinkBudget: 300, Effort: "high", GuardrailDeclines: []string{"no"}}}
	cfg := turn.withLoopBase(AgentLoopConfig{SystemPrompt: "kept"})
	if cfg.SystemPrompt != "kept" {
		t.Error("the base overwrote a field it does not own")
	}
	if cfg.BudgetKey != "a1" || cfg.DailySpendUSD != 2 || cfg.ActionQuotas["send"] != 4 {
		t.Errorf("caps not carried: key %q, cap %v, quotas %v", cfg.BudgetKey, cfg.DailySpendUSD, cfg.ActionQuotas)
	}
	if cfg.ThinkBudget != 300 || cfg.Effort != "high" || len(cfg.GuardrailDeclines) != 1 {
		t.Errorf("think settings or declines not carried: %v %q %v", cfg.ThinkBudget, cfg.Effort, cfg.GuardrailDeclines)
	}
	if cfg.AskFirst == nil {
		t.Error("ask-first marks not consulted")
	}
}

// loopBaseFields are the fields withLoopBase sets. A literal it wraps must
// not set them too: the base would overwrite the value without a word.
var loopBaseFields = map[string]bool{"ActionQuotas": true, "BudgetKey": true, "DailySpendUSD": true,
	"SharedBudgets": true, "ThinkBudget": true, "Effort": true, "GuardrailCheck": true,
	"GuardrailObserve": true, "GuardrailActionGate": true, "GuardrailHalted": true,
	"GuardrailReject": true, "GuardrailDeclines": true, "AskFirst": true}

// narrowLoops build a loop that is not an agent's run (a pipeline stage, a
// publisher's single instruction) and so carries no agent limits of its own.
var narrowLoops = map[string]bool{"pipeline_tools.go": true, "publish_readback_runner.go": true, "publish_agent_runner.go": true}

// Every agent run starts from the loop base. The agents tool's run once built
// its config by hand and left the caps out, so an agent sent that way spent
// past its own cap; a new run path written the same way fails here instead.
func TestEveryAgentRunStartsFromTheLoopBase(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	wrapped := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fset := gotoken.NewFileSet()
		af, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		inBase := map[*ast.CompositeLit]bool{}
		ast.Inspect(af, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "withLoopBase" && len(call.Args) == 1 {
					if cl, ok := call.Args[0].(*ast.CompositeLit); ok {
						inBase[cl] = true
					}
				}
			}
			return true
		})
		ast.Inspect(af, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if id, ok := cl.Type.(*ast.Ident); !ok || id.Name != "AgentLoopConfig" {
				return true
			}
			at := fset.Position(cl.Pos())
			if !inBase[cl] {
				if !narrowLoops[f] {
					t.Errorf("%s:%d builds an AgentLoopConfig outside withLoopBase: an agent run there is not held to the agent's caps and guardrails", f, at.Line)
				}
				return true
			}
			wrapped++
			for _, e := range cl.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok && loopBaseFields[k.Name] {
						t.Errorf("%s:%d sets %s, which withLoopBase overwrites: set it after the call", f, at.Line, k.Name)
					}
				}
			}
			return true
		})
	}
	if wrapped < 7 {
		t.Errorf("found %d run paths on the loop base, want at least the seven (web, sync, continuing, agents tool, fire, worker step, lead-plan step)", wrapped)
	}
}
