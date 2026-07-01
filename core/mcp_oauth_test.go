package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMCPPKCE(t *testing.T) {
	v, c := mcpPKCE()
	if v == "" || c == "" {
		t.Fatal("empty verifier or challenge")
	}
	sum := sha256.Sum256([]byte(v))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if c != want {
		t.Fatalf("challenge %q != S256(verifier) %q", c, want)
	}
	if s1, s2 := mcpRandomState(), mcpRandomState(); s1 == "" || s1 == s2 {
		t.Fatalf("state should be non-empty and vary: %q %q", s1, s2)
	}
}

func TestParseResourceMetadata(t *testing.T) {
	cases := map[string]string{
		`Bearer resource_metadata="https://rs.example/.well-known/oauth-protected-resource", error="x"`: "https://rs.example/.well-known/oauth-protected-resource",
		`Bearer resource_metadata=https://rs.example/prm`:                                               "https://rs.example/prm",
		`Bearer realm="x"`: "",
		``:                 "",
	}
	for header, want := range cases {
		if got := parseResourceMetadata(header); got != want {
			t.Errorf("parseResourceMetadata(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestMCPCanonicalResource(t *testing.T) {
	cases := map[string]string{
		"https://mcp.atlassian.com/v1/mcp":  "https://mcp.atlassian.com/v1/mcp",
		"https://mcp.example.com/":          "https://mcp.example.com",
		"https://mcp.example.com/mcp?x=1#f": "https://mcp.example.com/mcp",
	}
	for in, want := range cases {
		if got := mcpCanonicalResource(in); got != want {
			t.Errorf("mcpCanonicalResource(%q) = %q, want %q", in, got, want)
		}
	}
}

// newStubAS returns an authorization server serving RFC 8414 metadata,
// DCR, and a token endpoint that distinguishes the two grant types.
func newStubAS(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var base string
	srv := httptest.NewServer(mux)
	base = srv.URL
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 base,
			"authorization_endpoint": base + "/authorize",
			"token_endpoint":         base + "/token",
			"registration_endpoint":  base + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"client_id": "client-xyz"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code_verifier") == "" || r.Form.Get("resource") == "" {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "acc-1", "refresh_token": "ref-1", "expires_in": 3600,
			})
		case "refresh_token":
			// rotation-less refresh: no new refresh_token in the body.
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "acc-2", "expires_in": 3600,
			})
		default:
			w.WriteHeader(400)
		}
	})
	return srv
}

func TestMCPDiscover(t *testing.T) {
	as := newStubAS(t)
	defer as.Close()

	rsMux := http.NewServeMux()
	rs := httptest.NewServer(rsMux)
	defer rs.Close()
	rsMux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"resource":              rs.URL + "/mcp",
			"authorization_servers": []string{as.URL},
		})
	})
	rsMux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+rs.URL+`/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	})

	cfg, err := mcpDiscover(context.Background(), rs.URL+"/mcp")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if cfg.AuthEndpoint != as.URL+"/authorize" || cfg.TokenEndpoint != as.URL+"/token" || cfg.RegistrationEndpoint != as.URL+"/register" {
		t.Fatalf("endpoints wrong: %+v", cfg)
	}
	if cfg.Resource != rs.URL+"/mcp" {
		t.Fatalf("resource = %q, want %q", cfg.Resource, rs.URL+"/mcp")
	}
}

func TestMCPWellKnownPRM(t *testing.T) {
	// Path-aware (RFC 9728): well-known segment inserted before the path.
	if got := mcpWellKnownPRM("https://mcp.example.com/v1/mcp"); got != "https://mcp.example.com/.well-known/oauth-protected-resource/v1/mcp" {
		t.Errorf("path-aware = %q", got)
	}
	// Root variant ignores the path.
	if got := mcpWellKnownPRMRoot("https://mcp.example.com/v1/mcp"); got != "https://mcp.example.com/.well-known/oauth-protected-resource" {
		t.Errorf("root = %q", got)
	}
	// No path: both forms collapse to the same origin URL.
	if got := mcpWellKnownPRM("https://mcp.example.com"); got != "https://mcp.example.com/.well-known/oauth-protected-resource" {
		t.Errorf("no-path = %q", got)
	}
}

func TestMCPApplyResourceParam(t *testing.T) {
	// Audience set → send audience, drop resource (Auth0/Atlassian style).
	aud := url.Values{}
	mcpApplyResourceParam(aud, mcpOAuthConfig{Resource: "https://mcp.x/v1", Audience: "api.atlassian.com"})
	if aud.Get("audience") != "api.atlassian.com" || aud.Get("resource") != "" {
		t.Errorf("audience mode: %v", aud)
	}
	// No audience → send the RFC 8707 resource indicator (the MCP default).
	res := url.Values{}
	mcpApplyResourceParam(res, mcpOAuthConfig{Resource: "https://mcp.x/v1"})
	if res.Get("resource") != "https://mcp.x/v1" || res.Get("audience") != "" {
		t.Errorf("resource mode: %v", res)
	}
}

func TestMCPRegisterClient(t *testing.T) {
	as := newStubAS(t)
	defer as.Close()
	id, _, err := mcpRegisterClient(context.Background(), as.URL+"/register", "https://gohort.local/cb")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if id != "client-xyz" {
		t.Fatalf("client_id = %q", id)
	}
}

func TestMCPExchangeAndRefresh(t *testing.T) {
	as := newStubAS(t)
	defer as.Close()
	cfg := mcpOAuthConfig{ClientID: "client-xyz", TokenEndpoint: as.URL + "/token", Resource: "https://mcp.example/mcp"}

	tok, err := mcpExchangeCode(context.Background(), cfg, "the-code", "the-verifier", "https://gohort.local/cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if tok.AccessToken != "acc-1" || tok.RefreshToken != "ref-1" {
		t.Fatalf("token = %+v", tok)
	}
	if tok.Expiry.IsZero() {
		t.Fatal("expiry not set")
	}

	refreshed, err := mcpRefreshAccess(context.Background(), cfg, tok)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed.AccessToken != "acc-2" {
		t.Fatalf("refreshed access = %q", refreshed.AccessToken)
	}
	if refreshed.RefreshToken != "ref-1" { // carried forward (no rotation in stub)
		t.Fatalf("refresh token should carry forward, got %q", refreshed.RefreshToken)
	}
}

func TestMCPExchangeRequiresPKCEAndResource(t *testing.T) {
	as := newStubAS(t)
	defer as.Close()
	cfg := mcpOAuthConfig{ClientID: "client-xyz", TokenEndpoint: as.URL + "/token", Resource: ""}
	// Empty resource -> stub returns 400.
	if _, err := mcpExchangeCode(context.Background(), cfg, "c", "v", "https://gohort.local/cb"); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected 400 for missing resource, got %v", err)
	}
}
