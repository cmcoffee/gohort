package ui

import (
	"encoding/json"
	"strings"
	"testing"
)

// A heading with no field under it is a menu entry that opens onto nothing.
// The form drops it as it is served, a trailing one included, and keeps every
// heading that has fields.
func TestAFormDropsAHeadingWithNothingUnderIt(t *testing.T) {
	fields := []FormField{
		{Field: "name", Label: "Name"},
		{Type: "header", Label: "Delegation"},
		{Type: "header", Label: "Intake form"},
		{Field: "intake_form", Label: "Intake form (JSON)"},
		{Type: "header", Label: "Leftover"},
	}
	if got := EmptyHeadings(fields); strings.Join(got, ",") != "Delegation,Leftover" {
		t.Fatalf("EmptyHeadings = %v", got)
	}
	b, err := json.Marshal(FormPanel{Fields: fields, Steps: []FormStep{{Fields: fields}}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, `"Delegation"`) || strings.Contains(s, `"Leftover"`) {
		t.Errorf("an empty heading was served: %s", s)
	}
	if strings.Count(s, `"Intake form"`) != 2 || strings.Count(s, `"intake_form"`) != 2 {
		t.Errorf("a heading with fields, or a field, went missing: %s", s)
	}
}
