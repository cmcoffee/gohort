package orchestrate

// A collection attached to an agent used to be in scope by id, with no
// ownership check at all. So attaching a private collection and then sharing or
// publishing that agent served the owner's corpus to whoever could run it: the
// collection was never shared, the agent was, and nothing said the documents
// came with it.
//
// The rest of gohort already resolves a shared agent's dependencies in the
// RECIPIENT's namespace — AgentRecord.AllowedUsers says "no secret travels with
// the share", and credentials and tools have always worked that way. These
// tests pin that collections now do too.

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/snugforge/kvlite"
)

func scopeStores(t *testing.T) Database {
	t.Helper()
	savedRoot, savedVec := RootDB, VectorDB
	root := &DBase{Store: kvlite.MemStore()}
	RootDB, VectorDB = root, &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB, VectorDB = savedRoot, savedVec })
	return root
}

// readable reports whether the runtime user may resolve this collection, which
// is the gate the search sources now apply.
func readable(user, id string) bool {
	_, ok := LoadCollection(UserDB(CollectionsDB(), user), user, id)
	return ok
}

func TestAPrivateCorpusDoesNotTravelWithASharedAgent(t *testing.T) {
	scopeStores(t)
	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store in this configuration")
	}
	SaveCollection(owner, Collection{ID: "private-1", Owner: "alice", Name: "Runbooks"})

	if !readable("alice", "private-1") {
		t.Error("the owner's own turn lost its corpus; this flip must not change their behaviour")
	}
	if readable("bob", "private-1") {
		t.Error("a private collection is still readable by whoever runs a shared agent")
	}
}

// The two legitimate ways a corpus reaches somebody else, which is what makes
// the flip safe to make: share it to them, or widen it.
func TestASharedOrWidenedCorpusStillTravels(t *testing.T) {
	scopeStores(t)
	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store")
	}
	SaveCollection(owner, Collection{ID: "shared-1", Owner: "alice", Name: "Shared", AllowedUsers: []string{"bob"}})
	SaveCollection(owner, Collection{ID: "wide-1", Owner: "alice", Name: "Wide"})
	if err := PromoteCollectionToDeployment("alice", "wide-1"); err != nil {
		t.Fatalf("promote: %v", err)
	}

	if !readable("bob", "shared-1") {
		t.Error("a collection shared WITH bob does not reach him")
	}
	if !readable("dana", "wide-1") {
		t.Error("a deployment-wide collection does not reach everybody, which is what a published agent needs")
	}
	// And the peer share is still only for the person named.
	if readable("dana", "shared-1") {
		t.Error("a peer share reached somebody it did not name")
	}
}

// Never silent. A corpus that quietly stops answering produces a confident
// wrong answer rather than a missing one, and the person who can fix it is the
// owner, who is not in the turn.
func TestTheOwnerIsToldWhatWasWithheld(t *testing.T) {
	root := scopeStores(t)
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	savedRef := orchRef
	orchRef = app
	t.Cleanup(func() { orchRef = savedRef })

	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store")
	}
	SaveCollection(owner, Collection{ID: "private-1", Owner: "alice", Name: "Runbooks"})

	noteWithheldCollections("alice", "bob", "agent-1", []string{"private-1"})
	list := notices.List(root, "alice")
	if len(list) != 1 {
		t.Fatalf("the owner was not told: %+v", list)
	}
	// By NAME: "a collection was withheld" is a support ticket, "Runbooks was
	// withheld" is something to act on.
	if !strings.Contains(list[0].Title, "Runbooks") {
		t.Errorf("the notice does not name the collection: %q", list[0].Title)
	}
	if !strings.Contains(list[0].Body, "Share the collection") {
		t.Errorf("the notice does not say what to do about it: %q", list[0].Body)
	}
	// The owner's OWN runs are not an event: nothing was withheld from them.
	noteWithheldCollections("alice", "alice", "agent-1", []string{"private-1"})
	if got := len(notices.List(root, "alice")); got != 1 {
		t.Errorf("the owner is being told about their own turns: %d notices", got)
	}
}

// A skill's attached collections go through the SAME gate as the agent's own.
//
// That branch used to admit them by id with no ownership check, which was
// survivable only while every skill in scope was the runner's own. A skill
// shared with them, or one the deployment publishes, names somebody else's
// documents — and an id is not a grant.
//
// Structural, because the leak is a line that writes the source map directly
// rather than a behaviour with a seam to stand in: the guard has to be that
// nothing in the search-source builder reaches past admit().
func TestSkillCollectionsGoThroughTheOwnershipGate(t *testing.T) {
	raw, err := os.ReadFile("knowledge.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "admit := func(ids []string)")
	if start < 0 {
		t.Fatal("the ownership gate is gone")
	}
	// The gate itself is the one place allowed to write the map.
	body := src[start:]
	if end := strings.Index(body, "\n\tallow := func("); end > 0 {
		body = body[:end]
	}
	for i, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "exact[collectionSource(") {
			continue
		}
		// Two legitimate writers: inside admit, and the deployment-defaults
		// branch, whose collections are deployment-scoped by definition.
		if strings.Contains(line, "exact[collectionSource(cid)] = true") && i < 12 {
			continue // inside admit
		}
		if strings.Contains(line, "exact[collectionSource(c.ID)] = true") {
			continue // deployment defaults, already scoped
		}
		t.Errorf("a search source is admitted without the ownership gate:\n  %s", strings.TrimSpace(line))
	}
	if !strings.Contains(body, "admit(sk.AttachedCollections)") {
		t.Error("a skill's collections no longer go through the gate")
	}
}

// A skill published deployment-wide does not carry its author's private corpus:
// the reference travels, the documents do not.
func TestAPublishedSkillsPrivateCorpusStaysPrivate(t *testing.T) {
	db := scopeStores(t)
	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store")
	}
	SaveCollection(owner, Collection{ID: "private-1", Owner: "alice", Name: "Runbooks"})
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		AttachedCollections: []string{"private-1"}})

	// The skill reaches everybody once published; the collection does not.
	avail := AvailableSkills(db, "dana")
	if len(avail) != 0 {
		t.Fatalf("an unpublished skill already reaches an ordinary user: %+v", avail)
	}
	if readable("dana", "private-1") {
		t.Error("the author's private collection is readable by an ordinary user")
	}
}
