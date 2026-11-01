package bbolt

// This directory is a vendored copy of go.etcd.io/bbolt v1.5.0, MIT licensed.
// See LICENSE here for the original copyright notice, which travels with the
// code. It is the copy snugforge vendors for kvlite, so it reads the same
// database files gohort's store writes.
//
// WHY IT IS HERE RATHER THAN IN go.mod. This tree builds in GOPATH mode as well
// as module mode, and in GOPATH mode go.mod is ignored: an import has to exist
// as source under $GOPATH/src on every machine that builds, and snugforge's
// vendored copy is visible only to snugforge. gohort --migrate-dry-run
// (migrate_dry_run.go) reads the database raw, copying it and walking it to
// show what the migrations would change, which kvlite does not expose; its
// direct import broke the GOPATH build, the way core/pigo's first version did.
//
// WHAT WAS TAKEN. Every .go file of the package and of errors,
// internal/common and internal/freelist, byte for byte except the import
// paths of those three, rewritten from go.etcd.io/bbolt/... to here. A diff
// against upstream with that one substitution undone is empty. Left out: the
// README, Makefile, OWNERS and code of conduct.
//
// Nothing else in gohort should import this: the store is kvlite's.
