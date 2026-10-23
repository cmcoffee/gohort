package scribe

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A distill of a raw troubleshooting guide into a guide about reading logs
// brought the commands with it. Three steps let them through, and each is held
// to the receiving guide's brief now:
//
//   - the distill was told to make each piece "complete enough to use without
//     the source", which for a log line meant the command beside it;
//   - the Curator read "for guide" as "belongs there", with the brief one
//     lookup away;
//   - the Author was told to keep what the finding carries.
func TestADistilledPieceIsHeldToTheBriefAllTheWay(t *testing.T) {
	if !strings.Contains(distillPrompt, "The brief limits what goes INTO a piece") || strings.Contains(distillPrompt, "complete enough to use without the source") {
		t.Errorf("the distill keeps out of a piece what the brief excludes:\n%s", distillPrompt)
	}

	app := &Scribe{}
	app.DB = testStore(t)
	udb := UserDB(app.DB, "u")
	saveGuide(udb, Guide{ID: "logs", Owner: "u", Title: "Reading the logs", Brief: "Log lines and what they mean.\nNo commands or fixes."})
	saveGuide(udb, Guide{ID: "open", Owner: "u", Title: "Open notes"})
	pending := []DocFinding{
		{ID: "f1", Topic: "timeout line", Content: "x", Document: "logs"},
		{ID: "f2", Topic: "retry line", Content: "y", Document: "logs"},
		{ID: "f3", Topic: "misc", Content: "z", Document: "open"},
	}
	p := curatorBatchPrompt(udb, pending)
	head := p[:strings.Index(p, "### finding")]
	if strings.Count(head, "Reading the logs (guide_id logs): Log lines and what they mean. No commands or fixes.") != 1 {
		t.Errorf("the brief of the guide findings are for sits above them, once:\n%s", head)
	}
	if !strings.Contains(head, "Open notes (guide_id open): (none written") {
		t.Errorf("a guide with no brief says so:\n%s", head)
	}

	raw, err := os.ReadFile("curator.go")
	if err != nil {
		t.Fatal(err)
	}
	if src := string(raw); !strings.Contains(src, "It is not proof that it belongs there. Judge it by that guide's brief") || strings.Contains(src, "the guide is where it belongs. Place it there") {
		t.Error("the Curator judges a marked finding by its guide's brief, not by the mark")
	}

	if app.briefKeepRule(udb, "u", "logs") != briefKeepText || app.briefKeepRule(udb, "u", "open") != "" {
		t.Error("the Author is held to a brief when the guide has one, and only then")
	}
	for file, call := range map[string]string{"coauthor.go": "prompt += T.briefKeepRule(udb, user, guideID)", "curator.go": "prompt += T.briefKeepRule(udb, user, guideID)"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), call) {
			t.Errorf("%s: a background write is not held to the brief", file)
		}
	}
	if raw, _ := os.ReadFile("coauthor.go"); strings.Contains(string(raw), "preserve any values/citations the finding carries") {
		t.Error("the Author is no longer told to keep everything the finding carries")
	}
}

// What the Author says about a write, including what it left out for the
// brief, is kept on the digest entry, where it used to be thrown away.
func TestTheAuthorsNoteIsKeptForTheDigest(t *testing.T) {
	if got := authorNote("  Merged the timeout line.\n Left out: the restart command (not in the brief). "); got != "Merged the timeout line. Left out: the restart command (not in the brief)." {
		t.Errorf("got %q", got)
	}
	if got := authorNote(strings.Repeat("x", 500)); len([]rune(got)) != authorNoteMax+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("a long note is cut: %d runes", len([]rune(got)))
	}
	if authorSaid("") != "" || !strings.HasPrefix(authorSaid("ok"), " The Author said: ok") {
		t.Error("the Curator hears the Author's note, and nothing when there is none")
	}
}

// draft_section writes from a guide's Sources in one call of its own, and that
// call never saw the brief: with a raw troubleshooting guide attached as the
// Source of a guide about reading logs, it wrote the commands in too.
func TestADraftFromSourcesIsHeldToTheBrief(t *testing.T) {
	rule := draftBriefRule(Guide{Title: "Reading the logs", Brief: "Log lines and what they mean. No commands."})
	if !strings.Contains(rule, "No commands.") || !strings.Contains(rule, "Write in only what the brief says belongs") {
		t.Errorf("the draft is given the brief and told to keep to it:\n%s", rule)
	}
	if draftBriefRule(Guide{Title: "Open notes"}) != "" {
		t.Error("a guide with no brief adds nothing")
	}
	raw, err := os.ReadFile("coauthor.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "BannedWordsRule + draftBriefRule(g)") {
		t.Error("draft_section's call does not carry the brief")
	}
}

// Update from sources had the Author add a section for "something important
// the guide is missing" in its sources, and important was never measured
// against the brief: a raw troubleshooting guide as a Source made its
// commands important to a guide about reading logs. Both of its options now
// take only what the brief says belongs.
func TestUpdateFromSourcesKeepsToTheBrief(t *testing.T) {
	for _, text := range []string{updateSourcesEvery, updateSourcesOther} {
		if strings.Contains(text, "something important the guide is missing") || !strings.Contains(text, "writing brief says belongs in it") {
			t.Errorf("an Update from sources instruction does not keep to the brief:\n%s", text)
		}
		if !strings.Contains(text, "set to Distill") {
			t.Errorf("the Author is not told to leave the Distill guides to the distill:\n%s", text)
		}
	}
	if !strings.Contains(guideUpdateSourcesAction, updateSourcesTexts()) {
		t.Error("the button does not carry the instructions")
	}
}
