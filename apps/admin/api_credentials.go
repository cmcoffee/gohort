package admin

import (
	"errors"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// registerCredentialsRoutes wires the credentials API under the admin sub-mux.
func (a *AdminApp) registerCredentialsRoutes(sub *http.ServeMux) {
	// Secure API credentials. GET lists metadata (no secrets). POST
	// upserts a credential (consumes the secret). DELETE removes one.
	// GET ?audit=NAME returns recent calls for that credential.
	sub.HandleFunc("/api/secure-api", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.readCredentials, a.gate()),
		"POST":   a.credentialPost(),
		"DELETE": jsonapi.Post(a.db, a.deleteCredential, a.gate()),
	}.Serve)

	// Inline "Test token" for an oauth2 credential. The declarative form's
	// TestURL POSTs its current working state (the full record + secret);
	// we mint-and-discard a token from it and return {ok,message} / {ok,error}
	// so the operator can verify the config before relying on it. Works
	// pre-save (uses the typed secret) and on edit (falls back to the stored
	// secret when the password field is left blank).
	sub.HandleFunc("/api/secure-api/test", jsonapi.Post(a.db, a.handleSecureAPITest, a.gate()))

}

// credentialPost tells /api/secure-api's two POST shapes apart: a toggle
// names ?action= (and only the access pill sends a body), the upsert sends the
// whole record.
func (a *AdminApp) credentialPost() http.HandlerFunc {
	toggle := jsonapi.Post(a.db, a.credentialAction, a.gate(), jsonapi.EmptyBodyOK)
	save := jsonapi.Post(a.db, a.saveCredential, a.gate())
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") != "" {
			toggle(w, r)
			return
		}
		save(w, r)
	}
}

// readCredentials answers GET: what one of four queries names (a
// credential's audit, its declaring tools, its bindings, or its record),
// else the list.
func (a *AdminApp) readCredentials(c *jsonapi.Call) (any, error) {
	q := c.R.URL.Query()
	if name := q.Get("audit"); name != "" {
		return a.credentialAudit(c, name), nil
	}
	if name := strings.TrimSpace(q.Get("tools")); name != "" {
		return a.credentialTools(name), nil
	}
	if name := strings.TrimSpace(q.Get("bindings")); name != "" {
		return a.credentialBindings(name), nil
	}
	if name := strings.TrimSpace(q.Get("name")); name != "" {
		return a.credentialRecord(name)
	}
	return a.credentialList(), nil
}

// credentialAudit answers ?audit=: the recent calls through one credential.
func (a *AdminApp) credentialAudit(c *jsonapi.Call, name string) any {
	// owner selects the namespace: empty = the global credential of
	// that name (the admin page's usual subject), a username = that
	// user's own credential. Without it the audit view could only
	// ever read the global ring, which is the bare-name collision
	// this keying fixes — a per-user audit view passes owner here.
	owner := strings.TrimSpace(c.R.URL.Query().Get("owner"))
	return Secure().LoadAudit(owner, name)
}

// credentialTools answers ?tools=.
// Declaring-tools list for a credential — what a secured cred is
// locked to, and what a scoped one dispatches through.
func (a *AdminApp) credentialTools(name string) any {
	refs := []CredentialToolRef{}
	if CredentialToolsResolver != nil {
		if r := CredentialToolsResolver(name); r != nil {
			refs = r
		}
	}
	return refs
}

// credentialBindings answers ?bindings=.
// Tool-binding rows for a SECURED credential: what's approved, what's
// bound (auto-resolved from its declaration), and what's been revoked
// (a durable deny). Drives the Bindings expand + its Approve/Revoke
// actions. Each row carries the credential name so a row action can
// address both cred + tool.
func (a *AdminApp) credentialBindings(name string) any {
	type bindingRow struct {
		Cred     string `json:"cred"`
		Tool     string `json:"tool"`
		Status   string `json:"status"`
		Approved bool   `json:"_approved,omitempty"`
		Revoked  bool   `json:"_revoked,omitempty"`
	}
	rows := []bindingRow{}
	if c, ok := Secure().Load(name); ok {
		for _, t := range c.ApprovedToolBindings {
			rows = append(rows, bindingRow{Cred: name, Tool: t, Status: "bound", Approved: true})
		}
		for _, t := range c.RevokedToolBindings {
			rows = append(rows, bindingRow{Cred: name, Tool: t, Status: "revoked", Revoked: true})
		}
	}
	return rows
}

// credentialRecord answers ?name=.
// Single-record fetch for the declarative edit form's Source.
// Returns one credential as an object (secret never included —
// the password field stays blank on edit, which keeps the
// stored secret unchanged unless the admin types a new one).
func (a *AdminApp) credentialRecord(name string) (any, error) {
	for _, c := range Secure().ListWithPending() {
		if c.Name == name {
			return c, nil
		}
	}
	return nil, jsonapi.NotFound("not found")
}

// credentialList answers a bare GET.
// List — enrich each SECURED credential with whether it's orphaned
// (locked but no tool uses it → unreachable), for the dead-cred badge.
func (a *AdminApp) credentialList() any {
	type credRow struct {
		SecureCredential
		Orphaned bool `json:"orphaned,omitempty"`
		// AccessLevel is what the admin picks from the segmented pill:
		// "open" (generic call tool + auto-route + per-agent scope) or
		// "secured" (tool-locked; off auto-route + scope).
		AccessLevel string `json:"access_level"`
		// Namespace classifies the credential: "global" (Owner empty — this
		// admin surface manages it) vs "user" (owned by a user's namespace).
		Namespace string `json:"namespace"`
	}
	creds := Secure().ListWithPending()
	rows := make([]credRow, len(creds))
	for i, c := range creds {
		// Orphaned = secured and reachable by NOTHING. Connectors
		// count: a connector naming a credential is a declaring
		// consumer exactly as a tool with fetch_via: is, and
		// checking only tools badged a working credential
		// unreachable — peer image backends are the case that
		// surfaced it, and the local image and messaging
		// connectors have the same shape.
		orphaned := false
		if c.Secured && CredentialToolsResolver != nil {
			orphaned = len(CredentialToolsResolver(c.Name)) == 0 &&
				len(ConnectorsUsingCredential(RootDB, c.Name)) == 0
		}
		level := "open"
		if c.Secured {
			level = "secured"
		}
		ns := "global"
		if c.Owner != "" {
			ns = "user"
		}
		rows[i] = credRow{SecureCredential: c, Orphaned: orphaned, AccessLevel: level, Namespace: ns}
	}
	return rows
}

// credentialAction answers POST ?action=: one toggle on one credential.
func (a *AdminApp) credentialAction(c *jsonapi.Call, body struct {
	AccessLevel string `json:"access_level"` // the access pill's pick
}) (any, error) {
	r := c.R
	action := r.URL.Query().Get("action")
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	switch action {
	case "enable":
		if err := Secure().SetDisabled(name, false); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	case "disable":
		if err := Secure().SetDisabled(name, true); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	case "secure":
		// Tool-lock: reachable only through the tools that already
		// declare it, off the auto-route + auto-catalog, and no NEW
		// tool may declare it. Access follows those tools' scope, so
		// the per-agent scope surface no longer applies.
		if err := Secure().SetSecured(name, true); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	case "unsecure":
		// Release the tool-lock — the credential goes back to the
		// normal (Open) scoped/auto-routable model.
		if err := Secure().SetSecured(name, false); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	case "access":
		// One idempotent "set lockdown level" from the segmented pill:
		// "open" (unlocked) or "secured" (tool-locked).
		var secure bool
		switch strings.TrimSpace(body.AccessLevel) {
		case "open":
			secure = false
		case "secured":
			secure = true
		default:
			return nil, jsonapi.BadRequest("access_level must be open|secured")
		}
		if err := Secure().SetSecured(name, secure); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	case "approve_binding":
		// Admin approves a tool's request to bind this secured cred —
		// the tool can then dispatch through it (secret server-side), and
		// access follows the tool's own scope. Clears any revoke tombstone.
		tool := strings.TrimSpace(r.URL.Query().Get("tool"))
		if tool == "" {
			return nil, jsonapi.BadRequest("missing tool")
		}
		if err := Secure().ApproveToolBinding(name, tool); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	case "revoke_binding":
		// Admin revokes a binding — tombstoned, so dispatch refuses it
		// (the tool stays, but can't reach the cred until re-approved).
		tool := strings.TrimSpace(r.URL.Query().Get("tool"))
		if tool == "" {
			return nil, jsonapi.BadRequest("missing tool")
		}
		if err := Secure().RevokeToolBinding(name, tool); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	case "test":
		// Mint-and-discard an OAuth token to verify the config +
		// secret before relying on the credential. Returns the
		// outcome (incl. the provider's error on failure) so the
		// admin / LLM-assisted setup can iterate.
		msg, terr := Secure().TestMintToken(r.Context(), name)
		if terr != nil {
			return map[string]any{"ok": false, "message": terr.Error()}, nil
		}
		return map[string]any{"ok": true, "message": msg}, nil
	default:
		return nil, jsonapi.BadRequest("action must be enable|disable|secure|unsecure|access|approve_binding|revoke_binding|test")
	}
	RecordActivity(r, activity.Credential, c.User, credentialActionDetail(action, name, r.URL.Query().Get("tool")))
	return jsonapi.Empty(http.StatusNoContent), nil
}

// credentialSave is the record the credential form posts, secrets with it.
type credentialSave struct {
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	AllowedURLPattern string   `json:"allowed_url_pattern"`
	BaseURL           string   `json:"base_url"`
	AllowedEndpoints  []string `json:"allowed_endpoints"`
	AllowedUsers      []string `json:"allowed_users"`
	ParamName         string   `json:"param_name"`
	Description       string   `json:"description"`
	CredScope         string   `json:"cred_scope"`
	RequiresConfirm   bool     `json:"requires_confirm"`
	ConfirmWrites     bool     `json:"confirm_writes"`
	InsecureSkipTLS   bool     `json:"insecure_skip_tls"`
	Secret            string   `json:"secret"`
	Password          string   `json:"password"` // password-grant resource-owner password (the 2nd secret)
	AllowedMethods    []string `json:"allowed_methods"`
	DeniedURLPatterns []string `json:"denied_url_patterns"`
	MaxCallsPerDay    int      `json:"max_calls_per_day"`
	CostPerCall       float64  `json:"cost_per_call"`
	// OAuth2 (type == "oauth2").
	Grant        string `json:"grant"`
	TokenURL     string `json:"token_url"`
	AuthorizeURL string `json:"authorize_url"`
	ClientID     string `json:"client_id"`
	Username     string `json:"username"` // password grant: resource-owner username
	Scope        string `json:"scope"`
	JWTIssuer    string `json:"jwt_issuer"`
	JWTSubject   string `json:"jwt_subject"`
	JWTAudience  string `json:"jwt_audience"`
	JWTKeyID     string `json:"jwt_key_id"`
}

// saveCredential answers a POST of the whole record: create or edit.
func (a *AdminApp) saveCredential(c *jsonapi.Call, req credentialSave) (any, error) {
	cred := SecureCredential{
		Name:              strings.TrimSpace(req.Name),
		Type:              strings.TrimSpace(req.Type),
		AllowedURLPattern: strings.TrimSpace(req.AllowedURLPattern),
		BaseURL:           strings.TrimSpace(req.BaseURL),
		AllowedEndpoints:  req.AllowedEndpoints,
		AllowedUsers:      req.AllowedUsers,
		ParamName:         strings.TrimSpace(req.ParamName),
		Description:       strings.TrimSpace(req.Description),
		CredScope:         strings.TrimSpace(req.CredScope),
		RequiresConfirm:   req.RequiresConfirm,
		ConfirmWrites:     req.ConfirmWrites,
		InsecureSkipTLS:   req.InsecureSkipTLS,
		AllowedMethods:    req.AllowedMethods,
		DeniedURLPatterns: req.DeniedURLPatterns,
		MaxCallsPerDay:    req.MaxCallsPerDay,
		CostPerCall:       req.CostPerCall,
		Grant:             strings.TrimSpace(req.Grant),
		TokenURL:          strings.TrimSpace(req.TokenURL),
		AuthorizeURL:      strings.TrimSpace(req.AuthorizeURL),
		ClientID:          strings.TrimSpace(req.ClientID),
		Username:          strings.TrimSpace(req.Username),
		Scope:             strings.TrimSpace(req.Scope),
		JWTIssuer:         strings.TrimSpace(req.JWTIssuer),
		JWTSubject:        strings.TrimSpace(req.JWTSubject),
		JWTAudience:       strings.TrimSpace(req.JWTAudience),
		JWTKeyID:          strings.TrimSpace(req.JWTKeyID),
	}
	keepOAuthConfig(&cred)
	if err := saveCredWithPassword(cred, req.Secret, req.Password); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	// By name only: the log is read by every administrator.
	RecordActivity(c.R, activity.Credential, c.User, "saved the credential "+cred.Name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// Preserve the OAuth config of an existing credential when the
// caller (e.g. the admin just adding the secret to a Builder
// draft) doesn't resend it. Lets the admin complete a draft by
// pasting only the secret.
func keepOAuthConfig(c *SecureCredential) {
	if c.Type == SecureCredOAuth2 {
		if existing, ok := Secure().Load(c.Name); ok && existing.Type == SecureCredOAuth2 {
			if c.Grant == "" {
				c.Grant = existing.Grant
			}
			if c.TokenURL == "" {
				c.TokenURL = existing.TokenURL
			}
			if c.AuthorizeURL == "" {
				c.AuthorizeURL = existing.AuthorizeURL
			}
			if c.ClientID == "" {
				c.ClientID = existing.ClientID
			}
			if c.Username == "" {
				c.Username = existing.Username
			}
			if c.Scope == "" {
				c.Scope = existing.Scope
			}
			if c.JWTIssuer == "" {
				c.JWTIssuer = existing.JWTIssuer
			}
			if c.JWTSubject == "" {
				c.JWTSubject = existing.JWTSubject
			}
			if c.JWTAudience == "" {
				c.JWTAudience = existing.JWTAudience
			}
			if c.JWTKeyID == "" {
				c.JWTKeyID = existing.JWTKeyID
			}
		}
	}
}

// deleteCredential answers DELETE: removes one credential.
func (a *AdminApp) deleteCredential(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	if err := Secure().Delete(name); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	RecordActivity(c.R, activity.Credential, c.User, "deleted the credential "+name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleSecureAPITest serves /api/secure-api/test; registerCredentialsRoutes says what it is for.
func (a *AdminApp) handleSecureAPITest(c *jsonapi.Call, body struct {
	SecureCredential
	Secret string `json:"secret"`
}) (any, error) {
	// Under the request's context, so Cancel ends the token request.
	ctx := c.R.Context()
	msg, err := Secure().TestMintFromPosted(ctx, body.SecureCredential, body.Secret)
	if err != nil {
		if ctx.Err() != nil {
			err = errors.New("cancelled before the token endpoint answered")
		}
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	return map[string]any{"ok": true, "message": msg}, nil
}

// RegisterRoutes configures the administrative web interface and API endpoints.
// It sets up a sub-mux with routes for user management, system settings,
// cost tracking, and vector statistics, then prepares a gated handler to
// be mounted to the provided mux under the specified prefix.
// saveCredWithPassword saves a secure-API credential, plus the oauth2
// password-grant SECOND secret (the resource-owner password) when the grant is
// password. Empty password = keep the existing one, mirroring Save's
// empty-means-keep secret semantics.
func saveCredWithPassword(c SecureCredential, secret, password string) error {
	if err := Secure().Save(c, secret); err != nil {
		return err
	}
	if c.Grant == OAuthGrantPassword {
		return Secure().SavePassword(c.Name, password)
	}
	return nil
}

// credentialActionDetail says, for the activity log, what a credential toggle
// did.
func credentialActionDetail(action, name, tool string) string {
	switch action {
	case "enable":
		return "enabled the credential " + name
	case "disable":
		return "disabled the credential " + name
	case "secure":
		return "locked the credential " + name + " to the tools that declare it"
	case "unsecure":
		return "unlocked the credential " + name
	case "access":
		return "set the access of the credential " + name
	case "approve_binding":
		return "let the tool " + tool + " use the credential " + name
	case "revoke_binding":
		return "stopped the tool " + tool + " using the credential " + name
	}
	return action + " on the credential " + name
}
