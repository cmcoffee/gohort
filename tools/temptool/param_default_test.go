package temptool

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestParamDefaultKept: a declared default survives coercion (it used to be
// dropped there), a non-scalar one is refused rather than breaking the gob
// save, and an integer default is held as the float64 JSON gives.
func TestParamDefaultKept(t *testing.T) {
	p, err := coerceToolParam(map[string]any{"type": "integer", "default": float64(20)})
	if err != nil || p.Default != float64(20) {
		t.Fatalf("default dropped: %+v, %v", p, err)
	}
	if _, err := coerceToolParam(map[string]any{"type": "string", "default": []any{"a"}}); err == nil {
		t.Errorf("a list default must be refused")
	}
	if got := undefaulted([]string{"id", "limit"}, map[string]ToolParam{"id": {}, "limit": {Default: float64(5)}}); len(got) != 1 || got[0] != "id" {
		t.Errorf("a defaulted param is not required; got %v", got)
	}
}

// TestParamDefaultRoundTrip: the default survives create, the store (gob),
// and an unrelated update, for a toolbox action and an api tool alike. The
// update path rebuilds every field from the stored record, which is where
// fields have gone missing before.
func TestParamDefaultRoundTrip(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{Name: "no_auth", Type: SecureCredNone, BaseURL: "https://x.test"}, ""); err != nil {
		t.Fatalf("register credential: %v", err)
	}
	sess := newTestSession()
	if _, err := createGrouped(map[string]any{
		"name": "feeds", "description": "d", "mode": "toolbox", "credential": "no_auth",
		"actions": []any{map[string]any{
			"name": "feed", "url_template": "https://x.test/feed?limit={limit}",
			"params": map[string]any{"limit": map[string]any{"type": "integer", "default": 20}},
		}},
	}, sess); err != nil {
		t.Fatalf("create toolbox: %v", err)
	}
	if _, err := createGrouped(map[string]any{
		"name": "one_feed", "description": "d", "mode": "api", "credential": "no_auth",
		"url_template": "https://x.test/feed?limit={limit}",
		"params":       map[string]any{"limit": map[string]any{"type": "integer", "default": 20}},
	}, sess); err != nil {
		t.Fatalf("create api: %v", err)
	}
	for _, name := range []string{"feeds", "one_feed"} {
		if _, err := updateGrouped(map[string]any{"name": name, "description": "edited"}, sess); err != nil {
			t.Fatalf("update %s: %v", name, err)
		}
	}
	// Read back through the store, not the session copy.
	var tb, api *TempTool
	for _, d := range LoadSessionTempTools(sess.DB, sess.ChatSessionID) {
		d := d
		switch d.Name {
		case "feeds":
			tb = &d
		case "one_feed":
			api = &d
		}
	}
	if tb == nil || api == nil {
		t.Fatalf("tools not in the store")
	}
	if got := tb.Actions[0].Params["limit"].Default; got != float64(20) {
		t.Errorf("toolbox action default lost on the round trip: %v", got)
	}
	if got := api.Params["limit"].Default; got != float64(20) {
		t.Errorf("api tool default lost on the round trip: %v", got)
	}
}

// TestParamDefaultSentWhenOmitted is the live failure: an optional query
// param the model left out was removed from the URL, and the API answered
// 400. With a default it is sent, both on a real call of a toolbox action and
// on test's read probe of an endpoint that has no case.
func TestParamDefaultSentWhenOmitted(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	defer srv.Close()
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{Name: "feedsvc", Type: SecureCredNone, BaseURL: srv.URL}, ""); err != nil {
		t.Fatalf("cred: %v", err)
	}
	sess := &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()}}
	if _, err := createGrouped(map[string]any{
		"name": "feeds", "description": "d", "mode": "toolbox", "credential": "feedsvc",
		"actions": []any{map[string]any{
			"name": "feed", "url_template": srv.URL + "/feed?limit={limit}",
			"params": map[string]any{"limit": map[string]any{"type": "integer", "default": 20}},
		}},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	tt := sess.LookupTempTool("feeds")
	if tt == nil {
		t.Fatal("toolbox not live in the session")
	}
	if _, err := dispatchTempTool(sess, tt, map[string]any{"action": "feed"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	report, err := testGrouped(map[string]any{"name": "feeds"}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 || queries[0] != "limit=20" || queries[1] != "limit=20" {
		t.Errorf("both the call and the probe must send the default; queries %v\nreport:\n%s", queries, report)
	}
}
