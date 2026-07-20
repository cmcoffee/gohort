package core

import (
	"net/http"

	"github.com/cmcoffee/gohort/core/ui"
)

// AccountSectionEntry is a personal-preferences surface an app contributes to
// the /account page — the per-user sibling of AdminSectionEntry. Unlike admin
// sections (static deployment config), an account section is usually shaped by
// who is asking (a picker over the user's own agents, say), so the entry
// carries a per-request builder instead of a fixed section. The app
// self-registers in init (same pattern as RegisterAdminSection); the account
// page calls Build for the requesting user as it assembles its sections.
type AccountSectionEntry struct {
	// Build returns the section for one request/user. ok=false skips the
	// section for that user — the standard gate is
	// UserHasAppAccess(r, "<app path>") so a user without the contributing
	// app's grant never sees its preferences.
	Build func(r *http.Request, user string) (s ui.Section, ok bool)
	// Head is an optional ExtraHeadHTML fragment (client-action
	// registrations, styling) appended to the page when the section renders.
	Head string
}

var accountSections []AccountSectionEntry

// RegisterAccountSection adds an app-contributed preferences section to the
// /account page. Call once at startup (typically in the app's init).
func RegisterAccountSection(e AccountSectionEntry) {
	accountSections = append(accountSections, e)
}

// AccountSectionEntries returns the registered account sections in
// registration order — read by the account page as it builds.
func AccountSectionEntries() []AccountSectionEntry { return accountSections }
