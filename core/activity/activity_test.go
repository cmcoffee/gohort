package activity

import (
	"strings"
	"testing"
	"time"
)

// memStore is a Store in a map.
type memStore map[string]map[string]Event

func (m memStore) Set(table, key string, v interface{}) {
	if m[table] == nil {
		m[table] = map[string]Event{}
	}
	m[table][key] = v.(Event)
}
func (m memStore) Get(table, key string, out interface{}) bool {
	e, ok := m[table][key]
	if ok {
		*out.(*Event) = e
	}
	return ok
}
func (m memStore) Unset(table, key string) { delete(m[table], key) }
func (m memStore) Keys(table string) []string {
	var out []string
	for k := range m[table] {
		out = append(out, k)
	}
	return out
}

func TestTheLogListsNewestFirstAndFilters(t *testing.T) {
	db := memStore{}
	now := time.Now()
	Record(db, Event{At: now.Add(-3 * time.Minute), Kind: SignIn, User: "alice"})
	Record(db, Event{At: now.Add(-2 * time.Minute), Kind: SignInFailed, User: "mallory", Detail: "wrong username or password"})
	Record(db, Event{At: now.Add(-time.Minute), Kind: Account, User: "alice", Detail: "added the account bob"})
	got := List(db, Filter{})
	if len(got) != 3 || got[0].Kind != Account || got[2].Kind != SignIn {
		t.Fatalf("newest first: %+v", got)
	}
	if got := List(db, Filter{Kind: SignInFailed}); len(got) != 1 || got[0].User != "mallory" {
		t.Errorf("by kind: %+v", got)
	}
	if got := List(db, Filter{User: "ALICE"}); len(got) != 2 {
		t.Errorf("by user, any case: %+v", got)
	}
	if got := List(db, Filter{Limit: 1}); len(got) != 1 || got[0].Kind != Account {
		t.Errorf("a limit keeps the newest: %+v", got)
	}
	if Record(db, Event{}); len(List(db, Filter{})) != 3 {
		t.Error("an event with no kind was recorded")
	}
}

func TestOldEventsAreSweptAfterTheRetentionWindow(t *testing.T) {
	prev := RetentionDays
	RetentionDays = func() int { return 30 }
	t.Cleanup(func() { RetentionDays = prev })
	db := memStore{}
	now := time.Now()
	Record(db, Event{At: now.Add(-40 * 24 * time.Hour), Kind: SignIn, User: "old"})
	Record(db, Event{At: now.Add(-20 * 24 * time.Hour), Kind: SignIn, User: "kept"})
	if n := Prune(db, now); n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	if got := List(db, Filter{}); len(got) != 1 || got[0].User != "kept" {
		t.Errorf("after the sweep: %+v", got)
	}
	// The setting is read at sweep time, so changing it takes effect.
	RetentionDays = func() int { return 7 }
	if Prune(db, now); len(List(db, Filter{})) != 0 {
		t.Error("a shorter window should take the 20-day event too")
	}
}

func TestWhatAStrangerSentIsCut(t *testing.T) {
	db := memStore{}
	Record(db, Event{Kind: SignInFailed, User: strings.Repeat("x", 500), Browser: strings.Repeat("é", 300), Detail: "d"})
	e := List(db, Filter{})[0]
	if len(e.User) > 124 || len(e.Browser) > 204 || !strings.HasSuffix(e.Browser, "…") {
		t.Errorf("long fields should be cut: user %d, browser %d", len(e.User), len(e.Browser))
	}
	if !strings.HasPrefix(e.Browser, "é") || strings.ContainsRune(e.Browser, '�') {
		t.Error("the cut split a character")
	}
}
