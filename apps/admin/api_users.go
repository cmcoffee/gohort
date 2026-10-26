package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/promotion"
)

// registerUsersRoutes wires the users API under the admin sub-mux.
func (a *AdminApp) registerUsersRoutes(sub *http.ServeMux) {
	// API: list users.
	sub.HandleFunc("/api/users", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleUsersAPI, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleAddUser, a.gate()),
	}.Serve)

	// API: user candidates for ACL pickers ([{value,label}], approved users only).
	// Shared source for every ui.ACLPicker on this page (credential + tool access).
	sub.HandleFunc("/api/user-candidates", jsonapi.Get(a.db, a.handleUserCandidates, a.gate()))

	// API: feature access — the admin gate on outward-facing surfaces a user can
	// expose through their own keys (the OpenAI /v1 endpoint is the first). Reads
	// the generic shareable-feature registry; writes the per-feature allow-list.
	// GET (list) rows one per registered feature; GET ?feature= one policy record
	// for the ACLPicker; POST ?feature= sets its allowed_users. Generic over the
	// registry — an app declares a feature, this renders and stores it, with no
	// per-feature code here.
	sub.HandleFunc("/api/feature-access", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleFeatureAccess, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleFeatureAccessSave, a.gate()),
	}.Serve)

	// API: user-owned credentials — the governance view over the user plane. The
	// admin API Credentials page shows only GLOBAL creds; this surfaces the ones
	// users create for themselves, with owner-aware revoke (disable) + delete.
	sub.HandleFunc("/api/user-credentials", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleUserCredentials, a.gate()),
		"POST":   jsonapi.Post(a.db, a.handleUserCredentialAction, a.gate()),
		"DELETE": jsonapi.Delete(a.db, a.handleUserCredentialDelete, a.gate()),
	}.Serve)
	// Whether people add their own credentials and MCP servers at all.
	sub.HandleFunc("/api/personal-integrations", jsonapi.Methods{
		"GET":   jsonapi.Get(a.db, a.handlePersonalIntegrations, a.gate()),
		"POST":  jsonapi.Post(a.db, a.handlePersonalIntegrationsSave, a.gate(), jsonapi.MaxBody(1<<16)),
		"PUT":   jsonapi.Put(a.db, a.handlePersonalIntegrationsSave, a.gate(), jsonapi.MaxBody(1<<16)),
		"PATCH": jsonapi.Patch(a.db, a.handlePersonalIntegrationsSave, a.gate(), jsonapi.MaxBody(1<<16)),
	}.Serve)
	sub.HandleFunc("/api/personal-mcp-servers", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handlePersonalMCPServers, a.gate()),
		"POST":   jsonapi.Post(a.db, a.handlePersonalMCPServerAction, a.gate()),
		"DELETE": jsonapi.Delete(a.db, a.handlePersonalMCPServerDelete, a.gate()),
	}.Serve)

	// API: user-owned agents — the governance view over peer-shared agents. Agents
	// live in per-user UDBs, so the admin app enumerates them through an orchestrate
	// hook (nil until orchestrate's init runs). Revoke clears an agent's recipient
	// list without deleting the agent.
	sub.HandleFunc("/api/user-agents", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleUserAgents, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleUserAgentsAction, a.gate()),
	}.Serve)

	// The pipeline twin of /api/user-pipelines' agent sibling above. Same
	// enumerate-and-revoke, minus publish: a pipeline has no app surface to
	// flip on, so there is no second action to offer.
	sub.HandleFunc("/api/user-pipelines", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleUserPipelines, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleUserPipelinesAction, a.gate()),
	}.Serve)

	// The machine twin of the two above. Same enumerate-and-revoke; no publish,
	// for the reason the pipeline route states.
	sub.HandleFunc("/api/user-machines", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleUserMachines, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleUserMachinesAction, a.gate()),
	}.Serve)

	// API: promotion requests — the bottom-up publish queue. Users request that
	// their own resource be published deployment-wide; the admin approves (runs the
	// kind-specific side effect) or denies. Approve runs the kind's registered
	// approver: a tool is Shared to the global catalog, an app is shared to
	// every signed-in user, a public_link mints an app's anonymous URL.
	sub.HandleFunc("/api/promotions", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handlePromotions, a.gate()),
		"POST": jsonapi.Post(a.db, a.handlePromotionAction, a.gate()),
	}.Serve)

	// API: the global tool catalog — what this deployment publishes, and who
	// may take it.
	//
	// This replaced a per-(user, tool) ADOPTION ledger with a force-remove
	// button. Adoption is the user's own opt-in from their Extensions catalog:
	// which of the tools they are permitted to have they actually want loaded.
	// An admin reaching into that was answering a question nobody asks — the
	// governance question is whether a tool is published and to whom, and an
	// admin who wants somebody to stop using one takes away the permission
	// rather than the preference. Unshare and Access do that, on the row.
	sub.HandleFunc("/api/global-tools", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleGlobalTools, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleGlobalToolRollback, a.gate()),
	}.Serve)

	// API: user operations (update/delete/apps).
	sub.HandleFunc("/api/users/", a.userRoutes())

}

// handleUsersAPI serves /api/users; registerUsersRoutes says what it is for.
func (a *AdminApp) handleUsersAPI(c *jsonapi.Call) (any, error) {
	return json.RawMessage(UserListJSON(a.db)), nil
}

// handleUserCandidates serves /api/user-candidates; registerUsersRoutes says what it is for.
func (a *AdminApp) handleUserCandidates(c *jsonapi.Call) (any, error) {
	// No exclusion: an admin granting access to a deployment resource may
	// legitimately be one of the people granted it.
	return json.RawMessage(UserCandidatesJSON(a.db, "")), nil
}

// handleFeatureAccess serves /api/feature-access; registerUsersRoutes says what it is for.
func (a *AdminApp) handleFeatureAccess(c *jsonapi.Call) (any, error) {
	if feature := strings.TrimSpace(c.R.URL.Query().Get("feature")); feature != "" {
		p := LoadFeaturePolicy(a.db, feature)
		return map[string]any{"feature": feature, "allowed_users": p.AllowedUsers}, nil
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
	return rows, nil
}

// handleFeatureAccessSave sets who may use the ?feature= named.
func (a *AdminApp) handleFeatureAccessSave(c *jsonapi.Call, body struct {
	AllowedUsers []string `json:"allowed_users"`
}) (any, error) {
	feature := strings.TrimSpace(c.R.URL.Query().Get("feature"))
	if feature == "" || !IsShareableFeature(feature) {
		return nil, jsonapi.BadRequest("unknown feature")
	}
	SetFeatureAllowedUsers(a.db, feature, body.AllowedUsers)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleUserCredentials serves /api/user-credentials; registerUsersRoutes says what it is for.
func (a *AdminApp) handleUserCredentials(c *jsonapi.Call) (any, error) {
	type row struct {
		ID       string `json:"id"` // owner/name — unique across users (RowKey)
		Owner    string `json:"owner"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Disabled bool   `json:"disabled"`
		Secured  bool   `json:"secured"`
		// Who else can use this key, and whether any of them can write
		// with it. A share the admin cannot see is a share they cannot
		// govern, and a write lend is the one that arrives at the far
		// end wearing the owner's name.
		LentTo      string `json:"lent_to,omitempty"`
		Lent        bool   `json:"lent"`
		LendsWrites bool   `json:"lends_writes"`
		LendLocked  bool   `json:"lend_locked"`
		// Added while personal credentials need approval, and not yet
		// approved: Enable approves it.
		Awaiting bool `json:"awaiting"`
	}
	rows := []row{}
	for _, cr := range Secure().ListAllUserOwned() {
		rows = append(rows, row{ID: cr.Owner + "/" + cr.Name, Owner: cr.Owner, Name: cr.Name, Type: cr.Type, Disabled: cr.Disabled, Secured: cr.Secured,
			LentTo:      describeLend(cr),
			Lent:        len(cr.SharedReadOnly)+len(cr.SharedReadWrite) > 0,
			LendsWrites: len(cr.SharedReadWrite) > 0,
			LendLocked:  cr.AdminLendLocked,
			Awaiting:    cr.AwaitingApproval})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Owner != rows[j].Owner {
			return rows[i].Owner < rows[j].Owner
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

// handleUserCredentialAction holds an admin's decision on one user's
// credential.
func (a *AdminApp) handleUserCredentialAction(c *jsonapi.Call, _ struct{}) (any, error) {
	owner, name, err := ownedCredentialArgs(c)
	if err != nil {
		return nil, err
	}
	switch c.R.URL.Query().Get("action") {
	// The admin's actions HOLD: the owner's own switch cannot undo them
	// (AdminDisabled / AdminLendLocked); only an admin action here can.
	case "disable":
		err = Secure().AdminSetDisabledOwned(owner, name, true)
	case "enable":
		err = Secure().AdminSetDisabledOwned(owner, name, false)
	case "revoke_share":
		// Both lists at once. An admin either accepts a lend or stops
		// it; narrowing somebody's grant from writes to reads on their
		// behalf would leave the owner believing they gave one thing
		// and the borrower holding another.
		err = Secure().AdminRevokeShares(owner, name)
	case "allow_share":
		err = Secure().AdminAllowLending(owner, name)
	default:
		return nil, jsonapi.BadRequest("action must be enable|disable|revoke_share|allow_share")
	}
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleUserCredentialDelete removes one user's credential.
func (a *AdminApp) handleUserCredentialDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	owner, name, err := ownedCredentialArgs(c)
	if err != nil {
		return nil, err
	}
	if err = Secure().DeleteUser(owner, name); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// ownedCredentialArgs is the ?owner=&name= a credential action names.
func ownedCredentialArgs(c *jsonapi.Call) (owner, name string, err error) {
	q := c.R.URL.Query()
	owner, name = strings.TrimSpace(q.Get("owner")), strings.TrimSpace(q.Get("name"))
	if owner == "" || name == "" {
		return "", "", jsonapi.BadRequest("owner and name required")
	}
	return owner, name, nil
}

// handleUserAgents serves /api/user-agents; registerUsersRoutes says what it is for.
func (a *AdminApp) handleUserAgents(c *jsonapi.Call) (any, error) {
	rows := []UserOwnedAgentRow{}
	if AdminListUserOwnedAgents != nil {
		rows = AdminListUserOwnedAgents(a.db)
	}
	return rows, nil
}

// handleUserAgentsAction acts on one row of handleUserAgents.
func (a *AdminApp) handleUserAgentsAction(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	owner := strings.TrimSpace(q.Get("owner"))
	id := strings.TrimSpace(q.Get("id"))
	if owner == "" || id == "" {
		return nil, jsonapi.BadRequest("owner and id required")
	}
	var hook func(Database, string, string) error
	switch q.Get("action") {
	case "revoke_share":
		hook = AdminRevokeAgentShare
	case "publish": // admin "delegate to users" — flip Exposed on
		hook = AdminPublishAgent
	default:
		return nil, jsonapi.BadRequest("action must be revoke_share|publish")
	}
	if hook == nil {
		return nil, jsonapi.Status(http.StatusServiceUnavailable, "unavailable (orchestrate not wired)")
	}
	if err := hook(a.db, owner, id); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleUserPipelines serves /api/user-pipelines; registerUsersRoutes says what it is for.
func (a *AdminApp) handleUserPipelines(c *jsonapi.Call) (any, error) {
	rows := []UserOwnedPipelineRow{}
	if AdminListUserOwnedPipelines != nil {
		rows = AdminListUserOwnedPipelines(a.db)
	}
	return rows, nil
}

// handleUserPipelinesAction acts on one row of handleUserPipelines.
func (a *AdminApp) handleUserPipelinesAction(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	owner := strings.TrimSpace(q.Get("owner"))
	id := strings.TrimSpace(q.Get("id"))
	if owner == "" || id == "" {
		return nil, jsonapi.BadRequest("owner and id required")
	}
	if q.Get("action") != "revoke_share" {
		return nil, jsonapi.BadRequest("action must be revoke_share")
	}
	if AdminRevokePipelineShare == nil {
		return nil, jsonapi.Status(http.StatusServiceUnavailable, "unavailable (orchestrate not wired)")
	}
	if err := AdminRevokePipelineShare(a.db, owner, id); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleUserMachines serves /api/user-machines; registerUsersRoutes says what it is for.
func (a *AdminApp) handleUserMachines(c *jsonapi.Call) (any, error) {
	rows := []UserOwnedMachineRow{}
	if AdminListUserOwnedMachines != nil {
		rows = AdminListUserOwnedMachines(a.db)
	}
	return rows, nil
}

// handleUserMachinesAction acts on one row of handleUserMachines.
func (a *AdminApp) handleUserMachinesAction(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	owner := strings.TrimSpace(q.Get("owner"))
	id := strings.TrimSpace(q.Get("id"))
	if owner == "" || id == "" {
		return nil, jsonapi.BadRequest("owner and id required")
	}
	if q.Get("action") != "revoke_share" {
		return nil, jsonapi.BadRequest("action must be revoke_share")
	}
	if AdminRevokeMachineShare == nil {
		return nil, jsonapi.Status(http.StatusServiceUnavailable, "unavailable (orchestrate not wired)")
	}
	if err := AdminRevokeMachineShare(a.db, owner, id); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handlePromotions serves /api/promotions; registerUsersRoutes says what it is for.
func (a *AdminApp) handlePromotions(c *jsonapi.Call) (any, error) {
	// A tool request carries what approving it would publish: the
	// definition the owner's request froze, as a diff against the
	// published version for an update, whole for a first publish.
	type reqRow struct {
		PromotionRequest
		Update bool   `json:"update,omitempty"`
		Review string `json:"review,omitempty"`
	}
	releases := map[string]ToolRelease{}
	for _, rel := range ToolReleases(a.db) {
		releases[rel.Name] = rel
	}
	rows := []reqRow{}
	for _, req := range ListPromotionRequests(a.db, true) { // pending only — the actionable queue
		row := reqRow{PromotionRequest: req}
		if req.Kind == "tool" {
			row.Update, row.Review = toolRequestReview(a.db, releases, req.Owner, req.Name)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// handlePromotionAction approves or denies the ?id= request.
func (a *AdminApp) handlePromotionAction(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	action := q.Get("action")
	id := strings.TrimSpace(q.Get("id"))
	if id == "" {
		return nil, jsonapi.BadRequest("missing id")
	}
	req, ok := GetPromotionRequest(a.db, id)
	if !ok {
		return nil, jsonapi.NotFound("no such request")
	}
	switch action {
	case "approve":
		// The kind's registered side effect runs BEFORE the row is
		// marked approved, so a failure leaves the request pending
		// (re-approvable). Each kind registers its own approver (a
		// tool in core, an app in customapps); a kind without one
		// is refused here.
		if err := promotion.Approve(a.db, id, c.User); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	case "deny":
		if err := SetPromotionRequestState(a.db, id, PromotionDeniedState, c.User); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	default:
		return nil, jsonapi.BadRequest("action must be approve|deny")
	}
	verb := map[string]string{"approve": "approved", "deny": "denied"}[action]
	RecordActivity(c.R, activity.Approval, c.User, verb+" publishing the "+req.Kind+" "+req.Name+" of "+req.Owner)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleGlobalTools serves /api/global-tools; registerUsersRoutes says what it is for.
func (a *AdminApp) handleGlobalTools(c *jsonapi.Call) (any, error) {
	releases := a.toolReleases()
	// ?history=<tool>: the versions a release keeps to roll back to,
	// each with what rolling back to it would change.
	if name := strings.TrimSpace(c.R.URL.Query().Get("history")); name != "" {
		type histRow struct {
			Tool        string `json:"tool"`
			Owner       string `json:"owner"`
			PastVersion int    `json:"past_version"`
			Label       string `json:"label"`
			ApprovedAt  string `json:"approved_at,omitempty"`
			ApprovedBy  string `json:"approved_by,omitempty"`
			Changes     string `json:"changes"`
		}
		hist := []histRow{}
		if rel, ok := releases[name]; ok {
			for _, h := range rel.History {
				hr := histRow{
					Tool: rel.Name, Owner: rel.Owner, PastVersion: h.Version,
					Label: fmt.Sprintf("v%d", h.Version), ApprovedBy: h.ApprovedBy,
					Changes: rel.Tool.DefinitionDiff(h.Tool),
				}
				if !h.ApprovedAt.IsZero() {
					hr.ApprovedAt = h.ApprovedAt.UTC().Format("2006-01-02T15:04:05Z")
				}
				if hr.Changes == "" {
					hr.Changes = "The same definition as the current version."
				}
				hist = append(hist, hr)
			}
		}
		return hist, nil
	}
	type row struct {
		ID     string `json:"id"`
		Tool   string `json:"tool"`
		Owner  string `json:"owner"`
		Access string `json:"access"`
		// Restricted drives the badge: "every user" and "these three"
		// are different grants and should not read the same.
		Restricted bool `json:"restricted"`
		// Version is the approved version adopters run; the owner's
		// working copy may have moved on, which only an approved
		// update carries to them.
		Version    string `json:"version"`
		ApprovedAt string `json:"approved_at,omitempty"`
		HasHistory bool   `json:"has_history"`
		// UpdateRequested: the owner asked for their current copy to
		// become the next version; Review is the diff to approve or
		// deny in Pending promotions.
		UpdateRequested bool   `json:"update_requested"`
		Review          string `json:"review,omitempty"`
	}
	rows := []row{}
	// Who published each one. A catalog entry with no name behind it
	// is one nobody is answerable for.
	owners := SharedToolOwners(a.db)
	for _, p := range LoadSharedPersistentTempTools(a.db) {
		access, restricted := "Every user", false
		if len(p.AllowedUsers) > 0 {
			access, restricted = strings.Join(p.AllowedUsers, ", "), true
		}
		rw := row{
			ID: p.Tool.Name, Tool: p.Tool.Name, Owner: owners[p.Tool.Name],
			Access: access, Restricted: restricted,
		}
		if rel, ok := releases[p.Tool.Name]; ok {
			rw.Version = fmt.Sprintf("v%d", rel.Version)
			rw.HasHistory = len(rel.History) > 0
			if !rel.ApprovedAt.IsZero() {
				rw.ApprovedAt = rel.ApprovedAt.UTC().Format("2006-01-02T15:04:05Z")
			}
			if PendingPromotion(a.db, rel.Owner, "tool", rel.Name) {
				rw.UpdateRequested, rw.Review = toolRequestReview(a.db, releases, rel.Owner, rel.Name)
			}
		}
		rows = append(rows, rw)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Tool < rows[j].Tool })
	return rows, nil
}

// toolReleases is each published tool's release, by name.
func (a *AdminApp) toolReleases() map[string]ToolRelease {
	releases := map[string]ToolRelease{}
	for _, rel := range ToolReleases(a.db) {
		releases[rel.Name] = rel
	}
	return releases
}

// handleGlobalToolRollback is ?action=rollback&tool=&owner=&to=<version>:
// make a kept version the one adopters run. The one it replaces is kept in
// turn.
func (a *AdminApp) handleGlobalToolRollback(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	if q.Get("action") != "rollback" {
		return nil, jsonapi.BadRequest("action must be rollback")
	}
	name := strings.TrimSpace(q.Get("tool"))
	rel, ok := a.toolReleases()[name]
	if !ok || rel.Owner != strings.TrimSpace(q.Get("owner")) {
		return nil, jsonapi.NotFound("no published tool " + name + " of that owner's")
	}
	var to int
	if _, err := fmt.Sscanf(q.Get("to"), "%d", &to); err != nil {
		return nil, jsonapi.BadRequest("to must be a version number")
	}
	if err := rel.RollBack(a.db, to, c.User); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// userRoutes serves /api/users/{user} and /api/users/{user}/{action}. Every
// change it makes that succeeds is written to the activity log.
func (a *AdminApp) userRoutes() http.HandlerFunc {
	one := jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleUserOne, a.gate()),
		"PUT":    jsonapi.Put(a.db, audit("changed the account", a.handleUpdateUser), a.gate()),
		"DELETE": jsonapi.Delete(a.db, audit("deleted the account", a.handleDeleteUser), a.gate()),
	}
	actions := map[string]jsonapi.Methods{
		"apps":            {"PUT": jsonapi.Put(a.db, audit("changed the apps of", a.handleUpdateUserApps), a.gate())},
		"groups":          {"PUT": jsonapi.Put(a.db, audit("changed the groups of", a.handleUpdateUserGroups), a.gate())},
		"approve":         {"POST": jsonapi.Post(a.db, audit("approved the account", a.handleApproveUser), a.gate())},
		"reject":          {"POST": jsonapi.Post(a.db, audit("rejected the account", a.handleRejectUser), a.gate())},
		"reset-password":  {"POST": jsonapi.Post(a.db, audit("reset the password of", a.handleResetPassword), a.gate(), jsonapi.EmptyBodyOK)},
		"data":            {"GET": jsonapi.Get(a.db, a.handleUserDataSummary, a.gate())},
		"data-action":     {"POST": jsonapi.Post(a.db, audit("moved or deleted the data of", a.handleUserDataAction), a.gate())},
		"revoke-sessions": {"POST": jsonapi.Post(a.db, audit("signed %s out everywhere", a.handleRevokeSessions), a.gate())},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		_, action, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/users/"), "/")
		if !ok {
			one.Serve(w, r)
			return
		}
		m, ok := actions[action]
		if !ok {
			http.NotFound(w, r)
			return
		}
		m.Serve(w, r)
	}
}

// userInPath is the account /api/users/{user}/... names.
func userInPath(r *http.Request) string {
	name, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/users/"), "/")
	return name
}

// oneUser is the account /api/users/{user} names, which it must.
func oneUser(c *jsonapi.Call) (string, error) {
	name := userInPath(c.R)
	if name == "" {
		return "", jsonapi.BadRequest("username required")
	}
	return name, nil
}

// audit is fn, writing what it did to an account to the activity log when it
// succeeds. says is the change, the account's name following it, or standing
// at its %s. A refused or failed request is not a change, and logging it as
// one would put something in the record that never happened.
func audit[In any](says string, fn func(c *jsonapi.Call, in In) (any, error)) func(*jsonapi.Call, In) (any, error) {
	return func(c *jsonapi.Call, in In) (any, error) {
		out, err := fn(c, in)
		if err != nil {
			return out, err
		}
		detail := says + " " + userInPath(c.R)
		if strings.Contains(says, "%s") {
			detail = fmt.Sprintf(says, userInPath(c.R))
		}
		RecordActivity(c.R, activity.Account, c.User, detail)
		return out, nil
	}
}

// handleUserOne is one account's summary, so the framework's ChipPicker (apps)
// and any other per-user component can fetch the current state without
// scanning the full list.
func (a *AdminApp) handleUserOne(c *jsonapi.Call) (any, error) {
	username, err := oneUser(c)
	if err != nil {
		return nil, err
	}
	user, ok := AuthGetUser(a.db, username)
	if !ok {
		return nil, jsonapi.NotFound("user not found")
	}
	apps := user.Apps
	if apps == nil {
		apps = []string{}
	}
	groups := user.Groups
	if groups == nil {
		groups = []string{}
	}
	return map[string]any{
		"username": user.Username,
		"admin":    user.Admin,
		"pending":  user.Pending,
		"apps":     apps,
		"groups":   groups,
	}, nil
}

// handleAddUser adds an account, with a password or by invitation.
func (a *AdminApp) handleAddUser(c *jsonapi.Call, req struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Admin    bool   `json:"admin"`
	Invite   bool   `json:"invite"` // send a registration link instead of setting a password
}) (any, error) {
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		return nil, jsonapi.BadRequest("email required")
	}
	if _, exists := AuthGetUser(a.db, req.Username); exists {
		return nil, jsonapi.Status(http.StatusConflict, "user already exists")
	}
	current := c.User

	// Invite: create the account with no password and hand back a one-time link
	// the user clicks to set their own. Emailed when mail is configured; always
	// returned so the admin can copy it manually.
	if req.Invite {
		link, err := AuthCreateInvite(a.db, req.Username, req.Admin)
		if err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		emailed := false
		if EmailConfigured() {
			body := fmt.Sprintf("You've been invited to %s. Click the link below to set your password and activate your account:\n\n%s\n", ServiceName(), link)
			if SendNotification(req.Username, "["+ServiceName()+"] You've been invited", body) == nil {
				emailed = true
			}
		}
		Log("[admin] user %q invited %q (admin=%v, emailed=%v)", current, req.Username, req.Admin, emailed)
		RecordActivity(c.R, activity.Account, current, "invited "+req.Username+asAdmin(req.Admin))
		return map[string]any{"status": "invited", "link": link, "emailed": emailed}, nil
	}

	// Direct: set a password now.
	if req.Password == "" {
		return nil, jsonapi.BadRequest("password required (or choose to send an invite link)")
	}
	AuthSetUser(a.db, req.Username, req.Password, req.Admin)
	Log("[admin] user %q created user %q (admin=%v)", current, req.Username, req.Admin)
	RecordActivity(c.R, activity.Account, current, "added the account "+req.Username+asAdmin(req.Admin))
	return map[string]any{"status": "created"}, nil
}

// handleResetPassword resets a user's password: mode "set" (admin types a new
// one) or "link" (default, and what no body means: email/return a one-time
// reset link). Owner-of-the-deployment authority (admin-gated by the router).
func (a *AdminApp) handleResetPassword(c *jsonapi.Call, req struct {
	Mode     string `json:"mode"`
	Password string `json:"password"`
}) (any, error) {
	username := userInPath(c.R)
	if _, ok := AuthGetUser(a.db, username); !ok {
		return nil, jsonapi.NotFound("user not found")
	}
	current := c.User
	if req.Mode == "set" {
		if len(req.Password) < 6 || len(req.Password) > 128 {
			return nil, jsonapi.BadRequest("Password must be 6–128 characters.")
		}
		if !AuthAdminSetPassword(a.db, username, req.Password) {
			return nil, jsonapi.Status(http.StatusInternalServerError, "could not set password")
		}
		Log("[admin] user %q set password for %q", current, username)
		return map[string]any{"status": "password_set"}, nil
	}
	// Link mode.
	link, ok := AuthIssueResetLink(a.db, username)
	if !ok {
		return nil, jsonapi.NotFound("user not found")
	}
	emailed := false
	if EmailConfigured() {
		body := fmt.Sprintf("A password reset was requested for your %s account. Click the link below to set a new password:\n\n%s\n\nIf you didn't expect this, contact an administrator.", ServiceName(), link)
		if SendNotification(username, "["+ServiceName()+"] Password reset", body) == nil {
			emailed = true
		}
	}
	Log("[admin] user %q issued reset link for %q (emailed=%v)", current, username, emailed)
	return map[string]any{"status": "link_sent", "link": link, "emailed": emailed}, nil
}

// handleUpdateUser sets an account's password, its admin flag, or both.
func (a *AdminApp) handleUpdateUser(c *jsonapi.Call, req struct {
	Password string `json:"password,omitempty"`
	Admin    *bool  `json:"admin,omitempty"`
}) (any, error) {
	username, err := oneUser(c)
	if err != nil {
		return nil, err
	}
	user, ok := AuthGetUser(a.db, username)
	if !ok {
		return nil, jsonapi.NotFound("user not found")
	}

	admin := user.Admin
	if req.Admin != nil {
		admin = *req.Admin
	}

	// Prevent removing admin from yourself.
	current := c.User
	if current == username && !admin {
		return nil, jsonapi.BadRequest("cannot remove admin from yourself")
	}

	AuthSetUser(a.db, username, req.Password, admin)
	Log("[admin] user %q updated user %q", current, username)
	return map[string]string{"status": "updated"}, nil
}

// handleApproveUser approves an account awaiting approval.
func (a *AdminApp) handleApproveUser(c *jsonapi.Call, _ struct{}) (any, error) {
	username := userInPath(c.R)
	user, ok := AuthGetUser(a.db, username)
	if !ok {
		return nil, jsonapi.NotFound("user not found")
	}
	if !user.Pending {
		return nil, jsonapi.BadRequest("user is not pending")
	}
	AuthApproveUser(a.db, username)
	Log("[admin] user %q approved %q", c.User, username)
	return map[string]string{"status": "approved"}, nil
}

// handleRejectUser rejects an account awaiting approval.
func (a *AdminApp) handleRejectUser(c *jsonapi.Call, _ struct{}) (any, error) {
	username := userInPath(c.R)
	user, ok := AuthGetUser(a.db, username)
	if !ok {
		return nil, jsonapi.NotFound("user not found")
	}
	if !user.Pending {
		return nil, jsonapi.BadRequest("user is not pending")
	}
	AuthRejectUser(a.db, username)
	Log("[admin] user %q rejected %q", c.User, username)
	return map[string]string{"status": "rejected"}, nil
}

// handleRevokeSessions signs a user out of every browser they are logged into,
// leaving the account itself alone.
//
// There was no way to do this at all: sessions were only ever ended by the
// person holding them clicking Log out, so "their laptop was stolen" had no
// answer short of deleting the account and making a new one. Sliding renewal
// makes waiting for expiry a non-answer too, since a session in use keeps
// pushing its own expiry out.
func (a *AdminApp) handleRevokeSessions(c *jsonapi.Call, _ struct{}) (any, error) {
	username := userInPath(c.R)
	if _, ok := AuthGetUser(a.db, username); !ok {
		return nil, jsonapi.NotFound("user not found")
	}
	n := AuthRevokeUserSessions(a.db, username)
	Log("[admin] user %q signed %q out of %d session(s)", c.User, username, n)
	return map[string]any{"revoked": n}, nil
}

// handleDeleteUser deletes an account, once no app holds data of theirs.
func (a *AdminApp) handleDeleteUser(c *jsonapi.Call, _ struct{}) (any, error) {
	username, err := oneUser(c)
	if err != nil {
		return nil, err
	}
	// Prevent deleting yourself.
	current := c.User
	if current == username {
		return nil, jsonapi.BadRequest("cannot delete yourself")
	}
	if _, ok := AuthGetUser(a.db, username); !ok {
		return nil, jsonapi.NotFound("user not found")
	}

	// Refuse to delete if any registered app still has data for this user
	// unless the caller confirms. The admin UI pre-runs reassign/purge
	// via /data-action before this call.
	if c.R.URL.Query().Get("force") != "1" {
		for _, h := range RegisteredUserDataHandlers() {
			sum := h.Describe(username)
			for _, n := range sum.Counts {
				if n > 0 {
					return nil, jsonapi.Status(http.StatusConflict, "user still has app data; resolve via data-action or pass ?force=1")
				}
			}
		}
	}

	AuthDeleteUser(a.db, username)
	Log("[admin] user %q deleted user %q", current, username)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleUserDataSummary returns the per-app data footprint for a user so
// the admin UI can offer reassign/purge before deletion.
func (a *AdminApp) handleUserDataSummary(c *jsonapi.Call) (any, error) {
	username := userInPath(c.R)
	handlers := RegisteredUserDataHandlers()
	out := make([]UserDataSummary, 0, len(handlers))
	for _, h := range handlers {
		out = append(out, h.Describe(username))
	}
	return out, nil
}

// handleUserDataAction runs reassign/anonymize/purge on a single app's
// data for a single user. Body: {"app":"codewriter","action":"reassign","target":"other@example.com"}.
func (a *AdminApp) handleUserDataAction(c *jsonapi.Call, req struct {
	App    string `json:"app"`
	Action string `json:"action"`
	Target string `json:"target"`
}) (any, error) {
	username := userInPath(c.R)
	if req.App == "" || req.Action == "" {
		return nil, jsonapi.BadRequest("app and action required")
	}
	var handler UserDataHandler
	for _, h := range RegisteredUserDataHandlers() {
		if h.AppName() == req.App {
			handler = h
			break
		}
	}
	if handler == nil {
		return nil, jsonapi.NotFound("unknown app")
	}
	var err error
	switch req.Action {
	case "reassign":
		if req.Target == "" {
			return nil, jsonapi.BadRequest("target required for reassign")
		}
		if _, ok := AuthGetUser(a.db, req.Target); !ok {
			return nil, jsonapi.NotFound("target user not found")
		}
		// Every app's refusal first, whichever app this call moves: moves
		// run one app at a time, and a refusal met only on a later app's
		// turn left the earlier ones moved and the rest not.
		if problems := CheckUserDataReassign(username, req.Target); len(problems) > 0 {
			return nil, jsonapi.Status(http.StatusConflict, "nothing was moved: "+strings.Join(problems, "; "))
		}
		err = handler.Reassign(username, req.Target)
	case "anonymize":
		err = handler.Anonymize(username)
	case "purge":
		err = handler.Purge(username)
	default:
		return nil, jsonapi.BadRequest("unknown action")
	}
	if err != nil {
		// The administrator running it is the one who needs to know why.
		return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
	}
	Log("[admin] user %q ran %s/%s on %q", c.User, req.App, req.Action, username)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleUpdateUserApps sets the allowed apps for a specific user.
func (a *AdminApp) handleUpdateUserApps(c *jsonapi.Call, req struct {
	Apps []string `json:"apps"`
}) (any, error) {
	username := userInPath(c.R)
	if _, ok := AuthGetUser(a.db, username); !ok {
		return nil, jsonapi.NotFound("user not found")
	}
	AuthSetUserApps(a.db, username, req.Apps)
	Log("[admin] user %q set apps for %q: %v", c.User, username, req.Apps)
	return map[string]string{"status": "updated"}, nil
}

// handleUpdateUserGroups sets the assigned app-group IDs for a specific user.
// Each group expands to its member apps at access-check time.
func (a *AdminApp) handleUpdateUserGroups(c *jsonapi.Call, req struct {
	Groups []string `json:"groups"`
}) (any, error) {
	username := userInPath(c.R)
	if _, ok := AuthGetUser(a.db, username); !ok {
		return nil, jsonapi.NotFound("user not found")
	}
	AuthSetUserGroups(a.db, username, req.Groups)
	Log("[admin] user %q set groups for %q: %v", c.User, username, req.Groups)
	return map[string]string{"status": "updated"}, nil
}

// describeLend says who a user-owned credential reaches, phrased so a read lend
// and a write lend never read the same. Empty when it reaches nobody, which is
// the ordinary case and needs no words.
func describeLend(c SecureCredential) string {
	var parts []string
	if len(c.SharedReadOnly) > 0 {
		parts = append(parts, strings.Join(c.SharedReadOnly, ", ")+" (reads)")
	}
	if len(c.SharedReadWrite) > 0 {
		parts = append(parts, strings.Join(c.SharedReadWrite, ", ")+" (writes as owner)")
	}
	return strings.Join(parts, "; ")
}

// toolRequestReview is what approving owner's pending request for tool name
// would publish, for the reviewer: whether it is an update of a published
// tool, and the text to read (a diff against the published version, or the
// whole definition for a first publish).
func toolRequestReview(db Database, releases map[string]ToolRelease, owner, name string) (update bool, review string) {
	rel, published := releases[name]
	if published && rel.Owner != owner {
		return false, fmt.Sprintf("The deployment already publishes a tool called %s (%s's), so approving this is refused until one of them is renamed.", name, rel.Owner)
	}
	probe := rel
	if !published {
		probe = ToolRelease{Owner: owner, Name: name}
	}
	want, ok := probe.Requested(db)
	if !ok {
		return published, ""
	}
	if published {
		d := rel.Tool.DefinitionDiff(want)
		if d == "" {
			d = "No change from the published version."
		}
		return true, fmt.Sprintf("Update to %s's published tool, now at version %d. Approving makes this the next version for everyone who added it.\n\n%s", owner, rel.Version, d)
	}
	return false, "A new tool for the catalog. Its whole definition:\n\n" + (TempTool{}).DefinitionDiff(want)
}

// asAdmin says, for the activity log, that an account was given admin rights.
func asAdmin(admin bool) string {
	if admin {
		return " as an administrator"
	}
	return ""
}
