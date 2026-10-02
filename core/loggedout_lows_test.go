package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// Public means public in the router's reading too: an escaped slash or a dot
// segment cannot carry a request from a public prefix to somewhere else.
func TestAPublicPrefixCannotBeWalkedOutOf(t *testing.T) {
	RegisterPublicPath("/pubtest/")
	for _, target := range []string{"/pubtest%2f..%2fsecret", "/pubtest/%2e%2e/admin", "/pubtest/./x"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.Path, r.URL.RawPath = strings.ReplaceAll(strings.ReplaceAll(target, "%2f", "/"), "%2e", "."), target
		if isPublicRequest(r) {
			t.Errorf("%s passed as public", target)
		}
	}
	if !isPublicRequest(httptest.NewRequest(http.MethodGet, "/pubtest/hook", nil)) {
		t.Error("a plain public path was refused")
	}
}

// A reset token is stored under its hash; an older raw record still works and
// moves; a stored key presented as a token does not; setting the password
// ends every other outstanding link.
func TestResetTokensAreHashedAndEndWithAPasswordChange(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	tok := createResetToken(db, "alice")
	for _, k := range db.Keys(AuthResetTable) {
		if strings.Contains(k, tok) {
			t.Fatal("the token is a key name")
		}
		if _, ok := validateResetToken(db, k); ok {
			t.Error("a stored key worked as a token")
		}
	}
	if u, ok := validateResetToken(db, tok); !ok || u != "alice" {
		t.Fatal("a fresh token does not validate")
	}
	db.Set(AuthResetTable, "legacyraw", resetToken{Username: "alice", Expires: 1 << 40})
	if _, ok := validateResetToken(db, "legacyraw"); !ok {
		t.Fatal("a token issued before hashing stopped working")
	}
	AuthSetUser(db, "alice", "old-password", false)
	other := createResetToken(db, "alice")
	AuthAdminSetPassword(db, "alice", "new-password")
	if _, ok := validateResetToken(db, other); ok {
		t.Error("a reset link outlived the password being set")
	}
}

// With no accounts, only a Host a third-party site cannot point here is served.
func TestHostsThatCannotBeRebound(t *testing.T) {
	for _, h := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost", "gohort:8080", "nas.local", "box.lan:9000", "10.0.0.5"} {
		if !hostCannotBeRebound(h) {
			t.Errorf("%s refused", h)
		}
	}
	for _, h := range []string{"attacker.example", "rebind.attacker.example:8080"} {
		if hostCannotBeRebound(h) {
			t.Errorf("%s accepted", h)
		}
	}
}

// A renewal does not write back a session that was revoked meanwhile.
func TestARenewalDoesNotReviveARevokedSession(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	if saveRenewedAuthSession(db, "gone-token", authSession{User: "alice", Expires: 1 << 40}) {
		t.Error("a renewal wrote a session that no longer exists")
	}
	var cur authSession
	if db.Get(AuthSessionTable, sessionStoreKey("gone-token"), &cur) {
		t.Error("the revoked session is back in the store")
	}
}
