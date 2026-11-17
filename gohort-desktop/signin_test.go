package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// The window's "sign in with Microsoft" signs in through the person's
// browser: the app starts a sign-in with the hash of a secret it keeps,
// opens the browser on the confirm page, waits while the person has not
// confirmed, and once they have, claims it with the secret and keeps the
// session in its jar. A sign-in that ended says so instead of waiting on.
func TestTheWindowSignsInThroughTheBrowser(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	var mu sync.Mutex
	var challenge string
	confirmed, ended, claims := false, false, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		switch r.URL.Path {
		case "/api/desktop/signin/start":
			challenge = in["challenge"]
			w.Write([]byte(`{"code":"c0de","phrase":"K7QM-3XRD","url":"/desktop/signin?code=c0de"}`))
		case "/api/desktop/signin/claim":
			claims++
			sum := sha256.Sum256([]byte(in["verifier"]))
			switch {
			case in["code"] != "c0de" || hex.EncodeToString(sum[:]) != challenge || ended:
				w.WriteHeader(http.StatusGone)
			case !confirmed:
				w.WriteHeader(http.StatusAccepted)
			default:
				w.Write([]byte(`{"cookie":{"name":"gohort_session","value":"tok-1","path":"/","max_age":3600}}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv(core.ENV_GOHORT_ADDR, srv.URL)
	cfg, err := core.LoadConfigNamed("signin_test.db")
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := core.NewPersistentCookieJar(cfg.Settings())
	gp := &gohort_proxy{cfg: cfg, cookies: jar}

	w := httptest.NewRecorder()
	gp.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/login/sso", nil))
	if b := w.Body.String(); !strings.Contains(b, "K7QM-3XRD") || !strings.Contains(b, srv.URL+"/desktop/signin?code=c0de") {
		t.Fatalf("the window's sign in with Microsoft did not start a browser sign-in:\n%s", b)
	}
	if len(challenge) != 64 {
		t.Fatalf("the app sent no challenge: %q", challenge)
	}
	poll := func() string {
		w := httptest.NewRecorder()
		gp.ServeHTTP(w, httptest.NewRequest(http.MethodPost, SIGNIN_POLL_PATH, nil))
		var out map[string]string
		json.Unmarshal(w.Body.Bytes(), &out)
		return out["state"]
	}
	if got := poll(); got != "waiting" {
		t.Errorf("before the person confirmed: %q, want waiting", got)
	}
	u, _ := url.Parse(srv.URL)
	if len(jar.Cookies(u)) != 0 {
		t.Error("a session arrived before the person confirmed")
	}
	mu.Lock()
	confirmed = true
	mu.Unlock()
	if got := poll(); got != "done" {
		t.Errorf("once confirmed: %q, want done", got)
	}
	if c := jar.Cookies(u); len(c) != 1 || c[0].Name != "gohort_session" || c[0].Value != "tok-1" {
		t.Errorf("the session was not kept in the jar: %v", c)
	}
	if got := poll(); got != "ended" {
		t.Errorf("after it finished the app asked again: %q", got)
	}

	// A sign-in the server ended (cancelled, expired) says so.
	gp.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, SIGNIN_PATH, nil))
	mu.Lock()
	ended = true
	mu.Unlock()
	if got := poll(); got != "ended" {
		t.Errorf("a sign-in the server ended: %q, want ended", got)
	}
}
