package admin

// Source Hooks are connectors too (docs/design/connectors-and-flows.md):
// an API hook is a search, a RAG hook a store of passages, each reached at
// its endpoint with a key it keeps itself. The rows are read from the hooks
// as they are stored, so nothing moves: a hook is set up, turned off and
// removed under Admin > Extensions > Source Hooks. A paywall hook is not
// listed: it is a key sent when fetching certain sites, not something gohort
// asks a question of.

import (
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// hookRowPrefix names a Source Hook's row: a colon no stored connector's
// name can hold.
const hookRowPrefix = "hook:"

// hookConnector is one Source Hook's row.
type hookConnector struct {
	name    string // hook:<name>
	hook    sourcehooks.SourceHook
	wrapper string
	status  string
}

// hookConnectors are the API and RAG Source Hooks, as connectors.
func hookConnectors() []hookConnector {
	var out []hookConnector
	for _, h := range RegisteredSourceHooks() {
		if h.Type != HookTypeAPI && h.Type != HookTypeRAG {
			continue
		}
		hc := hookConnector{name: hookRowPrefix + h.Name, hook: h}
		hc.wrapper = hookWrapper(h)
		hc.status = hookStatus(h)
		out = append(out, hc)
	}
	return out
}

// hookWrapper is a hook's line, read inside to outside: what it gives, its
// query, and the host it reaches with the key it keeps.
func hookWrapper(h sourcehooks.SourceHook) string {
	gives := "Search results"
	if h.Type == HookTypeRAG {
		gives = "Document passages"
	}
	host := h.Endpoint
	if u, err := url.Parse(h.Endpoint); err == nil && u.Host != "" {
		host = u.Host
	}
	key := "no key"
	if h.AuthType != "" && h.AuthType != "none" {
		key = "its own " + string(h.AuthType) + " key"
	}
	return gives + " ⇐ " + hookParam(h) + "={query} ⇐ " + host + " (" + key + ")"
}

func hookParam(h sourcehooks.SourceHook) string {
	if p := strings.TrimSpace(h.QueryParam); p != "" {
		return p
	}
	return "q"
}

// hookUses is what the hook is used for: off, the web search engine,
// research on its topics, an agent's tool.
func hookUses(h sourcehooks.SourceHook) []string {
	switch {
	case h.Disabled:
		return []string{"Off."}
	case sourcehooks.IsSearchEngine(h):
		return []string{"The web search engine (Admin > Capabilities > Web Search)."}
	}
	var uses []string
	switch {
	case h.AlwaysActive:
		uses = append(uses, "Asked in every research run.")
	case len(h.TriggerDomains) > 0:
		uses = append(uses, "Asked in research on: "+strings.Join(h.TriggerDomains, ", ")+".")
	}
	if h.ExposeToLLM {
		name := strings.TrimSpace(h.ToolName)
		if name == "" {
			name = "its own tool"
		}
		uses = append(uses, "Offered to agents as "+name+".")
	}
	return uses
}

// hookStatus says what the hook is used for, where its key is, and where it
// is set up.
func hookStatus(h sourcehooks.SourceHook) string {
	parts := hookUses(h)
	if h.AuthType != "" && h.AuthType != "none" {
		parts = append(parts, "Its key is kept in the hook, not an API credential.")
	}
	return strings.Join(append(parts, "Set up under Admin > Extensions > Source Hooks."), " ")
}

// hookEndpoints is a hook's one endpoint: its query and how its results are
// read.
func hookEndpoints(hc hookConnector) []service.Endpoint {
	h := hc.hook
	field := func(v, def string) string {
		if strings.TrimSpace(v) != "" {
			return v
		}
		return def
	}
	reads := field(h.ResultsPath, "the reply") + "[each]: title ← " + field(h.TitleField, "title") + "; url ← " + field(h.URLField, "url") + "; snippet ← " + field(h.SnippetField, "snippet")
	if h.Type == HookTypeRAG {
		reads += "; content ← " + field(h.ContentField, "content")
	}
	used := strings.Join(hookUses(h), " ")
	if used == "" {
		used = "not used"
	}
	return []service.Endpoint{{Name: h.Name, Address: "GET " + h.Endpoint + "?" + hookParam(h) + "={query}",
		UsedBy: used, Sends: "{query}", Reads: reads}}
}

// hookByName is the row named name (hook:<name>).
func hookByName(name string) (hookConnector, bool) {
	for _, hc := range hookConnectors() {
		if hc.name == name {
			return hc, true
		}
	}
	return hookConnector{}, false
}
