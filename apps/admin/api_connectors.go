package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
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
	sub.HandleFunc("/api/connectors", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
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
			}
			var rows []connRow
			for _, c := range ListConnectors(RootDB) {
				_, canConfig := TemplateForConnector(c)
				rows = append(rows, connRow{c.Name, c.Kind, ConnectorSummary(c), c.Owner, c.Approved, c.LastError, c.Template, c.Kind == RestImageConnectorKind, canConfig})
			}
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			var err error
			switch r.URL.Query().Get("action") {
			case "approve":
				err = ApproveConnector(RootDB, name)
			case "unapprove":
				err = UnapproveConnector(RootDB, name)
			case "update_spec":
				// Replace the connector's kind-specific Spec with the posted JSON.
				// SaveConnector re-validates against the kind and, if the connector
				// is approved, re-materializes so an edit (e.g. a rest_image
				// backend's default_steps / submit_body) takes effect immediately.
				body, rerr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
				if rerr != nil {
					http.Error(w, "read error", http.StatusBadRequest)
					return
				}
				if !json.Valid(body) {
					http.Error(w, "spec must be valid JSON", http.StatusBadRequest)
					return
				}
				c, ok := GetConnector(RootDB, name)
				if !ok {
					http.Error(w, "no connector named "+name, http.StatusNotFound)
					return
				}
				// If comfy_workflow was edited as a nested object (the readable
				// Edit-spec view), fold it back into the spec's string field.
				c.Spec = json.RawMessage(stringifyComfyWorkflow(body))
				err = SaveConnector(RootDB, c)
			default:
				http.Error(w, "action must be approve|unapprove|update_spec", http.StatusBadRequest)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			if err := DeleteConnector(RootDB, name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Connector spec — GET the current kind-specific Spec as pretty JSON for the
	// admin's inline spec editor (the "Edit spec" row action). Paired with the
	// update_spec POST above. Read-only; no secret is ever in a Spec.
	sub.HandleFunc("/api/connectors/spec", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		c, ok := GetConnector(RootDB, name)
		if !ok {
			http.Error(w, "no connector named "+name, http.StatusNotFound)
			return
		}
		spec := "{}"
		if len(c.Spec) > 0 {
			// Show a rest_image comfy_workflow as a NESTED object (readable) rather
			// than an escaped string; other kinds just get plain indentation.
			if nested, ok := nestComfyWorkflow(c.Spec); ok {
				spec = string(nested)
			} else {
				var pretty bytes.Buffer
				if json.Indent(&pretty, c.Spec, "", "  ") == nil {
					spec = pretty.String()
				} else {
					spec = string(c.Spec)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name": c.Name, "kind": c.Kind, "approved": c.Approved, "spec": spec,
		})
	})

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
