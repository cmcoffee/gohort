package customapps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every ask is bounded before it is made: the whole app's daily spend, each
// user's, and each user's daily count. One user reaching their own cap does
// not stop another; the app's cap stops everyone.
func TestAppAsksAreCappedPerAppAndPerUser(t *testing.T) {
	ownerDB := &DBase{Store: kvlite.MemStore()}
	spec := AppSpec{Slug: "game", Owner: "alice", AgentID: "npc", AskDailyUSD: 1.00, AskUserDailyUSD: 0.40}
	prev := appAgentAsk
	t.Cleanup(func() { appAgentAsk = prev })
	asked := 0
	appAgentAsk = func(ctx context.Context, owner, agentID, prompt string, jsonMode bool) (string, float64, error) {
		asked++
		return "line " + prompt, 0.25, nil
	}
	T := &CustomApps{}
	ask := func(user, prompt string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		T.handleAsk(w, httptest.NewRequest(http.MethodPost, "/apps/game/ask", strings.NewReader(`{"prompt":"`+prompt+`"}`)), ownerDB, "alice", user, spec)
		return w
	}
	if w := ask("bob", "hello"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "line hello") {
		t.Fatalf("first ask: %d %s", w.Code, w.Body.String())
	}
	ask("bob", "again") // bob has now spent 0.50, over his 0.40
	if w := ask("bob", "third"); w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "you have spent") {
		t.Fatalf("bob over his own cap: %d %s", w.Code, w.Body.String())
	}
	if w := ask("carol", "hi"); w.Code != http.StatusOK {
		t.Fatalf("carol was stopped by bob's cap: %d %s", w.Code, w.Body.String())
	}
	ask("dave", "hi") // the app has now spent 1.00
	if w := ask("erin", "hi"); w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "this app has spent") {
		t.Fatalf("the app's cap did not stop erin: %d %s", w.Code, w.Body.String())
	}
	if asked != 4 {
		t.Errorf("asked the agent %d times, want 4: a refused ask must not reach it", asked)
	}

	// The count cap, for a model that costs nothing.
	freeDB := &DBase{Store: kvlite.MemStore()}
	free := AppSpec{Slug: "free", Owner: "alice", AgentID: "npc"}
	appAgentAsk = func(context.Context, string, string, string, bool) (string, float64, error) { return "ok", 0, nil }
	var last *httptest.ResponseRecorder
	for i := 0; i <= askUserDailyCalls; i++ {
		last = httptest.NewRecorder()
		T.handleAsk(last, httptest.NewRequest(http.MethodPost, "/apps/free/ask", strings.NewReader(`{"prompt":"x"}`)), freeDB, "alice", "bob", free)
	}
	if last.Code != http.StatusTooManyRequests || !strings.Contains(last.Body.String(), "times today") {
		t.Fatalf("ask %d: %d %s", askUserDailyCalls+1, last.Code, last.Body.String())
	}

	// No agent bound, an empty prompt, a GET: refused before anything runs.
	for _, c := range []struct {
		spec   AppSpec
		method string
		body   string
	}{
		{AppSpec{Slug: "x", Owner: "alice"}, http.MethodPost, `{"prompt":"hi"}`},
		{spec, http.MethodPost, `{"prompt":""}`},
		{spec, http.MethodGet, ``},
	} {
		w := httptest.NewRecorder()
		T.handleAsk(w, httptest.NewRequest(c.method, "/apps/x/ask", strings.NewReader(c.body)), ownerDB, "alice", "zed", c.spec)
		if w.Code == http.StatusOK {
			t.Errorf("%s %q on %+v was answered", c.method, c.body, c.spec)
		}
	}
}
