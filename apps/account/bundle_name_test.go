package account

import "testing"

// An exported app downloads as the .gohortapp Builder packs, a tool as a
// .gohorttool, and anything else as a .gohort.json; the content is the one
// bundle format either way.
func TestAnExportIsNamedForWhatItHolds(t *testing.T) {
	for typ, want := range map[string]string{
		"custom_app": "voidrunner.gohortapp",
		"tool":       "voidrunner.gohorttool",
		"skill":      "voidrunner.gohort.json",
		"":           "voidrunner.gohort.json",
	} {
		if got := downloadName("voidrunner", typ); got != want {
			t.Errorf("%q: %s, want %s", typ, got, want)
		}
	}
}
