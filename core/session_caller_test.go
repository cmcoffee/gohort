package core

// A session's model calls are named for its app. The caller was a bare UUID,
// unique so the scheduler takes turns between sessions, but it is also what
// names a call with no route key, so the request log and a peer lending its
// model showed "caller:ba0e9587-…" and nothing said whose call it was.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestASessionsCallsAreNamedForItsApp(t *testing.T) {
	a, b := sessionCallerID("autoblog"), sessionCallerID("autoblog")
	if !regexp.MustCompile(`^autoblog\.[0-9a-f]{8}$`).MatchString(a) || a == b {
		t.Errorf("callers should lead with the app and stay unique: %q %q", a, b)
	}
	if got := sessionCallerID("Some App!"); !strings.HasPrefix(got, "some_app.") {
		t.Errorf("an app name is slugged: %q", got)
	}
	if got := sessionCallerID(""); len(got) != 36 {
		t.Errorf("no app name keeps the whole UUID: %q", got)
	}
	// A peer shows the name, so it must pass the serving side's filter.
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set(peerCallHeader, "caller:"+a)
	if peerCallRoute(r) == "" {
		t.Errorf("the serving side would drop %q", "caller:"+a)
	}

	llm := &FakeLLM{Turns: []FakeTurn{{Content: "ok", Repeat: true}}}
	core := &AppCore{LLM: llm}
	core.SetAppName("autoblog")
	if _, err := core.CreateSession(WORKER).Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if c := llm.Config(0).Caller; !strings.HasPrefix(c, "autoblog.") {
		t.Errorf("the session's call went out as caller %q", c)
	}
}
