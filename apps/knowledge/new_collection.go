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
			Title: "Details",
			Intro: "A few questions that steer what goes in. Each one is optional.",
			Fields: []ui.FormField{
				{Field: "currency", Type: "select", Label: "How current does it have to be?",
					Options: []ui.SelectOption{
						{Value: "", Label: "No preference"},
						{Value: "current", Label: "Current matters most", Help: "Prefer the latest; older pages are likely out of date."},
						{Value: "recent", Label: "Recent is enough"},
					}},
				{Field: "depth", Type: "select", Label: "How much of it?",
					Options: []ui.SelectOption{
						{Value: "", Label: "No preference"},
						{Value: "overview", Label: "A solid overview", Help: "The essentials, not everything."},
						{Value: "comprehensive", Label: "Comprehensive"},
					}},
				{Field: "mostly", Type: "select", Label: "Where will its material come from?",
					Options: []ui.SelectOption{
						{Value: "", Label: "No preference"},
						{Value: "web", Label: "Mostly the web"},
						{Value: "files", Label: "Mostly my own files"},
						{Value: "both", Label: "Both"},
					}},
				{Field: "prefer", Type: "tags", Label: "Sources to prefer",
					Help: "Sites or publishers you trust for this. Searches favour them."},
				{Field: "avoid", Type: "tags", Label: "Sources to leave out",
					Help: "Pages from these are turned away."},
			},
		},
		{
			Title:  "Curation",
			Intro:  "Does its plan run on its own? You can change this on the collection's page.",
			Fields: curationFields(user, "cards"),
		},
	}
	if WebSearchAvailable() {
		// Unanswered, the create endpoint starts Suggest sources, the first
		// option, so what the select shows is what happens.
		last := &steps[len(steps)-1]
		last.Fields = append(last.Fields, ui.FormField{Field: "start", Type: "select", Label: "To start with",
			Help: "What runs as the collection is created. Both wait for you before anything is added.",
			Options: []ui.SelectOption{
				{Value: "suggest", Label: "Suggest sources", Help: "Searches from what you said and lists what it finds, each with its site and a line from it. You tick what goes in."},
				{Value: "plan", Label: "Draft a plan to fill it", Help: "Writes the searches it would run and how many documents to add, with its size. You check it, then run it."},
				{Value: "none", Label: "Nothing yet"},
			}})
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
