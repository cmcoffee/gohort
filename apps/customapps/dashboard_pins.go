package customapps

// An app on its owner's dashboard, when they ask for it.
//
// A custom app had no card: it was reached through My Apps and nowhere else.
// The dashboard's Customize page lists these, and an app switched on there
// gets a card of its own on that person's dashboard only.

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// dashboardGroupMyApps puts "My apps" right after the dashboard's own apps on
// the Customize page, ahead of agents.
const dashboardGroupMyApps = 10

// DashboardPinnable offers the viewer's own apps and the apps shared with
// them, each as a card to their app, under one "My apps" heading: the same
// list the My Apps page shows, so the two agree on what an app of theirs is.
// A disabled app is left out: its card would open onto "review, then Enable".
func (T *CustomApps) DashboardPinnable(r *http.Request) []DashboardCard {
	user := AuthCurrentUser(r)
	if T == nil || user == "" {
		return nil
	}
	var out []DashboardCard
	seen := map[string]bool{}
	add := func(s AppSpec, group string) {
		seen[s.Slug] = true
		if s.Disabled {
			return
		}
		desc := strings.TrimSpace(s.Desc)
		if r := []rune(desc); len(r) > 140 {
			desc = string(r[:140]) + "..."
		}
		out = append(out, DashboardCard{Name: s.Name, Desc: desc, Path: "/apps/" + s.Slug, Group: group, GroupOrder: dashboardGroupMyApps})
	}
	for _, s := range listSpecs(user) {
		add(s, "My apps")
	}
	for slug, owner := range ListSharedOwners(T.DB, sharedAppsIndex) {
		if owner == user || seen[slug] {
			continue
		}
		if s, ok := loadSpec(owner, slug); ok && s.Shared {
			add(s, "My apps")
		}
	}
	return out
}
