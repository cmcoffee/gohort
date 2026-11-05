package servitor

import (
	. "github.com/cmcoffee/gohort/core"
)

// A repo's files are written to its store a group at a time, one commit for
// each group. A commit per file paid an fsync per file: 3,000 files of 8KB
// took 48s to ingest and 52s to wipe, against 1.3s and 17ms in one commit
// per group. A group is held in memory until it is written, so it is bounded
// by size as well as by count. Reads are left as they are: reading every file
// for a search took 45ms either way.
var (
	repoWriteFiles = 500
	repoWriteBytes = 32 << 20
)

// repoCommit makes one group's writes in one commit; a test counts the
// commits, or fails one.
var repoCommit = func(store Database, fn func(b DBBatch) error) error { return store.Batch(fn) }

// repoWrite is one file's change: its content and hash set, or both dropped.
type repoWrite struct {
	key, content, hash string
	drop               bool
}

// repoWrites gathers an ingest's changes and commits them a group at a time.
// A file's content and its hash go in the same commit, so the store never
// holds a hash for content it does not have: a commit that fails leaves the
// file to be written again on the next ingest. After a failure it commits
// nothing more, and err says why.
type repoWrites struct {
	store   Database
	pending []repoWrite
	size    int
	err     error
}

// set writes a file's content and the hash that marks it current.
func (w *repoWrites) set(key, content, hash string) {
	w.add(repoWrite{key: key, content: content, hash: hash})
}

// unset drops a file and its hash.
func (w *repoWrites) unset(key string) {
	w.add(repoWrite{key: key, drop: true})
}

func (w *repoWrites) add(op repoWrite) {
	w.pending = append(w.pending, op)
	w.size += len(op.content)
	if len(w.pending) >= repoWriteFiles || w.size >= repoWriteBytes {
		w.flush()
	}
}

// flush commits what is pending, and reports the first failure so far.
func (w *repoWrites) flush() error {
	if w.err != nil || len(w.pending) == 0 {
		return w.err
	}
	ops := w.pending
	w.pending, w.size = nil, 0
	w.err = repoCommit(w.store, func(b DBBatch) error {
		for _, op := range ops {
			if op.drop {
				if err := b.Unset(repoFilesTable, op.key); err != nil {
					return err
				}
				if err := b.Unset(repoHashTable, op.key); err != nil {
					return err
				}
				continue
			}
			if err := b.Set(repoFilesTable, op.key, op.content); err != nil {
				return err
			}
			if err := b.Set(repoHashTable, op.key, op.hash); err != nil {
				return err
			}
		}
		return nil
	})
	return w.err
}
