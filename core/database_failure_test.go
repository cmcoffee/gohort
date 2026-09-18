package core

// What a store failure does now. The whole point of these is the thing that
// does NOT happen: the process is still here at the end of every one of them.
//
// Before this, each of these cases called Critical, which is Fatal, which is
// os.Exit(1) — so a test like this could not have been written at all. A test
// binary that exits mid-run reports nothing.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// failingStore is a real store that has stopped working. It EMBEDS a
// kvlite.Store rather than implementing the interface, because that interface
// has an unexported method and cannot be satisfied from out here.
type failingStore struct {
	kvlite.Store
	err error
}

func (f *failingStore) Get(table, key string, out interface{}) (bool, error) {
	return false, f.err
}
func (f *failingStore) Set(table, key string, v interface{}) error { return f.err }
func (f *failingStore) CryptSet(table, key string, v interface{}) error {
	return f.err
}
func (f *failingStore) Unset(table, key string) error       { return f.err }
func (f *failingStore) Keys(table string) ([]string, error) { return nil, f.err }
func (f *failingStore) CountKeys(table string) (int, error) { return 0, f.err }
func (f *failingStore) Tables() ([]string, error)           { return nil, f.err }
func (f *failingStore) AllTables() ([]string, error)        { return nil, f.err }
func (f *failingStore) Drop(table string) error             { return f.err }

func brokenDB(err error) Database {
	return &DBase{Store: &failingStore{Store: kvlite.MemStore(), err: err}}
}

// resetDBHealth clears the failure record so one test's failures are not read
// as another's.
func resetDBHealth(t *testing.T) {
	t.Helper()
	dbFailMu.Lock()
	dbFailState = DBFailureReport{}
	dbFailLastAt = time.Time{}
	dbFailMu.Unlock()
	t.Cleanup(func() {
		dbFailMu.Lock()
		dbFailState = DBFailureReport{}
		dbFailLastAt = time.Time{}
		dbFailMu.Unlock()
	})
}

func TestABrokenStoreDegradesInsteadOfExiting(t *testing.T) {
	resetDBHealth(t)
	db := brokenDB(errors.New("input/output error"))

	// Every one of these used to be the last line this process ran.
	var into string
	if db.Get("t", "k", &into) {
		t.Error("a failed read reported a hit")
	}
	if keys := db.Keys("t"); keys != nil {
		t.Errorf("a failed listing returned %v", keys)
	}
	if n := db.CountKeys("t"); n != 0 {
		t.Errorf("a failed count returned %d", n)
	}
	if tables := db.Tables(); tables != nil {
		t.Errorf("a failed table listing returned %v", tables)
	}
	if tables := db.AllTables(); tables != nil {
		t.Errorf("a failed full table listing returned %v", tables)
	}
	db.Set("t", "k", "v")
	db.CryptSet("t", "k", "v")
	db.Unset("t", "k")
	db.Drop("t")

	h := DBHealth()
	if h.Reads != 5 {
		t.Errorf("reads = %d, want 5", h.Reads)
	}
	if h.Writes != 4 {
		t.Errorf("writes = %d, want 4", h.Writes)
	}
	if !strings.Contains(h.Last, "input/output error") {
		t.Errorf("last = %q — the store's own reason must survive", h.Last)
	}
	if h.LastAt.IsZero() || h.Since.IsZero() {
		t.Errorf("health carries no timing: %+v", h)
	}
}

// A working store leaves no trace, because the bookkeeping is on the failing
// path only — successes are thousands of call sites and must not pay for it.
func TestAWorkingStoreRecordsNothing(t *testing.T) {
	resetDBHealth(t)
	db := &DBase{Store: kvlite.MemStore()}
	db.Set("t", "k", "v")
	var into string
	db.Get("t", "k", &into)
	db.Keys("t")
	if h := DBHealth(); h.Reads != 0 || h.Writes != 0 || h.Since != (time.Time{}) {
		t.Errorf("a healthy store reported failures: %+v", h)
	}
	if into != "v" {
		t.Errorf("round trip = %q", into)
	}
}

// Reads and writes are counted apart, because a store that cannot be read is
// degraded and one that cannot be written is losing work.
func TestReadAndWriteFailuresAreCountedApart(t *testing.T) {
	resetDBHealth(t)
	db := brokenDB(errors.New("disk full"))
	var into string
	db.Get("t", "k", &into)
	db.Set("t", "k", "v")
	db.Set("t", "k2", "v")
	h := DBHealth()
	if h.Reads != 1 || h.Writes != 2 {
		t.Errorf("reads=%d writes=%d, want 1 and 2", h.Reads, h.Writes)
	}
	if !strings.HasPrefix(h.LastOp, "set") {
		t.Errorf("last op = %q", h.LastOp)
	}
}

// TryGet is the way out of the hazard the plain signature accepts: it tells
// "nothing is here" apart from "something is here and I could not read it",
// which is what stops a caller writing a default over a record that was only
// unreadable.
func TestTryGetSeparatesAbsentFromUnreadable(t *testing.T) {
	resetDBHealth(t)

	working := &DBase{Store: kvlite.MemStore()}
	found, err := working.TryGet("t", "missing", new(string))
	if found || err != nil {
		t.Errorf("absent record: found=%v err=%v", found, err)
	}

	broken := brokenDB(errors.New("checksum mismatch"))
	found, err = broken.TryGet("t", "there", new(string))
	if err == nil {
		t.Fatal("an unreadable record came back as simply absent")
	}
	if found {
		t.Error("found should stay false when the read failed")
	}
	// And the plain form collapses the two, which is the documented cost.
	if broken.Get("t", "there", new(string)) {
		t.Error("Get reported a hit on a failed read")
	}
}

func TestTrySetReportsAFailedWrite(t *testing.T) {
	resetDBHealth(t)
	if err := (&DBase{Store: kvlite.MemStore()}).TrySet("t", "k", "v"); err != nil {
		t.Errorf("a working write returned %v", err)
	}
	err := brokenDB(errors.New("read-only file system")).TrySet("t", "k", "v")
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("err = %v", err)
	}
	if h := DBHealth(); h.Writes != 1 {
		t.Errorf("a TrySet failure was not recorded: %+v", h)
	}
}

// Since marks the start of the CURRENT run of failures, so a span that
// describes an outage is not extended by a blip weeks later.
func TestSinceRestartsAfterAQuietStretch(t *testing.T) {
	resetDBHealth(t)
	db := brokenDB(errors.New("i/o timeout"))
	db.Set("t", "k", "v")
	first := DBHealth().Since
	if first.IsZero() {
		t.Fatal("the first failure set no start")
	}

	db.Set("t", "k", "v")
	if got := DBHealth().Since; !got.Equal(first) {
		t.Errorf("a second failure moved the start of the run: %v -> %v", first, got)
	}

	// Backdate the last failure past the quiet gap: the next one begins a new
	// run rather than joining the old one.
	dbFailMu.Lock()
	dbFailLastAt = time.Now().Add(-2 * dbQuietGap)
	dbFailMu.Unlock()
	db.Set("t", "k", "v")
	if got := DBHealth().Since; got.Equal(first) {
		t.Error("a failure after a quiet stretch extended the old run")
	}
}

// A failed listing is not an empty one, and for an auth gate the difference is
// the whole door.
//
// This is the regression that made the store refactor dangerous rather than
// safe. AuthHasUsers walks the user table; Keys returns nil on a read error;
// "no keys" means "no accounts configured yet"; and AuthMiddleware serves
// every request unauthenticated in that state. Before the refactor the path
// could not be reached, because a read error ended the process. Making the
// failure survivable is what made it necessary to say which way it falls.
func TestAFailedUserListingReadsAsConfigured(t *testing.T) {
	resetDBHealth(t)

	// A store that works and genuinely has nobody.
	empty := &DBase{Store: kvlite.MemStore()}
	if AuthHasUsers(empty) {
		t.Error("an empty store reported users")
	}

	// A store that cannot be read must NOT report the same thing.
	broken := brokenDB(errors.New("input/output error"))
	if !AuthHasUsers(broken) {
		t.Fatal("a failed read reported 'no users configured' — which opens every request")
	}
	if h := DBHealth(); h.Reads == 0 {
		t.Error("the failed listing was not recorded")
	}
}

// TryKeys is what makes that distinction available at all.
func TestTryKeysSeparatesEmptyFromUnreadable(t *testing.T) {
	resetDBHealth(t)

	keys, err := (&DBase{Store: kvlite.MemStore()}).TryKeys("t")
	if err != nil || len(keys) != 0 {
		t.Errorf("an empty table: keys=%v err=%v", keys, err)
	}
	if _, err := brokenDB(errors.New("disk gone")).TryKeys("t"); err == nil {
		t.Fatal("an unreadable table came back as simply empty")
	}
	// And the plain form still collapses them, which is why the callers whose
	// empty case GRANTS something have to use TryKeys.
	if got := brokenDB(errors.New("disk gone")).Keys("t"); got != nil {
		t.Errorf("Keys returned %v on a failed read", got)
	}
}
