package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// httpTo sends one request to an app's HTTP API and decodes its JSON reply.
func httpTo(app *MCPServer, method, path, key, body string) (int, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.Header.Set("X-API-Key", key)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	app.handleApp(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// An app's tools over plain HTTP, with the same key as its MCP address: the
// list, a quick call answered with its result (its JSON as JSON), a long one
// answered 202 with its job, and that job collected, listed and stopped.
func TestAnAppOverPlainHTTP(t *testing.T) {
	app, s := jobsFixture(t)
	key := MintAccountTokenScoped("user-a", "script", &TokenScope{Features: []string{MCPFeatureKey}}).Token

	code, out := httpTo(app, http.MethodGet, "/app/lab/tools", key, "")
	if code != http.StatusOK || !strings.Contains(mustJSON(out), `"lab_quick"`) || strings.Contains(mustJSON(out), "lab_result") {
		t.Fatalf("GET tools: %d %s", code, mustJSON(out))
	}
	code, out = httpTo(app, http.MethodPost, "/app/lab/tools/lab_quick", key, `{"text":"hi"}`)
	if code != http.StatusOK || out["result"] != "quick:hi" {
		t.Errorf("a quick call: %d %s", code, mustJSON(out))
	}
	code, out = httpTo(app, http.MethodPost, "/app/lab/tools/lab_wait?wait=0", key, `{"tag":"H"}`)
	job, _ := out["job"].(string)
	if code != http.StatusAccepted || job == "" || out["running"] != true || out["job_url"] != "/mcp/app/lab/jobs/"+job {
		t.Fatalf("a long call: %d %s", code, mustJSON(out))
	}
	time.Sleep(20 * time.Millisecond)
	if code, out = httpTo(app, http.MethodGet, "/app/lab/jobs/"+job, key, ""); code != http.StatusOK || out["state"] != "running" || out["now"] != "warming up H" {
		t.Errorf("a running job: %d %s", code, mustJSON(out))
	}
	if code, out = httpTo(app, http.MethodGet, "/app/lab/jobs", key, ""); code != http.StatusOK || !strings.Contains(mustJSON(out), job) {
		t.Errorf("the key's jobs: %d %s", code, mustJSON(out))
	}
	close(s.release)
	if code, out = httpTo(app, http.MethodGet, "/app/lab/jobs/"+job+"?wait=1", key, ""); code != http.StatusOK || out["result"] != "done:H" || out["state"] != "ended" {
		t.Errorf("the ended job: %d %s", code, mustJSON(out))
	}
	// The same job over MCP: one door, two protocols.
	if got := callText(rpcTo(app, "/app/lab/", key, "tools/call", map[string]any{"name": "lab_result", "arguments": map[string]any{"job": job}})); got != "done:H" {
		t.Errorf("the HTTP call's job over MCP: %q", got)
	}
}

// A stopped job answers 409; the caller's other mistakes get their own codes,
// and nothing is reached without the key.
func TestAnAppsHTTPAPIRefusals(t *testing.T) {
	app, _ := jobsFixture(t)
	key := MintAccountTokenScoped("user-a", "script", &TokenScope{Features: []string{MCPFeatureKey}}).Token
	other := MintAccountTokenScoped("user-a", "other", &TokenScope{Features: []string{MCPFeatureKey}}).Token

	_, out := httpTo(app, http.MethodPost, "/app/lab/tools/lab_wait?wait=0", key, `{"tag":"S"}`)
	job, _ := out["job"].(string)
	if code, _ := httpTo(app, http.MethodDelete, "/app/lab/jobs/"+job, other, ""); code != http.StatusNotFound {
		t.Errorf("another key stopped the job: %d", code)
	}
	if code, out := httpTo(app, http.MethodDelete, "/app/lab/jobs/"+job, key, ""); code != http.StatusOK || out["state"] != "stopped" {
		t.Errorf("stop: %d %s", code, mustJSON(out))
	}
	if code, out := httpTo(app, http.MethodGet, "/app/lab/jobs/"+job, key, ""); code != http.StatusConflict || out["state"] != "stopped" {
		t.Errorf("a stopped job: %d %s", code, mustJSON(out))
	}
	for _, tc := range []struct {
		method, path, key, body string
		want                    int
	}{
		{http.MethodGet, "/app/lab/tools", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/app/elsewhere/tools", key, "", http.StatusNotFound},
		{http.MethodPost, "/app/lab/tools/nosuch", key, "{}", http.StatusNotFound},
		{http.MethodPost, "/app/lab/tools/lab_quick", key, "[1,2]", http.StatusBadRequest},
		{http.MethodPost, "/app/lab/tools/lab_quick?wait=soon", key, "{}", http.StatusBadRequest},
		{http.MethodGet, "/app/lab/tools/lab_quick", key, "", http.StatusMethodNotAllowed},
		{http.MethodGet, "/app/lab/jobs/000000000000", key, "", http.StatusNotFound},
		{http.MethodGet, "/app/lab/nothing", key, "", http.StatusNotFound},
	} {
		if code, out := httpTo(app, tc.method, tc.path, tc.key, tc.body); code != tc.want || out["ok"] != false {
			t.Errorf("%s %s: %d %s, want %d", tc.method, tc.path, code, mustJSON(out), tc.want)
		}
	}
	narrow := MintAccountTokenScoped("user-a", "narrow", &TokenScope{Features: []string{MCPFeatureKey}, Tools: &[]string{"lab_wait"}}).Token
	if code, _ := httpTo(app, http.MethodPost, "/app/lab/tools/lab_quick", narrow, "{}"); code != http.StatusForbidden {
		t.Errorf("a key narrowed away from lab_quick called it: %d", code)
	}
	appJobCap = 1
	if code, out := httpTo(app, http.MethodPost, "/app/lab/tools/lab_wait?wait=0", other, `{"tag":"C1"}`); code != http.StatusAccepted {
		t.Fatalf("first call under a cap of 1: %d %s", code, mustJSON(out))
	}
	if code, _ := httpTo(app, http.MethodPost, "/app/lab/tools/lab_quick", other, `{"text":"x"}`); code != http.StatusTooManyRequests {
		t.Errorf("a call over the cap: %d", code)
	}
}
