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
		if !h.ExposeToLLM {
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

// sourceHookToAgentToolDef converts one SourceHook into an
// AgentToolDef. Falls back to derived name/description when the
// admin didn't author them. Returns false when the hook is
// fundamentally unusable (empty Name, can't derive a valid tool name).
func sourceHookToAgentToolDef(h SourceHook) (AgentToolDef, bool) {
	toolName := strings.TrimSpace(h.ToolName)
	if toolName == "" {
		// Derive from the hook's display name. Lowercase + replace
		// non-alphanumerics with underscores so we land on a valid
		// tool name (LLM tool names need to match [a-zA-Z0-9_-]+).
		toolName = sanitizeToolName(h.Name)
		if toolName == "" {
			return AgentToolDef{}, false
		}
		toolName += "_search"
	}
	desc := strings.TrimSpace(h.ToolDescription)
	if desc == "" {
		switch h.Type {
		case HookTypeRAG:
			desc = fmt.Sprintf("Search %s (a curated RAG knowledge source). Returns document chunks ranked by relevance. Use when the user's question is in this source's domain; prefer over web_search for material that's specifically covered here.", h.Name)
		default: // HookTypeAPI
			desc = fmt.Sprintf("Search %s (a curated external API). Returns titled results with snippets. Use when the user's question is in this source's domain; prefer over web_search for specialized lookups.", h.Name)
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
			return result, nil
		},
	}
	return def, true
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
