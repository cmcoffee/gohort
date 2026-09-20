package peershare

// Peer sharing is the middle rung of the governance model: this record, these
// named people, nobody else. The recipient list lives on the record because
// that is what an owner edits and an admin audits; the index below is what
// makes it discoverable from the other side.
//
// Without that half the field is decorative, and that is not hypothetical:
// agents have carried AllowedUsers for some time with recipient-side
// resolution left as "a separate step", so an owner can pick recipients today
// and nothing appears for them.

import (
	"testing"
)

// memStore is a Store for tests. The leaf cannot reach core's DBase and does
// not need to: nothing here exercises serialization, only round-tripping.
type memStore struct{ m map[string]any }

func newStore() *memStore { return &memStore{m: map[string]any{}} }

func (s *memStore) Set(table, key string, value interface{}) { s.m[table+"\x00"+key] = value }
func (s *memStore) Unset(table, key string)                  { delete(s.m, table+"\x00"+key) }
func (s *memStore) Keys(table string) []string {
	var out []string
	for k := range s.m {
		if i := len(table); len(k) > i && k[:i] == table && k[i] == 0 {
			out = append(out, k[i+1:])
		}
	}
	return out
}

func TestASharedRecordIsFindableByItsRecipient(t *testing.T) {
	db := newStore()
	SetRecipients(db, "shared_things", "alice", "thing-1", []string{"bob", "carol"})

	for _, who := range []string{"bob", "carol"} {
		refs := List(db, "shared_things", who)
		if len(refs) != 1 || refs[0].Owner != "alice" || refs[0].ID != "thing-1" {
			t.Errorf("%s cannot find what was shared with them: %+v", who, refs)
		}
	}
	if got := List(db, "shared_things", "dana"); len(got) != 0 {
		t.Errorf("somebody who was not named can see it: %+v", got)
	}
}

// The owner edits a SET, so the index takes the whole list and works out the
// difference. A caller computing additions and removals itself would be a
// second place that knows the rule, and the one that drifts is whichever runs
// less often.
func TestRevokingDropsTheRecipient(t *testing.T) {
	db := newStore()
	SetRecipients(db, "shared_things", "alice", "thing-1", []string{"bob", "carol"})
	SetRecipients(db, "shared_things", "alice", "thing-1", []string{"carol"})

	if got := List(db, "shared_things", "bob"); len(got) != 0 {
		t.Errorf("a revoked recipient still finds it: %+v", got)
	}
	if got := List(db, "shared_things", "carol"); len(got) != 1 {
		t.Errorf("the kept recipient lost it: %+v", got)
	}
	// And unsharing entirely leaves nothing behind. An index entry outliving
	// its record points at nothing, which reads as access somebody lost.
	DropAll(db, "shared_things", "alice", "thing-1")
	if got := List(db, "shared_things", "carol"); len(got) != 0 {
		t.Errorf("entries survive the record: %+v", got)
	}
}

// One record's shares must not disturb another's, including another owner's
// record that happens to have the same id.
func TestSharesAreScopedToTheirRecord(t *testing.T) {
	db := newStore()
	SetRecipients(db, "shared_things", "alice", "same-id", []string{"bob"})
	SetRecipients(db, "shared_things", "dana", "same-id", []string{"bob"})
	SetRecipients(db, "shared_things", "alice", "other", []string{"bob"})

	if got := List(db, "shared_things", "bob"); len(got) != 3 {
		t.Fatalf("bob should see three distinct shares: %+v", got)
	}
	// Revoking one leaves the other two, including the same id under a
	// different owner.
	SetRecipients(db, "shared_things", "alice", "same-id", nil)
	got := List(db, "shared_things", "bob")
	if len(got) != 2 {
		t.Fatalf("revoking one share hit another: %+v", got)
	}
	for _, r := range got {
		if r.Owner == "alice" && r.ID == "same-id" {
			t.Error("the revoked one is still there")
		}
	}
}

// Sharing with yourself is not a share. It would put an owner's own record in
// their shared-with-me list, where it would appear twice.
func TestSharingWithYourselfIsNotAShare(t *testing.T) {
	db := newStore()
	SetRecipients(db, "shared_things", "alice", "thing-1", []string{"alice", "  ", "bob"})
	if got := List(db, "shared_things", "alice"); len(got) != 0 {
		t.Errorf("the owner appears as their own recipient: %+v", got)
	}
	if got := List(db, "shared_things", "bob"); len(got) != 1 {
		t.Errorf("a real recipient was lost alongside the blanks: %+v", got)
	}
}
