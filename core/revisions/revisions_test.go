package revisions

import (
	"encoding/json"
	"testing"
	"time"
)

// memStore is the three methods this package uses, backed by a map. Values go
// through JSON on the way in and out so the test exercises a round trip rather
// than handing back the same pointer the caller stored.
type memStore struct{ m map[string][]byte }

func newStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Get(table, key string, out interface{}) bool {
	blob, ok := s.m[table+"/"+key]
	if !ok {
		return false
	}
	return json.Unmarshal(blob, out) == nil
}

func (s *memStore) Set(table, key string, v interface{}) {
	blob, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	s.m[table+"/"+key] = blob
}

func (s *memStore) Unset(table, key string) { delete(s.m, table+"/"+key) }

type def struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Prompt  string    `json:"prompt"`
	Enabled bool      `json:"enabled"`
	Updated time.Time `json:"updated"`
}

func TestPushListAndLoad(t *testing.T) {
	db := newStore()
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	first := Push(db, KindAgent, "a1", def{ID: "a1", Name: "Scout", Prompt: "v1"}, at, "edited instructions")
	second := Push(db, KindAgent, "a1", def{ID: "a1", Name: "Scout", Prompt: "v2"}, at.Add(time.Hour), "edited instructions")
	if first != 1 || second != 2 {
		t.Fatalf("sequence ids = %d, %d; want 1, 2", first, second)
	}

	revs := List(db, KindAgent, "a1")
	if len(revs) != 2 {
		t.Fatalf("got %d revisions, want 2", len(revs))
	}
	// Newest first: the one worth going back to is almost always the last good
	// one, so a listing that starts at the oldest reads backwards.
	if revs[0].Seq != 2 || revs[1].Seq != 1 {
		t.Errorf("order = %d, %d; want newest first", revs[0].Seq, revs[1].Seq)
	}
	if revs[0].Stamp != "2026-09-17T13:00:00Z" {
		t.Errorf("stamp = %q", revs[0].Stamp)
	}
	if revs[0].Reason != "edited instructions" {
		t.Errorf("reason = %q", revs[0].Reason)
	}

	var got def
	if !Load(db, KindAgent, "a1", "1", &got) {
		t.Fatal("revision #1 did not load")
	}
	if got.Prompt != "v1" {
		t.Errorf("loaded prompt = %q, want v1", got.Prompt)
	}
	// Empty ref means the most recent, which is what a bare "undo" wants.
	if !Load(db, KindAgent, "a1", "", &got) || got.Prompt != "v2" {
		t.Errorf("empty ref loaded %q, want v2", got.Prompt)
	}
	if !Load(db, KindAgent, "a1", "#1", &got) || got.Prompt != "v1" {
		t.Errorf("#-prefixed ref loaded %q, want v1", got.Prompt)
	}
	if _, ok := Find(db, KindAgent, "a1", "99"); ok {
		t.Error("a revision id that was never issued must not resolve")
	}
}

// Kinds and ids are separate rings. A shared id across two kinds, or two ids
// of one kind, must not be able to hand back each other's history.
func TestRingsAreKeyedByKindAndID(t *testing.T) {
	db := newStore()
	at := time.Now()
	Push(db, KindAgent, "x", def{Prompt: "agent"}, at, "u")
	Push(db, KindSkill, "x", def{Prompt: "skill"}, at, "u")
	Push(db, KindAgent, "y", def{Prompt: "other"}, at, "u")

	var got def
	if !Load(db, KindAgent, "x", "", &got) || got.Prompt != "agent" {
		t.Errorf("agent ring = %q", got.Prompt)
	}
	if !Load(db, KindSkill, "x", "", &got) || got.Prompt != "skill" {
		t.Errorf("skill ring = %q", got.Prompt)
	}
	// Sequence numbers are per ring, so a second definition starts at 1.
	if revs := List(db, KindAgent, "y"); len(revs) != 1 || revs[0].Seq != 1 {
		t.Errorf("second definition's ring = %+v", revs)
	}
}

// Six deep, and the ids keep climbing after entries fall off the back so a
// reference never names a different version than it did when it was printed.
func TestRingTrimsAndIDsNeverRepeat(t *testing.T) {
	db := newStore()
	at := time.Now()
	for i := 1; i <= Kept+3; i++ {
		Push(db, KindMachine, "m", def{Prompt: string(rune('a' + i))}, at, "u")
	}
	revs := List(db, KindMachine, "m")
	if len(revs) != Kept {
		t.Fatalf("ring holds %d, want %d", len(revs), Kept)
	}
	if revs[0].Seq != Kept+3 {
		t.Errorf("newest seq = %d, want %d", revs[0].Seq, Kept+3)
	}
	if _, ok := Find(db, KindMachine, "m", "1"); ok {
		t.Error("an evicted revision must not resolve")
	}
	if next := Push(db, KindMachine, "m", def{Prompt: "z"}, at, "u"); next != Kept+4 {
		t.Errorf("next id = %d, want %d — ids must not be reused", next, Kept+4)
	}
}

// Rollback files nothing: restoring a known-good version after a bad edit must
// not push the bad one into a ring six deep.
func TestNoHistoryReasonStoresNothing(t *testing.T) {
	db := newStore()
	if seq := Push(db, KindPipeline, "p", def{Prompt: "v1"}, time.Now(), NoHistory); seq != 0 {
		t.Errorf("seq = %d, want 0", seq)
	}
	if revs := List(db, KindPipeline, "p"); len(revs) != 0 {
		t.Errorf("ring = %+v, want empty", revs)
	}
}

func TestDeleteDropsTheRing(t *testing.T) {
	db := newStore()
	Push(db, KindAgent, "a", def{Prompt: "v1"}, time.Now(), "u")
	Delete(db, KindAgent, "a")
	if revs := List(db, KindAgent, "a"); len(revs) != 0 {
		t.Errorf("history survived the definition: %+v", revs)
	}
}

func TestDiffers(t *testing.T) {
	base := def{ID: "a", Name: "Scout", Prompt: "v1", Updated: time.Now()}

	same := base
	same.Updated = base.Updated.Add(time.Hour)
	if Differs(base, same, "updated") {
		t.Error("a re-save that changed only the timestamp is not a revision")
	}

	flipped := base
	flipped.Enabled = true
	if !Differs(base, flipped, "updated") {
		t.Error("an enable flip is a change to the record")
	}

	edited := base
	edited.Prompt = "v2"
	if !Differs(base, edited, "updated") {
		t.Error("an edited prompt must file a revision")
	}

	// Doubt files a revision. An unreadable record is exactly the case where
	// history is worth keeping, so it must not be the one that skips it.
	if !Differs(base, func() {}, "updated") {
		t.Error("an uncomparable value must be treated as changed")
	}
}

func TestNilStoreAndEmptyKeysAreNoOps(t *testing.T) {
	if seq := Push(nil, KindAgent, "a", def{}, time.Now(), "u"); seq != 0 {
		t.Errorf("nil store seq = %d", seq)
	}
	db := newStore()
	if seq := Push(db, KindAgent, "", def{}, time.Now(), "u"); seq != 0 {
		t.Error("an empty id must not open a ring")
	}
	if revs := List(nil, KindAgent, "a"); revs != nil {
		t.Error("listing a nil store must be empty")
	}
	Delete(nil, KindAgent, "a") // must not panic
}
