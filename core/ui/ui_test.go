package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// A nav entry with an icon reaches the page's config with it, so the runtime
// draws it at the bar's end; one without carries no icon.
func TestANavIconReachesThePage(t *testing.T) {
	b := httptest.NewRecorder()
	p := Page{Title: "t", Nav: []NavLink{{Label: "Records", URL: "r"}, {Label: "Settings...", URL: "s", Icon: "settings"}}}
	if err := p.Render(b); err != nil {
		t.Fatal(err)
	}
	out := b.Body.String()
	if !strings.Contains(out, `{"label":"Settings...","url":"s","icon":"settings"}`) {
		t.Errorf("the icon entry is not in the page's config:\n%s", out)
	}
	if !strings.Contains(out, `{"label":"Records","url":"r"}`) {
		t.Errorf("a plain entry gained fields:\n%s", out)
	}
}
