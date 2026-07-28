// TechWriter's per-user Rules. Storage, the HTTP handler, and the
// system-prompt formatting all live in core (DocRulesSection /
// HandleDocRules) so every writer app offers the same feature; this file
// is just the namespace binding.
//
// The namespace resolves to the same "techwriter_rules" table the
// app-local implementation used, so existing rules carry over untouched.

package techwriter

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
)

const rulesNamespace = "techwriter"

func (T *TechWriterAgent) handleRules(w http.ResponseWriter, r *http.Request) {
	HandleDocRules(w, r, T.DB, rulesNamespace)
}

// loadUserRules returns the user's saved rules formatted as a
// system-prompt section, or "" when none are set.
func loadUserRules(udb Database) string {
	return DocRulesSection(udb, rulesNamespace)
}
