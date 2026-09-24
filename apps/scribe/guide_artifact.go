package scribe

// A guide (or article) as a portable artifact: the whole document, lossless,
// in the bundle format every Import button reads. The older doors stay (HTML,
// Markdown and PDF export; HTML import), but they are renderings: HTML import
// comes back as a single-body article, and sections, subtitle, attached
// knowledge and sources do not survive the round trip. This is the one that
// does.
//
// Import lands the document inert and private to the importer: a fresh id,
// shared with nobody, Private on (no web research until the owner opens it
// up), no publish history, no revisions but its own first one.

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// portableGuide is the recipe. Everything a reader or the Guide Author reads,
// nothing that describes this install: no id (the app-wide shared index is
// keyed by guide id, so a traveled id could collide with somebody else's
// shared guide), owner, sharing, publish records, timestamps or revisions.
type portableGuide struct {
	Name        string               `json:"name"` // the title; preview's identity
	Kind        string               `json:"kind,omitempty"`
	Subtitle    string               `json:"subtitle,omitempty"`
	Sections    []Section            `json:"sections"`
	ImageURL    string               `json:"image_url,omitempty"`
	Collections []string             `json:"collections,omitempty"`
	References  []ReferenceSelection `json:"references,omitempty"`
}

type guideArtifact struct{ app *Scribe }

// RegisterGuideArtifactType registers the "guide" type. Called from Routes,
// where the app's store is known.
func RegisterGuideArtifactType(app *Scribe) { RegisterArtifactType(&guideArtifact{app: app}) }

func (*guideArtifact) ArtifactType() string { return "guide" }

// UserImportable: a guide lands in the importer's own library, private.
func (*guideArtifact) UserImportable() bool { return true }

func (*guideArtifact) ImportFollowUp() string {
	return "Private to you, with web research off until you turn it on in its Settings."
}

// ContentKind: a document, exempt from the export secret scan.
func (*guideArtifact) ContentKind() bool { return true }

// SniffsRecipe claims a bare portable guide: sections plus a name, and no
// stages or phases (which would make it a pipeline or a machine).
func (*guideArtifact) SniffsRecipe(fields map[string]json.RawMessage) bool {
	_, secs := fields["sections"]
	_, name := fields["name"]
	_, stages := fields["stages"]
	_, phases := fields["phases"]
	return secs && name && !stages && !phases
}

func (a *guideArtifact) store(owner string) Database {
	if a.app == nil || a.app.DB == nil || strings.TrimSpace(owner) == "" {
		return nil
	}
	return UserDB(a.app.DB, owner)
}

// ListArtifacts lists every user's guides by id, so a backup is unambiguous
// even when two share a title.
func (a *guideArtifact) ListArtifacts(_ Database) []ArtifactSel {
	if AuthDB == nil {
		return nil
	}
	adb := AuthDB()
	if adb == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range AuthListUsers(adb) {
		udb := a.store(u.Username)
		if udb == nil {
			continue
		}
		for _, g := range listGuides(udb) {
			if g.Owner != "" && g.Owner != u.Username {
				continue
			}
			out = append(out, ArtifactSel{Type: "guide", Name: g.ID, Owner: u.Username})
		}
	}
	return out
}

// find resolves a guide by id, then by title, in the owner's OWN library.
// Never resolveGuide: that also reaches guides other people shared.
func (a *guideArtifact) find(owner, nameOrID string) (Guide, bool) {
	udb := a.store(owner)
	key := strings.TrimSpace(nameOrID)
	if udb == nil || key == "" {
		return Guide{}, false
	}
	if g, ok := loadGuide(udb, key); ok && (g.Owner == "" || g.Owner == owner) {
		return g, true
	}
	for _, g := range listGuides(udb) {
		if strings.EqualFold(strings.TrimSpace(g.Title), key) && (g.Owner == "" || g.Owner == owner) {
			return g, true
		}
	}
	return Guide{}, false
}

func portableFrom(g Guide) portableGuide {
	return portableGuide{
		Name:        g.Title,
		Kind:        g.Kind,
		Subtitle:    g.Subtitle,
		Sections:    g.sorted(),
		ImageURL:    g.ImageURL,
		Collections: g.Collections,
		References:  g.References,
	}
}

func (a *guideArtifact) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	g, ok := a.find(owner, name)
	if !ok {
		return nil, fmt.Errorf("no guide %q for user %q", name, owner)
	}
	return json.Marshal(portableFrom(g))
}

func (a *guideArtifact) ImportArtifact(_ Database, recipe json.RawMessage, owner string) (string, string, error) {
	udb := a.store(owner)
	if udb == nil {
		return "", "", Error("guide import requires an owner")
	}
	var p portableGuide
	if err := json.Unmarshal(recipe, &p); err != nil {
		return "", "", fmt.Errorf("invalid guide recipe: %w", err)
	}
	title := strings.TrimSpace(p.Name)
	if title == "" {
		return "", "", Error("missing guide title")
	}
	if _, exists := a.find(owner, title); exists {
		return title, "a guide with this title already exists", nil
	}
	if p.Kind != KindGuide && p.Kind != KindArticle {
		p.Kind = KindGuide
	}
	secs := make([]Section, 0, len(p.Sections))
	for i, s := range p.Sections {
		body, _ := sanitizeGuideArtifacts(s.Markdown)
		secs = append(secs, Section{ID: newID(), Title: s.Title, Markdown: body, Order: i + 1})
	}
	g := Guide{
		ID:          newID(),
		Kind:        p.Kind,
		Title:       title,
		Subtitle:    p.Subtitle,
		Sections:    secs,
		ImageURL:    p.ImageURL,
		Collections: p.Collections,
		References:  p.References,
		Owner:       owner,
		Private:     true,
	}
	saveGuideRev(udb, g, "Imported")
	return title, "", nil
}

// guideDeps is the one walk behind both dependency interfaces: attached
// collections travel by id, an agent source by name, and every other source
// is declared so the import can warn it is not there.
func guideDeps(p portableGuide, owner string) []ArtifactSel {
	var out []ArtifactSel
	seen := map[string]bool{}
	add := func(s ArtifactSel) {
		k := s.Type + "\x00" + s.Name
		if s.Name == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, s)
	}
	for _, id := range p.Collections {
		add(ArtifactSel{Type: "collection", Name: strings.TrimSpace(id), Owner: owner})
	}
	for _, ref := range p.References {
		if name := RefSourceSelName(ref.Kind, ref.ItemID); name != ":" {
			add(ArtifactSel{Type: "reference_source", Name: name, Owner: owner})
		}
	}
	return out
}

func (a *guideArtifact) Dependencies(_ Database, name, owner string) []ArtifactSel {
	g, ok := a.find(owner, name)
	if !ok {
		return nil
	}
	return guideDeps(portableFrom(g), owner)
}

func (a *guideArtifact) RecipeDependencies(_ Database, recipe json.RawMessage, owner string, _ func(typ, name string) bool) []ArtifactSel {
	var p portableGuide
	if json.Unmarshal(recipe, &p) != nil {
		return nil
	}
	return guideDeps(p, owner)
}
