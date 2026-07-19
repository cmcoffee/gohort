/*
	This is for registering agent modules to the gohort menu.
*/

package main

import (
	. "github.com/cmcoffee/gohort/core"

	_ "github.com/cmcoffee/gohort/apps/admin"
	_ "github.com/cmcoffee/gohort/apps/agents"
	_ "github.com/cmcoffee/gohort/apps/bridges"
	_ "github.com/cmcoffee/gohort/apps/codewriter"
	_ "github.com/cmcoffee/gohort/apps/account"
	_ "github.com/cmcoffee/gohort/apps/customapps"
	_ "github.com/cmcoffee/gohort/apps/guides"
	_ "github.com/cmcoffee/gohort/apps/knowledge"
	_ "github.com/cmcoffee/gohort/apps/mcpserver"
	_ "github.com/cmcoffee/gohort/apps/orchestrate"
	_ "github.com/cmcoffee/gohort/apps/prompts"
	// apps/phantom retired: transport + PhantomLink moved to apps/bridges, and
	// proactive / scheduled-callbacks / goal-conversations were dropped (an agent
	// on a channel covers them). No longer linked into the binary; the package
	// stays in-tree until its files are deleted. See the phantom-retirement audit.
	// apps/enginseer folded into apps/servitor as the Type=="repo" target-type:
	// a repo is now just another appliance you Map and ask questions about, sharing
	// servitor's full investigation shell (streaming plan-driven Map, probe/worker
	// split, scoped memory, toolbar). No longer linked into the binary.
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
