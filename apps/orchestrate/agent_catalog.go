package orchestrate

// An agent's catalogue, read by another agent without running it.
//
// Attached as a Source, an agent offered only consult_<agent>, which RUNS it:
// a model call and a turn for every lookup. That is the right tool for a
// question that needs judgement, and the wrong one for "where does the
// billing service live", which the agent already wrote down. A Servitor
// system offers both: search what was gathered, instantly, then investigate
// when that is not enough. These are the same two steps for an agent, and what
// makes a sub-agent that keeps a catalogue worth asking first
// (docs/design/agent-archetypes.md).
//
// Read-only and with no model call. They read what the agent recorded: its
// pinned notes, its saved findings, its own documents and collections, and
// its graph. Never its conversations.
//
// Offered for the user's OWN agents only. A shared agent's memory reaches a
// recipient through its share switches, which answer a different question
// (what the agent may draw on when it runs for them); until a read path
// honours those, a shared agent stays consult-only.

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/textutil"
)

// catalogPerLayer bounds each part of a catalogue search, so one lookup stays
// a lookup rather than a dump of the agent's memory.
const catalogPerLayer = 6

// catalogTextCap bounds one passage in a result.
const catalogTextCap = 600

// readAgentCatalog searches what agent has recorded for query, for another
// agent to read. topic narrows findings and documents to one bucket.
func (T *OrchestrateApp) readAgentCatalog(ctx context.Context, owner string, agent AgentRecord, query, topic string) string {
	udb := UserDB(T.DB, owner)
	ns := factsNamespace(agent.ID)
	var qVec []float32
	if GetEmbeddingConfig().Enabled {
		ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if v, err := Embed(ectx, query); err == nil {
			qVec = v
		}
		cancel()
	}
	var sections []string

	// The structure first: "where does X live" is a graph answer when the
	// agent recorded one, and a passage only approximates it.
	for _, e := range GraphEntitiesMentionedIn(udb, ns, query, 3) {
		sections = append(sections, renderGraphRecall(udb, ns, e, 1))
	}

	if !agent.DisableExplicit {
		facts := SearchMemoryFactsVec(udb, ns, query, qVec)
		if len(facts) > catalogPerLayer {
			facts = facts[:catalogPerLayer]
		}
		if len(facts) > 0 {
			var b strings.Builder
			b.WriteString("Notes:\n")
			for _, f := range facts {
				fmt.Fprintf(&b, "- %s%s\n", strings.TrimSpace(f.Note), FactStalenessNote(f, time.Now()))
			}
			sections = append(sections, strings.TrimRight(b.String(), "\n"))
		}
	}

	scope := ChunkScopeAll
	if agent.DisableInferred {
		scope = ChunkScopeCuratedOnly
	}
	hits := searchAgentKnowledgeVec(ctx, T.DB, owner, "", false, agent.ID, normalizeTopic(topic), query, qVec,
		catalogPerLayer*2, nil, agent.AttachedCollections, scope)
	var findings, docs []SearchHit
	for _, h := range hits {
		if h.Score < RelevanceFloor {
			continue
		}
		if chunkProvenance(h.Source, h.ReportID) == "derived" {
			if len(findings) < catalogPerLayer {
				findings = append(findings, h)
			}
		} else if len(docs) < catalogPerLayer {
			docs = append(docs, h)
		}
	}
	if s := renderCatalogHits("Findings it recorded", findings); s != "" {
		sections = append(sections, s)
	}
	if s := renderCatalogHits("From its documents", docs); s != "" {
		sections = append(sections, s)
	}

	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n")
}

// renderCatalogHits lists passages under a heading, each with what it is from.
func renderCatalogHits(heading string, hits []SearchHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(heading + ":\n")
	for _, h := range hits {
		label := strings.TrimSpace(chFirst(h.Title, h.Section))
		text := strings.TrimSpace(h.Text)
		if r := []rune(text); len(r) > catalogTextCap {
			text = string(r[:catalogTextCap]) + "..."
		}
		if label != "" {
			fmt.Fprintf(&b, "- (%s) %s\n", label, text)
		} else {
			fmt.Fprintf(&b, "- %s\n", text)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// agentCatalogTools are the read-first tools an agent attached as a Source
// offers beside consult_<agent>. Nil for an agent the user does not own.
func (T *OrchestrateApp) agentCatalogTools(user string, agent AgentRecord, name, consultTool string) []AgentToolDef {
	if T == nil || strings.TrimSpace(agent.Owner) != user {
		return nil
	}
	slug := textutil.SnakeFromDisplay(name)
	if slug == "" {
		return nil
	}
	search := AgentToolDef{
		Tool: Tool{
			Name:        "search_" + slug + "_memory",
			Description: fmt.Sprintf("Search what %s has already recorded (its notes, saved findings, documents and the links it has mapped between things) for a query, and return the best matches. Instant and read-only: it does not run %s or reach anything live. Look here FIRST, before searching for something yourself: if %s has catalogued where it lives, this says so. Use %s only when this does not hold what you need.", name, name, name, consultTool),
			Parameters: map[string]ToolParam{
				"query": {Type: "string", Description: "What you are looking for, e.g. \"which repository holds the billing service\"."},
				"topic": {Type: "string", Description: "Optional: one of its topic buckets, to narrow findings and documents to it."},
			},
			Required: []string{"query"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			q := strings.TrimSpace(stringArg(args, "query"))
			if q == "" {
				return "", fmt.Errorf("query is required")
			}
			out := T.readAgentCatalog(ctx, user, agent, q, stringArg(args, "topic"))
			if out == "" {
				return fmt.Sprintf("%s has nothing recorded that matches %q. That is not evidence it does not exist: ask %s with %s, or search for it yourself.", name, q, name, consultTool), nil
			}
			return out, nil
		},
	}
	about := AgentToolDef{
		Tool: Tool{
			Name:        "recall_" + slug + "_about",
			Description: fmt.Sprintf("Everything %s has recorded about one named thing (a service, a repository, a team, a person), resolved by name or alias: what it is, what it is linked to, and the notes that name it. Instant and read-only. With no name, lists the things it has recorded.", name),
			Parameters: map[string]ToolParam{
				"name": {Type: "string", Description: "The thing to look up, by name or alias. Omit to list what it has recorded."},
			},
			Caps: []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return T.recallAgentAbout(user, agent, name, strings.TrimSpace(stringArg(args, "name"))), nil
		},
	}
	return []AgentToolDef{search, about}
}

// recallAgentAbout is recall_about against another agent's graph: one named
// thing, its links and the notes that name it, or with no name the things the
// graph holds.
func (T *OrchestrateApp) recallAgentAbout(owner string, agent AgentRecord, agentName, thing string) string {
	udb := UserDB(T.DB, owner)
	ns := factsNamespace(agent.ID)
	known := ListGraphEntities(udb, ns)
	if thing == "" {
		if len(known) == 0 {
			return agentName + " has not mapped any named things yet."
		}
		names := make([]string, 0, len(known))
		for _, e := range known {
			names = append(names, e.Name)
		}
		return fmt.Sprintf("%s has recorded %d thing(s): %s.", agentName, len(names), strings.Join(names, ", "))
	}
	e, ok := FindGraphEntity(udb, ns, thing)
	if !ok {
		if len(known) == 0 {
			return fmt.Sprintf("%s has nothing mapped about %q (it has not mapped any named things yet).", agentName, thing)
		}
		names := make([]string, 0, 12)
		for _, k := range known {
			if names = append(names, k.Name); len(names) >= 12 {
				break
			}
		}
		return fmt.Sprintf("%s has nothing mapped under %q. Things it has: %s.", agentName, thing, strings.Join(names, ", "))
	}
	out := renderGraphRecall(udb, ns, e, 1)
	if !agent.DisableExplicit {
		var notes []string
		for _, f := range ListMemoryFacts(udb, ns) {
			if GraphEntityMentionedIn(e, f.Note) {
				notes = append(notes, "- "+strings.TrimSpace(f.Note))
			}
		}
		if len(notes) > 0 {
			out += "\n\nNotes that name it:\n" + strings.Join(notes, "\n")
		}
	}
	return out
}
