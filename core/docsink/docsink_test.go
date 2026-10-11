package docsink

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A sink is offered only to a user it is available to, and only a sink that
// can save is registered.
func TestASinkIsOfferedToWhoMayUseIt(t *testing.T) {
	save := func(user, title, html string) (string, string, error) { return "/x/", title, nil }
	Register(Sink{Name: "open", Label: "Save to Open", Save: save})
	Register(Sink{Name: "closed", Label: "Save to Closed", Save: save, Available: func(*http.Request) bool { return false }})
	Register(Sink{Name: "broken", Label: "Save to Broken"})
	r := httptest.NewRequest("GET", "/", nil)
	got := map[string]bool{}
	for _, s := range For(r) {
		got[s.Name] = true
	}
	if !got["open"] || got["closed"] || got["broken"] {
		t.Errorf("offered: %v", got)
	}
	if _, ok := Find(r, "closed"); ok {
		t.Error("a sink the user may not use was found")
	}
}
