// Secure API credential layer. Lets the operator register API keys /
// bearer tokens via the admin UI, and exposes one auto-generated tool
// per credential to the LLM. The LLM never sees the secret value:
//
//   - Secret is stored encrypted via kvlite CryptSet.
//   - The handler reads it into a transient header string at call time.
//   - The tool catalog shows the credential's name and allowed URL
//     pattern, but never the value.
//
// URL allowlist enforcement is the linchpin safety property — without
// it the LLM could redirect the credential to an attacker-controlled
// endpoint. Each credential records an `AllowedURLPattern` and the
// handler validates the resolved URL against it before any HTTP call.
//
// v1 supports four credential types: bearer (Authorization: Bearer ...),
// header (custom header name + value), query (custom query param), and
// basic_auth (HTTP basic). OAuth flow is a future addition; in the
// interim, paste a long-lived personal access token.
//
// All operations go through a singleton `*SecureAPI` accessed via
// Secure(). The store binds to the root global DB once (resolved from
// AuthDB) — secure-api credentials are intentionally global, so admin
// (which uses the root) and chat/phantom (which use bucketed views of
// the root) all read and write to the same namespace.

package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// isTimeoutErr reports whether err is a request timeout (deadline exceeded or a
// net timeout) rather than a hard failure like connection-refused or DNS.
func isTimeoutErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

const (
	secureAPITable      = "secure_api_credentials"
	secureAPIAuditTable = "secure_api_audit"
)

// Credential types. Stored as plain strings so admin UI can round-trip.
const (
	SecureCredBearer    = "bearer"
	SecureCredHeader    = "header"
	SecureCredQuery     = "query"
	SecureCredBasicAuth = "basic_auth"
	// SecureCredNone is for unauthenticated public APIs (Open-Meteo,
	// wttr.in, etc.). Carries no secret, injects no auth header, but
	// still goes through the same allow-list, audit, and rate-limit
	// machinery as authenticated credentials. Lets the LLM wrap public
	// HTTPS endpoints as api-mode tools without writing a Python
	// urllib client around them.
	SecureCredNone = "none"
)

// SecureCredential is the public-facing record. Secret is held in a
// separate encrypted entry so listing credentials in the admin UI
// (which doesn't need the value) never has to decrypt it. ParamName
// applies to header (the header name) and query (the query param).
type SecureCredential struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	AllowedURLPattern string `json:"allowed_url_pattern"`
	// BaseURL + AllowedEndpoints are the preferred, clearer split of the
	// allow-list: BaseURL pins the server (e.g. https://192.168.0.1) and
	// AllowedEndpoints is an add/remove list of path globs under it (e.g.
	// "/api/core/**"). A request is allowed when it falls under BaseURL AND
	// its path matches one endpoint; an EMPTY endpoint list means anything
	// under BaseURL. When BaseURL is set it supersedes AllowedURLPattern;
	// legacy credentials carrying only AllowedURLPattern keep working.
	BaseURL          string   `json:"base_url,omitempty"`
	AllowedEndpoints []string `json:"allowed_endpoints,omitempty"`
	Description      string   `json:"description,omitempty"`
	ParamName        string   `json:"param_name,omitempty"`
	RequiresConfirm  bool     `json:"requires_confirm"`
	// CostPerCall, when > 0, prices each dispatched call through this
	// credential into the admin cost chart + per-source breakdown (a "cost
	// hook"). 0 = untracked (a free endpoint). Recorded via RecordExternalCost.
	CostPerCall float64 `json:"cost_per_call,omitempty"`
	// InsecureSkipTLS skips TLS certificate verification for this
	// credential's requests. Required for LAN appliances (firewalls, NAS,
	// switches) that present self-signed certs OR are addressed by IP — no
	// cert can validate against a bare IP. Off by default; it reduces
	// transport security, so the credential's AllowedURLPattern (already the
	// linchpin gate) should be scoped tightly to the one host.
	InsecureSkipTLS bool `json:"insecure_skip_tls,omitempty"`
	// Disabled skips this credential from the auto-generated tool
	// catalog AND blocks dispatch for any wrapped temp tool that
	// references it. Use to temporarily revoke access entirely
	// (suspected misbehavior, vendor outage, etc.) without deleting
	// the encrypted secret + config. Inverted (Disabled rather than
	// Enabled) so that records written before this field was added —
	// which deserialize to the zero value — keep their default-on
	// behavior.
	Disabled bool `json:"disabled,omitempty"`
	// Restricted removes the auto-generated `call_<name>` direct
	// tool from every agent surface (chat, phantom, anywhere). The
	// LLM cannot invoke the credential directly under any
	// circumstance — only admin-approved wrapped temp tools that
	// reference it by name (place_call, etc.) keep dispatching
	// through it. Use this once specific wrappers have been approved
	// so the LLM is forced down a reviewed path instead of
	// improvising against the raw `call_<name>`. Different from
	// Disabled: Disabled kills the credential outright (wrappers
	// fail too); Restricted leaves wrappers working and only removes
	// the direct route. The inverse state is "Open" — direct
	// `call_<name>` exposed for the LLM to improvise against.
	//
	// Renamed from HideFromCatalog (2026-05); pre-rename gob records
	// decode into Restricted=false, so any previously-restricted
	// credential must be re-restricted once after upgrade.
	Restricted bool `json:"restricted,omitempty"`
	// AllowedMethods restricts which HTTP methods the LLM may use
	// against this credential. Empty/nil = all methods allowed
	// (legacy behavior). Set to ["GET","HEAD"] for a read-only
	// credential. Method check happens before any URL or auth work.
	AllowedMethods []string `json:"allowed_methods,omitempty"`
	// DeniedURLPatterns is a blacklist applied AFTER AllowedURLPattern.
	// Useful for "give it Vapi but never the billing endpoints" —
	// allow=https://api.vapi.ai/** + deny=https://api.vapi.ai/billing/**.
	// Glob shape matches AllowedURLPattern (* = single segment, ** = any).
	DeniedURLPatterns []string `json:"denied_url_patterns,omitempty"`
	// MaxCallsPerDay caps successful calls in a rolling 24-hour
	// window, counted from the audit log. 0 = unlimited (legacy).
	// When the cap is hit the dispatcher rejects with a clear "daily
	// cap of N reached for <name>" error so the operator sees it.
	MaxCallsPerDay int `json:"max_calls_per_day,omitempty"`
	// OAuth2 (Type == "oauth2"). Grant selects the flow; the encrypted
	// secret holds the grant's sensitive material (client_secret for
	// client_credentials, RSA private key for jwt_bearer, refresh token for
	// refresh_token). The minted access token is stored encrypted + cached
	// (apiclient TokenStore) and injected as Authorization: Bearer. See
	// secure_api_oauth.go.
	Grant        string `json:"grant,omitempty"`         // client_credentials | jwt_bearer | refresh_token | authorization_code
	AuthorizeURL string `json:"authorize_url,omitempty"` // authorization_code: the provider's consent (authorize) endpoint
	TokenURL     string `json:"token_url,omitempty"`     // OAuth token endpoint (https)
	ClientID     string `json:"client_id,omitempty"`     // client_credentials / refresh_token / password
	Username     string `json:"username,omitempty"`      // password grant: resource-owner username (config, not secret)
	Scope        string `json:"scope,omitempty"`         // requested scopes (space-separated)
	JWTIssuer    string `json:"jwt_issuer,omitempty"`    // jwt_bearer: iss claim
	JWTSubject   string `json:"jwt_subject,omitempty"`   // jwt_bearer: sub claim (optional)
	JWTAudience  string `json:"jwt_audience,omitempty"`  // jwt_bearer: aud (defaults to token_url)
	JWTKeyID     string `json:"jwt_key_id,omitempty"`    // jwt_bearer: kid header (optional)
	// CredScope selects whose secret a request uses:
	//   "" / "shared" — one deployment secret (the admin's), used for every
	//     user's calls (the legacy/default behavior). Right for a shared service
	//     account or a public-ish corporate read API.
	//   "per_user" — each user supplies + stores their OWN secret (key/token),
	//     entered on their Account page. The dispatch loads the CALLING user's
	//     secret (sess.Username); calls error until that user has set theirs.
	//     Right for "act as yourself" — per-user attribution, native permissions,
	//     bounded blast radius. The admin configures everything EXCEPT the secret.
	CredScope  string    `json:"cred_scope,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`

	// Pending is a computed (non-persisted) flag set by List() for an
	// oauth2 credential whose secret is still the "(pending)" draft
	// placeholder — i.e. Builder authored the config but the admin
	// hasn't filled in the client secret yet. Drives the "needs secret"
	// badge in the admin UI.
	Pending bool `json:"pending,omitempty"`
}

// secureCredSecretKey is the DB key under which the encrypted secret
// for a named credential lives. Kept separate from the metadata key
// so listing credentials doesn't pull encrypted blobs into memory.
func secureCredSecretKey(name string) string { return name + "__secret" }

// securePasswordKey is the DB key for the SECOND secret of an oauth2 password
// grant: the resource-owner password. Kept separate from __secret (which holds
// the client_secret) so the two secrets never share a blob (Option B). Only
// the password grant uses it.
func securePasswordKey(name string) string { return name + "__password" }

// SavePassword stores (or, on empty, preserves) the oauth2 password-grant
// resource-owner password. Mirrors Save's "empty means keep existing"
// semantics so an admin editing config without re-typing the password keeps
// it. Encrypted at rest like the primary secret.
func (s *SecureAPI) SavePassword(name, password string) error {
	if !s.ready() || name == "" {
		return fmt.Errorf("name required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(password) == "" {
		return nil // keep existing (or none yet)
	}
	s.db.CryptSet(secureAPITable, securePasswordKey(name), password)
	return nil
}

// loadPassword reads the decrypted password-grant password. Mirrors loadSecret.
func (s *SecureAPI) loadPassword(name string) (string, bool) {
	if !s.ready() {
		return "", false
	}
	var p string
	ok := s.db.Get(secureAPITable, securePasswordKey(name), &p)
	return p, ok
}

// SecureAPIAuditEntry is one row in the audit log. Kept short to
// avoid bloating storage; the full response body is NOT recorded.
type SecureAPIAuditEntry struct {
	CredentialName string    `json:"credential_name"`
	Method         string    `json:"method"`
	URL            string    `json:"url"`
	Status         int       `json:"status"`
	ResponseBytes  int       `json:"response_bytes"`
	Timestamp      time.Time `json:"timestamp"`
	Error          string    `json:"error,omitempty"`
}

// secureAPIMaxResponseBytes caps response bodies returned to the
// LLM as text directly (no pipe). 256KB ≈ ~64K tokens — generous
// for most APIs but leaves room for prompt + tool history within
// a 200K-class context window. Use save_to for large binary
// content; use response_pipe in api-mode tool defs to jq-filter
// large JSON down to needed fields before context.
func secureAPIMaxResponseBytes() int { return TuneInt("tune_secure_api_max_response_bytes") }

// secureAPIMaxResponseBytesForPipe is the higher input cap used
// when the caller has a response_pipe configured. The pipe will
// project the response down to a small output, so reading more
// of it doesn't bloat the LLM's context. This is what enables
// list-style endpoints (Vapi /call?limit=50, etc.) to be safely
// jq-filtered to {id, status, cost} projections — without this,
// the prior 256KB read cap truncated the JSON mid-string and jq
// would fail with "Unfinished string at EOF".
func secureAPIMaxResponseBytesForPipe() int {
	return TuneInt("tune_secure_api_max_response_bytes_pipe")
}

// secureAPIMaxSaveBytes caps response bodies written to the
// workspace via save_to. Higher than the text cap because the
// LLM never reads the bytes — they go straight to disk and the
// LLM only sees a short metadata line. 100MB covers most
// generated audio (10+ minutes of ElevenLabs MP3), short videos,
// PDFs, etc. Larger downloads are still within the workspace
// disk-quota footprint so they don't escape the sandbox.
func secureAPIMaxSaveBytes() int64 { return int64(TuneInt("tune_secure_api_max_save_bytes")) }

// secureAPIRequestTimeout caps wall-clock time per call.
func secureAPIRequestTimeout() time.Duration {
	return TuneDuration("tune_secure_api_request_timeout")
}

// auditRingSize is the per-credential audit-log retention. Older
// entries are dropped FIFO.
func auditRingSize() int { return TuneInt("tune_secure_api_audit_ring_size") }

func init() {
	RegisterTunable(TunableSpec{Key: "tune_secure_api_max_response_bytes", Category: "Limits", Label: "SecureAPI response byte cap", Help: "Max response body returned directly to the LLM as text (no response pipe).", Kind: KindInt, Default: 262144, Min: 16384, Max: 4194304})
	RegisterTunable(TunableSpec{Key: "tune_secure_api_max_response_bytes_pipe", Category: "Limits", Label: "SecureAPI response byte cap (piped)", Help: "Higher response read cap used when a response_pipe is configured.", Kind: KindInt, Default: 4194304, Min: 262144, Max: 67108864})
	RegisterTunable(TunableSpec{Key: "tune_secure_api_max_save_bytes", Category: "Limits", Label: "SecureAPI save byte cap", Help: "Max response body written to the workspace via save_to.", Kind: KindInt, Default: 104857600, Min: 1048576, Max: 1073741824})
	RegisterTunable(TunableSpec{Key: "tune_secure_api_request_timeout", Category: "Timeouts", Label: "SecureAPI request timeout", Help: "Wall-clock cap per SecureAPI call.", Kind: KindSeconds, Default: 30, Min: 5, Max: 300})
	RegisterTunable(TunableSpec{Key: "tune_secure_api_audit_ring_size", Category: "Limits", Label: "SecureAPI audit ring size", Help: "Per-credential audit-log retention; older entries drop FIFO.", Kind: KindInt, Default: 50, Min: 10, Max: 1000})
}

// ----------------------------------------------------------------------
// SecureAPI singleton
// ----------------------------------------------------------------------

// SecureAPI is the singleton accessor for the credential store. Holds
// a reference to the root global DB and serializes mutations under a
// single mutex.
type SecureAPI struct {
	db Database
	mu sync.Mutex
}

var (
	secureAPIInstance   *SecureAPI
	secureAPIInstanceMu sync.Mutex
)

// Secure returns the singleton SecureAPI. The DB binding is resolved
// from AuthDB() the first time it's needed and re-resolved while it's
// still nil (covers very-early-startup paths where AuthDB isn't yet
// wired). Once a non-nil DB is bound it sticks.
func Secure() *SecureAPI {
	secureAPIInstanceMu.Lock()
	defer secureAPIInstanceMu.Unlock()
	if secureAPIInstance == nil {
		secureAPIInstance = &SecureAPI{}
	}
	if secureAPIInstance.db == nil && AuthDB != nil {
		secureAPIInstance.db = AuthDB()
		// One-time cleanup: legacy "none" / "no_auth" credential
		// records were used to back the unauthenticated fetch path.
		// fetch_url now handles that natively (no credential record
		// involved), so any vestigial rows from prior versions are
		// dead weight in the admin UI. Remove them on first attach.
		secureAPIInstance.cleanupLegacyNoAuth()
		secureAPIInstance.migrateLegacyURLPatterns()
	}
	return secureAPIInstance
}

// migrateLegacyURLPatterns retires the legacy single-glob
// AllowedURLPattern from records where its meaning is exactly
// preservable as Base URL + (empty) Allowed Endpoints — i.e. plain
// prefix globs like "https://api.github.com/**". Two overlapping
// scoping fields on one record kept misleading both LLMs and admins
// about which one applied; the admin form no longer offers the legacy
// field, and this converges old records onto the one model. Patterns
// with mid-string globs are left untouched — the runtime fallback
// still honors them. Best-effort, idempotent, runs on first attach.
func (s *SecureAPI) migrateLegacyURLPatterns() {
	if s == nil || s.db == nil {
		return
	}
	for _, c := range s.List() {
		if strings.TrimSpace(c.BaseURL) != "" {
			continue
		}
		p := strings.TrimSpace(c.AllowedURLPattern)
		base := strings.TrimSuffix(p, "/**")
		if base == p || base == "" {
			continue // not a plain prefix glob
		}
		if strings.Contains(base, "*") ||
			(!strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://")) {
			continue
		}
		c.BaseURL = base
		c.AllowedURLPattern = ""
		s.db.Set(secureAPITable, c.Name, c)
		Log("[secure_api] migrated credential %q: legacy allowed_url_pattern %q → base_url %q (empty Allowed Endpoints = every path under it)", c.Name, p, base)
	}
}

// ensureNoAuthCredential installs a credential literally named
// cleanupLegacyNoAuth removes any legacy "none" or "no_auth"
// credential records left over from earlier versions. The
// unauthenticated fetch path now lives entirely in fetch_url's own
// runImpl — there's no credential record to back it. Leftover rows
// would just clutter the admin UI and confuse operators.
//
// Best-effort: failures are logged at Debug and don't block startup.
// Idempotent — re-runs on every process attach.
func (s *SecureAPI) cleanupLegacyNoAuth() {
	if s == nil || s.db == nil {
		return
	}
	for _, name := range []string{"none", "no_auth"} {
		if _, ok := s.Load(name); !ok {
			continue
		}
		if err := s.Delete(name); err != nil {
			Debug("[secure_api] cleanup of legacy %q credential failed: %v", name, err)
			continue
		}
		Log("[secure_api] removed vestigial %q credential — fetch_url now handles unauthenticated HTTP directly", name)
	}
}

// ready returns true when the store has a usable DB binding.
func (s *SecureAPI) ready() bool { return s != nil && s.db != nil }

// ----------------------------------------------------------------------
// CRUD
// ----------------------------------------------------------------------

// Save upserts a credential record and (re)stores its secret value
// encrypted. Validates inputs.
func (s *SecureAPI) Save(c SecureCredential, secret string) error {
	if !s.ready() {
		return fmt.Errorf("secure-api store not initialized (AuthDB unset)")
	}
	if !validToolNameStr(c.Name) {
		return fmt.Errorf("name must be lowercase letters/digits/underscores only")
	}
	switch c.Type {
	case SecureCredBearer, SecureCredHeader, SecureCredQuery, SecureCredBasicAuth, SecureCredNone:
	case SecureCredOAuth2:
		switch c.Grant {
		case OAuthGrantClientCredentials, OAuthGrantJWTBearer, OAuthGrantRefreshToken, OAuthGrantPassword:
		default:
			return fmt.Errorf("oauth2 credential needs a grant: client_credentials, jwt_bearer, refresh_token, or password")
		}
		if strings.TrimSpace(c.TokenURL) == "" {
			return fmt.Errorf("oauth2 credential needs a token_url (the https token endpoint)")
		}
		if !strings.HasPrefix(strings.ToLower(c.TokenURL), "https://") {
			return fmt.Errorf("oauth2 token_url must be https")
		}
		if c.Grant == OAuthGrantJWTBearer && strings.TrimSpace(c.JWTIssuer) == "" {
			return fmt.Errorf("jwt_bearer grant needs jwt_issuer (the iss claim)")
		}
		if c.Grant == OAuthGrantPassword && strings.TrimSpace(c.Username) == "" {
			return fmt.Errorf("password grant needs a username (the resource-owner username)")
		}
		// Config may have changed: drop any cached client + stored token so
		// the next call rebuilds from the new config.
		invalidateOAuthClient(c.Name)
	default:
		return fmt.Errorf("type must be bearer, header, query, basic_auth, oauth2, or none")
	}
	if (c.Type == SecureCredHeader || c.Type == SecureCredQuery) && strings.TrimSpace(c.ParamName) == "" {
		return fmt.Errorf("type %q requires a param_name", c.Type)
	}
	if strings.TrimSpace(c.BaseURL) == "" && strings.TrimSpace(c.AllowedURLPattern) == "" {
		return fmt.Errorf("a base_url (e.g. https://192.168.0.1) or an allowed_url_pattern is required")
	}
	if b := strings.TrimSpace(c.BaseURL); b != "" {
		if !strings.HasPrefix(strings.ToLower(b), "https://") && !strings.HasPrefix(strings.ToLower(b), "http://") {
			return fmt.Errorf("base_url must start with https:// or http://")
		}
	} else if !strings.HasPrefix(c.AllowedURLPattern, "https://") && !strings.HasPrefix(c.AllowedURLPattern, "http://") {
		return fmt.Errorf("allowed_url_pattern must start with http:// or https://")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Preserve CreatedAt + LastUsedAt across updates.
	var existing SecureCredential
	exists := s.db.Get(secureAPITable, c.Name, &existing)
	if exists {
		c.CreatedAt = existing.CreatedAt
		c.LastUsedAt = existing.LastUsedAt
		// Disabled + Restricted are owned by the enable/disable and
		// secure/open action endpoints, never the upsert form (which
		// doesn't carry those fields). Preserve them so editing a
		// credential's config can't silently re-enable or un-secure it.
		c.Disabled = existing.Disabled
		c.Restricted = existing.Restricted
	} else {
		c.CreatedAt = time.Now()
	}
	// Type "none" carries no secret. Persist a placeholder so the
	// dispatch path's loadSecret check still finds something (and
	// can short-circuit safely), and skip all the secret-required
	// branching below.
	if c.Type == SecureCredNone {
		s.db.Set(secureAPITable, c.Name, c)
		s.db.CryptSet(secureAPITable, secureCredSecretKey(c.Name), "(none)")
		return nil
	}
	// Secret is required on first save, optional on update — empty
	// secret on update means "leave the existing encrypted value in
	// place." Lets the operator change the URL pattern, description,
	// or other metadata without re-pasting the key on every edit.
	if strings.TrimSpace(secret) == "" {
		if !exists {
			return fmt.Errorf("secret value is required for new credentials")
		}
		var existingSecret string
		if !s.db.Get(secureAPITable, secureCredSecretKey(c.Name), &existingSecret) || existingSecret == "" {
			return fmt.Errorf("secret value is required (no existing secret to preserve)")
		}
		// Keep existing secret; just upsert the metadata record.
		s.db.Set(secureAPITable, c.Name, c)
		return nil
	}
	s.db.Set(secureAPITable, c.Name, c)
	s.db.CryptSet(secureAPITable, secureCredSecretKey(c.Name), secret)
	return nil
}

// Load fetches the public metadata for a credential by name. The
// legacy alias "none" resolves to "no_auth" so an LLM (or old record)
// still using the pre-migration name finds the renamed credential
// without a hard-error; matches migrateNoneRefsInRecords on the
// stored-record side.
func (s *SecureAPI) Load(name string) (SecureCredential, bool) {
	var c SecureCredential
	if !s.ready() || name == "" {
		return c, false
	}
	if name == "none" {
		name = "no_auth"
	}
	ok := s.db.Get(secureAPITable, name, &c)
	return c, ok
}

// List returns metadata for every registered credential. Secrets are
// not included.
func (s *SecureAPI) List() []SecureCredential {
	if !s.ready() {
		return nil
	}
	var out []SecureCredential
	for _, k := range s.db.Keys(secureAPITable) {
		// Skip the per-credential secret blobs (shared __secret, password, and
		// the per-user __usecret__<user> keys) — only metadata records list.
		if strings.Contains(k, "__") {
			continue
		}
		var c SecureCredential
		if s.db.Get(secureAPITable, k, &c) {
			out = append(out, c)
		}
	}
	return out
}

// PerUserConnection describes one per_user credential's state for a given user —
// for the Account page. Never carries the secret value.
type PerUserConnection struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Connected   bool   `json:"connected"`
	OAuth       bool   `json:"oauth"` // true → connect via the consent flow (Connect button), false → paste a key
	// Kind identifies which subsystem owns this connection so the Account panel
	// routes Connect/Disconnect correctly: "" (SecureAPI, the default) or "mcp"
	// (a per-user OAuth MCP server). ConnectURL, when set, is the account-relative
	// consent path for OAuth kinds that don't use the default SecureAPI route.
	Kind       string `json:"kind,omitempty"`
	ConnectURL string `json:"connect_url,omitempty"`
}

// PerUserConnectionsFor returns the per_user credentials a user can connect, each
// flagged connected/not + whether it's OAuth (consent) vs a pasted key. Disabled
// credentials are omitted. Used by the Account page's Connected-accounts section.
func (s *SecureAPI) PerUserConnectionsFor(user string) []PerUserConnection {
	var out []PerUserConnection
	for _, c := range s.List() {
		if !c.IsPerUser() || c.Disabled {
			continue
		}
		out = append(out, PerUserConnection{
			Name:        c.Name,
			Description: c.Description,
			Connected:   s.UserConnected(c, user),
			OAuth:       c.IsAuthCode(),
		})
	}
	return out
}

// ListWithPending is List() plus the computed "needs secret" flag on each
// authenticated credential (an unfinished draft still holding the "(pending)"
// placeholder secret). It reads + decrypts the secret per credential, so it is
// the ADMIN-facing variant ONLY — the hot tool-building path (BuildTools) uses
// plain List() to avoid that per-credential read.
func (s *SecureAPI) ListWithPending() []SecureCredential {
	creds := s.List()
	for i := range creds {
		creds[i].Pending = s.draftPending(creds[i].Name, creds[i].Type)
	}
	return creds
}

// draftPending reports whether an authenticated credential still holds the
// "(pending)" placeholder secret (config authored by Builder, secret not yet
// supplied by the admin). Generalizes the old oauth-only check to every type
// — drives the "NEEDS SECRET" badge for draft_api_credential drafts too. A
// "none" credential carries no secret, so it is never pending.
func (s *SecureAPI) draftPending(name, ctype string) bool {
	if ctype == SecureCredNone {
		return false
	}
	secret, ok := s.loadSecret(name)
	return !ok || secret == "" || secret == "(pending)"
}

// SetDisabled toggles the per-credential disabled flag.
func (s *SecureAPI) SetDisabled(name string, disabled bool) error {
	if !s.ready() || name == "" {
		return fmt.Errorf("name required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var c SecureCredential
	if !s.db.Get(secureAPITable, name, &c) {
		return fmt.Errorf("credential %q not found", name)
	}
	c.Disabled = disabled
	s.db.Set(secureAPITable, name, c)
	return nil
}

// SetRestricted toggles the per-credential Restricted flag. When
// true, the credential becomes wrapper-only: dispatchable through
// admin-approved api-mode temp tools but no longer producing a
// `call_<name>` direct tool in any agent's catalog. Used to
// "graduate" a credential from Open (improvisation allowed) to
// Restricted (only reviewed wrappers can use it).
func (s *SecureAPI) SetRestricted(name string, restricted bool) error {
	if !s.ready() || name == "" {
		return fmt.Errorf("name required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var c SecureCredential
	if !s.db.Get(secureAPITable, name, &c) {
		return fmt.Errorf("credential %q not found", name)
	}
	c.Restricted = restricted
	s.db.Set(secureAPITable, name, c)
	return nil
}

// Delete removes both metadata and encrypted secret.
func (s *SecureAPI) Delete(name string) error {
	if !s.ready() || name == "" {
		return fmt.Errorf("name required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.Unset(secureAPITable, name)
	s.db.Unset(secureAPITable, secureCredSecretKey(name))
	for _, k := range s.db.Keys(secureAPIAuditTable) {
		if strings.HasPrefix(k, name+":") {
			s.db.Unset(secureAPIAuditTable, k)
		}
	}
	return nil
}

// LoadAudit returns the most recent audit entries for a credential,
// newest first.
func (s *SecureAPI) LoadAudit(name string) []SecureAPIAuditEntry {
	if !s.ready() || name == "" {
		return nil
	}
	var entries []SecureAPIAuditEntry
	if s.db.Get(secureAPIAuditTable, name, &entries) {
		return entries
	}
	return nil
}

// recordAudit prepends an entry, capping ring size.
func (s *SecureAPI) recordAudit(e SecureAPIAuditEntry) {
	if !s.ready() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []SecureAPIAuditEntry
	s.db.Get(secureAPIAuditTable, e.CredentialName, &entries)
	entries = append([]SecureAPIAuditEntry{e}, entries...)
	if ringSize := auditRingSize(); len(entries) > ringSize {
		entries = entries[:ringSize]
	}
	s.db.Set(secureAPIAuditTable, e.CredentialName, entries)
}

// touch bumps LastUsedAt for the named credential. Best-effort.
func (s *SecureAPI) touch(name string) {
	if !s.ready() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var c SecureCredential
	if !s.db.Get(secureAPITable, name, &c) {
		return
	}
	c.LastUsedAt = time.Now()
	s.db.Set(secureAPITable, name, c)
}

// loadSecret returns the decrypted SHARED secret value for a credential.
func (s *SecureAPI) loadSecret(name string) (string, bool) {
	if !s.ready() {
		return "", false
	}
	var secret string
	ok := s.db.Get(secureAPITable, secureCredSecretKey(name), &secret)
	return secret, ok
}

// IsPerUser reports whether a credential is scoped so each user supplies their
// own secret.
func (c SecureCredential) IsPerUser() bool { return c.CredScope == "per_user" }

// secureCredUserSecretKey is the DB key for one user's secret on a per_user
// credential. Kept distinct from the shared __secret key so the two never mix.
func secureCredUserSecretKey(name, user string) string { return name + "__usecret__" + user }

// SaveUserSecret stores (encrypted) a user's own secret for a per_user
// credential. Empty value clears it (the user disconnecting).
func (s *SecureAPI) SaveUserSecret(name, user, value string) error {
	if !s.ready() || name == "" || user == "" {
		return fmt.Errorf("name and user required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(value) == "" {
		s.db.Unset(secureAPITable, secureCredUserSecretKey(name, user))
		return nil
	}
	s.db.CryptSet(secureAPITable, secureCredUserSecretKey(name, user), value)
	return nil
}

// loadUserSecret reads one user's decrypted secret for a per_user credential.
func (s *SecureAPI) loadUserSecret(name, user string) (string, bool) {
	if !s.ready() || user == "" {
		return "", false
	}
	var secret string
	ok := s.db.Get(secureAPITable, secureCredUserSecretKey(name, user), &secret)
	return secret, ok
}

// HasUserSecret reports whether a user has set their secret for a credential —
// never reveals the value (for the Account page's connected/disconnected badge).
func (s *SecureAPI) HasUserSecret(name, user string) bool {
	v, ok := s.loadUserSecret(name, user)
	return ok && strings.TrimSpace(v) != ""
}

// resolveSecret picks the secret to dispatch with: the calling user's own for a
// per_user credential, else the shared secret. user comes from the tool session.
func (s *SecureAPI) resolveSecret(c SecureCredential, user string) (string, bool) {
	if c.IsPerUser() {
		return s.loadUserSecret(c.Name, user)
	}
	return s.loadSecret(c.Name)
}

// CredentialStatus reports a credential's readiness as plain booleans — never
// the secret value. Lets Builder verify a credential it drafted is actually
// configured (enabled + secret pasted) before declaring a dependent build
// complete, while keeping the secret out of the LLM's context entirely.
func (s *SecureAPI) CredentialStatus(name string) (exists, enabled, hasSecret bool) {
	c, ok := s.Load(name)
	if !ok {
		return false, false, false
	}
	_, hasSecret = s.loadSecret(name)
	return true, !c.Disabled, hasSecret
}

// ----------------------------------------------------------------------
// Tool generation + dispatch
// ----------------------------------------------------------------------

// BuildTools converts every enabled registered credential into an
// AgentToolDef. The handler closure captures the credential's metadata
// + the calling session (so the save_to param can resolve to the
// session's workspace); secrets are loaded fresh at call time so
// rotations take effect immediately for in-flight sessions.
//
// sess may be nil for callers that don't have a session (e.g. tool
// catalog enumeration outside a request). With a nil session, save_to
// becomes unusable but text-mode responses still work.
//
// Respects both Disabled (hard kill) and Restricted (the admin
// "Restrict" toggle). A restricted credential is unreachable as a
// direct call_<name> tool from any agent surface — chat, phantom,
// anywhere — but admin-approved wrapped tools that reference it
// (api-mode temp tools) still dispatch through DispatchToolCall.
// That's the whole point: force the LLM through a reviewed wrapper
// instead of improvising against the raw credential.
func (s *SecureAPI) BuildTools(sess *ToolSession) []AgentToolDef {
	if !s.ready() {
		return nil
	}
	allKeys := s.db.Keys(secureAPITable)
	creds := s.List()
	out := make([]AgentToolDef, 0, len(creds))
	for _, c := range creds {
		if c.Disabled || c.Restricted {
			// Disabled = hard kill; Restricted = wrapper-only. Either
			// way, no direct LLM-callable exposure. Dispatch via
			// DispatchToolCall (used by api-mode temp tools) is
			// unaffected by Restricted.
			continue
		}
		// SecureCredNone (the bootstrapped "no_auth" credential) is
		// the policy storage for the bare fetch_url tool — operators
		// tune its AllowedURLPattern / rate limit / audit verbosity to
		// scope unauthenticated HTTP. Do NOT generate a separate
		// fetch_url_no_auth tool — fetch_url already covers that path
		// and dispatches through this credential for the same policy.
		// Exposing it as a parallel LLM-callable would be a redundant
		// catalog entry and force the LLM to disambiguate two tools
		// that do the same thing.
		if c.Type == SecureCredNone {
			continue
		}
		out = append(out, s.agentToolFromCredential(c, sess))
	}
	// Decode-mismatch diagnostic: only fires on the genuine failure
	// shape — the table has keys but nothing deserialized into a
	// SecureCredential. Empty-output states caused by Disabled or
	// Restricted are normal and shouldn't emit noise.
	if len(allKeys) > 0 && len(creds) == 0 {
		Debug("[secure_api] BuildTools: %d keys in table but 0 credentials decoded — check struct compat against persisted records", len(allKeys))
	}
	return out
}

func (s *SecureAPI) agentToolFromCredential(c SecureCredential, sess *ToolSession) AgentToolDef {
	// fetch_url_<credential> mirrors fetch_url's shape (method / body /
	// request_headers / save_to) but injects this credential's auth
	// server-side and bounds the URL space to the credential's
	// AllowedURLPattern. Visually groups with fetch_url in the
	// alphabetical catalog so related tools cluster together. The
	// legacy call_<credential> name still resolves at dispatch time
	// (see normalizeLegacyCallToolName) so existing AllowedTools
	// lists and authored temp tools don't break.
	toolName := "fetch_url_" + c.Name
	desc := fmt.Sprintf(
		"Fetch a URL on the %s API. Same shape as fetch_url (method / body / request_headers / save_to all supported), but the auth credential is injected server-side and the URL is bounded by an allow-list. You do not see the credential value. Allowed URLs: %s. %s",
		c.Name, c.AllowedURLPattern, c.Description,
	)
	desc = strings.TrimSpace(desc)
	return AgentToolDef{
		Tool: Tool{
			Name:        toolName,
			Description: desc,
			Parameters: map[string]ToolParam{
				"url": {
					Type:        "string",
					Description: "Full URL to call. Must match the credential's allowed URL pattern: " + c.AllowedURLPattern,
				},
				"method": {
					Type:        "string",
					Description: "HTTP method. Defaults to GET. Use POST/PUT/PATCH/DELETE for write operations.",
				},
				"body": {
					Type:        "string",
					Description: "Optional request body (typically JSON-encoded). Sent as-is with Content-Type: application/json unless overridden by request_headers.",
				},
				"request_headers": {
					Type:        "object",
					Description: "Optional extra headers as a {name: value} object. Cannot override the auth header — that's set by the credential.",
				},
				"save_to": {
					Type:        "string",
					Description: "Optional. Workspace-relative path to write the response body to as raw bytes (e.g. \"voice.mp3\", \"report.pdf\"). Use for binary responses (audio, image, PDF, archive) — without this, binary content returns as garbled text in the tool result. When set, the tool result is a short metadata line (status, size, content-type, path) instead of the body. Pair with attach_file to deliver the saved file to the user.",
				},
			},
			Required: []string{"url"},
			Caps:     []Capability{CapNetwork},
		},
		NeedsConfirm: c.RequiresConfirm,
		Handler: func(args map[string]any) (string, error) {
			return s.dispatch(c, args, sess)
		},
	}
}

// DispatchToolCall is the entry point used by api-mode temp tools
// (create_api_tool). Loads the named credential and dispatches a
// pre-resolved request through the same path as the auto-generated
// per-credential tool. sess provides workspace context for save_to;
// nil disables the save_to capability for this call.
func (s *SecureAPI) DispatchToolCall(sess *ToolSession, credName, urlStr, method, body string) (string, error) {
	return s.dispatchToolCall(sess, credName, urlStr, method, body, false)
}

// DispatchToolCallForPipe is identical to DispatchToolCall but signals
// that the caller will run the response through a sandboxed pipe
// (jq/awk/sed) before exposing it to the LLM. The internal dispatch
// reads with a higher byte cap (secureAPIMaxResponseBytesForPipe) and
// skips the output truncation marker, since the pipe will project the
// body down to a small output that fits in context. Use only when a
// pipe is genuinely going to run; calling this without piping leaks
// the full response into LLM context.
func (s *SecureAPI) DispatchToolCallForPipe(sess *ToolSession, credName, urlStr, method, body string) (string, error) {
	return s.dispatchToolCall(sess, credName, urlStr, method, body, true)
}

func (s *SecureAPI) dispatchToolCall(sess *ToolSession, credName, urlStr, method, body string, pipeFollowing bool) (string, error) {
	if credName == "" {
		return "", fmt.Errorf("credential name required")
	}
	// Legacy "no_auth" / "none" references — api-mode temp tools
	// authored before fetch_url absorbed the unauthenticated path.
	// Synthesize a SecureCredNone credential on the fly so the
	// existing dispatch machinery still runs (URL pattern wide-open,
	// no auth header, audit log + rate limit through the standard
	// path). Lets persisted tools keep working without a DB record.
	if credName == "no_auth" || credName == "none" {
		synth := SecureCredential{
			Name:              "no_auth",
			Type:              SecureCredNone,
			AllowedURLPattern: "https://**",
			Description:       "Synthesized unauthenticated dispatch — back-compat for tools authored before fetch_url subsumed this path.",
		}
		args := map[string]any{
			"url":    urlStr,
			"method": method,
		}
		if body != "" {
			args["body"] = body
		}
		return s.dispatch(synth, args, sess)
	}
	c, ok := s.Load(credName)
	if !ok {
		return "", fmt.Errorf("credential %q not registered", credName)
	}
	args := map[string]any{
		"url":    urlStr,
		"method": method,
	}
	if body != "" {
		args["body"] = body
	}
	if pipeFollowing {
		args["__pipe_following"] = true
	}
	return s.dispatch(c, args, sess)
}

// dispatch is the handler logic for one credential's tool. Validates
// the URL, reads the encrypted secret, builds the request with auth
// attached, executes, and either returns the response body as text
// (capped at secureAPIMaxResponseBytes) or writes raw bytes to a
// workspace file when save_to is set (capped at secureAPIMaxSaveBytes).
//
// sess is consulted only for the save_to path, which needs a workspace
// dir. nil sess just disables save_to.
func (s *SecureAPI) dispatch(c SecureCredential, args map[string]any, sess *ToolSession) (string, error) {
	if !s.ready() {
		return "", fmt.Errorf("secure-api store not initialized")
	}
	// Fail-closed on Disabled. BuildTools already hides disabled
	// credentials from the LLM catalog, but pre-existing watchers and
	// approved api-mode temp tools dispatch through this path
	// directly and would otherwise keep firing. Restricted is the
	// soft variant — it stays dispatchable on purpose so vetted
	// wrappers keep working — Disabled is the hard kill switch.
	if c.Disabled {
		return "", fmt.Errorf("credential %q is disabled", c.Name)
	}
	rawURL := strings.TrimSpace(StringArg(args, "url"))
	if rawURL == "" {
		return "", fmt.Errorf("url is required")
	}
	// Resolve a path-only URL against the credential's Base URL. LLMs
	// routinely author api-mode tools with url_template "/v1/posts"
	// (natural, since the credential already names the host) — but the
	// allowlist below matches raw strings, so a relative path could
	// NEVER pass it, and the resulting refusal blamed the credential's
	// config. The admin then chased base_url/endpoint "fixes" that
	// couldn't help (observed: a four-round misconfiguration spiral).
	// Joining here makes the natural authoring shape work and keeps
	// the allowlist semantics unchanged for absolute URLs.
	if strings.HasPrefix(rawURL, "/") && !strings.HasPrefix(rawURL, "//") {
		if base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"); base != "" {
			rawURL = base + rawURL
		} else {
			return "", fmt.Errorf("url %q is a path with no host, and credential %q has no Base URL to resolve it against — author the tool with an absolute https:// URL, or set the credential's Base URL", rawURL, c.Name)
		}
	}
	method := strings.ToUpper(strings.TrimSpace(StringArg(args, "method")))
	if method == "" {
		method = "GET"
	}
	body := StringArg(args, "body")
	saveTo := strings.TrimSpace(StringArg(args, "save_to"))
	if saveTo != "" && (sess == nil || sess.WorkspaceDir == "") {
		return "", fmt.Errorf("save_to requires a session with WorkspaceDir set")
	}
	// Pre-validate the save path before firing the request — no point
	// fetching megabytes of audio just to discover the path was bad.
	var savePath string
	if saveTo != "" {
		resolved, err := ResolveWorkspacePath(sess.WorkspaceDir, saveTo)
		if err != nil {
			return "", fmt.Errorf("save_to: %w", err)
		}
		savePath = resolved
	}

	// Method allowlist check. Empty list = all methods allowed
	// (legacy). When set, anything outside the list is rejected
	// before URL/auth work — a read-only credential with
	// AllowedMethods=["GET","HEAD"] denies POST/DELETE etc. cleanly.
	if len(c.AllowedMethods) > 0 {
		ok := false
		for _, m := range c.AllowedMethods {
			if strings.ToUpper(strings.TrimSpace(m)) == method {
				ok = true
				break
			}
		}
		if !ok {
			return "", fmt.Errorf("method %s not allowed for credential %q (allowed: %v)", method, c.Name, c.AllowedMethods)
		}
	}

	// URL allowlist check. THIS IS THE LINCHPIN. If the LLM somehow
	// produces a URL outside the allowed pattern, we refuse — no
	// header is ever attached.
	if !urlAllowedByCredential(c, rawURL) {
		// Render the SEMANTICS of an empty endpoint list, not the bare
		// "[]" — models (and admins) reliably misread "endpoints=[]"
		// as "nothing is allowed" when empty actually means everything
		// under base_url. Show the resolved meaning inline, where it's
		// read, instead of hoping a rule sentence elsewhere wins.
		eps := "(empty — every path under base_url is allowed; the endpoint list is NOT the problem)"
		if len(c.AllowedEndpoints) > 0 {
			eps = fmt.Sprintf("%v", c.AllowedEndpoints)
		}
		baseErr := fmt.Sprintf("url %q is not allowed for credential %q (base_url=%q, endpoints=%s", rawURL, c.Name, c.BaseURL, eps)
		if p := strings.TrimSpace(c.AllowedURLPattern); p != "" {
			baseErr += fmt.Sprintf(", legacy_pattern=%q", p)
		}
		baseErr += ")"
		// Say WHICH check failed. The raw config dump alone has sent
		// both LLMs and admins down the wrong path — "endpoints=[]"
		// reads as "nothing is allowed" when empty actually means
		// EVERYTHING under base_url, and a www-vs-bare-host mismatch
		// is invisible unless someone diffs the strings by eye.
		if base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"); base != "" {
			if rawURL != base && !strings.HasPrefix(rawURL, base+"/") {
				baseErr += fmt.Sprintf(". DIAGNOSIS: the request's scheme+host does not match Base URL %q — they must match EXACTLY (https vs http, and www.host vs bare host count as DIFFERENT hosts). The Allowed Endpoints list is NOT the problem. Fix: correct the Base URL, or author the tool with a path-only url so it inherits the credential's host", c.BaseURL)
			} else {
				baseErr += fmt.Sprintf(". DIAGNOSIS: the host matches; the PATH is outside Allowed Endpoints %v. An EMPTY list allows every path under Base URL; a non-empty list allows ONLY the listed patterns — add the missing pattern or clear the list", c.AllowedEndpoints)
			}
		}
		return "", credMisconfigEscalation(c.Name, baseErr, noteCredRejection(c.Name))
	}

	// URL deny patterns: applied after the allowlist for fine-grained
	// carve-outs ("allow Vapi but never /billing/**"). Each pattern
	// uses the same glob shape as AllowedURLPattern.
	for _, deny := range c.DeniedURLPatterns {
		deny = strings.TrimSpace(deny)
		if deny == "" {
			continue
		}
		if urlMatchesPattern(rawURL, deny) {
			return "", fmt.Errorf("url %q matches deny pattern %q for credential %q", rawURL, deny, c.Name)
		}
	}

	// Daily call cap: count successful (non-error) audit entries in
	// the last 24h. Non-zero MaxCallsPerDay activates the cap.
	if c.MaxCallsPerDay > 0 {
		cutoff := time.Now().Add(-24 * time.Hour)
		count := 0
		for _, e := range s.LoadAudit(c.Name) {
			if e.Timestamp.Before(cutoff) {
				continue
			}
			if e.Error != "" {
				continue // failed calls don't count toward the cap
			}
			count++
		}
		if count >= c.MaxCallsPerDay {
			return "", fmt.Errorf("daily cap of %d reached for credential %q (counted %d successful calls in the last 24h) — raise the cap in admin if this is legitimate", c.MaxCallsPerDay, c.Name, count)
		}
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	// http is allowed only when the credential's allow-list explicitly opts in
	// (e.g. a LAN appliance served over http). Honor BOTH the new BaseURL
	// scheme and the legacy AllowedURLPattern prefix — checking only the legacy
	// field left http base_url credentials unable to make any call.
	allowsHTTP := strings.HasPrefix(c.AllowedURLPattern, "http://") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.BaseURL)), "http://")
	if parsed.Scheme != "https" && !allowsHTTP {
		baseErr := fmt.Sprintf("non-https URL not allowed for credential %q (set the credential's Base URL to http:// if the host is plain-http)", c.Name)
		return "", credMisconfigEscalation(c.Name, baseErr, noteCredRejection(c.Name))
	}
	// Both config gates passed — the credential can reach this URL; reset the
	// repeated-rejection escalation so a later, unrelated mistake starts fresh.
	clearCredRejections(c.Name)

	// Type "none" carries no auth — public unauthenticated endpoints.
	// Skip the secret-load gate; the dispatch switch below also skips
	// the auth-injection branch for this type. AllowedURLPattern,
	// audit logging, rate limits, and HTTPS enforcement still apply.
	var secret string
	if c.Type != SecureCredNone {
		callUser := ""
		if sess != nil {
			callUser = sess.Username
		}
		if c.IsAuthCode() {
			// Interactive OAuth: use the CALLING user's per-user access token
			// (refreshed when expired). secret holds the bearer token; the OAuth2
			// injection branch below uses it directly instead of minting one.
			tctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			tok, terr := s.userAccessToken(tctx, c, callUser)
			cancel()
			if terr != nil || tok == "" {
				return "", fmt.Errorf("you haven't connected your %q account yet — click Connect on your Account page (Connected accounts)", c.Name)
			}
			secret = tok
		} else {
			var ok bool
			secret, ok = s.resolveSecret(c, callUser)
			if !ok || secret == "" {
				if c.IsPerUser() {
					return "", fmt.Errorf("you haven't connected your %q account yet — set your key on your Account page (Connected accounts)", c.Name)
				}
				return "", fmt.Errorf("credential %q has no stored secret (re-add it via the admin UI)", c.Name)
			}
		}
	}

	// Derive ctx from the session's NetworkConnector so a Private
	// toggle mid-flight CANCELS this HTTP call (context.Canceled
	// propagates through client.Do, returning early instead of
	// completing the request). Layer the per-call timeout on top so
	// the call still has its own deadline cap.
	var connector *NetworkConnector
	if sess != nil {
		connector = sess.Network
	}
	// Network egress is OFF for this turn (Private mode). Fail fast with a CLEAR
	// reason — otherwise the request cancels and surfaces as a generic "context
	// canceled" that the model misreads as the host being down (it then tells
	// the user the firewall is unreachable, which is wrong).
	if connector != nil && !connector.Allowed() {
		return "", fmt.Errorf("blocked by Private mode: network egress is OFF for this turn, so the call to %q was NOT attempted. The credential and host are fine — this is a local privacy setting, NOT a connectivity or firewall problem. To reach it, turn off Private mode on this agent (or dispatch to an agent that has network). Do NOT report the host as down or unreachable", rawURL)
	}
	baseCtx, releaseConn := connector.DeriveCancelCtx(context.Background())
	defer releaseConn()
	ctx, cancel := context.WithTimeout(baseCtx, secureAPIRequestTimeout())
	defer cancel()

	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), bodyReader)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}

	// Default User-Agent — matches the sandbox hook fetch + fetch_url.
	// Terse, opaque, no version, no reference URL — slips past the
	// dumb anti-bot heuristics that trip on "Go-http-client",
	// "+http://" reference patterns, or words like "hook"/"bot".
	// Caller-supplied request_headers["User-Agent"] overrides.
	req.Header.Set("User-Agent", "gohort/call")

	// Caller-supplied headers first; auth applied last so it can't
	// be overridden.
	if hdrs, ok := args["request_headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if str, ok := v.(string); ok {
				lower := strings.ToLower(k)
				if lower == "authorization" || lower == "proxy-authorization" {
					continue
				}
				req.Header.Set(k, str)
			}
		}
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	var oauthBearer string // captured for redaction
	switch c.Type {
	case SecureCredBearer:
		req.Header.Set("Authorization", "Bearer "+secret)
	case SecureCredHeader:
		req.Header.Set(c.ParamName, secret)
	case SecureCredQuery:
		q := req.URL.Query()
		q.Set(c.ParamName, secret)
		req.URL.RawQuery = q.Encode()
	case SecureCredBasicAuth:
		// Username is stored as plain config (c.Username) so it shows on
		// re-edit; the secret holds only the password. Combine them here.
		// Legacy credentials with no separate username carry "user:pass" in
		// the secret directly — the else-if keeps those working.
		pair := secret
		if c.Username != "" {
			pair = c.Username + ":" + secret
		} else if !strings.Contains(secret, ":") {
			return "", fmt.Errorf("basic_auth needs a username on the credential, or a 'username:password' secret")
		}
		auth := base64.StdEncoding.EncodeToString([]byte(pair))
		req.Header.Set("Authorization", "Basic "+auth)
	case SecureCredOAuth2:
		if c.IsAuthCode() {
			// Interactive consent: `secret` already holds the calling user's
			// per-user access token (resolved + refreshed above). Inject directly.
			req.Header.Set("Authorization", "Bearer "+secret)
			oauthBearer = secret
			break
		}
		// apiclient mints/caches/refreshes the access token and sets the
		// Authorization header; we capture the token for redaction.
		tok, oerr := s.oauthSetToken(c, secret, req)
		if oerr != nil {
			return "", fmt.Errorf("oauth token for %q: %w", c.Name, oerr)
		}
		oauthBearer = tok
	case SecureCredNone:
		// Public unauthenticated endpoint — no auth header injected.
	default:
		return "", fmt.Errorf("unknown credential type %q", c.Type)
	}

	// Build the redaction set BEFORE the request fires. Any string in
	// this slice will be replaced with [REDACTED] in any text we
	// return to the LLM (response body OR error message). Covers the
	// raw secret plus, for basic_auth, the base64-encoded form.
	redactList := []string{secret}
	if c.Type == SecureCredBasicAuth {
		pair := secret
		if c.Username != "" {
			pair = c.Username + ":" + secret
		}
		redactList = append(redactList, base64.StdEncoding.EncodeToString([]byte(pair)))
	}
	if oauthBearer != "" {
		redactList = append(redactList, oauthBearer)
	}
	redact := func(s string) string {
		for _, sec := range redactList {
			if sec == "" || len(sec) < 4 {
				continue
			}
			s = strings.ReplaceAll(s, sec, "[REDACTED]")
		}
		return s
	}

	httpClient := &http.Client{Timeout: secureAPIRequestTimeout()}
	if c.InsecureSkipTLS {
		// Per-credential opt-out of cert verification (self-signed / IP-addressed
		// LAN appliances). Scoped to this credential's allow-listed host only.
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		httpClient.Transport = tr
	}
	resp, err := httpClient.Do(req)
	if err == nil {
		// Cost hook: the request reached the endpoint (any status code is
		// billable), so price it into the per-source cost ledger. No-op when
		// this credential's CostPerCall is 0.
		RecordExternalCost("cred:"+c.Name, c.Name, c.CostPerCall)
	}
	auditEntry := SecureAPIAuditEntry{
		CredentialName: c.Name,
		Method:         method,
		URL:            rawURL,
		Timestamp:      time.Now(),
	}
	if err != nil {
		auditEntry.Error = redact(err.Error())
		s.recordAudit(auditEntry)
		// Private mode flipped ON mid-call → the context was cancelled. Report
		// the real reason, not a "request failed" the model reads as host-down.
		if connector != nil && !connector.Allowed() {
			return "", fmt.Errorf("blocked by Private mode: network egress was turned OFF mid-call, so the request to %q was cancelled — this is a privacy setting, NOT a host/connectivity failure. Turn off Private mode to reach it", rawURL)
		}
		// A TIMEOUT is usually transient (slow LAN appliance, connection warmup,
		// a momentary blip) — NOT a sign the IP / scheme / port / credential is
		// wrong. Steer the model away from confidently misdiagnosing a one-off
		// timeout as a config error and sending the user to change settings.
		if isTimeoutErr(err) {
			host := rawURL
			if parsed != nil && parsed.Host != "" {
				host = parsed.Host
			}
			return "", fmt.Errorf("%s did not respond within %s (timeout). This is OFTEN TRANSIENT — a slow LAN appliance, connection warmup, or a momentary network blip — and usually does NOT mean the IP, http-vs-https, port, or credential is wrong. Retry the request once or twice before concluding anything. Only suspect a misconfiguration if it times out REPEATEDLY across retries; do NOT tell the user to change the address/scheme/port based on a single timeout", host, secureAPIRequestTimeout())
		}
		return "", fmt.Errorf("request failed: %s", redact(err.Error()))
	}
	defer resp.Body.Close()

	// save_to path: read up to the larger save cap and stream straight
	// to disk. The LLM only sees a metadata line, so we don't pay the
	// cost of holding 100MB in memory just to redact-and-discard. Note
	// redaction is NOT applied to the saved bytes — the LLM never reads
	// them, and the typical use is binary content (audio, video) where
	// the secret string is statistically vanishingly unlikely to appear
	// anyway. The audit log records the metadata as usual.
	ct := resp.Header.Get("Content-Type")
	if saveTo != "" {
		// Read with cap+1 so we can detect truncation. ResponseBytes
		// in the audit reflects what we actually wrote.
		limited := io.LimitReader(resp.Body, secureAPIMaxSaveBytes()+1)
		written, err := writeWorkspaceFile(savePath, limited, secureAPIMaxSaveBytes())
		if err != nil {
			auditEntry.Status = resp.StatusCode
			auditEntry.Error = redact("save_to write: " + err.Error())
			s.recordAudit(auditEntry)
			return "", fmt.Errorf("save_to: %s", redact(err.Error()))
		}
		auditEntry.Status = resp.StatusCode
		auditEntry.ResponseBytes = int(written)
		s.recordAudit(auditEntry)
		s.touch(c.Name)
		return fmt.Sprintf("HTTP %d %s — saved %d bytes to %s (%s). Use attach_file(%q) to deliver to the user.",
			resp.StatusCode, http.StatusText(resp.StatusCode), written, saveTo, ct, saveTo), nil
	}

	// Read cap is higher when a response_pipe is going to project the
	// body down before the LLM sees it. Without that hint, we'd cut
	// large JSON mid-string and the pipe (jq) would fail to parse.
	// The pipe-output cap (maxOutput in temptool.go) handles the
	// LLM-visible side, so the pipe path is safe with a larger
	// upstream read.
	pipeFollowing, _ := args["__pipe_following"].(bool)
	readCap := secureAPIMaxResponseBytes()
	if pipeFollowing {
		readCap = secureAPIMaxResponseBytesForPipe()
	}
	limited := io.LimitReader(resp.Body, int64(readCap)+1)
	bodyBytes, err := io.ReadAll(limited)
	if err != nil {
		auditEntry.Status = resp.StatusCode
		auditEntry.Error = redact("body read: " + err.Error())
		s.recordAudit(auditEntry)
		return "", fmt.Errorf("read response: %s", redact(err.Error()))
	}
	truncated := false
	if len(bodyBytes) > readCap {
		bodyBytes = bodyBytes[:readCap]
		truncated = true
	}
	// When piping, suppress the truncation marker — the pipe filters
	// down anyway, the marker would just contaminate the LLM-visible
	// projected output. The pipe operates on whatever bytes we read.
	if pipeFollowing {
		truncated = false
	}
	if redacted := redact(string(bodyBytes)); redacted != string(bodyBytes) {
		bodyBytes = []byte(redacted)
		Debug("[secure_api] redacted secret from response body for credential %q", c.Name)
	}

	auditEntry.Status = resp.StatusCode
	auditEntry.ResponseBytes = len(bodyBytes)
	s.recordAudit(auditEntry)
	s.touch(c.Name)

	var sb strings.Builder
	fmt.Fprintf(&sb, "HTTP %d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	// An HTML body on an error status is a web PAGE, not an API
	// response — the URL path missed the API entirely (wrong prefix,
	// SPA catch-all route). Dumping kilobytes of <script> tags buries
	// that signal and floods the context; say it outright instead.
	if resp.StatusCode >= 400 {
		if b := strings.TrimSpace(string(bodyBytes)); strings.HasPrefix(b, "<!DOCTYPE") || strings.HasPrefix(b, "<!doctype") || strings.HasPrefix(b, "<html") {
			fmt.Fprintf(&sb, "[HTML error page suppressed — the server returned a web PAGE, not an API response. The URL path is almost certainly wrong for this API (missing prefix like /api, or a route the API doesn't serve). Re-check the endpoint path against the provider's docs; do NOT retry the same URL.]")
			return sb.String(), nil
		}
	}
	if strings.Contains(ct, "json") {
		var anyVal interface{}
		if json.Unmarshal(bodyBytes, &anyVal) == nil {
			if pretty, err := json.MarshalIndent(anyVal, "", "  "); err == nil {
				sb.Write(pretty)
				if truncated {
					sb.WriteString("\n... [TRUNCATED — response exceeded 256KB cap. To get the full data, narrow the request: add pagination/limit query params, filter on the API side (e.g. ?status=completed&limit=10), or wrap the call in a persistent tool with response_pipe to jq-project only the fields you need.]")
				}
				return sb.String(), nil
			}
		}
	}
	sb.Write(bodyBytes)
	if truncated {
		sb.WriteString("\n... [TRUNCATED — response exceeded 256KB cap. To get the full data, narrow the request: add pagination/limit query params, filter on the API side (e.g. ?status=completed&limit=10), or wrap the call in a persistent tool with response_pipe to jq-project only the fields you need.]")
	}
	return sb.String(), nil
}

// CredentialAuthGuard refuses a raw auth header aimed at a host that a
// registered credential already covers. LLMs that hold a key in
// context (a self-registration response, a value quoted in chat) will
// otherwise keep sending it inline via fetch_url forever — which (a)
// leaks the key into every transcript and (b) goes stale the moment
// the admin rotates the stored secret, producing 401 loops the model
// misdiagnoses as "the key was rotated upstream". Observed: a 2-hour
// spiral where the agent hardcoded its registration key while the
// user had already rotated it into the credential. Dispatching
// through the credential is always correct; this guard makes it the
// only path once a credential covers the host.
func CredentialAuthGuard(rawURL string, headers map[string]any) error {
	hasAuth := false
	for k := range headers {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "authorization", "x-api-key", "api-key", "x-auth-token":
			hasAuth = true
		}
	}
	if !hasAuth {
		return nil
	}
	s := Secure()
	if s == nil || !s.ready() {
		return nil
	}
	for _, c := range s.List() {
		base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
		if base == "" {
			continue
		}
		if rawURL == base || strings.HasPrefix(rawURL, base+"/") {
			return fmt.Errorf("refusing to send a raw auth header to %s — registered credential %q covers this host. Dispatch through the credential instead (the fetch_url_%s tool, or fetch_via(%q, url) in a script): the server injects the CURRENT stored secret, so calls keep working after a key rotation, and the key stays out of the conversation. If you hold a NEWER key than the stored one, save it with store_credential_secret(%q, <key>) first — never keep using an inline key", rawURL, c.Name, c.Name, c.Name, c.Name)
		}
	}
	return nil
}

// SetCredentialSecret stores (or overwrites) the encrypted secret of
// an existing credential without touching its config or enablement.
// This is the write-only vault path for keys an agent legitimately
// RECEIVES mid-flow (a self-registration response, a rotation): the
// key goes straight into the store instead of being echoed into the
// chat for a human to copy-paste into Admin > APIs. Enablement stays
// an admin decision.
func (s *SecureAPI) SetCredentialSecret(name, secret string) error {
	if !s.ready() {
		return fmt.Errorf("secure-api store not initialized")
	}
	name = strings.TrimSpace(name)
	if _, ok := s.Load(name); !ok {
		return fmt.Errorf("credential %q not registered — draft it first", name)
	}
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("refusing to store an empty secret")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.CryptSet(secureAPITable, secureCredSecretKey(name), secret)
	Log("[secure_api] credential %q secret updated via SetCredentialSecret", name)
	return nil
}

// ----------------------------------------------------------------------
// URL allowlist matching
// ----------------------------------------------------------------------

// urlMatchesPattern reports whether u satisfies pattern. Pattern uses
// a simple glob — `*` matches any non-slash run, `**` matches any run
// including slashes. Designed for endpoint allowlisting, not arbitrary
// regex (regex is too easy to get wrong).
//
//	https://api.github.com/*           matches https://api.github.com/repos
//	                                   does NOT match https://api.github.com/repos/x/y
//	https://api.github.com/**          matches both above
//	https://api.example.com/users/*    matches /users/me, NOT /users/me/repos
//
// Repeated-rejection guard. A credential whose allow-list / scheme keeps
// rejecting requests in a short window is misconfigured (wrong base_url,
// endpoints, or scheme) — retrying different URLs can never fix that. After a
// few rejections the dispatch escalates the error with a hard "stop and report
// the config" directive so an agent quits the blind-retry loop. A request that
// passes the gates clears the counter for that credential.
type credRejectTracker struct {
	count int
	since time.Time
}

var (
	credRejectMu sync.Mutex
	credRejects  = map[string]*credRejectTracker{}
)

const (
	credRejectWindow    = 2 * time.Minute
	credRejectThreshold = 3
)

func noteCredRejection(name string) int {
	credRejectMu.Lock()
	defer credRejectMu.Unlock()
	t := credRejects[name]
	now := time.Now()
	if t == nil || now.Sub(t.since) > credRejectWindow {
		t = &credRejectTracker{since: now}
		credRejects[name] = t
	}
	t.count++
	return t.count
}

func clearCredRejections(name string) {
	credRejectMu.Lock()
	delete(credRejects, name)
	credRejectMu.Unlock()
}

// credMisconfigEscalation appends the stop-and-report directive to a config
// rejection once the same credential has been rejected credRejectThreshold
// times in the window.
func credMisconfigEscalation(name, baseErr string, count int) error {
	if count < credRejectThreshold {
		return fmt.Errorf("%s", baseErr)
	}
	return fmt.Errorf("%s. STOP — this credential has been rejected %d times in a row. Its Base URL, Allowed Endpoints, or scheme is MISCONFIGURED, and trying different URLs will NOT fix it. Do not retry. Report this exact error to the user and ask them to correct the %q credential in Admin > APIs (Base URL must match the request's scheme+host, e.g. http:// vs https://; an Allowed Endpoint like /api/* permits everything under /api/).", baseErr, count, name)
}

// urlAllowedByCredential is the request-time allow-list gate. It prefers the
// BaseURL + AllowedEndpoints split (BaseURL pins the host; each endpoint is a
// path glob under it; an empty endpoint list allows anything under BaseURL),
// and falls back to the legacy single AllowedURLPattern for credentials that
// predate the split.
func urlAllowedByCredential(c SecureCredential, rawURL string) bool {
	if base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"); base != "" {
		eps := c.AllowedEndpoints
		if len(eps) == 0 {
			return urlMatchesPattern(rawURL, base+"/**")
		}
		for _, ep := range eps {
			ep = strings.TrimSpace(ep)
			if ep == "" {
				continue
			}
			if !strings.HasPrefix(ep, "/") {
				ep = "/" + ep
			}
			// "/api/*" intuitively means "everything under /api/", not just one
			// path segment. Bump a trailing single-star to "**" so a normal
			// user gets the breadth they expect without knowing the *-vs-**
			// glob convention. ("/api/core/*" -> "/api/core/**", etc.)
			if strings.HasSuffix(ep, "/*") {
				ep += "*"
			}
			if urlMatchesPattern(rawURL, base+ep) {
				return true
			}
		}
		return false
	}
	return urlMatchesPattern(rawURL, c.AllowedURLPattern)
}

func urlMatchesPattern(u, pattern string) bool {
	return globMatch(u, pattern)
}

func globMatch(s, pattern string) bool {
	si, pi := 0, 0
	for pi < len(pattern) {
		c := pattern[pi]
		if c == '*' {
			doubleStar := pi+1 < len(pattern) && pattern[pi+1] == '*'
			if doubleStar {
				pi += 2
				if pi == len(pattern) {
					return true
				}
				for k := si; k <= len(s); k++ {
					if globMatch(s[k:], pattern[pi:]) {
						return true
					}
				}
				return false
			}
			pi++
			if pi == len(pattern) {
				for ; si < len(s); si++ {
					if s[si] == '/' {
						return false
					}
				}
				return true
			}
			for k := si; k <= len(s); k++ {
				if k > si && s[k-1] == '/' {
					return false
				}
				if globMatch(s[k:], pattern[pi:]) {
					return true
				}
			}
			return false
		}
		if si == len(s) || s[si] != c {
			return false
		}
		si++
		pi++
	}
	return si == len(s)
}

// writeWorkspaceFile streams r to absPath, capping at maxBytes. Returns
// the byte count written. If the limit is hit the error reads
// "response exceeded %d byte cap"; partial output is left on disk so
// the LLM can decide whether to retry. Caller has already validated
// the path via ResolveWorkspacePath.
func writeWorkspaceFile(absPath string, r io.Reader, maxBytes int64) (int64, error) {
	f, err := os.Create(absPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	written, err := io.Copy(f, r)
	if err != nil {
		return written, err
	}
	if written > maxBytes {
		return written, fmt.Errorf("response exceeded %d byte cap (got %d)", maxBytes, written)
	}
	return written, nil
}

// validToolNameStr matches the temptool name validator. Inlined here
// to avoid an import cycle.
func validToolNameStr(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}
