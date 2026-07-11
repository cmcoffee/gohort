// Tool-gated prompt sections — admin authors a persona prompt with
// section blocks marked by the tools they reference; at build time
// the framework drops any section whose required tools aren't in
// the agent's allowed-tool set. Keeps the visible context honest
// (the model isn't told to call tools it doesn't have) and lets
// admins tighten an agent's allowlist without rewriting the prompt.
//
// Marker syntax — HTML comment immediately after a `## heading`:
//
//   ## Agent management
//   <!-- @requires-tools: create_agent, update_agent, list_agents -->
//
//   When the user asks you to make agents...
//
// Semantics:
//   - ALL listed tools must be present in the allowed-tool set. Any
//     missing one drops the whole section (heading + body).
//   - A section runs from its `## ` heading line up to (but not
//     including) the next `## ` heading at the same depth (or end of
//     prompt). Subheaders (`### `, `#### `) stay inside the parent.
//   - Lines outside any section (preamble before the first `## ` or
//     between an unmarked section and the next marker) are always
//     kept — only marked sections get stripped.
//   - Comments without a `@requires-tools:` directive are ignored
//     (passed through to the model verbatim).

package textutil

import (
	"regexp"
	"strings"
)

var promptSectionRequiresRE = regexp.MustCompile(`(?m)^\s*<!--\s*@requires-tools?\s*:\s*([^>]+?)\s*-->\s*$`)

// StripPromptSectionsForTools removes any `## ` section whose
// `<!-- @requires-tools: ... -->` marker lists a tool not in
// allowedTools. Returns the prompt with marked-but-unsupported
// sections excised. Idempotent; safe to call on prompts without
// any markers (returns the prompt unchanged).
//
// Nil `allowedTools` = "no restriction known, keep everything" —
// the right shape for agents whose `AllowedTools` field is empty
// (default-pool means every tool is in scope).
func StripPromptSectionsForTools(prompt string, allowedTools []string) string {
	if allowedTools == nil {
		return promptSectionRequiresRE.ReplaceAllString(prompt, "")
	}
	if !strings.Contains(prompt, "@requires-tool") {
		return prompt
	}
	allowed := map[string]bool{}
	for _, t := range allowedTools {
		allowed[strings.TrimSpace(t)] = true
	}
	lines := strings.Split(prompt, "\n")
	type section struct {
		startIdx int // index of the `## ` heading line
		endIdx   int // exclusive — first line of the NEXT `## ` or len(lines)
		required []string
	}
	var sections []*section
	var cur *section
	for i, ln := range lines {
		trim := strings.TrimSpace(ln)
		// New top-level section starts here.
		if strings.HasPrefix(trim, "## ") && !strings.HasPrefix(trim, "### ") {
			if cur != nil {
				cur.endIdx = i
				sections = append(sections, cur)
			}
			cur = &section{startIdx: i, endIdx: len(lines)}
			continue
		}
		// Look for the marker comment ONLY within the current
		// section (and only the line right after the heading is
		// strictly required; the regex finds it anywhere up to the
		// next section start, which is forgiving for blank lines).
		if cur != nil {
			if m := promptSectionRequiresRE.FindStringSubmatch(ln); m != nil {
				for _, name := range strings.Split(m[1], ",") {
					name = strings.TrimSpace(name)
					if name != "" {
						cur.required = append(cur.required, name)
					}
				}
			}
		}
	}
	if cur != nil {
		sections = append(sections, cur)
	}
	// Build the output, dropping sections whose required tools
	// aren't all in the allowed set.
	keep := make([]bool, len(lines))
	for i := range keep {
		keep[i] = true
	}
	for _, s := range sections {
		if len(s.required) == 0 {
			continue
		}
		drop := false
		for _, name := range s.required {
			if !allowed[name] {
				drop = true
				break
			}
		}
		if !drop {
			continue
		}
		for i := s.startIdx; i < s.endIdx; i++ {
			keep[i] = false
		}
	}
	out := make([]string, 0, len(lines))
	for i, ln := range lines {
		if keep[i] {
			out = append(out, ln)
		}
	}
	// Strip the @requires-tools comment lines from surviving
	// sections — they're authoring metadata, not for the model.
	cleaned := promptSectionRequiresRE.ReplaceAllString(strings.Join(out, "\n"), "")
	// Collapse the blank line the comment left behind.
	cleaned = regexp.MustCompile(`\n\n\n+`).ReplaceAllString(cleaned, "\n\n")
	return cleaned
}
