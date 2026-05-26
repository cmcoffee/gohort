package servitor

import (
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
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
