// Standing answers to a confirmation: what the user allowed, and how narrowly.
//
// A grant is created by one click on an approval card ("Always allow …") and
// consulted before every later escalation. It lives in the granting user's own
// store, so one person's standing yes is never another's.
//
// The shape is deliberately small. A grant names a scope, a tool, and — where
// the tool has a varying argument — a prefix of it. There is no wildcard
// syntax, no allow-everything entry, and no way to write one by hand from the
// browser: the only thing that mints a grant is a user answering a card that
// showed them the actual call. That is what keeps "what did I agree to" a
// question with a readable answer.
//
// See core.ToolConfirmation and core.CommandIsGrantable for the matching rules
// and, in particular, for why a chained shell command can never match one.
package orchestrate

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// toolGrantTable holds ToolGrant records in the granting user's own store.
const toolGrantTable = "tool_grants"

// ToolGrant is one standing approval.
type ToolGrant struct {
	ID string `json:"id"`
	// Scope is the app-supplied namespace the grant applies within (a
	// project id, a workspace). A grant never applies outside it.
	Scope string `json:"scope"`
	// Tool is the tool name the grant covers.
	Tool string `json:"tool"`
	// Prefix, when set, narrows the grant to calls whose granted argument
	// starts with it. Empty covers the tool outright within the scope.
	Prefix string `json:"prefix,omitempty"`
	// Label is what the card said the user was allowing, kept verbatim so
	// the manager screen can show them the sentence they agreed to rather
	// than a reconstruction of it.
	Label string    `json:"label,omitempty"`
	At    time.Time `json:"at"`
}

// covers reports whether this grant answers a call.
func (g ToolGrant) covers(scope, tool, argValue string) bool {
	if g.Scope != scope || g.Tool != tool {
		return false
	}
	if g.Prefix == "" {
		return true
	}
	return CommandMatchesPrefix(argValue, g.Prefix)
}

// listToolGrants returns a user's grants, newest first. A blank scope lists
// every scope, for the manager screen.
func listToolGrants(udb Database, scope string) []ToolGrant {
	out := []ToolGrant{}
	if udb == nil {
		return out
	}
	for _, id := range udb.Keys(toolGrantTable) {
		var g ToolGrant
		if !udb.Get(toolGrantTable, id, &g) {
			continue
		}
		if scope != "" && g.Scope != scope {
			continue
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// findToolGrant returns the grant covering this call, if the user has one.
func findToolGrant(udb Database, scope, tool, argValue string) (ToolGrant, bool) {
	for _, g := range listToolGrants(udb, scope) {
		if g.covers(scope, tool, argValue) {
			return g, true
		}
	}
	return ToolGrant{}, false
}

// saveToolGrant records a standing approval, replacing any existing grant that
// would already have covered the same ground.
//
// Replacing rather than appending keeps the list readable: clicking "always"
// twice on the same command should leave one entry saying what you allowed,
// not two saying it twice. A BROADER new grant also absorbs the narrower ones
// it makes redundant, so the manager screen never shows an entry that can
// never be the reason a call went through.
func saveToolGrant(udb Database, g ToolGrant) ToolGrant {
	if udb == nil {
		return g
	}
	g.ID = UUIDv4()
	g.At = time.Now().UTC()
	for _, old := range listToolGrants(udb, g.Scope) {
		if old.Tool != g.Tool {
			continue
		}
		// The new grant makes the old one redundant when it covers at least
		// as much: a tool-wide grant absorbs every prefix, and a shorter
		// prefix absorbs the longer ones under it.
		if g.Prefix == "" || (old.Prefix != "" && CommandMatchesPrefix(old.Prefix, g.Prefix)) {
			udb.Unset(toolGrantTable, old.ID)
		}
	}
	udb.Set(toolGrantTable, g.ID, g)
	return g
}

// deleteToolGrant revokes one grant.
func deleteToolGrant(udb Database, id string) bool {
	if udb == nil {
		return false
	}
	id = strings.TrimSpace(id)
	var g ToolGrant
	if !udb.Get(toolGrantTable, id, &g) {
		return false
	}
	udb.Unset(toolGrantTable, id)
	return true
}

// --- the manager surface -----------------------------------------------------

// PublicHandleGrants serves a host app's Permissions screen: GET lists the
// caller's standing approvals, DELETE revokes one.
//
// A grant is only ever created by answering a card, but it has to be
// REVOCABLE somewhere that isn't mid-conversation — otherwise "always allow"
// is a decision you can make in one click and never take back, which is not a
// bargain anyone should be offered.
//
// scope narrows the listing to one app-supplied namespace; the app passes
// whatever it scoped its grants under, so one project's Permissions screen
// never shows (or revokes) another's.
func (T *OrchestrateApp) PublicHandleGrants(w http.ResponseWriter, r *http.Request, scope, grantID string) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	_ = user
	switch r.Method {
	case http.MethodGet:
		writeGrantJSON(w, listToolGrants(udb, scope))
	case http.MethodDelete:
		// Revoking is scoped the same way listing is: an id the caller
		// cannot see in this scope is one they cannot delete through it.
		for _, g := range listToolGrants(udb, scope) {
			if g.ID == grantID {
				deleteToolGrant(udb, grantID)
				writeGrantJSON(w, map[string]any{"ok": true})
				return
			}
		}
		http.NotFound(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeGrantJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
