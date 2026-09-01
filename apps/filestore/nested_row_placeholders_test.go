package filestore

import (
	"os"
	"regexp"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The folder panel nests two tables — commands, and the toolboxes those
// commands were mapped into — inside a row of the stores table. A nested
// component's URL placeholders are filled TWICE: once at expand time against
// the STORE row, and again per inner row. The store row answers first, so any
// placeholder it can fill never reaches the inner row at all.
//
// This is not theoretical. Both nested tables addressed their rows by {name},
// and a store row has a name: Map opened a conversation about a command called
// after the folder (404), and Detach removed an attachment named after the
// folder — matching nothing, succeeding, and reporting that it had worked. Both
// looked correct in the source and in the rendered page.
//
// Inner rows are addressed by {id} instead. These tests hold the two halves of
// what makes that safe: the templates ask for id, and a store row has no id to
// answer with.

// A store row must not be able to answer the placeholders the inner tables use
// to name their own rows. If a field is added to storeRows that collides, the
// nested table silently addresses the wrong record — so the collision is caught
// here rather than in a 404 nobody can explain.
func TestInnerRowKeysAreNotShadowedByTheStoreRow(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	if _, err := SaveStore(app.DB, Store{Name: "Support bundles", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	rows := app.storeRows()
	if len(rows) != 1 {
		t.Fatalf("want one store row, got %d", len(rows))
	}
	for _, key := range []string{"id"} {
		if _, taken := rows[0][key]; taken {
			t.Errorf("a store row now answers {%s}, which the nested commands and "+
				"toolboxes tables use to address their OWN rows — the inner value "+
				"will never be substituted. Rename one of them.", key)
		}
	}
}

// The other half: the templates themselves. A row-scoped URL inside the folder
// panel must not reach for a placeholder the store row fills.
func TestNestedRowURLsDoNotAddressRowsByName(t *testing.T) {
	raw, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	// Every filestore URL template in the file, with the ones the OUTER stores
	// table owns removed — those legitimately read the store row.
	url := regexp.MustCompile(`"/filestore/api/[^"]*"`)
	for _, m := range url.FindAllString(string(raw), -1) {
		if strings.HasPrefix(m, `"/filestore/api/stores`) || strings.HasPrefix(m, `"/filestore/api/upload`) {
			continue
		}
		if strings.Contains(m, "{name}") {
			t.Errorf("%s addresses a nested row by {name}; the store row it is "+
				"expanded from answers that first. Use {id}.", m)
		}
	}
}
