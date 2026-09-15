// The retired-tool registry — the names that WERE tools and are not any more.
//
// The audit used to infer, from the sentence around a name, whether it was
// being used as a tool: "call X", "run X", a trailing "(". Every false positive
// came from that inference, and it cannot be fixed, because "the handler calls
// parse_config" is the same sentence whether parse_config is a tool or a
// function in some code the memory is describing. The difference is what the
// name IS, and that is not in the text.
//
// So this stops inferring and starts knowing. A name is reported only if it is
// recorded here as a tool that existed and no longer does. A function name in
// stored code was never a tool, is not in the registry, and is never mentioned
// again.
//
// RECONCILED ON READ, not written on create/delete. persistentTempToolsTable
// has nine write sites; hooking each is how one gets missed, and a registry
// that silently stops recording is worse than none. Instead the set only ever
// grows, updated from whatever the audit already enumerates: every name seen
// live is remembered, and anything remembered but no longer live is retired.
// The cost is that a tool created AND deleted between two audits is never seen
// — a gap worth taking for a mechanism that cannot drift out of date.
package orchestrate

import (
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// toolNameRegistryTable holds the per-user seen-set, keyed by username.
const toolNameRegistryTable = "tool_name_registry"

// retiredFrameworkTools are framework tools that were REMOVED, and so will
// never appear in any pool for the reconciler to notice.
//
// The unified memory surface replaced eight sibling tools with three verbs, and
// every agent memory that says "capture gotchas via store_fact" now names
// something that does not exist. That is exactly what this pane is for, and
// without this list it would be the one class of retirement the registry could
// not see — a tool pool only ever holds what a USER minted.
//
// Add a name here when a framework tool is removed. Renames belong here too:
// the old name is retired even though the capability survives.
var retiredFrameworkTools = []string{
	"store_fact", "forget_fact", "search_facts", "list_facts",
	"memory", "memory_save", "memory_search", "memory_forget",
	"knowledge_search", "fetch_knowledge_doc",
	"recall_history", "expand_history",
}

// observeToolNames records the names that exist now and returns those that once
// did and no longer do.
//
// current is every name reachable today — live, shared and orphaned. Orphaned
// counts as EXISTING here: the tool is still a record, just uncarried, and the
// audit reports it under its own finding. Retirement means the name is gone
// altogether.
func observeToolNames(udb Database, user string, current map[string]bool) map[string]bool {
	retired := map[string]bool{}
	for _, n := range retiredFrameworkTools {
		if !current[n] {
			retired[n] = true
		}
	}
	if udb == nil || user == "" {
		return retired
	}
	var seen []string
	udb.Get(toolNameRegistryTable, user, &seen)

	have := make(map[string]bool, len(seen))
	for _, n := range seen {
		have[n] = true
	}
	// Anything remembered but not reachable now has been retired.
	for n := range have {
		if !current[n] {
			retired[n] = true
		}
	}
	// Remember anything new. Sorted on write so the stored row is stable and a
	// diff of the registry reads as a change in membership rather than order.
	added := false
	for n := range current {
		if n != "" && !have[n] {
			have[n] = true
			added = true
		}
	}
	if added {
		all := make([]string, 0, len(have))
		for n := range have {
			all = append(all, n)
		}
		sort.Strings(all)
		udb.Set(toolNameRegistryTable, user, all)
	}
	return retired
}

// mentionsName reports whether text names this tool, on a word boundary.
//
// Bounded so that retiring "memory" does not report every sentence containing
// "memory_save", and retiring "store_fact" does not match "store_factory".
// Underscores are word characters, so a plain strings.Contains would do both.
func mentionsName(text, name string) (int, bool) {
	low, want := strings.ToLower(text), strings.ToLower(name)
	for from := 0; ; {
		i := strings.Index(low[from:], want)
		if i < 0 {
			return 0, false
		}
		i += from
		if !identChar(low, i-1) && !identChar(low, i+len(want)) {
			return i, true
		}
		from = i + len(want)
	}
}

func identChar(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}
