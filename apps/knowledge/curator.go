package knowledge

// How a collection is curated: the choice the guided setup and the
// collection's page both ask, in the same words.
//
// All three run the collection's plan (orchestrate collection_plan.go,
// docs/design/knowledge-one-curation.md): Hand Curated when the owner presses
// Run now, Auto-Curation also on a schedule, and Curation by Agent through one
// of the owner's agents. Auto-Curation used to be a hidden agent of
// Knowledge's own; it is the plan run now, and its stored value is kept.

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// curationFields ask how a collection is curated, in the same words in the
// guided setup (as cards) and on the collection's page (as a select): its
// curator is one stored field, and the create and steward endpoints read the
// choice (orchestrate chooseCurator). Auto-Curation and Hand Curated sit at
// either end, with Curation by Agent between them only when there are agents
// to pick, and then a second question: which one.
func curationFields(user, control string) []ui.FormField {
	agents := AgentNameOptions(user)
	options := []ui.SelectOption{{Value: "auto", Label: "Auto-Curation",
		Help: "Its plan runs on a schedule, finding material that fits what it is for. Each page is checked before it goes in."}}
	if len(agents) > 0 {
		options = append(options, ui.SelectOption{Value: "agent", Label: "Curation by Agent",
			Help: "One of your agents carries out its plan, with the tools it has. Pick which."})
	}
	options = append(options, ui.SelectOption{Value: "hand", Label: "Hand Curated",
		Help: "Its plan runs only when you press Run now; otherwise it holds what you put in it."})
	fields := []ui.FormField{
		{Field: "curation", Type: control, Label: "How it is curated", Options: options},
	}
	if len(agents) > 0 {
		fields = append(fields, ui.FormField{Field: "curator_agent", Type: "select", Label: "Which agent", ShowWhen: "curation:agent",
			Options: append([]ui.SelectOption{{Value: "", Label: "(choose one of your agents)"}}, agents...)})
	}
	return fields
}
