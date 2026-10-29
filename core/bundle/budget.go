// How many matches a search keeps, how they are shared out, and what a reply
// says about it.
//
// Both searches (a file store's folder search, a bundle's) keep up to a limit,
// share it so one noisy file cannot take it all, keep counting past both so a
// reply can say "of N", and say where a cut fell. That used to live inside the
// folder search alone, and the bundle search had a bare cap that read
// "TRUNCATED" and stopped counting, so it could say neither how many there
// were nor which files it never opened. One type now, used by both.
//
// Paging is not here. A reply is paged by the generic paged-output store
// (core's SpillOutput and read_output): a search keeps up to its limit,
// renders all of it with this summary first, and the reply's first page is
// what arrives; read_output reads on, or greps within the kept matches,
// without searching again.
package bundle

import (
	"fmt"
	"sort"
	"strings"
)

// SearchPageChars is the size of a search reply's first page; the rest of the
// reply is read with read_output. Matched to what a run_command page carries.
const SearchPageChars = 12000

// DefaultPerFile is one file's share of a search's kept matches when the
// caller does not say. Small against the limit on purpose: matches arrive in
// file order and the reply's first page is what is always read, so a share
// this size puts several files on that page before one noisy file fills it.
const DefaultPerFile = 25

// maxTallyRows is how many files a count reply lists.
const maxTallyRows = 200

// FileTally is one searched file's count: every match in it, and how many of
// those were kept (the per-file share is the difference).
type FileTally struct {
	File    string `json:"file"`
	Matches int    `json:"matches"`
	Kept    int    `json:"kept"`
}

// Budget shares out the matches a search keeps. Max is the limit for the whole
// search, zero for a search that keeps none and only counts. PerFile is one
// file's share, zero for none. The rest is what happened, filled in as the
// search reports its files, in file order.
type Budget struct {
	Max, PerFile int

	Kept, Total int         // matches kept, and counted, across the files read
	Tallies     []FileTally // files with a match, in search order
	CutInFiles  []FileTally // files that had more matches than PerFile
	Unsearched  int         // files never opened because Max was reached
}

// Offer is how many of one file's matches its scan should keep, in order:
// its share, or the whole limit when there is no share. A file is scanned
// before the files ahead of it are merged (parallel.go), so the scan cannot
// know the room left; it keeps the most the budget could take, and File cuts
// that down.
func (b *Budget) Offer() int {
	if b.Max <= 0 {
		return 0
	}
	if b.PerFile > 0 && b.PerFile < b.Max {
		return b.PerFile
	}
	return b.Max
}

// File counts one scanned file: matches is every match in it, offered how many
// its scan kept, the first ones in order. It returns how many of those the
// search keeps. What a file keeps is always its first matches, up to its share
// and the room left, so counting a whole file at once gives what counting each
// match in order would.
func (b *Budget) File(name string, matches, offered int) int {
	if matches == 0 {
		return 0
	}
	kept := offered
	if kept > matches {
		kept = matches
	}
	if b.PerFile > 0 && kept > b.PerFile {
		kept = b.PerFile
	}
	if room := b.Max - b.Kept; b.Max <= 0 {
		kept = 0
	} else if kept > room {
		kept = room
	}
	b.Kept += kept
	b.Total += matches
	t := FileTally{File: name, Matches: matches, Kept: kept}
	b.Tallies = append(b.Tallies, t)
	if b.PerFile > 0 && matches > b.PerFile {
		b.CutInFiles = append(b.CutInFiles, t)
	}
	return kept
}

// Full reports whether the search has kept all it may: the point to stop
// opening files (and count the rest as Unsearched).
func (b *Budget) Full() bool { return b.Max > 0 && b.Kept >= b.Max }

// Cut reports whether matches exist that were not kept, or files were left
// unread, so the kept matches are not the whole answer. Never for a search
// that keeps none by design (a count, a grouping, counts over time): there is
// no list for it to have cut.
func (b *Budget) Cut() bool { return b.Max > 0 && (b.Total > b.Kept || b.Unsearched > 0) }

// Summary is the head of a reply that lists matches: how many are shown of
// how many counted, and where any cut fell. It leads the reply rather than
// trailing it, because a long reply arrives as its first page and the summary
// must be on it. scanned and considered are the files read and in scope;
// filterParam names the tool's file filter for the advice.
func (b *Budget) Summary(scanned, considered int, filterParam string) string {
	var s strings.Builder
	if !b.Cut() {
		fmt.Fprintf(&s, "%d match(es) in %d file(s), of %d searched (%d in scope).\n", b.Total, len(b.Tallies), scanned, considered)
	} else {
		fmt.Fprintf(&s, "Showing %d of %d matches counted in %d of %d files.", b.Kept, b.Total, scanned, considered)
		limits := []string{fmt.Sprintf("keep at most %d", b.Max)}
		if b.PerFile > 0 {
			limits = append(limits, fmt.Sprintf("per_file=%d", b.PerFile))
		}
		fmt.Fprintf(&s, " Limits: %s.\n", strings.Join(limits, ", "))
	}
	// WHERE a cut happened, because the two call for different next steps:
	// a cut inside a file is read by narrowing to that file, a cut across
	// files by counting.
	if len(b.CutInFiles) > 0 {
		var parts []string
		for i, t := range b.CutInFiles {
			if i == 8 {
				parts = append(parts, fmt.Sprintf("and %d more", len(b.CutInFiles)-i))
				break
			}
			parts = append(parts, fmt.Sprintf("%s (%d of %d)", t.File, t.Kept, t.Matches))
		}
		fmt.Fprintf(&s, "Cut WITHIN files by per_file: %s. Search one of them with %s to see all its matches; the per-file share lifts for a single file.\n", strings.Join(parts, ", "), filterParam)
	}
	switch {
	case b.Unsearched > 0:
		fmt.Fprintf(&s, "Cut ACROSS files: the limit of %d kept matches was reached and %d file(s) were not searched, so nothing here says how often this occurs in them. Use count=true for totals everywhere, or narrow the pattern.\n", b.Max, b.Unsearched)
	case b.Total > b.Kept && len(b.CutInFiles) == 0:
		fmt.Fprintf(&s, "Cut at the limit of %d kept matches: there are more. Use count=true for totals, or narrow the pattern.\n", b.Max)
	}
	return s.String()
}

// Counts is the reply to a count: every file with a match, busiest first.
func (b *Budget) Counts(pattern string, scanned, considered int) string {
	var s strings.Builder
	if b.Total == 0 {
		fmt.Fprintf(&s, "No matches for %q in %d of %d files searched.\n", pattern, scanned, considered)
		return s.String()
	}
	tallies := append([]FileTally(nil), b.Tallies...)
	sort.SliceStable(tallies, func(i, j int) bool { return tallies[i].Matches > tallies[j].Matches })
	fmt.Fprintf(&s, "%d matches for %q in %d file(s), of %d searched (%d in scope):\n", b.Total, pattern, len(tallies), scanned, considered)
	for i, t := range tallies {
		if i == maxTallyRows {
			fmt.Fprintf(&s, "...and %d more file(s) with matches, not listed.\n", len(tallies)-i)
			break
		}
		fmt.Fprintf(&s, "%7d  %s\n", t.Matches, t.File)
	}
	return s.String()
}
