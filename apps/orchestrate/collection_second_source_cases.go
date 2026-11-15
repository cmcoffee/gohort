package orchestrate

// The second source's check tests (core/checktest): a claim from a secondary
// page, the pages read, and whether they back it, asked through the ruling's
// own prompt on the model a run is for.

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
	"github.com/cmcoffee/gohort/core/decide"
)

func init() {
	checktest.Register(checktest.Check{
		Key:   secondVerdict.Key,
		Label: "Knowledge second source",
		Cases: secondCases,
		Run:   runSecondCase,
	})
}

// secondCases: Input claim, site (the candidate's), pages (one
// "Title | URL | what it says" a line). Want: the answer.
func secondCases() []checktest.Case {
	c := func(id, title, want, claim string, pages ...string) checktest.Case {
		return checktest.Case{ID: id, Title: title, Want: want, Input: map[string]string{
			"claim": claim, "site": "someblog.example", "pages": strings.Join(pages, "\n")}}
	}
	const claim = "The example tool's audit log rotates daily and keeps 30 files."
	return []checktest.Case{
		c("confirmed", "Another site says the same", secondConfirmed, claim,
			"Logging reference | https://docs.vendor.example/logging | The audit log is rotated every day; the last 30 files are kept."),
		c("same-site", "Only the candidate's own site says it", secondUnconfirmed, claim,
			"Follow-up post | https://someblog.example/part-2 | As I wrote before, the audit log rotates daily and keeps 30 files."),
		c("copy", "A page that only quotes the candidate", secondUnconfirmed, claim,
			"Link roundup | https://roundup.example/week | someblog.example says: \"the audit log rotates daily and keeps 30 files\"."),
		c("contradicted", "Another site says otherwise", secondContradicted, claim,
			"Logging reference | https://docs.vendor.example/logging | The audit log is rotated weekly and 4 files are kept."),
		c("related-only", "A related claim, not the same one", secondUnconfirmed, claim,
			"Install guide | https://docs.vendor.example/install | The audit log is written to the state directory."),
	}
}

// runSecondCase asks one case through the ruling's prompt, laid out as the
// pipeline lays it out.
func runSecondCase(ctx context.Context, llm LLM, tc checktest.Case) checktest.Outcome {
	var list, says strings.Builder
	for i, ln := range strings.Split(tc.Input["pages"], "\n") {
		parts := strings.SplitN(ln, " | ", 3)
		if len(parts) != 3 {
			continue
		}
		n := string(rune('1' + i))
		list.WriteString("[" + n + "] " + parts[0] + " - " + parts[1] + "\n")
		says.WriteString("[" + n + "] " + parts[0] + "\n> " + parts[2] + "\n\n")
	}
	state := "## The claim\n" + tc.Input["claim"] + "\n\n## The candidate page\nhttps://" + tc.Input["site"] + "/post\nIts site: " + tc.Input["site"] +
		"\n\n## The pages read\n" + list.String() + "\n## What they say\n" + says.String()
	r, err := secondVerdict.Judge(ctx, decide.VerdictChat(llm.Chat), state)
	if err != nil {
		return checktest.Outcome{Got: ScanNoVerdict, Reason: err.Error()}
	}
	if r.Answer == "" {
		return checktest.Outcome{Got: ScanNoVerdict, Reason: "no answer"}
	}
	return checktest.Outcome{Got: r.Answer, Reason: r.Reason}
}
