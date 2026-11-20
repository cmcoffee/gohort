package anvil

// What the app this project holds did in real use: its scripts' recent runs,
// and what failed around them (an error its page hit in someone's browser, an
// ask its agent could not answer), from the record the live app keeps
// (tools/appscript/runlog.go). The assistant reads it to find out why the app
// does not work before changing anything.

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// appRunsShown is how many entries app_runs gives, newest first.
const appRunsShown = 30

func (c *toolCtx) appRunsTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "app_runs",
			Description: "What this app did when people used it, newest first: each live run of its data sources and actions (how it ended, its error, what it logged), " +
				"errors its page hit in a browser, and asks its agent could not answer. Read it first when the app does not work, to see what actually failed. " +
				"A run someone else started shows how it ended, never what it logged.",
			Parameters: map[string]ToolParam{},
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			return appRunsText(appscript.RecentRuns(c.user, c.project.AppSlug)), nil
		},
	}
}

// appRunsText is runs as the assistant reads them.
func appRunsText(runs []appscript.AppRun) string {
	if len(runs) == 0 {
		return "Nothing is recorded yet: no script of the app has run live, and nothing has failed in its page or its asks."
	}
	if len(runs) > appRunsShown {
		runs = runs[:appRunsShown]
	}
	var b strings.Builder
	b.WriteString("Newest first.\n")
	for _, r := range runs {
		what := r.Kind + " " + r.Name
		switch r.Kind {
		case "data":
			what = "data source " + r.Name
		case "page":
			what = "the page, in a browser"
		case "ask":
			what = "an ask of its agent"
		}
		how := "ok"
		if !r.OK {
			how = "FAILED"
		}
		who := "you"
		if r.Other {
			who = "someone using it"
		}
		fmt.Fprintf(&b, "\n- %s  %s  %s, by %s", r.At, what, how, who)
		if r.Times > 1 {
			fmt.Fprintf(&b, ", %d times (the latest shown)", r.Times)
		}
		if r.Error != "" {
			b.WriteString("\n  error: " + strings.ReplaceAll(r.Error, "\n", "\n    "))
		}
		for _, l := range r.Log {
			b.WriteString("\n  log: " + l)
		}
	}
	return b.String()
}
