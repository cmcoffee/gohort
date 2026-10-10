package servitor

// Per-user command permissions for the web surface: which risk CATEGORIES
// (see classify_command) the operator has chosen to run WITHOUT a confirmation
// prompt — the web analog of the CLI's --allow flag. Stored per-user (the udb
// is already user-scoped), same as the per-command always-allow list. A
// category left unchecked still prompts before every matching command.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
)

const (
	allowCategoriesTable = "ssh_allow_categories"
	allowCategoriesKey   = "current"
)

// loadAllowedCategories returns the set of categories the user auto-runs.
// Missing / unreadable record → empty set (everything prompts).
func loadAllowedCategories(udb Database) map[RiskCategory]bool {
	out := map[RiskCategory]bool{}
	if udb == nil {
		return out
	}
	var raw map[string]bool
	if udb.Get(allowCategoriesTable, allowCategoriesKey, &raw) {
		for _, c := range AllRiskCategories {
			if raw[string(c)] {
				out[c] = true
			}
		}
	}
	return out
}

// saveAllowedCategories persists the auto-run set, keeping only recognized
// category names so a stale/typo'd key can't linger in the record.
func saveAllowedCategories(udb Database, in map[string]bool) {
	if udb == nil {
		return
	}
	clean := map[string]bool{}
	for _, c := range AllRiskCategories {
		if in[string(c)] {
			clean[string(c)] = true
		}
	}
	udb.Set(allowCategoriesTable, allowCategoriesKey, clean)
}

// handlePermissions is GET (read the auto-run set) / POST (replace it). The
// response/request body is a flat {category: bool} map over every risk
// category, so the browser toggle UI round-trips it directly.
func (T *Servitor) handlePermissions(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		cats := loadAllowedCategories(udb)
		out := map[string]bool{}
		for _, c := range AllRiskCategories {
			out[string(c)] = cats[c]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	case http.MethodPost:
		var body map[string]bool
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		saveAllowedCategories(udb, body)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// alwaysAllowed is one command an operator answered Always for, as the
// Permissions dialog lists it.
type alwaysAllowed struct {
	ApplianceID string `json:"appliance_id"`
	Appliance   string `json:"appliance"` // its name, or the id when it is gone
	Command     string `json:"command"`
}

// listAlwaysAllowed is every Always answer in the user's store, by appliance
// and then command. Keys without an appliance are from before an answer named
// one; the gate no longer reads them, so they are not listed as if they did.
func (T *Servitor) listAlwaysAllowed(user string, udb Database) []alwaysAllowed {
	out := []alwaysAllowed{}
	names := map[string]string{}
	for _, k := range udb.Keys(alwaysAllowTable) {
		appliance, cmd, ok := strings.Cut(k, "\x00")
		if !ok || appliance == "" {
			continue
		}
		var on bool
		if !udb.Get(alwaysAllowTable, k, &on) || !on {
			continue
		}
		name, seen := names[appliance]
		if !seen {
			name = appliance
			if a, _, _, found := T.resolveAppliance(user, udb, appliance); found && strings.TrimSpace(a.Name) != "" {
				name = a.Name
			}
			names[appliance] = name
		}
		out = append(out, alwaysAllowed{ApplianceID: appliance, Appliance: name, Command: cmd})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Appliance != out[j].Appliance {
			return out[i].Appliance < out[j].Appliance
		}
		return out[i].Command < out[j].Command
	})
	return out
}

// handleAlwaysAllowed is GET (the user's Always answers) / POST {appliance_id,
// command} (remove one, so that command asks again on that appliance).
func (T *Servitor) handleAlwaysAllowed(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if udb == nil {
		http.Error(w, "no store for this user", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"commands": T.listAlwaysAllowed(user, udb)})
	case http.MethodPost:
		var body struct {
			ApplianceID string `json:"appliance_id"`
			Command     string `json:"command"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.ApplianceID == "" || body.Command == "" {
			http.Error(w, "appliance_id and command are required", http.StatusBadRequest)
			return
		}
		udb.Unset(alwaysAllowTable, alwaysAllowKey(body.ApplianceID, body.Command))
		Log("[servitor] %s removed always-allow on %s for: %s", user, body.ApplianceID, body.Command)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
