package knowledge

// What kind of collection it is: the guided setup's choice (orchestrate
// collection_guided.go, collectionKinds). A kind only sets the first values
// of settings every collection has, its schedule, documents per run, a rule
// and what starts; they are all changed on the collection's page afterwards.

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// kindFields ask what kind of collection it is, each kind saying when to use
// it and what it sets. Set it up yourself opens the settings themselves.
//
// Reference is the standard practice, and what a collection is when no kind
// is picked, where the server can search the web; without search nothing can
// look for material, so it is My own files (orchestrate guidedCollection.kind).
// The field says which: a visible field's Default does not choose a card.
func kindFields() []ui.FormField {
	search := WebSearchAvailable()
	unpicked := "Reference is the usual choice, and what it is if you pick none."
	if !search {
		unpicked = "Without web search on this server nothing can look for material, so it is My own files if you pick none."
	}
	kinds := []ui.SelectOption{
		{Value: "reference", Label: "Reference",
			Help: "For docs on a product, API or standard you work with, which change slowly. A plan to fill it is drafted for you to check and run; then it looks each month for what changed, up to 20 documents a run, preferring official sources."},
		{Value: "topic", Label: "Keep up with a topic",
			Help: "For news, releases or advisories, where what is new is the point. A plan is drafted for you to check and run; then it looks every week, up to 10 documents a run, and skips pages more than a year old."},
		{Value: "files", Label: "My own files",
			Help: "For material you upload yourself. Nothing is searched for: you add documents on its page."},
		{Value: "custom", Label: "Set it up yourself",
			Help: "Choose how often it looks for material, how much a run adds, and how it starts."},
	}
	fields := []ui.FormField{
		{Field: "kind", Type: "cards", Label: "Kind", Help: unpicked, Options: kinds},
		// The first option is what an untouched select sends nothing for,
		// and nothing is no schedule: what it shows is what happens.
		{Field: "refresh", Type: "select", Label: "Looks for new material", ShowWhen: "kind:custom",
			Options: []ui.SelectOption{
				{Value: "never", Label: "Only when I press Run now", Help: "For a collection you fill once, or by hand."},
				{Value: "month", Label: "Every month", Help: "For reference material that changes slowly."},
				{Value: "week", Label: "Every week", Help: "For a fast-moving subject: news, releases, advisories."},
			}},
		{Field: "docs_per_run", Type: "number", Label: "Documents per run", ShowWhen: "kind:custom", Placeholder: "20",
			Help: "At most this many are added in one run. Empty: 20."},
	}
	if search {
		// Unanswered, the create endpoint starts Suggest sources, the first
		// option, so what the select shows is what happens.
		fields = append(fields, ui.FormField{Field: "start", Type: "select", Label: "To start with", ShowWhen: "kind:custom",
			Help: "What runs as the collection is created. Both wait for you before anything is added.",
			Options: []ui.SelectOption{
				{Value: "suggest", Label: "Suggest sources", Help: "Searches from what you said and lists what it finds, each with its site and a line from it. You tick what goes in."},
				{Value: "plan", Label: "Draft a plan to fill it", Help: "Writes the searches it would run and how many documents to add, with its size. You check it, then run it."},
				{Value: "none", Label: "Nothing yet"},
			}})
	}
	return fields
}
