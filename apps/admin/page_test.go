package admin

import "testing"

// TestToggleLabel: the redundant 0=off suffix is stripped from a bool tunable's
// label (any spacing / casing), while a plain label and a non-matching
// parenthetical are left untouched.
func TestToggleLabel(t *testing.T) {
	cases := map[string]string{
		"Finding conflict detection (0 = off)":  "Finding conflict detection",
		"Automatic entity extraction (0 = off)": "Automatic entity extraction",
		"Something (0=off)":                     "Something",
		"Something (0 = OFF)":                    "Something",
		"Grounding gate: unsourced figures":     "Grounding gate: unsourced figures",
		"Recall min score (0 = off)":             "Recall min score", // helper strips; caller only applies it to bool knobs
		"Chunk size (chars)":                     "Chunk size (chars)",
	}
	for in, want := range cases {
		if got := toggleLabel(in); got != want {
			t.Errorf("toggleLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
