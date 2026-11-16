package knowledge

// How a collection is kept: the guided setup's choice. Both run the
// collection's plan (orchestrate collection_plan.go,
// docs/design/knowledge-one-curation.md): Hand Curated when the owner presses
// Run now, Auto-Curation also on a schedule. Agents submit documents; they
// are set on the collection's page.

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// curationFields ask how a new collection is kept, in the guided setup:
// Auto-Curation runs its plan every week (the collection page changes how
// often), Hand Curated only when the owner presses Run now. Agents are not a
// choice here: an agent submits documents, set on the collection's page.
func curationFields(user, control string) []ui.FormField {
	_ = user
	options := []ui.SelectOption{
		{Value: "auto", Label: "Auto-Curation",
			Help: "Its plan runs every week, finding material that fits what it is for. Each page is checked before it goes in."},
		{Value: "hand", Label: "Hand Curated",
			Help: "Its plan runs only when you press Run now; otherwise it holds what you, and agents you let submit, put in it."},
	}
	return []ui.FormField{{Field: "curation", Type: control, Label: "How it is kept", Options: options}}
}
