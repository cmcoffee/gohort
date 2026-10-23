package servitor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The chat's Edit and ✕ on a message reach Servitor's sessions as they reach
// orchestrate's: {at} cuts the thread from a message on, {delete_at} takes
// out one message and keeps the rest. Without the PATCH the page offered no
// controls at all, since it shows them only when an app can carry them out.
func TestASessionMessageCanBeEditedOrDeleted(t *testing.T) {
	twoUserAuth(t)
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "craig")
	saveSession(udb, "box-1", chatSession{ID: "s1", Name: "disk", Messages: []sessionMsg{
		{Role: "user", Content: "how full is /var"}, {Role: "assistant", Content: "82%"},
		{Role: "user", Content: "and /home"}, {Role: "assistant", Content: "40%"},
	}})
	patch := func(user, body string) int {
		w := httptest.NewRecorder()
		app.handleServitorSessionOne(w, reqAs(t, http.MethodPatch, "/api/sessions/s1?appliance_id=box-1", user, body))
		return w.Code
	}
	contents := func() []string {
		s, _ := loadSession(udb, "box-1", "s1")
		var out []string
		for _, m := range s.Messages {
			out = append(out, m.Content)
		}
		return out
	}

	if code := patch("craig", `{"delete_at":1}`); code != http.StatusOK {
		t.Fatalf("delete one: %d", code)
	}
	if got := contents(); len(got) != 3 || got[1] != "and /home" {
		t.Fatalf("one message out, the rest kept: %v", got)
	}
	if code := patch("craig", `{"at":1}`); code != http.StatusOK {
		t.Fatalf("cut: %d", code)
	}
	if got := contents(); len(got) != 1 || got[0] != "how full is /var" {
		t.Fatalf("everything from the cut on is gone: %v", got)
	}
	if code := patch("dana", `{"at":0}`); code != http.StatusNotFound {
		t.Fatalf("another person's session is not theirs to cut: %d", code)
	}
	if got := contents(); len(got) != 1 {
		t.Fatalf("a refused cut changes nothing: %v", got)
	}
}
