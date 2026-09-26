package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		`def fetch_via(self, credential, url, method="GET", body=None, headers=None, request_headers=None):`,
		`"headers": hdrs,`,
		`def fetch_via(credential, url, method="GET", body=None, headers=None, request_headers=None):`,
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
