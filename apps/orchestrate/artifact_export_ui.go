package orchestrate

// Export buttons on the orchestrate pages go through the shared bundle client
// (core ArtifactClientJS): the owner chooses which kinds of dependency travel
// with the item, and the file is a bundle every Import button reads. A
// recipient of a shared item keeps the plain recipe download, since the
// per-user export resolves only the requester's own records.

import (
	"encoding/json"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// artifactExportHead loads the shared client and registers the row and
// toolbar Export actions. Safe to include more than once on a page: the client
// defines itself once, and re-registering a name replaces it with the same.
func artifactExportHead() string {
	h := ui.NewHead().JS(ArtifactClientJS)
	for _, typ := range []string{"agent", "pipeline", "machine"} {
		h.ClientAction("export_"+typ, `function(ctx){ window.gohortArtifacts.exportAction('`+typ+`', 'id', 'name')(ctx); }`)
	}
	return h.Render()
}

// exportToolbarAction is the Export button on an item's own page: the
// choose-what-travels dialog for its owner, the plain recipe for anybody else.
func exportToolbarAction(typ, id, name, recipeURL string, mine bool) ui.ToolbarAction {
	if !mine {
		return ui.ToolbarAction{
			Label: "Export", Title: "Download this " + typ + "'s portable recipe",
			Method: "GET", URL: recipeURL,
		}
	}
	data, _ := json.Marshal(map[string]string{"name": id, "label": name})
	return ui.ToolbarAction{
		Label: "Export", Title: "Download this " + typ + ", choosing what goes with it",
		Method: "client", URL: "export_" + typ, Data: string(data),
	}
}
