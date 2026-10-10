/*
	This is for registering agent modules to the oddjob menu.
*/

package main

import (
	. "github.com/cmcoffee/oddjob/core"

	_ "github.com/cmcoffee/oddjob/apps/account"
	_ "github.com/cmcoffee/oddjob/apps/admin"
	_ "github.com/cmcoffee/oddjob/apps/agents"
	_ "github.com/cmcoffee/oddjob/apps/bridges"
	_ "github.com/cmcoffee/oddjob/apps/codewriter"
	_ "github.com/cmcoffee/oddjob/apps/customapps"
	_ "github.com/cmcoffee/oddjob/apps/filestore"
	_ "github.com/cmcoffee/oddjob/apps/extensions"
	_ "github.com/cmcoffee/oddjob/apps/knowledge"
	_ "github.com/cmcoffee/oddjob/apps/mcpserver"
	_ "github.com/cmcoffee/oddjob/apps/monitor"
	_ "github.com/cmcoffee/oddjob/apps/scribe"
	// OpenAI-compatible /v1 for external clients (a voice platform's custom-LLM
	// setting, an OpenAI SDK). NOTE: /v1/ is a public path — it bypasses cookie
	// auth and is guarded only by a personal access token, with no rate limit
	// yet. Comment this out to unmount it entirely.
	_ "github.com/cmcoffee/oddjob/apps/openaiapi"
	_ "github.com/cmcoffee/oddjob/apps/orchestrate"
	// Outbound publishing: the destinations a finished document can go to
	// (Confluence, a webhook) and the Publisher agent a writer app's Publish
	// button opens. No dashboard card of its own.
	_ "github.com/cmcoffee/oddjob/apps/publish"
	// apps/phantom retired: transport + PhantomLink moved to apps/bridges, and
	// proactive / scheduled-callbacks / goal-conversations were dropped (an agent
	// on a channel covers them). No longer linked into the binary; the package
	// stays in-tree until its files are deleted. See the phantom-retirement audit.
	// apps/enginseer folded into apps/servitor as the Type=="repo" target-type:
	// a repo is now just another appliance you Map and ask questions about, sharing
	// servitor's full investigation shell (streaming plan-driven Map, probe/worker
	// split, scoped memory, toolbar). No longer linked into the binary.
	_ "github.com/cmcoffee/oddjob/apps/servitor"
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
