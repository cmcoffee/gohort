package knowledge

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Find sources is handed to the collection page, which mounts it in the From
// the web area under Articles to fetch, for whoever may add documents and
// nobody else; nothing in it can close the script it rides in.
func TestFindSourcesSitsInFromTheWebForThoseWhoMayAdd(t *testing.T) {
	withStores(t)
	app := &KnowledgeApp{}
	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store in this configuration")
	}
	SaveCollection(owner, Collection{ID: "col-1", Owner: "alice", Name: "Runbooks</script>",
		AllowedUsers: []string{"bob", "carol"}, Contributors: []string{"bob"}})
	script := app.findSourcesScript("alice", "col-1")
	for _, want := range []string{"window.knowledgeFindSources = ", `"type":"stack"`, `"type":"action_list"`, "/orchestrate/api/collections/col-1/suggest/add"} {
		if !strings.Contains(script, want) {
			t.Errorf("the owner's Find sources has no %s:\n%s", want, script)
		}
	}
	if strings.Count(script, "</script>") != 1 {
		t.Errorf("something in the config closes the script:\n%s", script)
	}
	if app.findSourcesScript("bob", "col-1") == "" {
		t.Error("someone who may add has no Find sources")
	}
	if app.findSourcesScript("carol", "col-1") != "" {
		t.Error("a reader has Find sources")
	}
	from := documentsDetailBody[strings.Index(documentsDetailBody, "From the web"):]
	from = from[:strings.Index(from, "Documents")]
	if i, j := strings.Index(from, "docs-articles-list"), strings.Index(from, `id="docs-find-sources"`); i < 0 || j < i {
		t.Error("Find sources is not in the From the web area under Articles to fetch")
	}
	if !strings.Contains(documentsDetailAssets, "uiMountComponent(window.knowledgeFindSources") {
		t.Error("the page does not mount Find sources")
	}
}
