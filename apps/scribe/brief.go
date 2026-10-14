// A guide's writing brief: what this document is for and how it is written,
// in its owner's words, followed by everything that writes it.
//
// The Curator and the Guide Author are general-purpose. Handed findings that
// are incident overviews, they file incident overviews, because nothing tells
// them the document is meant to be something else: a set of general lessons
// another model reads before it looks at a dump, say. The brief is where the
// owner says so, once, for the one guide it applies to. The user's Rules
// (rulesNamespace) are a different thing: standing preferences for every
// document they write.
//
// It reaches every writer: the Author on an interactive turn, the Author's
// background placements for the Curator (which also get the user's Rules,
// which they used to miss), and the Curator itself, which decides by it what
// to keep, what to fold into an existing section, and what to drop.

package scribe

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// guideBriefMax bounds a brief. It rides on every turn that writes the guide.
const guideBriefMax = 2000

// guideBriefSection is the brief as a prompt section, "" when there is none.
func guideBriefSection(g Guide) string {
	brief := strings.TrimSpace(g.Brief)
	if brief == "" {
		return ""
	}
	return "\n\nTHIS GUIDE'S WRITING BRIEF, from its owner: what the document is for and how it is written. Follow it in everything you write in it:\n" + brief
}

// authorBackgroundContext is what a background run of the Author on guideID
// is told before its task: the user's Rules and the guide's brief, the two
// things an interactive turn carries in its instructions.
func (T *Scribe) authorBackgroundContext(udb Database, user, guideID string) string {
	out := DocRulesSection(udb, rulesNamespace)
	if g, _, _, ok := resolveGuide(T.DB, udb, user, guideID); ok {
		out += guideBriefSection(g)
	}
	return strings.TrimSpace(out)
}
