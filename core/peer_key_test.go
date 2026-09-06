package core

import (
	"encoding/json"
	"github.com/cmcoffee/snugforge/kvlite"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// --- the exec audit trail ----------------------------------------------------

// TestTheExecCommandIsAudited — the widest grant in the system ran a shell on a
// named appliance and left no record at all. Minting a key logged, an
// investigate question logged, the command actually EXECUTED did not, so after
// an incident there was nothing to read.
func TestTheExecCommandIsAudited(t *testing.T) {
	src, err := os.ReadFile("peer_investigate.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func HandlePeerExec")
	if i < 0 {
		t.Fatal("HandlePeerExec has moved")
	}
	h := body[i:]
	if j := strings.Index(h, "\nfunc "); j > 0 {
		h = h[:j]
	}
	// The command, the key and the appliance — a line naming fewer than all
	// three cannot answer "who ran what, where".
	if !strings.Contains(h, `Log("[peer] %s EXEC on %s: %s"`) {
		t.Error("the executed command is not logged before it runs")
	}
	for _, want := range []string{"k.Label", "truncateForAudit(cmd)"} {
		if !strings.Contains(h, want) {
			t.Errorf("the audit line does not carry %s", want)
		}
	}
	// Logged BEFORE the run: a command that hangs or kills the process is
	// exactly the one worth a record, and an after-only log loses those.
	if strings.Index(h, "Log(\"[peer] %s EXEC on %s: %s\"") > strings.Index(h, "PeerExecFunc(ctx") {
		t.Error("the command is logged only after it runs — a hang or a crash leaves no trace")
	}
	if !strings.Contains(h, "EXEC on %s failed after") || !strings.Contains(h, "EXEC on %s ok in") {
		t.Error("the outcome is not logged, so the trail says what was asked and never what happened")
	}
}

// TestAuditLinesCannotBeForged — an audit trail an attacker can write entries
// into is worse than none. A multi-line command would otherwise split the
// record, and a crafted later line could impersonate a separate entry.
func TestAuditLinesCannotBeForged(t *testing.T) {
	got := truncateForAudit("echo one\nLog: [peer] trusted EXEC on box: rm -rf /\r\necho two")
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("a newline survived into the audit line: %q", got)
	}
	if !strings.Contains(got, `\n`) {
		t.Errorf("the newline was dropped rather than escaped, losing what was actually run: %q", got)
	}
	// Long commands are bounded, and say they were bounded.
	long := strings.Repeat("x", auditCommandMax*3)
	out := truncateForAudit(long)
	if len(out) > auditCommandMax+64 {
		t.Errorf("a long command was not bounded: %d chars", len(out))
	}
	if !strings.Contains(out, "bytes total") {
		t.Errorf("truncation is silent, so the log reads as the whole command: %q", out[len(out)-40:])
	}
	// A short command survives whole — the common case must stay readable.
	if truncateForAudit("systemctl status nginx") != "systemctl status nginx" {
		t.Error("an ordinary command was altered")
	}
}

// --- the pre-auth throttle ---------------------------------------------------

func throttleReq(addr, key string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
	r.RemoteAddr = addr
	if key != "" {
		r.Header.Set(peerKeyHeader, key)
	}
	return r
}

// TestFailedAuthIsThrottledPerSource — LookupPeerKey reads every issued key
// before it can decide, and the per-key rate limit only applies once a key is
// RECOGNIZED. So an unauthenticated caller paid nothing and cost a full table
// read, without limit.
func TestFailedAuthIsThrottledPerSource(t *testing.T) {
	peerAuthFailMu.Lock()
	peerAuthFails = map[string]*peerFailWindow{}
	peerAuthFailMu.Unlock()

	bad := throttleReq("198.51.100.7:5555", "not-a-real-key")
	if peerSourceThrottled(bad) {
		t.Fatal("a source is throttled before it has failed anything")
	}
	for i := 0; i < peerAuthFailMax; i++ {
		peerNoteAuthFailure(bad)
	}
	if !peerSourceThrottled(bad) {
		t.Error("a source that spent its whole failure allowance is still admitted to the lookup")
	}
	// Another address is unaffected — the limit is per source, not global, or
	// one bad actor would lock every peer out.
	if peerSourceThrottled(throttleReq("203.0.113.9:4444", "x")) {
		t.Error("throttling one source throttled a different one")
	}
}

// TestTheThrottleCountsFailuresNotRequests — a busy legitimate peer makes
// hundreds of calls a minute from one address. Counting requests would cut it
// off; counting failures is what lets the ceiling be low enough to matter.
func TestTheThrottleCountsFailuresNotRequests(t *testing.T) {
	peerAuthFailMu.Lock()
	peerAuthFails = map[string]*peerFailWindow{}
	peerAuthFailMu.Unlock()

	good := throttleReq("198.51.100.8:6666", "valid")
	for i := 0; i < peerAuthFailMax*5; i++ {
		if peerSourceThrottled(good) {
			t.Fatalf("a source that never failed was throttled after %d successful checks", i)
		}
	}
}

// TestTheSourceIsTheConnectionNotAHeader — X-Forwarded-For is caller-controlled,
// so honoring it would let one source present a fresh identity per request and
// walk straight around the limit.
func TestTheSourceIsTheConnectionNotAHeader(t *testing.T) {
	r := throttleReq("198.51.100.9:7777", "x")
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	if got := peerRequestSource(r); got != "198.51.100.9" {
		t.Errorf("source = %q, want the TCP address — a header would be spoofable", got)
	}
	// A bare address (no port) still yields something rather than nothing:
	// returning "" would silently disable the throttle.
	r.RemoteAddr = "198.51.100.10"
	if peerRequestSource(r) == "" {
		t.Error("an unparseable RemoteAddr disabled the throttle instead of degrading")
	}
}

// TestEveryAuthEntryPointIsThrottled — a source sweep, because the defect
// returns the moment a third entry point authenticates on its own.
//
// The manifest already does: it is not gated on one capability, so it calls
// peerFromRequest directly rather than going through peerAuthorize. An
// unthrottled endpoint would simply be the one an attacker used.
func TestEveryAuthEntryPointIsThrottled(t *testing.T) {
	files, _ := regexpGlob(t, `peer_.*\.go$`)
	call := regexp.MustCompile(`peerFromRequest\(r\)`)
	found := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if !call.MatchString(line) {
				continue
			}
			found++
			throttled := false
			for back := i; back >= 0 && back > i-12; back-- {
				if strings.Contains(lines[back], "peerSourceThrottled(r)") {
					throttled = true
					break
				}
			}
			if !throttled {
				t.Errorf("%s:%d authenticates a peer without checking peerSourceThrottled first — "+
					"an unauthenticated caller can spend a full key-table read here, unbounded", f, i+1)
			}
		}
	}
	if found == 0 {
		t.Fatal("found no peer authentication sites — the sweep is no longer looking where they live")
	}
}

func regexpGlob(t *testing.T, pattern string) ([]string, error) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		if re.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Re-granting an EXISTING key.
//
// Until this existed the admin UI offered only Enable/Disable and Delete, so
// widening a grant meant deleting the key and minting a new one — which changes
// the secret, and therefore has to be re-pasted and re-probed on the other
// machine. That made shipping a new capability something no existing peer could
// pick up without an outage on both ends.

func TestReGrantingKeepsTheSecret(t *testing.T) {
	peerImageDB(t)
	pk, err := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	got, err := SetPeerKeyCaps(pk.ID, []string{PeerCapEmbeddings, PeerCapTranscribe})
	if err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	if got.Key != pk.Key {
		t.Error("the secret changed — every peer holding it would have to be re-pasted, which is the whole reason this exists")
	}
	if !got.Allows(PeerCapTranscribe) {
		t.Error("the new capability was not granted")
	}
	if !got.Allows(PeerCapEmbeddings) {
		t.Error("the existing capability was dropped")
	}
	// And it is durable, not just returned.
	stored, ok := LookupPeerKey(pk.Key)
	if !ok {
		t.Fatal("the key no longer authenticates")
	}
	if !stored.Allows(PeerCapTranscribe) {
		t.Error("the re-grant was not persisted")
	}
}

// Narrowing needs no separate revocation — Allows reads the list every call.
func TestReGrantingCanNarrow(t *testing.T) {
	peerImageDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings, PeerCapImages, PeerCapTranscribe}, 0)

	got, err := SetPeerKeyCaps(pk.ID, []string{PeerCapEmbeddings})
	if err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	if got.Allows(PeerCapImages) || got.Allows(PeerCapTranscribe) {
		t.Errorf("removed capabilities are still granted: %v", got.Caps)
	}
}

// A grant mint would refuse must not be settable through an edit — the two
// share one validator so they cannot drift.
func TestReGrantingValidatesLikeMinting(t *testing.T) {
	peerImageDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	if _, err := SetPeerKeyCaps(pk.ID, []string{"root"}); err == nil {
		t.Error("an unknown capability was accepted")
	} else if !strings.Contains(err.Error(), "unknown capability") {
		t.Errorf("error should name the problem: %v", err)
	}
	if _, err := SetPeerKeyCaps(pk.ID, nil); err == nil {
		t.Error("a key with no capabilities was accepted — it can authenticate but do nothing")
	}
	// The original grant survives a rejected edit.
	if stored, _ := LookupPeerKey(pk.Key); !stored.Allows(PeerCapEmbeddings) {
		t.Error("a rejected re-grant damaged the existing key")
	}
	if _, err := SetPeerKeyCaps("no-such-id", []string{PeerCapEmbeddings}); err == nil {
		t.Error("re-granting an unknown key succeeded")
	}
}

// anUnservedCap returns a capability this build declares but does not serve.
//
// Tests that need "an unbuilt capability" pick it from the predicate rather
// than naming one: images was that example until it shipped, at which point
// three tests asserted the opposite of the truth. Chosen dynamically, they keep
// testing the PROPERTY as capabilities land.
func anUnservedCap(t *testing.T) string {
	t.Helper()
	for _, c := range PeerCapabilities() {
		if !PeerCapabilityServed(c) {
			return c
		}
	}
	t.Skip("every declared capability is served — nothing left to test the unbuilt path with")
	return ""
}

// peerTestDB points RootDB at a fresh store and restores it afterwards, since
// the peer key store is process-global.
func peerTestDB(t *testing.T) {
	t.Helper()
	prev := RootDB
	t.Cleanup(func() { RootDB = prev })
	RootDB = &DBase{Store: kvlite.MemStore()}
	// The consuming side's credential cache is process-global and keyed by peer
	// NAME, and every test here calls its peer "gpu-box". Swapping the store
	// under it leaves a token minted against a database that no longer exists,
	// which the next test presents and the server correctly does not recognize.
	// In production nothing swaps RootDB, so this is test hygiene rather than a
	// missing invalidation — but it is exactly the failure a shared cache keyed
	// by a name rather than by a store produces.
	resetPeerTokenCache()
	InvalidatePeerResolution()
}

// A peer key must never satisfy ordinary user authentication. The tempting
// implementation registers it with RegisterAPIKeyValidator next to the desktop
// key — and a validator there returns a USERNAME, which userFromAPIKey turns
// into that user everywhere a request is authenticated. "You may use my GPU"
// would silently become "you may read my mail".
//
// This pins the separation at the seam where it would be lost.
func TestPeerKeyIsNotAUserCredential(t *testing.T) {
	peerTestDB(t)
	pk, err := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/anything", nil)
	r.Header.Set("X-API-Key", pk.Key)
	if user := APIKeyUser(r); user != "" {
		t.Fatalf("a peer key resolved to user %q — it must not authenticate as a person", user)
	}
	// And the credential it BUYS still works as a peer credential, which is the
	// whole point. The key itself authenticates nothing on either axis now: not
	// as a person, and not as a peer.
	r2 := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
	r2.Header.Set(peerKeyHeader, peerAuth(t, pk))
	if _, ok := peerFromRequest(r2); !ok {
		t.Error("an access token did not authenticate as a peer")
	}
}

// peerAuth is the credential a peer actually presents: an ACCESS TOKEN.
//
// A key authenticates nothing (see peer_token.go) — it is a pairing code, spent
// once. Tests that exercise a capability endpoint want a paired peer, not the
// pairing, so this mints the token pair directly rather than driving the HTTP
// exchange. Callable more than once for the same grant, which the exchange
// deliberately is not.
func peerAuth(t *testing.T, k PeerKey) string {
	t.Helper()
	pair, err := mintPeerTokenPair(k.ID, UUIDv4())
	if err != nil {
		t.Fatalf("mint token pair: %v", err)
	}
	return pair.AccessToken
}

// pairedPeer is the CONSUMING-side twin of peerAuth: it hands a RemotePeer the
// credentials it would be holding after a successful pairing.
//
// Tests that build a RemotePeer literal skip AddRemotePeer, which is where the
// real exchange happens — so without this they present the raw key and the
// serving half correctly refuses it. Both halves live in one process here, so
// minting on one side and storing on the other is honest rather than a stub.
func pairedPeer(t *testing.T, p RemotePeer, k PeerKey) RemotePeer {
	t.Helper()
	pair, err := mintPeerTokenPair(k.ID, UUIDv4())
	if err != nil {
		t.Fatalf("mint token pair: %v", err)
	}
	p.UseTokens = true
	p.AccessToken = pair.AccessToken
	p.RefreshToken = pair.RefreshToken
	p.AccessExpires = time.Now().Add(time.Duration(pair.ExpiresIn) * time.Second).Format(time.RFC3339)
	storePeerTokens(p.Name, peerTokens{
		Access: pair.AccessToken, Refresh: pair.RefreshToken,
		Expires: time.Now().Add(time.Duration(pair.ExpiresIn) * time.Second),
	})
	return p
}

// Capabilities are an allowlist. A key granted embeddings must not gain image
// rendering the day image sharing ships.
func TestPeerKeyCapabilitiesAreAnAllowlist(t *testing.T) {
	peerTestDB(t)
	pk, err := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !pk.Allows(PeerCapEmbeddings) {
		t.Error("granted capability not allowed")
	}
	for _, c := range []string{PeerCapImages, PeerCapModels, PeerCapExec} {
		if pk.Allows(c) {
			t.Errorf("ungranted capability %q was allowed", c)
		}
	}
}

// A typo'd capability is rejected at mint. Stored as-is it would produce a key
// that authenticates fine and is refused by every handler — which reads as a
// broken key rather than a misspelled grant.
func TestMintRejectsUnknownCapability(t *testing.T) {
	peerTestDB(t)
	if _, err := MintPeerKey("mac", []string{"embedding"}, 0); err == nil {
		t.Fatal("a misspelled capability should be rejected at mint")
	} else if !strings.Contains(err.Error(), "embeddings") {
		t.Errorf("the error should name the valid capabilities, got: %v", err)
	}
	if _, err := MintPeerKey("mac", nil, 0); err == nil {
		t.Error("a key granting nothing should be rejected")
	}
}

// Revocation has to take effect at lookup, not merely at each call site — a
// disabled key that still resolves is one forgotten check away from working.
func TestDisabledPeerKeyStopsResolving(t *testing.T) {
	peerTestDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)
	if _, ok := LookupPeerKey(pk.Key); !ok {
		t.Fatal("fresh key should resolve")
	}
	if !SetPeerKeyDisabled(pk.ID, true) {
		t.Fatal("disable failed")
	}
	if _, ok := LookupPeerKey(pk.Key); ok {
		t.Error("a disabled key still resolves")
	}
	// Belt and braces: even holding the record, Allows says no.
	disabled := PeerKey{Caps: []string{PeerCapEmbeddings}, Disabled: true}
	if disabled.Allows(PeerCapEmbeddings) {
		t.Error("a disabled key allows a granted capability")
	}
}

// An unauthenticated caller gets 401 and no manifest — the capability list is
// itself information about the host.
func TestPeerManifestRequiresAKey(t *testing.T) {
	peerTestDB(t)
	w := httptest.NewRecorder()
	HandlePeerManifest(w, httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no key → %d, want 401", w.Code)
	}
	if strings.Contains(w.Body.String(), PeerCapEmbeddings) {
		t.Error("the manifest leaked capability names to an unauthenticated caller")
	}
}

// The manifest distinguishes "not built yet" from "not granted to you", so a
// peer debugging its config does not have to guess which wall it hit.
func TestPeerManifestSeparatesServedFromGranted(t *testing.T) {
	peerTestDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	r := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
	r.Header.Set(peerKeyHeader, pk.Key)
	w := httptest.NewRecorder()
	HandlePeerManifest(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("manifest → %d: %s", w.Code, w.Body.String())
	}
	var m PeerManifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]PeerManifestEntry{}
	for _, e := range m.Capabilities {
		seen[e.Name] = e
	}
	if len(seen) != len(PeerCapabilities()) {
		t.Errorf("manifest listed %d capabilities, want all %d", len(seen), len(PeerCapabilities()))
	}
	if e := seen[PeerCapEmbeddings]; !e.Served || !e.Granted {
		t.Errorf("embeddings should be served AND granted: %+v", e)
	}
	unbuilt := anUnservedCap(t)
	if e := seen[unbuilt]; e.Served {
		t.Errorf("%s is not implemented yet but reported served: %+v", unbuilt, e)
	}
	if e := seen[unbuilt]; e.Granted {
		t.Errorf("%s was not granted to this key but reported granted: %+v", unbuilt, e)
	}
}

// A key without the capability is refused, and the refusal names what it DOES
// grant — otherwise a peer pointed at the wrong key cannot tell that from a
// capability the host does not offer.
func TestPeerEmbeddingsRefusesUngrantedKey(t *testing.T) {
	peerTestDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapImages}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/embeddings",
		strings.NewReader(`{"input":["hello"]}`))
	r.Header.Set("Authorization", "Bearer "+peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerEmbeddings(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("ungranted key → %d, want 403: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), PeerCapImages) {
		t.Errorf("refusal should name what the key does grant: %s", w.Body.String())
	}
}

// Bearer is accepted because that is what gohort's own EmbedWith sends. If this
// breaks, the consumer side stops being zero-code.
func TestPeerAcceptsBearerAsWellAsHeader(t *testing.T) {
	peerTestDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	cred := peerAuth(t, pk)
	for _, set := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set(peerKeyHeader, cred) },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+cred) },
		func(r *http.Request) { r.Header.Set("Authorization", "bearer "+cred) },
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
		set(r)
		if _, ok := peerFromRequest(r); !ok {
			t.Errorf("peer credential not accepted via %v", r.Header)
		}
	}
}

// Refusing to relay is what keeps A→B→A from becoming an invisible hang. An
// instance that borrows its own embeddings will not serve them onward.
func TestPeerEmbeddingsRefusesToRelay(t *testing.T) {
	peerTestDB(t)
	prev := GetEmbeddingConfig()
	t.Cleanup(func() { SetEmbeddingConfig(prev) })
	SetEmbeddingConfig(EmbeddingConfig{
		Enabled:  true,
		Endpoint: "https://other-instance.example/api/peer/v1",
		Model:    "nomic-embed-text",
	})
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/embeddings",
		strings.NewReader(`{"input":["hello"]}`))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerEmbeddings(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a relaying instance → %d, want 503: %s", w.Code, w.Body.String())
	}
}

// Answering from a different model than the caller named returns vectors from a
// space it does not think it is in, and nothing downstream can detect that. So
// the mismatch is refused rather than served.
func TestPeerEmbeddingsRefusesAModelMismatch(t *testing.T) {
	peerTestDB(t)
	prev := GetEmbeddingConfig()
	t.Cleanup(func() { SetEmbeddingConfig(prev) })
	SetEmbeddingConfig(EmbeddingConfig{
		Enabled: true, Endpoint: "http://localhost:11434/api", Model: "nomic-embed-text",
	})
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/embeddings",
		strings.NewReader(`{"model":"text-embedding-3-small","input":["hello"]}`))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerEmbeddings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("model mismatch → %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "nomic-embed-text") {
		t.Errorf("the refusal should name the model actually in use: %s", w.Body.String())
	}
}

// Both OpenAI input shapes are accepted: a bare string and an array.
func TestPeerEmbedRequestAcceptsBothInputShapes(t *testing.T) {
	var one peerEmbedRequest
	json.Unmarshal([]byte(`{"input":"hello"}`), &one)
	if got := one.inputs(); len(got) != 1 || got[0] != "hello" {
		t.Errorf("bare string input = %q", got)
	}
	var many peerEmbedRequest
	json.Unmarshal([]byte(`{"input":["a","b"]}`), &many)
	if got := many.inputs(); len(got) != 2 || got[1] != "b" {
		t.Errorf("array input = %q", got)
	}
	var none peerEmbedRequest
	json.Unmarshal([]byte(`{}`), &none)
	if got := none.inputs(); len(got) != 0 {
		t.Errorf("absent input = %q, want none", got)
	}
}

// The rate ceiling exists so a runaway peer cannot saturate this instance.
func TestPeerRateLimitStopsARunawayPeer(t *testing.T) {
	peerTestDB(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 3)
	for i := 0; i < 3; i++ {
		if !peerRateAllow(pk) {
			t.Fatalf("call %d refused under a ceiling of 3", i+1)
		}
	}
	if peerRateAllow(pk) {
		t.Error("the 4th call was allowed under a ceiling of 3")
	}
}
