package core

// Revision history for custom apps — the recovery half of app editing.
//
// The write-side guards refuse the shapes that read as a wipe (a document that
// collapsed in size, or one calling functions it no longer defines). They are
// not total, and they cannot be: a rewrite that quietly drops a feature is
// internally consistent, parses, loads, and looks exactly like an intentional
// revision. confirm_rewrite punches through the rest on purpose.
//
// So keep the last few versions. Two things follow from that. An author who
// destroys an app can put it back in one call instead of reconstructing it
// from memory — which is what produced the damage in the first place. And the
// guards get cheaper to be wrong about: with nothing behind them, a refusal
// has to be conservative or it blocks real work; with history behind them, the
// worst case of letting something through is one revert.
//
// History lives in its OWN table, not on the spec. A spec is read on every
// single page serve; hanging five copies of a 20KB document off it would put
// that cost on every request to buy something only the author ever reads.

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// AppRevisionTable holds one revision ring per app slug.
const AppRevisionTable = "app_spec_revisions"

// AppRevisionsKept is the ring depth. Deep enough to walk back through a bad
// authoring session (the observed one burned four saves), shallow enough that
// the stored bytes stay incidental.
const AppRevisionsKept = 6

// AppSaveNoHistory, passed as the reason to SaveAppSpecAs, suppresses the
// snapshot. The rollback paths use it: restoring a known-good revision after a
// failed edit must not file the broken one as history.
const AppSaveNoHistory = "-"

// AppRevision is one superseded version of an app, kept verbatim.
type AppRevision struct {
	// Seq is the revision's identity — a per-app counter that never repeats and
	// never shifts. A timestamp cannot do this job: Updated has second
	// resolution, two saves can land inside the same second, and then "revert
	// to 04:12:16Z" names two different documents. Position in the listing
	// can't either, since every new edit renumbers it. Seq is stable from the
	// moment it is assigned, which is what a reference an author copies out of
	// one tool result and pastes into the next has to be.
	Seq int `json:"seq"`
	// Stamp is the Updated time of the version being preserved — for reading,
	// not for addressing.
	Stamp string `json:"stamp"`
	// Reason names the edit that REPLACED this version ("update",
	// "replace_function drawBird"), so a list of revisions reads as a history
	// of what happened rather than a column of timestamps.
	Reason string `json:"reason,omitempty"`
	// Spec is the whole prior AppSpec, marshaled. Storing the spec rather than
	// just the page keeps a revert honest — data sources, actions and record
	// key move together with the document they were written against.
	Spec json.RawMessage `json:"spec"`
}

// appRevisionRing is the stored shape: one record per slug, oldest first.
// NextSeq keeps counting after entries fall off the back, so an id is never
// reused for a different document.
type appRevisionRing struct {
	Entries []AppRevision `json:"entries"`
	NextSeq int           `json:"next_seq"`
}

// PushAppRevision files a superseded spec, trimming the ring to depth, and
// returns the id it was filed under (0 when nothing was stored). Called by
// SaveAppSpecAs when a save actually changes the page; exported for callers
// that want to snapshot explicitly.
func PushAppRevision(prior AppSpec, reason string) int {
	db := appSpecStore(prior.Owner)
	if db == nil || prior.Slug == "" {
		return 0
	}
	blob, err := json.Marshal(prior)
	if err != nil {
		return 0
	}
	var ring appRevisionRing
	db.Get(AppRevisionTable, prior.Slug, &ring)
	if ring.NextSeq < 1 {
		ring.NextSeq = 1
	}
	seq := ring.NextSeq
	ring.NextSeq++
	ring.Entries = append(ring.Entries, AppRevision{
		Seq:    seq,
		Stamp:  prior.Updated,
		Reason: reason,
		Spec:   blob,
	})
	if n := len(ring.Entries); n > AppRevisionsKept {
		ring.Entries = ring.Entries[n-AppRevisionsKept:]
	}
	db.Set(AppRevisionTable, prior.Slug, ring)
	return seq
}

// ListAppRevisions returns an app's kept revisions, NEWEST first — the order
// an author reads them in, since the one worth reverting to is almost always
// the last good one rather than the oldest.
func ListAppRevisions(owner, slug string) []AppRevision {
	db := appSpecStore(owner)
	if db == nil {
		return nil
	}
	var ring appRevisionRing
	if !db.Get(AppRevisionTable, slug, &ring) {
		return nil
	}
	out := make([]AppRevision, 0, len(ring.Entries))
	for i := len(ring.Entries) - 1; i >= 0; i-- {
		out = append(out, ring.Entries[i])
	}
	return out
}

// LoadAppRevision resolves one kept revision back into a spec. ref is a
// revision id ("7" or "#7"), or a timestamp for an author working from what a
// tool result printed, or empty for the most recent. A timestamp that matches
// several (two saves inside one second) resolves to the newest — the id exists
// precisely so nobody has to rely on that.
func LoadAppRevision(owner, slug, ref string) (AppSpec, bool) {
	rev, ok := FindAppRevision(owner, slug, ref)
	if !ok {
		return AppSpec{}, false
	}
	var spec AppSpec
	if err := json.Unmarshal(rev.Spec, &spec); err != nil {
		return AppSpec{}, false
	}
	return spec, true
}

// FindAppRevision resolves a reference to a kept revision without unmarshaling
// the spec it holds.
func FindAppRevision(owner, slug, ref string) (AppRevision, bool) {
	revs := ListAppRevisions(owner, slug)
	if len(revs) == 0 {
		return AppRevision{}, false
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
		return AppRevision{}, false
	}
	for _, r := range revs {
		if r.Stamp == ref {
			return r, true
		}
	}
	return AppRevision{}, false
}

// DeleteAppRevisions drops an app's history, called when the app itself goes.
func DeleteAppRevisions(owner, slug string) {
	if db := appSpecStore(owner); db != nil {
		db.Unset(AppRevisionTable, slug)
	}
}

// AppRevisionAge renders how long ago a revision was superseded, for a listing
// an author reads at a glance. Returns "" for an unparsable stamp.
func AppRevisionAge(stamp string) string {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return pluralAge(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return pluralAge(int(d.Hours()), "hour")
	default:
		return pluralAge(int(d.Hours()/24), "day")
	}
}

func pluralAge(n int, unit string) string {
	s := itoaSmall(n) + " " + unit
	if n != 1 {
		s += "s"
	}
	return s + " ago"
}

// itoaSmall avoids pulling strconv into this file for one call.
func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// specPageChanged reports whether a save actually alters what the app serves.
// Metadata-only writes (enable/disable, publish token, a shared-flag flip) are
// not revisions of the document and must not push the real history out of the
// ring.
func specPageChanged(prior, next AppSpec) bool {
	return !bytes.Equal(prior.Page, next.Page) || !bytes.Equal(prior.Sections, next.Sections)
}
