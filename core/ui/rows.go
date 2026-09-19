package ui

// Ordering the rows a sectioned view draws.
//
// The "_section" convention is core/ui's: a cards view draws a heading each
// time that hidden field changes from the previous row DRAWN. That makes the
// ORDER of the rows load-bearing rather than cosmetic — two rows of the same
// section separated by a row of another draws that heading twice, and a reader
// sees one list as two.
//
// So the sorter belongs with the convention it serves. It lived in an app
// because that is where the first sectioned page was, which is how a generic
// thing comes to look like part of a feature.

import "sort"

// SortRowsBySection orders rows by section, then by when each will next happen,
// then by name.
//
// The section ORDER is the caller's, not a table here, because two pages
// composing the same records legitimately want opposite orders — one leading
// with what runs next, another with what is stuck. A shared table would make
// one page's sections all rank equal, and equal sections interleave.
//
// Within a section: a row with a next_run leads one without, then soonest
// first. RFC3339 in UTC sorts correctly as text, a fixed offset and a fixed
// width being the whole point of that format, so no parsing is needed and an
// unparseable stamp cannot become the epoch and jump to the top. Ties break on
// name, so the order is stable across refreshes rather than shuffling rows
// somebody is aiming at.
//
// A section not named in order sorts LAST rather than first: one added later
// appears at the bottom instead of displacing the ones people came for.
func SortRowsBySection(rows []map[string]any, order []string) {
	rank := make(map[string]int, len(order))
	for i, name := range order {
		rank[name] = i
	}
	rankOf := func(m map[string]any) int {
		if r, ok := rank[RowString(m, "_section")]; ok {
			return r
		}
		return len(order)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if ri, rj := rankOf(rows[i]), rankOf(rows[j]); ri != rj {
			return ri < rj
		}
		ni, nj := RowString(rows[i], "next_run"), RowString(rows[j], "next_run")
		if (ni == "") != (nj == "") {
			return ni != "" // a row with a next occurrence outranks one without
		}
		if ni != nj {
			return ni < nj
		}
		return RowString(rows[i], "name") < RowString(rows[j], "name")
	})
}

// RowString reads a string field off a row built as a map. Absent, or of
// another type, reads as empty — which is what an omitempty field that was
// blank looks like after a round trip through JSON.
func RowString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
