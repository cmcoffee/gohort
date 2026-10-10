package account

import "testing"

// An exported app downloads as the .oddjobapp Builder packs, a tool as a
// .oddjobtool, and anything else as a .oddjob.json; the content is the one
// bundle format either way.
func TestAnExportIsNamedForWhatItHolds(t *testing.T) {
	for typ, want := range map[string]string{
		"custom_app": "voidrunner.oddjobapp",
		"tool":       "voidrunner.oddjobtool",
		"skill":      "voidrunner.oddjob.json",
		"":           "voidrunner.oddjob.json",
	} {
		if got := downloadName("voidrunner", typ); got != want {
			t.Errorf("%q: %s, want %s", typ, got, want)
		}
	}
}
