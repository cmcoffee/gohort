package knowledge

import (
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// heldSection is the pages the collection check held because they contradict
// what the collection holds (orchestrate collection_held.go), each with its
// conflict, and the ways to settle it: by hand, or argued out first.
//
// For whoever decides about the collection, as the suggestions are: its
// owner, a maintainer, or an administrator for the deployment's own.
func (T *KnowledgeApp) heldSection(user, collectionID string) (ui.Section, bool) {
	if strings.TrimSpace(collectionID) == "" || strings.TrimSpace(user) == "" {
		return ui.Section{}, false
	}
	c, ok := LoadCollection(UserDB(CollectionsDB(), user), user, collectionID)
	if !ok {
		return ui.Section{}, false
	}
	if c.Owner != user && !CollectionMaintainer(c, user) && !(c.Owner == "" && IsDeploymentScope(c) && UserIsAdmin(user)) {
		return ui.Section{}, false
	}
	base := "/orchestrate/api/collections/" + url.PathEscape(collectionID) + "/held"
	settle := ui.ActionList{
		Source:         base + "/settle?sid={id}",
		LabelField:     "item",
		DescField:      "desc",
		HistoryField:   "history",
		PostTo:         base + "/settle?sid={id}",
		ButtonText:     "Settle it",
		ProgressSource: base + "/settle/progress?sid={id}",
		CancelTo:       base + "/settle/stop?sid={id}",
		// The ruling lands on the row: redraw the list when it does.
		Invalidate: []string{base},
	}
	return ui.Section{
		Title:    "Held for you",
		Subtitle: "Pages that did not pass the check before going in. They wait here for you.",
		Detail: "Before a page that a machine picked goes in (the curator's, or Auto-fill's), it is checked against what the collection is for and what it already says. A page from a secondary source (an article, a post, a story about the thing rather than its own word) also needs an independent source saying the same. A page is held here when it contradicts a document already in the collection, when another source contradicts it, or when no other source could be found saying the same.\n\n" +
			"Settle it by hand: add the page, add it in place of the document it contradicts, or leave it out. Or press Settle it first: two sides argue it, one for the page and one for what contradicts it, each citing what it reads; their citations are checked against those pages; then a ruling is shown here. Nothing changes until you choose.\n\n" +
			"Pages you pick yourself (your articles, sources you tick, uploads) are never checked.",
		Body: ui.Table{
			Source:       base,
			RecordsField: "held",
			RowKey:       "id",
			EmptyText:    "No pages held.",
			Columns: []ui.Col{
				{Field: "title", Label: "Page", Flex: 2, Link: "source_link"},
				{Field: "created", Label: "When", Format: "reltime", Mute: true},
				// Beneath, in full: what it contradicts is the point of the
				// row, and cutting it to fit the buttons left "Conflicts with".
				{Field: "conflict", Label: "Why it is held", Line: 2},
				{Field: "ruling", Label: "Ruling", Mute: true, Line: 2},
			},
			RowActions: []ui.RowAction{
				// Settle it needs something to argue the page against: the
				// document it contradicts, or the page that contradicted it.
				// Replace and keep need a document in the collection.
				ui.ExpandIf("Settle it", "can_settle", "", settle),
				{Type: "button", Label: "Add it", PostTo: base + "/add?sid={id}",
					Confirm: "Add this page to the collection?"},
				{Type: "button", Label: "Replace the old one", OnlyIf: "has_doc", PostTo: base + "/replace?sid={id}",
					Confirm: "Add this page and remove the document it contradicts from the collection?"},
				{Type: "button", Label: "Keep the old one", OnlyIf: "has_doc", PostTo: base + "/keep?sid={id}"},
				{Type: "button", Label: "Leave it out", HideIf: "has_doc", PostTo: base + "/keep?sid={id}"},
			},
		},
	}, true
}
