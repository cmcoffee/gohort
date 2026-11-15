package knowledge

import (
	"encoding/json"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// newCollectionForm is + New collection: a short guided setup in the page's
// dialog (docs/design/knowledge-curation.md). It asks what the collection is
// for, a few optional questions, how it should be filled, and which agents
// read it. Only the name is required; every other answer can be skipped.
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
			Intro: "What is this collection for? Pick the closest, or say it in your own words.",
			Fields: []ui.FormField{
				{Field: "purpose", Type: "cards", Label: "It is for",
					Options: []ui.SelectOption{
						{Value: "product", Label: "Our product or docs", Help: "Answer questions about a product, a service, or its documentation."},
						{Value: "topic", Label: "Keeping up with a topic", Help: "News and developments in a field, kept current."},
						{Value: "reference", Label: "A technology reference", Help: "How something works and how to use it: specs, guides, APIs."},
						{Value: "own_files", Label: "My own files", Help: "Notes, contracts, reports and exports you upload."},
					}},
				{Field: "name", Label: "Name", Required: true, Placeholder: "a short name for it",
					Help: "What it is called where you attach it to an agent or skill."},
				{Field: "about", Label: "In your own words", Type: "textarea", Rows: 3,
					Placeholder: "what it should hold, and what it is for",
					Help:        "Optional. Searches for material are written from this, so name the products, standards or sites it is about.",
					Detail: "Concrete names find better documents than a topic does: naming the official reference, the command guide and the standards body " +
						"gets more than naming the subject alone. It also tells you, later, what the collection is for. It becomes the collection's description, which you can edit on its page."},
				// The curator Auto-curated means, carried for the Filling
				// step: the create endpoint puts it in charge only when that
				// step's answer is "curated".
				{Field: "curator_agent", Type: "hidden", Default: curatorAgentID},
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
			Title: "Filling",
			Intro: "How should it be filled? You can change this on the collection's page.",
			Fields: []ui.FormField{
				{Field: "fill", Type: "cards", Label: "Filled",
					Options: []ui.SelectOption{
						{Value: "curated", Label: "Look after it for me",
							Help: "Auto-curated: Knowledge finds material that fits what you said and adds it, and proposes removals for you to confirm. Start it with Curate now on the collection's page."},
						{Value: "by_hand", Label: "I'll fill it myself",
							Help: "Nothing goes in unless you put it there. On the collection's page: upload your own files (contracts, notes, exports, PDFs), " +
								"list particular web pages to fetch, auto-fill a quick start from the web, or paste text."},
					}},
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
