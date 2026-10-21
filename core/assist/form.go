package assist

// The help any form can have: "✨ Get assistance" on a wizard, or on a
// form that asks for it (ui.FormPanel.Assist), opens a conversation that
// answers questions about the form and helps fill it in. It knows what the
// page tells it, the form's fields with their help and the section's own
// description, and nothing else: no app's records, no tools. A form whose
// app can do better gives its own endpoint (FormPanel.AssistURL, built with
// Handler) with what only it knows, as System Hooks does.
//
// Mounted at the root, for every app, by core's root routes. This package
// is imported by apps/admin, which every build carries.

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
)

// FormPath is where the generic form help is served.
const FormPath = "/api/assist/form"

func init() {
	RegisterRootRoute(FormPath, Handler(Spec{
		Route: "assist.form", Label: "Get assistance on a form",
		Allow: signedIn, Web: true,
	}))
}

// signedIn lets anyone signed in: the help reads nothing of theirs but what
// their own page sends.
func signedIn(w http.ResponseWriter, r *http.Request) bool {
	if AuthCurrentUser(r) == "" {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return false
	}
	return true
}
