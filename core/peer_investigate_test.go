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

// TestAllowsApplianceNeedsAllThreeParts: a capability grant reaches nothing on
// its own. Unlike the other capabilities, which are anonymous compute, exec
// runs as a user against a specific machine, so the key has to name both.
func TestAllowsApplianceNeedsAllThreeParts(t *testing.T) {
	full := PeerKey{Caps: []string{PeerCapExec}, Owner: "craig", Appliances: []string{"app-1"}}
	allows := func(k PeerKey, id string) bool { return k.AllowsApplianceFor(PeerCapExec, id) }
	if !allows(full, "app-1") {
		t.Fatal("a fully specified key was refused")
	}
	if allows(full, "app-2") {
		t.Error("a key reached an appliance it does not name")
	}

	noCap := full
	noCap.Caps = nil
	if allows(noCap, "app-1") {
		t.Error("a key with no exec capability reached an appliance")
	}
	noOwner := full
	noOwner.Owner = ""
	if allows(noOwner, "app-1") {
		t.Error("a key with no owner reached an appliance: the command would run as nobody")
	}
	noList := full
	noList.Appliances = nil
	if allows(noList, "app-1") {
		t.Error("EMPTY GRANTS NOTHING was violated: a key naming no appliances reached one")
	}
	disabled := full
	disabled.Disabled = true
	if allows(disabled, "app-1") {
		t.Error("a revoked key still reached an appliance")
	}
	// Anonymous compute never resolves against a machine.
	if (PeerKey{Caps: []string{PeerCapEmbeddings}, Owner: "craig", Appliances: []string{"app-1"}}).AllowsApplianceFor(PeerCapEmbeddings, "app-1") {
		t.Error("an embeddings grant reached an appliance")
	}
}

// A peer key lends resources, never the instance. Investigate (this
// instance's investigator answering a peer's question) and knowledge (a copy
// of what this instance had gathered) were retired for that. A key that still
// carries those names from before gets neither: no route, nothing in the
// manifest, and the system list it is shown follows exec alone.
func TestAPeerKeyReachesNoInvestigatorAndNoKnowledge(t *testing.T) {
	for _, c := range PeerCapabilities() {
		if c == "investigate" || c == "knowledge" {
			t.Errorf("%q can still be granted", c)
		}
	}
	if peerCapServed("investigate") || peerCapServed("knowledge") {
		t.Error("a retired capability is reported as served")
	}
	src, err := os.ReadFile("dashboard.go")
	if err != nil {
		t.Skipf("cannot read dashboard.go: %v", err)
	}
	for _, route := range []string{"/api/peer/v1/investigate", "/api/peer/v1/knowledge"} {
		if strings.Contains(string(src), `"`+route+`"`) {
			t.Errorf("%s is still routed", route)
		}
	}

	// An old key's retired names grant nothing, and an edit drops them
	// rather than refusing the save.
	old := PeerKey{Caps: []string{"investigate", "knowledge"}, Owner: "craig", Appliances: []string{"app-1"}}
	if old.AllowsApplianceFor("investigate", "app-1") || old.AllowsApplianceFor("knowledge", "app-1") {
		t.Error("a retired capability reaches a system")
	}
	if caps, err := cleanPeerCaps([]string{"investigate", "exec", "knowledge"}); err != nil || strings.Join(caps, ",") != "exec" {
		t.Errorf("re-granting a key that carries retired names: %v, %v", caps, err)
	}

	// The advertised list of systems follows exec.
	prev := PeerInvestigableFunc
	PeerInvestigableFunc = func(user string, ids []string) []PeerInvestigable {
		out := []PeerInvestigable{}
		for _, id := range ids {
			out = append(out, PeerInvestigable{ID: id, Name: id})
		}
		return out
	}
	t.Cleanup(func() { PeerInvestigableFunc = prev })
	if got := peerInvestigablesFor(old); len(got) != 0 {
		t.Errorf("a key with only retired names is shown systems: %v", got)
	}
	execKey := PeerKey{Caps: []string{PeerCapExec}, Owner: "craig", Appliances: []string{"app-1"}}
	if got := peerInvestigablesFor(execKey); len(got) != 1 || got[0].ID != "app-1" {
		t.Errorf("an exec key is shown %v, want app-1", got)
	}
}

// TestPeerExecRefusesAnUngrantedAppliance: the capability check passes and the
// appliance check must still refuse, before anything runs.
func TestPeerExecRefusesAnUngrantedAppliance(t *testing.T) {
	prev := PeerExecFunc
	ran := false
	PeerExecFunc = func(context.Context, string, string, string) (string, error) {
		ran = true
		return "", nil
	}
	t.Cleanup(func() { PeerExecFunc = prev })
	k := PeerKey{Caps: []string{PeerCapExec}, Owner: "craig", Appliances: []string{"allowed-1"}}
	if k.AllowsApplianceFor(PeerCapExec, "other-2") || ran {
		t.Error("an ungranted appliance was allowed")
	}
}

// TestPeerExecRefusesNonPost: the handler answers a GET before it looks at
// any key.
func TestPeerExecRefusesNonPost(t *testing.T) {
	w := httptest.NewRecorder()
	HandlePeerExec(w, httptest.NewRequest(http.MethodGet, "/api/peer/v1/exec", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET answered %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
}

// TestSetPeerKeyScopeRequiresAnOwner — naming machines with nobody to run as
// would authorize a request that then fails at dispatch, which is a worse
// failure than refusing at the point the operator can still fix it.
func TestSetPeerKeyScopeRequiresAnOwner(t *testing.T) {
	if RootDB == nil {
		t.Skip("no RootDB in this test binary")
	}
	pk, err := MintPeerKey("scope-test", []string{PeerCapExec}, 0)
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
	if len(cleared.Appliances) != 0 || !cleared.Allows(PeerCapExec) {
		t.Error("clearing the scope should empty the reach and keep the capability")
	}
	if cleared.AllowsApplianceFor(PeerCapExec, "app-1") {
		t.Error("a cleared key still reaches an appliance")
	}
}

// TestSetPeerKeyScopeRefusesWhatTheOwnerCannotReach — the check that doubles as
// the authorization rule for granting from an appliance's own page: you may
// only add a box to a key whose owner could already reach it, so the grant
// cannot widen what that identity sees. It also stops a grant that looks right
// in the admin table and fails at command time, from a machine nobody is at.
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

	pk, err := MintPeerKey("reach-test", []string{PeerCapExec}, 0)
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
// exactly like a bad key. Missed precisely this on a peer route once.
//
// Checked against the SOURCE rather than the registry: RegisterPublicPath runs
// during real route registration, which a unit-test binary never performs, so a
// runtime assertion here would pass or fail on which other tests happened to
// run first.
func TestEveryPeerRouteIsSessionAuthExempt(t *testing.T) {
	// The dashboard mux is wired in dashboard.go (ServeDashboard), which is
	// where the peer routes and their RegisterPublicPath calls both live.
	src, err := os.ReadFile("dashboard.go")
	if err != nil {
		t.Skipf("cannot read dashboard.go: %v", err)
	}
	body := string(src)
	handled := regexp.MustCompile(`mux\.HandleFunc\("(/api/peer/[^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(handled) == 0 {
		t.Fatal("found no peer routes in dashboard.go — has the registration moved?")
	}
	for _, m := range handled {
		path := m[1]
		if !strings.Contains(body, `RegisterPublicPath("`+path+`")`) {
			t.Errorf("%s is served but never RegisterPublicPath'd — the session layer will 401 it "+
				"with a bare \"unauthorized\" before the peer key is checked", path)
		}
	}
}
