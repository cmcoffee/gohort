package core

// One call through a credential, phase by phase: each check that refuses a
// call refuses it before anything is sent, the secret rides the request in
// the credential's own shape, and the reply the caller reads never carries it.

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const phaseSecret = "sekret-0123456789"

// phaseSeen is what the far end saw of the calls made to it.
type phaseSeen struct {
	mu   sync.Mutex
	hits int
	auth string
	key  string
	path string
}

func (p *phaseSeen) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits
}

// phaseServer answers every call with status and a body of ctype, in which
// {auth} is replaced by the Authorization header the call carried, so a reply
// that echoes the key can be checked for redaction.
func phaseServer(t *testing.T, seen *phaseSeen, status int, ctype, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.mu.Lock()
		seen.hits++
		seen.auth, seen.key, seen.path = r.Header.Get("Authorization"), r.Header.Get("X-Key"), r.URL.Path
		seen.mu.Unlock()
		w.Header().Set("Content-Type", ctype)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(strings.ReplaceAll(body, "{auth}", r.Header.Get("Authorization"))))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func phaseCred(srv *httptest.Server, typ string) SecureCredential {
	c := SecureCredential{Name: "phase_api", Type: typ, AllowedURLPattern: imageHostPattern(srv.URL)}
	if typ != SecureCredNone {
		c.inlineSecret = phaseSecret
	}
	return c
}

func TestARefusedCallSendsNothing(t *testing.T) {
	secureAPITestStore(t)
	var seen phaseSeen
	srv := phaseServer(t, &seen, 200, "application/json", `{}`)
	cases := []struct {
		name string
		cred func() SecureCredential
		args map[string]any
		sess *ToolSession
		want string
	}{
		{"disabled", func() SecureCredential { c := phaseCred(srv, SecureCredNone); c.Disabled = true; return c },
			map[string]any{"url": srv.URL + "/x"}, nil, `"phase_api"`},
		{"no url", func() SecureCredential { return phaseCred(srv, SecureCredNone) },
			map[string]any{}, nil, "url is required"},
		{"a path with no Base URL", func() SecureCredential { return phaseCred(srv, SecureCredNone) },
			map[string]any{"url": "/v1/x"}, nil, "no Base URL"},
		{"save_to with no workspace", func() SecureCredential { return phaseCred(srv, SecureCredNone) },
			map[string]any{"url": srv.URL + "/x", "save_to": "out.txt"}, nil, "save_to requires"},
		{"a method off the list", func() SecureCredential {
			c := phaseCred(srv, SecureCredNone)
			c.AllowedMethods = []string{"GET"}
			return c
		}, map[string]any{"url": srv.URL + "/x", "method": "POST"}, nil, "method POST not allowed"},
		{"a host off the list", func() SecureCredential { return phaseCred(srv, SecureCredNone) },
			map[string]any{"url": "https://elsewhere.invalid/x"}, nil, "is not allowed for credential"},
		{"a denied path", func() SecureCredential {
			c := phaseCred(srv, SecureCredNone)
			c.DeniedURLPatterns = []string{strings.TrimSuffix(imageHostPattern(srv.URL), "**") + "billing/**"}
			return c
		}, map[string]any{"url": srv.URL + "/billing/x"}, nil, "deny pattern"},
		{"no stored secret", func() SecureCredential {
			c := phaseCred(srv, SecureCredBearer)
			c.inlineSecret = ""
			return c
		}, map[string]any{"url": srv.URL + "/x"}, nil, "no stored secret"},
		// Refused before the call, not cancelled during it: the two read
		// differently, and only one says nothing was sent.
		{"Private mode", func() SecureCredential { return phaseCred(srv, SecureCredNone) },
			map[string]any{"url": srv.URL + "/x"}, &ToolSession{Network: NewNetworkConnector(true)}, "was NOT attempted"},
		{"a request that cannot be built", func() SecureCredential { return phaseCred(srv, SecureCredNone) },
			map[string]any{"url": srv.URL + "/x", "method": "GE T"}, nil, "build request"},
		{"basic auth with no username", func() SecureCredential { return phaseCred(srv, SecureCredBasicAuth) },
			map[string]any{"url": srv.URL + "/x"}, nil, "basic_auth needs a username"},
		{"a type nobody knows", func() SecureCredential { return phaseCred(srv, "carrier_pigeon") },
			map[string]any{"url": srv.URL + "/x"}, nil, "unknown credential type"},
	}
	for _, tc := range cases {
		before := seen.count()
		out, err := Secure().dispatch(tc.cred(), tc.args, tc.sess)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error naming %q, got %q / %v", tc.name, tc.want, out, err)
		}
		if seen.count() != before {
			t.Errorf("%s: the call reached the server", tc.name)
		}
	}
}

// A path-only url takes the credential's Base URL.
func TestAPathOnlyURLGoesToTheBaseURL(t *testing.T) {
	secureAPITestStore(t)
	var seen phaseSeen
	srv := phaseServer(t, &seen, 200, "application/json", `{}`)
	c := phaseCred(srv, SecureCredNone)
	c.BaseURL = srv.URL
	if _, err := Secure().dispatch(c, map[string]any{"url": "/v1/pages"}, nil); err != nil {
		t.Fatal(err)
	}
	if seen.path != "/v1/pages" {
		t.Errorf("the call went to %q", seen.path)
	}
}

func TestTheSecretRidesTheRequestAndNeverTheReply(t *testing.T) {
	secureAPITestStore(t)
	var seen phaseSeen
	srv := phaseServer(t, &seen, 200, "application/json", `{"saw":"{auth}"}`)
	basic := base64.StdEncoding.EncodeToString([]byte("robot:" + phaseSecret))
	for _, tc := range []struct {
		typ, auth, key string
	}{
		{SecureCredBearer, "Bearer " + phaseSecret, ""},
		{SecureCredBasicAuth, "Basic " + basic, ""},
		{SecureCredHeader, "", phaseSecret},
	} {
		c := phaseCred(srv, tc.typ)
		c.Username, c.ParamName = "robot", "X-Key"
		out, err := Secure().dispatch(c, map[string]any{"url": srv.URL + "/x"}, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		if seen.auth != tc.auth || seen.key != tc.key {
			t.Errorf("%s: the server saw Authorization %q, X-Key %q", tc.typ, seen.auth, seen.key)
		}
		if !strings.HasPrefix(out, "HTTP 200 OK\n") {
			t.Errorf("%s: the reply does not lead with its status: %q", tc.typ, out)
		}
		if strings.Contains(out, phaseSecret) || strings.Contains(out, basic) {
			t.Errorf("%s: the reply carries the secret: %q", tc.typ, out)
		}
		if tc.auth != "" && !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s: the echoed key is not marked redacted: %q", tc.typ, out)
		}
	}
	if audit := Secure().LoadAudit("", "phase_api"); len(audit) != 3 || audit[0].Status != 200 {
		t.Errorf("each call should be in the ledger with its status: %+v", audit)
	}
}

// save_to writes the reply to the workspace and says where; a text reply is
// redacted there too.
func TestSaveToWritesTheReplyToTheWorkspace(t *testing.T) {
	secureAPITestStore(t)
	var seen phaseSeen
	srv := phaseServer(t, &seen, 200, "text/plain", "key was {auth}")
	dir := t.TempDir()
	out, err := Secure().dispatch(phaseCred(srv, SecureCredBearer),
		map[string]any{"url": srv.URL + "/x", "save_to": "reply.txt"}, &ToolSession{WorkspaceDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "saved") || !strings.Contains(out, "reply.txt") {
		t.Errorf("the reply should say where it was saved: %q", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "reply.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), phaseSecret) || !strings.Contains(string(got), "[REDACTED]") {
		t.Errorf("the saved text carries the key: %q", got)
	}
}

// A web page on an error status is named for what it is, not dumped.
func TestAnHTMLErrorPageIsNamedNotDumped(t *testing.T) {
	secureAPITestStore(t)
	var seen phaseSeen
	srv := phaseServer(t, &seen, 404, "text/html", "<!DOCTYPE html><html><script>x</script></html>")
	out, err := Secure().dispatch(phaseCred(srv, SecureCredNone), map[string]any{"url": srv.URL + "/x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "HTTP 404 Not Found\n") || !strings.Contains(out, secureAPIHTMLPageNote) || strings.Contains(out, "<script>") {
		t.Errorf("an HTML error page should be replaced by the note: %q", out)
	}
}

// A 401 on a connected account drops the access token and keeps the refresh
// token, so this call fails but the next one mints a fresh token instead of
// sending the rejected one again.
func TestARejectedTokenIsDroppedForTheNextCall(t *testing.T) {
	secureAPITestStore(t)
	var seen phaseSeen
	srv := phaseServer(t, &seen, 401, "application/json", `{"error":"expired"}`)
	c := SecureCredential{Name: "phase_oauth", Type: SecureCredOAuth2, Grant: "authorization_code", AllowedURLPattern: imageHostPattern(srv.URL)}
	if err := Secure().SaveUserToken("phase_oauth", "alice", CredOAuthToken{AccessToken: phaseSecret, RefreshToken: "refresh-1", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := Secure().dispatch(c, map[string]any{"url": srv.URL + "/x"}, &ToolSession{Username: "alice"}); err != nil {
		t.Fatal(err)
	}
	if seen.auth != "Bearer "+phaseSecret {
		t.Fatalf("the call did not carry alice's token: %q", seen.auth)
	}
	tok, ok := Secure().loadUserTokenRaw("phase_oauth", "alice")
	if !ok || tok.AccessToken != "" || tok.RefreshToken != "refresh-1" {
		t.Errorf("after a 401 the access token should be gone and the refresh token kept: %+v", tok)
	}
}
