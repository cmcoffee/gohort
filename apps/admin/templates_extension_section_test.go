package admin

import (
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Save as template appears on the Extensions page for an administrator and
// for nobody else, and its dialog reaches the admin API from a page that is
// not under it.
func TestTemplatesSectionIsForAdministratorsOnly(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })

	before := len(ExtensionSectionEntries())
	(&AdminApp{}).registerTemplatesExtensionSection("/admin")
	entries := ExtensionSectionEntries()
	if len(entries) != before+1 {
		t.Fatalf("no section was registered")
	}
	var entry ExtensionSectionEntry
	for _, e := range entries {
		if e.Order == 90 {
			entry = e
		}
	}
	r := httptest.NewRequest("GET", "/extensions", nil)
	if _, ok := entry.Build(r, "bob"); ok {
		t.Error("a user who is not an administrator sees Save as template")
	}
	sec, ok := entry.Build(r, "root")
	if !ok {
		t.Fatal("an administrator does not see Save as template")
	}
	modal, isModal := sec.Body.(ui.ModalButton)
	if !isModal {
		t.Fatalf("the section is not the Save as template dialog: %T", sec.Body)
	}
	form, _ := modal.Body.(ui.FormPanel)
	if form.PostURL != "/admin/api/templates/save" {
		t.Errorf("the dialog posts to %q, which from /extensions is not the admin API", form.PostURL)
	}
	for _, f := range form.Fields {
		if f.Field == "pieces" && f.OptionsSource != "/admin/api/templates/pieces" {
			t.Errorf("the pieces list loads from %q", f.OptionsSource)
		}
	}
	// The Administrator page keeps its relative URLs and its table refresh.
	own := saveTemplateModal("").Body.(ui.FormPanel)
	if own.PostURL != "api/templates/save" || len(own.Invalidate) != 1 {
		t.Errorf("the admin page's dialog changed: %q %v", own.PostURL, own.Invalidate)
	}
}
