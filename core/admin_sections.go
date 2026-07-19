package core

import "github.com/cmcoffee/gohort/core/ui"

// AdminSectionEntry is a settings surface an app contributes to the admin page:
// a section (rendered under its own group tab) plus an optional ExtraHeadHTML
// fragment carrying any client actions the section's controls need. It lets an
// app whose configuration is FRAMEWORK tuning rather than agent behavior — e.g.
// the prompt-block editor — live inside the admin UI WITHOUT admin importing the
// app. The app self-registers here (init, like RegisterApp / RegisterAdminAgent)
// and the admin page reads the registry when it assembles its tabs. The section
// carries its own Group (the tab it lands under) and Wide flag.
type AdminSectionEntry struct {
	Section ui.Section
	Head    string // ExtraHeadHTML fragment (client-action registrations, etc.); may be empty
}

var adminSections []AdminSectionEntry

// RegisterAdminSection adds an app-contributed section to the admin page. Call
// once at startup (typically in the app's init), same pattern as the other
// self-registration hooks.
func RegisterAdminSection(e AdminSectionEntry) { adminSections = append(adminSections, e) }

// AdminSectionEntries returns the registered admin sections in registration
// order — read by the admin page as it builds its tabs.
func AdminSectionEntries() []AdminSectionEntry { return adminSections }
