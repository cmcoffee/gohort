package orchestrate

// A create or update that cannot be done is refused before anything is
// written: an update of an app that does not exist, a create with no name or
// none a slug can be made from, a create over an app that already has the
// slug, and notes over their cap.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAnAppWriteThatCannotBeDoneWritesNothing(t *testing.T) {
	pinRootDB(t)
	turn := &chatTurn{user: "u"}
	page := []any{map[string]any{"kind": "html", "html": "<!DOCTYPE html><html><body>hi</body></html>"}}
	if _, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Weather", "sections": page}, false); err != nil {
		t.Fatal(err)
	}
	before := len(ListAppSpecs("u"))
	for _, tc := range []struct {
		name     string
		args     map[string]any
		isUpdate bool
		want     string
	}{
		{"an update of an app that does not exist", map[string]any{"id": "nope", "sections": page}, true, "no matching app to update"},
		{"a create with no name", map[string]any{"sections": page}, false, "name is required"},
		{"a create no slug can be made from", map[string]any{"name": "!!!", "sections": page}, false, "could not derive a slug"},
		{"a create over an existing slug", map[string]any{"name": "Weather", "sections": page}, false, "already exists"},
		{"notes over the cap", map[string]any{"name": "Weather 2", "sections": page, "notes": strings.Repeat("x", (AppSpec{}).NotesCap()+10)}, false, "over the"},
	} {
		out, err := turn.appDefCreateOrUpdate(tc.args, tc.isUpdate)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want a refusal naming %q, got %q / %v", tc.name, tc.want, out, err)
		}
		if n := len(ListAppSpecs("u")); n != before {
			t.Errorf("%s: an app was written anyway (%d apps, was %d)", tc.name, n, before)
		}
	}
	if _, ok := LoadAppSpec("u", "nope"); ok {
		t.Error("the refused update made the app it could not find")
	}
}
