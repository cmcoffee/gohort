package scribe

import (
	"net/http/httptest"
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

// Scribe's ⚙ sits beside its title, as on every app, and opens a menu of
// what a person sets for themselves: the house style and their copy of the
// Guide Author, each where it already lives.
func TestScribesGearOpensItsSettings(t *testing.T) {
	w := httptest.NewRecorder()
	(&Scribe{}).servePage(w, httptest.NewRequest("GET", "/", nil))
	page := w.Body.String()
	for _, want := range []string{`"icon":"settings"`, `"action":"scribe_settings"`, `uiRegisterClientAction("scribe_settings"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %s", want)
		}
	}
	for _, want := range []string{"action: 'scribe_rules'", "url: 'chat/settings'"} {
		if !strings.Contains(scribeSettingsAction, want) {
			t.Errorf("the ⚙ menu does not offer %s", want)
		}
	}
}
