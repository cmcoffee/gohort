// An agent's ATTACHED SOURCES as a portable reference.
//
// The one dependency an agent recipe carried without declaring. Everything
// else it points at travels or is warned about — its tools, its machine, its
// pipelines, its skills, its collections — while an attachment rode along as
// two strings in the record, pointing at a file store or an appliance that
// exists on the exporting box and nowhere else. The agent then landed looking
// complete: the picker showed the attachment, and the tools it was supposed to
// mint were simply absent, which reads as tools that went missing rather than
// as a source nobody has.
//
// A source is NOT materializable from a recipe, and this type does not pretend
// otherwise. A file store is a path on a host; a servitor system is somebody
// else's machine and a credential to reach it. So import always SKIPS, with
// the one thing the importer actually needs: what to create, and what the
// agent will be able to call once they have.
//
// What it buys is the existence probe. The bundle machinery answers "is this
// reference satisfied here" by asking the type, so declaring sources makes the
// import preview and the post-import pass say "this agent expects a file store
// called Support bundles" — before the first turn, instead of after it.

package core

import (
	"encoding/json"
	"strconv"
	"strings"
)

// referenceSourceArtifact is registered from artifact_types.go alongside the
// other cross-app types. It reads the reference-source registry, so it covers
// every producer without naming one.
type referenceSourceArtifact struct{}

func (referenceSourceArtifact) ArtifactType() string { return "reference_source" }

// RefSourceSelName is the stable identity of one attached item: the pair the
// agent record actually stores. NOT the display label — a store can be renamed
// (that is exactly why its handle is frozen), and an attachment that stopped
// matching on a rename would be the bug this exists to report.
func RefSourceSelName(kind, itemID string) string {
	return strings.TrimSpace(kind) + ":" + strings.TrimSpace(itemID)
}

// splitRefSourceSel is the inverse. Splits on the FIRST colon: a kind never
// contains one, an item id might.
func splitRefSourceSel(name string) (kind, itemID string) {
	kind, itemID, _ = strings.Cut(strings.TrimSpace(name), ":")
	return strings.TrimSpace(kind), strings.TrimSpace(itemID)
}

func (referenceSourceArtifact) ListArtifacts(db Database) []ArtifactSel {
	authDB := AuthDB()
	if authDB == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range AuthListUsers(authDB) {
		for _, g := range ReferenceGroups(u.Username) {
			for _, it := range g.Items {
				out = append(out, ArtifactSel{
					Type:  "reference_source",
					Name:  RefSourceSelName(g.Kind, it.ID),
					Owner: u.Username,
				})
			}
		}
	}
	return out
}

// refSourceRecipe is what travels: enough for a person to recreate the thing
// on their side and recognise it when they have, and nothing that would let a
// bundle carry somebody's data or their access to a box.
type refSourceRecipe struct {
	Kind   string   `json:"kind"`
	ItemID string   `json:"item_id"`
	Label  string   `json:"label,omitempty"`
	Desc   string   `json:"desc,omitempty"`
	Source string   `json:"source,omitempty"` // the producer's own label ("File stores")
	Tools  []string `json:"tools,omitempty"`  // what attaching it mints, which is the point
}

func (referenceSourceArtifact) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	owner = strings.TrimSpace(owner)
	kind, itemID := splitRefSourceSel(name)
	if owner == "" || kind == "" || itemID == "" {
		return nil, Error("a reference source is exported as \"<kind>:<item_id>\" for a named owner")
	}
	rec := refSourceRecipe{Kind: kind, ItemID: itemID}
	for _, g := range ReferenceGroups(owner) {
		if g.Kind != kind {
			continue
		}
		rec.Source = g.Label
		for _, it := range g.Items {
			if it.ID == itemID {
				rec.Label, rec.Desc = it.Name, it.Desc
			}
		}
	}
	if rec.Label == "" {
		return nil, Error("no source " + name + " for user " + owner)
	}
	// The tool NAMES, because they are what an agent's steps and allowlists
	// refer to. Somebody rebuilding the source on the far side needs to know
	// which handle produces them — a store named differently mints different
	// names, and the agent is pinned to the ones it was built against.
	for _, td := range ReferenceItemTools(owner, kind, itemID) {
		rec.Tools = append(rec.Tools, td.Tool.Name)
	}
	return json.Marshal(rec)
}

// ImportArtifact never creates anything. It reports what has to exist, and
// whether it already does.
//
// Deliberately a SKIP rather than an error: the bundle is fine, the agent
// imports, and the person is told the one thing left to do. An error would
// fail an import over a step no importer could have avoided.
func (referenceSourceArtifact) ImportArtifact(_ Database, recipe json.RawMessage, owner string) (string, string, error) {
	var rec refSourceRecipe
	if err := json.Unmarshal(recipe, &rec); err != nil {
		return "", "", Error("unreadable reference-source recipe: " + err.Error())
	}
	name := RefSourceSelName(rec.Kind, rec.ItemID)
	label := strings.TrimSpace(rec.Label)
	if label == "" {
		label = name
	}
	if refSourceExists(strings.TrimSpace(owner), rec.Kind, rec.ItemID) {
		return name, "already here — the agent's attachment resolves against your own " + label, nil
	}
	what := strings.TrimSpace(rec.Source)
	if what == "" {
		what = "source"
	}
	reason := "a source cannot travel — create a " + what + " here with the handle " + strconv.Quote(rec.ItemID) +
		" (it was called " + strconv.Quote(label) + ")"
	if len(rec.Tools) > 0 {
		reason += ", which is what mints " + strings.Join(rec.Tools, ", ")
	}
	return name, reason + ", then re-attach it under the agent's Sources", nil
}

// refSourceExists reports whether this user can reach that exact item —
// the existence probe the unmet-dependency pass runs.
func refSourceExists(user, kind, itemID string) bool {
	for _, g := range ReferenceGroups(user) {
		if g.Kind != strings.TrimSpace(kind) {
			continue
		}
		for _, it := range g.Items {
			if it.ID == strings.TrimSpace(itemID) {
				return true
			}
		}
	}
	return false
}
