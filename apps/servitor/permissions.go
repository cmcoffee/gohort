package servitor

// Per-user command permissions for the web surface: which risk CATEGORIES
// (see classify_command) the operator has chosen to run WITHOUT a confirmation
// prompt — the web analog of the CLI's --allow flag. Stored per-user (the udb
// is already user-scoped), same as the per-command always-allow list. A
// category left unchecked still prompts before every matching command.

import (
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
func (T *Servitor) handlePermissions(c *jsonapi.Call) (any, error) {
	cats := loadAllowedCategories(c.UDB)
	out := map[string]bool{}
	for _, cat := range AllRiskCategories {
		out[string(cat)] = cats[cat]
	}
	return out, nil
}

// handlePermissionsSave sets which risk categories run without asking.
func (T *Servitor) handlePermissionsSave(c *jsonapi.Call, body map[string]bool) (any, error) {
	saveAllowedCategories(c.UDB, body)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// alwaysAllowed is one command an operator answered Always for, as the
// Permissions dialog lists it.
type alwaysAllowed struct {
	ApplianceID string `json:"appliance_id"`
	Appliance   string `json:"appliance"` // its name, or the id when it is gone
	Command     string `json:"command"`
	// Label says what the rule allows in words (describeRule), and Form is
	// the rule set out as the form's choices when it fits the form, for
	// editing there; nil for a rule edited as text.
	Label string    `json:"label"`
	Form  *ruleForm `json:"form,omitempty"`
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
		row := alwaysAllowed{ApplianceID: appliance, Appliance: name, Command: cmd, Label: describeRule(cmd)}
		if f, ok := parseRuleForm(cmd); ok {
			row.Form = &f
		}
		out = append(out, row)
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
func (T *Servitor) handleAlwaysAllowed(c *jsonapi.Call) (any, error) {
	if c.UDB == nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "no store for this user")
	}
	return map[string]any{"commands": T.listAlwaysAllowed(c.User, c.UDB)}, nil
}

// handleAlwaysAllowedSave removes an always-allow rule, replaces it with an
// edited one, or adds a new one typed in.
func (T *Servitor) handleAlwaysAllowedSave(c *jsonapi.Call, body alwaysAllowedReq) (any, error) {
	user, udb := c.User, c.UDB
	if udb == nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "no store for this user")
	}
	// A rule set out in the form is written as a rule string here, the one
	// place that knows how, for an add or an edit alike.
	if body.Form != nil {
		rule, err := composeRule(*body.Form)
		if err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		if !body.Add {
			// An edit keeps the program: the rule is for that program, and
			// another one is a rule of its own.
			old, ok := parseRuleForm(body.Command)
			var on bool
			if body.ApplianceID == "" || !udb.Get(alwaysAllowTable, alwaysAllowKey(body.ApplianceID, body.Command), &on) || !on {
				return nil, jsonapi.NotFound("that rule is no longer saved")
			}
			if !ok || old.Program != strings.TrimSpace(body.Form.Program) {
				return nil, jsonapi.BadRequest("an edit keeps the rule's program: add a new rule for another one")
			}
			udb.Unset(alwaysAllowTable, alwaysAllowKey(body.ApplianceID, body.Command))
			udb.Set(alwaysAllowTable, alwaysAllowKey(body.ApplianceID, rule), true)
			Log("[servitor] %s changed an always-allow rule on %s: %s -> %s", user, body.ApplianceID, body.Command, rule)
			return jsonapi.Empty(http.StatusNoContent), nil
		}
		body.Rule = rule
	}
	if body.Add {
		// Saved where an Always on that system's card would save it: this
		// user's store, for a system they can reach.
		rule := strings.TrimSpace(body.Rule)
		if body.ApplianceID == "" || rule == "" {
			return nil, jsonapi.BadRequest("appliance_id and rule are required")
		}
		if _, _, _, found := T.resolveAppliance(user, udb, body.ApplianceID); !found {
			return nil, jsonapi.NotFound("no such system")
		}
		if err := validNewRule(rule); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		udb.Set(alwaysAllowTable, alwaysAllowKey(body.ApplianceID, rule), true)
		Log("[servitor] %s added an always-allow rule on %s: %s", user, body.ApplianceID, rule)
		return jsonapi.Empty(http.StatusNoContent), nil
	}
	if body.ApplianceID == "" || body.Command == "" {
		return nil, jsonapi.BadRequest("appliance_id and command are required")
	}
	if rule := strings.TrimSpace(body.Rule); rule != "" {
		var on bool
		if !udb.Get(alwaysAllowTable, alwaysAllowKey(body.ApplianceID, body.Command), &on) || !on {
			return nil, jsonapi.NotFound("that rule is no longer saved")
		}
		if err := validEditedRule(body.Command, rule); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		udb.Unset(alwaysAllowTable, alwaysAllowKey(body.ApplianceID, body.Command))
		udb.Set(alwaysAllowTable, alwaysAllowKey(body.ApplianceID, rule), true)
		Log("[servitor] %s changed an always-allow rule on %s: %s -> %s", user, body.ApplianceID, body.Command, rule)
		return jsonapi.Empty(http.StatusNoContent), nil
	}
	udb.Unset(alwaysAllowTable, alwaysAllowKey(body.ApplianceID, body.Command))
	Log("[servitor] %s removed always-allow on %s for: %s", user, body.ApplianceID, body.Command)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// alwaysAllowedReq is what handleAlwaysAllowedSave is posted.
type alwaysAllowedReq struct {
	ApplianceID string `json:"appliance_id"`
	Command     string `json:"command"`
	// Rule, when set, replaces the saved one (an edit) rather than
	// removing it.
	Rule string `json:"rule"`
	// Add saves Rule as a new rule for the appliance, with no command
	// behind it (Command is not used): one typed into Permissions before
	// the command it covers has come up.
	Add bool `json:"add,omitempty"`
	// Form, when set, is the rule set out as the form's choices: written
	// as the rule (composeRule) for an add, or to replace Command for an
	// edit that keeps its program.
	Form *ruleForm `json:"form,omitempty"`
}
