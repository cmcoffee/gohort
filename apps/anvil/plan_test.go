package anvil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/agenthost"
)

// The plan is set, ticked step by step, and shown as one checklist card per
// plan; a step not in it, or a plan of nothing, is refused.
func TestThePlanIsAChecklistTheAgentTicks(t *testing.T) {
	var cards []agenthost.Block
	c := &toolCtx{sessionID: "s1", emitBlock: func(b agenthost.Block) { cards = append(cards, b) }}
	tool := c.planTool()
	call := func(args map[string]any) (string, error) { return tool.Handler(context.Background(), args) }
	if _, err := call(map[string]any{"done": []any{float64(1)}}); err == nil {
		t.Error("a step was ticked with no plan")
	}
	out, err := call(map[string]any{"steps": []any{"Read the parser", "Fix the bound", "Run the tests"}})
	if err != nil || !strings.Contains(out, "0 of 3 done. Next: Read the parser") {
		t.Fatalf("%q %v", out, err)
	}
	out, err = call(map[string]any{"done": []any{float64(1), float64(2)}})
	if err != nil || !strings.Contains(out, "2 of 3 done. Next: Run the tests") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := call(map[string]any{"done": []any{float64(4)}}); err == nil {
		t.Error("a step not in the plan was ticked")
	}
	if _, err := call(map[string]any{"steps": []any{" "}}); err == nil {
		t.Error("a plan of nothing was set")
	}
	last := cards[len(cards)-1]
	if last.Type != planBlock || last.ID != cards[0].ID || !strings.Contains(last.Data["steps"], `"done":true`) || last.Text != "2 of 3 done" {
		t.Errorf("the card: %+v (first %s)", last, cards[0].ID)
	}
	call(map[string]any{"steps": []any{"Start over"}})
	if cards[len(cards)-1].ID == cards[0].ID {
		t.Error("a new plan replaced the old plan's card instead of getting its own")
	}
}

// The workspace shows each step as it runs, in Anvil's words, with no
// Activity pane repeating them, and the agent may plan only where it may
// change things.
func TestTheWorkspaceShowsStepsInAnvilsWords(t *testing.T) {
	panel := (&Anvil{}).chatPanel(nil, "", "", nil)
	if !panel.ShowSteps || !panel.HideActivity {
		t.Errorf("show steps %v, hide activity %v", panel.ShowSteps, panel.HideActivity)
	}
	head := anvilHead([]Project{{ID: "p", Name: "x"}}, "u", true).Render()
	if !strings.Contains(head, "uiRegisterStepSummary") || !strings.Contains(head, planBlock) {
		t.Error("the page does not word its steps or render the plan")
	}
	has := func(c *toolCtx) bool {
		for _, td := range c.codeTools() {
			if td.Tool.Name == "plan" {
				return true
			}
		}
		return false
	}
	if !has(&toolCtx{}) || has(&toolCtx{readOnly: true}) {
		t.Error("the plan tool is offered where nothing may change, or not where it may")
	}
}

// Anvil's step words read its tools' real outputs.
func TestAnvilsStepWordsReadItsOutputs(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := `var reg = {}, groups = {}, words = {}, turn = null;
var window = {uiRegisterStepSummary: function(n, f) { reg[n] = f; },
  uiRegisterStepGroup: function(k, names, f) { names.forEach(function(n) { groups[n] = k; }); words[k] = f; },
  uiRegisterTurnSummary: function(f) { turn = f; }};
` + anvilStepSummariesJS + `
var cases = [
  [reg.read_file({path: 'main.go'}, null), 'Reading main.go'],
  [reg.read_file({path: 'main.go'}, 'main.go (lines 1-3 of 3):\n   1  package main'), 'Read main.go (lines 1-3 of 3)'],
  [reg.search_code({pattern: 'func main'}, 'main.go:3: func main() {}\nlib.go:9: func main2() {}'), 'Searched for "func main": 2 hits'],
  [reg.search_code({pattern: 'zz'}, 'No matches for zz in the project (4 files searched).'), 'Searched for "zz": no matches'],
  [reg.edit_file({path: 'main.go'}, 'Edited main.go (+2 −0). The change is recorded and can be reverted.'), 'Edited main.go (+2 −0)'],
  [reg.run({command: 'go test ./...'}, null), 'Running "go test ./..."'],
  [reg.run({command: 'go test ./...'}, 'Command failed (exit status 1):\nFAIL'), 'Failed: "go test ./..."'],
  [reg.check({}, 'The check passed (go test ./...) in 3s.\nok'), 'The check passed (go test ./...) in 3s.'],
  [reg.plan({}, 'Plan: 1 of 3 done. Next: Fix the bound'), 'Plan: 1 of 3 done'],
  // Looking around shares a line; a finished turn says what it did.
  [['read_file', 'list_files', 'search_code', 'git', 'plan'].map(function(n) { return groups[n]; }).join(','), 'look,look,look,look,look'],
  [words.look([{name: 'plan'}, {name: 'plan'}]), 'Updated the plan'],
  [groups.edit_file === undefined && groups.run === undefined, true],
  [words.look([{name: 'read_file'}, {name: 'read_file'}, {name: 'read_file'}, {name: 'search_code'}, {name: 'search_code'}]), 'Read 3 files, searched twice'],
  [words.look([{name: 'search_code'}, {name: 'git'}]), 'Searched once, looked at git'],
  [turn([{name: 'read_file'}, {name: 'edit_file'}, {name: 'create_file'}, {name: 'run'}, {name: 'check'}]), 'Read 1 file, changed 2 files, ran 1 command, ran the check'],
  [turn([{name: 'read_file'}, {name: 'read_file'}, {name: 'read_file'}]), 'Read 3 files'],
  [turn([{name: 'search_code'}, {name: 'list_files'}]), 'Listed 1 folder, searched once'],
];
var bad = cases.filter(function(c) { return c[0] !== c[1]; });
bad.forEach(function(c) { console.log('got ' + JSON.stringify(c[0]) + ' want ' + JSON.stringify(c[1])); });
process.exit(bad.length ? 1 : 0);
`
	f := filepath.Join(t.TempDir(), "steps.js")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The plan card's script parses.
func TestThePlanCardScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	f := filepath.Join(t.TempDir(), "plan.js")
	if err := os.WriteFile(f, []byte("var render = "+anvilPlanBlockJS+";\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
