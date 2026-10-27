package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/peershare"
	"github.com/cmcoffee/gohort/core/revisions"
	"github.com/cmcoffee/gohort/core/shareledger"
	"github.com/cmcoffee/snugforge/kvlite"
)

func boolRule() PlaybookRule {
	return PlaybookRule{
		Fact: "queue_draining",
		How:  "Read the consumer lag for the orders queue over the last five minutes.",
		Then: "Look at the consumer.",
		Else: "Look at the broker.",
	}
}

// A rule must name its fact and how to establish it, and carry at least one
// arm; a choice covers its values; nesting stops at two levels.
func TestPlaybookProblems(t *testing.T) {
	if p := boolRule().Problems("rule 1", 1); len(p) != 0 {
		t.Fatalf("a sound rule has no problems: %v", p)
	}
	bad := PlaybookRule{Fact: "a fact", Then: "x"}
	p := bad.Problems("rule 1", 1)
	if len(p) != 2 || !strings.Contains(p[0], "one word") || !strings.Contains(p[1], "how is required") {
		t.Fatalf("expected the fact-name and how problems, got %v", p)
	}
	if p := (PlaybookRule{Fact: "f", How: "h"}).Problems("r", 1); len(p) != 1 || !strings.Contains(p[0], "at least one arm") {
		t.Fatalf("an armless bool rule: %v", p)
	}
	choice := PlaybookRule{Fact: "state", How: "check", Type: "choice", Values: []string{"up", "down"}, Cases: map[string]string{"up": "fine", "sideways": "?"}}
	p = choice.Problems("r", 1)
	if len(p) != 1 || !strings.Contains(p[0], `"sideways"`) {
		t.Fatalf("a case outside the values: %v", p)
	}
	if p := (PlaybookRule{Fact: "state", How: "check", Type: "choice", Values: []string{"up"}}).Problems("r", 1); len(p) < 2 {
		t.Fatalf("one value and no arm are two problems: %v", p)
	}
	deep := boolRule()
	deep.Then, deep.ThenRule = "", &PlaybookRule{Fact: "l2", How: "h", ThenRule: &PlaybookRule{Fact: "l3", How: "h", Then: "x"}}
	p = deep.Problems("rule 1", 1)
	if len(p) != 1 || !strings.Contains(p[0], "rule 1.then_rule.then_rule") || !strings.Contains(p[0], "more than 2 levels") {
		t.Fatalf("three levels must be refused by path: %v", p)
	}
	if p := (PlaybookRule{Fact: "f", How: "h", Then: "x", ThenRule: &PlaybookRule{Fact: "g", How: "h", Then: "y"}}).Problems("r", 1); len(p) != 1 || !strings.Contains(p[0], "both prose and a rule") {
		t.Fatalf("an arm is prose or a rule: %v", p)
	}
	// Storage stores: a half-built rule saves (the editor's door), and the
	// record-level check still names it for the doors that refuse.
	if _, err := SaveSkill(memDB(t), "u", SkillRecord{Name: "s", Description: "d", Playbook: []PlaybookRule{bad}}); err != nil {
		t.Fatalf("save must store a half-built rule: %v", err)
	}
	if p := (SkillRecord{Playbook: []PlaybookRule{bad}}).PlaybookProblems(); len(p) == 0 || !strings.HasPrefix(p[0], "rule 1:") {
		t.Fatalf("record-level problems name the rule: %v", p)
	}
}

// The sentence reads the fields back the way the author would say them.
func TestPlaybookSentence(t *testing.T) {
	r := boolRule()
	if got := r.Sentence(); got != "Whenever this skill is consulted, establish queue_draining; if yes, Look at the consumer; if no, Look at the broker." {
		t.Fatalf("got %q", got)
	}
	r.When = []string{"stuck order", "order status"}
	r.Else, r.ElseRule = "", &PlaybookRule{Fact: "broker_up", How: "h", Then: "restart the consumer"}
	if got := r.Sentence(); !strings.HasPrefix(got, "When the message mentions stuck order or order status, establish queue_draining; if yes, Look at the consumer; if no, whenever this skill is consulted, establish broker_up; if yes, restart the consumer.") {
		t.Fatalf("got %q", got)
	}
	c := PlaybookRule{Fact: "state", How: "h", Type: "choice", Values: []string{"up", "down"}, Cases: map[string]string{"up": "fine", "down": "bad"}}
	if got := c.Sentence(); got != "Whenever this skill is consulted, establish state; if up, fine; if down, bad." {
		t.Fatalf("got %q", got)
	}
}

// A rule compiles to a one-phase unattended machine that validates, with
// the fact as a required declared output of the right type and the skill's
// tools on the step.
func TestPlaybookRuleCompilesToAValidMachine(t *testing.T) {
	skill := SkillRecord{Name: "Orders", AllowedTools: []string{"run_command"}, Playbook: []PlaybookRule{boolRule()}}
	def := boolRule().Machine(skill)
	if err := def.Validate(); err != nil {
		t.Fatalf("does not validate: %v", err)
	}
	if !def.Unattended || len(def.Phases) != 1 || def.Phases[0].Name != playbookEstablishPhase {
		t.Fatalf("one unattended establishing phase expected, got %+v", def)
	}
	ph := def.Phases[0]
	if len(ph.Output) != 2 || ph.Output[0].Name != "queue_draining" || ph.Output[0].Type != FieldBool || !ph.Output[0].Required {
		t.Fatalf("the fact must be the required bool output, got %+v", ph.Output)
	}
	if ph.Output[1].Name != PlaybookEvidenceField || !ph.Output[1].Required {
		t.Fatalf("the step must also report the evidence that decided it, got %+v", ph.Output[1])
	}
	if len(ph.Tools) != 1 || ph.Tools[0] != "run_command" {
		t.Fatalf("the step carries the skill's tools, got %v", ph.Tools)
	}
	if !strings.Contains(ph.Prompt, "consumer lag") || !strings.Contains(ph.Prompt, "{input}") {
		t.Fatalf("the prompt carries how and the input: %q", ph.Prompt)
	}
	choice := PlaybookRule{Fact: "state", How: "check", Type: "choice", Values: []string{"up", "down"}, Cases: map[string]string{"up": "a", "down": "b"}}
	cdef := choice.Machine(skill)
	if err := cdef.Validate(); err != nil {
		t.Fatalf("choice does not validate: %v", err)
	}
	if cdef.Phases[0].Output[0].Type != FieldString || !strings.Contains(cdef.Phases[0].Output[0].Desc, "up, down") {
		t.Fatalf("a choice fact is a string over its values, got %+v", cdef.Phases[0].Output[0])
	}
}

// Decide reads a bool from a bool or from the words a model writes, a choice
// case-insensitively, and refuses what decides nothing.
func TestPlaybookDecide(t *testing.T) {
	r := boolRule()
	for _, v := range []any{true, "true", "Yes", " y "} {
		if shown, arm, _, ok := r.Decide(v); !ok || shown != "true" || arm != "Look at the consumer." {
			t.Fatalf("%v must decide true: %q %q %v", v, shown, arm, ok)
		}
	}
	if _, arm, _, ok := r.Decide("no"); !ok || arm != "Look at the broker." {
		t.Fatal("no must decide false")
	}
	if _, _, _, ok := r.Decide("maybe"); ok {
		t.Fatal("maybe decides nothing")
	}
	if _, _, _, ok := r.Decide(3); ok {
		t.Fatal("a number decides nothing")
	}
	nested := boolRule()
	nested.Else, nested.ElseRule = "", &PlaybookRule{Fact: "broker_up", How: "h", Then: "x", Else: "y"}
	if _, arm, next, ok := nested.Decide(false); !ok || arm != "" || next == nil || next.Fact != "broker_up" {
		t.Fatal("a rule arm comes back as the nested rule")
	}
	c := PlaybookRule{Fact: "state", How: "h", Type: "choice", Values: []string{"Up", "down"}, Cases: map[string]string{"Up": "fine", "down": "bad"}}
	if shown, arm, _, ok := c.Decide("UP"); !ok || shown != "Up" || arm != "fine" {
		t.Fatalf("choice must match case-insensitively and show the declared value: %q %q %v", shown, arm, ok)
	}
	if _, _, _, ok := c.Decide("left"); ok {
		t.Fatal("a value outside the set decides nothing")
	}
}

// The fallback tells the model to establish the fact itself and gives every
// arm with its condition, nested rules included.
func TestPlaybookFallbackCarriesEveryArm(t *testing.T) {
	r := boolRule()
	r.Else, r.ElseRule = "", &PlaybookRule{Fact: "broker_up", How: "ping it", Then: "restart consumer", Else: "page the on-call"}
	fb := r.Fallback()
	for _, want := range []string{"Establish queue_draining first", "if true: Look at the consumer.", "if false: then Establish broker_up first: ping it", "if true: restart consumer", "if false: page the on-call"} {
		if !strings.Contains(fb, want) {
			t.Fatalf("missing %q in:\n%s", want, fb)
		}
	}
}

// A playbook fires on the framework's say-so when the skill's triggers or a
// rule's When match the turn; a skill with neither waits to be consulted.
func TestPlaybookApplies(t *testing.T) {
	s := SkillRecord{Triggers: []string{"disk"}, Playbook: []PlaybookRule{boolRule()}}
	if !s.PlaybookApplies("is this box low on disk?", nil) || s.PlaybookApplies("how are you", nil) {
		t.Fatal("the skill's triggers decide")
	}
	s.Triggers = nil
	s.Playbook[0].When = []string{"stuck order"}
	if !s.PlaybookApplies("order 12 is a stuck order", nil) || s.PlaybookApplies("is this box low on disk?", nil) {
		t.Fatal("a rule's when decides")
	}
	s.Playbook[0].When = nil
	if s.PlaybookApplies("is this box low on disk?", nil) {
		t.Fatal("no triggers and no when: the model consults, the framework does not fire")
	}
	s.Triggers, s.Disabled = []string{"disk"}, true
	if s.PlaybookApplies("disk", nil) {
		t.Fatal("a disabled skill never fires")
	}
}

// The available-skills list says a playbook skill establishes its facts on
// consult, so the model reaches for it early rather than working them out.
func TestAvailableSkillsNamesPlaybookFacts(t *testing.T) {
	out := RenderAvailableSkills([]SkillRecord{{Name: "Orders", Description: "orders help", Playbook: []PlaybookRule{boolRule()}}})
	if !strings.Contains(out, "playbook: consulting it establishes queue_draining") {
		t.Fatalf("missing playbook note:\n%s", out)
	}
}

// Skill names are what the model and the author type, and names collide: a
// user's own skill and one shared with them, a private skill and one that was
// published, two of one person's skills in older data. These pin that a
// collision is shown and refused, never resolved silently to whichever skill
// came first.

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

// Peer sharing on skills, from the recipient's side. A recipient gets the
// BEHAVIOUR: the skill activates on their turns, and they cannot edit, delete
// or inherit the owner's documents with it.

func skillShareStore(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	return db
}

func TestASharedSkillActivatesForItsRecipient(t *testing.T) {
	db := skillShareStore(t)
	if _, err := SaveSkill(db, "alice", SkillRecord{
		ID: "s1", Name: "Triage", Description: "Assess first", Instructions: "Assess.",
		AttachedCollections: []string{"col-1"}, AllowedUsers: []string{"bob"},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := sharedSkillsFor(db, "bob")
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("the recipient does not have it: %+v", got)
	}
	// The collection IDS travel; whether any of them resolves is decided in the
	// recipient's own namespace at retrieval time. Stripping them here meant a
	// skill and a collection deliberately shared with the same person still
	// could not work together.
	if len(got[0].AttachedCollections) != 1 {
		t.Errorf("the collection ids did not travel: %+v", got[0].AttachedCollections)
	}
	// It shows up where activation reads.
	avail := AvailableSkills(db, "bob")
	if len(avail) != 1 || avail[0].ID != "s1" {
		t.Errorf("it is not in the recipient's available set: %+v", avail)
	}
	// Somebody unnamed gets nothing.
	if got := AvailableSkills(db, "dana"); len(got) != 0 {
		t.Errorf("an unnamed user has it: %+v", got)
	}
}

// The record is the source of truth and the index is derived. A derived thing
// that can outvote its source is how a revoked share keeps working.
func TestRevokingASkillShareTakesEffect(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.", AllowedUsers: []string{"bob"}})
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})
	if got := sharedSkillsFor(db, "bob"); len(got) != 0 {
		t.Errorf("a revoked share survives: %+v", got)
	}
}

// A muted skill is muted for everybody. An owner who disabled it did not
// disable it only for themselves.
func TestADisabledSkillIsNotSharedEither(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		AllowedUsers: []string{"bob"}, Disabled: true})
	if got := sharedSkillsFor(db, "bob"); len(got) != 0 {
		t.Errorf("a disabled skill still activates for a recipient: %+v", got)
	}
}

// Deleting takes the shares with it: an index entry outliving its record points
// at nothing, which reads to a recipient as access they lost.
func TestDeletingASkillDropsItsShares(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.", AllowedUsers: []string{"bob"}})
	if !DeleteSkill(db, "alice", "s1") {
		t.Fatal("delete")
	}
	if got := peershare.List(db, sharedSkillsTable, "bob"); len(got) != 0 {
		t.Errorf("the index still points at a deleted skill: %+v", got)
	}
	if got := AvailableSkills(db, "bob"); len(got) != 0 {
		t.Errorf("the recipient still has it: %+v", got)
	}
}

// A recipient's own skill wins a name collision: it is the one they wrote.
func TestYourOwnSkillComesFirst(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Alice's.", AllowedUsers: []string{"bob"}})
	SaveSkill(db, "bob", SkillRecord{ID: "own", Name: "Triage", Instructions: "Bob's."})
	avail := AvailableSkills(db, "bob")
	if len(avail) != 2 || avail[0].ID != "own" {
		t.Errorf("the recipient's own skill is not first: %+v", avail)
	}
}

// A bundled tool does not travel either. Running it would run the owner's code
// in the recipient's session, under the recipient's credentials, with no
// approval anywhere — which is the one thing a tool share has to pass through.
func TestASharedSkillCarriesNoBundledTools(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		Tools:               []TempTool{{Name: "ssh_run"}, {Name: "page_oncall"}},
		AttachedCollections: []string{"col-1"},
		AllowedUsers:        []string{"bob"}})

	got := sharedSkillsFor(db, "bob")
	if len(got) != 1 {
		t.Fatalf("the recipient does not have it: %+v", got)
	}
	if len(got[0].Tools) != 0 {
		t.Errorf("the owner's tools travelled with the share: %+v", got[0].Tools)
	}
	// The collections are not the tools: an id resolves or it does not, and the
	// read path decides that per user. Only executable code is withheld here.
	if len(got[0].AttachedCollections) != 1 {
		t.Errorf("the collection ids were stripped as well: %+v", got[0].AttachedCollections)
	}
	// The owner's own copy is untouched: the strip is on the way out, not a
	// rewrite of what they built.
	own := LoadSkills(db, "alice")
	if len(own) != 1 || len(own[0].Tools) != 2 || len(own[0].AttachedCollections) != 1 {
		t.Errorf("the owner's own skill was stripped: %+v", own)
	}
}

// The recipient's model is TOLD what did not arrive. A skill whose steps
// reference a tool that is not there, with nothing saying so, is how a run
// invents a substitute and reports success.
func TestTheRecipientIsToldWhatDidNotArrive(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		Tools: []TempTool{{Name: "ssh_run"}}, AttachedCollections: []string{"col-1"},
		AllowedUsers: []string{"bob"}})

	block := skillInstructionsBlock(sharedSkillsFor(db, "bob")[0], nil)
	for _, want := range []string{"alice", "bundled tool"} {
		if !strings.Contains(block, want) {
			t.Errorf("the recipient is not told about %q:\n%s", want, block)
		}
	}
	// The owner's own turn says none of this.
	if b := skillInstructionsBlock(LoadSkills(db, "alice")[0], nil); strings.Contains(b, "did not come with it") {
		t.Errorf("the owner is told their own skill is incomplete:\n%s", b)
	}
}

// And the OWNER is told, because they are the only one who can fix it: share
// the tool as a tool, the collection as a collection, or reword the skill.
func TestTheOwnerIsToldTheirShareIsIncomplete(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		Tools: []TempTool{{Name: "ssh_run"}}, AllowedUsers: []string{"bob"}})
	sharedSkillsFor(db, "bob")

	notes := notices.List(db, "alice")
	if len(notes) != 1 || !strings.Contains(notes[0].Title, "Triage") {
		t.Fatalf("the owner was not told: %+v", notes)
	}
	// Not the recipient's problem to read about somebody else's configuration.
	if got := notices.List(db, "bob"); len(got) != 0 {
		t.Errorf("the recipient was told about the owner's setup: %+v", got)
	}
	// Fifty activations a day is one row with a count, not a stream.
	sharedSkillsFor(db, "bob")
	sharedSkillsFor(db, "bob")
	if notes := notices.List(db, "alice"); len(notes) != 1 {
		t.Errorf("repeats did not fold: %+v", notes)
	}
}

// A share with nothing withheld says nothing to anybody.
func TestACompleteShareIsQuiet(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		AllowedUsers: []string{"bob"}})
	if got := sharedSkillsFor(db, "bob"); len(got) != 1 {
		t.Fatalf("share: %+v", got)
	}
	if notes := notices.List(db, "alice"); len(notes) != 0 {
		t.Errorf("a complete share nagged its owner: %+v", notes)
	}
	if b := skillInstructionsBlock(sharedSkillsFor(db, "bob")[0], nil); strings.Contains(b, "did not come with it") {
		t.Errorf("a complete share claims something is missing:\n%s", b)
	}
}

// ----------------------------------------------------------------------
// The deployment rung
// ----------------------------------------------------------------------

// Publishing MOVES the record. One copy means an edit later is an edit
// everybody gets, and one answer to "whose is this".
func TestPublishingASkillMovesItToTheDeployment(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})

	if err := promoteSkillToDeployment("alice", "Triage"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := LoadSkills(db, "alice"); len(got) != 0 {
		t.Errorf("a copy stayed in the author's pool: %+v", got)
	}
	pub := DeploymentSkills(db)
	if len(pub) != 1 || pub[0].Name != "Triage" {
		t.Fatalf("the deployment does not have it: %+v", pub)
	}
	// The author's name stays on it. A published skill nobody is answerable
	// for is worse than none.
	if pub[0].Owner != "alice" {
		t.Errorf("authorship was dropped: %q", pub[0].Owner)
	}
	// And it reaches somebody who was never named anywhere.
	avail := AvailableSkills(db, "dana")
	if len(avail) != 1 || avail[0].Name != "Triage" {
		t.Errorf("a published skill does not reach an ordinary user: %+v", avail)
	}
}

// The bundled tools do not go. Everything true of that for one recipient is
// more true for every account at once: it would run the author's scripts in
// every session in the deployment, under each person's own credentials.
func TestAPublishedSkillCarriesNoTools(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		Tools:               []TempTool{{Name: "ssh_run"}},
		AttachedCollections: []string{"col-1"},
		AllowedUsers:        []string{"bob"}})
	promoteSkillToDeployment("alice", "s1")

	pub := DeploymentSkills(db)
	if len(pub) != 1 {
		t.Fatalf("publish: %+v", pub)
	}
	if len(pub[0].Tools) != 0 {
		t.Errorf("the author's tools went deployment-wide: %+v", pub[0].Tools)
	}
	// The collection ids stay: they are references, gated where they are read.
	if len(pub[0].AttachedCollections) != 1 {
		t.Errorf("the collection references were dropped: %+v", pub[0].AttachedCollections)
	}
	// The peer list goes, because everybody has it now — and leaving it would
	// have a later un-publishing silently restore an ACL the owner had forgotten.
	if len(pub[0].AllowedUsers) != 0 {
		t.Errorf("the old recipient list survived publication: %+v", pub[0].AllowedUsers)
	}
	if got := peershare.List(db, sharedSkillsTable, "bob"); len(got) != 0 {
		t.Errorf("the share index still points at the moved skill: %+v", got)
	}
}

// Your own skill wins over one the deployment publishes. Three tiers, most
// specific first.
func TestYourOwnSkillBeatsTheDeploymentsCopy(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Alice's."})
	promoteSkillToDeployment("alice", "s1")
	SaveSkill(db, "bob", SkillRecord{ID: "own", Name: "Triage", Instructions: "Bob's."})

	avail := AvailableSkills(db, "bob")
	if len(avail) != 2 || avail[0].ID != "own" {
		t.Fatalf("the user's own skill is not first: %+v", avail)
	}
}

// A name the deployment already publishes is refused. A turn matching that name
// has to have one answer.
func TestPublishingRefusesADuplicateName(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Alice's."})
	promoteSkillToDeployment("alice", "s1")
	SaveSkill(db, "carol", SkillRecord{ID: "s2", Name: "triage", Instructions: "Carol's."})

	err := promoteSkillToDeployment("carol", "s2")
	if err == nil {
		t.Fatal("a second skill of the same name was published")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
	// And the refused one is untouched.
	if got := LoadSkills(db, "carol"); len(got) != 1 {
		t.Errorf("the refused publish consumed the author's skill anyway: %+v", got)
	}
}

// Taking it back is the author's alone: nobody needs permission to stop
// publishing something they wrote.
func TestAnAuthorCanTakeTheirSkillBack(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})
	promoteSkillToDeployment("alice", "s1")

	if err := NarrowSkillToOwner(db, "bob", "s1"); err == nil {
		t.Error("somebody unpublished a skill that was not theirs")
	}
	if err := NarrowSkillToOwner(db, "alice", "s1"); err != nil {
		t.Fatalf("take back: %v", err)
	}
	if got := DeploymentSkills(db); len(got) != 0 {
		t.Errorf("it is still published: %+v", got)
	}
	if got := LoadSkills(db, "alice"); len(got) != 1 || got[0].Name != "Triage" {
		t.Errorf("it did not come home: %+v", got)
	}
	if got := AvailableSkills(db, "dana"); len(got) != 0 {
		t.Errorf("it still reaches everybody: %+v", got)
	}
}

// The author's own mute still counts once it is published: an admin approved
// publishing a skill, not publishing it regardless of what its author later
// decided about it.
func TestADisabledPublishedSkillActivatesForNobody(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})
	promoteSkillToDeployment("alice", "s1")

	pub := DeploymentSkills(db)
	pub[0].Disabled = true
	skillStore(db).Set(deploymentSkillsTable, "all", pub)

	if got := AvailableSkills(db, "dana"); len(got) != 0 {
		t.Errorf("a muted deployment skill still activates: %+v", got)
	}
}

// Muting a published skill hides it from everybody's turns. It must not hide it
// from the pool's own writes: publishing another skill, or taking another back,
// rebuilt the pool from the published list and erased every muted one.
func TestPublishingAndTakingBackKeepMutedDeploymentSkills(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.", Disabled: true})
	SaveSkill(db, "carol", SkillRecord{ID: "s2", Name: "Escalate", Instructions: "Page."})
	SaveSkill(db, "carol", SkillRecord{ID: "s3", Name: "Close", Instructions: "Close."})

	// A muted skill publishes as muted, and its author still sees it.
	if err := promoteSkillToDeployment("alice", "s1"); err != nil {
		t.Fatalf("publish muted: %v", err)
	}
	if got := PublishedSkillsBy(db, "alice"); len(got) != 1 || !got[0].Disabled {
		t.Fatalf("the author cannot see their muted published skill: %+v", got)
	}
	// Somebody else publishes, then takes one back.
	promoteSkillToDeployment("carol", "s2")
	promoteSkillToDeployment("carol", "s3")
	if err := NarrowSkillToOwner(db, "carol", "s3"); err != nil {
		t.Fatalf("take back: %v", err)
	}
	var muted *SkillRecord
	for _, s := range allDeploymentSkills(db) {
		if s.ID == "s1" {
			c := s
			muted = &c
		}
	}
	if muted == nil || !muted.Disabled {
		t.Fatalf("another skill's publish or take-back erased the muted one: %+v", allDeploymentSkills(db))
	}
	// The muted one is still its author's to take back, still muted.
	if err := NarrowSkillToOwner(db, "alice", "s1"); err != nil {
		t.Fatalf("a muted published skill could not be taken back: %v", err)
	}
	if got := LoadSkills(db, "alice"); len(got) != 1 || !got[0].Disabled {
		t.Errorf("taking it back lost the mute: %+v", got)
	}
	if got := DeploymentSkills(db); len(got) != 1 || got[0].ID != "s2" {
		t.Errorf("the published list is wrong: %+v", got)
	}
}

// The author edits a published skill in place, and only the author can. The
// edit stays in the deployment pool and cannot bring the bundled tools or a
// peer list back.
func TestAnAuthorCanEditTheirPublishedSkill(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess.",
		Tools: []TempTool{{Name: "ssh_run"}}})
	promoteSkillToDeployment("alice", "s1")

	pub := PublishedSkillsBy(db, "alice")[0]
	pub.Instructions = "Assess, then page."
	pub.Disabled = true
	pub.Tools = []TempTool{{Name: "ssh_run"}}
	pub.AllowedUsers = []string{"bob"}
	if _, err := saveDeploymentSkill(db, "bob", pub, "update"); err == nil {
		t.Error("somebody edited a published skill that was not theirs")
	}
	// Through the ordinary save, which every author-facing surface uses.
	if _, err := SaveSkill(db, "alice", pub); err != nil {
		t.Fatalf("author edit: %v", err)
	}
	got := PublishedSkillsBy(db, "alice")
	if len(got) != 1 || got[0].Instructions != "Assess, then page." || !got[0].Disabled {
		t.Fatalf("the edit did not land in the deployment pool: %+v", got)
	}
	if len(got[0].Tools) != 0 || len(got[0].AllowedUsers) != 0 {
		t.Errorf("an edit brought back what publishing dropped: %+v", got[0])
	}
	if len(LoadSkills(db, "alice")) != 0 {
		t.Error("the edit made a second copy in the author's pool")
	}
	if len(DeploymentSkills(db)) != 0 {
		t.Error("a muted published skill still activates")
	}
}

// Publishing MOVES a skill: same owner, same id. Its history came from the
// author's pool and must still be there, and an edit to the published copy
// is filed in the same ring. And muting a published skill does not take it
// off the author's list of what they have out there.
func TestAPublishedSkillKeepsItsHistoryAndStaysOnTheAuthorsList(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "v1"})
	s := LoadSkills(db, "alice")[0]
	s.Instructions = "v2"
	SaveSkill(db, "alice", s)
	store, key := SkillRevisionRing(db, "alice", "s1")
	before := len(revisions.List(store, revisions.KindSkill, key))
	if before == 0 {
		t.Fatal("setup: the edit should have filed a revision")
	}
	if err := promoteSkillToDeployment("alice", "s1"); err != nil {
		t.Fatal(err)
	}
	if n := len(revisions.List(store, revisions.KindSkill, key)); n != before {
		t.Fatalf("publishing dropped the history: %d before, %d after", before, n)
	}
	pub := PublishedSkillsBy(db, "alice")[0]
	pub.Instructions = "v3"
	pub.Disabled = true
	if _, err := SaveSkill(db, "alice", pub); err != nil {
		t.Fatal(err)
	}
	if n := len(revisions.List(store, revisions.KindSkill, key)); n != before+1 {
		t.Errorf("an edit to the published skill filed no revision: %d, want %d", n, before+1)
	}
	listed := false
	for _, g := range shareledger.Mine("alice") {
		if g.ID == "s1" {
			listed = true
		}
	}
	if !listed {
		t.Error("a muted published skill fell off its author's list of what they share")
	}
}

// Importing a recipe of a skill the importer has published wrote the import
// over the deployment copy (SaveSkill routes a published id there), disabled,
// so it vanished for everybody while the result said "imported".
func TestImportingYourOwnPublishedSkillLeavesItAlone(t *testing.T) {
	db := skillShareStore(t)
	SaveSkill(db, "alice", SkillRecord{ID: "s1", Name: "Triage", Instructions: "Assess."})
	if err := promoteSkillToDeployment("alice", "s1"); err != nil {
		t.Fatal(err)
	}
	recipe, _ := json.Marshal(SkillRecord{ID: "s1", Name: "Triage", Instructions: "Something else."})
	_, skipped, err := (skillArtifact{}).ImportArtifact(db, recipe, "alice")
	if err != nil || skipped == "" {
		t.Fatalf("the import should be skipped, got skipped=%q err=%v", skipped, err)
	}
	pub := PublishedSkillsBy(db, "alice")
	if len(pub) != 1 || pub[0].Instructions != "Assess." || pub[0].Disabled {
		t.Errorf("the deployment copy was changed by the import: %+v", pub)
	}
	if len(DeploymentSkills(db)) != 1 {
		t.Error("the published skill stopped activating for everybody")
	}
}

// A trigger matches a word or phrase, not any run of letters. As a plain
// substring, an agent with "PC" among its triggers (penal code) was offered
// for "the pcitrues just get stretched out", a typo of pictures on a turn
// about a web page's layout.
func TestATriggerMatchesWholeWords(t *testing.T) {
	for _, c := range []struct {
		trigger, msg string
		want         bool
	}{
		{"pc", "actually let's make the lay out more uniform the pcitrues just get stretched out", false},
		{"act", "actually, never mind", false},
		{"form", "make the layout more uniform", false},
		{"pc", "what does PC 372 cover?", true},
		{"pc", "is that a (PC) offense", true},
		{"penal code", "under the Penal Code, section 12", true},
		{"felony", "two felony counts", true},
		{"contract", "review these contracts", true},
		{"sentence", "she was sentenced in May", true},
		{"refund", "can I get a refund?", true},
		{"refund", "refunding it now", true},
		{"law", "my lawn needs mowing", false},
		{"law", "what does the law say", true},
		{"law", "the law's reach", true},
		{"c++", "a c++ question", true},
		{"café", "the café opens", true},
		{"café", "cafés nearby", true},
	} {
		if got := TriggersMatch([]string{c.trigger}, c.msg, nil); got != c.want {
			t.Errorf("%q in %q: got %v, want %v", c.trigger, c.msg, got, c.want)
		}
	}
	if !TriggersMatch([]string{"*.pdf"}, "see attached", []string{"Contract.PDF"}) {
		t.Error("a glob still matches attachment names")
	}
}

// The hint rides on the person's message, and a model that read it as theirs
// answered it ("the trigger about X is a false match"). It says they cannot
// see it.
func TestTheSkillHintSaysThePersonCannotSeeIt(t *testing.T) {
	if h := SkillTriggerHintBlock([]string{"Triage"}); !strings.Contains(h, "cannot see it, so never mention it") {
		t.Errorf("hint: %q", h)
	}
}
