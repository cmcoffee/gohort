package admin

// Sites for every user: the administrator's Always allow and Never for
// sending to a site (core/authorizations.go, AdminSites). A Never
// here refuses for everyone, over an owner's Always allow and the built-in
// package registries; an Always allow here gives way to an owner's own Never.
// What it governs is what each owner's list governs: a POST, PUT, PATCH or
// DELETE to a site no API credential covers, and an HTTPS connection through
// a script's proxy. Reads are not checked.

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

const outboundSitesSource = "api/outbound-sites"

// outboundSiteRow is one site on the admin list.
type outboundSiteRow struct {
	ID     string `json:"_id"`
	Host   string `json:"host"`
	Policy string `json:"_policy"`
}

// outboundSiteIn is the add form, a row's segmented control ({_policy}), or
// both.
type outboundSiteIn struct {
	Site   string `json:"site"`
	Value  string `json:"value"`
	Policy string `json:"_policy"`
}

func (a *AdminApp) registerOutboundSiteRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/"+outboundSitesSource, jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleOutboundSites, a.gate()),
		"POST":   jsonapi.Post(a.db, a.handleOutboundSiteAdd, a.gate()),
		"DELETE": jsonapi.Delete(a.db, a.handleOutboundSiteRemove, a.gate()),
	}.Serve)
	sub.HandleFunc("/"+outboundSitesSource+"/policy", jsonapi.Post(a.db, a.handleOutboundSitePolicy, a.gate()))
}

func (a *AdminApp) handleOutboundSites(c *jsonapi.Call) ([]outboundSiteRow, error) {
	out := []outboundSiteRow{}
	for _, e := range AdminSites.List(RootDB) {
		out = append(out, outboundSiteRow{ID: e.Target, Host: e.Target, Policy: e.Policy})
	}
	return out, nil
}

// adminSitePolicy is a posted choice as stored: allow or block, or why not.
func adminSitePolicy(v string) (string, error) {
	switch v = strings.TrimSpace(v); v {
	case PolicyAllow, PolicyBlock:
		return v, nil
	}
	return "", jsonapi.BadRequest("choose Always allow or Never")
}

func (a *AdminApp) handleOutboundSiteAdd(c *jsonapi.Call, in outboundSiteIn) (map[string]string, error) {
	host, why := SiteFromInput(in.Site)
	if why != "" {
		return nil, jsonapi.BadRequest(why)
	}
	policy, err := adminSitePolicy(in.Value)
	if err != nil {
		return nil, err
	}
	AdminSites.Set(RootDB, host, policy)
	Log("[outbound] %s set %s to %s for every user", c.User, host, policy)
	return map[string]string{"host": host, "policy": policy}, nil
}

func (a *AdminApp) handleOutboundSitePolicy(c *jsonapi.Call, in outboundSiteIn) (map[string]string, error) {
	host := strings.TrimSpace(c.R.URL.Query().Get("host"))
	if AdminSites.Policy(RootDB, host) == "" {
		return nil, jsonapi.NotFound("that site is not on the list")
	}
	value := strings.TrimSpace(c.R.URL.Query().Get("value"))
	if value == "" {
		value = in.Policy
	}
	policy, err := adminSitePolicy(value)
	if err != nil {
		return nil, err
	}
	AdminSites.Set(RootDB, host, policy)
	Log("[outbound] %s set %s to %s for every user", c.User, host, policy)
	return map[string]string{"host": host, "policy": policy}, nil
}

func (a *AdminApp) handleOutboundSiteRemove(c *jsonapi.Call, _ struct{}) (map[string]string, error) {
	host := strings.TrimSpace(c.R.URL.Query().Get("host"))
	if AdminSites.Policy(RootDB, host) == "" {
		return nil, jsonapi.NotFound("that site is not on the list")
	}
	AdminSites.Set(RootDB, host, "")
	Log("[outbound] %s removed %s from the list for every user", c.User, host)
	return map[string]string{"host": host}, nil
}

// adminSiteChoices are the two decisions an administrator makes for a site;
// "Ask first" is what no decision means, so it is Remove.
var adminSiteChoices = []ui.SelectOption{
	{Value: PolicyAllow, Label: "Always allow"},
	{Value: PolicyBlock, Label: "Never"},
}

// outboundSitesSection is the list and its add form, on the Oversight tab
// beside the deployment's other rules for everyone (the outward
// capabilities above it).
func outboundSitesSection() ui.Section {
	return ui.Section{
		Title:    "Sites agents may send to",
		Subtitle: "For every user: Never refuses a site for everyone, Always allow trusts it for everyone. A site not listed is each owner's to decide.",
		Detail: "Covers what each owner's own list covers: a POST, PUT, PATCH or DELETE to a site none of the user's API credentials cover, from fetch_url, an api tool or a script, and an HTTPS connection through a script's proxy (pip, npm, Python requests), where gohort cannot see whether it reads or uploads. Reads are not checked.\n\n" +
			"Never here wins over everything: an owner's Always allow and the built-in package registries. Always allow here gives way to an owner who set the site to Never for their own agents.\n\n" +
			"Owners keep their own lists on each agent's Security page (Network).",
		Body: ui.Stack{Children: []ui.Component{
			ui.Table{
				Source:    outboundSitesSource,
				RowKey:    "_id",
				EmptyText: "No sites decided for everyone. Each owner decides for their own agents.",
				Columns:   []ui.Col{{Field: "host", Label: "Site"}},
				RowActions: []ui.RowAction{{
					Type: "segmented", Field: "_policy", PostTo: outboundSitesSource + "/policy?host={host}",
					Options: adminSiteChoices,
				}, {
					Type: "button", Label: "Remove", Variant: "danger", Method: "DELETE",
					PostTo:  outboundSitesSource + "?host={host}",
					Confirm: "Take this site off the list? Each owner's own decision applies again.",
				}},
			},
			ui.FormPanel{
				PostURL:     outboundSitesSource,
				Method:      "POST",
				SubmitLabel: "Add",
				Fields: []ui.FormField{
					{Field: "site", Type: "text", Label: "Site",
						Help: "The site's address. A full link works too; only the site part is kept, and it covers that exact site, not others under the same domain."},
					{Field: "value", Type: "select", Label: "And then", Options: adminSiteChoices,
						Help: "Never refuses sending to it for every user. Always allow sends without asking, for every user who has not blocked it themselves."},
				},
			},
		}},
	}
}
