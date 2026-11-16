package knowledge

import (
	"encoding/json"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// findSources is Find sources and what it found, to tick (orchestrate
// collection_suggest.go): the guided setup's "Suggest sources, I'll pick",
// and there for any collection after. It sits in the From the web area of
// the collection page, under Articles to fetch, which is where what is
// ticked goes and is fetched from.
//
// For whoever may add documents to it, the owner and contributors, since
// adding is what ticking does. Not on the deployment's own collections, which
// only an administrator changes, from its own page.
func (T *KnowledgeApp) findSources(user, collectionID string) (ui.Component, bool) {
	if strings.TrimSpace(collectionID) == "" || strings.TrimSpace(user) == "" {
		return nil, false
	}
	c, ok := LoadCollection(UserDB(CollectionsDB(), user), user, collectionID)
	if !ok || IsDeploymentScope(c) || !CollectionContributor(c, user) {
		return nil, false
	}
	base := "/orchestrate/api/collections/" + url.PathEscape(collectionID) + "/suggest"
	return ui.Stack{Children: []ui.Component{
		ui.ActionList{
			Source:         base,
			LabelField:     "item",
			DescField:      "desc",
			HistoryField:   "history",
			PostTo:         base,
			ButtonText:     "Find sources",
			ProgressSource: base + "/progress",
			CancelTo:       base + "/stop",
			EmptyText:      "Finding sources needs web search, which is not set up on this server.",
			// When a search or an add ends, the list below is redrawn.
			Invalidate: []string{base + "/fields"},
		},
		ui.FormPanel{
			FieldsSource: base + "/fields",
			PostURL:      base + "/add",
			SubmitLabel:  "Add ticked",
			RefreshOn:    []string{base + "/fields"},
			// The row above rejoins the add, showing how it goes.
			Invalidate: []string{base},
		},
	}}, true
}

// findSourcesScript hands findSources to the page, which mounts it in the
// From the web area: a script setting window.knowledgeFindSources, or ""
// for someone who may not add. json.Marshal escapes "<", so nothing in it
// can close the script.
func (T *KnowledgeApp) findSourcesScript(user, collectionID string) string {
	comp, ok := T.findSources(user, collectionID)
	if !ok {
		return ""
	}
	b, err := json.Marshal(comp)
	if err != nil {
		return ""
	}
	return "<script>window.knowledgeFindSources = " + string(b) + ";</script>"
}
