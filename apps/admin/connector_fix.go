package admin

// Fixing a connector: a service connector (or an older image backend read as
// one, core/service) has a ✨ Fix on its row, the same assist System Hooks
// rows have. On the left, the connector's spec; on the right, the worker,
// told the connector, why it is failing and what its capabilities must give.
// Its tools check a draft, make one request of it and show the raw reply,
// and run a draft's contract test: the last two are real calls, on the
// connector's daily test budget. It proposes; the admin saves.
//
// Saving an older image backend converts it into a service connector, off
// until the admin approves it again, since what it reaches may have changed.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
}

// connectorFixAction is the row's ✨ Fix: the spec form beside its assistant.
func connectorFixAction() ui.RowAction {
	form := ui.FormPanel{
		Source:      "api/connectors/fix-form?name={name}",
		PostURL:     "api/connectors/fix-save?name={name}",
		SubmitLabel: "Save",
		Fields: []ui.FormField{
			{Field: "note", Type: "readonly", Label: "Saving"},
			{Field: "spec", Type: "textarea", Label: "Spec", Rows: 24},
		},
		Invalidate: []string{"api/connectors"},
		Assist:     true, AssistURL: "api/connectors/fix?name={name}",
	}
	act := ui.AssistAction("Fix", ui.AssistPanel{
		Work: form, URL: "api/connectors/fix?name={name}",
		Ask: "Why is this service failing, and what would fix it?", Web: true,
		Intro: "It knows this connector, how it has been failing, and what its capabilities must give back. " +
			"It can ask the service and try a corrected spec, which are real calls on the connector's daily test budget. " +
			"What it suggests fills the spec: nothing is saved until you save. Turn on web lookups to let it read the provider's documentation.",
		Placeholder: "Ask about it, or say what changed at the provider…",
	})
	act.OnlyIf, act.Compact = "fixable", true
	return act
}

// handleConnectorFixForm is the ?name= connector's spec for the Fix form,
// with a note on what saving does.
func (a *AdminApp) handleConnectorFixForm(c *jsonapi.Call) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	s, notes, conn, err := service.ForFix(name)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	pretty, _ := json.MarshalIndent(s, "", "  ")
	note := "Saving re-checks the spec and, if the connector is approved, puts the change live at once."
	if conn.Kind != service.Kind {
		note = "This " + conn.Kind + " connector is shown as a service spec. Saving converts it into a service connector, off until you approve it again."
	}
	if len(notes) > 0 {
		note += " Not carried over: " + strings.Join(notes, "; ") + "."
	}
	return map[string]any{"spec": string(pretty), "note": note}, nil
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
	msg := "Saved."
	if conn.Kind != service.Kind {
		if conn.Approved {
			if err := UnapproveConnector(RootDB, name); err != nil {
				return nil, jsonapi.BadRequest(err.Error())
			}
		}
		conn, _ = GetConnector(RootDB, name)
		conn.Kind = service.Kind
		msg = "Saved as a service connector. It is off: approve it to put it live."
	}
	conn.Spec = raw
	if err := SaveConnector(RootDB, conn); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"message": msg}, nil
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
	b.WriteString("Connectors are how gohort reaches outside services. This one, " + name + ", is a service connector: how gohort reaches the service (provider) and what it gives gohort (provides).\n\n")
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
