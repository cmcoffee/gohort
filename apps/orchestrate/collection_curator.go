package orchestrate

// The collection curator: an agent's collection kept in step with a source it
// did not write.
//
// Autofill is the other half of this and answers a different question. It goes
// looking on the open web for things that might belong, judges them, and adds
// what survives. It only ever ADDS, which is correct for a search: the web did
// not delete anything, you simply searched again.
//
// A curated collection is the inverse. Somewhere else is authoritative — a
// wiki space, a document system — and this collection is a copy of it. Then
// the interesting events are the ones autofill has no concept of: a document
// was EDITED and the copy is now wrong, or a document was DELETED and the copy
// is now a ghost that still answers questions. Both are worse than a missing
// document, because a stale copy is indistinguishable from a current one at
// the point where it gets quoted back to somebody.
//
// So the curator runs off an enumeration rather than a search
// (core.ReferenceEnumerator), and keeps a per-document ledger of what it put
// where. The ledger is what makes the three-way comparison possible: what the
// source has now, what was copied last time, and what is in the chunk store.
//
// The ledger lives in its own table rather than on the Collection record. A
// collection is read on every picker render and every attach; hanging a few
// hundred document rows off it would put that cost on all of them to serve
// something only a sync ever reads. Same call core made for app revisions.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// curatedDocsTable holds one ledger per (collection, source item).
const curatedDocsTable = "collection_curated_docs"

// curatedDoc is what the last sync left behind for one source document.
type curatedDoc struct {
	DocID string `json:"doc_id"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
	// Version is the source's change marker as it stood when this copy was
	// made. Opaque: the next sync asks only whether it still matches.
	Version string `json:"version,omitempty"`
	// ReportID is where the copy went in the chunk store. Kept because it is
	// the only handle a deletion has — without it, retiring a document means
	// guessing which chunks were its.
	ReportID string    `json:"report_id"`
	At       time.Time `json:"at"`
}

// curatedLedger is the stored shape: what this collection holds from one
// source item, keyed by the source's own document id.
type curatedLedger struct {
	Docs map[string]curatedDoc `json:"docs"`
	// LastSync and LastOutcome are for a person asking "did this run, and how
	// did it go" without opening a log.
	LastSync    time.Time `json:"last_sync,omitempty"`
	LastOutcome string    `json:"last_outcome,omitempty"`
}

// curatedLedgerKey names one collection's copy of one source item. Both parts
// are in the key because a collection may curate from several items (two wiki
// spaces), and a document id is only unique within its own item.
func curatedLedgerKey(collectionID, sourceKind, itemID string) string {
	return collectionID + "\x00" + sourceKind + "\x00" + itemID
}

func loadCuratedLedger(udb Database, collectionID, sourceKind, itemID string) curatedLedger {
	var l curatedLedger
	if udb != nil {
		udb.Get(curatedDocsTable, curatedLedgerKey(collectionID, sourceKind, itemID), &l)
	}
	if l.Docs == nil {
		l.Docs = map[string]curatedDoc{}
	}
	return l
}

func saveCuratedLedger(udb Database, collectionID, sourceKind, itemID string, l curatedLedger) {
	if udb != nil {
		udb.Set(curatedDocsTable, curatedLedgerKey(collectionID, sourceKind, itemID), l)
	}
}

// dropCuratedLedger forgets a collection's sync state for one item. Called
// when the collection goes: the ledger would otherwise name chunks that no
// longer exist and a collection reusing the id would inherit it.
func dropCuratedLedger(udb Database, collectionID, sourceKind, itemID string) {
	if udb != nil {
		udb.Unset(curatedDocsTable, curatedLedgerKey(collectionID, sourceKind, itemID))
	}
}

// curateResult is what one sync did, for a log line and a returned summary.
type curateResult struct {
	Added     int
	Updated   int
	Removed   int
	Unchanged int
	Failed    int
	// Notes carries the per-document failures. Bounded: a source that is down
	// fails every document, and a hundred identical lines say no more than
	// three of them plus a count.
	Notes []string
}

func (r curateResult) String() string {
	parts := []string{}
	for _, p := range []struct {
		n     int
		label string
	}{{r.Added, "added"}, {r.Updated, "updated"}, {r.Removed, "removed"}, {r.Unchanged, "unchanged"}, {r.Failed, "failed"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.label))
		}
	}
	if len(parts) == 0 {
		return "nothing to do"
	}
	return strings.Join(parts, ", ")
}

// maxCurateNotes bounds the failure list a result carries back.
const maxCurateNotes = 3

func (r *curateResult) note(format string, args ...any) {
	if len(r.Notes) < maxCurateNotes {
		r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
	}
}

// curateSpec is one sync: which collection, from which item of which source.
type curateSpec struct {
	User       string
	Collection Collection
	Source     ReferenceSource
	Item       string
}

// curatedReportID is where one source document's copy lives in the chunk
// store. Derived rather than random, so a re-pull of the same document
// REPLACES its chunks (ingest deletes by reportID first) instead of stacking a
// second copy beside the first.
func curatedReportID(collectionID, sourceKind, itemID, docID string) string {
	return "curated-" + collectionID + "-" + sourceKind + "-" + itemID + "-" + docID
}

// curateCollection brings one collection's copy of one source item up to date:
// pull what is new or changed, retire what the source no longer has, and leave
// the rest alone.
//
// The enumeration is authoritative for what EXISTS and the ledger is
// authoritative for what was copied. A document present in both with an
// unchanged version is not fetched at all, which is what keeps a sync over a
// large space cheap enough to run on a schedule.
func curateCollection(ctx context.Context, udb, chunkDB Database, spec curateSpec) (curateResult, error) {
	var res curateResult
	enum, ok := spec.Source.(ReferenceEnumerator)
	if !ok {
		return res, fmt.Errorf("%s cannot list what it holds, so a collection cannot be kept in step with it — attach it as a reference source instead", spec.Source.Label())
	}
	remote, err := enum.Documents(ctx, spec.User, spec.Item)
	if err != nil {
		return res, fmt.Errorf("could not list what %s holds: %w", spec.Source.Label(), err)
	}

	kind := spec.Source.Kind()
	ledger := loadCuratedLedger(udb, spec.Collection.ID, kind, spec.Item)

	// The whole remote set, before anything is decided, because the deletion
	// pass below asks "is this still there" of it.
	present := make(map[string]ReferenceDoc, len(remote))
	for _, d := range remote {
		if strings.TrimSpace(d.ID) != "" {
			present[d.ID] = d
		}
	}

	// Refuse to empty a collection on the strength of an empty answer. A
	// source that has genuinely been emptied and one whose auth quietly
	// expired return the same thing here, and only one of those should cost
	// somebody their copy. A source that knows it failed is expected to return
	// an error, which is handled above; this is for the one that does not.
	if len(present) == 0 && len(ledger.Docs) > 0 {
		return res, fmt.Errorf("%s returned nothing at all while this collection holds %d document(s) from it — refusing to treat that as a deletion of all of them; check the connection and run it again", spec.Source.Label(), len(ledger.Docs))
	}

	// Sorted, so a run over the same set does the same things in the same
	// order and a progress line means something.
	ids := make([]string, 0, len(present))
	for id := range present {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	began := time.Now()
	for i, id := range ids {
		if err := ctx.Err(); err != nil {
			// Ledger is saved below rather than lost: what was copied before
			// the cancel IS copied, and a sync that forgot it would re-pull it
			// all next time.
			res.note("stopped early: %v", err)
			break
		}
		doc := present[id]
		// ~2s tick rather than per document: a space of four hundred pages
		// would otherwise spend the run writing progress lines.
		if i%10 == 0 || time.Since(began) > 2*time.Second {
			ReportMaintenanceProgress(ctx, fmt.Sprintf("%d of %d · %s · %s",
				i+1, len(ids), spec.Collection.Name, time.Since(began).Round(time.Second)))
		}
		prior, known := ledger.Docs[id]
		if known && sameReferenceVersion(prior, doc) {
			res.Unchanged++
			continue
		}
		body, err := enum.DocumentBody(ctx, spec.User, spec.Item, id)
		if err != nil {
			res.Failed++
			res.note("%s: %v", docLabel(doc), err)
			continue
		}
		if strings.TrimSpace(body) == "" {
			// Nothing to copy is not a failure and not a deletion: the
			// document exists and is empty. Leave any prior copy alone rather
			// than replacing it with nothing.
			res.Failed++
			res.note("%s: nothing to copy", docLabel(doc))
			continue
		}
		reportID := curatedReportID(spec.Collection.ID, kind, spec.Item, id)
		title := strings.TrimSpace(doc.Title)
		if title == "" {
			title = id
		}
		if n := IngestDocument(ctx, chunkDB, collectionSource(spec.Collection.ID), reportID, title, body); n == 0 {
			res.Failed++
			res.note("%s: nothing was indexed", docLabel(doc))
			continue
		}
		if known {
			res.Updated++
		} else {
			res.Added++
		}
		ledger.Docs[id] = curatedDoc{
			DocID:    id,
			Title:    title,
			URL:      doc.URL,
			Version:  changeMarker(doc),
			ReportID: reportID,
			At:       time.Now(),
		}
	}

	// Retire what the source no longer has. Last, so a failure above cannot
	// leave a collection both un-updated AND emptied.
	for id, kept := range ledger.Docs {
		if _, still := present[id]; still {
			continue
		}
		DeleteReportChunks(chunkDB, kept.ReportID)
		delete(ledger.Docs, id)
		res.Removed++
	}

	ledger.LastSync = time.Now()
	ledger.LastOutcome = res.String()
	saveCuratedLedger(udb, spec.Collection.ID, kind, spec.Item, ledger)
	ReportMaintenanceOutcome(ctx, fmt.Sprintf("%s · %s", spec.Collection.Name, res.String()))
	Log("[orchestrate.curator] collection=%s source=%s item=%s %s",
		spec.Collection.ID, kind, spec.Item, res.String())
	return res, nil
}

// sameReferenceVersion reports whether a document is unchanged since it was
// copied. A source with neither a version nor a timestamp cannot say, and the
// honest answer to "has this changed" is then no — so it is re-pulled every
// time rather than assumed current.
func sameReferenceVersion(prior curatedDoc, now ReferenceDoc) bool {
	marker := changeMarker(now)
	return marker != "" && marker == prior.Version
}

// changeMarker is the source's version if it has one, else its timestamp.
func changeMarker(d ReferenceDoc) string {
	if v := strings.TrimSpace(d.Version); v != "" {
		return v
	}
	if !d.Updated.IsZero() {
		return d.Updated.UTC().Format(time.RFC3339)
	}
	return ""
}

func docLabel(d ReferenceDoc) string {
	if t := strings.TrimSpace(d.Title); t != "" {
		return t
	}
	return d.ID
}
