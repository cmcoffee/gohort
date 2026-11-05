package orchestrate

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// A heading and the list under it are one rule, the way a model writes a
// procedure: read a line at a time, the heading was a rule of its own that
// said nothing, and its steps rules that no longer said what they were about.
func TestAHeadingAndItsListAreOneRule(t *testing.T) {
	rules := "Be brief.\n" +
		"PROBLEM INVESTIGATION:\n" +
		"1. Check the logs first.\n" +
		"2) Confirm with the user before restarting anything.\n" +
		"\n" +
		"## Escalation\n" +
		"- Page the on-call engineer.\n" +
		"    and say which host.\n" +
		"**Never:**\n" +
		"* delete a backup\n" +
		"Plain rule after a list.\n" +
		"Cost note:\n" +
		"An unlisted sentence is not claimed by the heading above it."
	want := [][]string{
		{"Be brief."},
		{"PROBLEM INVESTIGATION:", "1. Check the logs first.", "2) Confirm with the user before restarting anything."},
		{"## Escalation", "- Page the on-call engineer.", "and say which host."},
		{"**Never:**", "* delete a backup"},
		{"Plain rule after a list."},
		{"Cost note:"},
		{"An unlisted sentence is not claimed by the heading above it."},
	}
	if got := ruleItems(rules); !reflect.DeepEqual(got, want) {
		t.Errorf("ruleItems:\n got %q\nwant %q", got, want)
	}
	// A list after a blank line belongs to no heading: its lines are rules.
	if got := ruleItems("Steps:\n\n- one\n- two"); len(got) != 3 {
		t.Errorf("a blank line ends a heading's list: %q", got)
	}
	// A list line with no heading over it is a rule of its own.
	if got := ruleItems("- one\n- two"); len(got) != 2 {
		t.Errorf("bullets with no heading are each a rule: %q", got)
	}
	// "#general" names a channel; it is not a markdown heading.
	if ruleHeading("#general: never post links") || ruleHeading("#general gets no links") {
		t.Error("a channel name read as a heading")
	}
}

// The prompt gives the agent the list under its heading, so each step still
// says what it is about.
func TestThePromptNestsAHeadingsList(t *testing.T) {
	got := renderRulesPromptSection("Be brief.\nPROBLEM INVESTIGATION:\n1. Check the logs first.\n2. Confirm before restarting.")
	want := "- Be brief.\n- PROBLEM INVESTIGATION:\n  1. Check the logs first.\n  2. Confirm before restarting.\n"
	if !strings.Contains(got, want) {
		t.Errorf("rules block:\n%s\nwant it to hold:\n%s", got, want)
	}
	shape := describeAgentPlainly(AgentRecord{Rules: "PROBLEM INVESTIGATION:\n1. Check the logs first."})
	found := false
	for _, line := range shape {
		if line == "PROBLEM INVESTIGATION: 1. Check the logs first." {
			found = true
		}
		if line == "PROBLEM INVESTIGATION:" {
			t.Errorf("the summary listed the heading as a rule: %q", shape)
		}
	}
	if !found {
		t.Errorf("the summary should list the heading with its steps: %q", shape)
	}
}

// A heading with nothing under it says nothing, and is refused where rules
// are written: Builder's create and update, and the agent page. Rules a save
// carries over unchanged are not, so such an agent still takes other edits.
func TestAHeadingWithNothingUnderItIsRefused(t *testing.T) {
	for _, ok := range []string{"", "Be brief.", "Steps:\n1. one", "Before a restart: ask first."} {
		if err := rulesProblem(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"PROBLEM INVESTIGATION:", "Be brief.\n## Escalation\nPage the on-call engineer."} {
		if err := rulesProblem(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}

	sess, parent, _ := addToolTestSess(t)
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": parent.ID, "rules": "PROBLEM INVESTIGATION:\nCheck the logs first."}, sess); err == nil ||
		!strings.Contains(err.Error(), "heading with no list") {
		t.Fatalf("update_agent took an empty heading: %v", err)
	}
	if rec, _ := loadAgent(sess.DB, parent.ID); rec.Rules != "" {
		t.Errorf("refused rules were saved: %q", rec.Rules)
	}
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": parent.ID, "rules": "PROBLEM INVESTIGATION:\n1. Check the logs first."}, sess); err != nil {
		t.Fatalf("update_agent refused a heading with its list: %v", err)
	}
	if _, err := (createAgentTool{}).RunWithSession(map[string]any{"name": "Fixer", "orchestrator_prompt": "Fix things.",
		"allowed_tools": []any{}, "rules": "Escalation:"}, sess); err == nil || !strings.Contains(err.Error(), "heading with no list") {
		t.Errorf("create_agent took an empty heading: %v", err)
	}

	// Stored before the check, and carried over by an edit to something else.
	rec, _ := loadAgent(sess.DB, parent.ID)
	rec.Rules = "Escalation:"
	saveAgent(sess.DB, rec)
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": parent.ID, "description": "fixes things"}, sess); err != nil {
		t.Errorf("an edit that leaves the rules alone was refused: %v", err)
	}
}

// A suggested rule is one line, and is added after a heading's list, not
// into it.
func TestAProposedRuleIsOneLineAndStaysOutOfAList(t *testing.T) {
	udb, turn := proposeRuleFixture(t)
	turn.askVia = func(prompt, detail string, yes []string) string { return yes[0] }
	for _, bad := range []string{"PROBLEM INVESTIGATION:\n1. Check the logs first.", "Escalation:"} {
		if _, err := turn.proposeRule(bad, "why"); err == nil {
			t.Errorf("proposed %q", bad)
		}
	}
	rec, _ := loadAgent(udb, "a1")
	rec.Rules = "Steps:\n1. Check the logs first."
	saveAgent(udb, rec)
	if _, err := turn.proposeRule("- Never restart without asking.", "why"); err != nil {
		t.Fatal(err)
	}
	rec, _ = loadAgent(udb, "a1")
	if got := ruleItems(rec.Rules); len(got) != 2 || got[1][0] != "- Never restart without asking." {
		t.Errorf("the added rule joined the list above it: %q", got)
	}
}

// The agent page's save refuses a heading with nothing under it, when the
// save changes the rules; one carrying stored rules over unchanged goes through.
func TestTheAgentPageRefusesAHeadingWithNothingUnderIt(t *testing.T) {
	app, udb := guardrailsPersistApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "X", Owner: "u", OrchestratorPrompt: "p", Rules: "Escalation:"}); err != nil {
		t.Fatal(err)
	}
	post := func(body string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		routed(app)(w, as(t, r, "u"))
		return w.Code
	}
	if code := post(`{"id":"a1","name":"X","orchestrator_prompt":"p","rules":"Escalation:","description":"edited"}`); code >= 300 {
		t.Errorf("a save carrying stored rules over unchanged was refused: %d", code)
	}
	if code := post(`{"id":"a1","name":"X","orchestrator_prompt":"p","rules":"Be brief.\nInvestigating:"}`); code != http.StatusBadRequest {
		t.Errorf("a heading with nothing under it was saved: %d", code)
	}
	if code := post(`{"id":"a1","name":"X","orchestrator_prompt":"p","rules":"Investigating:\n1. Check the logs first."}`); code >= 300 {
		t.Errorf("a heading with its list was refused: %d", code)
	}
}

// The agent page reads Rules the way the server does, or its editor would
// show one rule as several and save them apart again.
func TestThePageReadsRulesAsTheServerDoes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	page, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	src := regexp.MustCompile(`(?s)      function orchRuleItems\(text\) \{.*?\n      \}\n.*?      function orchRulesText\(items\) \{.*?\n      \}\n`).Find(page)
	if src == nil {
		t.Fatal("orchRuleItems and orchRulesText not found in the page")
	}
	inputs := []string{
		"Be brief.\nPROBLEM INVESTIGATION:\n1. Check the logs first.\n2) Confirm first.\n\n## Escalation\n- Page on-call.\n    and say which host.\n**Never:**\n* delete a backup\nPlain after.\nCost note:\nNot claimed.",
		"Steps:\n\n- one\n- two",
		"- one\n2. two",
		"#general: never post links",
	}
	inputsJSON, _ := json.Marshal(inputs)
	script := string(src) + "\nvar inputs = " + string(inputsJSON) + ";\nconsole.log(JSON.stringify(inputs.map(orchRuleItems)));\n" +
		// Saved and read back, a rule list is the same rules.
		"inputs.forEach(function(x) { var a = orchRuleItems(x), b = orchRuleItems(orchRulesText(a)); if (JSON.stringify(a) !== JSON.stringify(b)) { console.error('round trip changed ' + JSON.stringify(a) + ' to ' + JSON.stringify(b)); process.exit(1); } });\n" +
		"var saved = orchRulesText(['Steps:\\n1. one', '- after the list']); if (orchRuleItems(saved).length !== 2) { console.error('a rule after a list joined it: ' + JSON.stringify(saved)); process.exit(1); }\n"
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got [][]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	strip := regexp.MustCompile(`^\s*([-*]|\d+\.)\s*`)
	for i, in := range inputs {
		var want []string
		for _, item := range ruleItems(in) {
			if len(item) == 1 {
				want = append(want, strings.TrimSpace(strip.ReplaceAllString(item[0], "")))
				continue
			}
			want = append(want, strings.Join(item, "\n"))
		}
		// The page keeps an indent on a line that belongs by its indent alone;
		// the lines are the same once trimmed.
		for j := range got[i] {
			lines := strings.Split(got[i][j], "\n")
			for k := range lines {
				lines[k] = strings.TrimSpace(lines[k])
			}
			got[i][j] = strings.Join(lines, "\n")
		}
		if !reflect.DeepEqual(got[i], want) {
			t.Errorf("input %d:\npage   %q\nserver %q", i, got[i], want)
		}
	}
}

