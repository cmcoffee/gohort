package admin

// Editing a connector: its row's Edit (or Fix, when its spec does not
// validate) opens its spec beside an assistant, the same assist System Hooks
// rows have. On the left, the connector's spec and the job each capability
// it gives must do (its contract: what it takes, gives, and is checked for);
// on the right, the worker, told the connector, how it has been failing and
// what its capabilities must give. Its tools check a draft, make one request
// of it and show the raw reply, and run a draft's contract test: the last two
// are real calls, on the connector's daily test budget. It proposes; the
// admin saves. A connector a Go setup form made (ComfyUI, A1111) is edited
// in that form instead.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/assist"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/recipes"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
)

func (a *AdminApp) registerConnectorFixRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/connectors/fix", assist.Handler(assist.Spec{
		Route: "app.admin.connector_fix", Label: "Connectors: fix a service", App: "/admin",
		Allow: a.requireAdmin, About: connectorFixAbout, Tools: connectorFixTools,
		Web: true,
	}))
	sub.HandleFunc("/api/connectors/fix-form", jsonapi.Get(a.db, a.handleConnectorFixForm, a.gate()))
	sub.HandleFunc("/api/connectors/fix-save", jsonapi.Post(a.db, a.handleConnectorFixSave, a.gate(), jsonapi.MaxBody(1<<20)))
	sub.HandleFunc("/api/connectors/update-template", jsonapi.Post(a.db, a.handleConnectorTemplateUpdate, a.gate()))
	sub.HandleFunc("/api/connectors/update-template/undo", jsonapi.Post(a.db, a.handleConnectorTemplateUndo, a.gate()))
}

// connectorEditAction is the row's editor, labelled label and shown when
// the row's onlyIf holds: the spec form beside its assistant, first asked
// ask.
func connectorEditAction(label, onlyIf, ask string) ui.RowAction {
	form := ui.FormPanel{
		Source:      "api/connectors/fix-form?name={name}",
		PostURL:     "api/connectors/fix-save?name={name}",
		SubmitLabel: "Save",
		Fields: []ui.FormField{
			{Field: "wrapper", Type: "readonly", Label: "What it is"},
			{Field: "contract", Type: "readonly", Label: "Its job"},
			{Field: "note", Type: "readonly", Label: "Saving"},
			{Field: "spec", Type: "textarea", Label: "Spec", Rows: 24},
		},
		Invalidate: connectorListSources,
		Assist:     true, AssistURL: "api/connectors/fix?name={name}",
	}
	act := ui.AssistAction(label, ui.AssistPanel{
		Work: form, URL: "api/connectors/fix?name={name}",
		Ask: ask, Web: true,
		Intro: "It knows this connector, how it has been failing, and what its capabilities must give back. " +
			"It can ask the service and try a corrected spec, which are real calls on the connector's daily test budget. " +
			"What it suggests fills the spec: nothing is saved until you save. Turn on web lookups to let it read the provider's documentation.",
		Placeholder: "Ask about it, or say what changed at the provider…",
	})
	act.OnlyIf = onlyIf
	return act
}

// handleConnectorFixForm is the ?name= connector's spec for the Fix form,
// with a note on what saving does.
func (a *AdminApp) handleConnectorFixForm(c *jsonapi.Call) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	s, notes, _, err := service.ForFix(name)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	pretty, _ := json.MarshalIndent(s, "", "  ")
	note := "Saving re-checks the spec and puts the change live at once."
	if len(notes) > 0 {
		note += " Not carried over: " + strings.Join(notes, "; ") + "."
	}
	var jobs []string
	for _, b := range s.Provides {
		jobs = append(jobs, service.ContractText(b.Capability))
	}
	return map[string]any{"spec": string(pretty), "note": note, "wrapper": s.Describe(name),
		"contract": strings.Join(jobs, "\n\n")}, nil
}

// handleConnectorFixSave saves the Fix form's spec on the ?name= connector.
func (a *AdminApp) handleConnectorFixSave(c *jsonapi.Call, body struct {
	Spec string `json:"spec"`
}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	conn, ok := GetConnector(RootDB, name)
	if !ok {
		return nil, jsonapi.NotFound("no connector named " + name)
	}
	s, err := service.Parse([]byte(body.Spec))
	if err == nil {
		err = s.Validate()
	}
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	raw, _ := json.Marshal(s)
	conn.Spec = raw
	if err := SaveConnector(RootDB, conn); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	// An administrator saving the fix accepts it, a change waiting on it
	// with it.
	acceptChanges(name)
	return map[string]any{"message": "Saved."}, nil
}

// canonicalSpec is a service spec as it is saved: parsed and written again,
// so two specs that say the same thing compare equal however each was laid
// out. ok is false for one that does not parse.
func canonicalSpec(raw []byte) (string, bool) {
	s, err := service.Parse(raw)
	if err != nil {
		return "", false
	}
	b, err := json.Marshal(s)
	return string(b), err == nil
}

// templateUpdate is the spec connector conn's template writes now, when it
// differs from conn's own and the template can be rendered again without the
// answers given at install (recipes.UpdatableConnectorSpec). A connector made
// before its template learned a job (Gemini's editing) gets it this way, with
// its settings and key, which live apart from the spec, kept.
func templateUpdate(conn Connector) (string, bool) {
	if conn.Kind != service.Kind || strings.TrimSpace(conn.Template) == "" {
		return "", false
	}
	raw, ok := recipes.UpdatableConnectorSpec(RootDB, conn.Template)
	if !ok {
		return "", false
	}
	now, ok := canonicalSpec(raw)
	if !ok {
		return "", false
	}
	if held, ok := canonicalSpec(conn.Spec); ok && held == now {
		return "", false
	}
	return now, true
}

// handleConnectorTemplateUpdate replaces connector ?name='s spec with the one
// its template writes now. Its settings and key are kept: they live in the
// connection's own space, which the spec does not hold. A change made to the
// spec by hand is replaced, which its button says before it runs.
func (a *AdminApp) handleConnectorTemplateUpdate(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	conn, ok := GetConnector(RootDB, name)
	if !ok {
		return nil, jsonapi.NotFound("no connector named " + name)
	}
	spec, ok := templateUpdate(conn)
	if !ok {
		return nil, jsonapi.BadRequest(name + " already has what its template writes, or its template cannot be written again without its install answers")
	}
	conn.Spec = json.RawMessage(spec)
	if err := SaveConnector(RootDB, conn); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	// It holds what its template writes now, so it follows the template's
	// next revision on its own (connector_template_update.go).
	RootDB.Set(templateSpecTable, name, conn.Spec)
	RootDB.Unset(templateUndoTable, name)
	acceptChanges(name)
	adminChange(c.R, activity.Settings, "updated connector %s from its template %s", name, conn.Template)
	return map[string]any{"message": "Updated from its template. Its settings and key are kept."}, nil
}

// connectorFixAbout is what the assistant is told first: the connector, how
// it has been failing, what its capabilities must give, and how to work.
func connectorFixAbout(r *http.Request) (string, error) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	s, notes, conn, err := service.ForFix(name)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("Connectors are how gohort reaches outside services. This one, " + name + ", is a service connector: how gohort reaches the service (provider) and what it gives gohort (provides). Read inside to outside (what gohort asks of it ⇐ the endpoints that does it ⇐ what it reaches): " + s.Describe(name) + "\n\n")
	b.WriteString(service.Grammar + "\n\n")
	b.WriteString("What its capabilities must give:\n")
	for _, bd := range s.Provides {
		b.WriteString("  " + service.ContractText(bd.Capability) + "\n")
	}
	if conn.Kind != service.Kind {
		b.WriteString("\nIt is stored as an older " + conn.Kind + " connector, shown here translated; saving converts it.")
		if len(notes) > 0 {
			b.WriteString(" Not carried over: " + strings.Join(notes, "; ") + ".")
		}
		b.WriteString("\n")
	}
	rec := service.RecordOf(name)
	if h := rec.Health; h != nil {
		fmt.Fprintf(&b, "\nIts last free health check (%s): ok=%v, %s\n", h.At.Format("Jan 2 15:04"), h.OK, h.Message)
	}
	for capability, c := range rec.Capabilities {
		calls, failed := c.Recent()
		fmt.Fprintf(&b, "%s: %d calls in the last two hours, %d failed.", capability, calls, failed)
		if c.LastError != "" {
			fmt.Fprintf(&b, " Last failure (%s): %s", c.LastFail.Format("Jan 2 15:04"), c.LastError)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nThe form beside this conversation is its spec. You fill it; the admin saves it. A fix may change how the service is reached (address, headers, operations, paths, bodies) and how each capability's steps and outputs read the replies. It keeps what the connector provides, its prices and its test budget: those are the admin's, and a draft that changes them is refused.\n\n"+
		"How to work: read the failure. Check your draft with service_check (free). See how the service answers now with service_request: one operation of your draft and its raw reply. Run service_try for the contract test once the draft looks right, and say what came of it. service_request on any operation but the free health check, and service_try, are real calls that may cost money, on a budget of %d today: spend them on purpose. With web lookups on, read the provider's API documentation. Propose the whole corrected spec in the form, and say what you changed and why.",
		service.TestsLeft(name, s))
	return b.String(), nil
}

// connectorFixTools are the assistant's: check a draft, ask the service one
// operation of it, run its contract test. None saves.
func connectorFixTools(r *http.Request) []AgentToolDef {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	specParam := ToolParam{Type: "string", Description: "The whole draft spec, as JSON."}
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "service_check",
				Description: "Check a draft spec without calling the service: whether it is valid, and that it keeps what the connector provides, its prices and its test budget. Free.",
				Parameters:  map[string]ToolParam{"spec": specParam},
				Required:    []string{"spec"},
				Caps:        []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				raw, _ := args["spec"].(string)
				if _, err := service.CheckDraft(name, raw); err != nil {
					return "Not valid: " + err.Error(), nil
				}
				return "Valid, and it keeps what the connector provides, its prices and its budget.", nil
			},
		},
		{
			Tool: Tool{
				Name:        "service_request",
				Description: "Make one operation of a draft spec against the service and see its raw reply (status and body), to learn how the service answers now. The free health operation costs nothing; any other is a real call on the connector's daily test budget.",
				Parameters: map[string]ToolParam{
					"spec":      specParam,
					"operation": {Type: "string", Description: "The operation's name in the draft."},
					"values":    {Type: "object", Description: "Values for the operation's {tokens}, e.g. {\"prompt\": \"a red circle\", \"width\": 256, \"height\": 256, \"id\": \"job-1\"}. A file input is given a small sample picture."},
				},
				Required: []string{"spec", "operation"},
				Caps:     []Capability{CapExecute},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				raw, _ := args["spec"].(string)
				draft, err := service.CheckDraft(name, raw)
				if err != nil {
					return "Not valid: " + err.Error(), nil
				}
				opName, _ := args["operation"].(string)
				values, _ := args["values"].(map[string]any)
				return service.RequestDraft(ctx, name, draft, strings.TrimSpace(opName), values)
			},
		},
		{
			Tool: Tool{
				Name:        "service_try",
				Description: "Run a draft spec's contract test against the service, without saving it: each capability's smallest real call, through its check. A real call on the connector's daily test budget.",
				Parameters:  map[string]ToolParam{"spec": specParam},
				Required:    []string{"spec"},
				Caps:        []Capability{CapExecute},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				raw, _ := args["spec"].(string)
				draft, err := service.CheckDraft(name, raw)
				if err != nil {
					return "Not valid: " + err.Error(), nil
				}
				reps, err := service.TryDraft(ctx, name, draft)
				if err != nil {
					return err.Error(), nil
				}
				var lines []string
				for _, rep := range reps {
					line := rep.Capability + ": " + rep.Detail
					if rep.Priced {
						line += fmt.Sprintf(" (cost $%.4f)", rep.Cost)
					}
					lines = append(lines, line)
				}
				return strings.Join(lines, "\n") + "\nNothing was saved.", nil
			},
		},
	}
}
