package bridges

// A person's own Teams bot, under Extensions > Integrations: added from a bot they
// registered in Azure, reached through a webhook made with it, and packaged
// for Teams.

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/governance"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/teamsbots"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/core/webhooks"
)

// teamsBotRow is one bot as its row shows it. Its secret never leaves.
type teamsBotRow struct {
	Service  string `json:"service"`
	Name     string `json:"name"`
	AppID    string `json:"app_id"`
	Endpoint string `json:"endpoint"`
	Status   string `json:"status"`
	LastCall string `json:"last_call"`
	Trouble  bool   `json:"trouble"`
}

// teamsBotForm is the bot form, as posted.
type teamsBotForm struct {
	Name          string   `json:"name"`
	AppID         string   `json:"app_id"`
	TenantID      string   `json:"tenant_id"`
	Secret        string   `json:"secret"`
	Accept        []string `json:"accept"`
	Mention       []string `json:"mention"`
	ReplyInThread bool     `json:"reply_in_thread"`
	Reach         string   `json:"reach"`
	Service       string   `json:"service"`
	Package       string   `json:"package,omitempty"`
	Endpoint      string   `json:"endpoint,omitempty"` // shown, never saved
}

// botEndpoint is where Azure is to send b's activities: its webhook's
// address, or why there is none to give yet.
func botEndpoint(r *http.Request, b teamsbots.Bot) (string, bool) {
	h, ok := webhooks.Owned(b.Owner, b.Webhook)
	if !ok {
		return "its webhook is gone: save the bot again to make a new one", false
	}
	addr, ok := h.Address(webhooks.SiteAddress(r))
	if !ok {
		return "waiting for its peer relay to connect", false
	}
	return addr, true
}

// handleTeamsBots is the person's Teams bots:
//
//	GET                  the list
//	GET ?service=        one, as its edit form reads it
//	GET ?options=reach   how one may be reached
func (T *Bridges) handleTeamsBots(c *jsonapi.Call) (any, error) {
	q := c.R.URL.Query()
	if q.Get("options") == "reach" {
		opts := []ui.SelectOption{{Value: webhooks.ReachDirect, Label: "Here, at this site's own address"}}
		for _, conn := range ListConnectors(RootDB) {
			if conn.Kind == PeerRelayConnectorKind {
				opts = append(opts, ui.SelectOption{Value: webhooks.ReachPeer + conn.Name, Label: "Through the peer relay " + conn.Title()})
			}
		}
		return opts, nil
	}
	if svc := strings.TrimSpace(q.Get("service")); svc != "" {
		b, ok := teamsbots.Load(c.User, svc)
		if !ok {
			return nil, jsonapi.NotFound("you have no Teams bot for " + svc)
		}
		reach := webhooks.ReachDirect
		if h, ok := webhooks.Owned(b.Owner, b.Webhook); ok {
			reach = h.Reach
		}
		endpoint, _ := botEndpoint(c.R, b)
		return teamsBotForm{Endpoint: endpoint, Name: b.Name, AppID: b.AppID, TenantID: b.TenantID, Accept: b.Accept, Mention: b.Mention, ReplyInThread: b.ReplyInThread,
			Reach: reach, Service: b.Service, Package: "/bridges/api/teams-bots/package?service=" + b.Service}, nil
	}
	rows := []teamsBotRow{}
	for _, b := range teamsbots.List(c.User) {
		row := teamsBotRow{Service: b.Service, Name: b.Name, AppID: b.AppID, Status: "answering", LastCall: "none yet"}
		ok := true
		row.Endpoint, ok = botEndpoint(c.R, b)
		switch {
		case b.AwaitingApproval:
			row.Status = "waiting for an administrator's approval"
		case b.AdminDisabled:
			row.Status, row.Trouble = "switched off by an administrator", true
		case !ok:
			row.Trouble = true
		}
		if calls := webhooks.Calls(b.Webhook); len(calls) > 0 {
			row.LastCall = calls[0].At.Format("Jan 2 15:04") + ": " + calls[0].Outcome
			if !calls[0].OK {
				row.Trouble = true
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// handleTeamsBotSave adds a bot, or saves the ?service= one, and makes sure
// its webhook is there, reached as asked.
func (T *Bridges) handleTeamsBotSave(c *jsonapi.Call, f teamsBotForm) (any, error) {
	user := c.User
	svc := strings.TrimSpace(c.R.URL.Query().Get("service"))
	adding := svc == ""
	if adding {
		svc = strings.ToLower(strings.TrimSpace(f.Service))
		if svc == "" {
			svc = teamsbots.DefaultService
		}
	}
	prev, exists := teamsbots.Load(user, svc)
	if adding && exists {
		return nil, jsonapi.BadRequest("you have a Teams bot for " + svc + " already: edit it, or give this one another service name")
	}
	if !adding && !exists {
		return nil, jsonapi.NotFound("you have no Teams bot for " + svc)
	}
	var hold bool
	if adding {
		if by := T.servedElsewhere(user, svc); by != "" {
			return nil, jsonapi.BadRequest(fmt.Sprintf("%s already carries %s for you: one transport a service, or the two would race for its replies", by, svc))
		}
		if _, linked := T.linkFor(user, svc); linked {
			return nil, jsonapi.BadRequest("your linked service " + svc + " already carries it: unlink it first, or give the bot another service name")
		}
		var err error
		if hold, err = governance.Permit(governance.TeamsBots, user); err != nil {
			return nil, jsonapi.Forbidden(err.Error())
		}
	}
	// A new bot with nothing ticked starts where the design says: its own
	// chats, group chats when @mentioned, no team channel yet. An existing
	// bot keeps whatever it has, "none ticked" answering everywhere as before.
	accept, mention := f.Accept, f.Mention
	if adding && len(accept) == 0 && len(mention) == 0 {
		accept, mention = teamsbots.NewBotDefaults()
	}
	b, err := teamsbots.Save(teamsbots.Bot{Owner: user, Service: svc, Name: f.Name, AppID: f.AppID, TenantID: f.TenantID,
		Accept: accept, Mention: mention, ReplyInThread: f.ReplyInThread, Webhook: prev.Webhook}, f.Secret, hold)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	reach := strings.TrimSpace(f.Reach)
	if reach == "" {
		reach = webhooks.ReachDirect
	}
	if h, ok := webhooks.Owned(user, b.Webhook); ok {
		if h.Reach != reach {
			h.Reach = reach
			if _, err := webhooks.Update(user, h); err != nil {
				return nil, jsonapi.BadRequest(err.Error())
			}
		}
	} else {
		h, err := webhooks.Create(webhooks.Webhook{Label: b.Name + " (Teams bot)", Owner: user, Verify: teamsbots.VerifyName,
			Feeds: webhooks.FeedChannel + b.Service, Reach: reach}, "")
		if err != nil {
			return nil, jsonapi.BadRequest("the bot is saved, but its webhook could not be made: " + err.Error())
		}
		teamsbots.SetWebhook(user, b.Service, h.Name)
		b.Webhook = h.Name
	}
	msg := "Saved."
	if adding {
		addr, ok := botEndpoint(c.R, b)
		if ok {
			addr = "set the Azure Bot's messaging endpoint to " + addr
		}
		msg = fmt.Sprintf("Added %s: %s, then upload its app package to Teams (under Edit).", b.Name, addr)
		if b.AwaitingApproval {
			msg += " Teams bots need an administrator's approval here: it answers once one approves it."
		}
	}
	return map[string]string{"message": msg}, nil
}

// handleTeamsBotDelete removes the ?service= bot and its webhook.
func (T *Bridges) handleTeamsBotDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	svc := strings.TrimSpace(c.R.URL.Query().Get("service"))
	b, ok := teamsbots.Load(c.User, svc)
	if !ok {
		return nil, jsonapi.BadRequest("you have no Teams bot for " + svc)
	}
	teamsbots.Remove(c.User, svc)
	if b.Webhook != "" {
		webhooks.Remove(c.User, b.Webhook)
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleTeamsBotPackage sends the ?service= bot's Teams app package, made
// for the address its webhook is reached at.
func (T *Bridges) handleTeamsBotPackage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	b, found := teamsbots.Load(user, strings.TrimSpace(r.URL.Query().Get("service")))
	if !found {
		http.Error(w, "you have no such Teams bot", http.StatusNotFound)
		return
	}
	addr, ok := botEndpoint(r, b)
	if !ok {
		http.Error(w, "the bot has no address to package yet: "+addr, http.StatusConflict)
		return
	}
	zip, err := b.AppPackage(addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+b.Service+`-teams-app.zip"`)
	w.Write(zip)
}

// teamsBotFields are the bot form's fields; the service is named only when
// adding one.
func teamsBotFields(adding bool) []ui.FormField {
	acceptHelp := "None ticked answers in all three."
	if adding {
		acceptHelp = "None ticked here or below: one-to-one chats, and group chats when @mentioned. Team channels stay off until you tick them."
	}
	accept := ui.FormField{Field: "accept", Label: "Answers in", Type: "checklist", Options: []ui.SelectOption{
		{Value: "personal", Label: "One-to-one chats with the bot"},
		{Value: "groupChat", Label: "Group chats it is added to"},
		{Value: "channel", Label: "Team channels"},
	}, Help: acceptHelp}
	mention := ui.FormField{Field: "mention", Label: "Only when @mentioned in", Type: "checklist", Options: []ui.SelectOption{
		{Value: "groupChat", Label: "Group chats"},
		{Value: "channel", Label: "Team channels"},
	}, Help: "Where it is one voice among many, it answers only when someone names it. Teams usually sends a bot only those messages there anyway; this holds if it is given more."}
	var fields []ui.FormField
	if !adding {
		fields = append(fields, ui.FormField{Field: "endpoint", Label: "Messaging endpoint", Type: "readonly", Copy: true,
			Help: "Paste it into the Azure Bot's Configuration, as its Messaging endpoint."})
	}
	fields = append(fields, []ui.FormField{
		{Field: "name", Label: "Name", Required: true, Placeholder: "e.g. Wren", Help: "What Teams shows it as, up to 30 characters."},
		{Field: "app_id", Label: "Microsoft App ID", Required: true, Placeholder: "00000000-0000-0000-0000-000000000000",
			Help: "On the Azure Bot's Configuration page."},
		{Field: "tenant_id", Label: "App tenant ID", Placeholder: "00000000-0000-0000-0000-000000000000",
			Help: "On the same page, for a single-tenant bot (Azure makes new bots single-tenant). Blank for a multi-tenant one."},
	}...)
	secretHelp := "A client secret of the bot's app registration (Manage Password, New client secret). Stored encrypted; used only to sign its replies."
	if !adding {
		secretHelp = "Leave it blank to keep the one it has. " + secretHelp
	}
	fields = append(fields,
		ui.FormField{Field: "secret", Label: "Client secret", Type: "password", Required: adding, Help: secretHelp},
		accept,
		mention,
		ui.FormField{Field: "reply_in_thread", Label: "Reply in the thread", Type: "toggle",
			Help: "In a team channel, reply under the message that asked rather than as a new post."},
		ui.FormField{Field: "reach", Label: "Reached", Type: "select", OptionsSource: "/bridges/api/teams-bots?options=reach",
			Help: "Through a peer's relay when Microsoft cannot reach this site itself: Azure needs a public https address."},
	)
	if adding {
		fields = append(fields, ui.FormField{Field: "service", Label: "Service name", Placeholder: "teams",
			Help: "What channels bind to. Blank is teams."})
	} else {
		fields = append(fields, ui.FormField{Field: "package", Label: "Teams app package", Type: "link",
			Placeholder: "Download the Teams app package",
			Help:        "Upload it in Teams under Apps, Manage your apps, Upload an app. It names this bot and this site."})
	}
	return fields
}

// teamsBotRowActions are a bot's actions on its row under Integrations.
func teamsBotRowActions() []ui.RowAction {
	return []ui.RowAction{
		ui.ModalAction("Edit", ui.FormPanel{Source: "/bridges/api/teams-bots?service={service}", PostURL: "/bridges/api/teams-bots?service={service}",
			SubmitLabel: "Save", Fields: teamsBotFields(false)}),
		{Type: "button", Label: "Delete", Method: "DELETE", PostTo: "/bridges/api/teams-bots?service={service}", Variant: "danger",
			Confirm: "Delete this Teams bot? It stops answering, and its secret and webhook are removed. The Azure registration stays yours to remove."},
	}
}

// teamsBotHelp is how to set one up.
const teamsBotHelp = "How to set one up:\n\n" +
	"1. In the Azure portal, create an Azure Bot (single tenant). On its Configuration page copy the Microsoft App ID and App tenant ID; under Manage Password make a new client secret.\n" +
	"2. Add it here. Its messaging endpoint is on its row, and under Edit with a Copy button: paste it into the Azure Bot's Configuration as the Messaging endpoint.\n" +
	"3. In the Azure Bot's Channels, add Microsoft Teams.\n" +
	"4. Under Edit, download its Teams app package and upload it in Teams (Apps, Manage your apps, Upload an app). Your organisation may need to allow custom apps.\n" +
	"5. On the Bridges page, bind a channel to teams (or its service name) with the agent that answers.\n\n" +
	"Microsoft signs every message it sends the bot; one not signed for this bot's app id is refused, and its replies go only to Microsoft's Teams service."
