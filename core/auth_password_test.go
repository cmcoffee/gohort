package core

import (
	"strings"
	"testing"
)

func TestPasswordHashingBcryptAndMigration(t *testing.T) {
	pw := "correct horse battery staple"

	// New scheme is bcrypt (self-identifying by the "$2" prefix).
	h := hashPassword(pw)
	if !strings.HasPrefix(h, "$2") {
		t.Fatalf("hashPassword should produce a bcrypt hash, got %q", h)
	}
	if ok, legacy := verifyPassword(h, pw); !ok || legacy {
		t.Errorf("bcrypt verify: got ok=%v legacy=%v, want true,false", ok, legacy)
	}
	if ok, _ := verifyPassword(h, "wrong"); ok {
		t.Errorf("bcrypt verify accepted a wrong password")
	}

	// Per-user salt: two hashes of the SAME password must differ.
	if hashPassword(pw) == h {
		t.Errorf("bcrypt hashes of the same password should differ (missing salt)")
	}

	// Legacy SHA-256 hashes still verify AND flag for upgrade-on-login.
	legacy := legacyPasswordHash(pw)
	if ok, needsUpgrade := verifyPassword(legacy, pw); !ok || !needsUpgrade {
		t.Errorf("legacy verify: got ok=%v legacy=%v, want true,true", ok, needsUpgrade)
	}
	if ok, _ := verifyPassword(legacy, "wrong"); ok {
		t.Errorf("legacy verify accepted a wrong password")
	}

	// An empty stored hash never verifies (guards against a blank PassHash bypass).
	if ok, _ := verifyPassword("", pw); ok {
		t.Errorf("empty stored hash should never verify")
	}

	// Passwords longer than bcrypt's 72-byte input cap: the SHA-256 pre-digest
	// means two long passwords that differ only past byte 72 must NOT collide.
	long1 := strings.Repeat("a", 100) + "1"
	long2 := strings.Repeat("a", 100) + "2"
	lh := hashPassword(long1)
	if ok, _ := verifyPassword(lh, long1); !ok {
		t.Errorf("a long password should verify against its own hash")
	}
	if ok, _ := verifyPassword(lh, long2); ok {
		t.Errorf("distinct long passwords must not collide (bcrypt 72-byte cap not handled)")
	}
}
