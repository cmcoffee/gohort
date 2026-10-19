package ui

// A row's badges are whole pills. As ordinary cells they shrank with the text
// beside them, and a crowded row (one with a dozen badge columns) cut a pill
// partway through. A badge that is off left an empty cell behind, and each
// one still cost the row a gap, so no two rows placed their pills alike.

import (
	"strings"
	"testing"
)

func TestABadgeIsNeverCutAndAnOffOneTakesNoRoom(t *testing.T) {
	for _, want := range []string{
		".ui-row-cells > .ui-table-cell-badge { flex-shrink: 0; }",
		".ui-table-cell-badge > .ui-badge { white-space: nowrap; }",
		".ui-row-cells > .ui-table-cell-badge:empty { display: none; }",
		// The second line is prose: a note a row does not have gives its
		// share to the text beside it.
		".ui-row-cells-2 > .ui-table-cell:empty { display: none; }",
	} {
		if !strings.Contains(runtimeCSS, want) {
			t.Errorf("runtime.css is missing %q", want)
		}
	}
}
