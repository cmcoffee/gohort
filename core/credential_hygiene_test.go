// Three habits, one theme: a secret should not be readable from a log, should
// not be comparable a byte at a time, and should not last forever by default.
package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// --- F-05: the deployment key must not reach the access log -----------------

func TestAccessLogRedactsSecretQueryParams(t *testing.T) {
	// The access line deliberately keeps the query string, because the params
	// are usually what a 404 hinges on. That is also how the deployment-wide
	// API key — a blanket auth bypass — was being written to a file in
	// plaintext on every use.
	cases := []struct{ raw, want string }{
		{"key=s3cret", "key=REDACTED"},
		{"agent_id=abc&key=s3cret", "agent_id=abc&key=REDACTED"},
		{"KEY=s3cret", "KEY=REDACTED"},
		{"token=abc&session_id=keep", "token=REDACTED&session_id=keep"},
		{"api_key=a&access_token=b&secret=c", "api_key=REDACTED&access_token=REDACTED&secret=REDACTED"},
		// Untouched: these are the params the line exists for.
		{"agent_id=abc&format=json", "agent_id=abc&format=json"},
		{"", ""},
		// A name that merely contains "key" is not a secret.
		{"monkey=banana", "monkey=banana"},
	}
	for _, c := range cases {
		if got := redactQuerySecrets(c.raw); got != c.want {
			t.Errorf("redactQuerySecrets(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestAccessLogLineNeverCarriesTheKey(t *testing.T) {
	// The line the middleware actually emits, not just the helper it should be
	// calling. A test of redactQuerySecrets alone keeps passing if the log
	// line stops using it, which is exactly the regression worth catching.
	r := httptest.NewRequest("GET", "/api/live?key=s3cret-master-key&agent_id=abc", nil)
	line := accessLogPath(r)
	if strings.Contains(line, "s3cret-master-key") {
		t.Fatalf("the deployment key reached the access line: %q", line)
	}
	if !strings.Contains(line, "agent_id=abc") {
		t.Errorf("redaction ate a diagnostic param: %q", line)
	}
	if !strings.Contains(line, "/api/live") {
		t.Errorf("the path went missing: %q", line)
	}
	// No query at all stays untouched.
	if got := accessLogPath(httptest.NewRequest("GET", "/api/live", nil)); got != "/api/live" {
		t.Errorf("bare path changed: %q", got)
	}
}

func TestRedactionSurvivesAMalformedQuery(t *testing.T) {
	// Logged as close to what arrived as possible: the malformation is often
	// the thing being diagnosed.
	for _, raw := range []string{"=noname", "flag", "&&", "key", "key="} {
		if got := redactQuerySecrets(raw); strings.Contains(got, "REDACTED") && raw != "key=" {
			t.Errorf("redactQuerySecrets(%q) = %q — redacted something that is not a value", raw, got)
		}
	}
}

// --- F-11: constant-time comparisons ----------------------------------------

func TestEventMonitorTokenLookupIsExactAndScoped(t *testing.T) {
	// The token is the only thing guarding /api/operator/event/<token>, which
	// wakes an agent. Constant-time now; the behaviour must be unchanged.
	db := &DBase{Store: kvlite.MemStore()}
	SaveEventMonitor(db, EventMonitor{Owner: "craig", Name: "watch", Kind: EventKindWebhook, Token: "tok-abc"})
	SaveEventMonitor(db, EventMonitor{Owner: "dana", Name: "watch", Kind: EventKindWebhook, Token: "tok-xyz"})

	if m, ok := FindEventMonitorByToken(db, "tok-abc"); !ok || m.Owner != "craig" {
		t.Errorf("exact token did not resolve: %+v ok=%v", m, ok)
	}
	for _, bad := range []string{"", "tok-ab", "tok-abcd", "TOK-ABC", "nope"} {
		if _, ok := FindEventMonitorByToken(db, bad); ok {
			t.Errorf("token %q was accepted", bad)
		}
	}
}

// --- F-14: account tokens can expire, and record being used -----------------

func acctFixture(t *testing.T) {
	t.Helper()
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
}

func TestTokenWithNoExpiryStillWorks(t *testing.T) {
	// Every key minted before the field existed has an empty Expires, and
	// reading that as "expired" would log people out of integrations with no
	// warning.
	acctFixture(t)
	tok := MintAccountTokenScoped("craig", "legacy", &TokenScope{})
	if tok.Expires != "" {
		t.Fatalf("a key minted with no ttl should carry no deadline, got %q", tok.Expires)
	}
	if owner, ok := lookupAccountTokenOwner(tok.Token); !ok || owner != "craig" {
		t.Error("a key with no deadline stopped working")
	}
}

func TestExpiredTokenStopsAuthenticating(t *testing.T) {
	acctFixture(t)
	tok := MintAccountTokenExpiring("craig", "short", &TokenScope{}, time.Hour)
	if _, ok := lookupAccountTokenOwner(tok.Token); !ok {
		t.Fatal("fixture: the key should start valid")
	}

	// Move its deadline into the past.
	var stored AccountToken
	RootDB.Get(accountTokenTable, tok.Token, &stored)
	stored.Expires = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	RootDB.Set(accountTokenTable, tok.Token, stored)

	if _, ok := lookupAccountTokenOwner(tok.Token); ok {
		t.Error("an expired key still authenticates")
	}
	if AccountTokenFromRequest(reqWithKey(tok.Token)) != nil {
		t.Error("an expired key still resolves to a scope record")
	}
	// Removed on sight, so a sweep that has not run is never the difference
	// between valid and not.
	if RootDB.Get(accountTokenTable, tok.Token, &stored) {
		t.Error("the expired key was left in the store")
	}
}

func TestUnparseableDeadlineFailsClosed(t *testing.T) {
	tok := &AccountToken{Expires: "not-a-date"}
	if !tok.Expired() {
		t.Error("a corrupt deadline must read as expired, not as immortal")
	}
}

func TestUsingATokenRecordsIt(t *testing.T) {
	// Nothing wrote LastSeen before, so every key read as never-used and the
	// question that makes expiry actionable had no answer.
	acctFixture(t)
	tok := MintAccountTokenScoped("craig", "laptop", &TokenScope{})

	var before AccountToken
	RootDB.Get(accountTokenTable, tok.Token, &before)
	if before.LastSeen != "" {
		t.Fatal("fixture: a fresh key should not look used")
	}

	lookupAccountTokenOwner(tok.Token)

	var after AccountToken
	RootDB.Get(accountTokenTable, tok.Token, &after)
	if after.LastSeen == "" {
		t.Fatal("using a key did not record it")
	}
	// Second use inside the interval must not write again — the whole point of
	// the lazy write is that authentication stays a single Get.
	stamp := after.LastSeen
	lookupAccountTokenOwner(tok.Token)
	RootDB.Get(accountTokenTable, tok.Token, &after)
	if after.LastSeen != stamp {
		t.Error("LastSeen was rewritten on a second use inside the interval")
	}
}

func TestExpirySweepIsOwnerSafe(t *testing.T) {
	acctFixture(t)
	live := MintAccountTokenScoped("craig", "live", &TokenScope{})
	dead := MintAccountTokenExpiring("craig", "dead", &TokenScope{}, time.Hour)

	var d AccountToken
	RootDB.Get(accountTokenTable, dead.Token, &d)
	d.Expires = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	RootDB.Set(accountTokenTable, dead.Token, d)

	if n := SweepExpiredAccountTokens(); n != 1 {
		t.Fatalf("expected 1 key swept, got %d", n)
	}
	if _, ok := lookupAccountTokenOwner(live.Token); !ok {
		t.Error("the sweep took a key that had not expired")
	}
}

func TestSetExpiryIsOwnerScoped(t *testing.T) {
	acctFixture(t)
	tok := MintAccountTokenScoped("craig", "laptop", &TokenScope{})

	if SetAccountTokenExpiry("dana", tok.ID, time.Hour) {
		t.Error("dana re-dated craig's key")
	}
	if !SetAccountTokenExpiry("craig", tok.ID, 30*24*time.Hour) {
		t.Fatal("the owner could not set a deadline")
	}
	var stored AccountToken
	RootDB.Get(accountTokenTable, tok.Token, &stored)
	if stored.Expires == "" {
		t.Fatal("the deadline was not stored")
	}
	// Clearing goes back to permanent. Read into a FRESH struct: kvlite
	// encodes through gob, which omits zero values, so decoding a cleared
	// field into a reused struct leaves the previous value sitting there.
	if !SetAccountTokenExpiry("craig", tok.ID, 0) {
		t.Fatal("the owner could not clear the deadline")
	}
	var cleared AccountToken
	RootDB.Get(accountTokenTable, tok.Token, &cleared)
	if cleared.Expires != "" {
		t.Errorf("clearing left a deadline: %q", cleared.Expires)
	}
	if cleared.Expired() {
		t.Error("a cleared deadline still reads as expired")
	}
}

func reqWithKey(secret string) *http.Request {
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.Header.Set("X-API-Key", secret)
	return r
}
