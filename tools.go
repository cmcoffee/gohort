/*
	This is for registering tool modules to fuzz chat.
*/

package main

import (
	. "github.com/cmcoffee/oddjob/core"

	_ "github.com/cmcoffee/oddjob/tools/attach"
	_ "github.com/cmcoffee/oddjob/tools/browser"
	_ "github.com/cmcoffee/oddjob/tools/calculate"
	_ "github.com/cmcoffee/oddjob/tools/datemath"
	_ "github.com/cmcoffee/oddjob/tools/email"
	_ "github.com/cmcoffee/oddjob/tools/export"
	_ "github.com/cmcoffee/oddjob/tools/files"
	_ "github.com/cmcoffee/oddjob/tools/findtools"
	_ "github.com/cmcoffee/oddjob/tools/imagefetch"
	_ "github.com/cmcoffee/oddjob/tools/keepgoing"
	_ "github.com/cmcoffee/oddjob/tools/localexec"
	_ "github.com/cmcoffee/oddjob/tools/orchestrator"
	_ "github.com/cmcoffee/oddjob/tools/parsexml"
	_ "github.com/cmcoffee/oddjob/tools/readoutput"
	_ "github.com/cmcoffee/oddjob/tools/silent"
	_ "github.com/cmcoffee/oddjob/tools/status"
	_ "github.com/cmcoffee/oddjob/tools/temptool"
	_ "github.com/cmcoffee/oddjob/tools/timezone"
	_ "github.com/cmcoffee/oddjob/tools/video"
	_ "github.com/cmcoffee/oddjob/tools/websearch"
	_ "github.com/cmcoffee/oddjob/tools/workspace"
)

// wireToolDB is set during initialization to connect tools to their database
// buckets. No-op when not configured.
var wireToolDB = func() {}

// chatTools holds the loaded chat tools keyed by name.
var chatTools map[string]ChatTool

// loadTools drains the core tool registry into the chatTools map.
func loadTools() {
	chatTools = make(map[string]ChatTool)
	for _, t := range RegisteredChatTools() {
		chatTools[t.Name()] = t
	}
}

