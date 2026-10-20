package orchestrate

// Publishing an agent is the administrator's decision.
//
// An exposed agent reaches EVERY signed-in user of the deployment, through
// /agents/<slug> and, with mcp_exposed, through an external MCP client. That is
// the same reach a shared custom app has, and a shared app has needed an admin
// to approve it since v0.6.710. An agent is arguably the larger grant: it
// carries its owner's tools, its owner's credentials and its owner's memory,
// and every opener runs it as the owner.
//
// So it follows the same route as the other two publishable primitives. Owning
// something and sharing it with a named person stays entirely the owner's
// business; widening it to everybody is a request, and an administrator
// approves it from the Pending promotions queue. The kind is "agent", which the
// admin governance filter has listed all along with nothing able to file it.
//
// Turning it OFF stays owner-direct, exactly as revoking a share does: nobody
// needs permission to stop publishing.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
)

// agentPromotionKind is the promotion kind for publishing an agent. It covers
// BOTH exposure flags: the question an administrator is answering is "may this
// person's agent be reachable by everyone", and which door it is reachable
// through is a detail of the same decision.
const agentPromotionKind = "agent"

// requestIsAdminAgent decides who may publish an agent directly. A var so a
// test can stand in a non-admin without building a whole auth session, matching
// the seam customapps already uses.
var requestIsAdminAgent = RequestIsAdmin

// registerAgentPromotion wires the approver. Approving flips the flag the
// request was filed for, on the owner's own record.
func registerAgentPromotion(app *OrchestrateApp) {
	promotion.RegisterApprover(agentPromotionKind, func(owner, name string) error {
		return app.approveAgentPublish(owner, name)
	})
}

// agentPromotionName encodes which door the request is for, so one kind covers
// both without an administrator having to read two nearly identical rows and
// work out the difference.
//
// The agent id leads so the name sorts and reads as the agent it is about.
func agentPromotionName(agentID, flag string) string { return agentID + ":" + flag }

func parseAgentPromotionName(name string) (agentID, flag string) {
	id, f, ok := strings.Cut(strings.TrimSpace(name), ":")
	if !ok {
		// A request filed before the flag was part of the name means the web
		// surface, which was the only one that existed.
		return strings.TrimSpace(name), "exposed"
	}
	return id, f
}

// approveAgentPublish is what an administrator's Approve actually does.
func (T *OrchestrateApp) approveAgentPublish(owner, name string) error {
	agentID, flag := parseAgentPromotionName(name)
	udb := UserDB(T.DB, owner)
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		return Error("no agent " + agentID + " owned by " + owner)
	}
	switch flag {
	case "mcp_exposed":
		rec.MCPExposed = true
	default:
		if err := T.publishedSlugClash(rec); err != nil {
			return err
		}
		// The REACH. A card is presentation and needs nobody's approval, so
		// approving a request must not quietly add one: an owner who asked
		// "may everybody use this" did not ask for it on the dashboard.
		rec.Everyone = true
	}
	_, err := saveAgent(udb, rec)
	return err
}

// publishedSlugClash refuses to publish an agent under a name another
// PUBLISHED agent already answers to at /agents/<slug>.
//
// Refusing rather than letting the runtime suffix it (assignExposedSlugs)
// because this is the one moment somebody is deciding to widen reach, and the
// admin approving it is the right person to ask for a rename. Published means
// every granted user sees both cards, told apart only by a hex fragment in the
// URL, which is a poor thing to approve on purpose. An agent that is only
// peer-shared is not a clash: its reach is a few named people, and the suffix
// keeps them apart without asking anybody.
//
// The error leaves the request pending (promotion.Approve runs this before it
// marks the row), so approving again after the owner sets a Public name works.
func (T *OrchestrateApp) publishedSlugClash(rec AgentRecord) error {
	slug := ExposedSlug(rec)
	if slug == "" {
		return nil
	}
	for _, e := range T.exposedPool() {
		if e.AgentID == rec.ID || !e.Everyone || e.BaseSlug != slug {
			continue
		}
		return Error("another published agent (" + e.Name + ", owned by " + e.Owner + ") already uses /agents/" + slug +
			"; set a different public name on this one, then approve again")
	}
	return nil
}

// agentPublishNeedsApproval reports whether flipping this flag ON has to be
// filed rather than applied, and files it.
//
// Only ON, and only for a non-admin. Turning exposure off is always the
// owner's, because nobody needs permission to stop publishing, and an admin
// owner acts directly for the same reason they do on an app.
//
// Returns true when the caller must NOT apply the change.
func (T *OrchestrateApp) agentPublishNeedsApproval(r *http.Request, user, agentID, flag string, on bool, already bool) bool {
	if !on || already || requestIsAdminAgent(r) {
		return false
	}
	if err := CreatePromotionRequest(AuthDB(), user, agentPromotionKind, agentPromotionName(agentID, flag), ""); err != nil {
		Log("[orchestrate.publish] %s could not file a publish request for %s/%s: %v", user, agentID, flag, err)
	}
	return true
}

// handleAgentShare is the agent's share list: GET and POST
// /api/console/permissions/share?agent=<id>, {"allowed_users": [...]}.
//
// One list, as every other kind has: people, a team, or Everyone. Everyone is
// publishing, so it goes through the shared rule (promotion.AskEveryone):
// filed for an administrator unless one is asking, taken back by the owner at
// once. The names beside it are kept, and add to Everyone rather than narrow
// it: somebody named reaches the agent whether or not it is published.
func (T *OrchestrateApp) handleAgentShare(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rec, found := findAgentByNameOrID(udb, user, strings.TrimSpace(r.URL.Query().Get("agent")))
	if !found || !isShareableAgent(rec, user) {
		http.NotFound(w, r)
		return
	}
	ask := promotion.EveryoneAsk{
		Kind: agentPromotionKind, Owner: user, ID: agentPromotionName(rec.ID, "exposed"),
		Note:  "Agent " + strconv.Quote(chFirst(rec.Name, rec.ID)) + ": everyone can run it",
		Admin: requestIsAdminAgent(r),
	}
	if rec.Everyone {
		ask.Has = promotion.EveryoneOn
	}
	var db promotion.Store
	if AuthDB != nil {
		db = AuthDB()
	}
	list := func(a AgentRecord) map[string]any {
		return map[string]any{"allowed_users": withEveryone(nonNilUsers(a.AllowedUsers),
			a.Everyone || promotion.EveryonePending(db, ask) != "")}
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, list(rec))
	case http.MethodPost, http.MethodPatch:
		var body struct {
			AllowedUsers []string `json:"allowed_users"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		names, want := splitEveryone(body.AllowedUsers)
		if want {
			ask.Want = promotion.EveryoneOn
		}
		level, err := promotion.AskEveryone(db, ask)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if on := level != ""; on && !rec.Everyone {
			if err := T.publishedSlugClash(rec); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			rec.Everyone = true
		} else if !on {
			rec.Everyone = false
		}
		rec.AllowedUsers = normalizeShareList(names, user)
		saved, err := saveAgent(udb, rec)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, list(saved))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
