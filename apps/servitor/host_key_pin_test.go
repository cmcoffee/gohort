package servitor

// An SSH host key is trusted the first time and pinned: a different key later
// is refused, until the system is saved again.

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
	"golang.org/x/crypto/ssh"
)

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
	cb := pinnedHostKey("box.example:22")
	if err := cb("box.example:22", nil, first); err != nil {
		t.Fatalf("first contact should be trusted: %v", err)
	}
	if err := cb("box.example:22", nil, first); err != nil {
		t.Fatalf("the same key again should pass: %v", err)
	}
	if err := cb("box.example:22", nil, second); err == nil || !strings.Contains(err.Error(), "CHANGED") {
		t.Fatalf("a different key should be refused: %v", err)
	}
	forgetHostKey("box.example", 22)
	if err := cb("box.example:22", nil, second); err != nil {
		t.Fatalf("after re-saving the system, the new key should be trusted: %v", err)
	}
}
