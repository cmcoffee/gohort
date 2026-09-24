package orchestrate

// GET|POST /api/collections/{id}/steward — who is in charge of this collection.
//
// One field, and its own endpoint rather than a corner of a bigger one, because
// it is the whole of what the collection page asks about here: which agent may
// add to this corpus and prune it.

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func (T *OrchestrateApp) handleCollectionSteward(w http.ResponseWriter, r *http.Request, user string, c Collection) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{
			"curator_agent": c.CuratorAgent,
			"status":        T.stewardStatusLine(user, c),
		})
	case http.MethodPost:
		var body struct {
			CuratorAgent string `json:"curator_agent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Stored as given, including a name that matches no agent. An agent may
		// be renamed or not built yet, and a choice that could not be recorded
		// until its agent existed would have to be remembered by a person. The
		// status line reports one that does not resolve.
		//
		// A name that DOES resolve is stored as the id it resolves to, so it
		// is resolved once, here, against the agents of the person choosing,
		// rather than on every run: the grant matches by id
		// (curatedCollectionsFor), and an id survives a rename.
		c.CuratorAgent = strings.TrimSpace(body.CuratorAgent)
		if c.CuratorAgent != "" {
			if a, ok := findAgentByNameOrID(UserDB(T.DB, user), user, c.CuratorAgent); ok && a.ID != "" {
				c.CuratorAgent = a.ID
			}
		}
		saveCollection(UserDB(T.DB, user), c)
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// stewardStatusLine names who is in charge, and says so loudest when the answer
// has stopped resolving.
//
// An agent is named by TEXT, so a rename or a deletion leaves a collection
// pointing at nothing. Nothing breaks loudly when that happens — the agent
// simply never receives the corpus tools — so the page where somebody chose the
// name is the only place the difference can surface.
func (T *OrchestrateApp) stewardStatusLine(user string, c Collection) string {
	name := strings.TrimSpace(c.CuratorAgent)
	if name == "" {
		return "Nobody is maintaining this collection. It holds what has been put in it by hand, by upload, or by auto-fill."
	}
	// findAgentByNameOrID, not loadAgent, because that is what the RUN uses.
	// Checking more strictly here than the thing being reported on would make
	// an agent stored under a name, which resolves perfectly well, get reported
	// as broken while it quietly worked.
	a, ok := findAgentByNameOrID(UserDB(T.DB, user), user, name)
	if !ok {
		return "In charge: " + name + ", but that is not an agent of yours any more, so nothing is maintaining this. Pick another, or clear it."
	}
	// The NAME, not the stored id. The id is what survives a rename and is
	// therefore what gets saved; it is not what the person chose from the menu,
	// and reading their own choice back in a form they did not use makes a
	// correct setting look like a wrong one.
	return "In charge: " + chFirst(a.Name, name) + ", which can add documents to this collection and remove them from it."
}
