package bridges

// The Teams bots and linked services a person adds, listed and added under
// Extensions > Integrations with everything else they connect.

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/governance"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/sections"
	"github.com/cmcoffee/gohort/core/ui"
)

// registerIntegrations registers this app's kinds, for the people who may
// use it.
func (T *Bridges) registerIntegrations(prefix string) {
	allowed := func(r *http.Request, _ string) bool { return UserHasAppAccess(r, prefix) }
	sections.RegisterIntegrationKind(sections.IntegrationKind{
		Name:    "teams_bot",
		Label:   "Teams bot",
		Order:   40,
		Blurb:   "A Microsoft Teams bot of your own, which answers in Teams with your agents under its own name.",
		Help:    teamsBotHelp,
		Allowed: allowed,
		CanAdd:  func(user string) (bool, string) { return governance.Offer(governance.TeamsBots, user) },
		Fields:  func(string) []ui.FormField { return teamsBotFields(true) },
		Add:     jsonapi.Post(T.DB, T.handleTeamsBotSave, jsonapi.MaxBody(1<<14)),
		List:    jsonapi.Get(T.DB, T.handleTeamsBots),
		Actions: teamsBotRowActions(),
		Row: func(rec map[string]any) sections.IntegrationRow {
			row := sections.IntegrationRow{Key: recStr(rec, "service"), Title: recStr(rec, "name"),
				Detail: joinParts("channel "+recStr(rec, "service"), recStr(rec, "endpoint"), "last call "+recStr(rec, "last_call"))}
			status := recStr(rec, "status")
			switch {
			case strings.HasPrefix(status, "waiting"):
				row.Status = sections.StatusWaiting
			case strings.HasPrefix(status, "switched off"):
				row.Status = sections.StatusOff
			case recBool(rec, "trouble"):
				row.Status = sections.StatusTrouble
			}
			return row
		},
	})
	sections.RegisterIntegrationKind(sections.IntegrationKind{
		Name:    "link",
		Label:   "Linked service",
		Order:   50,
		Blurb:   "A messaging service such as Slack or Discord, reached through a connector that fetches and sends its messages.",
		Help:    linkHelp,
		Allowed: allowed,
		Fields:  func(string) []ui.FormField { return linkFields() },
		Add:     jsonapi.Post(T.DB, T.handleLinkSave, jsonapi.MaxBody(1<<14)),
		List:    jsonapi.Get(T.DB, T.handleLinks),
		Actions: linkRowActions(),
		Row: func(rec map[string]any) sections.IntegrationRow {
			row := sections.IntegrationRow{Key: recStr(rec, "service"), Title: recStr(rec, "service"),
				Detail: joinParts("through "+recStr(rec, "connector"), "chats "+recStr(rec, "chats"), "every "+recStr(rec, "every"), "last poll "+recStr(rec, "last"))}
			if recBool(rec, "trouble") {
				row.Status = sections.StatusTrouble
			}
			return row
		},
	})
}

// linkHelp is how to set one up.
const linkHelp = "How to set one up: an administrator adds the service's connector from its template (Slack messages, Discord messages) with its token. " +
	"Link it here under a service name, with the chats to read where the service fetches one at a time. " +
	"Then, on the Bridges page, bind a channel to that service name, and its agent answers there.\n\n" +
	"gohort asks the service for new messages on each poll, as you, so a connector limited to named users is linked only by one of them. " +
	"The first poll of each chat passes over what is already there. A reply that cannot be sent is kept and tried again on the next poll. " +
	"One link a service: a service another bridge already carries for you cannot be linked too."

// linkFields are the form a service is linked with.
func linkFields() []ui.FormField {
	return []ui.FormField{
		{Field: "connector", Label: "Connector", Type: "select", Required: true, OptionsSource: "/bridges/api/links?options=connectors",
			Help: "A connector that both fetches and sends messages, which you may use."},
		{Field: "service", Label: "Service name", Required: true, Placeholder: "e.g. slack",
			Help: "What channels bind to. Use slack or discord for those services."},
		{Field: "chats", Label: "Chats", Type: "tags", Placeholder: "e.g. C0123456789",
			Help: "The conversations to read, one each. Slack needs them (its channel ids); Discord reads the channel its connector is set to when blank."},
		{Field: "interval_secs", Label: "Poll every (seconds)", Type: "number", Min: linkMinSecs, Placeholder: "30"},
	}
}

// linkRowActions are a link's actions on its row.
func linkRowActions() []ui.RowAction {
	return []ui.RowAction{
		{Type: "button", Label: "Unlink", Method: "DELETE", PostTo: "/bridges/api/links?service={service}", Variant: "danger",
			Confirm: "Unlink this service? Its messages stop coming in, and replies waiting to go out stay queued.", Compact: true},
	}
}

func recStr(rec map[string]any, f string) string {
	s, _ := rec[f].(string)
	return strings.TrimSpace(s)
}

func recBool(rec map[string]any, f string) bool {
	b, _ := rec[f].(bool)
	return b
}

// joinParts joins the parts with "; ".
func joinParts(parts ...string) string { return strings.Join(parts, "; ") }
