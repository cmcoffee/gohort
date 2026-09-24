package codewriter

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The same reference picked on the writer and again for the turn is one
// reference: built twice, every tool it contributes reached the model twice.
func TestAReferencePickedTwiceIsOneReference(t *testing.T) {
	refs := uniqueReferences(ReferenceSelections{
		{Kind: "servitor", ItemID: "host-a"},
		{Kind: "guides", ItemID: "g1"},
		{Kind: "servitor", ItemID: "host-a"},
	})
	if len(refs) != 2 || refs[0].ItemID != "host-a" || refs[1].ItemID != "g1" {
		t.Errorf("want [host-a g1] in order, got %+v", refs)
	}
}
