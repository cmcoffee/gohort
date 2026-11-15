package orchestrate

// The collection check's check tests (core/checktest): a page about to go
// into a collection, what the collection already says, and what the check
// should answer, asked through the check's own prompt and reading
// (vetAgainst) on the model a run is for.

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

func init() {
	checktest.Register(checktest.Check{
		Key:   collectionVetKey,
		Label: "Knowledge page check",
		Cases: vetCases,
		Run:   runVetCase,
	})
}

// vetCases are the check's built-in cases. Input: purpose, excerpts (one
// "Title | text" a line), title, page. Want: the answer.
func vetCases() []checktest.Case {
	c := func(id, title, want, purpose, page string, excerpts ...string) checktest.Case {
		return checktest.Case{ID: id, Title: title, Want: want, Input: map[string]string{
			"purpose": purpose, "title": "Candidate page", "page": page, "excerpts": strings.Join(excerpts, "\n")}}
	}
	const tool = "Reference for running the example deployment tool."
	return []checktest.Case{
		c("new-ground", "Fits and covers something new", vetAdds, tool,
			"The tool's backup command copies the state directory to a dated archive. Run it before an upgrade.",
			"Install guide | Download the tool and put it on your path.", "Upgrade guide | Stop the service before upgrading."),
		c("contradicts", "A different default from another source", vetConflicts, tool,
			"A community blog: the tool's audit log is on by default, so nothing needs setting.",
			"Configuration reference, version 3 | The audit log is off by default; set audit=true to turn it on.", "Install guide | Download the tool and put it on your path."),
		c("newer-version", "The vendor's newer version of the same page", vetSupersedes, tool,
			"Configuration reference, version 4, from the tool's own documentation: as of version 4 the audit log is on by default.",
			"Configuration reference, version 3 | The audit log is off by default; set audit=true to turn it on."),
		c("same-again", "Says what the collection already says", vetDrop, tool,
			"To install the tool, download it and put it on your path.",
			"Install guide | Download the tool and put it on your path."),
		c("off-topic", "Does not fit what the collection is for", vetDrop, tool,
			"Ten easy weeknight pasta recipes for busy families.",
			"Install guide | Download the tool and put it on your path."),
		c("other-aspect", "A different aspect of the same thing", vetAdds, tool,
			"The audit log rotates daily and keeps 30 files.",
			"Configuration reference, version 3 | The audit log is off by default; set audit=true to turn it on."),
	}
}

// vetCaseExcerpts are a case's excerpts as the check is given them.
func vetCaseExcerpts(tc checktest.Case) []vetExcerpt {
	var out []vetExcerpt
	for _, ln := range strings.Split(tc.Input["excerpts"], "\n") {
		title, text, ok := strings.Cut(ln, " | ")
		if !ok {
			continue
		}
		out = append(out, vetExcerpt{DocID: "doc-" + strings.ToLower(strings.ReplaceAll(strings.TrimSpace(title), " ", "-")), Title: strings.TrimSpace(title), Text: strings.TrimSpace(text)})
	}
	return out
}

// runVetCase asks one case on llm through the check itself.
func runVetCase(ctx context.Context, llm LLM, tc checktest.Case) checktest.Outcome {
	c := Collection{ID: "case", Name: "Example tool reference", Description: tc.Input["purpose"]}
	res := vetAgainst(ctx, llm, c, vetCandidate{URL: "https://example.com/page", Title: tc.Input["title"], Text: tc.Input["page"]}, vetCaseExcerpts(tc))
	if res.Answer == "" {
		reason := "no answer"
		if res.Err != nil {
			reason = res.Err.Error()
		}
		return checktest.Outcome{Got: ScanNoVerdict, Reason: reason}
	}
	return checktest.Outcome{Got: res.Answer, Reason: res.Reason}
}
