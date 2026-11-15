package knowledge

import (
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// suggestedSourcesSection is Find sources and what it found, to tick
// (orchestrate collection_suggest.go): the guided setup's "Suggest sources,
// I'll pick", and there for any collection after.
//
// For whoever may add documents to it, the owner and contributors, since
// adding is what ticking does. Not on the deployment's own collections, which
// only an administrator changes, from its own page.
func (T *KnowledgeApp) suggestedSourcesSection(user, collectionID string) (ui.Section, bool) {
	if strings.TrimSpace(collectionID) == "" || strings.TrimSpace(user) == "" {
		return ui.Section{}, false
	}
	c, ok := LoadCollection(UserDB(CollectionsDB(), user), user, collectionID)
	if !ok || IsDeploymentScope(c) || !CollectionContributor(c, user) {
		return ui.Section{}, false
	}
	base := "/orchestrate/api/collections/" + url.PathEscape(collectionID) + "/suggest"
	return ui.Section{
		Title:    "Suggested sources",
		Subtitle: "Sources found for this collection, for you to choose from.",
		Detail: "Find sources writes searches from the collection's description, leaves out sites its rules say to skip and anything already in it, and puts sites the rules prefer first. " +
			"Nothing is added until you tick it. What you tick goes on Articles to fetch above and is fetched from there, so the list shows which are in.",
		Body: ui.Stack{Children: []ui.Component{
			ui.ActionList{
				Source:         base,
				LabelField:     "item",
				DescField:      "desc",
				HistoryField:   "history",
				PostTo:         base,
				ButtonText:     "Find sources",
				ProgressSource: base + "/progress",
				CancelTo:       base + "/stop",
				EmptyText:      "Suggesting sources needs web search, which is not set up on this server.",
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
		}},
	}, true
}

// fillSection is Fill it for me (orchestrate collection_fill.go): a plan
// drafted from the collection's description and rules, shown with its size
// and editable, then run with progress. Nothing runs until the plan is run.
// For whoever may add documents, as Suggested sources is.
func (T *KnowledgeApp) fillSection(user, collectionID string) (ui.Section, bool) {
	if strings.TrimSpace(collectionID) == "" || strings.TrimSpace(user) == "" {
		return ui.Section{}, false
	}
	c, ok := LoadCollection(UserDB(CollectionsDB(), user), user, collectionID)
	if !ok || IsDeploymentScope(c) || !CollectionContributor(c, user) {
		return ui.Section{}, false
	}
	base := "/orchestrate/api/collections/" + url.PathEscape(collectionID) + "/fill"
	return ui.Section{
		Title:    "Fill it for me",
		Subtitle: "A plan to fill this collection, shown with its size before anything runs.",
		Detail: "Draft a plan writes the web searches it would run from the collection's description and rules, and how many documents to add. " +
			"Change any of it, then run it: the searches run, pages that fit are fetched into the collection, and the run reports how far it has got. " +
			"It stays within the Ingest & autofill limits an administrator sets.",
		Body: ui.Stack{Children: []ui.Component{
			ui.ActionList{
				Source:         base,
				LabelField:     "item",
				DescField:      "desc",
				HistoryField:   "history",
				PostTo:         base,
				ButtonText:     "Draft a plan",
				ProgressSource: base + "/progress",
				CancelTo:       base + "/stop",
				EmptyText:      "Filling a collection needs web search, which is not set up on this server.",
				Invalidate:     []string{base + "/fields"},
			},
			ui.FormPanel{
				FieldsSource: base + "/fields",
				Source:       base + "/plan",
				PostURL:      base + "/run",
				SubmitLabel:  "Run this plan",
				RefreshOn:    []string{base + "/fields"},
				Invalidate:   []string{base},
			},
		}},
	}, true
}
