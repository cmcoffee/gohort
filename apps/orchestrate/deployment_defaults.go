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
// "undecided".
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
func effectiveDeploymentDefault(db Database, setting string) string {
	s, ok := triSettings[setting]
	if !ok {
		return ""
	}
	if v := deploymentSetting(db, deploymentDefault, setting); v != "" {
		return v
	}
	return s.framework
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
			ui.FormField{Field: s.key, Type: "select", Label: "Default Setting",
				Options: deploymentOptions(spec),
				Help:    "What every agent reads until it says otherwise. An agent can be given its own answer, in either direction, and that wins."},
			ui.FormField{Field: s.key + "_max", Type: "select", Label: "Maximum any agent may hold",
				Options: deploymentOptions(spec),
				Help:    "A ceiling, not a default: no agent resolves looser than this, whatever its owner set. Leave it at the loosest value to impose nothing.",
				Detail: "This is the only control here that an owner cannot override. Leave it unset unless the deployment genuinely has to hold the line - a maximum that duplicates the default just removes a choice people are allowed to make.\\n\\n" +
					"Set it and the agents already looser than it are clamped on their next turn. Their own setting is not rewritten, so lifting the ceiling gives them back what they had rather than leaving them reset."},
		)
	}
	return ui.Section{
		Group:    "Agents",
		Title:    "Agent security across the deployment",
		Subtitle: "What every fleet starts from, and what none of them may exceed.",
		Detail: "An agent answers for itself; failing that its owner's default for all their agents; failing that these. The maximum sits over all of it.\\n\\n" +
			"Owners keep their own layer on purpose. This sets a floor under it and a roof over it, not the room.",
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
func deploymentOptions(spec triSetting) []ui.SelectOption {
	order := spec.strictness
	if len(order) == 0 {
		order = spec.values
	}
	out := []ui.SelectOption{}
	for _, v := range order {
		out = append(out, ui.SelectOption{Value: v, Label: settingWord(spec.key, v)})
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
	{defaultShareCortex, "A shared agent's standing thread",
		"Whether somebody the agent is shared with sees what it has been doing."},
	{defaultShareReference, "A shared agent's attached knowledge",
		"Whether somebody the agent is shared with reaches the collections attached to it."},
	{defaultShareNotes, "A shared agent's working notes",
		"Whether somebody the agent is shared with reads its notes."},
	{defaultShareUploads, "A shared agent's uploads",
		"Whether somebody the agent is shared with reaches files uploaded to it."},
}
