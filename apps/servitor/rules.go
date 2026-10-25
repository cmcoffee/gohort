package servitor

import (
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
func (T *Servitor) handleRules(c *jsonapi.Call) (any, error) {
	applianceID := c.R.URL.Query().Get("appliance_id")
	if applianceID == "" {
		return nil, jsonapi.BadRequest("appliance_id required")
	}
	// Rules are the owner's directives — read from the owner's store so a
	// non-owner viewing a shared appliance sees the same rules (read-only).
	src := c.UDB
	if _, _, ownerUDB, found := T.resolveAppliance(c.User, c.UDB, applianceID); found {
		src = ownerUDB
	}
	rules := rulesForAppliance(src, applianceID)
	if rules == nil {
		rules = []ApplianceRule{}
	}
	return rules, nil
}

// handleRuleAdd sets a rule on a system.
func (T *Servitor) handleRuleAdd(c *jsonapi.Call, req ruleReq) (any, error) {
	if req.ApplianceID == "" || strings.TrimSpace(req.Rule) == "" {
		return nil, jsonapi.BadRequest("appliance_id and rule required")
	}
	a, _, ownerUDB, found := T.resolveAppliance(c.User, c.UDB, req.ApplianceID)
	if !found {
		return nil, jsonapi.NotFound("system not found")
	}
	// Only the owner or an admin sets rules — a shared appliance's rules are
	// the owner's, applied to everyone; a non-owner can't change them.
	if !canManageAppliance(c.User, a, servitorIsAdmin(c.R)) {
		return nil, jsonapi.Forbidden("only the owner or an admin can set rules on a shared system")
	}
	id := storeRule(ownerUDB, req.ApplianceID, req.Rule)
	return map[string]string{"id": id}, nil
}

// ruleReq is what handleRuleAdd is posted.
type ruleReq struct {
	ApplianceID string `json:"appliance_id"`
	Rule        string `json:"rule"`
}

// handleRuleDelete handles DELETE /api/rules/<id>.
func (T *Servitor) handleRuleDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	id := strings.TrimPrefix(c.R.URL.Path, "/api/rules/")
	if id == "" {
		return nil, jsonapi.BadRequest("id required")
	}
	// The rule id is "<applianceID>:<uuid>", so resolve the owning appliance
	// from it — a shared appliance's rules live in the owner's store, and only
	// the owner or an admin may delete them.
	applianceID := id
	if i := strings.Index(id, ":"); i >= 0 {
		applianceID = id[:i]
	}
	a, _, ownerUDB, found := T.resolveAppliance(c.User, c.UDB, applianceID)
	if !found {
		return nil, jsonapi.NotFound("not found")
	}
	if !canManageAppliance(c.User, a, servitorIsAdmin(c.R)) {
		return nil, jsonapi.Forbidden("only the owner or an admin can delete rules on a shared system")
	}
	deleteRule(ownerUDB, id)
	return jsonapi.Empty(http.StatusNoContent), nil
}
