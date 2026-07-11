package servitor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// servitorSource exposes each appliance servitor knows about as a generic
// reference source (core.ReferenceSource), so writer apps — techwriter today,
// others later — can ground a draft in the facts and knowledge docs servitor
// gathered about a system, without importing servitor. Registered once from
// RegisterRoutes. It holds the *Servitor so it can reuse the same share-aware
// resolvers servitor's own handlers use (resolveAppliance / listSharedAppliances)
// — the picker and Fetch therefore surface systems SHARED with the user, in the
// owner's context, exactly like servitor's appliance list does. The consumer's
// per-user DB can't read servitor's tables, so the source resolves the user's
// data itself.
type servitorSource struct{ app *Servitor }

func (s servitorSource) Kind() string  { return "system" }
func (s servitorSource) Label() string { return "Systems" }

// List returns the appliances the user can reach — the ones they OWN plus the
// ones shared with them — as pickable items, mirroring servitor's own appliance
// list (web.go GET handler).
func (s servitorSource) List(user string) []ReferenceItem {
	if s.app == nil || s.app.DB == nil || user == "" {
		return nil
	}
	var items []ReferenceItem
	seen := map[string]bool{}
	add := func(a Appliance, shared bool) {
		if a.ID == "" || seen[a.ID] {
			return
		}
		seen[a.ID] = true
		desc := a.Type
		if a.Host != "" {
			desc = strings.TrimSpace(a.Type + " · " + a.Host)
		}
		if shared {
			if desc == "" {
				desc = "shared"
			} else {
				desc += " · shared"
			}
		}
		name := a.Name
		if name == "" {
			name = a.ID
		}
		items = append(items, ReferenceItem{ID: a.ID, Name: name, Desc: desc})
	}
	// The user's OWN appliances.
	if udb := UserDB(s.app.DB, user); udb != nil {
		for _, key := range udb.Keys(applianceTable) {
			var a Appliance
			if udb.Get(applianceTable, key, &a) {
				add(a, false)
			}
		}
	}
	// Appliances shared with everyone by OTHER owners — usable, not owned.
	for id, owner := range s.app.listSharedAppliances() {
		if seen[id] || owner == user {
			continue
		}
		if ownerUDB := UserDB(s.app.DB, owner); ownerUDB != nil {
			var a Appliance
			if ownerUDB.Get(applianceTable, id, &a) {
				add(a, true)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

// Fetch returns a reference block for one appliance. When query is non-empty it
// returns only the SLICE of servitor's knowledge relevant to that subject —
// semantic search over the appliance's linked collections, plus any structured
// docs and facts that mention the query terms — so a writer app grounding a
// specific section gets focused material instead of the whole appliance. With no
// query (e.g. a whole-guide audit) or when nothing relevant is found, it falls
// back to the full picture.
func (s servitorSource) Fetch(ctx context.Context, user, itemID, query string) string {
	if s.app == nil || s.app.DB == nil || user == "" || itemID == "" {
		return ""
	}
	// Share-aware resolve: the record may be owned by the user, or shared with
	// them by another owner. resolveAppliance returns the owner's UserDB so all
	// knowledge reads (docs, facts, collections) run in the owner's context —
	// exactly ONE canonical place regardless of who opened it.
	a, owner, ownerUDB, ok := s.app.resolveAppliance(user, UserDB(s.app.DB, user), itemID)
	if !ok || a.ID == "" || ownerUDB == nil {
		return ""
	}

	if q := strings.TrimSpace(query); q != "" {
		if focused := s.fetchFocused(ctx, ownerUDB, owner, a, q); focused != "" {
			return focused
		}
		// Nothing matched the query — fall through to the full picture so the
		// consumer still gets grounding rather than an empty result.
	}
	return s.fetchFull(ownerUDB, a)
}

// fetchFocused returns only the knowledge relevant to query: semantic hits from
// the appliance's linked collections, plus structured docs and facts that
// mention the query terms. Returns "" when nothing matches so Fetch can fall
// back to the full picture.
func (s servitorSource) fetchFocused(ctx context.Context, udb Database, owner string, a Appliance, query string) string {
	var hits []SearchHit
	if len(a.Collections) > 0 {
		hits = SearchCollections(ctx, CollectionsDB(), owner, a.Collections, query, 6)
	}

	docs := allDocs(udb, a.ID)
	var matchedDocs []string
	for _, doc := range knowledgeDocNames {
		if c := strings.TrimSpace(docs[doc]); c != "" && refQueryMatch(c, query) {
			matchedDocs = append(matchedDocs, doc)
		}
	}

	var matchedFacts []SshFact
	for _, f := range factsForAppliance(udb, a.ID) {
		if f.Key != "" && refQueryMatch(f.Key+" "+f.Value, query) {
			matchedFacts = append(matchedFacts, f)
		}
	}

	if len(hits) == 0 && len(matchedDocs) == 0 && len(matchedFacts) == 0 {
		return ""
	}

	name := a.Name
	if name == "" {
		name = a.ID
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### System: %s (relevant to: %s)\n", name, query)
	if a.Host != "" {
		fmt.Fprintf(&b, "Host: %s\n", a.Host)
	}
	if len(hits) > 0 {
		b.WriteString("\n#### Relevant knowledge\n")
		for i, h := range hits {
			label := strings.TrimSpace(h.Title)
			if label == "" {
				label = h.Source
			}
			fmt.Fprintf(&b, "%d. [%s] %s\n\n", i+1, label, strings.TrimSpace(h.Text))
		}
	}
	for _, doc := range matchedDocs {
		fmt.Fprintf(&b, "\n#### %s\n%s\n", doc, strings.TrimSpace(docs[doc]))
	}
	if len(matchedFacts) > 0 {
		b.WriteString("\n#### Known facts\n")
		for _, f := range matchedFacts {
			fmt.Fprintf(&b, "- %s: %s\n", f.Key, f.Value)
		}
	}
	return strings.TrimSpace(b.String())
}

// fetchFull assembles the whole-knowledge reference block for one appliance: its
// operator notes, every accumulated knowledge doc, and the discrete facts. Used
// when there's no query, or as a fallback when a query matched nothing.
func (s servitorSource) fetchFull(udb Database, a Appliance) string {
	name := a.Name
	if name == "" {
		name = a.ID
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### System: %s\n", name)
	if a.Host != "" {
		fmt.Fprintf(&b, "Host: %s\n", a.Host)
	}
	if notes := strings.TrimSpace(a.Instructions); notes != "" {
		fmt.Fprintf(&b, "\nOperator notes:\n%s\n", notes)
	}

	// Knowledge docs servitor accumulated (overview/databases/filesystem/...).
	docs := allDocs(udb, a.ID)
	for _, doc := range knowledgeDocNames {
		if c := strings.TrimSpace(docs[doc]); c != "" {
			fmt.Fprintf(&b, "\n#### %s\n%s\n", doc, c)
		}
	}

	// Discrete facts (key/value, e.g. versions, ports, paths).
	if facts := factsForAppliance(udb, a.ID); len(facts) > 0 {
		b.WriteString("\n#### Known facts\n")
		for _, f := range facts {
			if f.Key == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s: %s\n", f.Key, f.Value)
		}
	}

	out := strings.TrimSpace(b.String())
	// Header-only (no docs, no facts) → nothing worth injecting.
	if !strings.Contains(out, "####") {
		return ""
	}
	return out
}

// ItemTools makes servitorSource a core.ReferenceToolProvider: an appliance the
// user attached to a guide (or any consumer) becomes three concrete, per-system
// tools in the agent's catalog rather than a generic pull_reference target. Two
// are read-only over the knowledge servitor already gathered; the third reaches
// the LIVE system. Names are slugged from the appliance so multiple attached
// systems don't collide. Returns nil when the appliance can't be resolved — the
// consumer then falls back to the flat Fetch/pull_reference path.
func (s servitorSource) ItemTools(user, itemID string) []AgentToolDef {
	if s.app == nil || s.app.DB == nil || user == "" || itemID == "" {
		return nil
	}
	a, _, _, ok := s.app.resolveAppliance(user, UserDB(s.app.DB, user), itemID)
	if !ok || a.ID == "" {
		return nil
	}
	name := a.Name
	if name == "" {
		name = a.ID
	}
	slug := RefToolSlug(name)
	if slug == "" {
		slug = RefToolSlug(a.ID)
	}
	if slug == "" {
		return nil
	}
	id := a.ID // capture for the closures; itemID and a.ID are the same value

	searchTool := AgentToolDef{
		Tool: Tool{
			Name:        "search_" + slug + "_knowledge",
			Description: fmt.Sprintf("Search the knowledge gohort has ALREADY gathered about the system %q — its mapped facts, structured docs, and linked collections — for material relevant to a query, and return the best matches. Read-only and instant: it does NOT touch the live machine. Use this FIRST when writing a guide section about %s; only reach for investigate_%s when you need something the gathered knowledge doesn't already contain.", name, name, slug),
			Parameters: map[string]ToolParam{
				"query": {Type: "string", Description: "What you're writing about — a focused topic, e.g. 'network interfaces and firewall zones'."},
			},
			Required: []string{"query"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			query := strings.TrimSpace(fmt.Sprint(args["query"]))
			txt := s.Fetch(context.Background(), user, id, query)
			if strings.TrimSpace(txt) == "" {
				return fmt.Sprintf("No gathered knowledge matches %q for %s. Try investigate_%s to gather it from the live system, or broaden the query.", query, name, slug), nil
			}
			return txt, nil
		},
	}

	factsTool := AgentToolDef{
		Tool: Tool{
			Name:        "get_" + slug + "_facts",
			Description: fmt.Sprintf("Return the discrete structured facts gohort has recorded about %q — versions, ports, paths, hostnames, service names — as a key/value list. Read-only and instant; no live access. Use to ground EXACT values in a guide section without re-investigating.", name),
			Caps:        []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			return s.factsBlock(user, id)
		},
	}

	investigateTool := AgentToolDef{
		Tool: Tool{
			Name:        "investigate_" + slug,
			Description: fmt.Sprintf("Investigate the LIVE system %q to answer a specific question, by dispatching gohort's investigator to run read-only SSH commands on it right now. Use this only when the gathered knowledge (search_%s_knowledge / get_%s_facts) doesn't already hold what a guide section needs — e.g. current config, running services, exact command output. Slow (tens of seconds) and it touches the real machine (read-only — any destructive command is auto-declined). Call it deliberately, then write the section grounded strictly in what it returns.", name, slug, slug),
			Parameters: map[string]ToolParam{
				"question": {Type: "string", Description: "The specific thing to find out on the live system, e.g. 'which TLS versions does the nginx config enable?'"},
			},
			Required: []string{"question"},
			Caps:     []Capability{CapNetwork, CapExecute},
		},
		Handler: func(args map[string]any) (string, error) {
			question := strings.TrimSpace(fmt.Sprint(args["question"]))
			if question == "" {
				return "", fmt.Errorf("question is required")
			}
			return s.app.InvestigateSync(context.Background(), user, id, question)
		},
	}

	return []AgentToolDef{searchTool, factsTool, investigateTool}
}

// factsBlock renders the appliance's discrete facts as a key/value list for the
// get_<system>_facts tool, resolved share-aware under the owner's store.
func (s servitorSource) factsBlock(user, itemID string) (string, error) {
	a, _, ownerUDB, ok := s.app.resolveAppliance(user, UserDB(s.app.DB, user), itemID)
	if !ok || ownerUDB == nil || a.ID == "" {
		return "", fmt.Errorf("system not found")
	}
	name := a.Name
	if name == "" {
		name = a.ID
	}
	facts := factsForAppliance(ownerUDB, a.ID)
	if len(facts) == 0 {
		return fmt.Sprintf("No structured facts recorded yet for %s. Use investigate_%s to gather them from the live system.", name, RefToolSlug(name)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Known facts for %s:\n", name)
	for _, f := range facts {
		if f.Key == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", f.Key, f.Value)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// InvestigateSync runs a one-shot, READ-ONLY investigation against an appliance
// and returns the lead investigator's final answer as plain text. It reuses the
// full runSession machinery (lead + SSH worker) but captures the final reply
// instead of streaming to a browser, and AUTO-DENIES every destructive command
// so an autonomous caller (the Guides co-author grounding a section, say) can
// never mutate the live system. Blocks until the investigation completes — treat
// it as slow (tens of seconds).
func (T *Servitor) InvestigateSync(ctx context.Context, user, applianceID, question string) (string, error) {
	if T == nil || T.DB == nil || user == "" || applianceID == "" {
		return "", fmt.Errorf("investigate: missing user or appliance")
	}
	q := strings.TrimSpace(question)
	if q == "" {
		return "", fmt.Errorf("investigate: empty question")
	}
	udb := UserDB(T.DB, user)
	appliance, ownerUser, _, found := T.resolveAppliance(user, udb, applianceID)
	if !found {
		return "", fmt.Errorf("investigate: appliance %q not found", applianceID)
	}
	label := appliance.Name + ": " + q
	if len(label) > 80 {
		label = label[:80]
	}
	sid := "guide-investigate-" + UUIDv4()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	probeSessions.Register(sid, label, cancel)
	sessionAppliances.Store(sid, appliance.ID)
	confirm := make(chan bool, 1)
	confirmChans.Store(sid, confirm)
	RegisterInjectionQueue(sid, "", "")
	// Auto-deny: feed `false` whenever runSession asks to confirm a destructive
	// command, so the investigation stays strictly read-only. The goroutine exits
	// with the run (buffered size 1; at most one denial sits queued at a time).
	go func() {
		for {
			select {
			case confirm <- false:
			case <-runCtx.Done():
				return
			}
		}
	}()
	// Synchronous: run the session INLINE (not `go`) so it blocks until the
	// investigation finishes, then pull the final reply from its recorded events.
	T.runSession(runCtx, sid, user, ownerUser, appliance, confirm, []Message{{Role: "user", Content: q}}, udb, false)
	events, _ := probeSessions.SnapshotEvents(sid)
	var reply, errText string
	for _, ev := range events {
		switch ev.Kind {
		case "reply":
			reply = ev.Text
		case "error":
			errText = ev.Text
		}
	}
	if strings.TrimSpace(reply) == "" {
		if strings.TrimSpace(errText) != "" {
			return "", fmt.Errorf("investigate: %s", errText)
		}
		return "", fmt.Errorf("investigate: no answer produced")
	}
	return reply, nil
}

// refQueryMatch reports whether text mentions any meaningful term (>=3 chars)
// from query, case-insensitively. A lightweight keyword filter for the
// structured docs and facts, which aren't embedded/searchable like collections.
func refQueryMatch(text, query string) bool {
	lt := strings.ToLower(text)
	for _, w := range strings.Fields(strings.ToLower(query)) {
		w = strings.Trim(w, ".,;:!?\"'()[]{}")
		if len(w) >= 3 && strings.Contains(lt, w) {
			return true
		}
	}
	return false
}
