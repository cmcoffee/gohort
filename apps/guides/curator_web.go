// The digest surface.
//
// The curator writes without asking, so this is not a nice-to-have panel — it is
// the thing that makes writing-without-asking reviewable. A digest stored and
// never shown is the same as no digest, which is why the endpoints here render
// EVERY outcome (including the discards and holds that changed nothing, the ones
// that reveal a miscalibrated curator) and carry the superseded text inline
// rather than making a reader go diff two revisions to find out what was lost.
package guides

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleCuratorRuns GETs the digest list: recent runs, newest first, with their
// entries and the pending queue depth.
func (T *Guides) handleCuratorRuns(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	writeJSON(w, map[string]any{
		"runs":    out,
		"pending": len(udb.Keys(findingsTable)),
	})
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
	if !(CanManageShared(user, owner, false) || g.sharedForEdit()) {
		return false, "you don't have edit access to that guide"
	}
	if _, revOK := loadRevision(ownerUDB, e.GuideID, e.PriorRev); !revOK {
		return false, "the revision this would restore has aged out of the guide's history"
	}
	return true, ""
}

// handleCuratorUndo POSTs {run_id, finding_id} and restores the guide to the
// revision that preceded that entry's write.
func (T *Guides) handleCuratorUndo(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if !IsStateChangingMethod(r.Method) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RunID     string `json:"run_id"`
		FindingID string `json:"finding_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.RunID) == "" || strings.TrimSpace(body.FindingID) == "" {
		http.Error(w, "run_id and finding_id are required", http.StatusBadRequest)
		return
	}
	if err := undoCuratorEntry(T.DB, udb, user, body.RunID, body.FindingID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleCuratorRunNow drains this user's queue immediately. The two automatic
// firings are both delayed by design, so without this there is no way to watch
// the curator handle a batch you just produced.
func (T *Guides) handleCuratorRunNow(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if !IsStateChangingMethod(r.Method) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(udb.Keys(findingsTable)) == 0 {
		writeJSON(w, map[string]any{"ran": false, "reason": "nothing is waiting to be curated"})
		return
	}
	run, err := T.RunCurator(r.Context(), user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ran": true, "run_id": run.ID, "entries": len(run.Entries)})
}

// handlePendingFindings GETs the queue itself — what is waiting and has not been
// decided. Distinct from the digest: the digest says what the curator DID, and a
// user wondering why their guide has not changed is asking what it has not done
// yet.
func (T *Guides) handlePendingFindings(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type row struct {
		ID         string `json:"id"`
		Topic      string `json:"topic"`
		Origin     string `json:"origin"`
		Confidence string `json:"confidence"`
		Submitted  string `json:"submitted"`
		Content    string `json:"content"`
	}
	pending := listPendingFindings(udb)
	out := make([]row, 0, len(pending))
	for _, f := range pending {
		out = append(out, row{
			ID: f.ID, Topic: f.Topic, Origin: findingOriginLabel(f.Origin),
			Confidence: f.Confidence, Submitted: f.Submitted, Content: f.Content,
		})
	}
	writeJSON(w, out)
}
