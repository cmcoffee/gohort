package orchestrate

import (
	"fmt"
	"regexp"
	"strings"
)

// changeLogHeadingRe is a notes heading for a record of changes.
var changeLogHeadingRe = regexp.MustCompile(`(?i)\b(change ?log|changes|history|revisions?|log)\b`)

// notesTrimHint says where to cut notes that are over the cap: the largest
// section, and when the notes carry a change record, that it goes. A bare
// "trim it" sent Builder round after round shaving sentences off a summary
// while the changelog that had grown it stayed.
func notesTrimHint(notes string) string {
	type section struct {
		head string
		n    int
	}
	var secs []section
	cur := section{head: "(the opening, before any heading)"}
	for _, line := range strings.Split(notes, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") {
			if cur.n > 0 {
				secs = append(secs, cur)
			}
			cur = section{head: t}
		}
		cur.n += len([]rune(line)) + 1
	}
	if cur.n > 0 {
		secs = append(secs, cur)
	}
	for _, s := range secs {
		if changeLogHeadingRe.MatchString(strings.TrimLeft(s.head, "# ")) {
			return fmt.Sprintf("drop the %q section (%d characters): what changed and why goes in publish's note=, which the revision history keeps (app_def revisions), and resend", s.head, s.n)
		}
	}
	big := section{}
	for _, s := range secs {
		if s.n > big.n {
			big = s
		}
	}
	if big.n == 0 {
		return "trim it and resend"
	}
	return fmt.Sprintf("the largest section is %q (%d characters): cut there first, and resend", big.head, big.n)
}
