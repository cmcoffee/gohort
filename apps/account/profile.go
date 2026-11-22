package account

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/core/userprofile"
)

// unlessTaken refuses h to a person whose user profile takes what away
// (core/userprofile), with a reason they can act on.
func unlessTaken(what string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if user := AuthCurrentUser(r); user != "" && userprofile.TakesFrom(user, what) {
			http.Error(w, userprofile.Refusal, http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// profileSection says which user profile shapes this person, and what it
// takes away, so a focused dashboard does not read as a broken one.
func profileSection(p userprofile.Profile) ui.Section {
	var takes []string
	for _, s := range userprofile.Sections {
		if p.Takes(s.Key) {
			takes = append(takes, strings.SplitN(s.Label, " (", 2)[0])
		}
	}
	for _, f := range ShareableFeatures() {
		if p.TakesFeature(f.Key) {
			takes = append(takes, f.Label)
		}
	}
	sub := "Your administrator set what gohort shows you."
	if len(takes) > 0 {
		sub += " It leaves out: " + strings.Join(takes, ", ") + "."
	}
	return ui.Section{
		Title:    "Your profile: " + p.Name,
		Subtitle: sub + " Ask them to change it.",
	}
}
