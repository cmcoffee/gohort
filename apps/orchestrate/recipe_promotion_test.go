package orchestrate

// The third rung for the two recipe primitives. Both already reached other
// people by name and both already ran in the REQUESTER's namespace, so
// publishing one is a wider grant on the same record rather than a move.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// recipeTestApp is newTestOrchestrate plus the save hook the running app wires
// at web-init. Without it the share index is never written, so a test would
// exercise a lookup that production never performs.
func recipeTestApp(t *testing.T) {
	t.Helper()
	newTestOrchestrate(t)
	prev := PipelineSavedHook
	PipelineSavedHook = syncPipelineShareIndex
	t.Cleanup(func() { PipelineSavedHook = prev })
}

func publishTestPipeline(t *testing.T, owner, name string) PipelineDef {
	t.Helper()
	return SavePipelineDef(UserDB(orchestrateBaseDB, owner), PipelineDef{
		Owner: owner, Name: name, Stages: []PipelineStage{{Name: "one"}}})
}

// Publishing reaches somebody who was never named anywhere, through the same
// function every surface already calls.
func TestAPublishedPipelineReachesEverybody(t *testing.T) {
	recipeTestApp(t)
	def := publishTestPipeline(t, "alice", "Nightly")

	if userCanRunSharedPipeline(def, "dana") {
		t.Fatal("an unshared pipeline already reaches an ordinary user")
	}
	if err := publishPipelineForDeployment("alice", "Nightly"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	for _, sp := range sharedPipelinesFor("dana") {
		if sp.Def.ID == def.ID {
			return
		}
	}
	t.Error("a published pipeline does not reach an ordinary user")
}

// The named list goes when it is published: everybody already includes them,
// and leaving it would have a later un-publishing restore an ACL the owner had
// forgotten.
func TestPublishingAPipelineClearsItsRecipientList(t *testing.T) {
	recipeTestApp(t)
	def := publishTestPipeline(t, "alice", "Nightly")
	def.AllowedUsers = []string{"bob"}
	SavePipelineDef(UserDB(orchestrateBaseDB, "alice"), def)

	if err := publishPipelineForDeployment("alice", def.ID); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, ok := LoadPipelineDef(UserDB(orchestrateBaseDB, "alice"), "alice", def.ID)
	if !ok {
		t.Fatal("the pipeline went missing")
	}
	if !got.Published || len(got.AllowedUsers) != 0 {
		t.Errorf("published=%v recipients=%v", got.Published, got.AllowedUsers)
	}
}

// Taking it back is the owner's, and only the owner's.
func TestOnlyTheOwnerTakesAPipelineBack(t *testing.T) {
	recipeTestApp(t)
	def := publishTestPipeline(t, "alice", "Nightly")
	publishPipelineForDeployment("alice", def.ID)

	if err := unpublishPipeline("bob", def.ID); err == nil {
		t.Error("somebody unpublished a pipeline that was not theirs")
	}
	if err := unpublishPipeline("alice", def.ID); err != nil {
		t.Fatalf("take back: %v", err)
	}
	got, _ := LoadPipelineDef(UserDB(orchestrateBaseDB, "alice"), "alice", def.ID)
	if got.Published {
		t.Error("it is still published")
	}
	if userCanRunSharedPipeline(got, "dana") {
		t.Error("it still reaches everybody")
	}
}

// A copy of a published recipe is not itself published. An administrator agreed
// that THAT record should reach everybody, not every copy anyone makes of it.
func TestACopyOfAPublishedRecipeIsNotPublished(t *testing.T) {
	recipeTestApp(t)
	def := publishTestPipeline(t, "alice", "Nightly")
	publishPipelineForDeployment("alice", def.ID)
	published, _ := LoadPipelineDef(UserDB(orchestrateBaseDB, "alice"), "alice", def.ID)

	dup := published
	dup.ID, dup.Owner, dup.AllowedUsers, dup.Published = "", "bob", nil, false
	saved := SavePipelineDef(UserDB(orchestrateBaseDB, "bob"), dup)
	if saved.Published {
		t.Error("the copy carried the approval with it")
	}
}

// An admin revoking a share has to reach the widest grant too, or it revokes
// nothing: the named list cleared and deployment-wide left standing.
func TestAdminRevokeAlsoUnpublishes(t *testing.T) {
	recipeTestApp(t)
	def := publishTestPipeline(t, "alice", "Nightly")
	publishPipelineForDeployment("alice", def.ID)

	if err := revokePipelineShareForAdmin(orchestrateBaseDB, "alice", def.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, _ := LoadPipelineDef(UserDB(orchestrateBaseDB, "alice"), "alice", def.ID)
	if got.Published {
		t.Error("an admin revoke left the pipeline deployment-wide")
	}
}

// Filed under the NAME, because an admin reading the queue needs to know what
// they are approving. A rename between filing and approval fails loudly.
func TestAPublishRequestResolvesByName(t *testing.T) {
	recipeTestApp(t)
	publishTestPipeline(t, "alice", "Nightly")

	if err := publishPipelineForDeployment("alice", "nightly"); err != nil {
		t.Errorf("a name should resolve, case and all: %v", err)
	}
	err := publishPipelineForDeployment("alice", "Renamed Since")
	if err == nil {
		t.Fatal("a stale name published something")
	}
	if !strings.Contains(err.Error(), "renamed") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
}

// Everyone is one more choice in a pipeline's and a machine's share list. From
// an ordinary owner it files the publish request and reads back as chosen
// while it waits; taken out, the request is withdrawn; from an administrator
// it publishes, and taken out it unpublishes and keeps the names left in the
// list.
func TestEveryoneInARecipesShareListFilesTheRequest(t *testing.T) {
	T, udb, _ := newTestOrchestrate(t)
	prev := PipelineSavedHook
	PipelineSavedHook = syncPipelineShareIndex
	t.Cleanup(func() { PipelineSavedHook = prev })
	prevAdmin := requestIsAdminAgent
	admin := false
	requestIsAdminAgent = func(*http.Request) bool { return admin }
	t.Cleanup(func() { requestIsAdminAgent = prevAdmin })

	pl := SavePipelineDef(udb, PipelineDef{Owner: "alice", Name: "Nightly", Stages: []PipelineStage{{Name: "one"}}})
	mc := SaveMachineDef(udb, MachineDef{Owner: "alice", Name: "Triage", Phases: []MachinePhase{{Name: "one"}}})

	for _, c := range []struct {
		kind, base string
		handle     func(http.ResponseWriter, *http.Request)
		published  func() bool
	}{
		{pipelinePromotionKind, "/api/pipelines/" + pl.ID, T.handlePipelineOne, func() bool {
			d, _ := LoadPipelineDef(udb, "alice", pl.ID)
			return d.Published
		}},
		{machinePromotionKind, "/api/machines/" + mc.ID, T.handleMachineOne, func() bool {
			d, _ := LoadMachineDef(udb, "alice", mc.ID)
			return d.Published
		}},
	} {
		name := map[string]string{pipelinePromotionKind: "Nightly", machinePromotionKind: "Triage"}[c.kind]
		share := func(method, body string) string {
			t.Helper()
			w := httptest.NewRecorder()
			c.handle(w, asUser(httptest.NewRequest(method, c.base+"/share", strings.NewReader(body)), "alice"))
			if w.Code != http.StatusOK {
				t.Fatalf("%s %s share answered %d: %s", c.kind, method, w.Code, w.Body.String())
			}
			var got struct {
				AllowedUsers []string `json:"allowed_users"`
			}
			json.Unmarshal(w.Body.Bytes(), &got)
			return strings.Join(got.AllowedUsers, ",")
		}

		admin = false
		if got := share(http.MethodPost, `{"allowed_users":["*","bob"]}`); got != "*,bob" {
			t.Errorf("%s: an owner's Everyone reads back %q", c.kind, got)
		}
		if c.published() || !PendingPromotion(AuthDB(), "alice", c.kind, name) {
			t.Errorf("%s: Everyone from an owner is a request, not a publish (published %v)", c.kind, c.published())
		}
		if got := share(http.MethodGet, ""); got != "*,bob" {
			t.Errorf("%s: the picker reads %q while the request waits", c.kind, got)
		}
		if got := share(http.MethodPost, `{"allowed_users":["bob"]}`); got != "bob" || PendingPromotion(AuthDB(), "alice", c.kind, name) {
			t.Errorf("%s: taking Everyone out left %q and the request pending=%v", c.kind, got, PendingPromotion(AuthDB(), "alice", c.kind, name))
		}

		admin = true
		if got := share(http.MethodPost, `{"allowed_users":["*","bob"]}`); got != "*" || !c.published() {
			t.Errorf("%s: an administrator's Everyone publishes and drops the names: %q, published %v", c.kind, got, c.published())
		}
		if got := share(http.MethodPost, `{"allowed_users":["carol"]}`); got != "carol" || c.published() {
			t.Errorf("%s: taking Everyone out unpublishes and keeps the names left: %q, published %v", c.kind, got, c.published())
		}
	}
}
