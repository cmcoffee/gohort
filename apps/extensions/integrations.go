package extensions

// Integrations: everything a person connects for themselves, in one list
// with one Add. Each kind is its own app's endpoints, registered with
// core/sections; this page lists them together and hands an add to the
// kind's own endpoint. Nothing is stored here.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/governance"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/sections"
	"github.com/cmcoffee/gohort/core/ui"
)

// pickField is the Add form's choice of kind. Each kind's own fields are
// named <kind>__<field>, so two kinds' fields of one name stay apart.
const pickField = "integration"

func kindField(kind, field string) string { return kind + "__" + field }

// registerIntegrations registers this app's kinds: APIs (its own, and those
// lent to the person), MCP servers and webhooks.
func (T *Extensions) registerIntegrations() {
	credList := jsonapi.Get(T.DB, T.handleCredentials)
	sections.RegisterIntegrationKind(sections.IntegrationKind{
		Name:  "api",
		Label: "API",
		Order: 10,
		Blurb: "An API your agents call as you, with your key.",
		Help: "Your agents each get a fetch_url_<name> tool for it. Its Security says who may use it: only tools that declare it, or the agents you pick, whether to ask you first, and who you lend it to. " +
			"You can paste its key now; after that, change or remove it under Connected accounts. Keys are stored encrypted and never shown to the assistant.",
		CanAdd:  func(user string) (bool, string) { return governance.Offer(governance.Credentials, user) },
		Fields:  func(string) []ui.FormField { return credentialFormFields() },
		Add:     T.credentialPost(),
		List:    credList,
		Actions: credentialRowActions(),
		Row: func(rec map[string]any) sections.IntegrationRow {
			reach := "all my agents"
			if recBool(rec, "secured") {
				reach = "tools only"
			}
			shared := recStr(rec, "shared_summary")
			if shared == "Just you" {
				shared = ""
			}
			row := sections.IntegrationRow{Key: recStr(rec, "name"), Title: recStr(rec, "name"),
				Detail: joinNonEmpty(recStr(rec, "base_url"), "reaches "+reach, recStr(rec, "lending_label"), labelled("shared: ", shared))}
			if recBool(rec, "handover_pending") {
				row.Detail = joinNonEmpty(row.Detail, "handover pending")
			}
			switch {
			case recBool(rec, "awaiting_approval"):
				row.Status = sections.StatusWaiting
			case recBool(rec, "disabled"):
				row.Status = sections.StatusOff
			case recBool(rec, "needs_key"):
				row.Status = sections.StatusNeedsKey
			}
			return row
		},
	})
	sections.RegisterIntegrationKind(sections.IntegrationKind{
		Name:  "api_lent",
		Label: "API lent to you",
		Order: 11,
		List:  withQuery(credList, "lent=1"),
		Row: func(rec map[string]any) sections.IntegrationRow {
			return sections.IntegrationRow{Key: recStr(rec, "owner") + "/" + recStr(rec, "name"), Title: recStr(rec, "name"),
				Detail: joinNonEmpty(labelled("lent by ", recStr(rec, "owner")), labelled("you may ", recStr(rec, "grant")), recStr(rec, "base_url"))}
		},
	})
	sections.RegisterIntegrationKind(sections.IntegrationKind{
		Name:  "mcp",
		Label: "MCP server",
		Order: 20,
		Blurb: "A server whose tools your agents use.",
		Help: "Once it answers, each of its tools reaches your agents as <name>_<tool>; an agent with its own tool list takes only the ones you put on it, and Private mode takes them away. " +
			"One that signs in with OAuth is listed under Connected accounts, where you press Connect to sign in. " +
			"A tool whose name says it changes something asks you in the chat first, and does not run where nobody is watching. Check asks the server again for what it offers.",
		CanAdd:  func(user string) (bool, string) { return governance.Offer(governance.MCPServers, user) },
		Fields:  func(string) []ui.FormField { return mcpAddFields() },
		Add:     T.mcpServerPost(),
		List:    jsonapi.Get(T.DB, T.handleMCPServers),
		Actions: mcpRowActions(),
		Row: func(rec map[string]any) sections.IntegrationRow {
			row := sections.IntegrationRow{Key: recStr(rec, "name"), Title: recStr(rec, "name"),
				Detail: joinNonEmpty(recStr(rec, "url"), labelled("signs in with ", recStr(rec, "auth")), labelled("tools: ", recStr(rec, "tools")))}
			switch {
			case recBool(rec, "awaiting"):
				row.Status = sections.StatusWaiting
			case recBool(rec, "disabled"):
				row.Status = sections.StatusOff
			case recStr(rec, "problem") != "":
				row.Status, row.Detail = sections.StatusTrouble, joinNonEmpty(row.Detail, labelled("last check: ", recStr(rec, "problem")))
			}
			return row
		},
	})
	whGet := jsonapi.Get(T.DB, T.handleWebhooks)
	sections.RegisterIntegrationKind(sections.IntegrationKind{
		Name:  "webhook",
		Label: "Webhook",
		Order: 30,
		Blurb: "An address a service such as GitHub, Stripe or Slack posts to.",
		Help: "Each checks who is posting and passes the event to one of your watches, or to a linked service's channel. " +
			"Ask an agent to watch for something posted to it first, which makes a watch; then add the webhook with the check the service offers and where its event's text is, " +
			"and give the service its address (under Edit) and secret. Each call is listed under Calls: refused ones say why.\n\n" +
			"A webhook that checks nothing has a long random address, since that address is then its only secret: use it only for a tool on your own network.\n\n" +
			"For Slack, so its messages come at once instead of on each poll: add a linked service for Slack first. Then add a webhook checked by Slack's request signing, " +
			"pasting the signing secret from your Slack app's Basic Information, reading text event.text, chat event.channel, from event.user, id event.ts, kind event.type, passing on only message, " +
			"and passing to the channel slack. Give its address to the Slack app under Event Subscriptions: Slack checks it at once, and this answers. Subscribe to message.channels.",
		Fields:   func(string) []ui.FormField { return webhookFields(true) },
		Defaults: withQuery(whGet, "options=new"),
		Add:      jsonapi.Post(T.DB, T.handleWebhookSave, jsonapi.MaxBody(1<<16)),
		List:     whGet,
		Actions:  webhookRowActions(),
		Row: func(rec map[string]any) sections.IntegrationRow {
			row := sections.IntegrationRow{Key: recStr(rec, "name"), Title: recStr(rec, "label"),
				Detail: joinNonEmpty(recStr(rec, "address"), labelled("checks ", recStr(rec, "checks")), labelled("passes to ", recStr(rec, "feeds")), labelled("last call ", recStr(rec, "last_call")))}
			if p := recStr(rec, "problem"); p != "" {
				row.Status, row.Detail = sections.StatusTrouble, joinNonEmpty(row.Detail, p)
			}
			if recBool(rec, "managed") {
				// Made with a Teams bot, whose row stands for it: deleting
				// it here would leave the bot unreachable.
				row.Key = ""
			}
			return row
		},
	})
}

// handleIntegrations is /api/integrations:
//
//	GET                the person's integrations of every kind, one list
//	GET ?options=new   the Add form's starting values
//	POST               the Add form: handed to the chosen kind's own endpoint
func (T *Extensions) handleIntegrations(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch {
	case r.Method == http.MethodPost:
		T.addIntegration(w, r, user)
	case r.Method != http.MethodGet:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	case r.URL.Query().Get("options") == "new":
		writeJSON(w, integrationDefaults(r, user))
	default:
		writeJSON(w, integrationRows(r, user))
	}
}

// integrationKindsFor are the kinds user sees.
func integrationKindsFor(r *http.Request, user string) []sections.IntegrationKind {
	var out []sections.IntegrationKind
	for _, k := range sections.IntegrationKinds() {
		if k.Allowed == nil || k.Allowed(r, user) {
			out = append(out, k)
		}
	}
	return out
}

// addable is whether user may add a k here, and what to tell them first.
func addable(k sections.IntegrationKind, user string) (bool, string) {
	if k.Fields == nil || k.Add == nil {
		return false, ""
	}
	if k.CanAdd == nil {
		return true, ""
	}
	return k.CanAdd(user)
}

// integrationRows is every kind's list, each row the kind's own fields (its
// row actions read them) and, under the kind's name, the same again, which
// is what keeps a kind's actions to its own rows.
func integrationRows(r *http.Request, user string) []map[string]any {
	rows := []map[string]any{}
	for _, k := range integrationKindsFor(r, user) {
		if k.List == nil || k.Row == nil {
			continue
		}
		res := callKind(k.List, r, http.MethodGet, nil)
		if res.code >= 300 {
			Debug("[integrations] %s list: HTTP %d %s", k.Name, res.code, strings.TrimSpace(res.body.String()))
			continue
		}
		var recs []map[string]any
		if err := json.Unmarshal(res.body.Bytes(), &recs); err != nil {
			Debug("[integrations] %s list: %v", k.Name, err)
			continue
		}
		for _, rec := range recs {
			ir := k.Row(rec)
			if ir.Key == "" {
				continue
			}
			row := map[string]any{}
			for f, v := range rec {
				row[f] = v
			}
			row[k.Name] = rec
			row["_key"] = k.Name + ":" + ir.Key
			row["_kind"] = k.Label
			row["_title"] = ir.Title
			row["_detail"] = ir.Detail
			row["_status"] = ir.Status
			rows = append(rows, row)
		}
	}
	return rows
}

// integrationDefaults are the Add form's starting values: each kind's own,
// under its fields' names.
func integrationDefaults(r *http.Request, user string) map[string]any {
	out := map[string]any{}
	for _, k := range integrationKindsFor(r, user) {
		if ok, _ := addable(k, user); !ok || k.Defaults == nil {
			continue
		}
		res := callKind(k.Defaults, r, http.MethodGet, nil)
		var vals map[string]any
		if res.code >= 300 || json.Unmarshal(res.body.Bytes(), &vals) != nil {
			continue
		}
		for f, v := range vals {
			out[kindField(k.Name, f)] = v
		}
	}
	return out
}

// addIntegration hands the Add form to the chosen kind's own endpoint, with
// only that kind's fields under their own names, and answers as it does.
func (T *Extensions) addIntegration(w http.ResponseWriter, r *http.Request, user string) {
	var form map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&form); err != nil {
		http.Error(w, "the form did not arrive whole", http.StatusBadRequest)
		return
	}
	name, _ := form[pickField].(string)
	var kind *sections.IntegrationKind
	for _, k := range integrationKindsFor(r, user) {
		if k.Name == name {
			kind = &k
			break
		}
	}
	if kind == nil {
		http.Error(w, "choose what you are connecting", http.StatusBadRequest)
		return
	}
	if ok, _ := addable(*kind, user); !ok {
		http.Error(w, "you cannot add a "+strings.ToLower(kind.Label)+" here: ask your administrator", http.StatusForbidden)
		return
	}
	in := map[string]any{}
	for f, v := range form {
		if rest, ok := strings.CutPrefix(f, kind.Name+"__"); ok {
			in[rest] = v
		}
	}
	body, _ := json.Marshal(in)
	res := callKind(kind.Add, r, http.MethodPost, body)
	if ct := res.header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(res.code)
	w.Write(res.body.Bytes())
}

// integrationsSection is the list and its Add.
func (T *Extensions) integrationsSection(r *http.Request, user string) ui.Section {
	kinds := integrationKindsFor(r, user)
	var actions []ui.RowAction
	picker := ui.FormField{Field: pickField, Label: "What are you connecting?", Type: "select", Required: true,
		Options: []ui.SelectOption{{Value: "", Label: "Choose one"}}}
	var fields []ui.FormField
	for _, k := range kinds {
		actions = append(actions, gateActions(k.Name, k.Actions)...)
		ok, note := addable(k, user)
		if !ok {
			continue
		}
		picker.Options = append(picker.Options, ui.SelectOption{Value: k.Name, Label: k.Label + ": " + k.Blurb})
		fields = append(fields, ui.FormField{Field: kindField(k.Name, "_about"), Type: "header", Label: k.Label,
			Help: joinSentences(k.Blurb, note), Detail: k.Help, ShowWhen: pickField + ":" + k.Name})
		fields = append(fields, kindFields(k.Name, k.Fields(user))...)
	}
	table := ui.Table{
		Source:  "api/integrations",
		RowKey:  "_key",
		GroupBy: "_kind",
		Columns: []ui.Col{
			{Field: "_title", Label: "Name", Flex: 1},
			{Field: "_detail", Label: "", Mute: true, Flex: 3},
			{Field: "_status", Label: "", Type: "badge", Badges: []ui.BadgeMapping{
				{Value: sections.StatusWaiting, Label: "Waiting for approval", Color: "warning"},
				{Value: sections.StatusNeedsKey, Label: "Needs its key", Color: "warning"},
				{Value: sections.StatusOff, Label: "Off", Color: "danger"},
				{Value: sections.StatusTrouble, Label: "Needs attention", Color: "danger"},
			}},
		},
		RowActions: actions,
		EmptyText:  "Nothing connected yet. Add one to let your agents reach an API, a server's tools, or a service.",
	}
	children := []ui.Component{table}
	if len(fields) > 0 {
		children = append(children, ui.ModalButton{
			Label:    "Add",
			Title:    "Add an integration",
			Subtitle: "Choose what you are connecting; its form follows.",
			Variant:  "primary",
			Width:    "600px",
			Body: ui.FormPanel{
				Assist:      true,
				Source:      "api/integrations?options=new",
				PostURL:     "api/integrations",
				SubmitLabel: "Add",
				Fields:      append([]ui.FormField{picker}, fields...),
				Invalidate:  []string{"api/integrations"},
			},
		})
	}
	return ui.Section{
		Title:    "Integrations",
		Wide:     true,
		Subtitle: "Everything you connect for yourself: APIs, MCP servers, webhooks, and the services your agents answer in. Their keys and sign-ins are under Connected accounts.",
		Detail: "They are yours: no other user reaches them unless you lend one, and they never appear on the admin page. " +
			"Add asks what you are connecting and then shows that kind's form, with how to set one up behind its info icon. " +
			"Each row's buttons are its kind's own.",
		Body: ui.Stack{Children: children},
	}
}

// kindFields are kind's add fields as the shared Add form has them: named
// under the kind, shown only while it is the one chosen, and their own
// conditions reading the kind's own fields.
func kindFields(kind string, fields []ui.FormField) []ui.FormField {
	out := make([]ui.FormField, 0, len(fields))
	for i, f := range fields {
		switch {
		case f.Field != "":
			f.Field = kindField(kind, f.Field)
		case f.Type == "header":
			// Named, so it shows and hides with the kind's other fields.
			f.Field = kindField(kind, "_h"+strconv.Itoa(i))
		}
		f.ShowWhen = chosenWhen(kind, f.ShowWhen)
		f.HideWhen = kindWhen(kind, f.HideWhen)
		f.LockWhen = kindWhen(kind, f.LockWhen)
		if f.TextField != "" {
			f.TextField = kindField(kind, f.TextField)
		}
		out = append(out, f)
	}
	return out
}

// kindWhen is a show_when expression with every field it reads named under
// kind: "auth:oauth;!token" reads mcp__auth and mcp__token.
func kindWhen(kind, expr string) string {
	if strings.TrimSpace(expr) == "" {
		return expr
	}
	groups := strings.Split(expr, "||")
	for gi, g := range groups {
		clauses := strings.Split(g, ";")
		for ci, c := range clauses {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			neg := ""
			if strings.HasPrefix(c, "!") {
				neg, c = "!", strings.TrimSpace(c[1:])
			}
			clauses[ci] = neg + kindField(kind, c)
		}
		groups[gi] = strings.Join(clauses, ";")
	}
	return strings.Join(groups, "||")
}

// chosenWhen is kindWhen, held to while kind is the one chosen.
func chosenWhen(kind, expr string) string {
	chosen := pickField + ":" + kind
	if strings.TrimSpace(expr) == "" {
		return chosen
	}
	groups := strings.Split(kindWhen(kind, expr), "||")
	for i, g := range groups {
		groups[i] = chosen + ";" + g
	}
	return strings.Join(groups, "||")
}

// gateActions keeps kind's row actions to its own rows: each shows only on a
// row with the kind's fields, and its own conditions read those fields.
func gateActions(kind string, acts []ui.RowAction) []ui.RowAction {
	out := make([]ui.RowAction, 0, len(acts))
	for _, a := range acts {
		if a.OnlyIf != "" {
			a.OnlyIf = kind + "." + a.OnlyIf
		} else {
			a.OnlyIf = kind
		}
		if a.HideIf != "" {
			a.HideIf = kind + "." + a.HideIf
		}
		if a.DisableIf != "" {
			a.DisableIf = kind + "." + a.DisableIf
		}
		out = append(out, a)
	}
	return out
}

// result is what a kind's endpoint answered.
type result struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (c *result) Header() http.Header { return c.header }
func (c *result) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}
func (c *result) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

// callKind calls one of a kind's endpoints as the person on r: the same
// request, with its own method and body and no query of this page's.
func callKind(h http.Handler, r *http.Request, method string, body []byte) *result {
	in := r.Clone(r.Context())
	in.Method = method
	in.URL = &url.URL{Path: r.URL.Path}
	in.Body = io.NopCloser(bytes.NewReader(body))
	in.ContentLength = int64(len(body))
	if body != nil {
		in.Header.Set("Content-Type", "application/json")
	}
	res := &result{header: http.Header{}}
	h.ServeHTTP(res, in)
	if res.code == 0 {
		res.code = http.StatusOK
	}
	return res
}

// withQuery is h asked with query q.
func withQuery(h http.Handler, q string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.RawQuery = q
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func recStr(rec map[string]any, f string) string {
	s, _ := rec[f].(string)
	return strings.TrimSpace(s)
}

func recBool(rec map[string]any, f string) bool {
	b, _ := rec[f].(bool)
	return b
}

// joinNonEmpty joins the parts that say something, with "; ".
func joinNonEmpty(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "; ")
}

// labelled is v after its lead-in, or nothing when there is no v.
func labelled(lead, v string) string {
	if v == "" {
		return ""
	}
	return lead + v
}

// joinSentences joins sentences with a space, leaving out empty ones.
func joinSentences(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, " ")
}
