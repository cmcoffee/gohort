package core

import (
	"fmt"
	"sync"

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
	Critical(t.table.Drop())
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
	Critical(err)
	return found
}

// Set sets the value for the given key in the table.
func (t Table) Set(key string, value interface{}) {
	Critical(t.table.Set(key, value))
}

// CryptSet encrypts and sets the given value for the given key.
func (t Table) CryptSet(key string, value interface{}) {
	Critical(t.table.CryptSet(key, value))
}

// Unset removes the key from the table.
func (t Table) Unset(key string) {
	Critical(t.table.Unset(key))
}

// Keys returns a slice of strings representing the keys in the table.
func (t Table) Keys() []string {
	keys, err := t.table.Keys()
	Critical(err)
	return keys
}

// CountKeys returns the number of keys in the table.
func (t Table) CountKeys() int {
	count, err := t.table.CountKeys()
	Critical(err)
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
	Critical(d.Store.Drop(table))
}

// CryptSet saves an encrypted value to the specified table and key.
func (d DBase) CryptSet(table, key string, value interface{}) {
	Critical(d.Store.CryptSet(table, key, value))
}

// Set saves a value to the specified table and key.
func (d DBase) Set(table, key string, value interface{}) {
	Critical(d.Store.Set(table, key, value))
}

// Get retrieves a value from the specified table by key.
func (d DBase) Get(table, key string, output interface{}) bool {
	found, err := d.Store.Get(table, key, output)
	Critical(err)
	return found
}

// Table returns a table object for the given table name.
func (d DBase) Table(table string) Table {
	return Table{table: d.Store.Table(table)}
}

// Keys returns a list of keys for the specified table.
func (d DBase) Keys(table string) []string {
	keylist, err := d.Store.Keys(table)
	Critical(err)
	return keylist
}

// CountKeys returns the number of keys in the specified table.
func (d DBase) CountKeys(table string) int {
	count, err := d.Store.CountKeys(table)
	Critical(err)
	return count
}

// Tables returns a list of all table names in the database.
func (d DBase) Tables() []string {
	tables, err := d.Store.Tables()
	Critical(err)
	return tables
}

// AllTables lists every bucket including sub-store namespaces (separator-named
// scopes Tables() hides). Used by maintenance sweeps that drop a whole scope.
func (d DBase) AllTables() []string {
	tables, err := d.Store.AllTables()
	Critical(err)
	return tables
}

// Unset removes the value associated with the given key from the table.
func (d DBase) Unset(table, key string) {
	Critical(d.Store.Unset(table, key))
}

// Close closes the underlying store.
func (d DBase) Close() {
	d.Store.Close()
}
