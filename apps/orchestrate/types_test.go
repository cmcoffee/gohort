package orchestrate

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The intake form as the editor's list sends it: choices as one comma line,
// a name left blank, a row with nothing in it. Choices are split, the name is
// made from the label, the empty row is dropped, and what the list does not
// show (a button's follow-up question, a ShowWhen) survives the edit.
func TestTheIntakeFormListDecodes(t *testing.T) {
	var spec IntakeFormSpec
	body := `[
		{"label": "Company name", "type": "text", "required": true},
		{"label": "Deadline", "type": "select", "options": "This week, This month , No rush"},
		{"name": "kind", "label": "What to do", "type": "button", "options": ["Fix", "Build"], "detail": {"Fix": "Fix what?"}, "show_when": "x"},
		{"label": "", "type": "text"}
	]`
	if err := json.Unmarshal([]byte(body), &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec) != 3 {
		t.Fatalf("fields %+v", spec)
	}
	if spec[0].Name != "company_name" || !spec[0].Required {
		t.Errorf("a name made from the label: %+v", spec[0])
	}
	if strings.Join(spec[1].Options, "|") != "This week|This month|No rush" {
		t.Errorf("choices from a comma line: %q", spec[1].Options)
	}
	if spec[2].Name != "kind" || strings.Join(spec[2].Options, "|") != "Fix|Build" || spec[2].Detail["Fix"] != "Fix what?" || spec[2].ShowWhen != "x" {
		t.Errorf("an authored field lost its finer parts: %+v", spec[2])
	}

	// The textarea's string form still decodes, the same way.
	var old IntakeFormSpec
	if err := json.Unmarshal([]byte(`"[{\"label\":\"Topics\",\"type\":\"checklist\",\"options\":\"AI, Finance\"}]"`), &old); err != nil || len(old) != 1 || old[0].Name != "topics" || len(old[0].Options) != 2 {
		t.Errorf("the string form: %+v %v", old, err)
	}
	if err := json.Unmarshal([]byte(`[{"label":"x","options":7}]`), &old); err == nil {
		t.Error("options that are neither a line nor a list were accepted")
	}
	if got := intakeFieldName("  What's your  budget? (USD) "); got != "what_s_your_budget_usd" {
		t.Errorf("name from a label: %q", got)
	}
}

// The agent page edits the intake form as that list, with the choices only
// where a field has them, not as a box of JSON.
func TestTheAgentPageEditsTheIntakeFormAsAList(t *testing.T) {
	src, err := os.ReadFile("page_agent.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{`Field: "intake_form", Type: "rows"`, `AddLabel:    "+ Add field"`, `HideWhen: "type:!select|checklist|button"`} {
		if !strings.Contains(s, want) {
			t.Errorf("page_agent.go no longer has %s", want)
		}
	}
	if strings.Contains(s, "Intake form (JSON)") {
		t.Error("the intake form is a JSON box again")
	}
}

// Action limits read the lines every API client and the authoring tools send
// (a line that does not parse is dropped, as always) and the list's rows (a
// half-filled row is an error, not a silent drop).
func TestActionLimitsReadLinesAndRows(t *testing.T) {
	var m ActionQuotaMap
	if err := json.Unmarshal([]byte(`["send_email = 3", "nonsense", "x = 0"]`), &m); err != nil || len(m) != 1 || m["send_email"] != 3 {
		t.Errorf("lines: %v %v", m, err)
	}
	if err := json.Unmarshal([]byte(`[{"action":"a","limit":2},{"action":"b","limit":"4"},{"action":" ","limit":""}]`), &m); err != nil || len(m) != 2 || m["b"] != 4 {
		t.Errorf("rows: %v %v", m, err)
	}
	if err := json.Unmarshal([]byte(`[{"action":"a"}]`), &m); err == nil || !strings.Contains(err.Error(), "how many times") {
		t.Errorf("a row with no limit: %v", err)
	}
	if rows := (ActionQuotaMap{"b": 1, "a": 2}).Rows(); len(rows) != 2 || rows[0]["action"] != "a" {
		t.Errorf("rows are sorted by action: %v", rows)
	}
}
