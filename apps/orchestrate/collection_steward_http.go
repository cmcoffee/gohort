package orchestrate

// GET|POST /api/collections/{id}/steward — who is in charge of this collection.
//
// One field, and its own endpoint rather than a corner of a bigger one, because
// it is the whole of what the collection page asks about here: which agent may
// add to this corpus and prune it.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

func (T *OrchestrateApp) collectionSteward(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	mode := curationOf(c.CuratorAgent)
	agent := ""
	if mode == curationAgent {
		agent = c.CuratorAgent
	}
	refresh := "never"
	if p, _, _, ok := refreshScheduled(c.Owner, c.ID); ok {
		refresh = p.Every
	}
	return map[string]any{
		"curation":      mode,
		"curator_agent": agent,
		"refresh":       refresh,
		"status":        T.stewardStatusLine(call.User, c),
	}, nil
}

// How a collection is curated, as the page offers it. Stored as one field,
// Collection.CuratorAgent: empty for Hand Curated, collectionAutoCurator for
// Auto-Curation, or one of the person's agents.
const (
	curationHand  = "hand"  // Hand Curated
	curationAuto  = "auto"  // Auto-Curation
	curationAgent = "agent" // Curation by Agent
)

// collectionAutoCurator is what Auto-Curation stores. It named the Knowledge
// app's own curating agent, which is gone: Auto-Curation is the collection's
// plan, run on its schedule (collection_plan.go). The value is kept so
// collections that chose it before read the same.
const collectionAutoCurator = "app-knowledge-curator"

// curationOf is the choice a stored curator stands for.
func curationOf(curator string) string {
	switch curator = strings.TrimSpace(curator); curator {
	case "":
		return curationHand
	case collectionAutoCurator:
		return curationAuto
	}
	return curationAgent
}

// chooseCurator is the curator a choice stores; agent is the person's pick.
// change is false when the choice leaves the curator as it is: Curation by
// Agent before an agent is picked.
func chooseCurator(udb Database, user, mode, agent string) (curator string, change bool, err error) {
	switch mode {
	case curationHand:
		return "", true, nil
	case curationAuto:
		return collectionAutoCurator, true, nil
	case curationAgent:
		agent = strings.TrimSpace(agent)
		if agent == "" {
			return "", false, nil
		}
		a, ok := findAgentByNameOrID(udb, user, agent)
		if !ok || a.ID == "" || hiddenAppAgent(a.ID) || a.ID == collectionAutoCurator {
			return "", false, fmt.Errorf("%q is not one of your agents", agent)
		}
		return a.ID, true, nil
	}
	return "", false, fmt.Errorf("unknown curation %q: hand, auto or agent", mode)
}

// collectionStewardSave names the agent that curates this collection.
//
// The page sends the choice (curation) with the agent it means; a caller
// sending only curator_agent sets it as before.
func (T *OrchestrateApp) collectionStewardSave(call *jsonapi.Call, body struct {
	Curation     string `json:"curation"`
	CuratorAgent string `json:"curator_agent"`
	// Refresh is how often the curator refreshes it: week, month or never
	// (collection_refresh.go). Absent leaves the schedule as it is.
	Refresh string `json:"refresh"`
}) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	user := call.User
	if mode := strings.TrimSpace(body.Curation); mode != "" {
		curator, change, cerr := chooseCurator(UserDB(T.DB, user), user, mode, body.CuratorAgent)
		if cerr != nil {
			return nil, jsonapi.BadRequest(cerr.Error())
		}
		if change {
			c.CuratorAgent = curator
			saveCollection(UserDB(T.DB, user), c)
		}
		if err := T.keepRefresh(c, body.Refresh); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		return map[string]any{"ok": true}, nil
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
	if err := T.keepRefresh(c, body.Refresh); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"ok": true}, nil
}

// keepRefresh brings the collection's refresh schedule in line with a save:
// none once nobody curates it, else the interval sent, when one was.
func (T *OrchestrateApp) keepRefresh(c Collection, every string) error {
	if strings.TrimSpace(c.CuratorAgent) == "" {
		return setRefresh(c.Owner, c.ID, "never")
	}
	if strings.TrimSpace(every) == "" {
		return nil
	}
	return setRefresh(c.Owner, c.ID, every)
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
	switch curationOf(name) {
	case curationHand:
		return "Hand Curated: the plan below runs only when you press Run now; otherwise it holds what you put in it."
	case curationAuto:
		return "Auto-Curation: the plan below runs on its schedule, and when you press Run now. Each page it finds is checked before it goes in."
	}
	// findAgentByNameOrID, not loadAgent, because that is what the RUN uses.
	// Checking more strictly here than the thing being reported on would make
	// an agent stored under a name, which resolves perfectly well, get reported
	// as broken while it quietly worked.
	a, ok := findAgentByNameOrID(UserDB(T.DB, user), user, name)
	if !ok {
		return "Curation by Agent: " + name + ", but that is not an agent of yours any more, so nothing is curating this. Pick another, or choose Hand Curated."
	}
	// The NAME, not the stored id. The id is what survives a rename and is
	// therefore what gets saved; it is not what the person chose from the menu,
	// and reading their own choice back in a form they did not use makes a
	// correct setting look like a wrong one.
	return "Curation by Agent: " + chFirst(a.Name, name) + " carries out the plan below with its own tools, and can add documents to this collection and remove them from it."
}
