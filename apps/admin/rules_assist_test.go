package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Suggest changes on a rules list talks to the worker with that list's own
// framing, chosen by the list's label on the server; a label that names no
// list is refused rather than sent with no framing.
func TestRulesAssistFramesEachListOnTheServer(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Tightened the first line.\n\n" + DraftFence + "\nDo not use em-dashes.\n```"}}}
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(fake, nil)
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })

	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerRulesRoutes(mux)
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/rules-assist", strings.NewReader(body)))
		return w
	}

	if w := call(`{"section":"Anything","message":"hi","assist_prompt":"You are a general assistant."}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown list answered %d", w.Code)
	}
	w := call(`{"section":"Style","message":"tighten it","draft":"Avoid em-dashes please.","assist_prompt":"You are a general assistant."}`)
	if w.Code != 200 {
		t.Fatalf("style assist answered %d %s", w.Code, w.Body.String())
	}
	var out struct{ Reply, Value string }
	json.NewDecoder(w.Body).Decode(&out)
	if out.Value != "Do not use em-dashes." || !strings.Contains(out.Reply, "Tightened") {
		t.Fatalf("reply = %+v", out)
	}
	sys := fake.Config(0).SystemPrompt
	if !strings.Contains(sys, "house-style rules") || strings.Contains(sys, "general assistant") || !strings.Contains(sys, "Avoid em-dashes please.") {
		t.Fatalf("system prompt = %q", sys)
	}
}
