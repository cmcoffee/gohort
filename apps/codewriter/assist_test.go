package codewriter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A document's ✨ Draft runs on core/assist: the document assist's prompt
// as before, the reply and the section's text without a heading that
// restates its name, on CodeWriter's own worker.
func TestTheDocumentAssistRunsOnTheAssistHandler(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:u", AuthUser{Username: "u"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Tightened it.\n```draft\n## Setup\nRun make.\n```"}}}
	T := &CodeWriterAgent{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}, LLM: fake}}
	r := httptest.NewRequest(http.MethodPost, "/api/assist", strings.NewReader(`{"name":"README","section":"Setup","message":"tighten","draft":"Run make, then run it.","mode":"write"}`))
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), "u")})
	w := httptest.NewRecorder()
	T.handleAssist(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out["reply"] != "Tightened it." || out["value"] != "Run make." {
		t.Fatalf("want the reply and the section's text: %d %s", w.Code, w.Body.String())
	}
	if sys := fake.Config(0).SystemPrompt; !strings.Contains(sys, BuildDocAssistPrompt("README", "Setup", "Run make, then run it.", "", "write")) {
		t.Errorf("the document assist's prompt should be used as written:\n%s", sys)
	}
	anon := httptest.NewRecorder()
	T.handleAssist(anon, httptest.NewRequest(http.MethodPost, "/api/assist", strings.NewReader(`{"message":"x"}`)))
	if anon.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d", anon.Code)
	}
}
