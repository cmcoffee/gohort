package servitor

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
	"golang.org/x/crypto/ssh"
)

// An SSH host key is trusted the first time and pinned: a different key later
// is refused, until the system is saved again.

func newHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestAHostKeyIsPinnedOnFirstUse(t *testing.T) {
	prev := hostKeyStore
	hostKeyStore = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { hostKeyStore = prev })

	first, second := newHostKey(t), newHostKey(t)
	cb := pinnedHostKey("alice", "sys1", "box.example:22")
	if err := cb("box.example:22", nil, first); err != nil {
		t.Fatalf("first contact should be trusted: %v", err)
	}
	if err := cb("box.example:22", nil, first); err != nil {
		t.Fatalf("the same key again should pass: %v", err)
	}
	if err := cb("box.example:22", nil, second); err == nil || !strings.Contains(err.Error(), "CHANGED") {
		t.Fatalf("a different key should be refused: %v", err)
	}
	forgetHostKey("alice", "sys1", "box.example", 22)
	if err := cb("box.example:22", nil, second); err != nil {
		t.Fatalf("after re-saving the system, the new key should be trusted: %v", err)
	}
}

// A pin is a system's own. Another user saving a system at the same address
// does not clear it (that let anybody re-trust whatever answered on
// somebody else's next connection, password and all), and a pin made by
// address alone before pins were per system still holds.
func TestAHostKeyPinIsTheSystemsOwn(t *testing.T) {
	prev := hostKeyStore
	hostKeyStore = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { hostKeyStore = prev })
	real, fake := newHostKey(t), newHostKey(t)

	alice := pinnedHostKey("alice", "sys1", "box.example:22")
	if err := alice("box.example:22", nil, real); err != nil {
		t.Fatal(err)
	}
	forgetHostKey("mallory", "sysX", "box.example", 22) // mallory saves a system at the same host
	if err := alice("box.example:22", nil, fake); err == nil {
		t.Fatal("another user's save cleared this system's pin")
	}
	// mallory's own system trusts afresh, and that does not touch alice's.
	if err := pinnedHostKey("mallory", "sysX", "box.example:22")("box.example:22", nil, fake); err != nil {
		t.Fatalf("mallory's own system: %v", err)
	}
	if err := alice("box.example:22", nil, real); err != nil {
		t.Fatalf("alice's pin moved: %v", err)
	}

	// A pin made by address alone before this.
	hostKeyStore.Set(hostKeyTable, "old.example:22", ssh.FingerprintSHA256(real))
	bob := pinnedHostKey("bob", "sys2", "old.example:22")
	if err := bob("old.example:22", nil, fake); err == nil {
		t.Fatal("an older pin was not honoured")
	}
	if err := bob("old.example:22", nil, real); err != nil {
		t.Fatalf("the pinned key: %v", err)
	}
	// Bob saving his system (a rebuilt machine) trusts its new key.
	forgetHostKey("bob", "sys2", "old.example", 22)
	if err := bob("old.example:22", nil, fake); err != nil {
		t.Fatalf("after bob re-saved his system: %v", err)
	}
}
