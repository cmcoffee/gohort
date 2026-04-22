/*
	This is for registering tool modules to fuzz chat.
*/

package main

import (
	. "github.com/cmcoffee/gohort/core"

	_ "github.com/cmcoffee/gohort/tools/calculate"
	_ "github.com/cmcoffee/gohort/tools/datemath"
	_ "github.com/cmcoffee/gohort/tools/email"
	_ "github.com/cmcoffee/gohort/tools/localtime"
	_ "github.com/cmcoffee/gohort/tools/mockshell"
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

