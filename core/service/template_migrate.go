package service

// A migration that moves settings onto a connector installs the template
// gohort ships for that provider, so the connector a move writes is the one
// Add would: one spec to read, test and fix, not a second copy in Go.

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/recipes"
)

// installMoved installs the built-in template id, its questions answered,
// as an approved connector, under the first free name after its own. Its
// credential, when it has one, holds key and is switched on; a key for a
// template that takes none is sent as a bearer token. from names the
// settings it was moved from, for the connector's description. It returns
// the connector's name and how many records it wrote.
func installMoved(db Database, id string, answers map[string]string, key, from string) (string, int, error) {
	r, _, ok := recipes.Get(db, id)
	if !ok {
		return "", 0, fmt.Errorf("the template %s is not on this gohort", id)
	}
	bundle, _, _, err := recipes.Fill(r, answers)
	if err != nil {
		return "", 0, fmt.Errorf("the template %s: %w", id, err)
	}
	var cred SecureCredential
	var conn Connector
	for _, a := range bundle.Artifacts {
		switch a.Type {
		case "credential":
			err = json.Unmarshal(a.Recipe, &cred)
		case "connector":
			err = json.Unmarshal(a.Recipe, &conn)
		}
		if err != nil {
			return "", 0, fmt.Errorf("the template %s: %w", id, err)
		}
	}
	if conn.Name == "" {
		return "", 0, fmt.Errorf("the template %s has no connector", id)
	}
	var spec map[string]any
	if err := json.Unmarshal(conn.Spec, &spec); err != nil {
		return "", 0, err
	}
	provider, _ := spec["provider"].(map[string]any)
	changed := 0
	if key = strings.TrimSpace(key); key != "" {
		if cred.Name == "" {
			base, _ := provider["base_url"].(string)
			cred = SecureCredential{Name: strings.ReplaceAll(id, "-", "_") + "_key", Type: SecureCredBearer,
				BaseURL: strings.TrimRight(base, "/"), Description: "The key " + from + " was sent with."}
		}
		cred.Name, cred.Disabled = freeCredentialName(cred.Name), false
		if err := Secure().Save(cred, key); err != nil {
			return "", changed, fmt.Errorf("saving the key as credential %s: %w", cred.Name, err)
		}
		changed++
		if provider != nil {
			provider["auth"] = map[string]any{"credential": cred.Name}
		}
	}
	if conn.Spec, err = json.Marshal(spec); err != nil {
		return "", changed, err
	}
	conn.Name, conn.Kind = freeConnectorName(db, conn.Name), Kind
	conn.Desc += ", moved here from the " + from + " settings"
	if err := SaveConnector(db, conn); err != nil {
		return "", changed, fmt.Errorf("saving connector %s: %w", conn.Name, err)
	}
	if err := ApproveConnector(db, conn.Name); err != nil {
		return "", changed, fmt.Errorf("approving connector %s: %w", conn.Name, err)
	}
	return conn.Name, changed + 1, nil
}
