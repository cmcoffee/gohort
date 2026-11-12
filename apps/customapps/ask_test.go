package customapps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
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
	appAgentAsk = func(ctx context.Context, owner, user, agentID, prompt string, jsonMode bool) (string, float64, error) {
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
	appAgentAsk = func(context.Context, string, string, string, string, bool) (string, float64, error) {
		return "ok", 0, nil
	}
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

// A script's gohort.ask is held to the same caps as the page's, charged to
// the person the run is for.
func TestAScriptsAskSharesThePagesCaps(t *testing.T) {
	saved, savedAsk := RootDB, appAgentAsk
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved; appAgentAsk = savedAsk })
	calls := 0
	appAgentAsk = func(ctx context.Context, owner, user, agentID, prompt string, jsonMode bool) (string, float64, error) {
		calls++
		return "fine", 0.30, nil
	}
	spec := AppSpec{Owner: "alice", Slug: "wx", AgentID: "a1", AskUserDailyUSD: 0.50}
	if text, err := appscript.AppAsk(context.Background(), spec, "bob", "hi", false); err != nil || text != "fine" {
		t.Fatalf("first ask: %q %v", text, err)
	}
	if _, err := appscript.AppAsk(context.Background(), spec, "bob", "hi", false); err != nil {
		t.Fatalf("second ask (0.30 spent of 0.50): %v", err)
	}
	if _, err := appscript.AppAsk(context.Background(), spec, "bob", "hi", false); err == nil || !strings.Contains(err.Error(), "you have spent") {
		t.Fatalf("over bob's cap: %v", err)
	}
	if _, err := appscript.AppAsk(context.Background(), spec, "carol", "hi", false); err != nil {
		t.Fatalf("carol has her own allowance: %v", err)
	}
	if calls != 3 {
		t.Fatalf("model calls = %d, want 3", calls)
	}
}

// A prompt is limited by what the model reads, as a chat message is, not by
// a character count: a document's worth goes through, in any script and
// however JSON escapes it, and only something past the runaway guard is
// refused, by the page's ask and a script's calls alike.
func TestALongAskGoesThrough(t *testing.T) {
	ownerDB := &DBase{Store: kvlite.MemStore()}
	spec := AppSpec{Slug: "notes", Owner: "alice", AgentID: "npc"}
	prev := appAgentAsk
	t.Cleanup(func() { appAgentAsk = prev })
	got := 0
	appAgentAsk = func(ctx context.Context, owner, user, agentID, prompt string, jsonMode bool) (string, float64, error) {
		got = len([]rune(prompt))
		return "ok", 0, nil
	}
	T := &CustomApps{}
	ask := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		T.handleAsk(w, httptest.NewRequest(http.MethodPost, "/apps/notes/ask", strings.NewReader(body)), ownerDB, "alice", "bob", spec)
		return w
	}
	const doc = 200000 // characters: a long document, far past the old 8000
	if w := ask(`{"prompt":"` + strings.Repeat("a", doc) + `"}`); w.Code != http.StatusOK || got != doc {
		t.Fatalf("a document-sized prompt: %d %.200s (agent got %d)", w.Code, w.Body.String(), got)
	}
	// Three bytes a character as text, six escaped: up to the guard, taken.
	cjk := appPromptMaxBytes / 3
	if w := ask(`{"prompt":"` + strings.Repeat(`\u6587`, cjk) + `"}`); w.Code != http.StatusOK || got != cjk {
		t.Fatalf("an escaped non-Latin prompt up to the guard: %d %.200s (agent got %d)", w.Code, w.Body.String(), got)
	}
	if w := ask(`{"prompt":"` + strings.Repeat("a", appPromptMaxBytes+1) + `"}`); w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "summarize") {
		t.Errorf("a prompt past the guard: %d %.200s", w.Code, w.Body.String())
	}
	if _, err := appscript.AppAsk(context.Background(), spec, "bob", strings.Repeat("a", appPromptMaxBytes+1), false); err == nil {
		t.Error("a script's ask past the guard went through")
	}
}
