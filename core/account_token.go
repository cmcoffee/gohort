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
	// LastSeen is the last time this key authenticated anything, to the hour.
	//
	// The field existed from the start and nothing ever wrote it, so every key
	// read as never-used and the one question that makes expiry actionable —
	// which of these is nobody using? — had no answer. Written lazily (see
	// touchAccountToken): once an hour per key, not once per request, so the
	// O(1) lookup this store was designed around stays that way.
	LastSeen string `json:"last_seen,omitempty"`
	// Expires bounds the key's life. RFC3339; EMPTY MEANS NEVER, which is what
	// every key minted before this field held, and changing that silently
	// would log people out of integrations they had no warning about.
	//
	// A bearer secret with no expiry is only ever revoked by somebody
	// remembering to. The peer surface solved the same problem with 15-minute
	// access tokens and a rotating refresh family, which is more machinery
	// than a key pasted into a client config wants; an optional deadline plus
	// an honest last-used date is the version that fits here.
	//
	// One trap, since "" is the meaningful cleared state: kvlite encodes
	// through gob, which omits zero values, so a Get that decodes a cleared
	// deadline into a REUSED struct leaves the old date in place. Every reader
	// here declares a fresh AccountToken per record; keep it that way.
	Expires string `json:"expires,omitempty"`

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
	// Tools the key may call over MCP ("ask_agent", "list_agents", …).
	//
	// A POINTER, and deliberately not the deny-by-default of the two fields
	// above. Nil means NOT NARROWED — every exposed tool — because this field
	// arrived after keys existed: a scoped key written before it would
	// otherwise lose every tool the moment the field shipped, which is the
	// breakage the nil-Scope grandfather exists to prevent, one level down.
	// Non-nil is deny-by-default like the rest, INCLUDING an empty list, so a
	// user who unticks everything gets a key that calls nothing rather than a
	// key that silently reverts to all.
	Tools *[]string `json:"tools,omitempty"`
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

// AllowsTool reports whether the key may call an MCP tool by name. Nil scope is
// the legacy grandfather; a nil Tools list inside a scope means the key was
// written before tool scoping and is not narrowed by it.
func (t *AccountToken) AllowsTool(name string) bool {
	if t == nil {
		return false
	}
	if t.Scope == nil || t.Scope.Tools == nil {
		return true
	}
	return containsFold(*t.Scope.Tools, name)
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

// Expired reports whether the key's deadline has passed. A key with no
// deadline never expires.
func (t *AccountToken) Expired() bool {
	if t == nil || strings.TrimSpace(t.Expires) == "" {
		return false
	}
	deadline, err := time.Parse(time.RFC3339, t.Expires)
	if err != nil {
		// An unparseable deadline is treated as expired. The alternative is a
		// key that outlives a corrupted date field forever, and failing closed
		// on a credential costs a re-mint rather than an open door.
		return true
	}
	return time.Now().After(deadline)
}

// accountTokenTouchInterval is how stale LastSeen may get before a request
// writes it. An hour is fine enough to answer "is anyone still using this?"
// and coarse enough that the write is nowhere near the hot path.
const accountTokenTouchInterval = time.Hour

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
	return MintAccountTokenExpiring(owner, name, scope, 0)
}

// MintAccountTokenExpiring mints a key that stops working after ttl. A ttl of
// zero or less means no deadline, which is what every key had before this
// existed.
func MintAccountTokenExpiring(owner, name string, scope *TokenScope, ttl time.Duration) AccountToken {
	t := MintAccountToken(owner, name)
	if scope != nil {
		t.Scope = scope
	}
	if ttl > 0 {
		t.Expires = time.Now().UTC().Add(ttl).Format(time.RFC3339)
	}
	if RootDB != nil {
		RootDB.Set(accountTokenTable, t.Token, t)
	}
	return t
}

// SetAccountTokenExpiry sets or clears the deadline on one of owner's keys.
// A ttl of zero or less clears it (back to never). Owner-scoped, so a user can
// never re-date another user's key. Returns true when a key matched.
func SetAccountTokenExpiry(owner, id string, ttl time.Duration) bool {
	if RootDB == nil {
		return false
	}
	for _, secret := range RootDB.Keys(accountTokenTable) {
		var t AccountToken
		if !RootDB.Get(accountTokenTable, secret, &t) || t.Owner != owner || t.ID != id {
			continue
		}
		if ttl > 0 {
			t.Expires = time.Now().UTC().Add(ttl).Format(time.RFC3339)
		} else {
			t.Expires = ""
		}
		RootDB.Set(accountTokenTable, secret, t)
		return true
	}
	return false
}

// SweepExpiredAccountTokens removes keys past their deadline and returns how
// many went. Expiry is enforced at lookup regardless (see
// lookupAccountTokenOwner); this just keeps the table and the account page
// free of rows that only mean "this stopped working a while ago".
func SweepExpiredAccountTokens() int {
	if RootDB == nil {
		return 0
	}
	n := 0
	for _, secret := range RootDB.Keys(accountTokenTable) {
		var t AccountToken
		if RootDB.Get(accountTokenTable, secret, &t) && t.Expired() {
			RootDB.Unset(accountTokenTable, secret)
			n++
		}
	}
	return n
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
	if RootDB.Get(accountTokenTable, secret, &t) && t.Owner != "" && !t.Expired() {
		return &t
	}
	return nil
}

// ExplicitTarget reports whether this key's scope names the canonical target
// EXPLICITLY. Unlike AllowsTarget it is false for a nil-scope legacy key —
// used where a deliberate per-key grant is itself the CONSENT (e.g. making a
// not-otherwise-exposed agent reachable through this key), which a
// grandfathered wildcard must not imply.
func (t *AccountToken) ExplicitTarget(canonical string) bool {
	if t == nil || t.Scope == nil {
		return false
	}
	return containsFold(t.Scope.Targets, canonical)
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
	if !RootDB.Get(accountTokenTable, secret, &t) || t.Owner == "" {
		return "", false
	}
	if t.Expired() {
		// Removed on sight rather than left to a sweep: a sweep that has not
		// run yet must never be the difference between a credential being
		// valid and not. Same rule peerKeyFromAccessToken applies.
		RootDB.Unset(accountTokenTable, secret)
		Log("[account] key %q (%s) expired at %s — removed", t.Name, t.ID, t.Expires)
		return "", false
	}
	touchAccountToken(secret, t)
	return t.Owner, true
}

// touchAccountToken records that a key was used, at most once an hour.
//
// The comment here used to explain that this function deliberately did not
// exist, to keep authentication a single Get. That reasoning held for the
// read; what it missed is that a key nobody can tell is unused is a key nobody
// ever revokes, which is a worse cost than one write an hour.
func touchAccountToken(secret string, t AccountToken) {
	now := time.Now().UTC()
	if t.LastSeen != "" {
		if seen, err := time.Parse(time.RFC3339, t.LastSeen); err == nil &&
			now.Sub(seen) < accountTokenTouchInterval {
			return
		}
	}
	t.LastSeen = now.Format(time.RFC3339)
	RootDB.Set(accountTokenTable, secret, t)
}

// maskAccountToken renders a non-secret hint of a token for display.
func maskAccountToken(s string) string {
	if len(s) <= 12 {
		return "••••"
	}
	return s[:8] + "…" + s[len(s)-4:]
}
