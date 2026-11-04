package main

// Every knob is filed under a category the Tuning tab lists.
//
// A knob's Category is a free string, and the Tuning tab makes one rail entry
// per distinct string. A misspelling ("Model Calls") or a knob filed under one
// of the retired unit buckets ("Limits", "Timeouts") makes a rail entry of its
// own, beside the subject it belongs to, and nothing warns. Here every app is
// linked in, so every registration is in the registry.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestEveryTunableIsFiledUnderAListedCategory(t *testing.T) {
	listed := map[string]bool{}
	for _, c := range TunableCategories {
		listed[c] = true
	}
	for _, s := range AllTunableSpecs() {
		if !listed[s.Category] {
			t.Errorf("%s (%q) is filed under %q, which is not in core.TunableCategories", s.Key, s.Label, s.Category)
		}
	}
}
