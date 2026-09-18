// Package revisions keeps the last few versions of an authored definition, so
// an edit that made something worse can be undone instead of reconstructed.
//
// Nothing versioned the definitions that decide how an agent behaves. Editing
// a persona, a skill's instructions, a machine phase or a pipeline stage
// overwrote the old one: saveAgent, SaveSkill, SaveMachineDef and
// SavePipelineDef are four straight overwrites. After a behaviour regression
// there was no answer to "what did this say last week", and no way back. One
// authoring session burned four saves walking an agent further from what it
// used to do, and every one of them was final.
//
// The shape is taken from core/appspec_revisions.go, which is this same
// feature for authored apps and has been carrying it since. Rather than copy
// that ring four more times, this generalizes it: one store, keyed by kind and
// id, holding the prior value verbatim as JSON.
//
// Two things follow from keeping history. An author who wrecks a definition
// can put it back in one call rather than rebuilding it from memory, which is
// what produced the damage in the first place. And the write-side guards get
// cheaper to be wrong about: with nothing behind them a refusal has to be
// conservative or it blocks real work, and with history behind them the worst
// case of letting an edit through is one revert.
//
// A ring of six is deliberate. This is "undo a mistake", not an audit log. An
// audit trail is a different feature with different retention, and building
// one here would put unbounded copies of every definition on disk to serve a
// question nobody has asked.
//
// A subpackage rather than another file in core: core sits at its file and
// export ceilings, and this is a self-contained store that only the surfaces
// doing the saving need. Store is declared here rather than imported for the
// same reason. It is the three methods this package uses, and core.Database
// satisfies it structurally, so a caller passes its existing handle.
package revisions

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Table holds every kind's rings. One table rather than one per kind, because
// the kind is already in the key and four tables would buy nothing but four
// names to keep in step.
const Table = "definition_revisions"

// Kept is the ring depth. Deep enough to walk back through a bad authoring
// session (the observed one burned four saves), shallow enough that the stored
// bytes stay incidental next to the definitions themselves.
const Kept = 6

// NoHistory, passed as a reason, suppresses the snapshot. Rollback uses it:
// restoring a known-good version after a bad edit must not file the bad one as
// history, or the ring fills with the thing being undone.
const NoHistory = "-"

// The kinds. Declared here so the string that keys a ring is written once, not
// once per save site where a typo would silently start a second history.
const (
	KindAgent    = "agent"
	KindSkill    = "skill"
	KindMachine  = "machine"
	KindPipeline = "pipeline"
)

// Store is the slice of a key-value database this package uses.
type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
}

// Revision is one superseded version of a definition, kept verbatim.
type Revision struct {
	// Seq is the revision's identity: a per-definition counter that never
	// repeats and never shifts. A timestamp cannot do this job, since two saves
	// can land inside the same second and then "revert to 04:12:16Z" names two
	// different records. Position in the listing cannot either, because every
	// new edit renumbers it. Seq is stable from the moment it is assigned,
	// which is what a reference an author reads in one place and types into
	// another has to be.
	Seq int `json:"seq"`
	// Stamp is when the preserved version was last written, for reading rather
	// than for addressing.
	Stamp string `json:"stamp"`
	// Reason names the edit that REPLACED this version ("edited instructions",
	// "playbook changed", "rolled back to #4"), so a list reads as a history of
	// what happened instead of a column of timestamps.
	Reason string `json:"reason,omitempty"`
	// Body is the whole prior record, marshaled. The whole record rather than
	// the fields someone thought were interesting: a revert is only honest if
	// everything that was written together comes back together.
	Body json.RawMessage `json:"body"`
}

// ring is the stored shape, oldest first. NextSeq keeps counting after entries
// fall off the back, so an id is never reused for a different version.
type ring struct {
	Entries []Revision `json:"entries"`
	NextSeq int        `json:"next_seq"`
}

// ringKey names one definition's history.
//
// The caller composes id, and where the store is SHARED it has to carry the
// owner: skills live in RootDB keyed by username, so a bare skill id would put
// two users' histories in the same ring and hand one of them the other's
// definitions. Per-user stores (an agent's, a machine's) need only the id,
// because the database is already the tenancy boundary.
func ringKey(kind, id string) string { return kind + ":" + id }

// Push files a superseded value and returns the id it was filed under, or 0
// when nothing was stored. stamp is when the preserved version was written;
// reason names the edit that replaced it.
func Push(db Store, kind, id string, prior any, stamp time.Time, reason string) int {
	if db == nil || kind == "" || id == "" || reason == NoHistory {
		return 0
	}
	blob, err := json.Marshal(prior)
	if err != nil {
		return 0
	}
	key := ringKey(kind, id)
	var r ring
	db.Get(Table, key, &r)
	if r.NextSeq < 1 {
		r.NextSeq = 1
	}
	seq := r.NextSeq
	r.NextSeq++
	r.Entries = append(r.Entries, Revision{
		Seq:    seq,
		Stamp:  stampOf(stamp),
		Reason: reason,
		Body:   blob,
	})
	if n := len(r.Entries); n > Kept {
		r.Entries = r.Entries[n-Kept:]
	}
	db.Set(Table, key, r)
	return seq
}

// List returns a definition's kept revisions, NEWEST first. That is the order
// an author reads them in: the one worth going back to is almost always the
// last good one, not the oldest.
func List(db Store, kind, id string) []Revision {
	if db == nil || kind == "" || id == "" {
		return nil
	}
	var r ring
	if !db.Get(Table, ringKey(kind, id), &r) {
		return nil
	}
	out := make([]Revision, 0, len(r.Entries))
	for i := len(r.Entries) - 1; i >= 0; i-- {
		out = append(out, r.Entries[i])
	}
	return out
}

// Find resolves a reference to one kept revision. ref is a revision id ("4" or
// "#4"), or a stamp for someone working from what a listing printed, or empty
// for the most recent. A stamp matching several resolves to the newest; the id
// exists precisely so nobody has to rely on that.
func Find(db Store, kind, id, ref string) (Revision, bool) {
	revs := List(db, kind, id)
	if len(revs) == 0 {
		return Revision{}, false
	}
	ref = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), "#"))
	if ref == "" {
		return revs[0], true
	}
	if n, err := strconv.Atoi(ref); err == nil {
		for _, r := range revs {
			if r.Seq == n {
				return r, true
			}
		}
		return Revision{}, false
	}
	for _, r := range revs {
		if r.Stamp == ref {
			return r, true
		}
	}
	return Revision{}, false
}

// Load resolves a reference and unmarshals the kept version into out.
func Load(db Store, kind, id, ref string, out any) bool {
	rev, ok := Find(db, kind, id, ref)
	if !ok {
		return false
	}
	return json.Unmarshal(rev.Body, out) == nil
}

// Delete drops a definition's history, for when the definition itself goes.
// Left behind, it would be a ring nothing can ever name again, and a new
// definition that reused the id would inherit somebody else's past.
func Delete(db Store, kind, id string) {
	if db != nil && kind != "" && id != "" {
		db.Unset(Table, ringKey(kind, id))
	}
}

// Differs reports whether a save actually changes the definition, ignoring the
// named JSON fields. Metadata-only writes are not revisions of the thing and
// must not push the real history out of a ring six deep: a debounced editor
// fires a save per keystroke pause, and an enable/disable flip or a re-save
// that touched nothing would otherwise evict every version worth keeping.
//
// Compared as decoded JSON rather than as marshaled bytes, so field order and
// formatting cannot read as a change. When either side cannot be compared the
// answer is yes: an unreadable record is the case where history is most worth
// keeping, so the doubt files a revision rather than skipping one.
func Differs(prior, next any, ignore ...string) bool {
	a, aok := comparable(prior, ignore)
	b, bok := comparable(next, ignore)
	if !aok || !bok {
		return true
	}
	return !reflect.DeepEqual(a, b)
}

func comparable(v any, ignore []string) (map[string]any, bool) {
	blob, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, false
	}
	for _, k := range ignore {
		delete(m, k)
	}
	return m, true
}

// stampOf renders a time for the Stamp field, empty for a zero time so a
// listing shows nothing rather than year 1.
func stampOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
