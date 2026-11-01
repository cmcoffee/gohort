package service

// Source Hooks were research sources described as data in a store of their
// own, with their own key storage and their own search code: the same thing
// a search connector is, with a weaker engine. This moves every stored hook
// once, at startup: an API or RAG hook becomes the search connector it
// described (its key in a credential, its price on the binding) and a
// Research Sources entry (its topics and agent tool), PubMed, OpenAlex and EDGAR become connectors on their built-in
// searches, the hook that was the web search engine hands the engine to its
// connector, and a paywall hook, which no code ever applied, becomes a
// credential for its sites, switched off. Then the hook is gone.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// sourceHookTable is where Source Hooks were stored, by lowercased name with
// spaces as underscores, each key's secret under key+"_auth".
const sourceHookTable = "source_hooks"

// hookBuiltins are the hooks whose search was Go, by lowercased name: the
// built-in search each moves to, its template, its address and how many
// results it asks for.
var hookBuiltins = map[string]struct{ builtin, template, base string }{
	"pubmed":    {"pubmed", "pubmed-search", "https://eutils.ncbi.nlm.nih.gov"},
	"openalex":  {"openalex", "openalex-search", "https://api.openalex.org"},
	"sec edgar": {"edgar", "edgar-search", "https://efts.sec.gov"},
	"edgar":     {"edgar", "edgar-search", "https://efts.sec.gov"},
	"sec":       {"edgar", "edgar-search", "https://efts.sec.gov"},
}

// hookSlug is a hook's name as a connector name: lowercase letters and
// digits, the rest as single dashes.
func hookSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	if s := strings.Trim(b.String(), "-"); s != "" {
		return s
	}
	return "source"
}

// HookConnector makes the search connector Source Hook h described, its key
// (when it has one) kept in the connection's own space, and the Research Sources
// entry for how research and agents asked it, on when approve (which puts
// the connector to use). from names where the hook came from, for the
// connector's description. It returns the connector's name and how many
// records it wrote.
func HookConnector(db Database, h sourcehooks.SourceHook, approve bool, from string) (string, int, error) {
	if h.Type == sourcehooks.HookTypePaywall {
		return "", 0, fmt.Errorf("%s is a paywall hook: it is a credential for its sites, not a search", h.Name)
	}
	slug := hookSlug(h.Name)
	var base, path, template string
	op := map[string]any{}
	fields := map[string]any{"title": "title", "url": "url", "snippet": "snippet", "published": "published"}
	resultsPath := "results"
	defaults := map[string]any{}
	if bi, ok := hookBuiltins[strings.ToLower(strings.TrimSpace(h.Name))]; ok {
		base, template = bi.base, bi.template
		if bi.builtin == "openalex" {
			// An OpenAlex hook could name its own address.
			if u, err := url.Parse(strings.TrimSpace(h.Endpoint)); err == nil && u.Host != "" {
				base = u.Scheme + "://" + u.Host
			}
		}
		op["builtin"] = bi.builtin
		defaults["count"] = 10
		if bi.builtin == "pubmed" {
			defaults["count"] = 5
		}
	} else {
		u, err := url.Parse(strings.TrimSpace(h.Endpoint))
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return "", 0, fmt.Errorf("%s has no endpoint to search: %q", h.Name, h.Endpoint)
		}
		base = u.Scheme + "://" + u.Host
		param := strings.TrimSpace(h.QueryParam)
		if param == "" {
			param = "q"
		}
		path = u.EscapedPath()
		if path == "" {
			path = "/"
		}
		if u.RawQuery != "" {
			path += "?" + u.RawQuery + "&" + url.QueryEscape(param) + "={query}"
		} else {
			path += "?" + url.QueryEscape(param) + "={query}"
		}
		op["path"] = path
		resultsPath = strings.TrimSpace(h.ResultsPath)
		pick := func(v, def string) string {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
			return def
		}
		snippet := pick(h.SnippetField, "snippet")
		if h.Type == sourcehooks.HookTypeRAG || strings.TrimSpace(h.ContentField) != "" {
			// A hook used a result's content when it had no snippet.
			snippet += "|" + pick(h.ContentField, "content")
		}
		fields = map[string]any{"title": pick(h.TitleField, "title"), "url": pick(h.URLField, "url"), "snippet": snippet}
	}

	changed := 0
	auth := map[string]any{"none": true}
	var config []any
	key := strings.TrimSpace(h.AuthKey)
	if h.AuthType == sourcehooks.HookAuthAPIKey || h.AuthType == sourcehooks.HookAuthBearer {
		send := "header:X-API-Key"
		switch {
		case template == "pubmed-search":
			send = "query:api_key"
		case strings.EqualFold(u2host(base), "www.courtlistener.com"):
			// CourtListener takes its token as "Token <key>", for its search
			// and for the opinions debate reads in full (SourceGet).
			send = "header:Authorization"
			if key != "" {
				key = "Token " + key
			}
		case h.AuthType == sourcehooks.HookAuthBearer:
			send = "bearer"
		}
		config = []any{map[string]any{"name": "api_key", "label": "API key", "type": "secret", "send": send, "required": true}}
		auth = map[string]any{"secret": "api_key"}
	}

	binding := map[string]any{"capability": "web.search", "plain": true, "steps": []any{map[string]any{"call": "search"}},
		"output": map[string]any{"results": map[string]any{"path": resultsPath, "fields": fields}}}
	if len(defaults) > 0 {
		binding["defaults"] = defaults
	}
	if h.CostPerCall > 0 {
		binding["cost"] = map[string]any{"per_call": h.CostPerCall}
	}
	spec := map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": base, "auth": auth, "operations": map[string]any{"search": op}},
		"provides": []any{binding},
	}
	if len(config) > 0 {
		spec["config"] = config
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", changed, err
	}
	c := Connector{Name: freeConnectorName(db, slug), Kind: Kind, Template: template, Spec: raw,
		Desc: "Search " + h.Name + ", moved here from the " + from}
	if err := SaveConnector(db, c); err != nil {
		return "", changed, fmt.Errorf("saving connector %s for %s: %w", c.Name, h.Name, err)
	}
	changed++
	// The key is the connection's own, for its address. A hook that came
	// with none (from a bundle, where secrets never travel) waits for it,
	// given under Settings on its row.
	if key != "" && len(config) > 0 {
		c, _ = GetConnector(db, c.Name)
		if err := SetConnectionSecret(db, c, "api_key", key, []string{base}); err != nil {
			return c.Name, changed, fmt.Errorf("keeping the key of %s: %w", h.Name, err)
		}
		changed++
	}
	// How research and agents asked the hook is its Research Sources entry,
	// which puts the connector to use when the hook was on.
	rs := ResearchSource{Connector: c.Name, Always: h.AlwaysActive, Off: !approve}
	for _, t := range h.TriggerDomains {
		if t = strings.TrimSpace(t); t != "" {
			rs.Topics = append(rs.Topics, t)
		}
	}
	if h.ExposeToLLM {
		rs.ToolName = strings.TrimSpace(h.ToolName)
		if !searchToolNameRE.MatchString(rs.ToolName) {
			rs.ToolName = strings.ReplaceAll(slug, "-", "_") + "_search"
		}
		rs.ToolDescription = strings.TrimSpace(h.ToolDescription)
	}
	if !rs.Always && len(rs.Topics) == 0 && rs.ToolName == "" {
		// Asked by nothing but Web Search, if that: no source to keep.
		if approve {
			if err := ApproveConnector(db, c.Name); err != nil {
				return c.Name, changed, err
			}
		}
		return c.Name, changed, nil
	}
	if err := SaveResearchSource(db, rs); err != nil {
		return c.Name, changed, err
	}
	return c.Name, changed + 1, nil
}

// paywallCredential keeps a paywall hook's key as a credential for its
// sites, switched off: no code ever sent it, and turning it on is what
// would start sending it, to those sites only, with fetch_url.
func paywallCredential(h sourcehooks.SourceHook) (int, error) {
	key := strings.TrimSpace(h.AuthKey)
	var sites []string
	for _, d := range h.Domains {
		if d = strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")), "/"); d != "" {
			sites = append(sites, "https://"+d)
		}
	}
	if key == "" || len(sites) == 0 || h.AuthType == sourcehooks.HookAuthNone {
		return 0, nil
	}
	cred := SecureCredential{Name: freeCredentialName(strings.ReplaceAll(hookSlug(h.Name), "-", "_") + "_paywall"),
		BaseURL: sites[0], AlsoCovers: sites[1:], Disabled: true,
		Description: "The key the paywall Source Hook " + h.Name + " held for its sites. No code ever applied it; enabled, fetch_url sends it to these sites."}
	if h.AuthType == sourcehooks.HookAuthBearer {
		cred.Type = SecureCredBearer
	} else {
		cred.Type, cred.ParamName = SecureCredHeader, "X-API-Key"
	}
	if err := Secure().Save(cred, key); err != nil {
		return 0, fmt.Errorf("saving the paywall key of %s: %w", h.Name, err)
	}
	return 1, nil
}

// MigrateSourceHooks moves every stored Source Hook onto a connector or a
// credential, once (Admin > Migrations shows it ran).
func MigrateSourceHooks(db Database) {
	NewMigrationRunner("core", "").Once("source_hooks_to_connectors:v1", func() int {
		changed, err := migrateSourceHooks(db)
		if err != nil {
			// Recorded on the marker, where Admin > Migrations shows it.
			panic(fmt.Sprintf("Source Hooks were not all moved to connectors: %v", err))
		}
		return changed
	})
}

// migrateSourceHooks is MigrateSourceHooks' work. A hook that cannot move
// (an API hook with no endpoint) is logged and removed: it never answered.
// It reports how many records it changed.
func migrateSourceHooks(db Database) (int, error) {
	if db == nil {
		return 0, nil
	}
	var engine, engineHook string
	db.Get(SearchTable, "provider", &engine)
	db.Get(SearchTable, "hook", &engineHook)
	total := 0
	for _, key := range db.Keys(sourceHookTable) {
		if strings.HasSuffix(key, "_auth") {
			continue
		}
		var h sourcehooks.SourceHook
		if !db.Get(sourceHookTable, key, &h) {
			continue
		}
		db.Get(sourceHookTable, key+"_auth", &h.AuthKey)
		if h.Type == sourcehooks.HookTypePaywall {
			n, err := paywallCredential(h)
			total += n
			if err != nil {
				return total, err
			}
		} else {
			name, n, err := HookConnector(db, h, !h.Disabled, "Source Hook "+h.Name)
			total += n
			if err != nil {
				Log("[migration] the Source Hook %s was not moved, and is removed: %v", h.Name, err)
			} else {
				Log("[migration] the Source Hook %s is now the search connector %s", h.Name, name)
				if engine == "hook" && strings.EqualFold(strings.TrimSpace(engineHook), strings.TrimSpace(h.Name)) {
					db.Set(SearchTable, "provider", media.ServiceProviderPrefix+name)
					db.Unset(SearchTable, "hook")
					total++
					Log("[migration] web search now comes from connector %s", name)
				}
			}
		}
		db.Unset(sourceHookTable, key)
		db.Unset(sourceHookTable, key+"_auth")
		total++
	}
	return total, nil
}

// MigrateResearchSettings moves a search connector's research topics and
// agent tool, which its spec held before they became the Research Sources
// tie-in, into a Research Sources entry, once, and takes them out of the
// spec. The entry is on when the connector was approved.
func MigrateResearchSettings(db Database) {
	NewMigrationRunner("core", "").Once("search_research_to_sources:v1", func() int {
		changed, err := migrateResearchSettings(db)
		if err != nil {
			// Recorded on the marker, where Admin > Migrations shows it.
			panic(fmt.Sprintf("research settings were not all moved to Research Sources: %v", err))
		}
		return changed
	})
}

func migrateResearchSettings(db Database) (int, error) {
	total := 0
	for _, c := range ListConnectors(db) {
		if c.Kind != Kind {
			continue
		}
		var spec map[string]any
		if json.Unmarshal(c.Spec, &spec) != nil {
			continue
		}
		provides, _ := spec["provides"].([]any)
		rs := ResearchSource{Connector: c.Name, Off: !c.Approved}
		found := false
		for _, p := range provides {
			b, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if r, ok := b["research"].(map[string]any); ok {
				found = true
				rs.Always, _ = r["always"].(bool)
				if ts, ok := r["topics"].([]any); ok {
					for _, t := range ts {
						rs.Topics = append(rs.Topics, fmt.Sprint(t))
					}
				}
			}
			if t, ok := b["tool"].(map[string]any); ok {
				found = true
				rs.ToolName, _ = t["name"].(string)
				rs.ToolDescription, _ = t["description"].(string)
			}
			delete(b, "research")
			delete(b, "tool")
		}
		if !found {
			continue
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			return total, err
		}
		c.Spec = raw
		if err := SaveConnector(db, c); err != nil {
			return total, fmt.Errorf("connector %s: %w", c.Name, err)
		}
		total++
		if err := SaveResearchSource(db, rs); err != nil {
			Log("[migration] connector %s's research settings were not kept: %v", c.Name, err)
			continue
		}
		total++
		Log("[migration] connector %s is now a research source (its topics and tool moved out of its spec)", c.Name)
	}
	return total, nil
}

// u2host is the host of an address.
func u2host(addr string) string {
	if u, err := url.Parse(addr); err == nil {
		return u.Host
	}
	return ""
}

func init() { RegisterArtifactType(sourceHookArtifact{}) }

// sourceHookArtifact reads a Source Hook a bundle made before Source Hooks
// became connectors: importing one makes the search connector it described,
// unapproved, and its Research Sources entry, off, its credential (secrets
// never travel) a draft waiting for its key. Nothing exports as one now:
// export the connector.
type sourceHookArtifact struct{}

func (sourceHookArtifact) ArtifactType() string { return "source_hook" }

func (sourceHookArtifact) ImportFollowUp() string {
	return "Now a search connector, and an off research source. Check where it points under Connectors, give its key under API Credentials if it takes one, then turn it on under Research Sources."
}

func (sourceHookArtifact) ListArtifacts(Database) []ArtifactSel { return nil }

func (sourceHookArtifact) ExportArtifact(_ Database, name, _ string) (json.RawMessage, error) {
	return nil, fmt.Errorf("Source Hooks became search connectors: export the connector %s is now", name)
}

func (sourceHookArtifact) ImportArtifact(db Database, recipe json.RawMessage, _ string) (string, string, error) {
	var h sourcehooks.SourceHook
	if err := json.Unmarshal(recipe, &h); err != nil {
		return "", "", fmt.Errorf("invalid source-hook recipe: %w", err)
	}
	if strings.TrimSpace(h.Name) == "" {
		return "", "", fmt.Errorf("missing source hook name")
	}
	if h.Type == sourcehooks.HookTypePaywall {
		return h.Name, "a paywall hook is a credential for its sites: add one under API Credentials", nil
	}
	h.AuthKey = ""
	name, _, err := HookConnector(db, h, false, "Source Hook "+h.Name+" in a bundle")
	return name, "", err
}
