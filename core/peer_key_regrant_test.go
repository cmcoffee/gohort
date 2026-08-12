package core

// Re-granting an EXISTING key.
//
// Until this existed the admin UI offered only Enable/Disable and Delete, so
// widening a grant meant deleting the key and minting a new one — which changes
// the secret, and therefore has to be re-pasted and re-probed on the other
// machine. That made shipping a new capability something no existing peer could
// pick up without an outage on both ends.

import (
	"strings"
	"testing"
)

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
