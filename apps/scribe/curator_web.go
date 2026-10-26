// The digest surface.
//
// The curator writes without asking, so this is not a nice-to-have panel — it is
// the thing that makes writing-without-asking reviewable. A digest stored and
// never shown is the same as no digest, which is why the endpoints here render
// EVERY outcome (including the discards and holds that changed nothing, the ones
// that reveal a miscalibrated curator) and carry the superseded text inline
// rather than making a reader go diff two revisions to find out what was lost.
package scribe

import (
	"context"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// handleCuratorRuns GETs the digest list: recent runs, newest first, with their
// entries and the pending queue depth.
func (T *Scribe) handleCuratorRuns(c *jsonapi.Call) (any, error) {
	udb, user := c.UDB, c.User
	type entryRow struct {
		Kind      string `json:"kind"`
		FindingID string `json:"finding_id"`
		Topic     string `json:"topic,omitempty"`
		Origin    string `json:"origin,omitempty"`
		GuideID   string `json:"guide_id,omitempty"`
		GuideName string `json:"guide_name,omitempty"`
		Section   string `json:"section,omitempty"`
		Note      string `json:"note,omitempty"`
		Replaced  string `json:"replaced,omitempty"`
		Undone    bool   `json:"undone,omitempty"`
		// CanUndo is computed rather than inferred in the browser: an entry
		// that changed no document, one already undone, and one whose revision
		// has aged out of the guide's capped history are three different
		// reasons a button should not be offered, and only the server can tell
		// them apart.
		CanUndo   bool   `json:"can_undo"`
		UndoBlock string `json:"undo_block,omitempty"` // why not, when CanUndo is false
	}
	type runRow struct {
		ID          string         `json:"id"`
		Started     string         `json:"started"`
		Age         string         `json:"age,omitempty"`
		Findings    int            `json:"findings"`
		Counts      map[string]int `json:"counts"`
		Unaccounted int            `json:"unaccounted,omitempty"`
		Summary     string         `json:"summary,omitempty"`
		Error       string         `json:"error,omitempty"`
		Entries     []entryRow     `json:"entries"`
	}

	runs := listCuratorRuns(udb)
	out := make([]runRow, 0, len(runs))
	for _, run := range runs {
		row := runRow{
			ID: run.ID, Started: run.Started, Age: curatorRunAge(run),
			Findings: run.Findings, Counts: run.Counts(),
			Unaccounted: run.Unaccounted(), Summary: run.Summary, Error: run.Error,
			Entries: make([]entryRow, 0, len(run.Entries)),
		}
		for _, e := range run.Entries {
			er := entryRow{
				Kind: e.Kind, FindingID: e.FindingID, Topic: e.Topic, Origin: e.Origin,
				GuideID: e.GuideID, GuideName: e.GuideName, Section: e.Section,
				Note: e.Note, Replaced: e.Replaced, Undone: e.Undone,
			}
			er.CanUndo, er.UndoBlock = undoAvailability(T.DB, udb, user, e)
			row.Entries = append(row.Entries, er)
		}
		out = append(out, row)
	}
	return map[string]any{
		"runs":    out,
		"pending": len(udb.Keys(findingsTable)),
	}, nil
}

// undoAvailability reports whether one entry can still be undone, and why not.
func undoAvailability(appDB, udb Database, user string, e CuratorEntry) (bool, string) {
	switch {
	case e.Undone:
		return false, "already undone"
	case e.GuideID == "" || e.PriorRev == "":
		return false, "this decision changed no document"
	}
	g, owner, ownerUDB, ok := resolveGuide(appDB, udb, user, e.GuideID)
	if !ok {
		return false, "that guide no longer exists"
	}
	if !(CanManageShared(user, owner, false) || g.editableBy(owner, user)) {
		return false, "you don't have edit access to that guide"
	}
	if _, revOK := loadRevision(ownerUDB, e.GuideID, e.PriorRev); !revOK {
		return false, "the revision this would restore has aged out of the guide's history"
	}
	return true, ""
}

// handleCuratorUndo POSTs {run_id, finding_id} and restores the guide to the
// revision that preceded that entry's write.
func (T *Scribe) handleCuratorUndo(c *jsonapi.Call, body struct {
	RunID     string `json:"run_id"`
	FindingID string `json:"finding_id"`
}) (any, error) {
	udb, user := c.UDB, c.User
	if strings.TrimSpace(body.RunID) == "" || strings.TrimSpace(body.FindingID) == "" {
		return nil, jsonapi.BadRequest("run_id and finding_id are required")
	}
	if err := undoCuratorEntry(T.DB, udb, user, body.RunID, body.FindingID); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusOK), nil
}

// handleCuratorRunNow drains this user's queue immediately. The two automatic
// firings are both delayed by design, so without this there is no way to watch
// the curator handle a batch you just produced.
func (T *Scribe) handleCuratorRunNow(c *jsonapi.Call, _ struct{}) (any, error) {
	r, udb, user := c.R, c.UDB, c.User
	if len(udb.Keys(findingsTable)) == 0 {
		return map[string]any{"ran": false, "reason": "nothing is waiting to be curated"}, nil
	}
	// Under the same per-user lock the automatic firings take. Without it a
	// press landing while the threshold or interval run was in flight read the
	// same pending findings and filed them a second time.
	mu := curatorLock(user)
	if !mu.TryLock() {
		return map[string]any{"ran": false, "reason": "the curator is already working through your queue"}, nil
	}
	defer mu.Unlock()
	// The run outlives the request: it files findings into guides as it goes,
	// and closing the page used to cancel it partway, leaving a batch half
	// placed and half returned to the queue.
	run, err := T.RunCurator(context.WithoutCancel(r.Context()), user)
	lastCuratorRun.Store(user, time.Now())
	if err != nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
	}
	return map[string]any{"ran": true, "run_id": run.ID, "entries": len(run.Entries)}, nil
}

// handlePendingFindings GETs the queue itself — what is waiting and has not been
// decided. Distinct from the digest: the digest says what the curator DID, and a
// user wondering why their guide has not changed is asking what it has not done
// yet.
func (T *Scribe) handlePendingFindings(c *jsonapi.Call) (any, error) {
	udb := c.UDB
	type row struct {
		ID         string `json:"id"`
		Topic      string `json:"topic"`
		Origin     string `json:"origin"`
		Confidence string `json:"confidence"`
		Submitted  string `json:"submitted"`
		Age        string `json:"age,omitempty"`
		Content    string `json:"content"`
		// For is the guide its producer was attached to, by title, when it
		// was and the guide is still there.
		For string `json:"for,omitempty"`
	}
	pending := listPendingFindings(udb)
	out := make([]row, 0, len(pending))
	for _, f := range pending {
		r := row{
			ID: f.ID, Topic: f.Topic, Origin: findingOriginLabel(f.Origin),
			Confidence: f.Confidence, Submitted: f.Submitted, Age: ageSince(f.Submitted), Content: f.Content,
		}
		if id := strings.TrimSpace(f.Document); id != "" {
			if g, ok := loadGuide(udb, id); ok {
				r.For = firstNonEmpty(g.Title, "Untitled guide")
			}
		}
		out = append(out, r)
	}
	return out, nil
}
