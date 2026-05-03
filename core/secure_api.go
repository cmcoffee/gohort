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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

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
)

// SecureCredential is the public-facing record. Secret is held in a
// separate encrypted entry so listing credentials in the admin UI
// (which doesn't need the value) never has to decrypt it. ParamName
// applies to header (the header name) and query (the query param).
type SecureCredential struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	AllowedURLPattern string `json:"allowed_url_pattern"`
	Description       string `json:"description,omitempty"`
	ParamName         string `json:"param_name,omitempty"`
	RequiresConfirm   bool   `json:"requires_confirm"`
	// Disabled skips this credential from the auto-generated tool
	// catalog without deleting it. Useful for temporarily revoking
	// access (suspected misbehavior, vendor outage, etc.) while
	// keeping the encrypted secret + config intact for re-enabling
	// later. Inverted (Disabled rather than Enabled) so that records
	// written before this field was added — which deserialize to the
	// zero value — keep their default-on behavior.
	Disabled   bool      `json:"disabled,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

// secureCredSecretKey is the DB key under which the encrypted secret
// for a named credential lives. Kept separate from the metadata key
// so listing credentials doesn't pull encrypted blobs into memory.
func secureCredSecretKey(name string) string { return name + "__secret" }

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

const (
	// secureAPIMaxResponseBytes caps response bodies returned to the
	// LLM. Beyond this the body is truncated with a notice. Prevents
	// a runaway endpoint from blowing the LLM's context.
	secureAPIMaxResponseBytes = 1 * 1024 * 1024 // 1 MB

	// secureAPIRequestTimeout caps wall-clock time per call.
	secureAPIRequestTimeout = 30 * time.Second

	// auditRingSize is the per-credential audit-log retention. Older
	// entries are dropped FIFO.
	auditRingSize = 50
)

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
	}
	return secureAPIInstance
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
	case SecureCredBearer, SecureCredHeader, SecureCredQuery, SecureCredBasicAuth:
	default:
		return fmt.Errorf("type must be bearer, header, query, or basic_auth")
	}
	if (c.Type == SecureCredHeader || c.Type == SecureCredQuery) && strings.TrimSpace(c.ParamName) == "" {
		return fmt.Errorf("type %q requires a param_name", c.Type)
	}
	if strings.TrimSpace(c.AllowedURLPattern) == "" {
		return fmt.Errorf("allowed_url_pattern is required (e.g. https://api.github.com/*)")
	}
	if !strings.HasPrefix(c.AllowedURLPattern, "https://") && !strings.HasPrefix(c.AllowedURLPattern, "http://") {
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
	} else {
		c.CreatedAt = time.Now()
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

// Load fetches the public metadata for a credential by name.
func (s *SecureAPI) Load(name string) (SecureCredential, bool) {
	var c SecureCredential
	if !s.ready() || name == "" {
		return c, false
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
		if strings.HasSuffix(k, "__secret") {
			continue
		}
		var c SecureCredential
		if s.db.Get(secureAPITable, k, &c) {
			out = append(out, c)
		}
	}
	return out
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
	if len(entries) > auditRingSize {
		entries = entries[:auditRingSize]
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

// loadSecret returns the decrypted secret value for a credential.
func (s *SecureAPI) loadSecret(name string) (string, bool) {
	if !s.ready() {
		return "", false
	}
	var secret string
	ok := s.db.Get(secureAPITable, secureCredSecretKey(name), &secret)
	return secret, ok
}

// ----------------------------------------------------------------------
// Tool generation + dispatch
// ----------------------------------------------------------------------

// BuildTools converts every enabled registered credential into an
// AgentToolDef. The handler closure captures the credential's metadata;
// secrets are loaded fresh at call time so rotations take effect
// immediately for in-flight sessions.
func (s *SecureAPI) BuildTools() []AgentToolDef {
	if !s.ready() {
		return nil
	}
	allKeys := s.db.Keys(secureAPITable)
	creds := s.List()
	out := make([]AgentToolDef, 0, len(creds))
	disabledCount := 0
	for _, c := range creds {
		if c.Disabled {
			disabledCount++
			continue
		}
		out = append(out, s.agentToolFromCredential(c))
	}
	if len(allKeys) > 0 && len(out) == 0 {
		Debug("[secure_api] BuildTools: %d keys in table, %d credentials decoded, %d disabled, 0 enabled — check key names, struct decode, or Disabled flag", len(allKeys), len(creds), disabledCount)
		for _, k := range allKeys {
			Debug("[secure_api]   key: %q", k)
		}
		for _, c := range creds {
			Debug("[secure_api]   credential: name=%q type=%q disabled=%v allowed=%q", c.Name, c.Type, c.Disabled, c.AllowedURLPattern)
		}
	}
	return out
}

func (s *SecureAPI) agentToolFromCredential(c SecureCredential) AgentToolDef {
	toolName := "call_" + c.Name
	desc := fmt.Sprintf(
		"Call the %s API. The auth credential is injected server-side; you do not see it. Allowed URLs: %s. %s",
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
			},
			Required: []string{"url"},
			Caps:     []Capability{CapNetwork},
		},
		NeedsConfirm: c.RequiresConfirm,
		Handler: func(args map[string]any) (string, error) {
			return s.dispatch(c, args)
		},
	}
}

// DispatchToolCall is the entry point used by api-mode temp tools
// (create_api_tool). Loads the named credential and dispatches a
// pre-resolved request through the same path as the auto-generated
// per-credential tool.
func (s *SecureAPI) DispatchToolCall(credName, urlStr, method, body string) (string, error) {
	if credName == "" {
		return "", fmt.Errorf("credential name required")
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
	return s.dispatch(c, args)
}

// dispatch is the handler logic for one credential's tool. Validates
// the URL, reads the encrypted secret, builds the request with auth
// attached, executes, returns the response body capped at
// secureAPIMaxResponseBytes.
func (s *SecureAPI) dispatch(c SecureCredential, args map[string]any) (string, error) {
	if !s.ready() {
		return "", fmt.Errorf("secure-api store not initialized")
	}
	rawURL := strings.TrimSpace(StringArg(args, "url"))
	if rawURL == "" {
		return "", fmt.Errorf("url is required")
	}
	method := strings.ToUpper(strings.TrimSpace(StringArg(args, "method")))
	if method == "" {
		method = "GET"
	}
	body := StringArg(args, "body")

	// URL allowlist check. THIS IS THE LINCHPIN. If the LLM somehow
	// produces a URL outside the allowed pattern, we refuse — no
	// header is ever attached.
	if !urlMatchesPattern(rawURL, c.AllowedURLPattern) {
		return "", fmt.Errorf("url %q is not allowed for credential %q (allowed pattern: %s)", rawURL, c.Name, c.AllowedURLPattern)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "https" && !strings.HasPrefix(c.AllowedURLPattern, "http://") {
		return "", fmt.Errorf("non-https URL not allowed for credential %q", c.Name)
	}

	secret, ok := s.loadSecret(c.Name)
	if !ok {
		return "", fmt.Errorf("credential %q has no stored secret (re-add it via the admin UI)", c.Name)
	}
	if secret == "" {
		return "", fmt.Errorf("credential %q has empty secret", c.Name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), secureAPIRequestTimeout)
	defer cancel()

	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), bodyReader)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}

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
		idx := strings.Index(secret, ":")
		if idx < 0 {
			return "", fmt.Errorf("basic_auth secret must be 'username:password'")
		}
		auth := base64.StdEncoding.EncodeToString([]byte(secret))
		req.Header.Set("Authorization", "Basic "+auth)
	default:
		return "", fmt.Errorf("unknown credential type %q", c.Type)
	}

	// Build the redaction set BEFORE the request fires. Any string in
	// this slice will be replaced with [REDACTED] in any text we
	// return to the LLM (response body OR error message). Covers the
	// raw secret plus, for basic_auth, the base64-encoded form.
	redactList := []string{secret}
	if c.Type == SecureCredBasicAuth {
		redactList = append(redactList, base64.StdEncoding.EncodeToString([]byte(secret)))
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

	httpClient := &http.Client{Timeout: secureAPIRequestTimeout}
	resp, err := httpClient.Do(req)
	auditEntry := SecureAPIAuditEntry{
		CredentialName: c.Name,
		Method:         method,
		URL:            rawURL,
		Timestamp:      time.Now(),
	}
	if err != nil {
		auditEntry.Error = redact(err.Error())
		s.recordAudit(auditEntry)
		return "", fmt.Errorf("request failed: %s", redact(err.Error()))
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, secureAPIMaxResponseBytes+1)
	bodyBytes, err := io.ReadAll(limited)
	if err != nil {
		auditEntry.Status = resp.StatusCode
		auditEntry.Error = redact("body read: " + err.Error())
		s.recordAudit(auditEntry)
		return "", fmt.Errorf("read response: %s", redact(err.Error()))
	}
	truncated := false
	if len(bodyBytes) > secureAPIMaxResponseBytes {
		bodyBytes = bodyBytes[:secureAPIMaxResponseBytes]
		truncated = true
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
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "json") {
		var anyVal interface{}
		if json.Unmarshal(bodyBytes, &anyVal) == nil {
			if pretty, err := json.MarshalIndent(anyVal, "", "  "); err == nil {
				sb.Write(pretty)
				if truncated {
					sb.WriteString("\n... [TRUNCATED — response exceeded 1MB cap]")
				}
				return sb.String(), nil
			}
		}
	}
	sb.Write(bodyBytes)
	if truncated {
		sb.WriteString("\n... [TRUNCATED — response exceeded 1MB cap]")
	}
	return sb.String(), nil
}

// ----------------------------------------------------------------------
// URL allowlist matching
// ----------------------------------------------------------------------

// urlMatchesPattern reports whether u satisfies pattern. Pattern uses
// a simple glob — `*` matches any non-slash run, `**` matches any run
// including slashes. Designed for endpoint allowlisting, not arbitrary
// regex (regex is too easy to get wrong).
//
//   https://api.github.com/*           matches https://api.github.com/repos
//                                      does NOT match https://api.github.com/repos/x/y
//   https://api.github.com/**          matches both above
//   https://api.example.com/users/*    matches /users/me, NOT /users/me/repos
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
