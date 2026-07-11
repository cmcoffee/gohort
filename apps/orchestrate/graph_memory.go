// link_entities / recall_about — the graph-memory layer for orchestrate
// agents (core/graphstore.go owns the store). The structured complement to
// store_fact: where a fact is a flat always-in-prompt note, a graph link is a
// relationship between named things the agent can traverse on demand. Pull-
// only (recall_about), so it costs no prompt tokens until used.
//
// Scope: core.GraphEntity / GraphEdge rows under namespace "agent:<id>" in
// the caller's per-user sub-store (udb) — same per-(user, agent) isolation as
// the factstore. Attached alongside store_fact (gated by explicitOff).

package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// entityRelatedPassages bridges graph → vector: it runs the hybrid knowledge
// recall on the entity's name + aliases and renders the top few hits, so a
// recall_about returns the structured graph AND the unstructured passages the
// knowledge store holds about the same entity. The entity name is the live
// join key — no stored cross-link to keep in sync. Returns "" when nothing
// matches or there's no corpus.
func (t *chatTurn) entityRelatedPassages(e GraphEntity) string {
	query := strings.TrimSpace(e.Name + " " + strings.Join(e.Aliases, " "))
	if query == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout())
	defer cancel()
	hits := searchAgentKnowledge(ctx, t.app.DB, t.user, t.ownerUser, t.agent.ID, generalTopic, query, 3, t.skillsActive, t.agent.AttachedCollections, ChunkScopeAll)
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Related passages (from your knowledge)\n")
	b.WriteString("Top knowledge-store matches about this entity. Cite specifics ONLY from these, not from memory.\n")
	for _, h := range hits {
		title := chFirst(h.Title, h.Section, h.Source, "passage")
		loc := ""
		if strings.TrimSpace(h.Locator) != "" {
			loc = " (" + h.Locator + ")"
		}
		b.WriteString("- " + title + loc + ": " + truncateObs(h.Text, 280) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// entityRelatedFacts bridges graph → Explicit memory: it surfaces the Explicit
// facts whose note names this entity (by canonical name or any alias), so one
// recall_about returns the graph relationships AND the flat facts about the same
// thing — the model no longer has to scan the whole always-on fact block to find
// the ones relevant to this entity. Read-only, name-as-join-key like
// entityRelatedPassages; facts share the entity's namespace. Returns "" when
// nothing matches. Short aliases (<3 chars) are skipped to avoid over-matching.
func (t *chatTurn) entityRelatedFacts(e GraphEntity) string {
	facts := ListMemoryFacts(t.udb, factsNamespace(t.agent.ID))
	var matched []string
	for _, f := range facts {
		if GraphEntityMentionedIn(e, f.Note) {
			matched = append(matched, f.Note)
			if len(matched) >= 8 {
				break
			}
		}
	}
	if len(matched) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Recorded facts mentioning " + e.Name + "\n")
	b.WriteString("Explicit-memory facts that name this entity.\n")
	for _, m := range matched {
		b.WriteString("- " + m + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// passageRelatedGraph bridges Reference → graph: given recalled passage text, it
// finds the graph entities those passages name and renders their relationships,
// so a memory_search returns the unstructured recollection AND the structured
// graph about the same things. The exact mirror of entityRelatedPassages (which
// runs graph → Reference). Name-as-join-key like the other bridges; no stored
// cross-link. Capped at a few entities to bound the appended tokens. Returns ""
// when the passages name no known entity.
func (t *chatTurn) passageRelatedGraph(text string) string {
	ns := factsNamespace(t.agent.ID)
	ents := GraphEntitiesMentionedIn(t.udb, ns, text, 3)
	if len(ents) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Related graph relationships\n")
	b.WriteString("Structured relationships you've recorded about entities named in these passages.\n")
	for _, e := range ents {
		b.WriteString(renderGraphRecall(t.udb, ns, e, 1))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// factEntityNudge bridges Explicit → graph the cheapest way there is: a pointer,
// no extra retrieval. Given a fact note and a pre-listed set of graph entities, it
// returns a " (recall_about \"X\" for its relationships)" suffix for the first
// known entity the note names — so a search_facts result quietly reveals when a
// flat fact has structured relationships behind it. Returns "" when the note names
// no known entity. Callers pass the entity slice so a multi-fact render lists the
// namespace once, not per row. Deliberately NOT applied to the always-in-prompt
// fact block: that stays lean (and core-side, with no graph coupling); the nudge
// belongs on the pull-only search path.
func factEntityNudge(entities []GraphEntity, note string) string {
	for _, e := range entities {
		if GraphEntityMentionedIn(e, note) {
			return fmt.Sprintf(" (recall_about %q for its relationships)", e.Name)
		}
	}
	return ""
}

// linkEntitiesToolDef lets the model record a RELATIONSHIP between two named
// things (subject -[relation]-> object), auto-creating/merging the entities.
func (t *chatTurn) linkEntitiesToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "link_entities",
			Description: "Record a RELATIONSHIP between two named things in your graph memory — the structured layer for who/what connects to whom. State it as subject-relation-object: subject=\"Robin\", relation=\"works at\", object=\"Acme\". Entities are auto-created and merged by name/alias (so \"Robin\" and \"Robin Vale\" become one node). Use this when you learn how named things relate — people to orgs, people to people, projects to owners, things to places.\n\n**Graph vs the other memory layers**: a RELATIONSHIP between named things → link_entities. A preference/directive that shapes every answer (\"user prefers metric\") → store_fact. Bulky reference material to recall later → memory_save. A document → knowledge_search.\n\nSet `replace`=true when this CORRECTS a single-valued relation (job, home, manager) so the old value is removed (\"works at Acme\" → \"works at Globex\"). Leave it off for relations that can have many values at once (knows, owns, member of). Put non-relational details (email, title, phone) in `subject_attrs`, not as fake relationships.",
			Parameters: map[string]ToolParam{
				"subject":       {Type: "string", Description: "The subject entity's name. e.g. \"Robin Vale\"."},
				"subject_kind":  {Type: "string", Description: "Subject type: person, org, project, place, or thing. Defaults to thing."},
				"relation":      {Type: "string", Description: "The relationship verb. e.g. \"works at\", \"reports to\", \"knows\", \"owns\", \"located in\"."},
				"object":        {Type: "string", Description: "The object entity's name. e.g. \"Acme\"."},
				"object_kind":   {Type: "string", Description: "Object type: person, org, project, place, or thing. Defaults to thing."},
				"object_attrs":  {Type: "object", Description: "Optional non-relational facts about the object, as key/value strings. e.g. {\"industry\": \"fintech\", \"hq\": \"Berlin\"}."},
				"subject_attrs": {Type: "object", Description: "Optional non-relational facts about the subject, as key/value strings. e.g. {\"email\": \"robin@acme.com\", \"title\": \"VP Eng\"}."},
				"note":          {Type: "string", Description: "Optional short qualifier on the relationship. e.g. \"since 2024\"."},
				"replace":       {Type: "boolean", Description: "True if this corrects a SINGLE-VALUED relation (removes the prior value for this subject+relation). Omit for multi-valued relations."},
			},
			Required: []string{"subject", "relation", "object"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			subject := strings.TrimSpace(stringArg(args, "subject"))
			relation := strings.TrimSpace(stringArg(args, "relation"))
			object := strings.TrimSpace(stringArg(args, "object"))
			if subject == "" || relation == "" || object == "" {
				return "", errors.New("subject, relation, and object are required")
			}
			ns := factsNamespace(t.agent.ID)
			subj, _ := UpsertGraphEntity(t.udb, ns, stringArg(args, "subject_kind"), subject, nil, attrsArg(args, "subject_attrs"))
			obj, _ := UpsertGraphEntity(t.udb, ns, stringArg(args, "object_kind"), object, nil, attrsArg(args, "object_attrs"))
			if subj.ID == "" || obj.ID == "" {
				return "", errors.New("failed to record entities")
			}
			replace := oArgBool(args, "replace")
			edge := LinkGraphEdge(t.udb, ns, subj.ID, relation, obj.ID, stringArg(args, "note"), replace)
			rel := strings.ReplaceAll(edge.Rel, "_", " ")
			msg := fmt.Sprintf("Linked: %s → %s → %s.", subj.Name, rel, obj.Name)
			if replace {
				msg += " Replaced any prior value for that relation."
			}
			return msg, nil
		},
	}
}

// forgetGraphToolDef lets the model delete from graph memory in-band — either a
// whole entity (by name, with every edge touching it) or a single relationship
// (subject-relation-object). The deletion counterpart to link_entities,
// mirroring forget_fact / memory(action="forget") in the other two layers so
// the agent can self-correct stale nodes without the admin UI.
func (t *chatTurn) forgetGraphToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "forget_graph",
			Description: "Delete from your graph memory when something you recorded is wrong or stale. Two modes:\n" +
				"- Remove ONE relationship: pass subject, relation, object (the same three you'd give link_entities) — e.g. subject=\"Robin\", relation=\"works at\", object=\"Acme\" removes just that edge, leaving both entities.\n" +
				"- Remove a WHOLE entity: pass only name — deletes that node and every relationship touching it (use when a person/org/project shouldn't be in the graph at all).\n" +
				"Resolves names/aliases like recall_about. This is the deletion counterpart to link_entities — use it to fix mistakes instead of leaving stale nodes to resurface in recall_about.",
			Parameters: map[string]ToolParam{
				"name":     {Type: "string", Description: "Entity to delete entirely, WITH all its relationships, by name or alias. Use this OR subject/relation/object, not both."},
				"subject":  {Type: "string", Description: "For removing one relationship: the subject entity name (as in link_entities)."},
				"relation": {Type: "string", Description: "For removing one relationship: the relationship verb, e.g. \"works at\"."},
				"object":   {Type: "string", Description: "For removing one relationship: the object entity name."},
			},
			Caps: []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			ns := factsNamespace(t.agent.ID)
			subject := strings.TrimSpace(stringArg(args, "subject"))
			relation := strings.TrimSpace(stringArg(args, "relation"))
			object := strings.TrimSpace(stringArg(args, "object"))
			name := strings.TrimSpace(stringArg(args, "name"))

			// Edge mode — remove one relationship, keep the entities.
			if subject != "" || relation != "" || object != "" {
				if subject == "" || relation == "" || object == "" {
					return "", errors.New("to remove a relationship, provide subject, relation, AND object (or pass name alone to delete a whole entity)")
				}
				subj, ok := FindGraphEntity(t.udb, ns, subject)
				if !ok {
					return fmt.Sprintf("No entity named %q in your graph memory — nothing to unlink.", subject), nil
				}
				obj, ok := FindGraphEntity(t.udb, ns, object)
				if !ok {
					return fmt.Sprintf("No entity named %q in your graph memory — nothing to unlink.", object), nil
				}
				if !DeleteGraphEdge(t.udb, ns, subj.ID, relation, obj.ID) {
					return fmt.Sprintf("No %q relationship from %s to %s to remove — it may already be gone.", relation, subj.Name, obj.Name), nil
				}
				return fmt.Sprintf("Removed relationship: %s → %s → %s. Both entities remain.", subj.Name, relation, obj.Name), nil
			}

			// Entity mode — delete the node and every edge touching it.
			if name == "" {
				return "", errors.New("provide name (to delete a whole entity) or subject/relation/object (to remove one relationship)")
			}
			e, ok := FindGraphEntity(t.udb, ns, name)
			if !ok {
				return fmt.Sprintf("Nothing in your graph memory named %q — nothing to delete.", name), nil
			}
			if !DeleteGraphEntity(t.udb, ns, e.ID) {
				return fmt.Sprintf("Could not delete %q — it may already have been removed.", e.Name), nil
			}
			return fmt.Sprintf("Deleted entity %q and all its relationships from graph memory.", e.Name), nil
		},
	}
}

// recallAboutToolDef resolves an entity by name/alias and returns what the
// agent knows about it: attributes + relationships (1 hop, or 2 on request).
func (t *chatTurn) recallAboutToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "recall_about",
			Description: "Look up EVERYTHING you know about a named thing in one call, resolved by name or alias. Returns the entity's attributes and relationships from graph memory (ask depth 2 to also see the neighbors' connections), PLUS the Explicit-memory facts that name it, PLUS the top matching passages from your knowledge store — so you get the structured graph, the flat facts, and the unstructured detail together instead of checking each layer separately. Use it whenever a person/org/project/place comes up and you want the recorded truth instead of guessing. Read-only; pairs with link_entities (which records).",
			Parameters: map[string]ToolParam{
				"name":  {Type: "string", Description: "The entity to look up, by name or any alias. e.g. \"Robin\"."},
				"depth": {Type: "integer", Description: "How many hops to expand: 1 (default, the entity and its direct relationships) or 2 (also the neighbors' relationships)."},
			},
			Required: []string{"name"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			name := strings.TrimSpace(stringArg(args, "name"))
			if name == "" {
				return "", errors.New("name is required")
			}
			ns := factsNamespace(t.agent.ID)
			e, ok := FindGraphEntity(t.udb, ns, name)
			if !ok {
				// Offer a few known names so the model can recognize a near-miss
				// instead of concluding it knows nothing.
				known := ListGraphEntities(t.udb, ns)
				if len(known) == 0 {
					return fmt.Sprintf("Nothing in your graph memory about %q (your graph is empty). Record relationships with link_entities as you learn them.", name), nil
				}
				names := make([]string, 0, len(known))
				for _, k := range known {
					names = append(names, k.Name)
					if len(names) >= 12 {
						break
					}
				}
				return fmt.Sprintf("No entity matching %q. Known entities: %s.", name, strings.Join(names, ", ")), nil
			}
			depth := oArgInt(args, "depth")
			if depth < 1 {
				depth = 1
			}
			if depth > 2 {
				depth = 2
			}
			out := renderGraphRecall(t.udb, ns, e, depth)
			// Graph → Explicit bridge: fold in the flat facts that name this
			// entity, so a focused recall surfaces them without scanning the
			// whole always-on fact block.
			if facts := t.entityRelatedFacts(e); facts != "" {
				out += "\n\n" + facts
			}
			// Graph → vector bridge: fold in the top passages the knowledge
			// store has about this entity, so one recall returns structured +
			// unstructured together.
			if passages := t.entityRelatedPassages(e); passages != "" {
				out += "\n\n" + passages
			}
			return out, nil
		},
	}
}

// renderGraphRecall formats an entity and its relationships for the LLM.
func renderGraphRecall(db Database, ns string, e GraphEntity, depth int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s", e.Name)
	if e.Kind != "" {
		fmt.Fprintf(&b, " (%s)", e.Kind)
	}
	b.WriteString("\n")
	if len(e.Aliases) > 0 {
		fmt.Fprintf(&b, "- Also known as: %s\n", strings.Join(e.Aliases, ", "))
	}
	if len(e.Attrs) > 0 {
		keys := make([]string, 0, len(e.Attrs))
		for k := range e.Attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "- %s: %s\n", k, e.Attrs[k])
		}
	}
	out := GraphEdgesFrom(db, ns, e.ID)
	in := GraphEdgesTo(db, ns, e.ID)
	if len(out) == 0 && len(in) == 0 {
		b.WriteString("- (no relationships recorded yet)\n")
	}
	for _, ed := range out {
		name := graphEndName(db, ns, ed.To)
		line := fmt.Sprintf("- %s → %s", strings.ReplaceAll(ed.Rel, "_", " "), name)
		if ed.Note != "" {
			line += " (" + ed.Note + ")"
		}
		b.WriteString(line + "\n")
		if depth >= 2 {
			for _, e2 := range GraphEdgesFrom(db, ns, ed.To) {
				if e2.To == e.ID {
					continue // don't loop straight back to the subject
				}
				fmt.Fprintf(&b, "    - %s → %s → %s\n", name, strings.ReplaceAll(e2.Rel, "_", " "), graphEndName(db, ns, e2.To))
			}
		}
	}
	for _, ed := range in {
		fmt.Fprintf(&b, "- %s → %s → (this)\n", graphEndName(db, ns, ed.From), strings.ReplaceAll(ed.Rel, "_", " "))
	}
	// Past (superseded) relationships — a corrected single-valued relation keeps a
	// validity window so recall can distinguish "used to" from "currently."
	for _, ed := range RetiredGraphEdgesFrom(db, ns, e.ID) {
		line := fmt.Sprintf("- (past) %s → %s", strings.ReplaceAll(ed.Rel, "_", " "), graphEndName(db, ns, ed.To))
		if !ed.RetiredAt.IsZero() {
			line += " (until " + ed.RetiredAt.Format("2006-01-02") + ")"
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// graphEndName resolves an entity ID to its display name (falls back to the ID).
// Uses the O(1) by-ID getter — the arg is an ID, so FindGraphEntity (name/alias
// resolution via full scan) would be both slower and semantically wrong here.
func graphEndName(db Database, ns, id string) string {
	if e, ok := GetGraphEntity(db, ns, id); ok {
		return e.Name
	}
	return id
}

// attrsArg reads an object-typed argument into a string map.
func attrsArg(args map[string]any, key string) map[string]string {
	raw, ok := args[key].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return out
}
