package core

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
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

	// Scope narrows what a key may do at an outward-facing surface (the OpenAI
	// /v1 endpoint). NIL = LEGACY UNRESTRICTED: a key minted before scoping
	// existed reaches every feature and target its owner can, so turning
	// enforcement on doesn't break a live integration. A non-nil Scope is
	// deny-by-default — only the listed features and targets are allowed.
	// New keys are minted with an explicit (possibly empty) Scope, so they are
	// restricted from the start; the nil case is strictly the pre-scoping
	// grandfather. See AllowsFeature / AllowsTarget.
	Scope *TokenScope `json:"scope,omitempty"`
}

// TokenScope is a key's allow-list. Two independent dimensions the user sets
// per key: which admin-permitted FEATURES the key may use (the OpenAI endpoint
// is the first), and which agent/channel/tier TARGETS it may drive. Both are
// deny-by-default within a non-nil scope.
type TokenScope struct {
	// Features the key may use, e.g. "openai". A feature the admin has not
	// permitted for this user is denied regardless — the key toggle only
	// narrows within what the admin already allows.
	Features []string `json:"features,omitempty"`
	// Targets the key may drive: "worker", "lead", "agent:<id>",
	// "channel:<chat>". Matched against the resolved /v1 target.
	Targets []string `json:"targets,omitempty"`
}

// AllowsFeature reports whether the key permits a feature. A nil scope is the
// legacy-unrestricted grandfather (see AccountToken.Scope) and allows anything.
func (t *AccountToken) AllowsFeature(feature string) bool {
	if t == nil {
		return false
	}
	if t.Scope == nil {
		return true // legacy: minted before scoping — unrestricted
	}
	return containsFold(t.Scope.Features, feature)
}

// AllowsTarget reports whether the key permits a resolved /v1 target. Nil scope
// = legacy-unrestricted. An empty target set on a non-nil scope denies all —
// deny-by-default is the whole point.
func (t *AccountToken) AllowsTarget(target string) bool {
	if t == nil {
		return false
	}
	if t.Scope == nil {
		return true // legacy: unrestricted
	}
	return containsFold(t.Scope.Targets, target)
}

// IsLegacyUnscoped reports a key that predates scoping (nil Scope). Surfaced so
// the account + admin UIs can flag "this key is unrestricted — set a scope",
// turning the grandfather from an invisible allow into a visible one.
func (t *AccountToken) IsLegacyUnscoped() bool { return t != nil && t.Scope == nil }

func containsFold(list []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
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

// MintAccountTokenScoped is MintAccountToken with an explicit scope. New keys
// go through here so they are deny-by-default from birth; pass an empty (non-
// nil) TokenScope for "reaches nothing yet", to be filled in by the editor.
func MintAccountTokenScoped(owner, name string, scope *TokenScope) AccountToken {
	t := MintAccountToken(owner, name)
	if scope != nil {
		t.Scope = scope
		if RootDB != nil {
			RootDB.Set(accountTokenTable, t.Token, t)
		}
	}
	return t
}

// SetAccountTokenScope replaces the scope on one of owner's tokens (by ID).
// Owner-scoped so a user can never rescope another user's key. A nil scope is
// rejected — clearing back to legacy-unrestricted is not an editor action, only
// the pre-scoping grandfather produces it. Returns true when a token matched.
func SetAccountTokenScope(owner, id string, scope *TokenScope) bool {
	if RootDB == nil || scope == nil {
		return false
	}
	for _, secret := range RootDB.Keys(accountTokenTable) {
		var t AccountToken
		if RootDB.Get(accountTokenTable, secret, &t) && t.Owner == owner && t.ID == id {
			t.Scope = scope
			RootDB.Set(accountTokenTable, secret, t)
			return true
		}
	}
	return false
}

// AccountTokenFromRequest resolves a request's API key to its full token record
// (scope included), for surfaces that must enforce per-key scope rather than
// only resolve the owner. Returns the raw record — do NOT echo t.Token, it is
// the live secret. nil when no valid account token is presented.
func AccountTokenFromRequest(r *http.Request) *AccountToken {
	secret := rawAPIKey(r)
	if secret == "" || RootDB == nil {
		return nil
	}
	var t AccountToken
	if RootDB.Get(accountTokenTable, secret, &t) && t.Owner != "" {
		return &t
	}
	return nil
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
