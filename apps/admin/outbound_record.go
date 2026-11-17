package admin

// The Outbound record (core/outbound): what agents sent, or tried to send,
// out of the deployment that someone should see. A search or source query
// naming private work, a send to a site nobody trusted, an HTTPS connection
// a script's proxy refused. Not the ordinary traffic: a query that was fine,
// or a write to a trusted site, is not an event.
//
// Beside "Sites agents may send to" on the Oversight tab, because it is the
// trail those decisions leave, and where a misconfigured agent, or one made
// to leak, shows itself as a run of rows.

import (
	"net/http"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/outbound"
	"github.com/cmcoffee/gohort/core/ui"
)

const (
	outboundEventsSource   = "api/outbound-events"
	outboundSettingsSource = "api/outbound-settings"
	// outboundShown is how many of the newest events the page lists.
	outboundShown = 300
)

func (a *AdminApp) registerOutboundRecordRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/"+outboundEventsSource, jsonapi.Get(a.db, a.handleOutboundEvents, a.gate()))
	sub.HandleFunc("/"+outboundEventsSource+"/clear", jsonapi.Post(a.db, a.handleOutboundClear, a.gate()))
	sub.HandleFunc("/"+outboundSettingsSource, jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleOutboundSettings, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleOutboundSettingsSave, a.gate()),
	}.Serve)
}

// outboundEventRow is one event as the page lists it.
type outboundEventRow struct {
	ID     string `json:"id"`
	At     string `json:"at"`
	Action string `json:"action"`
	Kind   string `json:"kind"`
	Dest   string `json:"dest"`
	Who    string `json:"who"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// outboundKindWords are the kinds as the page says them.
var outboundKindWords = map[string]string{
	outbound.KindSearch: "Web search",
	outbound.KindSource: "Source query",
	outbound.KindWrite:  "Send",
	outbound.KindTunnel: "Connection",
}

func (a *AdminApp) handleOutboundEvents(c *jsonapi.Call) ([]outboundEventRow, error) {
	out := []outboundEventRow{}
	for _, e := range outbound.Events(outboundShown, nil) {
		kind := outboundKindWords[e.Kind]
		if kind == "" {
			kind = e.Kind
		}
		out = append(out, outboundEventRow{
			ID: e.ID, At: e.At.Format(time.RFC3339), Action: e.Action, Kind: kind,
			Dest: e.Dest, Who: e.Who(), Reason: e.Reason, Detail: e.Detail,
		})
	}
	return out, nil
}

func (a *AdminApp) handleOutboundClear(c *jsonapi.Call, _ struct{}) (map[string]string, error) {
	outbound.Clear()
	adminChange(c.R, activity.Settings, "cleared the Outbound record")
	return map[string]string{"message": "Cleared."}, nil
}

// outboundSettingsForm is the page's setting: what the check on outgoing
// queries does.
type outboundSettingsForm struct {
	QueryCheck string `json:"query_check"`
}

func (a *AdminApp) handleOutboundSettings(c *jsonapi.Call) (outboundSettingsForm, error) {
	return outboundSettingsForm{QueryCheck: outbound.QueryMode()}, nil
}

func (a *AdminApp) handleOutboundSettingsSave(c *jsonapi.Call, f outboundSettingsForm) (outboundSettingsForm, error) {
	m := strings.TrimSpace(f.QueryCheck)
	switch m {
	case outbound.ModeOff, outbound.ModeWatch, outbound.ModeRefuse:
	default:
		return f, jsonapi.BadRequest("choose Watch, Refuse or Off")
	}
	outbound.SetQueryMode(m)
	adminChange(c.R, activity.Settings, "set the check on outgoing queries to %s", m)
	return outboundSettingsForm{QueryCheck: outbound.QueryMode()}, nil
}

// outboundRecordSection is the record and its one setting, on the Oversight
// tab after the sites list.
func outboundRecordSection() ui.Section {
	return ui.Section{
		Title:    "Outbound record",
		Subtitle: "What agents sent, or tried to send, out of the deployment that someone should see. Ordinary traffic is not listed.",
		Detail: "Listed: a web search or source query that names a project's private terms, carries what looks like a ticket ID, or names an internal host or address; a send to a site nobody has trusted, refused, asked about, or held for its owner; an HTTPS connection a script's proxy refused.\n\n" +
			"Private terms come from the work: Anvil names a project's name, its repository and its hosts. A query to a source whose address is inside this network is not leaving, and is not checked.\n\n" +
			"Watch lets a flagged query go and lists it, so the check can be judged on real searches before it stops any. Refuse stops it and tells the agent to search for the general concept instead. The newest 5,000 events are kept.",
		Body: ui.Stack{Children: []ui.Component{
			ui.FormPanel{
				Source: outboundSettingsSource,
				Method: "POST",
				Fields: []ui.FormField{{
					Field: "query_check", Type: "select", Label: "Queries naming private work",
					Options: []ui.SelectOption{
						{Value: outbound.ModeWatch, Label: "Watch: send, and list them here"},
						{Value: outbound.ModeRefuse, Label: "Refuse: stop them, and list them here"},
						{Value: outbound.ModeOff, Label: "Off: no check"},
					},
					Help: "What happens to a web search or source query that names private work, a ticket ID or an internal host.",
				}},
			},
			ui.Table{
				Source:    outboundEventsSource,
				RowKey:    "id",
				EmptyText: "Nothing yet: no agent has sent anything someone should see.",
				Columns: []ui.Col{
					{Field: "at", Label: "When", Format: "reltime", Flex: 0},
					{Field: "action", Label: "Outcome", Type: "badge", Flex: 0, Badges: []ui.BadgeMapping{
						{Value: outbound.ActionWatched, Label: "Watched", Color: "warning"},
						{Value: outbound.ActionRefused, Label: "Refused", Color: "danger"},
						{Value: outbound.ActionDeclined, Label: "Declined", Color: "mute"},
						{Value: outbound.ActionAllowed, Label: "Allowed", Color: "success"},
					}},
					{Field: "kind", Label: "What", Flex: 0},
					{Field: "dest", Label: "To", Flex: 1},
					{Field: "who", Label: "For", Flex: 1, Mute: true},
					{Field: "reason", Label: "Why", Flex: 2},
					{Field: "detail", Label: "Sent", Flex: 2, Mute: true},
				},
			},
			ui.Button{
				Label: "Clear the record", URL: outboundEventsSource + "/clear", Variant: "danger",
				Confirm:    "Clear every event in the Outbound record? This cannot be undone.",
				Invalidate: []string{outboundEventsSource},
			},
		}},
	}
}
