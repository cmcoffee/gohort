package checktest

// The last run as plain text, to paste somewhere else for a review: what was
// run on what, where each check stands on the map, and in full each case a
// model got wrong or could not answer, with the case's own input. The cases
// every model got right are only counted.

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// reportInputClip is how much of one input field the report shows.
const reportInputClip = 600

// TextReport is rep as plain text. header is lines about the install the caller
// knows (its version, the model on each tier), written first.
func TextReport(rep RunReport, header []string) string {
	var b strings.Builder
	b.WriteString("Check tests report\n")
	for _, h := range header {
		b.WriteString(h + "\n")
	}
	if rep.At.IsZero() {
		b.WriteString("\nNot run yet.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Run: %s, asked of %s", rep.At.Format("2006-01-02 15:04 MST"), strings.Join(rep.Models, ", "))
	if rep.Canceled {
		b.WriteString(" (stopped early)")
	}
	b.WriteString("\nSummary: " + Summary(rep) + "\n")

	b.WriteString("\nMap:\n")
	for _, ch := range Checks() {
		if !ch.Structured {
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): Offload model: %s; System One: %s\n", ch.Label, ch.Key, Standing(ch.Key, MapJudge), Standing(ch.Key, MapSystemOne))
	}

	inputs := caseInputs()
	var wrong, unsure []Row
	right := 0
	for _, r := range rep.Rows {
		bad, doubt := false, false
		for _, a := range r.Answers {
			switch {
			case a.Err != "" || (!a.Pass && !a.Unsure):
				bad = true
			case a.Unsure:
				doubt = true
			}
		}
		switch {
		case bad:
			wrong = append(wrong, r)
		case doubt:
			unsure = append(unsure, r)
		default:
			right++
		}
	}

	fmt.Fprintf(&b, "\nWrong or failed (%d):\n", len(wrong))
	if len(wrong) == 0 {
		b.WriteString("none\n")
	}
	for _, r := range wrong {
		fmt.Fprintf(&b, "\n[%s] %s (%s / %s)\n  want: %s\n", r.Label, r.Title, r.Check, r.Case, r.Want)
		if in := inputs[r.Check+"\x00"+r.Case]; len(in) > 0 {
			b.WriteString("  input:\n")
			keys := make([]string, 0, len(in))
			for k := range in {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, "    %s: %s\n", k, indentLines(clipInput(in[k]), "      "))
			}
		}
		for _, m := range rep.Models {
			if a, ok := r.Answers[m]; ok {
				b.WriteString("  " + answerLine(m, a, true) + "\n")
			}
		}
	}

	fmt.Fprintf(&b, "\nUnsure, not used, so no harm (%d):\n", len(unsure))
	if len(unsure) == 0 {
		b.WriteString("none\n")
	}
	for _, r := range unsure {
		var said []string
		for _, m := range rep.Models {
			if a, ok := r.Answers[m]; ok && a.Unsure {
				said = append(said, answerLine(m, a, false))
			}
		}
		fmt.Fprintf(&b, "- [%s] %s, want %s: %s\n", r.Label, r.Title, r.Want, strings.Join(said, "; "))
	}

	fmt.Fprintf(&b, "\nRight on every model: %d of %d cases.\n", right, len(rep.Rows))
	return b.String()
}

// answerLine is one model's answer to a case: what it said, how long it
// took, how it was graded, its confidence where it has one, and with why,
// its reason or error.
func answerLine(model string, a Answer, why bool) string {
	grade := "right"
	switch {
	case a.Err != "":
		grade = "FAIL (no answer)"
	case a.HandOff:
		grade = agreedWord(a.Right) + ", handed to the full check"
	case a.Unsure:
		grade = agreedWord(a.Right) + ", unsure"
	case !a.Pass:
		grade = "WRONG"
	}
	line := fmt.Sprintf("%s: %s, %dms, %s", model, a.Got, a.TookMS, grade)
	if a.Confidence > 0 || a.Bar > 0 {
		line += fmt.Sprintf(" (confidence %.2f", a.Confidence)
		if a.Bar > 0 {
			line += fmt.Sprintf(", needs %.2f", a.Bar)
		}
		line += ")"
	}
	if !why {
		return line
	}
	if a.Err != "" {
		return line + "\n    error: " + indentLines(a.Err, "    ")
	}
	if a.Reason != "" {
		return line + "\n    reason: " + indentLines(a.Reason, "    ")
	}
	return line
}

// agreedWord is right or wrong, for an answer graded but not used.
func agreedWord(right bool) string {
	if right {
		return "right"
	}
	return "wrong"
}

// caseInputs is every registered case's input, by check key and case ID.
func caseInputs() map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, ch := range Checks() {
		if ch.Cases == nil {
			continue
		}
		for _, c := range ch.Cases() {
			out[ch.Key+"\x00"+c.ID] = c.Input
		}
	}
	return out
}

// clipInput is one input field cut to reportInputClip, ending in "...".
func clipInput(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > reportInputClip {
		return string(r[:reportInputClip]) + "..."
	}
	return s
}

// indentLines indents every line of s after its first by pad.
func indentLines(s, pad string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n"+pad)
}
