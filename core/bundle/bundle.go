// Evidence bundles: an uploaded tree of logs and diagnostics, unpacked once,
// stored encrypted and line-sliced, and answered with search, range reads and a
// merged timeline instead of by handing anyone the files.
//
// Lifted out of servitor, which had the only implementation and which reached
// it through a per-appliance key. Nothing in the store was ever about
// appliances: bundleStore() already keyed on `"bundle:" + applianceID`, so the
// appliance id was doing duty as an opaque bundle id and the lift is mostly a
// rename. That is also why it needed no migration — the on-disk layout is
// unchanged, byte for byte.
//
// WHY THE STORE ARRIVES THROUGH A SEAM. core.Database is self-referential:
// Sub(prefix) returns Database. A package under core cannot name that type
// without importing the package it left, and a structurally identical
// interface declared here would NOT be satisfied by it, because the Sub
// methods would return different named types. So this package declares the
// narrow, non-self-referential slice it actually uses (Store, four methods)
// and takes the scoping through OpenStore, which core assigns at init. Same
// shape as core/sandbox's hooks, for the same reason.
//
// ADDRESSED BY (owner, id), NOT BY A HANDLE. Every operation is a method on
// Bundle rather than a function taking the pair, because they were nine
// functions all threading the same two strings — the exact shape the project's
// struct-first rule names. The pair stays the identity: a bundle is not a live
// resource, it is a place in a store, and re-deriving the store per call is
// what lets an ingest replace a bundle underneath a reader without either side
// holding a stale handle.
package bundle

import "strings"

// Store is the narrow slice of a key-value store this package needs.
//
// Four methods, none self-referential, so core.Database satisfies it
// structurally with no adapter. Deliberately not the whole Database interface:
// this package has no business dropping tables or opening sub-scopes, and an
// interface that named those would be claiming otherwise.
type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

// OpenStore returns one bundle's encrypted store, or nil when none is
// configured. Assigned by core at init; nil in a binary that never wired this
// package up, which every accessor treats as "no bundle", the answer that
// grants less.
var OpenStore func(owner, id string) Store

// Bundle identifies one bundle's evidence. The zero value is not usable;
// Open makes one.
type Bundle struct {
	Owner string
	ID    string
}

// Open names a bundle. It touches no storage — see the file comment on why the
// store is re-derived per call rather than held.
func Open(owner, id string) Bundle {
	return Bundle{Owner: strings.TrimSpace(owner), ID: strings.TrimSpace(id)}
}

// Valid reports whether this Bundle can address anything at all.
func (b Bundle) Valid() bool { return b.Owner != "" && b.ID != "" }

// store resolves this bundle's scope, or nil when the seam is unwired or the
// identity is incomplete.
func (b Bundle) store() Store {
	if OpenStore == nil || !b.Valid() {
		return nil
	}
	return OpenStore(b.Owner, b.ID)
}
