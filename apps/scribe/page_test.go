package scribe

import (
	"strings"
	"testing"
)

// Both copies of the guide pages' element helper check a link's address: the
// publish dialog's "Open" link is a webhook's word, and javascript: there ran
// as whoever clicked it.
func TestTheGuideElementHelperChecksLinks(t *testing.T) {
	for name, src := range map[string]string{"guideModalElJS": guideModalElJS, "guideSectionCode": guideSectionCode} {
		if !strings.Contains(src, "(k === 'href' || k === 'src') && window.uiSafeURL") {
			t.Errorf("%s sets href and src unchecked", name)
		}
	}
}
