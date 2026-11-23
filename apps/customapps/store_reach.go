package customapps

// What file stores an app reaches, said where it is shared.
//
// An administrator's grant of a file store belongs to the APP and covers
// everybody who uses it (core/appstores): the app's scripts run as its owner,
// so anybody the app is shared with reaches the store through it, though not
// the store itself. That was nowhere in sight when the app was shared or
// reviewed. The Share dialog and the administrator's review now say it.

import (
	"strings"

	"github.com/cmcoffee/gohort/core/appstores"
)

// storeReachLines is one line per store the app owner/slug is granted: its
// name, what the app may do there, and whether each person who uses it gets a
// folder of their own.
func storeReachLines(owner, slug string) []string {
	if appstores.Provider == nil {
		return nil
	}
	var out []string
	for _, g := range appstores.Provider.GrantsFor(appstores.App{Owner: owner, Slug: slug}) {
		var may []string
		switch {
		case g.Write:
			may = append(may, "read and change its files")
		case g.Read:
			may = append(may, "read its files")
		}
		if g.Commands {
			may = append(may, "run its commands")
		}
		if g.Pull {
			may = append(may, "pull files into it")
		}
		if len(may) == 0 {
			continue
		}
		name := g.Name
		if strings.TrimSpace(name) == "" {
			name = g.Store
		}
		line := name + ": " + strings.Join(may, ", ")
		if g.PerPerson {
			line += " (each person has their own folder)"
		}
		out = append(out, line)
	}
	return out
}
