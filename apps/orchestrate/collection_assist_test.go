package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The assist is told what a description is for and what the collection
// holds, so its first suggestion is about this collection and not any.
func TestTheDescriptionAssistKnowsTheCollection(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", Name: "Runbooks"}
	about := collectionAssistAbout(c, []string{"Draining a node", "Restoring etcd"})
	for _, want := range []string{"Collection name: Runbooks", "- Draining a node\n- Restoring etcd", "Auto-fill", "one to three sentences"} {
		if !strings.Contains(about, want) {
			t.Errorf("the assist is not told %q:\n%s", want, about)
		}
	}
	if empty := collectionAssistAbout(c, nil); !strings.Contains(empty, "no documents yet") {
		t.Errorf("an empty collection should say so:\n%s", empty)
	}
}
