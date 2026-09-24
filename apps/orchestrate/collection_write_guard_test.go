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
		if why := collectionWriteRefusal(c, "bob", action, http.MethodPost, false); why != "" {
			t.Errorf("a reader was refused %q: %s", action, why)
		}
	}
	if why := collectionWriteRefusal(c, "bob", "", http.MethodGet, false); why != "" {
		t.Errorf("a reader was refused the record itself: %s", why)
	}

	// Everything that changes the corpus, or the record around it.
	for _, action := range []string{"upload", "paste", "sources", "autofill", "research", "steward"} {
		why := collectionWriteRefusal(c, "bob", action, http.MethodPost, false)
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
	if collectionWriteRefusal(c, "bob", "", http.MethodPost, false) == "" {
		t.Error("a recipient could rename or re-scope somebody else's collection")
	}
}

// An action nobody has written yet is a write until its author says otherwise.
// The read list is short and explicit; the default is the safe direction.
func TestAnUnknownActionIsTreatedAsAWrite(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", AllowedUsers: []string{"bob"}}
	if collectionWriteRefusal(c, "bob", "some-new-thing", http.MethodPost, false) == "" {
		t.Error("an unrecognised action defaulted to allowed")
	}
}

// The owner may do anything, and a collection with no owner at all — a record
// from before ownership, or the framework's own — is nobody's to be refused.
func TestTheOwnerIsNeverRefused(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice"}
	for _, action := range []string{"upload", "research", ""} {
		if why := collectionWriteRefusal(c, "alice", action, http.MethodPost, false); why != "" {
			t.Errorf("the owner was refused %q: %s", action, why)
		}
	}
	if why := collectionWriteRefusal(Collection{ID: "old"}, "bob", "upload", http.MethodPost, false); why != "" {
		t.Errorf("an unowned collection refused somebody: %s", why)
	}
}

// A deployment-wide collection is readable by everybody and writable by its
// author. Publishing it widened who could CHANGE it, which is the same hole
// with a larger blast radius.
func TestPublishingACollectionDoesNotHandOutWrites(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", Name: "Handbook",
		Scope: CollectionScopeDeployment}
	if why := collectionWriteRefusal(c, "dana", "search", http.MethodPost, false); why != "" {
		t.Errorf("a deployment collection refused a reader: %s", why)
	}
	if collectionWriteRefusal(c, "dana", "upload", http.MethodPost, false) == "" {
		t.Error("anybody in the deployment could write to a published collection")
	}
}

// A contributor may ADD to the corpus, and nothing more. Somebody trusted to
// put a document in has not thereby been handed the collection.
func TestAContributorAddsButDoesNotOwn(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", Name: "Runbooks",
		AllowedUsers: []string{"bob", "carol"}, Contributors: []string{"bob"}}

	for _, action := range []string{"upload", "paste", "sources", "autofill", "research"} {
		if why := collectionWriteRefusal(c, "bob", action, http.MethodPost, false); why != "" {
			t.Errorf("a contributor was refused %q: %s", action, why)
		}
	}
	// Not the collection itself: renaming, re-scoping, re-sharing.
	why := collectionWriteRefusal(c, "bob", "", http.MethodPost, false)
	if why == "" {
		t.Error("a contributor could rename or re-scope the collection")
	} else if !strings.Contains(why, "add to it") {
		t.Errorf("the refusal does not distinguish adding from owning: %s", why)
	}
	// Reorganising what is already there is a different trust from adding to
	// it: the curator can move or drop what its owner put in.
	if collectionWriteRefusal(c, "bob", "steward", http.MethodPost, false) == "" {
		t.Error("a contributor could turn the curator loose on somebody else's corpus")
	}
	// And a reader who is not a contributor still only reads.
	if collectionWriteRefusal(c, "carol", "upload", http.MethodPost, false) == "" {
		t.Error("a plain recipient could upload")
	}
}

// Contributors are a subset of the people it is shared with. Somebody who
// cannot see what is already in a corpus would be writing into it blind, and a
// list that outlived the share would be waiting to hand write back.
func TestAContributorMustBeSomebodyItIsSharedWith(t *testing.T) {
	if got := keepOnly([]string{"bob", "dana"}, []string{"bob"}); len(got) != 1 || got[0] != "bob" {
		t.Errorf("the contributor list kept somebody it is not shared with: %+v", got)
	}
	if got := keepOnly([]string{"dana"}, []string{"bob"}); got != nil {
		t.Errorf("an entirely stale list survived: %+v", got)
	}
}

// Nobody holds this by default. Before the write gate every recipient could
// write because nothing checked, which is not the same as having been given it.
func TestContributorsAreEmptyByDefault(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", AllowedUsers: []string{"bob"}}
	if CollectionContributor(c, "bob") {
		t.Error("a plain share made somebody a contributor")
	}
	if !CollectionContributor(c, "alice") {
		t.Error("the owner is not a contributor to their own collection")
	}
}

// The deployment's own collection is everybody's to read and an
// administrator's to change: every agent without curated collections takes it
// as ground truth.
func TestTheDeploymentKnowledgeIsAnAdministratorsToChange(t *testing.T) {
	c := Collection{ID: DeploymentKnowledgeCollectionID, Name: "Deployment Knowledge", Scope: CollectionScopeDeployment}
	if why := collectionWriteRefusal(c, "bob", "search", http.MethodPost, false); why != "" {
		t.Errorf("a user could not search it: %s", why)
	}
	for _, action := range []string{"upload", "paste", "remove_doc", ""} {
		if collectionWriteRefusal(c, "bob", action, http.MethodPost, false) == "" {
			t.Errorf("a user could %q the deployment's knowledge", action)
		}
		if why := collectionWriteRefusal(c, "root", action, http.MethodPost, true); why != "" {
			t.Errorf("an admin was refused %q: %s", action, why)
		}
	}
	if collectionWriteRefusal(c, "bob", "", http.MethodDelete, false) == "" {
		t.Error("a user could delete the deployment's knowledge")
	}
}

// A reader is handed the collection's page, and that page lists the documents
// with a GET of "sources". It was gated as a corpus write, so every reader who
// was not also a contributor saw a shared collection as empty. Listing is a
// read; removing one document (sources/<id>) still is not.
func TestAReaderCanListTheDocumentsInASharedCollection(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", Name: "Runbooks",
		AllowedUsers: []string{"bob"}}
	if why := collectionWriteRefusal(c, "bob", "sources", http.MethodGet, false); why != "" {
		t.Errorf("a reader could not list the documents: %s", why)
	}
	if collectionWriteRefusal(c, "bob", "sources/doc-1", http.MethodDelete, false) == "" {
		t.Error("a reader could remove a document from somebody else's collection")
	}
	dk := Collection{ID: DeploymentKnowledgeCollectionID, Name: "Deployment Knowledge", Scope: CollectionScopeDeployment}
	if why := collectionWriteRefusal(dk, "bob", "sources", http.MethodGet, false); why != "" {
		t.Errorf("a user could not list the deployment's knowledge: %s", why)
	}
}
