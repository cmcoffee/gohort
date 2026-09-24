package orchestrate

// An agent's memory as a portable artifact, travelling with the agent only
// when asked for (opt-in; the export dialog offers it unticked). It is what the
// agent has learned for this person, which is exactly why it is not part of
// the agent's recipe: a recipe handed to a colleague should not hand over what
// the agent knows about you.
//
// What travels, per agent and per owned sub-agent:
//   - Explicit memory: the live facts, text only (embeddings are this
//     install's model). A fact somebody OTHER than the owner stated (a
//     channel contact) stays behind: it is that person's claim.
//   - Working notes: the text.
//   - Derived findings: the agent's own learned documents, text only, re-
//     embedded on the far side.
//
// What does not: uploads and pasted attachments (the person's files, and
// unbounded), kept images (references that do not resolve elsewhere),
// migrated phantom-chat knowledge (other people's conversations), the graph
// (auto-extracted third parties), subject-scoped memory, sessions.
//
// Import attaches it to the agent of that name in the importer's store, and
// only when that agent has no memory yet: if the agent in the same bundle was
// skipped because the name was taken, the memory would otherwise land on an
// unrelated agent.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

type portableFact struct {
	Note    string    `json:"note"`
	Created time.Time `json:"created,omitempty"`
}

type portableFinding struct {
	Topic string `json:"topic,omitempty"`
	Title string `json:"title,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Text  string `json:"text"`
}

type agentMemoryPart struct {
	SubAgent string            `json:"sub_agent,omitempty"` // "" = the agent itself
	Facts    []portableFact    `json:"facts,omitempty"`
	Notes    string            `json:"notes,omitempty"`
	Findings []portableFinding `json:"findings,omitempty"`
}

func (p agentMemoryPart) empty() bool {
	return len(p.Facts) == 0 && strings.TrimSpace(p.Notes) == "" && len(p.Findings) == 0
}

type agentMemoryRecipe struct {
	Name   string            `json:"name"`         // the agent's name
	Schema int               `json:"agent_memory"` // sniff key
	Parts  []agentMemoryPart `json:"parts"`
}

type agentMemoryArtifact struct{ app *OrchestrateApp }

// RegisterAgentMemoryArtifactType registers "agent_memory". Called from
// Routes, beside the agent type it travels with.
func RegisterAgentMemoryArtifactType(app *OrchestrateApp) {
	RegisterArtifactType(&agentMemoryArtifact{app: app})
}

func (*agentMemoryArtifact) ArtifactType() string  { return "agent_memory" }
func (*agentMemoryArtifact) UserImportable() bool  { return true }
func (*agentMemoryArtifact) OptInDependency() bool { return true }
func (*agentMemoryArtifact) ImportsLate() bool     { return true }

func (*agentMemoryArtifact) SniffsRecipe(fields map[string]json.RawMessage) bool {
	_, ok := fields["agent_memory"]
	return ok
}

func (m *agentMemoryArtifact) store(owner string) Database {
	if m.app == nil || m.app.DB == nil || strings.TrimSpace(owner) == "" {
		return nil
	}
	return UserDB(m.app.DB, owner)
}

// topAgent finds a top-level agent the owner owns, by name or id.
func topAgent(udb Database, owner, key string) (AgentRecord, bool) {
	key = strings.TrimSpace(key)
	for _, rec := range listAgents(udb, owner) {
		if rec.OwnedBy == "" && rec.Owner == owner && (rec.Name == key || rec.ID == key) {
			return rec, true
		}
	}
	return AgentRecord{}, false
}

// travellingFinding reports whether a knowledge chunk is one of the agent's
// own derived findings (see the file comment for what stays behind).
func travellingFinding(c EmbeddedChunk, prefix string) bool {
	if !sourceInScope(c.Source, prefix) || strings.HasSuffix(c.Source, ":attachments") {
		return false
	}
	return strings.HasPrefix(c.ReportID, "orch-know-") && !strings.Contains(c.ReportID, "-keptimg-")
}

// memoryPart reads one agent's travelling memory.
func memoryPart(udb Database, owner string, a AgentRecord) agentMemoryPart {
	ns := factsNamespace(a.ID)
	part := agentMemoryPart{}
	for _, f := range ListMemoryFacts(udb, ns) {
		if !(f.SpeakerIsOwner || strings.TrimSpace(f.Speaker) == "" && strings.TrimSpace(f.SpeakerHandle) == "") {
			continue
		}
		if note := strings.TrimSpace(f.Note); note != "" {
			part.Facts = append(part.Facts, portableFact{Note: note, Created: f.Created})
		}
	}
	part.Notes = strings.TrimSpace(LoadOperatingNotes(udb, ns).Text)
	if VectorDB == nil {
		return part
	}
	prefix := agentKnowledgePrefix(owner, a.ID)
	byDoc := map[string][]EmbeddedChunk{}
	for _, c := range ChunksWhere(VectorDB, func(c EmbeddedChunk) bool { return travellingFinding(c, prefix) }) {
		byDoc[c.ReportID] = append(byDoc[c.ReportID], c)
	}
	docs := make([]string, 0, len(byDoc))
	for id := range byDoc {
		docs = append(docs, id)
	}
	sort.Strings(docs)
	for _, id := range docs {
		chunks := byDoc[id]
		sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].Ord < chunks[j].Ord })
		var b strings.Builder
		for _, c := range chunks {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(strings.TrimSpace(c.Text))
		}
		first := chunks[0]
		topic := strings.TrimPrefix(strings.TrimPrefix(first.Source, prefix), ":")
		part.Findings = append(part.Findings, portableFinding{
			Topic: topic, Title: first.Title, Kind: first.Kind, Text: b.String(),
		})
	}
	return part
}

// recipeFor reads an agent's memory and its owned sub-agents'.
func (m *agentMemoryArtifact) recipeFor(owner, key string) (agentMemoryRecipe, bool) {
	udb := m.store(owner)
	if udb == nil {
		return agentMemoryRecipe{}, false
	}
	a, ok := topAgent(udb, owner, key)
	if !ok {
		return agentMemoryRecipe{}, false
	}
	r := agentMemoryRecipe{Name: a.Name, Schema: 1}
	if p := memoryPart(udb, owner, a); !p.empty() {
		r.Parts = append(r.Parts, p)
	}
	for _, s := range listAgents(udb, owner) {
		if s.OwnedBy != a.ID {
			continue
		}
		if p := memoryPart(udb, owner, s); !p.empty() {
			p.SubAgent = s.Name
			r.Parts = append(r.Parts, p)
		}
	}
	return r, len(r.Parts) > 0
}

// ListArtifacts lists the agents that have memory, so an account backup
// carries it.
func (m *agentMemoryArtifact) ListArtifacts(_ Database) []ArtifactSel {
	if AuthDB == nil {
		return nil
	}
	adb := AuthDB()
	if adb == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range AuthListUsers(adb) {
		udb := m.store(u.Username)
		if udb == nil {
			continue
		}
		for _, a := range listAgents(udb, u.Username) {
			if a.OwnedBy != "" || a.Owner != u.Username || isAppAgent(a.ID) {
				continue
			}
			if _, ok := m.recipeFor(u.Username, a.ID); ok {
				out = append(out, ArtifactSel{Type: "agent_memory", Name: a.Name, Owner: u.Username})
			}
		}
	}
	return out
}

// ExportArtifact errors for an agent with no memory, so a closure never carries
// an empty one and the existence probe agrees with export.
func (m *agentMemoryArtifact) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	r, ok := m.recipeFor(owner, name)
	if !ok {
		return nil, fmt.Errorf("agent %q has no memory to export", name)
	}
	return json.Marshal(r)
}

// Dependencies: memory belongs to its agent, so exporting it alone brings the
// agent too.
func (m *agentMemoryArtifact) Dependencies(_ Database, name, owner string) []ArtifactSel {
	return []ArtifactSel{{Type: "agent", Name: name, Owner: owner}}
}

func (m *agentMemoryArtifact) RecipeDependencies(_ Database, recipe json.RawMessage, owner string, _ func(typ, name string) bool) []ArtifactSel {
	var r agentMemoryRecipe
	if json.Unmarshal(recipe, &r) != nil || strings.TrimSpace(r.Name) == "" {
		return nil
	}
	return []ArtifactSel{{Type: "agent", Name: r.Name, Owner: owner}}
}

func (m *agentMemoryArtifact) ImportArtifact(_ Database, recipe json.RawMessage, owner string) (string, string, error) {
	udb := m.store(owner)
	if udb == nil {
		return "", "", Error("memory import requires an owner")
	}
	var r agentMemoryRecipe
	if err := json.Unmarshal(recipe, &r); err != nil {
		return "", "", fmt.Errorf("invalid agent memory: %w", err)
	}
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return "", "", Error("agent memory names no agent")
	}
	parent, ok := topAgent(udb, owner, name)
	if !ok {
		return name, "no agent named " + name + " here: import or create the agent first", nil
	}
	// Resolve every part's target before writing anything.
	type target struct {
		agent AgentRecord
		part  agentMemoryPart
	}
	var targets []target
	for _, p := range r.Parts {
		if p.SubAgent == "" {
			targets = append(targets, target{parent, p})
			continue
		}
		for _, s := range listAgents(udb, owner) {
			if s.OwnedBy == parent.ID && s.Name == p.SubAgent {
				targets = append(targets, target{s, p})
				break
			}
		}
	}
	for _, t := range targets {
		if !memoryPart(udb, owner, t.agent).empty() {
			return name, "agent " + t.agent.Name + " already has memory here, so this was not merged into it", nil
		}
	}
	for _, t := range targets {
		if n := strings.TrimSpace(t.part.Notes); n != "" {
			SaveOperatingNotes(udb, factsNamespace(t.agent.ID), n)
		}
	}
	// Facts can each cost a relevance check, and findings are re-embedded, so
	// both run after the request returns. The work is the importer's own and
	// must not die with the page.
	ctx := context.WithoutCancel(AppContext())
	go func() {
		facts, docs := 0, 0
		for _, t := range targets {
			ns := factsNamespace(t.agent.ID)
			for _, f := range t.part.Facts {
				if res := StoreMemoryFactP(udb, ns, f.Note, FactWritePolicy{Source: MemSourceImported}); res.Reason == FactStored {
					facts++
				}
			}
			if VectorDB == nil {
				continue
			}
			for i, d := range t.part.Findings {
				if strings.TrimSpace(d.Text) == "" {
					continue
				}
				src := knowledgeSource(owner, t.agent.ID, d.Topic)
				reportID := fmt.Sprintf("orch-know-%s-%s-imp%d", t.agent.ID, owner, i)
				IngestReportTitled(ctx, VectorDB, src, reportID, d.Title, d.Text, d.Kind)
				docs++
			}
		}
		Log("[orchestrate.memory] %s: imported memory for %q: %d fact(s), %d finding(s) indexed", owner, name, facts, docs)
	}()
	return name, "", nil
}
