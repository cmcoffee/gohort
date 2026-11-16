package orchestrate

// GET|POST /api/collections/{id}/steward — Auto-Curation: whether the
// collection's plan runs on its own, and how often.
//
// Curation is the collection's plan (collection_plan.go), run once with Run
// now or on this schedule (collection_refresh.go). No agent is in charge:
// agents may submit documents (collection_submit.go), and what stays is the
// plan's and the check's to decide. The schedule is the whole setting here.

import (
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
	if err := setRefresh(c.Owner, c.ID, body.Refresh); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"ok": true}, nil
}

// stewardStatusLine says how the collection is kept.
func (T *OrchestrateApp) stewardStatusLine(c Collection) string {
	if every := autoCurationOf(c); every != "never" {
		return "Auto-Curation: the plan below runs every " + every + ", and when you press Run now. Each page it finds is checked before it goes in."
	}
	return "Auto-Curation is off: the plan below runs when you press Run now."
}
