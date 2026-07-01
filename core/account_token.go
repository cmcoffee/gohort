package core

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// AccountToken is a per-user PERSONAL ACCESS TOKEN: the credential a user pastes
// into an external client (the X-API-Key header) to reach THEIR OWN gohort agents
// and MCP tools. It is the account-page-native equivalent of a Bridges key —
// minted/managed under /account, scoped to the user — so personal access lives
// with the user's account instead of being coupled to the messaging Bridges app.
//
// Stored in a single global table keyed by the SECRET so auth is an O(1) Get; the
// Owner field scopes listing + revocation. The full secret is returned exactly
// once (at mint) and never listed again — ListAccountTokens masks it.
type AccountToken struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Owner    string `json:"owner"`
	Token    string `json:"token,omitempty"` // full secret: returned once at mint; masked on list
	Created  string `json:"created"`
	LastSeen string `json:"last_seen,omitempty"`
}

const accountTokenTable = "account_tokens"

func init() { RegisterAPIKeyValidator(lookupAccountTokenOwner) }

func acctRandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// MintAccountToken creates and stores a personal token for owner and returns it
// WITH the secret populated (the only time the caller sees it).
func MintAccountToken(owner, name string) AccountToken {
	t := AccountToken{
		ID:      acctRandHex(6),
		Name:    strings.TrimSpace(name),
		Owner:   owner,
		Token:   "ght_" + acctRandHex(24),
		Created: time.Now().UTC().Format(time.RFC3339),
	}
	if RootDB != nil {
		RootDB.Set(accountTokenTable, t.Token, t)
	}
	return t
}

// ListAccountTokens returns owner's tokens (secret masked, never the real value),
// newest first.
func ListAccountTokens(owner string) []AccountToken {
	var out []AccountToken
	if RootDB == nil {
		return out
	}
	for _, secret := range RootDB.Keys(accountTokenTable) {
		var t AccountToken
		if RootDB.Get(accountTokenTable, secret, &t) && t.Owner == owner {
			t.Token = maskAccountToken(secret)
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

// RevokeAccountToken deletes one of owner's tokens by ID. Returns true if removed.
// Scoped to owner so a user can never revoke another user's token.
func RevokeAccountToken(owner, id string) bool {
	if RootDB == nil {
		return false
	}
	for _, secret := range RootDB.Keys(accountTokenTable) {
		var t AccountToken
		if RootDB.Get(accountTokenTable, secret, &t) && t.Owner == owner && t.ID == id {
			RootDB.Unset(accountTokenTable, secret)
			return true
		}
	}
	return false
}

// lookupAccountTokenOwner is the X-API-Key validator: a secret → owner resolver
// registered alongside the bridge-key and desktop-key validators. Read-only (no
// LastSeen write) so it stays a cheap O(1) Get on every authenticated request.
func lookupAccountTokenOwner(secret string) (string, bool) {
	if RootDB == nil || strings.TrimSpace(secret) == "" {
		return "", false
	}
	var t AccountToken
	if RootDB.Get(accountTokenTable, secret, &t) && t.Owner != "" {
		return t.Owner, true
	}
	return "", false
}

// maskAccountToken renders a non-secret hint of a token for display.
func maskAccountToken(s string) string {
	if len(s) <= 12 {
		return "••••"
	}
	return s[:8] + "…" + s[len(s)-4:]
}
