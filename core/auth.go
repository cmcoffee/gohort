package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	auth_table       = "auth_config"
	auth_session_tbl = "auth_sessions"
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
func isPublicPath(path string) bool {
	authPublicMu.Lock()
	defer authPublicMu.Unlock()
	return authPublicPaths[path]
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

const (
	auth_reset_tbl    = "auth_reset_tokens"
	resetTokenExpiry  = 1 * time.Hour
)

type resetToken struct {
	Username string `json:"username"`
	Expires  int64  `json:"expires"`
}

// createResetToken generates a password reset token for a user and
// stores it in the database. Returns the token string.
func createResetToken(db Database, username string) string {
	token := generateToken()
	db.Set(auth_reset_tbl, token, resetToken{
		Username: username,
		Expires:  time.Now().Add(resetTokenExpiry).Unix(),
	})
	return token
}

// validateResetToken checks a reset token and returns the username if valid.
func validateResetToken(db Database, token string) (string, bool) {
	var rt resetToken
	if !db.Get(auth_reset_tbl, token, &rt) {
		return "", false
	}
	if time.Now().Unix() >= rt.Expires {
		db.Unset(auth_reset_tbl, token)
		return "", false
	}
	return rt.Username, true
}

// consumeResetToken validates and removes a reset token.
func consumeResetToken(db Database, token string) (string, bool) {
	username, ok := validateResetToken(db, token)
	if ok {
		db.Unset(auth_reset_tbl, token)
	}
	return username, ok
}

// AuthUser represents a stored user account.
type AuthUser struct {
	Username      string   `json:"username"`
	PassHash      string   `json:"pass_hash"` // SHA-256 hex
	Admin         bool     `json:"admin"`
	Pending       bool     `json:"pending,omitempty"`  // true if awaiting admin approval
	Apps          []string `json:"apps,omitempty"`     // allowed app paths; empty = use defaults
	NotifyDefault bool     `json:"notify_default,omitempty"` // persistent notify preference
}

// AuthSetNotifyDefault updates the user's persistent notify preference.
func AuthSetNotifyDefault(db Database, username string, enabled bool) {
	var user AuthUser
	if !db.Get(auth_table, "user:"+username, &user) {
		return
	}
	user.NotifyDefault = enabled
	db.Set(auth_table, "user:"+username, user)
}

// AuthGetNotifyDefault returns the user's persistent notify preference.
func AuthGetNotifyDefault(db Database, username string) bool {
	var user AuthUser
	if !db.Get(auth_table, "user:"+username, &user) {
		return false
	}
	return user.NotifyDefault
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

// hashPassword returns the SHA-256 hex hash of a password with a fixed
// application salt. This is intentionally simple; the database itself
// is already hardware-encrypted via kvlite.
func hashPassword(password string) string {
	h := sha256.Sum256([]byte("gohort:" + password))
	return hex.EncodeToString(h[:])
}

// --- User Management (called from setup and admin app) ---

// AuthListUsers returns all configured auth users.
func AuthListUsers(db Database) []AuthUser {
	var users []AuthUser
	for _, key := range db.Keys(auth_table) {
		if !strings.HasPrefix(key, "user:") {
			continue
		}
		var user AuthUser
		if db.Get(auth_table, key, &user) {
			users = append(users, user)
		}
	}
	return users
}

// AuthGetUser retrieves a user by username.
func AuthGetUser(db Database, username string) (AuthUser, bool) {
	var user AuthUser
	ok := db.Get(auth_table, "user:"+username, &user)
	return user, ok
}

// AuthSetUser creates or updates a user. If password is non-empty it
// is hashed and stored; otherwise the existing hash is preserved.
func AuthSetUser(db Database, username, password string, admin bool) {
	var existing AuthUser
	db.Get(auth_table, "user:"+username, &existing)

	user := AuthUser{
		Username: username,
		Admin:    admin,
		Pending:  existing.Pending,
		Apps:     existing.Apps,
	}
	if password != "" {
		user.PassHash = hashPassword(password)
	} else {
		user.PassHash = existing.PassHash
	}
	db.Set(auth_table, "user:"+username, user)
}

// AuthSetUserApps updates the allowed app list for a user.
func AuthSetUserApps(db Database, username string, apps []string) {
	var user AuthUser
	if !db.Get(auth_table, "user:"+username, &user) {
		return
	}
	user.Apps = apps
	db.Set(auth_table, "user:"+username, user)
}

// AuthGetDefaultApps returns the default app paths assigned to new users.
func AuthGetDefaultApps(db Database) []string {
	var apps []string
	db.Get("web_config", "default_apps", &apps)
	return apps
}

// AuthSetDefaultApps stores the default app paths for new users.
func AuthSetDefaultApps(db Database, apps []string) {
	db.Set("web_config", "default_apps", apps)
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
	// Determine the user's allowed apps.
	allowed := user.Apps
	if len(allowed) == 0 {
		allowed = AuthGetDefaultApps(db)
	}
	// Empty allowed list means no apps (unless admin).
	for _, p := range allowed {
		if p == app_path {
			return true
		}
	}
	return false
}

// AuthApproveUser approves a pending user and sends them a notification.
func AuthApproveUser(db Database, username string) {
	var user AuthUser
	if !db.Get(auth_table, "user:"+username, &user) {
		return
	}
	user.Pending = false
	db.Set(auth_table, "user:"+username, user)

	if isValidEmail(username) {
		go SendNotification(username,
			"[" + ServiceName() + "] Your account has been approved",
			fmt.Sprintf("Your account on %s has been approved. You can now log in.\n\n%s/login\n", DashboardURL(), DashboardURL()))
	}
}

// AuthRejectUser removes a pending user and notifies them.
func AuthRejectUser(db Database, username string) {
	if isValidEmail(username) {
		go SendNotification(username,
			"[" + ServiceName() + "] Your account request",
			fmt.Sprintf("Your account request on %s was not approved at this time.\n", DashboardURL()))
	}
	db.Unset(auth_table, "user:"+username)
}

// AuthDeleteUser removes a user account.
func AuthDeleteUser(db Database, username string) {
	db.Unset(auth_table, "user:"+username)
}

// AuthCheckPassword verifies a username/password combination.
func AuthCheckPassword(db Database, username, password string) bool {
	user, ok := AuthGetUser(db, username)
	if !ok {
		return false
	}
	return user.PassHash == hashPassword(password)
}

// AuthHasUsers reports whether any user accounts exist.
func AuthHasUsers(db Database) bool {
	for _, key := range db.Keys(auth_table) {
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
	db.Set(auth_session_tbl, token, sess)

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
		db.Unset(auth_session_tbl, token)
		return "", false
	}

	// Fall back to database.
	var sess authSession
	if !db.Get(auth_session_tbl, token, &sess) {
		return "", false
	}
	if time.Now().Unix() >= sess.Expires {
		db.Unset(auth_session_tbl, token)
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
	db.Unset(auth_session_tbl, token)
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

		// Allow loopback requests -- internal inter-app HTTP calls.
		if ip := clientIP(r); ip != nil && ip.IsLoopback() {
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

		// Per-app access check. Extract the app path from the URL
		// (first path segment, e.g. "/research" from "/research/api/...").
		// Skip for root, login, logout, signup, and top-level API routes.
		path := r.URL.Path
		if path != "/" && !strings.HasPrefix(path, "/api/") &&
			path != "/login" && path != "/logout" && path != "/signup" &&
				path != "/forgot" && path != "/reset" {
			app_path := "/" + strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0]
			if !UserHasAppAccess(r, app_path) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
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
			db.Get(auth_table, "user:"+email, &user)
			user.Pending = true
			db.Set(auth_table, "user:"+email, user)

			Log("[auth] new signup (pending): %q from %s", email, clientIP(r))

			// Notify admins of the new signup.
			NotifyAdmin("[" + ServiceName() + "] New signup pending approval",
				fmt.Sprintf("A new user has signed up on %s and is awaiting approval.\n\nEmail: %s\n\nAdmin panel: %s/admin/\n", DashboardURL(), email, DashboardURL()))

			serveSignupPage(w, "Account created. An administrator will review your request.")

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func serveSignupPage(w http.ResponseWriter, errMsg string) {
	error_html := ""
	if errMsg != "" {
		error_html = fmt.Sprintf(`<div class="error">%s</div>`, errMsg)
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Gohort - Sign Up</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif;
    background: #0d1117; color: #c9d1d9; min-height: 100vh;
    display: flex; flex-direction: column; align-items: center; justify-content: center;
    padding: 20px;
  }
  .login-box {
    background: #161b22; border: 1px solid #30363d; border-radius: 8px;
    padding: 2rem; width: 100%%; max-width: 360px;
  }
  .ascii-logo {
    font-family: 'Courier New', Courier, monospace;
    font-size: 0.8rem; line-height: 1.15; white-space: pre;
    margin-bottom: 1.5rem; text-align: center;
    background: linear-gradient(135deg, #58a6ff 0%%, #a371f7 50%%, #f778ba 100%%);
    -webkit-background-clip: text; -webkit-text-fill-color: transparent;
    background-clip: text;
  }
  .form-group { margin-bottom: 1rem; }
  label {
    display: block; font-size: 0.85rem; color: #8b949e;
    margin-bottom: 0.3rem;
  }
  input[type="text"], input[type="password"], input[type="email"] {
    width: 100%%; padding: 0.6rem 0.8rem;
    background: #0d1117; border: 1px solid #30363d; border-radius: 6px;
    color: #c9d1d9; font-size: 0.95rem;
    transition: border-color 0.2s;
  }
  input:focus { outline: none; border-color: #58a6ff; }
  button {
    width: 100%%; padding: 0.7rem;
    background: #238636; border: 1px solid #2ea043; border-radius: 6px;
    color: #fff; font-size: 0.95rem; font-weight: 600;
    cursor: pointer; transition: background 0.2s;
  }
  button:hover { background: #2ea043; }
  .error {
    background: #da363340; border: 1px solid #da3633; border-radius: 6px;
    padding: 0.5rem 0.8rem; margin-bottom: 1rem;
    color: #f85149; font-size: 0.85rem;
  }
  .alt-link {
    display: block; text-align: center; margin-top: 1rem;
    font-size: 0.85rem; color: #58a6ff; text-decoration: none;
  }
  .alt-link:hover { text-decoration: underline; }
</style>
</head>
<body>
  <div class="login-box">
    <div class="ascii-logo">
  ____       _                _
 / ___| ___ | |__   ___  _ __| |_
| |  _ / _ \| '_ \ / _ \| '__| __|
| |_| | (_) | | | | (_) | |  | |_
 \____|\___/|_| |_|\___/|_|   \__|</div>
    %s
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
    <a class="alt-link" href="/login">Already have an account? Sign in</a>
  </div>
</body>
</html>`, error_html)

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
			user, exists := AuthGetUser(db, username)
			if !exists {
				serveResetPage(w, "", "Account not found.")
				return
			}
			AuthSetUser(db, username, password, user.Admin)
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

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Forgot Password</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif;
    background: #0d1117; color: #c9d1d9; min-height: 100vh;
    display: flex; flex-direction: column; align-items: center; justify-content: center;
    padding: 20px;
  }
  .login-box {
    background: #161b22; border: 1px solid #30363d; border-radius: 8px;
    padding: 2rem; width: 100%%; max-width: 360px;
  }
  .form-group { margin-bottom: 1rem; }
  label { display: block; font-size: 0.85rem; color: #8b949e; margin-bottom: 0.3rem; }
  input[type="text"], input[type="email"] {
    width: 100%%; padding: 0.6rem 0.8rem;
    background: #0d1117; border: 1px solid #30363d; border-radius: 6px;
    color: #c9d1d9; font-size: 0.95rem; transition: border-color 0.2s;
  }
  input:focus { outline: none; border-color: #58a6ff; }
  button {
    width: 100%%; padding: 0.7rem;
    background: #238636; border: 1px solid #2ea043; border-radius: 6px;
    color: #fff; font-size: 0.95rem; font-weight: 600;
    cursor: pointer; transition: background 0.2s;
  }
  button:hover { background: #2ea043; }
  .error {
    background: #da363340; border: 1px solid #da3633; border-radius: 6px;
    padding: 0.5rem 0.8rem; margin-bottom: 1rem; color: #f85149; font-size: 0.85rem;
  }
  .success {
    background: #23863640; border: 1px solid #238636; border-radius: 6px;
    padding: 0.5rem 0.8rem; margin-bottom: 1rem; color: #3fb950; font-size: 0.85rem;
  }
  .alt-link {
    display: block; text-align: center; margin-top: 1rem;
    font-size: 0.85rem; color: #58a6ff; text-decoration: none;
  }
  .alt-link:hover { text-decoration: underline; }
  h2 { font-size: 1.1rem; color: #f0f6fc; margin-bottom: 1rem; }
</style>
</head>
<body>
  <div class="login-box">
    <h2>Forgot Password</h2>
    %s
    <form method="POST" action="/forgot">
      <div class="form-group">
        <label for="email">Email</label>
        <input type="email" id="email" name="email" autocomplete="email" autofocus>
      </div>
      <button type="submit">Send Reset Link</button>
    </form>
    <a class="alt-link" href="/login">Back to login</a>
  </div>
</body>
</html>`, msg_html)

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

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Reset Password</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif;
    background: #0d1117; color: #c9d1d9; min-height: 100vh;
    display: flex; flex-direction: column; align-items: center; justify-content: center;
    padding: 20px;
  }
  .login-box {
    background: #161b22; border: 1px solid #30363d; border-radius: 8px;
    padding: 2rem; width: 100%%; max-width: 360px;
  }
  .form-group { margin-bottom: 1rem; }
  label { display: block; font-size: 0.85rem; color: #8b949e; margin-bottom: 0.3rem; }
  input[type="password"] {
    width: 100%%; padding: 0.6rem 0.8rem;
    background: #0d1117; border: 1px solid #30363d; border-radius: 6px;
    color: #c9d1d9; font-size: 0.95rem; transition: border-color 0.2s;
  }
  input:focus { outline: none; border-color: #58a6ff; }
  button {
    width: 100%%; padding: 0.7rem;
    background: #238636; border: 1px solid #2ea043; border-radius: 6px;
    color: #fff; font-size: 0.95rem; font-weight: 600;
    cursor: pointer; transition: background 0.2s;
  }
  button:hover { background: #2ea043; }
  .error {
    background: #da363340; border: 1px solid #da3633; border-radius: 6px;
    padding: 0.5rem 0.8rem; margin-bottom: 1rem; color: #f85149; font-size: 0.85rem;
  }
  .alt-link {
    display: block; text-align: center; margin-top: 1rem;
    font-size: 0.85rem; color: #58a6ff; text-decoration: none;
  }
  .alt-link:hover { text-decoration: underline; }
  h2 { font-size: 1.1rem; color: #f0f6fc; margin-bottom: 1rem; }
</style>
</head>
<body>
  <div class="login-box">
    <h2>Reset Password</h2>
    %s
    %s
    <a class="alt-link" href="/login">Back to login</a>
  </div>
</body>
</html>`, error_html, form_html)

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

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Gohort - Login</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif;
    background: #0d1117; color: #c9d1d9; min-height: 100vh;
    display: flex; flex-direction: column; align-items: center; justify-content: center;
    padding: 20px;
  }
  .login-box {
    background: #161b22; border: 1px solid #30363d; border-radius: 8px;
    padding: 2rem; width: 100%%; max-width: 360px;
  }
  .ascii-logo {
    font-family: 'Courier New', Courier, monospace;
    font-size: 0.8rem; line-height: 1.15; white-space: pre;
    margin-bottom: 1.5rem; text-align: center;
    background: linear-gradient(135deg, #58a6ff 0%%, #a371f7 50%%, #f778ba 100%%);
    -webkit-background-clip: text; -webkit-text-fill-color: transparent;
    background-clip: text;
  }
  .form-group { margin-bottom: 1rem; }
  label {
    display: block; font-size: 0.85rem; color: #8b949e;
    margin-bottom: 0.3rem;
  }
  input[type="text"], input[type="password"] {
    width: 100%%; padding: 0.6rem 0.8rem;
    background: #0d1117; border: 1px solid #30363d; border-radius: 6px;
    color: #c9d1d9; font-size: 0.95rem;
    transition: border-color 0.2s;
  }
  input:focus { outline: none; border-color: #58a6ff; }
  button {
    width: 100%%; padding: 0.7rem;
    background: #238636; border: 1px solid #2ea043; border-radius: 6px;
    color: #fff; font-size: 0.95rem; font-weight: 600;
    cursor: pointer; transition: background 0.2s;
  }
  button:hover { background: #2ea043; }
  .error {
    background: #da363340; border: 1px solid #da3633; border-radius: 6px;
    padding: 0.5rem 0.8rem; margin-bottom: 1rem;
    color: #f85149; font-size: 0.85rem;
  }
  .alt-link {
    display: block; text-align: center; margin-top: 1rem;
    font-size: 0.85rem; color: #58a6ff; text-decoration: none;
  }
  .alt-link:hover { text-decoration: underline; }
</style>
</head>
<body>
  <div class="login-box">
    <div class="ascii-logo">
  ____       _                _
 / ___| ___ | |__   ___  _ __| |_
| |  _ / _ \| '_ \ / _ \| '__| __|
| |_| | (_) | | | | (_) | |  | |_
 \____|\___/|_| |_|\___/|_|   \__|</div>
    %s
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
    %s
  </div>
</body>
</html>`, error_html, links)

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
