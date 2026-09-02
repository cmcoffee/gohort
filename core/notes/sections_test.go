package notes

import (
	"strings"
	"testing"
)

// The property the whole design rests on: reading a document and writing it
// back unchanged returns it byte for byte. A parser that tidies as it reads is
// one that loses things, and what it would lose here is the agent's own state.
func TestSplitJoinRoundTripsExactly(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":              "",
		"prose only":         "Drafting section 3. User wants the terse version.",
		"one section":        "## in flight\nDrafting section 3.\n",
		"preamble + section": "Loose opening line.\n\n## in flight\nDrafting.\n",
		"several":            "## a\none\n\n## b\ntwo\n\n## c\nthree\n",
		"no trailing nl":     "## a\none",
		"crlf":               "## a\r\none\r\n\r\n## b\r\ntwo\r\n",
		"blank lines":        "## a\n\n\n\nstill a\n\n\n",
		"heading no body":    "## a\n## b\nbody\n",
		"deeper headings":    "## a\n### not a register\nbody\n",
		"hash title":         "# Title\n\n## a\nbody\n",
	} {
		if got := joinSections(splitSections(doc)); got != doc {
			t.Errorf("%s: round trip changed the document:\n%q\n%q", name, doc, got)
		}
	}
}

// Only `## ` is a register. `#` is a document title and `###` is structure
// inside a section — a model writing sub-headings under its own register must
// not find them promoted to registers.
func TestOnlyLevelTwoHeadingsAreSections(t *testing.T) {
	secs := splitSections("# Title\nintro\n\n## real\nbody\n### deeper\nmore\n\n## also real\nx\n")
	var names []string
	for _, s := range secs {
		if s.name != "" {
			names = append(names, s.name)
		}
	}
	if len(names) != 2 || names[0] != "real" || names[1] != "also real" {
		t.Errorf("got %v, want the two ## headings", names)
	}
}

// Notes are where an agent parks shell and markdown samples. A fence full of
// `## comments` must not shatter the document.
func TestHeadingsInsideAFenceAreText(t *testing.T) {
	doc := "## setup\n```sh\n## not a heading\necho hi\n```\nstill setup\n"
	secs := splitSections(doc)
	if len(secs) != 1 || secs[0].name != "setup" {
		t.Fatalf("a fenced ## split the document: %d sections", len(secs))
	}
	if joinSections(secs) != doc {
		t.Error("round trip broke inside a fence")
	}
}

func TestApplyCreatesUpdatesAndRemoves(t *testing.T) {
	doc, err := ApplyNoteSection("", "in flight", "Drafting section 3.")
	if err != nil {
		t.Fatal(err)
	}
	if doc != "## in flight\nDrafting section 3.\n" {
		t.Fatalf("create: %q", doc)
	}

	doc, err = ApplyNoteSection(doc, "quirks", "llama slots rotate by LRU.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "## in flight\n") || !strings.Contains(doc, "## quirks\n") {
		t.Fatalf("append: %q", doc)
	}

	// Updated IN PLACE: an updated register stays where it was, so the block
	// can be read as a diff and the prompt prefix does not churn.
	doc, err = ApplyNoteSection(doc, "in flight", "Now on section 4.")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(doc, "## in flight") > strings.Index(doc, "## quirks") {
		t.Errorf("an update reordered the block: %q", doc)
	}
	if strings.Contains(doc, "section 3") {
		t.Errorf("the old body survived: %q", doc)
	}

	// Empty body removes it; the other register is untouched.
	doc, err = ApplyNoteSection(doc, "in flight", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc, "in flight") {
		t.Errorf("remove left it behind: %q", doc)
	}
	if !strings.Contains(doc, "llama slots rotate") {
		t.Errorf("remove took the wrong section: %q", doc)
	}

	// Removing what was never there is not an error and changes nothing.
	same, err := ApplyNoteSection(doc, "never existed", "")
	if err != nil || same != doc {
		t.Errorf("no-op remove changed the document: %q (%v)", same, err)
	}
}

// One register, however it is capitalized. Otherwise a model that varies its
// own wording keeps two copies of its state and they disagree.
func TestSectionMatchIsCaseAndSpaceInsensitive(t *testing.T) {
	doc, _ := ApplyNoteSection("", "In Flight", "first")
	doc, err := ApplyNoteSection(doc, "in   flight", "second")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(doc, "## ") != 1 {
		t.Fatalf("a second register was minted: %q", doc)
	}
	// Addressed, not renamed: the heading stays as the agent first wrote it.
	if !strings.Contains(doc, "## In Flight") {
		t.Errorf("the update renamed the section: %q", doc)
	}
	if !strings.Contains(doc, "second") || strings.Contains(doc, "first") {
		t.Errorf("body not replaced: %q", doc)
	}
}

// Text above the first heading is the agent's own preamble. A section write
// never disturbs it.
func TestThePreambleSurvivesASectionWrite(t *testing.T) {
	doc, err := ApplyNoteSection("Loose opening line.\n\n## a\nbody\n", "b", "new")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc, "Loose opening line.") {
		t.Errorf("preamble lost: %q", doc)
	}
	if !strings.Contains(doc, "## a\nbody") {
		t.Errorf("existing section lost: %q", doc)
	}
}

func TestApplyRefusesANamelessSection(t *testing.T) {
	if _, err := ApplyNoteSection("x", "  ", "y"); err == nil {
		t.Error("a nameless section must be refused — the caller means a whole rewrite")
	}
	if _, err := ApplyNoteSection("x", "two\nlines", "y"); err == nil {
		t.Error("a section name is one line")
	}
}

// The refusal is where the model decides what to do next, so it carries the one
// fact that makes the decision possible: which register is costing.
func TestOverCapAdviceNamesTheBiggest(t *testing.T) {
	doc := "## small\nx\n\n## huge\n" + strings.Repeat("y ", 200) + "\n"
	advice := OverCapAdvice(doc)
	if !strings.Contains(advice, `"huge"`) {
		t.Errorf("advice must name the biggest section: %q", advice)
	}
	if i, j := strings.Index(advice, "huge"), strings.Index(advice, "small"); i > j {
		t.Errorf("biggest first: %q", advice)
	}
	// One section is not a choice — naming it tells the reader nothing they
	// cannot see, so the message stays out of the way.
	if got := OverCapAdvice("## only\nbody\n"); got != "" {
		t.Errorf("a single section needs no advice, got %q", got)
	}
}
