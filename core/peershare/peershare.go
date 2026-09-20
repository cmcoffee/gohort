// Package peershare is the index that makes a peer share findable from the
// other side.
//
// The recipient list lives on the RECORD (AllowedUsers, the field agents,
// credentials, collections, skills and temp tools all carry) because that is
// what an owner edits and an admin audits. This is the DERIVED lookup, and
// without it the field is decorative: agents carried AllowedUsers for a long
// time with recipient-side resolution left as "a separate step", so an owner
// could pick recipients and nothing ever appeared for them.
//
// A subpackage rather than a file in core for the reason core/pacing and
// core/notes are: core sits at its export ceiling, and every exported name
// there lands in the namespace of every file that dot-imports it. Adding four
// more was what tripped the ceiling test, which is that test working.
//
// Storage only. WHAT is shared and whether a recipient may act on it belongs to
// whatever owns the record.
package peershare

import (
	"sort"
	"strings"
)

// Store is the slice of a key/value database this leaf uses. core's Database
// satisfies it structurally, so callers pass the handle they already have.
type Store interface {
	Set(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

// PeerShareRef is one shared record, from the recipient's side.
type Ref struct {
	Owner string
	ID    string
}

// key is recipient-first so a recipient's shares are a prefix scan,
// which is the read that happens on every list. The owner and id follow, joined
// by a byte neither can contain.
func key(recipient, owner, id string) string {
	return recipient + "\x00" + owner + "\x00" + id
}

// SetRecipients makes the index match a record's recipient list.
//
// Given the WHOLE list rather than a delta, because the owner edits a set: a
// caller that had to compute additions and removals would be a second place
// that knows the rule, and the one that drifts is whichever runs less often.
// Removals are found by scanning for this record's existing entries, so a
// recipient dropped from the list loses access on the next read.
func SetRecipients(db Store, indexTable, owner, id string, recipients []string) {
	if db == nil || owner == "" || id == "" {
		return
	}
	want := make(map[string]bool, len(recipients))
	for _, u := range recipients {
		if u = strings.TrimSpace(u); u != "" && u != owner {
			want[u] = true // sharing with yourself is not a share
		}
	}
	suffix := "\x00" + owner + "\x00" + id
	for _, k := range db.Keys(indexTable) {
		if !strings.HasSuffix(k, suffix) {
			continue
		}
		recipient := strings.TrimSuffix(k, suffix)
		if want[recipient] {
			delete(want, recipient) // already indexed
			continue
		}
		db.Unset(indexTable, k)
	}
	for recipient := range want {
		db.Set(indexTable, key(recipient, owner, id), true)
	}
}

// List returns every record shared WITH this recipient.
func List(db Store, indexTable, recipient string) []Ref {
	var out []Ref
	if db == nil || strings.TrimSpace(recipient) == "" {
		return out
	}
	prefix := recipient + "\x00"
	for _, k := range db.Keys(indexTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		owner, id, ok := strings.Cut(k[len(prefix):], "\x00")
		if !ok || owner == "" || id == "" {
			continue
		}
		out = append(out, Ref{Owner: owner, ID: id})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// DropAll removes every entry for one record, for use when the record
// itself is deleted. An index entry outliving its record is a row pointing at
// nothing, which reads to the recipient as something they lost access to rather
// than something that is gone.
func DropAll(db Store, indexTable, owner, id string) {
	SetRecipients(db, indexTable, owner, id, nil)
}
