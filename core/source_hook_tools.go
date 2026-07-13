// Auto-generated LLM tools backed by configured source hooks.
//
// When admin sets ExposeToLLM=true on a SourceHook, the framework
// exposes that hook as an AgentToolDef to any agent whose catalog
// includes it. Per-hook tool name + description (admin-authored)
// gives the LLM focused intent ("pubmed_search" with a description
// like "Search PubMed for peer-reviewed medical literature; prefer
// this over web_search for clinical questions") rather than a
// generic one-tool-many-hooks dispatcher the LLM has to disambiguate.
//
// Paywall hooks are excluded — they augment fetch_url with auth
// headers transparently, not stand-alone search.
//
// Wired via Apps' DynamicTools callback in agent loop config so the
// set updates as admin adds/removes hooks without process restart.

package core

import (
	"fmt"
	"strings"
)

// BuildSourceHookAgentToolDefs walks the configured source hooks and
// returns one AgentToolDef per hook whose ExposeToLLM is true and
// whose Type is API or RAG. Returns empty when no exposable hooks
// are configured.
func BuildSourceHookAgentToolDefs(db Database) []AgentToolDef {
	hooks := RegisteredSourceHooks()
	if len(hooks) == 0 {
		return nil
	}
	out := make([]AgentToolDef, 0, len(hooks))
	for _, h := range hooks {
		if !h.ExposeToLLM || h.Disabled {
			continue
		}
		// Paywall hooks aren't search surfaces — skip.
		if h.Type == HookTypePaywall {
			continue
		}
		if def, ok := sourceHookToAgentToolDef(h); ok {
			out = append(out, def)
		}
	}
	return out
}

// sourceHookToolName is the LLM-facing tool name for a hook: the
// admin-authored ToolName, else a derived "<sanitized-name>_search".
// Empty when the hook has no usable name. Shared by the auto-tool
// builder and SourceHookToolDefByName so the name a skill references
// matches the name the tool is registered under.
func sourceHookToolName(h SourceHook) string {
	if n := strings.TrimSpace(h.ToolName); n != "" {
		return n
	}
	// Derive from the hook's display name. Lowercase + replace
	// non-alphanumerics with underscores so we land on a valid tool
	// name (LLM tool names need to match [a-zA-Z0-9_-]+).
	n := sanitizeToolName(h.Name)
	if n == "" {
		return ""
	}
	return n + "_search"
}

// SourceHookToolDefByName builds the agent tool for the source hook
// whose tool name matches `name`, REGARDLESS of ExposeToLLM — so a
// skill can grant a source-hook tool contextually (name it in the
// skill's AllowedTools) without flipping the hook always-on. Paywall
// hooks are excluded (they augment fetch_url, not stand-alone search).
// Returns ok=false when no hook matches. Used by the skill-tool
// resolver (orchestrate + phantom) as a fallback when a name isn't a
// registered ChatTool.
func SourceHookToolDefByName(name string) (AgentToolDef, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AgentToolDef{}, false
	}
	for _, h := range RegisteredSourceHooks() {
		if h.Type == HookTypePaywall || h.Disabled {
			continue
		}
		if sourceHookToolName(h) == name {
			return sourceHookToAgentToolDef(h)
		}
	}
	return AgentToolDef{}, false
}

// FindSourceHookByToolName resolves an LLM tool name to the (non-paywall)
// source hook behind it, INCLUDING disabled hooks — it serves the artifact
// dependency walks, where "does this reference exist" is the question, not
// "may it fire". A skill or agent that allowlists a hook-backed tool name
// (pubmed_search) references the hook itself; the bundle closure uses this to
// carry the hook along instead of silently dropping the name.
func FindSourceHookByToolName(name string) (SourceHook, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return SourceHook{}, false
	}
	for _, h := range RegisteredSourceHooks() {
		if h.Type == HookTypePaywall {
			continue
		}
		if sourceHookToolName(h) == name {
			return h, true
		}
	}
	return SourceHook{}, false
}

// sourceHookToAgentToolDef converts one SourceHook into an
// AgentToolDef. Falls back to derived name/description when the
// admin didn't author them. Returns false when the hook is
// fundamentally unusable (empty Name, can't derive a valid tool name).
func sourceHookToAgentToolDef(h SourceHook) (AgentToolDef, bool) {
	toolName := sourceHookToolName(h)
	if toolName == "" {
		return AgentToolDef{}, false
	}
	desc := strings.TrimSpace(h.ToolDescription)
	if desc == "" {
		switch h.Type {
		case HookTypeRAG:
			desc = fmt.Sprintf("Search %s (a curated knowledge source) — returns document chunks ranked by relevance. Cite ONLY specifics that appear in the returned chunks; if they don't contain the exact answer, this source doesn't cover it (say so, don't supply a number/cite from memory). Prefer it over web_search for material it actually covers.", h.Name)
		default: // HookTypeAPI
			desc = fmt.Sprintf("Search %s (a curated external API) — returns titled results with snippets. Cite ONLY what the results contain; if they don't answer the question, the source doesn't cover it (don't fill the gap from memory). Prefer it over web_search for the specialized lookups it covers.", h.Name)
		}
	}
	// Capture the hook in the closure so the handler can call
	// QuerySourceHook against it without re-resolving by name (which
	// would be wrong if the hook gets deleted mid-session — better
	// to fail with a clear error on stale handler than silently call
	// a different hook).
	captured := h
	def := AgentToolDef{
		Tool: Tool{
			Name:        toolName,
			Description: desc,
			Parameters: map[string]ToolParam{
				"query": {Type: "string", Description: "Natural-language search query. Phrase like you'd phrase a web search; the hook's adapter handles any specialized syntax."},
			},
			Required: []string{"query"},
			// Hooks hit external services. Tagged CapNetwork so
			// Private mode (per the existing filter) strips them
			// when the user wants a local-only turn.
			Caps: []Capability{CapNetwork, CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			query := strings.TrimSpace(stringArgForHook(args, "query"))
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			result, err := QuerySourceHook(captured, query)
			if err != nil {
				return "", fmt.Errorf("source hook %q failed: %w", captured.Name, err)
			}
			result = strings.TrimSpace(result)
			if result == "" {
				return fmt.Sprintf("No results from %s for %q.", captured.Name, query), nil
			}
			// Grounding guard: a curated source returning SOMETHING doesn't
			// make the model's answer authoritative — the retrieved chunks
			// may be merely topical (e.g. case law for a statute-number
			// question) without containing the specific asked for. Without
			// this, the model treats "I searched an authoritative source" as
			// "my specific is authoritative" and fills the gap from memory.
			// Mirrors the skill_knowledge_search append.
			result += "\n\n[Grounding: cite ONLY specifics that actually appear in these results — a number, name, citation, figure, or quote not shown above is NOT in this source. If the results don't contain the exact answer, say this source doesn't cover it rather than supplying one from memory.]"
			return result, nil
		},
	}
	return def, true
}

// --- query_source: the single dispatcher (the agents pattern) ---
//
// Instead of N always-on per-hook tools (one schema each — unbounded
// growth as admin exposes more sources), ALL globally-exposed hooks
// collapse to ONE name-keyed dispatcher (query_source) plus a shown
// "Available sources" catalog block (RenderAvailableSourcesBlock) —
// mirroring agents(action="run") + the available-agents block. The block
// carries the per-source "use when" intent the old per-tool descriptions
// held; the model reads it, picks a source, and calls query_source.
//
// Skills that grant a SPECIFIC hook still get a focused per-hook tool via
// SourceHookToolDefByName — that scoping is correct for a skill's domain
// and is intentionally unaffected by this collapse.

// exposedQueryableHooks returns the hooks eligible for the query_source
// dispatcher + catalog block: ExposeToLLM and non-paywall (paywall hooks
// augment fetch_url, not stand-alone search).
func exposedQueryableHooks() []SourceHook {
	var out []SourceHook
	for _, h := range RegisteredSourceHooks() {
		if !h.ExposeToLLM || h.Type == HookTypePaywall || h.Disabled {
			continue
		}
		out = append(out, h)
	}
	return out
}

// QuerySourceToolDef builds the single query_source dispatcher over all
// exposed source hooks. Returns ok=false when none are exposed (caller
// skips it). `source` is constrained to an enum of the available source
// names; RenderAvailableSourcesBlock supplies the "use when" detail.
func QuerySourceToolDef(db Database) (AgentToolDef, bool) {
	hooks := exposedQueryableHooks()
	if len(hooks) == 0 {
		return AgentToolDef{}, false
	}
	// Resolution map (display name + derived tool-name, case-insensitive)
	// and the enum of canonical names the model picks from.
	byKey := make(map[string]SourceHook, len(hooks)*2)
	names := make([]string, 0, len(hooks))
	for _, h := range hooks {
		name := strings.TrimSpace(h.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
		byKey[strings.ToLower(name)] = h
		if tn := sourceHookToolName(h); tn != "" {
			byKey[strings.ToLower(tn)] = h
		}
	}
	if len(names) == 0 {
		return AgentToolDef{}, false
	}
	def := AgentToolDef{
		Tool: Tool{
			Name:        "query_source",
			Description: "Search one of the curated knowledge sources the admin wired up — see \"Available sources\" for what each covers and when to prefer it over web_search. Pick the source whose domain fits the question and pass a natural-language query; cite ONLY specifics that appear in the returned results.",
			Parameters: map[string]ToolParam{
				"source": {Type: "string", Enum: names, Description: "Which source to query — one of the names listed under \"Available sources\"."},
				"query":  {Type: "string", Description: "Natural-language search query. Phrase like a web search; the source's adapter handles any specialized syntax."},
			},
			Required: []string{"source", "query"},
			Caps:     []Capability{CapNetwork, CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			source := strings.TrimSpace(stringArgForHook(args, "source"))
			query := strings.TrimSpace(stringArgForHook(args, "query"))
			if source == "" || query == "" {
				return "", fmt.Errorf("source and query are required")
			}
			h, ok := byKey[strings.ToLower(source)]
			if !ok {
				return fmt.Sprintf("Unknown source %q. Available: %s.", source, strings.Join(names, ", ")), nil
			}
			result, err := QuerySourceHook(h, query)
			if err != nil {
				return "", fmt.Errorf("source %q failed: %w", h.Name, err)
			}
			result = strings.TrimSpace(result)
			if result == "" {
				return fmt.Sprintf("No results from %s for %q.", h.Name, query), nil
			}
			// Same grounding guard the per-hook tools carried.
			result += "\n\n[Grounding: cite ONLY specifics that actually appear in these results — a number, name, citation, figure, or quote not shown above is NOT in this source. If the results don't contain the exact answer, say this source doesn't cover it rather than supplying one from memory.]"
			return result, nil
		},
	}
	return def, true
}

// RenderAvailableSourcesBlock lists the exposed source hooks as a
// system-prompt catalog (name — "use when…"), mirroring the available-
// agents block. It's the menu the model reads to pick a `source` for
// query_source. Empty when no hooks are exposed.
func RenderAvailableSourcesBlock(db Database) string {
	hooks := exposedQueryableHooks()
	if len(hooks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Available sources\n\n")
	b.WriteString("Curated knowledge sources the admin wired up. When a question fits one of these, prefer query_source(source, query) over web_search — they're authoritative for their domain, and you should cite ONLY what their results contain. Format: **name** — what it covers / when to use.\n\n")
	for _, h := range hooks {
		name := strings.TrimSpace(h.Name)
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(h.ToolDescription)
		if desc == "" {
			switch h.Type {
			case HookTypeRAG:
				desc = "curated knowledge corpus — returns ranked document chunks."
			default:
				desc = "curated external API — returns titled results with snippets."
			}
		}
		b.WriteString("- **")
		b.WriteString(name)
		b.WriteString("** — ")
		b.WriteString(desc)
		b.WriteString("\n")
	}
	return b.String()
}

// stringArgForHook extracts a string arg with a small case-insensitive
// fallback to match the framework's general arg-canonicalization
// posture (LLMs sometimes capitalize differently than the declared
// param). Local helper to avoid a cross-package dep.
func stringArgForHook(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	lower := strings.ToLower(key)
	for k, v := range args {
		if strings.ToLower(k) == lower {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// sanitizeToolName turns "PubMed API" → "pubmed_api", "Westlaw" →
// "westlaw". Only alphanumeric + underscore in the output; runs of
// other characters collapse to a single underscore; leading/trailing
// underscores stripped.
func sanitizeToolName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}
