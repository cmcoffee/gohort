// settings.go — an app's tunables. The author declares them on the spec
// (AppSpec.Settings); this file is the surface a person turns them on: the
// Settings page (a FormPanel over the declared fields, nothing bespoke), the
// values endpoint it reads and writes, and the reset that puts the defaults
// back. Values are strings all the way down because an environment variable
// is a string, and that is where every script reads them (see settingsFor).
//
// Where a set value lives follows the setting's scope. The owner's values sit
// in the owner's record base under the app's slug and serve every user of a
// shared app for owner-scoped settings. A user-scoped setting is one person's
// own: it lives in THEIR base, beside their copy of the records, so two people
// sharing one dashboard each keep their own city. The page shows the owner
// everything and a recipient only what is theirs to set.
package customapps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// settingsTable holds each person's set values, keyed by app slug, in their
// record base: map[setting name]value.
const settingsTable = "custom_settings"

// visibleSettings is the subset of an app's declared settings a person may
// set: the owner sets all of them; anyone else sets only the per-user ones.
func visibleSettings(spec AppSpec, owner bool) []AppSetting {
	out := []AppSetting{}
	for _, s := range spec.Settings {
		if strings.TrimSpace(s.Name) == "" {
			continue
		}
		if owner || s.PerUser() {
			out = append(out, s)
		}
	}
	return out
}

// loadSettingValues reads one person's set values for an app. Missing = none.
func loadSettingValues(db Database, slug string) map[string]string {
	vals := map[string]string{}
	if db != nil {
		db.Get(settingsTable, slug, &vals)
	}
	if vals == nil {
		vals = map[string]string{}
	}
	return vals
}

// settingField is the form control for one declared setting. The declared
// default rides in the help text so a person can see what "revert" means
// before pressing it.
func settingField(s AppSetting) ui.FormField {
	f := ui.FormField{Field: s.Name, Label: s.Label, Help: s.Help}
	if f.Label == "" {
		f.Label = s.Name
	}
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case "number":
		f.Type = "number"
		if s.Max > s.Min {
			f.Min, f.Max = s.Min, s.Max
		}
	case "toggle":
		f.Type = "toggle"
	case "choice":
		f.Type = "select"
		for _, o := range s.Options {
			f.Options = append(f.Options, ui.SelectOption{Value: o, Label: o})
		}
	default:
		f.Type = "text"
	}
	if s.Default != "" && f.Type != "toggle" {
		if f.Help != "" {
			f.Help += " "
		}
		f.Help += "Default: " + s.Default + "."
	}
	return f
}

// typedSetting turns a stored string into what the form control expects: a
// number for a number field, a bool for a toggle, the string otherwise.
func typedSetting(s AppSetting, v string) any {
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case "number":
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return n
		}
		return v
	case "toggle":
		return settingTruthy(v)
	}
	return v
}

func settingTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "on", "1", "yes":
		return true
	}
	return false
}

// coerceSetting turns what the form posted into the stored string, refusing a
// value the declaration rules out: a number outside its range, a choice not
// in the list. A toggle is stored as "true"/"false" so a script can read it
// without guessing which spelling of yes it will get.
func coerceSetting(s AppSetting, v any) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case "number":
		var n float64
		switch x := v.(type) {
		case float64:
			n = x
		case json.Number:
			f, err := x.Float64()
			if err != nil {
				return "", fmt.Errorf("%s must be a number", s.Name)
			}
			n = f
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			if err != nil {
				return "", fmt.Errorf("%s must be a number", s.Name)
			}
			n = f
		case bool:
			return "", fmt.Errorf("%s must be a number", s.Name)
		default:
			return "", fmt.Errorf("%s must be a number", s.Name)
		}
		if s.Max > s.Min && (n < float64(s.Min) || n > float64(s.Max)) {
			return "", fmt.Errorf("%s must be between %d and %d", s.Name, s.Min, s.Max)
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case "toggle":
		switch x := v.(type) {
		case bool:
			return strconv.FormatBool(x), nil
		case string:
			return strconv.FormatBool(settingTruthy(x)), nil
		}
		return strconv.FormatBool(settingTruthy(fmt.Sprint(v))), nil
	case "choice":
		val := strings.TrimSpace(fmt.Sprint(v))
		if len(s.Options) > 0 {
			ok := false
			for _, o := range s.Options {
				if o == val {
					ok = true
					break
				}
			}
			if !ok {
				return "", fmt.Errorf("%s must be one of: %s", s.Name, strings.Join(s.Options, ", "))
			}
		}
		return val, nil
	}
	return strings.TrimSpace(fmt.Sprint(v)), nil
}

// settingsBase is where one person's set values for an app live — their own
// record base, which for a private-DB app is the app's file.
func (T *CustomApps) settingsBase(spec AppSpec, uid string) Database {
	return T.recordBase(spec, uid)
}

// handleSettingsPage renders the app's Settings page: the framework's form
// over the settings this person may set, loading from and saving to the
// values endpoint, with a revert that clears their overrides.
func (T *CustomApps) handleSettingsPage(w http.ResponseWriter, r *http.Request, spec AppSpec, owner bool) {
	base := T.WebPath() + "/" + spec.Slug
	fields := []ui.FormField{}
	for _, s := range visibleSettings(spec, owner) {
		fields = append(fields, settingField(s))
	}
	var body ui.Component
	if len(fields) == 0 {
		body = ui.EmptyState{
			Icon:  "⚙",
			Title: "Nothing to set",
			Hint:  "This app declares no settings you can change.",
		}
	} else {
		body = ui.FormPanel{
			Source:       base + "/_settings/values",
			Fields:       fields,
			ResetURL:     base + "/_settings/reset",
			ResetConfirm: "Put every setting on this page back to its default?",
		}
	}
	subtitle := "Each change saves as you make it. Scripts read these the next time they run."
	if !owner {
		subtitle = "Your own settings for this shared app. " + subtitle
	}
	ui.Page{
		Title:     spec.Name + " settings",
		ShowTitle: true,
		BackURL:   base + "/",
		MaxWidth:  "720px",
		Sections:  []ui.Section{{Title: "Settings", Subtitle: subtitle, Body: body}},
	}.ServeHTTP(w, r)
}

// handleSettingsValues is the form's record: GET returns the declared
// defaults overlaid with this person's set values, typed for the controls;
// POST stores what the form sent for the settings this person may set,
// leaving the rest as they were, and refuses a value the declaration rules
// out. Only visible settings are ever read or written here — a recipient
// cannot reach the owner's values by naming them.
func (T *CustomApps) handleSettingsValues(w http.ResponseWriter, r *http.Request, spec AppSpec, uid string, owner bool) {
	visible := visibleSettings(spec, owner)
	db := T.settingsBase(spec, uid)
	switch r.Method {
	case http.MethodGet:
		stored := loadSettingValues(db, spec.Slug)
		out := map[string]any{}
		for _, s := range visible {
			v, ok := stored[s.Name]
			if !ok {
				v = s.Default
			}
			out[s.Name] = typedSetting(s, v)
		}
		writeJSON(w, out)
	case http.MethodPost:
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		stored := loadSettingValues(db, spec.Slug)
		for _, s := range visible {
			v, ok := in[s.Name]
			if !ok {
				continue
			}
			val, err := coerceSetting(s, v)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			stored[s.Name] = val
		}
		db.Set(settingsTable, spec.Slug, stored)
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSettingsReset clears this person's set values for the settings they
// may set, so the declared defaults apply again. The form re-loads itself.
func (T *CustomApps) handleSettingsReset(w http.ResponseWriter, r *http.Request, spec AppSpec, uid string, owner bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	db := T.settingsBase(spec, uid)
	stored := loadSettingValues(db, spec.Slug)
	for _, s := range visibleSettings(spec, owner) {
		delete(stored, s.Name)
	}
	if len(stored) == 0 {
		db.Unset(settingsTable, spec.Slug)
	} else {
		db.Set(settingsTable, spec.Slug, stored)
	}
	writeJSON(w, map[string]bool{"ok": true})
}
