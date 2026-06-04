// Desktop bridge key — a per-user credential the gohort-desktop daemon
// uses to authenticate the WS tool bridge (and, as phantom adopts it, the
// iMessage hook/poll) when it can't present the viewer's session cookie.
//
// This is the de-phantom-ified successor to phantom's APIKey: the key
// concept, store, minting, and validator all live in core, so the daemon's
// credential is "this machine's bridge identity for user X," not a phantom
// setting. The viewer (cookie-authed) auto-provisions it via the
// /api/desktop/key endpoint instead of the user pasting one in.
//
// The WS endpoint (HandleDesktopBridge) already accepts the viewer's cookie
// directly, so this key is only needed by the headless daemon.

package core

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// desktopKeyTable is the RootDB table of desktop bridge keys, keyed by ID.
const desktopKeyTable = "desktop_keys"

// DesktopKey is a per-user bridge credential. One active key per user
// (mint is idempotent); rotatable.
type DesktopKey struct {
	ID       string `json:"id"`
	Key      string `json:"key"`   // the secret token (shown to the client)
	Owner    string `json:"owner"` // gohort username the key belongs to
	Created  string `json:"created"`
	LastSeen string `json:"last_seen,omitempty"`
}

func init() {
	// Register the validator at init — RootDB is checked at call time, so
	// it's fine that it isn't open yet. This makes an X-API-Key carrying a
	// desktop key resolve on /api/desktop/ws (and any other endpoint that
	// consults userFromAPIKey) as soon as the store has keys.
	RegisterAPIKeyValidator(LookupDesktopKey)
}

// MintDesktopKey returns the user's existing desktop key, creating one on
// first call (idempotent — one active key per user). Empty user → not ok.
func MintDesktopKey(user string) (string, bool) {
	user = strings.TrimSpace(user)
	if RootDB == nil || user == "" {
		return "", false
	}
	if dk, ok := desktopKeyForUser(user); ok {
		Debug("[desktop_key] returning EXISTING key for user=%q key=%s", user, maskKey(dk.Key))
		return dk.Key, true
	}
	dk := DesktopKey{
		ID:      UUIDv4(),
		Key:     strings.ReplaceAll(UUIDv4()+UUIDv4(), "-", ""),
		Owner:   user,
		Created: time.Now().Format(time.RFC3339),
	}
	RootDB.Set(desktopKeyTable, dk.ID, dk)
	Debug("[desktop_key] minted NEW key for user=%q key=%s len=%d", user, maskKey(dk.Key), len(dk.Key))
	return dk.Key, true
}

// maskKey shows just enough of a key to correlate logs without leaking it.
func maskKey(k string) string {
	if len(k) < 6 {
		return "(short/empty)"
	}
	return k[:6] + "…"
}

// RotateDesktopKey revokes the user's current key(s) and mints a fresh one.
func RotateDesktopKey(user string) (string, bool) {
	user = strings.TrimSpace(user)
	if RootDB == nil || user == "" {
		return "", false
	}
	for _, id := range RootDB.Keys(desktopKeyTable) {
		var dk DesktopKey
		if RootDB.Get(desktopKeyTable, id, &dk) && dk.Owner == user {
			RootDB.Unset(desktopKeyTable, id)
		}
	}
	return MintDesktopKey(user)
}

func desktopKeyForUser(user string) (DesktopKey, bool) {
	for _, id := range RootDB.Keys(desktopKeyTable) {
		var dk DesktopKey
		if RootDB.Get(desktopKeyTable, id, &dk) && dk.Owner == user {
			return dk, true
		}
	}
	return DesktopKey{}, false
}

// LookupDesktopKey resolves a secret to its owner username, updating
// LastSeen. Registered as an API-key validator (init above) so an
// X-API-Key carrying a desktop key authenticates the bridge.
func LookupDesktopKey(secret string) (string, bool) {
	secret = strings.TrimSpace(secret)
	if RootDB == nil || secret == "" {
		return "", false
	}
	n := 0
	for _, id := range RootDB.Keys(desktopKeyTable) {
		var dk DesktopKey
		if RootDB.Get(desktopKeyTable, id, &dk) {
			n++
			if dk.Key == secret {
				dk.LastSeen = time.Now().Format(time.RFC3339)
				RootDB.Set(desktopKeyTable, id, dk)
				return dk.Owner, true
			}
		}
	}
	// No match — log the received key + how many keys ARE stored, so a 401
	// reveals "stale key vs empty store vs wrong store" at a glance.
	Debug("[desktop_key] lookup MISS for received=%s (%d desktop key(s) in store)", maskKey(secret), n)
	return "", false
}

// HandleDesktopKey is the cookie-authed endpoint the desktop client calls
// to auto-provision its bridge key — no manual setup:
//
//	GET / POST /api/desktop/key  → mint-or-return the caller's key, as {"key": "..."}
//	DELETE     /api/desktop/key  → rotate (revoke old, return a fresh one)
//
// Cookie auth (AuthCurrentUser) only — this mints a credential, so it must
// never be reachable via the very key it issues.
func HandleDesktopKey(w http.ResponseWriter, r *http.Request) {
	user := AuthCurrentUser(r)
	if user == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var (
		key string
		ok  bool
	)
	switch r.Method {
	case http.MethodDelete:
		key, ok = RotateDesktopKey(user)
	default: // GET, POST
		key, ok = MintDesktopKey(user)
	}
	if !ok {
		http.Error(w, "could not provision key", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"key": key})
}
