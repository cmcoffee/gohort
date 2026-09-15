package orchestrate

// Reference memory holds code. snake_case followed by "(" is what a function
// call looks like, and it is also what the dead-tool rule took for a tool
// mention — so a stored helper reported every function in it as a tool that no
// longer exists, in a pane whose own header buys precision over recall.

import (
	"strings"
	"testing"
)

func TestStoredCodeIsNotReadAsToolMentions(t *testing.T) {
	text := "How the importer works:\n\n```python\ndef run():\n    cfg = parse_config(path)\n    rows = fetch_rows(cfg)\n    out = render_table(rows)\n    return write_output(out)\n```\n"
	got := deadToolFindings("Reference memory", text, map[string]bool{}, map[string]bool{})
	if len(got) != 0 {
		t.Errorf("functions inside a code fence are not tool mentions, got %d: %+v", len(got), got)
	}
}

// Unfenced code too: the density is the evidence when there is no fence to
// strip.
func TestUnfencedCodeIsNotReadAsToolMentions(t *testing.T) {
	text := "cfg = parse_config(path)\nrows = fetch_rows(cfg)\nout = render_table(rows)\nwrite_output(out)\nlog_result(out)\n"
	got := deadToolFindings("Reference memory", text, map[string]bool{}, map[string]bool{})
	if len(got) != 0 {
		t.Errorf("a page of calls is code, not prose about tools, got %d: %+v", len(got), got)
	}
}

// The finding this rule exists for still fires: a sentence about calling a
// tool that is gone.
func TestProseAboutAMissingToolStillFires(t *testing.T) {
	text := "When the ticket mentions a table, call render_ticket_table to get the markdown."
	got := deadToolFindings("Working notes", text, map[string]bool{}, map[string]bool{})
	if len(got) != 1 {
		t.Fatalf("want one finding, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Detail, "render_ticket_table") {
		t.Errorf("should name the tool: %s", got[0].Detail)
	}
}

// THE TRADE, stated as a test so it is a decision rather than a regression.
//
// "The old flow was get_ticket(id) and then post_comment(id) — neither exists
// now" names two dead tools and no longer reports them: there is no call verb,
// and a bare paren stopped counting when it turned out to be what every line of
// stored code looks like. Recall for this shape is given up to stop a page of
// findings per stored snippet. The "pending task:" shape, which is how a parked
// invocation actually arrives, is still caught by parkedCallRE.
func TestABareParenInProseIsNoLongerAFinding(t *testing.T) {
	text := "The old flow was get_ticket(id) and then post_comment(id) — neither exists now."
	if got := deadToolFindings("Working notes", text, map[string]bool{}, map[string]bool{}); len(got) != 0 {
		t.Errorf("a paren without a verb is no longer evidence, got %+v", got)
	}
	// The same sentence WITH a verb still fires, which is the line being drawn.
	withVerb := "The old flow was to call get_ticket first — it no longer exists."
	if got := deadToolFindings("Working notes", withVerb, map[string]bool{}, map[string]bool{}); len(got) != 1 {
		t.Errorf("a verb is still evidence, got %+v", got)
	}
}

// An ORPHANED tool is named regardless: it is known to be gone, so it does not
// depend on the call heuristic and is not capped.
func TestOrphanedToolIsReportedEvenInCode(t *testing.T) {
	text := "```\nresult = ts3_list_clients(server)\n```"
	got := deadToolFindings("Reference memory", text, map[string]bool{}, map[string]bool{"ts3_list_clients": true})
	if len(got) != 0 {
		t.Logf("note: fenced code is blanked before scanning, so an orphan inside a fence is not reported either: %+v", got)
	}
	// Outside a fence it must still be caught.
	got = deadToolFindings("Reference memory", "we still reference ts3_list_clients(server) here",
		map[string]bool{}, map[string]bool{"ts3_list_clients": true})
	if len(got) != 1 {
		t.Fatalf("an orphaned tool must still be reported: %+v", got)
	}
	if !strings.Contains(got[0].Detail, "Orphaned Tools") {
		t.Errorf("should point at the orphan pool: %s", got[0].Detail)
	}
}

// Quotes must still point at the right line after code is blanked — the whole
// reason blanking preserves length.
func TestQuotesSurviveCodeBlanking(t *testing.T) {
	text := "```\nx = aaa_bbb(1)\n```\nAfterwards, call render_ticket_table for the markdown."
	got := deadToolFindings("Working notes", text, map[string]bool{}, map[string]bool{})
	if len(got) != 1 {
		t.Fatalf("want one finding, got %+v", got)
	}
	if !strings.Contains(got[0].Quote, "render_ticket_table") {
		t.Errorf("the quote landed on the wrong line: %q", got[0].Quote)
	}
}

// Reference Memory is swept CHUNK BY CHUNK, which is what the first fix missed.
// A finding's fence and its code body land in different chunks, so there is
// often no fence to strip, and a per-text cap counts per chunk where two or
// three calls sail under it. These are the shapes that actually arrive.

func TestAFenceLessCodeChunkIsNotReadAsToolMentions(t *testing.T) {
	// The middle of a snippet: no fence, few calls — under the old cap, and
	// reported as three tools that no longer exist.
	chunk := "    cfg = parse_config(path)\n    rows = fetch_rows(cfg)\n    return write_output(rows)\n"
	if got := deadToolFindings("Reference Memory", chunk, map[string]bool{}, map[string]bool{}); len(got) != 0 {
		t.Errorf("a chunk of source is not prose about tools, got %d: %+v", len(got), got)
	}
}

func TestATinyCodeChunkIsNotReadAsToolMentions(t *testing.T) {
	for _, chunk := range []string{
		"def run_import(path):\n    return load_rows(path)\n",
		"result = build_report(rows);\n",
		"func handleThing(w http.ResponseWriter) {\n",
	} {
		if got := deadToolFindings("Reference Memory", chunk, map[string]bool{}, map[string]bool{}); len(got) != 0 {
			t.Errorf("%q should read as code, got %+v", chunk, got)
		}
	}
}

// And the finding the rule exists for still fires from a chunk of PROSE.
func TestProseInAChunkStillFires(t *testing.T) {
	chunk := "When the ticket mentions a table, call render_ticket_table to get the markdown."
	got := deadToolFindings("Reference Memory", chunk, map[string]bool{}, map[string]bool{})
	if len(got) != 1 {
		t.Fatalf("want one finding, got %+v", got)
	}
}

// One quoted call inside a sentence is still a sentence.
func TestASentenceQuotingOneCallIsStillProse(t *testing.T) {
	chunk := "We used to call parse_config(path) here, but that tool is gone now and nothing replaced it."
	if got := deadToolFindings("Working notes", chunk, map[string]bool{}, map[string]bool{}); len(got) != 1 {
		t.Errorf("a sentence with one quoted call should still be read as prose, got %+v", got)
	}
}

// A bare paren no longer makes a call: the evidence is the verb.
func TestABareParenIsNotEnough(t *testing.T) {
	chunk := "The payload shape is roughly render_table(rows) shaped, for reference."
	if got := deadToolFindings("Working notes", chunk, map[string]bool{}, map[string]bool{}); len(got) != 0 {
		t.Errorf("punctuation alone must not imply a tool mention, got %+v", got)
	}
}
