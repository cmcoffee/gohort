package admin

// A page that uses a connection sets it up: the connection declares the
// settings it takes and what kind of value each is (core/service config.go),
// the page shows them for whichever connection is chosen, under the choice,
// and saving keeps them in the connection's own space (core
// connection_space.go), a secret in its secret half. The page's own record
// keeps only the choice.
//
// Each setting is a form field named config/<connection>/<key>, shown while
// that connection is chosen; a secret's key is its name.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
)

const settingsPrefix = "config/"

// settingField is the form field of a connection's setting.
func settingField(connection, key string) string {
	return settingsPrefix + connection + "/" + key
}

// connectorSettingsFields are, for each connection giving capability, the
// settings it takes for it, shown while the field choice holds the
// connection's name and the condition also (a show_when, or "").
func connectorSettingsFields(capability, also, choice string) []ui.FormField {
	var out []ui.FormField
	for _, c := range ListConnectors(RootDB) {
		if c.Kind != service.Kind || !service.Provides(c, capability) {
			continue
		}
		when := choice + ":" + c.Name
		if also != "" {
			when = also + ";" + when
		}
		for _, st := range service.SettingsFor(c, capability) {
			out = append(out, settingFormFields(c.Name, st, when)...)
		}
	}
	return out
}

// keptField is the form field saying which secret connection keeps for its
// setting key (service.KeptSecrets), read-only beside the secret's own.
func keptField(connection, key string) string {
	return "kept/" + connection + "/" + key
}

// keystoreSuffix marks a secret setting's "Key from" choice: the Keystore
// entry its key comes from, or keyOwn for one the connection keeps itself.
const (
	keystoreSuffix = "@keystore"
	keyOwn         = "own"
)

// keyFromField is the form field choosing where secret setting key's value
// comes from.
func keyFromField(connection, key string) string {
	return settingField(connection, key+keystoreSuffix)
}

// settingFormFields are the controls a setting's kind of value draws: its
// own, and for a secret, where it comes from (a Keystore key, or its own,
// typed below), its own box, and a line saying which one is kept while one
// is.
func settingFormFields(connection string, st service.Setting, when string) []ui.FormField {
	ff := settingFormField(connection, st, when)
	if !st.Field.IsSecret() {
		return []ui.FormField{ff}
	}
	from := keyFromField(connection, st.Key)
	choose := ui.FormField{Field: from, Label: st.Field.Title() + " from", Type: "select", Default: keyOwn, ShowWhen: when,
		OptionsSource: keystoreSource + "/choices?name=" + connection + "&setting=" + st.Key,
		Help:          "A key kept once under Services > Keystore, used by everything that talks to this provider, or one this connection keeps for itself."}
	ff.ShowWhen = when + ";" + from + ":" + keyOwn
	kept := keptField(connection, st.Key)
	return []ui.FormField{choose, ff, {Field: kept, Type: "readonly", Label: "Saved " + st.Field.Title(), ShowWhen: when + ";" + kept}}
}

// settingFormField is the control a setting's kind of value draws.
func settingFormField(connection string, st service.Setting, when string) ui.FormField {
	f := st.Field
	ff := ui.FormField{Field: settingField(connection, st.Key), Label: f.Title(), Help: f.Help, Detail: f.Detail,
		Type: "text", Default: f.Default, Placeholder: f.Placeholder, Required: f.Required, ShowWhen: when}
	if ff.Placeholder == "" {
		ff.Placeholder = f.Default
	}
	options := func() {
		for _, o := range f.Options {
			label := o.Label
			if label == "" {
				label = o.Value
			}
			ff.Options = append(ff.Options, ui.SelectOption{Value: o.Value, Label: label})
		}
	}
	switch f.Kind() {
	case "longtext":
		ff.Type, ff.Rows = "textarea", f.Rows
	case "number":
		ff.Type, ff.Decimals = "number", f.Decimals
		if f.Min != nil {
			ff.Min = int(*f.Min)
		}
		if f.Max != nil {
			ff.Max = int(*f.Max)
		}
	case "toggle":
		ff.Type = "toggle"
	case "choice":
		ff.Type = "select"
		options()
	case "choices":
		ff.Type = "checklist"
		options()
	case "secret":
		ff.Type, ff.Default, ff.Placeholder = "password", "", "(not set)"
		if ff.Help == "" {
			ff.Help = "Kept with this connection, for the address it is given for. Blank keeps the one it has."
		}
	}
	if f.OptionsFrom != "" && (ff.Type == "text" || ff.Type == "select") {
		ff.SuggestionsSource = "api/connectors/options?name=" + connection + "&setting=" + st.Key
		ff.SuggestionsLabel = "List"
	}
	return ff
}

// pageSettings are the connection settings a page's form posted, by
// connection: config/<connection>/<key> fields.
type pageSettings map[string]map[string]string

func settingsFromBody(raw []byte) pageSettings {
	var all map[string]any
	if json.Unmarshal(raw, &all) != nil {
		return nil
	}
	out := pageSettings{}
	for k, v := range all {
		rest, ok := strings.CutPrefix(k, settingsPrefix)
		if !ok {
			continue
		}
		conn, key, ok := strings.Cut(rest, "/")
		if !ok || conn == "" || key == "" {
			continue
		}
		if out[conn] == nil {
			out[conn] = map[string]string{}
		}
		out[conn][key] = formValue(v)
	}
	return out
}

// formValue is a posted value as a setting keeps it: a checklist's values
// joined by commas.
func formValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []any:
		var parts []string
		for _, x := range t {
			parts = append(parts, strings.TrimSpace(fmt.Sprint(x)))
		}
		return strings.Join(parts, ",")
	case bool:
		return strconv.FormatBool(t)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// withSettings is a page's form: its own fields, and the settings it
// posted for its connections.
type withSettings[T any] struct {
	Form     T
	Settings pageSettings
}

func (w *withSettings[T]) UnmarshalJSON(raw []byte) error {
	if err := json.Unmarshal(raw, &w.Form); err != nil {
		return err
	}
	w.Settings = settingsFromBody(raw)
	return nil
}

// split is what the form posted for connection c's settings for capability:
// the values, over what it keeps (the settings of its other capabilities
// stay; one its spec no longer declares goes), and the secrets typed (one
// left as the page showed it is not).
func (s pageSettings) split(c Connector, capability string) (map[string]string, map[string]string) {
	values := map[string]string{}
	kept := ConnectionConfig(RootDB, c)
	for _, st := range service.SettingsFor(c, "") {
		if v, ok := kept[st.Key]; ok && !st.Field.IsSecret() {
			values[st.Key] = v
		}
	}
	secrets := map[string]string{}
	posted := s[c.Name]
	for _, st := range service.SettingsFor(c, capability) {
		v, ok := posted[st.Key]
		// A secret's key box is not posted while a Keystore key is chosen:
		// its "Key from" choice alone says where the key comes from.
		from, chose := posted[st.Key+keystoreSuffix]
		if !ok && !(st.Field.IsSecret() && chose) {
			continue
		}
		if st.Field.IsSecret() {
			from = strings.TrimSpace(from)
			switch {
			case from != "" && from != keyOwn:
				secrets[st.Key] = service.KeystoreValue(from)
			case v != "" && v != secretUnchanged:
				secrets[st.Key] = v
			case from == keyOwn && service.KeystoreOf(c, st.Key) != "":
				// Back to a key of its own with none typed: the reference
				// goes, and a required key says it is needed.
				secrets[st.Key] = service.KeystoreValue("")
			}
			continue
		}
		if v == "" {
			delete(values, st.Key)
		} else {
			values[st.Key] = v
		}
	}
	return values, secrets
}

// save keeps what the form posted for connection name's settings for
// capability, checked against what each takes; "" (no connection chosen)
// keeps nothing.
func (s pageSettings) save(name, capability string) error {
	if name == "" {
		return nil
	}
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return fmt.Errorf("no connector named %s", name)
	}
	values, secrets := s.split(c, capability)
	return service.SaveSettings(c, capability, values, secrets)
}

// testContext is ctx with what the form holds for connection name's
// settings for capability, for a Test before a save.
func (s pageSettings) testContext(ctx context.Context, name, capability string) context.Context {
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return ctx
	}
	values, secrets := s.split(c, capability)
	return service.WithSecrets(service.WithConfig(ctx, values), secrets)
}

// values is what the form holds for connection name's settings for
// capability, over what it keeps, or nil with none chosen.
func (s pageSettings) values(name, capability string) map[string]string {
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return nil
	}
	values, _ := s.split(c, capability)
	return values
}

// settingsValues are connection name's settings for capability as its
// form's fields: what it keeps, a checklist's as a list, and a secret as kept
// when it has one.
func settingsValues(name, capability string) map[string]any {
	out := map[string]any{}
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return out
	}
	kept, secrets := ConnectionConfig(RootDB, c), service.KeptSecrets(RootDB, c)
	for _, st := range service.SettingsFor(c, capability) {
		field := settingField(name, st.Key)
		switch {
		case st.Field.IsSecret():
			if id := service.KeystoreOf(c, st.Key); id != "" {
				out[keyFromField(name, st.Key)] = id
			} else {
				out[keyFromField(name, st.Key)] = keyOwn
				if service.HasSecret(c, st.Key) {
					out[field] = secretUnchanged
				}
			}
			if hint, ok := secrets[st.Key]; ok {
				out[keptField(name, st.Key)] = hint
			}
		case st.Field.Kind() == "choices":
			if v, ok := kept[st.Key]; ok {
				var list []string
				for _, p := range strings.Split(v, ",") {
					if p = strings.TrimSpace(p); p != "" {
						list = append(list, p)
					}
				}
				out[field] = list
			}
		case st.Field.Kind() == "toggle":
			if v, ok := kept[st.Key]; ok {
				b, _ := strconv.ParseBool(v)
				out[field] = b
			}
		default:
			if v, ok := kept[st.Key]; ok {
				out[field] = v
			}
		}
	}
	return out
}

// withValues is a page's record v as its form reads it, with connection
// name's settings for capability beside its own fields.
func withValues(v any, name, capability string) map[string]any {
	raw, _ := json.Marshal(v)
	out := map[string]any{}
	json.Unmarshal(raw, &out)
	for k, x := range settingsValues(name, capability) {
		out[k] = x
	}
	return out
}

// staleField is a page's line saying its choice needs setting again
// (markStale), shown only then.
var staleField = ui.FormField{Field: "_stale", Type: "readonly", Label: "Needs setting again", ShowWhen: "_stale"}

// markStale reads a page's choice in out[field] as not set, and says why
// under _stale, when it no longer reads (service.StaleWhy): never as some
// other choice. Entries of out[list] that no longer read are dropped from
// it and named too.
func markStale(out map[string]any, field, list string) map[string]any {
	var whys []string
	if v, _ := out[field].(string); v != "" {
		if why := service.StaleWhy(RootDB, v); why != "" {
			out[field] = ""
			whys = append(whys, "This setting "+why+". Choose it again below.")
		}
	}
	if list != "" {
		var kept []string
		for _, v := range toStrings(out[list]) {
			if why := service.StaleWhy(RootDB, v); why != "" {
				whys = append(whys, "One offered to agents "+why+", so it is no longer offered.")
				continue
			}
			kept = append(kept, v)
		}
		if kept == nil {
			kept = []string{}
		}
		out[list] = kept
	}
	if len(whys) > 0 {
		out["_stale"] = strings.Join(whys, " ")
	}
	return out
}

// toStrings is v as a list of strings: a []string, or a decoded JSON list.
func toStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// chosenConnection is the connection a provider setting names, or "".
func chosenConnection(provider string) string {
	name, _ := media.ChosenConnection(provider)
	return name
}

// connectionNameField is the hidden field the row's Settings form names its
// connection by, so each connection's fields show for it alone.
const connectionNameField = "_conn"

// connectionSettingsFields are every connection's settings, each shown while
// the form is for that connection: the row's Settings.
func connectionSettingsFields() []ui.FormField {
	out := []ui.FormField{{Field: connectionNameField, Type: "hidden"},
		{Field: "display_name", Label: "Display name", Placeholder: "(its name)",
			Help: "What the pages that offer it, and its row, show it as. Blank shows its name."}}
	for _, c := range ListConnectors(RootDB) {
		if c.Kind != service.Kind {
			continue
		}
		for _, st := range service.SettingsFor(c, "") {
			out = append(out, settingFormFields(c.Name, st, connectionNameField+":"+c.Name)...)
		}
	}
	return out
}

// handleConnectionSettings is connection name's settings, as its row's
// Settings form reads them.
func (a *AdminApp) handleConnectionSettings(c *jsonapi.Call) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if _, ok := GetConnector(RootDB, name); !ok {
		return nil, jsonapi.NotFound("no connector named " + name)
	}
	conn, _ := GetConnector(RootDB, name)
	out := settingsValues(name, "")
	out[connectionNameField], out["display_name"] = name, conn.DisplayName
	return out, nil
}

// handleConnectionAccess is who may reach connection ?name= through agents'
// tools, for its row's Access.
func (a *AdminApp) handleConnectionAccess(c *jsonapi.Call) (any, error) {
	conn, ok := GetConnector(RootDB, strings.TrimSpace(c.R.URL.Query().Get("name")))
	if !ok {
		return nil, jsonapi.NotFound("no connector named " + c.R.URL.Query().Get("name"))
	}
	users := conn.Access(RootDB)
	if users == nil {
		users = []string{}
	}
	return map[string]any{"name": conn.Name, "allowed_users": users}, nil
}

// handleConnectionAccessSave limits connection ?name= to the users posted;
// none is everyone.
func (a *AdminApp) handleConnectionAccessSave(c *jsonapi.Call, body struct {
	AllowedUsers []string `json:"allowed_users"`
}) (any, error) {
	conn, ok := GetConnector(RootDB, strings.TrimSpace(c.R.URL.Query().Get("name")))
	if !ok {
		return nil, jsonapi.NotFound("no connector named " + c.R.URL.Query().Get("name"))
	}
	if err := conn.SetAccess(RootDB, body.AllowedUsers); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	who := "all users"
	if users := conn.Access(RootDB); len(users) > 0 {
		who = strings.Join(users, ", ")
	}
	adminChange(c.R, activity.Settings, "connection %s reachable through agents' tools by %s", conn.Name, who)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleConnectionSettingsSave keeps what the row's Settings form posted for
// connection name.
func (a *AdminApp) handleConnectionSettingsSave(c *jsonapi.Call, form withSettings[struct {
	DisplayName string `json:"display_name"`
}]) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if err := SetConnectorDisplayName(RootDB, name, form.Form.DisplayName); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if err := form.Settings.save(name, ""); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	adminChange(c.R, activity.Settings, "updated the settings of connection %q", name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleConnectionOptions lists what a setting may pick from, asked with the
// form's values: the picker's answer, or why there is none.
func (a *AdminApp) handleConnectionOptions(c *jsonapi.Call, form withSettings[struct{}]) (any, error) {
	q := c.R.URL.Query()
	name, key := strings.TrimSpace(q.Get("name")), strings.TrimSpace(q.Get("setting"))
	ctx, end := service.Preview(form.Settings.testContext(c.R.Context(), name, ""), name)
	defer end()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	opts, err := service.ListOptions(ctx, name, key)
	if err != nil {
		return map[string]any{"error": "Could not list them: " + err.Error()}, nil
	}
	return map[string]any{"suggestions": opts, "note": "What " + name + " offers. Pick one, or type any value."}, nil
}
