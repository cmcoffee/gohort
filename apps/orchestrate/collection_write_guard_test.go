package orchestrate

// Sharing a collection used to hand over write access nobody granted.
//
// The route gate resolves with LoadCollection, which admits the owner, anybody
// it was shared with, and every user when it is deployment-wide. That is right
// for reading, and it was being used for everything: a recipient could upload
// into somebody else's corpus, run an autofill or a research pass into it, or
// rename and re-scope it. It mattered more than an extra file — an agent takes
// its collections as ground truth, so whoever can write to one decides what
// every agent reading it believes.

import (
	"net/http"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestARecipientMayReadACollectionAndNothingElse(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", Name: "Runbooks",
		AllowedUsers: []string{"bob"}}

	for _, action := range []string{"search", "export"} {
		if why := collectionWriteRefusal(c, "bob", action, http.MethodPost); why != "" {
			t.Errorf("a reader was refused %q: %s", action, why)
		}
	}
	if why := collectionWriteRefusal(c, "bob", "", http.MethodGet); why != "" {
		t.Errorf("a reader was refused the record itself: %s", why)
	}

	// Everything that changes the corpus, or the record around it.
	for _, action := range []string{"upload", "paste", "sources", "autofill", "research", "steward"} {
		why := collectionWriteRefusal(c, "bob", action, http.MethodPost)
		if why == "" {
			t.Errorf("a recipient could still %s into somebody else's collection", action)
			continue
		}
		// The refusal says whose it is and what to do instead, or the next
		// thing somebody tries is the same call with a different spelling.
		if !strings.Contains(why, "alice") || !strings.Contains(why, "copy") {
			t.Errorf("the refusal for %q does not explain itself: %s", action, why)
		}
	}
	if collectionWriteRefusal(c, "bob", "", http.MethodPost) == "" {
		t.Error("a recipient could rename or re-scope somebody else's collection")
	}
}

// An action nobody has written yet is a write until its author says otherwise.
// The read list is short and explicit; the default is the safe direction.
func TestAnUnknownActionIsTreatedAsAWrite(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", AllowedUsers: []string{"bob"}}
	if collectionWriteRefusal(c, "bob", "some-new-thing", http.MethodPost) == "" {
		t.Error("an unrecognised action defaulted to allowed")
	}
}

// The owner may do anything, and a collection with no owner at all — a record
// from before ownership, or the framework's own — is nobody's to be refused.
func TestTheOwnerIsNeverRefused(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice"}
	for _, action := range []string{"upload", "research", ""} {
		if why := collectionWriteRefusal(c, "alice", action, http.MethodPost); why != "" {
			t.Errorf("the owner was refused %q: %s", action, why)
		}
	}
	if why := collectionWriteRefusal(Collection{ID: "old"}, "bob", "upload", http.MethodPost); why != "" {
		t.Errorf("an unowned collection refused somebody: %s", why)
	}
}

// A deployment-wide collection is readable by everybody and writable by its
// author. Publishing it widened who could CHANGE it, which is the same hole
// with a larger blast radius.
func TestPublishingACollectionDoesNotHandOutWrites(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", Name: "Handbook",
		Scope: CollectionScopeDeployment}
	if why := collectionWriteRefusal(c, "dana", "search", http.MethodPost); why != "" {
		t.Errorf("a deployment collection refused a reader: %s", why)
	}
	if collectionWriteRefusal(c, "dana", "upload", http.MethodPost) == "" {
		t.Error("anybody in the deployment could write to a published collection")
	}
}
