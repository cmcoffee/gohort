package service

// A search connector (web.search) is offered beyond Web Search the way a
// Source Hook is: to research, on the topics it is for, and to agents, as a
// tool of its own (Binding.Research, Binding.Tool). Research reaches it
// through core/sourcehooks' seams, so it asks a connector as it asks a hook.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

func init() {
	sourcehooks.ConnectorSources = searchSources
	sourcehooks.ConnectorSearch = func(name, query string) (string, error) {
		return Search(context.Background(), name, query)
	}
}

// searchBinding is the approved connector named name's web.search binding.
func searchBinding(name string) (Binding, bool) {
	c, ok := GetConnector(RootDB, name)
	if !ok || c.Kind != Kind || !c.Approved {
		return Binding{}, false
	}
	s, err := Parse(c.Spec)
	if err != nil {
		return Binding{}, false
	}
	b, _, err := s.binding("web.search")
	return b, err == nil
}

// searchSources are the approved search connectors offered to research, as
// the entries research matches by topic.
func searchSources() []sourcehooks.SourceHook {
	var out []sourcehooks.SourceHook
	for _, c := range ListConnectors(RootDB) {
		if c.Kind != Kind || !c.Approved {
			continue
		}
		b, ok := searchBinding(c.Name)
		if !ok || b.Research == nil {
			continue
		}
		out = append(out, sourcehooks.SourceHook{Name: c.Name, Type: sourcehooks.HookTypeAPI, Connector: c.Name,
			TriggerDomains: b.Research.Topics, AlwaysActive: b.Research.Always})
	}
	return out
}

var (
	searchToolsMu sync.Mutex
	searchTools   = map[string]bool{}
)

// registerSearchTool offers the search connector named connector to agents
// as the tool its spec names. The registry keeps a tool once registered; one
// whose connector no longer offers it under that name leaves every catalog.
func registerSearchTool(connector string, b Binding) {
	if b.Tool == nil {
		return
	}
	key := b.Tool.Name + "\x00" + connector
	searchToolsMu.Lock()
	defer searchToolsMu.Unlock()
	if searchTools[key] {
		return
	}
	searchTools[key] = true
	RegisterChatTool(&searchTool{name: b.Tool.Name, connector: connector})
}

// searchTool is a search connector as an agent tool.
type searchTool struct{ name, connector string }

func (t *searchTool) Name() string { return t.name }

func (t *searchTool) Category() string { return "Search" }

// offered is the connector's tool setting, when it still offers this tool.
func (t *searchTool) offered() (*ToolUse, bool) {
	b, ok := searchBinding(t.connector)
	if !ok || b.Tool == nil || b.Tool.Name != t.name {
		return nil, false
	}
	return b.Tool, true
}

func (t *searchTool) Desc() string {
	if u, ok := t.offered(); ok && strings.TrimSpace(u.Description) != "" {
		return strings.TrimSpace(u.Description)
	}
	return "Search " + t.connector + ": results with titles, links and snippets. Use it where it knows more than web_search does."
}

func (t *searchTool) Params() map[string]ToolParam {
	return map[string]ToolParam{"query": {Type: "string", Description: "What to search for."}}
}

func (t *searchTool) SchemaWithSession(*ToolSession) (string, map[string]ToolParam) {
	if _, ok := t.offered(); !ok {
		return "", nil
	}
	return t.Desc(), t.Params()
}

func (t *searchTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} }

func (t *searchTool) IsInternetTool() bool { return true }

func (t *searchTool) Run(args map[string]any) (string, error) {
	return t.RunWithSession(args, nil)
}

func (t *searchTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if _, ok := t.offered(); !ok {
		return "", fmt.Errorf("%s is no longer offered: its connector %s was changed, turned off or removed", t.name, t.connector)
	}
	if sess != nil && !sess.NetworkAllowed() {
		return "", fmt.Errorf("%s refused: network is blocked for this turn (private mode is on)", t.name)
	}
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("say what to search for in query")
	}
	ctx := context.Background()
	if sess != nil {
		ctx = sess.Context()
	}
	return Search(ctx, t.connector, strings.TrimSpace(query))
}

// noUsableLink stands where a result's link would be when the search gave a
// link that cannot be opened. A reader of search results takes only a web
// address there, so it passes over the result; an agent still reads its
// title and snippet, and why there is nothing to open.
const noUsableLink = "(no usable link: Google gave a redirect that cannot be opened after the search)"

// MarkUnopenable puts noUsableLink in place of every link in numbered search
// results ("1. Title\n   URL\n   Snippet") that is a Google redirect. Since
// 2026-10-30 Google lets only the first few result links of a search be
// opened, and a search API serving Google results (Serper among them) gives
// the rest as google.com/goto links that lead nowhere. Every search goes
// through here: the built-in providers and every search connector.
func MarkUnopenable(results string) string {
	blocks := strings.Split(results, "\n\n")
	changed := false
	for i, block := range blocks {
		lines := strings.Split(block, "\n")
		if len(lines) < 2 || !googleRedirect(strings.TrimSpace(lines[1])) {
			continue
		}
		lines[1] = "   " + noUsableLink
		blocks[i] = strings.Join(lines, "\n")
		changed = true
	}
	if !changed {
		return results
	}
	return strings.Join(blocks, "\n\n")
}

// googleRedirect reports whether u is one of Google's result redirects
// (google.<tld>/goto or /url) rather than the page itself.
func googleRedirect(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if !strings.HasPrefix(host, "google.") {
		return false
	}
	return parsed.Path == "/goto" || strings.HasPrefix(parsed.Path, "/goto/") || parsed.Path == "/url"
}
