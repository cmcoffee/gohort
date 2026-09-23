// Values that hold for every agent until one says otherwise.
//
// A setting stored as a bool on an agent cannot express this. "Off because the
// default is off" and "off because I set it" are the same false, and the
// difference only appears later, when the default changes: one agent should
// follow and the other should not, and nothing recorded which was which.
//
// So a setting that can be defaulted is stored as a STRING, empty meaning "not
// decided here". The same reason gob cannot carry a *bool in this codebase: a
// pointer that must distinguish unset from false does not survive the round
// trip, and a string does.

package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const fleetDefaultsTable = "fleet_defaults"

// The settings that can carry a fleet default. Named, not open: a key nobody
// reads is a control that appears to work.
const (
	defaultWorkspaceNetwork = "workspace_network"
)

// Tri-state values. Empty is the third and is never written: it is what a
// record holds when nobody has decided.
const (
	settingOn  = "on"
	settingOff = "off"
)

func fleetDefaultKey(owner, setting string) string { return owner + "\x00" + setting }

// fleetDefault reads what holds for every agent of this owner, or "" when
// nothing does.
func fleetDefault(db Database, owner, setting string) string {
	if db == nil || strings.TrimSpace(owner) == "" {
		return ""
	}
	var v string
	db.Get(fleetDefaultsTable, fleetDefaultKey(owner, setting), &v)
	return strings.TrimSpace(v)
}

// setFleetDefault records it, or clears it when value is empty.
//
// Clearing is not the same as setting it off: cleared, every agent that had
// not decided goes back to the framework's own answer, which for workspace
// reach is allowed. That is a widening, and it is the owner's to make, but it
// is not what "off" means and the two must not be spelled the same way.
func setFleetDefault(db Database, owner, setting, value string) {
	if db == nil || strings.TrimSpace(owner) == "" {
		return
	}
	key := fleetDefaultKey(owner, setting)
	if value = strings.TrimSpace(value); value == "" {
		db.Unset(fleetDefaultsTable, key)
		return
	}
	db.Set(fleetDefaultsTable, key, value)
}

// agentWorkspaceNetwork resolves whether code in this agent's workspace may
// open a connection, in the order the answers override each other.
//
// The agent's own answer, then the legacy bool for a record written before
// this existed, then the owner's default, then the framework's: allowed. The
// framework's answer is last and is OPEN, which is what every deployment did
// before any of this and must stay true for one that sets nothing.
func agentWorkspaceNetwork(db Database, owner string, rec AgentRecord) bool {
	switch strings.TrimSpace(rec.WorkspaceNetwork) {
	case settingOn:
		return true
	case settingOff:
		return false
	}
	// An override made before the tri-state existed. Only ever true for a
	// block, since the old field could not record an explicit allow.
	if rec.WorkspaceNoNetwork {
		return false
	}
	if fleetDefault(db, owner, defaultWorkspaceNetwork) == settingOff {
		return false
	}
	return true
}

// workspaceNetworkSource says WHERE that answer came from, for a page that has
// to show an override as an override rather than as a value.
func workspaceNetworkSource(db Database, owner string, rec AgentRecord) string {
	switch strings.TrimSpace(rec.WorkspaceNetwork) {
	case settingOn:
		return "set on this agent: allowed"
	case settingOff:
		return "set on this agent: blocked"
	}
	if rec.WorkspaceNoNetwork {
		return "set on this agent: blocked"
	}
	if fleetDefault(db, owner, defaultWorkspaceNetwork) == settingOff {
		return "from the default for all agents: blocked"
	}
	return "from the default for all agents: allowed"
}

// agentDefaultsOwner is whose defaults an agent reads: its owner, falling back
// to the running user for a record that carries none (a seed, or one written
// before ownership was stamped).
//
// Not the runtime user. A shared agent runs for somebody else, and what its
// workspace may reach is a decision its OWNER made about their own agent, not
// a setting the visitor brings with them.
func agentDefaultsOwner(rec AgentRecord, fallback string) string {
	if o := strings.TrimSpace(rec.Owner); o != "" && o != seedOwner {
		return o
	}
	return strings.TrimSpace(fallback)
}

// handleFleetDefaults reads and writes the values that hold for every agent.
//
// GET answers with the settings as a flat object, which is what a FormPanel
// loads. PATCH takes the fields that changed, which is what per-field
// auto-save sends.
//
// Only NAMED settings are accepted. An open key-value store here would be a
// page where a typo silently creates a default nothing reads, which looks
// exactly like one that works.
func (T *OrchestrateApp) handleFleetDefaults(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{
			defaultWorkspaceNetwork: fleetDefault(RootDB, user, defaultWorkspaceNetwork),
		})
	case http.MethodPatch, http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		for _, setting := range []string{defaultWorkspaceNetwork} {
			raw, present := body[setting]
			if !present {
				continue
			}
			v := strings.TrimSpace(fmt.Sprint(raw))
			if raw == nil {
				v = ""
			}
			// Empty clears, which is NOT the same as off: cleared, an agent
			// that decided nothing goes back to the framework's answer.
			if v != "" && v != settingOn && v != settingOff {
				http.Error(w, setting+" must be on, off, or empty", http.StatusBadRequest)
				return
			}
			setFleetDefault(RootDB, user, setting, v)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
