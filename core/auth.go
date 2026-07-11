package core

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/ui"
	"golang.org/x/crypto/bcrypt"
)

const (
	auth_cookie_name = "gohort_session"
)

// AuthEnabled reports whether authentication is configured (at least
// one user exists in the database).
var AuthEnabled func() bool

// AuthDB provides the database handle used for auth. Set by the
// application at startup.
var AuthDB func() Database

// AuthSignupAllowed reports whether self-service signup is enabled.
// Loaded from the web_config table at startup.
var AuthSignupAllowed func() bool

// AuthSessionDays returns the session duration in days.
// Loaded from web_config; defaults to 7.
var AuthSessionDays func() int

// AuthAPIKey returns the configured API key for machine-to-machine
// access (e.g. external API endpoints). Empty means disabled.
var AuthAPIKey func() string

// AuthMaxAttempts returns the max failed login attempts before lockout.
// Defaults to 5.
var AuthMaxAttempts func() int

// AuthLockoutMinutes returns how long an IP is locked out after max
// failed attempts. Defaults to 15.
var AuthLockoutMinutes func() int

// authPublicPaths holds paths that bypass cookie auth entirely.
// Apps register these via RegisterPublicPath for endpoints that
// handle their own authentication (e.g. API key in query param).
var (
	authPublicMu    sync.Mutex
	authPublicPaths = make(map[string]bool)
)

// RegisterPublicPath marks a URL path as publicly accessible,
// bypassing the cookie auth middleware. The endpoint is responsible
// for its own authentication.
func RegisterPublicPath(path string) {
	authPublicMu.Lock()
	authPublicPaths[path] = true
	authPublicMu.Unlock()
}

// isPublicPath checks whether the given path is registered as public.
// Exact registrations match only that path; registrations ending in "/"
// act as prefix matches (e.g. "/relay/api/ack/" matches "/relay/api/ack/xyz").
func isPublicPath(path string) bool {
	authPublicMu.Lock()
	defer authPublicMu.Unlock()
	if authPublicPaths[path] {
		return true
	}
	for p := range authPublicPaths {
		if len(p) > 0 && p[len(p)-1] == '/' && strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// authSession tracks an active login session.
type authSession struct {
	User    string `json:"user"`
	Created int64  `json:"created"`
	Expires int64  `json:"expires"`
}

// session cache to avoid hitting the database on every request.
var (
	sessionMu    sync.RWMutex
	sessionCache = make(map[string]*authSession)
)

// --- Lockout tracking ---

type lockoutEntry struct {
	Attempts int
	LastFail time.Time
	Locked   time.Time // when the lockout started; zero if not locked
}

var (
	lockoutMu sync.Mutex
	lockouts  = make(map[string]*lockoutEntry)
)

func maxAttempts() int {
	if AuthMaxAttempts != nil {
		if n := AuthMaxAttempts(); n > 0 {
			return n
		}
	}
	return 5
}

func lockoutDuration() time.Duration {
	if AuthLockoutMinutes != nil {
		if n := AuthLockoutMinutes(); n > 0 {
			return time.Duration(n) * time.Minute
		}
	}
	return 15 * time.Minute
}

// recordFailedLogin increments the failure count for an IP and locks
// it out if the threshold is reached. Returns true if the IP is now locked.
func recordFailedLogin(ip string) bool {
	lockoutMu.Lock()
	defer lockoutMu.Unlock()

	entry, ok := lockouts[ip]
	if !ok {
		entry = &lockoutEntry{}
		lockouts[ip] = entry
	}

	// If currently locked, check if the lockout has expired.
	if !entry.Locked.IsZero() {
		if time.Since(entry.Locked) < lockoutDuration() {
			return true
		}
		// Lockout expired, reset.
		entry.Attempts = 0
		entry.Locked = time.Time{}
	}

	entry.Attempts++
	entry.LastFail = time.Now()

	if entry.Attempts >= maxAttempts() {
		entry.Locked = time.Now()
		return true
	}
	return false
}

// isLockedOut reports whether an IP is currently locked out.
func isLockedOut(ip string) bool {
	lockoutMu.Lock()
	defer lockoutMu.Unlock()

	entry, ok := lockouts[ip]
	if !ok {
		return false
	}
	if entry.Locked.IsZero() {
		return false
	}
	if time.Since(entry.Locked) >= lockoutDuration() {
		// Expired, clear it.
		entry.Attempts = 0
		entry.Locked = time.Time{}
		return false
	}
	return true
}

// clearLockout resets the failure count for an IP on successful login.
func clearLockout(ip string) {
	lockoutMu.Lock()
	defer lockoutMu.Unlock()
	delete(lockouts, ip)
}

// --- Password reset tokens ---

func resetTokenExpiry() time.Duration { return TuneDuration("tune_reset_token_expiry") }

func init() {
	RegisterTunable(TunableSpec{Key: "tune_reset_token_expiry", Category: "Timeouts", Label: "Password reset token expiry", Help: "How long a password-reset link stays valid after it is issued.", Kind: KindHours, Default: 1, Min: 1, Max: 72})
}

type resetToken struct {
	Username string `json:"username"`
	Expires  int64  `json:"expires"`
}

// createResetToken generates a password reset token for a user and
// stores it in the database. Returns the token string.
func createResetToken(db Database, username string) string {
	token := generateToken()
	db.Set(AuthResetTable, token, resetToken{
		Username: username,
		Expires:  time.Now().Add(resetTokenExpiry()).Unix(),
	})
	return token
}

// validateResetToken checks a reset token and returns the username if valid.
func validateResetToken(db Database, token string) (string, bool) {
	var rt resetToken
	if !db.Get(AuthResetTable, token, &rt) {
		return "", false
	}
	if time.Now().Unix() >= rt.Expires {
		db.Unset(AuthResetTable, token)
		return "", false
	}
	return rt.Username, true
}

// consumeResetToken validates and removes a reset token.
func consumeResetToken(db Database, token string) (string, bool) {
	username, ok := validateResetToken(db, token)
	if ok {
		db.Unset(AuthResetTable, token)
	}
	return username, ok
}

// AuthUser represents a stored user account.
type AuthUser struct {
	Username         string   `json:"username"`
	PassHash         string   `json:"pass_hash"` // bcrypt hash ($2…); legacy SHA-256 hex auto-upgrades on next login
	Admin            bool     `json:"admin"`
	Pending          bool     `json:"pending,omitempty"`           // true if awaiting admin approval
	Apps             []string `json:"apps,omitempty"`              // allowed app paths; empty = use defaults
	Groups           []string `json:"groups,omitempty"`            // assigned app-group IDs; each expands to its apps (see AuthResolveUserApps)
	NotifyDefault    bool     `json:"notify_default,omitempty"`    // persistent notify preference
	PrivateMode      bool     `json:"private_mode,omitempty"`      // persistent chat private-mode preference — DEPRECATED global fallback; per-agent override lives in PrivateModePerAgent
	APIExplorerMode  bool     `json:"api_explorer_mode,omitempty"` // persistent chat api-explorer preference
	InferredDisabled bool     `json:"inferred_disabled,omitempty"` // persistent per-user toggle: when true, suppresses the Reference Memory layer for this user across agents (memory_save/memory_search/memory_forget stripped, synthesis auto-ingest skipped, derived chunks excluded from recall). Per-agent override lives in InferredDisabledPerAgent. (Renamed from KnowledgeDisabled — the toggle never controlled the Knowledge layer; it controlled what is now called Reference Memory.)

	// PrivateModePerAgent / InferredDisabledPerAgent are per-(user, agent)
	// overrides for the corresponding global toggles. Keyed by agent ID.
	// Lookup precedence: per-agent override → global PrivateMode/
	// InferredDisabled → false. Lets different agents remember their
	// own preferred stance for the same user — flip Clean ON for Tax bot
	// without it bleeding into Chat.
	PrivateModePerAgent      map[string]bool `json:"private_mode_per_agent,omitempty"`
	InferredDisabledPerAgent map[string]bool `json:"inferred_disabled_per_agent,omitempty"`
}

// AuthSetNotifyDefault updates the user's persistent notify preference.
func AuthSetNotifyDefault(db Database, username string, enabled bool) {
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return
	}
	user.NotifyDefault = enabled
	db.Set(AuthTable, "user:"+username, user)
}

// AuthGetNotifyDefault returns the user's persistent notify preference.
func AuthGetNotifyDefault(db Database, username string) bool {
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return false
	}
	return user.NotifyDefault
}

// AuthSetPrivateMode updates the user's persistent chat private-mode preference.
// If no user record exists yet (e.g. unauthenticated session, or pre-auth
// startup) the write is a no-op AND a warning is logged so silent
// non-persistence is visible in the operator's log.
func AuthSetPrivateMode(db Database, username string, enabled bool) {
	if username == "" {
		Log("[auth] AuthSetPrivateMode: empty username, preference NOT persisted")
		return
	}
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		Log("[auth] AuthSetPrivateMode: user %q not found, preference NOT persisted (sign in first)", username)
		return
	}
	user.PrivateMode = enabled
	db.Set(AuthTable, "user:"+username, user)
}

// AuthGetPrivateMode returns the user's persistent chat private-mode preference.
// Defaults to false when the user has no stored preference (same as NotifyDefault).
func AuthGetPrivateMode(db Database, username string) bool {
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return false
	}
	return user.PrivateMode
}

// AuthSetInferredDisabled updates the user's persistent Reference Memory
// suppression preference. When true, the Reference Memory layer is
// bypassed for every turn the user runs (memory_save / memory_search /
// memory_forget stripped, synthesis auto-ingest skipped, derived chunks
// excluded from auto-inject) regardless of the agent's DisableInferred
// default. Symmetric shape to AuthSetPrivateMode — same warn-on-missing
// policy. Renamed from AuthSetKnowledgeDisabled — the toggle never
// controlled the Knowledge layer; it controlled what is now called
// Reference Memory.
func AuthSetInferredDisabled(db Database, username string, disabled bool) {
	if username == "" {
		Log("[auth] AuthSetInferredDisabled: empty username, preference NOT persisted")
		return
	}
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		Log("[auth] AuthSetInferredDisabled: user %q not found, preference NOT persisted (sign in first)", username)
		return
	}
	user.InferredDisabled = disabled
	db.Set(AuthTable, "user:"+username, user)
}

// AuthGetInferredDisabled returns the user's persistent Reference Memory
// suppression preference. Defaults to false (= Reference Memory enabled).
func AuthGetInferredDisabled(db Database, username string) bool {
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return false
	}
	return user.InferredDisabled
}

// AuthGetPrivateModeForAgent returns the effective Private toggle for
// the (user, agent) pair. Lookup precedence:
//  1. per-agent override (PrivateModePerAgent[agentID]) if present → use it
//  2. ANY per-agent overrides exist for this user (map non-empty) → agents
//     without explicit entries default to false (per-agent INDEPENDENT semantic)
//  3. NO per-agent overrides at all → fall back to global PrivateMode
//     (back-compat for users who never used per-agent toggles)
//
// The first per-agent set promotes the user from "global single-source-of-truth"
// to "per-agent independent." Without #2's gate, a stale global=true would
// bleed across every agent until each got its own explicit override — which
// breaks the "scoped to individual agents" mental model.
func AuthGetPrivateModeForAgent(db Database, username, agentID string) bool {
	if agentID == "" {
		return AuthGetPrivateMode(db, username)
	}
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return false
	}
	if v, ok := user.PrivateModePerAgent[agentID]; ok {
		return v
	}
	if len(user.PrivateModePerAgent) > 0 {
		return false
	}
	return user.PrivateMode
}

// AuthSetPrivateModeForAgent writes the per-(user, agent) Private
// toggle override. Empty agentID writes the global instead (caller
// is asking about the deployment-wide default).
func AuthSetPrivateModeForAgent(db Database, username, agentID string, enabled bool) {
	if agentID == "" {
		AuthSetPrivateMode(db, username, enabled)
		return
	}
	if username == "" {
		Log("[auth] AuthSetPrivateModeForAgent: empty username, preference NOT persisted")
		return
	}
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		Log("[auth] AuthSetPrivateModeForAgent: user %q not found, preference NOT persisted", username)
		return
	}
	if user.PrivateModePerAgent == nil {
		user.PrivateModePerAgent = map[string]bool{}
	}
	user.PrivateModePerAgent[agentID] = enabled
	db.Set(AuthTable, "user:"+username, user)
}

// AuthGetInferredDisabledForAgent — symmetric to PrivateMode variant.
// Per-agent set NON-EMPTY → independent semantics (default false for
// agents without explicit entries). Map empty → back-compat to global.
func AuthGetInferredDisabledForAgent(db Database, username, agentID string) bool {
	if agentID == "" {
		return AuthGetInferredDisabled(db, username)
	}
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return false
	}
	if v, ok := user.InferredDisabledPerAgent[agentID]; ok {
		return v
	}
	if len(user.InferredDisabledPerAgent) > 0 {
		return false
	}
	return user.InferredDisabled
}

// AuthSetInferredDisabledForAgent — symmetric writer.
func AuthSetInferredDisabledForAgent(db Database, username, agentID string, disabled bool) {
	if agentID == "" {
		AuthSetInferredDisabled(db, username, disabled)
		return
	}
	if username == "" {
		Log("[auth] AuthSetInferredDisabledForAgent: empty username, preference NOT persisted")
		return
	}
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		Log("[auth] AuthSetInferredDisabledForAgent: user %q not found, preference NOT persisted", username)
		return
	}
	if user.InferredDisabledPerAgent == nil {
		user.InferredDisabledPerAgent = map[string]bool{}
	}
	user.InferredDisabledPerAgent[agentID] = disabled
	db.Set(AuthTable, "user:"+username, user)
}

// AuthSetAPIExplorerMode updates the user's persistent chat API-explorer preference.
// API-explorer mode bumps the round budget and nudges the LLM toward
// iterating against APIs and saving discovered patterns as persistent
// tools — meant for figuring out unfamiliar API shapes.
func AuthSetAPIExplorerMode(db Database, username string, enabled bool) {
	if username == "" {
		Log("[auth] AuthSetAPIExplorerMode: empty username, preference NOT persisted")
		return
	}
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		Log("[auth] AuthSetAPIExplorerMode: user %q not found, preference NOT persisted (sign in first)", username)
		return
	}
	user.APIExplorerMode = enabled
	db.Set(AuthTable, "user:"+username, user)
}

// AuthGetAPIExplorerMode returns the user's persistent chat API-explorer preference.
func AuthGetAPIExplorerMode(db Database, username string) bool {
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return false
	}
	return user.APIExplorerMode
}

// sessionDuration returns the configured session lifetime.
func sessionDuration() time.Duration {
	days := 7
	if AuthSessionDays != nil {
		if d := AuthSessionDays(); d > 0 {
			days = d
		}
	}
	return time.Duration(days) * 24 * time.Hour
}

// sessionMaxAge returns the cookie MaxAge in seconds.
func sessionMaxAge() int {
	return int(sessionDuration().Seconds())
}

// isValidEmail performs a basic email format check.
func isValidEmail(email string) bool {
	at := strings.LastIndex(email, "@")
	if at < 1 {
		return false
	}
	domain := email[at+1:]
	if domain == "" || !strings.Contains(domain, ".") {
		return false
	}
	// No spaces allowed.
	if strings.ContainsAny(email, " \t\n\r") {
		return false
	}
	return true
}

// --- Password Hashing ---

// passwordDigest returns a fixed-length, NUL-free representation of a password
// (hex SHA-256, 64 ASCII bytes) to feed into bcrypt. This sidesteps bcrypt's
// 72-byte input cap and its NUL-byte truncation — gohort allows passwords up to
// 128 chars, which would otherwise be silently truncated or rejected. Pre-
// hashing before bcrypt is a standard, safe pattern (no length-extension concern
// because the output is then bcrypt'd with a per-user salt).
func passwordDigest(password string) []byte {
	h := sha256.Sum256([]byte(password))
	return []byte(hex.EncodeToString(h[:]))
}

// hashPassword returns a bcrypt hash of the password (per-user salt, adaptive
// cost). This replaces the legacy fixed-salt SHA-256 scheme; existing SHA-256
// hashes still verify via verifyPassword and are upgraded to bcrypt on the
// user's next successful login (see AuthCheckPassword). Returns "" only on the
// (practically impossible, since the input is a fixed 64-byte digest) bcrypt
// error; callers treat "" as "do not overwrite / fail".
func hashPassword(password string) string {
	b, err := bcrypt.GenerateFromPassword(passwordDigest(password), bcrypt.DefaultCost)
	if err != nil {
		Log("[auth] bcrypt hashing failed: %v", err)
		return ""
	}
	return string(b)
}

// legacyPasswordHash is the OLD fixed-salt SHA-256 scheme, kept ONLY to verify
// pre-migration hashes. Never used to create new hashes.
func legacyPasswordHash(password string) string {
	h := sha256.Sum256([]byte("gohort:" + password))
	return hex.EncodeToString(h[:])
}

// verifyPassword checks a password against a stored hash, transparently handling
// both bcrypt (new) and legacy SHA-256 (old) formats — bcrypt hashes are
// self-identifying by their "$2" prefix. legacy is true when the stored hash used
// the old scheme and should be upgraded to bcrypt on this successful login.
func verifyPassword(stored, password string) (ok, legacy bool) {
	if stored == "" {
		return false, false
	}
	if strings.HasPrefix(stored, "$2") { // bcrypt ($2a$/$2b$/$2y$)
		return bcrypt.CompareHashAndPassword([]byte(stored), passwordDigest(password)) == nil, false
	}
	// Legacy fixed-salt SHA-256 — constant-time compare, upgrade on success.
	return subtle.ConstantTimeCompare([]byte(stored), []byte(legacyPasswordHash(password))) == 1, true
}

// --- User Management (called from setup and admin app) ---

// AuthListUsers returns all configured auth users.
func AuthListUsers(db Database) []AuthUser {
	var users []AuthUser
	for _, key := range db.Keys(AuthTable) {
		if !strings.HasPrefix(key, "user:") {
			continue
		}
		var user AuthUser
		if db.Get(AuthTable, key, &user) {
			users = append(users, user)
		}
	}
	return users
}

// AuthGetUser retrieves a user by username.
func AuthGetUser(db Database, username string) (AuthUser, bool) {
	var user AuthUser
	ok := db.Get(AuthTable, "user:"+username, &user)
	return user, ok
}

// AuthSetUser creates or updates a user. If password is non-empty it
// is hashed and stored; otherwise the existing hash is preserved.
func AuthSetUser(db Database, username, password string, admin bool) {
	var existing AuthUser
	db.Get(AuthTable, "user:"+username, &existing)

	user := AuthUser{
		Username: username,
		Admin:    admin,
		Pending:  existing.Pending,
		Apps:     existing.Apps,
		Groups:   existing.Groups,
	}
	user.PassHash = existing.PassHash // default: keep existing (empty password = no change)
	if password != "" {
		if h := hashPassword(password); h != "" {
			user.PassHash = h
		}
	}
	db.Set(AuthTable, "user:"+username, user)
}

// AuthSetUserApps updates the allowed app list for a user.
func AuthSetUserApps(db Database, username string, apps []string) {
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return
	}
	user.Apps = apps
	db.Set(AuthTable, "user:"+username, user)
}

// AuthSetUserGroups updates the app-group IDs assigned to a user. Each group
// expands to its member apps at access-check time (see AuthResolveUserApps), so
// editing a group re-provisions every user assigned to it.
func AuthSetUserGroups(db Database, username string, groups []string) {
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return
	}
	user.Groups = groups
	db.Set(AuthTable, "user:"+username, user)
}

// AuthResolveUserApps returns the effective set of app paths a user may access:
// their own explicit Apps plus every app expanded from their assigned Groups.
// When the user has NEITHER explicit apps nor groups, the deployment default
// apps apply (preserving the "new user gets defaults" behavior). Any explicit
// grant — apps or groups — opts the user out of defaults, so their access is
// exactly what's assigned. Admins are handled by the caller (they bypass this).
func AuthResolveUserApps(db Database, user AuthUser) []string {
	if len(user.Apps) == 0 && len(user.Groups) == 0 {
		return AuthGetDefaultApps(db)
	}
	seen := map[string]bool{}
	var out []string
	add := func(paths []string) {
		for _, p := range paths {
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	add(user.Apps)
	add(ExpandAppGroups(db, user.Groups))
	return out
}

// AuthGetDefaultApps returns the default app paths assigned to new users.
func AuthGetDefaultApps(db Database) []string {
	var apps []string
	db.Get(WebTable, "default_apps", &apps)
	return apps
}

// AuthSetDefaultApps stores the default app paths for new users.
func AuthSetDefaultApps(db Database, apps []string) {
	db.Set(WebTable, "default_apps", apps)
}

// AuthGetChannelWakeRules returns the deployment-wide channel wake rules: the
// master gatekeeper ruleset merged on top of every channel's own per-channel
// rule before an inbound is allowed to wake its bound agent. Empty = no global
// rule (per-channel rules still apply). See core/channel.go Gatekeeper.
func AuthGetChannelWakeRules(db Database) string {
	var s string
	db.Get(WebTable, "channel_wake_rules", &s)
	return s
}

// AuthSetChannelWakeRules stores the deployment-wide channel wake rules.
func AuthSetChannelWakeRules(db Database, rules string) {
	db.Set(WebTable, "channel_wake_rules", rules)
}

// AuthGetUITheme returns the deployment-wide UI theme name (the data-theme
// value, e.g. "indigo"), or "" when unset — callers fall back to the framework
// default. Deployment-wide for now; could become per-user later.
func AuthGetUITheme(db Database) string {
	var s string
	db.Get(WebTable, "ui_theme", &s)
	return s
}

// AuthSetUITheme stores the deployment-wide UI theme name.
func AuthSetUITheme(db Database, theme string) {
	db.Set(WebTable, "ui_theme", theme)
}

// Deployment branding — a document/header brand (e.g. an org or research-group
// name like "SnugLab Research") and a site name. Used on exported documents
// (guide PDF/HTML headers + footers) and the PDF branding line. Both deployment-
// wide; "" when unset (callers fall back to a default).
func AuthGetDocBrand(db Database) string {
	var s string
	db.Get(WebTable, "doc_brand", &s)
	return s
}

func AuthSetDocBrand(db Database, v string) {
	db.Set(WebTable, "doc_brand", v)
	if v = strings.TrimSpace(v); v != "" {
		media.PDFBranding = v // keep PDF exports in sync without a restart
	}
}

func AuthGetSiteName(db Database) string {
	var s string
	db.Get(WebTable, "site_name", &s)
	return s
}

func AuthSetSiteName(db Database, v string) {
	db.Set(WebTable, "site_name", v)
}

// UserHasAppAccess checks whether the current request's user is allowed
// to access the app at the given path. Admins have access to all apps.
// When auth is not configured, everyone has access.
func UserHasAppAccess(r *http.Request, app_path string) bool {
	if AuthDB == nil {
		return true
	}
	db := AuthDB()
	if !AuthHasUsers(db) {
		return true
	}
	username := AuthCurrentUser(r)
	if username == "" {
		return false
	}
	user, ok := AuthGetUser(db, username)
	if !ok {
		return false
	}
	// Admins can access everything.
	if user.Admin {
		return true
	}
	// Determine the user's allowed apps: explicit grants + group expansions,
	// falling back to deployment defaults only when the user has neither.
	allowed := AuthResolveUserApps(db, user)
	// Empty allowed list means no apps (unless admin).
	for _, p := range allowed {
		// Exact grant, OR a grant nested UNDER this path. The nested case
		// matters for multi-app hosts like /agents, where each published/custom
		// app is granted as "/agents/<slug>" but the middleware gate only sees
		// the first path segment ("/agents"). Without this, a user granted a
		// specific "/agents/<slug>" app fails the coarse "/agents" gate and never
		// reaches the per-slug handler that would admit them — the app renders as
		// "access denied". The trailing "/" keeps "/foo" from matching "/foobar".
		// This only widens the COARSE gate: the per-slug handler still enforces
		// exactly which slug, so a grant to one agent doesn't unlock the others.
		if p == app_path || strings.HasPrefix(p, app_path+"/") {
			return true
		}
	}
	return false
}

// AuthApproveUser approves a pending user and sends them a notification.
func AuthApproveUser(db Database, username string) {
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return
	}
	user.Pending = false
	db.Set(AuthTable, "user:"+username, user)

	if isValidEmail(username) {
		go SendNotification(username,
			"["+ServiceName()+"] Your account has been approved",
			fmt.Sprintf("Your account on %s has been approved. You can now log in.\n\n%s/login\n", DashboardURL(), DashboardURL()))
	}
}

// AuthRejectUser removes a pending user and notifies them.
func AuthRejectUser(db Database, username string) {
	if isValidEmail(username) {
		go SendNotification(username,
			"["+ServiceName()+"] Your account request",
			fmt.Sprintf("Your account request on %s was not approved at this time.\n", DashboardURL()))
	}
	db.Unset(AuthTable, "user:"+username)
}

// AuthDeleteUser removes a user account.
func AuthDeleteUser(db Database, username string) {
	db.Unset(AuthTable, "user:"+username)
}

// AuthCheckPassword verifies a username/password combination. On a successful
// login against a legacy SHA-256 hash it transparently upgrades the stored hash
// to bcrypt (rehash-on-login migration), so no separate migration pass is needed.
func AuthCheckPassword(db Database, username, password string) bool {
	user, ok := AuthGetUser(db, username)
	if !ok {
		return false
	}
	valid, legacy := verifyPassword(user.PassHash, password)
	if !valid {
		return false
	}
	if legacy {
		if nh := hashPassword(password); nh != "" {
			user.PassHash = nh
			db.Set(AuthTable, "user:"+username, user)
			Log("[auth] upgraded password hash to bcrypt for %q", username)
		}
	}
	return true
}

// AuthChangePassword verifies the user's CURRENT password and, on success,
// updates ONLY the password hash — every other field (admin flag, apps, per-user
// preferences) is preserved, unlike AuthSetUser which rebuilds the record. Used
// by the self-service "change password" flow on the account page. Returns false
// if the user doesn't exist, the current password is wrong, or the new password
// is empty.
func AuthChangePassword(db Database, username, currentPassword, newPassword string) bool {
	if db == nil || strings.TrimSpace(newPassword) == "" {
		return false
	}
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return false
	}
	if valid, _ := verifyPassword(user.PassHash, currentPassword); !valid {
		return false
	}
	nh := hashPassword(newPassword)
	if nh == "" {
		return false
	}
	user.PassHash = nh
	db.Set(AuthTable, "user:"+username, user)
	return true
}

// AuthAdminSetPassword sets a user's password directly — no current-password
// check (admin authority) — and preserves every other field (admin flag, apps,
// per-user preferences), unlike AuthSetUser which rebuilds the record. Returns
// false if the user doesn't exist or the new password is empty.
func AuthAdminSetPassword(db Database, username, newPassword string) bool {
	if db == nil || strings.TrimSpace(newPassword) == "" {
		return false
	}
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return false
	}
	nh := hashPassword(newPassword)
	if nh == "" {
		return false
	}
	user.PassHash = nh
	db.Set(AuthTable, "user:"+username, user)
	return true
}

// AuthIssueResetLink mints a one-time password-reset token for an EXISTING user
// and returns the absolute /reset link. It does NOT send email — the caller
// decides whether to email it and/or show it for manual copy. ("", false) when
// the user doesn't exist.
func AuthIssueResetLink(db Database, username string) (string, bool) {
	if _, ok := AuthGetUser(db, username); !ok {
		return "", false
	}
	token := createResetToken(db, username)
	return DashboardURL() + "/reset?token=" + token, true
}

// AuthCreateInvite creates a NEW pre-approved user with no usable password and
// returns a one-time /reset link they use to set their own password. Fails if
// the user already exists. Until they set a password via the link they cannot
// log in (an empty hash never matches). Admin-authorized — the caller gates.
func AuthCreateInvite(db Database, username string, admin bool) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", fmt.Errorf("email is required")
	}
	if _, exists := AuthGetUser(db, username); exists {
		return "", fmt.Errorf("user already exists")
	}
	// Pre-approved (not pending) with an empty password hash — unusable until
	// the invitee sets one through the reset link.
	db.Set(AuthTable, "user:"+username, AuthUser{Username: username, Admin: admin})
	token := createResetToken(db, username)
	return DashboardURL() + "/reset?token=" + token, nil
}

// EmailConfigured reports whether outbound mail is set up (an SMTP server or a
// from-address). When false, invite / reset links can't be emailed and must be
// copied to the user by hand.
func EmailConfigured() bool {
	cfg := LoadMailConfig()
	return strings.TrimSpace(cfg.Server) != "" || strings.TrimSpace(cfg.From) != ""
}

// AuthHasUsers reports whether any user accounts exist.
func AuthHasUsers(db Database) bool {
	for _, key := range db.Keys(AuthTable) {
		if strings.HasPrefix(key, "user:") {
			return true
		}
	}
	return false
}

// --- Session Management ---

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// AuthCreateSession creates a new session for the given user and
// returns the session token. Sessions expire after 24 hours.
func AuthCreateSession(db Database, username string) string {
	token := generateToken()
	now := time.Now()
	sess := authSession{
		User:    username,
		Created: now.Unix(),
		Expires: now.Add(sessionDuration()).Unix(),
	}
	db.Set(AuthSessionTable, token, sess)

	sessionMu.Lock()
	sessionCache[token] = &sess
	sessionMu.Unlock()

	return token
}

// AuthValidateSession checks a session token and returns the username
// if valid. Expired sessions are cleaned up.
func AuthValidateSession(db Database, token string) (string, bool) {
	if token == "" {
		return "", false
	}

	// Check cache first.
	sessionMu.RLock()
	cached, ok := sessionCache[token]
	sessionMu.RUnlock()
	if ok {
		if time.Now().Unix() < cached.Expires {
			return cached.User, true
		}
		// Expired — remove.
		sessionMu.Lock()
		delete(sessionCache, token)
		sessionMu.Unlock()
		db.Unset(AuthSessionTable, token)
		return "", false
	}

	// Fall back to database.
	var sess authSession
	if !db.Get(AuthSessionTable, token, &sess) {
		return "", false
	}
	if time.Now().Unix() >= sess.Expires {
		db.Unset(AuthSessionTable, token)
		return "", false
	}

	// Cache it.
	sessionMu.Lock()
	sessionCache[token] = &sess
	sessionMu.Unlock()
	return sess.User, true
}

// AuthDestroySession removes a session (logout).
func AuthDestroySession(db Database, token string) {
	sessionMu.Lock()
	delete(sessionCache, token)
	sessionMu.Unlock()
	db.Unset(AuthSessionTable, token)
}

// AuthIsAdmin checks whether the currently authenticated user is an admin.
func AuthIsAdmin(db Database, r *http.Request) bool {
	cookie, err := r.Cookie(auth_cookie_name)
	if err != nil {
		return false
	}
	username, ok := AuthValidateSession(db, cookie.Value)
	if !ok {
		return false
	}
	user, ok := AuthGetUser(db, username)
	return ok && user.Admin
}

// AuthCurrentUser returns the username of the currently authenticated user.
func AuthCurrentUser(r *http.Request) string {
	if AuthDB == nil {
		return ""
	}
	cookie, err := r.Cookie(auth_cookie_name)
	if err != nil {
		return ""
	}
	username, ok := AuthValidateSession(AuthDB(), cookie.Value)
	if !ok {
		return ""
	}
	return username
}

// --- Middleware ---

// AuthMiddleware wraps an http.Handler and redirects unauthenticated
// requests to the login page. When auth is not configured (no users),
// all requests pass through.
// IsStateChangingMethod reports whether an HTTP method mutates server state and
// therefore warrants the CSRF Origin check. GET/HEAD/OPTIONS are safe/idempotent
// and are exempt — which is exactly why a handler that MUTATES must reject GET
// (a cross-site top-level GET carries the cookie under SameSite=Lax and bypasses
// the Origin check). Exported so handlers can enforce "unsafe method required".
func IsStateChangingMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// SameOriginRequest reports whether a request's Origin (or, failing that,
// Referer) is same-origin with the request host. Returns true when NEITHER
// header is present — a verify-if-present posture: browsers always send Origin on
// state-changing methods and on WebSocket handshakes, so a cross-origin attempt
// is caught, while the absent case is left to SameSite=Lax / the session-cookie
// gate rather than hard-blocking odd same-origin clients. Compares host:port
// exactly (an origin is scheme+host+port; a port mismatch is a different origin).
// Exported so browser-facing WebSocket upgraders can reuse the same check.
func SameOriginRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		if ref := r.Header.Get("Referer"); ref != "" {
			if u, err := url.Parse(ref); err == nil && u.Host != "" {
				origin = u.Scheme + "://" + u.Host
			}
		}
	}
	if origin == "" {
		return true // neither header present — SameSite=Lax is the control here
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	// gohort-desktop: the desktop app serves its UI from a LOOPBACK origin
	// (http://127.0.0.1:<port>, or wails.localhost on Windows WebView2) and makes
	// cookie-authenticated requests to the remote server, so its Origin never
	// matches the server host. A loopback origin cannot be forged by a remote
	// website — the browser stamps Origin from the actual page's origin, and a
	// remote page is never served from the victim's own loopback — so treat it as
	// same-origin. The residual case (a page served from the user's OWN localhost)
	// is inside the trust boundary a desktop webview already assumes.
	return isLoopbackHost(u.Hostname())
}

// isLoopbackHost reports whether a host is a loopback name/address: localhost,
// any *.localhost name (RFC 6761 — includes Wails' wails.localhost), or a
// loopback IP (127.0.0.0/8, ::1).
func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func AuthMiddleware(db Database, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Login/logout/signup/forgot/reset endpoints are always accessible.
		if r.URL.Path == "/login" || r.URL.Path == "/logout" || r.URL.Path == "/signup" ||
			r.URL.Path == "/forgot" || r.URL.Path == "/reset" {
			next.ServeHTTP(w, r)
			return
		}

		// Public paths registered by apps (handle their own auth).
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Allow genuine local requests -- internal inter-app HTTP calls loop
		// back over localhost. Keys on the real TCP peer, NOT clientIP: an
		// external client could otherwise send "X-Forwarded-For: 127.0.0.1" and
		// bypass auth entirely (clientIP trusts that header).
		if IsGenuineLocalRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		// API key bypass for machine-to-machine endpoints.
		if key := r.URL.Query().Get("key"); key != "" && AuthAPIKey != nil {
			if configured := AuthAPIKey(); configured != "" && key == configured {
				next.ServeHTTP(w, r)
				return
			}
		}

		// If no users configured, pass through.
		if !AuthHasUsers(db) {
			next.ServeHTTP(w, r)
			return
		}

		// Check session cookie.
		cookie, err := r.Cookie(auth_cookie_name)
		if err != nil {
			redirectToLogin(w, r)
			return
		}
		_, ok := AuthValidateSession(db, cookie.Value)
		if !ok {
			redirectToLogin(w, r)
			return
		}

		// CSRF defense for cookie-authenticated, state-changing requests. Reaching
		// here means the request carried a valid SESSION COOKIE (the machine-to-
		// machine paths — loopback, ?key=, public/bridge-key routes — already
		// returned above), so it is browser-driven and CSRF-relevant. SameSite=Lax
		// on the session cookie is the primary control (it withholds the cookie
		// from cross-site POST/PUT/PATCH/DELETE); this Origin/Referer check is
		// defense-in-depth for the residual Lax gaps and any browser without
		// SameSite. Verify-if-present: a mismatched Origin/Referer is rejected (a
		// CSRF attacker's browser always sends its own Origin on these methods);
		// when neither header is present we do NOT block, since Lax already covered
		// that case and some legitimate same-origin clients omit both.
		if IsStateChangingMethod(r.Method) && !SameOriginRequest(r) {
			http.Error(w, "cross-origin state-changing request rejected", http.StatusForbidden)
			return
		}

		// Per-app access check. Extract the app path from the URL
		// (first path segment, e.g. "/research" from "/research/api/...").
		// Skip for root, login, logout, signup, and top-level API routes.
		path := r.URL.Path
		// "/_"-prefixed paths are framework-internal shared assets (e.g.
		// /_ui/ui.js, /_ui/ui.css) that EVERY app page loads — they are not apps
		// and must never be per-app gated, or a locked-down (non-admin) user gets
		// a 403 on the runtime and every page they DO have access to renders
		// blank. They still sit behind the session check above; only the per-app
		// grant is skipped. (Admins bypass UserHasAppAccess entirely, which is why
		// this only bit restricted users.)
		if path != "/" && !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/_") &&
			path != "/login" && path != "/logout" && path != "/signup" &&
			path != "/forgot" && path != "/reset" {
			app_path := "/" + strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0]
			if !UserHasAppAccess(r, app_path) {
				writeForbidden(w, r, app_path)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func writeForbidden(w http.ResponseWriter, r *http.Request, app_path string) {
	if strings.Contains(strings.ToLower(r.URL.Path), "/api/") ||
		strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Error(w, "forbidden: no access to "+app_path, http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	body := fmt.Sprintf(`    <h1>Access denied</h1>
    <p>Your account does not have access to <code>%s</code>.</p>
    <p>Contact an administrator if you need access.</p>
    <p><a href="/">Return to dashboard</a></p>`, app_path)
	fmt.Fprint(w, authPageHTML("Access denied", body))
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	// For API calls return 401 instead of redirect.
	if strings.HasPrefix(r.URL.Path, "/api/") ||
		strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

// --- Login/Logout Handlers ---

// LoginHandler serves the login page (GET) and processes login (POST).
func LoginHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// If no users exist, redirect to dashboard.
			if !AuthHasUsers(db) {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			// If already authenticated, redirect to dashboard.
			if cookie, err := r.Cookie(auth_cookie_name); err == nil {
				if _, ok := AuthValidateSession(db, cookie.Value); ok {
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}
			}
			serveLoginPage(w, "")

		case http.MethodPost:
			r.ParseForm()
			username := strings.TrimSpace(r.FormValue("username"))
			password := r.FormValue("password")
			ip := clientIP(r).String()

			// Check lockout before attempting auth.
			if isLockedOut(ip) {
				Log("[auth] locked out IP %s attempted login as %q", ip, username)
				serveLoginPage(w, fmt.Sprintf("Too many failed attempts. Try again in %d minutes.", int(lockoutDuration().Minutes())))
				return
			}

			if !AuthCheckPassword(db, username, password) {
				Log("[auth] failed login attempt for user %q from %s", username, ip)
				locked := recordFailedLogin(ip)
				if locked {
					Log("[auth] IP %s locked out after %d failed attempts", ip, maxAttempts())
					serveLoginPage(w, fmt.Sprintf("Too many failed attempts. Try again in %d minutes.", int(lockoutDuration().Minutes())))
				} else {
					serveLoginPage(w, "Invalid username or password.")
				}
				return
			}

			// Block pending users from logging in.
			if user, ok := AuthGetUser(db, username); ok && user.Pending {
				serveLoginPage(w, "Your account is pending approval by an administrator.")
				return
			}

			clearLockout(ip)
			token := AuthCreateSession(db, username)
			http.SetCookie(w, &http.Cookie{
				Name:     auth_cookie_name,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Secure:   TLSEnabled(),
				MaxAge:   sessionMaxAge(),
			})
			Log("[auth] user %q logged in from %s", username, clientIP(r))
			http.Redirect(w, r, "/", http.StatusFound)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// LogoutHandler destroys the session and redirects to login.
func LogoutHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// POST-only: destroying the session on GET is CSRF (an attacker
		// force-logs-out the victim via a link/redirect/img — SameSite=Lax still
		// sends the cookie on a cross-site top-level GET). A GET here is treated as
		// a harmless navigation to the login page, NOT a logout; the UI logs out
		// via a POST form. Cross-site POST is a no-op anyway (Lax withholds the
		// cookie), so no session gets destroyed.
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		if cookie, err := r.Cookie(auth_cookie_name); err == nil {
			username, _ := AuthValidateSession(db, cookie.Value)
			AuthDestroySession(db, cookie.Value)
			if username != "" {
				Log("[auth] user %q logged out", username)
			}
		}
		http.SetCookie(w, &http.Cookie{
			Name:     auth_cookie_name,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

// SignupHandler serves the signup page (GET) and creates new users (POST).
func SignupHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Block if signup is disabled.
		if AuthSignupAllowed == nil || !AuthSignupAllowed() {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		switch r.Method {
		case http.MethodGet:
			// If already authenticated, redirect to dashboard.
			if cookie, err := r.Cookie(auth_cookie_name); err == nil {
				if _, ok := AuthValidateSession(db, cookie.Value); ok {
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}
			}
			serveSignupPage(w, "")

		case http.MethodPost:
			r.ParseForm()
			email := strings.TrimSpace(r.FormValue("email"))
			password := r.FormValue("password")
			confirm := r.FormValue("confirm")

			if email == "" || password == "" {
				serveSignupPage(w, "Email and password are required.")
				return
			}
			if len(email) > 254 || !isValidEmail(email) {
				serveSignupPage(w, "Please enter a valid email address.")
				return
			}
			if len(password) < 6 {
				serveSignupPage(w, "Password must be at least 6 characters.")
				return
			}
			if len(password) > 128 {
				serveSignupPage(w, "Password must be 128 characters or fewer.")
				return
			}
			if password != confirm {
				serveSignupPage(w, "Passwords do not match.")
				return
			}
			if _, exists := AuthGetUser(db, email); exists {
				serveSignupPage(w, "An account with that email already exists.")
				return
			}

			// Create as pending non-admin user.
			AuthSetUser(db, email, password, false)
			// Mark as pending approval.
			var user AuthUser
			db.Get(AuthTable, "user:"+email, &user)
			user.Pending = true
			db.Set(AuthTable, "user:"+email, user)

			Log("[auth] new signup (pending): %q from %s", email, clientIP(r))

			// Notify admins of the new signup.
			NotifyAdmin("["+ServiceName()+"] New signup pending approval",
				fmt.Sprintf("A new user has signed up on %s and is awaiting approval.\n\nEmail: %s\n\nAdmin panel: %s/admin/\n", DashboardURL(), email, DashboardURL()))

			serveSignupPage(w, "Account created. An administrator will review your request.")

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// authPageHTML wraps a page body in the shared, themed auth-page shell used by
// login / sign-up / forgot / reset / access-denied. Built by concatenation (not
// fmt) so CSS percent literals need no escaping, and themed via the active
// theme's tokens (data-theme + injected ThemeCSS). The body brings its own
// header (ascii-logo or an h1/h2) plus its form/content; the shell supplies the
// chrome + the .auth-box wrapper. Replaces five near-identical hand-rolled
// page templates.
func authPageHTML(title, body string) string {
	return `<!DOCTYPE html>
<html lang="en" data-theme="` + ui.ActiveTheme() + `">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>` + title + `</title>
<style>
` + ui.ThemeCSS() + `
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif;
    background: var(--bg-0); color: var(--text); min-height: 100vh;
    display: flex; flex-direction: column; align-items: center; justify-content: center;
    padding: 20px;
  }
  .auth-box {
    background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
    padding: 2rem; width: 100%; max-width: 380px;
  }
  .ascii-logo {
    font-family: 'Orbitron', -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, sans-serif;
    font-size: 2.4rem; font-weight: 700; line-height: 1; letter-spacing: 0.15em;
    text-transform: uppercase; margin-bottom: 1.5rem; text-align: center;
    background: linear-gradient(180deg, var(--text-hi) 0%, var(--border) 100%);
    -webkit-background-clip: text; -webkit-text-fill-color: transparent; background-clip: text;
  }
  h1 { color: var(--danger); font-size: 1.4rem; margin-bottom: 0.75rem; }
  h2 { font-size: 1.1rem; color: var(--text-hi); margin-bottom: 1rem; }
  p { margin: 8px 0; line-height: 1.5; }
  a { color: var(--accent); }
  code { background: var(--bg-0); padding: 2px 6px; border-radius: 4px; }
  .form-group { margin-bottom: 1rem; }
  label { display: block; font-size: 0.85rem; color: var(--text-mute); margin-bottom: 0.3rem; }
  input[type="text"], input[type="email"], input[type="password"] {
    width: 100%; padding: 0.6rem 0.8rem; background: var(--bg-0);
    border: 1px solid var(--border); border-radius: 6px; color: var(--text);
    font-size: 0.95rem; transition: border-color 0.2s;
  }
  input:focus { outline: none; border-color: var(--accent); }
  button {
    width: 100%; padding: 0.7rem; background: var(--accent); border: 1px solid var(--accent);
    border-radius: 6px; color: #fff; font-size: 0.95rem; font-weight: 600; cursor: pointer;
    transition: filter 0.2s;
  }
  button:hover { filter: brightness(1.08); }
  .error {
    background: color-mix(in srgb, var(--danger) 18%, transparent);
    border: 1px solid var(--danger); border-radius: 6px;
    padding: 0.5rem 0.8rem; margin-bottom: 1rem; color: var(--danger); font-size: 0.85rem;
  }
  .success {
    background: color-mix(in srgb, var(--success) 18%, transparent);
    border: 1px solid var(--success); border-radius: 6px;
    padding: 0.5rem 0.8rem; margin-bottom: 1rem; color: var(--success); font-size: 0.85rem;
  }
  .alt-link {
    display: block; text-align: center; margin-top: 1rem;
    font-size: 0.85rem; color: var(--accent); text-decoration: none;
  }
  .alt-link:hover { text-decoration: underline; }
</style>
</head>
<body>
  <div class="auth-box">
` + body + `
  </div>
</body>
</html>`
}

func serveSignupPage(w http.ResponseWriter, errMsg string) {
	error_html := ""
	if errMsg != "" {
		error_html = fmt.Sprintf(`<div class="error">%s</div>`, errMsg)
	}

	body := `    <div class="ascii-logo">Gohort</div>
    ` + error_html + `
    <form method="POST" action="/signup">
      <div class="form-group">
        <label for="email">Email</label>
        <input type="email" id="email" name="email" autocomplete="email" autofocus>
      </div>
      <div class="form-group">
        <label for="password">Password</label>
        <input type="password" id="password" name="password" autocomplete="new-password">
      </div>
      <div class="form-group">
        <label for="confirm">Confirm Password</label>
        <input type="password" id="confirm" name="confirm" autocomplete="new-password">
      </div>
      <button type="submit">Create Account</button>
    </form>
    <a class="alt-link" href="/login">Already have an account? Sign in</a>`
	html := authPageHTML("Gohort - Sign Up", body)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}

// ForgotHandler serves the forgot password page (GET) and sends reset emails (POST).
func ForgotHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			serveForgotPage(w, "", false)

		case http.MethodPost:
			r.ParseForm()
			email := strings.TrimSpace(r.FormValue("email"))

			// Always show success to avoid email enumeration.
			if email != "" && isValidEmail(email) {
				if user, ok := AuthGetUser(db, email); ok && !user.Pending {
					token := createResetToken(db, email)
					link := DashboardURL() + "/reset?token=" + token
					SendNotification(email,
						"["+ServiceName()+"] Password Reset",
						fmt.Sprintf("A password reset was requested for your account on %s.\n\nReset your password:\n\n%s\n\nThis link expires in 1 hour. If you did not request this, ignore this email.\n", DashboardURL(), link))
					Log("[auth] password reset requested for %q from %s", email, clientIP(r))
				}
			}
			serveForgotPage(w, "If an account exists with that email, a reset link has been sent.", true)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// ResetHandler serves the password reset page (GET) and processes the reset (POST).
func ResetHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Redirect(w, r, "/forgot", http.StatusFound)
			return
		}

		switch r.Method {
		case http.MethodGet:
			if _, ok := validateResetToken(db, token); !ok {
				serveResetPage(w, token, "This reset link is invalid or has expired.")
				return
			}
			serveResetPage(w, token, "")

		case http.MethodPost:
			r.ParseForm()
			password := r.FormValue("password")
			confirm := r.FormValue("confirm")

			username, ok := consumeResetToken(db, token)
			if !ok {
				serveResetPage(w, token, "This reset link is invalid or has expired.")
				return
			}
			if len(password) < 6 {
				// Re-create the token since consumeResetToken deleted it.
				new_token := createResetToken(db, username)
				serveResetPage(w, new_token, "Password must be at least 6 characters.")
				return
			}
			if len(password) > 128 {
				new_token := createResetToken(db, username)
				serveResetPage(w, new_token, "Password must be 128 characters or fewer.")
				return
			}
			if password != confirm {
				new_token := createResetToken(db, username)
				serveResetPage(w, new_token, "Passwords do not match.")
				return
			}
			if _, exists := AuthGetUser(db, username); !exists {
				serveResetPage(w, "", "Account not found.")
				return
			}
			// Preserve every field (admin, apps, preferences) — set only the hash.
			AuthAdminSetPassword(db, username, password)
			Log("[auth] password reset completed for %q from %s", username, clientIP(r))
			serveLoginPage(w, "Password has been reset. You can now log in.")

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func serveForgotPage(w http.ResponseWriter, msg string, success bool) {
	msg_html := ""
	if msg != "" {
		class := "error"
		if success {
			class = "success"
		}
		msg_html = fmt.Sprintf(`<div class="%s">%s</div>`, class, msg)
	}

	body := `    <h2>Forgot Password</h2>
    ` + msg_html + `
    <form method="POST" action="/forgot">
      <div class="form-group">
        <label for="email">Email</label>
        <input type="email" id="email" name="email" autocomplete="email" autofocus>
      </div>
      <button type="submit">Send Reset Link</button>
    </form>
    <a class="alt-link" href="/login">Back to login</a>`
	html := authPageHTML("Forgot Password", body)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}

func serveResetPage(w http.ResponseWriter, token string, errMsg string) {
	error_html := ""
	if errMsg != "" {
		error_html = fmt.Sprintf(`<div class="error">%s</div>`, errMsg)
	}
	// If token is empty (expired/invalid), don't show the form.
	form_html := ""
	if token != "" {
		form_html = fmt.Sprintf(`
    <form method="POST" action="/reset?token=%s">
      <div class="form-group">
        <label for="password">New Password</label>
        <input type="password" id="password" name="password" autocomplete="new-password" autofocus>
      </div>
      <div class="form-group">
        <label for="confirm">Confirm Password</label>
        <input type="password" id="confirm" name="confirm" autocomplete="new-password">
      </div>
      <button type="submit">Reset Password</button>
    </form>`, token)
	}

	body := `    <h2>Reset Password</h2>
    ` + error_html + `
    ` + form_html + `
    <a class="alt-link" href="/login">Back to login</a>`
	html := authPageHTML("Reset Password", body)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}

// signupEnabled reports whether self-service signup is currently allowed.
func signupEnabled() bool {
	return AuthSignupAllowed != nil && AuthSignupAllowed()
}

// --- Login Page ---

func serveLoginPage(w http.ResponseWriter, errMsg string) {
	error_html := ""
	if errMsg != "" {
		error_html = fmt.Sprintf(`<div class="error">%s</div>`, errMsg)
	}
	links := `<a class="alt-link" href="/forgot">Forgot password?</a>`
	if signupEnabled() {
		links += `<a class="alt-link" href="/signup">Don't have an account? Sign up</a>`
	}

	body := `    <div class="ascii-logo">Gohort</div>
    ` + error_html + `
    <form method="POST" action="/login">
      <div class="form-group">
        <label for="username">Email</label>
        <input type="text" id="username" name="username" autocomplete="username" autofocus>
      </div>
      <div class="form-group">
        <label for="password">Password</label>
        <input type="password" id="password" name="password" autocomplete="current-password">
      </div>
      <button type="submit">Sign In</button>
    </form>
    ` + links
	html := authPageHTML("Gohort - Login", body)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}

// --- Setup Menu Integration ---

// BuildAuthSetup creates the --setup menu section for authentication.
func BuildAuthSetup(db Database) (*Options, *authSetupState) {
	state := &authSetupState{}

	// Load existing users for display.
	state.users = AuthListUsers(db)

	menu := NewOptions(" [Authentication] ", "(selection or 'q' to return to previous)", 'q')

	menu.Func("List Users", func() bool {
		users := AuthListUsers(db)
		if len(users) == 0 {
			Stderr("\n  No users configured. Authentication is disabled.\n\n")
			return true
		}
		Stdout("\n  Configured Users:\n")
		for _, u := range users {
			role := "user"
			if u.Admin {
				role = "admin"
			}
			Stdout("    - %s (%s)\n", u.Username, role)
		}
		Stdout(NONE)
		return true
	})

	menu.Func("Add User", func() bool {
		username := GetInput("Email: ")
		if username == "" {
			return true
		}
		if _, exists := AuthGetUser(db, username); exists {
			Stderr("\n  User %q already exists.\n\n", username)
			return true
		}
		password := GetInput("Password: ")
		if password == "" {
			Stderr("\n  Password cannot be empty.\n\n")
			return true
		}
		admin_input := GetInput("Admin? (y/n): ")
		admin := strings.ToLower(strings.TrimSpace(admin_input)) == "y"
		AuthSetUser(db, username, password, admin)
		Stdout("\n  User %q added.\n\n", username)
		return true
	})

	menu.Func("Change Password", func() bool {
		username := GetInput("Email: ")
		if username == "" {
			return true
		}
		user, ok := AuthGetUser(db, username)
		if !ok {
			Stderr("\n  User %q not found.\n\n", username)
			return true
		}
		password := GetInput("New password: ")
		if password == "" {
			Stderr("\n  Password cannot be empty.\n\n")
			return true
		}
		AuthSetUser(db, username, password, user.Admin)
		Stdout("\n  Password updated for %q.\n\n", username)
		return true
	})

	menu.Func("Remove User", func() bool {
		username := GetInput("Email: ")
		if username == "" {
			return true
		}
		if _, ok := AuthGetUser(db, username); !ok {
			Stderr("\n  User %q not found.\n\n", username)
			return true
		}
		AuthDeleteUser(db, username)
		Stdout("\n  User %q removed.\n\n", username)
		return true
	})

	return menu, state
}

type authSetupState struct {
	users []AuthUser
}

// UserListJSON returns a JSON array of user summaries (no password hashes).
func UserListJSON(db Database) []byte {
	users := AuthListUsers(db)
	type userSummary struct {
		Username string   `json:"username"`
		Admin    bool     `json:"admin"`
		Pending  bool     `json:"pending"`
		Apps     []string `json:"apps"`
	}
	summaries := make([]userSummary, len(users))
	for i, u := range users {
		apps := u.Apps
		if apps == nil {
			apps = []string{}
		}
		summaries[i] = userSummary{Username: u.Username, Admin: u.Admin, Pending: u.Pending, Apps: apps}
	}
	data, _ := json.Marshal(summaries)
	return data
}
