// LLM-powered suggest for Tool Groups editor fields. Mirrors the
// {field, hint, record} → {value} shape the orchestrate agent editor
// uses, so the framework's ✨ Suggest button on each FormField just
// works.
//
// Worker tier (no remote LLM). The admin app doesn't embed AppCore
// itself, so we borrow the chat agent's AppCore (always registered
// and always has the worker LLM wired) to dispatch the call.

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// fieldsSuggestableToolGroup lists the editor fields the suggest
// endpoint will honor. Members is excluded — picking tools is a
// human-curation decision; we don't want the LLM hallucinating
// memberships.
var fieldsSuggestableToolGroup = map[string]bool{
	"name":        true,
	"description": true,
}

const toolGroupSuggestTimeout = 60 * time.Second

func (a *AdminApp) handleToolGroupSuggest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Field  string         `json:"field"`
		Hint   string         `json:"hint"`
		Record map[string]any `json:"record"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Field = strings.TrimSpace(req.Field)
	if !fieldsSuggestableToolGroup[req.Field] {
		http.Error(w, "field not suggestable", http.StatusBadRequest)
		return
	}

	core, err := borrowWorkerCore()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Side-fill mode: when the user clicks ✨ on description and the
	// name is still empty, ask the LLM for BOTH in one structured
	// call. The framework's runFieldSuggest applies the primary
	// `value` to the field that was clicked and any `extra.name`
	// through the same setter, so a single click lands a complete
	// record. Naming the group is partly the LLM's call anyway —
	// it'll be the one referencing the group by name later.
	wantsBoth := req.Field == "description" && emptyRecordField(req.Record, "name")

	ctx, cancel := context.WithTimeout(r.Context(), toolGroupSuggestTimeout)
	defer cancel()

	var prompt, sysPrompt string
	if wantsBoth {
		prompt = buildToolGroupSuggestBothPrompt(req.Hint, req.Record)
		sysPrompt = toolGroupSuggestBothSystemPrompt
	} else {
		prompt = buildToolGroupSuggestPrompt(req.Field, req.Hint, req.Record)
		sysPrompt = toolGroupSuggestSystemPrompt
	}

	f := false
	resp, err := core.LLM.Chat(ctx,
		[]Message{{Role: "user", Content: prompt}},
		WithSystemPrompt(sysPrompt),
		WithRouteKey("admin.tool_groups.suggest"),
		WithThink(f),
	)
	if err != nil {
		http.Error(w, "suggest failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if resp == nil {
		http.Error(w, "empty response", http.StatusBadGateway)
		return
	}

	out := map[string]any{}
	if wantsBoth {
		name, desc := parseBothResponse(resp.Content)
		if desc == "" {
			// Parser failed (model didn't follow format) — fall back
			// to treating the whole reply as the description.
			desc = cleanToolGroupSuggestion(resp.Content)
		}
		out["value"] = desc
		if name != "" {
			out["extra"] = map[string]any{"name": name}
		}
	} else {
		out["value"] = cleanToolGroupSuggestion(resp.Content)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// emptyRecordField reports whether the named field is missing or
// blank in the record. Treats both "" and unset the same so the
// side-fill only fires when the user has nothing typed.
func emptyRecordField(record map[string]any, field string) bool {
	v, ok := record[field]
	if !ok || v == nil {
		return true
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	return strings.TrimSpace(s) == ""
}

const toolGroupSuggestBothSystemPrompt = `You are an editor naming and describing a tool group — a bundle of related chat tools collapsed under one heading so an agent's tool catalog can show one expandable entry instead of N flat tools. The administrator picked the members already; you decide what to call the bundle and how to describe it.

You ARE the LLM that will eventually call expand_tool_group(name) when an agent needs this bundle, so the name you choose should be the name YOU would intuitively reach for from a tool catalog. Keep it short (1-3 words), lowercase snake_case when the dominant API or capability has an obvious name, Title Case for general capability bundles.

Reply with EXACTLY this format:

NAME: <one-line name>
DESCRIPTION: <2-4 sentence description>

No commentary, no markdown headers, no quotes around either value. The DESCRIPTION should lead with the bundle's capability (what tools can the agent do here) rather than the implementation (which specific APIs are wrapped). Don't enumerate the members by name — the framework lists them automatically alongside the description.`

// buildToolGroupSuggestBothPrompt feeds the LLM the member-tool
// descriptions and any hint, asking for NAME + DESCRIPTION together.
// Used only when name is empty at click time.
func buildToolGroupSuggestBothPrompt(hint string, record map[string]any) string {
	var b strings.Builder
	b.WriteString("## Member tools in this group\n\n")
	members := membersFromRecord(record)
	if len(members) == 0 {
		b.WriteString("(no members picked yet — propose a generic name + description and the admin will refine)\n\n")
	} else {
		for _, name := range members {
			t, ok := LookupChatTool(name)
			if !ok {
				fmt.Fprintf(&b, "- **%s** _(temp tool — description unavailable to admin)_\n", name)
				continue
			}
			fmt.Fprintf(&b, "- **%s** — %s\n", name, t.Desc())
		}
		b.WriteString("\n")
	}
	hint = strings.TrimSpace(hint)
	if hint != "" {
		b.WriteString("## Hint from the user\n\n")
		b.WriteString(hint)
		b.WriteString("\n\n")
	}
	b.WriteString("Reply with the NAME and DESCRIPTION as specified.")
	return b.String()
}

// parseBothResponse extracts name + description from the structured
// reply. Tolerates extra whitespace and stray markdown headers but
// requires the NAME: / DESCRIPTION: tags. Returns ("", "") when
// neither tag is found.
func parseBothResponse(s string) (name, desc string) {
	lines := strings.Split(s, "\n")
	var descBuf strings.Builder
	inDesc := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if !inDesc {
			if strings.HasPrefix(strings.ToUpper(t), "NAME:") {
				name = strings.TrimSpace(t[len("NAME:"):])
				continue
			}
			if strings.HasPrefix(strings.ToUpper(t), "DESCRIPTION:") {
				inDesc = true
				rest := strings.TrimSpace(t[len("DESCRIPTION:"):])
				if rest != "" {
					descBuf.WriteString(rest)
				}
				continue
			}
		} else {
			if descBuf.Len() > 0 {
				descBuf.WriteString(" ")
			}
			descBuf.WriteString(t)
		}
	}
	name = cleanToolGroupSuggestion(name)
	desc = cleanToolGroupSuggestion(descBuf.String())
	return name, desc
}

const toolGroupSuggestSystemPrompt = `You are an editor helping an administrator fill in one field of a tool-group definition. The group bundles related chat tools so the agent's tool catalog can collapse them into a single expandable entry. The user shows you the current state of the group (some fields filled, some blank) plus the field they want help with. You return ONLY the new value for that field — no commentary, no explanation, no markdown headers, no quotes around the value.

Be concise. Descriptions are LLM-facing summaries shown until the group is expanded, so they should help another LLM decide whether expanding the group would help with a given task. Name what the member tools collectively do.`

func buildToolGroupSuggestPrompt(field, hint string, record map[string]any) string {
	var b strings.Builder
	b.WriteString("## Tool group under construction\n\n")
	if record == nil {
		b.WriteString("(no fields filled yet)\n\n")
	} else {
		for _, k := range []string{"name", "description"} {
			v, ok := record[k]
			if !ok || v == nil {
				continue
			}
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s == "" {
				continue
			}
			fmt.Fprintf(&b, "### %s\n%s\n\n", k, s)
		}
		// Member tools — name + description of each. Fundamental signal
		// for composing a meaningful group description: the LLM sees
		// what's actually in the bundle, not just the group's slug.
		members := membersFromRecord(record)
		if len(members) > 0 {
			b.WriteString("### Member tools\n\n")
			for _, name := range members {
				t, ok := LookupChatTool(name)
				if !ok {
					fmt.Fprintf(&b, "- **%s** _(not in registry)_\n", name)
					continue
				}
				fmt.Fprintf(&b, "- **%s** — %s\n", name, t.Desc())
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("## Field to suggest\n\n")
	b.WriteString(field)
	b.WriteString("\n\n")
	switch field {
	case "name":
		b.WriteString("Short, human-readable group name (1-3 words). Examples: \"Communications\", \"Acme API\", \"Calendar\". Reuse the dominant API or capability name when one is obvious from the members.\n")
	case "description":
		b.WriteString("One short paragraph (2-4 sentences) describing what the member tools do collectively. The agent's LLM reads this to decide whether to call expand_tool_group. Lead with the capability, not the implementation (\"send messages across multiple channels\", not \"wraps Slack/SMS/email APIs\"). Don't enumerate the members by name — the framework lists them automatically alongside the description.\n")
	}
	hint = strings.TrimSpace(hint)
	if hint != "" {
		b.WriteString("\n## Hint from the user\n\n")
		b.WriteString(hint)
		b.WriteString("\n")
	}
	return b.String()
}

// membersFromRecord pulls the `members` field out of the record map
// regardless of whether it arrived as []any or []string (JSON-decoded
// arrays land as []any but a Go-side caller could pass []string).
func membersFromRecord(record map[string]any) []string {
	raw, ok := record["members"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// cleanToolGroupSuggestion strips leading/trailing whitespace and any
// stray surrounding quotes the LLM sometimes wraps text in despite the
// system prompt's "no quotes" instruction.
func cleanToolGroupSuggestion(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	return s
}

// handleToolGroupAutoCreate is the minimal-friction create flow:
// admin picks N member tools, server asks the LLM for a name +
// description based on the member descriptions, persists the group,
// returns the saved record. Admin can later refine name/description
// or add/remove members via the per-row editor.
func (a *AdminApp) handleToolGroupAutoCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Members []string `json:"members"`
		Hint    string   `json:"hint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cleaned := make([]string, 0, len(req.Members))
	seen := map[string]bool{}
	for _, m := range req.Members {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		cleaned = append(cleaned, m)
	}
	if len(cleaned) == 0 {
		http.Error(w, "at least one member is required", http.StatusBadRequest)
		return
	}
	core, err := borrowWorkerCore()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), toolGroupSuggestTimeout)
	defer cancel()

	// Reuse the same "give me NAME + DESCRIPTION" prompt the suggest
	// endpoint uses — single source of truth for what good output
	// looks like. Pass the members through the record shape the
	// prompt builder expects.
	record := map[string]any{"members": stringSliceToAny(cleaned)}
	prompt := buildToolGroupSuggestBothPrompt(req.Hint, record)

	f := false
	resp, err := core.LLM.Chat(ctx,
		[]Message{{Role: "user", Content: prompt}},
		WithSystemPrompt(toolGroupSuggestBothSystemPrompt),
		WithRouteKey("admin.tool_groups.suggest"),
		WithThink(f),
	)
	if err != nil {
		http.Error(w, "LLM call failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if resp == nil {
		http.Error(w, "empty LLM response", http.StatusBadGateway)
		return
	}
	name, desc := parseBothResponse(resp.Content)
	if name == "" {
		// Fall back to a generic name derived from member count so
		// the save doesn't fail just because the model didn't follow
		// the NAME: format. Admin can rename via the editor.
		name = fmt.Sprintf("Group of %d tools", len(cleaned))
	}
	if desc == "" {
		desc = strings.TrimSpace(resp.Content)
	}

	saved, err := SaveToolGroup(a.db, ToolGroup{
		Name:        name,
		Description: desc,
		Members:     cleaned,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(saved)
}

// stringSliceToAny converts a []string to []any so it slots into the
// record map shape buildToolGroupSuggestBothPrompt expects. Cheap.
func stringSliceToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// borrowWorkerCore finds any registered Agent whose AppCore has the
// worker LLM wired. Used by admin endpoints that need LLM access
// without owning an AppCore themselves. Prefers the chat agent
// (always present, always LLM-wired); falls back to any other agent.
func borrowWorkerCore() (*AppCore, error) {
	if a, ok := FindAgent("chat"); ok {
		if c := a.Get(); c != nil && c.LLM != nil {
			return c, nil
		}
	}
	// Walk the broader registry in case chat is somehow absent.
	for _, a := range RegisteredApps() {
		if c := a.Get(); c != nil && c.LLM != nil {
			return c, nil
		}
	}
	for _, a := range RegisteredAgents() {
		if c := a.Get(); c != nil && c.LLM != nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no worker LLM available")
}
