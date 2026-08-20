// An attachment is the one thing an agent points at that cannot travel, which
// is exactly why the bundle has to name it.
package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type packSource struct{ kind string }

func (s packSource) Kind() string { return s.kind }
func (packSource) Label() string  { return "File stores" }
func (packSource) List(user string) []ReferenceItem {
	if user != "u" {
		return nil
	}
	return []ReferenceItem{{ID: "support_bundles", Name: "Support bundles", Desc: "diagnostic dumps"}}
}
func (packSource) Fetch(context.Context, string, string, string) string { return "" }
func (packSource) ItemTools(user, itemID string) []AgentToolDef {
	return []AgentToolDef{{Tool: Tool{Name: "search_" + itemID}}, {Tool: Tool{Name: "read_" + itemID}}}
}

// The recipe carries what somebody needs to rebuild the source and recognise
// it when they have — and never the data, the path, or the way in.
func TestASourceExportsItsShapeAndNothingElse(t *testing.T) {
	RegisterReferenceSource(packSource{kind: "packfiles"})
	raw, err := referenceSourceArtifact{}.ExportArtifact(nil, "packfiles:support_bundles", "u")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var rec refSourceRecipe
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rec.Label != "Support bundles" || rec.ItemID != "support_bundles" {
		t.Errorf("the recipe should identify the item: %+v", rec)
	}
	// The tool names are the point: an agent is pinned to the names its
	// source minted, so the far side has to know which handle produces them.
	if strings.Join(rec.Tools, ",") != "search_support_bundles,read_support_bundles" {
		t.Errorf("the recipe should say what attaching it mints: %v", rec.Tools)
	}
}

// Import cannot create a store or an appliance and does not pretend to. It
// SKIPS — the bundle is fine, the agent lands, and the person is told the one
// thing left to do.
func TestImportingASourceExplainsWhatToBuildInstead(t *testing.T) {
	RegisterReferenceSource(packSource{kind: "packfiles"})
	recipe, _ := json.Marshal(refSourceRecipe{
		Kind: "packfiles", ItemID: "prod_logs", Label: "Prod logs",
		Source: "File stores", Tools: []string{"search_prod_logs"},
	})
	name, reason, err := referenceSourceArtifact{}.ImportArtifact(nil, recipe, "u")
	if err != nil {
		t.Fatalf("import should never fail over something the importer could not avoid: %v", err)
	}
	if name != "packfiles:prod_logs" {
		t.Errorf("the skip should name the attachment the agent holds: %q", name)
	}
	for _, want := range []string{"cannot travel", `"prod_logs"`, "search_prod_logs", "re-attach"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the reason should say what to build and what it mints (%q missing): %q", want, reason)
		}
	}

	// One that IS here reads differently: nothing to do, and saying so is not
	// the same message as saying nothing.
	here, _ := json.Marshal(refSourceRecipe{Kind: "packfiles", ItemID: "support_bundles", Label: "Support bundles"})
	_, hereReason, _ := referenceSourceArtifact{}.ImportArtifact(nil, here, "u")
	if !strings.Contains(hereReason, "already here") {
		t.Errorf("a source the importer already has should say so: %q", hereReason)
	}
}

// The identity is the pair the agent record stores, NOT the display label: a
// store can be renamed and its handle deliberately does not follow, so a
// selection keyed on the label would break on exactly the rename the handle
// exists to survive.
func TestASourceIsIdentifiedByItsHandleNotItsName(t *testing.T) {
	if got := RefSourceSelName(" files ", " support_bundles "); got != "files:support_bundles" {
		t.Errorf("selection name should be the trimmed pair, got %q", got)
	}
	kind, item := splitRefSourceSel("confluence:space:ENG")
	if kind != "confluence" || item != "space:ENG" {
		t.Errorf("the split is on the FIRST colon; an id may contain one: %q / %q", kind, item)
	}
}
