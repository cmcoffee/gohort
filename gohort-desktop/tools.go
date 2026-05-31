// Tool-package loader. Blank imports here pull each tool package
// into the build; each package's init() registers its tool with the
// core registry. Adding a new tool category = new package under
// tools/ + a blank import line here. No central switch statement,
// no per-tool wiring in main.
//
// Mirrors gohort's own tasks.go / tools.go loader pattern.

package main

import (
	_ "github.com/cmcoffee/gohort/gohort-desktop/tools/filesystem"
	// Future:
	// _ "github.com/cmcoffee/gohort/gohort-desktop/tools/apps"
	// _ "github.com/cmcoffee/gohort/gohort-desktop/tools/notify"
	// _ "github.com/cmcoffee/gohort/gohort-desktop/tools/screenshot"
)
