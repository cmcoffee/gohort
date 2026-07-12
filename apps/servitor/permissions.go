package servitor

// Per-user command permissions for the web surface: which risk CATEGORIES
// (see classify_command) the operator has chosen to run WITHOUT a confirmation
// prompt — the web analog of the CLI's --allow flag. Stored per-user (the udb
// is already user-scoped), same as the per-command always-allow list. A
// category left unchecked still prompts before every matching command.

import (
	"encoding/json"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
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
// response/request body is a flat {category: bool} map over the four
// categories, so the browser toggle UI round-trips it directly.
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
