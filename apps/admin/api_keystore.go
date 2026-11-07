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
}

// handleKeystoreChoices is where connection ?name='s secret ?setting= may
// come from, for its "Key from" choice: its own key, then every Keystore key
// of the right kind given for its address. Read when the form opens, so a
// key added a moment ago is there.
func (a *AdminApp) handleKeystoreChoices(c *jsonapi.Call) (any, error) {
	q := c.R.URL.Query()
	out := []map[string]string{{"value": keyOwn, "label": "Its own key (below)"}}
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
			"used_by": usedBy, "in_use": len(used) > 0,
		})
	}
	return rows, nil
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
