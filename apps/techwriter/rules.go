// Per-user "Rules" — short instructions that get prepended to the
// system prompt on every chat call. Replaces the older per-style
// Persona feature with something the operator actually uses: a list
// of constraints like "Strip mentions of sample system" or "Never
// post API keys or passwords." Each line is one rule.
//
// Storage: single key per user. Empty string = no rules. The chat
// handler reads this lazily; UI edits it via GET/POST /api/rules.

package techwriter

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const rulesTable = "techwriter_rules"
const rulesKey   = "default"

func (T *TechWriterAgent) handleRules(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if udb == nil {
		http.Error(w, "no user database", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var rules string
		udb.Get(rulesTable, rulesKey, &rules)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"rules": rules})
	case http.MethodPost:
		var req struct {
			Rules string `json:"rules"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		udb.Set(rulesTable, rulesKey, strings.TrimSpace(req.Rules))
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// loadUserRules returns the user's saved rules, formatted as a system
// prompt section. Returns empty string when no rules are set.
func loadUserRules(udb Database) string {
	if udb == nil {
		return ""
	}
	var rules string
	udb.Get(rulesTable, rulesKey, &rules)
	rules = strings.TrimSpace(rules)
	if rules == "" {
		return ""
	}
	return "\n\nUSER RULES (must be followed in every response):\n" + rules
}
