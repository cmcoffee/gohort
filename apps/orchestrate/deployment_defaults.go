// What the deployment says, under every owner's answer and over none of them.
//
// The chain used to be: the agent, then a legacy field, then the OWNER's
// default for their own fleet, then a constant compiled into the framework. So
// a multi-tenant deployment had nothing to say at all - a fleet nobody had
// touched read the framework's answer, which for workspace reach is OPEN, and
// an administrator had no way to make it anything else.
//
// TWO controls, because they are two different powers and collapsing them
// would give an administrator either too little or too much:
//
//   - The DEFAULT is what a fleet reads before its owner decides anything. An
//     owner overrides it freely, in either direction. It is a starting point,
//     and a starting point that could not be moved would not be one.
//
//   - The MAXIMUM is a ceiling, and it is the one that actually constrains: no
//     agent on this deployment resolves looser than this, whatever its owner
//     set. It is how "nothing here reaches the network" gets said, and before
//     it there was no way to say it - an owner could always widen their own
//     agents back open.
//
// Owners keep their own layer. Governance here runs own -> peer-share ->
// admin-widen, and moving the fleet default to the admin console would take a
// decision away from the person who built the agent and is answerable for it,
// while making a single-user deployment visit the admin console to configure
// its own fleet. The administrator gets a floor under that and a roof over it,
// not the room.
//
// Unset means unset for both, and neither is spelled the same way as "off".
// Cleared, the rung below answers; set to off, this rung does.

package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sections"
	"github.com/cmcoffee/gohort/core/ui"
)

// deploymentSettingsTable holds both, keyed by setting and by which of the two
// it is. Deployment-wide, so no owner in the key - that absence IS the scope.
const deploymentSettingsTable = "deployment_settings"

const (
	deploymentDefault = "default"
	deploymentMaximum = "max"
)

func deploymentKey(kind, setting string) string { return kind + "\x00" + setting }

// deploymentSetting reads one, or "" when nothing is set.
func deploymentSetting(db Database, kind, setting string) string {
	if db == nil {
		return ""
	}
	var v string
	db.Get(deploymentSettingsTable, deploymentKey(kind, setting), &v)
	return strings.TrimSpace(v)
}

// setDeploymentSetting records it, or clears it when value is empty.
func setDeploymentSetting(db Database, kind, setting, value string) {
	if db == nil {
		return
	}
	key := deploymentKey(kind, setting)
	if value = strings.TrimSpace(value); value == "" {
		db.Unset(deploymentSettingsTable, key)
		return
	}
	db.Set(deploymentSettingsTable, key, value)
}

// effectiveDeploymentDefault is what the deployment default ACTUALLY is, never
// "undecided" and never looser than the maximum.
//
// Undecided is a real state in a record - an agent that has answered nothing
// must be told apart from one that answered "off" - and it is NOT a state a
// deployment-wide setting gets to be in. There is exactly one default and it
// always has a value: a page reading "not set" leaves the reader to work out
// what happens instead, and the answer was buried in a constant.
//
// Unset resolves to the FRAMEWORK's answer, which is the least restrictive one
// for every setting that has a choice, and which is what the deployment was
// already doing. Installing this changes nothing until an administrator
// decides it should.
//
// CLAMPED BY THE MAXIMUM, because a default looser than the ceiling is a
// number nothing ever runs under. Nothing reads it - every agent following it
// is clamped on the way out - so a page showing "Default: Allowed" under a
// maximum of Blocked would be reporting a value that exists only on that page.
// The ceiling is a ceiling over the default too.
func effectiveDeploymentDefault(db Database, setting string) string {
	s, ok := triSettings[setting]
	if !ok {
		return ""
	}
	v := deploymentSetting(db, deploymentDefault, setting)
	if v == "" {
		v = s.framework
	}
	return clampToDeploymentMaximum(db, setting, v)
}

// deploymentDefaultChoices are the values the default may take: the maximum
// and everything stricter than it.
//
// A select that offered the looser ones would be offering a choice the system
// cannot hold - pick it and the answer comes back clamped, which reads as the
// control having ignored the click. Not offering it is the same rule as
// everywhere else here: a control must not offer a state its value cannot be.
func deploymentDefaultChoices(db Database, setting string) []string {
	s, ok := triSettings[setting]
	if !ok || len(s.strictness) == 0 {
		return s.values
	}
	ceiling := effectiveDeploymentMaximum(db, setting)
	at := slices.Index(s.strictness, ceiling)
	if at < 0 {
		at = 0
	}
	return s.strictness[at:]
}

// effectiveDeploymentMaximum is the ceiling, or the loosest value the setting
// takes when no administrator has set one.
//
// Same reasoning: "no maximum" and "a maximum at the loosest value" are the
// same thing to every reader of it, and naming the value is the one that says
// what is happening. Clamping to the loosest value is a no-op, so an unset
// ceiling constrains nothing - which is what it should do.
func effectiveDeploymentMaximum(db Database, setting string) string {
	s, ok := triSettings[setting]
	if !ok || len(s.strictness) == 0 {
		return ""
	}
	if v := deploymentSetting(db, deploymentMaximum, setting); v != "" {
		return v
	}
	return s.strictness[0]
}

// clampToDeploymentMaximum returns the stricter of an answer and the
// deployment's ceiling.
//
// Strictness is declared per setting rather than assumed, because "stricter"
// is not the same direction for all of them: for workspace reach OFF is
// stricter, and for inbound mode the order runs any, only, none. A generic
// ceiling that guessed would clamp half of them the wrong way, which is the
// one failure a ceiling must not have.
//
// A ceiling naming a value the setting does not take is IGNORED, not treated
// as strictest: a typo must not silently ground a deployment.
func clampToDeploymentMaximum(db Database, setting, answer string) string {
	s, ok := triSettings[setting]
	if !ok || len(s.strictness) == 0 {
		return answer
	}
	ceiling := deploymentSetting(db, deploymentMaximum, setting)
	if ceiling == "" {
		return answer
	}
	ci, ai := slices.Index(s.strictness, ceiling), slices.Index(s.strictness, answer)
	if ci < 0 || ai < 0 {
		return answer
	}
	if ci > ai {
		return ceiling
	}
	return answer
}

// handleDeploymentSettings serves both controls. ADMIN only - an owner may set
// what their own fleet does and may not set what everybody's does.
func (T *OrchestrateApp) handleDeploymentSettings(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if !RequestIsAdmin(r) {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Cache-Control", "no-store")
		out := map[string]any{}
		for setting := range triSettings {
			out[setting] = effectiveDeploymentDefault(RootDB, setting)
			out[setting+"_max"] = effectiveDeploymentMaximum(RootDB, setting)
		}
		writeJSON(w, out)
	case http.MethodPatch, http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		for setting, spec := range triSettings {
			for _, pair := range []struct{ field, kind string }{
				{setting, deploymentDefault},
				{setting + "_max", deploymentMaximum},
			} {
				raw, present := body[pair.field]
				if !present {
					continue
				}
				v := ""
				if raw != nil {
					v = strings.TrimSpace(fmt.Sprint(raw))
				}
				if v != "" && !slices.Contains(spec.values, v) {
					http.Error(w, pair.field+" does not take "+v, http.StatusBadRequest)
					return
				}
				setDeploymentSetting(RootDB, pair.kind, setting, v)
				// Tightening the MAXIMUM pulls the default down with it. The
				// alternative is refusing the write and asking the reader to
				// go and change the other control first, which is a rule the
				// page would have to teach; this way the ceiling simply means
				// what it says, and the default that was too loose is one the
				// deployment was not running under anyway.
				if pair.kind == deploymentMaximum {
					if held := clampToDeploymentMaximum(RootDB, setting,
						effectiveDeploymentDefault(RootDB, setting)); held != "" {
						setDeploymentSetting(RootDB, deploymentDefault, setting, held)
					}
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// The admin surface, contributed through the section registry rather than by
// the admin page importing this app - the same seam the prompt editor and the
// file store use.
func init() {
	sections.RegisterAdminSection(sections.AdminSectionEntry{
		App:     "/orchestrate",
		Section: deploymentSettingsSection(),
	})
}

func deploymentSettingsSection() ui.Section {
	const api = "/orchestrate/api/console/deployment-settings"
	fields := []ui.FormField{}
	for _, s := range deploymentSettingOrder {
		spec := triSettings[s.key]
		fields = append(fields,
			ui.FormField{Type: "header", Label: s.label, Help: s.help},
			// NO help line on either. The labels are the words, the heading
			// above them says what the setting is, and the ⓘ has the rest. A
			// line that repeats its own label is a line the reader learns to
			// skip, which is how the ones that DO say something new stop being
			// read.
			ui.FormField{Field: s.key, Type: "select", Label: "Default",
				Options: optionsFor(spec.key, deploymentDefaultChoices(RootDB, spec.key)),
				Detail: "What every agent reads until it answers for itself. An agent can be given its own answer, looser or stricter, and that wins - up to the limit.\n\n" +
					"Only values the limit allows are offered here. A default looser than the limit is a value nothing ever runs under: every agent following it would be clamped on the way out, so it would exist on this page and nowhere else. Tightening the limit therefore pulls this down with it."},
			ui.FormField{Field: s.key + "_max", Type: "select", Label: "Limit",
				Options: optionsFor(spec.key, deploymentOrder(spec)),
				Detail: "No agent may be looser than this, whatever its owner sets. The only control here an owner cannot override. Leave it at the loosest value unless the deployment genuinely has to hold the line - a limit that matches the default just removes a choice people are allowed to make.\n\n" +
					"Set it and the agents already looser than it are clamped on their next turn. Their own setting is not rewritten, so lifting the ceiling gives them back what they had rather than leaving them reset."},
		)
	}
	return ui.Section{
		Group:    "Agents",
		Title:    "Agent security across the deployment",
		Subtitle: "What every agent starts from, and what none of them may exceed.",
		Detail: "An agent answers for itself; failing that, the Default below. The Limit sits over both, including over the Default: a default looser than the limit is a value nothing ever runs under.\n\n" +
			"An owner still decides for each of their own agents. This is what those agents start from, and how far any of them may go.",
		Body: ui.FormPanel{Source: api, PostURL: api, Method: "PATCH", Fields: fields},
	}
}

// deploymentOptions builds a select from a setting's own values, so a value
// added to triSettings appears here without this file being touched.
//
// No "not set" option. A deployment-wide setting always has an answer, and
// offering undecided would put a state on the page that the resolution chain
// does not have a rung for - the reader would be choosing between a value and
// a value spelled differently.
//
// Ordered LOOSEST FIRST, which is also the order the ceiling ranks them in, so
// the two selects on a row read the same way down.
func deploymentOrder(spec triSetting) []string {
	if len(spec.strictness) > 0 {
		return spec.strictness
	}
	return spec.values
}

// optionsFor turns a list of values into a select, in the setting's own words.
func optionsFor(key string, values []string) []ui.SelectOption {
	out := []ui.SelectOption{}
	for _, v := range values {
		out = append(out, ui.SelectOption{Value: v, Label: settingWord(key, v)})
	}
	return out
}

// deploymentSettingOrder is how the admin section reads. The words each value
// goes by are NOT here: they live on the setting itself, so the admin page,
// the per-agent selects and the line under them all say the same thing. They
// said three different things when each built its own.
var deploymentSettingOrder = []struct {
	key, label, help string
}{
	{defaultWorkspaceNetwork, "Network from an agent's workspace",
		"Whether code running in an agent's sandbox may open connections."},
	{defaultInboundMode, "Which agents may dispatch to an agent",
		"Who an agent accepts work from. Anyone, only its named callers, or nobody."},
	{defaultGuardrailDepth, "How carefully an agent's own guardrails are checked",
		"Quick answers straight off; Standard and Thorough reason first, adding a few seconds to each checked reply. The deployment's Rules have their own depth, under Governance."},
	{defaultShareCortex, "A shared agent's standing thread",
		"Whether somebody the agent is shared with sees what it has been doing."},
	{defaultShareReference, "A shared agent's attached knowledge",
		"Whether somebody the agent is shared with reaches the collections attached to it."},
	{defaultShareNotes, "A shared agent's working notes",
		"Whether somebody the agent is shared with reads its notes."},
	{defaultShareUploads, "A shared agent's uploads",
		"Whether somebody the agent is shared with reaches files uploaded to it."},
}
