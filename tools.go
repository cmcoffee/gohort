/*
	This is for registering tool modules to fuzz chat.
*/

package main

import (
	. "github.com/cmcoffee/gohort/core"

	_ "github.com/cmcoffee/gohort/tools/attach"
	_ "github.com/cmcoffee/gohort/tools/browser"
	_ "github.com/cmcoffee/gohort/tools/calculate"
	_ "github.com/cmcoffee/gohort/tools/comedian"
	_ "github.com/cmcoffee/gohort/tools/datemath"
	_ "github.com/cmcoffee/gohort/tools/email"
	_ "github.com/cmcoffee/gohort/tools/files"
	_ "github.com/cmcoffee/gohort/tools/imagefetch"
	_ "github.com/cmcoffee/gohort/tools/localexec"
	_ "github.com/cmcoffee/gohort/tools/localtime"
	_ "github.com/cmcoffee/gohort/tools/orchestrator"
	_ "github.com/cmcoffee/gohort/tools/silent"
	_ "github.com/cmcoffee/gohort/tools/status"
	_ "github.com/cmcoffee/gohort/tools/temptool"
	_ "github.com/cmcoffee/gohort/tools/videodl"
	_ "github.com/cmcoffee/gohort/tools/websearch"
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

