package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
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
		"POST":   jsonapi.Post(a.db, a.handleConnectorAction, a.gate(), jsonapi.MaxBody(1<<20)),
		"DELETE": jsonapi.Delete(a.db, a.handleConnectorDelete, a.gate()),
	}.Serve)

	// Connector spec — GET the current kind-specific Spec as pretty JSON for the
	// admin's inline spec editor (the "Edit spec" row action). Paired with the
	// update_spec POST above. Read-only; no secret is ever in a Spec.
	sub.HandleFunc("/api/connectors/spec", jsonapi.Get(a.db, a.handleConnectorSpec, a.gate()))

	// A connector's contract test (a real call, within its daily budget) and
	// its free health check (core/service).
	sub.HandleFunc("/api/connectors/test", jsonapi.Post(a.db, a.handleConnectorTest, a.gate()))
	sub.HandleFunc("/api/connectors/health", jsonapi.Post(a.db, a.handleConnectorHealth, a.gate()))

	// A service's endpoints, each as it is sent and read and what uses it,
	// read from its spec (the row's Endpoints).
	sub.HandleFunc("/api/connectors/endpoints", jsonapi.Get(a.db, a.handleConnectorEndpoints, a.gate()))

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
		// A connector with a contract test and a health check (core/service):
		// how it has been doing, and the question asked before a test.
		Status      string `json:"status,omitempty"`
		Testable    bool   `json:"testable,omitempty"`
		HasHealth   bool   `json:"has_health,omitempty"`
		TestConfirm string `json:"test_confirm,omitempty"`
		Fixable     bool   `json:"fixable,omitempty"`
		// Stored is a connector kept in the store, which can be approved,
		// edited, exported and deleted; a built-in provider's row is not.
		// State is the badge: approved, pending or built in.
		Stored bool   `json:"stored"`
		State  string `json:"state"`
		// Endpoints is a service with endpoints to list (Endpoints on its row).
		Endpoints bool `json:"endpoints,omitempty"`
		// Group is the band the row sits in (connectorGroups).
		Group string `json:"group"`
	}
	var rows []connRow
	for _, conn := range ListConnectors(RootDB) {
		_, canConfig := TemplateForConnector(conn)
		row := connRow{conn.Name, conn.Kind, ConnectorSummary(conn), conn.Owner, conn.Approved, conn.LastError, conn.Template, conn.Kind == RestImageConnectorKind, canConfig, connectorWebhook(conn), "", false, false, "", service.Fixable(conn), true, "pending", conn.Kind == service.Kind, connectorGroup(conn.Kind)}
		if conn.Approved {
			row.State = "approved"
		}
		if service.Testable(conn) {
			row.Testable = true
			row.TestConfirm = testConfirm(conn.Name)
			row.Status, row.HasHealth = connectorStatus(conn)
		}
		rows = append(rows, row)
	}
	// gohort's own providers, after the ones added: read from their settings
	// (connector_builtins.go), tested by those settings' own tests.
	for _, b := range a.builtinConnectors() {
		rows = append(rows, connRow{Name: b.name, Kind: "built in", Summary: b.wrapper, Owner: "gohort",
			Status: b.status, Testable: b.test != nil, State: "built-in", Group: groupBuiltIn})
	}
	// The peers this instance borrows from (connector_peers.go), read from the
	// peers and the settings that name them, re-checked by Test.
	for _, pc := range a.peerConnectors() {
		rows = append(rows, connRow{Name: pc.name, Kind: "peer", Summary: pc.wrapper, Owner: "Peers",
			Status: pc.status, Testable: true, State: "peer", Endpoints: true, Group: groupPeers})
	}
	// The programs System Hooks' jobs run (connector_programs.go), read from
	// the jobs, set up and tested from System Hooks.
	for _, pc := range programRows() {
		rows = append(rows, connRow{Name: pc.name, Kind: "program", Summary: pc.wrapper, Owner: "System Hooks",
			Status: pc.status, State: "program", Endpoints: true, Group: groupPrograms})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return slices.Index(connectorGroups, rows[i].Group) < slices.Index(connectorGroups, rows[j].Group)
	})
	return rows, nil
}

// The Connectors table's bands, in order: what a connector is, which is
// what an admin looks for one by.
const (
	groupServices = "Services"
	groupMessages = "Messaging"
	groupWatches  = "Watches"
	groupTools    = "Tools for agents (MCP and desktop)"
	groupHooks    = "Adapted System Hooks"
	groupBuiltIn  = "Built in"
	groupPeers    = "Peers"
	groupPrograms = "Programs"
)

var connectorGroups = []string{groupServices, groupMessages, groupWatches, groupTools, groupHooks, groupBuiltIn, groupPeers, groupPrograms}

// connectorGroup is the band a stored connector of kind sits in.
func connectorGroup(kind string) string {
	switch kind {
	case service.Kind, RestImageConnectorKind:
		return groupServices
	case RestMessagingConnectorKind, MessagingBridgeConnectorKind, BotFrameworkConnectorKind:
		return groupMessages
	case RestPollConnectorKind:
		return groupWatches
	case RemoteMCPConnectorKind, DesktopMCPConnectorKind, DesktopCommandConnectorKind:
		return groupTools
	case flows.ConnectorKind:
		return groupHooks
	case PeerRelayConnectorKind:
		return groupPeers
	}
	return groupServices
}

// testConfirm is the question asked before a contract test: what it does and
// what it should cost.
func testConfirm(name string) string {
	cost := "It may cost money: no price is declared, so the cost is unknown."
	if est, known := service.Estimate(name); known {
		cost = fmt.Sprintf("It should cost about $%.4f.", est)
	}
	return "Run a contract test of " + name + "? It makes one small real call through the service and checks what comes back. " + cost + " Tests draw on the connector's daily test budget."
}

// connectorStatus is a service connector's record as one line: its health,
// its recent calls, and today's spend.
func connectorStatus(conn Connector) (string, bool) {
	rec := service.RecordOf(conn.Name)
	var parts []string
	hasHealth := false
	if conn.Kind == service.Kind {
		if spec, err := service.Parse(conn.Spec); err == nil && spec.Provider.Health != nil {
			hasHealth = true
		}
		switch h := rec.Health; {
		case h != nil && h.OK:
			parts = append(parts, "healthy, checked "+ago(h.At))
		case h != nil:
			parts = append(parts, "health check failed "+ago(h.At)+": "+h.Message)
		case hasHealth:
			parts = append(parts, "not checked yet")
		default:
			parts = append(parts, "no free health check: known only from its calls and tests")
		}
	}
	for capability, c := range rec.Capabilities {
		calls, failed := c.Recent()
		if calls > 0 {
			noun := "calls"
			if calls == 1 {
				noun = "call"
			}
			parts = append(parts, fmt.Sprintf("%s: %d %s in the last two hours, %d failed", capability, calls, noun, failed))
		}
		if !c.LastFail.IsZero() && c.LastFail.After(c.LastOK) {
			parts = append(parts, "failing since "+ago(c.LastFail)+": "+c.LastError)
		}
		if c.Spend > 0 {
			parts = append(parts, fmt.Sprintf("$%.4f today", c.Spend))
		}
		if c.Unpriced > 0 {
			parts = append(parts, fmt.Sprintf("%d unpriced calls today", c.Unpriced))
		}
	}
	if rec.Tests > 0 {
		t := fmt.Sprintf("%d contract tests today", rec.Tests)
		if rec.Tests == 1 {
			t = "1 contract test today"
		}
		if rec.TestSpend > 0 {
			t += fmt.Sprintf(" ($%.4f)", rec.TestSpend)
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, "; "), hasHealth
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return t.Format("Jan 2")
}

// handleConnectorTest runs the ?name= connector's contract test. A failure
// comes back as a warning, so its reason opens in a dialog rather than a
// toast that is gone before it is read.
func (a *AdminApp) handleConnectorTest(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	if strings.HasPrefix(name, builtinPrefix) {
		return a.builtinTest(c, name)
	}
	if strings.HasPrefix(name, peerRowPrefix) {
		return peerTest(c.R.Context(), name), nil
	}
	reps, err := service.Test(c.R.Context(), name)
	if err != nil {
		return map[string]any{"message": err.Error(), "warnings": []string{err.Error()}}, nil
	}
	var lines, warns []string
	for _, r := range reps {
		line := r.Capability + ": " + r.Detail
		if r.Priced {
			line += fmt.Sprintf(" (cost $%.4f)", r.Cost)
		}
		lines = append(lines, line)
		if !r.OK {
			warns = append(warns, line)
		}
	}
	return map[string]any{"message": strings.Join(lines, "\n"), "warnings": warns}, nil
}

// handleConnectorEndpoints lists the ?name= service's endpoints.
func (a *AdminApp) handleConnectorEndpoints(c *jsonapi.Call) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if strings.HasPrefix(name, peerRowPrefix) {
		pc, ok := a.peerByName(name)
		if !ok {
			return nil, jsonapi.Status(http.StatusNotFound, "no peer named "+strings.TrimPrefix(name, peerRowPrefix))
		}
		return peerEndpoints(pc), nil
	}
	if strings.HasPrefix(name, programPrefix) {
		pc, ok := programByName(name)
		if !ok {
			return nil, jsonapi.Status(http.StatusNotFound, "no program "+strings.TrimPrefix(name, programPrefix)+" is used here")
		}
		return programEndpoints(pc), nil
	}
	conn, ok := GetConnector(RootDB, name)
	if !ok || conn.Kind != service.Kind {
		return nil, jsonapi.Status(http.StatusNotFound, "no service connector named "+name)
	}
	s, err := service.Parse(conn.Spec)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return s.Endpoints(), nil
}

// handleConnectorHealth runs the ?name= service's free health check now.
func (a *AdminApp) handleConnectorHealth(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	h, err := service.CheckHealth(c.R.Context(), name)
	if err != nil {
		return map[string]any{"message": err.Error(), "warnings": []string{err.Error()}}, nil
	}
	if !h.OK {
		return map[string]any{"message": "Health check failed: " + h.Message, "warnings": []string{h.Message}}, nil
	}
	return map[string]any{"message": "Healthy: " + h.Message}, nil
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
