package orchestrate

// Fitting the catalog to the model's window.
//
// History already scales to the window; the system prompt and the tool
// schemas did not. Builder's ~13k-token prompt and ~55k tokens of schemas
// filled a 64k model before a word of history, so every round was refused, and
// recovery only cut history, which was never the bulk. Here the largest
// schemas step down behind load_tool until the fixed part fits, through the
// same index and lazy fallback the deferred authoring tools use. A model with
// room for the whole catalog gets it unchanged.

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// catalogWindowShare is the most of the window, in percent, that the system
// prompt and the tool schemas together may take. The rest is history, the
// thinking and the reply, which is what the history budget already assumes.
const catalogWindowShare = 40

// catalogKeepAlways are the tools a turn cannot do without, so none of them is
// ever held back: the way to load the rest, the ways to reach the person, and
// the loop's own controls.
var catalogKeepAlways = map[string]bool{
	"load_tool": true, "find_tools": true,
	"ask_user": true, "ask_user_form": true, "send_status": true,
	"keep_going": true, "stay_silent": true,
}

const heldBackToolIndexHeader = "\n\n## Tools held back for this model's context (load before use)\n" +
	"These exist but their parameters aren't loaded, so the conversation fits this model. When a request calls for one, first call `load_tool(names=[\"<name>\"])`, then use it normally.\n\n"

// fitToolsToWindow returns the tools to send and the tools to hold back, so
// that sysTokens plus the sent schemas take at most catalogWindowShare of
// window. The largest schemas go first, ties by name so a turn's catalog is
// the same every time it is built. keep names tools never held back. A window
// of 0 (unknown) holds nothing back.
func fitToolsToWindow(tools []AgentToolDef, sysTokens, window int, keep func(string) bool) (sent, held []AgentToolDef, over int) {
	if window <= 0 {
		return tools, nil, 0
	}
	limit := window * catalogWindowShare / 100
	size := make(map[string]int, len(tools))
	total := sysTokens
	for _, td := range tools {
		n := ToolSchemaTokens([]Tool{td.Tool})
		size[td.Tool.Name] = n
		total += n
	}
	if total <= limit {
		return tools, nil, 0
	}
	order := make([]AgentToolDef, 0, len(tools))
	for _, td := range tools {
		if !keep(td.Tool.Name) {
			order = append(order, td)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := size[order[i].Tool.Name], size[order[j].Tool.Name]
		if a != b {
			return a > b
		}
		return order[i].Tool.Name < order[j].Tool.Name
	})
	hold := map[string]bool{}
	for _, td := range order {
		if total <= limit {
			break
		}
		hold[td.Tool.Name] = true
		held = append(held, td)
		total -= size[td.Tool.Name]
	}
	for _, td := range tools {
		if !hold[td.Tool.Name] {
			sent = append(sent, td)
		}
	}
	if total > limit {
		over = total - limit
	}
	return sent, held, over
}

// fitCatalogToWindow holds back the largest schemas behind load_tool when the
// system prompt and the catalog would take more than catalogWindowShare of the
// model's window. Run once the catalog is final and before the blocks that
// list it, so the roster names only what was sent. Tools the turn's message
// names stay, as do catalogKeepAlways.
func (pr *planRun) fitCatalogToWindow() {
	t := pr.t
	if t.app == nil {
		return
	}
	window := t.app.LoopContextWindow()
	intent := strings.ToLower(t.intentText)
	keep := func(name string) bool {
		return catalogKeepAlways[name] || (intent != "" && intentMentionsTool(intent, name, ""))
	}
	sent, held, over := fitToolsToWindow(pr.allTools, EstimateTokens(pr.sys), window, keep)
	if len(held) == 0 {
		return
	}
	if t.deferredAuthoringDefs == nil {
		t.deferredAuthoringDefs = map[string]AgentToolDef{}
		t.deferredAuthoringLoaded = map[string]bool{}
	}
	var index strings.Builder
	names := make([]string, 0, len(held))
	for _, td := range held {
		t.deferredAuthoringDefs[td.Tool.Name] = td
		index.WriteString(deferredIndexLine(td))
		names = append(names, td.Tool.Name)
	}
	pr.allTools = sent
	pr.sys += heldBackToolIndexHeader + index.String()
	note := ""
	if over > 0 {
		note = fmt.Sprintf("; still ~%d tokens over, the system prompt is the bulk", over)
	}
	Log("[orchestrate.tools] agent=%s: catalog fitted to a %d-token window: %d tool(s) held back behind load_tool, largest first: %v%s",
		t.agent.ID, window, len(held), names, note)
}
