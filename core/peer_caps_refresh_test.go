package core

import "testing"

func capsManifest(entries ...PeerManifestEntry) PeerManifest {
	return PeerManifest{Instance: "far-side", Capabilities: entries}
}

func hasCap(caps []string, name string) bool {
	for _, c := range caps {
		if c == name {
			return true
		}
	}
	return false
}

// The window between a peer accepting connections and the app that implements
// a capability finishing its wiring is short, and the refresh reconciler runs
// on a 30-minute clock — so landing inside it wrote the capability out of the
// local record and every later call failed HERE, with no request made, until
// the next refresh. The far side was healthy throughout.
func TestRefreshKeepsAGrantedCapabilityThatIsMomentarilyUnserved(t *testing.T) {
	prev := []string{PeerCapExec, PeerCapInvestigate}
	m := capsManifest(
		PeerManifestEntry{Name: PeerCapExec, Served: false, Granted: true},
		PeerManifestEntry{Name: PeerCapInvestigate, Served: true, Granted: true},
	)
	got := refreshedCaps(prev, m)
	if !hasCap(got, PeerCapExec) {
		t.Errorf("exec was dropped on a not-yet-served refresh; caps = %v", got)
	}
	if !hasCap(got, PeerCapInvestigate) {
		t.Errorf("investigate should have survived; caps = %v", got)
	}
}

// A withdrawal is an operator's decision and must take effect at the next
// check — that is the whole reason the refresh replaces rather than merges.
func TestRefreshDropsARevokedCapability(t *testing.T) {
	prev := []string{PeerCapExec, PeerCapInvestigate}
	m := capsManifest(
		PeerManifestEntry{Name: PeerCapExec, Served: true, Granted: false},
		PeerManifestEntry{Name: PeerCapInvestigate, Served: true, Granted: true},
	)
	got := refreshedCaps(prev, m)
	if hasCap(got, PeerCapExec) {
		t.Errorf("a revoked grant must not survive the refresh; caps = %v", got)
	}
	if !hasCap(got, PeerCapInvestigate) {
		t.Errorf("investigate should have survived; caps = %v", got)
	}
}

// Retention only ever restores what a probe once saw working, so a capability
// that instance has never implemented cannot be invented by a later refresh.
func TestRefreshNeverInventsACapability(t *testing.T) {
	m := capsManifest(PeerManifestEntry{Name: PeerCapExec, Served: false, Granted: true})
	if got := refreshedCaps(nil, m); len(got) != 0 {
		t.Errorf("caps = %v, want none — this peer never served exec", got)
	}
}

// An instance not serving investigate sends no list at all, which is not the
// same statement as "nothing is granted any more". Reading absence as an
// empty answer emptied the remote-systems picker on a question the far side
// never answered.
func TestUnservedInvestigateDoesNotClaimAnEmptyList(t *testing.T) {
	if servesCap(capsManifest(PeerManifestEntry{Name: PeerCapInvestigate, Served: false, Granted: true}), PeerCapInvestigate) {
		t.Error("servesCap reported an unserved capability as served")
	}
	if !servesCap(capsManifest(PeerManifestEntry{Name: PeerCapInvestigate, Served: true, Granted: false}), PeerCapInvestigate) {
		t.Error("servesCap should key on Served alone — a served capability this key lacks is still being served")
	}
	if servesCap(capsManifest(), PeerCapInvestigate) {
		t.Error("a manifest that never mentions the capability is not serving it")
	}
}
