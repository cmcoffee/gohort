package codewriter

import "strings"

// resolveTemplateID picks the record a save should write to: the
// existing template carrying the same name, else "" for a fresh ID.
//
// Name-matched replacement is what keeps the picker usable. "Save as
// template" is a gesture people repeat after every tweak, and appending
// a new row each time turns the list into six copies of "Runbook" with
// no way to tell which is current. Case- and space-insensitive because
// the name comes from a free-text prompt, not a picker.
func resolveTemplateID(existing []TemplateRecord, name string) string {
	want := strings.TrimSpace(name)
	if want == "" {
		return ""
	}
	for _, e := range existing {
		if strings.EqualFold(strings.TrimSpace(e.Name), want) {
			return e.ID
		}
	}
	return ""
}
