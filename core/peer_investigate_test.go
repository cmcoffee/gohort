package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestAllowsApplianceNeedsAllThreeParts — a capability grant reaches nothing on
// its own. Unlike the other six capabilities, which are anonymous compute, an
// investigation runs as a user against a specific machine, so the key has to
// name both.
func TestAllowsApplianceNeedsAllThreeParts(t *testing.T) {
	full := PeerKey{Caps: []string{PeerCapInvestigate}, Owner: "craig", Appliances: []string{"app-1"}}
	if !full.AllowsAppliance("app-1") {
		t.Fatal("a fully specified key was refused")
	}
	if full.AllowsAppliance("app-2") {
		t.Error("a key reached an appliance it does not name")
	}

	noCap := full
	noCap.Caps = nil
	if noCap.AllowsAppliance("app-1") {
		t.Error("a key with no investigate capability reached an appliance")
	}
	noOwner := full
	noOwner.Owner = ""
	if noOwner.AllowsAppliance("app-1") {
		t.Error("a key with no owner reached an appliance — the investigation would run as nobody")
	}
	noList := full
	noList.Appliances = nil
	if noList.AllowsAppliance("app-1") {
		t.Error("EMPTY GRANTS NOTHING was violated — a key naming no appliances reached one")
	}
	disabled := full
	disabled.Disabled = true
	if disabled.AllowsAppliance("app-1") {
		t.Error("a revoked key still reached an appliance")
	}
}

// TestInvestigateIsAdvertisedAndServed — a capability the manifest offers but
// nothing implements is worse than one it never mentions.
func TestInvestigateIsAdvertisedAndServed(t *testing.T) {
	found := false
	for _, c := range PeerCapabilities() {
		if c == PeerCapInvestigate {
			found = true
		}
	}
	if !found {
		t.Error("investigate is not in PeerCapabilities, so no key can be granted it")
	}
	if !peerCapServed(PeerCapInvestigate) {
		t.Error("investigate is advertised but reported unserved")
	}
}

// TestPeerInvestigateRejectsAnUngrantedAppliance — the capability check passes
// and the appliance check must still refuse, with the granted set named so a
// peer pointed at the wrong key can tell that from an appliance this instance
// does not have.
func TestPeerInvestigateRejectsUngrantedAppliance(t *testing.T) {
	prev := PeerInvestigateFunc
	called := false
	PeerInvestigateFunc = func(context.Context, string, string, string) (string, error) {
		called = true
		return "should not happen", nil
	}
	t.Cleanup(func() { PeerInvestigateFunc = prev })

	k := PeerKey{Caps: []string{PeerCapInvestigate}, Owner: "craig", Appliances: []string{"allowed-1"}}
	if k.AllowsAppliance("other-2") {
		t.Fatal("precondition: the key should not allow other-2")
	}
	if called {
		t.Error("the investigator ran for an appliance the key does not name")
	}
}

// TestPeerInvestigateNeedsBothFields.
func TestPeerInvestigateRequiresBodyFields(t *testing.T) {
	body, _ := json.Marshal(map[string]string{"appliance_id": "", "question": ""})
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/investigate", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	HandlePeerInvestigate(w, r)
	// Unauthenticated, so it should not reach the field check — but it must not
	// be a 200 either.
	if w.Code == http.StatusOK {
		t.Errorf("an unauthenticated request succeeded (%d)", w.Code)
	}
}

// TestPeerInvestigateRefusesNonPost.
func TestPeerInvestigateRefusesNonPost(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/investigate", nil)
	w := httptest.NewRecorder()
	HandlePeerInvestigate(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET → %d, want 405", w.Code)
	}
}

// TestPeerInvestigateClientRefusesAPeerThatDoesNotOfferIt — caught locally with
// a message naming what the peer DOES offer, rather than by sending a request
// that comes back 403.
func TestPeerInvestigateClientChecksTheOffer(t *testing.T) {
	p := RemotePeer{Name: "mac", BaseURL: "https://example.invalid", Key: "k", Caps: []string{PeerCapEmbeddings}}
	_, err := PeerInvestigate(context.Background(), p, "app-1", "what is running?")
	if err == nil {
		t.Fatal("calling a peer that does not offer investigation succeeded")
	}
	if !strings.Contains(err.Error(), PeerCapInvestigate) {
		t.Errorf("error = %v, want it to name the missing capability", err)
	}
	if !strings.Contains(err.Error(), PeerCapEmbeddings) {
		t.Errorf("error = %v, want it to list what the peer does offer", err)
	}
	// The cached list is three situations in one sentence; the message has to
	// separate them or the reader fixes the wrong end.
	for _, want := range []string{"last check", "rebuild", "granted", "out of date"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q — the reader cannot tell which of the three causes it is:\n%v", want, err)
		}
	}
}

// TestPeerInvestigateClientNeedsConfiguration.
func TestPeerInvestigateClientNeedsConfiguration(t *testing.T) {
	_, err := PeerInvestigate(context.Background(),
		RemotePeer{Name: "mac", Caps: []string{PeerCapInvestigate}}, "app-1", "q")
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("an unconfigured peer gave %v", err)
	}
}

// TestPeerInvestigateReadsTheAnswer — and distinguishes a transport failure
// from an investigation that ran and could not answer, because the two need
// different fixes.
func TestPeerInvestigateReadsTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(peerKeyHeader) == "" {
			t.Error("the peer key was not carried, so the far side would 401")
		}
		var in struct {
			ApplianceID string `json:"appliance_id"`
			Question    string `json:"question"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.ApplianceID != "app-1" || in.Question != "what is running?" {
			t.Errorf("body = %+v", in)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"answer": "nginx and postgres"})
	}))
	defer srv.Close()

	p := RemotePeer{Name: "mac", BaseURL: srv.URL, Key: "secret", Caps: []string{PeerCapInvestigate}}
	got, err := PeerInvestigate(context.Background(), p, "app-1", "what is running?")
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	if got != "nginx and postgres" {
		t.Errorf("answer = %q", got)
	}
}

// TestPeerInvestigateSurfacesTheFarSidesReason — a 200 carrying an error is an
// investigation that ran and failed, and the caller needs the reason rather
// than a generic transport complaint.
func TestPeerInvestigateSurfacesTheFarSidesReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": "connection refused on the appliance"})
	}))
	defer srv.Close()

	p := RemotePeer{Name: "mac", BaseURL: srv.URL, Key: "secret", Caps: []string{PeerCapInvestigate}}
	_, err := PeerInvestigate(context.Background(), p, "app-1", "q")
	if err == nil {
		t.Fatal("an error response was read as success")
	}
	if !strings.Contains(err.Error(), "connection refused on the appliance") {
		t.Errorf("the far side's reason was lost: %v", err)
	}
}

// TestPeerInvestigateRejectsAnEmptyAnswer — an empty string would render in the
// chat as a successful investigation that found nothing, which is a different
// claim from "the far side returned nothing".
func TestPeerInvestigateRejectsAnEmptyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"answer": "   "})
	}))
	defer srv.Close()

	p := RemotePeer{Name: "mac", BaseURL: srv.URL, Key: "secret", Caps: []string{PeerCapInvestigate}}
	if _, err := PeerInvestigate(context.Background(), p, "app-1", "q"); err == nil {
		t.Error("an empty answer was accepted as a result")
	}
}

// TestSetPeerKeyScopeRequiresAnOwner — naming machines with nobody to run as
// would authorize a request that then fails at dispatch, which is a worse
// failure than refusing at the point the operator can still fix it.
func TestSetPeerKeyScopeRequiresAnOwner(t *testing.T) {
	if RootDB == nil {
		t.Skip("no RootDB in this test binary")
	}
	pk, err := MintPeerKey("scope-test", []string{PeerCapInvestigate}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	t.Cleanup(func() { DeletePeerKey(pk.ID) })

	if _, err := SetPeerKeyScope(pk.ID, "", []string{"app-1"}); err == nil {
		t.Error("appliances were accepted with no owner")
	}
	got, err := SetPeerKeyScope(pk.ID, "craig", []string{"app-1", " app-1 ", "", "app-2"})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if got.Owner != "craig" || len(got.Appliances) != 2 {
		t.Errorf("scope = owner %q over %v", got.Owner, got.Appliances)
	}
	// Clearing the list revokes reach without touching the capability.
	cleared, err := SetPeerKeyScope(pk.ID, "", nil)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if len(cleared.Appliances) != 0 || !cleared.Allows(PeerCapInvestigate) {
		t.Error("clearing the scope should empty the reach and keep the capability")
	}
	if cleared.AllowsAppliance("app-1") {
		t.Error("a cleared key still reaches an appliance")
	}
}

// TestSetPeerKeyScopeRefusesWhatTheOwnerCannotReach — the check that doubles as
// the authorization rule for granting from an appliance's own page: you may
// only add a box to a key whose owner could already reach it, so the grant
// cannot widen what that identity sees. It also stops a grant that looks right
// in the admin table and fails at question time, from a machine nobody is at.
func TestSetPeerKeyScopeRefusesWhatTheOwnerCannotReach(t *testing.T) {
	if RootDB == nil {
		t.Skip("no RootDB in this test binary")
	}
	prev := PeerInvestigableFunc
	// Only "reachable" resolves for craig.
	PeerInvestigableFunc = func(user string, ids []string) []PeerInvestigable {
		var out []PeerInvestigable
		for _, id := range ids {
			if user == "craig" && id == "reachable" {
				out = append(out, PeerInvestigable{ID: id, Name: id})
			}
		}
		return out
	}
	t.Cleanup(func() { PeerInvestigableFunc = prev })

	pk, err := MintPeerKey("reach-test", []string{PeerCapInvestigate}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	t.Cleanup(func() { DeletePeerKey(pk.ID) })

	if _, err := SetPeerKeyScope(pk.ID, "craig", []string{"reachable"}); err != nil {
		t.Fatalf("a reachable appliance was refused: %v", err)
	}
	_, err = SetPeerKeyScope(pk.ID, "craig", []string{"reachable", "someone-elses"})
	if err == nil {
		t.Fatal("an appliance the owner cannot reach was accepted")
	}
	if !strings.Contains(err.Error(), "someone-elses") {
		t.Errorf("error should name the unreachable appliance: %v", err)
	}
	if !strings.Contains(err.Error(), "craig") {
		t.Errorf("error should name the owner it would run as: %v", err)
	}
	// The refusal must not have partially applied.
	after, _ := LookupPeerKey(pk.Key)
	if len(after.Appliances) != 1 || after.Appliances[0] != "reachable" {
		t.Errorf("a refused grant changed the stored scope: %v", after.Appliances)
	}
}

// TestEveryPeerRouteIsSessionAuthExempt — a peer route that is not registered
// public gets 401'd by the SESSION layer before its handler runs, so the peer
// key is never even looked at. The caller then sees a bare "unauthorized"
// instead of the handler's message naming what was actually wrong, which reads
// exactly like a bad key. Missed precisely this on the investigate route.
//
// Checked against the SOURCE rather than the registry: RegisterPublicPath runs
// during real route registration, which a unit-test binary never performs, so a
// runtime assertion here would pass or fail on which other tests happened to
// run first.
func TestEveryPeerRouteIsSessionAuthExempt(t *testing.T) {
	src, err := os.ReadFile("webapp.go")
	if err != nil {
		t.Skipf("cannot read webapp.go: %v", err)
	}
	body := string(src)
	handled := regexp.MustCompile(`mux\.HandleFunc\("(/api/peer/[^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(handled) == 0 {
		t.Fatal("found no peer routes in webapp.go — has the registration moved?")
	}
	for _, m := range handled {
		path := m[1]
		if !strings.Contains(body, `RegisterPublicPath("`+path+`")`) {
			t.Errorf("%s is served but never RegisterPublicPath'd — the session layer will 401 it "+
				"with a bare \"unauthorized\" before the peer key is checked", path)
		}
	}
}
