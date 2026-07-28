package codewriter

import "testing"

// "Save as template" is a gesture people repeat after every tweak. If
// each save appended a row, the picker would fill with copies of the
// same name and no way to tell which is current.
func TestResolveTemplateID(t *testing.T) {
	existing := []TemplateRecord{
		{ID: "id-runbook", Name: "Runbook"},
		{ID: "id-notes", Name: "Meeting notes"},
	}
	cases := []struct {
		name, want string
	}{
		{"Runbook", "id-runbook"},
		{"runbook", "id-runbook"},     // case-insensitive
		{"  Runbook  ", "id-runbook"}, // free-text prompt, not a picker
		{"Meeting notes", "id-notes"},
		{"Postmortem", ""}, // new name -> fresh ID
		{"", ""},           // no name -> never match
		{"   ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveTemplateID(existing, c.name); got != c.want {
				t.Errorf("resolveTemplateID(%q) = %q, want %q", c.name, got, c.want)
			}
		})
	}
	if got := resolveTemplateID(nil, "Runbook"); got != "" {
		t.Errorf("empty store returned %q", got)
	}
}

// A saved template must be able to hold a name matching a built-in
// without the two colliding: they live in separate lists and only the
// saved one is addressable by ID.
func TestSavedTemplateMayShadowBuiltinName(t *testing.T) {
	saved := []TemplateRecord{{ID: "mine", Name: "Runbook"}} // also a built-in name
	if got := resolveTemplateID(saved, "Runbook"); got != "mine" {
		t.Errorf("expected the saved record to own its name, got %q", got)
	}
}
