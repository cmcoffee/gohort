package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestFetchViaHeadersReachWire is the regression guard for the CalDAV bridge
// gap: the in-script fetch_via helper could not send custom request headers
// (Depth, Content-Type), so a CalDAV PROPFIND/REPORT built as a shell tool
// kept failing while the equivalent api-mode tool — which DID accept
// request_headers — worked. handleFetchVia now routes through
// DispatchToolCallArgs, so caller headers land as request_headers on the wire.
// This asserts the through-line the hook depends on: method + arbitrary
// headers reach the server, and a caller Authorization header is stripped
// (credential auth wins).
func TestFetchViaHeadersReachWire(t *testing.T) {
	var gotMethod, gotDepth, gotCT, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotDepth = r.Header.Get("Depth")
		gotCT = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte("<multistatus/>"))
	}))
	defer srv.Close()

	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	// none-type cred over http:// (test server) — no secret needed, empty
	// endpoints = every path under base_url allowed.
	cred := SecureCredential{
		Name:    "apple_caldav",
		Type:    SecureCredNone,
		BaseURL: srv.URL,
	}
	if err := s.Save(cred, ""); err != nil {
		t.Fatalf("save cred: %v", err)
	}

	args := map[string]any{
		"url":    srv.URL + "/195178399/principal/",
		"method": "PROPFIND",
		"body":   "<propfind/>",
		"request_headers": map[string]any{
			"Depth":         "1",
			"Content-Type":  "application/xml",
			"Authorization": "Basic should-be-stripped",
		},
	}
	if _, err := s.DispatchToolCallArgs(nil, "apple_caldav", args); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if gotMethod != "PROPFIND" {
		t.Errorf("method: got %q want PROPFIND", gotMethod)
	}
	if gotDepth != "1" {
		t.Errorf("Depth header: got %q want 1", gotDepth)
	}
	if gotCT != "application/xml" {
		t.Errorf("Content-Type: got %q want application/xml", gotCT)
	}
	if gotAuth != "" {
		t.Errorf("Authorization should be stripped (credential auth wins), got %q", gotAuth)
	}
}

// TestShimFetchViaCarriesHeaders locks the shim-side half of the same fix:
// both the singleton method and the module-level fetch_via must expose a
// headers param and forward it in the hook payload. Without this the wire
// plumbing above is unreachable from a script.
func TestShimFetchViaCarriesHeaders(t *testing.T) {
	shim := SandboxHookPythonShim
	for _, want := range []string{
		`def fetch_via(self, credential, url, method="GET", body=None, headers=None, request_headers=None, timeout=None):`,
		`"headers": hdrs,`,
		`def fetch_via(credential, url, method="GET", body=None, headers=None, request_headers=None, timeout=None):`,
	} {
		if !strings.Contains(shim, want) {
			t.Errorf("shim missing fetch_via headers plumbing: %q", want)
		}
	}
}

// A script's fetch through a credential reads the whole body. The hook paths
// set the piped flag; without it a response over the general cap is cut, which
// is what turned a 263 KB audio response into JSON with a truncation marker
// inside a base64 string.
func TestAScriptFetchThroughACredentialGetsTheWholeBody(t *testing.T) {
	big := `{"data":"` + strings.Repeat("A", 300*1024) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	if err := s.Save(SecureCredential{Name: "gen", Type: SecureCredNone, BaseURL: srv.URL}, ""); err != nil {
		t.Fatal(err)
	}
	piped, err := s.DispatchToolCallArgs(nil, "gen", map[string]any{"url": srv.URL + "/x", "__pipe_following": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(piped, big) || strings.Contains(piped, "truncated") {
		t.Errorf("the piped read should carry the whole %d-byte body unmarked", len(big))
	}
	plain, _ := s.DispatchToolCallArgs(nil, "gen", map[string]any{"url": srv.URL + "/x"})
	if strings.Contains(plain, big) {
		t.Error("a read for a model still stops at the general cap")
	}
	for _, want := range []string{
		`def fetch_url(self, url, method="GET", headers=None, body=None, timeout=30, save_to=None, request_headers=None):`,
		`def fetch_url(url, method="GET", headers=None, body=None, timeout=30, save_to=None, request_headers=None):`,
		`request_headers=request_headers)`,
	} {
		if !strings.Contains(SandboxHookPythonShim, want) {
			t.Errorf("fetch_url should take request_headers as fetch_via does; missing %q", want)
		}
	}
}

// A script's timeout reaches the credentialed call when it is longer than the
// general cap, bounded, and never shortens it. The call used the general cap
// whatever the script asked, so a slow generation could not finish from a
// script.
func TestAScriptTimeoutOnlyRaisesTheCallCap(t *testing.T) {
	base := int(secureAPIRequestTimeout() / time.Second)
	args := map[string]interface{}{}
	raiseCallTimeout(args, map[string]interface{}{"timeout": float64(base + 60)})
	if args[secureTimeoutArg] != base+60 {
		t.Errorf("a longer timeout should reach the call: %v", args[secureTimeoutArg])
	}
	args = map[string]interface{}{}
	raiseCallTimeout(args, map[string]interface{}{"timeout": float64(1)})
	if _, set := args[secureTimeoutArg]; set {
		t.Error("a shorter timeout must not cut the general cap")
	}
	args = map[string]interface{}{}
	raiseCallTimeout(args, map[string]interface{}{"timeout": float64(10000)})
	if args[secureTimeoutArg] != maxHookTimeoutSecs {
		t.Errorf("the timeout is bounded: %v", args[secureTimeoutArg])
	}
	if d := hookMethodDeadline("fetch_via", map[string]interface{}{"timeout": float64(200)}); d < 200*time.Second {
		t.Errorf("the hook's own deadline must outlast the call, got %s", d)
	}
	if !strings.Contains(SandboxHookPythonShim, `request_headers=None, timeout=None):`) {
		t.Error("fetch_via should take timeout=")
	}
}

// A timeout names the fix for a slow endpoint instead of prescribing retries,
// which only hit the same limit: a music generation timed out three times
// running, each retry told it was probably a blip.
func TestATimeoutSaysToRaiseTheWait(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
	}))
	defer srv.Close()
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	if err := s.Save(SecureCredential{Name: "slow", Type: SecureCredNone, BaseURL: srv.URL}, ""); err != nil {
		t.Fatal(err)
	}
	_, err := s.DispatchToolCallArgs(nil, "slow", map[string]any{"url": srv.URL + "/gen", secureTimeoutArg: 1})
	if err == nil {
		t.Fatal("a call past its wait limit should fail")
	}
	for _, want := range []string{"did not respond within 1s", "timeout_sec", "timeout=", "retry once"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the timeout should say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "once or twice") {
		t.Error("the old advice to keep retrying is gone")
	}
}

// A provider refusing the content gets its own hint: rephrase what is asked,
// not the request's shape. The shape hint told a model to "iterate the
// request", and it rephrased a refused idea a dozen times.
func TestAContentRefusalIsNotAShapeError(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	s := &SecureAPI{db: &DBase{Store: kvlite.MemStore()}}
	if err := s.Save(SecureCredential{Name: "gen", Type: SecureCredNone, BaseURL: srv.URL}, ""); err != nil {
		t.Fatal(err)
	}
	body = `{"error":{"code":"prohibited_content","message":"Input blocked: the prompt contains sensitive words"}}`
	out, _ := s.DispatchToolCallArgs(nil, "gen", map[string]any{"url": srv.URL + "/gen", "method": "POST", "body": "{}"})
	if !strings.Contains(out, "REFUSED THE CONTENT") || !strings.Contains(out, "describe the style") || strings.Contains(out, "PATH, QUERY PARAMS") {
		t.Errorf("a content refusal should get the rephrase hint, not the shape hint:\n%s", out)
	}
	body = `{"error":{"message":"Unknown name \"input\": Cannot find field."}}`
	out, _ = s.DispatchToolCallArgs(nil, "gen", map[string]any{"url": srv.URL + "/gen", "method": "POST", "body": "{}"})
	if strings.Contains(out, "REFUSED THE CONTENT") || !strings.Contains(out, "PATH, QUERY PARAMS") {
		t.Errorf("a real shape error keeps the shape hint:\n%s", out)
	}
}
