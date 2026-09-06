package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerCredentialsRoutes wires the credentials API under the admin sub-mux.
func (a *AdminApp) registerCredentialsRoutes(sub *http.ServeMux) {
	// Secure API credentials. GET lists metadata (no secrets). POST
	// upserts a credential (consumes the secret). DELETE removes one.
	// GET ?audit=NAME returns recent calls for that credential.
	sub.HandleFunc("/api/secure-api", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			if name := r.URL.Query().Get("audit"); name != "" {
				// owner selects the namespace: empty = the global credential of
				// that name (the admin page's usual subject), a username = that
				// user's own credential. Without it the audit view could only
				// ever read the global ring, which is the bare-name collision
				// this keying fixes — a per-user audit view passes owner here.
				owner := strings.TrimSpace(r.URL.Query().Get("owner"))
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(Secure().LoadAudit(owner, name))
				return
			}
			// Declaring-tools list for a credential — what a secured cred is
			// locked to, and what a scoped one dispatches through.
			if name := strings.TrimSpace(r.URL.Query().Get("tools")); name != "" {
				refs := []CredentialToolRef{}
				if CredentialToolsResolver != nil {
					if r := CredentialToolsResolver(name); r != nil {
						refs = r
					}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(refs)
				return
			}
			// Tool-binding rows for a SECURED credential: what's approved, what's
			// bound (auto-resolved from its declaration), and what's been revoked
			// (a durable deny). Drives the Bindings expand + its Approve/Revoke
			// actions. Each row carries the credential name so a row action can
			// address both cred + tool.
			if name := strings.TrimSpace(r.URL.Query().Get("bindings")); name != "" {
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
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(rows)
				return
			}
			// Single-record fetch for the declarative edit form's Source.
			// Returns one credential as an object (secret never included —
			// the password field stays blank on edit, which keeps the
			// stored secret unchanged unless the admin types a new one).
			if name := strings.TrimSpace(r.URL.Query().Get("name")); name != "" {
				w.Header().Set("Content-Type", "application/json")
				for _, c := range Secure().ListWithPending() {
					if c.Name == name {
						json.NewEncoder(w).Encode(c)
						return
					}
				}
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			// List — enrich each SECURED credential with whether it's orphaned
			// (locked but no tool uses it → unreachable), for the dead-cred badge.
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
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			// Two POST shapes: the upsert body (full credential) and the
			// toggle action (?action=enable|disable&name=X). Distinguish
			// by query param.
			if action := r.URL.Query().Get("action"); action != "" {
				name := strings.TrimSpace(r.URL.Query().Get("name"))
				if name == "" {
					http.Error(w, "missing name", http.StatusBadRequest)
					return
				}
				switch action {
				case "enable":
					if err := Secure().SetDisabled(name, false); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "disable":
					if err := Secure().SetDisabled(name, true); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "secure":
					// Tool-lock: reachable only through the tools that already
					// declare it, off the auto-route + auto-catalog, and no NEW
					// tool may declare it. Access follows those tools' scope, so
					// the per-agent scope surface no longer applies.
					if err := Secure().SetSecured(name, true); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "unsecure":
					// Release the tool-lock — the credential goes back to the
					// normal (Open) scoped/auto-routable model.
					if err := Secure().SetSecured(name, false); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "access":
					// One idempotent "set lockdown level" from the segmented pill:
					// "open" (unlocked) or "secured" (tool-locked).
					var body struct {
						AccessLevel string `json:"access_level"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					var secure bool
					switch strings.TrimSpace(body.AccessLevel) {
					case "open":
						secure = false
					case "secured":
						secure = true
					default:
						http.Error(w, "access_level must be open|secured", http.StatusBadRequest)
						return
					}
					if err := Secure().SetSecured(name, secure); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "approve_binding":
					// Admin approves a tool's request to bind this secured cred —
					// the tool can then dispatch through it (secret server-side), and
					// access follows the tool's own scope. Clears any revoke tombstone.
					tool := strings.TrimSpace(r.URL.Query().Get("tool"))
					if tool == "" {
						http.Error(w, "missing tool", http.StatusBadRequest)
						return
					}
					if err := Secure().ApproveToolBinding(name, tool); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "revoke_binding":
					// Admin revokes a binding — tombstoned, so dispatch refuses it
					// (the tool stays, but can't reach the cred until re-approved).
					tool := strings.TrimSpace(r.URL.Query().Get("tool"))
					if tool == "" {
						http.Error(w, "missing tool", http.StatusBadRequest)
						return
					}
					if err := Secure().RevokeToolBinding(name, tool); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "test":
					// Mint-and-discard an OAuth token to verify the config +
					// secret before relying on the credential. Returns the
					// outcome (incl. the provider's error on failure) so the
					// admin / LLM-assisted setup can iterate.
					msg, terr := Secure().TestMintToken(name)
					w.Header().Set("Content-Type", "application/json")
					if terr != nil {
						json.NewEncoder(w).Encode(map[string]any{"ok": false, "message": terr.Error()})
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": msg})
					return
				default:
					http.Error(w, "action must be enable|disable|secure|unsecure|access|approve_binding|revoke_binding|test", http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var req struct {
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
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
				return
			}
			c := SecureCredential{
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
			// Preserve the OAuth config of an existing credential when the
			// caller (e.g. the admin just adding the secret to a Builder
			// draft) doesn't resend it. Lets the admin complete a draft by
			// pasting only the secret.
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
			if err := saveCredWithPassword(c, req.Secret, req.Password); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			if err := Secure().Delete(name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Inline "Test token" for an oauth2 credential. The declarative form's
	// TestURL POSTs its current working state (the full record + secret);
	// we mint-and-discard a token from it and return {ok,message} / {ok,error}
	// so the operator can verify the config before relying on it. Works
	// pre-save (uses the typed secret) and on edit (falls back to the stored
	// secret when the password field is left blank).
	sub.HandleFunc("/api/secure-api/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			SecureCredential
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		msg, err := Secure().TestMintFromPosted(body.SecureCredential, body.Secret)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": msg})
	})

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
