package main

// The local proxy listens on 127.0.0.1 and adds the user's gohort session and
// bridge key to whatever it forwards. Loopback is not a boundary: any process
// on the machine, any other local account, and a web page that finds the port
// (or rebinds a hostname to 127.0.0.1) could reach it, rewrite the server URL
// the bridge daemon trusts, and borrow the signed-in session.
//
// So the proxy answers only its own window. At launch a random secret is
// minted; the window's first navigation carries it once to /__desktop/boot,
// which sets it as a strict same-site cookie, and every later request must
// present that cookie. The Host header must be the loopback address itself,
// which is what defeats a rebound hostname.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

const (
	launchCookieName = "gohort_desktop_launch"
	launchBootPath   = "/__desktop/boot"
)

func newLaunchSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		core.Fatal("gohort-desktop: no randomness for the launch secret: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// launchBootURL is where the window's first navigation goes: the boot path
// with the secret, then on to next.
func launchBootURL(localBase, secret, next string) string {
	return localBase + launchBootPath + "?t=" + url.QueryEscape(secret) + "&next=" + url.QueryEscape(next)
}

// guardLaunch wraps the proxy so it serves only the window that holds the
// launch secret.
func guardLaunch(next http.Handler, secret string, port int) http.Handler {
	hosts := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", port): true,
		fmt.Sprintf("localhost:%d", port): true,
	}
	same := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hosts[strings.ToLower(r.Host)] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.URL.Path == launchBootPath {
			if !same(r.URL.Query().Get("t"), secret) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: launchCookieName, Value: secret, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
			dest := r.URL.Query().Get("next")
			if !strings.HasPrefix(dest, "/") || strings.HasPrefix(dest, "//") || strings.HasPrefix(dest, "/\\") {
				dest = "/"
			}
			http.Redirect(w, r, dest, http.StatusFound)
			return
		}
		c, err := r.Cookie(launchCookieName)
		if err != nil || !same(c.Value, secret) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			http.Error(w, "This address belongs to the Gohort desktop app and answers only its own window.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
