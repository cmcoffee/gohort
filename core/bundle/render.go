// A search's reply, shared by every tool that searches through this package
// (a bundle's, a repository's), so the same answer reads the same way.
package bundle

import (
	"fmt"
	"strings"
)

// RenderSearch is a search's reply: its hits, or its counts, groups or rows
// over time, and plainly what the search did not see. readTool names the
// tool that reads on past a cut record.
func RenderSearch(res SearchResult, q Query, readTool string) string {
	// Several patterns: a line each up top, then each one's answer as its
	// own search's would read, the rarest first (core/bundle lanes.go).
	if len(res.Lanes) > 1 {
		order := LaneOrder(res.Lanes)
		var b strings.Builder
		b.WriteString(LanesHead(res.Lanes, order))
		for _, i := range order {
			lq := q
			lq.Pattern, lq.Patterns = res.Lanes[i].Pattern, nil
			b.WriteString(LaneSection(lq.Pattern))
			b.WriteString(RenderSearch(res.Lane(i), lq, readTool))
		}
		return b.String()
	}
	if h := res.Histogram; h != nil && h.Lines+h.Undated > 0 {
		var b strings.Builder
		b.WriteString(h.Render(fmt.Sprintf("%q", q.Pattern), q.Bucket))
		fmt.Fprintf(&b, "(%d files scanned.)\n", res.Scanned)
		if res.Excluded > 0 {
			fmt.Fprintf(&b, "%d more line(s) matched but were left out by exclude, and are not counted.\n", res.Excluded)
		}
		if res.TimeUnknown > 0 {
			fmt.Fprintf(&b, "%d files were skipped because they carry no parseable timestamps and a time window was set.\n", res.TimeUnknown)
		}
		return b.String()
	}
	if res.Groups != nil && res.Groups.Lines > 0 {
		var b strings.Builder
		b.WriteString(res.Groups.Render(400))
		fmt.Fprintf(&b, "(%d files scanned.)\n", res.Scanned)
		if res.Excluded > 0 {
			fmt.Fprintf(&b, "%d more line(s) matched but were left out by exclude, and are in no shape above.\n", res.Excluded)
		}
		if res.TimeUnknown > 0 {
			fmt.Fprintf(&b, "%d files were skipped because they carry no parseable timestamps and a time window was set.\n", res.TimeUnknown)
		}
		return b.String()
	}
	notes := func(b *strings.Builder) {
		if res.Excluded > 0 {
			fmt.Fprintf(b, "%d more line(s) matched but were left out by exclude.\n", res.Excluded)
		}
		if res.TimeUnknown > 0 {
			fmt.Fprintf(b, "%d files were skipped because they carry no parseable timestamps and a time window was set.\n", res.TimeUnknown)
		}
	}
	if q.CountOnly {
		var b strings.Builder
		b.WriteString(res.Counts(q.Pattern, res.Scanned, res.Scanned+res.Unsearched))
		notes(&b)
		return b.String()
	}
	if len(res.Hits) == 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "No matches for %q in %d files scanned.\n", q.Pattern, res.Scanned)
		// The pattern IS there when exclude took its matches out; saying
		// "no matches" alone would report an absence the filter made.
		if res.Excluded > 0 {
			fmt.Fprintf(&b, "But %d line(s) matched the pattern and every one also matched exclude %q: narrow exclude, or drop it, to see them.\n", res.Excluded, q.Exclude)
		}
		if res.Skipped > 0 {
			fmt.Fprintf(&b, "%d files were excluded by the path filter or the time window.\n", res.Skipped)
		}
		if res.TimeUnknown > 0 {
			fmt.Fprintf(&b, "%d files were excluded because they carry no parseable timestamps and a time window was set: re-run without since/until to include them.\n", res.TimeUnknown)
		}
		return b.String()
	}
	// Everything that qualifies the hits comes first: a long reply arrives
	// as its first page, and these must be on it.
	var b strings.Builder
	b.WriteString(res.Summary(res.Scanned, res.Scanned+res.Unsearched, "glob"))
	notes(&b)
	b.WriteString("\n")
	withContext := q.Before > 0 || q.After > 0 || q.Records
	lastPath := ""
	for _, h := range res.Hits {
		if withContext {
			if h.Path != lastPath {
				fmt.Fprintf(&b, "\n=== %s ===\n", h.Path)
				lastPath = h.Path
			}
			above, below := RecordNotes(h.Record, readTool)
			b.WriteString(above)
			b.WriteString(strings.Join(h.Context, "\n"))
			b.WriteString("\n")
			b.WriteString(below)
			b.WriteString("--\n")
			continue
		}
		fmt.Fprintf(&b, "%s:%d: %s\n", h.Path, h.Line, h.Text)
	}
	return b.String()
}
