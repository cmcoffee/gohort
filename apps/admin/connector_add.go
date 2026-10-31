package admin

// Adding a connector from its spec: a service written out (or drafted by the
// assistant from the service's documentation), checked as it is written,
// tested before it is saved, and saved unapproved like every other new
// connector. Templates add the usual providers with their credentials; this
// is for a service no template knows.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/recipes"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
)

// draftBudgetName is whose daily test budget a draft's real calls draw on: one
// of its own, so drafting a new connector never spends an existing one's.
const draftBudgetName = "connector-draft"

func (a *AdminApp) registerConnectorAddRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/connectors/add", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleConnectorAddForm, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleConnectorAdd, a.gate(), jsonapi.MaxBody(1<<20)),
	}.Serve)
	sub.HandleFunc("/api/connectors/add/test", jsonapi.Post(a.db, a.handleConnectorAddTest, a.gate(), jsonapi.MaxBody(1<<20)))
	sub.HandleFunc("/api/connectors/add/assist", assist.Handler(assist.Spec{
		Route: "app.admin.connector_add", Label: "Connectors: add a service", App: "/admin",
		Allow: a.requireAdmin, About: a.connectorAddAbout, Tools: a.connectorAddTools,
		Web: true,
	}))
}

// connectorAddForm is the Add a connector form: a name and the spec, and,
// to read, what each capability runs on now and where a spec can start from.
type connectorAddForm struct {
	Name   string `json:"name"`
	Spec   string `json:"spec"`
	Now    string `json:"now,omitempty"`
	Starts string `json:"starts,omitempty"`
}

// handleConnectorAddForm is the blank form, with what exists already.
func (a *AdminApp) handleConnectorAddForm(c *jsonapi.Call) (any, error) {
	return connectorAddForm{Now: strings.Join(a.capabilitiesNow(), "; "), Starts: startingPointsText()}, nil
}

// capabilitiesNow is what each capability runs on here now: gohort's own
// provider, a peer, or a connector, so someone adding one sees what it would
// replace or sit beside.
func (a *AdminApp) capabilitiesNow() []string {
	own := func(p, builtin string) string {
		if isLocal(p) {
			return builtin
		}
		return providerName(p)
	}
	onOff := func(enabled bool, p, builtin string) string {
		if isLocal(p) && !enabled {
			return "off"
		}
		return own(p, builtin)
	}
	emb, tr, sp := a.storedEmbeddingConfig(), a.storedTranscribeConfig(), a.storedSpeechConfig()
	image := a.storedString(ImageTable, "provider")
	if image == "" {
		image = "gemini"
	}
	search, source := a.storedString(SearchTable, "provider"), a.storedString(SearchTable, "source")
	if search == "" {
		search = "duckduckgo"
	}
	switch {
	case !isLocal(source):
		search = providerName(source)
	case strings.HasPrefix(search, media.ServiceProviderPrefix):
		search = providerName(search)
	default:
		search += " (built in)"
	}
	mail := a.storedString(MailTable, "provider")
	switch {
	case strings.HasPrefix(mail, media.ServiceProviderPrefix):
		mail = providerName(mail)
	case a.storedString(MailTable, "server") == "" && a.storedString(MailTable, "from") == "":
		mail = "off"
	default:
		mail = "SMTP (built in)"
	}
	tier := func(table string) string {
		p := a.storedString(table, "provider")
		switch {
		case p == "":
			return "not set"
		case strings.HasPrefix(p, media.ServiceProviderPrefix) || strings.HasPrefix(p, "peer:"):
			return providerName(p)
		}
		return p + " (built-in client)"
	}
	return []string{
		"Embeddings: " + onOff(emb.Enabled, emb.Provider, "the local embedder (built in)"),
		"Transcription: " + onOff(tr.Enabled, tr.Provider, "the local endpoint (built in)"),
		"Speech: " + onOff(sp.Enabled, sp.Provider, "the local endpoint (built in)"),
		"Image Generation: " + imageChoiceName(image),
		"Web search: " + search,
		"Page rendering: " + own(LoadBrowseConfig().Source, "this machine's browser (built in)"),
		"Mail: " + mail,
		"Worker LLM: " + tier(LLMTable),
		"Lead LLM: " + tier(LeadLLMTable),
		"File conversion: programs on this machine (System Hooks)",
	}
}

// startingPoint is a spec a new connector can start from: a template's
// connector, or a service connector already here.
type startingPoint struct{ id, label, category, spec string }

// startingPoints are the templates that add a service connector, and the
// service connectors here, each with its spec ready to edit (a template's
// questions filled with their defaults).
func startingPoints() []startingPoint {
	var out []startingPoint
	for _, sum := range recipes.List(RootDB) {
		r, _, ok := recipes.Get(RootDB, sum.ID)
		if !ok {
			continue
		}
		for _, art := range r.Bundle.Artifacts {
			if art.Type != "connector" {
				continue
			}
			text := string(art.Recipe)
			for _, q := range r.Questions {
				text = strings.ReplaceAll(text, "{{"+q.Name+"}}", q.Default)
			}
			var rec struct {
				Kind string          `json:"kind"`
				Spec json.RawMessage `json:"spec"`
			}
			if json.Unmarshal([]byte(text), &rec) != nil || rec.Kind != service.Kind {
				continue
			}
			out = append(out, startingPoint{id: "template:" + r.ID, label: r.Title, category: sum.Category, spec: prettyJSON(rec.Spec)})
		}
	}
	for _, c := range ListConnectors(RootDB) {
		if c.Kind == service.Kind {
			out = append(out, startingPoint{id: "connector:" + c.Name, label: "the connector " + c.Name, spec: prettyJSON(c.Spec)})
		}
	}
	return out
}

func prettyJSON(raw []byte) string {
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") != nil {
		return string(raw)
	}
	return b.String()
}

// startingPointsText says where a spec can start from, for the form: the
// templates by category, then the connectors here.
func startingPointsText() string {
	var order []string
	byCategory := map[string][]string{}
	var here []string
	for _, p := range startingPoints() {
		if !strings.HasPrefix(p.id, "template:") {
			here = append(here, strings.TrimPrefix(p.label, "the connector "))
			continue
		}
		if _, seen := byCategory[p.category]; !seen {
			order = append(order, p.category)
		}
		byCategory[p.category] = append(byCategory[p.category], p.label)
	}
	// Short titles ("Brave" for "Brave (web search)"), unless two in one
	// category would read the same (ntfy, and ntfy).
	for c, labels := range byCategory {
		short := make([]string, len(labels))
		count := map[string]int{}
		for i, l := range labels {
			short[i], _, _ = strings.Cut(l, " (")
			count[short[i]]++
		}
		for i := range short {
			if count[short[i]] > 1 {
				short[i] = labels[i]
			}
		}
		byCategory[c] = short
	}
	var groups []string
	for _, c := range order {
		groups = append(groups, c+": "+strings.Join(byCategory[c], ", "))
	}
	text := "Ask the assistant to start from a template (" + strings.Join(groups, "; ") + ")"
	if len(here) > 0 {
		text += " or a connector here (" + strings.Join(here, ", ") + ")"
	}
	return text + ", and change what differs. gohort's own providers are written in Go, so they have no spec to start from."
}

// draft reads the form's spec and checks it.
func (f connectorAddForm) draft() (service.Spec, error) {
	if strings.TrimSpace(f.Spec) == "" {
		return service.Spec{}, fmt.Errorf("write the spec, or ask the assistant to draft it from the service's documentation")
	}
	s, err := service.Parse([]byte(f.Spec))
	if err != nil {
		return s, err
	}
	return s, s.Validate()
}

// connectorAddButton is the Connectors section's Add a connector.
func connectorAddButton() ui.ModalButton {
	return ui.ModalButton{
		Label: "Add a connector…", Title: "Add a connector", Align: "left", Width: "860px",
		Subtitle: "For a service no template knows: write its spec, or let the assistant draft it from the service's documentation. It is saved unapproved.",
		Body: ui.FormPanel{
			Source:      "api/connectors/add",
			PostURL:     "api/connectors/add",
			SubmitLabel: "Add connector",
			TestURL:     "api/connectors/add/test",
			TestLabel:   "Test the draft",
			Assist:      true,
			AssistURL:   "api/connectors/add/assist",
			AssistLabel: "Draft it with the assistant",
			Invalidate:  []string{"api/connectors"},
			Fields: []ui.FormField{
				{Field: "now", Label: "What each capability runs on now", Type: "readonly",
					Help: "An approved connector is offered beside these where each is set up; choosing it there is what puts it in use."},
				{Field: "starts", Label: "Starting from one", Type: "readonly"},
				{Field: "name", Label: "Name", Required: true, Placeholder: "acme-images",
					Help: "Lowercase letters, digits, dashes and underscores. The settings that use it list it by this name."},
				// Inline: a spec is edited in place, and its example shows as
				// a placeholder rather than a preview that reads as content.
				{Field: "spec", Label: "Spec", Type: "textarea", Rows: 22, Required: true, Inline: true,
					Placeholder: connectorAddExample,
					Help:        "provider is how the service is reached, provides what it gives gohort. Test the draft runs each capability's smallest real call through its contract's check, without saving.",
					Detail: "A key never goes in the spec: auth names an API credential, set up under API Credentials (a template makes both). " +
						"What a capability takes and gives is fixed by its contract (image.generate, speech.synthesize, embed.text, web.search, page.render, mail.send, file.convert, message.send, music.generate, llm.chat); the spec says how this service's endpoints do it. " +
						"Saved, it is off until you approve it on its row, and ✨ Fix there helps when the service changes."},
			},
		},
	}
}

// connectorAddExample is the spec box's placeholder: a small whole spec.
const connectorAddExample = `{
  "provider": {
    "transport": "http",
    "base_url": "https://api.example.com",
    "auth": {"credential": "example_key"},
    "operations": {
      "speak": {"method": "POST", "path": "/v1/speech", "body": {"text": "{text}", "voice": "{voice}"}, "read": "file"}
    }
  },
  "provides": [
    {"capability": "speech.synthesize", "steps": [{"call": "speak"}], "output": {"audio": {"from": "body"}}}
  ]
}`

// handleConnectorAdd saves the form's draft, checked, under a name not taken,
// as a service connector that is off until approved.
func (a *AdminApp) handleConnectorAdd(c *jsonapi.Call, f connectorAddForm) (any, error) {
	name := strings.TrimSpace(f.Name)
	if _, taken := GetConnector(RootDB, name); taken {
		return nil, jsonapi.BadRequest("there is already a connector called " + name + ": pick another name, or open that one's Edit spec")
	}
	s, err := f.draft()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	raw, _ := json.Marshal(s)
	if err := SaveConnector(RootDB, Connector{Name: name, Kind: service.Kind, Owner: c.User, Spec: raw}); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"ok": true, "message": "Added " + name + ". It is off: approve it on its row under Connectors to put it live."}, nil
}

// handleConnectorAddTest runs the form's draft contract test without saving
// it: real calls, on the drafts' own daily budget.
func (a *AdminApp) handleConnectorAddTest(c *jsonapi.Call, f connectorAddForm) (any, error) {
	s, err := f.draft()
	if err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	reps, err := service.TryDraft(c.R.Context(), draftBudgetName, s)
	if err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	ok, lines := true, []string{}
	for _, rep := range reps {
		line := rep.Capability + ": " + rep.Detail
		if rep.Priced {
			line += fmt.Sprintf(" (cost $%.4f)", rep.Cost)
		}
		lines = append(lines, line)
		ok = ok && rep.OK
	}
	msg := strings.Join(lines, "\n") + "\nNothing was saved."
	if !ok {
		return testResultBody(false, "", msg), nil
	}
	return testResultBody(true, msg, ""), nil
}

// connectorAddAbout is what the assistant is told: how a spec is written,
// every contract it can give, and how to work.
func (a *AdminApp) connectorAddAbout(r *http.Request) (string, error) {
	var b strings.Builder
	b.WriteString("Connectors are how gohort reaches outside services. The person is adding a new service connector: how gohort reaches the service (provider) and what it gives gohort (provides), as one JSON spec. The form beside this conversation has its name and its spec; you draft the spec there, the admin saves it, and it stays off until they approve it.\n\n")
	b.WriteString(service.Grammar + "\n\n")
	b.WriteString("The contracts a service can give, and what each takes and gives:\n")
	for _, name := range service.Names() {
		b.WriteString("  " + service.ContractText(name) + "\n")
	}
	b.WriteString("\nWhat each capability runs on here now: " + strings.Join(a.capabilitiesNow(), "; ") + ".\n")
	b.WriteString("Specs to start from, with service_start: ")
	var ids []string
	for _, p := range startingPoints() {
		ids = append(ids, p.id+" ("+p.label+")")
	}
	b.WriteString(strings.Join(ids, ", ") + ". Starting from the closest one and changing what differs is quicker and surer than writing from blank.\n\n")
	b.WriteString("  A model (llm.chat) is not mapped step by step: its binding is {capability: \"llm.chat\", dialect: \"openai\"|\"anthropic\"|\"gemini\", defaults: {model}}, with no steps or output, and it may be priced with cost {per_million_input, per_million_output}.\n\n")
	fmt.Fprintf(&b, "How to work: ask which service and what it should give gohort, then read the service's API documentation (turn on web lookups for it). A key never goes in the spec: auth names an API credential the admin sets up under API Credentials; say which one it needs and what kind (a bearer token, a header and its name, a query value). "+
		"Check a draft with service_check (free). See how the service answers with service_request: one operation of the draft and its raw reply. Run service_try for the contract test once it looks right. service_request on anything but the free health check, and service_try, are real calls that may cost money, on a budget of %d today shared by all drafts: spend them on purpose, and they need the credential to exist and be enabled. Put the whole spec in the form, and say what it does.",
		service.TestsLeft(draftBudgetName, service.Spec{}))
	return b.String(), nil
}

// connectorAddTools are the assistant's: check a draft, ask the service one
// operation of it, run its contract test. None saves.
func (a *AdminApp) connectorAddTools(r *http.Request) []AgentToolDef {
	specParam := ToolParam{Type: "string", Description: "The whole draft spec, as JSON."}
	check := func(raw string) (service.Spec, error) {
		return connectorAddForm{Spec: raw}.draft()
	}
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "service_start",
				Description: "The spec of a template's connector (template:<id>) or of a connector here (connector:<name>), to start a new one from: change what differs and put the whole spec in the form. Free.",
				Parameters:  map[string]ToolParam{"from": {Type: "string", Description: "template:<id> or connector:<name>, as the list of specs to start from names them."}},
				Required:    []string{"from"},
				Caps:        []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				from, _ := args["from"].(string)
				for _, p := range startingPoints() {
					if p.id == strings.TrimSpace(from) {
						return p.spec, nil
					}
				}
				return "Nothing to start from called " + from + ": name one from the list, as template:<id> or connector:<name>.", nil
			},
		},
		{
			Tool: Tool{
				Name:        "service_check",
				Description: "Check a draft spec without calling the service: whether it is valid, and what is wrong with it if not. Free.",
				Parameters:  map[string]ToolParam{"spec": specParam},
				Required:    []string{"spec"},
				Caps:        []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				raw, _ := args["spec"].(string)
				s, err := check(raw)
				if err != nil {
					return "Not valid: " + err.Error(), nil
				}
				return "Valid. It reads: " + s.Describe("(new)"), nil
			},
		},
		{
			Tool: Tool{
				Name:        "service_request",
				Description: "Make one operation of a draft spec against the service and see its raw reply (status and body), to learn how it answers. The free health operation costs nothing; any other is a real call on the drafts' daily test budget.",
				Parameters: map[string]ToolParam{
					"spec":      specParam,
					"operation": {Type: "string", Description: "The operation's name in the draft."},
					"values":    {Type: "object", Description: "Values for the operation's {tokens}, e.g. {\"prompt\": \"a red circle\", \"width\": 256}. A file input is given a small sample picture."},
				},
				Required: []string{"spec", "operation"},
				Caps:     []Capability{CapExecute},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				raw, _ := args["spec"].(string)
				s, err := check(raw)
				if err != nil {
					return "Not valid: " + err.Error(), nil
				}
				opName, _ := args["operation"].(string)
				values, _ := args["values"].(map[string]any)
				return service.RequestDraft(ctx, draftBudgetName, s, strings.TrimSpace(opName), values)
			},
		},
		{
			Tool: Tool{
				Name:        "service_try",
				Description: "Run a draft spec's contract test against the service, without saving it: each capability's smallest real call, through its check. A real call on the drafts' daily test budget.",
				Parameters:  map[string]ToolParam{"spec": specParam},
				Required:    []string{"spec"},
				Caps:        []Capability{CapExecute},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				raw, _ := args["spec"].(string)
				s, err := check(raw)
				if err != nil {
					return "Not valid: " + err.Error(), nil
				}
				reps, err := service.TryDraft(ctx, draftBudgetName, s)
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
