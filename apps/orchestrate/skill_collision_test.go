package orchestrate

// A skill name has to pick one skill, everywhere it is typed: the model's
// read_skill against the "Available skills" block, and an author's skill_def
// against the skills they own, published ones included.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
)

func skillDefCall(t *testing.T, sess *ToolSession, args map[string]any) (string, error) {
	t.Helper()
	return skillDefImpl{}.RunWithSession(args, sess)
}

func publishSkillForTest(t *testing.T, owner, name string) {
	t.Helper()
	if err := promotion.CreatePromotionRequest(AuthDB(), owner, SkillPromotionKind, name, ""); err != nil {
		t.Fatal(err)
	}
	if err := promotion.Approve(AuthDB(), promotion.RequestKey(SkillPromotionKind, owner, name), "admin"); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

// Bob chatting alice's agent: the skill tools resolve in alice's view, the same
// one the listing is built from, so a skill shared WITH alice is both listed
// and readable. The tools were built as the runner, against bob's own pool,
// and answered "no skill named" for every skill the listing had just shown.
func TestSkillToolsResolveWhereTheListingDoes(t *testing.T) {
	udb := reachFixture(t)
	SaveSkill(RootDB, "alice", SkillRecord{ID: "s1", Name: "Triage", Description: "d", Instructions: "Alice's."})
	SaveSkill(RootDB, "carol", SkillRecord{ID: "s2", Name: "Ledger", Description: "d", Instructions: "Carol's.", AllowedUsers: []string{"alice"}})
	SaveSkill(RootDB, "bob", SkillRecord{ID: "s-bob", Name: "Triage", Description: "d", Instructions: "Bob's."})

	agent := AgentRecord{ID: "a1", Owner: "alice", AllowedSkills: []string{"s1", "s2"}}
	turn := &chatTurn{
		ctx: context.Background(),
		udb: UserDB(RootDB, "bob"), user: "bob",
		ownerDB: udb, ownerUser: "alice",
		agent:           agent,
		deliveredSkills: map[string]bool{},
	}
	defs := turn.skillToolDefs()
	if len(defs) == 0 {
		t.Fatal("no skill tools")
	}
	read := defs[0]
	for name, want := range map[string]string{"Triage": "Alice's.", "Ledger": "Carol's."} {
		out, err := read.Handler(context.Background(), map[string]any{"skill": name})
		if err != nil {
			t.Errorf("read_skill(%q), a listed skill: %v", name, err)
			continue
		}
		if !strings.Contains(out, want) {
			t.Errorf("read_skill(%q) answered with another skill:\n%s", name, out)
		}
	}
	// The listing, rendered as the web turn renders it (runtime db and user),
	// names exactly what the enum offers.
	block := availableSkillsBlock(agent, UserDB(RootDB, "bob"), "bob")
	for _, h := range read.Tool.Parameters["skill"].Enum {
		if !strings.Contains(block, "**"+h+"**") {
			t.Errorf("enum offers %q, the listing does not show it:\n%s", h, block)
		}
	}
	if strings.Contains(block, "Bob's") || !strings.Contains(block, "**Ledger**") {
		t.Errorf("the listing is not the agent owner's set:\n%s", block)
	}
}

// A seed agent's "system" owner holds no skills: the listing falls back to
// the runner, as the turn's owner view does.
func TestASeedAgentsSkillsAreTheRunners(t *testing.T) {
	if got := skillOwnerFor(AgentRecord{Owner: seedOwner}, "bob"); got != "bob" {
		t.Errorf("seed agent resolved skills as %q", got)
	}
	if got := skillOwnerFor(AgentRecord{Owner: "alice"}, "bob"); got != "alice" {
		t.Errorf("published agent resolved skills as %q", got)
	}
}

// After an author publishes "Triage", skill_def by that name reaches the
// published record: create refuses rather than landing a private duplicate,
// update edits the deployment's copy, delete takes it back and deletes it.
func TestSkillDefByNameReachesAPublishedSkill(t *testing.T) {
	udb := reachFixture(t)
	sess := &ToolSession{DB: udb, Username: "alice"}
	if _, err := skillDefCall(t, sess, map[string]any{"action": "create", "name": "Triage",
		"description": "d", "instructions": "Assess."}); err != nil {
		t.Fatalf("create: %v", err)
	}
	publishSkillForTest(t, "alice", "Triage")

	if _, err := skillDefCall(t, sess, map[string]any{"action": "create", "name": "triage",
		"description": "d", "instructions": "Again."}); err == nil {
		t.Error("create made a second skill under a name the author already published")
	}
	if got := LoadSkills(udb, "alice"); len(got) != 0 {
		t.Fatalf("a private duplicate landed: %+v", got)
	}

	if _, err := skillDefCall(t, sess, map[string]any{"action": "update", "name": "Triage",
		"instructions": "Assess, then page."}); err != nil {
		t.Fatalf("update could not reach the published skill: %v", err)
	}
	pub := PublishedSkillsBy(udb, "alice")
	if len(pub) != 1 || pub[0].Instructions != "Assess, then page." {
		t.Fatalf("the edit did not reach the deployment's copy: %+v", pub)
	}
	if got := LoadSkills(udb, "alice"); len(got) != 0 {
		t.Errorf("update made a private copy: %+v", got)
	}

	if _, err := skillDefCall(t, sess, map[string]any{"action": "delete", "name": "Triage"}); err != nil {
		t.Fatalf("delete could not reach the published skill: %v", err)
	}
	if got := PublishedSkillsBy(udb, "alice"); len(got) != 0 {
		t.Errorf("the published skill survived its delete: %+v", got)
	}
	if got := LoadSkills(udb, "alice"); len(got) != 0 {
		t.Errorf("delete left it in the author's pool: %+v", got)
	}
}

// Create by a name the author already has privately is refused too: it used
// to upsert, replacing the record wholesale, which is update's job.
func TestSkillDefCreateRefusesANameYouHave(t *testing.T) {
	udb := reachFixture(t)
	sess := &ToolSession{DB: udb, Username: "alice"}
	args := map[string]any{"action": "create", "name": "Triage", "description": "d", "instructions": "Assess."}
	if _, err := skillDefCall(t, sess, args); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := skillDefCall(t, sess, args)
	if err == nil || !strings.Contains(err.Error(), "action=update") {
		t.Errorf("a second create by the same name was not refused toward update: %v", err)
	}
	if got := LoadSkills(udb, "alice"); len(got) != 1 {
		t.Errorf("want one skill, have %d", len(got))
	}
}

// create_collection mints "<skill> Knowledge". When one of that name exists
// (most likely this skill's corpus from before), minting a second made a
// same-named pair a later attach by name could confuse. Refused before anything
// is saved, pointing at the one that exists.
func TestSkillDefCreateCollectionRefusesAnExistingName(t *testing.T) {
	udb := reachFixture(t)
	sess := &ToolSession{DB: udb, Username: "alice"}
	SaveCollection(udb, Collection{ID: "col-old", Owner: "alice", Name: "Runbook Knowledge"})
	_, err := skillDefCall(t, sess, map[string]any{"action": "create", "name": "Runbook", "description": "d",
		"instructions": "Assess.", "create_collection": true})
	if err == nil || !strings.Contains(err.Error(), "col-old") {
		t.Fatalf("a second Runbook Knowledge was minted, or the refusal does not name the existing one: %v", err)
	}
	if got := LoadSkills(udb, "alice"); len(got) != 0 {
		t.Errorf("the refused create still saved the skill: %d", len(got))
	}
}
