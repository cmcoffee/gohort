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
	hits := searchAgentKnowledge(ctx, t.app.DB, t.user, t.agent.ID, generalTopic, query, 3, t.skillsActive, t.agent.AttachedCollections, ChunkScopeAll)
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

// linkEntitiesToolDef lets the model record a RELATIONSHIP between two named
// things (subject -[relation]-> object), auto-creating/merging the entities.
func (t *chatTurn) linkEntitiesToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "link_entities",
			Description: "Record a RELATIONSHIP between two named things in your graph memory — the structured layer for who/what connects to whom. State it as subject-relation-object: subject=\"Rory\", relation=\"works at\", object=\"Acme\". Entities are auto-created and merged by name/alias (so \"Rory\" and \"Rory Bartle\" become one node). Use this when you learn how named things relate — people to orgs, people to people, projects to owners, things to places.\n\n**Graph vs the other memory layers**: a RELATIONSHIP between named things → link_entities. A preference/directive that shapes every answer (\"user prefers metric\") → store_fact. Bulky reference material to recall later → memory_save. A document → knowledge_search.\n\nSet `replace`=true when this CORRECTS a single-valued relation (job, home, manager) so the old value is removed (\"works at Acme\" → \"works at Globex\"). Leave it off for relations that can have many values at once (knows, owns, member of). Put non-relational details (email, title, phone) in `subject_attrs`, not as fake relationships.",
			Parameters: map[string]ToolParam{
				"subject":      {Type: "string", Description: "The subject entity's name. e.g. \"Rory Bartle\"."},
				"subject_kind": {Type: "string", Description: "Subject type: person, org, project, place, or thing. Defaults to thing."},
				"relation":     {Type: "string", Description: "The relationship verb. e.g. \"works at\", \"reports to\", \"knows\", \"owns\", \"located in\"."},
				"object":       {Type: "string", Description: "The object entity's name. e.g. \"Acme\"."},
				"object_kind":  {Type: "string", Description: "Object type: person, org, project, place, or thing. Defaults to thing."},
				"subject_attrs": {Type: "object", Description: "Optional non-relational facts about the subject, as key/value strings. e.g. {\"email\": \"rory@acme.com\", \"title\": \"VP Eng\"}."},
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
			obj, _ := UpsertGraphEntity(t.udb, ns, stringArg(args, "object_kind"), object, nil, nil)
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

// recallAboutToolDef resolves an entity by name/alias and returns what the
// agent knows about it: attributes + relationships (1 hop, or 2 on request).
func (t *chatTurn) recallAboutToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "recall_about",
			Description: "Look up what you know about a named thing in your graph memory — its attributes and relationships — resolved by name or alias. Use it when a person/org/project/place comes up and you want the FACTS you've recorded (who they are, how they connect to others) instead of guessing. Returns the entity's attributes plus its relationships; ask for depth 2 to also see the neighbors' connections (one more hop). It ALSO folds in the top matching passages your knowledge store has about the entity, so you get the structured graph and the unstructured detail in one lookup. Read-only; pairs with link_entities (which records).",
			Parameters: map[string]ToolParam{
				"name":  {Type: "string", Description: "The entity to look up, by name or any alias. e.g. \"Rory\"."},
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
	return strings.TrimRight(b.String(), "\n")
}

// graphEndName resolves an entity ID to its display name (falls back to the ID).
func graphEndName(db Database, ns, id string) string {
	if e, ok := FindGraphEntity(db, ns, id); ok {
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
