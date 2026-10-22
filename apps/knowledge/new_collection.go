package knowledge

import (
	"encoding/json"

	"github.com/cmcoffee/gohort/core/ui"
)

// newCollectionForm is + New collection's form: the framework's FormPanel in
// the page's dialog, so it has the ✨ every form has. Here it is named Draft
// with AI and opens on the setting up, the way Scribe's Discuss opens on the
// brief: a person creating a collection usually knows the subject, not the
// words that make Auto-fill find the right documents.
//
// The helper knows the form from its fields' Help and Detail, so the
// guidance a description needs is written there, where the person reads it
// too.
func newCollectionForm() ui.FormPanel {
	return ui.FormPanel{
		PostURL:        "/orchestrate/api/collections",
		SubmitLabel:    "Create",
		RedirectURL:    "/knowledge/c/{id}",
		RedirectTarget: "_self",
		Assist:         true,
		AssistLabel:    "Draft with AI",
		AssistAsk:      "Assist me with setting up a knowledge store.",
		Fields: []ui.FormField{
			{Field: "name", Label: "Name", Placeholder: "e.g. Kubernetes Reference", Required: true,
				Help: "What it is called where you attach it to an agent or skill."},
			{Field: "description", Label: "Description", Type: "textarea", Rows: 3,
				Placeholder: "What documents should this collection contain?",
				Help:        "What it should hold, in one to three sentences. Auto-fill builds its web searches from this, so name the kinds of sources.",
				Detail: "Concrete names find better documents than a topic does: \"Official Kubernetes API reference, kubectl command guide, and CNCF operator best practices\" " +
					"gets more from Auto-fill than \"Things about Kubernetes\". It also tells you, later, what the collection is for."},
		},
	}
}

// newCollectionFormScript hands the form to the page's script, which mounts
// it in the dialog.
func newCollectionFormScript() string {
	b, err := json.Marshal(newCollectionForm())
	if err != nil {
		b = []byte("null")
	}
	return "<script>window.knowledgeNewCollectionForm = " + string(b) + ";</script>"
}
