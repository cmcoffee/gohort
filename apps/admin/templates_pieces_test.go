package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

// The Templates dialog's checklist of everything in the store is read when
// the dialog opens, never while the page is built: building it read every
// artifact of every kind for every user on every administrator page.
func TestTheTemplatesChecklistLoadsWhenTheDialogOpens(t *testing.T) {
	a := &AdminApp{}
	b, err := json.Marshal(a.templatesSection())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"options_source":"api/templates/pieces"`) {
		t.Error("the pieces checklist does not load from api/templates/pieces")
	}
}
