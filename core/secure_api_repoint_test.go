package core

import "testing"

// Saving a credential with an empty secret keeps the stored one, which was
// right for a description edit and wrong for a new address: an edit, or an
// agent's proposed edit, could point the key at any server. A user-owned
// credential moved to another host needs its secret entered again.
func TestRepointedCredentialNeedsItsSecretAgain(t *testing.T) {
	secureAPITestStore(t)
	s := Secure()
	c := SecureCredential{Name: "svc", Type: SecureCredBearer, BaseURL: "https://api.example.com/v1", Owner: "user1"}
	if err := s.Save(c, "tok-1"); err != nil {
		t.Fatal(err)
	}

	same := c
	same.BaseURL = "https://api.example.com/v2"
	same.Description = "path change only"
	if err := s.Save(same, ""); err != nil {
		t.Fatalf("a same-host edit should keep the secret: %v", err)
	}

	moved := same
	moved.BaseURL = "https://collector.example.net"
	if err := s.Save(moved, ""); err == nil {
		t.Fatal("moving the credential to another host kept its secret")
	}
	if got, _ := s.LoadUser("user1", "svc"); got.BaseURL != same.BaseURL {
		t.Fatalf("the refused move was stored anyway: %q", got.BaseURL)
	}
	if err := s.Save(moved, "tok-2"); err != nil {
		t.Fatalf("a move with the secret re-entered should save: %v", err)
	}

	// A draft over a credential that holds a real secret drops it when the
	// draft names another server, instead of leaving it for a re-enable.
	draft := SecureCredential{Name: "svc", Type: SecureCredBearer, BaseURL: "https://elsewhere.example.org", Owner: "user1"}
	if err := s.SaveAPIDraft(draft); err != nil {
		t.Fatal(err)
	}
	if _, _, hasSecret := s.CredentialStatusOwned("user1", "svc"); hasSecret {
		t.Fatal("a repointing draft kept the real secret")
	}

	if !credDestinationMoved(SecureCredential{TokenURL: "https://idp.example.com/token"}, SecureCredential{TokenURL: "https://idp.example.net/token"}) {
		t.Error("a new token endpoint host should count as a move")
	}
	if credDestinationMoved(SecureCredential{BaseURL: "https://a.example.com", AllowedURLPattern: "https://a.example.com/**"}, SecureCredential{BaseURL: "https://A.example.com/"}) {
		t.Error("a case change, or a field the form does not carry, is not a move")
	}
}
