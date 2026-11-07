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
	"github.com/cmcoffee/gohort/core/recipes"
	"github.com/cmcoffee/gohort/core/service"
)

// registerConnectorsRoutes wires the connectors API under the admin sub-mux.
func (a *AdminApp) registerConnectorsRoutes(sub *http.ServeMux) {
	// Connectors: GET lists the connectors (the service kind); POST
	// ?action=approve|unapprove|update_spec&name acts on one of any kind;
	// DELETE removes one and tears it down. A connector has no approval: the
	// pages that reference it reach it. The other kinds (MCP servers, desktop
	// programs, watches, messaging, peer relays), each moving to a page of its
	// own later, are listed apart and still approved here: Approve
	// MATERIALIZES the capability (for remote_mcp, an enabled MCP server).
	sub.HandleFunc("/api/connectors", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleConnectors, a.gate()),
		"POST":   jsonapi.Post(a.db, a.handleConnectorAction, a.gate(), jsonapi.MaxBody(1<<20)),
		"DELETE": jsonapi.Delete(a.db, a.handleConnectorDelete, a.gate()),
	}.Serve)
	sub.HandleFunc("/api/connectors/other", jsonapi.Get(a.db, a.handleOtherConnectors, a.gate()))

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

	// A connection's settings and secrets, kept in its own space (its row's
	// Settings), and the values a setting may pick from (options_from).
	sub.HandleFunc("/api/connectors/settings", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleConnectionSettings, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleConnectionSettingsSave, a.gate(), jsonapi.MaxBody(1<<20)),
	}.Serve)
	sub.HandleFunc("/api/connectors/options", jsonapi.Post(a.db, a.handleConnectionOptions, a.gate(), jsonapi.MaxBody(1<<20)))
	// Who may reach a connection through agents' tools (its row's Access).
	sub.HandleFunc("/api/connectors/access", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleConnectionAccess, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleConnectionAccessSave, a.gate()),
	}.Serve)
}

// handleConnectors is every connector, with what each can do from its row.
func (a *AdminApp) handleConnectors(c *jsonapi.Call) (any, error) {
	return connectorRows(true), nil
}

// handleOtherConnectors is every integration of another kind, listed apart
// until each moves to a page of its own.
func (a *AdminApp) handleOtherConnectors(c *jsonapi.Call) (any, error) {
	return connectorRows(false), nil
}

// connectorRows are the rows of the connectors (the service kind) or of the
// other kinds.
func connectorRows(connectors bool) any {
	type connRow struct {
		Name string `json:"name"`
		// Title is what it is shown as: its display name, or its name.
		Title     string `json:"title"`
		Kind      string `json:"kind"`
		Summary   string `json:"summary"`
		Owner     string `json:"owner"`
		Approved  bool   `json:"approved"`
		LastError string `json:"last_error"`
		Template  string `json:"template,omitempty"` // provenance (which template authored it)
		Webhook   string `json:"webhook,omitempty"`  // a push bridge's provider → gets "Webhook…"
		// Its one editor: EditForm, the Go setup form a template made it with
		// (ComfyUI, A1111); EditSpec, its spec beside the assistant; NeedsFix,
		// the same, as Fix, when its spec does not validate.
		EditForm bool `json:"edit_form,omitempty"`
		EditSpec bool `json:"edit_spec,omitempty"`
		NeedsFix bool `json:"needs_fix,omitempty"`
		// TemplateUpdate is a connector whose template now writes a
		// different spec than it holds (Update from template).
		TemplateUpdate bool `json:"template_update,omitempty"`
		// A connector with a contract test and a health check (core/service):
		// how it has been doing, and the question asked before a test.
		Status      string `json:"status,omitempty"`
		Testable    bool   `json:"testable,omitempty"`
		HasHealth   bool   `json:"has_health,omitempty"`
		TestConfirm string `json:"test_confirm,omitempty"`
		// State is the badge: approved or pending, or for a services
		// connector referenced, not referenced, or changed.
		State string `json:"state"`
		// Endpoints is a service with endpoints to list (Endpoints on its row).
		Endpoints bool `json:"endpoints,omitempty"`
		// Group is the band the row sits in (connectorGroups); GroupSummary
		// what its heading says the band holds, and GroupOpen whether the
		// band starts open.
		Group        string `json:"group"`
		GroupSummary string `json:"group_summary,omitempty"`
		GroupOpen    bool   `json:"group_open,omitempty"`
		// CanApprove and CanUnapprove are a connector approved or not here:
		// a services connector has no approval, and its State says whether
		// a page references it.
		CanApprove   bool `json:"can_approve,omitempty"`
		CanUnapprove bool `json:"can_unapprove,omitempty"`
		// Deletable is a connector no page names: one referenced is taken
		// off its pages first.
		Deletable bool `json:"deletable,omitempty"`
		// HasSettings is a services connection: its display name, and the
		// settings and secrets it declares, are set under its row's Settings.
		HasSettings bool `json:"has_settings,omitempty"`
		// HasAccess is a connection: who may reach it through agents' tools
		// is set under its row's Access.
		HasAccess bool `json:"has_access,omitempty"`
		// Key is which secret a connection keeps (service.KeptSecrets), so
		// one key can be told from another without opening its Settings.
		Key string `json:"key,omitempty"`
		// Check is what is wrong with a connection as it is saved
		// (service.SelfCheck), read each time the list is: fixing it clears it.
		Check string `json:"check,omitempty"`
	}
	var rows []connRow
	uses, research := service.TieIns(RootDB), researchNamed()
	for _, conn := range ListConnectors(RootDB) {
		if (conn.Kind == service.Kind) != connectors {
			continue
		}
		row := connRow{Name: conn.Name, Title: conn.Title(), Kind: conn.Kind, Summary: ConnectorSummary(conn), Owner: conn.Owner,
			Approved: conn.Approved, LastError: conn.LastError, Template: conn.Template,
			Webhook: connectorWebhook(conn), State: "pending",
			Endpoints: conn.Kind == service.Kind, Group: connectorGroup(conn, research, uses),
			Deletable: len(uses[conn.Name]) == 0, HasSettings: conn.Kind == service.Kind,
			HasAccess: conn.Kind == service.Kind && conn.ID != ""}
		row.EditForm, row.EditSpec, row.NeedsFix = connectorEditors(conn)
		_, row.TemplateUpdate = templateUpdate(conn)
		row.Key = keptSecretsLine(conn)
		if problems := service.SelfCheck(RootDB, conn); len(problems) > 0 {
			row.Check = "Needs attention: " + strings.Join(problems, "; ") + "."
		}
		if conn.Approved {
			row.State = "approved"
		}
		if service.Testable(conn) {
			row.Testable = true
			row.TestConfirm = testConfirm(conn.Name)
			row.Status, row.HasHealth = connectorStatus(conn)
		}
		if service.TieInConnector(conn.Kind) {
			row.State, row.Status = tieInState(conn, uses[conn.Name], row.Status)
		} else {
			row.CanApprove, row.CanUnapprove = !conn.Approved, conn.Approved
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return slices.Index(connectorGroups, rows[i].Group) < slices.Index(connectorGroups, rows[j].Group)
	})
	// Each band's heading says what it holds, so a folded band still does.
	type tally struct{ n, pending, failing, referenced int }
	t := map[string]*tally{}
	for _, r := range rows {
		g := t[r.Group]
		if g == nil {
			g = &tally{}
			t[r.Group] = g
		}
		g.n++
		switch {
		case r.State == "pending":
			g.pending++
		case r.State == "referenced":
			g.referenced++
		}
		if r.LastError != "" {
			g.failing++
		}
	}
	for i := range rows {
		g := t[rows[i].Group]
		parts := []string{fmt.Sprint(g.n)}
		for _, x := range []struct {
			n    int
			what string
		}{{g.pending, "pending"}, {g.failing, "with an error"}, {g.referenced, "referenced"}} {
			if x.n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", x.n, x.what))
			}
		}
		rows[i].GroupSummary = strings.Join(parts, " · ")
	}
	if rows == nil {
		return []connRow{}
	}
	return rows
}

// connectorEditors is which one editor a connector's row opens: form, the Go
// setup form a template made it with (ComfyUI, A1111); spec, its spec beside
// the assistant; fix, the same as Fix, when its spec does not validate. At
// most one is true. Shared by every table that lists connectors, so a
// connector opens the same editor wherever it is listed.
func connectorEditors(conn Connector) (form, spec, fix bool) {
	if !service.Fixable(conn) {
		return false, false, false
	}
	_, made := TemplateForConnector(conn)
	valid := ValidateConnector(conn) == nil
	return made && valid, !made && valid, !valid
}

// keptSecretsLine is connection c's row's word on the secrets it keeps:
// "API key ends in …ab12, given 1 Nov 2026", in the order its spec declares
// them.
func keptSecretsLine(c Connector) string {
	kept := service.KeptSecrets(RootDB, c)
	var parts []string
	for _, st := range service.SettingsFor(c, "") {
		if hint, ok := kept[st.Key]; ok {
			parts = append(parts, st.Field.Title()+" "+hint)
		}
	}
	return strings.Join(parts, "; ")
}

// The Connectors table's bands, in order: what a connector does, which is
// what an admin looks for one by. Only connectors with a spec are listed:
// what gohort does in Go (SMTP, the browser, DuckDuckGo, the LLM providers) is
// set up on its own page, peers under Peers, programs under System Hooks.
const (
	groupSearch   = "Web Search"
	groupResearch = "Research Sources"
	groupImages   = "Images and media"
	groupAudio    = "Speech and Music"
	groupEmbed    = "Embeddings"
	groupPages    = "Pages and Files"
	groupMail     = "Mail"
	groupMessages = "Messaging"
	groupOther    = "Other Services"
	groupWatches  = "Watches"
	groupTools    = "Tools for agents (MCP and desktop)"
	groupHooks    = "Adapted System Hooks"
	groupPeers    = "Peer relays"
)

var connectorGroups = []string{groupSearch, groupResearch, groupImages, groupAudio, groupEmbed, groupPages,
	groupMail, groupMessages, groupOther, groupWatches, groupTools, groupHooks, groupPeers}

// capabilityGroups is the band a services connector sits in, by the first
// capability it gives.
var capabilityGroups = map[string]string{
	"web.search": groupSearch, "image.search": groupSearch,
	"image.generate": groupImages, "image.edit": groupImages, "media.generate": groupImages,
	"speech.transcribe": groupAudio, "speech.synthesize": groupAudio, "speech.voices": groupAudio, "music.generate": groupAudio,
	"embed.text": groupEmbed, "page.render": groupPages, "file.convert": groupPages,
	"mail.send": groupMail, "message.send": groupMessages, "message.fetch": groupMessages,
}

// researchTemplates is the template category of a research source's.
const researchTemplates = "Research Sources"

// connectorGroup is the band a stored connector sits in. A search a
// research source names, or one made from a research source's template and
// not Web Search's engine, is a research source; uses are the pages naming
// each connector (service.TieIns).
func connectorGroup(conn Connector, research map[string]bool, uses map[string][]string) string {
	switch conn.Kind {
	case RestMessagingConnectorKind, MessagingBridgeConnectorKind:
		return groupMessages
	case RestPollConnectorKind:
		return groupWatches
	case RemoteMCPConnectorKind, DesktopMCPConnectorKind, DesktopCommandConnectorKind:
		return groupTools
	case flows.ConnectorKind:
		return groupHooks
	case PeerRelayConnectorKind:
		return groupPeers
	case service.Kind:
	default:
		return groupOther
	}
	s, err := service.Parse(conn.Spec)
	if err != nil || len(s.Provides) == 0 {
		return groupOther
	}
	g, ok := capabilityGroups[s.Provides[0].Capability]
	if !ok {
		return groupOther
	}
	if g == groupSearch && !slices.Contains(uses[conn.Name], "Web Search") && researchConnector(conn, research) {
		return groupResearch
	}
	return g
}

// researchConnector reports whether c is a research source's connector: one
// a research source names (research, researchNamed), or one made from a
// Research Sources template.
func researchConnector(c Connector, research map[string]bool) bool {
	if research[c.Name] {
		return true
	}
	r, _, ok := recipes.Get(RootDB, c.Template)
	return ok && c.Template != "" && r.Category == researchTemplates
}

// researchNamed are the connectors a research source names, on or off.
func researchNamed() map[string]bool {
	out := map[string]bool{}
	for _, rs := range service.ListResearchSources(RootDB) {
		out[rs.Connector] = true
	}
	return out
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
	action := q.Get("action")
	if conn, ok := GetConnector(RootDB, name); ok && service.TieInConnector(conn.Kind) && (action == "approve" || action == "unapprove") {
		return nil, jsonapi.BadRequest(name + " has no approval: it is reached through the pages that reference it (Web Search, Image Generation, Research Sources, ...)")
	}
	switch action {
	case "approve":
		err = ApproveConnector(RootDB, name)
	case "unapprove":
		err = UnapproveConnector(RootDB, name)
	case "update_spec":
		// SaveConnector re-validates against the kind and, if the connector
		// is approved, re-materializes so an edit (e.g. an image
		// backend's options) takes effect immediately.
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
		return nil, jsonapi.BadRequest("action must be approve|unapprove|update_spec|revert")
	}
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	// An adapted System Hook turned on names the connector that does its
	// conversion: turning it on accepts a change waiting on that connector.
	if conn, ok := GetConnector(RootDB, name); ok && conn.Kind == flows.ConnectorKind && action == "approve" {
		var ad flows.Adapted
		json.Unmarshal(conn.Spec, &ad)
		acceptChanges(ad.Service)
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleConnectorDelete removes the ?name= backend and tears it down.
func (a *AdminApp) handleConnectorDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	if err := service.RemoveConnector(RootDB, name); err != nil {
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
		// Show an older image connector's comfy_workflow as a NESTED object (readable) rather
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

// tieInState is a services connector's badge and the start of its status:
// the pages that put it to use, none, or a change waiting to be accepted.
func tieInState(c Connector, pages []string, status string) (string, string) {
	join := func(lead string) string {
		if status == "" {
			return lead
		}
		return lead + " " + status
	}
	if ch, ok := service.PendingChange(RootDB, c.Name); ok {
		return "changed", join(changedNote(ch))
	}
	if len(pages) == 0 {
		return "unreferenced", join("Not referenced: no page names it, so it makes no calls. Choose it where it is to be used (Web Search, Image Generation, Research Sources, ...).")
	}
	return "referenced", join("Referenced by " + strings.Join(pages, ", ") + ".")
}
