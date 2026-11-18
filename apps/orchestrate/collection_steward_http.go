package orchestrate

// GET|POST /api/collections/{id}/steward — Auto-Curation: whether the
// collection's plan runs on its own, and how often.
//
// Curation is the collection's plan (collection_plan.go), run once with Run
// now or on this schedule (collection_refresh.go). No agent is in charge:
// agents may submit documents (collection_submit.go), and what stays is the
// plan's and the check's to decide. The schedule is the whole setting here.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// collectionAutoCurator is what Auto-Curation stored in Collection.CuratorAgent
// in earlier versions. It is kept so those collections read the same: it is
// not an agent, and never one that may submit.
const collectionAutoCurator = "app-knowledge-curator"

func (T *OrchestrateApp) collectionSteward(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"refresh": autoCurationOf(c),
		"status":  T.stewardStatusLine(c),
	}, nil
}

// autoCurationOf is how often c's plan runs on its own: "week", "month", or
// "never".
func autoCurationOf(c Collection) string {
	// An app's collection says what its blueprint says, whether or not the
	// app's setting has it refreshing here.
	if c.OwningApp != "" && c.Refresh != "" {
		return c.Refresh
	}
	if p, _, _, ok := refreshScheduled(c.Owner, c.ID); ok {
		return p.Every
	}
	return "never"
}

// collectionStewardSave sets the schedule: week, month or never.
func (T *OrchestrateApp) collectionStewardSave(call *jsonapi.Call, body struct {
	Refresh string `json:"refresh"`
}) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(body.Refresh) == "" {
		return map[string]any{"ok": true}, nil
	}
	if err := setCollectionRefresh(UserDB(T.DB, c.Owner), c, body.Refresh, ""); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"ok": true}, nil
}

// stewardStatusLine says, in one line at the top of the collection's
// Curation section, what its settings come to: when it looks for material,
// how much a run adds, and where a page that fails the check goes.
func (T *OrchestrateApp) stewardStatusLine(c Collection) string {
	if len(c.SearchTerms) == 0 {
		if autoCurationOf(c) != "never" {
			return "Set to look for new material every " + autoCurationOf(c) + ", but there is no plan yet: Draft writes one from the description."
		}
		return "Filled by hand: there is no plan, so nothing is searched for. Draft writes a plan from the description if you want one."
	}
	adds := fmt.Sprintf("adding up to %d document%s a run", planDocs(c), plural(planDocs(c)))
	held := " Each page it finds is checked first; one that does not pass waits under Held for you."
	if every := autoCurationOf(c); every != "never" {
		return "Looks for new material every " + every + " and when you press Run now, " + adds + "." + held
	}
	return "Looks for material only when you press Run now, " + adds + "." + held
}
