package admin

// Keystore: the keys gohort's own services use, one entry per provider
// account, named by the model tiers and connectors that talk to it
// (core/keystore). This file is its admin API; page_keystore.go its page.

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/keystore"
	"github.com/cmcoffee/gohort/core/service"
)

const keystoreSource = "api/keystore"

func (a *AdminApp) registerKeystoreRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/"+keystoreSource, jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleKeystore, a.gate()),
		"POST":   jsonapi.Post(a.db, a.handleKeystoreSave, a.gate()),
		"DELETE": jsonapi.Delete(a.db, a.handleKeystoreDelete, a.gate()),
	}.Serve)
	sub.HandleFunc("/"+keystoreSource+"/choices", jsonapi.Get(a.db, a.handleKeystoreChoices, a.gate()))
	sub.HandleFunc("/"+keystoreSource+"/move", jsonapi.Post(a.db, a.handleKeystoreMove, a.gate()))
}

// heldPrefix marks a row for a key a connector or model tier keeps of its
// own, listed so it can be moved into the Keystore: "held:connector:<name>"
// or "held:tier:<table>". The colons cannot occur in an entry's ID.
const heldPrefix = "held:"

// keystoreTiers are the model tiers whose own keys the page lists.
var keystoreTiers = []struct{ table, label string }{{LLMTable, "Worker model"}, {LeadLLMTable, "Lead model"}}

// heldRows are the keys connectors and model tiers keep of their own, as the
// Keystore page lists them under its entries.
func (a *AdminApp) heldRows() []map[string]any {
	var rows []map[string]any
	for _, h := range service.KeysHeldByConnections() {
		rows = append(rows, map[string]any{"id": heldPrefix + "connector:" + h.Connector, "name": h.Title,
			"kind": "Held by a connector", "key": h.Hint, "addresses": strings.Join(h.Addresses, ", "),
			"used_by": "kept by connector " + h.Title + " itself", "held": true})
	}
	for _, t := range keystoreTiers {
		if hint, addrs := TierKeyHeld(a.db, t.table); hint != "" {
			rows = append(rows, map[string]any{"id": heldPrefix + "tier:" + t.table, "name": t.label,
				"kind": "Held by a model tier", "key": hint, "addresses": strings.Join(addrs, ", "),
				"used_by": "kept by the " + strings.ToLower(t.label) + " itself", "held": true})
		}
	}
	return rows
}

// handleKeystoreMove moves a held key (?holder= a held row's ID) into the
// Keystore, pointing its connector or tier at the entry.
func (a *AdminApp) handleKeystoreMove(c *jsonapi.Call, _ struct{}) (any, error) {
	holder, _ := strings.CutPrefix(strings.TrimSpace(c.R.URL.Query().Get("holder")), heldPrefix)
	kind, name, _ := strings.Cut(holder, ":")
	var moved string
	switch kind {
	case "connector":
		conn, ok := GetConnector(RootDB, name)
		if !ok {
			return nil, jsonapi.NotFound("no connector named " + name)
		}
		names, err := service.MoveToKeystore(conn)
		if err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		moved = strings.Join(names, ", ")
	case "tier":
		if name != LLMTable && name != LeadLLMTable {
			return nil, jsonapi.BadRequest("no such model tier")
		}
		var provider, endpoint string
		a.db.Get(name, "provider", &provider)
		a.db.Get(name, "endpoint", &endpoint)
		n, err := (LLMProviderConfig{Provider: provider, Endpoint: endpoint}).MoveKeyToKeystore(a.db, name)
		if err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		moved = n
		if err := ReloadLLMs(); err != nil {
			Log("[admin] reloading the models after moving a key: %v", err)
		}
	default:
		return nil, jsonapi.BadRequest("say which key to move")
	}
	adminChange(c.R, activity.Settings, "moved %s's key into the Keystore as %s", name, moved)
	return map[string]any{"message": "Moved into the Keystore as " + moved + "."}, nil
}

// handleKeystoreChoices is where connection ?name='s secret ?setting= may
// come from, for its "Key from" choice: its own key, then every Keystore key
// of the right kind given for its address. Read when the form opens, so a
// key added a moment ago is there.
func (a *AdminApp) handleKeystoreChoices(c *jsonapi.Call) (any, error) {
	q := c.R.URL.Query()
	out := []map[string]string{{"value": keyOwn, "label": "Its own key (below)"}}
	// A model tier's choice: every key, since the provider and address on its
	// form may not be saved yet. Its save says when one does not fit.
	if q.Get("tier") != "" {
		for _, e := range keystore.List(RootDB) {
			out = append(out, map[string]string{"value": e.ID, "label": "Keystore: " + e.Name,
				"help": strings.Join(e.Addresses, ", ") + "; " + keystore.Hint(RootDB, e)})
		}
		return out, nil
	}
	conn, ok := GetConnector(RootDB, strings.TrimSpace(q.Get("name")))
	if !ok {
		return out, nil
	}
	for _, e := range service.KeystoreChoices(conn, strings.TrimSpace(q.Get("setting"))) {
		out = append(out, map[string]string{"value": e.ID, "label": "Keystore: " + e.Name, "help": keystore.Hint(RootDB, e)})
	}
	// The one it names now, even if it no longer fits, so the form shows it
	// and its row's check says why.
	if cur := service.KeystoreOf(conn, strings.TrimSpace(q.Get("setting"))); cur != "" {
		listed := false
		for _, o := range out {
			listed = listed || o["value"] == cur
		}
		if e, found := keystore.Get(RootDB, cur); found && !listed {
			out = append(out, map[string]string{"value": e.ID, "label": "Keystore: " + e.Name + " (does not fit this connection)"})
		}
	}
	return out, nil
}

// keystoreForm is an entry as its form reads and saves it. The secret is
// never sent back: key and aws_pair come back blank, kept says which is kept.
type keystoreForm struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Addresses []string `json:"addresses"`
	Key       string   `json:"key"`
	AWSPair   string   `json:"aws_pair"`
	Profile   string   `json:"profile"`
	Kept      string   `json:"kept,omitempty"`
}

// keystoreKindLabel is an entry's kind as its row reads.
func keystoreKindLabel(kind string) string {
	if kind == keystore.KindAWS {
		return "AWS signing"
	}
	return "Key"
}

// handleKeystore is every entry as its row reads (?id= one entry, as its form
// reads).
func (a *AdminApp) handleKeystore(c *jsonapi.Call) (any, error) {
	if id := strings.TrimSpace(c.R.URL.Query().Get("id")); id != "" {
		e, ok := keystore.Get(RootDB, id)
		if !ok {
			return nil, jsonapi.Status(http.StatusNotFound, "no such key")
		}
		return keystoreForm{ID: e.ID, Name: e.Name, Kind: e.Kind, Addresses: e.Addresses, Profile: e.Profile,
			Kept: keystore.Hint(RootDB, e)}, nil
	}
	rows := []map[string]any{}
	for _, e := range keystore.List(RootDB) {
		used := keystore.UsedBy(e.ID)
		usedBy := "not used yet"
		if len(used) > 0 {
			usedBy = "used by " + strings.Join(used, ", ")
		}
		rows = append(rows, map[string]any{
			"id": e.ID, "name": e.Name, "kind": keystoreKindLabel(e.Kind),
			"addresses": strings.Join(e.Addresses, ", "), "key": keystore.Hint(RootDB, e),
			"used_by": usedBy, "in_use": len(used) > 0, "entry": true,
		})
	}
	return append(rows, a.heldRows()...), nil
}

// handleKeystoreSave adds or changes an entry. A blank key keeps the one
// kept; keystore.Save refuses moving the addresses without the key again.
func (a *AdminApp) handleKeystoreSave(c *jsonapi.Call, req keystoreForm) (any, error) {
	e := keystore.Entry{Name: req.Name, Kind: req.Kind, Addresses: req.Addresses, Profile: req.Profile}
	if prev, ok := keystore.Get(RootDB, req.ID); ok {
		e.ID = prev.ID
		e.Kind = prev.Kind // fixed once made: a key does not become a signing set
	}
	secret := req.Key
	if e.Kind == keystore.KindAWS {
		secret = req.AWSPair
	}
	if strings.TrimSpace(secret) == secretUnchanged {
		secret = ""
	}
	saved, err := keystore.Save(RootDB, e, secret)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	adminChange(c.R, activity.Settings, "saved keystore entry %s", saved.Name)
	return map[string]any{"id": saved.ID, "message": "Saved " + saved.Name + "."}, nil
}

// handleKeystoreDelete removes an entry nothing names.
func (a *AdminApp) handleKeystoreDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("id"))
	e, _ := keystore.Get(RootDB, id)
	if err := keystore.Delete(RootDB, id); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	adminChange(c.R, activity.Settings, "deleted keystore entry %s", e.Name)
	return jsonapi.Empty(http.StatusNoContent), nil
}
