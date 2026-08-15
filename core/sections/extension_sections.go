package sections

import (
	"net/http"

	"github.com/cmcoffee/gohort/core/ui"
)

// ExtensionSectionEntry is a surface an app contributes to the Extensions
// page — the third sibling of AdminSectionEntry (deployment config) and
// AccountSectionEntry (personal preferences).
//
// Extensions is where a user's own REUSABLE THINGS live: their tools,
// their credentials, and now anything else they author once and point
// several agents at. That is a different question from either sibling.
// Admin asks what the deployment allows; Account asks how this person
// likes things; Extensions asks what they have built.
//
// It exists because the alternative was the Extensions page importing
// another app's concepts to render them — the exact leak the other two
// registries were created to prevent. With it, an app registers its own
// section and the page stays a page.
//
// Same shape as an account section, and for the same reason: what
// belongs here is usually shaped by who is asking (a list of THEIR
// machines), so the entry carries a per-request builder rather than a
// fixed section.
type ExtensionSectionEntry struct {
	// Build returns the section for one request/user. ok=false skips it
	// for that user — the standard gate is UserHasAppAccess(r, "<app
	// path>"), so somebody without the contributing app's grant never
	// sees a surface for something they cannot reach.
	Build func(r *http.Request, user string) (s ui.Section, ok bool)
	// Head is an optional ExtraHeadHTML fragment (client-action
	// registrations, styling) appended to the page when the section
	// renders.
	Head string
	// Order sorts sections on the page, low to high. Zero means "after
	// the page's own", which is right for everything an app adds: the
	// tools and credentials a user reaches for constantly should not be
	// pushed down by whatever registered last.
	Order int
}

var extensionSections []ExtensionSectionEntry

// RegisterExtensionSection adds an app-contributed section to the
// Extensions page. Call once at startup (typically in the app's init).
func RegisterExtensionSection(e ExtensionSectionEntry) {
	extensionSections = append(extensionSections, e)
}

// ExtensionSectionEntries returns the registered sections, ordered by
// Order and then by registration — stable, so a page does not reshuffle
// between builds.
func ExtensionSectionEntries() []ExtensionSectionEntry {
	out := make([]ExtensionSectionEntry, len(extensionSections))
	copy(out, extensionSections)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Order < out[j-1].Order; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
