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
	"slices"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const fleetDefaultsTable = "fleet_defaults"

// The settings that can carry a fleet default. Named, not open: a key nobody
// reads is a control that appears to work.
const (
	defaultWorkspaceNetwork = "workspace_network"
	defaultShareCortex      = "share_cortex"
	defaultShareReference   = "share_reference"
	defaultShareNotes       = "share_notes"
	defaultShareUploads     = "share_uploads"
	defaultInboundMode      = "inbound_mode"
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
	return settingIsOn(db, owner, rec, defaultWorkspaceNetwork)
}

// workspaceNetworkSource says WHERE that answer came from, for a page that has
// to show an override as an override rather than as a value.
func workspaceNetworkSource(db Database, owner string, rec AgentRecord) string {
	return settingSource(db, owner, rec, defaultWorkspaceNetwork)
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
		out := map[string]any{}
		for setting := range triSettings {
			out[setting] = fleetDefault(RootDB, user, setting)
		}
		writeJSON(w, out)
	case http.MethodPatch, http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		for setting := range triSettings {
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
			if v != "" && !slices.Contains(triSettings[setting].values, v) {
				http.Error(w, setting+" does not take "+v, http.StatusBadRequest)
				return
			}
			setFleetDefault(RootDB, user, setting, v)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// triSetting describes one setting that can carry a fleet default.
//
// A table rather than a resolver each, because the shape is identical every
// time: the agent's own answer, a record written before the tri-state, the
// owner's default, the framework's. Written out four times it drifts in the
// order or in what an empty value means, and the difference between those is
// whether a fleet blocks something or opens it.
type triSetting struct {
	key string // the fleet-default key, and the record's json field
	// own reads the agent's own tri-state answer: "on", "off" or "".
	own func(AgentRecord) string
	// legacy reads a record written before this setting was tri-state, and
	// reports whether it said anything. Only a value the old field could
	// actually record counts: most were bools that could express one side.
	legacy func(AgentRecord) (string, bool)
	// framework is the answer when nobody has given one. It is what the
	// deployment did before any of this existed and must stay that way for
	// one that sets nothing.
	framework string
	// values are what may be stored, empty aside. Declared per setting
	// because they are not all on and off: inbound reach takes its own modes,
	// and a shared on/off check would refuse them while looking correct.
	values []string
}

// onOff is the common case, named once so a setting that takes it says so
// rather than repeating a literal that could drift.
func onOff() []string { return []string{settingOn, settingOff} }

var triSettings = map[string]triSetting{
	defaultWorkspaceNetwork: {
		key: defaultWorkspaceNetwork,
		own: func(a AgentRecord) string { return a.WorkspaceNetwork },
		// The old field could only ever record a BLOCK.
		legacy:    func(a AgentRecord) (string, bool) { return settingOff, a.WorkspaceNoNetwork },
		framework: settingOn,
		values:    onOff(),
	},
	defaultShareCortex: {
		key:       defaultShareCortex,
		own:       func(a AgentRecord) string { return a.ShareCortex },
		legacy:    func(a AgentRecord) (string, bool) { return settingOff, a.ShareHoldCortex },
		framework: settingOn,
		values:    onOff(),
	},
	defaultShareReference: {
		key:       defaultShareReference,
		own:       func(a AgentRecord) string { return a.ShareReference },
		legacy:    func(a AgentRecord) (string, bool) { return settingOff, a.ShareHoldReference },
		framework: settingOn,
		values:    onOff(),
	},
	defaultShareNotes: {
		key: defaultShareNotes,
		own: func(a AgentRecord) string { return a.ShareNotes },
		// The one legacy flag stored POSITIVELY: it granted rather than
		// withheld, so a true means on and its framework answer is off.
		legacy:    func(a AgentRecord) (string, bool) { return settingOn, a.ShareMemoryExplicit },
		framework: settingOff,
		values:    onOff(),
	},
	defaultShareUploads: {
		key:       defaultShareUploads,
		own:       func(a AgentRecord) string { return a.ShareUploads },
		legacy:    func(a AgentRecord) (string, bool) { return settingOff, a.ShareNoUploads },
		framework: settingOn,
		values:    onOff(),
	},
	defaultInboundMode: {
		key: defaultInboundMode,
		// Not on/off: its values are the inbound modes, and "" already meant
		// "any agent". It carries a default the same way regardless.
		own:       func(a AgentRecord) string { return a.InboundMode },
		legacy:    func(AgentRecord) (string, bool) { return "", false },
		framework: inboundAny,
		values:    []string{inboundOnly, inboundNone},
	},
}

// resolveSetting answers one setting for one agent, in the order the answers
// override each other.
func resolveSetting(db Database, owner string, rec AgentRecord, key string) string {
	s, ok := triSettings[key]
	if !ok {
		return ""
	}
	if v := strings.TrimSpace(s.own(rec)); v != "" {
		return v
	}
	if v, said := s.legacy(rec); said {
		return v
	}
	if v := fleetDefault(db, owner, s.key); v != "" {
		return v
	}
	return s.framework
}

// settingIsOn is the bool form, for a setting whose values are on and off.
func settingIsOn(db Database, owner string, rec AgentRecord, key string) bool {
	return resolveSetting(db, owner, rec, key) == settingOn
}

// settingSource says WHERE an answer came from, for a page that has to show an
// override as an override rather than as a value.
func settingSource(db Database, owner string, rec AgentRecord, key string) string {
	s, ok := triSettings[key]
	if !ok {
		return ""
	}
	if v := strings.TrimSpace(s.own(rec)); v != "" {
		return "set on this agent: " + v
	}
	if v, said := s.legacy(rec); said {
		return "set on this agent: " + v
	}
	if v := fleetDefault(db, owner, s.key); v != "" {
		return "from the default for all agents: " + v
	}
	return "not set anywhere, so: " + s.framework
}
