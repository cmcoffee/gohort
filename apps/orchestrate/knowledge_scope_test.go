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

	refs := []missingRef{{Kind: "collection", ID: "private-1", Name: "Runbooks"}}
	notifyMissingDependencies("alice", "bob", "agent-1", refs)
	list := notices.List(root, "alice")
	if len(list) != 1 {
		t.Fatalf("the owner was not told: %+v", list)
	}
	// By NAME: "a collection was withheld" is a support ticket, "Runbooks was
	// withheld" is something to act on.
	if !strings.Contains(list[0].Title, "Runbooks") {
		t.Errorf("the notice does not name the collection: %q", list[0].Title)
	}
	// What is true now: collections resolve as the OWNER, so the only way one
	// goes missing is that whoever shared it took it back or deleted it. The
	// old text blamed the collection being private to the owner.
	if strings.Contains(list[0].Body, "private to you") || !strings.Contains(list[0].Body, "Remove them") {
		t.Errorf("the notice does not say what happened and what to do about it: %q", list[0].Body)
	}
	// The owner's OWN runs are not an event here: the session's breadcrumb is
	// in front of them.
	notifyMissingDependencies("alice", "alice", "agent-1", refs)
	if got := len(notices.List(root, "alice")); got != 1 {
		t.Errorf("the owner is being told about their own turns: %d notices", got)
	}
}

// Every corpus source is admitted in ONE place, whatever that place currently
// decides.
//
// The RULE here has changed twice — admitted by id with no check, then
// resolved as the runner, now resolved as the agent's owner and scoped to that
// agent — and the guard is deliberately not about which of those is in force.
// It is that there is exactly ONE place a collection enters the source map, so
// the next change to the rule is a change to one function rather than a hunt
// for the branch somebody forgot.
//
// Structural, because the failure is a line that writes the map directly
// rather than a behaviour with a seam to stand in.
func TestEveryCorpusSourceGoesThroughTheOneBuilder(t *testing.T) {
	raw, err := os.ReadFile("knowledge.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "func agentCorpusSourceSet(")
	if start < 0 {
		t.Fatal("the one builder is gone")
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatal("cannot find the end of the builder")
	}
	inside := src[start : start+end]
	if !strings.Contains(inside, "exact[collectionSource(cid)] = true") {
		t.Error("the builder no longer admits anything, so everything else must be going round it")
	}
	rest := src[:start] + src[start+end:]
	for _, line := range strings.Split(rest, "\n") {
		if !strings.Contains(line, "exact[collectionSource(") {
			continue
		}
		// One legitimate writer outside it: the deployment-defaults branch,
		// whose collections are deployment-scoped by definition and so have
		// nothing for a gate to decide.
		t.Errorf("a corpus source is admitted outside the builder:\n  %s", strings.TrimSpace(line))
	}
	// And every reader goes through it rather than assembling its own. These
	// three each had their own copy, and the copies had drifted into three
	// different policies — so a collection the search deliberately left out
	// could still be read by asking for a document from it directly.
	if n := strings.Count(src, "agentCorpusSourceSet("); n < 2 {
		t.Errorf("only %d caller reaches the builder; the others are back to building their own", n)
	}
	if strings.Count(src, "t.agentCorpusSources(") != 2 {
		t.Error("the fetch paths no longer share the search path's source set")
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

// A dependency travels with the agent and is scoped to it. The owner's
// collection answers through THEIR agent, and nowhere else.
func TestAnAgentsCorpusTravelsButDoesNotEscape(t *testing.T) {
	scopeStores(t)
	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store in this configuration")
	}
	SaveCollection(owner, Collection{ID: "runbooks", Owner: "alice", Name: "Runbooks"})

	// Through alice's agent, run by bob: it is in scope, which is the whole
	// point of sharing an agent whose value is its author's documents.
	got, withheld := agentCorpusSourceSet("bob", "alice", []string{"runbooks"}, nil)
	if !got[collectionSource("runbooks")] {
		t.Error("the agent's own corpus did not travel with it")
	}
	if len(withheld) != 0 {
		t.Errorf("withheld = %v", withheld)
	}
	// Outside it, bob still cannot reach the collection: he cannot attach it
	// to an agent of his, and resolving it as himself still fails. The scope
	// is what makes the travelling safe.
	if readable("bob", "runbooks") {
		t.Error("the collection leaked into the recipient's own namespace")
	}
	// And an agent of bob's naming the same id gets nothing, because there is
	// no owner to resolve it as.
	if own, _ := agentCorpusSourceSet("bob", "", []string{"runbooks"}, nil); own[collectionSource("runbooks")] {
		t.Error("the id resolved for an agent that was not alice's")
	}
}

// A reference the OWNER can no longer read is a broken agent, and it still
// reports. What changed is who the check is about, not that there is one.
func TestABrokenReferenceIsStillReported(t *testing.T) {
	scopeStores(t)
	if UserDB(CollectionsDB(), "alice") == nil {
		t.Skip("no per-user store")
	}
	_, withheld := agentCorpusSourceSet("bob", "alice", []string{"deleted-one"}, nil)
	if len(withheld) != 1 || withheld[0] != "deleted-one" {
		t.Errorf("a dangling reference was not reported: %v", withheld)
	}
}
