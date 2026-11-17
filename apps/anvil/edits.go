// The edit journal: every write Anvil makes is recorded with the bytes that
// were there before it.
//
// This exists because of who is doing the editing. Claude Code can be handed
// a working tree because a strong model plus a human reading every diff plus
// git is three layers of recovery. Anvil is meant to run on the local worker
// as well, against a tree that may not be committed, driven by someone who
// is watching a chat panel rather than a diff review. Take away two of the
// three layers and "the model rewrote a file and got it wrong" stops being
// an inconvenience.
//
// So the undo is not git and not the model's memory of what it changed: it
// is a pre-image, captured inside the same call that does the write, keyed to
// an entry the UI can show and revert. A revert is a byte-for-byte restore of
// what was there, and it is refused when the file has changed since — a
// journal that silently clobbers a later edit is a second way to lose work
// rather than a way to recover from the first.
//
// Entries are per (user, project, session). Session scope is what makes
// "revert everything this conversation did" answerable, which is the request
// people actually have after a run goes wrong — not "revert edit #7".
package anvil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// editTable holds EditEntry records in the acting user's own store, keyed by
// entry ID. Per-user because the pre-image is file CONTENT — the same reason
// the tools are user-scoped, applied to the thing that remembers them.
const editTable = "anvil_edits"

// maxJournalBytes caps how large a file may be and still be journaled. Above
// it, the pre-image would cost more to store than the edit is likely worth,
// and the tools refuse the write rather than perform an unrevertable one:
// a silent downgrade from "revertable" to "not" is exactly the surprise this
// file exists to prevent.
const maxJournalBytes = 2 << 20 // 2 MiB

// Edit kinds.
const (
	EditModify = "modify"
	EditCreate = "create"
	EditDelete = "delete"
)

// EditEntry is one recorded write.
type EditEntry struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	SessionID string    `json:"session_id,omitempty"`
	Path      string    `json:"path"` // project-relative
	Kind      string    `json:"kind"` // EditModify | EditCreate | EditDelete
	At        time.Time `json:"at"`

	// Before is the file's content prior to the write; empty for a create.
	// After is what the write left; empty for a delete.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`

	// AfterHash pins what this entry believes is on disk now. A revert
	// compares it against the live file and refuses on a mismatch, so an
	// edit made outside Anvil (by the user, by a build, by another session)
	// is never silently thrown away.
	AfterHash string `json:"after_hash,omitempty"`

	// Diff is the rendered unified diff, computed once at write time. Held
	// rather than recomputed because the UI shows it on every session replay
	// and neither side of it is guaranteed to still be on disk.
	Diff string `json:"diff,omitempty"`
	// Added / Removed are the line counts, for a one-line card summary.
	Added   int `json:"added"`
	Removed int `json:"removed"`

	// Reverted records that this entry has been undone, and when. Kept
	// rather than deleted so the history stays truthful about what happened.
	Reverted   bool      `json:"reverted,omitempty"`
	RevertedAt time.Time `json:"reverted_at,omitempty"`
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// journalWrite records a completed write and returns the entry. before is the
// prior content ("" with existed=false for a create); after is what was
// written ("" for a delete).
func journalWrite(udb Database, projectID, sessionID, relPath string, before string, existed bool, after string, deleted bool) EditEntry {
	kind := EditModify
	switch {
	case deleted:
		kind = EditDelete
	case !existed:
		kind = EditCreate
	}
	e := EditEntry{
		ID:        UUIDv4(),
		ProjectID: projectID,
		SessionID: sessionID,
		Path:      relPath,
		Kind:      kind,
		At:        time.Now().UTC(),
		Before:    before,
		After:     after,
	}
	if !deleted {
		e.AfterHash = hashBytes([]byte(after))
	}
	e.Diff, e.Added, e.Removed = unifiedDiff(relPath, before, after)
	udb.Set(editTable, e.ID, e)
	return e
}

// listEdits returns this user's journal entries for one project, newest
// first. A blank sessionID means every session.
func listEdits(udb Database, projectID, sessionID string) []EditEntry {
	out := []EditEntry{}
	for _, id := range udb.Keys(editTable) {
		var e EditEntry
		if !udb.Get(editTable, id, &e) {
			continue
		}
		if e.ProjectID != projectID {
			continue
		}
		if sessionID != "" && e.SessionID != sessionID {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// revertEdit restores the pre-image for one entry.
//
// The live file must still match what the entry left behind. That check is
// the difference between an undo and a data-loss bug: without it, reverting
// an edit made an hour ago would discard everything typed since.
func revertEdit(udb Database, p Project, base string, e EditEntry) error {
	if e.Reverted {
		return fmt.Errorf("this edit was already reverted at %s", e.RevertedAt.Format(time.RFC3339))
	}
	abs, err := p.resolvePath(base, e.Path)
	if err != nil {
		return err
	}
	current, existed, err := readIfPresent(abs)
	if err != nil {
		return err
	}
	switch e.Kind {
	case EditDelete:
		if existed {
			return fmt.Errorf("%s exists again: it was recreated since the delete, so it is not overwritten", e.Path)
		}
	default:
		if !existed {
			return fmt.Errorf("%s no longer exists, so there is nothing to revert onto", e.Path)
		}
		if e.AfterHash != "" && hashBytes([]byte(current)) != e.AfterHash {
			return fmt.Errorf("%s has changed since this edit, and reverting would discard those later changes. Review the file first", e.Path)
		}
	}

	if e.Kind == EditCreate {
		if err := os.Remove(abs); err != nil {
			return fmt.Errorf("remove %s: %w", e.Path, err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return fmt.Errorf("create parent of %s: %w", e.Path, err)
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(abs); err == nil {
			mode = st.Mode().Perm()
		}
		if err := os.WriteFile(abs, []byte(e.Before), mode); err != nil {
			return fmt.Errorf("restore %s: %w", e.Path, err)
		}
	}
	e.Reverted = true
	e.RevertedAt = time.Now().UTC()
	udb.Set(editTable, e.ID, e)
	return nil
}

// readIfPresent reads a file, distinguishing "not there" from "failed".
func readIfPresent(abs string) (content string, existed bool, err error) {
	b, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(b), true, nil
}

// --- diff --------------------------------------------------------------------

// unifiedDiff renders a unified diff of before → after and reports the line
// counts. Hand-rolled rather than pulled in: the whole need is "show a human
// what changed in one file", the LCS below is a few dozen lines, and gohort
// prefers stdlib over a dependency for exactly this size of problem.
func unifiedDiff(path, before, after string) (diff string, added, removed int) {
	if before == after {
		return "", 0, 0
	}
	a := splitLinesKeepEmpty(before)
	b := splitLinesKeepEmpty(after)
	ops := diffOps(a, b)

	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", path, path)

	// Group ops into hunks with up to contextLines of unchanged lines
	// around each run of changes. A whole-file dump is unreadable on a big
	// file and is the reason diffs have hunks at all.
	const contextLines = 3
	type hunk struct{ start, end int } // indexes into ops
	var hunks []hunk
	for i := 0; i < len(ops); i++ {
		if ops[i].kind == opEqual {
			continue
		}
		start := i - contextLines
		if start < 0 {
			start = 0
		}
		j := i
		for j < len(ops) {
			if ops[j].kind != opEqual {
				j++
				continue
			}
			// Extend across a short stretch of equal lines so two nearby
			// changes stay in one hunk instead of splitting into two that
			// repeat the same context.
			run := 0
			for j+run < len(ops) && ops[j+run].kind == opEqual {
				run++
			}
			if run > contextLines*2 || j+run >= len(ops) {
				break
			}
			j += run
		}
		end := j + contextLines
		if end > len(ops) {
			end = len(ops)
		}
		if n := len(hunks); n > 0 && start <= hunks[n-1].end {
			hunks[n-1].end = end
		} else {
			hunks = append(hunks, hunk{start, end})
		}
		i = j
	}

	for _, h := range hunks {
		var aStart, bStart, aCount, bCount int
		aStart, bStart = -1, -1
		for _, op := range ops[h.start:h.end] {
			switch op.kind {
			case opEqual:
				if aStart < 0 {
					aStart, bStart = op.aLine, op.bLine
				}
				aCount++
				bCount++
			case opDelete:
				if aStart < 0 {
					aStart, bStart = op.aLine, op.bLine
				}
				aCount++
			case opInsert:
				if aStart < 0 {
					aStart, bStart = op.aLine, op.bLine
				}
				bCount++
			}
		}
		if aStart < 0 {
			continue
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart+1, aCount, bStart+1, bCount)
		for _, op := range ops[h.start:h.end] {
			switch op.kind {
			case opEqual:
				fmt.Fprintf(&out, " %s\n", op.text)
			case opDelete:
				fmt.Fprintf(&out, "-%s\n", op.text)
			case opInsert:
				fmt.Fprintf(&out, "+%s\n", op.text)
			}
		}
	}
	for _, op := range ops {
		switch op.kind {
		case opInsert:
			added++
		case opDelete:
			removed++
		}
	}
	return out.String(), added, removed
}

// splitLinesKeepEmpty splits on "\n" without inventing a trailing empty line
// for content that ends in a newline — otherwise every diff of a normal file
// reports a phantom change on its last line.
func splitLinesKeepEmpty(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

type opKind int

const (
	opEqual opKind = iota
	opDelete
	opInsert
)

type diffOp struct {
	kind         opKind
	text         string
	aLine, bLine int // 0-based line numbers in the respective sides
}

// diffOps computes a line-level diff via the standard LCS table.
//
// The table is O(len(a)*len(b)) in memory, which is fine for source files and
// catastrophic for a large generated one — so above lcsCellLimit it falls
// back to reporting the file as wholly replaced. A slow or memory-hungry diff
// on a 200k-line file would be a worse outcome than a coarse one.
func diffOps(a, b []string) []diffOp {
	const lcsCellLimit = 4_000_000
	if len(a)*len(b) > lcsCellLimit {
		ops := make([]diffOp, 0, len(a)+len(b))
		for i, line := range a {
			ops = append(ops, diffOp{opDelete, line, i, 0})
		}
		for j, line := range b {
			ops = append(ops, diffOp{opInsert, line, len(a), j})
		}
		return ops
	}
	// lcs[i][j] = length of the longest common subsequence of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{opEqual, a[i], i, j})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{opDelete, a[i], i, j})
			i++
		default:
			ops = append(ops, diffOp{opInsert, b[j], i, j})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, diffOp{opDelete, a[i], i, j})
	}
	for ; j < len(b); j++ {
		ops = append(ops, diffOp{opInsert, b[j], i, j})
	}
	return ops
}
