package knowledge

import (
	"encoding/json"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// collectionSuggestURL drafts a name or a description from what the person
// said the collection is for (orchestrate collection_naming.go).
const collectionSuggestURL = "/orchestrate/api/collection-suggest"

// newCollectionForm is + New collection: a short guided setup in the page's
// dialog (docs/design/knowledge-curation.md). It asks what the collection is
// for in the person's own words, offers a name and a description drafted from
// them, then a few optional questions, how it is curated, and which agents
// read it. What it is for and the name are required; the rest can be skipped.
//
// The answers are not stored as answers. The create endpoint
// (orchestrate collection_guided.go) turns them into what a collection
// already has: its description, which Auto-fill, research and the curator
// read, so what was asked keeps steering what is found; its filter rules;
// its curator; and the agents it is attached to.
//
// A wizard has the ✨ every form has. Here it is named Draft with AI and opens
// on the setting up: a person creating a collection usually knows the
// subject, not the words that make the searches find the right documents. The
// helper knows the form from its fields' Help and Detail, so the guidance is
// written there, where the person reads it too.
func newCollectionForm(user string) ui.FormPanel {
	steps := []ui.FormStep{
		{
			Title: "Purpose",
			Intro: "What is this collection for? Say it in your own words: the next step drafts a name and a description from it.",
			Fields: []ui.FormField{
				{Field: "about", Label: "What it is for", Type: "textarea", Rows: 4, Required: true,
					Placeholder: "what it should hold, and what you will use it for",
					Help:        "The products, standards, sites or kinds of document it is about, and how you will use it. Plain words are fine.",
					Detail:      "Concrete names find better documents than a topic does: naming the official reference, the command guide and the standards body gets more than naming the subject alone."},
				// Says the answers come from this setup, so the create
				// endpoint applies its choices (start) as made.
				{Field: "guided", Type: "hidden", Default: "true"},
			},
		},
		{
			Title: "Name",
			Intro: "A name and a description, drafted from what you said. Pick a name or write your own. Edit the description, or press Suggest to work on it with the assistant.",
			Fields: []ui.FormField{
				{Field: "name", Label: "Name", Required: true, Placeholder: "a short name for it",
					SuggestURL: collectionSuggestURL, SuggestOnOpen: true,
					Help: "What it is called where you attach it to an agent or skill."},
				{Field: "description", Label: "Description", Type: "textarea", Rows: 4,
					SuggestURL: collectionSuggestURL, SuggestOnOpen: true,
					Help:   "What it should hold and what it is for. The searches that find its material are written from this.",
					Detail: "It is kept as the collection's description, which you can edit on its page. A good one names the concrete things the collection is about, so searches for it find the right documents."},
			},
		},
		{
			Title:  "Kind",
			Intro:  "What kind of collection is it? Each kind sets how it is kept to start with; everything it sets can be changed on the collection's page.",
			Fields: kindFields(),
		},
		{
			Title: "Sources",
			Intro: "Sites to favour or to leave out. Both are optional.",
			Fields: []ui.FormField{
				{Field: "prefer", Type: "tags", Label: "Sources to prefer",
					Help: "Sites or publishers you trust for this. Searches favour them."},
				{Field: "avoid", Type: "tags", Label: "Sources to leave out",
					Help: "Pages from these are turned away."},
			},
		},
	}
	// Which agents read it, when there are agents to choose from.
	if agents := AgentNameOptions(user); len(agents) > 0 {
		steps = append(steps, ui.FormStep{
			Title: "Agents",
			Intro: "Which of your agents should read it? They search it when they answer. An agent's Knowledge tab changes this later.",
			Fields: []ui.FormField{
				{Field: "attach_to", Type: "checklist", Label: "Read by", Options: agents},
			},
		})
	}
	return ui.FormPanel{
		PostURL:        "/orchestrate/api/collections",
		SubmitLabel:    "Create",
		RedirectURL:    "/knowledge/c/{id}",
		RedirectTarget: "_self",
		Assist:         true,
		AssistLabel:    "Draft with AI",
		AssistAsk:      "Assist me with setting up a knowledge store.",
		Steps:          steps,
	}
}

// newCollectionFormScript hands the form to the page's script, which mounts
// it in the dialog.
func newCollectionFormScript(user string) string {
	b, err := json.Marshal(newCollectionForm(user))
	if err != nil {
		b = []byte("null")
	}
	return "<script>window.knowledgeNewCollectionForm = " + string(b) + ";</script>"
}
