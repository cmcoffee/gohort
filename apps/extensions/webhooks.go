package extensions

// A person's own webhooks (core/webhooks): an address a service posts to,
// checked, read, and passed on to one of their watches or, as a message,
// to one of their linked services' channels.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/core/webhooks"
)

// webhookRow is one webhook as the list shows it. Its secret never leaves.
type webhookRow struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Address  string `json:"address"`
	Checks   string `json:"checks"`
	Feeds    string `json:"feeds"`
	LastCall string `json:"last_call"`
	Problem  string `json:"problem,omitempty"`
	Open     bool   `json:"open"`
	Managed  bool   `json:"managed"` // made and kept by what it feeds (a Teams bot)
	Signed   bool   `json:"signed"`
}

// checksText is how webhook h checks its caller, in words.
func checksText(h webhooks.Webhook) string {
	switch h.Verify {
	case webhooks.VerifyToken:
		return "a token in " + h.Header
	case webhooks.VerifyHMAC:
		return fmt.Sprintf("an HMAC %s signature in %s", strings.ToUpper(strings.Replace(h.Algorithm, "sha", "sha-", 1)), h.Header)
	case webhooks.VerifySlack:
		return "Slack's request signing"
	}
	if webhooks.IsChannelCheck(h.Verify) {
		return "the " + h.Verify + " check of what it feeds"
	}
	return "nothing: its address is the secret"
}

func webhookRowOf(r *http.Request, h webhooks.Webhook) webhookRow {
	row := webhookRow{Name: h.Name, Label: h.Label, Checks: checksText(h), Feeds: "the watch " + h.Watch(),
		Open: h.Verify == webhooks.VerifyNone, Signed: h.Verify != webhooks.VerifyNone && !webhooks.IsChannelCheck(h.Verify),
		Managed: webhooks.IsChannelCheck(h.Verify), LastCall: "none yet"}
	if addr, ok := h.Address(webhooks.SiteAddress(r)); ok {
		row.Address = addr
	} else {
		row.Address = "waiting for its peer relay to connect"
	}
	if calls := webhooks.Calls(h.Name); len(calls) > 0 {
		row.LastCall = calls[0].At.Format("Jan 2 15:04") + ": " + calls[0].Outcome
	}
	if svc := h.Channel(); svc != "" {
		row.Feeds = "the channel " + svc
		if !linkedService(h.Owner, svc) {
			row.Problem = svc + " is no longer one of your linked services: link it under Bridges, or choose another under Edit."
		}
	} else if m, ok := GetEventMonitor(RootDB, h.Owner, h.Watch()); !ok || m.Kind != EventKindWebhook {
		row.Problem = "Its watch " + h.Watch() + " is gone: choose another under Edit."
	} else if m.Paused {
		row.Problem = "Its watch is paused: posts are accepted and not passed on."
	}
	return row
}

// linkedService reports whether svc is one of owner's linked services.
func linkedService(owner, svc string) bool {
	if webhooks.LinkedServices == nil {
		return false
	}
	for _, s := range webhooks.LinkedServices(owner) {
		if s == svc {
			return true
		}
	}
	return false
}

// webhookForm is the webhook form, as posted.
type webhookForm struct {
	Label     string `json:"label"`
	Verify    string `json:"verify"`
	Secret    string `json:"secret"`
	Header    string `json:"header"`
	Algorithm string `json:"algorithm"`
	Prefix    string `json:"prefix"`
	Text      string `json:"text"`
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	From      string `json:"from"`
	Chat      string `json:"chat"`
	Kinds     string `json:"kinds"`
	Feeds     string `json:"feeds"`
	Reach     string `json:"reach"`
	Address   string `json:"address,omitempty"` // shown, never saved
}

func (f webhookForm) webhook(owner string) webhooks.Webhook {
	var kinds []string
	for _, k := range strings.FieldsFunc(f.Kinds, func(r rune) bool { return r == ',' || r == '\n' }) {
		kinds = append(kinds, strings.TrimSpace(k))
	}
	return webhooks.Webhook{Label: f.Label, Owner: owner, Verify: f.Verify, Header: f.Header, Algorithm: f.Algorithm,
		Prefix: f.Prefix, Map: webhooks.Map{Text: f.Text, Kind: f.Kind, ID: f.ID, From: f.From, Chat: f.Chat},
		Kinds: kinds, Feeds: f.Feeds, Reach: f.Reach}
}

// handleWebhooks is the person's webhooks:
//
//	GET                    the list
//	GET ?name=             one, as its edit form reads it
//	GET ?options=feeds     what a webhook may pass on to: their watches
//	                       woken by a post, and their linked services
//	GET ?options=reach     how one may be reached
//	GET ?options=new       a new form's starting values, a fresh secret among them
//	GET ?calls=            one's recent calls
func (T *Extensions) handleWebhooks(c *jsonapi.Call) (any, error) {
	user, q := c.User, c.R.URL.Query()
	switch q.Get("options") {
	case "feeds":
		opts := []ui.SelectOption{}
		for _, m := range ListEventMonitors(RootDB, user) {
			if m.Kind == EventKindWebhook {
				opts = append(opts, ui.SelectOption{Value: webhooks.FeedWatch + m.Name, Label: "The watch " + m.Name})
			}
		}
		if webhooks.LinkedServices != nil {
			for _, svc := range webhooks.LinkedServices(user) {
				opts = append(opts, ui.SelectOption{Value: webhooks.FeedChannel + svc, Label: "The channel " + svc + " (a linked service)"})
			}
		}
		return opts, nil
	case "reach":
		opts := []ui.SelectOption{{Value: webhooks.ReachDirect, Label: "Here, at this site's own address"}}
		for _, conn := range ListConnectors(RootDB) {
			if conn.Kind == PeerRelayConnectorKind {
				opts = append(opts, ui.SelectOption{Value: webhooks.ReachPeer + conn.Name, Label: "Through the peer relay " + conn.Title()})
			}
		}
		return opts, nil
	case "new":
		return map[string]string{"verify": webhooks.VerifyHMAC, "algorithm": "sha256", "reach": webhooks.ReachDirect,
			"secret": webhooks.NewSecretValue()}, nil
	}
	if name := strings.TrimSpace(q.Get("calls")); name != "" {
		if _, ok := webhooks.Owned(user, name); !ok {
			return nil, jsonapi.NotFound("you have no webhook " + name)
		}
		out := []map[string]string{}
		for _, call := range webhooks.Calls(name) {
			out = append(out, map[string]string{"at": call.At.Format(time.DateTime), "outcome": call.Outcome, "kind": call.Kind})
		}
		return out, nil
	}
	if name := strings.TrimSpace(q.Get("name")); name != "" {
		h, ok := webhooks.Owned(user, name)
		if !ok {
			return nil, jsonapi.NotFound("you have no webhook " + name)
		}
		addr, ok := h.Address(webhooks.SiteAddress(c.R))
		if !ok {
			addr = "Shown once its peer relay connects."
		}
		return webhookForm{Address: addr, Label: h.Label, Verify: h.Verify, Header: h.Header, Algorithm: h.Algorithm, Prefix: h.Prefix,
			Text: h.Map.Text, Kind: h.Map.Kind, ID: h.Map.ID, From: h.Map.From, Chat: h.Map.Chat, Kinds: strings.Join(h.Kinds, ", "),
			Feeds: h.Feeds, Reach: h.Reach}, nil
	}
	rows := []webhookRow{}
	for _, h := range webhooks.List(user) {
		rows = append(rows, webhookRowOf(c.R, h))
	}
	return rows, nil
}

// handleWebhookSave is /api/webhooks' POST: ?action=secret gives one a new
// secret, ?name= saves one, neither adds one.
func (T *Extensions) handleWebhookSave(c *jsonapi.Call, f webhookForm) (any, error) {
	user, q := c.User, c.R.URL.Query()
	name := strings.TrimSpace(q.Get("name"))
	if q.Get("action") == "secret" {
		if err := webhooks.NewSecret(user, name, f.Secret); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		return map[string]string{"message": "Saved. Give the service the new secret, as the old one no longer works."}, nil
	}
	if name != "" {
		h := f.webhook(user)
		h.Name = name
		if _, err := webhooks.Update(user, h); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		return map[string]string{"message": "Saved."}, nil
	}
	h, err := webhooks.Create(f.webhook(user), f.Secret)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	addr, ok := h.Address(webhooks.SiteAddress(c.R))
	if !ok {
		addr = "shown on its row once its peer relay connects"
	}
	return map[string]string{"message": fmt.Sprintf("Added %s. Give the service its address: %s", h.Label, addr)}, nil
}

// handleWebhookDelete removes the ?name= webhook.
func (T *Extensions) handleWebhookDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	if err := webhooks.Remove(c.User, strings.TrimSpace(c.R.URL.Query().Get("name"))); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// webhookFields are the form's fields: how it checks only when adding one.
// Editing, the record it loads carries how it checks, which the header
// fields show by.
func webhookFields(adding bool) []ui.FormField {
	fields := []ui.FormField{}
	if !adding {
		fields = append(fields, ui.FormField{Field: "address", Label: "Address to give the service", Type: "readonly", Copy: true,
			Help: "Paste it where the service asks for its webhook URL (a payload URL, an endpoint, a request URL)."})
	}
	fields = append(fields, ui.FormField{Field: "label", Label: "Name", Required: true, Placeholder: "e.g. GitHub pushes",
		Help: "What you call it. Its address is made from it."})
	if adding {
		fields = append(fields,
			ui.FormField{Field: "verify", Label: "Checks the caller by", Type: "select", Options: []ui.SelectOption{
				{Value: webhooks.VerifyHMAC, Label: "A signature over what it posts (GitHub, Stripe, Shopify)"},
				{Value: webhooks.VerifyToken, Label: "A shared token in a header"},
				{Value: webhooks.VerifySlack, Label: "Slack's request signing (Slack's Events API)"},
				{Value: webhooks.VerifyNone, Label: "Nothing: only for a tool on your own network"},
			}, Help: "To check another way later, add another webhook: its address and secret go with the check."},
			ui.FormField{Field: "secret", Label: "Secret", ShowWhen: "verify:" + webhooks.VerifyToken + "|" + webhooks.VerifyHMAC + "|" + webhooks.VerifySlack,
				Help: "Copy this to the service now: it is not shown again. A service that gives you its own signing secret (Stripe, Slack): paste that here instead."},
		)
	}
	fields = append(fields,
		ui.FormField{Field: "header", Label: "Header", ShowWhen: "verify:" + webhooks.VerifyToken + "|" + webhooks.VerifyHMAC,
			Placeholder: "e.g. X-Hub-Signature-256", Help: "The header the service sends the token or signature in."},
		ui.FormField{Field: "algorithm", Label: "Signature", Type: "select", ShowWhen: "verify:" + webhooks.VerifyHMAC,
			Options: []ui.SelectOption{{Value: "sha256", Label: "HMAC SHA-256"}, {Value: "sha1", Label: "HMAC SHA-1"}}},
		ui.FormField{Field: "prefix", Label: "Before the signature", ShowWhen: "verify:" + webhooks.VerifyHMAC,
			Placeholder: "e.g. sha256=", Help: "What the service writes before the signature itself, if anything. GitHub writes sha256=."},
		ui.FormField{Field: "", Label: "Reading the event", Type: "header"},
		ui.FormField{Field: "text", Label: "Text", Placeholder: "e.g. head_commit.message",
			Help: "Where what your agent reads is, as a dotted path into what is posted (arrays by number). Blank passes on what was posted.",
			Detail: "A path reads into the JSON the service posts: pull_request.title, data.object.amount, alerts.0.annotations.summary. " +
				"Any of these may instead be header:<Name> to read a header, as header:X-GitHub-Event."},
		ui.FormField{Field: "kind", Label: "Kind", Placeholder: "e.g. header:X-GitHub-Event",
			Help: "What sort of event it is, if the service says."},
		ui.FormField{Field: "kinds", Label: "Pass on only these kinds", Placeholder: "e.g. push, release",
			Help: "Comma-separated. Blank passes on every kind. Needs Kind."},
		ui.FormField{Field: "id", Label: "Event id", Placeholder: "e.g. header:X-GitHub-Delivery",
			Help: "The event's own id, so a service sending one again is passed on once."},
		ui.FormField{Field: "from", Label: "From", Placeholder: "e.g. sender.login",
			Help: "Who or what it is from, if the service says. Passed to a channel, a post from no one (a bot's) is passed over."},
		ui.FormField{Field: "chat", Label: "Chat", Placeholder: "e.g. event.channel",
			Help: "The conversation a message is in: needed to pass it to a channel."},
		ui.FormField{Field: "", Label: "Passing it on", Type: "header"},
		ui.FormField{Field: "feeds", Label: "Passes it to", Type: "select", Required: true, OptionsSource: "api/webhooks?options=feeds",
			Help: "One of your watches that a post wakes, or the channel of one of your linked services. Ask an agent to watch for something posted, or link a service under Bridges, and it is listed here.",
			Detail: "A watch woken by a post wakes its agent with the event, in the thread it was set up in. It keeps its own limit on how often it is woken, whichever way it is posted to, and pausing it pauses this too. " +
				"Passed to a channel, each event is a message in its chat, answered by the agent bound to that service, through the linked service's connector; the link then fetches nothing itself."},
		ui.FormField{Field: "reach", Label: "Reached", Type: "select", OptionsSource: "api/webhooks?options=reach",
			Help: "Through a peer's relay when the service cannot reach this site itself."},
	)
	return fields
}

// webhooksSection is the Extensions section for them.
func (T *Extensions) webhooksSection(user string) ui.Section {
	table := ui.Table{
		Source: "api/webhooks",
		RowKey: "name",
		Columns: []ui.Col{
			{Field: "label", Label: "Name", Flex: 1},
			{Field: "address", Label: "Address", Mute: true, Flex: 2},
			{Field: "checks", Label: "Checks", Mute: true, Flex: 1},
			{Field: "feeds", Label: "Passes to", Mute: true},
			{Field: "open", Label: "", Type: "badge", Badges: []ui.BadgeMapping{
				{Value: true, Label: "Checks nothing", Color: "warning"},
			}},
			{Field: "last_call", Label: "Last call", Mute: true, Flex: 2},
			{Field: "problem", Label: "", Mute: true, Flex: 2},
		},
		RowActions: []ui.RowAction{
			ui.ModalActionIf("Edit", "", "managed", ui.FormPanel{Source: "api/webhooks?name={name}", PostURL: "api/webhooks?name={name}",
				SubmitLabel: "Save", Fields: webhookFields(false)}),
			ui.Expand("Calls", ui.Table{Source: "api/webhooks?calls={name}", Columns: []ui.Col{
				{Field: "at", Label: "When", Mute: true}, {Field: "kind", Label: "Kind", Mute: true},
				{Field: "outcome", Label: "What happened", Flex: 2}}, EmptyText: "Nothing has posted to it yet."}),
			ui.ModalActionIf("New secret", "signed", "", ui.FormPanel{Source: "api/webhooks?options=new",
				PostURL: "api/webhooks?action=secret&name={name}", SubmitLabel: "Use this secret", Fields: []ui.FormField{
					{Field: "secret", Label: "Secret", Required: true, Help: "Copy this to the service first: the old one stops working when you save, and this is not shown again."},
				}}),
			{Type: "button", Label: "Delete", Method: "DELETE", PostTo: "api/webhooks?name={name}", Variant: "danger",
				Confirm: "Delete this webhook? Posts to its address are refused from now on."},
		},
		EmptyText: "No webhooks yet.",
	}
	add := ui.ModalButton{
		Label:    "Add webhook",
		Title:    "Add a webhook",
		Subtitle: "An address a service posts to, which wakes one of your watches.",
		Variant:  "primary",
		Width:    "600px",
		Body: ui.FormPanel{
			Source:      "api/webhooks?options=new",
			PostURL:     "api/webhooks",
			SubmitLabel: "Add webhook",
			Fields:      webhookFields(true),
		},
	}
	return ui.Section{
		Title:    "Webhooks",
		Subtitle: "Addresses a service posts to, such as GitHub, Stripe or Slack: each checks who is posting and passes the event to one of your watches, or to a linked service's channel.",
		Detail: "How to set one up: ask an agent to watch for something posted to it, which makes a watch; add a webhook here that wakes that watch, " +
			"with the check the service offers and where its event's text is; then give the service the webhook's address and secret. " +
			"Each call is listed under Calls: refused ones say why. A post that is not checked, comes too often, or is an event already passed on is not passed on. " +
			"A webhook that checks nothing has a long random address, since that address is then its only secret: use it only for a tool on your own network.\n\n" +
			"For Slack, so its messages come at once instead of on each poll: link Slack (messages) under Bridges first. Then add a webhook checked by Slack's request signing, " +
			"pasting the signing secret from your Slack app's Basic Information, reading text event.text, chat event.channel, from event.user, id event.ts, kind event.type, passing on only message, " +
			"and passing to the channel slack. Give its address to the Slack app under Event Subscriptions: Slack checks it at once, and this answers. Subscribe to message.channels.",
		Body: ui.Stack{Children: []ui.Component{table, add}},
	}
}
