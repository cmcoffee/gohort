// Co-author tools for the Guide Author agent: add_section / edit_section write
// directly into the OPEN guide's section list (the store the viewer renders), so
// "add an introduction" appears in the document. Built as closures over this
// app's guide store and injected into the agent's run via
// PublicHandleSendWithAppTools — orchestrate runs them, ignorant of guide storage.
package guides

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// coauthorTools builds the Guide Author's tool kit for one chat turn. canEdit
// distinguishes the two roles that reach these tools:
//   - editor (owner/admin, or a guide shared for edit): the full kit, scoped to
//     the CALLER — they build the guide from their own research + sources.
//   - reader (view-only shared guide): handleChatSend hands them only the
//     read-only subset (see readOnlyGuideTools). For those tools, canEdit=false
//     switches reference access to the guide's OWNER, restricted to the sources
//     the owner LINKED to this guide — so a reader can ask questions answered
//     from the guide's own sources without being able to reach them directly or
//     browse the owner's wider registry.
func (T *Guides) coauthorTools(udb Database, orch *orchestrate.OrchestrateApp, user string, canEdit bool) []AgentToolDef {
	// openGuide resolves the active guide for this turn, fresh each call. The active
	// marker is per-user (udb), but a SHARED guide lives in its owner's store — so
	// resolve returns the owner's UserDB + owner username, and every content op runs
	// there. That's what lets a collaborator on an edit-shared guide write into the
	// one canonical document (and grow its owner-scoped research collection).
	openGuide := func() (Guide, Database, string, bool) {
		id := activeGuideID(udb)
		if id == "" {
			return Guide{}, nil, "", false
		}
		g, owner, oudb, ok := resolveGuide(T.DB, udb, user, id)
		return g, oudb, owner, ok
	}
	// findIdx locates a section by case-insensitive title, returning its index in
	// the guide's slice (-1 if absent).
	findIdx := func(g Guide, title string) int {
		title = strings.TrimSpace(title)
		for i := range g.Sections {
			if strings.EqualFold(strings.TrimSpace(g.Sections[i].Title), title) {
				return i
			}
		}
		return -1
	}
	sectionTitles := func(g Guide) string {
		var ts []string
		for _, s := range g.sorted() {
			ts = append(ts, s.Title)
		}
		return strings.Join(ts, ", ")
	}

	addSection := AgentToolDef{
		Tool: Tool{
			Name:        "add_section",
			Description: "Append a new section to the guide the user has OPEN. Provide the section title and its BODY as markdown (sub-headings as ###, lists, fenced code — do NOT repeat the title inside the body). Use this to add content the user asks for; it lands in the document and the viewer updates. Errors if no guide is open — ask the user to select or create one.",
			Parameters: map[string]ToolParam{
				"section_title": {Type: "string", Description: "Title of the new section (shown as a numbered heading + in the table of contents)."},
				"markdown":      {Type: "string", Description: "The section body as markdown. Substantive content, not a placeholder. No top-level heading — the title is separate."},
			},
			Required: []string{"section_title", "markdown"},
		},
		SingleFirePerBatch: true,
		Handler: func(args map[string]any) (string, error) {
			title := strings.TrimSpace(fmt.Sprint(args["section_title"]))
			md := strings.TrimSpace(fmt.Sprint(args["markdown"]))
			if md == "" {
				return "", fmt.Errorf("markdown is required — pass the section body")
			}
			g, ownerUDB, _, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			g.Sections = append(g.Sections, Section{ID: newID(), Title: title, Markdown: md, Order: g.nextOrder()})
			saveGuideRev(ownerUDB, g, "Added section: "+title)
			return fmt.Sprintf("Added the %q section to %q (now %d section%s).", title, g.Title, len(g.Sections), plural(len(g.Sections))), nil
		},
	}

	editSection := AgentToolDef{
		Tool: Tool{
			Name:        "edit_section",
			Description: "Replace the body of an EXISTING section in the open guide, matched by its title (case-insensitive). Provide the new markdown body. Use for revisions the user asks for (\"expand the install section\", \"fix the example in Setup\"). Errors if no guide is open or no section matches.",
			Parameters: map[string]ToolParam{
				"section_title": {Type: "string", Description: "Title of the section to edit (must match an existing section)."},
				"markdown":      {Type: "string", Description: "The new section body as markdown (replaces the old body). No top-level heading."},
			},
			Required: []string{"section_title", "markdown"},
		},
		SingleFirePerBatch: true,
		Handler: func(args map[string]any) (string, error) {
			title := strings.TrimSpace(fmt.Sprint(args["section_title"]))
			md := strings.TrimSpace(fmt.Sprint(args["markdown"]))
			g, ownerUDB, _, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			idx := findIdx(g, title)
			if idx < 0 {
				return "", fmt.Errorf("no section titled %q — existing sections: %s", title, sectionTitles(g))
			}
			g.Sections[idx].Markdown = md
			saveGuideRev(ownerUDB, g, "Edited section: "+title)
			return fmt.Sprintf("Updated the %q section in %q.", title, g.Title), nil
		},
	}

	listSections := AgentToolDef{
		Tool: Tool{
			Name:        "list_sections",
			Description: "List the sections of the OPEN guide, in order, with their titles. Call this to see the guide's current structure before renaming, deleting, moving, or editing a section — so you use the exact existing titles and correct positions. No arguments.",
		},
		Handler: func(args map[string]any) (string, error) {
			g, _, _, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			secs := g.sorted()
			if len(secs) == 0 {
				return fmt.Sprintf("%q has no sections yet.", g.Title), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%q has %d section%s:\n", g.Title, len(secs), plural(len(secs)))
			for i, s := range secs {
				fmt.Fprintf(&b, "%d. %s\n", i+1, s.Title)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}

	deleteSection := AgentToolDef{
		Tool: Tool{
			Name:        "delete_section",
			Description: "Remove a section from the open guide, matched by its title (case-insensitive). Use when the user asks to drop or remove a section. This can't be undone from here, so only delete when the user clearly asked.",
			Parameters: map[string]ToolParam{
				"section_title": {Type: "string", Description: "Title of the section to remove (must match an existing section)."},
			},
			Required: []string{"section_title"},
		},
		SingleFirePerBatch: true,
		Handler: func(args map[string]any) (string, error) {
			title := strings.TrimSpace(fmt.Sprint(args["section_title"]))
			g, ownerUDB, _, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			idx := findIdx(g, title)
			if idx < 0 {
				return "", fmt.Errorf("no section titled %q — existing sections: %s", title, sectionTitles(g))
			}
			removed := g.Sections[idx].Title
			g.Sections = append(g.Sections[:idx], g.Sections[idx+1:]...)
			normalizeOrder(&g)
			saveGuideRev(ownerUDB, g, "Removed section: "+removed)
			return fmt.Sprintf("Removed the %q section from %q (%d left).", removed, g.Title, len(g.Sections)), nil
		},
	}

	renameSection := AgentToolDef{
		Tool: Tool{
			Name:        "rename_section",
			Description: "Rename a section in the open guide (changes its heading + table-of-contents entry; the body is untouched). Match the existing section by its current title.",
			Parameters: map[string]ToolParam{
				"section_title": {Type: "string", Description: "Current title of the section."},
				"new_title":     {Type: "string", Description: "New title for the section."},
			},
			Required: []string{"section_title", "new_title"},
		},
		SingleFirePerBatch: true,
		Handler: func(args map[string]any) (string, error) {
			title := strings.TrimSpace(fmt.Sprint(args["section_title"]))
			newTitle := strings.TrimSpace(fmt.Sprint(args["new_title"]))
			if newTitle == "" {
				return "", fmt.Errorf("new_title is required")
			}
			g, ownerUDB, _, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			idx := findIdx(g, title)
			if idx < 0 {
				return "", fmt.Errorf("no section titled %q — existing sections: %s", title, sectionTitles(g))
			}
			g.Sections[idx].Title = newTitle
			saveGuideRev(ownerUDB, g, "Renamed section: "+title+" → "+newTitle)
			return fmt.Sprintf("Renamed %q to %q.", title, newTitle), nil
		},
	}

	moveSection := AgentToolDef{
		Tool: Tool{
			Name:        "move_section",
			Description: "Reorder a section: move it to a 1-based position in the open guide (1 = first). Use to rearrange the document — e.g. move \"Troubleshooting\" to the end, or move \"Overview\" to position 1. Call list_sections first to see current positions.",
			Parameters: map[string]ToolParam{
				"section_title": {Type: "string", Description: "Title of the section to move."},
				"position":      {Type: "integer", Description: "Target 1-based position (1 = first). Values past the end move it last."},
			},
			Required: []string{"section_title", "position"},
		},
		SingleFirePerBatch: true,
		Handler: func(args map[string]any) (string, error) {
			title := strings.TrimSpace(fmt.Sprint(args["section_title"]))
			pos := coerceIntArg(args["position"])
			g, ownerUDB, _, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			secs := g.sorted()
			idx := -1
			for i := range secs {
				if strings.EqualFold(strings.TrimSpace(secs[i].Title), title) {
					idx = i
					break
				}
			}
			if idx < 0 {
				return "", fmt.Errorf("no section titled %q — existing sections: %s", title, sectionTitles(g))
			}
			reordered, target := reorderSections(secs, idx, pos-1)
			g.Sections = reordered
			saveGuideRev(ownerUDB, g, "Moved section: "+title)
			return fmt.Sprintf("Moved %q to position %d in %q.", title, target+1, g.Title), nil
		},
	}

	// research dispatches to the seed-research agent — web search + source
	// fetching + cited synthesis — and returns its findings so the Guide Author
	// can write a section GROUNDED in real, cited sources instead of its priors.
	// Use before drafting accuracy-critical content (commands, flags, version
	// specifics). Runs an agent loop (tens of seconds), so the agent should call
	// it deliberately, not for every section.
	research := AgentToolDef{
		Tool: Tool{
			Name:        "research",
			Description: "Research a topic on the web before writing about it: searches, reads sources, and returns a cited synthesis (with a Sources list). Use this for accuracy-critical content — exact commands, flags, version numbers, API details — so the section is grounded in real sources rather than your own recollection. Then write the section with add_section, carrying the citations/links through. Takes tens of seconds; call it deliberately, not for trivial sections.",
			Parameters: map[string]ToolParam{
				"topic": {Type: "string", Description: "The specific thing to research — a focused question or subject, e.g. 'RKE2 agent join command and required ports' (not just 'Kubernetes')."},
			},
			Required: []string{"topic"},
		},
		Handler: func(args map[string]any) (string, error) {
			topic := strings.TrimSpace(fmt.Sprint(args["topic"]))
			if topic == "" {
				return "", fmt.Errorf("topic is required")
			}
			// seed-research resolves as a seed agent in the user's store. Run it
			// synchronously and hand its cited synthesis back to the Guide Author.
			out, err := orch.RunAgentSync(context.Background(), user, user, "seed-research", topic)
			if err != nil {
				return "", fmt.Errorf("research failed: %w", err)
			}
			// Auto-persist the cited synthesis into the guide's own research
			// collection, so it accretes a reusable knowledge base the
			// search_knowledge tool can recall later (across sessions). Best-effort:
			// the findings are still returned for immediate drafting even if there's
			// no open guide or the ingest is skipped.
			if g, ownerUDB, ownerUser, ok := openGuide(); ok && strings.TrimSpace(out) != "" {
				collID, _ := ensureGuideCollection(ownerUDB, ownerUser, g)
				reportID := fmt.Sprintf("guide-research-%s-%d", collID, time.Now().UnixNano())
				IngestReportTitled(context.Background(), VectorDB, CollectionSource(collID), reportID, topic, out, "research")
				out += "\n\n_(Saved to this guide's research collection — you can recall it later with search_knowledge.)_"
			}
			return out, nil
		},
	}

	// search_knowledge searches the knowledge collections ATTACHED to the open
	// guide (the user's own curated knowledge) and returns the most relevant
	// passages. Distinct from `research` (which goes to the web): this grounds a
	// section in the user's internal/private material. No-op-ish when the guide has
	// no attached collections — it tells the agent to ask the user to attach some.
	searchKnowledge := AgentToolDef{
		Tool: Tool{
			Name:        "search_knowledge",
			Description: "Search the knowledge collections attached to the OPEN guide (the user's own curated documents) for passages relevant to a query, and return the best matches with their source labels. Use this BEFORE web research when the guide is about internal/private material the user has collected — it grounds the section in their own knowledge. If nothing is attached, it says so; then fall back to the `research` tool for public topics.",
			Parameters: map[string]ToolParam{
				"query": {Type: "string", Description: "What to look up — a focused question or topic, e.g. 'firewall failover configuration steps'."},
			},
			Required: []string{"query"},
		},
		Handler: func(args map[string]any) (string, error) {
			query := strings.TrimSpace(fmt.Sprint(args["query"]))
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			g, _, ownerUser, ok := openGuide()
			if !ok {
				return "", fmt.Errorf("no guide is open — ask the user to select or create one first")
			}
			if len(g.Collections) == 0 {
				return "No knowledge collections are attached to this guide. Ask the user to attach one with the Knowledge button on the guide toolbar, or use the `research` tool for public topics.", nil
			}
			// Collections are owned by the guide's owner (user-scoped in their store),
			// so search as the owner — this is what lets a collaborator's search hit
			// the guide's own research collection.
			hits := SearchCollections(context.Background(), CollectionsDB(), ownerUser, g.Collections, query, 6)
			if len(hits) == 0 {
				return fmt.Sprintf("No matches for %q in the attached collections.", query), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Passages from the attached collections relevant to %q (use what fits, cite the source labels):\n", query)
			for _, h := range hits {
				label := strings.TrimSpace(h.Section)
				if label == "" {
					label = h.Source
				}
				b.WriteString("\n--- " + label + " ---\n")
				b.WriteString(strings.TrimSpace(h.Text) + "\n")
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}

	// list_reference_sources + pull_reference expose the cross-app reference
	// registry: knowledge OTHER gohort services have gathered — servitor Systems
	// (facts about the user's appliances) and connected document sources like
	// Confluence (any ExposeReference MCP server). This is how a guide gets BUILT
	// FROM internal knowledge, not just web research. Per-user and access-gated by
	// the registry: the user only ever sees their own systems / connected docs.
	listReferences := AgentToolDef{
		Tool: Tool{
			Name:        "list_reference_sources",
			Description: "List the internal knowledge sources you can pull into the guide from OTHER gohort services — e.g. Systems (facts gathered about the user's own servers/appliances) and connected document sources like Confluence. Returns each source's items with their IDs. Call this to discover what's available before pull_reference, especially when the user asks to build a guide ABOUT a specific system or from internal docs. No arguments.",
		},
		Handler: func(args map[string]any) (string, error) {
			// Reader of a shared guide: only the sources the OWNER linked to this
			// guide, resolved under the owner's identity. The reader never sees the
			// wider registry — just the guide's own sources.
			if !canEdit {
				g, _, ownerUser, ok := openGuide()
				if !ok || len(g.References) == 0 {
					return "This guide has no linked reference sources.", nil
				}
				return describeAttachedReferences(ownerUser, g.References), nil
			}
			groups := ReferenceGroups(user)
			if len(groups) == 0 {
				return "No internal reference sources are available right now. (Systems appear once the user has appliances in the servitor app; document sources like Confluence appear once they're connected as a reference source.) Use the `research` tool for public/web topics instead.", nil
			}
			// Which items did the user attach to THIS guide via the Sources picker?
			attached := map[string]bool{}
			if g, _, _, ok := openGuide(); ok {
				for _, s := range g.References {
					attached[s.Kind+"\x00"+s.ItemID] = true
				}
			}
			var b strings.Builder
			if len(attached) > 0 {
				b.WriteString("Internal reference sources. Items marked [attached] were selected by the user via the Sources button as this guide's sources — build the guide from those unless told otherwise. Pull any item with pull_reference:\n")
			} else {
				b.WriteString("Internal reference sources you can pull from with pull_reference:\n")
			}
			for _, g := range groups {
				fmt.Fprintf(&b, "\n%s (kind: %s):\n", g.Label, g.Kind)
				for _, it := range g.Items {
					mark := ""
					if attached[g.Kind+"\x00"+it.ID] {
						mark = " [attached]"
					}
					if strings.TrimSpace(it.Desc) != "" {
						fmt.Fprintf(&b, "- %s — %s [id: %s]%s\n", it.Name, it.Desc, it.ID, mark)
					} else {
						fmt.Fprintf(&b, "- %s [id: %s]%s\n", it.Name, it.ID, mark)
					}
				}
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}

	pullReference := AgentToolDef{
		Tool: Tool{
			Name:        "pull_reference",
			Description: "Pull the knowledge for one reference item (from list_reference_sources) into your context so you can write a guide section GROUNDED in it — e.g. build a guide from a system's gathered facts (servitor) or from connected docs (Confluence). Provide the kind and item id from list_reference_sources, and optionally a query describing what you're writing about (document sources use it to return the most relevant material; system sources return their full picture regardless). Then write the section with add_section using only details the reference actually contains — do not invent specifics it doesn't include.",
			Parameters: map[string]ToolParam{
				"kind":    {Type: "string", Description: "The source kind from list_reference_sources, e.g. \"system\" or \"mcp:confluence\"."},
				"item_id": {Type: "string", Description: "The item id from list_reference_sources."},
				"query":   {Type: "string", Description: "Optional: what you're writing about, to focus document-source results. System sources ignore it."},
			},
			Required: []string{"kind", "item_id"},
		},
		Handler: func(args map[string]any) (string, error) {
			kind := strings.TrimSpace(fmt.Sprint(args["kind"]))
			itemID := strings.TrimSpace(fmt.Sprint(args["item_id"]))
			if kind == "" || itemID == "" {
				return "", fmt.Errorf("kind and item_id are required — get them from list_reference_sources")
			}
			query := ""
			if q, ok := args["query"]; ok {
				query = strings.TrimSpace(fmt.Sprint(q))
			}
			// Editors pull from their own registry; readers pull ONLY the guide's
			// linked sources, resolved with the OWNER's identity — so the answer is
			// grounded in the guide's own sources, but the reader can't pull any
			// source the owner didn't link to this guide.
			fetchUser := user
			if !canEdit {
				g, _, ownerUser, ok := openGuide()
				if !ok || !referenceAttached(g, kind, itemID) {
					return "That source isn't linked to this guide — you can only pull the sources the guide's owner attached to it.", nil
				}
				fetchUser = ownerUser
			}
			txt := FetchReference(context.Background(), fetchUser, kind, itemID, query)
			if strings.TrimSpace(txt) == "" {
				return fmt.Sprintf("No content available for %s item %q — it may be empty, or the id is wrong; re-check with list_reference_sources.", kind, itemID), nil
			}
			return txt, nil
		},
	}

	return []AgentToolDef{addSection, editSection, listSections, renameSection, deleteSection, moveSection, research, searchKnowledge, listReferences, pullReference}
}

// reorderSections moves the section at idx to clampedTarget (0-based), clamping
// the target into range, then reassigns 1..N Order values. Returns the new slice
// and the resolved 0-based target. secs is treated as the current display order;
// it is not mutated (a copy is taken).
func reorderSections(secs []Section, idx, target int) ([]Section, int) {
	out := append([]Section(nil), secs...)
	if idx < 0 || idx >= len(out) {
		return out, idx
	}
	if target < 0 {
		target = 0
	}
	if target > len(out)-1 {
		target = len(out) - 1
	}
	moved := out[idx]
	out = append(out[:idx], out[idx+1:]...)
	out = append(out[:target], append([]Section{moved}, out[target:]...)...)
	for i := range out {
		out[i].Order = i + 1
	}
	return out, target
}

// normalizeOrder reassigns 1..N Order values in current sorted order, closing any
// gaps left by a deletion.
func normalizeOrder(g *Guide) {
	secs := g.sorted()
	for i := range secs {
		secs[i].Order = i + 1
	}
	g.Sections = secs
}

// coerceIntArg pulls an int from an LLM-supplied arg (float64 / int / numeric
// string).
func coerceIntArg(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		out := 0
		for _, c := range strings.TrimSpace(n) {
			if c < '0' || c > '9' {
				break
			}
			out = out*10 + int(c-'0')
		}
		return out
	}
	return 0
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// gatherLinkedSourceSnapshot pulls a bounded, owner-scoped snapshot of the
// guide's CURRENT linked knowledge — passages from its attached collections
// (searched per section) plus the content of each attached reference source. The
// audit feeds this to the research agent so it can compare the written guide
// against what its sources now say (contradictions, newly-added material the
// guide is missing). Returns "" when the guide has no linked sources, so the
// audit falls back to a plain web-currency check. Bounded so a large corpus /
// reference can't blow up the audit prompt.
func gatherLinkedSourceSnapshot(ctx context.Context, ownerUser string, g Guide) string {
	const maxCollectionHits = 15
	const maxRefChars = 2000
	var b strings.Builder

	if len(g.Collections) > 0 {
		seen := map[string]bool{}
		var hits []string
		for _, s := range g.sorted() {
			q := strings.TrimSpace(s.Title)
			if q == "" {
				continue
			}
			for _, h := range SearchCollections(ctx, CollectionsDB(), ownerUser, g.Collections, q, 2) {
				txt := strings.TrimSpace(h.Text)
				if txt == "" || seen[txt] {
					continue
				}
				seen[txt] = true
				label := strings.TrimSpace(h.Section)
				if label == "" {
					label = h.Source
				}
				hits = append(hits, "--- "+label+" ---\n"+txt)
				if len(hits) >= maxCollectionHits {
					break
				}
			}
			if len(hits) >= maxCollectionHits {
				break
			}
		}
		if len(hits) > 0 {
			b.WriteString("#### Knowledge-collection material\n\n")
			b.WriteString(strings.Join(hits, "\n\n"))
			b.WriteString("\n\n")
		}
	}

	for _, ref := range g.References {
		txt := strings.TrimSpace(FetchReference(ctx, ownerUser, ref.Kind, ref.ItemID, g.Title))
		if txt == "" {
			continue
		}
		if len(txt) > maxRefChars {
			txt = txt[:maxRefChars] + "\n…(truncated)"
		}
		fmt.Fprintf(&b, "#### Reference source [%s:%s]\n\n%s\n\n", ref.Kind, ref.ItemID, txt)
	}
	return strings.TrimSpace(b.String())
}

// runUpdateFromSources dispatches the Guide Author — with the full co-author kit
// — to revise the guide's sections against its CURRENT linked sources, then
// returns a short summary of what changed. It's the button-driven equivalent of
// asking the chat to "update from sources": same tools, same owner-scoped source
// resolution (search_knowledge / pull_reference), and every edit lands through
// edit_section/add_section as a revision (roll back via History). Runs in a
// dedicated hidden sub-session so the automated pass doesn't clutter the user's
// visible guide chat. Synchronous (an agent loop; tens of seconds).
func (T *Guides) runUpdateFromSources(ctx context.Context, udb Database, orch *orchestrate.OrchestrateApp, user, guideID string, private bool) (string, error) {
	// Point the co-author tools at this guide (they resolve the active guide, then
	// its owner's store) for the duration of the run.
	udb.Set(activeTable, "current", guideID)
	const prompt = "Update this guide so its sections reflect its LINKED SOURCES — the attached knowledge collections and reference sources — as they stand right now.\n\n" +
		"1. Call list_sections to see the current structure.\n" +
		"2. For the guide's subject and each section, use search_knowledge and pull_reference to gather what the linked sources CURRENTLY say.\n" +
		"3. Where a section is outdated or contradicted by the sources, call edit_section to revise it — grounded strictly in the sources, carrying any citations. Where the sources cover something important the guide is missing, add_section for it.\n" +
		"4. Leave sections that already match their sources unchanged — don't rewrite for the sake of it. Work ONLY from the guide's linked sources here; do not use web research.\n\n" +
		"When done, reply with a short bulleted summary of exactly which sections you changed or added and why. If nothing needed changing, say so plainly."
	tools := T.coauthorTools(udb, orch, user, true)
	// A Private guide's update must not touch the internet: block network on the
	// run's context (the dispatch drops network-capable tools when the ctx says so)
	// and withhold the web-research tool. The prompt already says source-only.
	if private {
		ctx = WithNetworkConnector(ctx, NewNetworkConnector(true))
		tools = withoutTools(tools, "research")
	}
	res, err := orch.RunAgentSyncContinuingRich(ctx, orchestrate.AgentSyncRun{
		AgentOwner:   user,
		RuntimeUser:  user,
		AgentKey:     guideAgentID,
		SubSessionID: "guide-update:" + guideID,
		FreshSession: true,
		Message:      prompt,
		AppTools:     tools,
	})
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// runIncorporate dispatches the Guide Author to weave a PUSHED finding INTO the
// guide — merging it into the right existing section or adding a new one, in the
// guide's voice — rather than blind-appending a raw block. Used by the
// document-target push path (servitor's ↗ Guide button + push_to_guide tool) so
// pushed content lands coherently. Honors the guide's Private (no-internet) flag.
func (T *Guides) runIncorporate(ctx context.Context, udb Database, orch *orchestrate.OrchestrateApp, user, guideID, suggestedTitle, content string, private bool) (string, error) {
	udb.Set(activeTable, "current", guideID)
	prompt := "A new finding has been pushed to this guide. Incorporate it CORRECTLY into the document — do NOT just paste it in as a raw block.\n\n" +
		"1. Call list_sections to see the current structure.\n" +
		"2. If the finding extends, updates, or overlaps an EXISTING section, use edit_section to weave it in so that section still reads as one coherent piece (merge and re-flow — don't tack a fragment on the end). If it's a genuinely new topic, use add_section with a fitting title"
	if strings.TrimSpace(suggestedTitle) != "" {
		prompt += " (a reasonable title: \"" + strings.TrimSpace(suggestedTitle) + "\")"
	}
	prompt += ".\n" +
		"3. Keep the guide's voice and structure, don't duplicate anything already covered, and preserve any values/citations the finding carries.\n\n" +
		"THE FINDING TO INCORPORATE:\n\n" + content + "\n\n" +
		"When done, reply with a one-line summary of what you changed."
	tools := T.coauthorTools(udb, orch, user, true)
	if private {
		ctx = WithNetworkConnector(ctx, NewNetworkConnector(true))
		tools = withoutTools(tools, "research")
	}
	res, err := orch.RunAgentSyncContinuingRich(ctx, orchestrate.AgentSyncRun{
		AgentOwner:   user,
		RuntimeUser:  user,
		AgentKey:     guideAgentID,
		SubSessionID: "guide-incorporate:" + guideID,
		FreshSession: true,
		Message:      prompt,
		AppTools:     tools,
	})
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// referenceAttached reports whether a (kind, item_id) is among the guide's
// linked reference sources — the gate that keeps a reader's pull_reference to
// the owner's guide-attached sources only.
func referenceAttached(g Guide, kind, itemID string) bool {
	for _, s := range g.References {
		if s.Kind == kind && s.ItemID == itemID {
			return true
		}
	}
	return false
}

// describeAttachedReferences renders just the guide's linked reference sources
// for a reader, resolving names/descriptions from the OWNER's registry (the
// sources belong to the owner). Only attached items are listed; the reader never
// sees the owner's wider registry.
func describeAttachedReferences(ownerUser string, refs []ReferenceSelection) string {
	attached := map[string]bool{}
	for _, s := range refs {
		attached[s.Kind+"\x00"+s.ItemID] = true
	}
	var b strings.Builder
	b.WriteString("This guide's linked reference sources (pull any with pull_reference):\n")
	any := false
	for _, g := range ReferenceGroups(ownerUser) {
		var lines []string
		for _, it := range g.Items {
			if !attached[g.Kind+"\x00"+it.ID] {
				continue
			}
			any = true
			if strings.TrimSpace(it.Desc) != "" {
				lines = append(lines, fmt.Sprintf("- %s — %s [kind: %s, id: %s]", it.Name, it.Desc, g.Kind, it.ID))
			} else {
				lines = append(lines, fmt.Sprintf("- %s [kind: %s, id: %s]", it.Name, g.Kind, it.ID))
			}
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "\n%s:\n%s\n", g.Label, strings.Join(lines, "\n"))
		}
	}
	if !any {
		return "This guide has linked reference sources, but they can't be resolved right now (the owner's access to them may have changed)."
	}
	return strings.TrimRight(b.String(), "\n")
}
