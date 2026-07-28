// Per-user "Rules" for a writer app: short standing instructions that
// ride on every assistant call. A list of constraints like "Never post
// API keys or passwords" or "Match the existing tone — terse, factual."
// Each line is one rule.
//
// NAMESPACED PER APP rather than shared across all of them. The rules
// that make sense for prose ("no hedging, no em-dashes") are not the
// rules that make sense for SQL ("always schema-qualify table names")
// or for prompt blocks ("never mention tools the framework appends").
// One pooled list would force every app to read constraints written for
// a different medium, which is worse than having no rules at all.
//
// Storage: one key per user per namespace. Empty string = no rules.

package core

import (
	"encoding/json"
	"net/http"
	"strings"
)

const docRulesKey = "default"

// DocRulesTable returns the per-user table name backing one app's rules.
// Namespace is the app's own name ("techwriter", "codewriter", …).
func DocRulesTable(namespace string) string {
	return strings.TrimSpace(namespace) + "_rules"
}

// LoadDocRules returns the user's saved rules for a namespace, verbatim
// and untrimmed of meaning — empty when nothing is set.
func LoadDocRules(udb Database, namespace string) string {
	if udb == nil {
		return ""
	}
	var rules string
	udb.Get(DocRulesTable(namespace), docRulesKey, &rules)
	return strings.TrimSpace(rules)
}

// FormatDocRules renders saved rules as a system-prompt section. Returns
// "" when there are none, so callers can concatenate unconditionally.
func FormatDocRules(rules string) string {
	rules = strings.TrimSpace(rules)
	if rules == "" {
		return ""
	}
	return "\n\nUSER RULES (must be followed in every response):\n" + rules
}

// DocRulesSection is the common case: load a namespace's rules and
// format them for a system prompt in one call.
func DocRulesSection(udb Database, namespace string) string {
	return FormatDocRules(LoadDocRules(udb, namespace))
}

// HandleDocRules serves the rules editor's GET/POST for one namespace.
// Apps mount it directly:
//
//	sub.HandleFunc("/api/rules", func(w http.ResponseWriter, r *http.Request) {
//	    HandleDocRules(w, r, T.DB, "techwriter")
//	})
func HandleDocRules(w http.ResponseWriter, r *http.Request, db Database, namespace string) {
	_, udb, ok := RequireUser(w, r, db)
	if !ok {
		return
	}
	if udb == nil {
		http.Error(w, "no user database", http.StatusInternalServerError)
		return
	}
	table := DocRulesTable(namespace)
	switch r.Method {
	case http.MethodGet:
		var rules string
		udb.Get(table, docRulesKey, &rules)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"rules": rules})
	case http.MethodPost:
		var req struct {
			Rules string `json:"rules"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		udb.Set(table, docRulesKey, strings.TrimSpace(req.Rules))
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
