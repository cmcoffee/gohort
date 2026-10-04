package temptool

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// fakeAPI is an httptest server behind a SecureCredNone credential, so a
// tool's live calls land here. It records each request as "METHOD path?query".
type fakeAPI struct {
	mu   sync.Mutex
	hits []string
	srv  *httptest.Server
}

func newFakeAPI(t *testing.T, cred string) (*fakeAPI, *ToolSession) {
	t.Helper()
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = w.Write([]byte(`{"ok":true,"items":[1]}`))
	}))
	t.Cleanup(f.srv.Close)
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	t.Cleanup(func() { AuthDB = prev })
	if err := Secure().Save(SecureCredential{Name: cred, Type: SecureCredNone, BaseURL: f.srv.URL}, ""); err != nil {
		t.Fatalf("cred: %v", err)
	}
	sess := &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()}}
	return f, sess
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.hits)
}

// TestCaseUnknownArgFails is the friction-report case: a case passing
// from1_currency for a param named from_currency PASSed, because the
// misspelled arg simply never reached the call. It now FAILs that case,
// names the param it was meant to be, and sends nothing for it.
func TestCaseUnknownArgFails(t *testing.T) {
	f, sess := newFakeAPI(t, "fx")
	if _, err := createGrouped(map[string]any{
		"name": "fx", "description": "d", "mode": "toolbox", "credential": "fx",
		"actions": []any{map[string]any{
			"name": "convert", "url_template": f.srv.URL + "/convert?from={from_currency}&to={to_currency}",
			"params": map[string]any{
				"from_currency": map[string]any{"type": "string"},
				"to_currency":   map[string]any{"type": "string"},
			},
		}},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	report, err := testGrouped(map[string]any{"name": "fx", "cases": []any{
		map[string]any{"action": "convert", "args": map[string]any{"from1_currency": "USD", "to_currency": "EUR"}},
	}}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !strings.Contains(report, "[FAIL] convert") || !strings.Contains(report, `did you mean "from_currency"`) {
		t.Errorf("want the case FAILed with the nearest declared param named; report:\n%s", report)
	}
	if strings.Contains(report, "Tool verified") {
		t.Errorf("a failed case must not verify the tool; report:\n%s", report)
	}
	if n := f.count(); n != 0 {
		t.Errorf("a case with a misnamed arg must not be probed; %d call(s) made", n)
	}
}

// TestCaseUnknownActionReported: a case whose action names no endpoint was
// dropped without a word; now it is a FAIL that lists the real endpoints.
func TestCaseUnknownActionReported(t *testing.T) {
	sess := newTestSession()
	sess.Network = NewNetworkConnector(true) // offline: only the case check matters here
	if _, err := createGrouped(map[string]any{
		"name": "svc", "description": "d", "mode": "toolbox", "credential": "no_auth",
		"actions": []any{map[string]any{"name": "feed", "url_template": "https://x.test/feed"}},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	report, err := testGrouped(map[string]any{"name": "svc", "cases": []any{
		map[string]any{"action": "fead", "args": map[string]any{}},
	}}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !strings.Contains(report, `[FAIL] case action="fead"`) || !strings.Contains(report, "Endpoints: feed") || !strings.Contains(report, "RESULT: FAILED") {
		t.Errorf("want the stray case reported as a failure; report:\n%s", report)
	}
}
