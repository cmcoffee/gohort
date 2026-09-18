package core

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// Database represents a key-value store with table management capabilities.
type Database interface {
	Sub(prefix string) Database
	Bucket(name string) Database
	Drop(table string)
	CryptSet(table, key string, value interface{})
	Set(table, key string, value interface{})
	Unset(table, key string)
	Get(table, key string, output interface{}) bool
	// TryGet and TrySet are the error-returning forms, for the callers that
	// can act on a failure. The plain forms above absorb it — see the store
	// failures section below for what that costs and why the signatures stay.
	TryGet(table, key string, output interface{}) (bool, error)
	TrySet(table, key string, value interface{}) error
	Keys(table string) []string
	CountKeys(table string) int
	Tables() []string
	// AllTables lists every bucket including sub-store namespaces (the
	// separator-named scopes Tables() hides) — for maintenance/enumeration.
	AllTables() []string
	Table(table string) Table
	Close()
}

var (
	// ResetDB is a function that resets the database.
	ResetDB = kvlite.CryptReset

	// ErrBadPadlock indicates an error when a padlock is invalid.
	ErrBadPadlock = kvlite.ErrBadPadlock

	// RootDB is the top-level application database, set at startup.
	// Apps can use it to access sibling buckets (e.g. for one-time migrations).
	RootDB Database

	// VectorDB is the dedicated store for the embedding/vector index
	// (the EmbeddedChunks table). Split out from RootDB so the derived,
	// regenerable chunk corpus — the hot path for semantic search — can
	// live on fast local storage even when RootDB sits on network
	// storage. Set at startup alongside RootDB; defaults to a separate
	// file co-located with the main DB unless [paths] vector_dir
	// relocates it. All SHARED chunk I/O (agent knowledge, collections,
	// skills, deployment KB) routes here; per-app private corpora that
	// must stay isolated (e.g. phantom) keep passing their own handle.
	VectorDB Database

	// RepoFilesDB is the dedicated store for cloned repository source used
	// by the repo browser. It is a BULK, re-clonable cache — thousands of
	// files per repo — so it is split off from RootDB to keep the main
	// (often network-hosted) DB lean, and relocatable to fast local storage
	// via [paths] repo_dir. Opened with the same hardware-locked at-rest
	// encryption as the other stores, so file bodies are encrypted on disk
	// with no extra work; the plaintext clone lives only transiently in a
	// tmpfs before ingest. Set at startup; nil when unset.
	RepoFilesDB Database

	// BundleFilesDB is the dedicated store for uploaded evidence bundles —
	// support dumps, log tarballs, diagnostic captures the user hands the
	// system rather than a host it can reach. Same reasoning as RepoFilesDB
	// (bulk, encrypted at rest, relocatable to local SSD via [paths]
	// bundle_dir), and a separate file for a different reason: repo content
	// is RE-CLONABLE and a bundle is not. Losing the repo store costs a
	// clone; losing the bundle store loses evidence that may no longer
	// exist anywhere else, so the two should not share a blast radius.
	// Set at startup; nil when unset.
	BundleFilesDB Database
)

// OpenDB opens a database from the given filename.
// It accepts an optional padlock byte slice for encryption.
func OpenDB(filename string, padlock ...byte) (Database, error) {
	db, err := kvlite.Open(filename, padlock[0:]...)
	if err != nil {
		return nil, err
	}
	return &DBase{db}, nil
}

// --- store failures -----------------------------------------------------------
//
// Every operation below used to end in Critical(err), which is Fatal, which is
// os.Exit(1). One error from the store — any error, on any table, for any user
// — took the whole multi-tenant server down with it.
//
// That is the wrong trade twice over. The motivating failures are transient:
// this deployment's database has lived on network storage, where a blip is a
// blip and not a diagnosis. And the trigger was never only disk trouble — Get
// returns an error when a stored value does not fit the type the caller asked
// for, so an admin probing a key in the DB browser could end the process by
// guessing a type wrong. That surface had to reach around this wrapper to be
// written at all (see the comment it left in apps/admin), which is the clearest
// statement of the problem there is: a feature that has to avoid the framework
// to be safe.
//
// So a failure degrades and SHOUTS instead of exiting. It does not exit even
// when the store is properly gone, which is the deliberate half of this:
// exiting takes away the page an operator would read to find out what is wrong,
// and a supervisor restarting into the same broken store is a crash loop rather
// than a recovery. A server that fails closed and says so is the recoverable
// shape.
//
// THE HAZARD THIS ACCEPTS, stated plainly. A failed read returns "not found",
// because false is the only thing the signature can say. A caller that reads,
// finds nothing, and writes a default will therefore overwrite a record that
// was there but unreadable. It cannot be fixed under these signatures, and
// changing them is 2907 call sites; TryGet exists for the callers where that
// matters, and the failure is logged distinctly so it is never a silent one.
// A failed WRITE is worse in a quieter way: the caller believes it persisted.
// Both are counted separately, because "reads are failing" and "writes are
// being lost" call for different urgency.

// DBFailureReport is what the store has been failing at lately. Zero values
// throughout mean it has not failed at all.
type DBFailureReport struct {
	// Reads and Writes count failed operations by kind since startup. Split
	// because a store that cannot be read is degraded and one that cannot be
	// written is losing work.
	Reads  int `json:"reads"`
	Writes int `json:"writes"`
	// Last is the most recent failure, with the operation that hit it.
	Last   string    `json:"last,omitempty"`
	LastOp string    `json:"last_op,omitempty"`
	LastAt time.Time `json:"last_at,omitempty"`
	// Since is when the current run of failures began: the first failure after
	// the last quiet stretch. A wide Since-to-LastAt span is an outage; a
	// narrow one is a blip.
	Since time.Time `json:"since,omitempty"`
}

var (
	dbFailMu     sync.Mutex
	dbFailState  DBFailureReport
	dbFailLastAt time.Time
)

// dbQuietGap is how long without a failure ends a run of them. A new failure
// after this restarts Since rather than extending a span that describes
// something that already got better.
const dbQuietGap = 5 * time.Minute

// DBHealth reports what the store has been failing at. Safe to call at any
// time, including from a handler that is itself reading a broken store.
func DBHealth() DBFailureReport {
	dbFailMu.Lock()
	defer dbFailMu.Unlock()
	return dbFailState
}

// dbFail records one store failure and reports whether there was one, so a
// caller reads as `if dbFail(...) { return zero }`.
//
// Only the failing path takes the lock. Successes are the hot path — thousands
// of call sites, many per request — and must not pay for bookkeeping that only
// matters when something is wrong.
func dbFail(write bool, op, table, key string, err error) bool {
	if err == nil {
		return false
	}
	now := time.Now()
	dbFailMu.Lock()
	if dbFailState.Since.IsZero() || now.Sub(dbFailLastAt) > dbQuietGap {
		dbFailState.Since = now
	}
	if write {
		dbFailState.Writes++
	} else {
		dbFailState.Reads++
	}
	dbFailState.Last = err.Error()
	dbFailState.LastOp = op + " " + table
	dbFailState.LastAt = now
	dbFailLastAt = now
	writes, reads := dbFailState.Writes, dbFailState.Reads
	dbFailMu.Unlock()

	// The key is in the log and not in the report: it is the thing that makes a
	// failure reproducible, and it is also the thing most likely to name
	// somebody's record, so it goes where an operator looks on purpose rather
	// than onto a status page.
	if write {
		Err("[database] %s %s/%s FAILED TO WRITE: %v (%d write / %d read failures so far — work is being lost)",
			op, table, key, err, writes, reads)
	} else {
		Err("[database] %s %s/%s failed: %v (%d read / %d write failures so far — this reads to the caller as 'not found')",
			op, table, key, err, reads, writes)
	}
	return true
}

// DBase is a database wrapper around kvlite.Store.
type DBase struct {
	Store kvlite.Store
}

// Table represents a table within the database.
type Table struct {
	table kvlite.Table
}

// Drop deletes the underlying table.
func (t Table) Drop() {
	dbFail(true, "drop", "", "", t.table.Drop())
}

// GetString retrieves a string value from the table by key.
func (T Table) GetString(key string) string {
	var x string
	T.Get(key, &x)
	return x
}

// Get retrieves a value from the table by key.
func (t Table) Get(key string, value interface{}) bool {
	found, err := t.table.Get(key, value)
	if dbFail(false, "get", "", key, err) {
		return false
	}
	return found
}

// Set sets the value for the given key in the table.
func (t Table) Set(key string, value interface{}) {
	dbFail(true, "set", "", key, t.table.Set(key, value))
}

// CryptSet encrypts and sets the given value for the given key.
func (t Table) CryptSet(key string, value interface{}) {
	dbFail(true, "cryptset", "", key, t.table.CryptSet(key, value))
}

// Unset removes the key from the table.
func (t Table) Unset(key string) {
	dbFail(true, "unset", "", key, t.table.Unset(key))
}

// Keys returns a slice of strings representing the keys in the table.
func (t Table) Keys() []string {
	keys, err := t.table.Keys()
	if dbFail(false, "keys", "", "", err) {
		return nil
	}
	return keys
}

// CountKeys returns the number of keys in the table.
func (t Table) CountKeys() int {
	count, err := t.table.CountKeys()
	if dbFail(false, "countkeys", "", "", err) {
		return 0
	}
	return count
}

// OpenCache opens a memory-only kvlite store.
func OpenCache() Database {
	return &DBase{kvlite.MemStore()}
}

// --- per-app private databases ------------------------------------------------
//
// By default every app shares the one global DB, namespaced by a Bucket keyed on
// the app's name (see get_agentstore). An app that holds a lot of data, or wants
// an isolated / independently relocatable / independently disposable store, can
// instead ask for its OWN hardware-locked kvlite database FILE — the same shape
// as VectorDB / RepoFilesDB, which are dedicated stores split off the main one.
//
// Go apps opt in by implementing PrivateDBApp; the framework then hands them a
// dedicated file instead of a bucket. Custom (app_def) apps opt in per-spec (see
// AppSpec.PrivateDB) and reach their file through OpenCustomAppDB. Both resolve
// through OpenAppDB, whose concrete secure-open is injected by main at startup
// (main owns the data dir + the hardware padlock; core does not).

// storeNameApp is satisfied by an app whose data is keyed by a name other
// than its own: it declares `StoreName() string`. An app renamed after it
// shipped keeps the bucket it was born with by returning the old name there,
// so the rename never strands what users already wrote: the tile, the path,
// and the package move, the data does not. Unexported on purpose — an app
// only needs the method, and every exported symbol here lands in the
// namespace of every dot-importer.
type storeNameApp interface {
	StoreName() string
}

// AppStoreName returns the name an app's store is keyed by: StoreName() when
// the app declares one, else its Name(). The framework consults this wherever
// it would otherwise use Name() to find the app's store (the shared bucket, or
// the private file for a PrivateDBApp).
func AppStoreName(a Agent) string {
	if s, ok := a.(storeNameApp); ok {
		if n := strings.TrimSpace(s.StoreName()); n != "" {
			return n
		}
	}
	return a.Name()
}

// PrivateDBApp is implemented by an app that wants its own dedicated kvlite
// database file rather than a bucket of the shared global DB.
type PrivateDBApp interface {
	UsePrivateDB() bool
}

var (
	privateDBOpener func(name string) (Database, error)
	privateDBs      = map[string]Database{}
	privateDBMu     sync.Mutex
)

// SetPrivateDBOpener wires the concrete secure database open. main calls this
// once at startup with a closure that builds the file path under the data dir
// and opens it hardware-locked (SecureDatabase). Until wired, OpenAppDB returns
// nil so callers fall back to the shared bucket.
func SetPrivateDBOpener(fn func(name string) (Database, error)) { privateDBOpener = fn }

// OpenAppDB returns the dedicated, hardware-locked kvlite database for the given
// logical name, opened once and cached for the process lifetime (opening the
// same file twice is unsafe). Returns nil when no opener is wired (e.g. a
// non-serve context) or the open fails; callers must fall back to a shared
// bucket on nil.
func OpenAppDB(name string) Database {
	privateDBMu.Lock()
	defer privateDBMu.Unlock()
	if db, ok := privateDBs[name]; ok {
		return db
	}
	if privateDBOpener == nil || name == "" {
		return nil
	}
	db, err := privateDBOpener(name)
	if err != nil {
		Err(fmt.Errorf("open private app database %q: %w", name, err))
		return nil
	}
	privateDBs[name] = db
	return db
}

// dbHandles interns the child Database handles minted by Bucket/Sub, so the
// SAME (parent, name) pair always yields the SAME pointer. Without this,
// every Bucket/Sub call allocated a fresh wrapper, which made pointer
// identity useless as a cache key — the chunk cache (core/vector_store.go)
// keys its snapshots by Database and was rebuilding on every read through a
// derived handle (UserDB chains never matched). Handles are stateless wrappers
// over a shared kvlite.Store, so sharing one across goroutines is safe. The
// map grows with distinct namespaces actually touched (users × buckets) and
// is never evicted — entries are two words each.
var (
	dbHandleMu sync.Mutex
	dbHandles  = map[dbHandleKey]Database{}
)

type dbHandleKey struct {
	parent *DBase
	bucket bool // Bucket vs Sub — same name, different kvlite namespace semantics
	name   string
}

// child returns the interned handle for this parent + operation + name,
// minting (and remembering) it on first use. Pointer receivers on Bucket/Sub
// are what make the parent's address a stable key: every Database in the
// process is a *DBase, and roots (OpenDB / OpenCache / literals) are created
// once, so interned chains stay pointer-identical all the way down.
func (d *DBase) child(bucket bool, name string) Database {
	dbHandleMu.Lock()
	defer dbHandleMu.Unlock()
	key := dbHandleKey{parent: d, bucket: bucket, name: name}
	if h, ok := dbHandles[key]; ok {
		return h
	}
	var s kvlite.Store
	if bucket {
		s = d.Store.Bucket(name)
	} else {
		s = d.Store.Sub(name)
	}
	h := &DBase{s}
	dbHandles[key] = h
	return h
}

// Bucket returns the Database instance representing the given table —
// interned, so repeated calls return the identical handle.
func (d *DBase) Bucket(table string) Database {
	return d.child(true, table)
}

// Sub returns the sub-database with the given prefix — interned, so repeated
// calls return the identical handle.
func (d *DBase) Sub(table string) Database {
	return d.child(false, table)
}

// Drop deletes the specified table.
func (d DBase) Drop(table string) {
	dbFail(true, "drop", table, "", d.Store.Drop(table))
}

// CryptSet saves an encrypted value to the specified table and key.
func (d DBase) CryptSet(table, key string, value interface{}) {
	dbFail(true, "cryptset", table, key, d.Store.CryptSet(table, key, value))
}

// Set saves a value to the specified table and key.
func (d DBase) Set(table, key string, value interface{}) {
	dbFail(true, "set", table, key, d.Store.Set(table, key, value))
}

// TrySet is Set for a caller that can do something about a failure — report it
// to the person whose work it was, refuse to continue, retry later. Set stays
// the common form because most callers genuinely cannot, and making 2907 of
// them pretend otherwise would be noise standing where a real check should be.
func (d DBase) TrySet(table, key string, value interface{}) error {
	err := d.Store.Set(table, key, value)
	dbFail(true, "set", table, key, err)
	return err
}

// Get retrieves a value from the specified table by key.
func (d DBase) Get(table, key string, output interface{}) bool {
	found, err := d.Store.Get(table, key, output)
	if dbFail(false, "get", table, key, err) {
		return false
	}
	return found
}

// TryGet is Get for a caller that must tell "there is nothing here" apart from
// "something is here and I could not read it". Get collapses both to false,
// which is what invites a caller to write a default over a record that was
// only unreadable; this is the way out of that for the callers where it
// matters.
func (d DBase) TryGet(table, key string, output interface{}) (bool, error) {
	found, err := d.Store.Get(table, key, output)
	dbFail(false, "get", table, key, err)
	return found, err
}

// Table returns a table object for the given table name.
func (d DBase) Table(table string) Table {
	return Table{table: d.Store.Table(table)}
}

// Keys returns a list of keys for the specified table.
func (d DBase) Keys(table string) []string {
	keylist, err := d.Store.Keys(table)
	if dbFail(false, "keys", table, "", err) {
		return nil
	}
	return keylist
}

// CountKeys returns the number of keys in the specified table.
func (d DBase) CountKeys(table string) int {
	count, err := d.Store.CountKeys(table)
	if dbFail(false, "countkeys", table, "", err) {
		return 0
	}
	return count
}

// Tables returns a list of all table names in the database.
func (d DBase) Tables() []string {
	tables, err := d.Store.Tables()
	if dbFail(false, "tables", "", "", err) {
		return nil
	}
	return tables
}

// AllTables lists every bucket including sub-store namespaces (separator-named
// scopes Tables() hides). Used by maintenance sweeps that drop a whole scope.
func (d DBase) AllTables() []string {
	tables, err := d.Store.AllTables()
	if dbFail(false, "alltables", "", "", err) {
		return nil
	}
	return tables
}

// Unset removes the value associated with the given key from the table.
func (d DBase) Unset(table, key string) {
	dbFail(true, "unset", table, key, d.Store.Unset(table, key))
}

// Close closes the underlying store.
func (d DBase) Close() {
	d.Store.Close()
}
