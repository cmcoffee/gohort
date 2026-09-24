package core

// Skill names are what the model and the author type, and names collide: a
// user's own skill and one shared with them, a private skill and one that was
// published, two of one person's skills in older data. These pin that a
// collision is shown and refused, never resolved silently to whichever skill
// came first.

import (
	"context"
	"strings"
	"testing"
)

func readSkill(t *testing.T, db Database, owner string, allowed []string, name string) (string, error) {
	t.Helper()
	def := BuildReadSkillTool(db, owner, allowed, map[string]bool{}, nil)
	return def.Handler(context.Background(), map[string]any{"skill": name})
}

// The listing names a shared and a published skill, so the tools must reach
// them. They resolved against the caller's own pool only, and answered "no
// skill named" for a skill the block had just advertised.
func TestSkillToolsReachSharedAndPublishedSkills(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Alice's triage.", AllowedUsers: []string{"bob"}})
	SaveSkill(db, "carol", SkillRecord{ID: "s2", Name: "Ledger", Instructions: "Carol's ledger."})
	if err := promoteSkillToDeployment("carol", "s2"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	allowed := []string{"s1", "s2"}

	for name, want := range map[string]string{"Triage": "Alice's triage.", "ledger": "Carol's ledger."} {
		out, err := readSkill(t, db, "bob", allowed, name)
		if err != nil {
			t.Errorf("read_skill(%q) for a skill the listing shows: %v", name, err)
			continue
		}
		if !strings.Contains(out, want) {
			t.Errorf("read_skill(%q) returned the wrong body:\n%s", name, out)
		}
	}
	enum := allowedSkillNames(db, "bob", allowed)
	if strings.Join(enum, ",") != "Ledger,Triage" {
		t.Errorf("the enum does not offer what the listing shows: %v", enum)
	}
}

// An agent that picked the shared skill gets the shared skill, even when the
// runner owns one by the same name. The resolver used to stop at the first
// name match, find the owner's own copy not allowed, and refuse.
func TestTheAllowedSkillWinsOverASameNamedOne(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Alice's.", AllowedUsers: []string{"bob"}})
	SaveSkill(db, "bob", SkillRecord{ID: "own", Name: "Triage", Instructions: "Bob's."})

	out, err := readSkill(t, db, "bob", []string{"s1"}, "Triage")
	if err != nil {
		t.Fatalf("the allowed skill was refused over a same-named one: %v", err)
	}
	if !strings.Contains(out, "Alice's.") {
		t.Errorf("resolved to the wrong skill:\n%s", out)
	}
	// Only one is in the set, so it keeps its plain name.
	if enum := allowedSkillNames(db, "bob", []string{"s1"}); strings.Join(enum, ",") != "Triage" {
		t.Errorf("a name unique in the set was decorated: %v", enum)
	}
}

// Both allowed: the listing says whose each is, the enum offers the same two
// handles, a handle picks one, and the bare name is refused with the choices
// rather than guessed.
func TestTwoSameNamedSkillsAreShownApart(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Description: "shared", Instructions: "Alice's.", AllowedUsers: []string{"bob"}})
	SaveSkill(db, "bob", SkillRecord{ID: "own", Name: "Triage", Description: "own", Instructions: "Bob's."})
	allowed := []string{"s1", "own"}

	block := RenderAvailableSkills(allowedSkillSet(db, "bob", allowed))
	for _, want := range []string{"**Triage (from alice)**", "**Triage (from bob)**"} {
		if !strings.Contains(block, want) {
			t.Errorf("the listing does not say whose %q is:\n%s", want, block)
		}
	}
	enum := allowedSkillNames(db, "bob", allowed)
	if strings.Join(enum, "|") != "Triage (from alice)|Triage (from bob)" {
		t.Fatalf("the enum is not the listing's handles: %v", enum)
	}
	for _, h := range enum {
		if !strings.Contains(block, "**"+h+"**") {
			t.Errorf("enum handle %q is not in the listing", h)
		}
	}
	out, err := readSkill(t, db, "bob", allowed, "Triage (from alice)")
	if err != nil || !strings.Contains(out, "Alice's.") {
		t.Errorf("the handle did not pick alice's: %v\n%s", err, out)
	}
	out, err = readSkill(t, db, "bob", allowed, "own")
	if err != nil || !strings.Contains(out, "Bob's.") {
		t.Errorf("the id did not pick bob's: %v\n%s", err, out)
	}
	if _, err := readSkill(t, db, "bob", allowed, "Triage"); err == nil ||
		!strings.Contains(err.Error(), "Triage (from alice)") || !strings.Contains(err.Error(), "Triage (from bob)") {
		t.Errorf("a bare ambiguous name was not refused with the choices: %v", err)
	}
	// The trigger hint names it the same way.
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Alice's.", Triggers: []string{"outage"}, AllowedUsers: []string{"bob"}})
	if hint := renderSkillTriggerHints(db, "bob", allowed, "an outage", nil); !strings.Contains(hint, "**Triage (from alice)**") {
		t.Errorf("the hint names the skill differently from the listing:\n%s", hint)
	}
}

// Two of one person's skills under one name, which older data can hold: the
// allowed one resolves, and when both are allowed the ids tell them apart.
func TestTwoOfOnePersonsSkillsWithOneName(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "bob", SkillRecord{ID: "a", Name: "Dup", Instructions: "First."})
	SaveSkill(db, "bob", SkillRecord{ID: "b", Name: "Dup", Instructions: "Second."})

	out, err := readSkill(t, db, "bob", []string{"b"}, "Dup")
	if err != nil || !strings.Contains(out, "Second.") {
		t.Errorf("the allowed one of two same-named skills was not found: %v\n%s", err, out)
	}
	enum := allowedSkillNames(db, "bob", []string{"a", "b"})
	if len(enum) != 2 || enum[0] == enum[1] {
		t.Fatalf("the enum lists one name twice: %v", enum)
	}
	for _, h := range enum {
		if _, err := readSkill(t, db, "bob", []string{"a", "b"}, h); err != nil {
			t.Errorf("handle %q from the enum does not resolve: %v", h, err)
		}
	}
}

// Taking a published skill back must not land it beside an own skill of the
// same name. Refused, with both left where they were.
func TestTakingASkillBackRefusesANameItWouldDuplicate(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Published."})
	if err := promoteSkillToDeployment("alice", "s1"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	SaveSkill(db, "alice", SkillRecord{ID: "s2", Name: "triage", Instructions: "Private."})

	err := NarrowSkillToOwner(db, "alice", "s1")
	if err == nil || !strings.Contains(err.Error(), "already have a skill called") {
		t.Fatalf("taking it back beside a same-named skill was not refused: %v", err)
	}
	if got := PublishedSkillsBy(db, "alice"); len(got) != 1 {
		t.Errorf("the refused take-back still moved it: %+v", got)
	}
	if got := LoadSkills(db, "alice"); len(got) != 1 || got[0].ID != "s2" {
		t.Errorf("the own pool changed: %+v", got)
	}
}

// Bundled tools attach only from the runtime user's own skills. A delivered
// shared skill carries none, and another person's own skill never attaches its
// scripts in this session just because an agent of theirs consulted it.
func TestDeliveredSkillToolsComeOnlyFromTheRunnersOwnSkills(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "x",
		Tools: []TempTool{{Name: "ssh_run"}}, AllowedUsers: []string{"bob"}})
	SaveSkill(db, "bob", SkillRecord{ID: "own", Name: "Mine", Instructions: "y",
		Tools: []TempTool{{Name: "calc"}}})

	sess := &ToolSession{}
	got := AttachDeliveredSkillTools(sess, db, "bob", map[string]bool{"s1": true, "own": true}, false)
	if strings.Join(got, ",") != "calc" {
		t.Errorf("attached %v; want only the runner's own skill's tool", got)
	}
	if sess.HasTempTool("ssh_run") {
		t.Error("another person's bundled script reached this session")
	}
}
