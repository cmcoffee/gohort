package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerUsersRoutes wires the users API under the admin sub-mux.
func (a *AdminApp) registerUsersRoutes(sub *http.ServeMux) {
	// API: list users.
	sub.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.Write(UserListJSON(a.db))
		case http.MethodPost:
			a.handleAddUser(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: user candidates for ACL pickers ([{value,label}], approved users only).
	// Shared source for every ui.ACLPicker on this page (credential + tool access).
	sub.HandleFunc("/api/user-candidates", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(UserCandidatesJSON(a.db))
	})

	// API: feature access — the admin gate on outward-facing surfaces a user can
	// expose through their own keys (the OpenAI /v1 endpoint is the first). Reads
	// the generic shareable-feature registry; writes the per-feature allow-list.
	// GET (list) rows one per registered feature; GET ?feature= one policy record
	// for the ACLPicker; POST ?feature= sets its allowed_users. Generic over the
	// registry — an app declares a feature, this renders and stores it, with no
	// per-feature code here.
	sub.HandleFunc("/api/feature-access", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		feature := strings.TrimSpace(r.URL.Query().Get("feature"))
		switch r.Method {
		case http.MethodGet:
			if feature != "" {
				p := LoadFeaturePolicy(a.db, feature)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"feature": feature, "allowed_users": p.AllowedUsers})
				return
			}
			type row struct {
				Feature      string   `json:"feature"` // RowKey
				Label        string   `json:"label"`
				Desc         string   `json:"desc"`
				AllowedUsers []string `json:"allowed_users,omitempty"`
				Access       string   `json:"access"` // human summary for the table
			}
			rows := []row{}
			for _, f := range ShareableFeatures() {
				p := LoadFeaturePolicy(a.db, f.Key)
				access := "All users"
				if len(p.AllowedUsers) > 0 {
					access = fmt.Sprintf("%d user(s)", len(p.AllowedUsers))
				}
				rows = append(rows, row{Feature: f.Key, Label: f.Label, Desc: f.Desc, AllowedUsers: p.AllowedUsers, Access: access})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			if feature == "" || !IsShareableFeature(feature) {
				http.Error(w, "unknown feature", http.StatusBadRequest)
				return
			}
			var body struct {
				AllowedUsers []string `json:"allowed_users"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			SetFeatureAllowedUsers(a.db, feature, body.AllowedUsers)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: user-owned credentials — the governance view over the user plane. The
	// admin API Credentials page shows only GLOBAL creds; this surfaces the ones
	// users create for themselves, with owner-aware revoke (disable) + delete.
	sub.HandleFunc("/api/user-credentials", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			type row struct {
				ID       string `json:"id"` // owner/name — unique across users (RowKey)
				Owner    string `json:"owner"`
				Name     string `json:"name"`
				Type     string `json:"type"`
				Disabled bool   `json:"disabled"`
				Secured  bool   `json:"secured"`
			}
			rows := []row{}
			for _, c := range Secure().ListAllUserOwned() {
				rows = append(rows, row{ID: c.Owner + "/" + c.Name, Owner: c.Owner, Name: c.Name, Type: c.Type, Disabled: c.Disabled, Secured: c.Secured})
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Owner != rows[j].Owner {
					return rows[i].Owner < rows[j].Owner
				}
				return rows[i].Name < rows[j].Name
			})
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if owner == "" || name == "" {
				http.Error(w, "owner and name required", http.StatusBadRequest)
				return
			}
			var err error
			switch r.URL.Query().Get("action") {
			case "disable":
				err = Secure().SetDisabledOwned(owner, name, true)
			case "enable":
				err = Secure().SetDisabledOwned(owner, name, false)
			default:
				http.Error(w, "action must be enable|disable", http.StatusBadRequest)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if owner == "" || name == "" {
				http.Error(w, "owner and name required", http.StatusBadRequest)
				return
			}
			if err := Secure().DeleteUser(owner, name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: user-owned agents — the governance view over peer-shared agents. Agents
	// live in per-user UDBs, so the admin app enumerates them through an orchestrate
	// hook (nil until orchestrate's init runs). Revoke clears an agent's recipient
	// list without deleting the agent.
	sub.HandleFunc("/api/user-agents", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			rows := []UserOwnedAgentRow{}
			if AdminListUserOwnedAgents != nil {
				rows = AdminListUserOwnedAgents(a.db)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			action := r.URL.Query().Get("action")
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if owner == "" || id == "" {
				http.Error(w, "owner and id required", http.StatusBadRequest)
				return
			}
			var hook func(Database, string, string) error
			switch action {
			case "revoke_share":
				hook = AdminRevokeAgentShare
			case "publish": // admin "delegate to users" — flip Exposed on
				hook = AdminPublishAgent
			default:
				http.Error(w, "action must be revoke_share|publish", http.StatusBadRequest)
				return
			}
			if hook == nil {
				http.Error(w, "unavailable (orchestrate not wired)", http.StatusServiceUnavailable)
				return
			}
			if err := hook(a.db, owner, id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// The pipeline twin of /api/user-pipelines' agent sibling above. Same
	// enumerate-and-revoke, minus publish: a pipeline has no app surface to
	// flip on, so there is no second action to offer.
	sub.HandleFunc("/api/user-pipelines", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			rows := []UserOwnedPipelineRow{}
			if AdminListUserOwnedPipelines != nil {
				rows = AdminListUserOwnedPipelines(a.db)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if owner == "" || id == "" {
				http.Error(w, "owner and id required", http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("action") != "revoke_share" {
				http.Error(w, "action must be revoke_share", http.StatusBadRequest)
				return
			}
			if AdminRevokePipelineShare == nil {
				http.Error(w, "unavailable (orchestrate not wired)", http.StatusServiceUnavailable)
				return
			}
			if err := AdminRevokePipelineShare(a.db, owner, id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// The machine twin of the two above. Same enumerate-and-revoke; no publish,
	// for the reason the pipeline route states.
	sub.HandleFunc("/api/user-machines", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			rows := []UserOwnedMachineRow{}
			if AdminListUserOwnedMachines != nil {
				rows = AdminListUserOwnedMachines(a.db)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if owner == "" || id == "" {
				http.Error(w, "owner and id required", http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("action") != "revoke_share" {
				http.Error(w, "action must be revoke_share", http.StatusBadRequest)
				return
			}
			if AdminRevokeMachineShare == nil {
				http.Error(w, "unavailable (orchestrate not wired)", http.StatusServiceUnavailable)
				return
			}
			if err := AdminRevokeMachineShare(a.db, owner, id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: promotion requests — the bottom-up publish queue. Users request that
	// their own resource be published deployment-wide; the admin approves (runs the
	// kind-specific side effect) or denies. Today only tool promotion is wired:
	// approve = Share the tool to the global catalog.
	sub.HandleFunc("/api/promotions", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ListPromotionRequests(a.db, true)) // pending only — the actionable queue
		case http.MethodPost:
			action := r.URL.Query().Get("action")
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			req, ok := GetPromotionRequest(a.db, id)
			if !ok {
				http.NotFound(w, r)
				return
			}
			switch action {
			case "approve":
				// Kind-specific side effect BEFORE marking approved, so a failure
				// leaves the request pending (re-approvable).
				switch req.Kind {
				case "tool":
					if err := SetPersistentTempToolShared(a.db, req.Owner, req.Name, true); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				default:
					http.Error(w, "approving "+req.Kind+" promotions is not supported yet", http.StatusBadRequest)
					return
				}
				if err := SetPromotionRequestState(a.db, id, PromotionApprovedState, AuthCurrentUser(r)); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			case "deny":
				if err := SetPromotionRequestState(a.db, id, PromotionDeniedState, AuthCurrentUser(r)); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			default:
				http.Error(w, "action must be approve|deny", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: global-tool adoptions — who has pulled each shared tool into their
	// fleet, so the admin can see blast radius before revoking one and force-remove
	// a specific user's adoption. One row per (tool, adopter); a stale row is an
	// adoption whose tool has since left the shared catalog.
	sub.HandleFunc("/api/tool-adoptions", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			shared := map[string]bool{}
			for _, p := range LoadSharedPersistentTempTools(a.db) {
				shared[p.Tool.Name] = true
			}
			type row struct {
				ID    string `json:"id"` // tool/user — unique (RowKey)
				Tool  string `json:"tool"`
				User  string `json:"user"`
				Stale bool   `json:"stale"`
			}
			rows := []row{}
			for _, u := range AuthListUsers(a.db) {
				for name := range LoadAdoptedGlobalTools(a.db, u.Username) {
					rows = append(rows, row{ID: name + "/" + u.Username, Tool: name, User: u.Username, Stale: !shared[name]})
				}
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Tool != rows[j].Tool {
					return rows[i].Tool < rows[j].Tool
				}
				return rows[i].User < rows[j].User
			})
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			user := strings.TrimSpace(r.URL.Query().Get("user"))
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if user == "" || name == "" {
				http.Error(w, "user and name required", http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("action") != "unadopt" {
				http.Error(w, "action must be unadopt", http.StatusBadRequest)
				return
			}
			if err := SetGlobalToolAdopted(a.db, user, name, false); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: user operations (update/delete/apps).
	sub.HandleFunc("/api/users/", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/users/")
		// Check for /api/users/{user}/{action}
		if parts := strings.SplitN(rest, "/", 2); len(parts) == 2 {
			username := parts[0]
			action := parts[1]
			switch action {
			case "apps":
				if r.Method == http.MethodPut {
					a.handleUpdateUserApps(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			case "groups":
				if r.Method == http.MethodPut {
					a.handleUpdateUserGroups(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			case "approve":
				if r.Method == http.MethodPost {
					a.handleApproveUser(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			case "reject":
				if r.Method == http.MethodPost {
					a.handleRejectUser(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			case "reset-password":
				if r.Method == http.MethodPost {
					a.handleResetPassword(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			case "data":
				if r.Method == http.MethodGet {
					a.handleUserDataSummary(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			case "data-action":
				if r.Method == http.MethodPost {
					a.handleUserDataAction(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			case "revoke-sessions":
				if r.Method == http.MethodPost {
					a.handleRevokeSessions(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			default:
				http.NotFound(w, r)
			}
			return
		}
		username := rest
		if username == "" {
			http.Error(w, "username required", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			// Return the single-user summary so the framework's
			// ChipPicker (apps) and any other per-user component can
			// fetch the current state without scanning the full list.
			user, ok := AuthGetUser(a.db, username)
			if !ok {
				http.Error(w, "user not found", http.StatusNotFound)
				return
			}
			apps := user.Apps
			if apps == nil {
				apps = []string{}
			}
			groups := user.Groups
			if groups == nil {
				groups = []string{}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"username": user.Username,
				"admin":    user.Admin,
				"pending":  user.Pending,
				"apps":     apps,
				"groups":   groups,
			})
		case http.MethodPut:
			a.handleUpdateUser(w, r, username)
		case http.MethodDelete:
			a.handleDeleteUser(w, r, username)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: current user identity.
	sub.HandleFunc("/api/whoami", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		username := AuthCurrentUser(r)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"username": username})
	})

}

func (a *AdminApp) handleAddUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Admin    bool   `json:"admin"`
		Invite   bool   `json:"invite"` // send a registration link instead of setting a password
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		http.Error(w, "email required", http.StatusBadRequest)
		return
	}
	if _, exists := AuthGetUser(a.db, req.Username); exists {
		http.Error(w, "user already exists", http.StatusConflict)
		return
	}
	current := AuthCurrentUser(r)

	// Invite: create the account with no password and hand back a one-time link
	// the user clicks to set their own. Emailed when mail is configured; always
	// returned so the admin can copy it manually.
	if req.Invite {
		link, err := AuthCreateInvite(a.db, req.Username, req.Admin)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		emailed := false
		if EmailConfigured() {
			body := fmt.Sprintf("You've been invited to %s. Click the link below to set your password and activate your account:\n\n%s\n", ServiceName(), link)
			if SendNotification(req.Username, "["+ServiceName()+"] You've been invited", body) == nil {
				emailed = true
			}
		}
		Log("[admin] user %q invited %q (admin=%v, emailed=%v)", current, req.Username, req.Admin, emailed)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "invited", "link": link, "emailed": emailed})
		return
	}

	// Direct: set a password now.
	if req.Password == "" {
		http.Error(w, "password required (or choose to send an invite link)", http.StatusBadRequest)
		return
	}
	AuthSetUser(a.db, req.Username, req.Password, req.Admin)
	Log("[admin] user %q created user %q (admin=%v)", current, req.Username, req.Admin)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "created"})
}

// handleResetPassword resets a user's password: mode "set" (admin types a new
// one) or "link" (default — email/return a one-time reset link). Owner-of-the-
// deployment authority (admin-gated by the router).
func (a *AdminApp) handleResetPassword(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Mode     string `json:"mode"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // empty body → default to link
	if _, ok := AuthGetUser(a.db, username); !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	current := AuthCurrentUser(r)
	if req.Mode == "set" {
		if len(req.Password) < 6 || len(req.Password) > 128 {
			http.Error(w, "Password must be 6–128 characters.", http.StatusBadRequest)
			return
		}
		if !AuthAdminSetPassword(a.db, username, req.Password) {
			http.Error(w, "could not set password", http.StatusInternalServerError)
			return
		}
		Log("[admin] user %q set password for %q", current, username)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "password_set"})
		return
	}
	// Link mode.
	link, ok := AuthIssueResetLink(a.db, username)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	emailed := false
	if EmailConfigured() {
		body := fmt.Sprintf("A password reset was requested for your %s account. Click the link below to set a new password:\n\n%s\n\nIf you didn't expect this, contact an administrator.", ServiceName(), link)
		if SendNotification(username, "["+ServiceName()+"] Password reset", body) == nil {
			emailed = true
		}
	}
	Log("[admin] user %q issued reset link for %q (emailed=%v)", current, username, emailed)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "link_sent", "link": link, "emailed": emailed})
}

func (a *AdminApp) handleUpdateUser(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Password string `json:"password,omitempty"`
		Admin    *bool  `json:"admin,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	user, ok := AuthGetUser(a.db, username)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	admin := user.Admin
	if req.Admin != nil {
		admin = *req.Admin
	}

	// Prevent removing admin from yourself.
	current := AuthCurrentUser(r)
	if current == username && !admin {
		http.Error(w, "cannot remove admin from yourself", http.StatusBadRequest)
		return
	}

	AuthSetUser(a.db, username, req.Password, admin)
	Log("[admin] user %q updated user %q", current, username)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

func (a *AdminApp) handleApproveUser(w http.ResponseWriter, r *http.Request, username string) {
	user, ok := AuthGetUser(a.db, username)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if !user.Pending {
		http.Error(w, "user is not pending", http.StatusBadRequest)
		return
	}
	AuthApproveUser(a.db, username)
	current := AuthCurrentUser(r)
	Log("[admin] user %q approved %q", current, username)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "approved"})
}

func (a *AdminApp) handleRejectUser(w http.ResponseWriter, r *http.Request, username string) {
	user, ok := AuthGetUser(a.db, username)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if !user.Pending {
		http.Error(w, "user is not pending", http.StatusBadRequest)
		return
	}
	AuthRejectUser(a.db, username)
	current := AuthCurrentUser(r)
	Log("[admin] user %q rejected %q", current, username)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "rejected"})
}

// handleRevokeSessions signs a user out of every browser they are logged into,
// leaving the account itself alone.
//
// There was no way to do this at all: sessions were only ever ended by the
// person holding them clicking Log out, so "their laptop was stolen" had no
// answer short of deleting the account and making a new one. Sliding renewal
// makes waiting for expiry a non-answer too, since a session in use keeps
// pushing its own expiry out.
func (a *AdminApp) handleRevokeSessions(w http.ResponseWriter, r *http.Request, username string) {
	if _, ok := AuthGetUser(a.db, username); !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	n := AuthRevokeUserSessions(a.db, username)
	Log("[admin] user %q signed %q out of %d session(s)", AuthCurrentUser(r), username, n)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"revoked": n})
}

func (a *AdminApp) handleDeleteUser(w http.ResponseWriter, r *http.Request, username string) {
	// Prevent deleting yourself.
	current := AuthCurrentUser(r)
	if current == username {
		http.Error(w, "cannot delete yourself", http.StatusBadRequest)
		return
	}
	if _, ok := AuthGetUser(a.db, username); !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	// Refuse to delete if any registered app still has data for this user
	// unless the caller confirms. The admin UI pre-runs reassign/purge
	// via /data-action before this call.
	if r.URL.Query().Get("force") != "1" {
		for _, h := range RegisteredUserDataHandlers() {
			sum := h.Describe(username)
			for _, n := range sum.Counts {
				if n > 0 {
					http.Error(w, "user still has app data; resolve via data-action or pass ?force=1", http.StatusConflict)
					return
				}
			}
		}
	}

	AuthDeleteUser(a.db, username)
	Log("[admin] user %q deleted user %q", current, username)

	w.WriteHeader(http.StatusNoContent)
}

// handleUserDataSummary returns the per-app data footprint for a user so
// the admin UI can offer reassign/purge before deletion.
func (a *AdminApp) handleUserDataSummary(w http.ResponseWriter, r *http.Request, username string) {
	handlers := RegisteredUserDataHandlers()
	out := make([]UserDataSummary, 0, len(handlers))
	for _, h := range handlers {
		out = append(out, h.Describe(username))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleUserDataAction runs reassign/anonymize/purge on a single app's
// data for a single user. Body: {"app":"codewriter","action":"reassign","target":"other@example.com"}.
func (a *AdminApp) handleUserDataAction(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		App    string `json:"app"`
		Action string `json:"action"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.App == "" || req.Action == "" {
		http.Error(w, "app and action required", http.StatusBadRequest)
		return
	}
	var handler UserDataHandler
	for _, h := range RegisteredUserDataHandlers() {
		if h.AppName() == req.App {
			handler = h
			break
		}
	}
	if handler == nil {
		http.Error(w, "unknown app", http.StatusNotFound)
		return
	}
	var err error
	switch req.Action {
	case "reassign":
		if req.Target == "" {
			http.Error(w, "target required for reassign", http.StatusBadRequest)
			return
		}
		if _, ok := AuthGetUser(a.db, req.Target); !ok {
			http.Error(w, "target user not found", http.StatusNotFound)
			return
		}
		err = handler.Reassign(username, req.Target)
	case "anonymize":
		err = handler.Anonymize(username)
	case "purge":
		err = handler.Purge(username)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	current := AuthCurrentUser(r)
	Log("[admin] user %q ran %s/%s on %q", current, req.App, req.Action, username)
	w.WriteHeader(http.StatusNoContent)
}

// handleUpdateUserApps sets the allowed apps for a specific user.
func (a *AdminApp) handleUpdateUserApps(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Apps []string `json:"apps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if _, ok := AuthGetUser(a.db, username); !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	AuthSetUserApps(a.db, username, req.Apps)
	current := AuthCurrentUser(r)
	Log("[admin] user %q set apps for %q: %v", current, username, req.Apps)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// handleUpdateUserGroups sets the assigned app-group IDs for a specific user.
// Each group expands to its member apps at access-check time.
func (a *AdminApp) handleUpdateUserGroups(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Groups []string `json:"groups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if _, ok := AuthGetUser(a.db, username); !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	AuthSetUserGroups(a.db, username, req.Groups)
	current := AuthCurrentUser(r)
	Log("[admin] user %q set groups for %q: %v", current, username, req.Groups)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}
