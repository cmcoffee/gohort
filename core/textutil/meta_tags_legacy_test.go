package textutil

import (
	"strings"
	"testing"
)

// History written before the rename carries the old marker; both are
// stripped, so an old thread's closed notes do not leak into a reply.
func TestBothMetaMarkersAreStripped(t *testing.T) {
	in := "Hello <gohort-meta>closed</gohort-meta> there <oddjob-meta>also</oddjob-meta> end"
	out := StripMetaTags(in)
	if strings.Contains(out, "closed") || strings.Contains(out, "also") || !strings.Contains(out, "Hello") {
		t.Errorf("markers survived: %q", out)
	}
}
