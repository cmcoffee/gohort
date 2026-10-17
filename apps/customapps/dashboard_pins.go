package customapps

// An app on its owner's dashboard, when they ask for it.
//
// A custom app had no card: it was reached through My Apps and nowhere else.
// The gear on the dashboard's My apps section lists these, and an app switched on there
// gets a card of its own on that person's dashboard only.

import (
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// dashboardGroupMyApps puts "My apps" right after the dashboard's own apps,
// ahead of agents.
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
	// By name, where the person has not placed them: the spec store's keys
	// promise no order.
	own := listSpecs(user)
	sort.SliceStable(own, func(i, j int) bool { return own[i].Name < own[j].Name })
	for _, s := range own {
		add(s, "My apps")
	}
	// In slug order: the index is a map, and a card the person never placed
	// keeps the place it is given here.
	owners := ListSharedOwners(T.DB, sharedAppsIndex)
	slugs := make([]string, 0, len(owners))
	for slug := range owners {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		owner := owners[slug]
		if owner == user || seen[slug] {
			continue
		}
		if s, ok := loadSpec(owner, slug); ok && appReaches(s, user) {
			add(s, "My apps")
		}
	}
	return out
}
