package servitor

import (
	"strings"
	"time"

	"encoding/json"
	. "github.com/cmcoffee/gohort/core"
	"net/http"
)

const rulesTable = "ssh_rules"

// ApplianceRule is a standing instruction the user has established for a specific appliance.
type ApplianceRule struct {
	ID          string `json:"id"`
	ApplianceID string `json:"appliance_id"`
	Rule        string `json:"rule"`
	Created     string `json:"created"` // RFC3339
}

// storeRule writes a new rule for an appliance.
func storeRule(udb Database, applianceID, rule string) string {
	if udb == nil || applianceID == "" || strings.TrimSpace(rule) == "" {
		return ""
	}
	id := applianceID + ":" + UUIDv4()
	udb.Set(rulesTable, id, ApplianceRule{
		ID:          id,
		ApplianceID: applianceID,
		Rule:        strings.TrimSpace(rule),
		Created:     time.Now().Format(time.RFC3339),
	})
	return id
}

// rulesForAppliance returns all stored rules for one appliance, oldest first.
func rulesForAppliance(udb Database, applianceID string) []ApplianceRule {
	if udb == nil {
		return nil
	}
	prefix := applianceID + ":"
	var out []ApplianceRule
	for _, k := range udb.Keys(rulesTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var r ApplianceRule
		if udb.Get(rulesTable, k, &r) {
			out = append(out, r)
		}
	}
	// Sort by Created ascending.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Created < out[j-1].Created; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// deleteRule removes a rule by its full DB key.
func deleteRule(udb Database, id string) {
	if udb == nil {
		return
	}
	udb.Unset(rulesTable, id)
}

// formatRules renders the rule list as a numbered block for prompt injection.
func formatRules(rules []ApplianceRule) string {
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range rules {
		b.WriteString(strings.TrimSpace(r.Rule))
		if i < len(rules)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// handleRules handles GET (list) and POST (create) for appliance rules.
func (T *Servitor) handleRules(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	applianceID := r.URL.Query().Get("appliance_id")
	switch r.Method {
	case http.MethodGet:
		if applianceID == "" {
			http.Error(w, "appliance_id required", http.StatusBadRequest)
			return
		}
		// Rules are the owner's directives — read from the owner's store so a
		// non-owner viewing a shared appliance sees the same rules (read-only).
		src := udb
		if _, _, ownerUDB, found := T.resolveAppliance(userID, udb, applianceID); found {
			src = ownerUDB
		}
		rules := rulesForAppliance(src, applianceID)
		if rules == nil {
			rules = []ApplianceRule{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rules)
	case http.MethodPost:
		var req struct {
			ApplianceID string `json:"appliance_id"`
			Rule        string `json:"rule"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ApplianceID == "" || strings.TrimSpace(req.Rule) == "" {
			http.Error(w, "appliance_id and rule required", http.StatusBadRequest)
			return
		}
		a, _, ownerUDB, found := T.resolveAppliance(userID, udb, req.ApplianceID)
		if !found {
			http.Error(w, "appliance not found", http.StatusNotFound)
			return
		}
		// Only the owner or an admin sets rules — a shared appliance's rules are
		// the owner's, applied to everyone; a non-owner can't change them.
		if !canManageAppliance(userID, a, servitorIsAdmin(r)) {
			http.Error(w, "only the owner or an admin can set rules on a shared appliance", http.StatusForbidden)
			return
		}
		id := storeRule(ownerUDB, req.ApplianceID, req.Rule)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRuleDelete handles DELETE /api/rules/<id>.
func (T *Servitor) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/rules/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	// The rule id is "<applianceID>:<uuid>", so resolve the owning appliance
	// from it — a shared appliance's rules live in the owner's store, and only
	// the owner or an admin may delete them.
	applianceID := id
	if i := strings.Index(id, ":"); i >= 0 {
		applianceID = id[:i]
	}
	a, _, ownerUDB, found := T.resolveAppliance(userID, udb, applianceID)
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !canManageAppliance(userID, a, servitorIsAdmin(r)) {
		http.Error(w, "only the owner or an admin can delete rules on a shared appliance", http.StatusForbidden)
		return
	}
	deleteRule(ownerUDB, id)
	w.WriteHeader(http.StatusNoContent)
}
