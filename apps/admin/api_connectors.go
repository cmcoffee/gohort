package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// registerConnectorsRoutes wires the connectors API under the admin sub-mux.
func (a *AdminApp) registerConnectorsRoutes(sub *http.ServeMux) {
	// Connectors — "bridge types" drafted by an authoring agent (the connector
	// tool) and awaiting admin approval, e.g. a calendar/CRM exposed through its
	// MCP server. GET lists; POST?action=approve|unapprove&name toggles; DELETE
	// removes + tears down. Approve MATERIALIZES the underlying capability (for
	// remote_mcp: an enabled MCP server), so its tools go live for agents. The
	// LLM never handles a secret — auth is a referenced credential or per-user
	// oauth; approval is the human gate.
	sub.HandleFunc("/api/connectors", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleConnectors, a.gate()),
		"POST":   jsonapi.Post(a.db, a.handleConnectorAction, a.gate(), jsonapi.EmptyBodyOK, jsonapi.MaxBody(1<<20)),
		"DELETE": jsonapi.Delete(a.db, a.handleConnectorDelete, a.gate()),
	}.Serve)

	// Connector spec — GET the current kind-specific Spec as pretty JSON for the
	// admin's inline spec editor (the "Edit spec" row action). Paired with the
	// update_spec POST above. Read-only; no secret is ever in a Spec.
	sub.HandleFunc("/api/connectors/spec", jsonapi.Get(a.db, a.handleConnectorSpec, a.gate()))

}

// handleConnectors is every backend, with what each can do from its row.
func (a *AdminApp) handleConnectors(c *jsonapi.Call) (any, error) {
	type connRow struct {
		Name         string `json:"name"`
		Kind         string `json:"kind"`
		Summary      string `json:"summary"`
		Owner        string `json:"owner"`
		Approved     bool   `json:"approved"`
		LastError    string `json:"last_error"`
		Template     string `json:"template,omitempty"` // provenance (which template authored it)
		IsImage      bool   `json:"is_image"`           // rest_image → image-section toolbar pick
		Configurable bool   `json:"configurable"`       // resolves to a template → gets "Configure" (incl. imports)
		Webhook      string `json:"webhook,omitempty"`  // a push bridge's provider → gets "Webhook…"
	}
	var rows []connRow
	for _, conn := range ListConnectors(RootDB) {
		_, canConfig := TemplateForConnector(conn)
		rows = append(rows, connRow{conn.Name, conn.Kind, ConnectorSummary(conn), conn.Owner, conn.Approved, conn.LastError, conn.Template, conn.Kind == RestImageConnectorKind, canConfig, connectorWebhook(conn)})
	}
	return rows, nil
}

// handleConnectorAction is ?action= on the ?name= backend: approve,
// unapprove, or update_spec, which replaces its kind-specific Spec with the
// posted JSON.
func (a *AdminApp) handleConnectorAction(c *jsonapi.Call, body json.RawMessage) (any, error) {
	q := c.R.URL.Query()
	name := strings.TrimSpace(q.Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	var err error
	switch q.Get("action") {
	case "approve":
		err = ApproveConnector(RootDB, name)
	case "unapprove":
		err = UnapproveConnector(RootDB, name)
	case "update_spec":
		// SaveConnector re-validates against the kind and, if the connector
		// is approved, re-materializes so an edit (e.g. a rest_image
		// backend's default_steps / submit_body) takes effect immediately.
		if !json.Valid(body) {
			return nil, jsonapi.BadRequest("spec must be valid JSON")
		}
		conn, ok := GetConnector(RootDB, name)
		if !ok {
			return nil, jsonapi.NotFound("no connector named " + name)
		}
		// If comfy_workflow was edited as a nested object (the readable
		// Edit-spec view), fold it back into the spec's string field.
		conn.Spec = json.RawMessage(stringifyComfyWorkflow(body))
		err = SaveConnector(RootDB, conn)
	default:
		return nil, jsonapi.BadRequest("action must be approve|unapprove|update_spec")
	}
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleConnectorDelete removes the ?name= backend and tears it down.
func (a *AdminApp) handleConnectorDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	if err := DeleteConnector(RootDB, name); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleConnectorSpec is the ?name= backend's spec as pretty JSON, for the
// inline spec editor. No secret is ever in a Spec.
func (a *AdminApp) handleConnectorSpec(c *jsonapi.Call) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	conn, ok := GetConnector(RootDB, name)
	if !ok {
		return nil, jsonapi.NotFound("no connector named " + name)
	}
	spec := "{}"
	if len(conn.Spec) > 0 {
		// Show a rest_image comfy_workflow as a NESTED object (readable) rather
		// than an escaped string; other kinds just get plain indentation.
		if nested, ok := nestComfyWorkflow(conn.Spec); ok {
			spec = string(nested)
		} else {
			var pretty bytes.Buffer
			if json.Indent(&pretty, conn.Spec, "", "  ") == nil {
				spec = pretty.String()
			} else {
				spec = string(conn.Spec)
			}
		}
	}
	return map[string]any{
		"name": conn.Name, "kind": conn.Kind, "approved": conn.Approved, "spec": spec,
	}, nil
}

// nestComfyWorkflow renders a spec for the Edit-spec view with comfy_workflow as
// a NESTED JSON object instead of an escaped string, so the graph is readable.
// Returns (indented, true) only when it nested a string-held workflow; otherwise
// (nil, false) to fall back to plain indentation (non-image specs, or a workflow
// already stored as an object).
func nestComfyWorkflow(spec []byte) ([]byte, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(spec, &m) != nil {
		return nil, false
	}
	wf, ok := m["comfy_workflow"]
	if !ok {
		return nil, false
	}
	var inner string
	if json.Unmarshal(wf, &inner) != nil || !json.Valid([]byte(inner)) {
		return nil, false // not a JSON-string-holding-JSON
	}
	m["comfy_workflow"] = json.RawMessage(inner)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, false
	}
	return out, true
}

// stringifyComfyWorkflow is the save-path inverse: if comfy_workflow was edited as
// a nested object (from nestComfyWorkflow's view), fold it back into a string
// (pretty-printed) so it matches the spec's string field. An already-string (or
// absent) workflow is left untouched.
func stringifyComfyWorkflow(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	wf, ok := m["comfy_workflow"]
	if !ok {
		return body
	}
	t := bytes.TrimSpace(wf)
	if len(t) == 0 || t[0] == '"' {
		return body // already a JSON string
	}
	strified, err := json.Marshal(PrettyComfyJSON(string(t)))
	if err != nil {
		return body
	}
	m["comfy_workflow"] = json.RawMessage(strified)
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// connectorWebhook names a push bridge's webhook provider, or "" for anything
// else, so only those rows offer the address and secret.
func connectorWebhook(c Connector) string {
	if c.Kind != RestMessagingConnectorKind {
		return ""
	}
	var s RestMessagingSpec
	if json.Unmarshal(c.Spec, &s) != nil {
		return ""
	}
	return s.WebhookProvider
}
