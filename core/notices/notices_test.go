package notices

import (
	"reflect"
	"testing"
)

// memStore is a Store for tests. The leaf cannot reach core's DBase and does
// not need to: nothing here exercises serialization, only round-tripping. Same
// shape as core/notes' test store, for the same reason.
type memStore struct{ m map[string]any }

func newStore(t *testing.T) *memStore {
	t.Helper()
	return &memStore{m: map[string]any{}}
}

func (s *memStore) Get(table, key string, out interface{}) bool {
	v, ok := s.m[table+"\x00"+key]
	if !ok {
		return false
	}
	dst, src := reflect.ValueOf(out), reflect.ValueOf(v)
	if dst.Kind() != reflect.Ptr || !src.Type().AssignableTo(dst.Elem().Type()) {
		return false
	}
	dst.Elem().Set(src)
	return true
}

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

// The count IS the feature. A recurring task that fires hourly into a refusal
// says the same sentence twenty-four times a day, and a surface that shows it
// twenty-four times is one the owner turns off inside a week, which means it is
// off when something new happens.
func TestRepeatsFoldIntoOneRow(t *testing.T) {
	db := newStore(t)
	n := Notice{Owner: "alice", Agent: "nightly", Kind: KindStopped, Title: "send_email was not run"}

	first, isNew := Record(db, n)
	if !isNew || first.Count != 1 {
		t.Fatalf("first occurrence: new=%v count=%d", isNew, first.Count)
	}
	for i := 0; i < 23; i++ {
		if _, isNew := Record(db, n); isNew {
			t.Fatal("a repeat was reported as the first occurrence, which would forward it again")
		}
	}
	list := List(db, "alice")
	if len(list) != 1 {
		t.Fatalf("expected one row, got %d", len(list))
	}
	if list[0].Count != 24 {
		t.Errorf("count did not follow the occurrences: %d", list[0].Count)
	}
	// The badge counts NOTICES, not occurrences: a row that happened 24 times
	// is one thing the owner has not looked at.
	if got := Unread(db, "alice"); got != 1 {
		t.Errorf("badge reads %d, which is a number about the world rather than about the owner", got)
	}
}

// Different agents saying the same sentence are different facts, and folding
// them would hide which schedule is broken.
func TestNoticesFoldPerAgentAndKind(t *testing.T) {
	db := newStore(t)
	Record(db, Notice{Owner: "alice", Agent: "nightly", Kind: KindStopped, Title: "send_email was not run"})
	Record(db, Notice{Owner: "alice", Agent: "weekly", Kind: KindStopped, Title: "send_email was not run"})
	Record(db, Notice{Owner: "alice", Agent: "nightly", Kind: KindBlocked, Title: "send_email was not run"})
	if got := len(List(db, "alice")); got != 3 {
		t.Errorf("expected three distinct notices, got %d", got)
	}
	// And another owner's notices are not visible here at all.
	Record(db, Notice{Owner: "bob", Agent: "nightly", Kind: KindStopped, Title: "send_email was not run"})
	if got := len(List(db, "alice")); got != 3 {
		t.Errorf("another owner's notice leaked into this list: %d", got)
	}
}

// A repeat is news about the world, so it comes back unread. Reading it does
// not reset the count: how often it has happened stays true afterwards, and
// that number is the only evidence that says chronic rather than blip.
func TestARepeatReopensAndKeepsItsCount(t *testing.T) {
	db := newStore(t)
	n := Notice{Owner: "alice", Agent: "nightly", Kind: KindStopped, Title: "send_email was not run"}
	stored, _ := Record(db, n)
	MarkRead(db, "alice", stored.ID)
	if Unread(db, "alice") != 0 {
		t.Fatal("marking read did not take")
	}
	again, _ := Record(db, n)
	if again.Read {
		t.Error("it happened again and the owner is not being told")
	}
	if again.Count != 2 {
		t.Errorf("count reset on re-raise: %d", again.Count)
	}
	MarkRead(db, "alice", stored.ID)
	if got := List(db, "alice")[0].Count; got != 2 {
		t.Errorf("reading it threw away how often it had happened: %d", got)
	}
}

// Dismiss is not a mute. The thing coming back is correct for a condition, and
// pretending otherwise would make this a place to hide bad news.
func TestDismissingSomethingThatRecursBringsItBack(t *testing.T) {
	db := newStore(t)
	n := Notice{Owner: "alice", Agent: "nightly", Kind: KindStopped, Title: "send_email was not run"}
	stored, _ := Record(db, n)
	Remove(db, "alice", stored.ID)
	if len(List(db, "alice")) != 0 {
		t.Fatal("dismiss did not remove it")
	}
	back, isNew := Record(db, n)
	if !isNew {
		t.Error("a dismissed notice did not come back as a first occurrence, so it would never forward again")
	}
	if back.Count != 1 {
		t.Errorf("it came back carrying the old count: %d", back.Count)
	}
}

// Unread first, then most recent. An old unread notice that keeps recurring has
// a new Last and must not sink under a one-off from this morning.
func TestUnreadSortsAheadOfRead(t *testing.T) {
	db := newStore(t)
	old, _ := Record(db, Notice{Owner: "alice", Agent: "a", Kind: KindStopped, Title: "older"})
	Record(db, Notice{Owner: "alice", Agent: "b", Kind: KindStopped, Title: "newer"})
	MarkRead(db, "alice", old.ID)

	list := List(db, "alice")
	if len(list) != 2 || list[0].Title != "newer" {
		t.Fatalf("unread did not sort first: %+v", list)
	}
	// Re-raising the read one puts it back on top.
	Record(db, Notice{Owner: "alice", Agent: "a", Kind: KindStopped, Title: "older"})
	if list = List(db, "alice"); list[0].Title != "older" {
		t.Errorf("a recurrence did not resurface: %+v", list)
	}
}

// Nothing without an owner or a title is storable: a notice nobody can be shown
// is a row that accumulates forever in a table nobody reads.
func TestAnUnaddressedNoticeIsNotStored(t *testing.T) {
	db := newStore(t)
	for _, n := range []Notice{
		{Owner: "", Title: "something"},
		{Owner: "alice", Title: "   "},
	} {
		if _, isNew := Record(db, n); isNew {
			t.Errorf("stored an unaddressed notice: %+v", n)
		}
	}
	if got := len(List(db, "alice")); got != 0 {
		t.Errorf("%d rows written anyway", got)
	}
}

// The bell is chrome: a deployment with no storage wired still has to render
// one, and a signed-out viewer has to get an empty list rather than an error.
// A notifications surface that can take a page down is worse than no surface.
func TestAMissingStoreIsQuiet(t *testing.T) {
	if got := List(nil, "alice"); got != nil {
		t.Errorf("a nil store listed %d notices", len(got))
	}
	if got := Unread(nil, "alice"); got != 0 {
		t.Errorf("a nil store counted %d unread", got)
	}
	if _, isNew := Record(nil, Notice{Owner: "alice", Title: "something"}); isNew {
		t.Error("a nil store reported a first occurrence, which would forward it")
	}
	// And the mutators do not panic on one either.
	MarkRead(nil, "alice", "x")
	MarkAllRead(nil, "alice")
	Remove(nil, "alice", "x")
}
