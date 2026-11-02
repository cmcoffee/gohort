package service

// A search connector (web.search) is referenced beyond Web Search as a
// research source: research asks it on its topics, and agents through a
// tool of its own, as Source Hooks were asked. Which connectors, on what, is
// the Research Sources tie-in, kept here; the connector stays how a service
// is reached and read. Research reaches a source through core/sourcehooks'
// seams.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

func init() {
	sourcehooks.ConnectorSources = searchSources
	sourcehooks.ConnectorSearch = func(name, query string) (string, error) {
		return Search(context.Background(), name, query)
	}
}

// ResearchSourcesTable holds the Research Sources tie-in: which search
// connectors research asks, on which topics, and which are offered to
// agents as tools. The connector itself stays how a service is
// reached and read; how gohort uses it for research is set here.
const ResearchSourcesTable = "research_sources"

// ResearchSource is one search connector referenced as a research source.
type ResearchSource struct {
	Connector string `json:"connector"`
	// Topics are the research topics (legal, medical, ...) it is asked on;
	// Always asks it on every research run.
	Topics []string `json:"topics,omitempty"`
	Always bool     `json:"always,omitempty"`
	// ToolName, when set, offers it to agents as a tool of that name (under
	// Research sources in the agent editor), with ToolDescription saying
	// when to choose it over web_search; a skill may grant it by that name.
	ToolName        string `json:"tool_name,omitempty"`
	ToolDescription string `json:"tool_description,omitempty"`
	// Off keeps the entry and asks nothing of the connector.
	Off bool `json:"off,omitempty"`
}

// ListResearchSources is every research source, by connector name.
func ListResearchSources(db Database) []ResearchSource {
	var out []ResearchSource
	if db == nil {
		return out
	}
	keys := db.Keys(ResearchSourcesTable)
	sort.Strings(keys)
	for _, k := range keys {
		var rs ResearchSource
		if db.Get(ResearchSourcesTable, k, &rs) {
			out = append(out, rs)
		}
	}
	return out
}

// toolNameTaken reports whether name is a tool agents already have under
// that name (a built-in, or a name the framework keeps), or another research
// source's than the one on connector.
func toolNameTaken(db Database, connector, name string) bool {
	if _, ok := FindChatTool(name); ok || IsReservedToolName(name) {
		return true
	}
	for _, rs := range ListResearchSources(db) {
		if rs.Connector != connector && rs.ToolName == name {
			return true
		}
	}
	return false
}

// GetResearchSource is the research source on connector name.
func GetResearchSource(db Database, name string) (ResearchSource, bool) {
	var rs ResearchSource
	if db == nil {
		return rs, false
	}
	ok := db.Get(ResearchSourcesTable, strings.TrimSpace(name), &rs)
	return rs, ok
}

// SaveResearchSource puts the search connector rs names to use as a
// research source: it must give web.search, research needs its topics or
// Always unless it is only offered to agents, and a tool name is one an
// agent can call that no other tool or source has. Saving it is an
// administrator's acceptance of the connector as it is now
// (AcceptChanges).
func SaveResearchSource(db Database, rs ResearchSource) error {
	rs.Connector = strings.TrimSpace(rs.Connector)
	c, ok := GetConnector(db, rs.Connector)
	if !ok || c.Kind != Kind || !Provides(c, "web.search") {
		return fmt.Errorf("no search connector named %q: a research source is a connector that gives web.search", rs.Connector)
	}
	var topics []string
	for _, t := range rs.Topics {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			topics = append(topics, t)
		}
	}
	rs.Topics = topics
	rs.ToolName, rs.ToolDescription = strings.TrimSpace(rs.ToolName), strings.TrimSpace(rs.ToolDescription)
	switch {
	case rs.ToolName != "" && !searchToolNameRE.MatchString(rs.ToolName):
		return fmt.Errorf("tool name %q: lowercase letters, digits and _, 3 to 41 of them, starting with a letter", rs.ToolName)
	case rs.ToolName != "" && toolNameTaken(db, rs.Connector, rs.ToolName):
		return fmt.Errorf("tool name %q is already another tool's: agents would reach that one instead; choose another", rs.ToolName)
	case !rs.Always && len(rs.Topics) == 0 && rs.ToolName == "":
		return fmt.Errorf("say when %s is asked: on topics, always, or offered to agents by a tool name", rs.Connector)
	}
	db.Set(ResearchSourcesTable, rs.Connector, rs)
	// Saving it here accepts a change waiting on its connector.
	AcceptChanges(db, rs.Connector)
	return nil
}

// DeleteResearchSource stops asking connector name as a research source;
// the connector stays.
func DeleteResearchSource(db Database, name string) {
	if db != nil {
		db.Unset(ResearchSourcesTable, strings.TrimSpace(name))
	}
}

// searchSources are the research sources that are on and whose connector
// has no change waiting, as the entries research matches by topic and
// agents ask through their tools.
func searchSources() []sourcehooks.SourceHook {
	var out []sourcehooks.SourceHook
	for _, rs := range ListResearchSources(RootDB) {
		c, ok := GetConnector(RootDB, rs.Connector)
		if rs.Off || !ok || c.Kind != Kind || held(c.Name) || !Provides(c, "web.search") {
			continue
		}
		out = append(out, sourcehooks.SourceHook{Name: c.Name, Type: sourcehooks.HookTypeAPI, Connector: c.Name,
			TriggerDomains: rs.Topics, AlwaysActive: rs.Always,
			ExposeToLLM: rs.ToolName != "", ToolName: rs.ToolName, ToolDescription: rs.ToolDescription})
	}
	return out
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
