// Local artifact catalog: a curated set of gohort.bundle/v1 bundles that ship
// IN-TREE (embedded) so an admin can install a ready-made connector, tool, API
// credential, or agent with one click instead of exchanging files by hand. It
// is the offline precursor to a remote marketplace — same envelope, same
// import-as-DRAFT governance: InstallCatalogEntry just feeds the entry's bundle
// to ImportArtifactBundle, so a catalog install lands pending/unapproved exactly
// like a file import (connectors unapproved, tools pending, credentials inert).
//
// A catalog file wraps a bundle with display metadata (id/title/description/
// category). Add an entry by dropping a .json file in core/catalog/ — no code
// change. A malformed file is skipped (logged), never fatal.
package core

import (
	"embed"
	"encoding/json"
	"sort"
	"strings"
)

//go:embed catalog/*.json
var catalogFiles embed.FS

// CatalogFile is the on-disk shape: display metadata + the importable bundle.
type CatalogFile struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Category    string         `json:"category,omitempty"`
	Bundle      ArtifactBundle `json:"bundle"`
}

// CatalogArtifactRef summarizes one artifact an entry would install.
type CatalogArtifactRef struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// CatalogEntry is the browse view: metadata + a summary of WHAT installing adds
// (type + name per artifact), without the full recipes. Summary is the same list
// rendered as a compact display string for a table column.
type CatalogEntry struct {
	ID          string               `json:"id"`
	Title       string               `json:"title"`
	Description string               `json:"description,omitempty"`
	Category    string               `json:"category,omitempty"`
	Contains    []CatalogArtifactRef `json:"contains"`
	Summary     string               `json:"summary"`
}

// loadCatalogFiles parses every embedded entry, sorted by category then title.
func loadCatalogFiles() []CatalogFile {
	entries, err := catalogFiles.ReadDir("catalog")
	if err != nil {
		return nil
	}
	var out []CatalogFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, rErr := catalogFiles.ReadFile("catalog/" + e.Name())
		if rErr != nil {
			continue
		}
		var cf CatalogFile
		if json.Unmarshal(data, &cf) != nil || strings.TrimSpace(cf.ID) == "" {
			Log("[catalog] skipping malformed entry %q", e.Name())
			continue
		}
		out = append(out, cf)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// ListCatalog returns the browse view of every embedded catalog entry.
func ListCatalog() []CatalogEntry {
	var out []CatalogEntry
	for _, cf := range loadCatalogFiles() {
		e := CatalogEntry{ID: cf.ID, Title: cf.Title, Description: cf.Description, Category: cf.Category}
		var parts []string
		for _, a := range cf.Bundle.Artifacts {
			e.Contains = append(e.Contains, CatalogArtifactRef{Type: a.Type, Name: a.Name})
			parts = append(parts, a.Name+" ("+a.Type+")")
		}
		e.Summary = strings.Join(parts, ", ")
		out = append(out, e)
	}
	return out
}

// GetCatalogBundle returns the importable bundle for a catalog id.
func GetCatalogBundle(id string) (ArtifactBundle, bool) {
	id = strings.TrimSpace(id)
	for _, cf := range loadCatalogFiles() {
		if cf.ID == id {
			return cf.Bundle, true
		}
	}
	return ArtifactBundle{}, false
}

// InstallCatalogEntry imports the named catalog entry's bundle as DRAFTS owned
// by owner — the same path (and governance) as a file import.
func InstallCatalogEntry(db Database, id, owner string) (ArtifactImportResult, error) {
	bundle, ok := GetCatalogBundle(id)
	if !ok {
		return ArtifactImportResult{}, Error("no catalog entry with id " + strings.TrimSpace(id))
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return ArtifactImportResult{}, err
	}
	return ImportArtifactBundle(db, data, owner)
}
