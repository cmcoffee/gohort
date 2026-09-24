package codewriter

// A writer (the panel's saved "mode") as a portable artifact: its name,
// language, the sources it writes against and the collections it searches.
//
// An agent source is stored by the agent's id, and an agent is reborn under a
// fresh id when it is imported, so the recipe names it instead: export swaps
// the id for the agent's name and import swaps it back, resolving the name in
// the importer's own agents (which may arrive in the same bundle, hence
// ImportsLate). A name that resolves to nothing is kept as the name: fetching
// resolves an agent by name as well as id. Every other source kind is carried
// as it is and declared, so the import can say it is missing.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const agentRefKind = "agent"

type portableWriter struct {
	Name        string              `json:"name"`
	Desc        string              `json:"description,omitempty"`
	Lang        string              `json:"lang,omitempty"`
	Sources     ReferenceSelections `json:"references,omitempty"`
	Collections []string            `json:"collections,omitempty"`
}

type writerArtifact struct{ app *CodeWriterAgent }

func (*writerArtifact) ArtifactType() string { return "writer" }
func (*writerArtifact) UserImportable() bool { return true }
func (*writerArtifact) ImportsLate() bool    { return true }

func (w *writerArtifact) store(owner string) Database {
	if w.app == nil || w.app.DB == nil || strings.TrimSpace(owner) == "" {
		return nil
	}
	return UserDB(w.app.DB, owner)
}

func (w *writerArtifact) all(owner string) []WriterRecord {
	udb := w.store(owner)
	if udb == nil {
		return nil
	}
	var out []WriterRecord
	for _, k := range udb.Keys(writerTable) {
		if rec, ok := loadWriter(udb, k); ok {
			out = append(out, rec)
		}
	}
	return out
}

func (w *writerArtifact) find(owner, nameOrID string) (WriterRecord, bool) {
	key := strings.TrimSpace(nameOrID)
	if rec, ok := loadWriter(w.store(owner), key); ok {
		return rec, true
	}
	for _, rec := range w.all(owner) {
		if strings.EqualFold(strings.TrimSpace(rec.Name), key) {
			return rec, true
		}
	}
	return WriterRecord{}, false
}

func (w *writerArtifact) ListArtifacts(_ Database) []ArtifactSel {
	if AuthDB == nil {
		return nil
	}
	adb := AuthDB()
	if adb == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range AuthListUsers(adb) {
		for _, rec := range w.all(u.Username) {
			out = append(out, ArtifactSel{Type: "writer", Name: rec.Name, Owner: u.Username})
		}
	}
	return out
}

// agentItems lists the owner's agents as the reference picker sees them.
func agentItems(owner string) []ReferenceItem {
	for _, g := range ReferenceGroups(owner) {
		if g.Kind == agentRefKind {
			return g.Items
		}
	}
	return nil
}

// rewriteAgents maps each agent source's ItemID through fn.
func rewriteAgents(sources ReferenceSelections, fn func(string) string) ReferenceSelections {
	out := make(ReferenceSelections, 0, len(sources))
	for _, s := range sources {
		if s.Kind == agentRefKind {
			s.ItemID = fn(s.ItemID)
		}
		out = append(out, s)
	}
	return out
}

func (w *writerArtifact) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	rec, ok := w.find(owner, name)
	if !ok {
		return nil, fmt.Errorf("no writer named %q for user %q", name, owner)
	}
	items := agentItems(owner)
	sources := rewriteAgents(rec.Sources, func(id string) string {
		for _, it := range items {
			if it.ID == id {
				return it.Name
			}
		}
		return id
	})
	return json.Marshal(portableWriter{
		Name: rec.Name, Desc: rec.Desc, Lang: rec.Lang, Sources: sources, Collections: rec.Collections,
	})
}

func (w *writerArtifact) ImportArtifact(_ Database, recipe json.RawMessage, owner string) (string, string, error) {
	udb := w.store(owner)
	if udb == nil {
		return "", "", Error("writer import requires an owner")
	}
	var p portableWriter
	if err := json.Unmarshal(recipe, &p); err != nil {
		return "", "", fmt.Errorf("invalid writer recipe: %w", err)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return "", "", Error("missing writer name")
	}
	if _, exists := w.find(owner, name); exists {
		return name, "a writer with this name already exists", nil
	}
	items := agentItems(owner)
	rec := WriterRecord{
		ID: UUIDv4(), Name: name, Desc: p.Desc, Lang: p.Lang, Collections: p.Collections,
		Sources: rewriteAgents(p.Sources, func(ref string) string {
			for _, it := range items {
				if strings.EqualFold(it.Name, ref) {
					return it.ID
				}
			}
			return ref
		}),
		Date: time.Now().Format(time.RFC3339),
	}
	udb.Set(writerTable, rec.ID, rec)
	return name, "", nil
}

func writerDeps(p portableWriter, owner string, agentName func(string) string) []ArtifactSel {
	var out []ArtifactSel
	for _, s := range p.Sources {
		if s.Kind == agentRefKind {
			out = append(out, ArtifactSel{Type: "agent", Name: agentName(s.ItemID), Owner: owner})
			continue
		}
		if name := RefSourceSelName(s.Kind, s.ItemID); name != ":" {
			out = append(out, ArtifactSel{Type: "reference_source", Name: name, Owner: owner})
		}
	}
	for _, id := range p.Collections {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, ArtifactSel{Type: "collection", Name: id, Owner: owner})
		}
	}
	return out
}

func (w *writerArtifact) Dependencies(_ Database, name, owner string) []ArtifactSel {
	rec, ok := w.find(owner, name)
	if !ok {
		return nil
	}
	items := agentItems(owner)
	return writerDeps(portableWriter{Sources: rec.Sources, Collections: rec.Collections}, owner, func(id string) string {
		for _, it := range items {
			if it.ID == id {
				return it.Name
			}
		}
		return id
	})
}

func (w *writerArtifact) RecipeDependencies(_ Database, recipe json.RawMessage, owner string, _ func(typ, name string) bool) []ArtifactSel {
	var p portableWriter
	if json.Unmarshal(recipe, &p) != nil {
		return nil
	}
	return writerDeps(p, owner, func(s string) string { return s })
}
