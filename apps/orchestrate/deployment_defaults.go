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
			out[setting] = deploymentSetting(RootDB, deploymentDefault, setting)
			out[setting+"_max"] = deploymentSetting(RootDB, deploymentMaximum, setting)
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
			ui.FormField{Field: s.key, Type: "select", Label: "Default for a fleet that has not decided",
				Options: deploymentOptions(spec, s.words, "No deployment default"),
				Help:    "What an owner's agents read before the owner sets anything. They can change it, in either direction."},
			ui.FormField{Field: s.key + "_max", Type: "select", Label: "Maximum any agent may hold",
				Options: deploymentOptions(spec, s.words, "No maximum"),
				Help:    "A ceiling, not a default: no agent resolves looser than this, whatever its owner set.",
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
func deploymentOptions(spec triSetting, words map[string]string, unset string) []ui.SelectOption {
	out := []ui.SelectOption{{Value: "", Label: unset}}
	for _, v := range spec.values {
		label := words[v]
		if label == "" {
			label = v
		}
		out = append(out, ui.SelectOption{Value: v, Label: label})
	}
	return out
}

// deploymentSettingOrder is how the admin section reads, and the words each
// setting uses for its values. Declared rather than derived: "on" means
// "allowed" for one of these and "they see it" for another, and a page that
// said "on" six times would be a page nobody could act on.
var deploymentSettingOrder = []struct {
	key, label, help string
	words            map[string]string
}{
	{defaultWorkspaceNetwork, "Network from an agent's workspace",
		"Whether code running in an agent's sandbox may open connections.",
		map[string]string{settingOn: "Allowed", settingOff: "Blocked"}},
	{defaultInboundMode, "Which agents may dispatch to an agent",
		"Who an agent accepts work from. Anyone, only its named callers, or nobody.",
		map[string]string{inboundOnly: "Only its named callers", inboundNone: "Nobody"}},
	{defaultShareCortex, "A shared agent's standing thread",
		"Whether somebody the agent is shared with sees what it has been doing.",
		map[string]string{settingOn: "They see it", settingOff: "Kept to the owner"}},
	{defaultShareReference, "A shared agent's attached knowledge",
		"Whether somebody the agent is shared with reaches the collections attached to it.",
		map[string]string{settingOn: "They see it", settingOff: "Kept to the owner"}},
	{defaultShareNotes, "A shared agent's working notes",
		"Whether somebody the agent is shared with reads its notes.",
		map[string]string{settingOn: "They see it", settingOff: "Kept to the owner"}},
	{defaultShareUploads, "A shared agent's uploads",
		"Whether somebody the agent is shared with reaches files uploaded to it.",
		map[string]string{settingOn: "They see it", settingOff: "Kept to the owner"}},
}
