// Knowledge on a project: collections the assistant searches while it works
// there, chosen per project. Anvil searches nothing else; the deployment's
// collections, whatever other apps gathered about anything, are not reached
// by default (agenthost.Agent.KnowledgeAttachedOnly).
//
// Attaching gives nobody anything new: the list offers only what the person
// choosing may read, a save keeps only those, and each turn searches only
// the ones the person asking may read, so a directory project an
// administrator shares searches, for each person, what is theirs to see.
package anvil

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// readableCollections is the ids in ids that user may read, in order, once
// each.
func readableCollections(user string, ids []string) []string {
	cdb := UserDB(CollectionsDB(), user)
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id == "" || seen[id] {
			continue
		}
		if _, ok := LoadCollection(cdb, user, id); ok {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// collectionOptions are the collections user may attach: their own, those
// shared with them, and the deployment's, each saying which it is.
func collectionOptions(user string) []ui.SelectOption {
	var out []ui.SelectOption
	for _, c := range ListCollections(UserDB(CollectionsDB(), user), user) {
		label := strings.TrimSpace(c.Name)
		if label == "" {
			label = c.ID
		}
		switch {
		case IsDeploymentScope(c):
			label += " (everyone's)"
		case c.Owner != user:
			label += " (shared with you)"
		}
		out = append(out, ui.SelectOption{Value: c.ID, Label: label})
	}
	return out
}

// knowledgeField is the project setting.
func knowledgeField(user string) ui.FormField {
	opts := collectionOptions(user)
	help := "What the assistant searches as it works here: reference such as an API's docs, the team's standards or runbooks. Nothing ticked: it searches no knowledge."
	if len(opts) == 0 {
		help = "You have no knowledge collections yet. Make one under Knowledge, then tick it here."
	}
	return ui.FormField{Field: "collections", Label: "Knowledge", Type: "checklist", Options: opts, Help: help}
}
