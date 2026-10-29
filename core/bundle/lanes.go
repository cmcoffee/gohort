// Several patterns in one search.
//
// An investigation asks after several things at once: timeouts, refused
// connections, out-of-memory kills. One pattern per search meant one call per
// thing, and each call read the whole bundle again. A search of several reads
// each line once and tests every pattern against it, and each pattern is its
// own LANE: its own count, its own share of the limits, its own hits, groups
// or counts over time. Every lane's answer is the one a search for that
// pattern alone would give; only the reading is shared. One difference, and
// only ever toward more: a search alone stops opening files once its limit is
// full, and a lane that fills before the others is counted on through the
// files they still read, so its total covers more of the bundle.
//
// Alternation ("timeout|refused|oom") was the workaround, and it answers a
// different question: one count for all three, and one limit the noisiest of
// them fills before the rarest is reached. A lane cannot crowd out another.
package bundle

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// MaxPatterns is how many patterns one search may look for. A lane is a bit
// in a uint32 (Filter.Which); the limit is about the reply, which carries a
// section per pattern.
const MaxPatterns = 8

// Patterns is the list a search looks for: pattern, then more, with blanks
// and repeats dropped. An error for none, or for more than MaxPatterns.
func Patterns(pattern string, more []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, p := range append([]string{pattern}, more...) {
		if p = strings.TrimSpace(p); p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pattern is required: give pattern, or several in patterns")
	}
	if len(out) > MaxPatterns {
		return nil, fmt.Errorf("%d patterns given: one search looks for at most %d", len(out), MaxPatterns)
	}
	return out, nil
}

// PatternsArg reads a tool's list of patterns as a model sends it: a JSON
// array (the normal case), or the same array sent as a string, or one bare
// pattern. Never split on commas: a regular expression can hold them.
func PatternsArg(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			} else if e != nil {
				out = append(out, fmt.Sprint(e))
			}
		}
		return out
	case string:
		var list []string
		if strings.HasPrefix(strings.TrimSpace(t), "[") && json.Unmarshal([]byte(t), &list) == nil {
			return list
		}
		if strings.TrimSpace(t) != "" {
			return []string{t}
		}
	}
	return nil
}

// NewFilter compiles a search's patterns, one lane each, and its exclude.
// prefix goes in front of every expression ("(?i)" for a case-insensitive
// search).
func NewFilter(patterns []string, exclude, prefix string) (Filter, error) {
	var f Filter
	for i, p := range patterns {
		m, err := CompileMatcher(prefix + p)
		if err != nil {
			return f, fmt.Errorf("pattern %q is not a valid regular expression: %w", p, err)
		}
		if i == 0 {
			f.Match = m
		} else {
			f.Also = append(f.Also, m)
		}
	}
	if exclude = strings.TrimSpace(exclude); exclude != "" {
		m, err := CompileMatcher(prefix + exclude)
		if err != nil {
			return f, fmt.Errorf("exclude %q is not a valid regular expression: %w", exclude, err)
		}
		f.Exclude = m
	}
	return f, nil
}

// Lane is one pattern's answer in a search of several, as if it had been
// searched alone. T is what the search keeps of a match (a bundle's Hit, a
// file store's Match).
type Lane[T any] struct {
	Pattern string
	// Budget is embedded so a lane's counts read as its own, as a search's
	// do (budget.go).
	Budget
	Hits      []T
	Excluded  int        // lines this pattern matched that exclude took back out
	Groups    *Grouper   // when the search groups
	Histogram *Histogram // when it counts over time
}

// AllFull reports whether every lane has kept all it may, the point a search
// stops opening files.
func AllFull[T any](lanes []Lane[T]) bool {
	for i := range lanes {
		if !lanes[i].Full() {
			return false
		}
	}
	return true
}

// TakeLanes offers one match to the lanes in mask that still have room under
// offer, counting it against each, and returns those lanes. offered is how
// many of a file's matches each lane has been offered so far. A match is kept
// once however many lanes take it, and handed to each by LaneItems.
func TakeLanes(offered []int, mask uint32, offer int) uint32 {
	var took uint32
	for l := range offered {
		if bit := uint32(1) << l; mask&bit != 0 && offered[l] < offer {
			offered[l]++
			took |= bit
		}
	}
	return took
}

// LaneItems is the first n of items offered to lane l (bit l of their
// masks), in order.
func LaneItems[T any](items []T, masks []uint32, l, n int) []T {
	var out []T
	for i := range items {
		if len(out) == n {
			break
		}
		if masks[i]&(1<<l) != 0 {
			out = append(out, items[i])
		}
	}
	return out
}

// LaneOrder is the order a reply gives its lanes in: fewest matches first, so
// the rare pattern's answer is on the first page and the noisy one's runs
// on past it. Ties keep the order the patterns were given.
func LaneOrder[T any](lanes []Lane[T]) []int {
	order := make([]int, len(lanes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return lanes[order[a]].Total < lanes[order[b]].Total })
	return order
}

// LanesHead heads the reply to a search of several patterns: each one's count
// on a line, in the order the sections below come in.
func LanesHead[T any](lanes []Lane[T], order []int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d patterns searched in one pass. Each is answered in its own section below as if searched alone, fewest matches first:\n", len(lanes))
	for _, i := range order {
		ln := &lanes[i]
		count := "no matches"
		if ln.Total > 0 {
			count = fmt.Sprintf("%d match(es) in %d file(s)", ln.Total, len(ln.Tallies))
		}
		fmt.Fprintf(&b, "  %q: %s", ln.Pattern, count)
		if ln.Excluded > 0 {
			fmt.Fprintf(&b, " (%d more left out by exclude)", ln.Excluded)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// LaneSection is the heading of one pattern's section in that reply.
func LaneSection(pattern string) string {
	return fmt.Sprintf("\n######## pattern %q ########\n", pattern)
}
