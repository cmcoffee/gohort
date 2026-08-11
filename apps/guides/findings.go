// The findings inbox and the curator's digest.
//
// A finding arrives from a producer that named no destination (see
// core/docs/findings.go) and waits here until the curator drains the queue. The
// digest is the record of what the curator then did — and because the curator
// writes on its own authority, the digest is the ONLY thing standing between
// "an agent maintains your documents" and "an agent edits your documents and
// you find out by reading them". It is treated accordingly: every outcome is
// recorded, including the ones that changed nothing, and a placement carries the
// revision that preceded it so a disagreement costs one click.
package guides

import (
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// findingsTable holds the pending queue, keyed by finding id.
	findingsTable = "guide_findings"
	// curatorRunsTable holds the digests, keyed by run id.
	curatorRunsTable = "guide_curator_runs"
	// maxCuratorRuns caps the retained digest history. Old runs age out; the
	// guide revisions they reference are capped separately (maxRevisions) and
	// will usually outlive them, so a very old digest's undo may no longer
	// resolve — surfaced in the UI rather than silently failing.
	maxCuratorRuns = 100
	// maxPendingFindings bounds the queue. Past it the OLDEST are dropped:
	// a queue that grows without limit because nothing is draining it is a
	// broken curator, and holding a year of unread findings helps nobody.
	maxPendingFindings = 500
)

// Outcome kinds recorded in a digest. Every finding in a batch lands on exactly
// one of these, so the count of entries always equals the count of findings and
// a missing finding is visible as an arithmetic error rather than as absence.
const (
	OutcomePlaced        = "placed"
	OutcomeSuperseded    = "superseded"
	OutcomeContradiction = "contradiction"
	OutcomeDiscarded     = "discarded"
	OutcomeHeld          = "held"
	OutcomeCreated       = "created"
)

// CuratorEntry is one decision about one finding.
type CuratorEntry struct {
	Kind      string `json:"kind"` // one of the Outcome* constants
	FindingID string `json:"finding_id"`
	Topic     string `json:"topic"`  // the finding's own framing, for reading the digest
	Origin    string `json:"origin"` // rendered "system · web-prod-01"
	GuideID   string `json:"guide_id,omitempty"`
	GuideName string `json:"guide_name,omitempty"`
	Section   string `json:"section,omitempty"`
	// Note is the curator's stated reason. Required for discard and hold,
	// where it is the only thing that makes the decision reviewable.
	Note string `json:"note,omitempty"`
	// Replaced is the section body as it stood BEFORE a supersede. Kept in the
	// digest rather than left to the revision history because "what did this
	// replace" is the question a reader of the digest is actually asking, and
	// making them diff two revisions to answer it is how digests stop getting
	// read.
	Replaced string `json:"replaced,omitempty"`
	// PriorRev is the guide revision id from immediately before this entry's
	// write. Undo restores it.
	PriorRev string `json:"prior_rev,omitempty"`
	Undone   bool   `json:"undone,omitempty"`
}

// CuratorRun is one drain of the inbox — the digest.
type CuratorRun struct {
	ID       string         `json:"id"`
	Started  string         `json:"started"`
	Finished string         `json:"finished,omitempty"`
	Owner    string         `json:"owner"`
	Findings int            `json:"findings"`
	Entries  []CuratorEntry `json:"entries"`
	// Summary is the curator's own one-paragraph account of the batch.
	Summary string `json:"summary,omitempty"`
	// Error records a run that failed partway. The entries recorded before the
	// failure stand — they are real writes — so a failed run is not the same as
	// a run that did nothing, and the digest has to say which happened.
	Error string `json:"error,omitempty"`
}

// Counts tallies entries by kind, for the digest header.
func (r CuratorRun) Counts() map[string]int {
	out := map[string]int{}
	for _, e := range r.Entries {
		out[e.Kind]++
	}
	return out
}

// Unaccounted reports findings that entered the run without producing an entry —
// the arithmetic check. A non-zero value means the curator dropped something on
// the floor, which is a bug in the curator loop rather than a decision, and the
// digest says so instead of quietly showing a short list.
func (r CuratorRun) Unaccounted() int {
	if n := r.Findings - len(r.Entries); n > 0 {
		return n
	}
	return 0
}

// --- inbox ---

// SubmitFinding queues a finding for the user. Implements core.FindingTarget.
func (g *guideTarget) SubmitFinding(user string, f DocFinding) (string, error) {
	udb := UserDB(g.app.DB, user)
	if udb == nil {
		return "", errNoStore
	}
	if strings.TrimSpace(f.ID) == "" {
		f.ID = newID()
	}
	if strings.TrimSpace(f.Submitted) == "" {
		f.Submitted = now()
	}
	if strings.TrimSpace(f.Origin.Observed) == "" {
		f.Origin.Observed = f.Submitted
	}
	f.Confidence = NormalizeConfidence(f.Confidence)
	f.Content = strings.TrimSpace(f.Content)
	f.Topic = strings.TrimSpace(f.Topic)
	udb.Set(findingsTable, f.ID, f)
	prunePendingFindings(udb)
	// Threshold firing. Runs in the background: a producer reporting a finding
	// is mid-investigation and must not wait on a curator batch.
	g.app.maybeRunCurator(user)
	return f.ID, nil
}

// PendingFindings reports the queue depth. Implements core.FindingTarget.
func (g *guideTarget) PendingFindings(user string) int {
	udb := UserDB(g.app.DB, user)
	if udb == nil {
		return 0
	}
	return len(udb.Keys(findingsTable))
}

// listPendingFindings returns the queue oldest-first, which is also the order
// the curator reads them in: a later finding that supersedes an earlier one
// should be applied after it, not before.
func listPendingFindings(udb Database) []DocFinding {
	if udb == nil {
		return nil
	}
	keys := udb.Keys(findingsTable)
	out := make([]DocFinding, 0, len(keys))
	for _, k := range keys {
		var f DocFinding
		if udb.Get(findingsTable, k, &f) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Submitted != out[j].Submitted {
			return out[i].Submitted < out[j].Submitted
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// dropFinding removes one from the queue once it has an outcome.
func dropFinding(udb Database, id string) {
	if udb != nil && id != "" {
		udb.Unset(findingsTable, id)
	}
}

// prunePendingFindings enforces the queue cap, oldest first.
func prunePendingFindings(udb Database) {
	pending := listPendingFindings(udb)
	if len(pending) <= maxPendingFindings {
		return
	}
	drop := pending[:len(pending)-maxPendingFindings]
	for _, f := range drop {
		udb.Unset(findingsTable, f.ID)
	}
	Log("[guides.curator] dropped %d findings over the %d queue cap — is the curator running?",
		len(drop), maxPendingFindings)
}

// findingOriginLabel renders an origin for a human reading the digest.
func findingOriginLabel(o DocFindingOrigin) string {
	label := strings.TrimSpace(o.ItemLabel)
	if label == "" {
		label = strings.TrimSpace(o.ItemID)
	}
	kind := strings.TrimSpace(o.SourceKind)
	switch {
	case kind == "" && label == "":
		return "unattributed"
	case label == "":
		return kind
	case kind == "":
		return label
	}
	return kind + " · " + label
}

// --- digest ---

// saveCuratorRun persists a digest, capping history.
func saveCuratorRun(udb Database, r CuratorRun) {
	if udb == nil || r.ID == "" {
		return
	}
	udb.Set(curatorRunsTable, r.ID, r)
	runs := listCuratorRuns(udb)
	if len(runs) <= maxCuratorRuns {
		return
	}
	for _, old := range runs[maxCuratorRuns:] {
		udb.Unset(curatorRunsTable, old.ID)
	}
}

// listCuratorRuns returns digests newest-first.
func listCuratorRuns(udb Database) []CuratorRun {
	if udb == nil {
		return nil
	}
	keys := udb.Keys(curatorRunsTable)
	out := make([]CuratorRun, 0, len(keys))
	for _, k := range keys {
		var r CuratorRun
		if udb.Get(curatorRunsTable, k, &r) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Started != out[j].Started {
			return out[i].Started > out[j].Started
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// loadCuratorRun fetches one digest.
func loadCuratorRun(udb Database, id string) (CuratorRun, bool) {
	var r CuratorRun
	if udb == nil || id == "" {
		return r, false
	}
	ok := udb.Get(curatorRunsTable, id, &r)
	return r, ok
}

// latestRevisionID returns the id of the guide's most recent revision, captured
// before a curator write so the entry can offer an undo. Empty when the guide
// has no revisions yet (a brand-new guide), in which case undo has nothing to
// restore and the UI says so rather than offering a button that fails.
func latestRevisionID(udb Database, guideID string) string {
	revs := listRevisions(udb, guideID)
	if len(revs) == 0 {
		return ""
	}
	return revs[len(revs)-1].ID
}

// undoCuratorEntry restores the guide to the revision that preceded one entry's
// write, and marks the entry undone.
//
// It restores a POINT IN TIME, so undoing an entry also reverses anything
// written after it in the same guide. That is stated in the UI rather than
// worked around: reconstructing an intermediate state by replaying later entries
// would produce a document that never existed and that nobody reviewed.
func undoCuratorEntry(appDB, udb Database, user, runID, findingID string) error {
	run, ok := loadCuratorRun(udb, runID)
	if !ok {
		return errRunNotFound
	}
	for i := range run.Entries {
		e := &run.Entries[i]
		if e.FindingID != findingID {
			continue
		}
		if e.Undone {
			return errAlreadyUndone
		}
		if e.GuideID == "" || e.PriorRev == "" {
			return errNothingToRestore
		}
		g, owner, ownerUDB, found := resolveGuide(appDB, udb, user, e.GuideID)
		if !found {
			return errGuideGone
		}
		if !(CanManageShared(user, owner, false) || g.sharedForEdit()) {
			return errNoEditAccess
		}
		rev, revOK := loadRevision(ownerUDB, e.GuideID, e.PriorRev)
		if !revOK {
			return errRevisionAged
		}
		restored := rev.Guide
		restored.ID = e.GuideID
		saveGuideRev(ownerUDB, restored, "Undid curator "+e.Kind+": "+e.Topic)
		e.Undone = true
		saveCuratorRun(udb, run)
		return nil
	}
	return errEntryNotFound
}

// curatorErr is this file's error type; the messages are user-facing (they
// surface in the digest UI), so they say what to do rather than what failed.
type curatorErr string

func (e curatorErr) Error() string { return string(e) }

const (
	errNoStore          = curatorErr("no store for that user")
	errRunNotFound      = curatorErr("that curator run is no longer in the digest history")
	errEntryNotFound    = curatorErr("no such entry in that run")
	errAlreadyUndone    = curatorErr("that change has already been undone")
	errNothingToRestore = curatorErr("that entry changed no document, so there is nothing to undo")
	errGuideGone        = curatorErr("that guide no longer exists")
	errNoEditAccess     = curatorErr("you don't have edit access to that guide")
	errRevisionAged     = curatorErr("the revision this would restore has aged out of the guide's history")
)

// curatorRunAge reports how long ago a run started, for the digest list.
func curatorRunAge(r CuratorRun) string {
	t, err := time.Parse(time.RFC3339, r.Started)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + "h ago"
	default:
		return itoa(int(d.Hours()/24)) + "d ago"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
