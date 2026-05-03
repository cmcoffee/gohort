/*
	This is for registering agent modules to the gohort menu.
*/

package main

import (
	. "github.com/cmcoffee/gohort/core"

	_ "github.com/cmcoffee/gohort/apps/admin"
	_ "github.com/cmcoffee/gohort/apps/answer"
	_ "github.com/cmcoffee/gohort/apps/chat"
	_ "github.com/cmcoffee/gohort/apps/codewriter"
	_ "github.com/cmcoffee/gohort/apps/dual"
	_ "github.com/cmcoffee/gohort/apps/phantom"
	_ "github.com/cmcoffee/gohort/apps/servitor"
	_ "github.com/cmcoffee/gohort/apps/techwriter"
)

// loadAgents drains the core agent registry and registers agents and apps with the command menu.
func loadAgents() {
	for _, a := range RegisteredAgents() {
		command.Register(a)
	}
	for _, a := range RegisteredApps() {
		command.RegisterApp(a)
	}
	for _, a := range RegisteredAdminAgents() {
		command.RegisterAdmin(a)
	}
}
